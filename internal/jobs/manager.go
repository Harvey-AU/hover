package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Harvey-AU/hover/internal/crawler"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/logging"
	"github.com/Harvey-AU/hover/internal/util"
	"github.com/getsentry/sentry-go"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

var jobsLog = logging.Component("jobs")

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

	// GetRobotsRules fetches parsed robots.txt rules for a domain.
	// Used by worker-side link-discovery filtering. Returns nil (not an
	// error) when the crawler is unavailable — callers treat nil rules
	// as "no restriction", matching the historical behaviour.
	GetRobotsRules(ctx context.Context, domain string) (*crawler.RobotsRules, error)
}

// TaskScheduleCallback is called after tasks are successfully inserted
// into Postgres. The callback receives the data needed to schedule
// the tasks into an external broker (e.g. Redis). If nil, no external
// scheduling occurs (legacy DB-queue mode).
type TaskScheduleCallback func(ctx context.Context, jobID string, entries []TaskScheduleEntry)

// TaskScheduleEntry carries the minimal data needed to schedule a
// newly inserted task into the Redis ZSET.
//
// RunAt is the earliest time at which the task may be dispatched. For
// freshly created pending tasks this is typically "now", but for
// waiting/retry rows it carries the backoff deadline so callbacks do
// not mistakenly schedule them as ready-now.
type TaskScheduleEntry struct {
	TaskID     string
	PageID     int
	Host       string
	Path       string
	Status     string // "pending", "waiting", "skipped"
	Priority   float64
	RetryCount int
	SourceType string
	SourceURL  string
	RunAt      time.Time
}

// JobManager handles job creation and lifecycle management
type JobManager struct {
	db      *sql.DB
	dbQueue DbQueueProvider
	crawler CrawlerInterface

	// OnTasksEnqueued is called after successful Postgres task insertion.
	// Set by the API server or worker service to schedule tasks into Redis.
	OnTasksEnqueued TaskScheduleCallback

	// OnProgressMilestone is called after a batch flush observes the
	// per-job progress crossing a 10% boundary (floor(new/10) > floor(old/10)).
	// Set by the lighthouse scheduler so audits can be enqueued progressively
	// while the crawl runs. Fire-and-forget: never returns an error and must
	// not block the batch loop. Nil callback is allowed (production deploys
	// without the analysis app, tests).
	OnProgressMilestone ProgressMilestoneCallback

	// lastMilestoneFired is the in-process record of the last 10%
	// boundary that has been signalled per job, gating MaybeFireMilestones
	// against duplicate fires within this replica. Multiple replicas may
	// each fire the same milestone independently — the lighthouse
	// scheduler dedupes via lighthouse_runs.UNIQUE(job_id, page_id), so
	// at-most-once-per-replica is acceptable.
	milestoneMu        sync.Mutex
	lastMilestoneFired map[string]int // jobID -> milestone (0,10,20...100)

	// Map to track which pages have been processed for each job
	processedPages map[string]struct{} // Key format: "jobID_pageID"
	pagesMutex     sync.RWMutex        // Mutex for thread-safe access

	sitemapSem chan struct{} // limits concurrent sitemap batch insertions across all jobs
}

// ProgressMilestoneCallback is invoked when a job's progress crosses a
// 10% boundary. oldPct and newPct are the integer percentages before
// and after the flush that triggered the milestone. The callback is
// fire-and-forget and runs on the batch flusher's goroutine, so
// implementations must return promptly — long-running work belongs in a
// goroutine inside the callback.
type ProgressMilestoneCallback func(ctx context.Context, jobID string, oldPct, newPct int)

// NewJobManager creates a new job manager
func NewJobManager(db *sql.DB, dbQueue DbQueueProvider, crawler CrawlerInterface) *JobManager {
	sitemapConcurrency := sitemapInsertConcurrency()
	return &JobManager{
		db:                 db,
		dbQueue:            dbQueue,
		crawler:            crawler,
		processedPages:     make(map[string]struct{}),
		lastMilestoneFired: make(map[string]int),
		sitemapSem:         make(chan struct{}, sitemapConcurrency),
	}
}

