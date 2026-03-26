package observability

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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

	workerTaskQueueWait     metric.Float64Histogram
	workerTaskTotalDuration metric.Float64Histogram

	workerTaskClaimLatency metric.Float64Histogram

	workerTaskRetryCounter   metric.Int64Counter
	workerTaskFailureCounter metric.Int64Counter
	workerTaskWaitingCounter metric.Int64Counter

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

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(promExporter),
	)
	otel.SetMeterProvider(meterProvider)

	initOnce.Do(func() {
		workerTracer = tracerProvider.Tracer("hover/worker")
		_ = initWorkerInstruments(meterProvider)
		_ = initJobInstruments(meterProvider)
		_ = initDBPoolInstruments(meterProvider)
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

func getOTLPEndpointOption(endpoint string) otlptracehttp.Option {
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return otlptracehttp.WithEndpointURL(endpoint)
	}
	return otlptracehttp.WithEndpoint(endpoint)
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
func RecordWorkerTask(ctx context.Context, metrics WorkerTaskMetrics) {
	if workerTaskDuration != nil {
		workerTaskDuration.Record(ctx, float64(metrics.Duration.Milliseconds()),
			metric.WithAttributes(attribute.String("job.id", metrics.JobID), attribute.String("task.status", metrics.Status)))
	}

	if metrics.QueueWait > 0 && workerTaskQueueWait != nil {
		workerTaskQueueWait.Record(ctx, float64(metrics.QueueWait.Milliseconds()),
			metric.WithAttributes(attribute.String("job.id", metrics.JobID), attribute.String("task.status", metrics.Status)))
	}

	if metrics.TotalDuration > 0 && workerTaskTotalDuration != nil {
		workerTaskTotalDuration.Record(ctx, float64(metrics.TotalDuration.Milliseconds()),
			metric.WithAttributes(attribute.String("job.id", metrics.JobID), attribute.String("task.status", metrics.Status)))
	}

	if workerTaskTotal != nil {
		workerTaskTotal.Add(ctx, 1,
			metric.WithAttributes(attribute.String("job.id", metrics.JobID), attribute.String("task.status", metrics.Status)))
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

func RecordJobInfoCacheHit(ctx context.Context, jobID string) {
	if jobInfoCacheHitsCounter == nil {
		return
	}
	jobInfoCacheHitsCounter.Add(ctx, 1,
		metric.WithAttributes(attribute.String("job.id", jobID)))
}

func RecordJobInfoCacheMiss(ctx context.Context, jobID string) {
	if jobInfoCacheMissCounter == nil {
		return
	}
	jobInfoCacheMissCounter.Add(ctx, 1,
		metric.WithAttributes(attribute.String("job.id", jobID)))
}

func RecordJobInfoCacheInvalidation(ctx context.Context, jobID, reason string) {
	if jobInfoCacheInvalidation == nil {
		return
	}
	jobInfoCacheInvalidation.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("job.id", jobID),
			attribute.String("cache.reason", reason),
		))
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
func RecordTaskClaimAttempt(ctx context.Context, jobID string, latency time.Duration, status string) {
	if workerTaskClaimLatency != nil {
		workerTaskClaimLatency.Record(ctx, float64(latency.Milliseconds()),
			metric.WithAttributes(
				attribute.String("job.id", jobID),
				attribute.String("claim.status", status),
			))
	}
}

// RecordWorkerTaskRetry records a retry attempt for a task.
func RecordWorkerTaskRetry(ctx context.Context, jobID string, reason string) {
	if workerTaskRetryCounter != nil {
		workerTaskRetryCounter.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String("job.id", jobID),
				attribute.String("task.retry_reason", reason),
			))
	}
}

// RecordWorkerTaskFailure records a permanently failed task.
func RecordWorkerTaskFailure(ctx context.Context, jobID string, reason string) {
	if workerTaskFailureCounter != nil {
		workerTaskFailureCounter.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String("job.id", jobID),
				attribute.String("task.failure_reason", reason),
			))
	}
}

// RecordTaskWaiting records when tasks move into the waiting queue along with the reason.
func RecordTaskWaiting(ctx context.Context, jobID string, reason string, count int) {
	if workerTaskWaitingCounter == nil || count <= 0 {
		return
	}

	attrs := []attribute.KeyValue{
		attribute.String("task.waiting_reason", reason),
	}
	if jobID != "" {
		attrs = append(attrs, attribute.String("job.id", jobID))
	}

	workerTaskWaitingCounter.Add(ctx, int64(count), metric.WithAttributes(attrs...))
}

// RecordDBPoolRejection increments the pool rejection counter when requests are rejected before acquiring a connection.
func RecordDBPoolRejection(ctx context.Context) {
	if dbPoolRejectCounter != nil {
		dbPoolRejectCounter.Add(ctx, 1, metric.WithAttributes())
	}
}
