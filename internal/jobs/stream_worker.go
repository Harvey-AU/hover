package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Harvey-AU/hover/internal/broker"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/observability"
	"golang.org/x/sync/singleflight"
)

// defaultActiveJobsLimit is the fallback cap on the number of active jobs
// scanned by the dispatcher per refresh tick. Configurable via the
// STREAM_ACTIVE_JOBS_LIMIT environment variable (explicit override), or
// derived from GNH_MAX_WORKERS × GNH_PENDING_ADMISSION_WORKER_FACTOR with
// a floor of GNH_PENDING_ADMISSION_LIMIT_MIN — matching pre-merge behaviour.
const (
	defaultActiveJobsLimit              = 200
	defaultPendingAdmissionLimitMin     = 250
	defaultPendingAdmissionWorkerFactor = 3
)

// activeJobsLimit returns the configured limit for refreshActiveJobs.
//
// Resolution order:
//  1. STREAM_ACTIVE_JOBS_LIMIT (explicit override — keeps a single-dial
//     escape hatch for ops).
//  2. max(GNH_MAX_WORKERS × GNH_PENDING_ADMISSION_WORKER_FACTOR,
//     GNH_PENDING_ADMISSION_LIMIT_MIN) — the pre-merge formula, so the
//     admission breadth scales with the worker pool by default. At prod
//     sizing (GNH_MAX_WORKERS=130, factor=3, min=250) this yields 390.
//  3. defaultActiveJobsLimit (200) if nothing else is configured.
func activeJobsLimit() int {
	if v := strings.TrimSpace(os.Getenv("STREAM_ACTIVE_JOBS_LIMIT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		jobsLog.Warn("invalid STREAM_ACTIVE_JOBS_LIMIT, ignoring",
			"value", v)
	}

	minLimit := envIntWithDefault("GNH_PENDING_ADMISSION_LIMIT_MIN", defaultPendingAdmissionLimitMin)
	factor := envIntWithDefault("GNH_PENDING_ADMISSION_WORKER_FACTOR", defaultPendingAdmissionWorkerFactor)
	maxWorkers := jobDefaultConcurrency()

	derived := maxWorkers * factor
	if derived < minLimit {
		derived = minLimit
	}
	if derived <= 0 {
		return defaultActiveJobsLimit
	}
	return derived
}

// StreamWorkerDeps groups the dependencies for a StreamWorkerPool.
type StreamWorkerDeps struct {
	Consumer     *broker.Consumer
	Scheduler    *broker.Scheduler
	Counters     *broker.RunningCounters
	Pacer        *broker.DomainPacer
	Executor     *TaskExecutor
	BatchManager *db.BatchManager
	DBQueue      DbQueueInterface
	JobManager   JobManagerInterface

	// HTMLPersister, when non-nil, receives the HTML payload from each
	// completed task and is responsible for streaming it directly to R2
	// before stamping the storage metadata back onto the task row. Left
	// nil when ARCHIVE_PROVIDER is unset (e.g. local dev without R2),
	// in which case completed tasks persist without HTML.
	HTMLPersister *HTMLPersister
}

// StreamWorkerOpts holds tunables for the stream worker pool.
type StreamWorkerOpts struct {
	// NumWorkers is the number of consumer goroutines.
	NumWorkers int

	// TasksPerWorker is the max concurrent in-flight tasks per consumer
	// goroutine. Global parallelism ceiling = NumWorkers × TasksPerWorker.
	// Mirrors the pre-Redis WorkerPool WORKER_CONCURRENCY semaphore.
	// A value of 0 or 1 preserves the one-task-at-a-time legacy behaviour.
	TasksPerWorker int

	// ReclaimInterval is how often stale messages are reclaimed.
	ReclaimInterval time.Duration
}

// DefaultStreamWorkerOpts returns production defaults.
func DefaultStreamWorkerOpts() StreamWorkerOpts {
	return StreamWorkerOpts{
		NumWorkers:      30,
		TasksPerWorker:  20,
		ReclaimInterval: 30 * time.Second,
	}
}

const jobInfoTTL = 5 * time.Minute

type cachedJobInfo struct {
	info      *JobInfo
	expiresAt time.Time
}

