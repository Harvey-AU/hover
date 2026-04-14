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
	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/getsentry/sentry-go"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	// --- logging ---
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	if os.Getenv("APP_ENV") == "development" {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}

	log.Info().Msg("hover worker starting")

	// --- sentry ---
	if dsn := os.Getenv("SENTRY_DSN"); dsn != "" {
		if err := sentry.Init(sentry.ClientOptions{
			Dsn:         dsn,
			Environment: os.Getenv("APP_ENV"),
		}); err != nil {
			log.Warn().Err(err).Msg("failed to initialise Sentry")
		} else {
			defer sentry.Flush(2 * time.Second)
		}
	}

	// --- observability ---
	if os.Getenv("OBSERVABILITY_ENABLED") == "true" {
		providers, err := observability.Init(context.Background(), observability.Config{
			Enabled:      true,
			ServiceName:  "hover-worker",
			Environment:  os.Getenv("APP_ENV"),
			OTLPEndpoint: strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
			OTLPHeaders:  parseOTLPHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")),
		})
		if err != nil {
			log.Warn().Err(err).Msg("failed to initialise observability")
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
		sentry.CaptureException(err)
		log.Fatal().Err(err).Msg("failed to connect to PostgreSQL")
	}
	defer func() {
		log.Info().Msg("closing database connection")
		// Allow in-flight batch manager flushes and counter syncs to complete
		// before tearing down the connection pool.
		time.Sleep(1 * time.Second)
		_ = pgDB.Close()
	}()

	queueDB := pgDB
	if queueURL := strings.TrimSpace(os.Getenv("DATABASE_QUEUE_URL")); queueURL != "" {
		queueCtx, queueCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer queueCancel()
		qConn, err := db.InitFromURLWithSuffixRetry(queueCtx, queueURL, os.Getenv("APP_ENV"), "queue")
		if err != nil {
			sentry.CaptureException(err)
			log.Fatal().Err(err).Msg("failed to connect to queue PostgreSQL")
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
	redisClient, err := broker.NewClient(redisCfg, log.Logger)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create Redis client")
	}
	defer redisClient.Close()

	if err := redisClient.Ping(context.Background()); err != nil {
		log.Fatal().Err(err).Msg("failed to ping Redis")
	}
	log.Info().Msg("connected to Redis")

	// --- broker components ---
	scheduler := broker.NewScheduler(redisClient, log.Logger)
	pacerCfg := broker.DefaultPacerConfig()
	pacer := broker.NewDomainPacer(redisClient, pacerCfg, log.Logger)
	counters := broker.NewRunningCounters(redisClient, log.Logger)

	machineName := os.Getenv("FLY_MACHINE_ID")
	if machineName == "" {
		machineName, _ = os.Hostname()
	}

	numWorkers := envInt("WORKER_COUNT", 30)
	if numWorkers < 1 {
		log.Fatal().Int("value", numWorkers).Msg("WORKER_COUNT must be >= 1")
	}
	consumerOpts := broker.DefaultConsumerOpts(fmt.Sprintf("worker-%s", machineName))
	consumer := broker.NewConsumer(redisClient, consumerOpts, log.Logger)

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
	jobManager.OnTasksEnqueued = func(ctx context.Context, jobID string, entries []jobs.TaskScheduleEntry) {
		schedEntries := make([]broker.ScheduleEntry, 0, len(entries))
		for _, e := range entries {
			if e.Status == "skipped" {
				continue
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
				RunAt:      time.Now(),
			})
		}
		if len(schedEntries) > 0 {
			if err := scheduler.ScheduleBatch(ctx, schedEntries); err != nil {
				log.Error().Err(err).Str("job_id", jobID).Int("count", len(schedEntries)).
					Msg("failed to schedule tasks into Redis")
			}
		}
	}

	// --- stream worker pool ---
	swpOpts := jobs.StreamWorkerOpts{
		NumWorkers:      numWorkers,
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
		Logger:       log.Logger,
	}, swpOpts)

	// --- dispatcher ---
	dispatcherOpts := broker.DefaultDispatcherOpts()
	dispatcher := broker.NewDispatcher(
		redisClient, scheduler, pacer, counters,
		swp, // implements broker.JobLister
		swp, // implements broker.ConcurrencyChecker
		dispatcherOpts,
		log.Logger,
	)

	// --- running counter DB sync ---
	counterSyncSec := envInt("REDIS_COUNTER_SYNC_INTERVAL_S", 5)
	if counterSyncSec < 1 {
		counterSyncSec = 5
	}
	syncInterval := time.Duration(counterSyncSec) * time.Second
	syncFn := broker.DefaultDBSyncFunc(pgDB.GetDB())

	// --- start everything ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	swp.Start(ctx)
	go dispatcher.Run(ctx)
	go counters.StartDBSync(ctx, syncInterval, syncFn)

	log.Info().
		Int("workers", numWorkers).
		Str("machine", machineName).
		Msg("hover worker ready")

	// --- graceful shutdown ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Info().Str("signal", sig.String()).Msg("shutdown signal received")

	cancel() // stop dispatcher, counter sync, and all workers

	// Wait for workers to drain (with timeout).
	done := make(chan struct{})
	go func() {
		swp.Stop()
		close(done)
	}()

	select {
	case <-done:
		log.Info().Msg("all workers stopped cleanly")
	case <-time.After(3 * time.Minute):
		log.Warn().Msg("shutdown timed out after 3 minutes")
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

func parseOTLPHeaders(raw string) map[string]string {
	headers := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		parts := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(parts) == 2 {
			headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return headers
}

// Compile-time interface checks.
var (
	_ broker.JobLister          = (*jobs.StreamWorkerPool)(nil)
	_ broker.ConcurrencyChecker = (*jobs.StreamWorkerPool)(nil)
)
