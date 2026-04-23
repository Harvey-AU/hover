package observability

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// ParseOTLPHeaders converts a comma-separated `key=value` list (matching the
// OTEL_EXPORTER_OTLP_HEADERS env var format) into a map. Whitespace around
// pairs and tokens is trimmed; empty pairs, pairs without `=`, and entries
// with an empty key are skipped.
func ParseOTLPHeaders(raw string) map[string]string {
	headers := make(map[string]string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return headers
	}

	for _, pair := range strings.Split(raw, ",") {
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

// Config controls observability initialisation.
type Config struct {
	Enabled        bool
	ServiceName    string
	Environment    string
	OTLPEndpoint   string
	OTLPHeaders    map[string]string
	OTLPInsecure   bool
	MetricsAddress string
}

// Providers exposes configured telemetry providers.
type Providers struct {
	TracerProvider *sdktrace.TracerProvider
	MeterProvider  *sdkmetric.MeterProvider
	Propagator     propagation.TextMapPropagator
	MetricsHandler http.Handler
	Shutdown       func(ctx context.Context) error
	Config         Config
}

var (
	initOnce sync.Once

	workerTracer trace.Tracer

	workerTaskDuration     metric.Float64Histogram
	workerTaskTotal        metric.Int64Counter
	workerConcurrentTasks  metric.Int64UpDownCounter
	workerConcurrencyLimit metric.Int64Gauge

	workerTaskQueueWait       metric.Float64Histogram
	workerTaskTotalDuration   metric.Float64Histogram
	workerTaskOutcomeDuration metric.Float64Histogram
	workerTaskOutcomeTotal    metric.Int64Counter

	workerTaskClaimLatency metric.Float64Histogram

	workerTaskRetryCounter   metric.Int64Counter
	workerTaskFailureCounter metric.Int64Counter
	workerTaskWaitingCounter metric.Int64Counter

	crawlerPhaseDuration metric.Float64Histogram
	crawlerPhaseTotal    metric.Int64Counter

	jobRunningTasksGauge     metric.Int64Gauge
	jobConcurrencyLimitGauge metric.Int64Gauge
	jobInfoCacheHitsCounter  metric.Int64Counter
	jobInfoCacheMissCounter  metric.Int64Counter
	jobInfoCacheInvalidation metric.Int64Counter
	jobInfoCacheSizeGauge    metric.Int64Gauge

	dbPoolInUseGauge        metric.Int64Gauge
	dbPoolIdleGauge         metric.Int64Gauge
	dbPoolWaitCountGauge    metric.Int64Gauge
	dbPoolWaitDurationGauge metric.Float64Gauge
	dbPoolUsageGauge        metric.Float64Gauge
	dbPoolMaxOpenGauge      metric.Int64Gauge
	dbPoolReservedGauge     metric.Int64Gauge
	dbPoolRejectCounter     metric.Int64Counter

	dbPressureEMAGauge       metric.Float64Gauge
	dbPressureLimitGauge     metric.Int64Gauge
	dbPressureAdjustCounter  metric.Int64Counter
	dbSemaphoreWaitHistogram metric.Float64Histogram

	fdCurrentGauge  metric.Int64Gauge
	fdLimitGauge    metric.Int64Gauge
	fdPressureGauge metric.Float64Gauge

	// --- Redis broker instruments (Tier 1 + Tier 2). ---
	// Tier 1: gauges scraped by the broker probe goroutine.
	brokerStreamLengthGauge    metric.Int64Gauge
	brokerScheduledDepthGauge  metric.Int64Gauge
	brokerConsumerPendingGauge metric.Int64Gauge
	brokerOutboxBacklogGauge   metric.Int64Gauge
	brokerOutboxAgeGauge       metric.Float64Gauge
	brokerRedisPingHistogram   metric.Float64Histogram

	// Tier 1: dispatch outcomes counter.
	brokerDispatchCounter metric.Int64Counter

	// Tier 2: autoclaim + message age.
	brokerAutoclaimCounter    metric.Int64Counter
	brokerMessageAgeHistogram metric.Float64Histogram

	// Tier 2: pacer signals.
	brokerPacerPushbackCounter metric.Int64Counter
	brokerPacerDelayHistogram  metric.Float64Histogram

	// Tier 2: counter sync skew and Redis pool stats.
	brokerCounterSyncSkew metric.Float64Histogram
	brokerRedisPoolInUse  metric.Int64Gauge
	brokerRedisPoolIdle   metric.Int64Gauge
	brokerRedisPoolWait   metric.Int64Gauge

	// Tier 2: counter-vs-PEL drift and orphan PEL detection. These catch
	// the "job frozen with in-flight work" failure mode — historically
	// invisible because the Postgres mirror happily reflected the stuck
	// Redis counter.
	brokerCounterPELSkewHistogram metric.Float64Histogram
	brokerPELWithoutConsumerGauge metric.Int64Gauge
)

// Init configures tracing and metrics exporters. When cfg.Enabled is false the function is a no-op.
func Init(ctx context.Context, cfg Config) (*Providers, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	if cfg.ServiceName == "" {
		cfg.ServiceName = "hover"
	}

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.DeploymentEnvironment(cfg.Environment),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build otel resource: %w", err)
	}

	var spanExporter sdktrace.SpanExporter
	if cfg.OTLPEndpoint != "" {
		clientOpts := []otlptracehttp.Option{
			getOTLPEndpointOption(cfg.OTLPEndpoint),
		}
		if cfg.OTLPInsecure {
			clientOpts = append(clientOpts, otlptracehttp.WithInsecure())
		}
		if len(cfg.OTLPHeaders) > 0 {
			clientOpts = append(clientOpts, otlptracehttp.WithHeaders(cfg.OTLPHeaders))
		}

		exp, err := otlptracehttp.New(ctx, clientOpts...)
		if err != nil {
			// Log error but don't fail app startup - observability is optional
			fmt.Printf("WARN: Failed to create OTLP trace exporter (traces disabled): %v\n", err)
			fmt.Printf("WARN: Endpoint: %s\n", cfg.OTLPEndpoint)
			// Continue without tracing - app should still function
		} else {
			spanExporter = exp
			fmt.Printf("INFO: OTLP trace exporter initialised successfully for endpoint: %s\n", cfg.OTLPEndpoint)
		}
	}

	traceOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
	}
	if spanExporter != nil {
		traceOpts = append(traceOpts, sdktrace.WithBatcher(spanExporter))
	}

	tracerProvider := sdktrace.NewTracerProvider(traceOpts...)
	otel.SetTracerProvider(tracerProvider)

	prop := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
	otel.SetTextMapPropagator(prop)

	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
	)
	promExporter, err := otelprom.New(
		otelprom.WithRegisterer(registry),
	)
	if err != nil {
		_ = tracerProvider.Shutdown(ctx) // best-effort cleanup
		return nil, fmt.Errorf("create Prometheus exporter: %w", err)
	}

	meterOpts := []sdkmetric.Option{
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(promExporter),
	}

	if cfg.OTLPEndpoint != "" {
		metricsEndpoint := deriveMetricsEndpoint(cfg.OTLPEndpoint)
		metricClientOpts := []otlpmetrichttp.Option{
			otlpmetrichttp.WithEndpointURL(metricsEndpoint),
		}
		if cfg.OTLPInsecure {
			metricClientOpts = append(metricClientOpts, otlpmetrichttp.WithInsecure())
		}
		if len(cfg.OTLPHeaders) > 0 {
			metricClientOpts = append(metricClientOpts, otlpmetrichttp.WithHeaders(cfg.OTLPHeaders))
		}
		metricExporter, merr := otlpmetrichttp.New(ctx, metricClientOpts...)
		if merr != nil {
			fmt.Printf("WARN: Failed to create OTLP metric exporter (metrics push disabled): %v\n", merr)
		} else {
			meterOpts = append(meterOpts, sdkmetric.WithReader(
				sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(otelExportInterval())),
			))
			fmt.Printf("INFO: OTLP metric exporter initialised successfully for endpoint: %s\n", metricsEndpoint)
		}
	}

	meterProvider := sdkmetric.NewMeterProvider(meterOpts...)
	otel.SetMeterProvider(meterProvider)

	initOnce.Do(func() {
		workerTracer = tracerProvider.Tracer("hover/worker")
		_ = initWorkerInstruments(meterProvider)
		_ = initCrawlerInstruments(meterProvider)
		_ = initJobInstruments(meterProvider)
		_ = initDBPoolInstruments(meterProvider)
		_ = initBrokerInstruments(meterProvider)
	})

	shutdown := func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		var allErr error
		if err := meterProvider.Shutdown(ctx); err != nil {
			allErr = errors.Join(allErr, fmt.Errorf("metric provider shutdown: %w", err))
		}
		if err := tracerProvider.Shutdown(ctx); err != nil {
			allErr = errors.Join(allErr, fmt.Errorf("trace provider shutdown: %w", err))
		}
		return allErr
	}

	return &Providers{
		TracerProvider: tracerProvider,
		MeterProvider:  meterProvider,
		Propagator:     prop,
		MetricsHandler: promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
		Shutdown:       shutdown,
		Config:         cfg,
	}, nil
}

