package jobs

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Harvey-AU/hover/internal/archive"
	"github.com/Harvey-AU/hover/internal/crawler"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/Harvey-AU/hover/internal/util"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// RetryDecision describes what should happen after a task error.
// The caller (stream worker) uses this to decide whether to
// reschedule via Redis or mark the task as permanently failed.
type RetryDecision struct {
	ShouldRetry        bool
	NextRunAt          time.Time
	Reason             string // "blocking", "retryable", "domain_delay"
	IsPermanentFailure bool
}

// TaskOutcome is the result of executing a single task.
type TaskOutcome struct {
	// Task is the populated db.Task with status, metrics, and JSONB
	// fields set. Ready to be passed to the batch manager.
	Task *db.Task

	// CrawlResult is the raw crawler output (may be nil on error).
	CrawlResult *crawler.CrawlResult

	// Retry is set when the task should be rescheduled instead of
	// permanently completed/failed.
	Retry *RetryDecision

	// DiscoveredLinks, if non-empty, should be persisted and
	// scheduled into the ZSET.
	DiscoveredLinks map[string][]string

	// HTMLUpload, if non-nil, should be uploaded to storage.
	HTMLUpload *TaskHTMLUpload

	// RateLimited is true when the crawl received a 429/403/503,
	// used by the caller to update domain pacer state.
	RateLimited bool

	// Success is true when the crawl completed without error.
	Success bool
}

// TaskHTMLUpload holds the data needed to upload HTML to storage.
type TaskHTMLUpload struct {
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

// ExecutorConfig holds configuration for the task executor.
type ExecutorConfig struct {
	MaxBlockingRetries int
	MaxTaskRetries     int
	BaseDelayMS        int
	MaxDelayMS         int
}

// DefaultExecutorConfig returns production defaults.
func DefaultExecutorConfig() ExecutorConfig {
	return ExecutorConfig{
		MaxBlockingRetries: 3,
		MaxTaskRetries:     MaxTaskRetries,
		BaseDelayMS:        50,
		MaxDelayMS:         60000,
	}
}

// TaskExecutor runs crawl tasks and produces outcomes without
// side effects on counters, schedulers, or persistence. The caller
// is responsible for acting on the returned TaskOutcome.
type TaskExecutor struct {
	crawler CrawlerInterface
	cfg     ExecutorConfig
}

// NewTaskExecutor creates a TaskExecutor.
func NewTaskExecutor(c CrawlerInterface, cfg ExecutorConfig) *TaskExecutor {
	return &TaskExecutor{
		crawler: c,
		cfg:     cfg,
	}
}

// Execute runs a crawl for the given task and returns the outcome.
// It does NOT modify any external state — all decisions are returned
// in the TaskOutcome for the caller to act on.
//
// The task parameter is a jobs.Task (enriched with job info like
// DomainName, FindLinks, CrawlDelay). The returned TaskOutcome
// contains a *db.Task populated for batch persistence.
func (e *TaskExecutor) Execute(ctx context.Context, task *Task) *TaskOutcome {
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

	urlStr := ConstructTaskURL(task.Path, task.Host, task.DomainName)

	jobsLog.Debug("Starting URL warm", "url", urlStr, "task_id", task.ID)

	result, err := e.crawler.WarmURL(ctx, urlStr, task.FindLinks)
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

		jobsLog.Debug("Crawler failed", "error", err, "task_id", task.ID, "rate_limited", rateLimited, "status_code", statusCodeOrZero(result))

		return e.buildErrorOutcome(ctx, task, result, err, rateLimited)
	}

	// Guard against nil result — crawler should always return a result
	// on success, but defensive check prevents downstream panics.
	if result == nil {
		status = "error"
		nilErr := fmt.Errorf("crawler returned nil result for %s", urlStr)
		span.RecordError(nilErr)
		span.SetStatus(codes.Error, nilErr.Error())
		return e.buildErrorOutcome(ctx, task, nil, nilErr, false)
	}

	span.SetAttributes(
		attribute.Int("http.status_code", result.StatusCode),
		attribute.Int("task.links_found", len(result.Links)),
		attribute.String("task.content_type", result.ContentType),
	)
	span.SetStatus(codes.Ok, "completed")

	jobsLog.Debug("Crawler completed", "status_code", result.StatusCode, "task_id", task.ID, "links_found", len(result.Links), "content_type", result.ContentType)

	return e.buildSuccessOutcome(task, result)
}

