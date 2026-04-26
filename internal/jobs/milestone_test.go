package jobs

import (
	"context"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMaybeFireMilestones_FiresOnceWhenMilestoneAdvances covers the
// happy path: a single job whose progress crosses a 10% boundary fires
// OnProgressMilestone exactly once with the new milestone value, and
// the in-process tracker advances so the next call at the same
// milestone is a no-op.
func TestMaybeFireMilestones_FiresOnceWhenMilestoneAdvances(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mockDB.Close() })

	jm := &JobManager{
		db:                 mockDB,
		lastMilestoneFired: make(map[string]int),
	}

	var (
		mu    sync.Mutex
		fires []struct {
			jobID          string
			oldPct, newPct int
		}
	)
	jm.OnProgressMilestone = func(_ context.Context, jobID string, oldPct, newPct int) {
		mu.Lock()
		fires = append(fires, struct {
			jobID          string
			oldPct, newPct int
		}{jobID, oldPct, newPct})
		mu.Unlock()
	}

	// First call: 25/100 = 25% → milestone 20%, fires from 0 to 20.
	mock.ExpectQuery(`SELECT id, total_tasks`).
		WithArgs(pq.Array([]string{"job-1"})).
		WillReturnRows(sqlmock.NewRows([]string{"id", "total_tasks", "finished"}).
			AddRow("job-1", 100, 25))

	jm.MaybeFireMilestones(context.Background(), []string{"job-1"})

	mu.Lock()
	require.Len(t, fires, 1)
	assert.Equal(t, "job-1", fires[0].jobID)
	assert.Equal(t, 0, fires[0].oldPct)
	assert.Equal(t, 20, fires[0].newPct)
	mu.Unlock()

	// Second call at 27/100 = still 20% → no fire.
	mock.ExpectQuery(`SELECT id, total_tasks`).
		WithArgs(pq.Array([]string{"job-1"})).
		WillReturnRows(sqlmock.NewRows([]string{"id", "total_tasks", "finished"}).
			AddRow("job-1", 100, 27))

	jm.MaybeFireMilestones(context.Background(), []string{"job-1"})
	mu.Lock()
	require.Len(t, fires, 1, "no new fire — still in the same 20% milestone band")
	mu.Unlock()

	// Third call at 33/100 = 33% → milestone 30, fires from 20 to 30.
	mock.ExpectQuery(`SELECT id, total_tasks`).
		WithArgs(pq.Array([]string{"job-1"})).
		WillReturnRows(sqlmock.NewRows([]string{"id", "total_tasks", "finished"}).
			AddRow("job-1", 100, 33))

	jm.MaybeFireMilestones(context.Background(), []string{"job-1"})
	mu.Lock()
	require.Len(t, fires, 2)
	assert.Equal(t, 20, fires[1].oldPct)
	assert.Equal(t, 30, fires[1].newPct)
	mu.Unlock()

	require.NoError(t, mock.ExpectationsWereMet())
}

// TestMaybeFireMilestones_NoCallbackIsNoOp short-circuits when no
// callback is registered — the function must not even open a DB query
// in that case to keep the batch flush hot path cheap on deployments
// without the analysis app.
func TestMaybeFireMilestones_NoCallbackIsNoOp(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mockDB.Close() })

	jm := &JobManager{
		db:                 mockDB,
		lastMilestoneFired: make(map[string]int),
	}
	jm.MaybeFireMilestones(context.Background(), []string{"job-1"})

	require.NoError(t, mock.ExpectationsWereMet(), "no DB query should have been issued")
}

// TestClearMilestoneState_RemovesEntry pins the cleanup contract that
// terminal job transitions rely on so the in-process tracker doesn't
// grow unboundedly on a long-running worker.
func TestClearMilestoneState_RemovesEntry(t *testing.T) {
	jm := &JobManager{
		lastMilestoneFired: map[string]int{
			"job-a": 30,
			"job-b": 100,
		},
	}

	jm.clearMilestoneState("job-a")
	_, stillThere := jm.lastMilestoneFired["job-a"]
	assert.False(t, stillThere, "cleared job must be removed from tracker")
	assert.Equal(t, 100, jm.lastMilestoneFired["job-b"], "other jobs untouched")

	// Idempotent — clearing a missing entry is a no-op.
	jm.clearMilestoneState("never-existed")
	assert.Len(t, jm.lastMilestoneFired, 1)
}

// TestMaybeFireMilestones_ZeroTotalSkipped guards against the divide-by-zero
// path: a brand-new job with total_tasks=0 must not fire a milestone.
func TestMaybeFireMilestones_ZeroTotalSkipped(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mockDB.Close() })

	jm := &JobManager{
		db:                 mockDB,
		lastMilestoneFired: make(map[string]int),
	}
	called := false
	jm.OnProgressMilestone = func(_ context.Context, _ string, _, _ int) { called = true }

	mock.ExpectQuery(`SELECT id, total_tasks`).
		WithArgs(pq.Array([]string{"job-1"})).
		WillReturnRows(sqlmock.NewRows([]string{"id", "total_tasks", "finished"}).
			AddRow("job-1", 0, 0))

	jm.MaybeFireMilestones(context.Background(), []string{"job-1"})

	assert.False(t, called, "no fire when total_tasks is zero")
	require.NoError(t, mock.ExpectationsWereMet())
}
