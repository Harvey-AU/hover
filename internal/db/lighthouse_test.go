package db

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMarkLighthouseRunRunning_AcceptsPendingAndRunning pins the
// reclaim contract: a row that's already 'running' (left in flight by
// a crashed or shutting-down consumer) must transition cleanly when
// XAUTOCLAIM hands it to a fresh consumer. Returns moved=true so the
// consumer proceeds with the audit; the status='running' guard on
// CompleteLighthouseRun bounds any double-completion race.
//
// Also pins the source_task_id passthrough so the analysis-side runner
// can build R2 keys without an extra SELECT, including the empty-string
// fallback when the FK has been NULLed via ON DELETE SET NULL.
func TestMarkLighthouseRunRunning_AcceptsPendingAndRunning(t *testing.T) {
	cases := []struct {
		name        string
		rows        *sqlmock.Rows
		expectQuery bool
		wantMoved   bool
		wantTaskID  string
	}{
		{
			name:        "first delivery returns task id",
			rows:        sqlmock.NewRows([]string{"coalesce"}).AddRow("task-abc"),
			expectQuery: true,
			wantMoved:   true,
			wantTaskID:  "task-abc",
		},
		{
			name:        "reclaim of in-flight still returns task id",
			rows:        sqlmock.NewRows([]string{"coalesce"}).AddRow("task-xyz"),
			expectQuery: true,
			wantMoved:   true,
			wantTaskID:  "task-xyz",
		},
		{
			name:        "null source_task_id surfaces as empty string",
			rows:        sqlmock.NewRows([]string{"coalesce"}).AddRow(""),
			expectQuery: true,
			wantMoved:   true,
			wantTaskID:  "",
		},
		{
			name:        "terminal row not touched",
			rows:        sqlmock.NewRows([]string{"coalesce"}),
			expectQuery: true,
			wantMoved:   false,
			wantTaskID:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockDB, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = mockDB.Close() })

			db := &DB{client: mockDB}

			mock.ExpectQuery(`UPDATE lighthouse_runs`).
				WithArgs(int64(42)).
				WillReturnRows(tc.rows)

			moved, taskID, err := db.MarkLighthouseRunRunning(context.Background(), 42)
			require.NoError(t, err)
			assert.Equal(t, tc.wantMoved, moved)
			assert.Equal(t, tc.wantTaskID, taskID)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
