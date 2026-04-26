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

// TestClient_RemoveJobKeys seeds the full per-job key set for one job
// alongside an unrelated job, then asserts RemoveJobKeys deletes the
// targeted job's keys without disturbing the other.
func TestClient_RemoveJobKeys(t *testing.T) {
	client, _ := newTestClientWithMiniredis(t)
	ctx := context.Background()

	const target = "job-target"
	const survivor = "job-survivor"

	// Seed the targeted job: schedule ZSET, both streams (with their
	// consumer groups), and a running-counter entry.
	require.NoError(t, client.rdb.ZAdd(ctx, ScheduleKey(target),
		redis.Z{Score: 1, Member: "task-1"}).Err())
	require.NoError(t, client.rdb.XGroupCreateMkStream(ctx,
		StreamKey(target), ConsumerGroup(target), "0").Err())
	require.NoError(t, client.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamKey(target),
		Values: map[string]interface{}{"task_id": "t1"},
	}).Err())
	require.NoError(t, client.rdb.XGroupCreateMkStream(ctx,
		LighthouseStreamKey(target), LighthouseConsumerGroup(target), "0").Err())
	require.NoError(t, client.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: LighthouseStreamKey(target),
		Values: map[string]interface{}{"task_id": "lh1"},
	}).Err())
	require.NoError(t, client.rdb.HSet(ctx,
		RunningCountersKey, target, 5).Err())

	// Seed an unrelated job that must survive the cleanup.
	require.NoError(t, client.rdb.ZAdd(ctx, ScheduleKey(survivor),
		redis.Z{Score: 2, Member: "task-2"}).Err())
	require.NoError(t, client.rdb.HSet(ctx,
		RunningCountersKey, survivor, 9).Err())

	require.NoError(t, client.RemoveJobKeys(ctx, target))

	// Targeted keys gone.
	for _, key := range []string{
		ScheduleKey(target),
		StreamKey(target),
		LighthouseStreamKey(target),
	} {
		exists, err := client.rdb.Exists(ctx, key).Result()
		require.NoError(t, err)
		assert.Equal(t, int64(0), exists, "key %s must be deleted", key)
	}
	// Targeted running-counter field gone.
	exists, err := client.rdb.HExists(ctx, RunningCountersKey, target).Result()
	require.NoError(t, err)
	assert.False(t, exists, "running counter for %s must be deleted", target)

	// Survivor untouched.
	survScore, err := client.rdb.ZScore(ctx, ScheduleKey(survivor), "task-2").Result()
	require.NoError(t, err)
	assert.Equal(t, float64(2), survScore)
	survCount, err := client.rdb.HGet(ctx, RunningCountersKey, survivor).Int64()
	require.NoError(t, err)
	assert.Equal(t, int64(9), survCount)
}

// TestClient_RemoveJobKeys_Idempotent verifies RemoveJobKeys tolerates
// missing streams / consumer groups, so a partially-cleaned or
// never-started job can be re-cleaned without error.
func TestClient_RemoveJobKeys_Idempotent(t *testing.T) {
	client, _ := newTestClientWithMiniredis(t)
	ctx := context.Background()

	// Nothing seeded — cleanup must succeed silently.
	require.NoError(t, client.RemoveJobKeys(ctx, "ghost"))

	// Run again to confirm a second call is also harmless.
	require.NoError(t, client.RemoveJobKeys(ctx, "ghost"))
}

// TestClient_RemoveJobKeys_RejectsEmpty guards against a caller passing
// "" by mistake — that would HDEL nothing but DEL-against-prefix would
// match everything if the key helpers ever changed shape.
func TestClient_RemoveJobKeys_RejectsEmpty(t *testing.T) {
	client, _ := newTestClientWithMiniredis(t)
	ctx := context.Background()

	err := client.RemoveJobKeys(ctx, "")
	require.Error(t, err)
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
