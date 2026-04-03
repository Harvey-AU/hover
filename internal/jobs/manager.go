package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Harvey-AU/hover/internal/crawler"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/util"
	"github.com/getsentry/sentry-go"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// DbQueueProvider defines the interface for database operations
type DbQueueProvider interface {
	Execute(ctx context.Context, fn func(*sql.Tx) error) error
	EnqueueURLs(ctx context.Context, jobID string, pages []db.Page, sourceType string, sourceURL string) error
	CleanupStuckJobs(ctx context.Context) error
}

// JobManagerInterface defines the interface for job management operations
type JobManagerInterface interface {
	// Core job operations used by API layer
	CreateJob(ctx context.Context, options *JobOptions) (*Job, error)
	CancelJob(ctx context.Context, jobID string) error
	GetJobStatus(ctx context.Context, jobID string) (*Job, error)

	// Additional job operations
	GetJob(ctx context.Context, jobID string) (*Job, error)
	EnqueueJobURLs(ctx context.Context, jobID string, pages []db.Page, sourceType string, sourceURL string) error

	// Job utility methods
	IsJobComplete(job *Job) bool
	CalculateJobProgress(job *Job) float64
	ValidateStatusTransition(from, to JobStatus) error
	UpdateJobStatus(ctx context.Context, jobID string, status JobStatus) error
}

// JobManager handles job creation and lifecycle management
type JobManager struct {
	db      *sql.DB
	dbQueue DbQueueProvider
	crawler CrawlerInterface

	workerPool *WorkerPool

	// Map to track which pages have been processed for each job
	processedPages map[string]struct{} // Key format: "jobID_pageID"
	pagesMutex     sync.RWMutex        // Mutex for thread-safe access
}

// NewJobManager creates a new job manager
func NewJobManager(db *sql.DB, dbQueue DbQueueProvider, crawler CrawlerInterface, workerPool *WorkerPool) *JobManager {
	return &JobManager{
		db:             db,
		dbQueue:        dbQueue,
		crawler:        crawler,
		workerPool:     workerPool,
		processedPages: make(map[string]struct{}),
	}
}

// handleExistingJobs checks for existing active jobs and cancels them if found
func (jm *JobManager) handleExistingJobs(ctx context.Context, domain string, userID *string, organisationID *string) error {
	// Need either user_id or organisation_id to check for duplicates
	if (userID == nil || *userID == "") && (organisationID == nil || *organisationID == "") {
		return nil // Skip check if neither ID is provided
	}

	var existingJobID string
	var existingJobStatus string
	var existingOrgID sql.NullString
	var existingUserID sql.NullString

	// Build query dynamically based on available IDs
	var query string
	var args []any

	if organisationID != nil && *organisationID != "" {
		// Prefer organisation-level duplicate checking (multi-user organisations)
		query = `
			SELECT j.id, j.status, j.organisation_id, j.user_id
			FROM jobs j
			JOIN domains d ON j.domain_id = d.id
			WHERE d.name = $1
			AND j.organisation_id = $2
			AND j.status IN ('pending', 'initializing', 'running', 'paused')
			ORDER BY j.created_at DESC
			LIMIT 1
		`
		args = []any{domain, *organisationID}
	} else {
		// Fall back to user-level checking for users without organisations
		query = `
			SELECT j.id, j.status, j.organisation_id, j.user_id
			FROM jobs j
			JOIN domains d ON j.domain_id = d.id
			WHERE d.name = $1
			AND j.user_id = $2
			AND j.status IN ('pending', 'initializing', 'running', 'paused')
			ORDER BY j.created_at DESC
			LIMIT 1
		`
		args = []any{domain, *userID}
	}

	err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, query, args...).Scan(
			&existingJobID, &existingJobStatus, &existingOrgID, &existingUserID)
	})

	if err == nil && existingJobID != "" {
		// Found an existing active job for the same domain and user/organisation
		logEvent := log.Info().
			Str("existing_job_id", existingJobID).
			Str("existing_job_status", existingJobStatus).
			Str("domain", domain)

		if existingOrgID.Valid {
			logEvent = logEvent.Str("organisation_id", existingOrgID.String)
		}
		if existingUserID.Valid {
			logEvent = logEvent.Str("user_id", existingUserID.String)
		}

		logEvent.Msg("Found existing active job for domain, cancelling it")

		if err := jm.CancelJob(ctx, existingJobID); err != nil {
			log.Error().
				Err(err).
				Str("job_id", existingJobID).
				Msg("Failed to cancel existing job")
			// Continue with new job creation even if cancellation fails
		}
	} else if err != nil && err != sql.ErrNoRows {
		// Log query error but continue with job creation
		log.Warn().
			Err(err).
			Str("domain", domain).
			Msg("Error checking for existing jobs")
	}

	return nil // Always return nil to continue with job creation
}

