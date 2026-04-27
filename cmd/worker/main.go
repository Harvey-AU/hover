// cmd/worker is the dedicated crawl worker service. It consumes
// tasks from Redis Streams, executes crawls, and persists results
// to Postgres via the batch manager.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Harvey-AU/hover/internal/archive"
	"github.com/Harvey-AU/hover/internal/broker"
	"github.com/Harvey-AU/hover/internal/crawler"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/jobs"
	"github.com/Harvey-AU/hover/internal/lighthouse"
	"github.com/Harvey-AU/hover/internal/logging"
	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/Harvey-AU/hover/internal/watchdog"
	"github.com/getsentry/sentry-go"
	"github.com/lib/pq"
)

var workerLog = logging.Component("worker")

func main() {
	appEnv := os.Getenv("APP_ENV")

	// --- sentry (initialise first so logging.Setup can wire the sentry slog handler) ---
	if dsn := os.Getenv("SENTRY_DSN"); dsn != "" {
		if err := sentry.Init(sentry.ClientOptions{
			Dsn:              dsn,
			Environment:      appEnv,
			AttachStacktrace: true,
			BeforeSend:       logging.BeforeSend,
		}); err != nil {
			// Sentry failed to init — fall through with plain slog only.
			fmt.Fprintf(os.Stderr, "failed to initialise Sentry: %v\n", err)
		} else {
			defer sentry.Flush(2 * time.Second)
		}
	}

	// --- logging (slog + sentry fanout) ---
	logging.Setup(logging.ParseLevel(os.Getenv("LOG_LEVEL")), appEnv)
	defer flushAsyncLogs()

	workerLog.Info("hover worker starting")

	// --- observability ---
	if os.Getenv("OBSERVABILITY_ENABLED") == "true" {
		// Derive service.name from FLY_APP_NAME so traces from review apps
		// (e.g. hover-worker-pr-342) are distinguishable from production.
		serviceName := strings.TrimSpace(os.Getenv("FLY_APP_NAME"))
		if serviceName == "" {
			serviceName = "hover-worker"
		}
		metricsAddr := os.Getenv("METRICS_ADDR")
		if metricsAddr == "" {
			metricsAddr = ":9464"
		}
		providers, err := observability.Init(context.Background(), observability.Config{
			Enabled:        true,
			ServiceName:    serviceName,
			Environment:    appEnv,
			OTLPEndpoint:   strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
			OTLPHeaders:    observability.ParseOTLPHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")),
			MetricsAddress: metricsAddr,
		})
		if err != nil {
			workerLog.Warn("failed to initialise observability", "error", err)
		} else {
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = providers.Shutdown(ctx)
			}()

			// Expose the Prometheus registry on METRICS_ADDR so the Alloy
			// sidecar (alloy.river) can scrape it and add app/environment
			// labels. Without this the worker's metrics only reach Grafana
			// via OTLP push and the dashboard's app filter excludes them.
			//
			// Also mount /debug/pprof on the same port so a wedged worker
			// can be debugged via `fly proxy 9464` without redeploying. The
			// metrics port is on Fly's internal network only, so no auth
			// guard is required.
			if providers.MetricsHandler != nil && metricsAddr != "" {
				mux := http.NewServeMux()
				mux.Handle("/metrics", providers.MetricsHandler)
				mux.HandleFunc("/debug/pprof/", pprof.Index)
				mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
				mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
				mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
				mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
				metricsSrv := &http.Server{
					Addr:              metricsAddr,
					Handler:           mux,
					ReadHeaderTimeout: 5 * time.Second,
				}
				metricsListener, err := net.Listen("tcp", metricsAddr)
				if err != nil {
					workerLog.Error("metrics server failed to bind", "error", err, "addr", metricsAddr)
				} else {
					go func() {
						workerLog.Info("metrics server listening", "addr", metricsAddr)
						if err := metricsSrv.Serve(metricsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
							workerLog.Error("metrics server failed", "error", err)
						}
					}()
					defer func() {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						if err := metricsSrv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
							workerLog.Warn("graceful shutdown of metrics server failed", "error", err)
						}
					}()
				}
			}
		}
	}

	// --- postgres ---
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer dbCancel()

	pgDB, err := db.WaitForDatabase(dbCtx, 5*time.Minute)
	if err != nil {
		workerLog.Fatal("failed to connect to PostgreSQL", "error", err)
	}
	defer func() {
		workerLog.Info("closing database connection")
		// Allow in-flight batch manager flushes and counter syncs to complete
		// before tearing down the connection pool.
		time.Sleep(1 * time.Second)
		_ = pgDB.Close()
	}()

	queueDB := pgDB
	if queueURL := strings.TrimSpace(os.Getenv("DATABASE_QUEUE_URL")); queueURL != "" {
		queueCtx, queueCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer queueCancel()
		qConn, err := db.InitFromURLWithSuffixRetry(queueCtx, queueURL, appEnv, "queue")
		if err != nil {
			workerLog.Fatal("failed to connect to queue PostgreSQL", "error", err)
		}
		defer func() {
			time.Sleep(500 * time.Millisecond)
			_ = qConn.Close()
		}()
		queueDB = qConn
	}

	dbQueue := db.NewDbQueue(queueDB)

	// --- redis ---
	redisCfg := broker.ConfigFromEnv()
	redisClient, err := broker.NewClient(redisCfg)
	if err != nil {
		workerLog.Fatal("failed to create Redis client", "error", err)
	}
	defer redisClient.Close()

	if err := redisClient.Ping(context.Background()); err != nil {
		workerLog.Fatal("failed to ping Redis", "error", err)
	}
	workerLog.Info("connected to Redis")

	// --- broker components ---
	// Use the DB-backed constructor so Reschedule mirrors pacing
	// push-backs to tasks.run_at (survives a Redis flush). Route to
	// queueDB because tasks + task_outbox live there when DATABASE_QUEUE_URL
	// is set; falls back to pgDB in single-DB deployments.
	scheduler := broker.NewSchedulerWithDB(redisClient, queueDB.GetDB())
	pacerCfg := broker.DefaultPacerConfig()
	pacer := broker.NewDomainPacer(redisClient, pacerCfg)
	counters := broker.NewRunningCounters(redisClient)

	// Flush accumulated per-domain adaptive-delay state on each boot.
	// Pre-merge the DomainLimiter was in-memory and reset on every
	// worker restart. Post-merge this state lives in Redis with a 24h
	// TTL, so a brief run of 429s can throttle a domain for a full day
	// even after the upstream target recovers. Wiping on boot mirrors
	// the pre-merge behaviour without removing the adaptive growth
	// during the worker's lifetime. Disable by setting
	// GNH_PACER_FLUSH_ON_START=false.
	if strings.TrimSpace(os.Getenv("GNH_PACER_FLUSH_ON_START")) != "false" {
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 10*time.Second)
		deleted, err := pacer.FlushAdaptiveDelays(flushCtx)
		flushCancel()
		if err != nil {
			workerLog.Warn("pacer adaptive-delay flush failed", "error", err)
		} else {
			workerLog.Info("pacer adaptive-delay flush complete", "keys_deleted", deleted)
		}
	}

	// Sweep stale per-job entries from every dom:flight HASH. See
	// sweepOrphanInflightOnBoot for the full rationale.
	sweepOrphanInflightOnBoot(redisClient, pgDB.GetDB())

	machineName := os.Getenv("FLY_MACHINE_ID")
	if machineName == "" {
		machineName, _ = os.Hostname()
	}

	numWorkers := envInt("WORKER_COUNT", 30)
	if numWorkers < 1 {
		workerLog.Fatal("WORKER_COUNT must be >= 1", "value", numWorkers)
	}
	// Tasks-per-worker semaphore capacity. Mirrors the pre-Redis
	// WORKER_CONCURRENCY dial: each consumer goroutine can hold up to
	// this many in-flight tasks. Global task ceiling = numWorkers × tpw.
	tasksPerWorker := envInt("WORKER_CONCURRENCY", 20)
	if tasksPerWorker < 1 {
		workerLog.Fatal("WORKER_CONCURRENCY must be >= 1", "value", tasksPerWorker)
	}
	consumerOpts := broker.DefaultConsumerOpts(fmt.Sprintf("worker-%s", machineName))
	consumer := broker.NewConsumer(redisClient, consumerOpts)

	// --- crawler ---
	crawlerCfg := crawler.DefaultConfig()
	cr := crawler.New(crawlerCfg)

	// --- executor ---
	executorCfg := jobs.DefaultExecutorConfig()
	executor := jobs.NewTaskExecutor(cr, executorCfg)

	// --- batch manager ---
	batchManager := db.NewBatchManager(dbQueue)
	defer batchManager.Stop()

	// --- html persister (direct-to-R2) ---
	// Built only when ARCHIVE_PROVIDER + ARCHIVE_BUCKET are set; without
	// those the worker still runs but completed tasks persist without
	// HTML. This keeps local dev (no R2 creds) functional and matches
	// the existing scheduler's "feature disabled when env unset" idiom.
	htmlPersister := buildHTMLPersister(workerLog, dbQueue)

	// --- job manager (for EnqueueJobURLs + OnTasksEnqueued callback) ---
	jobManager := jobs.NewJobManager(pgDB.GetDB(), dbQueue, cr)
	jobManager.OnTasksEnqueued = makeTasksEnqueuedHandler(scheduler, workerLog)

	// --- lighthouse scheduler ---
	// The scheduler runs in-process on the worker so the producer side
	// of the lighthouse pipeline shares the same DB pool as the crawl
	// path. The consumer side (hover-analysis) lives in a separate Fly
	// app; only the scheduler bits are wired here.
	lhScheduler := lighthouse.NewScheduler(pgDB, dbQueue)
	jobManager.OnProgressMilestone = func(ctx context.Context, jobID string, oldPct, newPct int) {
		// JobManager exposes the full crossing (oldPct, newPct) for
		// future consumers; the lighthouse scheduler only needs the
		// milestone it should sample at, which is newPct.
		if err := lhScheduler.OnMilestone(ctx, jobID, newPct); err != nil {
			workerLog.Warn("lighthouse scheduler OnMilestone failed",
				"error", err, "job_id", jobID, "milestone", newPct)
		}
	}

	// Wire the milestone hook through the batch flusher so progress
	// crossings are checked off the same code path that updates job
	// stats. SetOnBatchFlushed is a fire-and-forget signal — failures
	// inside MaybeFireMilestones are logged, never returned.
	batchManager.SetOnBatchFlushed(jobManager.MaybeFireMilestones)

	// --- stream worker pool ---
	swpOpts := jobs.StreamWorkerOpts{
		NumWorkers:      numWorkers,
		TasksPerWorker:  tasksPerWorker,
		ReclaimInterval: 30 * time.Second,
	}

	swp := jobs.NewStreamWorkerPool(jobs.StreamWorkerDeps{
		Consumer:      consumer,
		Scheduler:     scheduler,
		Counters:      counters,
		Pacer:         pacer,
		Executor:      executor,
		BatchManager:  batchManager,
		DBQueue:       dbQueue,
		JobManager:    jobManager,
		HTMLPersister: htmlPersister,
	}, swpOpts)

	// Wedge watchdog: tick the heartbeat per task outcome and exit the
	// process if the heartbeat goes flat with active jobs. This is the
	// last-resort recovery when a Go-side wedge (blocked stdout, exhausted
	// resource, mutex deadlock) prevents in-process recovery. Fly's
	// restart=always policy brings the worker back with fresh state.
	heartbeat := &watchdog.Heartbeat{}
	swp.SetHeartbeat(heartbeat)

	// --- dispatcher ---
	dispatcherOpts := broker.DefaultDispatcherOpts()
	dispatcher := broker.NewDispatcher(
		redisClient, scheduler, pacer, counters,
		swp, // implements broker.JobLister
		swp, // implements broker.ConcurrencyChecker
		dispatcherOpts,
	)

	// Flip pending → running on the first successful publish for each
	// job. Without this hook, jobs created via CreateJob stay at
	// 'pending' forever (UpdateJobStatus has no other callers in the
	// production graph) and the dashboard "Starting up" pill never
	// goes away. MarkJobRunning is a guarded UPDATE so it is idempotent
	// across worker restarts. Returning the error lets the dispatcher
	// retry on the next tick if the DB call fails transiently.
	dispatcher.SetOnFirstDispatch(func(ctx context.Context, jobID string) error {
		return jobManager.MarkJobRunning(ctx, jobID)
	})

	// Self-heal lever for the running-counter drift class PR #362
	// could not fully eliminate. When CanDispatch keeps refusing
	// dispatch despite due work in the ZSET for longer than
	// REDIS_DISPATCH_STUCK_THRESHOLD_S, the dispatcher fires a single
	// reconcile against the authoritative PEL via the worker pool.
	// Rate-limited internally to one trigger per 2× threshold per job.
	dispatcher.SetReconciler(swp)

	// --- running counter DB sync ---
	counterSyncSec := envInt("REDIS_COUNTER_SYNC_INTERVAL_S", 5)
	if counterSyncSec < 1 {
		counterSyncSec = 5
	}
	syncInterval := time.Duration(counterSyncSec) * time.Second
	syncFn := broker.DefaultDBSyncFunc(pgDB.GetDB())

	// --- outbox sweeper ---
	// Drains task_outbox rows written in the same tx as tasks, guaranteeing
	// durable Redis scheduling even if the fire-and-forget OnTasksEnqueued
	// callback loses the write (Redis blip, process crash, etc.).
	outboxOpts := broker.DefaultOutboxSweeperOpts()
	if v := envInt("OUTBOX_SWEEP_INTERVAL_MS", 0); v > 0 {
		outboxOpts.Interval = time.Duration(v) * time.Millisecond
	}
	if v := envInt("OUTBOX_SWEEP_BATCH_SIZE", 0); v > 0 {
		outboxOpts.BatchSize = v
	}
	if v := envInt("OUTBOX_SWEEP_STATEMENT_TIMEOUT_MS", 0); v > 0 {
		outboxOpts.StatementTimeout = time.Duration(v) * time.Millisecond
	}
	// Sweeper reads task_outbox, which is written in the same tx as tasks
	// — so it belongs on queueDB in split deployments.
	outboxSweeper := broker.NewOutboxSweeper(queueDB.GetDB(), scheduler, outboxOpts)

	// --- broker telemetry probe ---
	// Scrapes stream length / ZSET depth / XPENDING per active job plus
	// outbox backlog, Redis PING, and pool stats every 5s. Without this,
	// the Tier 1 gauges stay at zero because they have no natural emission
	// site in the request path. Uses queueDB so the outbox gauges reflect
	// the database that actually holds task_outbox rows.
	probeOpts := broker.DefaultProbeOpts()
	probe := broker.NewProbe(redisClient, queueDB.GetDB(), swp, probeOpts)

	// --- start everything ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Persister gets a context decoupled from the main cancel: when
	// SIGTERM fires we cancel the stream worker first (so no new outcomes
	// arrive), then drain the persister under its own live context, only
	// tearing it down once Stop() returns. If the persister shared `ctx`,
	// its upload workers would exit on the very first cancel() and any
	// HTML payload enqueued during swp.Stop would land in a queue with
	// no readers — silent data loss.
	persisterCtx, persisterCancel := context.WithCancel(context.Background())
	defer persisterCancel()

	if htmlPersister != nil {
		htmlPersister.Start(persisterCtx)
	}
	swp.Start(ctx)
	go dispatcher.Run(ctx)
	go counters.StartDBSync(ctx, syncInterval, syncFn)
	go outboxSweeper.Run(ctx)
	go probe.Run(ctx)

	go startWatchdog(ctx, heartbeat, swp)

	workerLog.Info("hover worker ready",
		"workers", numWorkers,
		"tasks_per_worker", tasksPerWorker,
		"task_slots", numWorkers*tasksPerWorker,
		"machine", machineName,
	)

	// --- graceful shutdown ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	workerLog.Info("shutdown signal received", "signal", sig.String())

	cancel() // stop dispatcher, counter sync, and all workers

	// Wait for workers to drain (with timeout).
	done := make(chan struct{})
	go func() {
		swp.Stop()
		// Stop the persister AFTER the worker pool has drained so any
		// final outcomes still in flight reach the queue before we close
		// it. Persister.Stop runs under persisterCtx (still live), waits
		// for in-flight uploads + the final metadata flush, then tears
		// down its own ctx. We then release persisterCancel so any leaks
		// on a Stop bug don't survive shutdown.
		if htmlPersister != nil {
			htmlPersister.Stop()
		}
		persisterCancel()
		close(done)
	}()

	select {
	case <-done:
		workerLog.Info("all workers stopped cleanly")
	case <-time.After(3 * time.Minute):
		workerLog.Warn("shutdown timed out after 3 minutes")
	}
}

