package broker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/lib/pq"
)

// DefaultOutboxMaxAttempts is the retry cap before a row is dead-lettered.
// Chosen so the worst-case age of a row stuck in backoff is bounded by
// MaxAttempts × MaxBackoff — at the defaults, 10 × 5 min = 50 min, which
// caps the oldest-age gauge even if a subset of rows can never be dispatched.
const DefaultOutboxMaxAttempts = 10

// OutboxSweeperOpts configures a Sweeper.
type OutboxSweeperOpts struct {
	// Interval between sweep ticks. Default: 500ms.
	Interval time.Duration
	// BatchSize caps how many rows are claimed per tick. Default: 200.
	BatchSize int
	// BaseBackoff is the first retry delay on ScheduleBatch failure.
	// Default: 2s. Each subsequent attempt doubles up to MaxBackoff.
	BaseBackoff time.Duration
	// MaxBackoff caps the retry delay. Default: 5 minutes.
	MaxBackoff time.Duration
	// MaxAttempts is the retry cap before a row is moved to
	// task_outbox_dead. Default: 10.
	MaxAttempts int
	// StatementTimeout bounds each sweep tick's total DB work. Guards
	// against a pathological sweeper tx holding locks indefinitely. 0
	// leaves the DB's default in place. Default: 5s.
	StatementTimeout time.Duration
}

// DefaultOutboxSweeperOpts returns sensible production defaults.
//
// The sweep interval was lowered from 5s to 500ms because the outbox
// sits on the hot path between newly-authored tasks and the Redis
// ZSET that dispatchers poll. At 5s, each just-completed task waited
// up to 5s for its newly-discovered siblings to reach a worker, which
// dominated end-to-end throughput on small jobs. The sweep is an
// index-only SKIP LOCKED query; running it 10× more often is cheap.
//
// StatementTimeout was raised from 5s to 15s after HOVER-K3: when the
// shared queue pool saturates under bulk-lane load, pool acquire alone
// can eat several seconds of the tick budget, leaving sub-second
// headroom for the actual SELECT/UPDATE work and surfacing as
// "bump attempts: context deadline exceeded". 15s is comfortably
// shorter than session/idle timeouts but tolerates pool wait spikes.
func DefaultOutboxSweeperOpts() OutboxSweeperOpts {
	return OutboxSweeperOpts{
		Interval:         500 * time.Millisecond,
		BatchSize:        200,
		BaseBackoff:      2 * time.Second,
		MaxBackoff:       5 * time.Minute,
		MaxAttempts:      DefaultOutboxMaxAttempts,
		StatementTimeout: 15 * time.Second,
	}
}

// Sweeper polls task_outbox for due rows and pushes them into Redis
// via Scheduler.ScheduleBatch. Deletes rows on success; bumps attempts
// and run_at on failure.
//
// Safe to run multiple replicas: each claim tx uses FOR UPDATE SKIP
// LOCKED so replicas partition the due rows rather than contending.
type Sweeper struct {
	db        *sql.DB
	scheduler *Scheduler
	opts      OutboxSweeperOpts
}

// NewOutboxSweeper constructs a Sweeper.
func NewOutboxSweeper(db *sql.DB, scheduler *Scheduler, opts OutboxSweeperOpts) *Sweeper {
	if opts.Interval <= 0 {
		opts.Interval = 500 * time.Millisecond
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 200
	}
	if opts.BaseBackoff <= 0 {
		opts.BaseBackoff = 2 * time.Second
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 5 * time.Minute
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = DefaultOutboxMaxAttempts
	}
	return &Sweeper{db: db, scheduler: scheduler, opts: opts}
}

// Run drives the sweeper loop until ctx is cancelled. Errors from
// individual ticks are logged; the loop keeps going.
func (s *Sweeper) Run(ctx context.Context) {
	brokerLog.Info("outbox sweeper started",
		"interval", s.opts.Interval, "batch_size", s.opts.BatchSize)

	t := time.NewTicker(s.opts.Interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			brokerLog.Info("outbox sweeper stopped", "reason", ctx.Err())
			return
		case <-t.C:
			if err := s.Tick(ctx); err != nil {
				brokerLog.Error("outbox sweep tick failed", "error", err)
			}
		}
	}
}

// outboxRow mirrors the columns read from task_outbox.
type outboxRow struct {
	id         int64
	taskID     string
	jobID      string
	pageID     int
	host       string
	path       string
	priority   float64
	retryCount int
	sourceType string
	sourceURL  string
	runAt      time.Time
	attempts   int
}

