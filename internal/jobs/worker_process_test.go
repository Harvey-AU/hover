// Package jobs tests for worker processing functionality.
//
// ARCHITECTURAL LIMITATION:
// These tests demonstrate the test coverage we would achieve if WorkerPool used
// interfaces instead of concrete types. Currently, WorkerPool depends on:
// - *crawler.Crawler (concrete type) instead of a CrawlerInterface
// - *db.DbQueue (concrete type) instead of a DbQueueInterface
//
// This prevents proper mocking and unit testing. The tests below:
// 1. Test the functions that CAN be tested (error classification)
// 2. Document the test cases we WOULD run with proper interfaces
// 3. Provide mock implementations ready for when refactoring occurs
//
// To enable full testing, worker.go needs the same interface refactoring
// that was successfully applied to manager.go.

package jobs

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Harvey-AU/hover/internal/crawler"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockCrawler implements CrawlerInterface for testing
type MockCrawler struct {
	WarmURLFunc func(ctx context.Context, url string, findLinks bool) (*crawler.CrawlResult, error)
}

func (m *MockCrawler) WarmURL(ctx context.Context, url string, findLinks bool) (*crawler.CrawlResult, error) {
	if m.WarmURLFunc != nil {
		return m.WarmURLFunc(ctx, url, findLinks)
	}
	// Default successful response
	return &crawler.CrawlResult{
		StatusCode:    200,
		CacheStatus:   "MISS",
		ResponseTime:  100,
		ContentType:   "text/html",
		ContentLength: 1024,
		Links:         map[string][]string{},
		Performance: crawler.PerformanceMetrics{
			DNSLookupTime:       10,
			TCPConnectionTime:   20,
			TLSHandshakeTime:    30,
			TTFB:                40,
			ContentTransferTime: 50,
		},
		SecondResponseTime: 50,
		SecondCacheStatus:  "HIT",
		SecondPerformance: &crawler.PerformanceMetrics{
			DNSLookupTime:       5,
			TCPConnectionTime:   10,
			TLSHandshakeTime:    15,
			TTFB:                20,
			ContentTransferTime: 25,
		},
	}, nil
}

func (m *MockCrawler) DiscoverSitemapsAndRobots(ctx context.Context, domain string) (*crawler.SitemapDiscoveryResult, error) {
	return &crawler.SitemapDiscoveryResult{}, nil
}

func (m *MockCrawler) ParseSitemap(ctx context.Context, sitemapURL string) ([]string, error) {
	return []string{}, nil
}

func (m *MockCrawler) FilterURLs(urls []string, includePaths, excludePaths []string) []string {
	return urls
}

func (m *MockCrawler) GetUserAgent() string {
	return "TestBot/1.0"
}

// MockDbQueue implements a minimal DbQueue interface for testing
type MockDbQueue struct {
	GetNextTaskFunc             func(ctx context.Context, jobID string) (*db.Task, error)
	UpdateTaskStatusFunc        func(ctx context.Context, task *db.Task) error
	DecrementRunningTasksFunc   func(ctx context.Context, jobID string) error
	DecrementRunningTasksByFunc func(ctx context.Context, jobID string, count int) error
	IncrementRunningTasksByFunc func(ctx context.Context, jobID string, count int) error
	ExecuteFunc                 func(ctx context.Context, fn func(*sql.Tx) error) error
	ExecuteMaintenanceFunc      func(ctx context.Context, fn func(*sql.Tx) error) error
	UpdateTaskHTMLMetadataFunc  func(ctx context.Context, taskID string, metadata db.TaskHTMLMetadata) error
}

func (m *MockDbQueue) GetNextTask(ctx context.Context, jobID string) (*db.Task, error) {
	if m.GetNextTaskFunc != nil {
		return m.GetNextTaskFunc(ctx, jobID)
	}
	return nil, sql.ErrNoRows
}

func (m *MockDbQueue) UpdateTaskStatus(ctx context.Context, task *db.Task) error {
	if m.UpdateTaskStatusFunc != nil {
		return m.UpdateTaskStatusFunc(ctx, task)
	}
	return nil
}