// MaybeFireMilestones inspects each job's current progress and, if its
// 10% milestone has advanced since the last fire on this replica,
// invokes the registered OnProgressMilestone callback once. Designed to
// be wired as the BatchFlushCallback on a db.BatchManager:
//
//	batchMgr.SetOnBatchFlushed(jobsManager.MaybeFireMilestones)
//
// Cheap when no callback is set or when no milestones have changed —
// the path is a single SELECT plus a map lookup per job.
//
// Multi-replica safety: multiple workers may each compute the same
// milestone and fire concurrently. Downstream dedupe (lighthouse_runs
// UNIQUE(job_id, page_id) for the lighthouse case) makes this safe; the
// in-process map merely keeps a single replica from re-firing on every
// flush.
func (jm *JobManager) MaybeFireMilestones(ctx context.Context, jobIDs []string) {
	if jm == nil || jm.OnProgressMilestone == nil || len(jobIDs) == 0 {
		return
	}

	rows, err := jm.db.QueryContext(ctx, `
		SELECT id, total_tasks,
		       COALESCE(completed_tasks, 0) + COALESCE(failed_tasks, 0) + COALESCE(skipped_tasks, 0)
		  FROM jobs
		 WHERE id = ANY($1)
	`, pq.Array(jobIDs))
	if err != nil {
		jobsLog.Warn("milestone progress read failed", "error", err, "job_count", len(jobIDs))
		return
	}
	defer rows.Close()

	type pending struct {
		jobID  string
		oldPct int
		newPct int
	}
	var fires []pending

	for rows.Next() {
		var (
			jobID                  string
			total, finishedTaskCnt int
		)
		if err := rows.Scan(&jobID, &total, &finishedTaskCnt); err != nil {
			jobsLog.Warn("milestone progress scan failed", "error", err)
			continue
		}
		if total <= 0 {
			continue
		}
		// Integer percent (0..100). Saturate at 100 in case triggers ever
		// over-count.
		newPct := finishedTaskCnt * 100 / total
		if newPct > 100 {
			newPct = 100
		}
		newMilestone := (newPct / 10) * 10

		jm.milestoneMu.Lock()
		oldMilestone, ok := jm.lastMilestoneFired[jobID]
		if ok && newMilestone <= oldMilestone {
			jm.milestoneMu.Unlock()
			continue
		}
		// 0% is not a milestone — every fresh job sits at 0% before
		// any tasks complete, and the first batch flush would otherwise
		// emit a spurious (0, 0) callback on the very first observation
		// (where ok is false). The lighthouse scheduler handles 0%
		// fine via the no-completed-tasks branch, but the noise drowns
		// the milestone signal in logs and metrics.
		if newMilestone == 0 {
			jm.lastMilestoneFired[jobID] = 0
			jm.milestoneMu.Unlock()
			continue
		}
		jm.lastMilestoneFired[jobID] = newMilestone
		jm.milestoneMu.Unlock()

		fires = append(fires, pending{
			jobID:  jobID,
			oldPct: oldMilestone, // 0 if first observation
			newPct: newMilestone,
		})
	}
	if err := rows.Err(); err != nil {
		jobsLog.Warn("milestone progress iterate failed", "error", err)
	}

	for _, f := range fires {
		jobsLog.Debug("milestone crossed",
			"job_id", f.jobID, "old_pct", f.oldPct, "new_pct", f.newPct)
		jm.OnProgressMilestone(ctx, f.jobID, f.oldPct, f.newPct)
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
		args := []any{
			"existing_job_id", existingJobID,
			"existing_job_status", existingJobStatus,
			"domain", domain,
		}
		if existingOrgID.Valid {
			args = append(args, "organisation_id", existingOrgID.String)
		}
		if existingUserID.Valid {
			args = append(args, "user_id", existingUserID.String)
		}
		jobsLog.Info("Found existing active job for domain, cancelling it", args...)

		if err := jm.CancelJob(ctx, existingJobID); err != nil {
			jobsLog.Error("Failed to cancel existing job", "error", err, "job_id", existingJobID)
			// Continue with new job creation even if cancellation fails
		}
	} else if err != nil && err != sql.ErrNoRows {
		// Log query error but continue with job creation
		jobsLog.Warn("Error checking for existing jobs", "error", err, "domain", domain)
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

// GetRobotsRules fetches parsed robots.txt rules for a domain via the
// underlying crawler. Returns nil rules (and nil error) when the crawler
// is unavailable, matching the legacy worker behaviour where missing
// rules mean "no restriction".
func (jm *JobManager) GetRobotsRules(ctx context.Context, domain string) (*crawler.RobotsRules, error) {
	if jm.crawler == nil {
		return nil, nil
	}
	result, err := jm.crawler.DiscoverSitemapsAndRobots(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("jobs: fetch robots for %s: %w", domain, err)
	}
	if result == nil {
		return nil, nil
	}
	return result.RobotsRules, nil
}

// validateRootURLAccess checks robots.txt rules and validates root URL access
func (jm *JobManager) validateRootURLAccess(ctx context.Context, job *Job, normalisedDomain string, rootPath string) (*crawler.RobotsRules, error) {
	var robotsRules *crawler.RobotsRules

	if jm.crawler != nil {
		// Use DiscoverSitemapsAndRobots which already includes parsing
		discoveryResult, err := jm.crawler.DiscoverSitemapsAndRobots(ctx, normalisedDomain)
		if err != nil {
			jobsLog.Error("Failed to fetch robots.txt for manual URL", "error", err, "domain", normalisedDomain)

			// Update job with error
			if updateErr := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `
					UPDATE jobs
					SET status = $1, error_message = $2, completed_at = $3
					WHERE id = $4
				`, JobStatusFailed, fmt.Sprintf("Failed to fetch robots.txt: %v", err), time.Now().UTC(), job.ID)
				return err
			}); updateErr != nil {
				jobsLog.Error("Failed to update job status", "error", updateErr, "job_id", job.ID)
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
		jobsLog.Warn("Root path is disallowed by robots.txt, job cannot proceed",
			"job_id", job.ID, "domain", normalisedDomain, "path", rootPath)

		// Update job with error
		if updateErr := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				UPDATE jobs
				SET status = $1, error_message = $2, completed_at = $3
				WHERE id = $4
			`, JobStatusFailed, "Root path (/) is disallowed by robots.txt", time.Now().UTC(), job.ID)
			return err
		}); updateErr != nil {
			jobsLog.Error("Failed to update job status", "error", updateErr, "job_id", job.ID)
		}
		return nil, fmt.Errorf("root path is disallowed by robots.txt")
	}

	return robotsRules, nil
}

// createManualRootTask creates page and task records for the root URL
func (jm *JobManager) createManualRootTask(ctx context.Context, job *Job, domainID int, rootPath string) error {
	var taskID string
	var pageID int
	var taskRunAt time.Time

	err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
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

		// Enqueue the root URL with its page ID.
		// Root/homepage tasks get priority 1.0 so downstream link discovery
		// (which multiplies by 0.9) produces non-zero child priorities.
		const rootPriority = 1.0
		taskID = uuid.New().String()
		now := time.Now().UTC()
		taskRunAt = now
		_, err = tx.ExecContext(ctx, `
			INSERT INTO tasks (
				id, job_id, page_id, host, path, status, created_at, retry_count,
				source_type, source_url, priority_score
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		`, taskID, job.ID, pageID, job.Domain, rootPath, "pending", now, 0, "manual", "", rootPriority)

		if err != nil {
			return fmt.Errorf("failed to enqueue task for root path: %w", err)
		}

		// Mirror into task_outbox so the sweeper can durably push this
		// task into Redis even if the OnTasksEnqueued callback fails.
		if err := db.InsertOutboxRow(ctx, tx, db.OutboxEntry{
			TaskID:     taskID,
			JobID:      job.ID,
			PageID:     pageID,
			Host:       job.Domain,
			Path:       rootPath,
			Priority:   rootPriority,
			RetryCount: 0,
			SourceType: "manual",
			RunAt:      now,
		}); err != nil {
			return fmt.Errorf("failed to insert outbox row for root task: %w", err)
		}

		jm.markPageProcessed(job.ID, pageID)
		return nil
	})

	if err != nil {
		jobsLog.Error("Failed to create and enqueue root URL", "error", err)
		return err
	}

	// Notify Redis broker if configured.
	// RunAt = now: the root task is ready to dispatch immediately.
	if jm.OnTasksEnqueued != nil {
		jm.OnTasksEnqueued(ctx, job.ID, []TaskScheduleEntry{{
			TaskID:     taskID,
			PageID:     pageID,
			Host:       job.Domain,
			Path:       rootPath,
			Status:     "pending",
			Priority:   1.0,
			SourceType: "manual",
			RunAt:      taskRunAt,
		}})
	}

	jobsLog.Info("Added root URL to job queue", "job_id", job.ID, "domain", job.Domain)

	return nil
}

// setupJobURLDiscovery handles URL discovery for the job (sitemap or manual)
func (jm *JobManager) setupJobURLDiscovery(ctx context.Context, job *Job, options *JobOptions, domainID int, normalisedDomain string) error {
	if options.UseSitemap {
		jobID := job.ID
		go func() {
			procCtx, procCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
			defer procCancel()
			jm.processSitemap(procCtx, jobID, normalisedDomain, options.IncludePaths, options.ExcludePaths)
		}()
		return nil
	}

	// Manual root URL creation - process in background for consistency
	// Use detached context with timeout for background processing
	backgroundCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	go func() {
		defer cancel()
		rootPath := "/"
		_, err := jm.validateRootURLAccess(backgroundCtx, job, normalisedDomain, rootPath)
		if err != nil {
			jobsLog.Error("Failed to validate root URL access", "error", err, "job_id", job.ID)
			return // Error already logged and job updated by validateRootURLAccess
		}

		// Create page and task records for the root URL
		if err := jm.createManualRootTask(backgroundCtx, job, domainID, rootPath); err != nil {
			jobsLog.Error("Failed to create manual root task", "error", err, "job_id", job.ID)
			return
		}

		// Note: task notification is handled via OnTasksEnqueued callback (Redis scheduling)
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
		defaultConcurrency := jobDefaultConcurrency()
		jobsLog.Info("Concurrency not specified; using default",
			"domain", normalisedDomain,
			"default_concurrency", defaultConcurrency,
			"source", "GNH_MAX_WORKERS")
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
		return nil, err
	}

	jobsLog.Info("Created new job",
		"job_id", job.ID,
		"domain", job.Domain,
		"use_sitemap", options.UseSitemap,
		"find_links", options.FindLinks,
		"max_pages", options.MaxPages,
	)

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
		jobsLog.Debug("All URLs already processed, skipping", "job_id", jobID, "skipped_urls", len(pages))
		return nil
	}

	jobsLog.Debug("Enqueueing filtered URLs",
		"job_id", jobID,
		"total_urls", len(pages),
		"new_urls", len(filteredPages),
		"skipped_urls", len(pages)-len(filteredPages),
	)

	// Use the filtered lists to enqueue only new pages
	err := jm.dbQueue.EnqueueURLs(ctx, jobID, filteredPages, sourceType, sourceURL)

	// Only mark pages as processed if the enqueue was successful
	if err == nil {

		// Mark all successfully enqueued pages as processed
		for _, page := range filteredPages {
			jm.markPageProcessed(jobID, page.ID)
		}

		// Notify external scheduler (Redis) if configured.
		if jm.OnTasksEnqueued != nil {
			jm.scheduleEnqueuedTasks(ctx, jobID, filteredPages, sourceType, sourceURL)
		}
	} else {
		jobsLog.Error("Failed to enqueue URLs, not marking pages as processed",
			"error", err, "job_id", jobID, "url_count", len(filteredPages))
	}

	return err
}

// scheduleEnqueuedTasks queries for tasks just inserted into Postgres
// and passes them to the OnTasksEnqueued callback for Redis scheduling.
//
// NOTE: This re-queries by page_id which could theoretically pick up
// pre-existing rows on conflict. In practice the ON CONFLICT in
// EnqueueURLs only updates pending/waiting/skipped tasks (same states
// we filter here), so the result set matches what was just inserted.
// A cleaner approach (returning IDs from EnqueueURLs) requires changing
// the DbQueueProvider interface — deferred to Stage 2.
func (jm *JobManager) scheduleEnqueuedTasks(ctx context.Context, jobID string, pages []db.Page, sourceType, sourceURL string) {
	if jm.OnTasksEnqueued == nil || len(pages) == 0 {
		return
	}

	pageIDs := make([]int, 0, len(pages))
	for _, p := range pages {
		if p.ID != 0 {
			pageIDs = append(pageIDs, p.ID)
		}
	}
	if len(pageIDs) == 0 {
		return
	}

	var entries []TaskScheduleEntry
	err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, page_id, host, path, status, priority_score, retry_count, source_type, source_url, run_at
			FROM tasks
			WHERE job_id = $1 AND page_id = ANY($2) AND status IN ('pending', 'waiting')
		`, jobID, pq.Array(pageIDs))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e TaskScheduleEntry
			if err := rows.Scan(&e.TaskID, &e.PageID, &e.Host, &e.Path, &e.Status, &e.Priority, &e.RetryCount, &e.SourceType, &e.SourceURL, &e.RunAt); err != nil {
				return err
			}
			entries = append(entries, e)
		}
		return rows.Err()
	})
	if err != nil {
		jobsLog.Error("failed to query tasks for Redis scheduling", "error", err, "job_id", jobID)
		return
	}

	if len(entries) > 0 {
		jm.OnTasksEnqueued(ctx, jobID, entries)
	}
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
		if err != nil {
			return err
		}

		// Drop any task_outbox rows for this job so the sweeper does
		// not waste work ZADDing tasks whose status has just flipped
		// to skipped. Without this, outbox rows for cancelled jobs
		// linger until their next sweep and inflate the outbox backlog
		// and oldest-age gauges.
		_, err = tx.ExecContext(ctx, `
			DELETE FROM task_outbox WHERE job_id = $1
		`, job.ID)

		return err
	})

	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		jobsLog.Error("Failed to cancel job", "error", err, "job_id", job.ID)
		return fmt.Errorf("failed to cancel job: %w", err)
	}

	// Clear processed pages for this job
	jm.clearProcessedPages(job.ID)

	jobsLog.Debug("Cancelled job", "job_id", job.ID, "domain", job.Domain)

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
		jobsLog.Error("Failed to cleanup stuck jobs during status check", "error", err)
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
		jobsLog.Error("Failed to discover sitemaps and robots rules", "error", err, "domain", domain)
		return []string{}, &crawler.RobotsRules{}, err
	}

	sitemaps := discoveryResult.Sitemaps
	robotsRules := discoveryResult.RobotsRules

	// Log discovered sitemaps
	jobsLog.Info("Sitemaps discovered", "domain", domain, "sitemap_count", len(sitemaps))

	// Process each sitemap to extract URLs
	var urls []string
	for _, sitemapURL := range sitemaps {
		jobsLog.Info("Processing sitemap", "sitemap_url", sitemapURL)

		sitemapURLs, err := sitemapCrawler.ParseSitemap(ctx, sitemapURL)
		if err != nil {
			jobsLog.Warn("Error parsing sitemap", "error", err, "sitemap_url", sitemapURL)
			continue
		}

		jobsLog.Info("Parsed URLs from sitemap", "sitemap_url", sitemapURL, "url_count", len(sitemapURLs))

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
					jobsLog.Debug("URL blocked by robots.txt", "url", urlStr, "path", path)
				}
			}
		}
		jobsLog.Info("Filtered URLs against robots.txt rules",
			"original_count", len(filteredURLs),
			"allowed_count", len(allowedURLs),
			"blocked_count", len(filteredURLs)-len(allowedURLs),
		)
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
			jobsLog.Info("Set homepage priority to 1.000", "job_id", jobID)
		}
	}

	// Use our wrapper function that checks for duplicates
	baseURL := fmt.Sprintf("https://%s", domain)
	if err := jm.EnqueueJobURLs(ctx, jobID, pagesWithPriority, sourceType, baseURL); err != nil {
		return fmt.Errorf("failed to enqueue URLs: %w", err)
	}

	jobsLog.Info("Added URLs to job queue",
		"job_id", jobID, "domain", domain, "url_count", len(urls), "source_type", sourceType)

	// Recalculate job statistics after bulk operation
	if err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `SELECT recalculate_job_stats($1)`, jobID)
		return err
	}); err != nil {
		jobsLog.Error("Failed to recalculate job stats", "error", err, "job_id", jobID)
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
		jobsLog.Error("Failed to update domain with crawl delay", "error", err, "domain", domain, "crawl_delay", crawlDelay)
	} else {
		jobsLog.Info("Updated domain with crawl delay from robots.txt", "domain", domain, "crawl_delay", crawlDelay)
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
		JobStatusCompleted: {JobStatusRunning, JobStatusArchived}, // Restart or archive
		JobStatusFailed:    {JobStatusRunning, JobStatusArchived}, // Retry or archive
		JobStatusCancelled: {JobStatusRunning, JobStatusArchived}, // Restart or archive
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
		jobsLog.Error("Failed to update job with error message", "error", updateErr, "job_id", jobID)
	}
}