// createJobObject creates a new Job instance with the given options and normalized domain
func createJobObject(options *JobOptions, normalisedDomain string) *Job {
	return &Job{
		ID:                       uuid.New().String(),
		Domain:                   normalisedDomain,
		UserID:                   options.UserID,
		OrganisationID:           options.OrganisationID,
		Status:                   JobStatusPending,
		Progress:                 0,
		TotalTasks:               0,
		CompletedTasks:           0,
		FoundTasks:               0,
		SitemapTasks:             0,
		FailedTasks:              0,
		CreatedAt:                time.Now().UTC(),
		Concurrency:              options.Concurrency,
		FindLinks:                options.FindLinks,
		MaxPages:                 options.MaxPages,
		IncludePaths:             options.IncludePaths,
		ExcludePaths:             options.ExcludePaths,
		RequiredWorkers:          options.RequiredWorkers,
		AllowCrossSubdomainLinks: options.AllowCrossSubdomainLinks,
		SourceType:               options.SourceType,
		SourceDetail:             options.SourceDetail,
		SourceInfo:               options.SourceInfo,
		SchedulerID:              options.SchedulerID,
	}
}

// setupJobDatabase creates domain and job records in the database
// Returns the domain ID for use in subsequent operations
func (jm *JobManager) setupJobDatabase(ctx context.Context, job *Job, normalisedDomain string) (int, error) {
	var domainID int

	err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		// Get or create domain ID
		err := tx.QueryRow(`
			INSERT INTO domains(name) VALUES($1) 
			ON CONFLICT (name) DO UPDATE SET name=EXCLUDED.name 
			RETURNING id`, normalisedDomain).Scan(&domainID)
		if err != nil {
			return fmt.Errorf("failed to get or create domain: %w", err)
		}

		// Insert the job
		_, err = tx.Exec(
			`INSERT INTO jobs (
				id, domain_id, user_id, organisation_id, status, progress, total_tasks, completed_tasks, failed_tasks, skipped_tasks,
				created_at, concurrency, find_links, include_paths, exclude_paths,
				required_workers, max_pages, allow_cross_subdomain_links,
				found_tasks, sitemap_tasks, source_type, source_detail, source_info, scheduler_id
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)`,
			job.ID, domainID, job.UserID, job.OrganisationID, string(job.Status), job.Progress,
			job.TotalTasks, job.CompletedTasks, job.FailedTasks, job.SkippedTasks,
			job.CreatedAt, job.Concurrency, job.FindLinks,
			db.Serialise(job.IncludePaths), db.Serialise(job.ExcludePaths),
			job.RequiredWorkers, job.MaxPages, job.AllowCrossSubdomainLinks,
			job.FoundTasks, job.SitemapTasks, job.SourceType, job.SourceDetail, job.SourceInfo,
			job.SchedulerID,
		)
		return err
	})

	if err != nil {
		return 0, fmt.Errorf("failed to create job: %w", err)
	}

	return domainID, nil
}

