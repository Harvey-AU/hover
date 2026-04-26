package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// LighthouseRunStatus values mirror the CHECK constraint on
// lighthouse_runs.status.
type LighthouseRunStatus string

const (
	LighthouseRunPending      LighthouseRunStatus = "pending"
	LighthouseRunRunning      LighthouseRunStatus = "running"
	LighthouseRunSucceeded    LighthouseRunStatus = "succeeded"
	LighthouseRunFailed       LighthouseRunStatus = "failed"
	LighthouseRunSkippedQuota LighthouseRunStatus = "skipped_quota"
)

// LighthouseSelectionBand values mirror the CHECK constraint on
// lighthouse_runs.selection_band.
type LighthouseSelectionBand string

const (
	LighthouseBandFastest   LighthouseSelectionBand = "fastest"
	LighthouseBandSlowest   LighthouseSelectionBand = "slowest"
	LighthouseBandReconcile LighthouseSelectionBand = "reconcile"
)

// LighthouseRun represents a row in the lighthouse_runs table. Optional
// metric fields are pointers so we can distinguish "not yet measured"
// from "measured zero".
type LighthouseRun struct {
	ID                 int64
	JobID              string
	PageID             int
	SourceTaskID       *string
	SelectionBand      LighthouseSelectionBand
	SelectionMilestone int
	Status             LighthouseRunStatus
	PerformanceScore   *int
	LCPMs              *int
	CLS                *float64
	INPMs              *int
	TBTMs              *int
	FCPMs              *int
	SpeedIndexMs       *int
	TTFBMs             *int
	TotalByteWeight    *int64
	ReportKey          *string
	ErrorMessage       *string
	ScheduledAt        time.Time
	StartedAt          *time.Time
	CompletedAt        *time.Time
	DurationMs         *int
}

// LighthouseRunInsert carries the fields populated by the scheduler when
// a new audit is queued. Status defaults to 'pending' at the database.
type LighthouseRunInsert struct {
	JobID              string
	PageID             int
	SourceTaskID       string // empty string -> NULL
	SelectionBand      LighthouseSelectionBand
	SelectionMilestone int
}

// LighthouseRunMetrics carries the headline metrics produced by a runner
// after a successful audit. Pointers preserve "missing" semantics.
type LighthouseRunMetrics struct {
	PerformanceScore *int
	LCPMs            *int
	CLS              *float64
	INPMs            *int
	TBTMs            *int
	FCPMs            *int
	SpeedIndexMs     *int
	TTFBMs           *int
	TotalByteWeight  *int64
	ReportKey        string
	DurationMs       int
}

// ErrLighthouseRunNotFound is returned when a row lookup misses.
var ErrLighthouseRunNotFound = errors.New("lighthouse run not found")

// InsertLighthouseRun inserts a pending lighthouse_runs row. The
// (job_id, page_id) UNIQUE constraint protects against duplicates if
// the scheduler races across milestones; ON CONFLICT DO NOTHING returns
// a zero rows-affected count so the caller can detect the dedupe.
//
// Returns the new row's id, or 0 if the insert was a no-op due to
// the unique constraint.
func InsertLighthouseRun(ctx context.Context, tx *sql.Tx, insert LighthouseRunInsert) (int64, error) {
	const q = `
		INSERT INTO lighthouse_runs (
			job_id, page_id, source_task_id, selection_band, selection_milestone, status
		) VALUES (
			$1, $2, NULLIF($3, ''), $4, $5, 'pending'
		)
		ON CONFLICT (job_id, page_id) DO NOTHING
		RETURNING id
	`

	var id int64
	err := tx.QueryRowContext(ctx, q,
		insert.JobID,
		insert.PageID,
		insert.SourceTaskID,
		string(insert.SelectionBand),
		insert.SelectionMilestone,
	).Scan(&id)

	if errors.Is(err, sql.ErrNoRows) {
		// Conflict path: row already exists for (job_id, page_id).
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("insert lighthouse_runs: %w", err)
	}
	return id, nil
}

