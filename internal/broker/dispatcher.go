package broker

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// DispatcherOpts controls the dispatcher's scan behaviour.
type DispatcherOpts struct {
	// ScanInterval is how often the dispatcher sweeps all active job
	// ZSETs for due items. Default 100ms.
	ScanInterval time.Duration

	// BatchSize is the maximum number of ZSET entries fetched per job
	// per scan tick. Default 50.
	BatchSize int64
}

// DefaultDispatcherOpts returns production defaults, optionally
// overridden by environment variables.
func DefaultDispatcherOpts() DispatcherOpts {
	interval := time.Duration(envInt("REDIS_DISPATCH_INTERVAL_MS", 100)) * time.Millisecond
	batch := int64(envInt("REDIS_DISPATCH_BATCH_SIZE", 50))
	return DispatcherOpts{
		ScanInterval: interval,
		BatchSize:    batch,
	}
}

// JobLister returns the set of active job IDs the dispatcher should
// scan. Implementations typically query Postgres.
type JobLister interface {
	ActiveJobIDs(ctx context.Context) ([]string, error)
}

// ConcurrencyChecker determines whether a job has capacity for more
// in-flight tasks.
type ConcurrencyChecker interface {
	// CanDispatch returns true if the job has room for another task.
	CanDispatch(ctx context.Context, jobID string) (bool, error)
}

// Dispatcher is a long-running goroutine that moves due items from
// per-job Redis ZSETs into per-job Redis Streams.
type Dispatcher struct {
	scheduler  *Scheduler
	pacer      *DomainPacer
	counters   *RunningCounters
	client     *Client
	jobLister  JobLister
	concCheck  ConcurrencyChecker
	opts       DispatcherOpts
	logger     zerolog.Logger
}

// NewDispatcher creates a Dispatcher.
func NewDispatcher(
	client *Client,
	scheduler *Scheduler,
	pacer *DomainPacer,
	counters *RunningCounters,
	jobLister JobLister,
	concCheck ConcurrencyChecker,
	opts DispatcherOpts,
	logger zerolog.Logger,
) *Dispatcher {
	return &Dispatcher{
		scheduler: scheduler,
		pacer:     pacer,
		counters:  counters,
		client:    client,
		jobLister: jobLister,
		concCheck: concCheck,
		opts:      opts,
		logger:    logger.With().Str("component", "dispatcher").Logger(),
	}
}