// validateRootURLAccess checks robots.txt rules and validates root URL access
func (jm *JobManager) validateRootURLAccess(ctx context.Context, job *Job, normalisedDomain string, rootPath string) (*crawler.RobotsRules, error) {
	var robotsRules *crawler.RobotsRules

	if jm.crawler != nil {
		// Use DiscoverSitemapsAndRobots which already includes parsing
		discoveryResult, err := jm.crawler.DiscoverSitemapsAndRobots(ctx, normalisedDomain)
		if err != nil {
			log.Error().
				Err(err).
				Str("domain", normalisedDomain).
				Msg("Failed to fetch robots.txt for manual URL")

			// Update job with error
			if updateErr := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `
					UPDATE jobs
					SET status = $1, error_message = $2, completed_at = $3
					WHERE id = $4
				`, JobStatusFailed, fmt.Sprintf("Failed to fetch robots.txt: %v", err), time.Now().UTC(), job.ID)
				return err
			}); updateErr != nil {
				log.Error().Err(updateErr).Str("job_id", job.ID).Msg("Failed to update job status")
			}
			return nil, fmt.Errorf("failed to fetch robots.txt: %w", err)
		}

		// Use the already parsed robots rules from discovery
		robotsRules = discoveryResult.RobotsRules

		// Store crawl delay if specified in robots.txt
		if robotsRules != nil && robotsRules.CrawlDelay > 0 {
			jm.updateDomainCrawlDelay(ctx, normalisedDomain, robotsRules.CrawlDelay)
		}
	}

	// Check if root path is allowed by robots.txt
	if robotsRules != nil && !crawler.IsPathAllowed(robotsRules, rootPath) {
		log.Warn().
			Str("job_id", job.ID).
			Str("domain", normalisedDomain).
			Str("path", rootPath).
			Msg("Root path is disallowed by robots.txt, job cannot proceed")

		// Update job with error
		if updateErr := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				UPDATE jobs
				SET status = $1, error_message = $2, completed_at = $3
				WHERE id = $4
			`, JobStatusFailed, "Root path (/) is disallowed by robots.txt", time.Now().UTC(), job.ID)
			return err
		}); updateErr != nil {
			log.Error().Err(updateErr).Str("job_id", job.ID).Msg("Failed to update job status")
		}
		return nil, fmt.Errorf("root path is disallowed by robots.txt")
	}

	return robotsRules, nil
}

// createManualRootTask creates page and task records for the root URL
func (jm *JobManager) createManualRootTask(ctx context.Context, job *Job, domainID int, rootPath string) error {
	err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		var pageID int
		err := tx.QueryRowContext(ctx, `
				INSERT INTO pages (domain_id, host, path)
				VALUES ($1, $2, $3)
				ON CONFLICT (domain_id, host, path) DO UPDATE SET path = EXCLUDED.path
				RETURNING id
			`, domainID, job.Domain, rootPath).Scan(&pageID)

		if err != nil {
			return fmt.Errorf("failed to create page record for root path: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO domain_hosts (domain_id, host, is_primary, last_seen_at)
			VALUES ($1, $2, TRUE, NOW())
			ON CONFLICT (domain_id, host) DO UPDATE
			SET is_primary = TRUE,
				last_seen_at = NOW()
		`, domainID, job.Domain); err != nil {
			return fmt.Errorf("failed to upsert domain host for root path: %w", err)
		}

		// Enqueue the root URL with its page ID
		_, err = tx.ExecContext(ctx, `
			INSERT INTO tasks (
				id, job_id, page_id, host, path, status, created_at, retry_count,
				source_type, source_url
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		`, uuid.New().String(), job.ID, pageID, job.Domain, rootPath, "pending", time.Now().UTC(), 0, "manual", "")

		if err != nil {
			return fmt.Errorf("failed to enqueue task for root path: %w", err)
		}

		jm.markPageProcessed(job.ID, pageID)
		return nil
	})

	if err != nil {
		log.Error().Err(err).Msg("Failed to create and enqueue root URL")
		return err
	}

	log.Info().
		Str("job_id", job.ID).
		Str("domain", job.Domain).
		Msg("Added root URL to job queue")

	return nil
}

// setupJobURLDiscovery handles URL discovery for the job (sitemap or manual)
func (jm *JobManager) setupJobURLDiscovery(ctx context.Context, job *Job, options *JobOptions, domainID int, normalisedDomain string) error {
	if options.UseSitemap {
		// Fetch and process sitemap in a separate goroutine
		// Use detached context with timeout for background processing
		backgroundCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		go func() {
			defer cancel()
			jm.processSitemap(backgroundCtx, job.ID, normalisedDomain, options.IncludePaths, options.ExcludePaths)
		}()
		return nil
	}

	// Manual root URL creation - process in background for consistency
	// Use detached context with timeout for background processing
	backgroundCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	go func() {
		defer cancel()
		rootPath := "/"
		_, err := jm.validateRootURLAccess(backgroundCtx, job, normalisedDomain, rootPath)
		if err != nil {
			log.Error().Err(err).Str("job_id", job.ID).Msg("Failed to validate root URL access")
			return // Error already logged and job updated by validateRootURLAccess
		}

		// Create page and task records for the root URL
		if err := jm.createManualRootTask(backgroundCtx, job, domainID, rootPath); err != nil {
			log.Error().Err(err).Str("job_id", job.ID).Msg("Failed to create manual root task")
			return
		}

		// Notify workers immediately that new tasks are available
		if jm.workerPool != nil {
			jm.workerPool.NotifyNewTasks()
		}
	}()

	return nil
}

// CreateJob creates a new job with the given options
func (jm *JobManager) CreateJob(ctx context.Context, options *JobOptions) (*Job, error) {
	span := sentry.StartSpan(ctx, "manager.create_job")
	defer span.Finish()

	span.SetTag("domain", options.Domain)

	normalisedDomain := util.NormaliseDomain(options.Domain)

	if options.Concurrency <= 0 {
		defaultConcurrency := fallbackJobConcurrency
		if jm.workerPool != nil && jm.workerPool.maxWorkers > 0 {
			defaultConcurrency = jm.workerPool.maxWorkers
		}
		log.Info().
			Str("domain", normalisedDomain).
			Int("default_concurrency", defaultConcurrency).
			Msg("Concurrency not specified; using worker pool maximum")
		options.Concurrency = defaultConcurrency
	}

	// Handle any existing active jobs for the same domain and user/organisation
	if err := jm.handleExistingJobs(ctx, normalisedDomain, options.UserID, options.OrganisationID); err != nil {
		return nil, fmt.Errorf("failed to handle existing jobs: %w", err)
	}

	// Create a new job object
	job := createJobObject(options, normalisedDomain)

	// Setup database records for the job
	domainID, err := jm.setupJobDatabase(ctx, job, normalisedDomain)
	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		sentry.CaptureException(err)
		return nil, err
	}

	log.Info().
		Str("job_id", job.ID).
		Str("domain", job.Domain).
		Bool("use_sitemap", options.UseSitemap).
		Bool("find_links", options.FindLinks).
		Int("max_pages", options.MaxPages).
		Msg("Created new job")

	// Setup URL discovery (sitemap or manual root URL)
	if err := jm.setupJobURLDiscovery(ctx, job, options, domainID, normalisedDomain); err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		return job, err // Return job even on URL discovery error (some errors are expected)
	}

	return job, nil
}