// --- outcome builders ---

// taskToDBTask converts a jobs.Task to a db.Task for persistence.
func taskToDBTask(t *Task) *db.Task {
	return &db.Task{
		ID:            t.ID,
		JobID:         t.JobID,
		PageID:        t.PageID,
		Host:          t.Host,
		Path:          t.Path,
		Status:        string(t.Status),
		CreatedAt:     t.CreatedAt,
		StartedAt:     t.StartedAt,
		RetryCount:    t.RetryCount,
		Error:         t.Error,
		SourceType:    t.SourceType,
		SourceURL:     t.SourceURL,
		PriorityScore: t.PriorityScore,
	}
}

func (e *TaskExecutor) buildSuccessOutcome(task *Task, result *crawler.CrawlResult) *TaskOutcome {
	now := time.Now().UTC()

	dbTask := taskToDBTask(task)
	dbTask.Status = string(TaskStatusCompleted)
	dbTask.CompletedAt = now
	dbTask.StatusCode = result.StatusCode
	dbTask.ResponseTime = result.ResponseTime
	dbTask.CacheStatus = result.CacheStatus
	dbTask.ContentType = result.ContentType
	dbTask.ContentLength = result.ContentLength

	if util.IsSignificantRedirect(result.URL, result.RedirectURL) {
		dbTask.RedirectURL = result.RedirectURL
	}

	// Performance metrics.
	dbTask.DNSLookupTime = result.Performance.DNSLookupTime
	dbTask.TCPConnectionTime = result.Performance.TCPConnectionTime
	dbTask.TLSHandshakeTime = result.Performance.TLSHandshakeTime
	dbTask.TTFB = result.Performance.TTFB
	dbTask.ContentTransferTime = result.Performance.ContentTransferTime

	// Second request metrics.
	dbTask.SecondResponseTime = result.SecondResponseTime
	dbTask.SecondCacheStatus = result.SecondCacheStatus
	if result.SecondPerformance != nil {
		dbTask.SecondContentLength = result.SecondContentLength
		dbTask.SecondDNSLookupTime = result.SecondPerformance.DNSLookupTime
		dbTask.SecondTCPConnectionTime = result.SecondPerformance.TCPConnectionTime
		dbTask.SecondTLSHandshakeTime = result.SecondPerformance.TLSHandshakeTime
		dbTask.SecondTTFB = result.SecondPerformance.TTFB
		dbTask.SecondContentTransferTime = result.SecondPerformance.ContentTransferTime
	}

	// Marshal JSONB fields with safe defaults.
	populateJSONBFields(dbTask, result)

	outcome := &TaskOutcome{
		Task:        dbTask,
		CrawlResult: result,
		Success:     true,
	}

	// Discovered links.
	if len(result.Links) > 0 {
		outcome.DiscoveredLinks = cloneDiscoveredLinks(result.Links)
	}

	// HTML upload.
	if upload, ok := buildHTMLUpload(dbTask, result, now); ok {
		outcome.HTMLUpload = upload
		applyHTMLMetadata(dbTask, upload)
	}

	return outcome
}

