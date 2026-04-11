package jobs

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/rand"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Harvey-AU/hover/internal/archive"
	"github.com/Harvey-AU/hover/internal/crawler"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/Harvey-AU/hover/internal/storage"
	"github.com/Harvey-AU/hover/internal/techdetect"
	"github.com/Harvey-AU/hover/internal/util"
	"github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5"
	"github.com/lib/pq"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"golang.org/x/net/publicsuffix"
	"golang.org/x/sync/singleflight"
)

const (
	taskProcessingTimeout      = 2 * time.Minute
	poolSaturationBackoff      = 2 * time.Second
	defaultJobFailureThreshold = 20
	defaultRunningTaskBatch    = 4
	defaultRunningTaskFlush    = 50 * time.Millisecond
	discoveredLinksDBTimeout   = 30 * time.Second
	discoveredLinksMinRemain   = 8 * time.Second
	discoveredLinksMinTimeout  = 5 * time.Second

	// concurrencyBufferFactor controls the headroom applied when converting
	// job-level concurrency into worker capacity so we keep a small cushion
	// without overshooting.
	concurrencyBufferFactor = 1.1

	// fallbackJobConcurrency is used when a job does not report an explicit
	// concurrency (or the limiter has not yet seeded a value). This mirrors
	// the API default.
	fallbackJobConcurrency = 20

	// concurrencyBlockCooldown defines the window in which we consider a job
	// recently concurrency-blocked for the purposes of suppressing scale ups.
	concurrencyBlockCooldown = 30 * time.Second

	// maxWorkersProduction is the default cap; override with GNH_MAX_WORKERS.
	maxWorkersProduction = 160
	// maxWorkersStaging keeps preview/staging environments conservative.
	maxWorkersStaging = 10

	pendingRebalanceInterval = 5 * time.Minute
	pendingRebalanceJobLimit = 25
	pendingUnlimitedCap      = 100
	taskHTMLStorageBucket    = "task-html"
	taskHTMLContentEncoding  = "gzip"
	taskHTMLPersistQueueSize = 64
	taskHTMLPersistWorkers   = 8
	maxHTMLPersistWorkers    = 32
	maxHTMLPersistQueueSize  = 10_000
	taskHTMLDrainTimeout     = 15 * time.Second
	taskHTMLReadyRetryDelay  = 250 * time.Millisecond
	taskHTMLReadyMaxWait     = 5 * time.Second
	archivePingTimeout       = 10 * time.Second
	discoveredLinkQueueSize  = 256
	discoveredLinkWorkers    = 4
)

type storageUploader interface {
	Upload(ctx context.Context, bucket, path string, data []byte, contentType string) (string, error)
	UploadWithOptions(ctx context.Context, bucket, path string, data []byte, options storage.UploadOptions) (string, error)
	Delete(ctx context.Context, bucket, path string) error
	Download(ctx context.Context, bucket, path string) ([]byte, error)
}

type taskHTMLUpload struct {
	Bucket              string
	Path                string
	ContentType         string
	UploadContentType   string
	ContentEncoding     string
	SizeBytes           int64
	CompressedSizeBytes int64
	SHA256              string
	CapturedAt          time.Time
	Payload             []byte
}

type taskHTMLPersistRequest struct {
	Task        db.Task
	ContentType string
	Body        []byte
	CapturedAt  time.Time
}

type discoveredLinkPersistRequest struct {
	Task      Task
	Links     map[string][]string
	SourceURL string
}

// ErrDomainDelay is returned by processTask when the domain rate-limit window
// has not elapsed. The worker should requeue the task as waiting and immediately
// pick a different task rather than blocking on the delay.
var ErrDomainDelay = errors.New("domain rate limit delay")

// domainDelayPause is a short back-off applied after requeueing a domain-delayed
// task. It prevents a tight DB claim loop when all pending tasks belong to
// rate-limited domains.
const domainDelayPause = 100 * time.Millisecond

type WaitingReason string

const (
	waitingReasonDomainDelay    WaitingReason = "domain_delay"
	waitingReasonWorkerCapacity WaitingReason = "worker_capacity"
	waitingReasonConcurrencyCap WaitingReason = "concurrency_limit"
	waitingReasonBlockingRetry  WaitingReason = "blocking_retry"
	waitingReasonRetryableError WaitingReason = "retryable_error"
)

// JobPerformance tracks performance metrics for a specific job
type JobPerformance struct {
	RecentTasks  []int64   // Last 5 task response times for this job
	CurrentBoost int       // Current performance boost workers for this job
	LastCheck    time.Time // When we last evaluated this job
	// LastConcurrencyBlock captures when this job last hit a concurrency cap.
	LastConcurrencyBlock time.Time
}

type WorkerPool struct {
	db               *sql.DB
	dbQueue          DbQueueInterface
	dbConfig         *db.Config
	crawler          CrawlerInterface
	domainLimiter    *DomainLimiter
	batchManager     *db.BatchManager // Batch manager for task updates
	numWorkers       int
	jobs             map[string]bool
	jobsMutex        sync.RWMutex
	stopCh           chan struct{}
	wg               sync.WaitGroup
	recoveryInterval time.Duration
	stopping         atomic.Bool
	activeJobs       sync.WaitGroup
	baseWorkerCount  int
	currentWorkers   int
	maxWorkers       int // Maximum workers allowed (environment-specific)
	workersMutex     sync.RWMutex
	cleanupInterval  time.Duration
	notifyCh         chan struct{}
	jobManager       *JobManager // Reference to JobManager for duplicate checking

	// Per-worker task concurrency
	workerConcurrency int               // How many tasks each worker can process concurrently
	workerSemaphores  []chan struct{}   // One semaphore per worker to limit concurrent tasks
	workerWaitGroups  []*sync.WaitGroup // One wait group per worker for graceful shutdown

	// Performance scaling
	jobPerformance map[string]*JobPerformance
	perfMutex      sync.RWMutex

	// Job info cache to avoid repeated DB lookups
	jobInfoCache map[string]*JobInfo
	jobInfoMutex sync.RWMutex
	jobInfoGroup singleflight.Group

	// Job failure tracking
	jobFailureMutex     sync.Mutex
	jobFailureCounters  map[string]*jobFailureState
	jobFailureThreshold int

	// Priority update debouncing
	priorityMutex         sync.Mutex
	priorityUpdateTracker map[string]*priorityUpdateState

	// Idle worker scaling
	idleWorkers      map[int]time.Time // workerID -> when they went idle
	idleWorkersMutex sync.RWMutex
	lastScaleDown    time.Time
	lastScaleEval    time.Time
	idleThreshold    int           // from GNH_WORKER_IDLE_THRESHOLD (default 0 = disabled)
	scaleCooldown    time.Duration // from GNH_WORKER_SCALE_COOLDOWN_SECONDS (default 15s)

	// Health probe
	probeInterval time.Duration // from GNH_HEALTH_PROBE_INTERVAL_SECONDS (default 0 = disabled)

	// Background loop intervals
	promotionInterval        time.Duration // from GNH_QUOTA_PROMOTION_INTERVAL_SECONDS (default 5s, min 2s)
	taskMonitorInterval      time.Duration // from GNH_TASK_MONITOR_INTERVAL_SECONDS (default 10s, min 5s)
	waitingRecoveryInterval  time.Duration // from GNH_WAITING_RECOVERY_INTERVAL_SECONDS (default 2s, min 1s)
	linkDiscoveryMinPriority float64       // from GNH_LINK_DISCOVERY_MIN_PRIORITY (default 0.7)

	// Running task release batching
	runningTaskReleaseCh            chan string
	runningTaskReleaseBatchSize     int
	runningTaskReleaseFlushInterval time.Duration
	runningTaskReleaseMu            sync.Mutex
	runningTaskReleasePending       map[string]int

	// In-memory running_tasks tracking (avoids hot-row contention on the jobs row).
	// Counters are seeded from DB after reconciliation and kept in sync as tasks are
	// claimed and completed. Increments are flushed to DB asynchronously.
	runningTasksInMem       map[string]*atomic.Int64
	runningTasksInMemMu     sync.RWMutex
	runningTasksIncrPending map[string]int
	runningTasksIncrMu      sync.Mutex
	runningTasksIncrCh      chan string

	// Technology detection
	techDetector            *techdetect.Detector
	techDetectedDomains     map[int]bool // Domains already detected in this session
	techDetectedMutex       sync.RWMutex
	storageClient           storageUploader // For uploading HTML samples
	taskHTMLPersistCh       chan *taskHTMLPersistRequest
	taskHTMLWorkerCount     int
	taskHTMLPending         atomic.Int64
	discoveredLinkPersistCh chan *discoveredLinkPersistRequest
	discoveredLinkPending   atomic.Int64

	// Cold-storage archiver (nil when ARCHIVE_PROVIDER is unset)
	archiver *archive.Archiver
}

func (wp *WorkerPool) ensureDomainLimiter() *DomainLimiter {
	if wp.domainLimiter == nil {
		wp.domainLimiter = newDomainLimiter(wp.dbQueue)
	}
	return wp.domainLimiter
}

// activeJobCount returns the number of jobs currently tracked by the worker pool.
func (wp *WorkerPool) activeJobCount() int {
	wp.jobsMutex.RLock()
	defer wp.jobsMutex.RUnlock()
	return len(wp.jobs)
}

// IdleWorkerCount returns the number of workers currently idle.
func (wp *WorkerPool) IdleWorkerCount() int {
	wp.idleWorkersMutex.RLock()
	defer wp.idleWorkersMutex.RUnlock()
	return len(wp.idleWorkers)
}

func (wp *WorkerPool) logScalingDecision(decision, reason string, currentWorkers, targetWorkers int, metadata map[string]int) {
	var event *zerolog.Event
	if decision == "no_change" {
		event = log.Debug()
	} else {
		event = log.Info()
	}

	event = event.
		Str("decision", decision).
		Str("reason", reason).
		Int("current_workers", currentWorkers).
		Int("target_workers", targetWorkers).
		Int("max_workers", wp.maxWorkers).
		Int("active_jobs", wp.activeJobCount())

	for key, value := range metadata {
		event = event.Int(key, value)
	}

	event.Msg("Scaling evaluation completed")
}

func (wp *WorkerPool) recordWaitingTask(ctx context.Context, task *db.Task, reason WaitingReason) {
	if task == nil {
		return
	}

	observability.RecordTaskWaiting(ctx, task.JobID, string(reason), 1)

	log.Debug().
		Str("task_id", task.ID).
		Str("job_id", task.JobID).
		Str("waiting_reason", string(reason)).
		Msg("Task transitioned to waiting state")
}