// Helper method to check if a page has been processed for a job
func (jm *JobManager) isPageProcessed(jobID string, pageID int) bool {
	key := fmt.Sprintf("%s_%d", jobID, pageID)
	jm.pagesMutex.RLock()
	defer jm.pagesMutex.RUnlock()
	_, exists := jm.processedPages[key]
	return exists
}

// Helper method to mark a page as processed for a job
func (jm *JobManager) markPageProcessed(jobID string, pageID int) {
	key := fmt.Sprintf("%s_%d", jobID, pageID)
	jm.pagesMutex.Lock()
	defer jm.pagesMutex.Unlock()
	jm.processedPages[key] = struct{}{}
}

// Helper method to clear processed pages for a job (when job is completed or canceled)
func (jm *JobManager) clearProcessedPages(jobID string) {
	jm.pagesMutex.Lock()
	defer jm.pagesMutex.Unlock()

	// Find all keys that start with this job ID
	prefix := jobID + "_"
	for key := range jm.processedPages {
		if strings.HasPrefix(key, prefix) {
			delete(jm.processedPages, key)
		}
	}
}

// EnqueueJobURLs is a wrapper around dbQueue.EnqueueURLs that adds duplicate detection
func (jm *JobManager) EnqueueJobURLs(ctx context.Context, jobID string, pages []db.Page, sourceType string, sourceURL string) error {
	span := sentry.StartSpan(ctx, "manager.enqueue_job_urls")
	defer span.Finish()

	span.SetTag("job_id", jobID)
	span.SetTag("url_count", fmt.Sprintf("%d", len(pages)))

	if len(pages) == 0 {
		return nil
	}

	// Filter out pages that have already been processed
	var filteredPages []db.Page

	for _, page := range pages {
		if !jm.isPageProcessed(jobID, page.ID) {
			filteredPages = append(filteredPages, page)
			// Don't mark as processed yet - we'll do that after successful enqueue
		}
	}

	// If all pages were already processed, just return success
	if len(filteredPages) == 0 {
		log.Debug().
			Str("job_id", jobID).
			Int("skipped_urls", len(pages)).
			Msg("All URLs already processed, skipping")
		return nil
	}

	log.Debug().
		Str("job_id", jobID).
		Int("total_urls", len(pages)).
		Int("new_urls", len(filteredPages)).
		Int("skipped_urls", len(pages)-len(filteredPages)).
		Msg("Enqueueing filtered URLs")

	// Use the filtered lists to enqueue only new pages
	err := jm.dbQueue.EnqueueURLs(ctx, jobID, filteredPages, sourceType, sourceURL)

	// Only mark pages as processed if the enqueue was successful
	if err == nil {

		// Mark all successfully enqueued pages as processed
		for _, page := range filteredPages {
			jm.markPageProcessed(jobID, page.ID)
		}
	} else {
		log.Error().
			Err(err).
			Str("job_id", jobID).
			Int("url_count", len(filteredPages)).
			Msg("Failed to enqueue URLs, not marking pages as processed")
	}

	return err
}

// CancelJob cancels a running job
func (jm *JobManager) CancelJob(ctx context.Context, jobID string) error {
	span := sentry.StartSpan(ctx, "manager.cancel_job")
	defer span.Finish()

	span.SetTag("job_id", jobID)

	// Get the job using our new method
	job, err := jm.GetJob(ctx, jobID)
	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		sentry.CaptureException(err)
		return fmt.Errorf("failed to get job: %w", err)
	}

	// Check if job can be canceled
	if job.Status != JobStatusRunning && job.Status != JobStatusPending && job.Status != JobStatusPaused && job.Status != JobStatusInitialising {
		return fmt.Errorf("job cannot be canceled: %s", job.Status)
	}

	// Update job status to cancelled
	job.Status = JobStatusCancelled
	job.CompletedAt = time.Now().UTC()

	// Use dbQueue for transaction safety
	err = jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		// Update job status
		_, err := tx.ExecContext(ctx, `
			UPDATE jobs
			SET status = $1, completed_at = $2
			WHERE id = $3
		`, job.Status, job.CompletedAt, job.ID)

		if err != nil {
			return err
		}

		// Cancel pending and waiting tasks
		_, err = tx.ExecContext(ctx, `
			UPDATE tasks
			SET status = $1
			WHERE job_id = $2 AND status IN ($3, $4)
		`, TaskStatusSkipped, job.ID, TaskStatusPending, TaskStatusWaiting)

		return err
	})

	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		sentry.CaptureException(err)
		log.Error().Err(err).Str("job_id", job.ID).Msg("Failed to cancel job")
		return fmt.Errorf("failed to cancel job: %w", err)
	}

	// Remove job from worker pool
	if jm.workerPool != nil {
		jm.workerPool.RemoveJob(job.ID)
	}

	// Clear processed pages for this job
	jm.clearProcessedPages(job.ID)

	log.Debug().
		Str("job_id", job.ID).
		Str("domain", job.Domain).
		Msg("Cancelled job")

	return nil
}

