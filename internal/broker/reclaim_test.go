package broker

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClient_ReclaimTerminalJobKeys_HappyPath seeds three jobs (one
// terminal, one still running, one with only a lighthouse stream and
// also terminal) and verifies the sweeper cleans only the terminal ones
// while leaving the running job intact.
func TestClient_ReclaimTerminalJobKeys_HappyPath(t *testing.T) {
	client, _ := newTestClientWithMiniredis(t)
	ctx := context.Background()

	// Job A: terminal, full key set.
	require.NoError(t, client.rdb.ZAdd(ctx, ScheduleKey("A"),
		redis.Z{Score: 1, Member: "task-a"}).Err())
	require.NoError(t, client.rdb.XGroupCreateMkStream(ctx,
		StreamKey("A"), ConsumerGroup("A"), "0").Err())
	require.NoError(t, client.rdb.HSet(ctx, RunningCountersKey, "A", 3).Err())

	// Job B: still running, must survive.
	require.NoError(t, client.rdb.ZAdd(ctx, ScheduleKey("B"),
		redis.Z{Score: 2, Member: "task-b"}).Err())
	require.NoError(t, client.rdb.HSet(ctx, RunningCountersKey, "B", 7).Err())

	// Job C: terminal, lighthouse-only — exercises the :lh suffix path.
	require.NoError(t, client.rdb.XGroupCreateMkStream(ctx,
		LighthouseStreamKey("C"), LighthouseConsumerGroup("C"), "0").Err())

	filter := func(ctx context.Context, ids []string) ([]string, error) {
		var terminal []string
		for _, id := range ids {
			if id == "A" || id == "C" {
				terminal = append(terminal, id)
			}
		}
		return terminal, nil
	}

	report, err := client.ReclaimTerminalJobKeys(ctx, filter)
	require.NoError(t, err)
	// Three unique candidates: A, B, C.
	assert.Equal(t, 3, report.CandidatesScanned)
	assert.Equal(t, 2, report.TerminalJobs)
	assert.Equal(t, 2, report.Cleaned)
	assert.Equal(t, 0, report.Failed)
	assert.NoError(t, report.FirstError)

	// A and C gone.
	for _, key := range []string{
		ScheduleKey("A"), StreamKey("A"), LighthouseStreamKey("C"),
	} {
		exists, err := client.rdb.Exists(ctx, key).Result()
		require.NoError(t, err)
		assert.Equal(t, int64(0), exists, "%s must be deleted", key)
	}
	exA, err := client.rdb.HExists(ctx, RunningCountersKey, "A").Result()
	require.NoError(t, err)
	assert.False(t, exA)

	// B intact.
	bScore, err := client.rdb.ZScore(ctx, ScheduleKey("B"), "task-b").Result()
	require.NoError(t, err)
	assert.Equal(t, float64(2), bScore)
	bCount, err := client.rdb.HGet(ctx, RunningCountersKey, "B").Int64()
	require.NoError(t, err)
	assert.Equal(t, int64(7), bCount)
}

// TestClient_ReclaimTerminalJobKeys_Empty verifies the sweeper is a
// no-op when Redis holds no per-job state.
func TestClient_ReclaimTerminalJobKeys_Empty(t *testing.T) {
	client, _ := newTestClientWithMiniredis(t)
	ctx := context.Background()

	called := false
	filter := func(ctx context.Context, ids []string) ([]string, error) {
		called = true
		return nil, nil
	}

	report, err := client.ReclaimTerminalJobKeys(ctx, filter)
	require.NoError(t, err)
	assert.Equal(t, 0, report.CandidatesScanned)
	assert.False(t, called, "filter must not be invoked when there are no candidates")
}

// TestClient_ReclaimTerminalJobKeys_FilterError surfaces filter errors
// without partial cleanup running.
func TestClient_ReclaimTerminalJobKeys_FilterError(t *testing.T) {
	client, _ := newTestClientWithMiniredis(t)
	ctx := context.Background()

	require.NoError(t, client.rdb.ZAdd(ctx, ScheduleKey("X"),
		redis.Z{Score: 1, Member: "task-x"}).Err())

	wantErr := errors.New("postgres unavailable")
	filter := func(ctx context.Context, ids []string) ([]string, error) {
		return nil, wantErr
	}

	report, err := client.ReclaimTerminalJobKeys(ctx, filter)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Equal(t, 1, report.CandidatesScanned)
	assert.Equal(t, 0, report.Cleaned)

	// X still present — no partial cleanup before the filter answered.
	exists, err := client.rdb.Exists(ctx, ScheduleKey("X")).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists)
}

// TestClient_ReclaimTerminalJobKeys_RejectsNilFilter guards the
// happy-path call signature.
func TestClient_ReclaimTerminalJobKeys_RejectsNilFilter(t *testing.T) {
	client, _ := newTestClientWithMiniredis(t)
	ctx := context.Background()

	_, err := client.ReclaimTerminalJobKeys(ctx, nil)
	require.Error(t, err)
}

// TestClient_listJobIDsInRedis exercises every source the sweeper
// scans, including the lighthouse stream :lh suffix and the running-
// counter HASH-only path.
func TestClient_listJobIDsInRedis(t *testing.T) {
	client, _ := newTestClientWithMiniredis(t)
	ctx := context.Background()

	require.NoError(t, client.rdb.ZAdd(ctx, ScheduleKey("sched-only"),
		redis.Z{Score: 1, Member: "m"}).Err())
	require.NoError(t, client.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamKey("stream-only"),
		Values: map[string]interface{}{"task_id": "t"},
	}).Err())
	require.NoError(t, client.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: LighthouseStreamKey("lh-only"),
		Values: map[string]interface{}{"task_id": "t"},
	}).Err())
	require.NoError(t, client.rdb.HSet(ctx,
		RunningCountersKey, "counter-only", 1).Err())
	// Job present in both schedule and stream — must dedupe to one.
	require.NoError(t, client.rdb.ZAdd(ctx, ScheduleKey("dual"),
		redis.Z{Score: 1, Member: "m"}).Err())
	require.NoError(t, client.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamKey("dual"),
		Values: map[string]interface{}{"task_id": "t"},
	}).Err())

	got, err := client.listJobIDsInRedis(ctx)
	require.NoError(t, err)
	sort.Strings(got)
	assert.Equal(t,
		[]string{"counter-only", "dual", "lh-only", "sched-only", "stream-only"},
		got,
	)
}
