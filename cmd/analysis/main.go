// cmd/analysis is the dedicated lighthouse audit service. It consumes
// per-job streams (stream:{jobID}:lh) populated by the crawl-side
// dispatcher when a milestone fires, runs each audit through a Runner,
// and writes the results back into lighthouse_runs.
//
// Phase 2 ships this as a stub-runner skeleton — the Go binary, stream
// consumer, and result-write loop. Phase 3 layers Chromium and the
// lighthouse npm package on top via Dockerfile.analysis and swaps the
// stub runner for a localRunner that shells out to the real binary.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Harvey-AU/hover/internal/archive"
	"github.com/Harvey-AU/hover/internal/broker"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/lighthouse"
	"github.com/Harvey-AU/hover/internal/logging"
	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/getsentry/sentry-go"
	"github.com/redis/go-redis/v9"
)

var analysisLog = logging.Component("analysis")

// stream-message field names. Mirror dispatcher.publishAndRemove so a
// drift between producer and consumer is a quick grep away.
const (
	streamFieldRunID = "lighthouse_run_id"
	streamFieldJobID = "job_id"
	streamFieldURL   = "source_url"
	streamFieldHost  = "host"
	streamFieldPath  = "path"
)

func main() {
	appEnv := os.Getenv("APP_ENV")

	// --- sentry first so logging.Setup can wire its handler ---
	if dsn := os.Getenv("SENTRY_DSN"); dsn != "" {
		if err := sentry.Init(sentry.ClientOptions{
			Dsn:              dsn,
			Environment:      appEnv,
			AttachStacktrace: true,
			BeforeSend:       logging.BeforeSend,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "failed to initialise Sentry: %v\n", err)
		} else {
			defer sentry.Flush(2 * time.Second)
		}
	}

	logging.Setup(logging.ParseLevel(os.Getenv("LOG_LEVEL")), appEnv)
	analysisLog.Info("hover analysis starting")

	// --- observability ---
	if os.Getenv("OBSERVABILITY_ENABLED") == "true" {
		serviceName := strings.TrimSpace(os.Getenv("FLY_APP_NAME"))
		if serviceName == "" {
			serviceName = "hover-analysis"
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
			analysisLog.Warn("failed to initialise observability", "error", err)
		} else {
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = providers.Shutdown(ctx)
			}()
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
				listener, err := net.Listen("tcp", metricsAddr)
				if err != nil {
					analysisLog.Error("metrics server failed to bind", "error", err, "addr", metricsAddr)
				} else {
					go func() {
						analysisLog.Info("metrics server listening", "addr", metricsAddr)
						if err := metricsSrv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
							analysisLog.Error("metrics server failed", "error", err)
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
		analysisLog.Fatal("failed to connect to PostgreSQL", "error", err)
	}
	defer func() {
		_ = pgDB.Close()
	}()

	// --- redis ---
	redisCfg := broker.ConfigFromEnv()
	redisClient, err := broker.NewClient(redisCfg)
	if err != nil {
		analysisLog.Fatal("failed to create Redis client", "error", err)
	}
	defer redisClient.Close()
	if err := redisClient.Ping(context.Background()); err != nil {
		analysisLog.Fatal("failed to ping Redis", "error", err)
	}
	analysisLog.Info("connected to Redis")

	// --- root context tied to OS signals ---
	rootCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg := loadConsumerConfig()
	analysisLog.Info("consumer configured",
		"max_concurrency", cfg.maxConcurrency,
		"audit_timeout_ms", cfg.auditTimeout.Milliseconds(),
		"poll_interval_ms", cfg.pollInterval.Milliseconds(),
		"runner", cfg.runner,
		"memory_shed_mb", cfg.memoryShedMB,
	)

	// --- archive provider (only required by the local runner) ---
	provider, bucket := loadArchiveProvider(rootCtx, cfg.runner)

	// --- runner ---
	runner, err := selectRunner(cfg, provider, bucket)
	if err != nil {
		analysisLog.Fatal("failed to construct lighthouse runner", "error", err)
	}

	consumer := newConsumer(pgDB, redisClient, runner, cfg)
	consumer.run(rootCtx)

	analysisLog.Info("analysis service stopped")
}

// loadArchiveProvider builds a ColdStorageProvider from ARCHIVE_*
// env vars. The stub runner doesn't need it, so a missing config is
// only fatal when LIGHTHOUSE_RUNNER=local. For "stub" we log and
// continue with a nil provider so review apps without R2 credentials
// still boot.
func loadArchiveProvider(ctx context.Context, runner string) (archive.ColdStorageProvider, string) {
	cfg := archive.ConfigFromEnv()
	if cfg == nil {
		if runner == "local" {
			analysisLog.Fatal("LIGHTHOUSE_RUNNER=local requires ARCHIVE_PROVIDER + ARCHIVE_BUCKET")
		}
		analysisLog.Info("archive provider unconfigured; stub runner doesn't need it")
		return nil, ""
	}
	provider, err := archive.ProviderFromEnv()
	if err != nil {
		if runner == "local" {
			analysisLog.Fatal("failed to construct archive provider", "error", err)
		}
		analysisLog.Warn("failed to construct archive provider; stub runner unaffected", "error", err)
		return nil, ""
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := provider.Ping(pingCtx, cfg.Bucket); err != nil {
		if runner == "local" {
			analysisLog.Fatal("archive provider ping failed", "error", err, "bucket", cfg.Bucket)
		}
		analysisLog.Warn("archive provider ping failed; stub runner unaffected", "error", err)
		return nil, ""
	}
	analysisLog.Info("archive provider ready", "provider", provider.Provider(), "bucket", cfg.Bucket)
	return provider, cfg.Bucket
}

// runnerEnvKey selects the runner implementation. v1 only has the stub
// runner; Phase 3 introduces 'local' which shells out to Chromium.
const runnerEnvKey = "LIGHTHOUSE_RUNNER"

// selectRunner builds the configured Runner. Returns an error rather
// than falling back silently so a Dockerfile/toml drift (missing
// LIGHTHOUSE_BIN, missing R2 credentials when local is requested) is a
// loud boot failure rather than a degraded service that quietly stubs
// every audit. Unknown choices stay loud-but-not-fatal: log and stub
// so an inadvertent typo in env doesn't take the service down.
func selectRunner(cfg consumerConfig, provider archive.ColdStorageProvider, bucket string) (lighthouse.Runner, error) {
	switch cfg.runner {
	case "stub":
		analysisLog.Info("using stub lighthouse runner")
		return lighthouse.NewStubRunner(), nil
	case "local":
		analysisLog.Info("using local lighthouse runner",
			"lighthouse_bin", cfg.lighthouseBin,
			"chromium_bin", cfg.chromiumBin,
		)
		return lighthouse.NewLocalRunner(lighthouse.LocalRunnerConfig{
			LighthouseBin: cfg.lighthouseBin,
			ChromiumBin:   cfg.chromiumBin,
			Provider:      provider,
			Bucket:        bucket,
			MemoryShedMB:  cfg.memoryShedMB,
			ProfilePreset: lighthouse.ProfileMobile,
		})
	default:
		analysisLog.Warn("unknown LIGHTHOUSE_RUNNER value; falling back to stub", "requested", cfg.runner)
		return lighthouse.NewStubRunner(), nil
	}
}

// consumerConfig holds the tunables the analysis service exposes via env.
type consumerConfig struct {
	maxConcurrency  int
	auditTimeout    time.Duration
	pollInterval    time.Duration
	reclaimInterval time.Duration
	reclaimMinIdle  time.Duration
	consumerName    string

	// Phase 3 additions. Only consulted when runner == "local".
	runner        string // "stub" | "local"
	lighthouseBin string // path to bundled lighthouse CLI
	chromiumBin   string // path to bundled Chromium binary
	memoryShedMB  int    // free-memory floor before deferring an audit
}

func loadConsumerConfig() consumerConfig {
	runner := strings.ToLower(strings.TrimSpace(os.Getenv(runnerEnvKey)))
	if runner == "" {
		runner = "stub"
	}
	cfg := consumerConfig{
		maxConcurrency:  envIntDefault("LIGHTHOUSE_MAX_CONCURRENCY", 1),
		auditTimeout:    time.Duration(envIntDefault("LIGHTHOUSE_AUDIT_TIMEOUT_MS", 90_000)) * time.Millisecond,
		pollInterval:    time.Duration(envIntDefault("LIGHTHOUSE_POLL_INTERVAL_MS", 1_000)) * time.Millisecond,
		reclaimInterval: time.Duration(envIntDefault("LIGHTHOUSE_RECLAIM_INTERVAL_S", 60)) * time.Second,
		// MinIdle is the floor the message must have been pending before
		// XAUTOCLAIM will reassign it. Three minutes mirrors the crawl
		// stream's REDIS_AUTOCLAIM_MIN_IDLE_S default and is safely
		// longer than the per-audit timeout (default 90s) plus DB write.
		reclaimMinIdle: time.Duration(envIntDefault("LIGHTHOUSE_RECLAIM_MIN_IDLE_S", 180)) * time.Second,
		runner:         runner,
		lighthouseBin:  strings.TrimSpace(os.Getenv("LIGHTHOUSE_BIN")),
		chromiumBin:    strings.TrimSpace(os.Getenv("CHROMIUM_BIN")),
		memoryShedMB:   envIntDefault("LIGHTHOUSE_MEMORY_SHED_THRESHOLD_MB", 600),
	}
	if cfg.maxConcurrency < 1 {
		cfg.maxConcurrency = 1
	}
	machineName := os.Getenv("FLY_MACHINE_ID")
	if machineName == "" {
		machineName, _ = os.Hostname()
	}
	if machineName == "" {
		machineName = "analysis"
	}
	cfg.consumerName = "lh-" + machineName
	return cfg
}

func envIntDefault(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// consumer is the analysis-side stream consumer. Each tick:
//  1. List jobs with pending or running lighthouse_runs.
//  2. For each, XReadGroup the lighthouse stream and dispatch messages
//     onto a bounded worker pool.
//  3. Each worker runs the audit via the configured Runner and writes
//     the result back to the matching lighthouse_runs row.
type consumer struct {
	db     *db.DB
	rdb    *broker.Client
	runner lighthouse.Runner
	cfg    consumerConfig
	sem    chan struct{}
}

func newConsumer(database *db.DB, rdb *broker.Client, runner lighthouse.Runner, cfg consumerConfig) *consumer {
	return &consumer{
		db:     database,
		rdb:    rdb,
		runner: runner,
		cfg:    cfg,
		sem:    make(chan struct{}, cfg.maxConcurrency),
	}
}

func (c *consumer) run(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.pollInterval)
	defer ticker.Stop()

	reclaimTicker := time.NewTicker(c.cfg.reclaimInterval)
	defer reclaimTicker.Stop()

	var wg sync.WaitGroup

	// Run an immediate reclaim sweep on start so a fresh pod replacing
	// a crashed one picks up its abandoned PEL entries before any new
	// XReadGroup ">" deliveries land.
	c.reclaimAllJobs(ctx, &wg)

	for {
		select {
		case <-ctx.Done():
			analysisLog.Info("analysis consumer stopping; waiting for in-flight audits")
			wg.Wait()
			return
		case <-ticker.C:
			jobs, err := c.activeJobIDs(ctx)
			if err != nil {
				analysisLog.Warn("failed to list jobs with lighthouse work", "error", err)
				continue
			}
			for _, jobID := range jobs {
				if ctx.Err() != nil {
					break
				}
				c.consumeOne(ctx, &wg, jobID)
			}
		case <-reclaimTicker.C:
			c.reclaimAllJobs(ctx, &wg)
		}
	}
}

// reclaimAllJobs runs an XAUTOCLAIM sweep across every active job.
// Cheap when nothing is stuck: each empty stream returns an empty
// page in one round-trip.
func (c *consumer) reclaimAllJobs(ctx context.Context, wg *sync.WaitGroup) {
	jobs, err := c.activeJobIDs(ctx)
	if err != nil {
		analysisLog.Warn("reclaim sweep: failed to list jobs", "error", err)
		return
	}
	for _, jobID := range jobs {
		if ctx.Err() != nil {
			return
		}
		c.reclaimStale(ctx, wg, jobID)
	}
}

// activeJobIDs returns distinct job IDs with at least one lighthouse_runs
// row in pending or running. Switching to this from a generic "active
// jobs" query keeps the consumer focused on jobs that actually have
// lighthouse work waiting, which matters more than crawl status when
// reconciliation passes outlive the crawl.
//
// No LIMIT is applied: the active-job count is bounded in practice by
// the per-job 100-audit cap and the analysis app's own throughput, and
// per-tick polling is non-blocking (Block: -1) so a wide active set
// simply round-robins through more streams per tick rather than
// starving anything.
func (c *consumer) activeJobIDs(ctx context.Context) ([]string, error) {
	rows, err := c.db.GetDB().QueryContext(ctx, `
		SELECT DISTINCT job_id
		  FROM lighthouse_runs
		 WHERE status IN ('pending', 'running')
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var jobID string
		if err := rows.Scan(&jobID); err != nil {
			return nil, err
		}
		out = append(out, jobID)
	}
	return out, rows.Err()
}

// reclaimStale runs an XAUTOCLAIM sweep so a fresh consumer (e.g. a
// freshly-deployed analysis pod) picks up messages stuck in another
// consumer's PEL after a crash. Without this, a process that died
// between MarkLighthouseRunRunning and XAck would leave the stream
// message orphaned forever — XReadGroup ">" only delivers
// never-delivered entries, so a new consumer never sees the abandoned
// IDs.
//
// Reclaimed messages are dispatched through the same processOne path
// as new deliveries.
func (c *consumer) reclaimStale(ctx context.Context, wg *sync.WaitGroup, jobID string) {
	streamKey := broker.LighthouseStreamKey(jobID)
	groupName := broker.LighthouseConsumerGroup(jobID)

	if err := c.ensureGroup(ctx, streamKey, groupName); err != nil {
		analysisLog.Warn("reclaim ensure consumer group failed",
			"error", err, "job_id", jobID)
		return
	}

	cursor := "0-0"
	for ctx.Err() == nil {
		msgs, next, err := c.rdb.RDB().XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   streamKey,
			Group:    groupName,
			Consumer: c.cfg.consumerName,
			MinIdle:  c.cfg.reclaimMinIdle,
			Start:    cursor,
			Count:    int64(c.cfg.maxConcurrency),
		}).Result()
		if err != nil {
			if isNoGroupErr(err) {
				return
			}
			analysisLog.Warn("XAutoClaim failed", "error", err, "job_id", jobID)
			return
		}
		if len(msgs) == 0 && (next == "0-0" || next == "") {
			return
		}
		c.dispatchMessages(ctx, wg, jobID, streamKey, groupName, msgs)
		if next == "0-0" || next == "" {
			return
		}
		cursor = next
	}
}

// consumeOne does one XReadGroup tick for a single job. Messages are
// dispatched into the bounded worker pool and processed concurrently up
// to LIGHTHOUSE_MAX_CONCURRENCY.
//
// Block: -1 makes XReadGroup return immediately when the stream is
// empty, so a tick over many active jobs does not stall on any one of
// them. The outer ticker (LIGHTHOUSE_POLL_INTERVAL_MS) sets the cadence.
func (c *consumer) consumeOne(ctx context.Context, wg *sync.WaitGroup, jobID string) {
	streamKey := broker.LighthouseStreamKey(jobID)
	groupName := broker.LighthouseConsumerGroup(jobID)

	if err := c.ensureGroup(ctx, streamKey, groupName); err != nil {
		analysisLog.Warn("ensure consumer group failed", "error", err, "job_id", jobID)
		return
	}

	streams, err := c.rdb.RDB().XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    groupName,
		Consumer: c.cfg.consumerName,
		Streams:  []string{streamKey, ">"},
		Count:    int64(c.cfg.maxConcurrency),
		Block:    -1,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return
	}
	if err != nil {
		// Stream/group not yet created — common before first XADD lands.
		if isNoGroupErr(err) {
			return
		}
		analysisLog.Warn("XReadGroup failed", "error", err, "job_id", jobID)
		return
	}

	for _, stream := range streams {
		c.dispatchMessages(ctx, wg, jobID, streamKey, groupName, stream.Messages)
	}
}

// dispatchMessages turns a batch of stream entries (from either
// XReadGroup or XAutoClaim) into runner work, sharing the bounded
// worker pool. Malformed messages are ACKed to drop them.
func (c *consumer) dispatchMessages(
	ctx context.Context,
	wg *sync.WaitGroup,
	jobID, streamKey, groupName string,
	msgs []redis.XMessage,
) {
	for _, msg := range msgs {
		runID, ok := parseRunID(msg.Values)
		if !ok {
			analysisLog.Warn("malformed lighthouse stream message; ACKing to drop",
				"job_id", jobID, "message_id", msg.ID)
			_ = c.rdb.RDB().XAck(ctx, streamKey, groupName, msg.ID).Err()
			continue
		}
		url := stringFromMap(msg.Values, streamFieldURL)
		if url == "" {
			url = composeURLFromMap(msg.Values)
		}
		pageID, _ := strconv.Atoi(stringFromMap(msg.Values, "page_id"))

		req := lighthouse.AuditRequest{
			RunID:   runID,
			JobID:   jobID,
			PageID:  pageID,
			URL:     url,
			Profile: lighthouse.ProfileMobile,
			Timeout: c.cfg.auditTimeout,
		}

		// Acquire the semaphore before spawning so a backpressured
		// consumer doesn't pile up goroutines waiting on the channel.
		select {
		case c.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		wg.Add(1)
		go func(req lighthouse.AuditRequest, msgID string) {
			defer wg.Done()
			defer func() { <-c.sem }()
			c.processOne(ctx, req, msgID, streamKey, groupName)
		}(req, msg.ID)
	}
}

// processOne transitions the lighthouse_runs row to running, executes
// the audit, then writes the terminal status. The XAck only fires once
// the DB write succeeds so a crash mid-run leaves the message redelivered
// to a fresh consumer (CompleteLighthouseRun / FailLighthouseRun gate on
// status='running' so the redelivery is a no-op for already-completed
// rows — see internal/db/lighthouse.go).
func (c *consumer) processOne(ctx context.Context, req lighthouse.AuditRequest, msgID, streamKey, groupName string) {
	startedAt := time.Now()

	moved, sourceTaskID, err := c.db.MarkLighthouseRunRunning(ctx, req.RunID)
	if err != nil {
		analysisLog.Warn("MarkLighthouseRunRunning failed", "error", err,
			"run_id", req.RunID, "job_id", req.JobID)
		return
	}
	if !moved {
		// MarkLighthouseRunRunning accepts both pending and running, so
		// the only way to land here is a terminal status — the row is
		// already succeeded, failed, or skipped_quota. The stream entry
		// is stale; ACK so the dispatcher's at-least-once redelivery
		// stops re-driving completed work.
		analysisLog.Debug("lighthouse run already terminal; acking stale redelivery",
			"run_id", req.RunID, "job_id", req.JobID, "message_id", msgID)
		_ = c.rdb.RDB().XAck(ctx, streamKey, groupName, msgID).Err()
		return
	}
	req.SourceTaskID = sourceTaskID

	analysisLog.Info("lighthouse audit started",
		"run_id", req.RunID, "job_id", req.JobID,
		"page_id", req.PageID,
		// Strip query/fragment so customer session tokens or signed-link
		// tokens aren't written to centralised logs. Mirrors the same
		// rule applied inside StubRunner.Run.
		"url", lighthouse.SanitiseAuditURL(req.URL),
		"profile", string(req.Profile),
	)

	result, runErr := c.runner.Run(ctx, req)

	if runErr != nil {
		duration := time.Since(startedAt)
		// Memory-shed defers the audit just like a shutdown: leave the
		// row in 'running' and skip the ACK so XAUTOCLAIM redelivers
		// once memory recovers. Treating it as a permanent failure
		// would burn the audit slot and we'd never re-attempt.
		if errors.Is(runErr, lighthouse.ErrMemoryShed) {
			analysisLog.Info("lighthouse audit shed (low memory); leaving for redelivery",
				"run_id", req.RunID, "job_id", req.JobID,
				"duration_ms", duration.Milliseconds(),
			)
			// Tick the shed outcome so a rising shed rate on Grafana
			// is the unambiguous signal that the analysis fleet is
			// memory-saturated. No duration histogram bucket — these
			// audits never actually ran.
			observability.RecordLighthouseRun(ctx, req.JobID, "shed")
			return
		}
		// Shutdown cancellation must not be turned into a permanent
		// 'failed' row. SIGTERM cancels the root context, which would
		// otherwise mark the in-flight audit failed AND ACK the stream
		// entry — a deploy would burn whatever was mid-flight. Leave
		// the row in 'running' and skip the ACK so XAUTOCLAIM (or the
		// next XReadGroup ">" delivery to a fresh consumer) redelivers
		// the message; the status='running' guard on
		// MarkLighthouseRunRunning makes the redelivery a no-op for
		// already-handled rows.
		if errors.Is(runErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			analysisLog.Info("lighthouse audit interrupted by shutdown; leaving for redelivery",
				"run_id", req.RunID, "job_id", req.JobID,
				"duration_ms", duration.Milliseconds(),
			)
			return
		}
		analysisLog.Warn("lighthouse audit failed",
			"error", runErr, "run_id", req.RunID,
			"job_id", req.JobID, "duration_ms", duration.Milliseconds(),
		)
		if dbErr := c.db.FailLighthouseRun(ctx, req.RunID, runErr.Error(), int(duration.Milliseconds())); dbErr != nil {
			analysisLog.Warn("FailLighthouseRun write failed",
				"error", dbErr, "run_id", req.RunID, "job_id", req.JobID)
			// Don't ACK — let the message redeliver so we retry the row write.
			return
		}
		observability.RecordLighthouseRun(ctx, req.JobID, "failed")
		observability.RecordLighthouseRunDuration(ctx, req.JobID, "failed", float64(duration.Milliseconds()))
		_ = c.rdb.RDB().XAck(ctx, streamKey, groupName, msgID).Err()
		return
	}

	duration := result.Duration
	if duration <= 0 {
		duration = time.Since(startedAt)
	}

	if dbErr := c.db.CompleteLighthouseRun(ctx, req.RunID, db.LighthouseRunMetrics{
		PerformanceScore: result.PerformanceScore,
		LCPMs:            result.LCPMs,
		CLS:              result.CLS,
		INPMs:            result.INPMs,
		TBTMs:            result.TBTMs,
		FCPMs:            result.FCPMs,
		SpeedIndexMs:     result.SpeedIndexMs,
		TTFBMs:           result.TTFBMs,
		TotalByteWeight:  result.TotalByteWeight,
		ReportKey:        result.ReportKey,
		DurationMs:       int(duration.Milliseconds()),
	}); dbErr != nil {
		analysisLog.Warn("CompleteLighthouseRun write failed",
			"error", dbErr, "run_id", req.RunID, "job_id", req.JobID)
		// Don't ACK — let the redelivery retry the write.
		return
	}

	observability.RecordLighthouseRun(ctx, req.JobID, "succeeded")
	observability.RecordLighthouseRunDuration(ctx, req.JobID, "succeeded", float64(duration.Milliseconds()))

	analysisLog.Info("lighthouse audit completed",
		"run_id", req.RunID, "job_id", req.JobID,
		"page_id", req.PageID,
		"duration_ms", duration.Milliseconds(),
	)

	_ = c.rdb.RDB().XAck(ctx, streamKey, groupName, msgID).Err()
}

func (c *consumer) ensureGroup(ctx context.Context, streamKey, groupName string) error {
	err := c.rdb.RDB().XGroupCreateMkStream(ctx, streamKey, groupName, "0").Err()
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "BUSYGROUP") {
		return nil
	}
	return err
}

// --- helpers ---

func parseRunID(values map[string]interface{}) (int64, bool) {
	raw, ok := values[streamFieldRunID].(string)
	if !ok || raw == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func stringFromMap(values map[string]interface{}, key string) string {
	if v, ok := values[key].(string); ok {
		return v
	}
	return ""
}

func composeURLFromMap(values map[string]interface{}) string {
	host := stringFromMap(values, streamFieldHost)
	path := stringFromMap(values, streamFieldPath)
	if host == "" {
		return ""
	}
	if path == "" {
		path = "/"
	}
	return "https://" + host + path
}

func isNoGroupErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "NOGROUP") || strings.Contains(s, "no such key")
}
