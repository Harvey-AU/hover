package lighthouse

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// SchedulerDB is the narrow subset of *db.DB the scheduler needs. Kept
// as an interface so unit tests can inject a fake without standing up a
// full Postgres pool.
type SchedulerDB interface {
	GetCompletedTasksForLighthouseSampling(ctx context.Context, jobID string) ([]db.CompletedTaskForSampling, error)
	GetLighthouseRunPageBands(ctx context.Context, jobID string) (map[int]db.LighthouseSelectionBand, error)
}

// TxRunner runs the supplied function inside a Postgres transaction.
// Mirrors db.QueueExecutor.ExecuteWithContext so the scheduler can
// reuse the same retry / pool semantics the rest of the codebase
// already exercises.
type TxRunner interface {
	ExecuteWithContext(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error
}

// Scheduler turns crawl progress into pending lighthouse_runs rows and
// matching task_outbox entries. One scheduler is bound to a single
// SchedulerDB / TxRunner pair; the JobManager invokes OnMilestone via
// the OnProgressMilestone callback whenever a flush observes a 10%
// boundary crossing.
type Scheduler struct {
	db    SchedulerDB
	queue TxRunner
}

// NewScheduler constructs a Scheduler. Callers are expected to wire
// the returned Scheduler.OnMilestone into JobManager.OnProgressMilestone
// during bootstrap.
func NewScheduler(database SchedulerDB, queue TxRunner) *Scheduler {
	return &Scheduler{db: database, queue: queue}
}

// OnMilestone runs the sampler at the given milestone (0..100) and
// enqueues any newly chosen audits. Safe to call from the milestone
// callback path: errors are returned for the caller to log, but the
// caller is expected to never let a scheduler failure block the batch
// loop.
//
// At milestone == 100, samples are tagged as the 'reconcile' band so
// the analytics layer can distinguish opportunistic per-decade picks
// from the catch-up pass at job completion.
func (s *Scheduler) OnMilestone(ctx context.Context, jobID string, milestone int) error {
	if s == nil {
		return nil
	}
	if milestone < 0 {
		milestone = 0
	}
	if milestone > 100 {
		milestone = 100
	}

	completed, err := s.db.GetCompletedTasksForLighthouseSampling(ctx, jobID)
	if err != nil {
		return fmt.Errorf("lighthouse: load completed tasks: %w", err)
	}
	if len(completed) == 0 {
		lighthouseLog.Debug("milestone has no completed tasks yet",
			"job_id", jobID, "milestone", milestone)
		return nil
	}

	existingBands, err := s.db.GetLighthouseRunPageBands(ctx, jobID)
	if err != nil {
		return fmt.Errorf("lighthouse: load already-sampled page bands: %w", err)
	}
	alreadySampled := make(map[int]SelectionBand, len(existingBands))
	for pageID, b := range existingBands {
		alreadySampled[pageID] = SelectionBand(b)
	}

	// Map page_id -> meta so the band selection (which only carries
	// CompletedTask) can hydrate back into the outbox row's metadata.
	meta := make(map[int]db.CompletedTaskForSampling, len(completed))
	tasksForSampler := make([]CompletedTask, 0, len(completed))
	for _, t := range completed {
		meta[t.PageID] = t
		tasksForSampler = append(tasksForSampler, CompletedTask{
			PageID:       t.PageID,
			TaskID:       t.TaskID,
			ResponseTime: t.ResponseTime,
		})
	}

	samples := SelectSamples(tasksForSampler, milestone, alreadySampled)
	if len(samples) == 0 {
		lighthouseLog.Debug("milestone produced no new samples",
			"job_id", jobID, "milestone", milestone,
			"completed", len(completed), "already_sampled", len(alreadySampled))
		return nil
	}

	tagBand := func(b SelectionBand) SelectionBand {
		if milestone >= 100 {
			return BandReconcile
		}
		return b
	}

	// scheduledByBand is populated per-attempt and only published to
	// the outer scope after the tx commits, so retries inside
	// ExecuteWithContext cannot inflate the metrics counts. The closure
	// owns the per-attempt map; the outer scope reads only after a
	// successful return.
	var scheduledByBand map[SelectionBand]int
	now := time.Now().UTC()

	err = s.queue.ExecuteWithContext(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		attemptByBand := make(map[SelectionBand]int, 3)
		var (
			outboxTaskIDs    []string
			outboxJobIDs     []string
			outboxPageIDs    []int
			outboxHosts      []string
			outboxPaths      []string
			outboxPriorities []float64
			outboxSourceURL  []string
			outboxRunIDs     []int64
			outboxRunAt      []time.Time
		)

		for _, smp := range samples {
			m, ok := meta[smp.Task.PageID]
			if !ok {
				// Should be impossible — sampler only returns IDs from the
				// input list — but skip rather than crash if it ever happens.
				continue
			}
			band := tagBand(smp.Band)

			runID, err := db.InsertLighthouseRun(txCtx, tx, db.LighthouseRunInsert{
				JobID:              jobID,
				PageID:             m.PageID,
				SourceTaskID:       m.TaskID,
				SelectionBand:      db.LighthouseSelectionBand(band),
				SelectionMilestone: milestone,
			})
			if err != nil {
				return fmt.Errorf("insert lighthouse_runs: %w", err)
			}
			if runID == 0 {
				// Lost the race against another replica's milestone for
				// this (job_id, page_id). Skip; the winning row already
				// has its outbox entry.
				continue
			}

			outboxTaskIDs = append(outboxTaskIDs, uuid.NewString())
			outboxJobIDs = append(outboxJobIDs, jobID)
			outboxPageIDs = append(outboxPageIDs, m.PageID)
			outboxHosts = append(outboxHosts, m.Host)
			outboxPaths = append(outboxPaths, m.Path)
			outboxPriorities = append(outboxPriorities, m.Priority)
			outboxSourceURL = append(outboxSourceURL, lighthouseAuditURL(m.Host, m.Path))
			outboxRunIDs = append(outboxRunIDs, runID)
			outboxRunAt = append(outboxRunAt, now)
			attemptByBand[band]++
		}

		if len(outboxTaskIDs) == 0 {
			scheduledByBand = attemptByBand
			return nil
		}

		// Bulk-insert via unnest so the round-trip cost is one statement
		// even for the maximum 100-audit per-job ceiling. task_id and
		// job_id columns are TEXT (see migration 20260421090000), so we
		// pass them as text arrays without a UUID cast — the lighthouse
		// task_id is a freshly generated UUID in canonical text form,
		// which the column accepts as-is.
		const insertOutbox = `
			INSERT INTO task_outbox (
				task_id, job_id, page_id, host, path,
				priority, retry_count, source_type, source_url,
				run_at, attempts, created_at,
				task_type, lighthouse_run_id
			)
			SELECT
				t_task, t_job, t_page, t_host, t_path,
				t_priority, 0, 'lighthouse', t_url,
				t_run_at, 0, NOW(),
				'lighthouse', t_run_id
			FROM UNNEST(
				$1::text[],
				$2::text[],
				$3::int[],
				$4::text[],
				$5::text[],
				$6::double precision[],
				$7::text[],
				$8::bigint[],
				$9::timestamptz[]
			) AS u(
				t_task, t_job, t_page, t_host, t_path,
				t_priority, t_url, t_run_id, t_run_at
			)
			ON CONFLICT (task_id) DO NOTHING
		`

		if _, err := tx.ExecContext(txCtx, insertOutbox,
			pq.Array(outboxTaskIDs),
			pq.Array(outboxJobIDs),
			pq.Array(outboxPageIDs),
			pq.Array(outboxHosts),
			pq.Array(outboxPaths),
			pq.Array(outboxPriorities),
			pq.Array(outboxSourceURL),
			pq.Array(outboxRunIDs),
			pq.Array(outboxRunAt),
		); err != nil {
			return fmt.Errorf("insert lighthouse outbox rows: %w", err)
		}
		scheduledByBand = attemptByBand
		return nil
	})

	if err != nil {
		return fmt.Errorf("lighthouse: enqueue samples: %w", err)
	}

	for band, count := range scheduledByBand {
		lighthouseLog.Info("lighthouse runs scheduled",
			"job_id", jobID,
			"milestone", milestone,
			"band", string(band),
			"count", count,
		)
		observability.RecordLighthouseScheduled(ctx, jobID, string(band), count)
	}

	return nil
}

// lighthouseAuditURL composes the URL the runner should audit. Crawl
// hosts are stored without a scheme; lighthouse always audits over
// https in v1, matching what the crawler itself does.
func lighthouseAuditURL(host, path string) string {
	if host == "" {
		return ""
	}
	if path == "" {
		path = "/"
	}
	return "https://" + host + path
}
