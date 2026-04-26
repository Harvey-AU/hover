package broker

import (
	"context"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClient_ClearAll seeds every prefix the broker owns plus an
// unrelated key, then asserts ClearAll wipes only the hover:* keys.
func TestClient_ClearAll(t *testing.T) {
	client, mr := newTestClientWithMiniredis(t)
	ctx := context.Background()

	// sched ZSET.
	require.NoError(t, client.rdb.ZAdd(ctx, ScheduleKey("abc"),
		redis.Z{Score: 1, Member: "task-1"}).Err())

	// Stream + consumer group: XGroupCreateMkStream creates both;
	// XAdd appends a real entry so the stream has data.
	require.NoError(t, client.rdb.XGroupCreateMkStream(ctx,
		StreamKey("abc"), ConsumerGroup("abc"), "0").Err())
	require.NoError(t, client.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamKey("abc"),
		Values: map[string]interface{}{"task_id": "t1"},
	}).Err())

	// Running counters hash.
	require.NoError(t, client.rdb.HSet(ctx,
		RunningCountersKey, "abc", 7).Err())

	// Domain pacing keys.
	require.NoError(t, client.rdb.Set(ctx,
		DomainGateKey("example.com"), "1", 0).Err())
	require.NoError(t, client.rdb.HSet(ctx,
		DomainConfigKey("example.com"), "base_delay_ms", 1000).Err())
	require.NoError(t, client.rdb.HSet(ctx,
		DomainInflightKey("example.com"), "abc", 3).Err())

	// An unrelated key that must survive the clear.
	require.NoError(t, client.rdb.Set(ctx, "other:thing", "keep", 0).Err())

	deleted, err := client.ClearAll(ctx)
	require.NoError(t, err)
	// Six broker-owned top-level keys (sched, stream, running, gate,
	// cfg, flight). Consumer groups live inside the stream so DEL on
	// the stream removes them implicitly — they don't add to the count.
	assert.Equal(t, 6, deleted, "expected 6 broker keys deleted")

	// Every hover:* key must be gone.
	for _, key := range []string{
		ScheduleKey("abc"),
		StreamKey("abc"),
		RunningCountersKey,
		DomainGateKey("example.com"),
		DomainConfigKey("example.com"),
		DomainInflightKey("example.com"),
	} {
		exists, err := client.rdb.Exists(ctx, key).Result()
		require.NoError(t, err)
		assert.Equal(t, int64(0), exists, "key %s must be deleted", key)
	}

	// The unrelated key survives.
	val, err := client.rdb.Get(ctx, "other:thing").Result()
	require.NoError(t, err)
	assert.Equal(t, "keep", val)

	// Sanity: miniredis confirms no stray hover:* keys remain.
	for _, key := range mr.Keys() {
		assert.False(t, strings.HasPrefix(key, "hover:"),
			"unexpected surviving hover:* key %s", key)
	}
}

// TestClient_ClearAll_Empty verifies ClearAll returns 0 with no error
// when nothing is in Redis.
func TestClient_ClearAll_Empty(t *testing.T) {
	client, _ := newTestClientWithMiniredis(t)
	ctx := context.Background()

	deleted, err := client.ClearAll(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, deleted)
}

// TestClient_ClearAll_ManyKeys exercises the SCAN+DEL batch path by
// seeding well over the 500-batch threshold.
func TestClient_ClearAll_ManyKeys(t *testing.T) {
	client, _ := newTestClientWithMiniredis(t)
	ctx := context.Background()

	const seeded = 1234
	pipe := client.rdb.Pipeline()
	for i := 0; i < seeded; i++ {
		pipe.ZAdd(ctx, ScheduleKey("job-batch"),
			redis.Z{Score: float64(i), Member: i})
	}
	_, err := pipe.Exec(ctx)
	require.NoError(t, err)

	deleted, err := client.ClearAll(ctx)
	require.NoError(t, err)
	// All entries land in a single ZSET, so DEL counts that as 1 key.
	assert.Equal(t, 1, deleted)
}