// StreamWorkerPool consumes tasks from Redis Streams, executes them
// via the TaskExecutor, and acts on the returned TaskOutcome
// (ack, reschedule, persist results).
type StreamWorkerPool struct {
	consumer      *broker.Consumer
	scheduler     *broker.Scheduler
	counters      *broker.RunningCounters
	pacer         *broker.DomainPacer
	executor      *TaskExecutor
	batchManager  *db.BatchManager
	dbQueue       DbQueueInterface
	jobManager    JobManagerInterface
	htmlPersister *HTMLPersister
	opts          StreamWorkerOpts

	// Job info cache with TTL eviction.
	jobInfoCache map[string]*cachedJobInfo
	jobInfoMutex sync.RWMutex
	jobInfoGroup singleflight.Group

	// Active job IDs — refreshed periodically for round-robin.
	activeJobs   []string
	activeJobsMu sync.RWMutex

	// linkDiscoverySem caps concurrent calls into ProcessDiscoveredLinks.
	// Each link-discovery call runs CreatePageRecords + EnqueueJobURLs on
	// the bulk DB lane, so under heavy load all NumWorkers × TasksPerWorker
	// goroutines used to pile onto the pool simultaneously and saturate it
	// (HOVER-KG, 2k+ goroutines at one event). Capping this path leaves
	// pool headroom for promotion, counter sync, and the outbox sweeper.
	linkDiscoverySem chan struct{}

	wg     sync.WaitGroup
	cancel context.CancelFunc
}

// defaultLinkDiscoveryMaxInflight: at 32 the cap throttled production
// throughput to ~3–3.5k tasks/min (each call is bulk-pool-bound, so the
// semaphore ceiling becomes the global enqueue ceiling). 128 keeps fan-out
// well clear of the 2k+ goroutine event that motivated the cap while
// restoring headroom above the previous 4k tasks/min run rate.
const defaultLinkDiscoveryMaxInflight = 128

// linkDiscoveryMaxInflight returns the configured cap on concurrent
// in-flight ProcessDiscoveredLinks executions.
func linkDiscoveryMaxInflight() int {
	if v := strings.TrimSpace(os.Getenv("JOBS_LINK_DISCOVERY_MAX_INFLIGHT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		jobsLog.Warn("invalid JOBS_LINK_DISCOVERY_MAX_INFLIGHT, using default",
			"value", v, "default", defaultLinkDiscoveryMaxInflight)
	}
	return defaultLinkDiscoveryMaxInflight
}

// NewStreamWorkerPool creates a StreamWorkerPool.
func NewStreamWorkerPool(deps StreamWorkerDeps, opts StreamWorkerOpts) *StreamWorkerPool {
	return &StreamWorkerPool{
		consumer:         deps.Consumer,
		scheduler:        deps.Scheduler,
		counters:         deps.Counters,
		pacer:            deps.Pacer,
		executor:         deps.Executor,
		batchManager:     deps.BatchManager,
		dbQueue:          deps.DBQueue,
		jobManager:       deps.JobManager,
		htmlPersister:    deps.HTMLPersister,
		opts:             opts,
		jobInfoCache:     make(map[string]*cachedJobInfo),
		linkDiscoverySem: make(chan struct{}, linkDiscoveryMaxInflight()),
	}
}

// Start launches consumer goroutines and the reclaim loop.
// Blocks until Stop is called or ctx is cancelled.
func (swp *StreamWorkerPool) Start(ctx context.Context) {
	ctx, swp.cancel = context.WithCancel(ctx)

	// Refresh active jobs first so reconcile knows which streams to probe.
	swp.refreshActiveJobs(ctx)

	// Reconcile running counters from the Redis PEL (XPENDING).
	swp.reconcileCounters(ctx)

	// Start consumer goroutines.
	for i := range swp.opts.NumWorkers {
		swp.wg.Add(1)
		go func(id int) {
			defer swp.wg.Done()
			swp.workerLoop(ctx, id)
		}(i)
	}

	// Start reclaim loop.
	swp.wg.Add(1)
	go func() {
		defer swp.wg.Done()
		swp.reclaimLoop(ctx)
	}()

	// Start active jobs refresh loop.
	swp.wg.Add(1)
	go func() {
		defer swp.wg.Done()
		swp.activeJobsRefreshLoop(ctx)
	}()

	// Start periodic counter reconciliation. reconcileCounters was
	// previously called only at Start, so any mid-run drift (e.g. missed
	// decrement after a worker crash, or a stale counter left over from a
	// dead stream) persisted until the next deploy. Run it every couple of
	// minutes so the counters self-heal against the authoritative PEL.
	swp.wg.Add(1)
	go func() {
		defer swp.wg.Done()
		swp.reconcileLoop(ctx)
	}()

	jobsLog.Info("stream worker pool started", "workers", swp.opts.NumWorkers)
}

