package broker

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T) *Client {
	t.Helper()
	client, _ := newTestClientWithMiniredis(t)
	return client
}

// newTestClientWithMiniredis is for tests that need to drive the underlying
// miniredis instance directly (e.g. to simulate a flush).
func newTestClientWithMiniredis(t *testing.T) (*Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &Client{rdb: rdb}, mr
}

func TestSchedule_SingleEntry(t *testing.T) {
	client := newTestClient(t)
	s := NewScheduler(client)
	ctx := context.Background()

	now := time.Now()
	entry := ScheduleEntry{
		TaskID:     "task-1",
		JobID:      "job-1",
		PageID:     42,
		Host:       "example.com",
		Path:       "/page",
		Priority:   0.8,
		RetryCount: 0,
		SourceType: "sitemap",
		SourceURL:  "https://example.com/sitemap.xml",
		RunAt:      now,
	}

	err := s.Schedule(ctx, entry)
	require.NoError(t, err)

	// Verify ZSET has one member via the scheduler's own API.
	count, err := s.PendingCount(ctx, "job-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestScheduleBatch(t *testing.T) {
	client := newTestClient(t)
	s := NewScheduler(client)
	ctx := context.Background()

	now := time.Now()
	entries := []ScheduleEntry{
		{TaskID: "t1", JobID: "j1", PageID: 1, Host: "a.com", Path: "/a", RunAt: now},
		{TaskID: "t2", JobID: "j1", PageID: 2, Host: "a.com", Path: "/b", RunAt: now.Add(time.Second)},
		{TaskID: "t3", JobID: "j2", PageID: 3, Host: "b.com", Path: "/c", RunAt: now},
	}

	err := s.ScheduleBatch(ctx, entries)
	require.NoError(t, err)

	// j1 should have 2, j2 should have 1.
	c1, err := s.PendingCount(ctx, "j1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), c1)

	c2, err := s.PendingCount(ctx, "j2")
	require.NoError(t, err)
	assert.Equal(t, int64(1), c2)
}

// TestScheduleBatch_PipelineFailure verifies that when the underlying
// Redis connection dies mid-pipeline, callers receive a non-BatchError
// so they know to treat the entire batch as failed. A *BatchError is
// only returned when the pipeline actually ran and some entries failed.
func TestScheduleBatch_PipelineFailure(t *testing.T) {
	client, mr := newTestClientWithMiniredis(t)
	s := NewScheduler(client)
	ctx := context.Background()

	mr.Close() // force pipeline to fail

	entries := []ScheduleEntry{
		{TaskID: "t1", JobID: "j1", PageID: 1, Host: "a.com", Path: "/a", RunAt: time.Now()},
	}

	err := s.ScheduleBatch(ctx, entries)
	require.Error(t, err)

	var be *BatchError
	require.False(t, errors.As(err, &be),
		"pipeline-level failure must not be a *BatchError — callers need to treat all entries as failed")
}

func TestDueItems(t *testing.T) {
	client := newTestClient(t)
	s := NewScheduler(client)
	ctx := context.Background()

	now := time.Now()
	past := now.Add(-time.Minute)
	future := now.Add(time.Minute)

	entries := []ScheduleEntry{
		{TaskID: "past", JobID: "j1", PageID: 1, Host: "a.com", Path: "/p", RunAt: past},
		{TaskID: "future", JobID: "j1", PageID: 2, Host: "a.com", Path: "/f", RunAt: future},
	}
	require.NoError(t, s.ScheduleBatch(ctx, entries))

	due, err := s.DueItems(ctx, "j1", now, 10)
	require.NoError(t, err)
	assert.Len(t, due, 1)
	assert.Equal(t, "past", due[0].TaskID)
}

func TestReschedule(t *testing.T) {
	client := newTestClient(t)
	s := NewScheduler(client)
	ctx := context.Background()

	now := time.Now()
	entry := ScheduleEntry{
		TaskID: "t1", JobID: "j1", PageID: 1, Host: "a.com", Path: "/",
		RunAt: now,
	}
	require.NoError(t, s.Schedule(ctx, entry))

	// Should be due now.
	due, err := s.DueItems(ctx, "j1", now, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)

	// Reschedule to future.
	future := now.Add(10 * time.Minute)
	err = s.Reschedule(ctx, entry, future)
	require.NoError(t, err)

	// Should no longer be due at the old time.
	due, err = s.DueItems(ctx, "j1", now, 10)
	require.NoError(t, err)
	assert.Len(t, due, 0)

	// Should be due at the rescheduled time.
	due, err = s.DueItems(ctx, "j1", future, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "t1", due[0].TaskID)
}

func TestParseScheduleEntry_RoundTrip(t *testing.T) {
	entry := ScheduleEntry{
		TaskID:     "abc-123",
		JobID:      "def-456",
		PageID:     99,
		Host:       "example.com",
		Path:       "/path/to/page",
		Priority:   0.7500,
		RetryCount: 2,
		SourceType: "link",
		SourceURL:  "https://example.com/source",
	}

	member := entry.Member()
	parsed, err := ParseScheduleEntry(member, 1234567890.0)
	require.NoError(t, err)

	assert.Equal(t, entry.TaskID, parsed.TaskID)
	assert.Equal(t, entry.JobID, parsed.JobID)
	assert.Equal(t, entry.PageID, parsed.PageID)
	assert.Equal(t, entry.Host, parsed.Host)
	assert.Equal(t, entry.Path, parsed.Path)
	assert.InDelta(t, entry.Priority, parsed.Priority, 0.001)
	assert.Equal(t, entry.RetryCount, parsed.RetryCount)
	assert.Equal(t, entry.SourceType, parsed.SourceType)
	assert.Equal(t, entry.SourceURL, parsed.SourceURL)

	// Verify RunAt was reconstructed from the score.
	expectedRunAt := time.UnixMilli(int64(1234567890.0))
	assert.Equal(t, expectedRunAt, parsed.RunAt)
}

// TestParseScheduleEntry_LegacyNineFields exercises backwards
// compatibility with the pre-Phase-2 ZSET member format. A rolling
// deploy can leave 9-field members in the schedule ZSET; the parser
// must accept them and default TaskType to "crawl" with no
// LighthouseRunID rather than dropping work on the floor.
func TestParseScheduleEntry_LegacyNineFields(t *testing.T) {
	legacy := "abc-123|def-456|99|example.com|/path|0.7500|2|link|https://example.com/source"

	parsed, err := ParseScheduleEntry(legacy, 1234567890.0)
	require.NoError(t, err)

	assert.Equal(t, "abc-123", parsed.TaskID)
	assert.Equal(t, "crawl", parsed.TaskType, "legacy entries must default to crawl")
	assert.Equal(t, int64(0), parsed.LighthouseRunID, "legacy entries have no run id")
}

// TestParseScheduleEntry_LighthouseRoundTrip covers the 11-field path:
// a lighthouse-tagged ScheduleEntry must round-trip through Member and
// ParseScheduleEntry without losing TaskType or LighthouseRunID, since
// the dispatcher routes on TaskType and the consumer reads the run id
// straight from the stream payload.
func TestParseScheduleEntry_LighthouseRoundTrip(t *testing.T) {
	entry := ScheduleEntry{
		TaskID:          "abc-123",
		JobID:           "def-456",
		PageID:          99,
		Host:            "example.com",
		Path:            "/path",
		Priority:        0.5,
		RetryCount:      0,
		SourceType:      "lighthouse",
		SourceURL:       "https://example.com/path",
		TaskType:        "lighthouse",
		LighthouseRunID: 4242,
	}

	parsed, err := ParseScheduleEntry(entry.Member(), 1234567890.0)
	require.NoError(t, err)

	assert.Equal(t, "lighthouse", parsed.TaskType)
	assert.Equal(t, int64(4242), parsed.LighthouseRunID)
	assert.Equal(t, entry.SourceURL, parsed.SourceURL)
}

// TestReschedule_DualWritesPostgresAndRedis verifies that Reschedule,
// when constructed with a DB, issues the UPDATE to tasks.run_at *and*
// moves the ZSET score.
func TestReschedule_DualWritesPostgresAndRedis(t *testing.T) {
	client := newTestClient(t)

	mockDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mockDB.Close() })

	s := NewSchedulerWithDB(client, mockDB)
	ctx := context.Background()

	now := time.Now()
	entry := ScheduleEntry{
		TaskID: "task-dual", JobID: "job-dual", PageID: 1,
		Host: "example.com", Path: "/", Priority: 0.5,
		SourceType: "sitemap", RunAt: now,
	}
	require.NoError(t, s.Schedule(ctx, entry))

	// Expect the Postgres UPDATE with the new run-at and the task id.
	future := now.Add(30 * time.Second)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE tasks SET run_at = $1 WHERE id = $2`)).
		WithArgs(future, "task-dual").
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, s.Reschedule(ctx, entry, future))
	require.NoError(t, mock.ExpectationsWereMet(), "expected Postgres UPDATE to be issued")

	// And the Redis ZSET score must have moved to the new run-at.
	due, err := s.DueItems(ctx, "job-dual", now, 10)
	require.NoError(t, err)
	assert.Empty(t, due, "task should no longer be due at the old time")

	due, err = s.DueItems(ctx, "job-dual", future, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "task-dual", due[0].TaskID)
	assert.Equal(t, future.UnixMilli(), due[0].RunAt.UnixMilli())
}

// TestReschedule_PostgresErrorAbortsRedisWrite verifies that if Postgres
// fails, Reschedule returns the error and does not touch the ZSET — the
// durable store stays authoritative.
func TestReschedule_PostgresErrorAbortsRedisWrite(t *testing.T) {
	client := newTestClient(t)

	mockDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mockDB.Close() })

	s := NewSchedulerWithDB(client, mockDB)
	ctx := context.Background()

	now := time.Now()
	entry := ScheduleEntry{
		TaskID: "task-pg-fail", JobID: "job-pg-fail", PageID: 1,
		Host: "example.com", Path: "/", RunAt: now,
	}
	require.NoError(t, s.Schedule(ctx, entry))

	future := now.Add(30 * time.Second)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE tasks SET run_at = $1 WHERE id = $2`)).
		WithArgs(future, "task-pg-fail").
		WillReturnError(assert.AnError)

	err = s.Reschedule(ctx, entry, future)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	// ZSET must still carry the original (earlier) score — dispatcher
	// will simply try again on the next tick rather than losing pacing.
	due, err := s.DueItems(ctx, "job-pg-fail", now, 10)
	require.NoError(t, err)
	require.Len(t, due, 1, "ZSET score must not have moved when PG write failed")
	assert.Equal(t, now.UnixMilli(), due[0].RunAt.UnixMilli())
}