// MarkLighthouseRunRunning transitions a pending row to running and
// stamps started_at. Returns false if the row is already past pending,
// so the caller can avoid double-dispatch.
func (db *DB) MarkLighthouseRunRunning(ctx context.Context, runID int64) (bool, error) {
	const q = `
		UPDATE lighthouse_runs
		   SET status = 'running',
		       started_at = NOW()
		 WHERE id = $1 AND status = 'pending'
	`

	result, err := db.client.ExecContext(ctx, q, runID)
	if err != nil {
		return false, fmt.Errorf("mark lighthouse run running: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return rows > 0, nil
}

// CompleteLighthouseRun records a successful audit's metrics on a row
// and stamps completed_at + duration_ms. Status moves to 'succeeded'.
//
// The status='running' guard prevents a stale or duplicate-delivered
// runner from clobbering a row that has already reached a terminal
// state (succeeded, failed, or skipped_quota) — Redis stream redelivery
// is at-least-once.
func (db *DB) CompleteLighthouseRun(ctx context.Context, runID int64, metrics LighthouseRunMetrics) error {
	const q = `
		UPDATE lighthouse_runs
		   SET status            = 'succeeded',
		       performance_score = $2,
		       lcp_ms            = $3,
		       cls               = $4,
		       inp_ms            = $5,
		       tbt_ms            = $6,
		       fcp_ms            = $7,
		       speed_index_ms    = $8,
		       ttfb_ms           = $9,
		       total_byte_weight = $10,
		       report_key        = NULLIF($11, ''),
		       duration_ms       = $12,
		       completed_at      = NOW()
		 WHERE id = $1 AND status = 'running'
	`

	result, err := db.client.ExecContext(ctx, q,
		runID,
		metrics.PerformanceScore,
		metrics.LCPMs,
		metrics.CLS,
		metrics.INPMs,
		metrics.TBTMs,
		metrics.FCPMs,
		metrics.SpeedIndexMs,
		metrics.TTFBMs,
		metrics.TotalByteWeight,
		metrics.ReportKey,
		metrics.DurationMs,
	)
	if err != nil {
		return fmt.Errorf("complete lighthouse run: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return ErrLighthouseRunNotFound
	}
	return nil
}

// FailLighthouseRun records a permanent failure's stderr/error message.
// Used after the runner exhausts its retry budget. Like
// CompleteLighthouseRun, gated on status='running' to avoid a
// duplicate-delivered worker overwriting a row that already reached a
// terminal state.
func (db *DB) FailLighthouseRun(ctx context.Context, runID int64, errorMessage string, durationMs int) error {
	const q = `
		UPDATE lighthouse_runs
		   SET status        = 'failed',
		       error_message = NULLIF($2, ''),
		       duration_ms   = $3,
		       completed_at  = NOW()
		 WHERE id = $1 AND status = 'running'
	`

	result, err := db.client.ExecContext(ctx, q, runID, errorMessage, durationMs)
	if err != nil {
		return fmt.Errorf("fail lighthouse run: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return ErrLighthouseRunNotFound
	}
	return nil
}

// MarkLighthouseRunSkippedQuota records that a sampled page was dropped
// because the plan-tier audit budget was exhausted. The row stays so
// the UI can explain the absence.
func (db *DB) MarkLighthouseRunSkippedQuota(ctx context.Context, runID int64) error {
	const q = `
		UPDATE lighthouse_runs
		   SET status       = 'skipped_quota',
		       completed_at = NOW()
		 WHERE id = $1 AND status = 'pending'
	`
	result, err := db.client.ExecContext(ctx, q, runID)
	if err != nil {
		return fmt.Errorf("mark skipped quota: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return ErrLighthouseRunNotFound
	}
	return nil
}

// ListLighthouseRunsByJob returns all runs for a job ordered by
// scheduled_at, oldest first. Used by the API surface layer to build
// the per-job results view.
func (db *DB) ListLighthouseRunsByJob(ctx context.Context, jobID string) ([]LighthouseRun, error) {
	const q = `
		SELECT id, job_id, page_id, source_task_id,
		       selection_band, selection_milestone, status,
		       performance_score, lcp_ms, cls, inp_ms, tbt_ms,
		       fcp_ms, speed_index_ms, ttfb_ms, total_byte_weight,
		       report_key, error_message,
		       scheduled_at, started_at, completed_at, duration_ms
		  FROM lighthouse_runs
		 WHERE job_id = $1
		 ORDER BY scheduled_at ASC, id ASC
	`

	rows, err := db.client.QueryContext(ctx, q, jobID)
	if err != nil {
		return nil, fmt.Errorf("list lighthouse runs: %w", err)
	}
	defer rows.Close()

	var runs []LighthouseRun
	for rows.Next() {
		run, err := scanLighthouseRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate lighthouse runs: %w", err)
	}
	return runs, nil
}

// GetLighthouseRunPageIDs returns the set of page IDs already queued
// for a job. Used by the sampler to dedupe across milestones.
func (db *DB) GetLighthouseRunPageIDs(ctx context.Context, jobID string) (map[int]struct{}, error) {
	const q = `
		SELECT page_id
		  FROM lighthouse_runs
		 WHERE job_id = $1
	`

	rows, err := db.client.QueryContext(ctx, q, jobID)
	if err != nil {
		return nil, fmt.Errorf("list lighthouse run page ids: %w", err)
	}
	defer rows.Close()

	seen := make(map[int]struct{})
	for rows.Next() {
		var pageID int
		if err := rows.Scan(&pageID); err != nil {
			return nil, fmt.Errorf("scan page id: %w", err)
		}
		seen[pageID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate page ids: %w", err)
	}
	return seen, nil
}

// CompletedTaskForSampling carries the per-task metadata the lighthouse
// scheduler needs to both feed the sampler (PageID, TaskID, ResponseTime)
// and write outbox rows (Host, Path, Priority). Source URL is computed
// later by the scheduler from Host+Path so the structure stays in sync
// with what the sampler exposes.
type CompletedTaskForSampling struct {
	TaskID       string
	PageID       int
	Host         string
	Path         string
	Priority     float64
	ResponseTime int64
}

// GetCompletedTasksForLighthouseSampling returns the metadata needed to
// feed the lighthouse sampler and produce outbox rows. Restricted to
// task_type='crawl' so an early lighthouse audit cannot become a sample
// candidate for a later one. Excludes tasks without a response_time
// (sampling is band-by-response-time; rows without it can't be ranked).
//
// jobID is the textual job identifier used elsewhere; the join into
// tasks happens via tasks.job_id without coercion since the schema has
// already aligned both columns to TEXT.
func (db *DB) GetCompletedTasksForLighthouseSampling(ctx context.Context, jobID string) ([]CompletedTaskForSampling, error) {
	const q = `
		SELECT id, page_id, host, path,
		       COALESCE(priority_score, 0)::double precision AS priority,
		       COALESCE(response_time, 0)::bigint AS response_time
		  FROM tasks
		 WHERE job_id = $1
		   AND status = 'completed'
		   AND task_type = 'crawl'
		   AND response_time IS NOT NULL
		   AND response_time > 0
	`

	rows, err := db.client.QueryContext(ctx, q, jobID)
	if err != nil {
		return nil, fmt.Errorf("list completed tasks for lighthouse sampling: %w", err)
	}
	defer rows.Close()

	var out []CompletedTaskForSampling
	for rows.Next() {
		var t CompletedTaskForSampling
		if err := rows.Scan(&t.TaskID, &t.PageID, &t.Host, &t.Path, &t.Priority, &t.ResponseTime); err != nil {
			return nil, fmt.Errorf("scan completed task for sampling: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate completed tasks for sampling: %w", err)
	}
	return out, nil
}

func scanLighthouseRun(rows *sql.Rows) (LighthouseRun, error) {
	var (
		run                                               LighthouseRun
		sourceTaskID, reportKey, errorMessage             sql.NullString
		performance, lcp, inp, tbt, fcp, speedIndex, ttfb sql.NullInt32
		duration                                          sql.NullInt32
		cls                                               sql.NullFloat64
		totalByteWeight                                   sql.NullInt64
		startedAt, completedAt                            sql.NullTime
	)

	if err := rows.Scan(
		&run.ID, &run.JobID, &run.PageID, &sourceTaskID,
		&run.SelectionBand, &run.SelectionMilestone, &run.Status,
		&performance, &lcp, &cls, &inp, &tbt,
		&fcp, &speedIndex, &ttfb, &totalByteWeight,
		&reportKey, &errorMessage,
		&run.ScheduledAt, &startedAt, &completedAt, &duration,
	); err != nil {
		return run, fmt.Errorf("scan lighthouse run: %w", err)
	}

	if sourceTaskID.Valid {
		s := sourceTaskID.String
		run.SourceTaskID = &s
	}
	if reportKey.Valid {
		s := reportKey.String
		run.ReportKey = &s
	}
	if errorMessage.Valid {
		s := errorMessage.String
		run.ErrorMessage = &s
	}
	if performance.Valid {
		v := int(performance.Int32)
		run.PerformanceScore = &v
	}
	if lcp.Valid {
		v := int(lcp.Int32)
		run.LCPMs = &v
	}
	if cls.Valid {
		v := cls.Float64
		run.CLS = &v
	}
	if inp.Valid {
		v := int(inp.Int32)
		run.INPMs = &v
	}
	if tbt.Valid {
		v := int(tbt.Int32)
		run.TBTMs = &v
	}
	if fcp.Valid {
		v := int(fcp.Int32)
		run.FCPMs = &v
	}
	if speedIndex.Valid {
		v := int(speedIndex.Int32)
		run.SpeedIndexMs = &v
	}
	if ttfb.Valid {
		v := int(ttfb.Int32)
		run.TTFBMs = &v
	}
	if totalByteWeight.Valid {
		v := totalByteWeight.Int64
		run.TotalByteWeight = &v
	}
	if startedAt.Valid {
		t := startedAt.Time
		run.StartedAt = &t
	}
	if completedAt.Valid {
		t := completedAt.Time
		run.CompletedAt = &t
	}
	if duration.Valid {
		v := int(duration.Int32)
		run.DurationMs = &v
	}
	return run, nil
}