// Stop signals all goroutines to stop and waits for them.
func (swp *StreamWorkerPool) Stop() {
	if swp.cancel != nil {
		swp.cancel()
	}
	swp.wg.Wait()
	jobsLog.Info("stream worker pool stopped")
}

// --- consumer loop ---

func (swp *StreamWorkerPool) workerLoop(ctx context.Context, workerID int) {
	logger := jobsLog.With("worker_id", workerID)
	logger.Debug("worker started")

	// Per-worker semaphore controlling in-flight task concurrency.
	// Global parallelism = NumWorkers × capacity. When TasksPerWorker is 0
	// or 1 we keep the legacy one-at-a-time behaviour by using a capacity
	// of 1 (sem still applied so the fan-out call site stays uniform).
	perWorker := swp.opts.TasksPerWorker
	if perWorker < 1 {
		perWorker = 1
	}
	sem := make(chan struct{}, perWorker)
	var inflight sync.WaitGroup

	// dispatch runs processMessage on a child goroutine gated by sem.
	// Callers must call inflight.Wait() before shutdown.
	dispatch := func(msg broker.StreamMessage) {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		inflight.Add(1)
		go func(m broker.StreamMessage) {
			defer inflight.Done()
			defer func() { <-sem }()
			swp.processMessage(ctx, m)
		}(msg)
	}

	defer inflight.Wait()

	for {
		if ctx.Err() != nil {
			return
		}

		// Round-robin across active jobs.
		jobIDs := swp.getActiveJobs()
		if len(jobIDs) == 0 {
			// No active jobs — wait a bit.
			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
				continue
			}
		}

		processed := false
		for _, jobID := range jobIDs {
			if ctx.Err() != nil {
				return
			}

			msgs, err := swp.consumer.ReadNonBlocking(ctx, jobID)
			if err != nil {
				logger.Error("stream read error", "error", err, "job_id", jobID)
				continue
			}

			for _, msg := range msgs {
				dispatch(msg)
				processed = true
			}
		}

		if !processed {
			// No messages from any stream — block briefly on one job.
			//
			// Previously every worker blocked on jobIDs[0] here, which meant
			// under low load the first active job saw N workers queued on its
			// stream while the other N-1 jobs saw none. Shard by workerID so
			// workers spread across the active set instead, keeping BLOCK
			// pressure evenly distributed.
			if n := len(jobIDs); n > 0 {
				target := jobIDs[workerID%n]
				msgs, err := swp.consumer.Read(ctx, target)
				if err != nil {
					logger.Warn("blocking read error", "error", err, "job_id", target)
				}
				for _, msg := range msgs {
					dispatch(msg)
				}
			}
		}
	}
}

func (swp *StreamWorkerPool) processMessage(ctx context.Context, msg broker.StreamMessage) {
	logger := jobsLog.With("task_id", msg.TaskID, "job_id", msg.JobID)

	// Load job info and build enriched Task.
	task, err := swp.buildTask(ctx, msg)
	if err != nil {
		logger.Error("failed to build task from stream message", "error", err)
		// Do NOT ACK — buildTask failures are typically transient (DB
		// timeouts, connection errors). Leaving the message in the PEL
		// lets XAUTOCLAIM redeliver it after the idle threshold.
		return
	}

	// Execute the crawl.
	processStart := time.Now()
	taskCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	outcome := swp.executor.Execute(taskCtx, task)
	cancel()
	processDuration := time.Since(processStart)

	// Record task outcome telemetry.
	outcomeLabel, reason := classifyTaskOutcome(outcome)
	observability.RecordWorkerTaskOutcome(ctx, observability.WorkerTaskOutcomeMetrics{
		JobID:    msg.JobID,
		Outcome:  outcomeLabel,
		Reason:   reason,
		Duration: processDuration,
	})

	// Act on the outcome.
	swp.handleOutcome(ctx, msg, outcome)
}