func (e *TaskExecutor) buildErrorOutcome(ctx context.Context, task *Task, result *crawler.CrawlResult, taskErr error, rateLimited bool) *TaskOutcome {
	now := time.Now().UTC()
	dbTask := taskToDBTask(task)
	populateRequestDiagnostics(dbTask, result)

	outcome := &TaskOutcome{
		Task:        dbTask,
		CrawlResult: result,
		RateLimited: rateLimited,
	}

	if isBlockingError(taskErr) {
		if dbTask.RetryCount < e.cfg.MaxBlockingRetries {
			dbTask.RetryCount++
			dbTask.Error = taskErr.Error()
			dbTask.Status = string(TaskStatusWaiting)
			dbTask.StartedAt = time.Time{}

			backoff := e.blockingBackoff(dbTask.RetryCount)
			outcome.Retry = &RetryDecision{
				ShouldRetry: true,
				NextRunAt:   now.Add(backoff),
				Reason:      "blocking",
			}
			observability.RecordWorkerTaskRetry(ctx, dbTask.JobID, "blocking")
		} else {
			dbTask.Status = string(TaskStatusFailed)
			dbTask.CompletedAt = now
			dbTask.Error = taskErr.Error()
			outcome.Retry = &RetryDecision{IsPermanentFailure: true}
			observability.RecordWorkerTaskFailure(ctx, dbTask.JobID, "blocking")
		}
	} else if isRetryableError(taskErr) && dbTask.RetryCount < e.cfg.MaxTaskRetries {
		dbTask.RetryCount++
		dbTask.Error = taskErr.Error()
		dbTask.Status = string(TaskStatusWaiting)
		dbTask.StartedAt = time.Time{}

		backoff := e.retryableBackoff(dbTask.RetryCount)
		outcome.Retry = &RetryDecision{
			ShouldRetry: true,
			NextRunAt:   now.Add(backoff),
			Reason:      "retryable",
		}
		observability.RecordWorkerTaskRetry(ctx, dbTask.JobID, "retryable")
	} else {
		dbTask.Status = string(TaskStatusFailed)
		dbTask.CompletedAt = now
		dbTask.Error = taskErr.Error()
		outcome.Retry = &RetryDecision{IsPermanentFailure: true}

		failureReason := "non_retryable"
		if isRetryableError(taskErr) {
			failureReason = "retryable_exhausted"
		} else if isBlockingError(taskErr) {
			failureReason = "blocking"
		}
		observability.RecordWorkerTaskFailure(ctx, dbTask.JobID, failureReason)
	}

	return outcome
}

// --- backoff computation ---

func (e *TaskExecutor) blockingBackoff(retryCount int) time.Duration {
	base := time.Duration(e.cfg.BaseDelayMS) * time.Millisecond
	delay := base * (1 << retryCount) // exponential
	max := time.Duration(e.cfg.MaxDelayMS) * time.Millisecond
	if delay > max {
		delay = max
	}
	return delay
}

func (e *TaskExecutor) retryableBackoff(retryCount int) time.Duration {
	delay := time.Duration(retryCount) * time.Second // linear
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	return delay
}

// --- shared helpers (extracted from worker.go) ---

// ConstructTaskURL builds a full URL from task path, host, and domain.
// Exported so the stream worker can use it.
func ConstructTaskURL(path, host, domainName string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return util.NormaliseURL(path)
	} else if host != "" {
		return util.ConstructURL(host, path)
	} else if domainName != "" {
		return util.ConstructURL(domainName, path)
	}
	return util.NormaliseURL(path)
}

// Error classification — unchanged from worker.go.

func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	errorStr := strings.ToLower(err.Error())
	networkErrors := strings.Contains(errorStr, "timeout") ||
		strings.Contains(errorStr, "deadline exceeded") ||
		strings.Contains(errorStr, "connection") ||
		strings.Contains(errorStr, "network") ||
		strings.Contains(errorStr, "temporary") ||
		strings.Contains(errorStr, "reset by peer") ||
		strings.Contains(errorStr, "broken pipe") ||
		strings.Contains(errorStr, "unexpected eof")
	serverErrors := strings.Contains(errorStr, "internal server error") ||
		strings.Contains(errorStr, "bad gateway") ||
		strings.Contains(errorStr, "gateway timeout") ||
		strings.Contains(errorStr, "502") ||
		strings.Contains(errorStr, "504") ||
		strings.Contains(errorStr, "500")
	return networkErrors || serverErrors
}

func isBlockingError(err error) bool {
	if err == nil {
		return false
	}
	errorStr := strings.ToLower(err.Error())
	return strings.Contains(errorStr, "403") ||
		strings.Contains(errorStr, "forbidden") ||
		strings.Contains(errorStr, "429") ||
		strings.Contains(errorStr, "too many requests") ||
		strings.Contains(errorStr, "rate limit") ||
		strings.Contains(errorStr, "503") ||
		strings.Contains(errorStr, "service unavailable")
}

