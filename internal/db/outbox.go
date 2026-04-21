package db

import (
	"context"
	"database/sql"
	"time"
)

// OutboxEntry represents a row in the task_outbox table, carrying
// everything the Redis scheduler needs to push a task into its
// ZSET without re-reading from the tasks table.
type OutboxEntry struct {
	TaskID     string
	JobID      string
	PageID     int
	Host       string
	Path       string
	Priority   float64
	RetryCount int
	SourceType string
	SourceURL  string
	RunAt      time.Time
}

// InsertOutboxRow writes a single outbox row inside the given tx.
// Used by call sites that insert individual tasks (e.g. the manual
// root-URL path). The bulk EnqueueURLs path populates task_outbox
// inline via a CTE instead of calling this helper.
func InsertOutboxRow(ctx context.Context, tx *sql.Tx, entry OutboxEntry) error {
	runAt := entry.RunAt
	if runAt.IsZero() {
		runAt = time.Now().UTC()
	}

	const q = `
		INSERT INTO task_outbox (
			task_id, job_id, page_id, host, path,
			priority, retry_count, source_type, source_url, run_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10
		)
	`

	_, err := tx.ExecContext(ctx, q,
		entry.TaskID,
		entry.JobID,
		entry.PageID,
		entry.Host,
		entry.Path,
		entry.Priority,
		entry.RetryCount,
		entry.SourceType,
		entry.SourceURL,
		runAt,
	)
	return err
}