func (swp *StreamWorkerPool) handleOutcome(ctx context.Context, msg broker.StreamMessage, outcome *TaskOutcome) {
	domain := msg.Host

	// For retryable tasks, the retry enqueue and the ACK must be a single
	// atomic Redis operation. A naive two-step (Schedule then Ack) has two
	// failure modes: (a) if Schedule fails we leave the message in the PEL
	// for redelivery — OK; but (b) if Schedule succeeds and Ack fails, the
	// retry is queued AND the original message stays in the PEL, so
	// XAUTOCLAIM will redeliver it and cause a duplicate crawl. The broker
	// exposes ScheduleAndAck (MULTI/EXEC) to avoid both failure modes.
	if outcome.Retry != nil && outcome.Retry.ShouldRetry {
		entry := broker.ScheduleEntry{
			TaskID:     outcome.Task.ID,
			JobID:      outcome.Task.JobID,
			PageID:     outcome.Task.PageID,
			Host:       msg.Host,
			Path:       msg.Path,
			Priority:   msg.Priority,
			RetryCount: outcome.Task.RetryCount,
			SourceType: outcome.Task.SourceType,
			SourceURL:  outcome.Task.SourceURL,
			RunAt:      outcome.Retry.NextRunAt,
		}
		if err := swp.scheduler.ScheduleAndAck(ctx, entry, msg.JobID, msg.MessageID); err != nil {
			jobsLog.Error("retry schedule-and-ack failed, leaving in PEL for redelivery", "error", err, "task_id", outcome.Task.ID)
			return
		}
	} else {
		// Non-retry path: just ACK.
		if err := swp.consumer.Ack(ctx, msg.JobID, msg.MessageID); err != nil {
			jobsLog.Error("failed to ACK, skipping bookkeeping", "error", err, "message_id", msg.MessageID)
			return
		}
	}

	// Decrement running counter.
	decrementedOK := true
	if _, err := swp.counters.Decrement(ctx, msg.JobID); err != nil {
		decrementedOK = false
		jobsLog.Warn("counter decrement failed", "error", err, "job_id", msg.JobID)
	}

	// A concurrency slot just freed. Promote one waiting task for this
	// job into pending so the freshly vacated slot doesn't sit idle
	// until the next link-discovery burst arrives. Skipped when the
	// decrement failed because the counter state is ambiguous and
	// double-promotion on retry would exceed the concurrency cap.
	//
	// Intentionally synchronous: the promoter is a single UPDATE +
	// INSERT and runs on the bulk lane, so adding a goroutine here
	// would just introduce a race for no throughput benefit.
	if decrementedOK && swp.dbQueue != nil {
		if _, err := swp.dbQueue.PromoteWaitingToPending(ctx, msg.JobID, 1); err != nil {
			jobsLog.Warn("waiting->pending promotion failed",
				"error", err, "job_id", msg.JobID)
		}
	}

	// Release domain pacer.
	if err := swp.pacer.Release(ctx, domain, msg.JobID, outcome.Success, outcome.RateLimited); err != nil {
		jobsLog.Warn("pacer release failed", "error", err, "domain", domain)
	}

	// Queue task update for batch persistence. Hand the payload to the
	// HTML persister BEFORE queueing the row so the persister always sees
	// a fresh *db.Task pointer — the batch manager mutates the row in
	// place under its own lock, and the persister reads only the IDs we
	// captured at Enqueue time.
	if outcome.HTMLUpload != nil && swp.htmlPersister != nil {
		swp.htmlPersister.Enqueue(ctx, outcome.Task, outcome.HTMLUpload)
	}
	swp.batchManager.QueueTaskUpdate(outcome.Task)

	// Handle discovered links — enqueue to Postgres then schedule.
	if len(outcome.DiscoveredLinks) > 0 {
		swp.handleDiscoveredLinks(ctx, outcome)
	}
}