// enqueueFallbackURL creates and enqueues a fallback root URL when no sitemap URLs are found
func (jm *JobManager) enqueueFallbackURL(ctx context.Context, jobID, domain string) error {
	jobsLog.Info("No URLs found in sitemap, falling back to root page", "job_id", jobID, "domain", domain)

	// Create fallback root URL
	rootURL := fmt.Sprintf("https://%s/", domain)
	fallbackURLs := []string{rootURL}

	if err := jm.enqueueURLsForJob(ctx, jobID, domain, fallbackURLs, "fallback"); err != nil {
		jobsLog.Error("Failed to enqueue fallback root URL", "error", err, "job_id", jobID, "domain", domain)

		// Update job with error
		jm.updateJobWithError(ctx, jobID, fmt.Sprintf("Failed to create fallback task: %v", err))
		return err
	}

	jobsLog.Info("Created fallback root page task - job will proceed with link discovery",
		"job_id", jobID, "domain", domain)

	return nil
}

// enqueueSitemapURLs enqueues discovered sitemap URLs for processing
func (jm *JobManager) enqueueSitemapURLs(ctx context.Context, jobID, domain string, urls []string) error {
	// Log URLs for debugging
	for i, url := range urls {
		jobsLog.Debug("URL from sitemap", "job_id", jobID, "domain", domain, "index", i, "url", url)
	}

	if err := jm.enqueueURLsForJob(ctx, jobID, domain, urls, "sitemap"); err != nil {
		jobsLog.Error("Failed to enqueue sitemap URLs", "error", err, "job_id", jobID, "domain", domain)
		return err
	}

	return nil
}

