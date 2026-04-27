package broker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- publishAndRemove validation -------------------------------------------

// TestPublishAndRemove_RejectsLighthouseWithoutRunID exercises the
// poison-message guard added with task_type routing: a lighthouse
// outbox row that somehow reaches the dispatcher without a populated
// LighthouseRunID must be rejected before the XADD lands, otherwise
// the analysis consumer would receive a message it cannot tie back to
// any lighthouse_runs row.
func TestPublishAndRemove_RejectsLighthouseWithoutRunID(t *testing.T) {
	lister := &staticJobLister{ids: []string{"job-bad"}}
	conc := &staticConcurrency{can: true}
	d, _, _, _, _, _ := newDispatcherRig(t, lister, conc)

	entry := ScheduleEntry{
		TaskID:          "task-bad",
		JobID:           "job-bad",
		PageID:          1,
		Host:            "example.com",
		Path:            "/",
		Priority:        0.5,
		SourceType:      "lighthouse",
		SourceURL:       "https://example.com/",
		RunAt:           time.Now(),
		TaskType:        "lighthouse",
		LighthouseRunID: 0, // intentionally missing
	}

	err := d.publishAndRemove(context.Background(), &entry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing lighthouse_run_id")
}

// TestPublishAndRemove_RejectsUnknownTaskType ensures a producer that
// drifts ahead of the dispatcher (e.g. a new task type added to the
// schema before this code knows how to route it) fails fast rather
// than silently dumping work onto the crawl stream.
func TestPublishAndRemove_RejectsUnknownTaskType(t *testing.T) {
	lister := &staticJobLister{ids: []string{"job-unknown"}}
	conc := &staticConcurrency{can: true}
	d, _, _, _, _, _ := newDispatcherRig(t, lister, conc)

	entry := ScheduleEntry{
		TaskID:     "task-x",
		JobID:      "job-unknown",
		PageID:     1,
		Host:       "example.com",
		Path:       "/",
		Priority:   0.5,
		SourceType: "future",
		RunAt:      time.Now(),
		TaskType:   "future-thing",
	}

	err := d.publishAndRemove(context.Background(), &entry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown task_type")
}

// --- Test doubles for dispatcher collaborators ---------------------------

type staticJobLister struct {
	ids []string
	err error
}

func (l *staticJobLister) ActiveJobIDs(_ context.Context) ([]string, error) {
	return l.ids, l.err
}

type staticConcurrency struct {
	can bool
	err error
}

func (c *staticConcurrency) CanDispatch(_ context.Context, _ string) (bool, error) {
	return c.can, c.err
}

// --- Helpers -------------------------------------------------------------

func seedEntry(t *testing.T, s *Scheduler, jobID, taskID, host string, runAt time.Time) ScheduleEntry {
	t.Helper()
	entry := ScheduleEntry{
		TaskID:     taskID,
		JobID:      jobID,
		PageID:     1,
		Host:       host,
		Path:       "/",
		Priority:   0.5,
		RetryCount: 0,
		SourceType: "sitemap",
		SourceURL:  "https://" + host + "/sitemap.xml",
		RunAt:      runAt,
	}
	require.NoError(t, s.Schedule(context.Background(), entry))
	return entry
}

func newDispatcherRig(t *testing.T, lister JobLister, conc ConcurrencyChecker) (*Dispatcher, *Scheduler, *DomainPacer, *RunningCounters, *Client, *miniredis.Miniredis) {
	t.Helper()
	client, mr := newTestClientWithMiniredis(t)
	scheduler := NewScheduler(client)
	counters := NewRunningCounters(client)
	pacer := NewDomainPacer(client, DefaultPacerConfig())
	d := NewDispatcher(client, scheduler, pacer, counters, lister, conc, DispatcherOpts{
		ScanInterval: 10 * time.Millisecond,
		BatchSize:    50,
	})
	return d, scheduler, pacer, counters, client, mr
}

// --- Happy path ---------------------------------------------------------

// Verifies the full dispatcher → stream → consumer path:
// schedule a task, dispatch it, read it from the stream, ack it.
func TestDispatcher_HappyPath_SchedulesThenConsumes(t *testing.T) {
	lister := &staticJobLister{ids: []string{"job-happy"}}
	conc := &staticConcurrency{can: true}
	d, s, _, counters, client, _ := newDispatcherRig(t, lister, conc)
	ctx := context.Background()

	// Schedule a task due now.
	entry := seedEntry(t, s, "job-happy", "task-1", "example.com", time.Now())

	// One dispatcher tick should move it from ZSET to Stream.
	n, err := d.dispatchJob(ctx, "job-happy", time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, n, "expected 1 dispatched task")

	// ZSET is empty, counter is 1.
	pending, err := s.PendingCount(ctx, "job-happy")
	require.NoError(t, err)
	assert.Equal(t, int64(0), pending)

	running, err := counters.Get(ctx, "job-happy")
	require.NoError(t, err)
	assert.Equal(t, int64(1), running)

	// Consumer reads it back.
	consumer := NewConsumer(client, DefaultConsumerOpts("test-consumer"))
	msgs, err := consumer.ReadNonBlocking(ctx, "job-happy")
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, entry.TaskID, msgs[0].TaskID)
	assert.Equal(t, entry.Host, msgs[0].Host)

	// Ack.
	require.NoError(t, consumer.Ack(ctx, "job-happy", msgs[0].MessageID))
}

// --- Concurrency gating -------------------------------------------------

func TestDispatcher_ConcurrencyGate_BlocksRemainder(t *testing.T) {
	lister := &staticJobLister{ids: []string{"job-c"}}
	conc := &staticConcurrency{can: false} // never allow dispatch
	d, s, _, counters, _, _ := newDispatcherRig(t, lister, conc)
	ctx := context.Background()

	seedEntry(t, s, "job-c", "t1", "a.com", time.Now())
	seedEntry(t, s, "job-c", "t2", "b.com", time.Now())

	n, err := d.dispatchJob(ctx, "job-c", time.Now())
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// Both tasks stay in the ZSET, counter untouched.
	pending, err := s.PendingCount(ctx, "job-c")
	require.NoError(t, err)
	assert.Equal(t, int64(2), pending)

	running, err := counters.Get(ctx, "job-c")
	require.NoError(t, err)
	assert.Equal(t, int64(0), running)
}

func TestDispatcher_ConcurrencyCheckError_BreaksBatch(t *testing.T) {
	lister := &staticJobLister{ids: []string{"job-e"}}
	conc := &staticConcurrency{err: errors.New("boom")}
	d, s, _, _, _, _ := newDispatcherRig(t, lister, conc)
	ctx := context.Background()

	seedEntry(t, s, "job-e", "t1", "a.com", time.Now())

	n, err := d.dispatchJob(ctx, "job-e", time.Now())
	require.NoError(t, err)
	assert.Equal(t, 0, n, "concurrency error should stop dispatch")

	pending, err := s.PendingCount(ctx, "job-e")
	require.NoError(t, err)
	assert.Equal(t, int64(1), pending)
}

// --- Domain pacing ------------------------------------------------------

// A domain with a non-zero delay should pace successive dispatches:
// the first acquires immediately, the second is rescheduled.
func TestDispatcher_DomainPacing_ReschedulesWhenGated(t *testing.T) {
	lister := &staticJobLister{ids: []string{"job-p"}}
	conc := &staticConcurrency{can: true}
	d, s, pacer, _, _, _ := newDispatcherRig(t, lister, conc)
	ctx := context.Background()

	// 2 second delay — easy to observe.
	require.NoError(t, pacer.Seed(ctx, "slow.com", 2000, 0, 0))

	now := time.Now()
	e1 := seedEntry(t, s, "job-p", "t1", "slow.com", now)
	e2 := seedEntry(t, s, "job-p", "t2", "slow.com", now)

	n, err := d.dispatchJob(ctx, "job-p", now)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only first task should dispatch under 2s pacing")

	// t1 gone from ZSET, t2 rescheduled to a later score.
	pending, err := s.PendingCount(ctx, "job-p")
	require.NoError(t, err)
	assert.Equal(t, int64(1), pending)

	// t2 score should now be in the future (beyond `now`).
	remaining, err := s.DueItems(ctx, "job-p", now, 10)
	require.NoError(t, err)
	assert.Empty(t, remaining, "t2 must not be due immediately after pacing pushback")

	// Sanity: dispatched entry's member is gone; rescheduled entry's member is unchanged.
	_ = e1
	_ = e2
}

// --- Malformed ZSET entry cleanup --------------------------------------

func TestDispatcher_MalformedZSETEntry_IsRemoved(t *testing.T) {
	lister := &staticJobLister{ids: []string{"job-m"}}
	conc := &staticConcurrency{can: true}
	d, s, _, _, client, _ := newDispatcherRig(t, lister, conc)
	ctx := context.Background()

	// Insert a malformed member directly.
	key := ScheduleKey("job-m")
	require.NoError(t, client.rdb.ZAdd(ctx, key, redis.Z{
		Score:  float64(time.Now().UnixMilli()),
		Member: "not|enough|parts",
	}).Err())

	// Dispatcher tick should silently drop it.
	_, err := d.dispatchJob(ctx, "job-m", time.Now())
	require.NoError(t, err)

	pending, err := s.PendingCount(ctx, "job-m")
	require.NoError(t, err)
	assert.Equal(t, int64(0), pending, "malformed entry should be ZREM'd")
}

// --- Atomic Schedule+Ack for retries -----------------------------------

// ScheduleAndAck must enqueue the retry into the ZSET and ACK the
// original stream message in one atomic Redis operation, so a partial
// failure can't leave the retry queued while the original is still in
// the PEL (which would duplicate the crawl on XAUTOCLAIM redelivery).
func TestScheduler_ScheduleAndAck_AtomicRetry(t *testing.T) {
	lister := &staticJobLister{ids: []string{"job-a"}}
	conc := &staticConcurrency{can: true}
	d, scheduler, _, _, client, _ := newDispatcherRig(t, lister, conc)
	ctx := context.Background()

	// Dispatch one task so there's a stream + group + PEL entry.
	seedEntry(t, scheduler, "job-a", "t1", "a.com", time.Now())
	_, err := d.dispatchJob(ctx, "job-a", time.Now())
	require.NoError(t, err)

	consumer := NewConsumer(client, DefaultConsumerOpts("test-c"))
	msgs, err := consumer.ReadNonBlocking(ctx, "job-a")
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	originalID := msgs[0].MessageID

	// Perform the atomic retry schedule+ack.
	retryEntry := ScheduleEntry{
		TaskID:     "t1",
		JobID:      "job-a",
		PageID:     1,
		Host:       "a.com",
		Path:       "/",
		Priority:   0.5,
		RetryCount: 1,
		SourceType: "sitemap",
		SourceURL:  "https://a.com/sitemap.xml",
		RunAt:      time.Now().Add(30 * time.Second),
	}
	require.NoError(t, scheduler.ScheduleAndAck(ctx, retryEntry, "job-a", originalID))

	// ZSET holds the retry.
	pending, err := scheduler.PendingCount(ctx, "job-a")
	require.NoError(t, err)
	assert.Equal(t, int64(1), pending, "retry should be in ZSET")

	// PEL is empty (message was ACKed).
	result, err := client.rdb.XPending(ctx, StreamKey("job-a"), ConsumerGroup("job-a")).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), result.Count, "PEL should be empty after atomic ack")
}

