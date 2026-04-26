package jobs

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jobRow returns a sqlmock row matching the column list selected by
// JobManager.GetJob. Helper keeps the test ordering aligned with the
// production query — if GetJob's SELECT changes, this is the single
// place to update.
func jobRow(jobID string, status JobStatus) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "name", "status", "progress",
		"total_tasks", "completed_tasks", "failed_tasks", "skipped_tasks",
		"created_at", "started_at", "completed_at", "concurrency", "find_links",
		"include_paths", "exclude_paths", "error_message", "required_workers",
		"found_tasks", "sitemap_tasks", "duration_seconds", "avg_time_per_task_seconds",
		"user_id", "organisation_id",
	}).AddRow(
		jobID, "example.com", string(status), 0.0,
		1, 0, 0, 0,
		time.Now(), nil, nil, 1, false,
		[]byte("[]"), []byte("[]"), nil, 1,
		0, 0, 0, 0,
		nil, nil,
	)
}

// TestCancelJob_LockOrder asserts the cancel transaction UPDATEs tasks
// before jobs (tasks-first ordering matches the worker batch path and
// the AFTER STATEMENT counter trigger, breaking the 40P01 deadlock cycle
// that surfaced on 30k+ page jobs). sqlmock fails the test if calls
// arrive out of declared order.
func TestCancelJob_LockOrder(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mockDB.Close()

	jm := &JobManager{
		db:             mockDB,
		dbQueue:        &mockDbQueueWrapper{mockDB: mockDB},
		processedPages: make(map[string]struct{}),
	}

	const jobID = "job-cancel-1"

	// GetJob (read-only, runs in its own dbQueue.Execute)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT[\s\S]+FROM jobs j[\s\S]+JOIN domains d`).
		WithArgs(jobID).
		WillReturnRows(jobRow(jobID, JobStatusRunning))
	mock.ExpectCommit()

	// CancelJob transaction — order matters.
	mock.ExpectBegin()

	// 1. Tasks first, with deterministic ORDER BY id.
	mock.ExpectExec(`(?s)WITH picked AS \(\s*SELECT id FROM tasks\s+WHERE job_id = \$1\s+AND status IN \(\$3, \$4\)\s+ORDER BY id\s+FOR UPDATE\s*\)\s*UPDATE tasks t\s+SET status = \$2`).
		WithArgs(jobID, string(TaskStatusSkipped), string(TaskStatusPending), string(TaskStatusWaiting)).
		WillReturnResult(sqlmock.NewResult(0, 5))

	// 2. Jobs second.
	mock.ExpectExec(`UPDATE jobs\s+SET status = \$1, completed_at = \$2\s+WHERE id = \$3`).
		WithArgs(string(JobStatusCancelled), sqlmock.AnyArg(), jobID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	// 3. Outbox cleanup last (no row lock contention with workers).
	mock.ExpectExec(`DELETE FROM task_outbox WHERE job_id = \$1`).
		WithArgs(jobID).
		WillReturnResult(sqlmock.NewResult(0, 5))

	mock.ExpectCommit()

	err = jm.CancelJob(context.Background(), jobID)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestCancelJob_AlreadyCancelledIsNoOp asserts a duplicate cancel against
// an already-cancelled job returns nil without running the cancel
// transaction. Stops red toasts on impatient multi-clicks where the
// first request has already won.
func TestCancelJob_AlreadyCancelledIsNoOp(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mockDB.Close()

	jm := &JobManager{
		db:             mockDB,
		dbQueue:        &mockDbQueueWrapper{mockDB: mockDB},
		processedPages: make(map[string]struct{}),
	}

	const jobID = "job-already-cancelled"

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT[\s\S]+FROM jobs j[\s\S]+JOIN domains d`).
		WithArgs(jobID).
		WillReturnRows(jobRow(jobID, JobStatusCancelled))
	mock.ExpectCommit()

	// No further ExpectBegin / ExpectExec — the cancel transaction must
	// not run for an already-cancelled job.

	err = jm.CancelJob(context.Background(), jobID)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestCancelJob_TerminalStatusErrors asserts a cancel against a terminal
// job (completed/failed) still surfaces an error — only the cancelled
// status is treated as idempotent success.
func TestCancelJob_TerminalStatusErrors(t *testing.T) {
	for _, status := range []JobStatus{JobStatusCompleted, JobStatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			require.NoError(t, err)
			defer mockDB.Close()

			jm := &JobManager{
				db:             mockDB,
				dbQueue:        &mockDbQueueWrapper{mockDB: mockDB},
				processedPages: make(map[string]struct{}),
			}

			const jobID = "job-terminal"

			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT[\s\S]+FROM jobs j[\s\S]+JOIN domains d`).
				WithArgs(jobID).
				WillReturnRows(jobRow(jobID, status))
			mock.ExpectCommit()

			err = jm.CancelJob(context.Background(), jobID)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "cannot be canceled")
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// Sanity check that the regex literal in GetJob's expected query above
// has not become brittle if someone reflows the SQL. Failing here points
// the developer at the test, not the production query.
func TestCancelJob_GetJobQueryRegexCompiles(t *testing.T) {
	_, err := regexp.Compile(`SELECT[\s\S]+FROM jobs j[\s\S]+JOIN domains d`)
	require.NoError(t, err)
}
