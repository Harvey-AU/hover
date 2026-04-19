package broker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ScheduleEntry contains the data needed to schedule a task into
// the Redis ZSET. The entry is stored as a pipe-delimited member
// string; the score is the earliest unix-millisecond timestamp at
// which the task may run.
type ScheduleEntry struct {
	TaskID     string
	JobID      string
	PageID     int
	Host       string
	Path       string
	Priority   float64
	RetryCount int
	SourceType string
	SourceURL  string
	RunAt      time.Time
}

// Member returns the pipe-delimited string stored in the ZSET.
func (e ScheduleEntry) Member() string {
	return FormatScheduleEntry(
		e.TaskID, e.JobID, e.PageID,
		e.Host, e.Path, e.Priority,
		e.RetryCount, e.SourceType, e.SourceURL,
	)
}

// Score returns the ZSET score (unix milliseconds).
func (e ScheduleEntry) Score() float64 {
	return float64(e.RunAt.UnixMilli())
}

// ParseScheduleEntry reconstructs a ScheduleEntry from its
// pipe-delimited ZSET member string plus the score.
func ParseScheduleEntry(member string, score float64) (ScheduleEntry, error) {
	parts := strings.SplitN(member, "|", 9)
	if len(parts) != 9 {
		return ScheduleEntry{}, fmt.Errorf("broker: malformed schedule entry: %q", member)
	}

	pageID, err := strconv.Atoi(parts[2])
	if err != nil {
		return ScheduleEntry{}, fmt.Errorf("broker: bad page_id in entry: %w", err)
	}
	priority, err := strconv.ParseFloat(parts[5], 64)
	if err != nil {
		return ScheduleEntry{}, fmt.Errorf("broker: bad priority in entry: %w", err)
	}
	retryCount, err := strconv.Atoi(parts[6])
	if err != nil {
		return ScheduleEntry{}, fmt.Errorf("broker: bad retry_count in entry: %w", err)
	}

	return ScheduleEntry{
		TaskID:     parts[0],
		JobID:      parts[1],
		PageID:     pageID,
		Host:       parts[3],
		Path:       parts[4],
		Priority:   priority,
		RetryCount: retryCount,
		SourceType: parts[7],
		SourceURL:  parts[8],
		RunAt:      time.UnixMilli(int64(score)),
	}, nil
}

// Scheduler manages delayed task scheduling via Redis sorted sets.
type Scheduler struct {
	client *Client
}

// NewScheduler creates a Scheduler.
func NewScheduler(client *Client) *Scheduler {
	return &Scheduler{client: client}
}

// Schedule adds a single task to the job's ZSET.
func (s *Scheduler) Schedule(ctx context.Context, entry ScheduleEntry) error {
	key := ScheduleKey(entry.JobID)
	z := redis.Z{Score: entry.Score(), Member: entry.Member()}

	if err := s.client.rdb.ZAdd(ctx, key, z).Err(); err != nil {
		return fmt.Errorf("broker: schedule task %s: %w", entry.TaskID, err)
	}
	return nil
}

// ScheduleBatch adds multiple tasks to their respective job ZSETs
// using a pipeline for efficiency.
func (s *Scheduler) ScheduleBatch(ctx context.Context, entries []ScheduleEntry) error {
	if len(entries) == 0 {
		return nil
	}

	pipe := s.client.rdb.Pipeline()
	for i := range entries {
		key := ScheduleKey(entries[i].JobID)
		z := redis.Z{Score: entries[i].Score(), Member: entries[i].Member()}
		pipe.ZAdd(ctx, key, z)
	}

	cmds, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("broker: schedule batch (%d entries): %w", len(entries), err)
	}

	var errs int
	for _, cmd := range cmds {
		if cmd.Err() != nil {
			errs++
		}
	}
	if errs > 0 {
		brokerLog.Warn("partial schedule batch failure", "failed", errs, "total", len(entries))
		return fmt.Errorf("broker: %d of %d schedule entries failed", errs, len(entries))
	}
	return nil
}

// Remove deletes a task from the job's ZSET (e.g. on cancellation).
func (s *Scheduler) Remove(ctx context.Context, jobID, member string) error {
	return s.client.rdb.ZRem(ctx, ScheduleKey(jobID), member).Err()
}

// DueItems returns up to limit entries whose score is <= now from the
// given job's ZSET. Items are returned but not removed — the caller
// is responsible for ZREM after successful dispatch.
func (s *Scheduler) DueItems(ctx context.Context, jobID string, now time.Time, limit int64) ([]ScheduleEntry, error) {
	key := ScheduleKey(jobID)
	nowMS := fmt.Sprintf("%d", now.UnixMilli())

	results, err := s.client.rdb.ZRangeByScoreWithScores(ctx, key, &redis.ZRangeBy{
		Min:   "-inf",
		Max:   nowMS,
		Count: limit,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("broker: due items for job %s: %w", jobID, err)
	}

	entries := make([]ScheduleEntry, 0, len(results))
	for _, z := range results {
		member, ok := z.Member.(string)
		if !ok {
			brokerLog.Warn("removing non-string ZSET member", "member", z.Member)
			if memberStr, stringable := z.Member.(fmt.Stringer); stringable {
				if remErr := s.client.rdb.ZRem(ctx, key, memberStr.String()).Err(); remErr != nil {
					brokerLog.Warn("failed to ZREM non-string member", "error", remErr)
				}
			}
			continue
		}
		entry, err := ParseScheduleEntry(member, z.Score)
		if err != nil {
			brokerLog.Warn("removing malformed schedule entry", "error", err, "member", member)
			if remErr := s.client.rdb.ZRem(ctx, key, member).Err(); remErr != nil {
				brokerLog.Warn("failed to ZREM malformed entry", "error", remErr, "member", member)
			}
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// Reschedule updates the score (run-at time) for an existing entry
// in the ZSET. This is used when the dispatcher cannot dispatch yet
// (domain pacing, concurrency limit) and needs to push the item back.
func (s *Scheduler) Reschedule(ctx context.Context, jobID string, member string, newRunAt time.Time) error {
	key := ScheduleKey(jobID)
	score := float64(newRunAt.UnixMilli())

	// ZADD XX updates only if the member already exists.
	return s.client.rdb.ZAddArgs(ctx, key, redis.ZAddArgs{
		XX:      true,
		Members: []redis.Z{{Score: score, Member: member}},
	}).Err()
}

// PendingCount returns the total number of scheduled items for a job.
func (s *Scheduler) PendingCount(ctx context.Context, jobID string) (int64, error) {
	return s.client.rdb.ZCard(ctx, ScheduleKey(jobID)).Result()
}

// RemoveJobSchedule deletes the entire ZSET for a job
// (used on job cancellation or completion).
func (s *Scheduler) RemoveJobSchedule(ctx context.Context, jobID string) error {
	return s.client.rdb.Del(ctx, ScheduleKey(jobID)).Err()
}