// processSitemap fetches and processes a sitemap for a domain
func (jm *JobManager) processSitemap(ctx context.Context, jobID, domain string, includePaths, excludePaths []string) {
	// Guard against nil dependencies (e.g., in test environments)
	if jm.crawler == nil || jm.dbQueue == nil || jm.db == nil {
		jobsLog.Warn("Skipping sitemap processing due to missing dependencies", "job_id", jobID, "domain", domain)
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
		jobsLog.Warn("Aborting sitemap processing: failed to enter initialising", "error", err, "job_id", jobID)
		return
	}

	jobsLog.Info("Starting sitemap processing", "job_id", jobID, "domain", domain)

	// Step 1: Discover and parse sitemaps
	urls, robotsRules, err := jm.discoverAndParseSitemaps(ctx, domain)
	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		jobsLog.Error("Failed to discover sitemaps", "error", err, "job_id", jobID, "domain", domain)

		jm.updateJobWithError(ctx, jobID, fmt.Sprintf("Failed to discover sitemaps: %v", err))
		// Ensure job exits initialising state on error
		if markErr := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				UPDATE jobs SET status = $1, completed_at = $2
				WHERE id = $3 AND status IN ($4, $5)
			`, JobStatusFailed, time.Now().UTC(), jobID, JobStatusInitialising, JobStatusPending)
			return err
		}); markErr != nil {
			jobsLog.Warn("Failed to mark job as failed after sitemap error", "error", markErr, "job_id", jobID)
		}
		return
	}

	// Step 2: Update domain crawl delay if present
	jm.updateDomainCrawlDelay(ctx, domain, robotsRules.CrawlDelay)

	// Step 3: Filter URLs against robots.txt and path patterns
	urls = jm.filterURLsAgainstRobots(urls, robotsRules, includePaths, excludePaths)

	// Step 4: Record filtered sitemap URL count as a snapshot before any tasks are inserted.
	if err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE jobs SET sitemap_tasks_found = $1 WHERE id = $2
		`, len(urls), jobID)
		return err
	}); err != nil {
		jobsLog.Warn("Failed to record sitemap_tasks_found", "error", err, "job_id", jobID, "sitemap_tasks_found", len(urls))
	}

	// Step 5: Enqueue URLs in batches or create fallback
	if len(urls) > 0 {
		// Hoist the homepage to the front so it's in the first batch and gets
		// priority 1.0 scoring regardless of its position in the sitemap.
		urls = hoistHomepageToFront(urls, domain)

		// Throttled batch insertion — small batches with a sleep between each to
		// avoid a write burst that spikes DB pressure and overwhelms Supabase.
		batchSize := sitemapBatchSize()
		batchDelay := sitemapBatchDelay()
		totalBatches := (len(urls) + batchSize - 1) / batchSize

		jobsLog.Info("Enqueueing sitemap URLs in batches",
			"job_id", jobID,
			"total_urls", len(urls),
			"batch_size", batchSize,
			"total_batches", totalBatches,
			"batch_delay_ms", batchDelay,
		)

		firstBatchDone := false

		for i := 0; i < len(urls); i += batchSize {
			end := min(i+batchSize, len(urls))
			batch := urls[i:end]
			batchNum := (i / batchSize) + 1

			select {
			case jm.sitemapSem <- struct{}{}:
			case <-ctx.Done():
				jm.clearInitialisingAfterSitemapCancel(jobID, batchNum)
				jobsLog.Warn("Stopped waiting for sitemap insert slot", "error", ctx.Err(), "job_id", jobID, "batch_number", batchNum)
				return
			}

			err := jm.enqueueSitemapURLs(ctx, jobID, domain, batch)
			<-jm.sitemapSem
			if err != nil {
				jobsLog.Warn("Failed to enqueue URL batch, continuing with next batch",
					"error", err, "job_id", jobID, "batch_number", batchNum,
					"batch_start", i, "batch_size", len(batch),
				)
				// Continue to next batch even if one fails
				continue
			}

			jobsLog.Info("Enqueued URL batch",
				"job_id", jobID,
				"batch_number", batchNum,
				"total_batches", totalBatches,
				"urls_enqueued", end,
				"total_urls", len(urls),
			)

			// After the first successful batch, open the job for workers immediately.
			// Remaining batches continue inserting in the background while workers
			// drain the already-enqueued tasks.
			if !firstBatchDone {
				firstBatchDone = true
				if err := jm.transitionInitialisingToPending(ctx, jobID); err != nil {
					jobsLog.Warn("Failed to open job after first batch", "error", err, "job_id", jobID)
				}
				// Task notification is handled via OnTasksEnqueued callback (Redis scheduling).
			}

			// Throttle between batches to spread DB write load.
			if end < len(urls) {
				time.Sleep(batchDelay)
			}
		}

		// If every batch failed, still exit initialising so the job doesn't get stuck.
		if !firstBatchDone {
			if err := jm.transitionInitialisingToPending(ctx, jobID); err != nil {
				jobsLog.Warn("Failed to transition job from initialising to pending after all batches failed",
					"error", err, "job_id", jobID)
				return
			}
		}
	} else {
		if err := jm.enqueueFallbackURL(ctx, jobID, domain); err != nil {
			return
		}
		// Fallback URL enqueued — open the job for workers.
		if err := jm.transitionInitialisingToPending(ctx, jobID); err != nil {
			jobsLog.Warn("Failed to transition job from initialising to pending after fallback URL",
				"error", err, "job_id", jobID)
			return
		}
	}
}

