package broker

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunningCounters_IncrementDecrement(t *testing.T) {
	client := newTestClient(t)
	rc := NewRunningCounters(client)
	ctx := context.Background()

	val, err := rc.Increment(ctx, "job-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), val)

	val, err = rc.Increment(ctx, "job-1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), val)

	val, err = rc.Decrement(ctx, "job-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), val)

	// Decrement to zero should clean up.
	val, err = rc.Decrement(ctx, "job-1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), val)

	// Get after cleanup should return 0.
	val, err = rc.Get(ctx, "job-1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), val)
}

func TestRunningCounters_GetAll(t *testing.T) {
	client := newTestClient(t)
	rc := NewRunningCounters(client)
	ctx := context.Background()

	_, err := rc.Increment(ctx, "j1")
	require.NoError(t, err)
	_, err = rc.Increment(ctx, "j1")
	require.NoError(t, err)
	_, err = rc.Increment(ctx, "j2")
	require.NoError(t, err)

	counts, err := rc.GetAll(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), counts["j1"])
	assert.Equal(t, int64(1), counts["j2"])
}

func TestRunningCounters_Reconcile(t *testing.T) {
	client := newTestClient(t)
	rc := NewRunningCounters(client)
	ctx := context.Background()

	// Start with some stale data.
	_, err := rc.Increment(ctx, "stale-job")
	require.NoError(t, err)

	// Reconcile with authoritative counts.
	err = rc.Reconcile(ctx, map[string]int64{
		"j1": 5,
		"j2": 3,
	})
	require.NoError(t, err)

	// Stale job should be gone.
	val, err := rc.Get(ctx, "stale-job")
	require.NoError(t, err)
	assert.Equal(t, int64(0), val)

	// New values should be set.
	val, err = rc.Get(ctx, "j1")
	require.NoError(t, err)
	assert.Equal(t, int64(5), val)
}

// TestRunningCounters_Reconcile_Empty covers the no-running-tasks
// branch: Reconcile must clear the hash without invoking the script.
func TestRunningCounters_Reconcile_Empty(t *testing.T) {
	client := newTestClient(t)
	rc := NewRunningCounters(client)
	ctx := context.Background()

	_, err := rc.Increment(ctx, "ghost")
	require.NoError(t, err)

	require.NoError(t, rc.Reconcile(ctx, nil))

	all, err := rc.GetAll(ctx)
	require.NoError(t, err)
	assert.Empty(t, all)
}

// TestRunningCounters_Reconcile_AllNonPositive verifies the path where
// every supplied count is <=0: the hash is cleared, no fields written.
func TestRunningCounters_Reconcile_AllNonPositive(t *testing.T) {
	client := newTestClient(t)
	rc := NewRunningCounters(client)
	ctx := context.Background()

	_, err := rc.Increment(ctx, "old")
	require.NoError(t, err)

	require.NoError(t, rc.Reconcile(ctx, map[string]int64{
		"a": 0,
		"b": -3,
	}))

	all, err := rc.GetAll(ctx)
	require.NoError(t, err)
	assert.Empty(t, all)
}

// TestRunningCounters_Reconcile_Atomic exercises the Lua script via a
// large fan-in: many fields land in one EVAL. Catches regressions
// where the script mishandles long ARGV or unpack widths.
func TestRunningCounters_Reconcile_Atomic(t *testing.T) {
	client := newTestClient(t)
	rc := NewRunningCounters(client)
	ctx := context.Background()

	counts := make(map[string]int64, 200)
	for i := 0; i < 200; i++ {
		counts[fmt.Sprintf("job-%03d", i)] = int64(i + 1)
	}
	require.NoError(t, rc.Reconcile(ctx, counts))

	got, err := rc.GetAll(ctx)
	require.NoError(t, err)
	assert.Equal(t, len(counts), len(got))
	for k, v := range counts {
		assert.Equal(t, v, got[k], "field %s", k)
	}
}