func (wp *WorkerPool) fetchJobInfoFromDB(ctx context.Context, jobID string) (*JobInfo, error) {
	var (
		domainID                 int
		domainName               string
		crawlDelay               sql.NullInt64
		adaptiveDelay            sql.NullInt64
		adaptiveFloor            sql.NullInt64
		findLinks                bool
		allowCrossSubdomainLinks bool
		concurrency              int
	)

	err := wp.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT d.id, d.name, d.crawl_delay_seconds, d.adaptive_delay_seconds, d.adaptive_delay_floor_seconds,
			       j.find_links, j.allow_cross_subdomain_links, j.concurrency
			FROM domains d
			JOIN jobs j ON j.domain_id = d.id
			WHERE j.id = $1
		`, jobID).Scan(&domainID, &domainName, &crawlDelay, &adaptiveDelay, &adaptiveFloor, &findLinks, &allowCrossSubdomainLinks, &concurrency)
	})
	if err != nil {
		return nil, err
	}

	info := &JobInfo{
		DomainID:                 domainID,
		DomainName:               domainName,
		FindLinks:                findLinks,
		AllowCrossSubdomainLinks: allowCrossSubdomainLinks,
		Concurrency:              concurrency,
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

	return info, nil
}

func (wp *WorkerPool) loadJobInfo(ctx context.Context, jobID string, options *JobOptions) (*JobInfo, error) {
	wp.jobInfoMutex.RLock()
	if info, exists := wp.jobInfoCache[jobID]; exists {
		wp.jobInfoMutex.RUnlock()
		observability.RecordJobInfoCacheHit(ctx, jobID)
		return info, nil
	}
	wp.jobInfoMutex.RUnlock()

	observability.RecordJobInfoCacheMiss(ctx, jobID)

	val, err, _ := wp.jobInfoGroup.Do(jobID, func() (any, error) {
		info, fetchErr := wp.fetchJobInfoFromDB(ctx, jobID)
		if fetchErr != nil {
			return nil, fetchErr
		}

		if options != nil {
			info.FindLinks = options.FindLinks
			info.AllowCrossSubdomainLinks = options.AllowCrossSubdomainLinks
			if options.Concurrency > 0 {
				info.Concurrency = options.Concurrency
			}
		}

		wp.jobInfoMutex.Lock()
		wp.jobInfoCache[jobID] = info
		wp.jobInfoMutex.Unlock()
		wp.recordJobInfoCacheSize(ctx)

		return info, nil
	})

	if err != nil {
		return nil, err
	}

	if info, ok := val.(*JobInfo); ok {
		return info, nil
	}

	return nil, fmt.Errorf("unexpected job info type for job %s", jobID)
}

func (wp *WorkerPool) shouldThrottlePriorityUpdate(jobID string, priority float64) (bool, time.Duration) {
	var cooldown time.Duration
	var tier string

	switch {
	case priority >= 0.8:
		return false, 0
	case priority >= 0.4:
		cooldown = 5 * time.Second
		tier = "medium"
	default:
		cooldown = 30 * time.Second
		tier = "low"
	}

	now := time.Now()

	wp.priorityMutex.Lock()
	defer wp.priorityMutex.Unlock()

	state, exists := wp.priorityUpdateTracker[jobID]
	if !exists {
		state = &priorityUpdateState{}
		wp.priorityUpdateTracker[jobID] = state
	}

	var last time.Time
	switch tier {
	case "medium":
		last = state.lastMedium
	case "low":
		last = state.lastLow
	}

	if !last.IsZero() {
		elapsed := now.Sub(last)
		if elapsed < cooldown {
			return true, cooldown - elapsed
		}
	}

	switch tier {
	case "medium":
		state.lastMedium = now
	case "low":
		state.lastLow = now
	}

	return false, 0
}

func (wp *WorkerPool) recordJobInfoCacheSize(ctx context.Context) {
	wp.jobInfoMutex.RLock()
	size := len(wp.jobInfoCache)
	wp.jobInfoMutex.RUnlock()
	observability.RecordJobInfoCacheSize(ctx, size)
}

// JobInfo caches job-specific data that doesn't change during execution
type JobInfo struct {
	DomainID                 int
	DomainName               string
	FindLinks                bool
	AllowCrossSubdomainLinks bool
	CrawlDelay               int
	Concurrency              int
	AdaptiveDelay            int
	AdaptiveDelayFloor       int
	RobotsRules              *crawler.RobotsRules // Cached robots.txt rules for URL filtering
}

type jobFailureState struct {
	streak    int
	triggered bool
}

type priorityUpdateState struct {
	lastMedium time.Time
	lastLow    time.Time
}

func jobFailureThresholdFromEnv() int {
	threshold := defaultJobFailureThreshold
	if raw := strings.TrimSpace(os.Getenv("GNH_JOB_FAILURE_THRESHOLD")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			threshold = parsed
		}
	}
	return threshold
}

func idleThresholdFromEnv() int {
	if raw := strings.TrimSpace(os.Getenv("GNH_WORKER_IDLE_THRESHOLD")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			return parsed
		}
	}
	return 0 // Default disabled
}

func scaleCooldownFromEnv() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("GNH_WORKER_SCALE_COOLDOWN_SECONDS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			return time.Duration(parsed) * time.Second
		}
	}
	return 15 * time.Second // Default 15s
}

func probeIntervalFromEnv() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("GNH_HEALTH_PROBE_INTERVAL_SECONDS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 10 {
			return time.Duration(parsed) * time.Second
		}
	}
	return 0 // Default disabled
}

func runningTaskBatchSizeFromEnv() int {
	if raw := strings.TrimSpace(os.Getenv("GNH_RUNNING_TASK_BATCH_SIZE")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 {
			return parsed
		}
	}
	return defaultRunningTaskBatch
}

func htmlPersistWorkersFromEnv() int {
	if raw := strings.TrimSpace(os.Getenv("GNH_HTML_PERSIST_WORKERS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 {
			return min(parsed, maxHTMLPersistWorkers)
		}
	}
	return taskHTMLPersistWorkers
}

func htmlPersistQueueSizeFromEnv() int {
	if raw := strings.TrimSpace(os.Getenv("GNH_HTML_PERSIST_QUEUE_SIZE")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 {
			return min(parsed, maxHTMLPersistQueueSize)
		}
	}
	return taskHTMLPersistQueueSize
}

func linkDiscoveryMinPriorityFromEnv() float64 {
	const fallback = 0.7
	if raw := strings.TrimSpace(os.Getenv("GNH_LINK_DISCOVERY_MIN_PRIORITY")); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed >= 0 && parsed <= 1 {
			return parsed
		}
	}
	return fallback
}

func runningTaskFlushIntervalFromEnv() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("GNH_RUNNING_TASK_FLUSH_INTERVAL_MS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			return time.Duration(parsed) * time.Millisecond
		}
	}
	return defaultRunningTaskFlush
}

func maxWorkersFromEnv() int {
	if raw := strings.TrimSpace(os.Getenv("GNH_MAX_WORKERS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 {
			return parsed
		}
	}
	return maxWorkersProduction
}

func quotaPromotionIntervalFromEnv() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("GNH_QUOTA_PROMOTION_INTERVAL_SECONDS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 2 {
			return time.Duration(parsed) * time.Second
		}
	}
	return 5 * time.Second
}

func waitingRecoveryIntervalFromEnv() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("GNH_WAITING_RECOVERY_INTERVAL_SECONDS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 {
			return time.Duration(parsed) * time.Second
		}
	}
	if raw := strings.TrimSpace(os.Getenv("GNH_QUOTA_PROMOTION_INTERVAL_SECONDS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 {
			return time.Duration(parsed) * time.Second
		}
	}
	return 2 * time.Second
}

func taskMonitorIntervalFromEnv() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("GNH_TASK_MONITOR_INTERVAL_SECONDS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 5 {
			return time.Duration(parsed) * time.Second
		}
	}
	return 10 * time.Second
}

func NewWorkerPool(sqlDB *sql.DB, dbQueue DbQueueInterface, crawler CrawlerInterface, numWorkers int, workerConcurrency int, dbConfig *db.Config) *WorkerPool {
	// Validate inputs
	if sqlDB == nil {
		panic("database connection is required")
	}
	if dbQueue == nil {
		panic("database queue is required")
	}
	if crawler == nil {
		panic("crawler is required")
	}
	if numWorkers < 1 {
		panic("numWorkers must be at least 1")
	}
	if workerConcurrency < 1 || workerConcurrency > 20 {
		panic("workerConcurrency must be between 1 and 20")
	}
	if dbConfig == nil {
		panic("database configuration is required")
	}

	// Determine max workers: env override (GNH_MAX_WORKERS), staging hard cap, then default.
	maxWorkers := maxWorkersFromEnv()
	if env := os.Getenv("APP_ENV"); env == "staging" {
		maxWorkers = maxWorkersStaging
	}

	// Create batch manager before WorkerPool construction (db package reference must happen here)
	batchMgr := db.NewBatchManager(dbQueue)
	domainLimiter := newDomainLimiter(dbQueue)

	// Wire up domain limiter concurrency override to queue
	dbQueue.SetConcurrencyOverride(func(jobID string, domain string) int {
		return domainLimiter.GetEffectiveConcurrency(jobID, domain)
	})

	// Initialise per-worker structures for concurrency control
	workerSemaphores := make([]chan struct{}, numWorkers)
	workerWaitGroups := make([]*sync.WaitGroup, numWorkers)
	for i := range numWorkers {
		workerSemaphores[i] = make(chan struct{}, workerConcurrency)
		workerWaitGroups[i] = &sync.WaitGroup{}
	}

	failureThreshold := jobFailureThresholdFromEnv()
	idleThreshold := idleThresholdFromEnv()
	scaleCooldown := scaleCooldownFromEnv()
	probeInterval := probeIntervalFromEnv()
	runningTaskBatchSize := runningTaskBatchSizeFromEnv()
	runningTaskFlushInterval := runningTaskFlushIntervalFromEnv()
	runningTaskBuffer := max(numWorkers*workerConcurrency*2, 64)
	waitingRecoveryInterval := waitingRecoveryIntervalFromEnv()
	linkDiscoveryMinPriority := linkDiscoveryMinPriorityFromEnv()

	wp := &WorkerPool{
		db:              sqlDB,
		dbQueue:         dbQueue,
		dbConfig:        dbConfig,
		crawler:         crawler,
		domainLimiter:   domainLimiter,
		batchManager:    batchMgr,
		numWorkers:      numWorkers,
		baseWorkerCount: numWorkers,
		currentWorkers:  numWorkers,
		maxWorkers:      maxWorkers,
		jobs:            make(map[string]bool),

		stopCh:           make(chan struct{}),
		notifyCh:         make(chan struct{}, 1), // Buffer of 1 to prevent blocking
		recoveryInterval: 1 * time.Minute,
		cleanupInterval:  time.Minute,

		// Per-worker task concurrency
		workerConcurrency: workerConcurrency,
		workerSemaphores:  workerSemaphores,
		workerWaitGroups:  workerWaitGroups,

		// Performance scaling
		jobPerformance: make(map[string]*JobPerformance),

		// Job info cache
		jobInfoCache: make(map[string]*JobInfo),

		// Job failure tracking
		jobFailureCounters:  make(map[string]*jobFailureState),
		jobFailureThreshold: failureThreshold,

		priorityUpdateTracker: make(map[string]*priorityUpdateState),

		// Idle worker scaling
		idleWorkers:   make(map[int]time.Time),
		idleThreshold: idleThreshold,
		scaleCooldown: scaleCooldown,

		// Health probe
		probeInterval: probeInterval,

		// Background loop intervals
		promotionInterval:        quotaPromotionIntervalFromEnv(),
		taskMonitorInterval:      taskMonitorIntervalFromEnv(),
		waitingRecoveryInterval:  waitingRecoveryInterval,
		linkDiscoveryMinPriority: linkDiscoveryMinPriority,

		// Running task release batching
		runningTaskReleaseCh:            make(chan string, runningTaskBuffer),
		runningTaskReleaseBatchSize:     runningTaskBatchSize,
		runningTaskReleaseFlushInterval: runningTaskFlushInterval,
		runningTaskReleasePending:       make(map[string]int),

		// In-memory running_tasks tracking
		runningTasksInMem:       make(map[string]*atomic.Int64),
		runningTasksIncrPending: make(map[string]int),
		runningTasksIncrCh:      make(chan string, runningTaskBuffer),

		// Technology detection (initialised lazily to avoid startup errors)
		techDetectedDomains:     make(map[int]bool),
		discoveredLinkPersistCh: make(chan *discoveredLinkPersistRequest, discoveredLinkQueueSize),
	}

	// Initialise technology detector (non-fatal if it fails)
	if detector, err := techdetect.New(); err != nil {
		log.Warn().Err(err).Msg("Failed to initialise technology detector - tech detection disabled")
	} else {
		wp.techDetector = detector
		log.Info().Msg("Technology detector initialised")
	}

	// Initialise storage client for HTML uploads (non-fatal if not configured).
	// Local development may only have SUPABASE_AUTH_URL in older .env.local files.
	supabaseURL := strings.TrimSuffix(os.Getenv("SUPABASE_URL"), "/")
	if supabaseURL == "" && os.Getenv("APP_ENV") == "development" {
		supabaseURL = strings.TrimSuffix(os.Getenv("SUPABASE_AUTH_URL"), "/")
	}
	supabasePublishableKey := os.Getenv("SUPABASE_PUBLISHABLE_KEY")
	supabaseSecretKey := os.Getenv("SUPABASE_SERVICE_ROLE_KEY")
	if supabaseURL != "" && supabaseSecretKey != "" {
		wp.storageClient = storage.New(supabaseURL, supabasePublishableKey, supabaseSecretKey)
		htmlQueueSize := htmlPersistQueueSizeFromEnv()
		wp.taskHTMLWorkerCount = htmlPersistWorkersFromEnv()
		wp.taskHTMLPersistCh = make(chan *taskHTMLPersistRequest, htmlQueueSize)
		log.Info().Int("queue_size", htmlQueueSize).Int("workers", wp.taskHTMLWorkerCount).Msg("Storage client initialised for HTML uploads")
	} else {
		log.Debug().Msg("Storage client not configured - page HTML will not be stored (set SUPABASE_SERVICE_ROLE_KEY)")
	}

	// Initialise cold-storage archiver (gated by ARCHIVE_PROVIDER env var).
	archiveCfg := archive.ConfigFromEnv()
	switch {
	case archiveCfg == nil:
		log.Info().Msg("ARCHIVE: incomplete config or ARCHIVE_PROVIDER not set — archiving DISABLED")
	case wp.storageClient == nil:
		log.Error().Msg("ARCHIVE: storage client not available (missing SUPABASE_SERVICE_ROLE_KEY?) — archiving DISABLED")
	default:
		provider, err := archive.ProviderFromEnv()
		if err != nil {
			log.Error().Err(err).Msg("ARCHIVE: failed to create provider — archiving DISABLED")
		} else if provider == nil {
			log.Warn().Msg("ARCHIVE: provider env vars set but provider is nil — archiving DISABLED")
		} else {
			pingCtx, cancel := context.WithTimeout(context.Background(), archivePingTimeout)
			err := provider.Ping(pingCtx, archiveCfg.Bucket)
			cancel()
			if err != nil {
				log.Error().Err(err).
					Str("provider", archiveCfg.Provider).
					Str("bucket", archiveCfg.Bucket).
					Msg("ARCHIVE: cannot reach cold-storage bucket — archiving DISABLED")
			} else {
				src := archive.NewTaskHTMLSource(wp.dbQueue, archiveCfg.RetentionJobs)
				wp.archiver = archive.NewArchiver(provider, wp.storageClient, *archiveCfg, wp.dbQueue.MarkFullyArchivedJobs, src)
				log.Info().Str("provider", archiveCfg.Provider).Str("bucket", archiveCfg.Bucket).Msg("ARCHIVE: scheduler initialised — archiving ENABLED")
			}
		}
	}

	// Start the notification listener when we have connection details available.
	if hasNotificationConfig(dbConfig) {
		wp.wg.Go(func() {
			wp.listenForNotifications(context.Background())
		})
	} else {
		log.Debug().Msg("Skipping LISTEN/NOTIFY setup: database config lacks connection details")
	}

	return wp
}

func (wp *WorkerPool) Start(ctx context.Context) {
	log.Info().Int("workers", wp.numWorkers).Msg("Starting worker pool")

	// Reconcile running_tasks counters before starting workers
	// This prevents capacity leaks from deployments, crashes, or migration timing
	reconcileCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := wp.reconcileRunningTaskCounters(reconcileCtx); err != nil {
		sentry.CaptureException(err)
		log.Error().Err(err).Msg("Failed to reconcile running_tasks counters - workers may be blocked")
		// Continue startup even if reconciliation fails (logged for monitoring)
	}

	for i := 0; i < wp.numWorkers; i++ {
		i := i
		wp.wg.Go(func() {
			time.Sleep(time.Duration(i*50) * time.Millisecond)
			wp.worker(ctx, i)
		})
	}

	// Start the recovery monitor
	wp.wg.Go(func() {
		wp.recoveryMonitor(ctx)
	})

	// Run initial cleanup
	if err := wp.CleanupStuckJobs(ctx); err != nil {
		sentry.CaptureException(err)
		log.Error().Err(err).Msg("Failed to perform initial job cleanup")
	}

	// Recover jobs that were running before restart
	if err := wp.recoverRunningJobs(ctx); err != nil {
		sentry.CaptureException(err)
		log.Error().Err(err).Msg("Failed to recover running jobs on startup")
	}

	wp.StartTaskMonitor(ctx)
	wp.StartCleanupMonitor(ctx)
	wp.StartWaitingTaskRecoveryMonitor(ctx)
	wp.startRunningTaskReleaseLoop(ctx)
	wp.startRunningTaskIncrementLoop(ctx)
	wp.startTaskHTMLPersistenceLoop(ctx)
	wp.startDiscoveredLinkPersistenceLoop(ctx)

	// Start cold-storage archiver if configured.
	if wp.archiver != nil {
		wp.wg.Go(func() {
			wp.archiver.Run(ctx, wp.stopCh)
		})
	}

	// Start orphaned task cleanup loop
	wp.wg.Go(func() {
		wp.cleanupOrphanedTasksLoop(ctx)
	})
}

func (wp *WorkerPool) Stop() {
	// Only stop once - use atomic compare-and-swap to ensure thread safety
	if wp.stopping.CompareAndSwap(false, true) {
		log.Debug().Msg("Stopping worker pool")
		close(wp.stopCh)
		wp.wg.Wait()
		wp.flushRunningTaskReleases(context.Background())
		wp.flushRunningTaskIncrements(context.Background())
		// Stop batch manager to flush remaining updates
		if wp.batchManager != nil {
			wp.batchManager.Stop()
		}
		log.Debug().Msg("Worker pool stopped")
	}
}

// reconcileRunningTaskCounters resets running_tasks to match actual task status
// This fixes counter leaks from:
// - Deployment race conditions (tasks completing during graceful shutdown)
// - Crash recovery (batch manager unable to flush)
// - Migration backfill timing (tasks counted as running but completed before new code started)
func (wp *WorkerPool) reconcileRunningTaskCounters(ctx context.Context) error {
	log.Info().Msg("Reconciling running_tasks counters with actual task status")

	// Atomic query: Reset all running_tasks based on current task.status = 'running'
	// Returns jobs that had mismatched counters for observability
	query := `
		WITH actual_counts AS (
			SELECT
				job_id,
				COUNT(*) as actual_running
			FROM tasks
			WHERE status = 'running'
			GROUP BY job_id
		),
		reconciled_jobs AS (
			UPDATE jobs
			SET running_tasks = COALESCE(ac.actual_running, 0)
			FROM actual_counts ac
			WHERE jobs.id = ac.job_id
			  AND jobs.status IN ('running', 'pending')
			  AND jobs.running_tasks != COALESCE(ac.actual_running, 0)
			RETURNING
				jobs.id,
				jobs.running_tasks as old_value,
				COALESCE(ac.actual_running, 0) as new_value
		),
		zero_out_jobs AS (
			UPDATE jobs
			SET running_tasks = 0
			WHERE status IN ('running', 'pending')
			  AND running_tasks > 0
			  AND id NOT IN (SELECT job_id FROM actual_counts)
			RETURNING
				id,
				running_tasks as old_value,
				0 as new_value
		)
		SELECT
			id,
			old_value,
			new_value,
			old_value - new_value as leaked_tasks
		FROM (
			SELECT * FROM reconciled_jobs
			UNION ALL
			SELECT * FROM zero_out_jobs
		) combined
		ORDER BY leaked_tasks DESC
	`

	rows, err := wp.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to reconcile running_tasks counters: %w", err)
	}
	defer rows.Close()

	totalLeaked := 0
	jobsFixed := 0

	for rows.Next() {
		var jobID string
		var oldValue, newValue, leaked int

		if err := rows.Scan(&jobID, &oldValue, &newValue, &leaked); err != nil {
			log.Warn().Err(err).Msg("Failed to scan reconciliation result")
			continue
		}

		totalLeaked += leaked
		jobsFixed++

		log.Info().
			Str("job_id", jobID).
			Int("old_counter", oldValue).
			Int("actual_running", newValue).
			Int("leaked_tasks", leaked).
			Msg("Reconciled running_tasks counter")
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error reading reconciliation results: %w", err)
	}

	if totalLeaked > 0 {
		log.Warn().
			Int("total_leaked_tasks", totalLeaked).
			Int("jobs_fixed", jobsFixed).
			Msg("Running_tasks counters reconciled - capacity leak detected and fixed")
	} else {
		log.Info().Msg("Running_tasks counters already accurate - no reconciliation needed")
	}

	// Seed in-memory counters from DB so the Go-side concurrency gate is accurate
	// immediately after reconciliation (before any tasks are claimed).
	if err := wp.seedRunningTasksInMemFromDB(ctx); err != nil {
		log.Warn().Err(err).Msg("Failed to seed in-memory running_tasks counters - concurrency gate may be loose until first claim")
	}

	return nil
}

type jobCapacity struct {
	id      string
	cap     int
	pending int
}

var pendingOverflowJobsQuery = fmt.Sprintf(`
	SELECT
		id,
		CASE
			WHEN concurrency IS NULL OR concurrency = 0 THEN %d
			ELSE concurrency
		END as cap,
		pending_tasks
	FROM jobs
	WHERE status = 'running'
	  AND pending_tasks > CASE
			WHEN concurrency IS NULL OR concurrency = 0 THEN %d
			ELSE concurrency
		END
	ORDER BY pending_tasks - CASE
			WHEN concurrency IS NULL OR concurrency = 0 THEN %d
			ELSE concurrency
		END DESC
	LIMIT $1
`, pendingUnlimitedCap, pendingUnlimitedCap, pendingUnlimitedCap)

var rebalanceJobPendingQuery = `
	WITH ranked_tasks AS (
		SELECT
			id,
			ROW_NUMBER() OVER (
				ORDER BY priority_score DESC, created_at ASC
			) as rank
		FROM tasks
		WHERE job_id = $1
		  AND status = 'pending'
	)
	UPDATE tasks
	SET status = 'waiting'
	WHERE id IN (
		SELECT id FROM ranked_tasks WHERE rank > $2
	)
`

// rebalancePendingQueues ensures each running job's pending queue doesn't exceed concurrency
// This is a safety guardrail against bugs that cause pending queue overflow
// For each job, keeps only the highest-priority pending tasks (up to concurrency limit)
// and demotes the rest to waiting status
func (wp *WorkerPool) rebalancePendingQueues(ctx context.Context) error {
	if wp.db == nil {
		return errors.New("worker pool database is not configured")
	}

	runCtx := ctx
	if runCtx == nil {
		runCtx = context.Background()
	}

	log.Debug().Msg("Rebalancing pending queues across running jobs")

	// Get system-wide task status counts for observability
	var totalPending, totalWaiting, totalRunning, totalCompleted, totalFailed int
	err := wp.db.QueryRowContext(runCtx, `
		SELECT
			COALESCE(SUM(pending_tasks), 0) AS pending,
			COALESCE(SUM(waiting_tasks), 0) AS waiting,
			COALESCE(SUM(running_tasks), 0) AS running,
			COALESCE(SUM(completed_tasks), 0) AS completed,
			COALESCE(SUM(failed_tasks), 0) AS failed
		FROM jobs
		WHERE status = 'running'
	`).Scan(&totalPending, &totalWaiting, &totalRunning, &totalCompleted, &totalFailed)

	if err != nil {
		log.Warn().Err(err).Msg("Failed to get system task status counts")
	} else {
		log.Info().
			Int("pending", totalPending).
			Int("waiting", totalWaiting).
			Int("running", totalRunning).
			Int("completed", totalCompleted).
			Int("failed", totalFailed).
			Msg("System task status counts")
	}

	rows, err := wp.db.QueryContext(runCtx, pendingOverflowJobsQuery, pendingRebalanceJobLimit)
	if err != nil {
		return fmt.Errorf("failed to query pending queue overflows: %w", err)
	}
	defer rows.Close()

	var overflowJobs []jobCapacity
	for rows.Next() {
		var jc jobCapacity
		if err := rows.Scan(&jc.id, &jc.cap, &jc.pending); err != nil {
			return fmt.Errorf("failed to scan pending overflow row: %w", err)
		}
		overflowJobs = append(overflowJobs, jc)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error reading pending overflow rows: %w", err)
	}

	if len(overflowJobs) == 0 {
		log.Debug().Msg("All pending queues within limits - no rebalancing needed")
		return nil
	}

	totalDemoted := 0
	correctedJobs := 0

	for _, job := range overflowJobs {
		result, err := wp.db.ExecContext(runCtx, rebalanceJobPendingQuery, job.id, job.cap)
		if err != nil {
			log.Error().
				Err(err).
				Str("job_id", job.id).
				Msg("Failed to demote excess pending tasks")
			continue
		}

		demoted, err := result.RowsAffected()
		if err != nil {
			log.Warn().
				Err(err).
				Str("job_id", job.id).
				Msg("Failed to read demoted row count")
			continue
		}

		if demoted == 0 {
			continue
		}

		totalDemoted += int(demoted)
		correctedJobs++

		log.Info().
			Str("job_id", job.id).
			Int64("demoted_count", demoted).
			Msg("Demoted excess pending tasks to waiting")
	}

	if totalDemoted > 0 {
		log.Warn().
			Int("total_demoted", totalDemoted).
			Int("jobs_rebalanced", correctedJobs).
			Msg("Pending queue overflow detected and corrected")
	}

	return nil
}

// WaitForJobs waits for all active jobs to complete
func (wp *WorkerPool) WaitForJobs() {
	wp.activeJobs.Wait()
}

func (wp *WorkerPool) AddJob(jobID string, options *JobOptions) {
	wp.jobsMutex.Lock()
	wp.jobs[jobID] = true
	wp.jobsMutex.Unlock()

	// Seed in-memory running_tasks counter at zero for new jobs.
	// If a counter already exists (e.g. job was re-added) leave it in place.
	wp.runningTasksInMemMu.Lock()
	if _, exists := wp.runningTasksInMem[jobID]; !exists {
		wp.runningTasksInMem[jobID] = &atomic.Int64{}
	}
	wp.runningTasksInMemMu.Unlock()

	// Initialise performance tracking for this job
	wp.perfMutex.Lock()
	wp.jobPerformance[jobID] = &JobPerformance{
		RecentTasks:  make([]int64, 0, 5),
		CurrentBoost: 0,
		LastCheck:    time.Now(),
	}
	wp.perfMutex.Unlock()

	wp.jobFailureMutex.Lock()
	wp.jobFailureCounters[jobID] = &jobFailureState{}
	wp.jobFailureMutex.Unlock()

	// Cache job info to avoid repeated database lookups
	ctx := context.Background()
	jobInfo, err := wp.loadJobInfo(ctx, jobID, options)

	if err == nil {
		wp.ensureDomainLimiter().Seed(jobInfo.DomainName, jobInfo.CrawlDelay, jobInfo.AdaptiveDelay, jobInfo.AdaptiveDelayFloor)

		// Parse robots.txt to get filtering rules
		robotsRules, err := crawler.ParseRobotsTxt(ctx, jobInfo.DomainName, wp.crawler.GetUserAgent())
		if err != nil {
			log.Debug().
				Err(err).
				Str("domain", jobInfo.DomainName).
				Msg("Failed to parse robots.txt, proceeding without restrictions")
			// Only capture to Sentry if it's not a 404 (which is normal)
			if !strings.Contains(err.Error(), "404") {
				sentry.CaptureMessage(fmt.Sprintf("Failed to parse robots.txt for %s: %v", jobInfo.DomainName, err))
			}
			jobInfo.RobotsRules = &crawler.RobotsRules{} // Empty rules = no restrictions
		} else {
			jobInfo.RobotsRules = robotsRules
		}

		log.Trace().
			Str("job_id", jobID).
			Str("domain", jobInfo.DomainName).
			Int("crawl_delay", jobInfo.CrawlDelay).
			Int("concurrency", jobInfo.Concurrency).
			Int("disallow_patterns", len(jobInfo.RobotsRules.DisallowPatterns)).
			Msg("Cached job info with robots rules")
	} else {
		log.Error().Err(err).Str("job_id", jobID).Msg("Failed to cache job info")
		sentry.CaptureException(fmt.Errorf("failed to cache job info for job %s: %w", jobID, err))
	}

	targetWorkers := wp.calculateConcurrencyTarget()

	wp.workersMutex.RLock()
	currentWorkers := wp.currentWorkers
	wp.workersMutex.RUnlock()

	decision := "no_change"
	reason := "capacity_sufficient"

	if targetWorkers > currentWorkers {
		wp.scaleWorkers(context.Background(), targetWorkers)
		decision = "scale_up"
		reason = "job_added"
	} else if currentWorkers >= wp.maxWorkers {
		reason = "at_max_workers"
	}

	wp.logScalingDecision(decision, reason, currentWorkers, targetWorkers, nil)

	log.Debug().
		Str("job_id", jobID).
		Int("current_workers", currentWorkers).
		Int("target_workers", targetWorkers).
		Msg("Added job to worker pool")
}

// NotifyNewTasks wakes workers to check for new tasks immediately
// instead of waiting for the next task monitor tick (30 seconds).
func (wp *WorkerPool) NotifyNewTasks() {
	select {
	case wp.notifyCh <- struct{}{}:
		log.Debug().Msg("Notified workers of new tasks")
	default:
		// Channel already has notification pending
	}
}

// calculateConcurrencyTarget determines the desired worker count based on the
// sum of per-job concurrency limits (after domain limiter adjustments) and the
// configured per-worker concurrency. A small buffer is applied to keep the pool
// responsive without significantly overshooting.
func (wp *WorkerPool) calculateConcurrencyTarget() int {
	wp.jobsMutex.RLock()
	jobIDs := make([]string, 0, len(wp.jobs))
	for jobID := range wp.jobs {
		jobIDs = append(jobIDs, jobID)
	}
	wp.jobsMutex.RUnlock()

	if len(jobIDs) == 0 {
		target := min(wp.baseWorkerCount, wp.maxWorkers)
		return target
	}

	totalConcurrency := 0

	wp.jobInfoMutex.RLock()
	for _, jobID := range jobIDs {
		concurrency := fallbackJobConcurrency
		if jobInfo, exists := wp.jobInfoCache[jobID]; exists {
			if jobInfo.Concurrency > 0 {
				concurrency = jobInfo.Concurrency
			}
			if wp.domainLimiter != nil && jobInfo.DomainName != "" {
				if effective := wp.domainLimiter.GetEffectiveConcurrency(jobID, jobInfo.DomainName); effective > 0 {
					concurrency = effective
				}
			}
		}
		if concurrency < 1 {
			concurrency = 1
		}
		totalConcurrency += concurrency
	}
	wp.jobInfoMutex.RUnlock()

	perWorkerConcurrency := max(wp.workerConcurrency, 1)

	target := min(max(int(math.Ceil(float64(totalConcurrency)/float64(perWorkerConcurrency)*concurrencyBufferFactor)), wp.baseWorkerCount), wp.maxWorkers)

	return target
}

// recordConcurrencyBlock notes when a job hits its concurrency ceiling so that
// performance scaling can avoid fighting those limits and gradually release any
// boost that is no longer useful.
func (wp *WorkerPool) recordConcurrencyBlock(jobID string) {
	wp.perfMutex.Lock()
	if perf, exists := wp.jobPerformance[jobID]; exists {
		perf.LastConcurrencyBlock = time.Now()
		if perf.CurrentBoost > 0 {
			perf.CurrentBoost--
		}
	}
	wp.perfMutex.Unlock()
}

func (wp *WorkerPool) RemoveJob(jobID string) {
	wp.jobsMutex.Lock()
	delete(wp.jobs, jobID)
	wp.jobsMutex.Unlock()

	// Flush any pending running_tasks increments before removing the job's state.
	wp.flushRunningTaskIncrementForJob(context.Background(), jobID)
	wp.runningTasksInMemMu.Lock()
	delete(wp.runningTasksInMem, jobID)
	wp.runningTasksInMemMu.Unlock()

	// Remove performance boost for this job
	wp.perfMutex.Lock()
	var jobBoost int
	if perf, exists := wp.jobPerformance[jobID]; exists {
		jobBoost = perf.CurrentBoost
		delete(wp.jobPerformance, jobID)
	}
	wp.perfMutex.Unlock()

	// Remove from job info cache
	wp.jobInfoMutex.Lock()
	delete(wp.jobInfoCache, jobID)
	wp.jobInfoMutex.Unlock()
	observability.RecordJobInfoCacheInvalidation(context.Background(), jobID, "job_removed")
	wp.recordJobInfoCacheSize(context.Background())

	wp.priorityMutex.Lock()
	delete(wp.priorityUpdateTracker, jobID)
	wp.priorityMutex.Unlock()

	wp.jobFailureMutex.Lock()
	delete(wp.jobFailureCounters, jobID)
	wp.jobFailureMutex.Unlock()

	// Simple scaling: remove 5 workers per job + any performance boost, minimum of base count
	wp.workersMutex.Lock()
	oldWorkers := wp.currentWorkers
	targetWorkers := max(wp.currentWorkers-5-jobBoost, wp.baseWorkerCount)

	log.Debug().
		Str("job_id", jobID).
		Int("current_workers", oldWorkers).
		Int("target_workers", targetWorkers).
		Int("job_boost_removed", jobBoost).
		Msg("Scaling down worker pool")

	wp.currentWorkers = targetWorkers
	// Note: We don't actually stop excess workers, they'll exit on next task completion
	wp.workersMutex.Unlock()

	decision := "no_change"
	reason := "base_capacity"
	if targetWorkers < oldWorkers {
		decision = "scale_down"
		reason = "job_removed"
	}
	wp.logScalingDecision(decision, reason, oldWorkers, targetWorkers, map[string]int{
		"job_boost_removed": jobBoost,
	})

	log.Debug().
		Str("job_id", jobID).
		Msg("Removed job from worker pool")
}

func (wp *WorkerPool) resetJobFailureStreak(jobID string) {
	if jobID == "" || wp.jobFailureThreshold <= 0 {
		return
	}

	wp.jobFailureMutex.Lock()
	if state, ok := wp.jobFailureCounters[jobID]; ok && !state.triggered {
		state.streak = 0
	}
	wp.jobFailureMutex.Unlock()
}

func (wp *WorkerPool) recordJobFailure(ctx context.Context, jobID, taskID string, taskErr error) {
	if jobID == "" || wp.jobFailureThreshold <= 0 {
		return
	}

	wp.jobsMutex.RLock()
	_, active := wp.jobs[jobID]
	wp.jobsMutex.RUnlock()
	if !active {
		return
	}

	var (
		streak    int
		triggered bool
	)

	wp.jobFailureMutex.Lock()
	state, ok := wp.jobFailureCounters[jobID]
	if !ok {
		state = &jobFailureState{}
		wp.jobFailureCounters[jobID] = state
	}
	if !state.triggered {
		state.streak++
		streak = state.streak
		if state.streak >= wp.jobFailureThreshold {
			state.triggered = true
			triggered = true
		}
	}
	wp.jobFailureMutex.Unlock()

	if triggered {
		wp.markJobFailedDueToConsecutiveFailures(ctx, jobID, streak, taskErr)
		return
	}

	if streak > 0 {
		log.Trace().
			Str("job_id", jobID).
			Str("task_id", taskID).
			Int("failure_streak", streak).
			Int("threshold", wp.jobFailureThreshold).
			Msg("Incremented job failure streak")
	}
}

func (wp *WorkerPool) markJobFailedDueToConsecutiveFailures(ctx context.Context, jobID string, streak int, lastErr error) {
	message := fmt.Sprintf("Job failed after %d consecutive task failures", streak)
	if lastErr != nil {
		message = fmt.Sprintf("%s (last error: %s)", message, lastErr.Error())
	}

	log.Error().
		Str("job_id", jobID).
		Int("failure_streak", streak).
		Int("threshold", wp.jobFailureThreshold).
		Msg(message)

	failCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	updateErr := wp.dbQueue.Execute(failCtx, func(tx *sql.Tx) error {
		now := time.Now().UTC()

		// Mark job as failed
		_, err := tx.ExecContext(failCtx, `
			UPDATE jobs
			SET status = $1,
				completed_at = COALESCE(completed_at, $2),
				error_message = $3
			WHERE id = $4
				AND status <> $5
				AND status <> $6
		`, JobStatusFailed, now, message, jobID, JobStatusFailed, JobStatusCancelled)
		if err != nil {
			return fmt.Errorf("failed to update job status: %w", err)
		}

		// Clean up orphaned pending/waiting tasks
		result, err := tx.ExecContext(failCtx, `
			UPDATE tasks
			SET status = 'skipped',
				completed_at = $1,
				error = $2
			WHERE job_id = $3
				AND status IN ('pending', 'waiting')
		`, now, "Job failed due to consecutive task failures", jobID)
		if err != nil {
			return fmt.Errorf("failed to clean up orphaned tasks: %w", err)
		}

		orphanedCount, _ := result.RowsAffected()
		if orphanedCount > 0 {
			log.Info().
				Str("job_id", jobID).
				Int64("orphaned_tasks", orphanedCount).
				Msg("Cleaned up orphaned tasks from failed job")
		}

		return nil
	})

	if updateErr != nil {
		log.Error().Err(updateErr).Str("job_id", jobID).Msg("Failed to mark job as failed after consecutive task failures")
	}

	wp.RemoveJob(jobID)

	// Notifications are created by the database trigger (update_job_progress)
	// when job status transitions to 'failed'.
}

// processTaskResult handles the result from a task execution and updates backoff state
func (wp *WorkerPool) processTaskResult(result error, workerID int, consecutiveNoTasks *int) {
	if result != nil {
		if errors.Is(result, sql.ErrNoRows) || errors.Is(result, db.ErrConcurrencyBlocked) {
			*consecutiveNoTasks++

			// Trigger emergency scale-down when concurrency blocked
			if errors.Is(result, db.ErrConcurrencyBlocked) {
				wp.maybeEmergencyScaleDown()
			}
		} else if errors.Is(result, db.ErrPoolSaturated) {
			*consecutiveNoTasks = max(*consecutiveNoTasks, 1)
			log.Warn().Err(result).Int("worker_id", workerID).Msg("Control-lane DB saturation while claiming work")
		} else {
			log.Error().Err(result).Int("worker_id", workerID).Msg("Task processing failed")
		}
	} else {
		*consecutiveNoTasks = 0
		wp.clearWorkerIdle(workerID)
	}
}

// calculateBackoffSleep computes the sleep duration with exponential backoff and jitter
func calculateBackoffSleep(consecutiveNoTasks int, baseSleep, maxSleep time.Duration) time.Duration {
	baseSleepTime := min(time.Duration(float64(baseSleep)*math.Pow(1.5, float64(min(consecutiveNoTasks, 10)))), maxSleep)
	jitter := time.Duration(rand.Int63n(2000)) * time.Millisecond //nolint:gosec // 0-2s jitter for retry logic is not security-sensitive
	return baseSleepTime + jitter
}

func (wp *WorkerPool) worker(ctx context.Context, workerID int) {
	log.Info().
		Int("worker_id", workerID).
		Int("concurrency", wp.workerConcurrency).
		Msg("Starting worker")

	// Record worker capacity once at startup
	observability.RecordWorkerConcurrency(ctx, workerID, 0, int64(wp.workerConcurrency))

	// Get this worker's semaphore and wait group
	sem := wp.workerSemaphores[workerID]
	wg := wp.workerWaitGroups[workerID]

	// Create a context for this worker that we can cancel on exit
	workerCtx, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()

	// Track consecutive no-task counts for backoff (only in main goroutine)
	consecutiveNoTasks := 0
	maxSleep := 5 * time.Second         // Note: Changed from 30 to 5 seconds, to increase responsiveness when inactive.
	baseSleep := 200 * time.Millisecond // Faster processing when active

	// Channel to receive task results from concurrent goroutines
	type taskResult struct {
		err error
	}
	resultsCh := make(chan taskResult, wp.workerConcurrency)

	// Wait for all in-flight tasks to complete, then drain results channel
	defer func() {
		wg.Wait() // Wait for all task goroutines to complete
		// Now drain any remaining results
		for {
			select {
			case <-resultsCh:
				// Discard
			default:
				return
			}
		}
	}()

	for {
		// Check for stop signals or notifications
		select {
		case <-wp.stopCh:
			log.Debug().Int("worker_id", workerID).Msg("Worker received stop signal")
			return
		case <-ctx.Done():
			log.Debug().Int("worker_id", workerID).Msg("Worker context cancelled")
			return
		case <-wp.notifyCh:
			// Reset backoff when notified of new tasks
			consecutiveNoTasks = 0
		case result := <-resultsCh:
			// Process result from concurrent task goroutine
			wp.processTaskResult(result.err, workerID, &consecutiveNoTasks)
			continue
		default:
			// Continue to claim tasks
		}

		// Check if this worker should exit (we've scaled down)
		wp.workersMutex.RLock()
		shouldExit := workerID >= wp.currentWorkers
		wp.workersMutex.RUnlock()

		if shouldExit {
			return
		}

		// Apply backoff logic when no tasks are available
		if consecutiveNoTasks > 0 {
			// Track idle workers for scaling decisions (if feature enabled)
			if wp.idleThreshold > 0 && consecutiveNoTasks >= wp.idleThreshold {
				if wp.markWorkerIdle(workerID) {
					wp.maybeScaleDown()
				}
			}

			// Only log occasionally during quiet periods
			if consecutiveNoTasks == 1 || consecutiveNoTasks%10 == 0 {
				log.Debug().Int("consecutive_no_tasks", consecutiveNoTasks).Msg("Waiting for new tasks")
			}
			// Exponential backoff with a maximum, plus jitter to prevent thundering herd
			sleepTime := calculateBackoffSleep(consecutiveNoTasks, baseSleep, maxSleep)

			// Wait for either the backoff duration, a notification, or task completion
			select {
			case <-time.After(sleepTime):
				consecutiveNoTasks = 0 // Reset backoff to retry claiming
				wp.clearWorkerIdle(workerID)
			case <-wp.notifyCh:
				consecutiveNoTasks = 0
				wp.clearWorkerIdle(workerID)
			case result := <-resultsCh:
				wp.processTaskResult(result.err, workerID, &consecutiveNoTasks)
			case <-wp.stopCh:
				return
			case <-ctx.Done():
				return
			}
			continue // Loop back to check signals before attempting to claim
		}

		// Try to acquire a semaphore slot (non-blocking)
		select {
		case sem <- struct{}{}:
			// Successfully acquired slot, launch goroutine to process task
			wg.Go(func() {
				defer func() {
					<-sem // Release semaphore slot
					observability.RecordWorkerConcurrency(workerCtx, workerID, -1, 0)
				}()

				// Record task start
				observability.RecordWorkerConcurrency(workerCtx, workerID, +1, 0)

				err := wp.processNextTask(workerCtx)

				// Non-blocking send to prevent shutdown hang
				select {
				case resultsCh <- taskResult{err: err}:
				case <-workerCtx.Done():
					// Worker cancelled, don't block (covers both worker exit and parent context)
				}
			})
		default:
			// All slots full, wait briefly for a slot to free up or check for results
			select {
			case <-time.After(baseSleep):
			case result := <-resultsCh:
				wp.processTaskResult(result.err, workerID, &consecutiveNoTasks)
			case <-wp.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}
}

// claimPendingTask attempts to claim a pending task from any active job
func (wp *WorkerPool) claimPendingTask(ctx context.Context) (*db.Task, error) {
	// Get the list of active jobs
	wp.jobsMutex.RLock()
	activeJobs := make([]string, 0, len(wp.jobs))
	for jobID := range wp.jobs {
		activeJobs = append(activeJobs, jobID)
	}
	wp.jobsMutex.RUnlock()

	// If no active jobs, return immediately
	if len(activeJobs) == 0 {
		return nil, sql.ErrNoRows
	}

	// Track if we saw any concurrency-blocked jobs
	sawConcurrencyBlocked := false

	// Snapshot job info to reduce lock contention
	wp.jobInfoMutex.RLock()
	jobInfoSnapshot := make(map[string]*JobInfo, len(activeJobs))
	for _, jobID := range activeJobs {
		if info, exists := wp.jobInfoCache[jobID]; exists {
			jobInfoSnapshot[jobID] = info
		}
	}
	wp.jobInfoMutex.RUnlock()

	// Try to get a task from each active job
	for _, jobID := range activeJobs {
		// Skip jobs whose domain isn't available yet
		if wp.domainLimiter != nil {
			if jobInfo, exists := jobInfoSnapshot[jobID]; exists && jobInfo.DomainName != "" {
				if wp.domainLimiter.EstimatedWait(jobInfo.DomainName) > 0 {
					continue // Domain not available yet, try other jobs
				}
			}
		}

		// Go-side concurrency gate: atomically reserve a slot before hitting DB.
		// Uses FetchAdd to avoid TOCTOU: two goroutines both reading counter < limit
		// then both proceeding to GetNextTask, over-claiming by 1.
		var reservedInMem bool
		if jobInfo, exists := jobInfoSnapshot[jobID]; exists && jobInfo.Concurrency > 0 {
			wp.runningTasksInMemMu.RLock()
			counter, counterExists := wp.runningTasksInMem[jobID]
			wp.runningTasksInMemMu.RUnlock()
			if counterExists {
				prev := counter.Add(1) - 1
				if prev >= int64(jobInfo.Concurrency) {
					counter.Add(-1) // rollback: over concurrency limit
					sawConcurrencyBlocked = true
					wp.recordConcurrencyBlock(jobID)
					continue
				}
				reservedInMem = true
			}
		}

		task, err := wp.dbQueue.GetNextTask(ctx, jobID)
		if err == sql.ErrNoRows {
			if reservedInMem {
				wp.decrementRunningTaskInMem(jobID) // rollback reservation
			}
			continue // Try next job
		}
		if errors.Is(err, db.ErrPoolSaturated) {
			if reservedInMem {
				wp.decrementRunningTaskInMem(jobID) // rollback reservation
			}
			return nil, err
		}
		if err != nil {
			if reservedInMem {
				wp.decrementRunningTaskInMem(jobID) // rollback reservation
			}
			log.Error().Err(err).Str("job_id", jobID).Msg("Error getting next pending task")
			return nil, err // Return actual errors
		}
		if task != nil {
			if reservedInMem {
				// Slot already reserved atomically; just queue the async DB flush.
				wp.queueRunningTaskIncrDB(task.JobID)
			} else {
				// Counter didn't exist yet (first task for this job) — full increment.
				wp.incrementRunningTaskInMem(task.JobID)
			}
			log.Debug().
				Str("task_id", task.ID).
				Str("job_id", task.JobID).
				Int("page_id", task.PageID).
				Str("path", task.Path).
				Float64("priority", task.PriorityScore).
				Msg("Found and claimed pending task")
			return task, nil
		}
	}

	// If all jobs were concurrency-blocked, return that instead of no rows
	if sawConcurrencyBlocked {
		return nil, db.ErrConcurrencyBlocked
	}

	// No tasks found in any job
	return nil, sql.ErrNoRows
}

// prepareTaskForProcessing converts db.Task to jobs.Task and enriches with job info
func (wp *WorkerPool) prepareTaskForProcessing(ctx context.Context, task *db.Task) (*Task, error) {
	// Convert db.Task to jobs.Task for processing
	jobsTask := &Task{
		ID:            task.ID,
		JobID:         task.JobID,
		PageID:        task.PageID,
		Host:          task.Host,
		Path:          task.Path,
		Status:        TaskStatus(task.Status),
		CreatedAt:     task.CreatedAt,
		StartedAt:     task.StartedAt,
		RetryCount:    task.RetryCount,
		SourceType:    task.SourceType,
		SourceURL:     task.SourceURL,
		PriorityScore: task.PriorityScore,
	}

	// Get job info from cache
	wp.jobInfoMutex.RLock()
	jobInfo, exists := wp.jobInfoCache[task.JobID]
	wp.jobInfoMutex.RUnlock()

	if exists {
		jobsTask.DomainID = jobInfo.DomainID
		jobsTask.DomainName = jobInfo.DomainName
		jobsTask.FindLinks = jobInfo.FindLinks
		jobsTask.AllowCrossSubdomainLinks = jobInfo.AllowCrossSubdomainLinks
		jobsTask.CrawlDelay = jobInfo.CrawlDelay
		jobsTask.JobConcurrency = jobInfo.Concurrency
		jobsTask.AdaptiveDelay = jobInfo.AdaptiveDelay
		jobsTask.AdaptiveDelayFloor = jobInfo.AdaptiveDelayFloor
	} else {
		// Fallback to database if not in cache (shouldn't happen normally)
		log.Warn().Str("job_id", task.JobID).Msg("Job info not in cache, querying database")

		info, err := wp.loadJobInfo(ctx, task.JobID, nil)
		if err != nil {
			log.Error().Err(err).Str("job_id", task.JobID).Msg("Failed to get domain info")
		} else {
			jobsTask.DomainID = info.DomainID
			jobsTask.DomainName = info.DomainName
			jobsTask.FindLinks = info.FindLinks
			jobsTask.AllowCrossSubdomainLinks = info.AllowCrossSubdomainLinks
			jobsTask.CrawlDelay = info.CrawlDelay
			jobsTask.JobConcurrency = info.Concurrency
			jobsTask.AdaptiveDelay = info.AdaptiveDelay
			jobsTask.AdaptiveDelayFloor = info.AdaptiveDelayFloor
			wp.ensureDomainLimiter().Seed(info.DomainName, info.CrawlDelay, info.AdaptiveDelay, info.AdaptiveDelayFloor)
		}
	}
	if jobsTask.JobConcurrency <= 0 {
		jobsTask.JobConcurrency = 1
	}

	return jobsTask, nil
}

func (wp *WorkerPool) processNextTask(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			recoveredErr := fmt.Errorf("panic in processNextTask: %v", r)
			if hub := sentry.CurrentHub(); hub != nil {
				hub.Recover(r)
			} else {
				sentry.CaptureException(recoveredErr)
			}
			log.Error().
				Interface("panic", r).
				Bytes("stack", stack).
				Msg("Recovered from panic in processNextTask")
			if err == nil {
				err = recoveredErr
			}
		}
	}()

	// Claim a pending task from active jobs
	task, err := wp.claimPendingTask(ctx)
	if err != nil {
		return err
	}
	if task != nil {
		// Prepare task for processing with job info
		jobsTask, err := wp.prepareTaskForProcessing(ctx, task)
		if err != nil {
			log.Error().Err(err).Str("task_id", task.ID).Msg("Failed to prepare task")
			return err
		}

		// Process the task
		taskCtx, cancel := context.WithTimeout(ctx, taskProcessingTimeout)
		defer cancel()

		result, err := wp.processTask(taskCtx, jobsTask)
		if errors.Is(err, ErrDomainDelay) {
			// Domain rate-limit window not elapsed — requeue as waiting without
			// incrementing RetryCount or recording a failure. The worker is free
			// to immediately claim a task from a different domain.
			task.Status = string(TaskStatusWaiting)
			task.StartedAt = time.Time{}
			wp.decrementRunningTaskInMem(task.JobID)
			if err := wp.releaseRunningTaskSlot(task.JobID); err != nil {
				log.Error().Err(err).Str("job_id", task.JobID).Str("task_id", task.ID).
					Msg("Failed to decrement running_tasks counter on domain delay requeue")
			}
			wp.batchManager.QueueTaskUpdate(task)
			wp.recordWaitingTask(ctx, task, waitingReasonDomainDelay)
			log.Debug().
				Str("task_id", task.ID).
				Str("domain", jobsTask.DomainName).
				Msg("Task requeued as waiting: domain rate-limit window not elapsed")
			// Brief pause so the worker does not immediately spin back to the DB
			// when all pending tasks belong to rate-limited domains.
			select {
			case <-time.After(domainDelayPause):
			case <-ctx.Done():
			}
			return nil
		}
		if err != nil {
			return wp.handleTaskError(ctx, task, result, err)
		} else {
			return wp.handleTaskSuccess(ctx, task, result)
		}
	}

	// No tasks found in any active jobs
	return sql.ErrNoRows
}

// EnqueueURLs is a wrapper that ensures all task enqueuing goes through the JobManager.
// This allows for centralised logic, such as duplicate checking, to be applied.
func (wp *WorkerPool) EnqueueURLs(ctx context.Context, jobID string, pages []db.Page, sourceType string, sourceURL string) error {
	log.Debug().
		Str("job_id", jobID).
		Str("source_type", sourceType).
		Int("url_count", len(pages)).
		Msg("EnqueueURLs called via WorkerPool, passing to JobManager")

	// The jobManager must be set for the worker pool to function correctly.
	if wp.jobManager == nil {
		err := fmt.Errorf("jobManager is not set on WorkerPool - cannot enqueue URLs")
		log.Error().Err(err).Msg("Failed to enqueue URLs")
		return err
	}

	if err := wp.jobManager.EnqueueJobURLs(ctx, jobID, pages, sourceType, sourceURL); err != nil {
		if errors.Is(err, db.ErrPoolSaturated) {
			log.Warn().
				Str("job_id", jobID).
				Str("source_type", sourceType).
				Msg("Database pool saturated while enqueueing URLs; backing off")
			time.Sleep(poolSaturationBackoff)
		}
		return err
	}

	return nil
}

// StartTaskMonitor starts a background process that monitors for pending tasks
func (wp *WorkerPool) StartTaskMonitor(ctx context.Context) {
	log.Info().Msg("Starting task monitor to check for pending tasks")
	wp.wg.Go(func() {
		ticker := time.NewTicker(wp.taskMonitorInterval)
		defer ticker.Stop()

		// Pending queue rebalancer ticker - demote excess pending to waiting
		rebalanceTicker := time.NewTicker(pendingRebalanceInterval)
		defer rebalanceTicker.Stop()
		log.Info().
			Dur("interval", pendingRebalanceInterval).
			Msg("Pending queue rebalancer enabled")

		// Health probe ticker (optional, disabled by default)
		var probeTicker *time.Ticker
		var probeCh <-chan time.Time
		if wp.probeInterval > 0 {
			probeTicker = time.NewTicker(wp.probeInterval)
			probeCh = probeTicker.C
			defer probeTicker.Stop()
			log.Info().Dur("interval", wp.probeInterval).Msg("Health probe enabled")
		}

		for {
			select {
			case <-ctx.Done():
				log.Info().Msg("Task monitor stopped due to context cancellation")
				return
			case <-wp.stopCh:
				log.Info().Msg("Task monitor stopped due to stop signal")
				return
			case <-ticker.C:
				log.Debug().Msg("Task monitor checking for pending tasks")
				if err := wp.checkForPendingTasks(ctx); err != nil {
					log.Error().Err(err).Msg("Error checking for pending tasks")
				}
			case <-rebalanceTicker.C:
				log.Debug().Msg("Running pending queue rebalancer")
				if err := wp.rebalancePendingQueues(ctx); err != nil {
					log.Error().Err(err).Msg("Error rebalancing pending queues")
				}
			case <-probeCh:
				if probeCh != nil {
					wp.healthProbe(ctx)
				}
			}
		}
	})

	log.Info().Msg("Task monitor started successfully")
}

// checkForPendingTasks looks for any pending tasks and adds their jobs to the pool
func (wp *WorkerPool) checkForPendingTasks(ctx context.Context) error {
	log.Debug().Msg("Checking database for jobs with pending tasks")

	var jobIDs []string
	err := wp.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		// Query for jobs with pending tasks using job counters
		// Include pending jobs so fresh jobs after a reset get picked up immediately.
		rows, err := tx.QueryContext(ctx, `
			SELECT id
			FROM jobs
			WHERE status IN ('pending', 'running')
			  AND pending_tasks > 0
			LIMIT 100
		`)

		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var jobID string
			if err := rows.Scan(&jobID); err != nil {
				return err
			}
			jobIDs = append(jobIDs, jobID)
		}
		return rows.Err()
	})

	if err != nil {
		log.Error().Err(err).Msg("Failed to query for jobs with pending tasks")
		return err
	}

	jobsFound := len(jobIDs)
	foundIDs := jobIDs
	// For each job with pending tasks, add it to the worker pool
	for _, jobID := range jobIDs {
		// Check if already in our active jobs
		wp.jobsMutex.RLock()
		active := wp.jobs[jobID]
		wp.jobsMutex.RUnlock()

		if !active {
			// Add job to the worker pool
			log.Info().Str("job_id", jobID).Msg("Adding job with pending tasks to worker pool")

			// Get job options
			var findLinks bool
			var allowCrossSubdomainLinks bool
			err := wp.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
				return tx.QueryRowContext(ctx, `
					SELECT find_links, allow_cross_subdomain_links
					FROM jobs
					WHERE id = $1
				`, jobID).Scan(&findLinks, &allowCrossSubdomainLinks)
			})

			if err != nil {
				log.Error().Err(err).Str("job_id", jobID).Msg("Failed to get job options")
				continue
			}

			options := &JobOptions{
				FindLinks:                findLinks,
				AllowCrossSubdomainLinks: allowCrossSubdomainLinks,
			}

			wp.AddJob(jobID, options)

			// Update job status if needed
			err = wp.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `
					UPDATE jobs SET
						status = $1,
						started_at = CASE WHEN started_at IS NULL THEN $2 ELSE started_at END
					WHERE id = $3 AND status = $4
				`, JobStatusRunning, time.Now().UTC(), jobID, JobStatusPending)
				return err
			})

			if err != nil {
				log.Error().Err(err).Str("job_id", jobID).Msg("Failed to update job status")
			} else {
				log.Info().Str("job_id", jobID).Msg("Updated job status to running")
			}
		} else {
			log.Debug().Str("job_id", jobID).Msg("Job already active in worker pool")
		}
	}

	if jobsFound == 0 {
		log.Debug().Msg("No jobs with pending tasks found")
	} else {
		log.Debug().Int("count", jobsFound).Msg("Found jobs with pending tasks")
	}

	foundSet := make(map[string]struct{}, len(foundIDs))
	for _, id := range foundIDs {
		foundSet[id] = struct{}{}
	}
	var toRemove []string
	wp.jobsMutex.RLock()
	for jobID := range wp.jobs {
		if _, ok := foundSet[jobID]; !ok {
			toRemove = append(toRemove, jobID)
		}
	}
	wp.jobsMutex.RUnlock()
	for _, id := range toRemove {
		removable, err := wp.ensureJobSafeToRemove(ctx, id)
		if err != nil {
			log.Error().Err(err).Str("job_id", id).Msg("Failed to verify job removal safety")
			continue
		}
		if !removable {
			continue
		}

		log.Info().Str("job_id", id).Msg("Job has no pending tasks, removing from worker pool")
		wp.RemoveJob(id)
	}

	return nil
}

type jobQueueState struct {
	Status      string
	Pending     int
	Waiting     int
	Running     int
	Total       int
	Completed   int
	Failed      int
	Skipped     int
	Concurrency sql.NullInt64
	CreatedAt   time.Time
}

func (s jobQueueState) remainingWork() int {
	remaining := s.Total - (s.Completed + s.Failed + s.Skipped)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (s jobQueueState) availablePendingSlots() int {
	if s.Concurrency.Valid && s.Concurrency.Int64 > 0 {
		slots := int(s.Concurrency.Int64) - (s.Running + s.Pending)
		if slots < 0 {
			return 0
		}
		return slots
	}

	capacity := pendingUnlimitedCap - (s.Running + s.Pending)
	if capacity < 0 {
		return 0
	}
	return capacity
}

func (wp *WorkerPool) ensureJobSafeToRemove(ctx context.Context, jobID string) (bool, error) {
	state, err := wp.loadJobQueueState(ctx, jobID)
	if err != nil {
		return false, err
	}

	switch JobStatus(state.Status) {
	case JobStatusCompleted, JobStatusCancelled, JobStatusFailed:
		return true, nil
	case JobStatusInitialising:
		// Job is still discovering sitemap URLs — do not remove yet,
		// but fail-safe if the goroutine hung or crashed.
		const initialisingTimeout = 35 * time.Minute
		if !state.CreatedAt.IsZero() && time.Since(state.CreatedAt) > initialisingTimeout {
			if err := wp.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `
					UPDATE jobs
					SET status = $1, completed_at = $2, error_message = $3
					WHERE id = $4 AND status = $5
				`, JobStatusFailed, time.Now().UTC(), "Job timed out while initialising", jobID, JobStatusInitialising)
				return err
			}); err != nil {
				return false, err
			}
			log.Warn().Str("job_id", jobID).Msg("Timed out stale initialising job")
			return true, nil
		}
		return false, nil
	}

	if state.remainingWork() == 0 && state.Pending == 0 && state.Waiting == 0 && state.Running == 0 {
		if err := wp.markJobCompleted(ctx, jobID); err != nil {
			return false, fmt.Errorf("failed to mark job %s complete: %w", jobID, err)
		}
		return true, nil
	}

	if state.Running > 0 {
		recovered, recErr := wp.requeueStaleRunningTasks(ctx, jobID, time.Now().UTC().Add(-TaskStaleTimeout))
		if recErr != nil {
			return false, recErr
		}
		if recovered > 0 {
			log.Warn().
				Str("job_id", jobID).
				Int64("tasks_requeued", recovered).
				Msg("Recovered stale running tasks before removing job")
		}
		return false, nil
	}

	if state.Waiting > 0 {
		slots := state.availablePendingSlots()
		if slots <= 0 {
			return false, nil
		}
		promoted, promoteErr := wp.promoteWaitingTasks(ctx, jobID, slots)
		if promoteErr != nil {
			return false, promoteErr
		}
		if promoted > 0 {
			log.Info().
				Str("job_id", jobID).
				Int64("tasks_promoted", promoted).
				Int("available_slots", slots).
				Msg("Promoted waiting tasks to pending before job removal")
		}
		return false, nil
	}

	if state.Pending > 0 {
		return false, nil
	}

	if err := wp.markJobCompleted(ctx, jobID); err != nil {
		return false, fmt.Errorf("failed to mark quiet job %s complete: %w", jobID, err)
	}
	return true, nil
}

func (wp *WorkerPool) loadJobQueueState(ctx context.Context, jobID string) (*jobQueueState, error) {
	state := &jobQueueState{}
	err := wp.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT status, pending_tasks, waiting_tasks, running_tasks,
			       total_tasks, completed_tasks, failed_tasks, skipped_tasks, concurrency, created_at
			FROM jobs
			WHERE id = $1
		`, jobID).Scan(
			&state.Status,
			&state.Pending,
			&state.Waiting,
			&state.Running,
			&state.Total,
			&state.Completed,
			&state.Failed,
			&state.Skipped,
			&state.Concurrency,
			&state.CreatedAt,
		)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load job state: %w", err)
	}
	return state, nil
}

