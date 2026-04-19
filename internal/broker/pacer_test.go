package broker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDomainPacer_Seed(t *testing.T) {
	client := newTestClient(t)
	pacer := NewDomainPacer(client, DefaultPacerConfig())
	ctx := context.Background()

	err := pacer.Seed(ctx, "example.com", 100, 200, 50)
	require.NoError(t, err)

	// Verify hash was set.
	key := DomainConfigKey("example.com")
	base, err := client.rdb.HGet(ctx, key, "base_delay_ms").Result()
	require.NoError(t, err)
	assert.Equal(t, "100", base)

	adaptive, err := client.rdb.HGet(ctx, key, "adaptive_delay_ms").Result()
	require.NoError(t, err)
	assert.Equal(t, "200", adaptive)
}

func TestDomainPacer_Seed_Idempotent(t *testing.T) {
	client := newTestClient(t)
	pacer := NewDomainPacer(client, DefaultPacerConfig())
	ctx := context.Background()

	require.NoError(t, pacer.Seed(ctx, "example.com", 100, 200, 50))
	require.NoError(t, pacer.Seed(ctx, "example.com", 999, 999, 999))

	// First seed wins (HSETNX).
	key := DomainConfigKey("example.com")
	base, err := client.rdb.HGet(ctx, key, "base_delay_ms").Result()
	require.NoError(t, err)
	assert.Equal(t, "100", base)
}

func TestDomainPacer_TryAcquire_NoDelay(t *testing.T) {
	client := newTestClient(t)
	pacer := NewDomainPacer(client, DefaultPacerConfig())
	ctx := context.Background()

	// No domain config seeded — delay is 0, always acquires.
	result, err := pacer.TryAcquire(ctx, "no-config.com")
	require.NoError(t, err)
	assert.True(t, result.Acquired)
}

func TestDomainPacer_TryAcquire_WithDelay(t *testing.T) {
	client, mr := newTestClientWithMiniredis(t)
	pacer := NewDomainPacer(client, DefaultPacerConfig())
	ctx := context.Background()

	require.NoError(t, pacer.Seed(ctx, "slow.com", 1000, 0, 0))

	// First acquire should succeed.
	r1, err := pacer.TryAcquire(ctx, "slow.com")
	require.NoError(t, err)
	assert.True(t, r1.Acquired)

	// Second acquire should fail (gate held).
	r2, err := pacer.TryAcquire(ctx, "slow.com")
	require.NoError(t, err)
	assert.False(t, r2.Acquired)
	assert.True(t, r2.RetryAfter > 0)

	// Fast-forward past the delay.
	mr.FastForward(r2.RetryAfter)

	// Third acquire should succeed.
	r3, err := pacer.TryAcquire(ctx, "slow.com")
	require.NoError(t, err)
	assert.True(t, r3.Acquired)
}

func TestDomainPacer_Inflight(t *testing.T) {
	client := newTestClient(t)
	pacer := NewDomainPacer(client, DefaultPacerConfig())
	ctx := context.Background()

	require.NoError(t, pacer.IncrementInflight(ctx, "example.com", "job-1"))
	require.NoError(t, pacer.IncrementInflight(ctx, "example.com", "job-1"))

	count, err := pacer.GetInflight(ctx, "example.com", "job-1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)

	require.NoError(t, pacer.DecrementInflight(ctx, "example.com", "job-1"))

	count, err = pacer.GetInflight(ctx, "example.com", "job-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestDomainPacer_Release_AdaptiveDelay(t *testing.T) {
	client := newTestClient(t)
	cfg := DefaultPacerConfig()
	cfg.SuccessThreshold = 2
	cfg.DelayStepMS = 100
	pacer := NewDomainPacer(client, cfg)
	ctx := context.Background()

	// Seed with an adaptive delay.
	require.NoError(t, pacer.Seed(ctx, "test.com", 50, 300, 50))
	require.NoError(t, pacer.IncrementInflight(ctx, "test.com", "j1"))

	// Two successes should reduce the adaptive delay.
	require.NoError(t, pacer.Release(ctx, "test.com", "j1", true, false))
	require.NoError(t, pacer.IncrementInflight(ctx, "test.com", "j1"))
	require.NoError(t, pacer.Release(ctx, "test.com", "j1", true, false))

	key := DomainConfigKey("test.com")
	adaptive, err := client.rdb.HGet(ctx, key, "adaptive_delay_ms").Result()
	require.NoError(t, err)
	assert.Equal(t, "200", adaptive) // 300 - 100

	// A rate-limit error should increase it.
	require.NoError(t, pacer.IncrementInflight(ctx, "test.com", "j1"))
	require.NoError(t, pacer.Release(ctx, "test.com", "j1", false, true))

	adaptive, err = client.rdb.HGet(ctx, key, "adaptive_delay_ms").Result()
	require.NoError(t, err)
	assert.Equal(t, "300", adaptive) // 200 + 100
}