// GetJob retrieves a job by ID
func (jm *JobManager) GetJob(ctx context.Context, jobID string) (*Job, error) {
	span := sentry.StartSpan(ctx, "jobs.get_job")
	defer span.Finish()

	span.SetTag("job_id", jobID)

	var job Job
	var includePaths, excludePaths []byte
	var startedAt, completedAt sql.NullTime
	var errorMessage, userID, organisationID sql.NullString

	// Use DbQueue.Execute for transactional safety
	err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		// Query for job with domain join
		err := tx.QueryRowContext(ctx, `
			SELECT
				j.id, d.name, j.status, j.progress, j.total_tasks, j.completed_tasks, j.failed_tasks, j.skipped_tasks,
				j.created_at, j.started_at, j.completed_at, j.concurrency, j.find_links,
				j.include_paths, j.exclude_paths, j.error_message, j.required_workers,
				j.found_tasks, j.sitemap_tasks, j.duration_seconds, j.avg_time_per_task_seconds,
				j.user_id, j.organisation_id
			FROM jobs j
			JOIN domains d ON j.domain_id = d.id
			WHERE j.id = $1
		`, jobID).Scan(
			&job.ID, &job.Domain, &job.Status, &job.Progress, &job.TotalTasks, &job.CompletedTasks,
			&job.FailedTasks, &job.SkippedTasks, &job.CreatedAt, &startedAt, &completedAt, &job.Concurrency,
			&job.FindLinks, &includePaths, &excludePaths, &errorMessage, &job.RequiredWorkers,
			&job.FoundTasks, &job.SitemapTasks, &job.DurationSeconds, &job.AvgTimePerTaskSeconds,
			&userID, &organisationID,
		)
		return err
	})

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("job %s not found: %w", jobID, err)
	} else if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		return nil, fmt.Errorf("failed to get job: %w", err)
	}

	// Handle nullable fields
	if startedAt.Valid {
		job.StartedAt = startedAt.Time
	}

	if completedAt.Valid {
		job.CompletedAt = completedAt.Time
	}

	if errorMessage.Valid {
		job.ErrorMessage = errorMessage.String
	}

	if userID.Valid {
		job.UserID = &userID.String
	}

	if organisationID.Valid {
		job.OrganisationID = &organisationID.String
	}

	// Parse arrays from JSON
	if len(includePaths) > 0 {
		err = json.Unmarshal(includePaths, &job.IncludePaths)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal include paths: %w", err)
		}
	}

	if len(excludePaths) > 0 {
		err = json.Unmarshal(excludePaths, &job.ExcludePaths)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal exclude paths: %w", err)
		}
	}

	return &job, nil
}

// GetJobStatus gets the current status of a job
func (jm *JobManager) GetJobStatus(ctx context.Context, jobID string) (*Job, error) {
	// First cleanup any stuck jobs using dbQueue
	if err := jm.dbQueue.CleanupStuckJobs(ctx); err != nil {
		log.Error().Err(err).Msg("Failed to cleanup stuck jobs during status check")
		// Don't return error, continue with status check
	}

	span := sentry.StartSpan(ctx, "manager.get_job_status")
	defer span.Finish()

	span.SetTag("job_id", jobID)

	job, err := jm.GetJob(ctx, jobID)
	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		return nil, fmt.Errorf("failed to get job: %w", err)
	}

	return job, nil
}