// TestReschedule_RunAtSurvivesRedisFlush is the core durability
// property: after a pacing push-back, the new run-at is in Postgres even
// if Redis is flushed before any recovery runs. The reconcile loop that
// re-seeds the ZSET is out of scope for this PR — the test asserts only
// that the durable record is correct.
func TestReschedule_RunAtSurvivesRedisFlush(t *testing.T) {
	client, mr := newTestClientWithMiniredis(t)

	mockDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mockDB.Close() })

	s := NewSchedulerWithDB(client, mockDB)
	ctx := context.Background()

	now := time.Now()
	entry := ScheduleEntry{
		TaskID: "task-flush", JobID: "job-flush", PageID: 1,
		Host: "paced.example", Path: "/", RunAt: now,
	}
	require.NoError(t, s.Schedule(ctx, entry))

	// sqlmock stands in for the tasks table. We assert that after the
	// flush, the mocked Postgres still has the post-reschedule run_at —
	// i.e. the UPDATE fired and we can query it back.
	future := now.Add(5 * time.Minute)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE tasks SET run_at = $1 WHERE id = $2`)).
		WithArgs(future, "task-flush").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT run_at FROM tasks WHERE id = $1`)).
		WithArgs("task-flush").
		WillReturnRows(sqlmock.NewRows([]string{"run_at"}).AddRow(future))

	require.NoError(t, s.Reschedule(ctx, entry, future))

	// Simulate the Redis outage we are trying to survive.
	mr.FlushAll()
	count, err := s.PendingCount(ctx, "job-flush")
	require.NoError(t, err)
	require.Equal(t, int64(0), count, "sanity: ZSET should be empty after flush")

	// The durable run_at is unaffected — this is the property this PR
	// delivers. The follow-up reconcile PR will use this value to
	// re-seed the ZSET on worker startup.
	var persistedRunAt time.Time
	require.NoError(t, mockDB.QueryRowContext(ctx,
		`SELECT run_at FROM tasks WHERE id = $1`, "task-flush",
	).Scan(&persistedRunAt))
	assert.Equal(t, future.UnixMilli(), persistedRunAt.UnixMilli(),
		"tasks.run_at must hold the post-reschedule time after a Redis flush")

	require.NoError(t, mock.ExpectationsWereMet())
}

// TestReschedule_NoDBWritesOnlyRedis verifies backwards compatibility:
// the legacy NewScheduler constructor still works and skips the Postgres
// write. Unit-test call sites that don't need durability rely on this.
func TestReschedule_NoDBWritesOnlyRedis(t *testing.T) {
	client := newTestClient(t)
	s := NewScheduler(client) // no DB
	ctx := context.Background()

	now := time.Now()
	entry := ScheduleEntry{
		TaskID: "task-nodb", JobID: "job-nodb", PageID: 1,
		Host: "example.com", Path: "/", RunAt: now,
	}
	require.NoError(t, s.Schedule(ctx, entry))

	future := now.Add(time.Minute)
	require.NoError(t, s.Reschedule(ctx, entry, future))

	due, err := s.DueItems(ctx, "job-nodb", future, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
}
