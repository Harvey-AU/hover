package broker

import (
	"context"
	"database/sql"
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

// runAtExecer is the narrow subset of *sql.DB used by Reschedule to
// mirror the new run-at time into Postgres. It is an interface so tests
// can inject sqlmock.
type runAtExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Scheduler manages delayed task scheduling via Redis sorted sets.
//
// When constructed via NewSchedulerWithDB, Reschedule dual-writes the new
// run-at time to the tasks.run_at column so pacing push-backs survive a
// Redis flush. The db dependency is optional (nil in unit tests that
// don't exercise the durability path).
type Scheduler struct {
	client *Client
	db     runAtExecer
}

// NewScheduler creates a Scheduler without Postgres mirroring. Reschedule
// writes only to the Redis ZSET. Suitable for tests that don't need the
// durability path.
func NewScheduler(client *Client) *Scheduler {
	return &Scheduler{client: client}
}

// NewSchedulerWithDB creates a Scheduler that mirrors Reschedule run-at
// updates to the tasks.run_at column. This is the production constructor.
func NewSchedulerWithDB(client *Client, db *sql.DB) *Scheduler {
	return &Scheduler{client: client, db: db}
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

// ScheduleAndAck atomically enqueues a retry into the job's ZSET and
// acknowledges (removes) the original stream message in a single
// Redis MULTI/EXEC. This prevents the two-step race where Schedule
// succeeds but Ack fails — which would leave the retry queued while
// the original stays in the PEL, allowing XAUTOCLAIM to redeliver it
// and causing a duplicate crawl.
//
// Redis MULTI/EXEC is atomic on a single server: either both
// operations apply or neither does. The caller receives a single
// error to act on.
func (s *Scheduler) ScheduleAndAck(ctx context.Context, entry ScheduleEntry, ackJobID, messageID string) error {
	schedKey := ScheduleKey(entry.JobID)
	streamKey := StreamKey(ackJobID)
	groupName := ConsumerGroup(ackJobID)
	z := redis.Z{Score: entry.Score(), Member: entry.Member()}

	pipe := s.client.rdb.TxPipeline()
	zaddCmd := pipe.ZAdd(ctx, schedKey, z)
	xackCmd := pipe.XAck(ctx, streamKey, groupName, messageID)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("broker: schedule-and-ack task %s: %w", entry.TaskID, err)
	}
	if err := zaddCmd.Err(); err != nil {
		return fmt.Errorf("broker: schedule-and-ack ZADD %s: %w", entry.TaskID, err)
	}
	if err := xackCmd.Err(); err != nil {
		return fmt.Errorf("broker: schedule-and-ack XACK %s: %w", messageID, err)
	}
	return nil
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

// Reschedule updates the score (run-at time) for an existing entry in
// the ZSET. Used when the dispatcher cannot dispatch yet (domain pacing,
// concurrency limit) and needs to push the item back.
//
// When the Scheduler was constructed with a *sql.DB, the new run-at is
// also written to the tasks.run_at column. Postgres is written first so
// that if the process crashes or Redis is flushed between the two
// writes, the durable store holds the newer time; the next dispatch
// attempt will see the correct pacing window.
//
// TODO(run-at-reconcile): once the task-lifecycle redesign lands (a
// dedicated 'scheduled' status flipped by the dispatcher on XADD), add a
// startup sweep that re-seeds the ZSET from tasks.run_at for
// status='scheduled' rows with no ZSET member. Blocked on the outbox PR.
func (s *Scheduler) Reschedule(ctx context.Context, entry ScheduleEntry, newRunAt time.Time) error {
	if s.db != nil {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE tasks SET run_at = $1 WHERE id = $2`,
			newRunAt, entry.TaskID,
		); err != nil {
			return fmt.Errorf("broker: persist run_at for task %s: %w", entry.TaskID, err)
		}
	}

	return s.rescheduleZSet(ctx, entry, newRunAt)
}

// RescheduleZSet is Reschedule without the Postgres mirror. Use this on
// the hot pacer push-back path where every dispatcher iteration would
// otherwise issue a synchronous UPDATE — at 100 paced ops/sec the
// per-op DB latency stalled the single dispatcher goroutine and the ZSET
// backed up to 80k+ entries on 2026-04-22.
//
// Safety: pacer push-back is ephemeral (seconds, not minutes). If Redis
// loses the ZSET between a push-back and dispatch, OutboxSweeper rehydrates
// from tasks.run_at. The authoritative run_at in Postgres is written on
// initial enqueue; a missed push-back just means the task is re-attempted
// slightly sooner than its pacer gate allows — the next TryAcquire will
// push it back again. No task is lost.
func (s *Scheduler) RescheduleZSet(ctx context.Context, entry ScheduleEntry, newRunAt time.Time) error {
	return s.rescheduleZSet(ctx, entry, newRunAt)
}

func (s *Scheduler) rescheduleZSet(ctx context.Context, entry ScheduleEntry, newRunAt time.Time) error {
	key := ScheduleKey(entry.JobID)
	score := float64(newRunAt.UnixMilli())

	// ZADD XX updates only if the member already exists.
	return s.client.rdb.ZAddArgs(ctx, key, redis.ZAddArgs{
		XX:      true,
		Members: []redis.Z{{Score: score, Member: entry.Member()}},
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