// Run is the dispatcher's main loop. It blocks until ctx is
// cancelled. Start it as a goroutine.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.opts.ScanInterval)
	defer ticker.Stop()

	d.logger.Info().
		Dur("interval", d.opts.ScanInterval).
		Int64("batch", d.opts.BatchSize).
		Msg("dispatcher started")

	for {
		select {
		case <-ctx.Done():
			d.logger.Info().Msg("dispatcher stopping")
			return
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

func (d *Dispatcher) tick(ctx context.Context) {
	jobIDs, err := d.jobLister.ActiveJobIDs(ctx)
	if err != nil {
		d.logger.Error().Err(err).Msg("failed to list active jobs")
		return
	}

	now := time.Now()
	for _, jobID := range jobIDs {
		if ctx.Err() != nil {
			return
		}
		dispatched, err := d.dispatchJob(ctx, jobID, now)
		if err != nil {
			d.logger.Error().Err(err).Str("job_id", jobID).Msg("dispatch error")
		}
		if dispatched > 0 {
			d.logger.Debug().Str("job_id", jobID).Int("dispatched", dispatched).Msg("dispatched tasks")
		}
	}
}

// dispatchJob moves due items from the job's ZSET to its Stream.
// Returns the number of tasks successfully dispatched.
func (d *Dispatcher) dispatchJob(ctx context.Context, jobID string, now time.Time) (int, error) {
	entries, err := d.scheduler.DueItems(ctx, jobID, now, d.opts.BatchSize)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}

	dispatched := 0
	for i := range entries {
		entry := &entries[i]

		// Check job-level concurrency.
		canDispatch, err := d.concCheck.CanDispatch(ctx, jobID)
		if err != nil {
			d.logger.Warn().Err(err).Str("job_id", jobID).Msg("concurrency check failed, skipping batch")
			break
		}
		if !canDispatch {
			// Job at capacity — remaining items stay in ZSET.
			break
		}

		// Check domain pacing.
		domain := entry.Host
		paceResult, err := d.pacer.TryAcquire(ctx, domain)
		if err != nil {
			d.logger.Warn().Err(err).Str("domain", domain).Msg("pacer check failed")
			continue
		}
		if !paceResult.Acquired {
			// Domain in delay window — reschedule with estimated wait.
			newRunAt := now.Add(paceResult.RetryAfter)
			if err := d.scheduler.Reschedule(ctx, jobID, entry.Member(), newRunAt); err != nil {
				d.logger.Warn().Err(err).Str("task_id", entry.TaskID).Msg("reschedule failed")
			}
			continue
		}

		// Publish to stream.
		if err := d.publishToStream(ctx, entry); err != nil {
			// Release the domain permit since we didn't actually start work.
			_ = d.pacer.Release(ctx, domain, jobID, false, false)
			d.logger.Warn().Err(err).Str("task_id", entry.TaskID).Msg("stream publish failed")
			continue
		}

		// Increment running counter.
		if _, err := d.counters.Increment(ctx, jobID); err != nil {
			d.logger.Warn().Err(err).Str("job_id", jobID).Msg("counter increment failed")
		}

		// Increment domain inflight counter.
		if err := d.pacer.IncrementInflight(ctx, domain, jobID); err != nil {
			d.logger.Warn().Err(err).Str("domain", domain).Msg("inflight increment failed")
		}

		// Remove from ZSET.
		if err := d.scheduler.Remove(ctx, jobID, entry.Member()); err != nil {
			d.logger.Warn().Err(err).Str("task_id", entry.TaskID).Msg("ZREM failed after dispatch")
		}

		dispatched++
	}

	return dispatched, nil
}

// publishToStream XADDs the task envelope to the job's stream,
// creating the stream and consumer group lazily if needed.
func (d *Dispatcher) publishToStream(ctx context.Context, entry *ScheduleEntry) error {
	streamKey := StreamKey(entry.JobID)
	groupName := ConsumerGroup(entry.JobID)

	// Ensure consumer group exists (idempotent).
	d.ensureConsumerGroup(ctx, streamKey, groupName)

	args := &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]interface{}{
			"task_id":     entry.TaskID,
			"job_id":      entry.JobID,
			"page_id":     strconv.Itoa(entry.PageID),
			"host":        entry.Host,
			"path":        entry.Path,
			"priority":    fmt.Sprintf("%.4f", entry.Priority),
			"retry_count": strconv.Itoa(entry.RetryCount),
			"source_type": entry.SourceType,
			"source_url":  entry.SourceURL,
		},
	}

	return d.client.rdb.XAdd(ctx, args).Err()
}

// ensureConsumerGroup creates the consumer group if it doesn't exist.
// Errors are logged but not propagated — the group may already exist.
func (d *Dispatcher) ensureConsumerGroup(ctx context.Context, streamKey, groupName string) {
	err := d.client.rdb.XGroupCreateMkStream(ctx, streamKey, groupName, "0").Err()
	if err != nil && !isGroupExistsErr(err) {
		d.logger.Warn().Err(err).Str("stream", streamKey).Msg("failed to create consumer group")
	}
}

func isGroupExistsErr(err error) bool {
	return err != nil && err.Error() == "BUSYGROUP Consumer Group name already exists"
}

// envDuration reads a duration-in-milliseconds from the environment.
func envDuration(key string, defMS int) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return time.Duration(defMS) * time.Millisecond
	}
	ms, err := strconv.Atoi(v)
	if err != nil {
		return time.Duration(defMS) * time.Millisecond
	}
	return time.Duration(ms) * time.Millisecond
}