// otelExportInterval returns the OTEL metric export interval from
// GNH_OTEL_EXPORT_INTERVAL_SECONDS (default 60s). A longer interval reduces
// export frequency but increases the aggregated payload size per export; 60s
// was chosen to keep per-export bursts within Grafana Mimir's ingestion rate
// limits under expected load.
func otelExportInterval() time.Duration {
	if s := os.Getenv("GNH_OTEL_EXPORT_INTERVAL_SECONDS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			fmt.Printf("WARN: GNH_OTEL_EXPORT_INTERVAL_SECONDS=%q is not a valid integer (%v); using default 60s\n", s, err)
			return 60 * time.Second
		}
		if n <= 0 {
			fmt.Printf("WARN: GNH_OTEL_EXPORT_INTERVAL_SECONDS=%d is non-positive; using default 60s\n", n)
			return 60 * time.Second
		}
		return time.Duration(n) * time.Second
	}
	return 60 * time.Second
}

func getOTLPEndpointOption(endpoint string) otlptracehttp.Option {
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return otlptracehttp.WithEndpointURL(endpoint)
	}
	return otlptracehttp.WithEndpoint(endpoint)
}

// deriveMetricsEndpoint converts a traces OTLP endpoint URL to the metrics equivalent.
// e.g. ".../otlp/v1/traces" → ".../otlp/v1/metrics"
func deriveMetricsEndpoint(tracesEndpoint string) string {
	if strings.HasSuffix(tracesEndpoint, "/v1/traces") {
		return strings.TrimSuffix(tracesEndpoint, "/v1/traces") + "/v1/metrics"
	}
	return tracesEndpoint
}

