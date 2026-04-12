package broker

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// RunningCounters tracks how many tasks are currently in-flight per
// job using a single Redis HASH. This replaces the in-memory atomic
// counters and async DB flush loops in the old worker pool.
type RunningCounters struct {
	client *Client
	logger zerolog.Logger
}

// NewRunningCounters creates a RunningCounters.
func NewRunningCounters(client *Client, logger zerolog.Logger) *RunningCounters {
	return &RunningCounters{
		client: client,
		logger: logger.With().Str("component", "counters").Logger(),
	}
}

// Increment atomically bumps the running count for a job.
// Returns the new count.
func (rc *RunningCounters) Increment(ctx context.Context, jobID string) (int64, error) {
	return rc.client.rdb.HIncrBy(ctx, RunningCountersKey, jobID, 1).Result()
}

// Decrement atomically reduces the running count for a job.
// Returns the new count. Cleans up zero entries.
func (rc *RunningCounters) Decrement(ctx context.Context, jobID string) (int64, error) {
	val, err := rc.client.rdb.HIncrBy(ctx, RunningCountersKey, jobID, -1).Result()
	if err != nil {
		return 0, err
	}
	if val <= 0 {
		// Remove zero/negative entries to keep the hash clean.
		rc.client.rdb.HDel(ctx, RunningCountersKey, jobID)
		return 0, nil
	}
	return val, nil
}

// Get returns the current running count for a single job.
func (rc *RunningCounters) Get(ctx context.Context, jobID string) (int64, error) {
	val, err := rc.client.rdb.HGet(ctx, RunningCountersKey, jobID).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return val, err
}

// GetAll returns the running counts for all jobs.
func (rc *RunningCounters) GetAll(ctx context.Context) (map[string]int64, error) {
	result, err := rc.client.rdb.HGetAll(ctx, RunningCountersKey).Result()
	if err != nil {
		return nil, err
	}

	counts := make(map[string]int64, len(result))
	for jobID, v := range result {
		n, _ := strconv.ParseInt(v, 10, 64)
		counts[jobID] = n
	}
	return counts, nil
}

// Reconcile sets the running counters from an authoritative source
// (typically a Postgres query on startup). This overwrites any
// stale state in Redis.
func (rc *RunningCounters) Reconcile(ctx context.Context, counts map[string]int64) error {
	if len(counts) == 0 {
		// No running tasks — clear the hash.
		return rc.client.rdb.Del(ctx, RunningCountersKey).Err()
	}

	pipe := rc.client.rdb.Pipeline()
	// Delete existing hash and rebuild.
	pipe.Del(ctx, RunningCountersKey)
	fields := make([]interface{}, 0, len(counts)*2)
	for jobID, count := range counts {
		if count > 0 {
			fields = append(fields, jobID, strconv.FormatInt(count, 10))
		}
	}
	if len(fields) > 0 {
		pipe.HSet(ctx, RunningCountersKey, fields...)
	}

	_, err := pipe.Exec(ctx)
	return err
}

// RemoveJob clears the running counter for a specific job
// (e.g. on job completion/cancellation).
func (rc *RunningCounters) RemoveJob(ctx context.Context, jobID string) error {
	return rc.client.rdb.HDel(ctx, RunningCountersKey, jobID).Err()
}

// --- Periodic DB sync ---

// DBSyncFunc is called periodically to write running counters back
// to Postgres for API visibility. The function receives a map of
// jobID -> count.
type DBSyncFunc func(ctx context.Context, counts map[string]int64) error

// StartDBSync runs a background loop that periodically reads all
// running counters from Redis and calls syncFn to persist them to
// Postgres. Blocks until ctx is cancelled.
func (rc *RunningCounters) StartDBSync(ctx context.Context, interval time.Duration, syncFn DBSyncFunc) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			counts, err := rc.GetAll(ctx)
			if err != nil {
				rc.logger.Error().Err(err).Msg("failed to read running counters for DB sync")
				continue
			}
			if err := syncFn(ctx, counts); err != nil {
				rc.logger.Error().Err(err).Msg("failed to sync running counters to DB")
			}
		}
	}
}

// DefaultDBSyncFunc returns a DBSyncFunc that updates the jobs table
// running_tasks column using the provided *sql.DB.
func DefaultDBSyncFunc(sqlDB *sql.DB) DBSyncFunc {
	return func(ctx context.Context, counts map[string]int64) error {
		if len(counts) == 0 {
			return nil
		}

		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		stmt, err := tx.PrepareContext(ctx,
			`UPDATE jobs SET running_tasks = $1 WHERE id = $2 AND status IN ('running', 'pending')`)
		if err != nil {
			return fmt.Errorf("prepare stmt: %w", err)
		}
		defer stmt.Close()

		for jobID, count := range counts {
			if _, err := stmt.ExecContext(ctx, count, jobID); err != nil {
				return fmt.Errorf("update job %s: %w", jobID, err)
			}
		}

		return tx.Commit()
	}
}