func (swp *StreamWorkerPool) handleDiscoveredLinks(ctx context.Context, outcome *TaskOutcome) {
	if swp.jobManager == nil || len(outcome.DiscoveredLinks) == 0 {
		return
	}

	// Cap concurrent link-discovery DB chains so the bulk pool isn't
	// drained by every worker hitting CreatePageRecords + EnqueueJobURLs
	// simultaneously (HOVER-KG). Block on the semaphore so the caller
	// goroutine backpressures naturally; ctx cancellation aborts the wait.
	if swp.linkDiscoverySem != nil {
		select {
		case swp.linkDiscoverySem <- struct{}{}:
			defer func() { <-swp.linkDiscoverySem }()
		case <-ctx.Done():
			return
		}
	}

	// Build a Task with the fields ProcessDiscoveredLinks needs.
	task := &Task{
		ID:                       outcome.Task.ID,
		JobID:                    outcome.Task.JobID,
		Host:                     outcome.Task.Host,
		Path:                     outcome.Task.Path,
		PriorityScore:            outcome.Task.PriorityScore,
		FindLinks:                true, // only called when links exist
		AllowCrossSubdomainLinks: false,
	}

	// Enrich from job info cache.
	info, err := swp.loadJobInfo(ctx, outcome.Task.JobID)
	if err != nil {
		jobsLog.Error("failed to load job info for link discovery", "error", err, "job_id", outcome.Task.JobID)
		return
	}
	task.DomainID = info.DomainID
	task.DomainName = info.DomainName
	task.AllowCrossSubdomainLinks = info.AllowCrossSubdomainLinks

	sourceURL := ConstructTaskURL(outcome.Task.Path, outcome.Task.Host, info.DomainName)
	if outcome.CrawlResult != nil && outcome.CrawlResult.URL != "" {
		sourceURL = outcome.CrawlResult.URL
	}

	deps := LinkDiscoveryDeps{
		DBQueue:     swp.dbQueue,
		JobManager:  swp.jobManager,
		MinPriority: linkDiscoveryMinPriorityFromEnv(),
	}

	ProcessDiscoveredLinks(ctx, deps, task, outcome.DiscoveredLinks, sourceURL, info.RobotsRules)
}

// defaultReconcileIntervalS is how often the background loop rebuilds
// the running-counters HASH from the authoritative XPENDING view when
// REDIS_COUNTER_RECONCILE_INTERVAL_S is unset. Two minutes is a
// compromise between prompt drift recovery and XPENDING cost across
// hundreds of active jobs.
const defaultReconcileIntervalS = 120

func reconcileInterval() time.Duration {
	return time.Duration(envIntWithDefault("REDIS_COUNTER_RECONCILE_INTERVAL_S", defaultReconcileIntervalS)) * time.Second
}

func (swp *StreamWorkerPool) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(reconcileInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			swp.reconcileCounters(ctx)
		}
	}
}

// --- reclaim loop ---

func (swp *StreamWorkerPool) reclaimLoop(ctx context.Context) {
	ticker := time.NewTicker(swp.opts.ReclaimInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			swp.reclaimStaleMessages(ctx)
		}
	}
}

