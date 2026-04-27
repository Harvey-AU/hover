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

// ScheduleEntry is encoded as a pipe-delimited ZSET member with the
// run-at unix-ms as the score. TaskType routes to a stream:
// "crawl" → StreamKey, "lighthouse" → LighthouseStreamKey.
type ScheduleEntry struct {
	TaskID          string
	JobID           string
	PageID          int
	Host            string
	Path            string
	Priority        float64
	RetryCount      int
	SourceType      string
	SourceURL       string
	RunAt           time.Time
	TaskType        string
	LighthouseRunID int64
}

func (e ScheduleEntry) Member() string {
	taskType := e.TaskType
	if taskType == "" {
		taskType = "crawl"
	}
	return FormatScheduleEntry(
		e.TaskID, e.JobID, e.PageID,
		e.Host, e.Path, e.Priority,
		e.RetryCount, e.SourceType, e.SourceURL,
		taskType, e.LighthouseRunID,
	)
}

func (e ScheduleEntry) Score() float64 {
	return float64(e.RunAt.UnixMilli())
}

// ParseScheduleEntry accepts both the 9-field legacy format and the
// 11-field current format so a rolling deploy drains without a flush.
func ParseScheduleEntry(member string, score float64) (ScheduleEntry, error) {
	parts := strings.SplitN(member, "|", 11)
	if len(parts) != 9 && len(parts) != 11 {
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

	taskType := "crawl"
	var lighthouseRunID int64
	if len(parts) == 11 {
		taskType = parts[9]
		if taskType == "" {
			taskType = "crawl"
		}
		if parts[10] != "" {
			lighthouseRunID, err = strconv.ParseInt(parts[10], 10, 64)
			if err != nil {
				return ScheduleEntry{}, fmt.Errorf("broker: bad lighthouse_run_id in entry: %w", err)
			}
		}
	}

	return ScheduleEntry{
		TaskID:          parts[0],
		JobID:           parts[1],
		PageID:          pageID,
		Host:            parts[3],
		Path:            parts[4],
		Priority:        priority,
		RetryCount:      retryCount,
		SourceType:      parts[7],
		SourceURL:       parts[8],
		RunAt:           time.UnixMilli(int64(score)),
		TaskType:        taskType,
		LighthouseRunID: lighthouseRunID,
	}, nil
}

// runAtExecer is *sql.DB's narrow subset used by Reschedule, behind
// an interface so tests can inject sqlmock.
type runAtExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Scheduler manages delayed scheduling via Redis sorted sets.
// NewSchedulerWithDB enables dual-write of Reschedule's run-at into
// tasks.run_at so pacing push-backs survive a Redis flush.
type Scheduler struct {
	client *Client
	db     runAtExecer
}

// NewScheduler creates a Scheduler without Postgres mirroring.
func NewScheduler(client *Client) *Scheduler {
	return &Scheduler{client: client}
}

func NewSchedulerWithDB(client *Client, db *sql.DB) *Scheduler {
	return &Scheduler{client: client, db: db}
}

func (s *Scheduler) Schedule(ctx context.Context, entry ScheduleEntry) error {
	key := ScheduleKey(entry.JobID)
	z := redis.Z{Score: entry.Score(), Member: entry.Member()}

	if err := s.client.rdb.ZAdd(ctx, key, z).Err(); err != nil {
		return fmt.Errorf("broker: schedule task %s: %w", entry.TaskID, err)
	}
	return nil
}

// BatchError is returned by ScheduleBatch on partial pipeline
// failure. Type-assert via errors.As to retry FailedIndices.
type BatchError struct {
	FailedIndices []int
	Total         int
	Err           error
}

func (e *BatchError) Error() string {
	return fmt.Sprintf("broker: %d of %d schedule entries failed: %v",
		len(e.FailedIndices), e.Total, e.Err)
}

func (e *BatchError) Unwrap() error { return e.Err }

// ScheduleBatch returns *BatchError when the pipeline ran but
// individual ZADDs failed; a non-BatchError means the pipeline itself
// failed and all entries must be treated as failed.
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

	var (
		failed   []int
		firstErr error
	)
	for i, cmd := range cmds {
		if cmdErr := cmd.Err(); cmdErr != nil {
			failed = append(failed, i)
			if firstErr == nil {
				firstErr = cmdErr
			}
		}
	}
	if len(failed) == 0 {
		return nil
	}
	brokerLog.Warn("partial schedule batch failure",
		"failed", len(failed), "total", len(entries), "first_error", firstErr)
	return &BatchError{FailedIndices: failed, Total: len(entries), Err: firstErr}
}

func (s *Scheduler) Remove(ctx context.Context, jobID, member string) error {
	return s.client.rdb.ZRem(ctx, ScheduleKey(jobID), member).Err()
}

// ScheduleAndAck atomically enqueues the retry and ACKs the original
// in one MULTI/EXEC. Two-step would let XAUTOCLAIM redeliver a stuck
// PEL entry and double-crawl if Ack failed after Schedule succeeded.
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

// DueItems returns up to limit entries with score ≤ now. Items are
// not removed — caller ZREMs after successful dispatch.
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

// Reschedule pushes an existing entry's run-at later. With *sql.DB
// configured it dual-writes Postgres first so a crash between writes
// leaves the durable store with the newer time.
//
// TODO(run-at-reconcile): once tasks have a dedicated 'scheduled'
// status, add a startup sweep that re-seeds ZSET from tasks.run_at
// for scheduled rows missing a ZSET member.
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

// RescheduleZSet is Reschedule without the Postgres mirror, for the
// hot pacer-pushback path. The synchronous UPDATE backed up the ZSET
// to 80k+ entries at 100 paced ops/sec on 2026-04-22. Safe because
// OutboxSweeper rehydrates from tasks.run_at if Redis loses state.
func (s *Scheduler) RescheduleZSet(ctx context.Context, entry ScheduleEntry, newRunAt time.Time) error {
	return s.rescheduleZSet(ctx, entry, newRunAt)
}

func (s *Scheduler) rescheduleZSet(ctx context.Context, entry ScheduleEntry, newRunAt time.Time) error {
	key := ScheduleKey(entry.JobID)
	score := float64(newRunAt.UnixMilli())

	// ZADD XX: update only if member exists.
	return s.client.rdb.ZAddArgs(ctx, key, redis.ZAddArgs{
		XX:      true,
		Members: []redis.Z{{Score: score, Member: entry.Member()}},
	}).Err()
}

func (s *Scheduler) PendingCount(ctx context.Context, jobID string) (int64, error) {
	return s.client.rdb.ZCard(ctx, ScheduleKey(jobID)).Result()
}

func (s *Scheduler) RemoveJobSchedule(ctx context.Context, jobID string) error {
	return s.client.rdb.Del(ctx, ScheduleKey(jobID)).Err()
}