func (wp *WorkerPool) markJobCompleted(ctx context.Context, jobID string) error {
	// Notifications are now created by the database trigger (update_job_progress)
	// when job status transitions to 'completed'. This ensures notifications are
	// created regardless of which code path completes the job.
	return wp.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		// Only update if job is not already in a terminal state (cancelled, failed)
		// This prevents race conditions where a job was cancelled but running tasks complete
		_, err := tx.ExecContext(ctx, `
			UPDATE jobs
			SET status = $1,
				completed_at = COALESCE(completed_at, $2),
				progress = 100.0
			WHERE id = $3
			  AND status NOT IN ($4, $5)
		`, JobStatusCompleted, time.Now().UTC(), jobID, JobStatusCancelled, JobStatusFailed)
		return err
	})
}

func (wp *WorkerPool) promoteWaitingTasks(ctx context.Context, jobID string, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	var promoted int64
	err := wp.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			WITH cte AS (
				SELECT id
				FROM tasks
				WHERE job_id = $1 AND status = $2
				ORDER BY created_at ASC
				LIMIT $3
			)
			UPDATE tasks
			SET status = $4,
				started_at = NULL
			WHERE id IN (SELECT id FROM cte)
		`, jobID, TaskStatusWaiting, limit, TaskStatusPending)
		if err != nil {
			return err
		}
		promoted, err = result.RowsAffected()
		return err
	})
	return promoted, err
}

func (wp *WorkerPool) requeueStaleRunningTasks(ctx context.Context, jobID string, staleBefore time.Time) (int64, error) {
	var recovered int64
	err := wp.dbQueue.ExecuteMaintenance(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			WITH cte AS (
				SELECT id
				FROM tasks
				WHERE job_id = $1
				  AND status = $2
				  AND (started_at IS NULL OR started_at < $3)
				ORDER BY started_at NULLS FIRST
				LIMIT 200
			)
			UPDATE tasks
			SET status = $4,
				started_at = NULL,
				retry_count = retry_count + 1
			WHERE id IN (SELECT id FROM cte)
		`, jobID, TaskStatusRunning, staleBefore, TaskStatusPending)
		if err != nil {
			return err
		}
		recovered, err = result.RowsAffected()
		return err
	})
	if err != nil || recovered == 0 {
		return recovered, err
	}
	if decErr := wp.dbQueue.DecrementRunningTasksBy(ctx, jobID, int(recovered)); decErr != nil {
		return recovered, fmt.Errorf("failed to release running slots for job %s: %w", jobID, decErr)
	}
	wp.decrementRunningTaskInMemBy(jobID, int(recovered))
	return recovered, nil
}

