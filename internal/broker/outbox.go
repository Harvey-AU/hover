package broker

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// OutboxSweeperOpts configures a Sweeper.
type OutboxSweeperOpts struct {
	// Interval between sweep ticks. Default: 5s.
	Interval time.Duration
	// BatchSize caps how many rows are claimed per tick. Default: 200.
	BatchSize int
	// BaseBackoff is the first retry delay on ScheduleBatch failure.
	// Default: 2s. Each subsequent attempt doubles up to MaxBackoff.
	BaseBackoff time.Duration
	// MaxBackoff caps the retry delay. Default: 5 minutes.
	MaxBackoff time.Duration
}

// DefaultOutboxSweeperOpts returns sensible production defaults.
func DefaultOutboxSweeperOpts() OutboxSweeperOpts {
	return OutboxSweeperOpts{
		Interval:    5 * time.Second,
		BatchSize:   200,
		BaseBackoff: 2 * time.Second,
		MaxBackoff:  5 * time.Minute,
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
		opts.Interval = 5 * time.Second
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("outbox: begin tx: %w", err)
	}
	// Rollback is a no-op after successful commit.
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
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

	ids := make([]int64, 0, len(claimed))
	for _, r := range claimed {
		ids = append(ids, r.id)
	}

	if err := s.scheduler.ScheduleBatch(ctx, entries); err != nil {
		// Bump attempts + push run_at forward with exponential backoff.
		// Rows stay claimed under the tx lock until commit; other
		// replicas cannot pick them up until then.
		if updErr := s.bumpAttempts(ctx, tx, ids); updErr != nil {
			return fmt.Errorf("outbox: bump attempts after schedule failure: %w (schedule err: %v)", updErr, err)
		}
		if cmErr := tx.Commit(); cmErr != nil {
			return fmt.Errorf("outbox: commit backoff update: %w (schedule err: %v)", cmErr, err)
		}
		return fmt.Errorf("outbox: schedule batch: %w", err)
	}

	// Success: delete the rows we just dispatched.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM task_outbox WHERE id = ANY($1)
	`, pq.Array(ids)); err != nil {
		return fmt.Errorf("outbox: delete dispatched rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("outbox: commit dispatch: %w", err)
	}

	brokerLog.Debug("outbox sweep tick dispatched",
		"dispatched", len(entries))
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