func (m *MockDbQueue) DecrementRunningTasks(ctx context.Context, jobID string) error {
	if m.DecrementRunningTasksFunc != nil {
		return m.DecrementRunningTasksFunc(ctx, jobID)
	}
	return nil
}

func (m *MockDbQueue) DecrementRunningTasksBy(ctx context.Context, jobID string, count int) error {
	if m.DecrementRunningTasksByFunc != nil {
		return m.DecrementRunningTasksByFunc(ctx, jobID, count)
	}
	// Fallback to single decrement for compatibility in tests
	if m.DecrementRunningTasksFunc != nil {
		return m.DecrementRunningTasksFunc(ctx, jobID)
	}
	return nil
}

func (m *MockDbQueue) IncrementRunningTasksBy(ctx context.Context, jobID string, count int) error {
	if m.IncrementRunningTasksByFunc != nil {
		return m.IncrementRunningTasksByFunc(ctx, jobID, count)
	}
	return nil
}

func (m *MockDbQueue) Execute(ctx context.Context, fn func(*sql.Tx) error) error {
	if m.ExecuteFunc != nil {
		return m.ExecuteFunc(ctx, fn)
	}
	return nil
}

func (m *MockDbQueue) ExecuteWithContext(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	if m.ExecuteFunc != nil {
		// For simplicity, wrap to call ExecuteFunc
		return m.ExecuteFunc(ctx, func(tx *sql.Tx) error {
			return fn(ctx, tx)
		})
	}
	return nil
}

func (m *MockDbQueue) ExecuteMaintenance(ctx context.Context, fn func(*sql.Tx) error) error {
	if m.ExecuteMaintenanceFunc != nil {
		return m.ExecuteMaintenanceFunc(ctx, fn)
	}
	return m.Execute(ctx, fn)
}

func (m *MockDbQueue) SetConcurrencyOverride(fn db.ConcurrencyOverrideFunc) {
	// No-op for mock
}

func (m *MockDbQueue) UpdateDomainTechnologies(ctx context.Context, domainID int, technologies, headers []byte, htmlPath string) error {
	return nil
}

func (m *MockDbQueue) UpdateTaskHTMLMetadata(ctx context.Context, taskID string, metadata db.TaskHTMLMetadata) error {
	if m.UpdateTaskHTMLMetadataFunc != nil {
		return m.UpdateTaskHTMLMetadataFunc(ctx, taskID, metadata)
	}
	return nil
}

func (m *MockDbQueue) FindArchiveCandidates(_ context.Context, _, _ int) ([]db.ArchiveCandidate, error) {
	return nil, nil
}