// --- XAUTOCLAIM reclaim ------------------------------------------------

// Messages unacked past MinIdleTime must be reclaimed by the next consumer
// invocation. Uses a short real sleep since miniredis's XAUTOCLAIM uses
// wall-clock idle time rather than its virtual clock.
func TestConsumer_ReclaimStaleMessage(t *testing.T) {
	client := newTestClient(t)
	scheduler := NewScheduler(client)
	counters := NewRunningCounters(client)
	pacer := NewDomainPacer(client, DefaultPacerConfig())
	lister := &staticJobLister{ids: []string{"job-r"}}
	conc := &staticConcurrency{can: true}
	d := NewDispatcher(client, scheduler, pacer, counters, lister, conc, DispatcherOpts{
		ScanInterval: 10 * time.Millisecond,
		BatchSize:    10,
	})
	ctx := context.Background()

	// Dispatch one task so a stream + group exists.
	seedEntry(t, scheduler, "job-r", "t1", "a.com", time.Now())
	_, err := d.dispatchJob(ctx, "job-r", time.Now())
	require.NoError(t, err)

	// First consumer reads but does NOT ack.
	c1 := NewConsumer(client, ConsumerOpts{
		ConsumerName:  "worker-1",
		BlockTimeout:  0,
		Count:         10,
		MinIdleTime:   50 * time.Millisecond,
		MaxDeliveries: 3,
	})
	msgs, err := c1.ReadNonBlocking(ctx, "job-r")
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	// Short real sleep so the message's idle time exceeds MinIdleTime.
	time.Sleep(100 * time.Millisecond)

	// Second consumer reclaims it.
	c2 := NewConsumer(client, ConsumerOpts{
		ConsumerName:  "worker-2",
		BlockTimeout:  0,
		Count:         10,
		MinIdleTime:   50 * time.Millisecond,
		MaxDeliveries: 3,
	})
	reclaimed, dead, err := c2.ReclaimStale(ctx, "job-r")
	require.NoError(t, err)
	assert.Empty(t, dead, "first reclaim should not dead-letter")
	require.Len(t, reclaimed, 1, "stale message should be reclaimed")
	assert.Equal(t, msgs[0].TaskID, reclaimed[0].TaskID)
}