// SetJobManager sets the JobManager reference for duplicate task checking
func (wp *WorkerPool) SetJobManager(jm *JobManager) {
	wp.jobManager = jm
}

// recoverStaleTasks checks for and resets stale tasks in batches
// Processes oldest tasks first, handles cancelled/failed jobs separately
func (wp *WorkerPool) recoverStaleTasks(ctx context.Context) error {
	const batchSize = 100
	staleTime := time.Now().UTC().Add(-TaskStaleTimeout)

	// STEP 1: Handle tasks from cancelled/failed jobs separately
	// These should be marked as failed immediately, not retried
	cancelledCount, err := wp.recoverTasksFromDeadJobs(ctx, staleTime)
	if err != nil {
		log.Error().Err(err).Msg("Failed to recover tasks from cancelled/failed jobs")
		// Don't fail the whole recovery, continue to running jobs
	} else if cancelledCount > 0 {
		log.Info().
			Int64("tasks_marked_failed", cancelledCount).
			Msg("Marked stuck tasks from cancelled/failed jobs as failed")
	}

	// STEP 2: Process tasks from running jobs in batches
	totalRecovered := 0
	totalFailed := 0
	batchNum := 0
	consecutiveFailures := 0
	const maxConsecutiveFailures = 5

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		batchNum++
		recovered, failed, err := wp.recoverStaleBatch(ctx, staleTime, batchSize, batchNum)

		if err != nil {
			consecutiveFailures++
			log.Warn().
				Err(err).
				Int("batch_num", batchNum).
				Int("consecutive_failures", consecutiveFailures).
				Msg("Batch recovery failed")

			// If we've failed too many times in a row, bail out
			if consecutiveFailures >= maxConsecutiveFailures {
				log.Error().
					Int("max_failures", maxConsecutiveFailures).
					Msg("Recovery failed after max consecutive failures, will retry next cycle")
				return fmt.Errorf("recovery failed after %d consecutive batch failures: %w", maxConsecutiveFailures, err)
			}

			// Exponential backoff between failed batches
			backoff := time.Duration(consecutiveFailures) * time.Second
			log.Debug().
				Dur("backoff", backoff).
				Msg("Sleeping before retrying next batch")
			time.Sleep(backoff)
			continue
		}

		// Success - reset failure counter
		consecutiveFailures = 0
		totalRecovered += recovered
		totalFailed += failed

		// If we processed fewer than batchSize, we're done
		if recovered+failed < batchSize {
			break
		}

		// Small delay between batches to avoid overwhelming the database
		time.Sleep(100 * time.Millisecond)
	}

	if totalRecovered > 0 || totalFailed > 0 {
		log.Info().
			Int("tasks_recovered", totalRecovered).
			Int("tasks_failed", totalFailed).
			Int("batches_processed", batchNum).
			Msg("Completed stale task recovery")

		if err := wp.reconcileRunningTaskCounters(ctx); err != nil {
			log.Error().Err(err).Msg("Failed to reconcile running task counters after stale task recovery")
		}
	}

	return nil
}

// recoverTasksFromDeadJobs marks tasks from cancelled/failed jobs as failed
func (wp *WorkerPool) recoverTasksFromDeadJobs(ctx context.Context, staleTime time.Time) (int64, error) {
	var totalAffected int64

	// Process in batches to avoid transaction timeout
	for {
		var affected int64
		err := wp.dbQueue.ExecuteMaintenance(ctx, func(tx *sql.Tx) error {
			result, err := tx.ExecContext(ctx, `
				UPDATE tasks
				SET status = $1,
					error = $2,
					completed_at = $3
				FROM jobs j
				WHERE tasks.job_id = j.id
					AND tasks.status = $4
					AND tasks.started_at < $5
					AND j.status IN ($6, $7)
					AND tasks.id IN (
						SELECT t.id
						FROM tasks t
						JOIN jobs j2 ON t.job_id = j2.id
						WHERE t.status = $4
							AND t.started_at < $5
							AND j2.status IN ($6, $7)
						ORDER BY t.started_at ASC
						LIMIT 100
					)
			`, TaskStatusFailed, "Job was cancelled or failed", time.Now().UTC(),
				TaskStatusRunning, staleTime, JobStatusCancelled, JobStatusFailed)

			if err != nil {
				return err
			}

			affected, err = result.RowsAffected()
			return err
		})

		if err != nil {
			return totalAffected, err
		}

		totalAffected += affected

		// If we updated fewer than 100, we're done
		if affected < 100 {
			break
		}
	}

	return totalAffected, nil
}

// recoverStaleBatch processes one batch of stale tasks
func (wp *WorkerPool) recoverStaleBatch(ctx context.Context, staleTime time.Time, batchSize int, batchNum int) (recovered int, failed int, err error) {
	err = wp.dbQueue.ExecuteMaintenance(ctx, func(tx *sql.Tx) error {
		// Query for one batch of stale tasks, oldest first
		// Note: We recover stuck tasks regardless of job status to prevent tasks
		// from being orphaned when jobs are marked completed/cancelled/failed
		rows, err := tx.QueryContext(ctx, `
			SELECT t.id, t.retry_count, t.job_id
			FROM tasks t
			WHERE t.status = $1
				AND t.started_at < $2
			ORDER BY t.started_at ASC
			LIMIT $3
		`, TaskStatusRunning, staleTime, batchSize)

		if err != nil {
			return err
		}
		defer rows.Close()

		type staleTask struct {
			id         string
			retryCount int
			jobID      string
		}

		var tasks []staleTask
		for rows.Next() {
			var task staleTask
			if err := rows.Scan(&task.id, &task.retryCount, &task.jobID); err != nil {
				log.Warn().Err(err).Msg("Failed to scan stale task row")
				continue
			}
			tasks = append(tasks, task)
		}

		if err := rows.Err(); err != nil {
			return err
		}

		if len(tasks) == 0 {
			return nil // No tasks to process
		}

		log.Debug().
			Int("batch_num", batchNum).
			Int("batch_size", len(tasks)).
			Msg("Processing stale task batch")

		// Update tasks in this batch
		now := time.Now().UTC()
		for _, task := range tasks {
			if task.retryCount >= MaxTaskRetries {
				_, err = tx.ExecContext(ctx, `
					UPDATE tasks
					SET status = $1,
						error = $2,
						completed_at = $3
					WHERE id = $4
				`, TaskStatusFailed, "Max retries exceeded", now, task.id)

				if err != nil {
					log.Warn().
						Err(err).
						Str("task_id", task.id).
						Msg("Failed to mark task as failed")
					return err
				}
				failed++
			} else {
				_, err = tx.ExecContext(ctx, `
					UPDATE tasks
					SET status = $1,
						started_at = NULL,
						retry_count = retry_count + 1
					WHERE id = $2
				`, TaskStatusPending, task.id)

				if err != nil {
					log.Warn().
						Err(err).
						Str("task_id", task.id).
						Msg("Failed to reset task to pending")
					return err
				}
				recovered++
			}
		}

		log.Debug().
			Int("batch_num", batchNum).
			Int("recovered", recovered).
			Int("failed", failed).
			Msg("Completed batch recovery")

		return nil
	})

	return recovered, failed, err
}

