package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"runtime/trace"

	"github.com/Harvey-AU/hover/internal/api"
	"github.com/Harvey-AU/hover/internal/broker"
	"github.com/Harvey-AU/hover/internal/crawler"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/jobs"
	"github.com/Harvey-AU/hover/internal/logging"
	"github.com/Harvey-AU/hover/internal/loops"
	"github.com/Harvey-AU/hover/internal/notifications"
	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/getsentry/sentry-go"
	"github.com/joho/godotenv"
	"golang.org/x/time/rate"
)

var startupLog = logging.Component("startup")

const (
	schedulerTickInterval   = 30 * time.Second
	schedulerBatchSize      = 50
	completionCheckInterval = 30 * time.Second
	healthCheckInterval     = 5 * time.Minute
)

// startJobScheduler starts background service to create jobs from schedulers
// It respects context cancellation for graceful shutdown
// The WaitGroup must be marked Done when this function exits
func startJobScheduler(ctx context.Context, wg *sync.WaitGroup, jobsManager *jobs.JobManager, pgDB *db.DB) {
	defer wg.Done()

	ticker := time.NewTicker(schedulerTickInterval)
	defer ticker.Stop()

	startupLog.Info("Job scheduler started")

	for {
		select {
		case <-ctx.Done():
			startupLog.Info("Job scheduler stopped")
			return
		case <-ticker.C:
			schedulers, err := pgDB.GetSchedulersReadyToRun(ctx, schedulerBatchSize)
			if err != nil {
				// Warn not Error: this loop retries every 30s, so auto-capture
				// would flood Sentry during any transient Postgres outage.
				startupLog.Warn("Failed to get schedulers ready to run", "error", err)
				continue
			}

			// Batch lookup all domain names to avoid N+1 queries
			domainIDs := make([]int, 0, len(schedulers))
			for _, scheduler := range schedulers {
				domainIDs = append(domainIDs, scheduler.DomainID)
			}

			domainNames, err := pgDB.GetDomainNames(ctx, domainIDs)
			if err != nil {
				startupLog.Warn("Failed to get domain names for schedulers", "error", err)
				continue
			}

			for _, scheduler := range schedulers {
				domainName, ok := domainNames[scheduler.DomainID]
				if !ok {
					startupLog.Warn("Domain name not found", "domain_id", scheduler.DomainID, "scheduler_id", scheduler.ID)
					continue
				}

				// Check if a job started too recently (within half the schedule interval)
				lastJobStart, err := pgDB.GetLastJobStartTimeForScheduler(ctx, scheduler.ID)
				if err != nil {
					startupLog.Warn("Failed to get last job start time", "error", err, "scheduler_id", scheduler.ID)
					continue
				}

				if lastJobStart != nil {
					minInterval := time.Duration(scheduler.ScheduleIntervalHours) * time.Hour / 2
					timeSinceLastJob := time.Since(*lastJobStart)

					if timeSinceLastJob < minInterval {
						startupLog.Info("Skipping scheduled job - last job started too recently",
							"scheduler_id", scheduler.ID,
							"domain", domainName,
							"time_since_last_job", timeSinceLastJob,
							"minimum_interval", minInterval,
						)

						// Update next_run_at to the next valid time slot
						nextRun := time.Now().UTC().Add(time.Duration(scheduler.ScheduleIntervalHours) * time.Hour)
						if err := pgDB.UpdateSchedulerNextRun(ctx, scheduler.ID, nextRun); err != nil {
							startupLog.Warn("Failed to update scheduler next run", "error", err, "scheduler_id", scheduler.ID)
						}
						continue
					}
				}

				// Create JobOptions from scheduler
				sourceType := "scheduler"
				opts := &jobs.JobOptions{
					Domain:                   domainName,
					OrganisationID:           &scheduler.OrganisationID,
					UseSitemap:               true,
					Concurrency:              scheduler.Concurrency,
					FindLinks:                scheduler.FindLinks,
					AllowCrossSubdomainLinks: true,
					MaxPages:                 scheduler.MaxPages,
					IncludePaths:             scheduler.IncludePaths,
					ExcludePaths:             scheduler.ExcludePaths,
					RequiredWorkers:          scheduler.RequiredWorkers,
					SourceType:               &sourceType,
					SourceDetail:             &scheduler.ID,
					SchedulerID:              &scheduler.ID,
				}

				// Create job (standard flow)
				job, err := jobsManager.CreateJob(ctx, opts)
				if err != nil {
					startupLog.Warn("Failed to create scheduled job", "error", err, "scheduler_id", scheduler.ID)
					continue
				}

				// Update scheduler next_run_at
				nextRun := time.Now().UTC().Add(time.Duration(scheduler.ScheduleIntervalHours) * time.Hour)
				if err := pgDB.UpdateSchedulerNextRun(ctx, scheduler.ID, nextRun); err != nil {
					startupLog.Warn("Failed to update scheduler next run", "error", err, "scheduler_id", scheduler.ID)
				} else {
					startupLog.Info("Created scheduled job",
						"scheduler_id", scheduler.ID,
						"job_id", job.ID,
						"domain", domainName,
						"next_run_at", nextRun,
					)
				}
			}
		}
	}
}