// --- OnFirstDispatch hook -----------------------------------------------

// TestDispatcher_OnFirstDispatch_FiresExactlyOncePerJob verifies the
// hook fires the first time a task lands in the stream for a given
// jobID and never again in the same dispatcher's lifetime, even after
// many dispatches across multiple ticks.
func TestDispatcher_OnFirstDispatch_FiresExactlyOncePerJob(t *testing.T) {
	lister := &staticJobLister{ids: []string{"job-A", "job-B"}}
	conc := &staticConcurrency{can: true}
	d, s, _, _, _, _ := newDispatcherRig(t, lister, conc)
	ctx := context.Background()

	calls := make(map[string]int)
	d.SetOnFirstDispatch(func(_ context.Context, jobID string) {
		calls[jobID]++
	})

	// Seed several tasks for two different jobs.
	now := time.Now()
	seedEntry(t, s, "job-A", "a1", "a.com", now)
	seedEntry(t, s, "job-A", "a2", "a.com", now)
	seedEntry(t, s, "job-A", "a3", "a.com", now)
	seedEntry(t, s, "job-B", "b1", "b.com", now)
	seedEntry(t, s, "job-B", "b2", "b.com", now)

	for _, id := range []string{"job-A", "job-B"} {
		_, err := d.dispatchJob(ctx, id, now)
		require.NoError(t, err)
		// Run a second tick to confirm the hook does not fire again.
		_, err = d.dispatchJob(ctx, id, now)
		require.NoError(t, err)
	}

	assert.Equal(t, 1, calls["job-A"], "job-A hook must fire exactly once")
	assert.Equal(t, 1, calls["job-B"], "job-B hook must fire exactly once")
}

// TestDispatcher_OnFirstDispatch_NotInvokedWhenUnset confirms the
// dispatcher tolerates a nil hook (default state) without panicking.
func TestDispatcher_OnFirstDispatch_NotInvokedWhenUnset(t *testing.T) {
	lister := &staticJobLister{ids: []string{"job-N"}}
	conc := &staticConcurrency{can: true}
	d, s, _, _, _, _ := newDispatcherRig(t, lister, conc)
	ctx := context.Background()

	seedEntry(t, s, "job-N", "n1", "n.com", time.Now())

	n, err := d.dispatchJob(ctx, "job-N", time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}