// Tick runs a single sweep iteration. Exported for tests so they
// can deterministically trigger a sweep without waiting for the
// ticker.
func (s *Sweeper) Tick(ctx context.Context) error {
	// Bound the whole tick — DB work and the Redis ScheduleBatch call —
	// to StatementTimeout. SET LOCAL statement_timeout only fires while a
	// SQL statement is executing, so if ScheduleBatch wedges on Redis the
	// row locks would persist; a tx-level context deadline cancels the
	// transaction and releases the locks instead.
	tickCtx := ctx
	cancel := func() {}
	if s.opts.StatementTimeout > 0 {
		tickCtx, cancel = context.WithTimeout(ctx, s.opts.StatementTimeout)
	}
	defer cancel()

	tx, err := s.db.BeginTx(tickCtx, nil)
	if err != nil {
		return fmt.Errorf("outbox: begin tx: %w", err)
	}
	// Rollback is a no-op after successful commit.
	defer func() { _ = tx.Rollback() }()

	// Belt-and-braces: if the server somehow outlives the client context
	// (e.g. pgbouncer masking cancellation), the DB-side timeout still
	// aborts the statement.
	if s.opts.StatementTimeout > 0 {
		if _, err := tx.ExecContext(tickCtx,
			fmt.Sprintf(`SET LOCAL statement_timeout = %d`,
				s.opts.StatementTimeout.Milliseconds()),
		); err != nil {
			return fmt.Errorf("outbox: set statement_timeout: %w", err)
		}
	}

	rows, err := tx.QueryContext(tickCtx, `
		SELECT id, task_id, job_id, page_id, host, path,
		       priority, retry_count, source_type, source_url,
		       run_at, attempts
		FROM task_outbox
		WHERE run_at <= NOW()
		ORDER BY run_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, s.opts.BatchSize)
	if err != nil {
		return fmt.Errorf("outbox: select due rows: %w", err)
	}

	var claimed []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(
			&r.id, &r.taskID, &r.jobID, &r.pageID, &r.host, &r.path,
			&r.priority, &r.retryCount, &r.sourceType, &r.sourceURL,
			&r.runAt, &r.attempts,
		); err != nil {
			_ = rows.Close()
			return fmt.Errorf("outbox: scan row: %w", err)
		}
		claimed = append(claimed, r)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("outbox: iterate rows: %w", err)
	}

	if len(claimed) == 0 {
		// Nothing to do — commit the empty tx and wait for the next tick.
		return tx.Commit()
	}

	entries := make([]ScheduleEntry, 0, len(claimed))
	for _, r := range claimed {
		entries = append(entries, ScheduleEntry{
			TaskID:     r.taskID,
			JobID:      r.jobID,
			PageID:     r.pageID,
			Host:       r.host,
			Path:       r.path,
			Priority:   r.priority,
			RetryCount: r.retryCount,
			SourceType: r.sourceType,
			SourceURL:  r.sourceURL,
			RunAt:      r.runAt,
		})
	}

	schedErr := s.scheduler.ScheduleBatch(tickCtx, entries)

	// Partition the claimed rows into successes and failures based on
	// what ScheduleBatch actually did:
	//   * nil error:     every ZADD succeeded — delete all rows.
	//   * *BatchError:   pipeline completed but some entries failed —
	//                    delete the succeeded ones, bump the failed ones.
	//   * other error:   pipeline could not execute — treat all as failed.
	var (
		succeeded  []int64     // task_outbox.id values to DELETE
		retry      []outboxRow // rows to bump attempts / run_at
		deadLetter []outboxRow // rows at or over MaxAttempts
		lastErrMsg string
	)

	var be *BatchError
	switch {
	case schedErr == nil:
		succeeded = make([]int64, 0, len(claimed))
		for _, r := range claimed {
			succeeded = append(succeeded, r.id)
		}
	case errors.As(schedErr, &be):
		failedSet := make(map[int]struct{}, len(be.FailedIndices))
		for _, idx := range be.FailedIndices {
			failedSet[idx] = struct{}{}
		}
		succeeded = make([]int64, 0, len(claimed)-len(failedSet))
		retry = make([]outboxRow, 0, len(failedSet))
		for i, r := range claimed {
			if _, bad := failedSet[i]; bad {
				retry = append(retry, r)
				continue
			}
			succeeded = append(succeeded, r.id)
		}
		lastErrMsg = be.Err.Error()
	default:
		retry = append([]outboxRow(nil), claimed...)
		lastErrMsg = schedErr.Error()
	}

	// Classify retries over the attempts cap as dead-letters. We check
	// attempts+1 because the retry path is about to perform a +1 bump,
	// so a row currently at MaxAttempts-1 would reach MaxAttempts this
	// tick and should be terminal.
	if len(retry) > 0 && s.opts.MaxAttempts > 0 {
		kept := retry[:0]
		for _, r := range retry {
			if r.attempts+1 >= s.opts.MaxAttempts {
				deadLetter = append(deadLetter, r)
				continue
			}
			kept = append(kept, r)
		}
		retry = kept
	}

	if len(succeeded) > 0 {
		if _, err := tx.ExecContext(tickCtx,
			`DELETE FROM task_outbox WHERE id = ANY($1)`,
			pq.Array(succeeded),
		); err != nil {
			return fmt.Errorf("outbox: delete dispatched rows: %w", err)
		}
	}

	retryIDs := make([]int64, 0, len(retry))
	for _, r := range retry {
		retryIDs = append(retryIDs, r.id)
	}
	if len(retryIDs) > 0 {
		if err := s.bumpAttempts(tickCtx, tx, retryIDs); err != nil {
			return fmt.Errorf("outbox: bump attempts: %w", err)
		}
	}

	if len(deadLetter) > 0 {
		if err := s.moveToDeadLetter(tickCtx, tx, deadLetter, lastErrMsg); err != nil {
			return fmt.Errorf("outbox: dead-letter: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("outbox: commit: %w", err)
	}

	// Per-row outcomes are mutually exclusive. A pipeline-level failure
	// shows up as all rows in `retried` (or `dead_lettered` if capped);
	// it is not emitted as a separate row-count to avoid double counting.
	observability.RecordBrokerOutboxSweep(ctx, "dispatched", len(succeeded))
	observability.RecordBrokerOutboxSweep(ctx, "retried", len(retry))
	observability.RecordBrokerOutboxSweep(ctx, "dead_lettered", len(deadLetter))

	if schedErr != nil {
		brokerLog.Debug("outbox sweep tick partial",
			"dispatched", len(succeeded),
			"retried", len(retry),
			"dead_lettered", len(deadLetter),
			"schedule_err", schedErr)
		return fmt.Errorf("outbox: schedule batch: %w", schedErr)
	}

	brokerLog.Debug("outbox sweep tick dispatched",
		"dispatched", len(succeeded))
	return nil
}

// moveToDeadLetter copies the given rows into task_outbox_dead with the
// failing error message attached, and deletes them from task_outbox. Runs
// in the caller's tx so the move is atomic with the rest of the sweep.
func (s *Sweeper) moveToDeadLetter(ctx context.Context, tx *sql.Tx, rows []outboxRow, lastErr string) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.id)
	}
	// Copy first, delete second. The SELECT filters by id rather than
	// re-scanning so rows we never claimed (locked by another replica)
	// are not touched.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO task_outbox_dead (
			original_id, task_id, job_id, page_id, host, path,
			priority, retry_count, source_type, source_url,
			run_at, attempts, created_at, last_error
		)
		SELECT id, task_id, job_id, page_id, host, path,
		       priority, retry_count, source_type, source_url,
		       run_at, attempts + 1, created_at, $2
		FROM task_outbox
		WHERE id = ANY($1)
	`, pq.Array(ids), lastErr); err != nil {
		return fmt.Errorf("insert dead rows: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM task_outbox WHERE id = ANY($1)`,
		pq.Array(ids),
	); err != nil {
		return fmt.Errorf("delete dead rows: %w", err)
	}
	brokerLog.Warn("outbox rows dead-lettered",
		"count", len(ids), "last_error", lastErr)
	return nil
}

// bumpAttempts advances attempts and run_at for claimed rows after a
// ScheduleBatch failure. Backoff grows exponentially in BaseBackoff
// doublings, capped at MaxBackoff.
func (s *Sweeper) bumpAttempts(ctx context.Context, tx *sql.Tx, ids []int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE task_outbox
		SET attempts = attempts + 1,
		    run_at = NOW() + LEAST(
		        $1::interval * POWER(2, attempts),
		        $2::interval
		    )
		WHERE id = ANY($3)
	`,
		intervalString(s.opts.BaseBackoff),
		intervalString(s.opts.MaxBackoff),
		pq.Array(ids),
	)
	return err
}

// intervalString formats a Go duration as a Postgres interval literal
// in milliseconds. Postgres accepts "%d milliseconds" in interval casts.
func intervalString(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%d milliseconds", d.Milliseconds())
}
