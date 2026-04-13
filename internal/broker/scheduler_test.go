package broker

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T) (*Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &Client{rdb: rdb, logger: zerolog.Nop()}, mr
}

func TestSchedule_SingleEntry(t *testing.T) {
	client, _ := newTestClient(t)
	s := NewScheduler(client, zerolog.Nop())
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
	client, _ := newTestClient(t)
	s := NewScheduler(client, zerolog.Nop())
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

func TestDueItems(t *testing.T) {
	client, _ := newTestClient(t)
	s := NewScheduler(client, zerolog.Nop())
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
	client, _ := newTestClient(t)
	s := NewScheduler(client, zerolog.Nop())
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
	err = s.Reschedule(ctx, "j1", entry.Member(), future)
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