func (swp *StreamWorkerPool) reclaimStaleMessages(ctx context.Context) {
	jobIDs := swp.getActiveJobs()
	for _, jobID := range jobIDs {
		reclaimed, deadLetter, err := swp.consumer.ReclaimStale(ctx, jobID)
		if err != nil {
			jobsLog.Warn("reclaim failed", "error", err, "job_id", jobID)
			continue
		}

		// Re-process reclaimed messages.
		for _, msg := range reclaimed {
			jobsLog.Info("reclaimed stale task", "task_id", msg.TaskID, "message_id", msg.MessageID)
			swp.processMessage(ctx, msg)
		}

		// Dead-letter messages that exceeded max deliveries.
		for _, msg := range deadLetter {
			jobsLog.Warn("dead-lettering task after max deliveries", "task_id", msg.TaskID, "retry_count", msg.RetryCount)
			if err := swp.consumer.Ack(ctx, jobID, msg.MessageID); err != nil {
				jobsLog.Error("dead-letter ACK failed, skipping", "error", err, "task_id", msg.TaskID)
				continue
			}
			if _, err := swp.counters.Decrement(ctx, jobID); err != nil {
				jobsLog.Warn("counter decrement on dead-letter failed", "error", err)
			}
			// Release domain pacer inflight slot.
			if err := swp.pacer.Release(ctx, msg.Host, jobID, false, false); err != nil {
				jobsLog.Warn("pacer release on dead-letter failed", "error", err, "domain", msg.Host)
			}

			// Mark as failed in Postgres.
			dbTask := &db.Task{
				ID:          msg.TaskID,
				JobID:       msg.JobID,
				Status:      string(TaskStatusFailed),
				CompletedAt: time.Now().UTC(),
				Error:       "exceeded maximum delivery attempts",
				RetryCount:  msg.RetryCount,
			}
			swp.batchManager.QueueTaskUpdate(dbTask)
			observability.RecordWorkerTaskFailure(ctx, jobID, "dead_letter")
		}
	}
}

// --- active jobs ---

func (swp *StreamWorkerPool) activeJobsRefreshLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			swp.refreshActiveJobs(ctx)
		}
	}
}

