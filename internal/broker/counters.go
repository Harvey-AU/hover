package broker

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

// RunningCounters tracks how many tasks are currently in-flight per
// job using a single Redis HASH. This replaces the in-memory atomic
// counters and async DB flush loops in the old worker pool.
type RunningCounters struct {
	client *Client
}

// NewRunningCounters creates a RunningCounters.
func NewRunningCounters(client *Client) *RunningCounters {
	return &RunningCounters{client: client}
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
		if err := rc.client.rdb.HDel(ctx, RunningCountersKey, jobID).Err(); err != nil {
			brokerLog.Warn("failed to clean zero counter entry", "error", err, "job_id", jobID)
		}
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
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			brokerLog.Warn("non-numeric running counter", "job_id", jobID, "value", v, "error", err)
			continue
		}
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
				brokerLog.Error("failed to read running counters for DB sync", "error", err)
				continue
			}
			if err := syncFn(ctx, counts); err != nil {
				brokerLog.Error("failed to sync running counters to DB", "error", err)
			}
		}
	}
}

// DefaultDBSyncFunc returns a DBSyncFunc that updates the jobs table
// running_tasks column using the provided *sql.DB.
func DefaultDBSyncFunc(sqlDB *sql.DB) DBSyncFunc {
	return func(ctx context.Context, counts map[string]int64) error {
		// When counts is empty all jobs have finished — reset any stale
		// positive running_tasks left in Postgres.
		if len(counts) == 0 {
			_, err := sqlDB.ExecContext(ctx,
				`UPDATE jobs SET running_tasks = 0 WHERE running_tasks > 0 AND status IN ('running', 'pending')`)
			return err
		}

		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		// Snapshot PG running_tasks before writing so we can emit the
		// Redis-vs-PG skew per job. A consistent skew > small noise
		// usually means a counter leak (increment/decrement imbalance)
		// or a sync lag spike.
		jobIDsForSnapshot := make([]string, 0, len(counts))
		for jobID := range counts {
			jobIDsForSnapshot = append(jobIDsForSnapshot, jobID)
		}
		priorCounts := make(map[string]int64, len(counts))
		if len(jobIDsForSnapshot) > 0 {
			rows, qerr := tx.QueryContext(ctx,
				`SELECT id, running_tasks FROM jobs WHERE id = ANY($1)`,
				pq.Array(jobIDsForSnapshot))
			if qerr == nil {
				for rows.Next() {
					var id string
					var rt int64
					if scanErr := rows.Scan(&id, &rt); scanErr == nil {
						priorCounts[id] = rt
					}
				}
				_ = rows.Close()
			}
		}

		stmt, err := tx.PrepareContext(ctx,
			`UPDATE jobs SET running_tasks = $1 WHERE id = $2 AND status IN ('running', 'pending')`)
		if err != nil {
			return fmt.Errorf("prepare stmt: %w", err)
		}
		defer stmt.Close()

		jobIDs := make([]string, 0, len(counts))
		for jobID, count := range counts {
			if _, err := stmt.ExecContext(ctx, count, jobID); err != nil {
				return fmt.Errorf("update job %s: %w", jobID, err)
			}
			jobIDs = append(jobIDs, jobID)
			observability.RecordBrokerCounterSyncSkew(ctx, jobID,
				math.Abs(float64(count-priorCounts[jobID])))
		}

		// Zero out any active jobs whose counters are no longer tracked
		// (they finished between sync intervals).
		if len(jobIDs) > 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE jobs SET running_tasks = 0
				 WHERE running_tasks > 0
				   AND status IN ('running', 'pending')
				   AND id != ALL($1)`,
				pq.Array(jobIDs),
			); err != nil {
				return fmt.Errorf("zero stale running_tasks: %w", err)
			}
		}

		return tx.Commit()
	}
}