// recoverRunningJobs finds jobs that were in 'running' state when the server shut down
// and resets their 'running' tasks to 'pending', then adds them to the worker pool
func (wp *WorkerPool) recoverRunningJobs(ctx context.Context) error {
	log.Info().Msg("Recovering jobs that were running before restart")

	var jobIDs []string
	err := wp.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		// Find jobs with 'running' status that have 'running' tasks
		rows, err := tx.QueryContext(ctx, `
			SELECT DISTINCT j.id
			FROM jobs j
			JOIN tasks t ON j.id = t.job_id
			WHERE j.status = $1
			AND t.status = $2
		`, JobStatusRunning, TaskStatusRunning)

		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var jobID string
			if err := rows.Scan(&jobID); err != nil {
				return err
			}
			jobIDs = append(jobIDs, jobID)
		}

		return rows.Err()
	})

	if err != nil {
		log.Error().Err(err).Msg("Failed to query for running jobs with running tasks")
		return err
	}

	var recoveredJobs []string
	for _, jobID := range jobIDs {

		// Reset running tasks to pending for this job
		err := wp.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
			result, err := tx.ExecContext(ctx, `
				UPDATE tasks 
				SET status = $1,
					started_at = NULL,
					retry_count = retry_count + 1
				WHERE job_id = $2 
				AND status = $3
			`, TaskStatusPending, jobID, TaskStatusRunning)

			if err != nil {
				return err
			}

			rowsAffected, _ := result.RowsAffected()
			log.Info().
				Str("job_id", jobID).
				Int64("tasks_reset", rowsAffected).
				Msg("Reset running tasks to pending")

			return nil
		})

		if err != nil {
			log.Error().Err(err).Str("job_id", jobID).Msg("Failed to reset running tasks")
			continue
		}

		// Add job back to worker pool
		wp.AddJob(jobID, nil)
		recoveredJobs = append(recoveredJobs, jobID)

		log.Info().Str("job_id", jobID).Msg("Recovered running job and added to worker pool")
	}

	if len(recoveredJobs) > 0 {
		log.Info().
			Int("count", len(recoveredJobs)).
			Strs("job_ids", recoveredJobs).
			Msg("Successfully recovered running jobs from restart")

		if err := wp.reconcileRunningTaskCounters(ctx); err != nil {
			log.Error().Err(err).Msg("Failed to reconcile running task counters after job recovery")
		}
	} else {
		log.Debug().Msg("No running jobs found to recover")
	}

	return nil
}

// recoveryMonitor periodically checks for and recovers stale tasks
func (wp *WorkerPool) recoveryMonitor(ctx context.Context) {
	ticker := time.NewTicker(wp.recoveryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-wp.stopCh:
			return
		case <-ticker.C:
			if err := wp.recoverStaleTasks(ctx); err != nil {
				log.Error().Err(err).Msg("Failed to recover stale tasks")
			}
		}
	}
}

// scaleWorkers increases the worker pool size to the target number
func (wp *WorkerPool) scaleWorkers(ctx context.Context, targetWorkers int) {
	if wp.stopping.Load() {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Str("stack", string(debug.Stack())).
				Msg("Recovered from panic in scaleWorkers")
		}
	}()

	wp.workersMutex.Lock()
	defer wp.workersMutex.Unlock()

	if targetWorkers <= wp.currentWorkers {
		return // No need to scale up
	}

	workersToAdd := targetWorkers - wp.currentWorkers

	log.Debug().
		Int("current_workers", wp.currentWorkers).
		Int("adding_workers", workersToAdd).
		Int("target_workers", targetWorkers).
		Msg("Scaling worker pool")

	// Initialise semaphores and wait groups for new workers
	for i := range workersToAdd {
		workerID := wp.currentWorkers + i

		// Extend slices if needed
		if workerID >= len(wp.workerSemaphores) {
			wp.workerSemaphores = append(wp.workerSemaphores, make(chan struct{}, wp.workerConcurrency))
			wp.workerWaitGroups = append(wp.workerWaitGroups, &sync.WaitGroup{})
		}

		wp.wg.Add(1)
		go func(id, idx int) {
			defer wp.wg.Done()
			time.Sleep(time.Duration(idx*50) * time.Millisecond)
			wp.worker(ctx, id)
		}(workerID, i)
	}

	wp.currentWorkers = targetWorkers
}

// markWorkerIdle records that a worker has gone idle
// Returns true if this is a new idle worker (wasn't already idle)
func (wp *WorkerPool) markWorkerIdle(workerID int) bool {
	wp.idleWorkersMutex.Lock()
	defer wp.idleWorkersMutex.Unlock()

	if _, exists := wp.idleWorkers[workerID]; exists {
		return false // Already idle
	}

	wp.idleWorkers[workerID] = time.Now()
	return true
}

// clearWorkerIdle removes a worker from the idle set
func (wp *WorkerPool) clearWorkerIdle(workerID int) {
	wp.idleWorkersMutex.Lock()
	defer wp.idleWorkersMutex.Unlock()

	delete(wp.idleWorkers, workerID)
}

// resetIdleTracking clears all idle worker tracking (called when work resumes)
func (wp *WorkerPool) resetIdleTracking() {
	wp.idleWorkersMutex.Lock()
	defer wp.idleWorkersMutex.Unlock()

	wp.idleWorkers = make(map[int]time.Time)
}

// maybeEmergencyScaleDown immediately scales down when all jobs hit concurrency limits
// Uses 2s cooldown and doesn't require all workers idle
func (wp *WorkerPool) maybeEmergencyScaleDown() {
	// Feature disabled
	if wp.idleThreshold == 0 {
		return
	}

	emergencyCooldown := 2 * time.Second
	if time.Since(wp.lastScaleEval) < emergencyCooldown {
		return
	}
	wp.lastScaleEval = time.Now()

	if time.Since(wp.lastScaleDown) < emergencyCooldown {
		wp.workersMutex.RLock()
		currentWorkers := wp.currentWorkers
		wp.workersMutex.RUnlock()
		wp.logScalingDecision("no_change", "emergency_cooldown_active", currentWorkers, currentWorkers, nil)
		return
	}

	wp.workersMutex.RLock()
	currentWorkers := wp.currentWorkers
	wp.workersMutex.RUnlock()

	// Calculate optimal workers based on active job concurrency
	wp.jobsMutex.RLock()
	wp.jobInfoMutex.RLock()
	totalConcurrency := 0
	for jobID := range wp.jobs {
		jobInfo, exists := wp.jobInfoCache[jobID]
		if !exists {
			continue
		}
		effective := wp.domainLimiter.GetEffectiveConcurrency(jobID, jobInfo.DomainName)
		totalConcurrency += effective
	}
	wp.jobInfoMutex.RUnlock()
	wp.jobsMutex.RUnlock()

	// If no jobs active, don't scale down (let normal scaling handle it)
	if totalConcurrency == 0 {
		wp.logScalingDecision("no_change", "no_active_jobs", currentWorkers, currentWorkers, nil)
		return
	}

	// Calculate optimal workers with 1.2× buffer
	optimalWorkers := min(
		// Ensure at least base worker count
		// Cap at configured max workers
		max(

			int(math.Ceil(float64(totalConcurrency)/float64(wp.workerConcurrency)*1.2)), wp.baseWorkerCount), wp.maxWorkers)

	// Only scale down if we're significantly over-provisioned (more than 20% above optimal)
	threshold := int(float64(optimalWorkers) * 1.2)
	if currentWorkers <= threshold {
		wp.logScalingDecision("no_change", "within_optimal_threshold", currentWorkers, optimalWorkers, map[string]int{
			"total_concurrency": totalConcurrency,
		})
		return
	}

	wp.jobsMutex.RLock()
	for jobID := range wp.jobs {
		wp.recordConcurrencyBlock(jobID)
	}
	wp.jobsMutex.RUnlock()

	wp.logScalingDecision("scale_down", "concurrency_blocked", currentWorkers, optimalWorkers, map[string]int{
		"total_concurrency": totalConcurrency,
	})

	wp.scaleDownWorkers(optimalWorkers)
}

// maybeScaleDown checks if all workers are idle and scales down if appropriate
func (wp *WorkerPool) maybeScaleDown() {
	// Feature disabled
	if wp.idleThreshold == 0 {
		return
	}

	wp.idleWorkersMutex.RLock()
	idleCount := len(wp.idleWorkers)
	wp.idleWorkersMutex.RUnlock()

	wp.workersMutex.RLock()
	currentWorkers := wp.currentWorkers
	wp.workersMutex.RUnlock()

	// Not all workers are idle
	if idleCount < currentWorkers {
		wp.logScalingDecision("no_change", "workers_still_active", currentWorkers, currentWorkers, map[string]int{
			"idle_workers": idleCount,
		})
		return
	}

	// Check cooldown
	if time.Since(wp.lastScaleDown) < wp.scaleCooldown {
		wp.logScalingDecision("no_change", "scale_cooldown_active", currentWorkers, currentWorkers, map[string]int{
			"idle_workers": idleCount,
		})
		return
	}

	// Calculate needed slots based on job concurrency
	wp.jobsMutex.RLock()
	wp.jobInfoMutex.RLock()
	neededSlots := 0
	for jobID := range wp.jobs {
		jobInfo, exists := wp.jobInfoCache[jobID]
		if !exists {
			continue
		}
		effective := wp.domainLimiter.GetEffectiveConcurrency(jobID, jobInfo.DomainName)
		neededSlots += effective
	}
	wp.jobInfoMutex.RUnlock()
	wp.jobsMutex.RUnlock()

	// Calculate target workers with buffer
	target := wp.baseWorkerCount
	if neededSlots > 0 {
		target = max(int(math.Ceil(float64(neededSlots)/float64(wp.workerConcurrency)))+2, wp.baseWorkerCount)
	}

	// Only scale down if target is less than current
	if target >= currentWorkers {
		wp.logScalingDecision("no_change", "at_min_capacity", currentWorkers, target, map[string]int{
			"idle_workers": idleCount,
			"needed_slots": neededSlots,
		})
		return
	}

	wp.logScalingDecision("scale_down", "idle_capacity", currentWorkers, target, map[string]int{
		"idle_workers": idleCount,
		"needed_slots": neededSlots,
	})
	wp.scaleDownWorkers(target)
}

// scaleDownWorkers reduces the worker pool size to the target number
// Workers will exit naturally when they check shouldExit()
func (wp *WorkerPool) scaleDownWorkers(target int) {
	wp.workersMutex.Lock()
	defer wp.workersMutex.Unlock()

	if target >= wp.currentWorkers {
		return // No need to scale down
	}

	oldWorkers := wp.currentWorkers
	wp.currentWorkers = target
	wp.lastScaleDown = time.Now()

	// Remove idle entries for workers being scaled down to prevent stale tracking
	wp.idleWorkersMutex.Lock()
	idleCount := len(wp.idleWorkers)
	for workerID := range wp.idleWorkers {
		if workerID >= target {
			delete(wp.idleWorkers, workerID)
		}
	}
	wp.idleWorkersMutex.Unlock()

	log.Debug().
		Int("old_workers", oldWorkers).
		Int("new_workers", target).
		Int("idle_workers", idleCount).
		Msg("Scaling down worker pool")
}

// returnTaskToPending returns a claimed task back to pending status
// Used by the health probe to check for work without actually processing it
func (wp *WorkerPool) returnTaskToPending(ctx context.Context, task *db.Task) error {
	err := wp.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		// Return task to pending
		_, err := tx.ExecContext(ctx, `
			UPDATE tasks
			SET status = $1, started_at = NULL
			WHERE id = $2
		`, TaskStatusPending, task.ID)
		if err != nil {
			return fmt.Errorf("failed to return task to pending: %w", err)
		}

		// Decrement running tasks counter in DB
		_, err = tx.ExecContext(ctx, `
			UPDATE jobs
			SET running_tasks = running_tasks - 1
			WHERE id = $1 AND running_tasks > 0
		`, task.JobID)
		if err != nil {
			return fmt.Errorf("failed to decrement running tasks: %w", err)
		}

		return nil
	})
	if err != nil {
		return err
	}
	// Mirror decrement in in-memory counter.
	wp.decrementRunningTaskInMem(task.JobID)
	return nil
}

// healthProbe periodically checks for work when all workers are idle
// It claims a task and immediately returns it to pending to detect work availability
func (wp *WorkerPool) healthProbe(ctx context.Context) {
	// Only probe when all workers are idle
	wp.idleWorkersMutex.RLock()
	idleCount := len(wp.idleWorkers)
	wp.idleWorkersMutex.RUnlock()

	wp.workersMutex.RLock()
	currentWorkers := wp.currentWorkers
	wp.workersMutex.RUnlock()

	if idleCount < currentWorkers {
		// Not all workers are idle, skip probe
		return
	}

	log.Debug().Msg("Health probe: All workers idle, checking for work")

	// Get list of active job IDs
	wp.jobsMutex.RLock()
	jobIDs := make([]string, 0, len(wp.jobs))
	for jobID := range wp.jobs {
		jobIDs = append(jobIDs, jobID)
	}
	wp.jobsMutex.RUnlock()

	if len(jobIDs) == 0 {
		log.Debug().Msg("Health probe: No active jobs")
		return
	}

	// Try to claim any available task
	task, err := wp.claimPendingTask(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Debug().Msg("Health probe: No work found")
			return
		}
		if errors.Is(err, db.ErrConcurrencyBlocked) {
			log.Debug().Msg("Health probe: Work exists but blocked by concurrency limits")
			return
		}
		log.Error().Err(err).Msg("Health probe: Error claiming task")
		return
	}

	// Found work! Return task to pending and wake workers
	if err := wp.returnTaskToPending(ctx, task); err != nil {
		log.Error().Err(err).Str("job_id", task.JobID).Str("task_id", task.ID).
			Msg("Health probe: Failed to return task to pending")
		// Task is claimed but we couldn't return it - let it be processed normally
	}

	log.Debug().Str("job_id", task.JobID).Msg("Health probe: Found work, waking workers")

	// Wake workers (non-blocking send)
	select {
	case wp.notifyCh <- struct{}{}:
	default:
	}

	// Reset idle tracking since work was found
	wp.resetIdleTracking()
}

// StartCleanupMonitor starts the cleanup monitor goroutine
func (wp *WorkerPool) StartCleanupMonitor(ctx context.Context) {
	wp.wg.Go(func() {
		ticker := time.NewTicker(wp.cleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-wp.stopCh:
				return
			case <-ticker.C:
				if err := wp.CleanupStuckJobs(ctx); err != nil {
					log.Error().Err(err).Msg("Failed to cleanup stuck jobs")
				}
			}
		}
	})
	log.Info().Msg("Job cleanup monitor started")
}

// StartWaitingTaskRecoveryMonitor continuously recovers jobs whose waiting
// queues have stalled despite free capacity being available.
func (wp *WorkerPool) StartWaitingTaskRecoveryMonitor(ctx context.Context) {
	wp.wg.Go(func() {
		ticker := time.NewTicker(wp.waitingRecoveryInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-wp.stopCh:
				return
			case <-ticker.C:
				if err := wp.recoverWaitingTasks(ctx); err != nil {
					log.Error().Err(err).Msg("Failed to recover waiting tasks")
				}
			}
		}
	})
	log.Info().Dur("interval", wp.waitingRecoveryInterval).Msg("Waiting task recovery monitor started")
}

// StartQuotaPromotionMonitor is retained as a compatibility shim for existing
// callers; waiting-task recovery now handles both quota-aware and generic
// promotion paths.
func (wp *WorkerPool) StartQuotaPromotionMonitor(ctx context.Context) {
	wp.StartWaitingTaskRecoveryMonitor(ctx)
}

func (wp *WorkerPool) recoverWaitingTasks(ctx context.Context) error {
	type jobInfo struct {
		ID             string
		Status         string
		QuotaRemaining sql.NullInt64
		Concurrency    int
		RunningTasks   int
		PendingTasks   int
	}
	var jobs []jobInfo

	err := wp.dbQueue.ExecuteControl(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT
				j.id,
				j.status,
				CASE
					WHEN j.organisation_id IS NOT NULL THEN get_daily_quota_remaining(j.organisation_id)
					ELSE NULL
				END AS quota_remaining,
				COALESCE(j.concurrency, 0) AS concurrency,
				j.running_tasks,
				j.pending_tasks
			FROM jobs j
			WHERE j.waiting_tasks > 0
			  AND j.status IN ('running', 'pending')
		`)
		if err != nil {
			return fmt.Errorf("failed to find jobs with waiting work: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var j jobInfo
			if err := rows.Scan(&j.ID, &j.Status, &j.QuotaRemaining, &j.Concurrency, &j.RunningTasks, &j.PendingTasks); err != nil {
				return fmt.Errorf("failed to scan waiting job: %w", err)
			}
			jobs = append(jobs, j)
		}

		return rows.Err()
	})
	if err != nil {
		return err
	}

	totalPromoted := 0
	for _, job := range jobs {
		slots := int(^uint(0) >> 1)
		if job.QuotaRemaining.Valid {
			slots = int(job.QuotaRemaining.Int64)
		}
		if job.Concurrency > 0 {
			if available := job.Concurrency - job.RunningTasks - job.PendingTasks; available < slots {
				slots = available
			}
		} else {
			if capAvail := pendingUnlimitedCap - (job.PendingTasks + job.RunningTasks); capAvail < slots {
				slots = capAvail
			}
		}
		if slots <= 0 {
			continue
		}

		if job.Status == "pending" {
			err := wp.dbQueue.ExecuteControl(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `
					UPDATE jobs SET
						status = $1,
						started_at = CASE WHEN started_at IS NULL THEN $2 ELSE started_at END
					WHERE id = $3 AND status = 'pending'
				`, JobStatusRunning, time.Now().UTC(), job.ID)
				return err
			})
			if err != nil {
				log.Warn().Err(err).Str("job_id", job.ID).Msg("Failed to transition pending waiting job to running")
				continue
			}
			wp.AddJob(job.ID, nil)
		}

		var promoted int
		err := wp.dbQueue.ExecuteControl(ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT promote_waiting_tasks_for_job($1, $2)`, job.ID, slots).Scan(&promoted)
		})
		if err != nil {
			log.Warn().Err(err).Str("job_id", job.ID).Msg("Failed to recover waiting tasks for job")
			continue
		}
		if promoted > 0 {
			totalPromoted += promoted
			log.Debug().Str("job_id", job.ID).Int("promoted", promoted).Int("slots", slots).Msg("Recovered waiting tasks for job")
		}
	}

	if totalPromoted > 0 {
		log.Info().Int("total_promoted", totalPromoted).Int("jobs", len(jobs)).Msg("Recovered waiting tasks")
		wp.NotifyNewTasks()
	}

	return nil
}

// CleanupStuckJobs finds and fixes jobs that are stuck in pending/running state
// despite having all their tasks completed
func (wp *WorkerPool) CleanupStuckJobs(ctx context.Context) error {
	span := sentry.StartSpan(ctx, "jobs.cleanup_stuck_jobs")
	defer span.Finish()

	var completedJobs, timedOutJobs int64

	err := wp.dbQueue.ExecuteMaintenance(ctx, func(tx *sql.Tx) error {
		// 1. Mark jobs as completed when all tasks are done
		result, err := tx.ExecContext(ctx, `
				UPDATE jobs
				SET status = $1,
					completed_at = COALESCE(completed_at, $2),
				progress = 100.0
			WHERE (status = $3 OR status = $4)
			AND total_tasks > 0
			AND total_tasks = completed_tasks + failed_tasks + skipped_tasks
		`, JobStatusCompleted, time.Now().UTC(), JobStatusPending, JobStatusRunning)

		if err != nil {
			return err
		}

		completedJobs, err = result.RowsAffected()
		if err != nil {
			return err
		}

		// 2. Mark jobs as failed when stuck for too long
		// - Pending jobs with 0 tasks for 5 minutes (sitemap processing likely failed)
		// - Running jobs with no task progress for 30 minutes (excluding jobs with waiting tasks)
		// - Jobs running for all tasks failed
		result, err = tx.ExecContext(ctx, `
			UPDATE jobs
			SET status = $1,
				completed_at = $2,
				error_message = CASE
					WHEN status = $3 AND total_tasks = 0 THEN 'Job timed out: no tasks created after 5 minutes (sitemap processing may have failed)'
					WHEN total_tasks > 0 AND total_tasks = failed_tasks THEN 'Job failed: all tasks failed'
					ELSE 'Job timed out: no task progress for 30 minutes'
				END
			WHERE (
				-- Pending jobs with no tasks for 5+ minutes
				(status = $3 AND total_tasks = 0 AND created_at < $4)
				OR
				-- Running jobs where all tasks failed
				(status = $5 AND total_tasks > 0 AND total_tasks = failed_tasks)
				OR
				-- Running jobs with no task updates for 30+ minutes
				-- Exclude jobs with waiting tasks ONLY if org is over quota (legitimate wait)
				-- If quota available but tasks still waiting, something is stuck - should timeout
				(status = $5 AND total_tasks > 0
					AND NOT (
						EXISTS (SELECT 1 FROM tasks WHERE job_id = jobs.id AND status = 'waiting')
						AND organisation_id IS NOT NULL
						AND get_daily_quota_remaining(organisation_id) <= 0
					)
					AND COALESCE((
						SELECT MAX(GREATEST(started_at, completed_at))
						FROM tasks
						WHERE job_id = jobs.id
					), created_at) < $6)
			)
		`, JobStatusFailed, time.Now().UTC(), JobStatusPending, time.Now().UTC().Add(-5*time.Minute), JobStatusRunning, time.Now().UTC().Add(-30*time.Minute))

		if err != nil {
			return err
		}

		timedOutJobs, err = result.RowsAffected()
		if err != nil {
			return err
		}

		// Orphaned task cleanup now runs in separate goroutine (cleanupOrphanedTasksLoop)
		// to avoid 60s transaction timeout constraint

		return nil
	})

	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		return fmt.Errorf("failed to cleanup stuck jobs: %w", err)
	}

	if completedJobs > 0 {
		log.Info().
			Int64("jobs_completed", completedJobs).
			Msg("Marked stuck jobs as completed")
	}

	if timedOutJobs > 0 {
		log.Warn().
			Int64("jobs_failed", timedOutJobs).
			Msg("Marked timed-out jobs as failed")
	}

	return nil
}

