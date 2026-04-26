package broker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
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

	// ParallelJobs caps how many per-job dispatch goroutines run
	// concurrently inside a single tick. Pre-change the tick processed
	// jobs serially, which made the per-task Redis round-trip cost scale
	// O(N_jobs × batch). Under a 100-job workload that serialised the
	// dispatcher into a ~70× backlog. Default 32. Override via
	// REDIS_DISPATCH_PARALLEL_JOBS.
	ParallelJobs int
}

// DefaultDispatcherOpts returns production defaults, optionally
// overridden by environment variables.
func DefaultDispatcherOpts() DispatcherOpts {
	interval := time.Duration(envInt("REDIS_DISPATCH_INTERVAL_MS", 100)) * time.Millisecond
	batch := int64(envInt("REDIS_DISPATCH_BATCH_SIZE", 50))
	parallel := envInt("REDIS_DISPATCH_PARALLEL_JOBS", 32)
	return DispatcherOpts{
		ScanInterval: interval,
		BatchSize:    batch,
		ParallelJobs: parallel,
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

	// groupEnsured remembers per-job consumer groups we have already
	// created. XGroupCreateMkStream is idempotent but still a Redis
	// round-trip; calling it once per dispatched task was the second
	// biggest RTT cost in the serial tick. The map is never cleared
	// intentionally — the keyspace is bounded by the number of jobs the
	// worker has ever dispatched for, which is small in practice and
	// released when the worker process exits.
	groupEnsured sync.Map
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

	// Parallelise per-job dispatch. Each job operates on its own ZSET,
	// Stream, and counter, and dispatchJob's only cross-job dependency
	// is the shared Redis client (goroutine-safe) and pacer (likewise).
	// A bounded semaphore keeps the fan-out predictable under 100+ jobs
	// without opening the pool to unbounded goroutine growth.
	concurrency := d.opts.ParallelJobs
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(jobIDs) {
		concurrency = len(jobIDs)
	}
	if concurrency == 0 {
		return
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
loop:
	for _, jobID := range jobIDs {
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			// `break` inside a select only exits the select; use a
			// label so we actually stop fanning out new work.
			break loop
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			dispatched, err := d.dispatchJob(ctx, id, now)
			if err != nil {
				brokerLog.Error("dispatch error", "error", err, "job_id", id)
			}
			if dispatched > 0 {
				brokerLog.Debug("dispatched tasks", "job_id", id, "dispatched", dispatched)
			}
		}(jobID)
	}
	wg.Wait()
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
			// Redis-only: pacer push-back is ephemeral (hundreds of ms to
			// a few seconds). Doing a synchronous Postgres UPDATE per
			// paced op serialised the dispatcher and collapsed throughput
			// on 2026-04-22 when "paced" dominated at 100 ops/s. If Redis
			// loses state the OutboxSweeper rehydrates from tasks.run_at —
			// a missed push-back just means one extra TryAcquire round-trip.
			newRunAt := now.Add(paceResult.RetryAfter)
			if err := d.scheduler.RescheduleZSet(ctx, *entry, newRunAt); err != nil {
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
//
// Stream selection is driven by entry.TaskType:
//   - "crawl" (default) → StreamKey(jobID): the legacy crawl stream
//     consumed by hover-worker.
//   - "lighthouse"      → LighthouseStreamKey(jobID): consumed by the
//     hover-analysis service. The payload carries lighthouse_run_id so
//     the consumer can update the matching lighthouse_runs row without
//     a Postgres lookup.
func (d *Dispatcher) publishAndRemove(ctx context.Context, entry *ScheduleEntry) error {
	taskType := entry.TaskType
	if taskType == "" {
		taskType = "crawl"
	}

	var (
		streamKey string
		groupName string
	)
	switch taskType {
	case "crawl":
		streamKey = StreamKey(entry.JobID)
		groupName = ConsumerGroup(entry.JobID)
	case "lighthouse":
		// A lighthouse outbox row without lighthouse_run_id is a
		// poison message — the analysis consumer reads run_id straight
		// off the stream payload and has no way to fall back to the
		// DB. Reject early so the bad row stays in the outbox and gets
		// surfaced via the existing dead-letter path rather than
		// silently churning through the stream.
		if entry.LighthouseRunID <= 0 {
			return fmt.Errorf("broker: lighthouse task %s missing lighthouse_run_id", entry.TaskID)
		}
		streamKey = LighthouseStreamKey(entry.JobID)
		groupName = LighthouseConsumerGroup(entry.JobID)
	default:
		// Unknown task_type means a producer drift the dispatcher
		// can't safely route. Don't silently fall through to the crawl
		// stream — that would put lighthouse-shaped work in front of
		// crawl workers and produce hard-to-debug runtime parse errors.
		return fmt.Errorf("broker: unknown task_type %q for task %s", taskType, entry.TaskID)
	}

	// Ensure consumer group exists (idempotent).
	if err := d.ensureConsumerGroup(ctx, streamKey, groupName); err != nil {
		return fmt.Errorf("broker: ensure consumer group %s: %w", groupName, err)
	}

	values := map[string]interface{}{
		"task_id":     entry.TaskID,
		"job_id":      entry.JobID,
		"page_id":     strconv.Itoa(entry.PageID),
		"host":        entry.Host,
		"path":        entry.Path,
		"priority":    fmt.Sprintf("%.4f", entry.Priority),
		"retry_count": strconv.Itoa(entry.RetryCount),
		"source_type": entry.SourceType,
		"source_url":  entry.SourceURL,
		"task_type":   taskType,
	}
	if taskType == "lighthouse" {
		// Already validated non-zero above; emit unconditionally so the
		// consumer never sees a lighthouse message without it.
		values["lighthouse_run_id"] = strconv.FormatInt(entry.LighthouseRunID, 10)
	}

	pipe := d.client.rdb.TxPipeline()
	pipe.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		Values: values,
	})
	pipe.ZRem(ctx, ScheduleKey(entry.JobID), entry.Member())

	_, err := pipe.Exec(ctx)
	return err
}

// ensureConsumerGroup creates the consumer group if it doesn't exist.
// Returns nil if the group already exists or was created successfully.
//
// Results are memoised per group name so the hot per-task path short-circuits
// after the first successful create. XGroupCreateMkStream is idempotent but
// even a BUSYGROUP reply still costs a full Redis RTT — under the serial
// dispatcher that was the second largest per-task cost after the pacer.
func (d *Dispatcher) ensureConsumerGroup(ctx context.Context, streamKey, groupName string) error {
	if _, ok := d.groupEnsured.Load(groupName); ok {
		return nil
	}
	err := d.client.rdb.XGroupCreateMkStream(ctx, streamKey, groupName, "0").Err()
	if err != nil && !isGroupExistsErr(err) {
		return err
	}
	d.groupEnsured.Store(groupName, struct{}{})
	return nil
}

func isGroupExistsErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "BUSYGROUP")
}
