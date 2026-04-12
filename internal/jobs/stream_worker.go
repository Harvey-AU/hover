package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/Harvey-AU/hover/internal/broker"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/rs/zerolog"
	"golang.org/x/sync/singleflight"
)

// StreamWorkerDeps groups the dependencies for a StreamWorkerPool.
type StreamWorkerDeps struct {
	Consumer     *broker.Consumer
	Scheduler    *broker.Scheduler
	Counters     *broker.RunningCounters
	Pacer        *broker.DomainPacer
	Executor     *TaskExecutor
	BatchManager *db.BatchManager
	DBQueue      DbQueueInterface
	Logger       zerolog.Logger
}

// StreamWorkerOpts holds tunables for the stream worker pool.
type StreamWorkerOpts struct {
	// NumWorkers is the number of consumer goroutines.
	NumWorkers int

	// ReclaimInterval is how often stale messages are reclaimed.
	ReclaimInterval time.Duration
}

// DefaultStreamWorkerOpts returns production defaults.
func DefaultStreamWorkerOpts() StreamWorkerOpts {
	return StreamWorkerOpts{
		NumWorkers:      30,
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
	consumer     *broker.Consumer
	scheduler    *broker.Scheduler
	counters     *broker.RunningCounters
	pacer        *broker.DomainPacer
	executor     *TaskExecutor
	batchManager *db.BatchManager
	dbQueue      DbQueueInterface
	opts         StreamWorkerOpts
	logger       zerolog.Logger

	// Job info cache with TTL eviction.
	jobInfoCache map[string]*cachedJobInfo
	jobInfoMutex sync.RWMutex
	jobInfoGroup singleflight.Group

	// Active job IDs — refreshed periodically for round-robin.
	activeJobs   []string
	activeJobsMu sync.RWMutex

	wg     sync.WaitGroup
	cancel context.CancelFunc
}

// NewStreamWorkerPool creates a StreamWorkerPool.
func NewStreamWorkerPool(deps StreamWorkerDeps, opts StreamWorkerOpts) *StreamWorkerPool {
	return &StreamWorkerPool{
		consumer:     deps.Consumer,
		scheduler:    deps.Scheduler,
		counters:     deps.Counters,
		pacer:        deps.Pacer,
		executor:     deps.Executor,
		batchManager: deps.BatchManager,
		dbQueue:      deps.DBQueue,
		opts:         opts,
		logger:       deps.Logger.With().Str("component", "stream_worker").Logger(),
		jobInfoCache: make(map[string]*cachedJobInfo),
	}
}

// Start launches consumer goroutines and the reclaim loop.
// Blocks until Stop is called or ctx is cancelled.
func (swp *StreamWorkerPool) Start(ctx context.Context) {
	ctx, swp.cancel = context.WithCancel(ctx)

	// Reconcile running counters from Postgres.
	swp.reconcileCounters(ctx)

	// Refresh active jobs immediately.
	swp.refreshActiveJobs(ctx)

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

	swp.logger.Info().Int("workers", swp.opts.NumWorkers).Msg("stream worker pool started")
}

// Stop signals all goroutines to stop and waits for them.
func (swp *StreamWorkerPool) Stop() {
	if swp.cancel != nil {
		swp.cancel()
	}
	swp.wg.Wait()
	swp.logger.Info().Msg("stream worker pool stopped")
}

// --- consumer loop ---

func (swp *StreamWorkerPool) workerLoop(ctx context.Context, workerID int) {
	logger := swp.logger.With().Int("worker_id", workerID).Logger()
	logger.Debug().Msg("worker started")

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
				logger.Error().Err(err).Str("job_id", jobID).Msg("stream read error")
				continue
			}

			for _, msg := range msgs {
				swp.processMessage(ctx, msg)
				processed = true
			}
		}

		if !processed {
			// No messages from any stream — block briefly on first stream.
			if len(jobIDs) > 0 {
				msgs, err := swp.consumer.Read(ctx, jobIDs[0])
				if err != nil {
					logger.Warn().Err(err).Msg("blocking read error")
				}
				for _, msg := range msgs {
					swp.processMessage(ctx, msg)
				}
			}
		}
	}
}

func (swp *StreamWorkerPool) processMessage(ctx context.Context, msg broker.StreamMessage) {
	logger := swp.logger.With().
		Str("task_id", msg.TaskID).
		Str("job_id", msg.JobID).
		Logger()

	// Load job info and build enriched Task.
	task, err := swp.buildTask(ctx, msg)
	if err != nil {
		logger.Error().Err(err).Msg("failed to build task from stream message")
		// ACK to prevent infinite redelivery of unprocessable messages.
		_ = swp.consumer.Ack(ctx, msg.JobID, msg.MessageID)
		_, _ = swp.counters.Decrement(ctx, msg.JobID)
		return
	}

	// Execute the crawl.
	taskCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	outcome := swp.executor.Execute(taskCtx, task)
	cancel()

	// Act on the outcome.
	swp.handleOutcome(ctx, msg, outcome)
}