// discoverAndParseSitemaps discovers and parses all sitemaps for a domain
func (jm *JobManager) discoverAndParseSitemaps(ctx context.Context, domain string) ([]string, *crawler.RobotsRules, error) {
	// Use the injected crawler if available, otherwise create a new one
	var sitemapCrawler CrawlerInterface
	if jm.crawler != nil {
		sitemapCrawler = jm.crawler
	} else {
		// Create a crawler config that allows skipping already cached URLs
		crawlerConfig := crawler.DefaultConfig()
		crawlerConfig.SkipCachedURLs = false
		sitemapCrawler = crawler.New(crawlerConfig)
	}

	// Discover sitemaps and robots.txt rules for the domain
	discoveryResult, err := sitemapCrawler.DiscoverSitemapsAndRobots(ctx, domain)
	if err != nil {
		log.Error().
			Err(err).
			Str("domain", domain).
			Msg("Failed to discover sitemaps and robots rules")
		return []string{}, &crawler.RobotsRules{}, err
	}

	sitemaps := discoveryResult.Sitemaps
	robotsRules := discoveryResult.RobotsRules

	// Log discovered sitemaps
	log.Info().
		Str("domain", domain).
		Int("sitemap_count", len(sitemaps)).
		Msg("Sitemaps discovered")

	// Process each sitemap to extract URLs
	var urls []string
	for _, sitemapURL := range sitemaps {
		log.Info().
			Str("sitemap_url", sitemapURL).
			Msg("Processing sitemap")

		sitemapURLs, err := sitemapCrawler.ParseSitemap(ctx, sitemapURL)
		if err != nil {
			log.Warn().
				Err(err).
				Str("sitemap_url", sitemapURL).
				Msg("Error parsing sitemap")
			continue
		}

		log.Info().
			Str("sitemap_url", sitemapURL).
			Int("url_count", len(sitemapURLs)).
			Msg("Parsed URLs from sitemap")

		urls = append(urls, sitemapURLs...)
	}

	return urls, robotsRules, nil
}

// filterURLsAgainstRobots filters URLs against robots.txt rules and path patterns
func (jm *JobManager) filterURLsAgainstRobots(urls []string, robotsRules *crawler.RobotsRules, includePaths, excludePaths []string) []string {
	// Use the injected crawler if available for path filtering
	var filteredURLs []string
	if jm.crawler != nil && (len(includePaths) > 0 || len(excludePaths) > 0) {
		filteredURLs = jm.crawler.FilterURLs(urls, includePaths, excludePaths)
	} else {
		filteredURLs = urls
	}

	// Filter URLs against robots.txt rules
	if robotsRules != nil && len(robotsRules.DisallowPatterns) > 0 {
		var allowedURLs []string
		for _, urlStr := range filteredURLs {
			// Extract path from URL
			if parsedURL, err := url.Parse(urlStr); err == nil {
				path := parsedURL.Path
				if crawler.IsPathAllowed(robotsRules, path) {
					allowedURLs = append(allowedURLs, urlStr)
				} else {
					log.Debug().
						Str("url", urlStr).
						Str("path", path).
						Msg("URL blocked by robots.txt")
				}
			}
		}
		log.Info().
			Int("original_count", len(filteredURLs)).
			Int("allowed_count", len(allowedURLs)).
			Int("blocked_count", len(filteredURLs)-len(allowedURLs)).
			Msg("Filtered URLs against robots.txt rules")
		return allowedURLs
	}

	return filteredURLs
}

// enqueueURLsForJob creates page records and enqueues URLs for a job
func (jm *JobManager) enqueueURLsForJob(ctx context.Context, jobID, domain string, urls []string, sourceType string) error {
	if len(urls) == 0 {
		return nil
	}

	// Get domain ID from the job
	var domainID int
	err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT domain_id FROM jobs WHERE id = $1
		`, jobID).Scan(&domainID)
	})
	if err != nil {
		return fmt.Errorf("failed to get domain ID: %w", err)
	}

	// Create page records and get their IDs
	pageIDs, hosts, paths, err := db.CreatePageRecords(ctx, jm.dbQueue, domainID, domain, urls)
	if err != nil {
		return fmt.Errorf("failed to create page records: %w", err)
	}

	// Prepare pages with priorities
	pagesWithPriority := make([]db.Page, len(pageIDs))
	for i, pageID := range pageIDs {
		pagesWithPriority[i] = db.Page{
			ID:       pageID,
			Host:     hosts[i],
			Path:     paths[i],
			Priority: 0.1, // Default sitemap priority
		}
		// Set homepage priority to 1.000
		if paths[i] == "/" {
			pagesWithPriority[i].Priority = 1.000
			log.Info().
				Str("job_id", jobID).
				Msg("Set homepage priority to 1.000")
		}
	}

	// Use our wrapper function that checks for duplicates
	baseURL := fmt.Sprintf("https://%s", domain)
	if err := jm.EnqueueJobURLs(ctx, jobID, pagesWithPriority, sourceType, baseURL); err != nil {
		return fmt.Errorf("failed to enqueue URLs: %w", err)
	}

	log.Info().
		Str("job_id", jobID).
		Str("domain", domain).
		Int("url_count", len(urls)).
		Str("source_type", sourceType).
		Msg("Added URLs to job queue")

	// Recalculate job statistics after bulk operation
	if err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `SELECT recalculate_job_stats($1)`, jobID)
		return err
	}); err != nil {
		log.Error().
			Err(err).
			Str("job_id", jobID).
			Msg("Failed to recalculate job stats")
	}

	return nil
}

// updateDomainCrawlDelay updates the domain's crawl delay from robots.txt
func (jm *JobManager) updateDomainCrawlDelay(ctx context.Context, domain string, crawlDelay int) {
	if crawlDelay <= 0 {
		return
	}

	if err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE domains
			SET crawl_delay_seconds = $1
			WHERE name = $2
		`, crawlDelay, domain)
		return err
	}); err != nil {
		log.Error().
			Err(err).
			Str("domain", domain).
			Int("crawl_delay", crawlDelay).
			Msg("Failed to update domain with crawl delay")
	} else {
		log.Info().
			Str("domain", domain).
			Int("crawl_delay", crawlDelay).
			Msg("Updated domain with crawl delay from robots.txt")
	}
}