// startHealthMonitoring starts background monitoring for job completion and system health
// It respects context cancellation for graceful shutdown
// The WaitGroup must be marked Done when this function exits
func startHealthMonitoring(ctx context.Context, wg *sync.WaitGroup, pgDB *db.DB) {
	defer wg.Done() // Signal completion when exiting

	completionTicker := time.NewTicker(completionCheckInterval)
	defer completionTicker.Stop()

	healthTicker := time.NewTicker(healthCheckInterval)
	defer healthTicker.Stop()

	// Helper to check job completion
	checkJobCompletion := func() {
		rows, err := pgDB.GetDB().Query(`
			UPDATE jobs
			SET status = 'completed', completed_at = NOW()
			WHERE (completed_tasks + failed_tasks) >= (total_tasks - COALESCE(skipped_tasks, 0))
			  AND status = 'running'
			RETURNING id
		`)
		if err != nil {
			startupLog.Error("Failed to update completed jobs", "error", err)
			return
		}
		defer rows.Close()

		for rows.Next() {
			var jobID string
			if err := rows.Scan(&jobID); err == nil {
				startupLog.Info("Job marked as completed", "job_id", jobID)
			}
		}
	}

	// Helper to check system health (stuck jobs/tasks)
	checkSystemHealth := func() {
		// Check for stuck jobs - get total count first
		var totalStuckJobs int
		err := pgDB.GetDB().QueryRow(`
			SELECT COUNT(*)
			FROM jobs j
			WHERE j.status = 'running'
			  AND j.progress = 0
			  AND j.started_at < NOW() - INTERVAL '15 minutes'
		`).Scan(&totalStuckJobs)

		if err != nil {
			startupLog.Error("Failed to count stuck jobs", "error", err)
		}

		// Get sample of stuck jobs for details
		type stuckJobInfo struct {
			ID        string
			DomainID  int
			StartedAt time.Time
			Progress  float64
		}
		var stuckJobs []stuckJobInfo

		if totalStuckJobs > 0 {
			stuckJobRows, err := pgDB.GetDB().Query(`
				SELECT j.id, j.domain_id, j.started_at, j.progress
				FROM jobs j
				WHERE j.status = 'running'
				  AND j.progress = 0
				  AND j.started_at < NOW() - INTERVAL '15 minutes'
				ORDER BY j.started_at ASC
				LIMIT 10
			`)
			if err != nil {
				startupLog.Error("Failed to query stuck jobs sample", "error", err)
			} else {
				defer stuckJobRows.Close()
				for stuckJobRows.Next() {
					var job stuckJobInfo
					if err := stuckJobRows.Scan(&job.ID, &job.DomainID, &job.StartedAt, &job.Progress); err == nil {
						stuckJobs = append(stuckJobs, job)
					}
				}
			}
		}

		if totalStuckJobs > 0 && len(stuckJobs) > 0 {
			jobIDs := make([]string, len(stuckJobs))
			for i, job := range stuckJobs {
				jobIDs[i] = job.ID
			}

			startupLog.Error("CRITICAL: Jobs stuck without progress for >15 minutes",
				"total_stuck_jobs", totalStuckJobs,
				"sample_count", len(stuckJobs),
				"sample_job_ids", jobIDs,
				"oldest_job", stuckJobs[0].ID,
				"oldest_started_at", stuckJobs[0].StartedAt,
			)
		}

		// Check for stuck tasks - get total counts first
		var totalStuckTasks int
		var totalAffectedJobs int

		err = pgDB.GetDB().QueryRow(`
			SELECT COUNT(*), COUNT(DISTINCT job_id)
			FROM tasks
			WHERE status = 'running'
			  AND started_at < NOW() - INTERVAL '3 minutes'
		`).Scan(&totalStuckTasks, &totalAffectedJobs)

		if err != nil {
			startupLog.Error("Failed to count stuck tasks", "error", err)
		}

		// Get sample of stuck tasks for details
		type stuckTaskInfo struct {
			ID         string
			JobID      string
			Path       string
			StartedAt  time.Time
			RetryCount int
		}
		var stuckTasks []stuckTaskInfo

		if totalStuckTasks > 0 {
			stuckTaskRows, err := pgDB.GetDB().Query(`
				SELECT t.id, t.job_id, p.path, t.started_at, t.retry_count
				FROM tasks t
				JOIN pages p ON t.page_id = p.id
				WHERE t.status = 'running'
				  AND t.started_at < NOW() - INTERVAL '3 minutes'
				ORDER BY t.started_at ASC
				LIMIT 20
			`)
			if err != nil {
				startupLog.Error("Failed to query stuck tasks sample", "error", err)
			} else {
				defer stuckTaskRows.Close()
				for stuckTaskRows.Next() {
					var task stuckTaskInfo
					if err := stuckTaskRows.Scan(&task.ID, &task.JobID, &task.Path, &task.StartedAt, &task.RetryCount); err == nil {
						stuckTasks = append(stuckTasks, task)
					}
				}
			}
		}

		if totalStuckTasks > 0 && len(stuckTasks) > 0 {
			// Get unique job IDs from sample for context
			jobIDMap := make(map[string]struct{})
			for _, task := range stuckTasks {
				jobIDMap[task.JobID] = struct{}{}
			}
			sampleJobIDs := make([]string, 0, len(jobIDMap))
			for jobID := range jobIDMap {
				sampleJobIDs = append(sampleJobIDs, jobID)
			}

			startupLog.Error("CRITICAL: Tasks stuck in running state for >3 minutes",
				"total_stuck_tasks", totalStuckTasks,
				"total_affected_jobs", totalAffectedJobs,
				"sample_task_count", len(stuckTasks),
				"sample_job_count", len(sampleJobIDs),
				"sample_job_ids", sampleJobIDs,
				"oldest_task", stuckTasks[0].ID,
				"oldest_stuck_at", stuckTasks[0].StartedAt,
			)
		}
	}

	// Run initial checks
	checkJobCompletion()

	startupLog.Info("Health monitoring started")

	for {
		select {
		case <-ctx.Done():
			startupLog.Info("Health monitoring stopped")
			return
		case <-completionTicker.C:
			checkJobCompletion()
		case <-healthTicker.C:
			checkSystemHealth()
		}
	}
}