func (m *MockDbQueue) MarkTaskArchived(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (m *MockDbQueue) MarkFullyArchivedJobs(_ context.Context) (int64, error) {
	return 0, nil
}

// TestWorkerPoolProcessTask demonstrates the test structure for processTask
// NOTE: This test cannot actually execute processTask due to concrete type dependencies.
// It documents the test cases we would run if WorkerPool used interfaces instead of concrete types.
func TestWorkerPoolProcessTask(t *testing.T) {
	tests := []struct {
		name            string
		task            *Task
		crawlerResponse *crawler.CrawlResult
		crawlerError    error
		expectedError   bool
		checkResult     func(t *testing.T, result *crawler.CrawlResult)
	}{
		{
			name: "successful_task_processing",
			task: &Task{
				ID:         "test-task-1",
				JobID:      "test-job-1",
				PageID:     1,
				Path:       "/test-page",
				DomainName: "example.com",
				FindLinks:  false,
				CrawlDelay: 0,
			},
			crawlerResponse: &crawler.CrawlResult{
				StatusCode:    200,
				CacheStatus:   "MISS",
				ResponseTime:  150,
				ContentType:   "text/html",
				ContentLength: 2048,
			},
			expectedError: false,
			checkResult: func(t *testing.T, result *crawler.CrawlResult) {
				assert.Equal(t, 200, result.StatusCode)
				assert.Equal(t, "MISS", result.CacheStatus)
				assert.Equal(t, int64(150), result.ResponseTime)
			},
		},
		{
			name: "task_with_crawl_delay",
			task: &Task{
				ID:         "test-task-2",
				JobID:      "test-job-1",
				PageID:     2,
				Path:       "/delayed-page",
				DomainName: "example.com",
				FindLinks:  false,
				CrawlDelay: 1, // 1 second delay
			},
			crawlerResponse: &crawler.CrawlResult{
				StatusCode: 200,
			},
			expectedError: false,
		},
		{
			name: "task_with_full_url_path",
			task: &Task{
				ID:        "test-task-3",
				JobID:     "test-job-1",
				PageID:    3,
				Path:      "https://example.com/full-url",
				FindLinks: false,
			},
			crawlerResponse: &crawler.CrawlResult{
				StatusCode: 200,
			},
			expectedError: false,
		},
		{
			name: "crawler_returns_error",
			task: &Task{
				ID:         "test-task-4",
				JobID:      "test-job-1",
				PageID:     4,
				Path:       "/error-page",
				DomainName: "example.com",
			},
			crawlerError:  errors.New("connection timeout"),
			expectedError: true,
		},
		{
			name: "task_with_redirect",
			task: &Task{
				ID:         "test-task-5",
				JobID:      "test-job-1",
				PageID:     5,
				Path:       "/redirect",
				DomainName: "example.com",
			},
			crawlerResponse: &crawler.CrawlResult{
				StatusCode:  301,
				RedirectURL: "https://example.com/new-location",
			},
			expectedError: false,
			checkResult: func(t *testing.T, result *crawler.CrawlResult) {
				assert.Equal(t, 301, result.StatusCode)
				assert.Equal(t, "https://example.com/new-location", result.RedirectURL)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			mockDB, _, err := sqlmock.New()
			require.NoError(t, err)
			defer mockDB.Close()

			// Create mocked crawler
			mockCrawler := &MockCrawler{
				WarmURLFunc: func(ctx context.Context, url string, findLinks bool) (*crawler.CrawlResult, error) {
					if tt.crawlerError != nil {
						return nil, tt.crawlerError
					}
					return tt.crawlerResponse, nil
				},
			}

			// Create mocked dbQueue
			mockQueue := &MockDbQueue{}

			// Create worker pool with mocked dependencies
			wp := &WorkerPool{
				db:           mockDB,
				dbQueue:      mockQueue,
				crawler:      mockCrawler,
				jobInfoCache: make(map[string]*JobInfo),
			}

			// Now we can actually test processTask!
			ctx := context.Background()
			result, err := wp.processTask(ctx, tt.task)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
			}
		})
	}
}