// IsJobComplete checks if all tasks in a job are processed
func (jm *JobManager) IsJobComplete(job *Job) bool {
	// A job is complete when all tasks are either completed, failed, or skipped
	// and the job is currently running
	if job.Status != JobStatusRunning {
		return false
	}

	processedTasks := job.CompletedTasks + job.FailedTasks + job.SkippedTasks
	return processedTasks >= job.TotalTasks
}

// CalculateJobProgress calculates the progress percentage of a job
func (jm *JobManager) CalculateJobProgress(job *Job) float64 {
	if job.TotalTasks == 0 {
		return 0.0
	}

	processedTasks := job.CompletedTasks + job.FailedTasks + job.SkippedTasks
	return float64(processedTasks) / float64(job.TotalTasks) * 100.0
}

// ValidateStatusTransition checks if a status transition is valid
func (jm *JobManager) ValidateStatusTransition(from, to JobStatus) error {
	// Allow restarts from completed/cancelled/failed states
	if to == JobStatusRunning && (from == JobStatusCompleted || from == JobStatusCancelled || from == JobStatusFailed) {
		return nil
	}

	// Normal forward transitions
	validTransitions := map[JobStatus][]JobStatus{
		JobStatusPending:   {JobStatusRunning, JobStatusCancelled},
		JobStatusRunning:   {JobStatusCompleted, JobStatusFailed, JobStatusCancelled},
		JobStatusCompleted: {JobStatusRunning}, // Restart
		JobStatusFailed:    {JobStatusRunning}, // Retry
		JobStatusCancelled: {JobStatusRunning}, // Restart
	}

	allowed, exists := validTransitions[from]
	if !exists {
		return fmt.Errorf("invalid status transition from %s to %s", from, to)
	}

	if slices.Contains(allowed, to) {
		return nil
	}

	return fmt.Errorf("invalid status transition from %s to %s", from, to)
}

// UpdateJobStatus updates the status of a job with appropriate timestamps
func (jm *JobManager) UpdateJobStatus(ctx context.Context, jobID string, status JobStatus) error {
	return jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		var query string
		var args []any

		switch status {
		case JobStatusCompleted:
			query = "UPDATE jobs SET status = $1, completed_at = $2 WHERE id = $3"
			args = []any{string(status), time.Now().UTC(), jobID}
		case JobStatusRunning:
			query = "UPDATE jobs SET status = $1, started_at = $2 WHERE id = $3"
			args = []any{string(status), time.Now().UTC(), jobID}
		default:
			query = "UPDATE jobs SET status = $1 WHERE id = $2"
			args = []any{string(status), jobID}
		}

		_, err := tx.Exec(query, args...)
		return err
	})
}