func (swp *StreamWorkerPool) refreshActiveJobs(ctx context.Context) {
	var jobIDs []string
	limit := activeJobsLimit()
	err := swp.dbQueue.ExecuteControl(ctx, func(tx *sql.Tx) error {
		// Pre-merge ordering (FIFO): oldest updated first so long-running
		// jobs don't starve when more than `limit` jobs are in flight.
		// The new-world default of `created_at DESC` (newest first) was a
		// regression — under deep backlog it starved older jobs. Matches
		// pre-merge internal/jobs/worker.go:2273-2276.
		rows, err := tx.QueryContext(ctx,
			`SELECT id FROM jobs
			 WHERE status IN ('running', 'pending')
			 ORDER BY updated_at ASC NULLS FIRST,
			          started_at ASC NULLS FIRST,
			          created_at ASC
			 LIMIT $1`,
			limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			jobIDs = append(jobIDs, id)
		}
		return rows.Err()
	})
	if err != nil {
		// Downgrade to Warn: the refresh runs every 10s, so a transient control
		// DB outage would otherwise flood Sentry with one event per tick.
		jobsLog.Warn("failed to refresh active jobs", "error", err)
		return
	}

	// Never evict a job whose Redis PEL still holds in-flight work. The active-
	// list ordering + limit can shuffle a mid-flight job off the window, which
	// freezes both dispatch (counter gate stuck at the cap) and consume (no
	// worker reads that job's stream). Union the Postgres-derived set with any
	// job that has a non-zero running_counters entry so those streams keep
	// draining until the PEL is genuinely empty.
	counters, counterErr := swp.counters.GetAll(ctx)
	if counterErr != nil {
		jobsLog.Warn("failed to read running counters for active-job merge", "error", counterErr)
	} else if len(counters) > 0 {
		seen := make(map[string]struct{}, len(jobIDs))
		for _, id := range jobIDs {
			seen[id] = struct{}{}
		}
		extra := 0
		for id, n := range counters {
			if n <= 0 {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			jobIDs = append(jobIDs, id)
			seen[id] = struct{}{}
			extra++
		}
		if extra > 0 {
			jobsLog.Info("retained active jobs with in-flight work",
				"retained", extra, "total_active", len(jobIDs))
		}
	}

	swp.activeJobsMu.Lock()
	swp.activeJobs = jobIDs
	swp.activeJobsMu.Unlock()
}

func (swp *StreamWorkerPool) getActiveJobs() []string {
	swp.activeJobsMu.RLock()
	defer swp.activeJobsMu.RUnlock()
	return swp.activeJobs
}

// ActiveJobIDs implements broker.JobLister for the dispatcher.
func (swp *StreamWorkerPool) ActiveJobIDs(ctx context.Context) ([]string, error) {
	return swp.getActiveJobs(), nil
}

// --- job info ---

func (swp *StreamWorkerPool) buildTask(ctx context.Context, msg broker.StreamMessage) (*Task, error) {
	info, err := swp.loadJobInfo(ctx, msg.JobID)
	if err != nil {
		return nil, fmt.Errorf("load job info for %s: %w", msg.JobID, err)
	}

	return &Task{
		ID:                       msg.TaskID,
		JobID:                    msg.JobID,
		PageID:                   msg.PageID,
		Host:                     msg.Host,
		Path:                     msg.Path,
		Status:                   TaskStatusRunning,
		StartedAt:                time.Now().UTC(),
		RetryCount:               msg.RetryCount,
		SourceType:               msg.SourceType,
		SourceURL:                msg.SourceURL,
		PriorityScore:            msg.Priority,
		DomainID:                 info.DomainID,
		DomainName:               info.DomainName,
		FindLinks:                info.FindLinks,
		AllowCrossSubdomainLinks: info.AllowCrossSubdomainLinks,
		CrawlDelay:               info.CrawlDelay,
		JobConcurrency:           info.Concurrency,
		AdaptiveDelay:            info.AdaptiveDelay,
		AdaptiveDelayFloor:       info.AdaptiveDelayFloor,
	}, nil
}

func (swp *StreamWorkerPool) loadJobInfo(ctx context.Context, jobID string) (*JobInfo, error) {
	now := time.Now()

	swp.jobInfoMutex.RLock()
	if cached, ok := swp.jobInfoCache[jobID]; ok && now.Before(cached.expiresAt) {
		swp.jobInfoMutex.RUnlock()
		return cached.info, nil
	}
	swp.jobInfoMutex.RUnlock()

	val, err, _ := swp.jobInfoGroup.Do(jobID, func() (any, error) {
		info, fetchErr := swp.fetchJobInfo(ctx, jobID)
		if fetchErr != nil {
			return nil, fetchErr
		}

		swp.jobInfoMutex.Lock()
		swp.jobInfoCache[jobID] = &cachedJobInfo{
			info:      info,
			expiresAt: time.Now().Add(jobInfoTTL),
		}
		swp.jobInfoMutex.Unlock()

		return info, nil
	})
	if err != nil {
		return nil, err
	}

	return val.(*JobInfo), nil
}

func (swp *StreamWorkerPool) fetchJobInfo(ctx context.Context, jobID string) (*JobInfo, error) {
	var info JobInfo
	var crawlDelay, adaptiveDelay, adaptiveFloor sql.NullInt64

	err := swp.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT d.id, d.name, d.crawl_delay_seconds, d.adaptive_delay_seconds,
			       d.adaptive_delay_floor_seconds, j.find_links,
			       j.allow_cross_subdomain_links, j.concurrency
			FROM domains d
			JOIN jobs j ON j.domain_id = d.id
			WHERE j.id = $1
		`, jobID).Scan(
			&info.DomainID, &info.DomainName, &crawlDelay, &adaptiveDelay,
			&adaptiveFloor, &info.FindLinks, &info.AllowCrossSubdomainLinks,
			&info.Concurrency,
		)
	})
	if err != nil {
		return nil, err
	}

	if crawlDelay.Valid {
		info.CrawlDelay = int(crawlDelay.Int64)
	}
	if adaptiveDelay.Valid {
		info.AdaptiveDelay = int(adaptiveDelay.Int64)
	}
	if adaptiveFloor.Valid {
		info.AdaptiveDelayFloor = int(adaptiveFloor.Int64)
	}

	// Hydrate robots.txt rules so link discovery can enforce them.
	// Without this, ProcessDiscoveredLinks sees a nil ruleset and
	// lets every discovered URL through, bypassing disallow rules.
	// Fetch lives in JobManager because it already owns a crawler
	// handle. The caller caches the whole JobInfo for jobInfoTTL,
	// so this fetch runs at most once every 5 minutes per job.
	if swp.jobManager != nil {
		rules, robotsErr := swp.jobManager.GetRobotsRules(ctx, info.DomainName)
		if robotsErr != nil {
			// Log and continue with nil rules — better to enqueue a few
			// extra URLs than to stall the worker when robots.txt is
			// temporarily unavailable. Matches legacy behaviour.
			jobsLog.Warn("failed to load robots rules for job info, continuing without",
				"error", robotsErr, "domain", info.DomainName)
		} else {
			info.RobotsRules = rules
		}
	}

	return &info, nil
}

// --- counter reconciliation ---

// reconcileCounters rebuilds the per-job RunningCounters HASH in Redis
// from the authoritative stream PEL (XPENDING). The previous version
// queried Postgres for tasks where status='running', but the batch
// writer never transitions tasks through that status — they go
// pending → completed/failed in one UPDATE — so the query always
// returned zero and the reconcile silently wiped the counters,
// stalling dispatch until the next decrement restored them.
//
// The PEL is the source of truth for in-flight work per job because
// a message stays in the PEL from XREADGROUP delivery until XACK,
// which brackets the worker's crawl + persist lifecycle.
func (swp *StreamWorkerPool) reconcileCounters(ctx context.Context) {
	activeSet := swp.getActiveJobs()
	activeLookup := make(map[string]struct{}, len(activeSet))
	for _, id := range activeSet {
		activeLookup[id] = struct{}{}
	}
	jobIDs := append([]string(nil), activeSet...)

	// Also include any job currently present in the Redis counters hash,
	// in case refreshActiveJobs hasn't surfaced it yet (e.g. a job that
	// just transitioned status).
	existing, err := swp.counters.GetAll(ctx)
	if err != nil {
		jobsLog.Warn("failed to read existing running counters; continuing with active-job set only", "error", err)
	} else {
		for id := range existing {
			if _, ok := activeLookup[id]; !ok {
				jobIDs = append(jobIDs, id)
			}
		}
	}

	counts := make(map[string]int64, len(jobIDs))
	orphanPELCount := int64(0)
	for _, jobID := range jobIDs {
		n, err := swp.consumer.PendingCount(ctx, jobID)
		if err != nil {
			jobsLog.Warn("PendingCount failed during reconcile; skipping job", "job_id", jobID, "error", err)
			// Preserve the existing counter rather than zeroing it on a
			// transient Redis hiccup — better to over-count briefly than
			// to flood a job with extra dispatches.
			if prev, ok := existing[jobID]; ok {
				counts[jobID] = prev
			}
			continue
		}
		if n > 0 {
			counts[jobID] = n
			// A job with a non-zero PEL that isn't in the worker's active
			// set is a stalled-dispatch smoking gun: no one will read from
			// its stream, so those messages sit forever. Surface this as a
			// dedicated metric so we can alert on it.
			if _, isActive := activeLookup[jobID]; !isActive {
				orphanPELCount++
				jobsLog.Warn("job has pending work but is not in active-job set",
					"job_id", jobID, "pending", n)
			}
		}
		// Record the skew between the old Redis HASH value and the
		// authoritative PEL so we can detect leaks continuously, not only
		// by eyeballing Postgres.
		prev := existing[jobID]
		diff := prev - n
		if diff < 0 {
			diff = -diff
		}
		observability.RecordBrokerCounterPELSkew(ctx, jobID, float64(diff))
	}

	if err := swp.counters.Reconcile(ctx, counts); err != nil {
		jobsLog.Error("failed to reconcile running counters in Redis", "error", err)
		return
	}

	observability.RecordBrokerPELWithoutConsumer(ctx, orphanPELCount)

	total := int64(0)
	for _, c := range counts {
		total += c
	}
	jobsLog.Info("reconciled running counters from PEL",
		"total_running", total,
		"jobs_probed", len(jobIDs),
		"jobs_with_pel", len(counts),
		"orphan_pel_jobs", orphanPELCount)
}

// --- concurrency checking (for dispatcher) ---

// CanDispatch returns true if the job has capacity for more in-flight
// tasks. Implements broker.ConcurrencyChecker.
func (swp *StreamWorkerPool) CanDispatch(ctx context.Context, jobID string) (bool, error) {
	info, err := swp.loadJobInfo(ctx, jobID)
	if err != nil {
		return false, err
	}

	concurrency := info.Concurrency
	if concurrency <= 0 {
		concurrency = fallbackJobConcurrency // 20, same as API default
	}

	running, err := swp.counters.Get(ctx, jobID)
	if err != nil {
		return false, err
	}

	return running < int64(concurrency), nil
}