// WrapHandler applies OpenTelemetry instrumentation to an http.Handler when the providers are active.
func WrapHandler(handler http.Handler, prov *Providers) http.Handler {
	if prov == nil || prov.TracerProvider == nil {
		return handler
	}

	options := []otelhttp.Option{
		otelhttp.WithTracerProvider(prov.TracerProvider),
		otelhttp.WithPropagators(prov.Propagator),
		otelhttp.WithMeterProvider(prov.MeterProvider),
		otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
			return fmt.Sprintf("%s %s", r.Method, r.URL.Path)
		}),
		// Skip tracing for health checks to reduce noise
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/health"
		}),
	}

	return otelhttp.NewHandler(handler, "http.server", options...)
}

func initWorkerInstruments(meterProvider *sdkmetric.MeterProvider) error {
	if meterProvider == nil {
		return nil
	}

	meter := meterProvider.Meter("hover/worker")

	var err error
	workerTaskDuration, err = meter.Float64Histogram(
		"bee.worker.task.duration_ms",
		metric.WithUnit("ms"),
		metric.WithDescription("Time taken to process a cache warming task"),
	)
	if err != nil {
		return err
	}

	workerTaskTotal, err = meter.Int64Counter(
		"bee.worker.task.total",
		metric.WithDescription("Counts task outcomes processed by the worker pool"),
	)
	if err != nil {
		return err
	}

	workerConcurrentTasks, err = meter.Int64UpDownCounter(
		"bee.worker.concurrent_tasks",
		metric.WithDescription("Current number of tasks being processed concurrently by a worker"),
	)
	if err != nil {
		return err
	}

	workerConcurrencyLimit, err = meter.Int64Gauge(
		"bee.worker.concurrency_capacity",
		metric.WithDescription("Maximum concurrent tasks allowed per worker"),
	)
	if err != nil {
		return err
	}

	workerTaskQueueWait, err = meter.Float64Histogram(
		"bee.worker.task.queue_wait_ms",
		metric.WithUnit("ms"),
		metric.WithDescription("Time tasks spend waiting in the queue before a worker starts processing"),
	)
	if err != nil {
		return err
	}

	workerTaskTotalDuration, err = meter.Float64Histogram(
		"bee.worker.task.total_duration_ms",
		metric.WithUnit("ms"),
		metric.WithDescription("End-to-end time from task creation until completion"),
	)
	if err != nil {
		return err
	}

	workerTaskOutcomeDuration, err = meter.Float64Histogram(
		"bee.worker.task.outcome_duration_ms",
		metric.WithUnit("ms"),
		metric.WithDescription("Task processing duration grouped by outcome and reason"),
	)
	if err != nil {
		return err
	}

	workerTaskOutcomeTotal, err = meter.Int64Counter(
		"bee.worker.task.outcomes_total",
		metric.WithDescription("Counts task processing outcomes grouped by outcome and reason"),
	)
	if err != nil {
		return err
	}

	workerTaskClaimLatency, err = meter.Float64Histogram(
		"bee.worker.task.claim_latency_ms",
		metric.WithUnit("ms"),
		metric.WithDescription("Latency to claim a task from the database"),
	)
	if err != nil {
		return err
	}

	workerTaskRetryCounter, err = meter.Int64Counter(
		"bee.worker.task.retries_total",
		metric.WithDescription("Number of task retry attempts"),
	)
	if err != nil {
		return err
	}

	workerTaskFailureCounter, err = meter.Int64Counter(
		"bee.worker.task.failures_total",
		metric.WithDescription("Number of permanently failed tasks"),
	)
	if err != nil {
		return err
	}

	workerTaskWaitingCounter, err = meter.Int64Counter(
		"bee.worker.task.waiting_total",
		metric.WithDescription("Number of times tasks enter waiting state"),
	)
	return err
}

func initJobInstruments(meterProvider *sdkmetric.MeterProvider) error {
	if meterProvider == nil {
		return nil
	}

	meter := meterProvider.Meter("hover/jobs")

	var err error
	jobRunningTasksGauge, err = meter.Int64Gauge(
		"bee.jobs.running_tasks",
		metric.WithDescription("Number of tasks currently running for a job"),
	)
	if err != nil {
		return err
	}

	jobConcurrencyLimitGauge, err = meter.Int64Gauge(
		"bee.jobs.concurrency_limit",
		metric.WithDescription("Concurrency limit configured for a job (0 indicates unlimited)"),
	)
	if err != nil {
		return err
	}

	jobInfoCacheHitsCounter, err = meter.Int64Counter(
		"bee.jobs.cache_hits_total",
		metric.WithDescription("Job info cache hits"),
	)
	if err != nil {
		return err
	}

	jobInfoCacheMissCounter, err = meter.Int64Counter(
		"bee.jobs.cache_misses_total",
		metric.WithDescription("Job info cache misses"),
	)
	if err != nil {
		return err
	}

	jobInfoCacheInvalidation, err = meter.Int64Counter(
		"bee.jobs.cache_invalidations_total",
		metric.WithDescription("Job info cache invalidations by reason"),
	)
	if err != nil {
		return err
	}

	jobInfoCacheSizeGauge, err = meter.Int64Gauge(
		"bee.jobs.cache_size",
		metric.WithDescription("Current job info cache size"),
	)
	return err
}

