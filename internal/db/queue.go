package db

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/rs/zerolog/log"

	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/Harvey-AU/hover/internal/util"
)

var ErrTaskNotReadyForHTMLMetadata = errors.New("task not ready for html metadata")

// ConcurrencyOverrideFunc is a callback to get effective concurrency for a job
// Returns the effective concurrency limit, or 0 if no override exists
type ConcurrencyOverrideFunc func(jobID string, domain string) int

// DbQueue is a PostgreSQL implementation of a job queue
type DbQueue struct {
	db *DB
	// Mutex to prevent concurrent cleanup operations that cause prepared statement conflicts
	cleanupMutex        sync.Mutex
	poolWarnThreshold   float64
	poolRejectThreshold float64
	lastWarnLog         time.Time
	lastRejectLog       time.Time
	lastRejectWaitCount int64
	poolSemaphore       chan struct{}
	preserveConnections int
	maxConcurrent       int
	maxTxRetries        int
	retryBaseDelay      time.Duration
	retryMaxDelay       time.Duration
	rng                 *rand.Rand
	// concurrencyOverride is a callback to get effective concurrency overrides from domain limiter
	concurrencyOverride ConcurrencyOverrideFunc
}

// ErrPoolSaturated is returned when the database connection pool cannot provide
// a connection before the caller's context expires.
var ErrPoolSaturated = errors.New("database connection pool saturated")

// ErrConcurrencyBlocked is returned when pending tasks exist but all are blocked
// by job concurrency limits. Workers should back off when receiving this error.
var ErrConcurrencyBlocked = errors.New("tasks exist but blocked by concurrency limits")

const (
	defaultPoolWarnThreshold   = 0.90
	defaultPoolRejectThreshold = 0.95
	minRejectWaitDelta         = 5
	poolLogCooldown            = 5 * time.Second
	defaultQueueConcurrency    = 12
	defaultTxRetries           = 3
	defaultRetryBaseDelay      = 200 * time.Millisecond
	defaultRetryMaxDelay       = 1500 * time.Millisecond

	waitingReasonConcurrencyLimit = "concurrency_limit"
	waitingReasonQuotaExhausted   = "quota_exhausted"
)

// NewDbQueue creates a PostgreSQL job queue
func NewDbQueue(db *DB) *DbQueue {
	warn := parseThresholdEnv("DB_POOL_WARN_THRESHOLD", defaultPoolWarnThreshold)
	reject := parseThresholdEnv("DB_POOL_REJECT_THRESHOLD", defaultPoolRejectThreshold)

	// Ensure thresholds are sane and warn <= reject
	if reject <= 0 || reject > 1 {
		reject = defaultPoolRejectThreshold
	}
	if warn <= 0 || warn >= reject {
		warn = reject - 0.05
		if warn <= 0 {
			warn = defaultPoolWarnThreshold
		}
	}

	reserve := parseReservedConnections()
	queueLimit := parseIntEnv("DB_QUEUE_MAX_CONCURRENCY", defaultQueueConcurrency)
	txRetries := parseIntEnv("DB_TX_MAX_RETRIES", defaultTxRetries)
	baseDelay := parseDurationEnvMS("DB_TX_BACKOFF_BASE_MS", defaultRetryBaseDelay)
	maxDelay := max(parseDurationEnvMS("DB_TX_BACKOFF_MAX_MS", defaultRetryMaxDelay), baseDelay)

	var semaphore chan struct{}
	maxConcurrent := queueLimit
	if db != nil && db.config != nil {
		maxOpen := db.config.MaxOpenConns
		if maxOpen > 0 {
			poolCapacity := max(maxOpen-reserve, 1)
			if queueLimit > 0 && queueLimit < poolCapacity {
				maxConcurrent = queueLimit
			} else {
				maxConcurrent = poolCapacity
			}
			reserve = maxOpen - maxConcurrent
		}
	}

	if maxConcurrent < 1 {
		maxConcurrent = 1
	}

	semaphore = make(chan struct{}, maxConcurrent)

	return &DbQueue{
		db:                  db,
		poolWarnThreshold:   warn,
		poolRejectThreshold: reject,
		poolSemaphore:       semaphore,
		preserveConnections: reserve,
		maxConcurrent:       maxConcurrent,
		maxTxRetries:        txRetries,
		retryBaseDelay:      baseDelay,
		retryMaxDelay:       maxDelay,
		rng:                 nil, // Initialised on first use in randInt63n
	}
}

// SetConcurrencyOverride sets a callback to retrieve effective concurrency from the domain limiter
func (q *DbQueue) SetConcurrencyOverride(fn ConcurrencyOverrideFunc) {
	q.concurrencyOverride = fn
}

func parseThresholdEnv(key string, fallback float64) float64 {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		if parsed, err := strconv.ParseFloat(val, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func parseReservedConnections() int {
	const defaultReserve = 4
	if val := strings.TrimSpace(os.Getenv("DB_POOL_RESERVED_CONNECTIONS")); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil {
			if parsed < 0 {
				return 0
			}
			return parsed
		}
	}
	return defaultReserve
}

func parseIntEnv(key string, fallback int) int {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil {
			return parsed
		}
	}
	return fallback
}

func parseDurationEnvMS(key string, fallback time.Duration) time.Duration {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil {
			if parsed < 0 {
				return fallback
			}
			return time.Duration(parsed) * time.Millisecond
		}
	}
	return fallback
}

// Execute runs a database operation in a transaction
func (q *DbQueue) Execute(ctx context.Context, fn func(*sql.Tx) error) error {
	totalStart := time.Now()

	// Add timeout to context if none exists
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	maxAttempts := max(q.maxTxRetries, 1)

	var lastErr error
	var poolWaitTotal time.Duration
	var execTotal time.Duration

	for attempt := range maxAttempts {
		poolWaitStart := time.Now()
		release, err := q.ensurePoolCapacity(ctx)
		poolWait := time.Since(poolWaitStart)
		poolWaitTotal += poolWait

		if err != nil {
			// Classify the error type for observability
			errorClass := classifyError(err)

			if !q.shouldRetry(err) || attempt == maxAttempts-1 {
				// Log pool saturation details
				log.Warn().
					Err(err).
					Str("error_class", errorClass).
					Dur("pool_wait_total", poolWaitTotal).
					Dur("total_duration", time.Since(totalStart)).
					Int("attempt", attempt+1).
					Int("max_attempts", maxAttempts).
					Msg("Failed to acquire database connection")
				return err
			}

			backoff := q.computeBackoff(attempt)
			log.Debug().
				Err(err).
				Str("error_class", errorClass).
				Dur("backoff", backoff).
				Int("attempt", attempt+1).
				Msg("Pool capacity check failed, retrying after backoff")

			if err := q.waitForRetry(ctx, attempt); err != nil {
				return err
			}
			continue
		}

		execStart := time.Now()
		execErr := q.executeOnce(ctx, fn)
		execDuration := time.Since(execStart)
		execTotal += execDuration
		release()

		if execErr == nil {
			totalDuration := time.Since(totalStart)
			// Log if transaction was slow (>5s) to help diagnose performance issues
			if totalDuration > 5*time.Second {
				log.Warn().
					Dur("total_duration", totalDuration).
					Dur("pool_wait_total", poolWaitTotal).
					Dur("exec_total", execTotal).
					Int("attempts", attempt+1).
					Msg("Slow database transaction completed")
			}
			return nil
		}

		lastErr = execErr
		if errors.Is(execErr, sql.ErrNoRows) {
			totalDuration := time.Since(totalStart)
			log.Debug().
				Err(execErr).
				Dur("total_duration", totalDuration).
				Dur("pool_wait_total", poolWaitTotal).
				Dur("exec_total", execTotal).
				Int("attempt", attempt+1).
				Msg("Database transaction finished with no rows")
			return execErr
		}
		// Don't log concurrency blocking as error - it's normal backoff behaviour
		if errors.Is(execErr, ErrConcurrencyBlocked) {
			return execErr
		}

		errorClass := classifyError(execErr)

		if !q.shouldRetry(execErr) || attempt == maxAttempts-1 {
			totalDuration := time.Since(totalStart)
			log.Error().
				Err(execErr).
				Str("error_class", errorClass).
				Dur("total_duration", totalDuration).
				Dur("pool_wait_total", poolWaitTotal).
				Dur("exec_total", execTotal).
				Int("attempt", attempt+1).
				Int("max_attempts", maxAttempts).
				Bool("retryable", q.shouldRetry(execErr)).
				Msg("Database transaction failed")
			return execErr
		}

		backoff := q.computeBackoff(attempt)
		log.Debug().
			Err(execErr).
			Str("error_class", errorClass).
			Dur("backoff", backoff).
			Dur("exec_duration", execDuration).
			Int("attempt", attempt+1).
			Msg("Transaction failed, retrying after backoff")

		if err := q.waitForRetry(ctx, attempt); err != nil {
			return err
		}
	}

	return lastErr
}