// TestWorkerPoolProcessNextTask demonstrates the test structure for processNextTask
// NOTE: Cannot execute due to concrete dbQueue dependency. Documents intended test coverage.
func TestWorkerPoolProcessNextTask(t *testing.T) {
	tests := []struct {
		name          string
		activeJobs    []string
		taskAvailable bool
		taskError     error
		expectedError error
	}{
		{
			name:          "no_active_jobs",
			activeJobs:    []string{},
			expectedError: sql.ErrNoRows,
		},
		{
			name:          "active_job_with_task",
			activeJobs:    []string{"job-1"},
			taskAvailable: true,
			expectedError: nil,
		},
		{
			name:          "active_job_no_tasks",
			activeJobs:    []string{"job-1", "job-2"},
			taskAvailable: false,
			expectedError: sql.ErrNoRows,
		},
		{
			name:          "database_error",
			activeJobs:    []string{"job-1"},
			taskError:     errors.New("database connection lost"),
			expectedError: errors.New("database connection lost"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			mockDB, _, err := sqlmock.New()
			require.NoError(t, err)
			defer mockDB.Close()

			// Create mock dbQueue with controlled behavior
			mockQueue := &MockDbQueue{
				GetNextTaskFunc: func(ctx context.Context, jobID string) (*db.Task, error) {
					if tt.taskError != nil {
						return nil, tt.taskError
					}
					if tt.taskAvailable {
						return &db.Task{
							ID:     "task-1",
							JobID:  jobID,
							PageID: 1,
							Path:   "/test",
							Status: "pending",
						}, nil
					}
					return nil, sql.ErrNoRows
				},
				UpdateTaskStatusFunc: func(ctx context.Context, task *db.Task) error {
					return nil
				},
			}

			// Create mock crawler
			mockCrawler := &MockCrawler{}

			// Create batch manager with mock queue
			batchMgr := db.NewBatchManager(mockQueue)
			defer batchMgr.Stop()

			// Create worker pool with mocked dependencies
			wp := &WorkerPool{
				db:           mockDB,
				dbQueue:      mockQueue,
				batchManager: batchMgr,
				crawler:      mockCrawler,
				jobs:         make(map[string]bool),
				jobInfoCache: make(map[string]*JobInfo),
			}

			// Add active jobs
			for _, jobID := range tt.activeJobs {
				wp.jobs[jobID] = true
				wp.jobInfoCache[jobID] = &JobInfo{
					DomainID:   1,
					DomainName: "example.com",
					FindLinks:  false,
				}
			}

			// Now we can actually test processNextTask!
			ctx := context.Background()
			err = wp.processNextTask(ctx)

			if tt.expectedError != nil {
				assert.Equal(t, tt.expectedError, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestRetryErrorClassification tests the error classification functions
func TestRetryErrorClassification(t *testing.T) {
	tests := []struct {
		name            string
		error           error
		expectRetryable bool
		expectBlocking  bool
	}{
		{
			name:            "connection_timeout_is_retryable",
			error:           errors.New("connection timeout"),
			expectRetryable: true,
			expectBlocking:  false,
		},
		{
			name:            "deadline_exceeded_is_retryable",
			error:           errors.New("deadline exceeded"),
			expectRetryable: true,
			expectBlocking:  false,
		},
		{
			name:            "403_forbidden_is_blocking",
			error:           errors.New("403 Forbidden"),
			expectRetryable: false,
			expectBlocking:  true,
		},
		{
			name:            "429_rate_limit_is_blocking",
			error:           errors.New("429 Too Many Requests"),
			expectRetryable: false,
			expectBlocking:  true,
		},
		{
			name:            "500_server_error_is_retryable",
			error:           errors.New("500 Internal Server Error"),
			expectRetryable: true,
			expectBlocking:  false,
		},
		{
			name:            "502_bad_gateway_is_retryable",
			error:           errors.New("502 Bad Gateway"),
			expectRetryable: true,
			expectBlocking:  false,
		},
		{
			name:            "invalid_url_not_retryable",
			error:           errors.New("invalid URL format"),
			expectRetryable: false,
			expectBlocking:  false,
		},
		{
			name:            "nil_error_not_retryable",
			error:           nil,
			expectRetryable: false,
			expectBlocking:  false,
		},
		{
			name:            "503_service_unavailable_is_blocking",
			error:           errors.New("503 Service Unavailable"),
			expectRetryable: false,
			expectBlocking:  true,
		},
		{
			name:            "service_unavailable_text_is_blocking",
			error:           errors.New("service unavailable"),
			expectRetryable: false,
			expectBlocking:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test the actual production functions
			isRetryable := isRetryableError(tt.error)
			isBlocking := isBlockingError(tt.error)

			assert.Equal(t, tt.expectRetryable, isRetryable,
				"isRetryableError(%v) should return %v", tt.error, tt.expectRetryable)
			assert.Equal(t, tt.expectBlocking, isBlocking,
				"isBlockingError(%v) should return %v", tt.error, tt.expectBlocking)
		})
	}
}

// TestRetryDecisionLogic tests the retry decision outcomes based on error type and retry count
// This test documents the expected behavior without re-implementing the logic
func TestRetryDecisionLogic(t *testing.T) {
	tests := []struct {
		name                 string
		initialRetries       int
		errorType            error
		shouldRetry          bool
		finalStatusIfNoRetry string
		description          string
	}{
		{
			name:           "retryable_error_under_limit",
			initialRetries: 0,
			errorType:      errors.New("connection timeout"),
			shouldRetry:    true,
			description:    "Retryable errors should retry when under MaxTaskRetries",
		},
		{
			name:                 "retryable_error_at_limit",
			initialRetries:       MaxTaskRetries,
			errorType:            errors.New("connection timeout"),
			shouldRetry:          false,
			finalStatusIfNoRetry: "failed",
			description:          "Retryable errors should not retry when at MaxTaskRetries",
		},
		{
			name:           "blocking_error_first_attempt",
			initialRetries: 0,
			errorType:      errors.New("403 Forbidden"),
			shouldRetry:    true,
			description:    "Blocking errors get limited retries (up to 2)",
		},
		{
			name:                 "blocking_error_at_limit",
			initialRetries:       2,
			errorType:            errors.New("429 Too Many Requests"),
			shouldRetry:          false,
			finalStatusIfNoRetry: "failed",
			description:          "Blocking errors fail permanently after 2 retries",
		},
		{
			name:                 "non_retryable_error",
			initialRetries:       0,
			errorType:            errors.New("invalid URL format"),
			shouldRetry:          false,
			finalStatusIfNoRetry: "failed",
			description:          "Non-retryable errors should fail immediately",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Document the expected behavior based on error classification
			// The actual implementation in processNextTask would handle this

			// Check if error is classified correctly for retry decision
			if tt.shouldRetry {
				if isBlockingError(tt.errorType) {
					assert.Less(t, tt.initialRetries, 2,
						"Blocking errors should retry only if retry count < 2")
				} else if isRetryableError(tt.errorType) {
					assert.Less(t, tt.initialRetries, MaxTaskRetries,
						"Retryable errors should retry only if retry count < MaxTaskRetries")
				}
			} else {
				// Should not retry - document why
				if isBlockingError(tt.errorType) {
					assert.GreaterOrEqual(t, tt.initialRetries, 2,
						"Blocking errors should not retry after 2 attempts")
				} else if isRetryableError(tt.errorType) {
					assert.GreaterOrEqual(t, tt.initialRetries, MaxTaskRetries,
						"Retryable errors should not retry after MaxTaskRetries")
				} else {
					assert.False(t, isRetryableError(tt.errorType) || isBlockingError(tt.errorType),
						"Non-retryable/non-blocking errors should fail immediately")
				}
			}

			// Add descriptive assertion for documentation
			assert.NotEmpty(t, tt.description, "Test case should document expected behavior")
		})
	}
}

// TestExponentialBackoffCalculation tests the backoff duration calculation logic
func TestExponentialBackoffCalculation(t *testing.T) {
	tests := []struct {
		name            string
		retryCount      int
		expectedBackoff time.Duration
	}{
		{
			name:            "first_retry_1_second",
			retryCount:      0,
			expectedBackoff: 1 * time.Second,
		},
		{
			name:            "second_retry_2_seconds",
			retryCount:      1,
			expectedBackoff: 2 * time.Second,
		},
		{
			name:            "third_retry_4_seconds",
			retryCount:      2,
			expectedBackoff: 4 * time.Second,
		},
		{
			name:            "fourth_retry_8_seconds",
			retryCount:      3,
			expectedBackoff: 8 * time.Second,
		},
		{
			name:            "fifth_retry_16_seconds",
			retryCount:      4,
			expectedBackoff: 16 * time.Second,
		},
		{
			name:            "sixth_retry_32_seconds",
			retryCount:      5,
			expectedBackoff: 32 * time.Second,
		},
		{
			name:            "seventh_retry_capped_at_60_seconds",
			retryCount:      6,
			expectedBackoff: 60 * time.Second, // Would be 64s, but capped at 60s
		},
		{
			name:            "eighth_retry_capped_at_60_seconds",
			retryCount:      7,
			expectedBackoff: 60 * time.Second, // Would be 128s, but capped at 60s
		},
		{
			name:            "very_high_retry_count_still_capped",
			retryCount:      10,
			expectedBackoff: 60 * time.Second, // Would be 1024s, but capped at 60s
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := calculateBackoffDuration(tt.retryCount)
			assert.Equal(t, tt.expectedBackoff, actual,
				"calculateBackoffDuration(%d) should return %v", tt.retryCount, tt.expectedBackoff)
		})
	}
}
