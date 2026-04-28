package jobs

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBlockJob_LockOrder asserts the block transaction issues
// statements in the same lock order as CancelJob — tasks → jobs →
// task_outbox → domains. The first three mirror the worker batch path
// and AFTER STATEMENT counter trigger; reversing them is the same
// 40P01 deadlock the cancel test guards against. The fourth (domains)
// must come last so nothing else holds a domain row lock for the
// duration of the long tasks UPDATE.
func TestBlockJob_LockOrder(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mockDB.Close()

	jm := &JobManager{
		db:             mockDB,
		dbQueue:        &mockDbQueueWrapper{mockDB: mockDB},
		processedPages: make(map[string]struct{}),
	}

	const (
		jobID  = "job-block-1"
		vendor = "akamai"
		reason = "Server: AkamaiGHost on 403"
	)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT[\s\S]+FROM jobs j[\s\S]+JOIN domains d`).
		WithArgs(jobID).
		WillReturnRows(jobRow(jobID, JobStatusRunning))
	mock.ExpectCommit()

	mock.ExpectBegin()

	// 1. Tasks first, ORDER BY id.
	mock.ExpectExec(`(?s)WITH picked AS \(\s*SELECT id FROM tasks\s+WHERE job_id = \$1\s+AND status IN \(\$3, \$4\)\s+ORDER BY id\s+FOR UPDATE\s*\)\s*UPDATE tasks t\s+SET status = \$2`).
		WithArgs(jobID, string(TaskStatusSkipped), string(TaskStatusPending), string(TaskStatusWaiting)).
		WillReturnResult(sqlmock.NewResult(0, 5))

	// 2. Jobs second — error_message carries the WAF reason.
	mock.ExpectExec(`UPDATE jobs\s+SET status = \$1, completed_at = \$2, error_message = \$3\s+WHERE id = \$4`).
		WithArgs(string(JobStatusBlocked), sqlmock.AnyArg(), sqlmock.AnyArg(), jobID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	// 3. Outbox cleanup.
	mock.ExpectExec(`DELETE FROM task_outbox WHERE job_id = \$1`).
		WithArgs(jobID).
		WillReturnResult(sqlmock.NewResult(0, 5))

	// 4. Domain row last.
	mock.ExpectExec(`UPDATE domains\s+SET waf_blocked\s+= TRUE,\s+waf_vendor\s+= \$1,\s+waf_blocked_at = NOW\(\)\s+WHERE id = \(SELECT domain_id FROM jobs WHERE id = \$2\)`).
		WithArgs(vendor, jobID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectCommit()

	err = jm.BlockJob(context.Background(), jobID, vendor, reason)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestBlockJob_AlreadyBlockedIsNoOp covers the circuit-breaker-races-
// pre-flight scenario: a second BlockJob against an already-blocked
// job must not run the transaction again.
func TestBlockJob_AlreadyBlockedIsNoOp(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mockDB.Close()

	jm := &JobManager{
		db:             mockDB,
		dbQueue:        &mockDbQueueWrapper{mockDB: mockDB},
		processedPages: make(map[string]struct{}),
	}

	const jobID = "job-already-blocked"

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT[\s\S]+FROM jobs j[\s\S]+JOIN domains d`).
		WithArgs(jobID).
		WillReturnRows(jobRow(jobID, JobStatusBlocked))
	mock.ExpectCommit()

	err = jm.BlockJob(context.Background(), jobID, "akamai", "")
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestBlockJob_TerminalStatusErrors asserts a block against a
// completed/failed/cancelled job returns an error rather than
// silently overwriting a finished result.
func TestBlockJob_TerminalStatusErrors(t *testing.T) {
	for _, status := range []JobStatus{JobStatusCompleted, JobStatusFailed, JobStatusCancelled} {
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

			err = jm.BlockJob(context.Background(), jobID, "akamai", "")
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "cannot be blocked")
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
