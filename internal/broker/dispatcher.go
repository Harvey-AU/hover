package broker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/redis/go-redis/v9"
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
	scheduler *Scheduler
	pacer     *DomainPacer
	counters  *RunningCounters
	client    *Client
	jobLister JobLister
	concCheck ConcurrencyChecker
	opts      DispatcherOpts
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
) *Dispatcher {
	return &Dispatcher{
		scheduler: scheduler,
		pacer:     pacer,
		counters:  counters,
		client:    client,
		jobLister: jobLister,
		concCheck: concCheck,
		opts:      opts,
	}
}

// Run is the dispatcher's main loop. It blocks until ctx is
// cancelled. Start it as a goroutine.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.opts.ScanInterval)
	defer ticker.Stop()

	brokerLog.Info("dispatcher started",
		"interval", d.opts.ScanInterval,
		"batch", d.opts.BatchSize,
	)

	for {
		select {
		case <-ctx.Done():
			brokerLog.Info("dispatcher stopping")
			return
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

func (d *Dispatcher) tick(ctx context.Context) {
	jobIDs, err := d.jobLister.ActiveJobIDs(ctx)
	if err != nil {
		brokerLog.Error("failed to list active jobs", "error", err)
		return
	}

	now := time.Now()
	for _, jobID := range jobIDs {
		if ctx.Err() != nil {
			return
		}
		dispatched, err := d.dispatchJob(ctx, jobID, now)
		if err != nil {
			brokerLog.Error("dispatch error", "error", err, "job_id", jobID)
		}
		if dispatched > 0 {
			brokerLog.Debug("dispatched tasks", "job_id", jobID, "dispatched", dispatched)
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
			brokerLog.Warn("concurrency check failed, skipping batch", "error", err, "job_id", jobID)
			observability.RecordBrokerDispatch(ctx, jobID, "err")
			break
		}
		if !canDispatch {
			// Job at capacity — remaining items stay in ZSET.
			observability.RecordBrokerDispatch(ctx, jobID, "capacity")
			break
		}

		// Check domain pacing.
		domain := entry.Host
		paceResult, err := d.pacer.TryAcquire(ctx, domain)
		if err != nil {
			brokerLog.Warn("pacer check failed", "error", err, "domain", domain)
			observability.RecordBrokerDispatch(ctx, jobID, "err")
			continue
		}
		if !paceResult.Acquired {
			// Domain in delay window — reschedule with estimated wait.
			// Reschedule dual-writes the new run-at to Postgres so the
			// pacing push-back survives a Redis flush.
			newRunAt := now.Add(paceResult.RetryAfter)
			if err := d.scheduler.Reschedule(ctx, *entry, newRunAt); err != nil {
				brokerLog.Warn("reschedule failed", "error", err, "task_id", entry.TaskID)
			}
			observability.RecordBrokerDispatch(ctx, jobID, "paced")
			observability.RecordBrokerPacerPushback(ctx, domain, "gate")
			continue
		}

		// Publish to stream and remove from ZSET atomically via pipeline.
		// On failure, the TryAcquire gate is left to expire on its own; we
		// must not call Release here because IncrementInflight has not run
		// yet, so Release would decrement a counter that was never incremented.
		if err := d.publishAndRemove(ctx, entry); err != nil {
			brokerLog.Warn("stream publish+remove failed", "error", err, "task_id", entry.TaskID)
			observability.RecordBrokerDispatch(ctx, jobID, "err")
			continue
		}

		// Increment running counter.
		if _, err := d.counters.Increment(ctx, jobID); err != nil {
			brokerLog.Warn("counter increment failed", "error", err, "job_id", jobID)
		}

		// Increment domain inflight counter.
		if err := d.pacer.IncrementInflight(ctx, domain, jobID); err != nil {
			brokerLog.Warn("inflight increment failed", "error", err, "domain", domain)
		}

		observability.RecordBrokerDispatch(ctx, jobID, "ok")
		dispatched++
	}

	return dispatched, nil
}

// publishAndRemove atomically XADDs the task to the job's stream
// and ZREMs it from the schedule ZSET in a single Redis pipeline,
// preventing double-dispatch if either operation were to fail alone.
func (d *Dispatcher) publishAndRemove(ctx context.Context, entry *ScheduleEntry) error {
	streamKey := StreamKey(entry.JobID)
	groupName := ConsumerGroup(entry.JobID)

	// Ensure consumer group exists (idempotent).
	if err := d.ensureConsumerGroup(ctx, streamKey, groupName); err != nil {
		return fmt.Errorf("broker: ensure consumer group %s: %w", groupName, err)
	}

	pipe := d.client.rdb.TxPipeline()
	pipe.XAdd(ctx, &redis.XAddArgs{
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
	})
	pipe.ZRem(ctx, ScheduleKey(entry.JobID), entry.Member())

	_, err := pipe.Exec(ctx)
	return err
}

// ensureConsumerGroup creates the consumer group if it doesn't exist.
// Returns nil if the group already exists or was created successfully.
func (d *Dispatcher) ensureConsumerGroup(ctx context.Context, streamKey, groupName string) error {
	err := d.client.rdb.XGroupCreateMkStream(ctx, streamKey, groupName, "0").Err()
	if err != nil && !isGroupExistsErr(err) {
		return err
	}
	return nil
}

func isGroupExistsErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "BUSYGROUP")
}
