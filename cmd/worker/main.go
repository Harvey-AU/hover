// cmd/worker is the dedicated crawl worker service. It consumes
// tasks from Redis Streams, executes crawls, and persists results
// to Postgres via the batch manager.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Harvey-AU/hover/internal/broker"
	"github.com/Harvey-AU/hover/internal/crawler"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/jobs"
	"github.com/Harvey-AU/hover/internal/logging"
	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/getsentry/sentry-go"
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

	workerLog.Info("hover worker starting")

	// --- observability ---
	if os.Getenv("OBSERVABILITY_ENABLED") == "true" {
		providers, err := observability.Init(context.Background(), observability.Config{
			Enabled:      true,
			ServiceName:  "hover-worker",
			Environment:  appEnv,
			OTLPEndpoint: strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
			OTLPHeaders:  observability.ParseOTLPHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")),
		})
		if err != nil {
			workerLog.Warn("failed to initialise observability", "error", err)
		} else {
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = providers.Shutdown(ctx)
			}()
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

	// --- job manager (for EnqueueJobURLs + OnTasksEnqueued callback) ---
	jobManager := jobs.NewJobManager(pgDB.GetDB(), dbQueue, cr)
	// Respect each entry's RunAt so waiting/retry rows keep their backoff
	// deadline instead of being scheduled as ready-now.
	jobManager.OnTasksEnqueued = func(ctx context.Context, jobID string, entries []jobs.TaskScheduleEntry) {
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
		if len(schedEntries) > 0 {
			if err := scheduler.ScheduleBatch(ctx, schedEntries); err != nil {
				workerLog.Error("failed to schedule tasks into Redis",
					"error", err, "job_id", jobID, "count", len(schedEntries))
			}
		}
	}

	// --- stream worker pool ---
	swpOpts := jobs.StreamWorkerOpts{
		NumWorkers:      numWorkers,
		TasksPerWorker:  tasksPerWorker,
		ReclaimInterval: 30 * time.Second,
	}

	swp := jobs.NewStreamWorkerPool(jobs.StreamWorkerDeps{
		Consumer:     consumer,
		Scheduler:    scheduler,
		Counters:     counters,
		Pacer:        pacer,
		Executor:     executor,
		BatchManager: batchManager,
		DBQueue:      dbQueue,
		JobManager:   jobManager,
	}, swpOpts)

	// --- dispatcher ---
	dispatcherOpts := broker.DefaultDispatcherOpts()
	dispatcher := broker.NewDispatcher(
		redisClient, scheduler, pacer, counters,
		swp, // implements broker.JobLister
		swp, // implements broker.ConcurrencyChecker
		dispatcherOpts,
	)

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

	swp.Start(ctx)
	go dispatcher.Run(ctx)
	go counters.StartDBSync(ctx, syncInterval, syncFn)
	go outboxSweeper.Run(ctx)
	go probe.Run(ctx)

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

// Compile-time interface checks.
var (
	_ broker.JobLister          = (*jobs.StreamWorkerPool)(nil)
	_ broker.ConcurrencyChecker = (*jobs.StreamWorkerPool)(nil)
)