// cleanupOrphanedTasksLoop runs as a separate goroutine to clean up orphaned tasks
// from failed jobs. Runs outside the 60s maintenance transaction to avoid timeout
// with large task counts.
func (wp *WorkerPool) cleanupOrphanedTasksLoop(ctx context.Context) {
	// Run every 30 seconds (more frequent than maintenance cycle)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	log.Info().Msg("Orphaned task cleanup loop started")

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("Orphaned task cleanup loop stopped (context cancelled)")
			return
		case <-wp.stopCh:
			log.Info().Msg("Orphaned task cleanup loop stopped (worker pool stopped)")
			return
		case <-ticker.C:
			if err := wp.cleanupOrphanedTasks(ctx); err != nil {
				// Log but continue - this is a background cleanup process
				log.Error().Err(err).Msg("Failed to cleanup orphaned tasks")
			}
		}
	}
}

// cleanupOrphanedTasks processes one failed job with orphaned tasks
// Uses a separate transaction with no timeout constraint
func (wp *WorkerPool) cleanupOrphanedTasks(ctx context.Context) error {
	span := sentry.StartSpan(ctx, "jobs.cleanup_orphaned_tasks")
	defer span.Finish()

	// Begin a new transaction (not the maintenance transaction)
	tx, err := wp.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Select one failed job with orphaned tasks
	// Uses EXISTS subquery to avoid DISTINCT (incompatible with FOR UPDATE)
	// Orders by created_at for deterministic oldest-first processing
	var targetJobID string
	var jobErrorMsg sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT j.id, j.error_message
		FROM jobs j
		WHERE j.status = $1
			AND EXISTS (
				SELECT 1 FROM tasks t
				WHERE t.job_id = j.id
					AND t.status IN ($2, $3)
			)
		ORDER BY j.created_at ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`, JobStatusFailed, TaskStatusPending, TaskStatusWaiting).Scan(&targetJobID, &jobErrorMsg)

	if err == sql.ErrNoRows {
		// No failed jobs with orphaned tasks - nothing to do
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to select failed job with orphaned tasks: %w", err)
	}

	// Determine error message to use for orphaned tasks
	errorMsg := "Job failed"
	if jobErrorMsg.Valid && jobErrorMsg.String != "" {
		errorMsg = jobErrorMsg.String
	}

	cleanupStart := time.Now()

	// Release the selection transaction before batched updates
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to release job selection lock: %w", err)
	}

	const batchSize = 1000
	const maxIterations = 1000
	now := time.Now().UTC()
	totalCleaned := int64(0)
	iterations := 0

	for {
		if ctx.Err() != nil {
			span.SetData("tasks_cleaned", totalCleaned)
			span.SetData("partial_cleanup", true)
			return fmt.Errorf("cleanup cancelled after processing %d tasks: %w", totalCleaned, ctx.Err())
		}

		iterations++
		if iterations > maxIterations {
			log.Warn().
				Str("job_id", targetJobID).
				Int64("tasks_cleaned", totalCleaned).
				Int("iterations", iterations).
				Msg("Orphaned task cleanup stopped: maximum iteration limit reached")
			span.SetData("tasks_cleaned", totalCleaned)
			span.SetData("iteration_limit_reached", true)
			break
		}

		batchTx, err := wp.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin batch transaction: %w", err)
		}

		result, err := batchTx.ExecContext(ctx, `
			WITH batch AS (
				SELECT id
				FROM tasks
				WHERE job_id = $1 AND status IN ($2, $3)
				ORDER BY id
				LIMIT $4
			)
			UPDATE tasks
			SET status = $5,
				error = $6,
				completed_at = $7
			WHERE id IN (SELECT id FROM batch)
		`, targetJobID, TaskStatusPending, TaskStatusWaiting, batchSize, TaskStatusFailed, errorMsg, now)
		if err != nil {
			_ = batchTx.Rollback()
			return fmt.Errorf("failed to update orphaned tasks for job %s: %w", targetJobID, err)
		}
		cleaned, err := result.RowsAffected()
		if err != nil {
			_ = batchTx.Rollback()
			return fmt.Errorf("failed to get affected rows: %w", err)
		}
		if err := batchTx.Commit(); err != nil {
			return fmt.Errorf("failed to commit batch transaction: %w", err)
		}

		totalCleaned += cleaned
		if cleaned < batchSize {
			break
		}
		// Brief pause between batches to avoid a write burst spiking the DB EMA.
		time.Sleep(100 * time.Millisecond)
	}

	cleanupDuration := time.Since(cleanupStart)

	log.Info().
		Str("job_id", targetJobID).
		Int64("tasks_cleaned", totalCleaned).
		Dur("duration_ms", cleanupDuration).
		Msg("Orphaned task cleanup completed for job")

	span.SetData("job_id", targetJobID)
	span.SetData("tasks_cleaned", totalCleaned)
	span.SetData("duration_ms", cleanupDuration.Milliseconds())

	return nil
}

// startRunningTaskReleaseLoop batches running_tasks decrements to reduce contention.
func (wp *WorkerPool) startRunningTaskReleaseLoop(ctx context.Context) {
	if wp.runningTaskReleaseCh == nil {
		return
	}

	wp.wg.Go(func() {

		interval := wp.runningTaskReleaseFlushInterval
		if interval <= 0 {
			interval = defaultRunningTaskFlush
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				wp.flushRunningTaskReleases(context.Background())
				return
			case <-wp.stopCh:
				wp.flushRunningTaskReleases(context.Background())
				return
			case jobID := <-wp.runningTaskReleaseCh:
				if jobID == "" {
					continue
				}
				count := wp.incrementPendingRunningTaskRelease(jobID)
				if count >= wp.runningTaskReleaseBatchSize {
					wp.flushRunningTaskReleaseForJob(ctx, jobID)
				}
			case <-ticker.C:
				wp.flushRunningTaskReleases(ctx)
			}
		}
	})
}

func (wp *WorkerPool) incrementPendingRunningTaskRelease(jobID string) int {
	wp.runningTaskReleaseMu.Lock()
	defer wp.runningTaskReleaseMu.Unlock()
	wp.runningTaskReleasePending[jobID]++
	return wp.runningTaskReleasePending[jobID]
}

func (wp *WorkerPool) flushRunningTaskReleaseForJob(ctx context.Context, jobID string) {
	wp.runningTaskReleaseMu.Lock()
	count := wp.runningTaskReleasePending[jobID]
	if count > 0 {
		delete(wp.runningTaskReleasePending, jobID)
	}
	wp.runningTaskReleaseMu.Unlock()

	if count > 0 {
		wp.flushRunningTaskReleaseCount(ctx, jobID, count)
	}
}

func (wp *WorkerPool) flushRunningTaskReleases(ctx context.Context) {
	wp.runningTaskReleaseMu.Lock()
	if len(wp.runningTaskReleasePending) == 0 {
		wp.runningTaskReleaseMu.Unlock()
		return
	}
	pending := make(map[string]int, len(wp.runningTaskReleasePending))
	maps.Copy(pending, wp.runningTaskReleasePending)
	wp.runningTaskReleasePending = make(map[string]int)
	wp.runningTaskReleaseMu.Unlock()

	for jobID, count := range pending {
		wp.flushRunningTaskReleaseCount(ctx, jobID, count)
	}
}

func (wp *WorkerPool) flushRunningTaskReleaseCount(ctx context.Context, jobID string, count int) {
	if count <= 0 {
		return
	}

	baseCtx := ctx
	if baseCtx == nil || baseCtx.Err() != nil {
		baseCtx = context.Background()
	}

	flushCtx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
	defer cancel()

	if err := wp.dbQueue.DecrementRunningTasksBy(flushCtx, jobID, count); err != nil {
		log.Error().
			Err(err).
			Str("job_id", jobID).
			Int("release_count", count).
			Msg("Failed to release running task slots")

		// Requeue for retry on next tick
		wp.runningTaskReleaseMu.Lock()
		wp.runningTaskReleasePending[jobID] += count
		wp.runningTaskReleaseMu.Unlock()
	}
}

// --- In-memory running_tasks increment helpers ---

// incrementRunningTaskInMem bumps the in-memory counter and queues a DB flush.
func (wp *WorkerPool) incrementRunningTaskInMem(jobID string) {
	// Guard: maps may be nil in tests that construct WorkerPool directly.
	if wp.runningTasksInMem == nil {
		return
	}

	wp.runningTasksInMemMu.RLock()
	counter, exists := wp.runningTasksInMem[jobID]
	wp.runningTasksInMemMu.RUnlock()

	if !exists {
		wp.runningTasksInMemMu.Lock()
		counter, exists = wp.runningTasksInMem[jobID]
		if !exists {
			counter = &atomic.Int64{}
			wp.runningTasksInMem[jobID] = counter
		}
		wp.runningTasksInMemMu.Unlock()
	}
	counter.Add(1)

	if wp.runningTasksIncrPending == nil {
		return
	}

	wp.runningTasksIncrMu.Lock()
	wp.runningTasksIncrPending[jobID]++
	count := wp.runningTasksIncrPending[jobID]
	wp.runningTasksIncrMu.Unlock()

	// Trigger an early flush if we've accumulated a batch.
	if count >= wp.runningTaskReleaseBatchSize && wp.runningTasksIncrCh != nil {
		select {
		case wp.runningTasksIncrCh <- jobID:
		default:
		}
	}
}

// queueRunningTaskIncrDB queues a DB flush for a job whose in-memory counter was
// already bumped atomically by the concurrency gate. Only queues the DB side.
func (wp *WorkerPool) queueRunningTaskIncrDB(jobID string) {
	if wp.runningTasksIncrPending == nil {
		return
	}

	wp.runningTasksIncrMu.Lock()
	wp.runningTasksIncrPending[jobID]++
	count := wp.runningTasksIncrPending[jobID]
	wp.runningTasksIncrMu.Unlock()

	if count >= wp.runningTaskReleaseBatchSize && wp.runningTasksIncrCh != nil {
		select {
		case wp.runningTasksIncrCh <- jobID:
		default:
		}
	}
}

// decrementRunningTaskInMem decrements the in-memory counter by 1.
func (wp *WorkerPool) decrementRunningTaskInMem(jobID string) {
	wp.runningTasksInMemMu.RLock()
	counter, exists := wp.runningTasksInMem[jobID]
	wp.runningTasksInMemMu.RUnlock()
	if exists {
		counter.Add(-1)
	}
}

// decrementRunningTaskInMemBy decrements the in-memory counter by count.
func (wp *WorkerPool) decrementRunningTaskInMemBy(jobID string, count int) {
	if count <= 0 {
		return
	}
	wp.runningTasksInMemMu.RLock()
	counter, exists := wp.runningTasksInMem[jobID]
	wp.runningTasksInMemMu.RUnlock()
	if exists {
		counter.Add(-int64(count))
	}
}

// flushRunningTaskIncrements flushes all pending increments to the DB.
func (wp *WorkerPool) flushRunningTaskIncrements(ctx context.Context) {
	wp.runningTasksIncrMu.Lock()
	if len(wp.runningTasksIncrPending) == 0 {
		wp.runningTasksIncrMu.Unlock()
		return
	}
	snapshot := make(map[string]int, len(wp.runningTasksIncrPending))
	for jobID, count := range wp.runningTasksIncrPending {
		snapshot[jobID] = count
	}
	wp.runningTasksIncrPending = make(map[string]int)
	wp.runningTasksIncrMu.Unlock()

	for jobID, count := range snapshot {
		if err := wp.dbQueue.IncrementRunningTasksBy(ctx, jobID, count); err != nil {
			log.Warn().Err(err).Str("job_id", jobID).Int("count", count).
				Msg("Failed to flush running task increments, re-queuing")
			wp.runningTasksIncrMu.Lock()
			wp.runningTasksIncrPending[jobID] += count
			wp.runningTasksIncrMu.Unlock()
		}
	}
}

// flushRunningTaskIncrementForJob flushes pending increments for a single job.
func (wp *WorkerPool) flushRunningTaskIncrementForJob(ctx context.Context, jobID string) {
	wp.runningTasksIncrMu.Lock()
	count := wp.runningTasksIncrPending[jobID]
	if count == 0 {
		wp.runningTasksIncrMu.Unlock()
		return
	}
	delete(wp.runningTasksIncrPending, jobID)
	wp.runningTasksIncrMu.Unlock()

	if err := wp.dbQueue.IncrementRunningTasksBy(ctx, jobID, count); err != nil {
		log.Warn().Err(err).Str("job_id", jobID).Int("count", count).
			Msg("Failed to flush running task increment for job, re-queuing")
		wp.runningTasksIncrMu.Lock()
		wp.runningTasksIncrPending[jobID] += count
		wp.runningTasksIncrMu.Unlock()
	}
}

// startRunningTaskIncrementLoop mirrors startRunningTaskReleaseLoop but for increments.
func (wp *WorkerPool) startRunningTaskIncrementLoop(ctx context.Context) {
	wp.wg.Go(func() {
		interval := wp.runningTaskReleaseFlushInterval
		if interval <= 0 {
			interval = defaultRunningTaskFlush
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				wp.flushRunningTaskIncrements(context.Background())
				return
			case <-wp.stopCh:
				wp.flushRunningTaskIncrements(context.Background())
				return
			case jobID := <-wp.runningTasksIncrCh:
				if jobID != "" {
					wp.flushRunningTaskIncrementForJob(ctx, jobID)
				}
			case <-ticker.C:
				wp.flushRunningTaskIncrements(ctx)
			}
		}
	})
}

// seedRunningTasksInMemFromDB loads current running_tasks values for active jobs into memory.
func (wp *WorkerPool) seedRunningTasksInMemFromDB(ctx context.Context) error {
	rows, err := wp.db.QueryContext(ctx, `
		SELECT id, running_tasks
		FROM jobs
		WHERE status IN ('running', 'pending')
	`)
	if err != nil {
		return fmt.Errorf("query running_tasks for seeding: %w", err)
	}
	defer rows.Close()

	wp.runningTasksInMemMu.Lock()
	defer wp.runningTasksInMemMu.Unlock()

	for rows.Next() {
		var jobID string
		var runningTasks int64
		if err := rows.Scan(&jobID, &runningTasks); err != nil {
			log.Warn().Err(err).Msg("Failed to scan running_tasks row during seeding")
			continue
		}
		counter, exists := wp.runningTasksInMem[jobID]
		if !exists {
			counter = &atomic.Int64{}
			wp.runningTasksInMem[jobID] = counter
		}
		counter.Store(runningTasks)
	}
	return rows.Err()
}

func (wp *WorkerPool) releaseRunningTaskSlot(jobID string) error {
	if jobID == "" {
		return fmt.Errorf("jobID cannot be empty")
	}

	// Flush any pending increment for this job before queuing the decrement.
	// Without this, a decrement can reach the DB before its matching increment,
	// causing the WHERE running_tasks > 0 guard to skip the decrement entirely —
	// leaving running_tasks permanently inflated.
	if wp.runningTasksIncrCh != nil {
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
		wp.flushRunningTaskIncrementForJob(flushCtx, jobID)
		flushCancel()
	}

	select {
	case wp.runningTaskReleaseCh <- jobID:
		return nil
	default:
		log.Debug().Str("job_id", jobID).Msg("Running task release channel full, forcing flush")
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
		wp.flushRunningTaskReleaseForJob(flushCtx, jobID)
		flushCancel()

		select {
		case wp.runningTaskReleaseCh <- jobID:
			return nil
		default:
			log.Warn().Str("job_id", jobID).Msg("Falling back to direct running_tasks decrement")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := wp.dbQueue.DecrementRunningTasksBy(ctx, jobID, 1); err != nil {
				wp.runningTaskReleaseMu.Lock()
				wp.runningTaskReleasePending[jobID]++
				wp.runningTaskReleaseMu.Unlock()
				return err
			}
			return nil
		}
	}
}

// processTask processes an individual task
// constructTaskURL builds a proper URL from task path and domain information
func constructTaskURL(path, host, domainName string) string {
	// Check if path is already a full URL
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return util.NormaliseURL(path)
	} else if host != "" {
		return util.ConstructURL(host, path)
	} else if domainName != "" {
		// Use centralized URL construction
		return util.ConstructURL(domainName, path)
	} else {
		// Fallback case - assume path is a full URL but missing protocol
		return util.NormaliseURL(path)
	}
}

func cloneDiscoveredLinks(links map[string][]string) map[string][]string {
	if len(links) == 0 {
		return nil
	}
	cloned := make(map[string][]string, len(links))
	for category, values := range links {
		cloned[category] = append([]string(nil), values...)
	}
	return cloned
}

func (wp *WorkerPool) enqueueDiscoveredLinks(task *db.Task, result *crawler.CrawlResult) {
	if wp.discoveredLinkPersistCh == nil || task == nil || result == nil || len(result.Links) == 0 {
		return
	}

	wp.jobInfoMutex.RLock()
	jobInfo, exists := wp.jobInfoCache[task.JobID]
	wp.jobInfoMutex.RUnlock()
	if !exists {
		log.Debug().Str("job_id", task.JobID).Str("task_id", task.ID).Msg("Skipping async link expansion: job info not cached")
		return
	}
	if !jobInfo.FindLinks {
		return
	}

	request := &discoveredLinkPersistRequest{
		Task: Task{
			ID:                       task.ID,
			JobID:                    task.JobID,
			PageID:                   task.PageID,
			Host:                     task.Host,
			Path:                     task.Path,
			PriorityScore:            task.PriorityScore,
			DomainID:                 jobInfo.DomainID,
			DomainName:               jobInfo.DomainName,
			FindLinks:                jobInfo.FindLinks,
			AllowCrossSubdomainLinks: jobInfo.AllowCrossSubdomainLinks,
		},
		Links:     cloneDiscoveredLinks(result.Links),
		SourceURL: constructTaskURL(task.Path, task.Host, jobInfo.DomainName),
	}

	wp.discoveredLinkPending.Add(1)
	select {
	case wp.discoveredLinkPersistCh <- request:
	default:
		wp.discoveredLinkPending.Add(-1)
		log.Warn().
			Str("task_id", task.ID).
			Str("job_id", task.JobID).
			Int("queue_cap", cap(wp.discoveredLinkPersistCh)).
			Msg("Discovered link queue full, skipping async expansion")
	}
}

func (wp *WorkerPool) startDiscoveredLinkPersistenceLoop(ctx context.Context) {
	if wp.discoveredLinkPersistCh == nil {
		return
	}

	log.Info().Int("workers", discoveredLinkWorkers).Int("queue_cap", cap(wp.discoveredLinkPersistCh)).Msg("Starting discovered link workers")
	for range discoveredLinkWorkers {
		wp.wg.Go(func() {
			for {
				select {
				case request := <-wp.discoveredLinkPersistCh:
					if request != nil {
						wp.processDiscoveredLinks(context.Background(), &request.Task, request.Links, request.SourceURL)
						wp.discoveredLinkPending.Add(-1)
					}
				case <-wp.stopCh:
					return
				case <-ctx.Done():
					return
				}
			}
		})
	}
}

// applyCrawlDelay applies robots.txt crawl delay if specified for the task's domain
func applyCrawlDelay(task *Task) {
	if task.CrawlDelay > 0 {
		log.Debug().
			Str("task_id", task.ID).
			Str("domain", task.DomainName).
			Int("crawl_delay_seconds", task.CrawlDelay).
			Msg("Applying crawl delay from robots.txt")
		time.Sleep(time.Duration(task.CrawlDelay) * time.Second)
	}
}

// processDiscoveredLinks handles link processing and enqueueing for discovered URLs.
func (wp *WorkerPool) processDiscoveredLinks(ctx context.Context, task *Task, links map[string][]string, sourceURL string) {
	log.Debug().
		Str("task_id", task.ID).
		Int("total_links_found", len(links["header"])+len(links["footer"])+len(links["body"])).
		Bool("find_links_enabled", task.FindLinks).
		Msg("Starting link processing and priority assignment")

	// Use domain ID from task (already populated from job cache)
	domainID := task.DomainID
	if domainID == 0 {
		log.Error().
			Str("task_id", task.ID).
			Str("job_id", task.JobID).
			Msg("Missing domain ID; skipping link processing")
		return
	}

	// Get robots rules from cache for URL filtering
	var robotsRules *crawler.RobotsRules
	wp.jobInfoMutex.RLock()
	if jobInfo, exists := wp.jobInfoCache[task.JobID]; exists {
		robotsRules = jobInfo.RobotsRules
	}
	wp.jobInfoMutex.RUnlock()

	isHomepage := task.Path == "/"

	processLinkCategory := func(links []string, priority float64) {
		if len(links) == 0 {
			return
		}
		if priority < wp.linkDiscoveryMinPriority {
			log.Debug().
				Str("task_id", task.ID).
				Float64("priority", priority).
				Float64("min_priority", wp.linkDiscoveryMinPriority).
				Msg("Skipping discovered link persistence below priority threshold")
			return
		}

		baseURL, baseErr := url.Parse(sourceURL)

		if err := ctx.Err(); err != nil {
			log.Debug().
				Err(err).
				Str("job_id", task.JobID).
				Str("domain", task.DomainName).
				Str("task_id", task.ID).
				Msg("Skipping discovered link processing: parent task context is done")
			return
		}

		// 1. Filter links for same-site and robots.txt compliance
		var filtered []string
		var blockedCount int
		for _, link := range links {
			linkURL, err := url.Parse(link)
			if err != nil {
				continue
			}

			if !linkURL.IsAbs() {
				if baseErr != nil || baseURL == nil {
					continue
				}
				linkURL = baseURL.ResolveReference(linkURL)
			}

			if isLinkAllowedForTask(linkURL.Hostname(), task) {
				linkURL.Fragment = ""
				if linkURL.Path != "/" && strings.HasSuffix(linkURL.Path, "/") {
					linkURL.Path = strings.TrimSuffix(linkURL.Path, "/")
				}

				// Check robots.txt rules
				if robotsRules != nil && !crawler.IsPathAllowed(robotsRules, linkURL.Path) {
					blockedCount++
					log.Debug().
						Str("url", linkURL.String()).
						Str("path", linkURL.Path).
						Str("source", sourceURL).
						Msg("Link blocked by robots.txt")
					continue
				}

				filtered = append(filtered, linkURL.String())
			}
		}

		if blockedCount > 0 {
			log.Debug().
				Str("task_id", task.ID).
				Int("blocked_count", blockedCount).
				Int("allowed_count", len(filtered)).
				Msg("Filtered discovered links against robots.txt")
		}

		if len(filtered) == 0 {
			return
		}

		linkCtxTimeout := discoveredLinksDBTimeout
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= discoveredLinksMinRemain {
				log.Warn().
					Str("job_id", task.JobID).
					Str("domain", task.DomainName).
					Str("task_id", task.ID).
					Dur("remaining", remaining).
					Msg("Skipping discovered link persistence: task deadline too close")
				return
			}

			maxTimeout := remaining - discoveredLinksMinRemain
			if maxTimeout < linkCtxTimeout {
				linkCtxTimeout = maxTimeout
			}
		}
		if linkCtxTimeout < discoveredLinksMinTimeout {
			log.Warn().
				Str("job_id", task.JobID).
				Str("domain", task.DomainName).
				Str("task_id", task.ID).
				Dur("timeout", linkCtxTimeout).
				Msg("Skipping discovered link persistence: insufficient timeout budget")
			return
		}
		// Keep request-scoped values while detaching from parent cancellation/deadline.
		linkCtx, linkCancel := context.WithTimeout(context.WithoutCancel(ctx), linkCtxTimeout)
		defer linkCancel()

		// 2. Create page records
		pageIDs, hosts, paths, err := db.CreatePageRecords(linkCtx, wp.dbQueue, domainID, task.DomainName, filtered)
		if err != nil {
			log.Error().Err(err).Msg("Failed to create page records for links")
			return
		}

		// 3. Create a slice of db.Page for enqueuing
		pagesToEnqueue := make([]db.Page, len(pageIDs))
		for i := range pageIDs {
			pagesToEnqueue[i] = db.Page{
				ID:   pageIDs[i],
				Host: hosts[i],
				Path: paths[i],
				// Priority will be set by the caller of processLinkCategory
			}
		}

		// 4. Enqueue new tasks
		if err := wp.EnqueueURLs(linkCtx, task.JobID, pagesToEnqueue, "link", sourceURL); err != nil {
			log.Error().Err(err).Msg("Failed to enqueue discovered links")
			return // Stop if enqueuing fails
		}

		// 5. Update priorities for the newly created tasks
		if err := wp.updateTaskPriorities(linkCtx, task.JobID, domainID, priority, paths); err != nil {
			log.Error().Err(err).Msg("Failed to update task priorities for discovered links")
		}
	}

	// Apply priorities based on page type and link category
	if isHomepage {
		log.Debug().Str("task_id", task.ID).Msg("Processing links from HOMEPAGE")
		processLinkCategory(links["header"], 1.000)
		processLinkCategory(links["footer"], 0.990)
		processLinkCategory(links["body"], task.PriorityScore*0.9) // Children of homepage
	} else {
		log.Debug().Str("task_id", task.ID).Msg("Processing links from regular page")
		// For all other pages, only process body links
		processLinkCategory(links["body"], task.PriorityScore*0.9) // Children of other pages
	}
}

// handleTaskError processes task failures with appropriate retry logic and status updates
func (wp *WorkerPool) handleTaskError(ctx context.Context, task *db.Task, result *crawler.CrawlResult, taskErr error) error {
	now := time.Now().UTC()
	retryReason := "non_retryable"
	wp.populateRequestDiagnostics(task, result)

	// Check if this is a blocking error (403/429/503)
	if isBlockingError(taskErr) {
		maxRetries := wp.domainLimiter.cfg.MaxBlockingRetries
		if task.RetryCount < maxRetries {
			retryReason = "blocking"
			task.RetryCount++
			task.Error = taskErr.Error()
			// Route retries through waiting to respect pending queue cap
			task.Status = string(TaskStatusWaiting)
			task.StartedAt = time.Time{} // Reset started time
			wp.recordWaitingTask(ctx, task, waitingReasonBlockingRetry)
			log.Debug().
				Err(taskErr).
				Str("task_id", task.ID).
				Int("retry_count", task.RetryCount).
				Int("max_retries", maxRetries).
				Msg("Blocking error (403/429/503), retry scheduled via waiting status")
			observability.RecordWorkerTaskRetry(ctx, task.JobID, retryReason)
		} else {
			// Mark as permanently failed after 2 retries
			task.Status = string(TaskStatusFailed)
			task.CompletedAt = now
			task.Error = taskErr.Error()
			log.Debug().
				Err(taskErr).
				Str("task_id", task.ID).
				Int("retry_count", task.RetryCount).
				Msg("Task blocked permanently after exhausting retries")
			wp.recordJobFailure(ctx, task.JobID, task.ID, taskErr)
			observability.RecordWorkerTaskFailure(ctx, task.JobID, "blocking")
		}
	} else if isRetryableError(taskErr) && task.RetryCount < MaxTaskRetries {
		// For other retryable errors, use normal retry limit
		retryReason = "retryable"
		task.RetryCount++
		task.Error = taskErr.Error()
		// Route retries through waiting to respect pending queue cap
		task.Status = string(TaskStatusWaiting)
		task.StartedAt = time.Time{} // Reset started time
		wp.recordWaitingTask(ctx, task, waitingReasonRetryableError)
		log.Debug().
			Err(taskErr).
			Str("task_id", task.ID).
			Int("retry_count", task.RetryCount).
			Msg("Task failed with retryable error, scheduling retry via waiting status")
		observability.RecordWorkerTaskRetry(ctx, task.JobID, retryReason)
	} else {
		// Mark as permanently failed
		task.Status = string(TaskStatusFailed)
		task.CompletedAt = now
		task.Error = taskErr.Error()
		logger := log.Error()
		if isClientOrRedirectError(taskErr) {
			logger = log.Debug()
		}
		logger.
			Err(taskErr).
			Str("task_id", task.ID).
			Int("retry_count", task.RetryCount).
			Msg("Task failed permanently")
		wp.recordJobFailure(ctx, task.JobID, task.ID, taskErr)
		failureReason := retryReason
		if !isBlockingError(taskErr) && !isRetryableError(taskErr) {
			failureReason = "non_retryable"
		} else if isRetryableError(taskErr) {
			failureReason = "retryable_exhausted"
		} else if isBlockingError(taskErr) {
			failureReason = "blocking"
		}
		observability.RecordWorkerTaskFailure(ctx, task.JobID, failureReason)
	}

	// Decrement in-memory counter immediately, then queue async DB decrement.
	wp.decrementRunningTaskInMem(task.JobID)
	if err := wp.releaseRunningTaskSlot(task.JobID); err != nil {
		log.Error().Err(err).Str("job_id", task.JobID).Str("task_id", task.ID).
			Msg("Failed to decrement running_tasks counter")
		// Don't return error here; failed decrements are buffered/retried and reconciliation fixes any drift
	}

	// Queue task update for batch processing (detailed field updates)
	wp.batchManager.QueueTaskUpdate(task)

	return nil
}

func (wp *WorkerPool) populateRequestDiagnostics(task *db.Task, result *crawler.CrawlResult) {
	task.RequestDiagnostics = []byte("{}")
	if result == nil || result.RequestDiagnostics == nil {
		return
	}

	if diagnosticsBytes, err := json.Marshal(result.RequestDiagnostics); err == nil {
		if json.Valid(diagnosticsBytes) {
			task.RequestDiagnostics = diagnosticsBytes
		} else {
			log.Warn().Str("task_id", task.ID).Msg("Request diagnostics produced invalid JSON, using empty object")
		}
	} else {
		log.Error().Err(err).Str("task_id", task.ID).Interface("request_diagnostics", result.RequestDiagnostics).Msg("Failed to marshal request diagnostics")
	}
}

func canonicalTaskHTMLContentType(contentType string) string {
	trimmed := strings.TrimSpace(contentType)
	if trimmed == "" {
		return ""
	}

	mediaType, _, err := mime.ParseMediaType(trimmed)
	if err != nil {
		return strings.ToLower(trimmed)
	}

	return strings.ToLower(mediaType)
}

func normalisedTaskHTMLContentType(contentType string) string {
	trimmed := strings.TrimSpace(contentType)
	if trimmed == "" {
		return "text/html"
	}

	return strings.ToLower(trimmed)
}

func canStoreTaskHTML(contentType string, body []byte) bool {
	if len(body) == 0 {
		return false
	}

	mediaType := canonicalTaskHTMLContentType(contentType)
	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}

func gzipTaskHTML(body []byte) ([]byte, error) {
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	if _, err := gzipWriter.Write(body); err != nil {
		return nil, fmt.Errorf("write gzip html: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, fmt.Errorf("close gzip html: %w", err)
	}
	return buffer.Bytes(), nil
}

func buildTaskHTMLUpload(task *db.Task, result *crawler.CrawlResult, capturedAt time.Time) (*taskHTMLUpload, bool, error) {
	if task == nil || result == nil {
		return nil, false, nil
	}
	if !canStoreTaskHTML(result.ContentType, result.Body) {
		return nil, false, nil
	}

	payload, err := gzipTaskHTML(result.Body)
	if err != nil {
		return nil, false, err
	}

	contentType := normalisedTaskHTMLContentType(result.ContentType)
	uploadContentType := canonicalTaskHTMLContentType(result.ContentType)
	if uploadContentType == "" {
		uploadContentType = "text/html"
	}
	checksum := sha256.Sum256(result.Body)

	return &taskHTMLUpload{
		Bucket:              taskHTMLStorageBucket,
		Path:                archive.TaskHTMLObjectPath(task.JobID, task.ID),
		ContentType:         contentType,
		UploadContentType:   uploadContentType,
		ContentEncoding:     taskHTMLContentEncoding,
		SizeBytes:           int64(len(result.Body)),
		CompressedSizeBytes: int64(len(payload)),
		SHA256:              hex.EncodeToString(checksum[:]),
		CapturedAt:          capturedAt,
		Payload:             payload,
	}, true, nil
}

func applyTaskHTMLMetadata(task *db.Task, upload *taskHTMLUpload) {
	if task == nil || upload == nil {
		return
	}

	task.HTMLStorageBucket = upload.Bucket
	task.HTMLStoragePath = upload.Path
	task.HTMLContentType = upload.ContentType
	task.HTMLContentEncoding = upload.ContentEncoding
	task.HTMLSizeBytes = upload.SizeBytes
	task.HTMLCompressedSizeBytes = upload.CompressedSizeBytes
	task.HTMLSHA256 = upload.SHA256
	task.HTMLCapturedAt = upload.CapturedAt
}

func (wp *WorkerPool) startTaskHTMLPersistenceLoop(ctx context.Context) {
	if wp.taskHTMLPersistCh == nil {
		return
	}

	workerCount := wp.taskHTMLWorkerCount
	pressureThreshold := cap(wp.taskHTMLPersistCh) / 2
	log.Info().Int("workers", workerCount).Int("pressure_threshold", pressureThreshold).Msg("Starting HTML persistence workers")
	for range workerCount {
		wp.wg.Go(func() {
			wp.taskHTMLPersistenceWorker(ctx)
		})
	}

	// Periodic queue-depth monitor: logs at warn when depth exceeds half capacity.
	wp.wg.Go(func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				depth := len(wp.taskHTMLPersistCh)
				if depth > pressureThreshold {
					log.Warn().
						Int("queue_depth", depth).
						Int("queue_cap", cap(wp.taskHTMLPersistCh)).
						Int("workers", workerCount).
						Int("pending", int(wp.taskHTMLPending.Load())).
						Msg("HTML persistence queue pressure")
				}
			case <-wp.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	})
}

func (wp *WorkerPool) taskHTMLPersistenceWorker(ctx context.Context) {
	drain := func(drainCtx context.Context) {
		for {
			if wp.taskHTMLPending.Load() == 0 {
				return
			}
			select {
			case <-drainCtx.Done():
				return
			case request := <-wp.taskHTMLPersistCh:
				wp.processTaskHTMLPersistence(drainCtx, request)
			case <-time.After(50 * time.Millisecond):
				// Backoff before re-checking pending count
			}
		}
	}

	for {
		select {
		case request := <-wp.taskHTMLPersistCh:
			wp.processTaskHTMLPersistence(ctx, request)
		case <-wp.stopCh:
			drainCtx, cancel := context.WithTimeout(context.Background(), taskHTMLDrainTimeout)
			drain(drainCtx)
			cancel()
			return
		case <-ctx.Done():
			drainCtx, cancel := context.WithTimeout(context.Background(), taskHTMLDrainTimeout)
			drain(drainCtx)
			cancel()
			return
		}
	}
}

func (wp *WorkerPool) persistTaskHTML(ctx context.Context, task *db.Task, result *crawler.CrawlResult, capturedAt time.Time) {
	if wp.storageClient == nil || wp.taskHTMLPersistCh == nil || task == nil || result == nil {
		return
	}
	if !canStoreTaskHTML(result.ContentType, result.Body) {
		return
	}

	// Fast-path: skip the expensive body copy when the queue is clearly full.
	// len(ch) is a non-blocking approximation; the select below handles the
	// race where the queue fills between this check and the actual send.
	if len(wp.taskHTMLPersistCh) >= cap(wp.taskHTMLPersistCh) {
		log.Warn().
			Str("task_id", task.ID).
			Str("job_id", task.JobID).
			Int("queue_cap", cap(wp.taskHTMLPersistCh)).
			Int("workers", wp.taskHTMLWorkerCount).
			Msg("HTML persistence queue full, skipping upload")
		return
	}

	wp.taskHTMLPending.Add(1)
	select {
	case wp.taskHTMLPersistCh <- &taskHTMLPersistRequest{
		Task:        *task,
		ContentType: result.ContentType,
		Body:        append([]byte(nil), result.Body...),
		CapturedAt:  capturedAt,
	}:
	default:
		// Queue filled in the race window between len check and send.
		wp.taskHTMLPending.Add(-1)
		log.Warn().
			Str("task_id", task.ID).
			Str("job_id", task.JobID).
			Int("queue_cap", cap(wp.taskHTMLPersistCh)).
			Int("workers", wp.taskHTMLWorkerCount).
			Msg("HTML persistence queue full, skipping upload")
	}
}

func (wp *WorkerPool) processTaskHTMLPersistence(ctx context.Context, request *taskHTMLPersistRequest) {
	defer wp.taskHTMLPending.Add(-1)

	if request == nil || wp.storageClient == nil {
		return
	}

	result := &crawler.CrawlResult{ContentType: request.ContentType, Body: request.Body}
	upload, ok, err := buildTaskHTMLUpload(&request.Task, result, request.CapturedAt)
	if err != nil {
		log.Warn().Err(err).Str("task_id", request.Task.ID).Msg("Failed to prepare task HTML for storage")
		return
	}
	if !ok {
		return
	}

	uploadCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if _, err := wp.storageClient.UploadWithOptions(uploadCtx, upload.Bucket, upload.Path, upload.Payload, storage.UploadOptions{
		ContentType:     upload.UploadContentType,
		ContentEncoding: upload.ContentEncoding,
	}); err != nil {
		log.Warn().Err(err).Str("task_id", request.Task.ID).Str("job_id", request.Task.JobID).Msg("Failed to upload task HTML to storage")
		return
	}

	htmlTask := request.Task
	applyTaskHTMLMetadata(&htmlTask, upload)

	metadata := db.TaskHTMLMetadata{
		StorageBucket:       upload.Bucket,
		StoragePath:         upload.Path,
		ContentType:         upload.ContentType,
		ContentEncoding:     upload.ContentEncoding,
		SizeBytes:           upload.SizeBytes,
		CompressedSizeBytes: upload.CompressedSizeBytes,
		SHA256:              upload.SHA256,
		CapturedAt:          upload.CapturedAt,
	}

	readyCtx, readyCancel := context.WithTimeout(ctx, taskHTMLReadyMaxWait)
	defer readyCancel()
	sawNotReady := false

	for {
		persistCtx, persistCancel := context.WithTimeout(readyCtx, 10*time.Second)
		err = wp.dbQueue.UpdateTaskHTMLMetadata(persistCtx, request.Task.ID, metadata)
		persistCancel()
		if err == nil {
			break
		}
		if !errors.Is(err, db.ErrTaskNotReadyForHTMLMetadata) {
			break
		}
		sawNotReady = true

		select {
		case <-readyCtx.Done():
			err = readyCtx.Err()
		case <-time.After(taskHTMLReadyRetryDelay):
			continue
		}
		break
	}

	if err != nil {
		if sawNotReady && errors.Is(err, context.DeadlineExceeded) {
			log.Warn().Err(err).Str("task_id", request.Task.ID).Str("job_id", request.Task.JobID).Msg("Task HTML metadata still not ready after retries; leaving uploaded object in storage")
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 10*time.Second)
		defer cleanupCancel()
		if deleteErr := wp.storageClient.Delete(cleanupCtx, upload.Bucket, upload.Path); deleteErr != nil {
			log.Warn().Err(deleteErr).Str("task_id", request.Task.ID).Str("job_id", request.Task.JobID).Msg("Failed to clean up uploaded task HTML after metadata persistence failure")
		}
		log.Warn().Err(err).Str("task_id", request.Task.ID).Str("job_id", request.Task.JobID).Msg("Failed to persist task HTML metadata")
		return
	}

	log.Debug().
		Str("task_id", request.Task.ID).
		Str("job_id", request.Task.JobID).
		Int64("html_size_bytes", upload.SizeBytes).
		Int64("html_compressed_size_bytes", upload.CompressedSizeBytes).
		Msg("Stored task HTML in storage")
}

// handleTaskSuccess processes successful task completion with metrics and database updates
func (wp *WorkerPool) handleTaskSuccess(ctx context.Context, task *db.Task, result *crawler.CrawlResult) error {
	now := time.Now().UTC()

	wp.resetJobFailureStreak(task.JobID)

	// Mark as completed with basic metrics
	task.Status = string(TaskStatusCompleted)
	task.CompletedAt = now
	task.StatusCode = result.StatusCode
	task.ResponseTime = result.ResponseTime
	task.CacheStatus = result.CacheStatus
	task.ContentType = result.ContentType
	task.ContentLength = result.ContentLength
	// Only store redirect_url if it's a significant redirect (different domain or path)
	if util.IsSignificantRedirect(result.URL, result.RedirectURL) {
		task.RedirectURL = result.RedirectURL
	}

	// Performance metrics
	task.DNSLookupTime = result.Performance.DNSLookupTime
	task.TCPConnectionTime = result.Performance.TCPConnectionTime
	task.TLSHandshakeTime = result.Performance.TLSHandshakeTime
	task.TTFB = result.Performance.TTFB
	task.ContentTransferTime = result.Performance.ContentTransferTime

	// Second request metrics
	task.SecondResponseTime = result.SecondResponseTime
	task.SecondCacheStatus = result.SecondCacheStatus
	if result.SecondPerformance != nil {
		task.SecondContentLength = result.SecondContentLength
		task.SecondDNSLookupTime = result.SecondPerformance.DNSLookupTime
		task.SecondTCPConnectionTime = result.SecondPerformance.TCPConnectionTime
		task.SecondTLSHandshakeTime = result.SecondPerformance.TLSHandshakeTime
		task.SecondTTFB = result.SecondPerformance.TTFB
		task.SecondContentTransferTime = result.SecondPerformance.ContentTransferTime
	}

	// Marshal JSONB fields - ensure all marshaling succeeds before updating task
	// Always provide safe defaults for all JSON fields
	task.Headers = []byte("{}")
	task.SecondHeaders = []byte("{}")
	task.CacheCheckAttempts = []byte("[]")
	task.RequestDiagnostics = []byte("{}")

	// Only attempt marshaling if data exists and is non-empty
	if len(result.Headers) > 0 {
		if headerBytes, err := json.Marshal(result.Headers); err == nil {
			// Validate that the marshaled JSON is valid
			if json.Valid(headerBytes) {
				task.Headers = headerBytes
			} else {
				log.Warn().Str("task_id", task.ID).Msg("Headers produced invalid JSON, using empty object")
			}
		} else {
			log.Error().Err(err).Str("task_id", task.ID).Interface("headers", result.Headers).Msg("Failed to marshal headers")
		}
	}

	if len(result.SecondHeaders) > 0 {
		if secondHeaderBytes, err := json.Marshal(result.SecondHeaders); err == nil {
			// Validate that the marshaled JSON is valid
			if json.Valid(secondHeaderBytes) {
				task.SecondHeaders = secondHeaderBytes
			} else {
				log.Warn().Str("task_id", task.ID).Msg("Second headers produced invalid JSON, using empty object")
			}
		} else {
			log.Error().Err(err).Str("task_id", task.ID).Interface("second_headers", result.SecondHeaders).Msg("Failed to marshal second headers")
		}
	}

	if len(result.CacheCheckAttempts) > 0 {
		if attemptsBytes, err := json.Marshal(result.CacheCheckAttempts); err == nil {
			// Validate that the marshaled JSON is valid
			if json.Valid(attemptsBytes) {
				task.CacheCheckAttempts = attemptsBytes
			} else {
				log.Warn().Str("task_id", task.ID).Msg("Cache check attempts produced invalid JSON, using empty array")
			}
		} else {
			log.Error().Err(err).Str("task_id", task.ID).Interface("cache_attempts", result.CacheCheckAttempts).Msg("Failed to marshal cache check attempts")
		}
	}

	wp.populateRequestDiagnostics(task, result)

	// Decrement in-memory counter immediately, then queue async DB decrement.
	wp.decrementRunningTaskInMem(task.JobID)
	if err := wp.releaseRunningTaskSlot(task.JobID); err != nil {
		log.Error().Err(err).Str("job_id", task.JobID).Str("task_id", task.ID).
			Msg("Failed to decrement running_tasks counter")
		// Don't return error here; failed decrements are buffered/retried and reconciliation keeps counters accurate
	}

	// Queue task update for batch processing (detailed field updates)
	wp.batchManager.QueueTaskUpdate(task)
	if len(result.Links) > 0 {
		wp.enqueueDiscoveredLinks(task, result)
	}

	wp.persistTaskHTML(ctx, task, result, now)

	// Evaluate job performance for scaling
	if result.ResponseTime > 0 {
		wp.evaluateJobPerformance(task.JobID, result.ResponseTime)
	}

	// Run technology detection for this domain (once per session, async)
	// Use bounded context to ensure detection doesn't hang during shutdown
	if result.StatusCode >= 200 && result.StatusCode < 300 && len(result.BodySample) > 0 {
		go func() { //nolint:gosec // G118: intentionally outlives request; async tech detection
			detectCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			wp.detectTechnologies(detectCtx, task, result)
		}()
	}

	return nil
}

// detectTechnologies runs wappalyzer detection on the crawl result and updates the domain.
// Only runs once per domain per worker pool session to avoid redundant detection.
// If storage is configured, uploads the full HTML body to Supabase Storage.
func (wp *WorkerPool) detectTechnologies(ctx context.Context, task *db.Task, result *crawler.CrawlResult) {
	if wp.techDetector == nil {
		return
	}

	// Look up domain info from job cache
	wp.jobInfoMutex.RLock()
	jobInfo, exists := wp.jobInfoCache[task.JobID]
	wp.jobInfoMutex.RUnlock()

	if !exists || jobInfo.DomainID == 0 {
		log.Debug().Str("job_id", task.JobID).Msg("No job info cached for technology detection")
		return
	}

	domainID := jobInfo.DomainID
	domainName := jobInfo.DomainName

	// Check if already detected for this domain in this session
	wp.techDetectedMutex.RLock()
	alreadyDetected := wp.techDetectedDomains[domainID]
	wp.techDetectedMutex.RUnlock()

	if alreadyDetected {
		return
	}

	// Mark as detected before processing to prevent duplicate detection
	wp.techDetectedMutex.Lock()
	// Double-check after acquiring write lock
	if wp.techDetectedDomains[domainID] {
		wp.techDetectedMutex.Unlock()
		return
	}
	wp.techDetectedDomains[domainID] = true
	wp.techDetectedMutex.Unlock()

	// Run detection using truncated body sample
	detectResult := wp.techDetector.Detect(result.Headers, result.BodySample)

	// Marshal technologies and headers for storage
	techJSON, err := detectResult.TechnologiesJSON()
	if err != nil {
		log.Error().Err(err).Int("domain_id", domainID).Msg("Failed to marshal technologies")
		return
	}

	headersJSON, err := detectResult.HeadersJSON()
	if err != nil {
		log.Error().Err(err).Int("domain_id", domainID).Msg("Failed to marshal headers")
		return
	}

	// Update domain with detection results
	if err := wp.dbQueue.UpdateDomainTechnologies(ctx, domainID, techJSON, headersJSON, ""); err != nil {
		log.Warn().Err(err).Int("domain_id", domainID).Msg("Failed to update domain technologies")
		return
	}

	log.Info().
		Int("domain_id", domainID).
		Str("domain", domainName).
		Int("tech_count", len(detectResult.Technologies)).
		Interface("technologies", detectResult.Technologies).
		Msg("Technology detection completed")
}

func (wp *WorkerPool) processTask(ctx context.Context, task *Task) (*crawler.CrawlResult, error) {
	start := time.Now()
	status := "success"
	queueWait := time.Duration(0)
	if !task.CreatedAt.IsZero() {
		if !task.StartedAt.IsZero() {
			queueWait = task.StartedAt.Sub(task.CreatedAt)
		} else {
			queueWait = time.Since(task.CreatedAt)
		}
	}

	ctx, span := observability.StartWorkerTaskSpan(ctx, observability.WorkerTaskSpanInfo{
		JobID:     task.JobID,
		TaskID:    task.ID,
		Domain:    task.DomainName,
		Path:      task.Path,
		FindLinks: task.FindLinks,
	})
	defer span.End()

	defer func() {
		totalDuration := time.Duration(0)
		if !task.CreatedAt.IsZero() {
			totalDuration = time.Since(task.CreatedAt)
		}

		observability.RecordWorkerTask(ctx, observability.WorkerTaskMetrics{
			JobID:         task.JobID,
			Status:        status,
			Duration:      time.Since(start),
			QueueWait:     queueWait,
			TotalDuration: totalDuration,
		})
	}()

	// Construct a proper URL for processing
	urlStr := constructTaskURL(task.Path, task.Host, task.DomainName)

	log.Debug().Str("url", urlStr).Str("task_id", task.ID).Msg("Starting URL warm")

	limiter := wp.ensureDomainLimiter()
	permit, acquired := limiter.TryAcquire(DomainRequest{
		Domain:      task.DomainName,
		JobID:       task.JobID,
		RobotsDelay: time.Duration(task.CrawlDelay) * time.Second,
		JobConcurrency: func() int {
			if task.JobConcurrency > 0 {
				return task.JobConcurrency
			}
			return 1
		}(),
	})
	if !acquired {
		return nil, ErrDomainDelay
	}
	released := false
	defer func() {
		if !released {
			permit.Release(false, false)
		}
	}()

	result, err := wp.crawler.WarmURL(ctx, urlStr, task.FindLinks)
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		rateLimited := false
		if result != nil {
			switch result.StatusCode {
			case http.StatusTooManyRequests, http.StatusForbidden, http.StatusServiceUnavailable:
				rateLimited = true
			}
		}
		if !rateLimited {
			rateLimited = IsRateLimitError(err)
		}
		log.Debug().Err(err).
			Str("task_id", task.ID).
			Bool("rate_limited", rateLimited).
			Int("status_code", func() int {
				if result != nil {
					return result.StatusCode
				}
				return 0
			}()).
			Msg("Crawler failed")
		permit.Release(false, rateLimited)
		released = true
		return result, fmt.Errorf("crawler error: %w", err)
	}
	permit.Release(true, false)
	released = true

	if result != nil {
		span.SetAttributes(
			attribute.Int("http.status_code", result.StatusCode),
			attribute.Int("task.links_found", len(result.Links)),
			attribute.String("task.content_type", result.ContentType),
		)
	}
	span.SetStatus(codes.Ok, "completed")

	log.Debug().
		Int("status_code", result.StatusCode).
		Str("task_id", task.ID).
		Int("links_found", len(result.Links)).
		Str("content_type", result.ContentType).
		Msg("Crawler completed")

	return result, nil
}

// isRetryableError checks if an error should trigger a retry
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errorStr := strings.ToLower(err.Error())

	// Network/timeout errors that should be retried
	networkErrors := strings.Contains(errorStr, "timeout") ||
		strings.Contains(errorStr, "deadline exceeded") ||
		strings.Contains(errorStr, "connection") ||
		strings.Contains(errorStr, "network") ||
		strings.Contains(errorStr, "temporary") ||
		strings.Contains(errorStr, "reset by peer") ||
		strings.Contains(errorStr, "broken pipe") ||
		strings.Contains(errorStr, "unexpected eof")

	// Server errors that should be retried (likely due to load/temporary issues)
	// Note: 503 Service Unavailable is treated as a blocking error (see isBlockingError)
	serverErrors := strings.Contains(errorStr, "internal server error") ||
		strings.Contains(errorStr, "bad gateway") ||
		strings.Contains(errorStr, "gateway timeout") ||
		strings.Contains(errorStr, "502") ||
		strings.Contains(errorStr, "504") ||
		strings.Contains(errorStr, "500")

	return networkErrors || serverErrors
}

// isBlockingError checks if an error indicates we're being blocked
func isBlockingError(err error) bool {
	if err == nil {
		return false
	}

	errorStr := strings.ToLower(err.Error())

	// Blocking/rate limit errors that need special handling with exponential backoff
	return strings.Contains(errorStr, "403") ||
		strings.Contains(errorStr, "forbidden") ||
		strings.Contains(errorStr, "429") ||
		strings.Contains(errorStr, "too many requests") ||
		strings.Contains(errorStr, "rate limit") ||
		strings.Contains(errorStr, "503") ||
		strings.Contains(errorStr, "service unavailable")
}

var statusCodePattern = regexp.MustCompile(`\b(\d{3})\b`)

func isClientOrRedirectError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "crawler error") {
		keywords := []string{
			"not found",
			"bad request",
			"unauthorized",
			"forbidden",
			"gone",
			"method not allowed",
			"temporary redirect",
			"permanent redirect",
			"moved permanently",
			"see other",
		}
		for _, kw := range keywords {
			if strings.Contains(lower, kw) {
				return true
			}
		}
	}
	matches := statusCodePattern.FindAllStringSubmatch(lower, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		code, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		if code >= 400 && code < 500 {
			return true
		}
	}
	return false
}

// calculateBackoffDuration computes exponential backoff duration for retry attempts
// Uses 2^retryCount formula with a maximum cap of 60 seconds
func calculateBackoffDuration(retryCount int) time.Duration {
	// 2^0 = 1s, 2^1 = 2s, 2^2 = 4s, 2^3 = 8s, etc.
	// Cap retryCount to a safe value to prevent overflow during bit shift
	if retryCount < 0 {
		retryCount = 0
	}
	shift := min(
		//nolint:gosec // retryCount is capped and checked for negative
		uint(retryCount), 30)
	backoffSeconds := 1 << shift // Bit shift for 2^retryCount
	backoffDuration := time.Duration(backoffSeconds) * time.Second

	// Cap at 60 seconds maximum
	maxBackoff := 60 * time.Second
	if backoffDuration > maxBackoff {
		backoffDuration = maxBackoff
	}

	return backoffDuration
}

func normaliseComparableHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	return strings.TrimPrefix(host, "www.")
}

func sameRegistrableDomain(hostA, hostB string) bool {
	normalisedA := normaliseComparableHost(hostA)
	normalisedB := normaliseComparableHost(hostB)
	if normalisedA == "" || normalisedB == "" {
		return false
	}

	rootA, errA := publicsuffix.EffectiveTLDPlusOne(normalisedA)
	rootB, errB := publicsuffix.EffectiveTLDPlusOne(normalisedB)
	if errA != nil || errB != nil {
		return normalisedA == normalisedB
	}

	return rootA == rootB
}

func sameHostWithWWWEquivalence(hostA, hostB string) bool {
	return normaliseComparableHost(hostA) == normaliseComparableHost(hostB)
}

// isLinkAllowedForTask determines whether a discovered hostname should be queued.
func isLinkAllowedForTask(discoveredHost string, task *Task) bool {
	if task == nil {
		return false
	}

	if task.AllowCrossSubdomainLinks {
		return sameRegistrableDomain(discoveredHost, task.DomainName)
	}

	if task.Host != "" {
		return sameHostWithWWWEquivalence(discoveredHost, task.Host)
	}

	return sameHostWithWWWEquivalence(discoveredHost, task.DomainName)
}

// updateTaskPriorities updates the priority scores for tasks of linked pages
func (wp *WorkerPool) updateTaskPriorities(ctx context.Context, jobID string, domainID int, newPriority float64, paths []string) error {
	if len(paths) == 0 {
		return nil
	}

	if skip, wait := wp.shouldThrottlePriorityUpdate(jobID, newPriority); skip {
		log.Debug().
			Str("job_id", jobID).
			Float64("priority", newPriority).
			Dur("cooldown_remaining", wait).
			Msg("Debounced priority update")
		return nil
	}

	uniquePaths := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		if _, exists := seen[p]; exists {
			continue
		}
		seen[p] = struct{}{}
		uniquePaths = append(uniquePaths, p)
	}

	var rowsAffected int64
	err := wp.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		// Update priorities using GREATEST of structural priority and traffic score
		// Join through jobs to get organisation_id for page_analytics lookup
		result, err := tx.ExecContext(ctx, `
			UPDATE tasks t
			SET priority_score = GREATEST(
				t.priority_score,
				$1,
				COALESCE(pa.traffic_score, 0)
			)
			FROM pages p
			JOIN jobs j ON j.id = $2
			LEFT JOIN page_analytics pa ON pa.organisation_id = j.organisation_id
				AND pa.domain_id = p.domain_id
				AND pa.path = p.path
			WHERE t.page_id = p.id
			AND t.job_id = $2
			AND p.domain_id = $3
			AND p.path = ANY($4)
			AND (
				t.priority_score < $1
				OR t.priority_score < COALESCE(pa.traffic_score, 0)
			)
		`, newPriority, jobID, domainID, pq.Array(uniquePaths))

		if err != nil {
			return err
		}

		rowsAffected, err = result.RowsAffected()
		return err
	})

	if err != nil {
		return fmt.Errorf("failed to update task priorities: %w", err)
	}

	if rowsAffected > 0 {
		log.Debug().
			Str("job_id", jobID).
			Int64("tasks_updated", rowsAffected).
			Float64("new_priority", newPriority).
			Msg("Updated task priorities for discovered links")
	} else {
		log.Debug().
			Str("job_id", jobID).
			Float64("priority", newPriority).
			Int("paths", len(uniquePaths)).
			Msg("Priority update skipped (no lower-priority tasks found)")
	}

	return nil
}

// evaluateJobPerformance checks if a job needs performance scaling while
// respecting concurrency-derived capacity limits.
func (wp *WorkerPool) evaluateJobPerformance(jobID string, responseTime int64) {
	var (
		avgResponseTime int64
		oldBoost        int
		neededBoost     int
		recentBlocking  bool
	)

	wp.perfMutex.Lock()
	perf, exists := wp.jobPerformance[jobID]
	if !exists {
		wp.perfMutex.Unlock()
		return // Job not tracked
	}

	// Add response time to recent tasks (sliding window of 5)
	perf.RecentTasks = append(perf.RecentTasks, responseTime)
	if len(perf.RecentTasks) > 5 {
		perf.RecentTasks = perf.RecentTasks[1:] // Remove oldest
	}

	// Only evaluate after we have enough samples for a stable average
	if len(perf.RecentTasks) < 3 {
		perf.LastCheck = time.Now()
		wp.perfMutex.Unlock()
		return
	}

	var total int64
	for _, rt := range perf.RecentTasks {
		total += rt
	}
	avgResponseTime = total / int64(len(perf.RecentTasks))

	oldBoost = perf.CurrentBoost
	recentBlocking = !perf.LastConcurrencyBlock.IsZero() && time.Since(perf.LastConcurrencyBlock) < concurrencyBlockCooldown

	switch {
	case avgResponseTime >= 4000:
		neededBoost = 20
	case avgResponseTime >= 3000:
		neededBoost = 15
	case avgResponseTime >= 2000:
		neededBoost = 10
	case avgResponseTime >= 1000:
		neededBoost = 5
	default:
		neededBoost = 0
	}

	if recentBlocking && neededBoost > oldBoost {
		neededBoost = oldBoost
	}

	perf.LastCheck = time.Now()
	wp.perfMutex.Unlock()

	if neededBoost == oldBoost {
		return
	}

	actualBoost := neededBoost

	if neededBoost > oldBoost {
		boostDiff := neededBoost - oldBoost
		targetWorkers := wp.calculateConcurrencyTarget()

		wp.workersMutex.RLock()
		currentWorkers := wp.currentWorkers
		wp.workersMutex.RUnlock()

		desiredWorkers := min(min(currentWorkers+boostDiff, targetWorkers), wp.maxWorkers)

		additionalWorkers := desiredWorkers - currentWorkers
		if additionalWorkers <= 0 {
			actualBoost = oldBoost
		} else {
			actualBoost = oldBoost + additionalWorkers
			target := desiredWorkers
			if !wp.stopping.Load() {
				go wp.scaleWorkers(context.Background(), target)
			}
		}
	}

	clamped := neededBoost > oldBoost && actualBoost == oldBoost

	log.Debug().
		Str("job_id", jobID).
		Int64("avg_response_time", avgResponseTime).
		Int("requested_boost", neededBoost).
		Int("old_boost", oldBoost).
		Int("new_boost", actualBoost).
		Bool("recently_blocked", recentBlocking).
		Bool("clamped_by_concurrency", clamped).
		Msg("Job performance scaling evaluated")

	wp.perfMutex.Lock()
	if perf, exists := wp.jobPerformance[jobID]; exists {
		perf.CurrentBoost = actualBoost
		perf.LastCheck = time.Now()
	}
	wp.perfMutex.Unlock()
}

func hasNotificationConfig(cfg *db.Config) bool {
	if cfg == nil {
		return false
	}
	if cfg.DatabaseURL != "" {
		return true
	}
	return cfg.Host != "" && cfg.Port != "" && cfg.Database != "" && cfg.User != ""
}

// listenForNotifications sets up PostgreSQL LISTEN/NOTIFY
func (wp *WorkerPool) listenForNotifications(ctx context.Context) {
	var conn *pgx.Conn
	var err error

	connect := func() (*pgx.Conn, error) {
		c, err := pgx.Connect(ctx, wp.dbConfig.ConnectionString())
		if err != nil {
			return nil, err
		}
		_, err = c.Exec(ctx, "LISTEN new_tasks")
		if err != nil {
			_ = c.Close(ctx)
			return nil, err
		}
		return c, nil
	}

	conn, err = connect()
	if err != nil {
		log.Error().Err(err).Msg("Failed to connect for notifications initially")
		return
	}
	defer conn.Close(ctx)

	for {
		select {
		case <-wp.stopCh:
			log.Debug().Msg("Notification listener received stop signal.")
			return
		case <-ctx.Done():
			log.Debug().Msg("Notification listener context cancelled.")
			return
		default:
			// Non-blocking check for stop signal before waiting for notification
		}

		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil || wp.stopping.Load() {
				return // Context cancelled or pool is stopping
			}
			log.Warn().Err(err).Msg("Error waiting for notification, reconnecting...")
			_ = conn.Close(ctx)
			time.Sleep(5 * time.Second) // Wait before reconnecting

			conn, err = connect()
			if err != nil {
				log.Warn().Err(err).Msg("Failed to reconnect for notifications")
				continue
			}
			continue
		}

		log.Debug().Str("channel", notification.Channel).Msg("Received database notification")
		// Notify workers of new tasks (non-blocking)
		select {
		case wp.notifyCh <- struct{}{}:
		default:
			// Channel already has notification pending
		}
	}
}