// Config holds the application configuration loaded from environment variables
type Config struct {
	Port                  string // HTTP port to listen on
	Env                   string // Environment (development/production)
	SentryDSN             string // Sentry DSN for error tracking
	LogLevel              string // Log level (debug, info, warn, error)
	FlightRecorderEnabled bool   // Flight recorder for performance debugging
	ObservabilityEnabled  bool   // Toggle OpenTelemetry + Prometheus exporters
	MetricsAddr           string // Address for Prometheus metrics endpoint (":9464" style)
	OTLPEndpoint          string // OTLP HTTP endpoint for trace export
	OTLPHeaders           string // Comma separated headers for OTLP exporter
	OTLPInsecure          bool   // Disable TLS verification for OTLP exporter
}

//nolint:gocyclo // main function setup is naturally complex but straightforward setup logic
func main() {
	// Parse command line flags
	logLevelFlag := flag.String("log-level", "", "Log level (debug, info, warn, error) - overrides LOG_LEVEL env var")
	flag.Parse()

	// Load .env files - .env.local takes priority for development
	if err := godotenv.Load(".env.local", ".env"); err != nil {
		startupLog.Debug("Notice: .env files not loaded (expected in some environments)", "error", err)
	}

	// Determine log level: command line flag takes priority over environment variable
	logLevel := getEnvWithDefault("LOG_LEVEL", "info")
	if *logLevelFlag != "" {
		logLevel = *logLevelFlag
	}

	// Load configuration
	config := &Config{
		Port:                  getEnvWithDefault("PORT", "8080"),
		Env:                   getEnvWithDefault("APP_ENV", "development"),
		SentryDSN:             os.Getenv("SENTRY_DSN"),
		LogLevel:              logLevel,
		FlightRecorderEnabled: getEnvWithDefault("FLIGHT_RECORDER_ENABLED", "false") == "true",
		ObservabilityEnabled:  getEnvWithDefault("OBSERVABILITY_ENABLED", "true") == "true",
		MetricsAddr:           getEnvWithDefault("METRICS_ADDR", ":9464"),
		OTLPEndpoint:          os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		OTLPHeaders:           os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"),
		OTLPInsecure:          getEnvWithDefault("OTEL_EXPORTER_OTLP_INSECURE", "false") == "true",
	}

	// Start flight recorder if enabled
	if config.FlightRecorderEnabled {
		f, err := os.Create("trace.out")
		if err != nil {
			startupLog.Fatal("failed to create trace file", "error", err)
		}

		if err := trace.Start(f); err != nil {
			startupLog.Fatal("failed to start flight recorder", "error", err)
		}
		startupLog.Info("Flight recorder enabled, writing to trace.out")

		// Defer closing the trace and the file to the shutdown sequence
		defer func() {
			trace.Stop()
			if err := f.Close(); err != nil {
				startupLog.Error("failed to close trace file", "error", err)
			}
			startupLog.Info("Flight recorder stopped and trace file closed.")
		}()
	}

	var err error

	// Initialise Sentry before setupLogging so the fanout handler wires
	// the Sentry client that already exists, not a nil one.
	if config.SentryDSN != "" {
		err := sentry.Init(sentry.ClientOptions{
			Dsn:         config.SentryDSN,
			Environment: config.Env,
			TracesSampleRate: func() float64 {
				if config.Env == "production" {
					return 0.1 // 10% sampling in production
				}
				return 1.0 // 100% sampling in development
			}(),
			AttachStacktrace: true,
			Debug:            config.Env == "development",
			BeforeSend:       logging.BeforeSend,
		})
		if err != nil {
			startupLog.Warn("Failed to initialise Sentry", "error", err)
		} else {
			startupLog.Info("Sentry initialised successfully", "environment", config.Env)
			// Ensure Sentry flushes before application exits
			defer sentry.Flush(2 * time.Second)
		}
	} else {
		startupLog.Warn("Sentry DSN not configured, error tracking disabled")
	}

	setupLogging(config)

	var (
		obsProviders *observability.Providers
		metricsSrv   *http.Server
	)

	if config.ObservabilityEnabled {
		obsProviders, err = observability.Init(context.Background(), observability.Config{
			Enabled:        true,
			ServiceName:    "hover",
			Environment:    config.Env,
			OTLPEndpoint:   strings.TrimSpace(config.OTLPEndpoint),
			OTLPHeaders:    parseOTLPHeaders(config.OTLPHeaders),
			OTLPInsecure:   config.OTLPInsecure,
			MetricsAddress: config.MetricsAddr,
		})
		if err != nil {
			startupLog.Warn("Failed to initialise observability providers", "error", err)
		} else {
			defer func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := obsProviders.Shutdown(shutdownCtx); err != nil {
					startupLog.Warn("Failed to flush telemetry providers cleanly", "error", err)
				}
			}()

			if obsProviders.MetricsHandler != nil && config.MetricsAddr != "" {
				metricsSrv = &http.Server{
					Addr:              config.MetricsAddr,
					Handler:           obsProviders.MetricsHandler,
					ReadHeaderTimeout: 5 * time.Second,
				}

				// Bind first, then log readiness so a bind failure surfaces as an
				// error instead of a misleading "listening" line followed by a
				// silent Serve crash.
				metricsListener, err := net.Listen("tcp", config.MetricsAddr)
				if err != nil {
					startupLog.Error("Metrics server failed to bind", "error", err, "addr", config.MetricsAddr)
				} else {
					go func() {
						startupLog.Info("Metrics server listening", "addr", config.MetricsAddr)
						if err := metricsSrv.Serve(metricsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
							startupLog.Error("Metrics server failed", "error", err)
						}
					}()

					defer func() {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						if err := metricsSrv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
							startupLog.Warn("Graceful shutdown of metrics server failed", "error", err)
						}
					}()
				}
			}
		}
	}

	appEnv := os.Getenv("APP_ENV")

	// Connect to PostgreSQL with retry logic to handle temporary unavailability
	// Use a generous timeout to allow for Supabase maintenance windows
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer dbCancel()

	pgDB, err := db.WaitForDatabase(dbCtx, 5*time.Minute)
	if err != nil {
		startupLog.Fatal("Failed to connect to PostgreSQL database", "error", err)
	}
	// Defer DB close - will execute AFTER worker pool stops due to defer LIFO order
	defer func() {
		startupLog.Info("Closing database connections")
		// Give connections time to drain gracefully
		time.Sleep(1 * time.Second)
		if err := pgDB.Close(); err != nil {
			startupLog.Error("Error closing database", "error", err)
		}
		startupLog.Info("Database closed")
	}()

	startupLog.Info("Connected to PostgreSQL database")

	queueDB := pgDB
	if queueURL := strings.TrimSpace(os.Getenv("DATABASE_QUEUE_URL")); queueURL != "" {
		// Create fresh context for queue connection to ensure full retry budget
		// (primary connection may have consumed most of the shared context)
		queueCtx, queueCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer queueCancel()

		queueConn, err := db.InitFromURLWithSuffixRetry(queueCtx, queueURL, appEnv, "queue")
		if err != nil {
			startupLog.Fatal("Failed to connect to queue PostgreSQL database", "error", err)
		}

		defer func() {
			startupLog.Info("Closing queue database connections")
			time.Sleep(500 * time.Millisecond)
			if err := queueConn.Close(); err != nil {
				startupLog.Error("Error closing queue database", "error", err)
			}
			startupLog.Info("Queue database closed")
		}()

		queueDB = queueConn
		startupLog.Info("Queue database connection established")
	}

	// Initialise crawler
	crawlerConfig := crawler.DefaultConfig()
	cr := crawler.New(crawlerConfig) // QUESTION: Should we change cr to crawler for clarity, as others have clearer names.

	// Create database queue for operations
	dbQueue := db.NewDbQueue(queueDB)

	// --- Redis scheduler (optional — tasks are dispatched via Redis, consumed by the worker service) ---
	redisCfg := broker.ConfigFromEnv()

	// Create job manager (no local worker pool — tasks are consumed by the worker service)
	jobsManager := jobs.NewJobManager(pgDB.GetDB(), dbQueue, cr)

	if redisCfg.URL != "" {
		redisClient, err := broker.NewClient(redisCfg)
		if err != nil {
			startupLog.Fatal("failed to create Redis client", "error", err)
		}
		defer redisClient.Close()

		if err := redisClient.Ping(context.Background()); err != nil {
			startupLog.Fatal("failed to ping Redis", "error", err)
		}
		startupLog.Info("connected to Redis")

		scheduler := broker.NewScheduler(redisClient)

		// Wire callback: when tasks are inserted into Postgres, schedule them into Redis
		jobsManager.OnTasksEnqueued = func(ctx context.Context, jobID string, entries []jobs.TaskScheduleEntry) {
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
					startupLog.Error("failed to schedule tasks into Redis",
						"error", err, "job_id", jobID, "count", len(schedEntries))
				}
			}
		}
	} else {
		startupLog.Warn("REDIS_URL not set — task dispatch to Redis is disabled; API will still create tasks in Postgres")
	}

	// Create notification service with Slack channel
	notificationService := notifications.NewService(pgDB)
	slackChannel, err := notifications.NewSlackChannel(pgDB)
	if err != nil {
		startupLog.Warn("Failed to create Slack channel - notifications disabled", "error", err)
	} else {
		notificationService.AddChannel(slackChannel)
		startupLog.Info("Slack notification channel enabled")
	}

	// Create context for background goroutines that need graceful shutdown
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel() // Ensure context is cancelled on exit

	// WaitGroup to track background goroutines for clean shutdown
	var backgroundWG sync.WaitGroup

	// Create a rate limiter
	limiter := newRateLimiter()

	// Check GA4 integration availability
	googleClientID := os.Getenv("GOOGLE_CLIENT_ID")
	googleClientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	if googleClientID == "" || googleClientSecret == "" {
		startupLog.Info("GA4 integration unavailable: GOOGLE_CLIENT_ID or GOOGLE_CLIENT_SECRET not configured")
	}

	// Initialise Loops email client (nil-safe for dev environments)
	var loopsClient *loops.Client
	if loopsAPIKey := os.Getenv("LOOPS_API_KEY"); loopsAPIKey != "" {
		loopsClient = loops.New(loopsAPIKey)
		startupLog.Info("Loops email client initialised")
	} else {
		startupLog.Info("Loops email client unavailable: LOOPS_API_KEY not configured")
	}

	// Create API handler with dependencies
	apiHandler := api.NewHandler(
		pgDB,
		jobsManager,
		loopsClient,
		googleClientID,
		googleClientSecret,
	)

	// Create HTTP multiplexer
	mux := http.NewServeMux()

	// Setup API routes
	apiHandler.SetupRoutes(mux)

	// Create middleware stack with rate limiting.
	// Static assets are excluded — browsers request many files in parallel
	// on hard refresh and these are cheap to serve.
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		isStatic := strings.HasPrefix(p, "/js/") ||
			strings.HasPrefix(p, "/app/") ||
			strings.HasPrefix(p, "/styles/") ||
			strings.HasPrefix(p, "/assets/") ||
			strings.HasPrefix(p, "/web/") ||
			strings.HasPrefix(p, "/images/") ||
			p == "/config.js" ||
			p == "/favicon.ico"
		if !isStatic {
			ip := getClientIP(r)
			if !limiter.getLimiter(ip).Allow() {
				api.WriteErrorMessage(w, r, "Too many requests", http.StatusTooManyRequests, api.ErrCodeRateLimit)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})

	// Add middleware in reverse order (outermost first)
	handler = api.LoggingMiddleware(handler)
	handler = api.RequestIDMiddleware(handler)
	handler = api.SecurityHeadersMiddleware(handler)
	handler = api.CrossOriginProtectionMiddleware(handler)
	handler = api.CORSMiddleware(handler)
	handler = observability.WrapHandler(handler, obsProviders)

	// Create a new HTTP server
	server := &http.Server{
		Addr:              ":" + config.Port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second, // Fix G112: Potential Slowloris Attack
	}

	// Channel to listen for termination signals
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	// Channel to signal when the server has shut down
	done := make(chan struct{})

	// Channel to receive server errors
	serverErrCh := make(chan error, 1)

	go func() {
		startupLog.Info("Starting server", "port", config.Port)

		// Bind the listener first so that we only announce readiness once the
		// port is actually accepting connections. If the bind fails we surface
		// the error instead of claiming the server is ready at dead URLs.
		listener, err := net.Listen("tcp", ":"+config.Port)
		if err != nil {
			serverErrCh <- fmt.Errorf("failed to bind %s: %w", config.Port, err)
			return
		}

		baseURL := fmt.Sprintf("http://localhost:%s", config.Port)
		startupLog.Info("Hover server ready", "environment", config.Env)
		startupLog.Info("Open Homepage", "homepage", baseURL)
		startupLog.Info("Open Dashboard", "dashboard", baseURL+"/dashboard")
		startupLog.Info("Health Check", "health", baseURL+"/health")
		if config.Env == "development" {
			startupLog.Info("Open Supabase Studio", "supabase_studio", "http://localhost:54323")
		}

		if err := server.Serve(listener); err != nil {
			serverErrCh <- err
			return
		}
		serverErrCh <- nil
	}()

	go func() {
		<-stop
		startupLog.Info("Shutting down server...")

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			startupLog.Error("Server forced to shutdown", "error", err)
		}

		close(done)
	}()

	// Note: no local worker pool — task execution is handled by the dedicated
	// worker service (cmd/worker). The API server only schedules tasks into Redis.

	// Start background health monitoring with cancellable context
	backgroundWG.Add(1)
	go startHealthMonitoring(appCtx, &backgroundWG, pgDB)

	// Start scheduler service
	backgroundWG.Add(1)
	go startJobScheduler(appCtx, &backgroundWG, jobsManager, pgDB)

	// Start notification listener (uses polling mode with Supabase pooler)
	backgroundWG.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				startupLog.Error("Recovered panic in notification listener", "panic", r)
			}
		}()

		notifications.StartWithFallback(appCtx, pgDB.GetConfig().ConnectionString(), notificationService)
	})

	// Wait for either the server to exit or shutdown signal completion
	var serverErr error
	select {
	case serverErr = <-serverErrCh:
	case <-done:
		serverErr = <-serverErrCh
	}

	// Ensure background goroutines stop
	appCancel()
	backgroundWG.Wait()
	startupLog.Info("All background goroutines stopped")

	if serverErr != nil && !errors.Is(serverErr, http.ErrServerClosed) {
		startupLog.Fatal("Server error", "error", serverErr)
	}

	startupLog.Info("Server stopped")
}