func initCrawlerInstruments(meterProvider *sdkmetric.MeterProvider) error {
	if meterProvider == nil {
		return nil
	}

	meter := meterProvider.Meter("hover/crawler")

	var err error
	crawlerPhaseDuration, err = meter.Float64Histogram(
		"bee.crawler.phase.duration_ms",
		metric.WithUnit("ms"),
		metric.WithDescription("Duration of crawler phases grouped by phase and outcome"),
	)
	if err != nil {
		return err
	}

	crawlerPhaseTotal, err = meter.Int64Counter(
		"bee.crawler.phase.total",
		metric.WithDescription("Counts crawler phase executions grouped by phase and outcome"),
	)
	return err
}

func initDBPoolInstruments(meterProvider *sdkmetric.MeterProvider) error {
	if meterProvider == nil {
		return nil
	}

	meter := meterProvider.Meter("hover/db_pool")

	var err error
	dbPoolInUseGauge, err = meter.Int64Gauge(
		"bee.db.pool.in_use",
		metric.WithDescription("Current number of connections in use"),
	)
	if err != nil {
		return err
	}

	dbPoolIdleGauge, err = meter.Int64Gauge(
		"bee.db.pool.idle",
		metric.WithDescription("Current number of idle connections"),
	)
	if err != nil {
		return err
	}

	dbPoolWaitCountGauge, err = meter.Int64Gauge(
		"bee.db.pool.wait_count",
		metric.WithDescription("Total number of waits for a database connection"),
	)
	if err != nil {
		return err
	}

	dbPoolWaitDurationGauge, err = meter.Float64Gauge(
		"bee.db.pool.wait_duration_ms",
		metric.WithDescription("Total time spent waiting for database connections (milliseconds)"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return err
	}

	dbPoolUsageGauge, err = meter.Float64Gauge(
		"bee.db.pool.usage_ratio",
		metric.WithDescription("Connection pool usage ratio (in_use / max_open)"),
	)
	if err != nil {
		return err
	}

	dbPoolMaxOpenGauge, err = meter.Int64Gauge(
		"bee.db.pool.max_open",
		metric.WithDescription("Maximum configured open connections"),
	)
	if err != nil {
		return err
	}

	dbPoolReservedGauge, err = meter.Int64Gauge(
		"bee.db.pool.reserved",
		metric.WithDescription("Connections reserved for critical operations"),
	)
	if err != nil {
		return err
	}

	dbPoolRejectCounter, err = meter.Int64Counter(
		"bee.db.pool.rejects_total",
		metric.WithDescription("Number of pool rejections when context expires before acquiring connection"),
	)
	if err != nil {
		return err
	}

	fdCurrentGauge, err = meter.Int64Gauge(
		"bee.process.fd.current",
		metric.WithDescription("Current number of open file descriptors"),
	)
	if err != nil {
		return err
	}

	fdLimitGauge, err = meter.Int64Gauge(
		"bee.process.fd.limit",
		metric.WithDescription("File descriptor soft limit"),
	)
	if err != nil {
		return err
	}

	fdPressureGauge, err = meter.Float64Gauge(
		"bee.process.fd.pressure",
		metric.WithDescription("File descriptor usage ratio (current / limit)"),
	)
	if err != nil {
		return err
	}

	dbPressureEMAGauge, err = meter.Float64Gauge(
		"bee.db.pressure.ema_ms",
		metric.WithUnit("ms"),
		metric.WithDescription("Exponential moving average of DB query execution time — the pressure controller signal"),
	)
	if err != nil {
		return err
	}

	dbPressureLimitGauge, err = meter.Int64Gauge(
		"bee.db.pressure.limit",
		metric.WithDescription("Current pressure-adjusted concurrency limit for the DB queue semaphore"),
	)
	if err != nil {
		return err
	}

	dbPressureAdjustCounter, err = meter.Int64Counter(
		"bee.db.pressure.adjustments_total",
		metric.WithDescription("Number of times the pressure controller scaled concurrency up or down"),
	)
	if err != nil {
		return err
	}

	dbSemaphoreWaitHistogram, err = meter.Float64Histogram(
		"bee.db.semaphore.wait_ms",
		metric.WithUnit("ms"),
		metric.WithDescription("Time spent waiting to acquire a DB queue semaphore slot"),
	)
	return err
}

// WorkerTaskSpanInfo describes the attributes used when starting a worker task span.
type WorkerTaskSpanInfo struct {
	JobID     string
	TaskID    string
	Domain    string
	Path      string
	FindLinks bool
}

// WorkerTaskMetrics describes a processed task for metric recording.
type WorkerTaskMetrics struct {
	JobID         string
	Status        string
	Duration      time.Duration
	QueueWait     time.Duration
	TotalDuration time.Duration
}

type WorkerTaskOutcomeMetrics struct {
	JobID    string
	Outcome  string
	Reason   string
	Duration time.Duration
}

type CrawlerPhaseMetrics struct {
	Phase    string
	Outcome  string
	Duration time.Duration
}

// StartWorkerTaskSpan starts a span for an individual worker task.
func StartWorkerTaskSpan(ctx context.Context, info WorkerTaskSpanInfo) (context.Context, trace.Span) {
	t := workerTracer
	if t == nil {
		t = otel.Tracer("hover/worker")
	}

	attrs := []attribute.KeyValue{
		attribute.String("job.id", info.JobID),
		attribute.String("task.id", info.TaskID),
		attribute.String("task.domain", info.Domain),
		attribute.String("task.path", info.Path),
		attribute.Bool("task.find_links", info.FindLinks),
	}

	return t.Start(ctx, "worker.process_task", trace.WithAttributes(attrs...))
}

// RecordWorkerTask emits worker task metrics when instrumentation is initialised.
//
// Note: job.id is intentionally dropped from metric labels to keep Prometheus
// active-series cardinality bounded. Use spans (which still carry job.id on
// the worker.process_task span attributes) to pivot per-job when needed.
func RecordWorkerTask(ctx context.Context, metrics WorkerTaskMetrics) {
	attrs := metric.WithAttributes(attribute.String("task.status", metrics.Status))

	if workerTaskDuration != nil {
		workerTaskDuration.Record(ctx, float64(metrics.Duration.Milliseconds()), attrs)
	}

	if workerTaskQueueWait != nil {
		workerTaskQueueWait.Record(ctx, float64(metrics.QueueWait.Milliseconds()), attrs)
	}

	if workerTaskTotalDuration != nil {
		workerTaskTotalDuration.Record(ctx, float64(metrics.TotalDuration.Milliseconds()), attrs)
	}

	if workerTaskTotal != nil {
		workerTaskTotal.Add(ctx, 1, attrs)
	}
}

// RecordWorkerTaskOutcome emits task processing duration grouped by outcome.
func RecordWorkerTaskOutcome(ctx context.Context, metrics WorkerTaskOutcomeMetrics) {
	attrs := []attribute.KeyValue{
		attribute.String("task.outcome", metrics.Outcome),
		attribute.String("task.reason", metrics.Reason),
	}

	if workerTaskOutcomeDuration != nil {
		workerTaskOutcomeDuration.Record(ctx, float64(metrics.Duration.Milliseconds()),
			metric.WithAttributes(attrs...))
	}

	if workerTaskOutcomeTotal != nil {
		workerTaskOutcomeTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// RecordCrawlerPhase emits duration and count metrics for a crawler phase.
func RecordCrawlerPhase(ctx context.Context, metrics CrawlerPhaseMetrics) {
	attrs := []attribute.KeyValue{
		attribute.String("crawler.phase", metrics.Phase),
		attribute.String("crawler.outcome", metrics.Outcome),
	}

	if crawlerPhaseDuration != nil {
		crawlerPhaseDuration.Record(ctx, float64(metrics.Duration.Milliseconds()),
			metric.WithAttributes(attrs...))
	}

	if crawlerPhaseTotal != nil {
		crawlerPhaseTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// RecordWorkerConcurrency records the change in concurrent tasks for a worker.
// delta: +1 when starting a task, -1 when completing
// capacity: the worker's concurrency limit (only recorded once per worker on startup)
func RecordWorkerConcurrency(ctx context.Context, workerID int, delta int64, capacity int64) {
	if workerConcurrentTasks != nil {
		workerConcurrentTasks.Add(ctx, delta,
			metric.WithAttributes(attribute.Int("worker.id", workerID)))
	}

	if capacity > 0 && workerConcurrencyLimit != nil {
		workerConcurrencyLimit.Record(ctx, capacity,
			metric.WithAttributes(attribute.Int("worker.id", workerID)))
	}
}

// RecordJobConcurrencySnapshot captures the running task count and concurrency limit for a job.
func RecordJobConcurrencySnapshot(ctx context.Context, jobID string, runningTasks int64, concurrencyLimit int64, unlimited bool) {
	if jobRunningTasksGauge != nil {
		jobRunningTasksGauge.Record(ctx, runningTasks,
			metric.WithAttributes(attribute.String("job.id", jobID)))
	}

	if jobConcurrencyLimitGauge != nil {
		jobConcurrencyLimitGauge.Record(ctx, concurrencyLimit,
			metric.WithAttributes(
				attribute.String("job.id", jobID),
				attribute.Bool("job.concurrency_unlimited", unlimited),
			))
	}
}

// Note: jobID argument is retained for API stability and future span/trace
// correlation, but is no longer attached as a metric label. See RecordWorkerTask.
func RecordJobInfoCacheHit(ctx context.Context, jobID string) {
	_ = jobID
	if jobInfoCacheHitsCounter == nil {
		return
	}
	jobInfoCacheHitsCounter.Add(ctx, 1)
}

func RecordJobInfoCacheMiss(ctx context.Context, jobID string) {
	_ = jobID
	if jobInfoCacheMissCounter == nil {
		return
	}
	jobInfoCacheMissCounter.Add(ctx, 1)
}

func RecordJobInfoCacheInvalidation(ctx context.Context, jobID, reason string) {
	_ = jobID
	if jobInfoCacheInvalidation == nil {
		return
	}
	jobInfoCacheInvalidation.Add(ctx, 1,
		metric.WithAttributes(attribute.String("cache.reason", reason)))
}

func RecordJobInfoCacheSize(ctx context.Context, size int) {
	if jobInfoCacheSizeGauge == nil {
		return
	}
	jobInfoCacheSizeGauge.Record(ctx, int64(size))
}

// DBPoolSnapshot describes a database connection pool state.
type DBPoolSnapshot struct {
	InUse        int
	Idle         int
	WaitCount    int64
	WaitDuration time.Duration
	MaxOpen      int
	Reserved     int
	Usage        float64
}

// RecordDBPoolStats records database pool utilisation metrics.
func RecordDBPoolStats(ctx context.Context, snapshot DBPoolSnapshot) {
	if dbPoolInUseGauge != nil {
		dbPoolInUseGauge.Record(ctx, int64(snapshot.InUse), metric.WithAttributes())
	}
	if dbPoolIdleGauge != nil {
		dbPoolIdleGauge.Record(ctx, int64(snapshot.Idle), metric.WithAttributes())
	}
	if dbPoolWaitCountGauge != nil {
		dbPoolWaitCountGauge.Record(ctx, snapshot.WaitCount, metric.WithAttributes())
	}
	if dbPoolWaitDurationGauge != nil {
		dbPoolWaitDurationGauge.Record(ctx, float64(snapshot.WaitDuration)/float64(time.Millisecond), metric.WithAttributes())
	}
	if dbPoolUsageGauge != nil {
		dbPoolUsageGauge.Record(ctx, snapshot.Usage, metric.WithAttributes())
	}
	if dbPoolMaxOpenGauge != nil {
		dbPoolMaxOpenGauge.Record(ctx, int64(snapshot.MaxOpen), metric.WithAttributes())
	}
	if dbPoolReservedGauge != nil {
		dbPoolReservedGauge.Record(ctx, int64(snapshot.Reserved), metric.WithAttributes())
	}
}

// RecordTaskClaimAttempt records the latency of claiming a task from the queue.
// jobID is retained for signature stability but not emitted as a label.
func RecordTaskClaimAttempt(ctx context.Context, jobID string, latency time.Duration, status string) {
	_ = jobID
	if workerTaskClaimLatency != nil {
		workerTaskClaimLatency.Record(ctx, float64(latency.Milliseconds()),
			metric.WithAttributes(attribute.String("claim.status", status)))
	}
}

// RecordWorkerTaskRetry records a retry attempt for a task.
func RecordWorkerTaskRetry(ctx context.Context, jobID string, reason string) {
	_ = jobID
	if workerTaskRetryCounter != nil {
		workerTaskRetryCounter.Add(ctx, 1,
			metric.WithAttributes(attribute.String("task.retry_reason", reason)))
	}
}

// RecordWorkerTaskFailure records a permanently failed task.
func RecordWorkerTaskFailure(ctx context.Context, jobID string, reason string) {
	_ = jobID
	if workerTaskFailureCounter != nil {
		workerTaskFailureCounter.Add(ctx, 1,
			metric.WithAttributes(attribute.String("task.failure_reason", reason)))
	}
}

// RecordTaskWaiting records when tasks move into the waiting queue along with the reason.
func RecordTaskWaiting(ctx context.Context, jobID string, reason string, count int) {
	_ = jobID
	if workerTaskWaitingCounter == nil || count <= 0 {
		return
	}

	workerTaskWaitingCounter.Add(ctx, int64(count),
		metric.WithAttributes(attribute.String("task.waiting_reason", reason)))
}

// RecordDBPoolRejection increments the pool rejection counter when requests are rejected before acquiring a connection.
func RecordDBPoolRejection(ctx context.Context) {
	if dbPoolRejectCounter != nil {
		dbPoolRejectCounter.Add(ctx, 1, metric.WithAttributes())
	}
}

// RecordDBPressureStats records the pressure controller's current EMA and concurrency limit.
// Call this alongside RecordDBPoolStats for a complete pool+pressure snapshot in Grafana.
func RecordDBPressureStats(ctx context.Context, emaMs float64, limit int32) {
	if dbPressureEMAGauge != nil {
		dbPressureEMAGauge.Record(ctx, emaMs)
	}
	if dbPressureLimitGauge != nil {
		dbPressureLimitGauge.Record(ctx, int64(limit))
	}
}

// RecordDBPressureAdjustment increments the adjustment counter.
// direction must be "up" or "down".
func RecordDBPressureAdjustment(ctx context.Context, direction string) {
	if dbPressureAdjustCounter != nil {
		dbPressureAdjustCounter.Add(ctx, 1,
			metric.WithAttributes(attribute.String("direction", direction)))
	}
}

// RecordSemaphoreWait records the time spent waiting to acquire a DB queue semaphore slot.
func RecordSemaphoreWait(ctx context.Context, waitMs float64) {
	if dbSemaphoreWaitHistogram != nil {
		dbSemaphoreWaitHistogram.Record(ctx, waitMs)
	}
}

// RecordFDStats records file descriptor usage metrics.
func RecordFDStats(ctx context.Context, current, limit int, pressure float64) {
	if fdCurrentGauge != nil {
		fdCurrentGauge.Record(ctx, int64(current), metric.WithAttributes())
	}
	if fdLimitGauge != nil {
		fdLimitGauge.Record(ctx, int64(limit), metric.WithAttributes())
	}
	if fdPressureGauge != nil {
		fdPressureGauge.Record(ctx, pressure, metric.WithAttributes())
	}
}

// initBrokerInstruments registers the Tier 1 and Tier 2 Redis broker
// metrics. Called once from Init().
func initBrokerInstruments(meterProvider *sdkmetric.MeterProvider) error {
	if meterProvider == nil {
		return nil
	}

	meter := meterProvider.Meter("hover/broker")

	var err error

	// --- Tier 1 ---
	brokerStreamLengthGauge, err = meter.Int64Gauge(
		"bee.broker.stream_length",
		metric.WithDescription("Current XLEN for a job's Redis stream"),
	)
	if err != nil {
		return err
	}

	brokerScheduledDepthGauge, err = meter.Int64Gauge(
		"bee.broker.scheduled_zset_depth",
		metric.WithDescription("Current ZCARD for a job's schedule ZSET"),
	)
	if err != nil {
		return err
	}

	brokerConsumerPendingGauge, err = meter.Int64Gauge(
		"bee.broker.consumer_pending",
		metric.WithDescription("Current XPENDING count for a job's consumer group"),
	)
	if err != nil {
		return err
	}

	brokerOutboxBacklogGauge, err = meter.Int64Gauge(
		"bee.broker.outbox_backlog",
		metric.WithDescription("Rows currently in task_outbox awaiting dispatch"),
	)
	if err != nil {
		return err
	}

	brokerOutboxAgeGauge, err = meter.Float64Gauge(
		"bee.broker.outbox_age_seconds",
		metric.WithUnit("s"),
		metric.WithDescription("Age of the oldest due task_outbox row (NOW - MIN(run_at))"),
	)
	if err != nil {
		return err
	}

	brokerRedisPingHistogram, err = meter.Float64Histogram(
		"bee.broker.redis_ping_duration_ms",
		metric.WithUnit("ms"),
		metric.WithDescription("Round-trip time of periodic Redis PING"),
	)
	if err != nil {
		return err
	}

	brokerDispatchCounter, err = meter.Int64Counter(
		"bee.broker.dispatch_total",
		metric.WithDescription("Dispatcher outcomes grouped by result (ok|err|capacity|paced)"),
	)
	if err != nil {
		return err
	}

	// --- Tier 2 ---
	brokerAutoclaimCounter, err = meter.Int64Counter(
		"bee.broker.autoclaim_total",
		metric.WithDescription("XAUTOCLAIM outcomes grouped by result (reclaimed|dead_letter)"),
	)
	if err != nil {
		return err
	}

	brokerMessageAgeHistogram, err = meter.Float64Histogram(
		"bee.broker.consumer_message_age_ms",
		metric.WithUnit("ms"),
		metric.WithDescription("Age of a stream message at the moment a consumer receives it"),
	)
	if err != nil {
		return err
	}

	brokerPacerPushbackCounter, err = meter.Int64Counter(
		"bee.broker.pacer_pushback_total",
		metric.WithDescription("Pacer pushbacks grouped by reason (gate|rate_limited)"),
	)
	if err != nil {
		return err
	}

	brokerPacerDelayHistogram, err = meter.Float64Histogram(
		"bee.broker.pacer_delay_ms",
		metric.WithUnit("ms"),
		metric.WithDescription("Effective per-domain pacing delay applied by TryAcquire"),
	)
	if err != nil {
		return err
	}

	brokerCounterSyncSkew, err = meter.Float64Histogram(
		"bee.broker.counter_sync_skew",
		metric.WithDescription("Absolute skew between Redis and Postgres running counters at sync time"),
	)
	if err != nil {
		return err
	}

	brokerRedisPoolInUse, err = meter.Int64Gauge(
		"bee.broker.redis_pool.in_use",
		metric.WithDescription("Number of active Redis connections checked out of the pool"),
	)
	if err != nil {
		return err
	}

	brokerRedisPoolIdle, err = meter.Int64Gauge(
		"bee.broker.redis_pool.idle",
		metric.WithDescription("Number of idle Redis connections in the pool"),
	)
	if err != nil {
		return err
	}

	brokerRedisPoolWait, err = meter.Int64Gauge(
		"bee.broker.redis_pool.wait",
		metric.WithDescription("Running total of pool acquisition waits"),
	)
	if err != nil {
		return err
	}

	brokerCounterPELSkewHistogram, err = meter.Float64Histogram(
		"bee.broker.counter_pel_skew",
		metric.WithDescription("Absolute skew between the Redis running counter and the authoritative XPENDING count at reconcile time"),
	)
	if err != nil {
		return err
	}

	brokerPELWithoutConsumerGauge, err = meter.Int64Gauge(
		"bee.broker.pel_without_consumer",
		metric.WithDescription("Count of jobs whose stream PEL is non-zero but which are absent from the worker's active-job set (frozen-job smoking gun)"),
	)
	return err
}

// BrokerStreamStats captures per-job broker depth probed from Redis.
type BrokerStreamStats struct {
	JobID          string
	StreamLength   int64
	ScheduledDepth int64
	Pending        int64
}

// RecordBrokerStreamStats emits Tier 1 per-job depth gauges.
func RecordBrokerStreamStats(ctx context.Context, s BrokerStreamStats) {
	attrs := metric.WithAttributes(attribute.String("job.id", s.JobID))
	if brokerStreamLengthGauge != nil {
		brokerStreamLengthGauge.Record(ctx, s.StreamLength, attrs)
	}
	if brokerScheduledDepthGauge != nil {
		brokerScheduledDepthGauge.Record(ctx, s.ScheduledDepth, attrs)
	}
	if brokerConsumerPendingGauge != nil {
		brokerConsumerPendingGauge.Record(ctx, s.Pending, attrs)
	}
}

// RecordBrokerOutbox emits the outbox backlog + age gauges.
func RecordBrokerOutbox(ctx context.Context, backlog int64, oldestAgeSeconds float64) {
	if brokerOutboxBacklogGauge != nil {
		brokerOutboxBacklogGauge.Record(ctx, backlog)
	}
	if brokerOutboxAgeGauge != nil {
		brokerOutboxAgeGauge.Record(ctx, oldestAgeSeconds)
	}
}

// RecordBrokerRedisPing emits the periodic Redis PING RTT.
func RecordBrokerRedisPing(ctx context.Context, duration time.Duration, ok bool) {
	if brokerRedisPingHistogram != nil {
		brokerRedisPingHistogram.Record(ctx, float64(duration.Milliseconds()),
			metric.WithAttributes(attribute.Bool("ping.ok", ok)))
	}
}

// RecordBrokerDispatch increments the dispatch outcomes counter.
// outcome values: "ok", "err", "capacity", "paced".
// jobID is retained for call-site stability but not emitted as a label.
func RecordBrokerDispatch(ctx context.Context, jobID, outcome string) {
	_ = jobID
	if brokerDispatchCounter == nil {
		return
	}
	brokerDispatchCounter.Add(ctx, 1,
		metric.WithAttributes(attribute.String("outcome", outcome)))
}

// RecordBrokerAutoclaim increments the autoclaim outcomes counter.
// result values: "reclaimed", "dead_letter".
func RecordBrokerAutoclaim(ctx context.Context, jobID, result string, count int) {
	_ = jobID
	if brokerAutoclaimCounter == nil || count <= 0 {
		return
	}
	brokerAutoclaimCounter.Add(ctx, int64(count),
		metric.WithAttributes(attribute.String("result", result)))
}

// RecordBrokerMessageAge records how long a stream message sat pending
// before a consumer received it. Call once per parsed XREADGROUP message.
func RecordBrokerMessageAge(ctx context.Context, jobID string, ageMs float64) {
	_ = jobID
	if brokerMessageAgeHistogram == nil {
		return
	}
	brokerMessageAgeHistogram.Record(ctx, ageMs)
}

// RecordBrokerPacerPushback increments the pushback counter.
// reason values: "gate" (domain-gate NX hold), "rate_limited" (release feedback).
// domain is retained for call-site stability but not emitted as a label
// (per-domain cardinality is unbounded at launch scale).
func RecordBrokerPacerPushback(ctx context.Context, domain, reason string) {
	_ = domain
	if brokerPacerPushbackCounter == nil {
		return
	}
	brokerPacerPushbackCounter.Add(ctx, 1,
		metric.WithAttributes(attribute.String("reason", reason)))
}

// RecordBrokerPacerDelay records the effective per-domain pacing delay
// observed at TryAcquire time. domain is retained for API stability but not
// emitted as a label.
func RecordBrokerPacerDelay(ctx context.Context, domain string, delayMs float64) {
	_ = domain
	if brokerPacerDelayHistogram == nil {
		return
	}
	brokerPacerDelayHistogram.Record(ctx, delayMs)
}

// RecordBrokerCounterSyncSkew records the absolute difference between
// the Redis running counter and Postgres running_tasks at sync time.
func RecordBrokerCounterSyncSkew(ctx context.Context, jobID string, skew float64) {
	if brokerCounterSyncSkew == nil {
		return
	}
	brokerCounterSyncSkew.Record(ctx, skew,
		metric.WithAttributes(attribute.String("job.id", jobID)))
}

// RecordBrokerCounterPELSkew records the absolute difference between the
// Redis HASH counter for a job and the authoritative XPENDING count
// observed during reconciliation. A persistent non-zero skew indicates
// the counter leaked (drift fix shipped in fix-broker-counter-drift).
// jobID retained for call-site stability; omitted from labels to keep
// cardinality bounded.
func RecordBrokerCounterPELSkew(ctx context.Context, jobID string, skew float64) {
	_ = jobID
	if brokerCounterPELSkewHistogram == nil {
		return
	}
	brokerCounterPELSkewHistogram.Record(ctx, skew)
}

// RecordBrokerPELWithoutConsumer emits the number of jobs with a non-zero
// stream PEL that are NOT in the worker's active-job set. In a healthy
// system this is always zero — a non-zero reading means dispatch/consume
// have diverged and those jobs' tasks are stalled.
func RecordBrokerPELWithoutConsumer(ctx context.Context, count int64) {
	if brokerPELWithoutConsumerGauge == nil {
		return
	}
	brokerPELWithoutConsumerGauge.Record(ctx, count)
}

// RedisPoolSnapshot mirrors the subset of *redis.PoolStats we care about.
type RedisPoolSnapshot struct {
	InUse int64
	Idle  int64
	Waits int64
}

// RecordBrokerRedisPool emits the Redis client pool gauges.
func RecordBrokerRedisPool(ctx context.Context, snap RedisPoolSnapshot) {
	if brokerRedisPoolInUse != nil {
		brokerRedisPoolInUse.Record(ctx, snap.InUse)
	}
	if brokerRedisPoolIdle != nil {
		brokerRedisPoolIdle.Record(ctx, snap.Idle)
	}
	if brokerRedisPoolWait != nil {
		brokerRedisPoolWait.Record(ctx, snap.Waits)
	}
}
