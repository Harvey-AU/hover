package broker

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunningCounters_IncrementDecrement(t *testing.T) {
	client, _ := newTestClient(t)
	rc := NewRunningCounters(client, zerolog.Nop())
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
	client, _ := newTestClient(t)
	rc := NewRunningCounters(client, zerolog.Nop())
	ctx := context.Background()

	_, _ = rc.Increment(ctx, "j1")
	_, _ = rc.Increment(ctx, "j1")
	_, _ = rc.Increment(ctx, "j2")

	counts, err := rc.GetAll(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), counts["j1"])
	assert.Equal(t, int64(1), counts["j2"])
}

func TestRunningCounters_Reconcile(t *testing.T) {
	client, _ := newTestClient(t)
	rc := NewRunningCounters(client, zerolog.Nop())
	ctx := context.Background()

	// Start with some stale data.
	_, _ = rc.Increment(ctx, "stale-job")

	// Reconcile with authoritative counts.
	err := rc.Reconcile(ctx, map[string]int64{
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