// ExecuteWithContext runs a transactional operation with full context propagation.
// The callback receives both the context and transaction, ensuring SQL statements
// can respect timeouts. This should be preferred over Execute for all new code.
func (q *DbQueue) ExecuteWithContext(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	totalStart := time.Now()

	// Add timeout to context if none exists
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	maxAttempts := max(q.maxTxRetries, 1)

	var lastErr error
	var poolWaitTotal time.Duration
	var execTotal time.Duration

	for attempt := range maxAttempts {
		poolWaitStart := time.Now()
		release, err := q.ensurePoolCapacity(ctx)
		poolWait := time.Since(poolWaitStart)
		poolWaitTotal += poolWait

		if err != nil {
			// Classify the error type for observability
			errorClass := classifyError(err)

			if !q.shouldRetry(err) || attempt == maxAttempts-1 {
				// Log pool saturation details
				log.Warn().
					Err(err).
					Str("error_class", errorClass).
					Dur("pool_wait_total", poolWaitTotal).
					Dur("total_duration", time.Since(totalStart)).
					Int("attempt", attempt+1).
					Int("max_attempts", maxAttempts).
					Msg("Failed to acquire database connection")
				return err
			}

			backoff := q.computeBackoff(attempt)
			log.Debug().
				Err(err).
				Str("error_class", errorClass).
				Dur("backoff", backoff).
				Int("attempt", attempt+1).
				Msg("Pool capacity check failed, retrying after backoff")

			if err := q.waitForRetry(ctx, attempt); err != nil {
				return err
			}
			continue
		}

		execStart := time.Now()
		execErr := q.executeOnceWithContext(ctx, fn)
		execDuration := time.Since(execStart)
		execTotal += execDuration
		release()

		if execErr == nil {
			totalDuration := time.Since(totalStart)
			// Log if transaction was slow (>5s) to help diagnose performance issues
			if totalDuration > 5*time.Second {
				log.Warn().
					Dur("total_duration", totalDuration).
					Dur("pool_wait_total", poolWaitTotal).
					Dur("exec_total", execTotal).
					Int("attempts", attempt+1).
					Msg("Slow database transaction completed")
			}
			return nil
		}

		lastErr = execErr
		if errors.Is(execErr, sql.ErrNoRows) {
			totalDuration := time.Since(totalStart)
			log.Debug().
				Err(execErr).
				Dur("total_duration", totalDuration).
				Dur("pool_wait_total", poolWaitTotal).
				Dur("exec_total", execTotal).
				Int("attempt", attempt+1).
				Msg("Database transaction finished with no rows")
			return execErr
		}
		// Don't log concurrency blocking as error - it's normal backoff behaviour
		if errors.Is(execErr, ErrConcurrencyBlocked) {
			return execErr
		}

		errorClass := classifyError(execErr)

		if !q.shouldRetry(execErr) || attempt == maxAttempts-1 {
			totalDuration := time.Since(totalStart)
			log.Error().
				Err(execErr).
				Str("error_class", errorClass).
				Dur("total_duration", totalDuration).
				Dur("pool_wait_total", poolWaitTotal).
				Dur("exec_total", execTotal).
				Int("attempt", attempt+1).
				Int("max_attempts", maxAttempts).
				Bool("retryable", q.shouldRetry(execErr)).
				Msg("Database transaction failed")
			return execErr
		}

		backoff := q.computeBackoff(attempt)
		log.Debug().
			Err(execErr).
			Str("error_class", errorClass).
			Dur("backoff", backoff).
			Dur("exec_duration", execDuration).
			Int("attempt", attempt+1).
			Msg("Transaction failed, retrying after backoff")

		if err := q.waitForRetry(ctx, attempt); err != nil {
			return err
		}
	}

	return lastErr
}