// --- helpers ---

// flushAsyncLogs drains the async stdout buffer (if installed) so
// logs queued at process exit reach the platform log shipper.
// Idempotent and safe in dev mode (StdoutAsync returns nil).
func flushAsyncLogs() {
	if a := logging.StdoutAsync(); a != nil {
		a.Close()
	}
}

// startWatchdog wires the wedge watchdog using the stream worker's
// active-jobs view as the "should be working" signal. Extracted from
// main to keep main's cyclomatic complexity bounded.
//
// StallThreshold is sized to comfortably exceed the per-task context
// timeout in stream_worker.processTask (2 minutes). Heartbeat ticks
// only fire after handleOutcome returns, so a single long-running task
// — or a brief pacer-throttled idle while jobs are alive — can leave
// the heartbeat flat for up to one task budget. 3 minutes provides
// 60s margin against the worst single-task case while still catching
// genuine wedges within minutes.
func startWatchdog(ctx context.Context, hb *watchdog.Heartbeat, swp *jobs.StreamWorkerPool) {
	watchdog.Run(ctx, hb, watchdog.Options{
		StallThreshold: 3 * time.Minute,
		CheckInterval:  15 * time.Second,
		GracePeriod:    2 * time.Minute,
		HasWork: func(checkCtx context.Context) bool {
			ids, err := swp.ActiveJobIDs(checkCtx)
			if err != nil {
				// Treat unknown work state as "yes": false-trip is
				// safer than missing a real wedge. The check has its
				// own 5s context timeout in the watchdog so this
				// can't itself wedge.
				return true
			}
			return len(ids) > 0
		},
		Logger: slog.Default().With("component", "watchdog"),
	})
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// envDuration parses a Go duration string (e.g. "250ms", "5s") from the
// environment, falling back to def for missing or unparseable values.
func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// makeTasksEnqueuedHandler returns the JobManager.OnTasksEnqueued callback
// that translates jobs.TaskScheduleEntry into broker.ScheduleEntry and pushes
// them onto the Redis ZSET. Pulled out of main() so the boot sequence stays
// under the cyclomatic-complexity budget; callers are not expected to share
// this handler.
//
// Respects each entry's RunAt so waiting/retry rows keep their backoff
// deadline instead of being scheduled as ready-now.
func makeTasksEnqueuedHandler(scheduler *broker.Scheduler, log *logging.Logger) func(context.Context, string, []jobs.TaskScheduleEntry) {
	return func(ctx context.Context, jobID string, entries []jobs.TaskScheduleEntry) {
		schedEntries := make([]broker.ScheduleEntry, 0, len(entries))
		for _, e := range entries {
			if e.Status == "skipped" {
				continue
			}
			runAt := e.RunAt
			if runAt.IsZero() {
				runAt = time.Now()
			}
			schedEntries = append(schedEntries, broker.ScheduleEntry{
				TaskID:     e.TaskID,
				JobID:      jobID,
				PageID:     e.PageID,
				Host:       e.Host,
				Path:       e.Path,
				Priority:   e.Priority,
				RetryCount: e.RetryCount,
				SourceType: e.SourceType,
				SourceURL:  e.SourceURL,
				RunAt:      runAt,
			})
		}
		if len(schedEntries) == 0 {
			return
		}
		if err := scheduler.ScheduleBatch(ctx, schedEntries); err != nil {
			log.Error("failed to schedule tasks into Redis",
				"error", err, "job_id", jobID, "count", len(schedEntries))
		}
	}
}

// buildHTMLPersister constructs the direct-to-R2 HTML persister, or returns
// nil when the archive env is unset (local dev) or the cold-storage provider
// can't be built. Extracted from main() to keep the boot sequence's
// cyclomatic complexity within the linter budget — the persister setup has
// its own cluster of nested branches that don't belong inline.
func buildHTMLPersister(log *logging.Logger, dbQueue *db.DbQueue) *jobs.HTMLPersister {
	// Distinguish "fully disabled" (both unset — legitimate local dev) from
	// "partially configured" (one unset — almost certainly a deploy typo).
	// Treating partial config as disabled would silently recreate the
	// missing-capture failure mode this stage is meant to fix.
	provider := strings.TrimSpace(os.Getenv("ARCHIVE_PROVIDER"))
	bucket := strings.TrimSpace(os.Getenv("ARCHIVE_BUCKET"))
	switch {
	case provider == "" && bucket == "":
		log.Info("ARCHIVE_PROVIDER/ARCHIVE_BUCKET unset — html persister disabled")
		return nil
	case provider == "" || bucket == "":
		log.Fatal("html persister misconfigured — set both ARCHIVE_PROVIDER and ARCHIVE_BUCKET, or neither",
			"archive_provider_set", provider != "",
			"archive_bucket_set", bucket != "")
	}

	archCfg := archive.ConfigFromEnv()
	if archCfg == nil {
		log.Fatal("html persister: ConfigFromEnv returned nil despite provider+bucket set")
	}

	coldProvider, err := archive.ProviderFromEnv()
	if err != nil {
		log.Fatal("failed to construct cold storage provider for html persister", "error", err)
	}
	if coldProvider == nil {
		log.Warn("ARCHIVE_PROVIDER set but provider construction returned nil; html persistence disabled")
		return nil
	}

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := coldProvider.Ping(pingCtx, archCfg.Bucket); err != nil {
		log.Fatal("html persister bucket ping failed", "error", err, "bucket", archCfg.Bucket)
	}

	cfg := jobs.DefaultHTMLPersisterConfig()
	cfg.Bucket = archCfg.Bucket
	cfg.Provider = coldProvider.Provider()
	if v := envInt("HTML_PERSIST_WORKERS", 0); v > 0 {
		cfg.Workers = v
	}
	if v := envInt("HTML_PERSIST_QUEUE", 0); v > 0 {
		cfg.QueueSize = v
	}
	if v := envInt("HTML_PERSIST_BATCH_SIZE", 0); v > 0 {
		cfg.BatchSize = v
	}
	if d := envDuration("HTML_PERSIST_FLUSH_INTERVAL", 0); d > 0 {
		cfg.FlushInterval = d
	}
	if d := envDuration("HTML_PERSIST_UPLOAD_TIMEOUT", 0); d > 0 {
		cfg.UploadTimeout = d
	}

	persister, err := jobs.NewHTMLPersister(cfg, jobs.HTMLPersisterDeps{
		Provider: coldProvider,
		DBQueue:  dbQueue,
	})
	if err != nil {
		log.Fatal("failed to construct html persister", "error", err)
	}
	log.Info("html persister wired",
		"bucket", cfg.Bucket,
		"provider", cfg.Provider,
		"workers", cfg.Workers,
		"queue", cfg.QueueSize,
		"batch_size", cfg.BatchSize,
	)
	return persister
}

// sweepOrphanInflightOnBoot scans hover:dom:flight:* and removes
// per-job fields whose jobID is no longer in the active Postgres set.
//
// dom:flight is incremented by the dispatcher on publish and
// decremented by the worker on pacer.Release; a hard SIGKILL (Fly OOM,
// panic, force-redeploy) bypasses the graceful shutdown that drains
// in-flight tasks, so the increment runs but the decrement never does.
// Unlike the running-counter HASH there is no dedicated reconciler for
// dom:flight, so drift accumulates across every restart on every
// domain that had work in flight at SIGTERM. Run once on boot, before
// the dispatcher starts, so new dispatches in this run write into a
// clean slate. Active set comes from Postgres to avoid wiping fields
// for jobs the dispatcher will resume.
//
// All failures are logged and tolerated: the sweep is a best-effort
// hygiene pass, not load-bearing for correctness.
func sweepOrphanInflightOnBoot(redisClient *broker.Client, sqlDB *sql.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var activeJobIDs []string
	// 'initializing' is a live pre-running state (sitemap fetch + parse)
	// that the rest of the lifecycle treats as active — including
	// MarkJobRunning, which flips initializing → running on the
	// dispatcher's first publish. Excluding it here would let the sweep
	// wipe dom:flight entries for jobs that are about to dispatch.
	err := sqlDB.QueryRowContext(ctx, `
		SELECT COALESCE(array_agg(id::text), ARRAY[]::text[])
		  FROM jobs
		 WHERE status IN ('running', 'pending', 'initializing')
	`).Scan(pq.Array(&activeJobIDs))
	if err != nil {
		workerLog.Warn("dom:flight orphan sweep skipped — active job query failed",
			"error", err)
		return
	}

	removed, err := redisClient.SweepOrphanInflight(ctx, activeJobIDs)
	if err != nil {
		workerLog.Warn("dom:flight orphan sweep failed", "error", err)
		return
	}
	if removed > 0 {
		workerLog.Info("dom:flight orphan sweep complete",
			"fields_removed", removed, "active_jobs", len(activeJobIDs))
	}
}

// Compile-time interface checks.
var (
	_ broker.JobLister          = (*jobs.StreamWorkerPool)(nil)
	_ broker.ConcurrencyChecker = (*jobs.StreamWorkerPool)(nil)
	_ broker.Reconciler         = (*jobs.StreamWorkerPool)(nil)
)
