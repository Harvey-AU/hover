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

	// StuckThreshold is how long a single job may sit blocked on the
	// CanDispatch capacity gate (with due work in its ZSET) before the
	// dispatcher fires a self-heal counter reconcile. Default 30s,
	// overridable via REDIS_DISPATCH_STUCK_THRESHOLD_S. Self-heal is
	// rate-limited to one trigger per 2× this value per job so a
	// genuinely-at-capacity job can't burn Redis with reconciles.
	StuckThreshold time.Duration
}

// DefaultDispatcherOpts returns production defaults, optionally
// overridden by environment variables.
func DefaultDispatcherOpts() DispatcherOpts {
	interval := time.Duration(envInt("REDIS_DISPATCH_INTERVAL_MS", 100)) * time.Millisecond
	batch := int64(envInt("REDIS_DISPATCH_BATCH_SIZE", 50))
	parallel := envInt("REDIS_DISPATCH_PARALLEL_JOBS", 32)
	stuck := time.Duration(envInt("REDIS_DISPATCH_STUCK_THRESHOLD_S", 30)) * time.Second
	return DispatcherOpts{
		ScanInterval:   interval,
		BatchSize:      batch,
		ParallelJobs:   parallel,
		StuckThreshold: stuck,
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

// Reconciler triggers an immediate per-job counter reconciliation
// from the authoritative Redis PEL. The dispatcher invokes this as a
// last-resort self-heal when CanDispatch keeps refusing dispatch
// despite due work sitting in the ZSET — the symptom of a drifted
// `hover:running` counter pinning a job at its concurrency cap.
//
// Implementations must be safe for concurrent invocation and should
// debounce a flood of triggers down to at most one in-flight
// reconcile (the StreamWorkerPool implementation uses a TryLock).
type Reconciler interface {
	TriggerReconcile(ctx context.Context)
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

	// firstDispatched remembers jobs we have already fired the
	// onFirstDispatch hook for in this dispatcher's lifetime. Resets on
	// process restart, but the hook implementation must be idempotent
	// (typical: a guarded UPDATE that no-ops once status moves past
	// pending) so re-firing on restart costs at most one cheap UPDATE.
	//
	// Only populated on hook success — a transient hook failure leaves
	// the entry absent so the next dispatch tick retries. Without that,
	// one bad UPDATE could leave a job stranded in pending for the
	// dispatcher's lifetime.
	firstDispatched sync.Map

	// onFirstDispatch is invoked the first time dispatchJob successfully
	// publishes a task for a given jobID. Optional: nil disables the
	// hook. Used by the worker to flip status pending → running so the
	// status pill reflects reality without requiring every potential
	// entry point to coordinate the transition.
	//
	// Returning a non-nil error signals a transient failure: the
	// dispatcher does NOT mark the job as "first-dispatched", so the
	// next dispatch tick will retry. Implementations should make this
	// idempotent — the hook may fire repeatedly for the same job until
	// it succeeds.
	onFirstDispatch func(ctx context.Context, jobID string) error

	// reconciler, when non-nil, is invoked as a self-heal when a job
	// has been blocked on the capacity gate for longer than
	// opts.StuckThreshold while its ZSET still has due work. Optional:
	// nil disables the self-heal path entirely (covers tests and any
	// embedded scenarios where the worker pool isn't wired in).
	reconciler Reconciler

	// stuckMu guards stuckSince and lastTrigger. Held only for the few
	// map-mutating ops in maybeTriggerReconcile and clearStuck — the
	// reconciler call itself runs outside the lock.
	stuckMu sync.Mutex

	// stuckSince records, per jobID, the timestamp of the first
	// dispatcher tick where the capacity gate fired with due work in
	// the ZSET. Cleared the moment that job dispatches successfully or
	// runs a tick with no due items, so transient at-capacity bursts
	// (the normal path for a healthy fast job) never trip the heuristic.
	stuckSince map[string]time.Time

	// lastTrigger records, per jobID, when we last fired the reconciler
	// for that job. Persisted across stuck/recover cycles so a job
	// flapping between stuck and not-stuck can't drive reconcile faster
	// than the rate-limit window allows.
	lastTrigger map[string]time.Time
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

// SetOnFirstDispatch installs a callback fired the first time
// dispatchJob successfully publishes a task for a given jobID in this
// dispatcher's lifetime. The hook must be idempotent — see
// firstDispatched on Dispatcher. Returning a non-nil error from the
// hook causes the dispatcher to retry on the next dispatch.
func (d *Dispatcher) SetOnFirstDispatch(fn func(ctx context.Context, jobID string) error) {
	d.onFirstDispatch = fn
}

// SetReconciler installs the self-heal target invoked when a single
// job sits blocked on the CanDispatch capacity gate for longer than
// opts.StuckThreshold while its ZSET still has due work. Pass nil to
// disable the self-heal path; nil is the default and is tolerated
// throughout the dispatcher hot path.
func (d *Dispatcher) SetReconciler(r Reconciler) {
	d.reconciler = r
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
		// No due work this tick — whatever stuck state we'd been
		// tracking is no longer load-bearing. Clearing here matters
		// when a job's tasks all reschedule into the future: we must
		// not carry the old stuck-since timestamp into the next time
		// items come due, otherwise the heuristic would treat the
		// wall-clock gap as continuous capacity-blocked time.
		d.clearStuck(jobID)
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
			// Job at capacity — remaining items stay in ZSET. We're
			// inside the entries loop, so we already know there is at
			// least one due task right now: that's the precondition
			// for the self-heal heuristic. A genuinely-busy job clears
			// the stuck-since timestamp on its very next successful
			// dispatch, so the heuristic only fires when the gate
			// stays shut for opts.StuckThreshold of wall-clock time.
			observability.RecordBrokerDispatch(ctx, jobID, "capacity")
			d.maybeTriggerReconcile(ctx, jobID, now)
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

		// First successful publish for this job in this dispatcher's
		// lifetime → flip status pending → running. We Load before
		// calling the hook and Store only after success: a transient
		// failure must not be memoised, otherwise one bad UPDATE could
		// leave a job stranded in pending for the dispatcher's whole
		// lifetime. Worst case under load is a few duplicate UPDATEs
		// while the database recovers; the hook is guarded so the
		// extra UPDATEs are no-ops once a peer wins the race.
		if d.onFirstDispatch != nil {
			if _, seen := d.firstDispatched.Load(jobID); !seen {
				if err := d.onFirstDispatch(ctx, jobID); err != nil {
					brokerLog.Warn("onFirstDispatch failed",
						"error", err, "job_id", jobID)
				} else {
					d.firstDispatched.Store(jobID, struct{}{})
				}
			}
		}

		observability.RecordBrokerDispatch(ctx, jobID, "ok")
		dispatched++
	}

	if dispatched > 0 {
		// Any forward progress for this job invalidates the stuck
		// hypothesis. We deliberately do not clear lastTrigger here —
		// that's the rate-limit memory and must persist across
		// stuck/recover cycles to stop a flapping job from driving
		// reconcile faster than the rate-limit window allows.
		d.clearStuck(jobID)
	}

	return dispatched, nil
}

// maybeTriggerReconcile updates the per-job stuck timestamp and, if
// the job has been stuck for opts.StuckThreshold and we're outside
// the per-job rate-limit window, invokes the installed Reconciler.
//
// This is the dispatcher's only self-heal lever: PR #362 closed the
// known sources of `hover:running` drift, but the cost of an unknown
// future drift class is a job that throttles to ~0% throughput while
// peers run at full speed. Triggering an immediate reconcile from
// the authoritative PEL re-aligns the counter with reality without
// waiting up to 120s for the periodic reconcileLoop.
func (d *Dispatcher) maybeTriggerReconcile(ctx context.Context, jobID string, now time.Time) {
	if d.reconciler == nil {
		return
	}

	threshold := d.opts.StuckThreshold
	if threshold <= 0 {
		// Self-heal explicitly disabled by configuration. Don't even
		// record a stuck timestamp — keeps the maps tidy when the
		// feature is off.
		return
	}

	d.stuckMu.Lock()
	if d.stuckSince == nil {
		d.stuckSince = make(map[string]time.Time)
	}
	if d.lastTrigger == nil {
		d.lastTrigger = make(map[string]time.Time)
	}

	first, ok := d.stuckSince[jobID]
	if !ok {
		d.stuckSince[jobID] = now
		d.stuckMu.Unlock()
		return
	}
	if now.Sub(first) < threshold {
		d.stuckMu.Unlock()
		return
	}

	rateLimit := 2 * threshold
	if last, seen := d.lastTrigger[jobID]; seen && now.Sub(last) < rateLimit {
		// Still stuck, but we already nudged the reconciler recently.
		// Letting the next nudge wait until the rate-limit window
		// expires keeps the cost predictable for a job that's just
		// genuinely at its concurrency cap.
		d.stuckMu.Unlock()
		return
	}
	d.lastTrigger[jobID] = now
	d.stuckMu.Unlock()

	brokerLog.Warn("dispatcher self-heal: triggering counter reconcile",
		"job_id", jobID, "stuck_for", now.Sub(first).String())
	d.reconciler.TriggerReconcile(ctx)
}

// clearStuck removes the per-job stuck timestamp. lastTrigger is
// intentionally preserved so the rate limit survives a brief
// recovery window.
func (d *Dispatcher) clearStuck(jobID string) {
	d.stuckMu.Lock()
	defer d.stuckMu.Unlock()
	if d.stuckSince != nil {
		delete(d.stuckSince, jobID)
	}
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