// IsRateLimitError is exported for use by the stream worker when
// determining whether to update domain pacer state.
func IsRateLimitErrorCheck(err error) bool {
	return IsRateLimitError(err)
}

// --- JSONB helpers ---

func populateJSONBFields(task *db.Task, result *crawler.CrawlResult) {
	task.Headers = []byte("{}")
	task.SecondHeaders = []byte("{}")
	task.CacheCheckAttempts = []byte("[]")
	task.RequestDiagnostics = []byte("{}")

	if len(result.Headers) > 0 {
		if b, err := json.Marshal(result.Headers); err == nil && json.Valid(b) {
			task.Headers = b
		}
	}
	if len(result.SecondHeaders) > 0 {
		if b, err := json.Marshal(result.SecondHeaders); err == nil && json.Valid(b) {
			task.SecondHeaders = b
		}
	}
	if len(result.CacheCheckAttempts) > 0 {
		if b, err := json.Marshal(result.CacheCheckAttempts); err == nil && json.Valid(b) {
			task.CacheCheckAttempts = b
		}
	}

	populateRequestDiagnostics(task, result)
}

func populateRequestDiagnostics(task *db.Task, result *crawler.CrawlResult) {
	task.RequestDiagnostics = []byte("{}")
	if result == nil || result.RequestDiagnostics == nil {
		return
	}
	if b, err := json.Marshal(result.RequestDiagnostics); err == nil && json.Valid(b) {
		task.RequestDiagnostics = b
	}
}

// --- HTML helpers ---

const (
	htmlStorageBucket   = "task-html"
	htmlContentEncoding = "gzip"
)

func buildHTMLUpload(task *db.Task, result *crawler.CrawlResult, capturedAt time.Time) (*TaskHTMLUpload, bool) {
	if task == nil || result == nil || len(result.Body) == 0 {
		return nil, false
	}

	mediaType := canonicalHTMLContentType(result.ContentType)
	if mediaType != "text/html" && mediaType != "application/xhtml+xml" {
		return nil, false
	}

	payload, err := gzipHTML(result.Body)
	if err != nil {
		jobsLog.Error("Failed to gzip HTML", "error", err, "task_id", task.ID)
		return nil, false
	}

	uploadCT := mediaType
	if uploadCT == "" {
		uploadCT = "text/html"
	}
	checksum := sha256.Sum256(result.Body)

	return &TaskHTMLUpload{
		Bucket:              htmlStorageBucket,
		Path:                archive.TaskHTMLObjectPath(task.JobID, task.ID),
		ContentType:         normalisedHTMLContentType(result.ContentType),
		UploadContentType:   uploadCT,
		ContentEncoding:     htmlContentEncoding,
		SizeBytes:           int64(len(result.Body)),
		CompressedSizeBytes: int64(len(payload)),
		SHA256:              hex.EncodeToString(checksum[:]),
		CapturedAt:          capturedAt,
		Payload:             payload,
	}, true
}

func applyHTMLMetadata(task *db.Task, upload *TaskHTMLUpload) {
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

func canonicalHTMLContentType(ct string) string {
	trimmed := strings.TrimSpace(ct)
	if trimmed == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(trimmed)
	if err != nil {
		return strings.ToLower(trimmed)
	}
	return strings.ToLower(mediaType)
}

func normalisedHTMLContentType(ct string) string {
	trimmed := strings.TrimSpace(ct)
	if trimmed == "" {
		return "text/html"
	}
	return strings.ToLower(trimmed)
}

func gzipHTML(body []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(body); err != nil {
		return nil, fmt.Errorf("write gzip html: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close gzip html: %w", err)
	}
	return buf.Bytes(), nil
}

func cloneDiscoveredLinks(links map[string][]string) map[string][]string {
	if len(links) == 0 {
		return nil
	}
	cloned := make(map[string][]string, len(links))
	for cat, vals := range links {
		cloned[cat] = append([]string(nil), vals...)
	}
	return cloned
}

func statusCodeOrZero(result *crawler.CrawlResult) int {
	if result != nil {
		return result.StatusCode
	}
	return 0
}

// Sentinel errors.
var (
	ErrDomainDelay = errors.New("domain rate limit delay")
)