func (q *DbQueue) executeOnce(ctx context.Context, fn func(*sql.Tx) error) error {
	beginStart := time.Now()
	tx, err := q.db.client.BeginTx(ctx, nil)
	beginDuration := time.Since(beginStart)

	if err != nil {
		sentry.CaptureException(err)
		log.Error().
			Err(err).
			Dur("begin_duration", beginDuration).
			Msg("Failed to begin transaction")
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	var committed bool
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	queryStart := time.Now()
	if err := fn(tx); err != nil {
		queryDuration := time.Since(queryStart)
		// Log slow queries even when they fail
		if queryDuration > 5*time.Second {
			log.Warn().
				Err(err).
				Dur("begin_duration", beginDuration).
				Dur("query_duration", queryDuration).
				Msg("Slow query failed in transaction")
		}
		return err
	}
	queryDuration := time.Since(queryStart)

	commitStart := time.Now()
	if err := tx.Commit(); err != nil {
		commitDuration := time.Since(commitStart)
		sentry.CaptureException(err)
		log.Error().
			Err(err).
			Dur("begin_duration", beginDuration).
			Dur("query_duration", queryDuration).
			Dur("commit_duration", commitDuration).
			Msg("Failed to commit transaction")
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	committed = true
	commitDuration := time.Since(commitStart)

	// Log breakdown for slow transactions
	totalDuration := beginDuration + queryDuration + commitDuration
	if totalDuration > 5*time.Second {
		log.Warn().
			Dur("total", totalDuration).
			Dur("begin", beginDuration).
			Dur("query", queryDuration).
			Dur("commit", commitDuration).
			Msg("Slow transaction breakdown")
	}

	return nil
}

func (q *DbQueue) executeOnceWithContext(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	beginStart := time.Now()
	tx, err := q.db.client.BeginTx(ctx, nil)
	beginDuration := time.Since(beginStart)

	if err != nil {
		sentry.CaptureException(err)
		log.Error().
			Err(err).
			Dur("begin_duration", beginDuration).
			Msg("Failed to begin transaction")
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	var committed bool
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	queryStart := time.Now()
	if err := fn(ctx, tx); err != nil {
		queryDuration := time.Since(queryStart)
		// Log slow queries even when they fail
		if queryDuration > 5*time.Second {
			log.Warn().
				Err(err).
				Dur("begin_duration", beginDuration).
				Dur("query_duration", queryDuration).
				Msg("Slow query failed in transaction")
		}
		return err
	}
	queryDuration := time.Since(queryStart)

	commitStart := time.Now()
	if err := tx.Commit(); err != nil {
		commitDuration := time.Since(commitStart)
		sentry.CaptureException(err)
		log.Error().
			Err(err).
			Dur("begin_duration", beginDuration).
			Dur("query_duration", queryDuration).
			Dur("commit_duration", commitDuration).
			Msg("Failed to commit transaction")
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	committed = true
	commitDuration := time.Since(commitStart)

	// Log breakdown for slow transactions
	totalDuration := beginDuration + queryDuration + commitDuration
	if totalDuration > 5*time.Second {
		log.Warn().
			Dur("total", totalDuration).
			Dur("begin", beginDuration).
			Dur("query", queryDuration).
			Dur("commit", commitDuration).
			Msg("Slow transaction breakdown")
	}

	return nil
}

// classifyError categorises errors for observability and retry logging
func classifyError(err error) string {
	if err == nil {
		return "none"
	}

	if errors.Is(err, context.Canceled) {
		return "context_cancelled"
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}

	if errors.Is(err, ErrPoolSaturated) {
		return "pool_saturated"
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "network_timeout"
		}
		return "network_error"
	}

	errStr := strings.ToLower(err.Error())

	if strings.Contains(errStr, "sqlstate") {
		return "postgres_error"
	}
	if strings.Contains(errStr, "too many clients") {
		return "too_many_clients"
	}
	if strings.Contains(errStr, "connection reset") {
		return "connection_reset"
	}
	if strings.Contains(errStr, "connection refused") {
		return "connection_refused"
	}
	if strings.Contains(errStr, "timeout") {
		return "timeout"
	}

	return "unknown"
}

func (q *DbQueue) shouldRetry(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.Canceled) {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	if errors.Is(err, ErrPoolSaturated) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return true
		}
	}

	errStr := strings.ToLower(err.Error())
	retrySnippets := []string{
		"failed to begin transaction",
		"failed to commit transaction",
		"too many clients",
		"connection reset",
		"connection refused",
		"timeout",
		"connection pool saturated",
		"deadlock detected",
	}
	for _, snippet := range retrySnippets {
		if strings.Contains(errStr, snippet) {
			return true
		}
	}

	return false
}

func (q *DbQueue) waitForRetry(ctx context.Context, attempt int) error {
	backoff := q.computeBackoff(attempt)
	select {
	case <-time.After(backoff):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *DbQueue) computeBackoff(attempt int) time.Duration {
	base := q.retryBaseDelay
	max := q.retryMaxDelay
	if base <= 0 {
		base = defaultRetryBaseDelay
	}
	if max < base {
		max = base
	}

	factor := 1 << attempt
	delay := min(time.Duration(factor)*base, max)

	jitterRange := delay / 5
	if jitterRange <= 0 {
		return delay
	}
	return delay - jitterRange + time.Duration(q.randInt63n(int64(jitterRange*2)))
}

func (q *DbQueue) randInt63n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	// Use crypto/rand for security compliance, even for non-sensitive jitter
	res, err := crand.Int(crand.Reader, big.NewInt(n))
	if err == nil {
		return res.Int64()
	}
	// Fallback to basic rand if crypto fails
	return rand.Int63n(n) //nolint:gosec // safe fallback for non-sensitive jitter
}

// ExecuteMaintenance runs a low-impact transaction that bypasses pool saturation guards.
// This is intended for housekeeping tasks that must always run, even when the pool is busy.
func (q *DbQueue) ExecuteMaintenance(ctx context.Context, fn func(*sql.Tx) error) error {
	if q == nil || q.db == nil || q.db.client == nil {
		return fmt.Errorf("maintenance transaction requires an initialised database connection")
	}

	// Keep maintenance units short-lived to minimise pool impact.
	// Allow 65s to accommodate recovery batches processing large backlogs.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 65*time.Second)
		defer cancel()
	}

	tx, err := q.db.client.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		sentry.CaptureException(err)
		return fmt.Errorf("failed to begin maintenance transaction: %w", err)
	}
	var committed bool
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Apply a statement timeout so maintenance never blocks the pool indefinitely.
	// Set to 60s to allow recovery batches time to process large backlogs.
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '60s'`); err != nil {
		log.Warn().Err(err).Msg("Failed to set maintenance statement timeout")
	}

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		sentry.CaptureException(err)
		return fmt.Errorf("failed to commit maintenance transaction: %w", err)
	}
	committed = true

	return nil
}

func (q *DbQueue) ensurePoolCapacity(ctx context.Context) (func(), error) {
	noop := func() {}
	if q == nil || q.db == nil || q.db.client == nil {
		return noop, nil
	}

	release := noop
	if q.poolSemaphore != nil {
		select {
		case q.poolSemaphore <- struct{}{}:
			release = func() { <-q.poolSemaphore }
		case <-ctx.Done():
			// Explicitly log pool rejections when context expires before acquiring semaphore
			log.Debug().
				Err(ctx.Err()).
				Int("semaphore_capacity", cap(q.poolSemaphore)).
				Int("semaphore_in_use", len(q.poolSemaphore)).
				Msg("Pool semaphore rejected request - context done before acquiring slot")
			observability.RecordDBPoolRejection(ctx)
			return noop, ErrPoolSaturated
		}
	}

	// Check fd pressure before acquiring a DB connection
	fdCurrent, fdLimit, fdErr := util.FDUsage()
	if fdErr == nil {
		fdPressure := util.FDPressureFrom(fdCurrent, fdLimit)
		observability.RecordFDStats(ctx, fdCurrent, fdLimit, fdPressure)
		if fdPressure > 0.90 {
			log.Warn().
				Int("fd_current", fdCurrent).
				Int("fd_limit", fdLimit).
				Float64("fd_pressure", fdPressure).
				Msg("File descriptor pressure critical — rejecting DB operation")
			release()
			return noop, ErrPoolSaturated
		}
	}

	stats := q.db.client.Stats()
	maxOpen := stats.MaxOpenConnections
	if maxOpen == 0 && q.db.config != nil {
		maxOpen = q.db.config.MaxOpenConns
	}
	if maxOpen <= 0 {
		return release, nil
	}

	usage := float64(stats.InUse) / float64(maxOpen)

	observability.RecordDBPoolStats(ctx, observability.DBPoolSnapshot{
		InUse:        stats.InUse,
		Idle:         stats.Idle,
		WaitCount:    stats.WaitCount,
		WaitDuration: stats.WaitDuration,
		MaxOpen:      maxOpen,
		Reserved:     q.preserveConnections,
		Usage:        usage,
	})

	waitDelta := stats.WaitCount - q.lastRejectWaitCount
	if usage >= q.poolRejectThreshold && waitDelta >= minRejectWaitDelta && time.Since(q.lastRejectLog) > poolLogCooldown {
		log.Warn().
			Int("in_use", stats.InUse).
			Int("max_open", maxOpen).
			Int("reserved", q.preserveConnections).
			Float64("usage", usage).
			Int64("wait_count", stats.WaitCount).
			Int64("wait_delta", waitDelta).
			Msg("DB pool saturated: requests will queue")
		sentry.WithScope(func(scope *sentry.Scope) {
			scope.SetLevel(sentry.LevelWarning)
			scope.SetTag("event_type", "db_pool")
			scope.SetTag("state", "queue")
			scope.SetContext("db_pool", map[string]any{
				"in_use":     stats.InUse,
				"max_open":   maxOpen,
				"reserved":   q.preserveConnections,
				"idle":       stats.Idle,
				"wait_count": stats.WaitCount,
				"wait_delta": waitDelta,
				"usage":      usage,
			})
			sentry.CaptureMessage("DB pool saturated (queuing)")
		})
		q.lastRejectLog = time.Now()
		q.lastRejectWaitCount = stats.WaitCount
	}

	if usage >= q.poolWarnThreshold && time.Since(q.lastWarnLog) > poolLogCooldown {
		log.Warn().
			Int("in_use", stats.InUse).
			Int("max_open", maxOpen).
			Int("reserved", q.preserveConnections).
			Float64("usage", usage).
			Int64("wait_count", stats.WaitCount).
			Msg("DB pool nearing capacity")
		q.lastWarnLog = time.Now()
	}

	return release, nil
}

// Task represents a task in the queue
type Task struct {
	ID          string
	JobID       string
	PageID      int
	Host        string
	Path        string
	Status      string
	CreatedAt   time.Time
	StartedAt   time.Time
	CompletedAt time.Time
	RetryCount  int
	Error       string
	SourceType  string
	SourceURL   string

	// Result data
	StatusCode          int
	ResponseTime        int64
	CacheStatus         string
	ContentType         string
	ContentLength       int64
	Headers             []byte // Stored as JSONB
	RedirectURL         string
	DNSLookupTime       int64
	TCPConnectionTime   int64
	TLSHandshakeTime    int64
	TTFB                int64
	ContentTransferTime int64

	// Second request data
	SecondResponseTime        int64
	SecondCacheStatus         string
	SecondContentLength       int64
	SecondHeaders             []byte // Stored as JSONB
	SecondDNSLookupTime       int64
	SecondTCPConnectionTime   int64
	SecondTLSHandshakeTime    int64
	SecondTTFB                int64
	SecondContentTransferTime int64
	CacheCheckAttempts        []byte // Stored as JSONB
	RequestDiagnostics        []byte // Stored as JSONB
	HTMLStorageBucket         string
	HTMLStoragePath           string
	HTMLContentType           string
	HTMLContentEncoding       string
	HTMLSizeBytes             int64
	HTMLCompressedSizeBytes   int64
	HTMLSHA256                string
	HTMLCapturedAt            time.Time

	// Priority
	PriorityScore float64
}

// TaskHTMLMetadata stores storage metadata for captured task HTML.
type TaskHTMLMetadata struct {
	StorageBucket       string
	StoragePath         string
	ContentType         string
	ContentEncoding     string
	SizeBytes           int64
	CompressedSizeBytes int64
	SHA256              string
	CapturedAt          time.Time
}

// GetNextTask gets a pending task using row-level locking
// Uses FOR UPDATE SKIP LOCKED to prevent lock contention between workers
// Combines SELECT and UPDATE in a CTE for atomic claiming
func (q *DbQueue) GetNextTask(ctx context.Context, jobID string) (*Task, error) {
	var task Task
	now := time.Now().UTC()

	// Track total time including retries
	totalStart := time.Now()
	var attemptCount int

	err := q.Execute(ctx, func(tx *sql.Tx) error {
		attemptCount++
		queryStart := time.Now()

		// Use CTE to select and update in a single atomic query
		// This reduces transaction time and minimises lock holding
		// Also enforces per-job concurrency limits by checking running_tasks < concurrency
		// Only locks the specific job row for the task we claim (not all eligible jobs)
		query := `
			WITH next_task AS (
				-- Claim a task and check job concurrency in one step
				SELECT t.id, t.job_id, t.page_id, t.host, t.path, t.created_at, t.retry_count,
				       t.source_type, t.source_url, t.priority_score
				FROM tasks t
				INNER JOIN jobs j ON t.job_id = j.id
				WHERE t.status = 'pending'
				AND j.status = 'running'
				-- Support legacy jobs with NULL or 0 concurrency (unlimited)
				AND (j.concurrency IS NULL OR j.concurrency = 0 OR j.running_tasks < j.concurrency)
				-- Quota enforcement: don't claim if org has exceeded daily quota (completed pages only)
				AND (j.organisation_id IS NULL OR NOT is_org_over_daily_quota(j.organisation_id))
		`

		// Add job filter if specified
		args := []any{now}
		if jobID != "" {
			query += " AND t.job_id = $2"
			args = append(args, jobID)
		}

		query += `
				ORDER BY t.priority_score DESC, t.created_at ASC
				LIMIT 1
				FOR UPDATE OF t SKIP LOCKED
			),
			job_update AS (
				UPDATE jobs j
				SET running_tasks = running_tasks + 1
				FROM next_task nt
				WHERE j.id = nt.job_id
				  AND (j.concurrency IS NULL OR j.concurrency = 0 OR j.running_tasks < j.concurrency)
				RETURNING j.id, j.running_tasks, j.concurrency
			),
			task_update AS (
				UPDATE tasks
				SET status = 'running', started_at = $1
				FROM next_task nt
				JOIN job_update ju ON ju.id = nt.job_id
				WHERE tasks.id = nt.id
				RETURNING tasks.id, tasks.job_id, tasks.page_id, tasks.host, tasks.path,
				          tasks.created_at, tasks.retry_count, tasks.source_type,
				          tasks.source_url, tasks.priority_score,
				          ju.running_tasks, ju.concurrency
			)
			SELECT id, job_id, page_id, host, path, created_at, retry_count, source_type, source_url, priority_score,
			       running_tasks, concurrency
			FROM task_update
		`

		// Execute the combined query
		row := tx.QueryRowContext(ctx, query, args...)

		var jobRunningTasks sql.NullInt64
		var jobConcurrency sql.NullInt64
		err := row.Scan(
			&task.ID, &task.JobID, &task.PageID, &task.Host, &task.Path,
			&task.CreatedAt, &task.RetryCount, &task.SourceType, &task.SourceURL,
			&task.PriorityScore, &jobRunningTasks, &jobConcurrency,
		)
		elapsed := time.Since(queryStart)

		if err == sql.ErrNoRows {
			// Use sentinel value for metrics when jobID is empty (all-jobs query)
			metricsJobID := jobID
			if metricsJobID == "" {
				metricsJobID = "__all__"
			}

			if jobID == "" {
				hasBlocked := q.hasAnyConcurrencyBlockedTasks(ctx, tx)
				if hasBlocked {
					log.Debug().
						Str("job_id", jobID).
						Dur("query_duration", elapsed).
						Int("attempt", attemptCount).
						Msg("Tasks available but blocked by job concurrency limit")
					observability.RecordTaskClaimAttempt(ctx, metricsJobID, elapsed, "concurrency_blocked")
					return ErrConcurrencyBlocked
				}
			} else if q.jobHasConcurrencyBlockedTasks(ctx, tx, jobID) {
				log.Debug().
					Str("job_id", jobID).
					Dur("query_duration", elapsed).
					Int("attempt", attemptCount).
					Msg("Tasks available but blocked by job concurrency limit")
				observability.RecordTaskClaimAttempt(ctx, metricsJobID, elapsed, "concurrency_blocked")
				return ErrConcurrencyBlocked
			}

			log.Debug().
				Str("job_id", jobID).
				Dur("query_duration", elapsed).
				Int("attempt", attemptCount).
				Msg("No pending task available for job")
			observability.RecordTaskClaimAttempt(ctx, metricsJobID, elapsed, "empty")
			return sql.ErrNoRows
		}
		if err != nil {
			// Use sentinel value for metrics when jobID is empty (all-jobs query)
			metricsJobID := jobID
			if metricsJobID == "" {
				metricsJobID = "__all__"
			}
			log.Error().
				Err(err).
				Str("job_id", jobID).
				Dur("query_duration", elapsed).
				Int("attempt", attemptCount).
				Msg("Failed to claim next task")
			observability.RecordTaskClaimAttempt(ctx, metricsJobID, elapsed, "error")
			return fmt.Errorf("failed to claim task: %w", err)
		}

		task.Status = "running"
		task.StartedAt = now

		runningValue := int64(0)
		if jobRunningTasks.Valid {
			runningValue = jobRunningTasks.Int64
		}

		concurrencyValue := int64(0)
		unlimited := true
		if jobConcurrency.Valid && jobConcurrency.Int64 > 0 {
			concurrencyValue = jobConcurrency.Int64
			unlimited = false
		}

		log.Debug().
			Str("job_id", task.JobID).
			Str("task_id", task.ID).
			Dur("query_duration", elapsed).
			Int("attempt", attemptCount).
			Int64("job_running_tasks", runningValue).
			Int64("job_concurrency_limit", concurrencyValue).
			Bool("job_concurrency_unlimited", unlimited).
			Msg("Claimed next task")

		observability.RecordTaskClaimAttempt(ctx, task.JobID, elapsed, "claimed")
		observability.RecordJobConcurrencySnapshot(ctx, task.JobID, runningValue, concurrencyValue, unlimited)

		return nil
	})

	totalElapsed := time.Since(totalStart)

	// Log summary if there were retries or if total time was significantly longer than expected
	if attemptCount > 1 || totalElapsed > 5*time.Second {
		logEvent := log.Info()
		if totalElapsed > 30*time.Second {
			logEvent = log.Warn()
		}
		logEvent.
			Str("job_id", jobID).
			Dur("total_duration", totalElapsed).
			Int("attempts", attemptCount).
			Msg("Task claim completed after retries or slow execution")
	}

	if err == sql.ErrNoRows {
		return nil, nil // No tasks available
	}
	if errors.Is(err, ErrConcurrencyBlocked) {
		// Tasks exist but blocked by concurrency - return sentinel for backoff
		return nil, err
	}
	if err != nil {
		// Log error with total context
		log.Error().
			Err(err).
			Str("job_id", jobID).
			Dur("total_duration", totalElapsed).
			Int("attempts", attemptCount).
			Msg("Error getting next pending task")
		return nil, err
	}

	return &task, nil
}

// enqueueJobConfig holds configuration fetched from the database for task enqueueing
type enqueueJobConfig struct {
	maxPages         int
	concurrency      sql.NullInt64
	runningTasks     int
	pendingTaskCount int
	domainID         sql.NullInt64
	domainName       sql.NullString
	orgID            sql.NullString
	quotaRemaining   sql.NullInt64
	currentTaskCount int
}

// deduplicatePages removes duplicate pages, keeping highest priority for each page ID
func deduplicatePages(pages []Page) []Page {
	uniquePages := make([]Page, 0, len(pages))
	seen := make(map[int]int, len(pages))
	for _, page := range pages {
		if page.ID == 0 {
			continue
		}
		if idx, ok := seen[page.ID]; ok {
			if page.Priority > uniquePages[idx].Priority {
				uniquePages[idx].Priority = page.Priority
			}
			continue
		}
		seen[page.ID] = len(uniquePages)
		uniquePages = append(uniquePages, page)
	}
	return uniquePages
}

// calculateEffectiveConcurrency applies domain limiter override if applicable
func (q *DbQueue) calculateEffectiveConcurrency(
	jobID string,
	concurrency sql.NullInt64,
	domainName sql.NullString,
) sql.NullInt64 {
	if q.concurrencyOverride == nil || !domainName.Valid {
		return concurrency
	}
	override := q.concurrencyOverride(jobID, domainName.String)
	if override <= 0 {
		return concurrency
	}
	// Use the minimum of configured concurrency and limiter override
	if !concurrency.Valid || concurrency.Int64 == 0 {
		return sql.NullInt64{Int64: int64(override), Valid: true}
	}
	if int64(override) < concurrency.Int64 {
		return sql.NullInt64{Int64: int64(override), Valid: true}
	}
	return concurrency
}

// calculateAvailableSlots determines how many pending tasks can be created
// Returns availableSlots and whether quota was the limiting factor
func calculateAvailableSlots(
	effectiveConcurrency sql.NullInt64,
	runningTasks, pendingTaskCount int,
	quotaRemaining sql.NullInt64,
) (slots int, quotaLimited bool) {
	const maxPendingQueueSize = 100

	if !effectiveConcurrency.Valid || effectiveConcurrency.Int64 == 0 {
		// Even for unlimited concurrency jobs, cap pending queue to prevent flooding
		used := runningTasks + pendingTaskCount
		slots = max(maxPendingQueueSize-used, 0)
	} else {
		// Capacity = concurrency - (running + existing_pending)
		capacity := int(effectiveConcurrency.Int64)
		used := runningTasks + pendingTaskCount
		slots = max(capacity-used, 0)
	}

	// Cap available slots by daily quota remaining (if org has quota system)
	if quotaRemaining.Valid {
		remaining := int(quotaRemaining.Int64)
		if remaining <= 0 {
			// Quota exhausted or exceeded - no new pending tasks
			slots = 0
			quotaLimited = true
		} else if remaining < slots {
			slots = remaining
			quotaLimited = true
		}
	}

	return slots, quotaLimited
}

// EnqueueURLs adds multiple URLs as tasks for a job
func (q *DbQueue) EnqueueURLs(ctx context.Context, jobID string, pages []Page, sourceType string, sourceURL string) error {
	if len(pages) == 0 {
		return nil
	}

	return q.Execute(ctx, func(tx *sql.Tx) error {
		uniquePages := deduplicatePages(pages)
		if len(uniquePages) == 0 {
			return nil
		}

		// Get job's max_pages, concurrency, domain, org, and current task counts
		var cfg enqueueJobConfig
		err := tx.QueryRowContext(ctx, `
			SELECT j.max_pages, j.concurrency, j.running_tasks, j.pending_tasks, j.domain_id, d.name,
				   COALESCE((SELECT COUNT(*) FROM tasks WHERE job_id = $1 AND status != 'skipped'), 0),
				   j.organisation_id,
				   CASE WHEN j.organisation_id IS NOT NULL
				        THEN get_daily_quota_remaining(j.organisation_id)
				        ELSE NULL
				   END
			FROM jobs j
			LEFT JOIN domains d ON j.domain_id = d.id
			WHERE j.id = $1
			FOR UPDATE OF j
		`, jobID).Scan(&cfg.maxPages, &cfg.concurrency, &cfg.runningTasks, &cfg.pendingTaskCount,
			&cfg.domainID, &cfg.domainName, &cfg.currentTaskCount, &cfg.orgID, &cfg.quotaRemaining)
		if err != nil {
			return fmt.Errorf("failed to get job configuration and task count: %w", err)
		}

		// Calculate available slots with concurrency override and quota limits
		effectiveConcurrency := q.calculateEffectiveConcurrency(jobID, cfg.concurrency, cfg.domainName)
		concurrencySlots, _ := calculateAvailableSlots(effectiveConcurrency, cfg.runningTasks, cfg.pendingTaskCount, sql.NullInt64{})
		availableSlots, quotaLimited := calculateAvailableSlots(effectiveConcurrency, cfg.runningTasks, cfg.pendingTaskCount, cfg.quotaRemaining)

		// Ensure we don't try to create more tasks than we have pages
		if availableSlots > len(uniquePages) {
			availableSlots = len(uniquePages)
		}

		// Count how many tasks will be pending/waiting vs skipped
		pendingCount := 0
		waitingCount := 0
		skippedCount := 0
		for range uniquePages {
			if cfg.maxPages == 0 || cfg.currentTaskCount+pendingCount+waitingCount < cfg.maxPages {
				if pendingCount < availableSlots {
					pendingCount++
				} else {
					waitingCount++
				}
			} else {
				skippedCount++
			}
		}

		// Use array-based insert to minimise round-trips and leverage Postgres batching
		insertQuery := `
			INSERT INTO tasks (
				id, job_id, page_id, host, path, status, created_at, retry_count,
				source_type, source_url, priority_score
			)
			SELECT
				unnest_ids,
				unnest_job_ids,
				unnest_page_ids,
				unnest_hosts,
				unnest_paths,
				unnest_statuses,
				unnest_created_at,
				unnest_retry_counts,
				unnest_source_types,
				unnest_source_urls,
				unnest_priorities
			FROM UNNEST(
				$1::uuid[],
				$2::uuid[],
				$3::int[],
				$4::text[],
				$5::text[],
				$6::text[],
				$7::timestamptz[],
				$8::int[],
				$9::text[],
				$10::text[],
				$11::double precision[]
			) AS t(
				unnest_ids,
				unnest_job_ids,
				unnest_page_ids,
				unnest_hosts,
				unnest_paths,
				unnest_statuses,
				unnest_created_at,
				unnest_retry_counts,
				unnest_source_types,
				unnest_source_urls,
				unnest_priorities
			)
			ON CONFLICT (job_id, page_id) DO UPDATE
			SET status = EXCLUDED.status,
				host = EXCLUDED.host,
				created_at = EXCLUDED.created_at,
				retry_count = EXCLUDED.retry_count,
				source_type = EXCLUDED.source_type,
				source_url = EXCLUDED.source_url,
				priority_score = GREATEST(tasks.priority_score, EXCLUDED.priority_score),
				started_at = NULL,
				completed_at = NULL,
				error = NULL
			WHERE tasks.status IN ('pending', 'waiting', 'skipped')
		`

		now := time.Now().UTC()
		processedPending := 0
		processedWaiting := 0

		var (
			taskIDs     []string
			jobIDs      []string
			pageIDs     []int
			hosts       []string
			paths       []string
			statuses    []string
			createdAts  []time.Time
			retryCounts []int
			sourceTypes []string
			sourceURLs  []string
			priorities  []float64
		)

		for _, page := range uniquePages {
			if page.ID == 0 {
				continue
			}

			host := page.Host
			if host == "" && cfg.domainName.Valid {
				host = cfg.domainName.String
			}
			if host == "" {
				log.Warn().Str("job_id", jobID).Int("page_id", page.ID).Msg("Skipping page enqueue due to missing host")
				continue
			}

			var status string
			if cfg.maxPages == 0 || cfg.currentTaskCount+processedPending+processedWaiting < cfg.maxPages {
				if processedPending < availableSlots {
					status = "pending"
					processedPending++
				} else {
					status = "waiting"
					processedWaiting++
				}
			} else {
				status = "skipped"
			}

			taskIDs = append(taskIDs, uuid.New().String())
			jobIDs = append(jobIDs, jobID)
			pageIDs = append(pageIDs, page.ID)
			hosts = append(hosts, host)
			paths = append(paths, page.Path)
			statuses = append(statuses, status)
			createdAts = append(createdAts, now)
			retryCounts = append(retryCounts, 0)
			sourceTypes = append(sourceTypes, sourceType)
			// Only store source_url for 'link' source type (pages discovered from other pages)
			if sourceType == "link" {
				sourceURLs = append(sourceURLs, sourceURL)
			} else {
				sourceURLs = append(sourceURLs, "")
			}
			priorities = append(priorities, page.Priority)
		}

		if len(taskIDs) == 0 {
			return nil
		}

		_, err = tx.ExecContext(ctx, insertQuery,
			pq.Array(taskIDs),
			pq.Array(jobIDs),
			pq.Array(pageIDs),
			pq.Array(hosts),
			pq.Array(paths),
			pq.Array(statuses),
			pq.Array(createdAts),
			pq.Array(retryCounts),
			pq.Array(sourceTypes),
			pq.Array(sourceURLs),
			pq.Array(priorities),
		)

		if err != nil {
			return fmt.Errorf("failed to insert tasks: %w", err)
		}

		// Apply traffic scores from page_analytics using GREATEST
		// This ensures high-traffic pages get prioritised even if structural priority is low
		if cfg.orgID.Valid && cfg.domainID.Valid {
			_, err = tx.ExecContext(ctx, `
				UPDATE tasks t
				SET priority_score = GREATEST(t.priority_score, COALESCE(pa.traffic_score, 0))
				FROM pages p
				JOIN page_analytics pa ON pa.organisation_id = $1
					AND pa.domain_id = $2
					AND pa.path = p.path
				WHERE t.page_id = p.id
				AND t.job_id = $3
				AND t.status IN ('pending', 'waiting')
				AND COALESCE(pa.traffic_score, 0) > t.priority_score
			`, cfg.orgID.String, cfg.domainID.Int64, jobID)
			if err != nil {
				// Log but don't fail - traffic scores are an optimisation
				log.Warn().
					Err(err).
					Str("job_id", jobID).
					Str("next_action", "continuing_without_traffic_scores").
					Msg("Failed to apply traffic scores to new tasks; continuing without scores")
			}
		}

		// Note: Daily usage is incremented when tasks COMPLETE, not when created
		// See batch.go FlushUpdates for quota increment on completion/failure

		// Log when tasks are placed in waiting status
		if processedWaiting > 0 {
			waitingReason := waitingReasonConcurrencyLimit
			if quotaLimited {
				waitingReason = waitingReasonQuotaExhausted
			}
			observability.RecordTaskWaiting(ctx, jobID, waitingReason, processedWaiting)

			logEvent := log.Debug().
				Str("job_id", jobID).
				Int("waiting_tasks", processedWaiting).
				Int("pending_tasks", processedPending).
				Int("running_tasks", cfg.runningTasks).
				Int("existing_pending", cfg.pendingTaskCount).
				Int("available_slots", availableSlots).
				Int64("concurrency_limit", cfg.concurrency.Int64).
				Str("waiting_reason", waitingReason)

			if quotaLimited {
				logEvent.Int64("quota_remaining", cfg.quotaRemaining.Int64).
					Int("concurrency_slots", concurrencySlots).
					Msg("Created tasks in waiting status due to daily quota limit")
			} else {
				logEvent.Msg("Created tasks in waiting status due to job concurrency limit")
			}
		}

		return nil
	})
}

// CleanupStuckJobs finds and fixes jobs that are stuck in pending/running state
// despite having all their tasks completed
func (q *DbQueue) CleanupStuckJobs(ctx context.Context) error {
	// Serialize cleanup operations to prevent prepared statement conflicts
	q.cleanupMutex.Lock()
	defer q.cleanupMutex.Unlock()

	span := sentry.StartSpan(ctx, "db.cleanup_stuck_jobs")
	defer span.Finish()

	// Define status constants for job states
	const (
		JobStatusCompleted = "completed"
		JobStatusPending   = "pending"
		JobStatusRunning   = "running"
	)

	result, err := q.db.client.ExecContext(ctx, `
		UPDATE jobs
		SET status = $1,
			completed_at = COALESCE(completed_at, $2),
			progress = 100.0
		WHERE (status = $3 OR status = $4)
		AND total_tasks > 0
		AND total_tasks = completed_tasks + failed_tasks + skipped_tasks
	`, JobStatusCompleted, time.Now().UTC(), JobStatusPending, JobStatusRunning)

	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		sentry.CaptureException(err)
		return fmt.Errorf("failed to cleanup stuck jobs: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		log.Info().
			Int64("jobs_fixed", rowsAffected).
			Msg("Fixed stuck jobs")
	}

	return nil
}

// UpdateTaskStatus updates a task's status and associated metadata in a single function
// This provides a unified way to handle various task state transitions
func (q *DbQueue) UpdateTaskStatus(ctx context.Context, task *Task) error {
	if task == nil {
		return fmt.Errorf("cannot update nil task")
	}

	now := time.Now().UTC()

	// Set appropriate timestamps based on status if not already set
	if task.Status == "running" && task.StartedAt.IsZero() {
		task.StartedAt = now
	}
	if (task.Status == "completed" || task.Status == "failed") && task.CompletedAt.IsZero() {
		task.CompletedAt = now
	}

	// Update task in a transaction
	// Also adjust running_tasks counter for the job when status changes
	err := q.Execute(ctx, func(tx *sql.Tx) error {
		var err error
		var jobID string

		// Use different update logic based on status
		switch task.Status {
		case "running":
			// Increment running_tasks when manually setting to running (rare, but handle it)
			err = tx.QueryRowContext(ctx, `
				WITH task_update AS (
					UPDATE tasks
					SET status = $1, started_at = $2
					WHERE id = $3
					RETURNING job_id
				)
				UPDATE jobs
				SET running_tasks = running_tasks + 1
				FROM task_update
				WHERE jobs.id = task_update.job_id
				RETURNING task_update.job_id
			`, task.Status, task.StartedAt, task.ID).Scan(&jobID)

		case "completed":
			// Ensure JSONB fields are never nil and are valid JSON
			headers := task.Headers
			if len(headers) == 0 {
				headers = []byte("{}")
			}
			secondHeaders := task.SecondHeaders
			if len(secondHeaders) == 0 {
				secondHeaders = []byte("{}")
			}
			cacheCheckAttempts := task.CacheCheckAttempts
			if len(cacheCheckAttempts) == 0 {
				cacheCheckAttempts = []byte("[]")
			}
			requestDiagnostics := task.RequestDiagnostics
			if len(requestDiagnostics) == 0 {
				requestDiagnostics = []byte("{}")
			}

			var htmlCapturedAt any
			if !task.HTMLCapturedAt.IsZero() {
				htmlCapturedAt = task.HTMLCapturedAt
			}

			// Log the actual values being passed for debugging
			log.Debug().
				Str("task_id", task.ID).
				Int("headers_bytes", len(headers)).
				Int("second_headers_bytes", len(secondHeaders)).
				Int("cache_check_attempts_bytes", len(cacheCheckAttempts)).
				Int("request_diagnostics_bytes", len(requestDiagnostics)).
				Msg("Updating task with JSONB fields")

			// Update task fields only (running_tasks decremented separately via DecrementRunningTasks)
			err = tx.QueryRowContext(ctx, `
					UPDATE tasks
					SET status = $1, completed_at = $2, status_code = $3,
						response_time = $4, cache_status = $5, content_type = $6,
						content_length = $7, headers = $8::jsonb, redirect_url = $9,
						dns_lookup_time = $10, tcp_connection_time = $11, tls_handshake_time = $12,
						ttfb = $13, content_transfer_time = $14,
						second_response_time = $15, second_cache_status = $16,
						second_content_length = $17, second_headers = $18::jsonb,
						second_dns_lookup_time = $19, second_tcp_connection_time = $20,
						second_tls_handshake_time = $21, second_ttfb = $22,
						second_content_transfer_time = $23,
						retry_count = $24, cache_check_attempts = $25::jsonb,
						request_diagnostics = $26::jsonb,
						html_storage_bucket = COALESCE(NULLIF($27, ''), html_storage_bucket),
						html_storage_path = COALESCE(NULLIF($28, ''), html_storage_path),
						html_content_type = COALESCE(NULLIF($29, ''), html_content_type),
						html_content_encoding = COALESCE(NULLIF($30, ''), html_content_encoding),
						html_size_bytes = CASE WHEN $31 > 0 THEN $31 ELSE html_size_bytes END,
						html_compressed_size_bytes = CASE WHEN $32 > 0 THEN $32 ELSE html_compressed_size_bytes END,
						html_sha256 = COALESCE(NULLIF($33, ''), html_sha256),
						html_captured_at = COALESCE($34, html_captured_at)
					WHERE id = $35
					RETURNING job_id
				`, task.Status, task.CompletedAt, task.StatusCode,
				task.ResponseTime, task.CacheStatus, task.ContentType,
				task.ContentLength, string(headers), task.RedirectURL,
				task.DNSLookupTime, task.TCPConnectionTime, task.TLSHandshakeTime,
				task.TTFB, task.ContentTransferTime,
				task.SecondResponseTime, task.SecondCacheStatus,
				task.SecondContentLength, string(secondHeaders),
				task.SecondDNSLookupTime, task.SecondTCPConnectionTime,
				task.SecondTLSHandshakeTime, task.SecondTTFB,
				task.SecondContentTransferTime,
				task.RetryCount, string(cacheCheckAttempts), string(requestDiagnostics),
				task.HTMLStorageBucket, task.HTMLStoragePath, task.HTMLContentType,
				task.HTMLContentEncoding, task.HTMLSizeBytes, task.HTMLCompressedSizeBytes,
				task.HTMLSHA256, htmlCapturedAt, task.ID).Scan(&jobID)

		case "failed":
			requestDiagnostics := task.RequestDiagnostics
			if len(requestDiagnostics) == 0 {
				requestDiagnostics = []byte("{}")
			}

			// Update task fields only (running_tasks decremented separately via DecrementRunningTasks)
			err = tx.QueryRowContext(ctx, `
				UPDATE tasks
				SET status = $1, completed_at = $2, error = $3, retry_count = $4,
					request_diagnostics = $5::jsonb
				WHERE id = $6
				RETURNING job_id
			`, task.Status, task.CompletedAt, task.Error, task.RetryCount, string(requestDiagnostics), task.ID).Scan(&jobID)

		case "waiting":
			requestDiagnostics := task.RequestDiagnostics
			if len(requestDiagnostics) == 0 {
				requestDiagnostics = []byte("{}")
			}

			err = tx.QueryRowContext(ctx, `
				UPDATE tasks
				SET status = $1, started_at = NULL, error = $2, retry_count = $3,
					request_diagnostics = $4::jsonb
				WHERE id = $5
				RETURNING job_id
			`, task.Status, task.Error, task.RetryCount, string(requestDiagnostics), task.ID).Scan(&jobID)

		case "skipped":
			// Update task fields only (running_tasks decremented separately via DecrementRunningTasks)
			err = tx.QueryRowContext(ctx, `
				UPDATE tasks
				SET status = $1
				WHERE id = $2
				RETURNING job_id
			`, task.Status, task.ID).Scan(&jobID)

		case "pending":
			// Update task fields only (running_tasks decremented separately via DecrementRunningTasks)
			// The task will re-increment when claimed again
			err = tx.QueryRowContext(ctx, `
				UPDATE tasks
				SET status = $1, retry_count = $2, started_at = $3
				WHERE id = $4
				RETURNING job_id
			`, task.Status, task.RetryCount, task.StartedAt, task.ID).Scan(&jobID)

		default:
			// Generic status update
			_, err = tx.ExecContext(ctx, `
				UPDATE tasks
				SET status = $1
				WHERE id = $2
			`, task.Status, task.ID)
		}

		if err != nil {
			return fmt.Errorf("failed to update task status: %w", err)
		}

		return nil
	})

	if err != nil {
		return err
	}

	return nil
}

// UpdateTaskHTMLMetadata persists storage metadata for an already completed task.
func (q *DbQueue) UpdateTaskHTMLMetadata(ctx context.Context, taskID string, metadata TaskHTMLMetadata) error {
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("taskID cannot be empty")
	}
	if strings.TrimSpace(metadata.StorageBucket) == "" {
		return fmt.Errorf("storage bucket cannot be empty")
	}
	if strings.TrimSpace(metadata.StoragePath) == "" {
		return fmt.Errorf("storage path cannot be empty")
	}

	var capturedAt any
	if !metadata.CapturedAt.IsZero() {
		capturedAt = metadata.CapturedAt
	}

	return q.Execute(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE tasks
			SET html_storage_bucket = COALESCE(NULLIF($2, ''), html_storage_bucket),
				html_storage_path = COALESCE(NULLIF($3, ''), html_storage_path),
				html_content_type = COALESCE(NULLIF($4, ''), html_content_type),
				html_content_encoding = COALESCE(NULLIF($5, ''), html_content_encoding),
				html_size_bytes = CASE WHEN $6 > 0 THEN $6 ELSE html_size_bytes END,
				html_compressed_size_bytes = CASE WHEN $7 > 0 THEN $7 ELSE html_compressed_size_bytes END,
				html_sha256 = COALESCE(NULLIF($8, ''), html_sha256),
				html_captured_at = COALESCE($9, html_captured_at)
			WHERE id = $1 AND status IN ('running', 'completed')
		`, taskID, metadata.StorageBucket, metadata.StoragePath, metadata.ContentType,
			metadata.ContentEncoding, metadata.SizeBytes, metadata.CompressedSizeBytes,
			metadata.SHA256, capturedAt)
		if err != nil {
			return fmt.Errorf("failed to update task HTML metadata: %w", err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to read task HTML metadata update result: %w", err)
		}
		if rowsAffected == 0 {
			var status string
			err = tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = $1`, taskID).Scan(&status)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return fmt.Errorf("task %s not found: %w", taskID, sql.ErrNoRows)
			case err != nil:
				return fmt.Errorf("failed to inspect task HTML metadata state: %w", err)
			case status == "pending" || status == "waiting":
				return fmt.Errorf("%w: %s", ErrTaskNotReadyForHTMLMetadata, taskID)
			default:
				return fmt.Errorf("task %s is not eligible for HTML metadata updates in status %q", taskID, status)
			}
		}
		return nil
	})
}

// DecrementRunningTasks immediately decrements the running_tasks counter for a job.
// This is called when a task completes to free up concurrency slots without waiting for batch flush.
// Also promotes one waiting task to pending if job still has capacity.
// The actual task field updates are still handled by the batch manager for efficiency.
func (q *DbQueue) DecrementRunningTasks(ctx context.Context, jobID string) error {
	return q.DecrementRunningTasksBy(ctx, jobID, 1)
}

// DecrementRunningTasksBy releases multiple running task slots for a job in one trip.
func (q *DbQueue) DecrementRunningTasksBy(ctx context.Context, jobID string, count int) error {
	if jobID == "" {
		return fmt.Errorf("jobID cannot be empty")
	}
	if count <= 0 {
		return nil
	}

	log.Debug().
		Str("job_id", jobID).
		Int("release_count", count).
		Msg("DecrementRunningTasksBy called")

	var freedCapacity bool

	err := q.ExecuteWithContext(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		// Decrement running_tasks count in a single atomic update
		decrementQuery := `
			UPDATE jobs
			SET running_tasks = GREATEST(0, running_tasks - $2)
			WHERE id = $1 AND running_tasks > 0
		`
		result, err := tx.ExecContext(txCtx, decrementQuery, jobID, count)
		if err != nil {
			log.Error().Err(err).Str("job_id", jobID).Msg("DecrementRunningTasksBy database error")
			return fmt.Errorf("failed to decrement running_tasks for job %s: %w", jobID, err)
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			log.Error().Err(err).Str("job_id", jobID).Msg("DecrementRunningTasksBy failed to get rows affected")
		} else {
			log.Debug().
				Str("job_id", jobID).
				Int64("rows_affected", rowsAffected).
				Int("requested_release", count).
				Msg("DecrementRunningTasksBy executed")
		}

		freedCapacity = rowsAffected > 0

		return nil
	})

	if err != nil {
		return err
	}

	if !freedCapacity {
		// No slots freed (job already at 0), nothing to promote
		return nil
	}

	// Run promotion outside the job update transaction to avoid holding both job and
	// task locks concurrently, which was leading to deadlocks under load.
	promoteCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := q.promoteWaitingTask(promoteCtx, jobID); err != nil {
		// Best-effort: log but don't fail – the caller already freed slots.
		log.Warn().Err(err).Str("job_id", jobID).Msg("Failed to promote waiting task after slot release")
	}

	return nil
}

// promoteWaitingTask best-effort promotes a single waiting task for the given job.
func (q *DbQueue) promoteWaitingTask(ctx context.Context, jobID string) error {
	return q.ExecuteWithContext(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(txCtx, `SELECT promote_waiting_task_for_job($1)`, jobID)
		return err
	})
}
func (q *DbQueue) hasAnyConcurrencyBlockedTasks(ctx context.Context, tx *sql.Tx) bool {
	query := `
		SELECT EXISTS (
			SELECT 1
			FROM jobs
			WHERE status = 'running'
			  AND concurrency IS NOT NULL
			  AND concurrency > 0
			  AND running_tasks >= concurrency
			  AND pending_tasks > 0
		)
	`
	var exists bool
	if err := tx.QueryRowContext(ctx, query).Scan(&exists); err != nil {
		log.Warn().Err(err).Msg("hasAnyConcurrencyBlockedTasks fallback query failed")
		return false
	}
	return exists
}

func (q *DbQueue) jobHasConcurrencyBlockedTasks(ctx context.Context, tx *sql.Tx, jobID string) bool {
	if jobID == "" {
		return false
	}
	query := `
		SELECT EXISTS (
			SELECT 1
			FROM jobs
			WHERE id = $1
			  AND status = 'running'
			  AND concurrency IS NOT NULL
			  AND concurrency > 0
			  AND running_tasks >= concurrency
			  AND pending_tasks > 0
		)
	`
	var exists bool
	if err := tx.QueryRowContext(ctx, query, jobID).Scan(&exists); err != nil {
		log.Warn().Err(err).Str("job_id", jobID).Msg("jobHasConcurrencyBlockedTasks query failed")
		return false
	}
	return exists
}

// UpdateDomainTechnologies updates the detected technologies for a domain.
// Delegates to the underlying DB implementation.
func (q *DbQueue) UpdateDomainTechnologies(ctx context.Context, domainID int, technologies, headers []byte, htmlPath string) error {
	if q == nil || q.db == nil {
		return fmt.Errorf("queue not initialised")
	}
	return q.db.UpdateDomainTechnologies(ctx, domainID, technologies, headers, htmlPath)
}