// getEnvWithDefault retrieves an environment variable or returns a default value if not set
func getEnvWithDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func parseOTLPHeaders(raw string) map[string]string {
	headers := make(map[string]string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return headers
	}

	for pair := range strings.SplitSeq(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}

		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			continue
		}

		headers[key] = value
	}

	return headers
}

// setupLogging configures the logging system
func setupLogging(config *Config) {
	logging.Setup(logging.ParseLevel(config.LogLevel), config.Env)
}

// RateLimiter represents a rate limiting system based on client IP addresses
type RateLimiter struct {
	limits   map[string]*IPRateLimiter
	mu       sync.Mutex
	rate     rate.Limit
	capacity int
}

// IPRateLimiter wraps a token bucket rate limiter specific to an IP address
type IPRateLimiter struct {
	limiter *rate.Limiter
}

// newRateLimiter creates a new rate limiter with default settings
func newRateLimiter() *RateLimiter {
	return &RateLimiter{
		limits:   make(map[string]*IPRateLimiter),
		rate:     rate.Limit(20), // 20 requests per second for dashboard
		capacity: 10,             // 10 burst capacity
	}
}

// getLimiter returns the rate limiter for a specific IP address
func (rl *RateLimiter) getLimiter(ip string) *IPRateLimiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	limiter, exists := rl.limits[ip]
	if !exists {
		limiter = &IPRateLimiter{
			limiter: rate.NewLimiter(rl.rate, rl.capacity),
		}
		rl.limits[ip] = limiter
	}

	return limiter
}

// Allow checks if a request from this IP should be allowed
func (ipl *IPRateLimiter) Allow() bool {
	return ipl.limiter.Allow()
}

// getClientIP extracts the client's IP address from a request
func getClientIP(r *http.Request) string {
	// Check for X-Forwarded-For header first (for clients behind proxies)
	ip := r.Header.Get("X-Forwarded-For")
	if ip != "" {
		// X-Forwarded-For might contain multiple IPs, take the first one
		ips := strings.Split(ip, ",")
		ip = strings.TrimSpace(ips[0])
		return ip
	}

	// If no X-Forwarded-For, use RemoteAddr
	ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	return ip
}