func (jm *JobManager) clearInitialisingAfterSitemapCancel(jobID string, batchNum int) {
	go func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := jm.dbQueue.Execute(cleanupCtx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(cleanupCtx, `
				UPDATE jobs
				SET status = $1
				WHERE id = $2 AND status = $3
			`, JobStatusPending, jobID, JobStatusInitialising)
			return err
		}); err != nil {
			jobsLog.Warn("Failed to clear initialising state after sitemap cancellation",
				"error", err, "job_id", jobID, "batch_number", batchNum)
		}
	}()
}

// transitionInitialisingToPending moves a job from initialising → pending so the
// worker pool can pick it up. Safe to call mid-sitemap-crawl once the first batch
// of tasks has been inserted.
func (jm *JobManager) transitionInitialisingToPending(ctx context.Context, jobID string) error {
	return jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
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
	})
}

// hoistHomepageToFront moves the root path URL to the front of the list so it
// is guaranteed to be in the first sitemap batch and scored with priority 1.0.
// Sitemaps do not guarantee any ordering so we enforce this explicitly.
func hoistHomepageToFront(urls []string, domain string) []string {
	if len(urls) == 0 {
		return urls
	}
	homepages := []string{
		"https://" + domain + "/",
		"https://" + domain,
		"http://" + domain + "/",
		"http://" + domain,
	}
	for i, u := range urls {
		for _, hp := range homepages {
			if u == hp {
				if i == 0 {
					return urls
				}
				// Swap to front
				result := make([]string, len(urls))
				result[0] = urls[i]
				copy(result[1:], urls[:i])
				copy(result[1+i:], urls[i+1:])
				return result
			}
		}
	}
	return urls
}