// updateJobWithError updates a job with an error message
func (jm *JobManager) updateJobWithError(ctx context.Context, jobID, errorMessage string) {
	if updateErr := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE jobs
			SET error_message = $1
			WHERE id = $2
		`, errorMessage, jobID)
		return err
	}); updateErr != nil {
		log.Error().Err(updateErr).Str("job_id", jobID).Msg("Failed to update job with error message")
	}
}

// enqueueFallbackURL creates and enqueues a fallback root URL when no sitemap URLs are found
func (jm *JobManager) enqueueFallbackURL(ctx context.Context, jobID, domain string) error {
	log.Info().
		Str("job_id", jobID).
		Str("domain", domain).
		Msg("No URLs found in sitemap, falling back to root page")

	// Create fallback root URL
	rootURL := fmt.Sprintf("https://%s/", domain)
	fallbackURLs := []string{rootURL}

	if err := jm.enqueueURLsForJob(ctx, jobID, domain, fallbackURLs, "fallback"); err != nil {
		log.Error().
			Err(err).
			Str("job_id", jobID).
			Str("domain", domain).
			Msg("Failed to enqueue fallback root URL")

		// Update job with error
		jm.updateJobWithError(ctx, jobID, fmt.Sprintf("Failed to create fallback task: %v", err))
		return err
	}

	log.Info().
		Str("job_id", jobID).
		Str("domain", domain).
		Msg("Created fallback root page task - job will proceed with link discovery")

	return nil
}

// enqueueSitemapURLs enqueues discovered sitemap URLs for processing
func (jm *JobManager) enqueueSitemapURLs(ctx context.Context, jobID, domain string, urls []string) error {
	// Log URLs for debugging
	for i, url := range urls {
		log.Debug().
			Str("job_id", jobID).
			Str("domain", domain).
			Int("index", i).
			Str("url", url).
			Msg("URL from sitemap")
	}

	if err := jm.enqueueURLsForJob(ctx, jobID, domain, urls, "sitemap"); err != nil {
		log.Error().
			Err(err).
			Str("job_id", jobID).
			Str("domain", domain).
			Msg("Failed to enqueue sitemap URLs")
		return err
	}

	return nil
}

// processSitemap fetches and processes a sitemap for a domain
func (jm *JobManager) processSitemap(ctx context.Context, jobID, domain string, includePaths, excludePaths []string) {
	// Guard against nil dependencies (e.g., in test environments)
	if jm.crawler == nil || jm.dbQueue == nil || jm.db == nil {
		log.Warn().
			Str("job_id", jobID).
			Str("domain", domain).
			Msg("Skipping sitemap processing due to missing dependencies")
		return
	}

	span := sentry.StartSpan(ctx, "manager.process_sitemap")
	defer span.Finish()

	span.SetTag("job_id", jobID)
	span.SetTag("domain", domain)

	// Mark job as initialising so the worker pool doesn't prematurely
	// complete it before sitemap URLs have been enqueued.
	if err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE jobs SET status = $1
			WHERE id = $2 AND status = $3
		`, JobStatusInitialising, jobID, JobStatusPending)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("job %s not in expected status %s", jobID, JobStatusPending)
		}
		return nil
	}); err != nil {
		log.Warn().Err(err).Str("job_id", jobID).Msg("Aborting sitemap processing: failed to enter initialising")
		return
	}

	log.Info().
		Str("job_id", jobID).
		Str("domain", domain).
		Msg("Starting sitemap processing")

	// Step 1: Discover and parse sitemaps
	urls, robotsRules, err := jm.discoverAndParseSitemaps(ctx, domain)
	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		log.Error().
			Err(err).
			Str("job_id", jobID).
			Str("domain", domain).
			Msg("Failed to discover sitemaps")

		jm.updateJobWithError(ctx, jobID, fmt.Sprintf("Failed to discover sitemaps: %v", err))
		// Ensure job exits initialising state on error
		_ = jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				UPDATE jobs SET status = $1, completed_at = $2
				WHERE id = $3 AND status IN ($4, $5)
			`, JobStatusFailed, time.Now().UTC(), jobID, JobStatusInitialising, JobStatusPending)
			return err
		})
		return
	}

	// Step 2: Update domain crawl delay if present
	jm.updateDomainCrawlDelay(ctx, domain, robotsRules.CrawlDelay)

	// Step 3: Filter URLs against robots.txt and path patterns
	urls = jm.filterURLsAgainstRobots(urls, robotsRules, includePaths, excludePaths)

	// Step 4: Enqueue URLs in batches or create fallback
	if len(urls) > 0 {
		// Process URLs in batches to avoid database timeouts on large sitemaps
		const batchSize = 1000
		totalBatches := (len(urls) + batchSize - 1) / batchSize

		log.Info().
			Str("job_id", jobID).
			Int("total_urls", len(urls)).
			Int("batch_size", batchSize).
			Int("total_batches", totalBatches).
			Msg("Enqueueing sitemap URLs in batches")

		for i := 0; i < len(urls); i += batchSize {
			end := min(i+batchSize, len(urls))
			batch := urls[i:end]
			batchNum := (i / batchSize) + 1

			if err := jm.enqueueSitemapURLs(ctx, jobID, domain, batch); err != nil {
				log.Warn().
					Err(err).
					Str("job_id", jobID).
					Int("batch_number", batchNum).
					Int("batch_start", i).
					Int("batch_size", len(batch)).
					Msg("Failed to enqueue URL batch, continuing with next batch")
				// Continue to next batch even if one fails
				continue
			}

			log.Info().
				Str("job_id", jobID).
				Int("batch_number", batchNum).
				Int("total_batches", totalBatches).
				Int("urls_enqueued", end).
				Int("total_urls", len(urls)).
				Msg("Enqueued URL batch")
		}
	} else {
		if err := jm.enqueueFallbackURL(ctx, jobID, domain); err != nil {
			return
		}
	}

	// Transition from initialising → pending so the worker pool picks it up
	if err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE jobs SET status = $1
			WHERE id = $2 AND status = $3
		`, JobStatusPending, jobID, JobStatusInitialising)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("job %s no longer in initialising state (may have been cancelled)", jobID)
		}
		return nil
	}); err != nil {
		log.Warn().Err(err).Str("job_id", jobID).Msg("Failed to transition job from initialising to pending")
		return
	}

	// Notify workers immediately that new tasks are available
	if jm.workerPool != nil {
		jm.workerPool.NotifyNewTasks()
	}
}
