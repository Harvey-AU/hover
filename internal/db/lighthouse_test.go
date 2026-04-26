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
// XAUTOCLAIM hands it to a fresh consumer. Returns true so the
// consumer proceeds with the audit; the status='running' guard on
// CompleteLighthouseRun bounds any double-completion race.
func TestMarkLighthouseRunRunning_AcceptsPendingAndRunning(t *testing.T) {
	cases := []struct {
		name     string
		affected int64
		want     bool
	}{
		{"first delivery (pending → running)", 1, true},
		{"reclaim of in-flight (running → running)", 1, true},
		{"terminal row not touched", 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockDB, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = mockDB.Close() })

			db := &DB{client: mockDB}

			mock.ExpectExec(`UPDATE lighthouse_runs`).
				WithArgs(int64(42)).
				WillReturnResult(sqlmock.NewResult(0, tc.affected))

			moved, err := db.MarkLighthouseRunRunning(context.Background(), 42)
			require.NoError(t, err)
			assert.Equal(t, tc.want, moved)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