// sitemapInsertConcurrency returns the maximum number of jobs that may insert
// sitemap batches concurrently. Configurable via GNH_SITEMAP_CONCURRENCY; defaults to 3.
func sitemapInsertConcurrency() int {
	const defaultConcurrency = 3
	if val := strings.TrimSpace(os.Getenv("GNH_SITEMAP_CONCURRENCY")); val != "" {
		n, err := strconv.Atoi(val)
		if err != nil {
			jobsLog.Warn("Invalid GNH_SITEMAP_CONCURRENCY — using default", "GNH_SITEMAP_CONCURRENCY", val, "error", err)
		} else if n <= 0 {
			jobsLog.Warn("GNH_SITEMAP_CONCURRENCY must be positive — using default", "GNH_SITEMAP_CONCURRENCY", val)
		} else {
			return n
		}
	}
	return defaultConcurrency
}

// sitemapBatchSize returns the number of URLs to insert per sitemap batch.
// Configurable via GNH_SITEMAP_BATCH_SIZE; defaults to 100.
func sitemapBatchSize() int {
	const defaultSize = 100
	if val := strings.TrimSpace(os.Getenv("GNH_SITEMAP_BATCH_SIZE")); val != "" {
		n, err := strconv.Atoi(val)
		if err != nil {
			jobsLog.Warn("Invalid GNH_SITEMAP_BATCH_SIZE — using default", "GNH_SITEMAP_BATCH_SIZE", val, "error", err)
		} else if n <= 0 {
			jobsLog.Warn("GNH_SITEMAP_BATCH_SIZE must be positive — using default", "GNH_SITEMAP_BATCH_SIZE", val)
		} else {
			return n
		}
	}
	return defaultSize
}

// sitemapBatchDelay returns the delay between sitemap insertion batches.
// Configurable via GNH_SITEMAP_BATCH_DELAY_MS; defaults to 200ms.
func sitemapBatchDelay() time.Duration {
	const defaultDelay = 200 * time.Millisecond
	if val := strings.TrimSpace(os.Getenv("GNH_SITEMAP_BATCH_DELAY_MS")); val != "" {
		n, err := strconv.Atoi(val)
		if err != nil {
			jobsLog.Warn("Invalid GNH_SITEMAP_BATCH_DELAY_MS — using default", "GNH_SITEMAP_BATCH_DELAY_MS", val, "error", err)
		} else if n < 0 {
			jobsLog.Warn("GNH_SITEMAP_BATCH_DELAY_MS must be non-negative — using default", "GNH_SITEMAP_BATCH_DELAY_MS", val)
		} else {
			return time.Duration(n) * time.Millisecond
		}
	}
	return defaultDelay
}