func (swp *StreamWorkerPool) handleOutcome(ctx context.Context, msg broker.StreamMessage, outcome *TaskOutcome) {
	domain := msg.Host

	// For retryable tasks, schedule the retry BEFORE ACKing so that if
	// scheduling fails the message stays in the PEL for redelivery.
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
		if err := swp.scheduler.Schedule(ctx, entry); err != nil {
			swp.logger.Error().Err(err).
				Str("task_id", outcome.Task.ID).
				Msg("retry schedule failed, leaving in PEL for redelivery")
			return
		}
	}

	// ACK the stream message (remove from PEL).
	if err := swp.consumer.Ack(ctx, msg.JobID, msg.MessageID); err != nil {
		swp.logger.Error().Err(err).Str("message_id", msg.MessageID).Msg("failed to ACK")
	}

	// Decrement running counter.
	if _, err := swp.counters.Decrement(ctx, msg.JobID); err != nil {
		swp.logger.Warn().Err(err).Str("job_id", msg.JobID).Msg("counter decrement failed")
	}

	// Release domain pacer.
	if err := swp.pacer.Release(ctx, domain, msg.JobID, outcome.Success, outcome.RateLimited); err != nil {
		swp.logger.Warn().Err(err).Str("domain", domain).Msg("pacer release failed")
	}

	// Queue task update for batch persistence.
	swp.batchManager.QueueTaskUpdate(outcome.Task)

	// Handle discovered links — enqueue to Postgres then schedule.
	if len(outcome.DiscoveredLinks) > 0 {
		swp.handleDiscoveredLinks(ctx, outcome)
	}

	// HTML upload: metadata is already applied to outcome.Task by the
	// executor via applyHTMLMetadata. The actual upload to storage will
	// be wired through a persistence loop in cmd/worker (Stage 2).
}

func (swp *StreamWorkerPool) handleDiscoveredLinks(ctx context.Context, outcome *TaskOutcome) {
	// TODO(stage2): Wire discovered link persistence and Redis scheduling.
	// This requires extracting the link filtering/insertion logic from the
	// old worker pool's processDiscoveredLinks into a reusable function,
	// then calling EnqueueURLs + scheduler.ScheduleBatch here.
	// Tracked as part of Stage 2 implementation.
	swp.logger.Debug().
		Str("task_id", outcome.Task.ID).
		Int("link_count", len(outcome.DiscoveredLinks)).
		Msg("discovered links pending persistence (Stage 2)")
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
			swp.logger.Warn().Err(err).Str("job_id", jobID).Msg("reclaim failed")
			continue
		}

		// Re-process reclaimed messages.
		for _, msg := range reclaimed {
			swp.logger.Info().Str("task_id", msg.TaskID).Str("message_id", msg.MessageID).Msg("reclaimed stale task")
			swp.processMessage(ctx, msg)
		}

		// Dead-letter messages that exceeded max deliveries.
		for _, msg := range deadLetter {
			swp.logger.Warn().Str("task_id", msg.TaskID).Int("retry_count", msg.RetryCount).Msg("dead-lettering task after max deliveries")
			_ = swp.consumer.Ack(ctx, jobID, msg.MessageID)
			_, decErr := swp.counters.Decrement(ctx, jobID)
			if decErr != nil {
				swp.logger.Warn().Err(decErr).Msg("counter decrement on dead-letter failed")
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
	err := swp.dbQueue.ExecuteControl(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id FROM jobs WHERE status IN ('running', 'pending') ORDER BY created_at DESC LIMIT 200`)
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
		swp.logger.Error().Err(err).Msg("failed to refresh active jobs")
		return
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

	return &info, nil
}

// --- counter reconciliation ---

func (swp *StreamWorkerPool) reconcileCounters(ctx context.Context) {
	counts := make(map[string]int64)

	err := swp.dbQueue.ExecuteControl(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT job_id, COUNT(*) FROM tasks WHERE status = 'running' GROUP BY job_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var jobID string
			var count int64
			if err := rows.Scan(&jobID, &count); err != nil {
				return err
			}
			counts[jobID] = count
		}
		return rows.Err()
	})
	if err != nil {
		swp.logger.Error().Err(err).Msg("failed to query running task counts for reconciliation")
		return
	}

	if err := swp.counters.Reconcile(ctx, counts); err != nil {
		swp.logger.Error().Err(err).Msg("failed to reconcile running counters in Redis")
		return
	}

	total := int64(0)
	for _, c := range counts {
		total += c
	}
	swp.logger.Info().Int64("total_running", total).Int("jobs", len(counts)).Msg("reconciled running counters")
}

// --- concurrency checking (for dispatcher) ---

// CanDispatch returns true if the job has capacity for more in-flight
// tasks. Implements broker.ConcurrencyChecker.
func (swp *StreamWorkerPool) CanDispatch(ctx context.Context, jobID string) (bool, error) {
	info, err := swp.loadJobInfo(ctx, jobID)
	if err != nil {
		return false, err
	}

	running, err := swp.counters.Get(ctx, jobID)
	if err != nil {
		return false, err
	}

	return running < int64(info.Concurrency), nil
}
