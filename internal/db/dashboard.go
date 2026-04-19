package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// JobStats represents job statistics for the dashboard
type JobStats struct {
	TotalJobs         int     `json:"total_jobs"`
	RunningJobs       int     `json:"running_jobs"`
	CompletedJobs     int     `json:"completed_jobs"`
	FailedJobs        int     `json:"failed_jobs"`
	TotalTasks        int     `json:"total_tasks"`
	AvgCompletionTime float64 `json:"avg_completion_time"`
}

// ActivityPoint represents a data point for activity charts
type ActivityPoint struct {
	Timestamp  string `json:"timestamp"`
	JobsCount  int    `json:"jobs_count"`
	TasksCount int    `json:"tasks_count"`
}

// GetJobStats retrieves job statistics for the dashboard
func (db *DB) GetJobStats(organisationID string, startDate, endDate *time.Time) (*JobStats, error) {
	query := `
		SELECT
			COUNT(*) as total_jobs,
			COALESCE(SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END), 0) as running_jobs,
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0) as completed_jobs,
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) as failed_jobs,
			COALESCE(SUM(COALESCE(total_tasks, 0)), 0) as total_tasks,
			AVG(
				CASE WHEN status = 'completed' AND started_at IS NOT NULL AND completed_at IS NOT NULL 
				THEN EXTRACT(EPOCH FROM (completed_at - started_at))
				ELSE NULL END
			) as avg_completion_time
		FROM jobs 
		WHERE organisation_id = $1`

	args := []any{organisationID}
	argCount := 1

	// Add date filtering if provided
	if startDate != nil {
		argCount++
		query += fmt.Sprintf(" AND created_at >= $%d", argCount)
		args = append(args, *startDate)
	}
	if endDate != nil {
		argCount++
		query += fmt.Sprintf(" AND created_at <= $%d", argCount)
		args = append(args, *endDate)
	}

	var stats JobStats
	var avgCompletionTime sql.NullFloat64

	err := db.client.QueryRow(query, args...).Scan(
		&stats.TotalJobs,
		&stats.RunningJobs,
		&stats.CompletedJobs,
		&stats.FailedJobs,
		&stats.TotalTasks,
		&avgCompletionTime,
	)

	if err != nil {
		dbLog.Error("Dashboard stats query failed", "error", err, "org_id", organisationID)
		return nil, err
	}

	if avgCompletionTime.Valid {
		stats.AvgCompletionTime = avgCompletionTime.Float64
	}

	return &stats, nil
}

// GetJobActivity retrieves job activity data for charts
func (db *DB) GetJobActivity(organisationID string, startDate, endDate *time.Time) ([]ActivityPoint, error) {
	// Determine the appropriate time grouping based on date range
	var timeGroup string
	var intervalStr string

	if startDate != nil && endDate != nil {
		duration := endDate.Sub(*startDate)
		if duration <= 24*time.Hour {
			// Less than 1 day: group by hour
			timeGroup = "DATE_TRUNC('hour', created_at)"
			intervalStr = "1 hour"
		} else if duration <= 7*24*time.Hour {
			// Less than 1 week: group by 6 hours
			timeGroup = "DATE_TRUNC('hour', created_at) + INTERVAL '6 hours' * FLOOR(EXTRACT(HOUR FROM created_at) / 6)"
			intervalStr = "6 hours"
		} else if duration <= 30*24*time.Hour {
			// Less than 1 month: group by day
			timeGroup = "DATE_TRUNC('day', created_at)"
			intervalStr = "1 day"
		} else {
			// More than 1 month: group by week
			timeGroup = "DATE_TRUNC('week', created_at)"
			intervalStr = "1 week"
		}
	} else {
		// Default to daily grouping
		timeGroup = "DATE_TRUNC('day', created_at)"
		intervalStr = "1 day"
	}

	// #nosec G201
	query := fmt.Sprintf(`
		WITH time_series AS (
			SELECT generate_series(
				COALESCE($2, DATE_TRUNC('day', NOW() - INTERVAL '7 days')),
				COALESCE($3, DATE_TRUNC('day', NOW())),
				INTERVAL '%s'
			) as time_bucket
		),
		job_activity AS (
			SELECT 
				%s as time_bucket,
				COUNT(*) as jobs_count,
				SUM(COALESCE(total_tasks, 0)) as tasks_count
			FROM jobs 
			WHERE organisation_id = $1`, intervalStr, timeGroup)

	args := []any{organisationID, startDate, endDate}
	argCount := 3

	// Add date filtering if provided
	if startDate != nil {
		argCount++
		query += fmt.Sprintf(" AND created_at >= $%d", argCount)
		args = append(args, *startDate)
	}
	if endDate != nil {
		argCount++
		query += fmt.Sprintf(" AND created_at <= $%d", argCount)
		args = append(args, *endDate)
	}
	//nolint:gosec // intervalStr and timeGroup are internal logic constants
	query += `
			GROUP BY ` + timeGroup + `
		)
		SELECT 
			ts.time_bucket,
			COALESCE(ja.jobs_count, 0) as jobs_count,
			COALESCE(ja.tasks_count, 0) as tasks_count
		FROM time_series ts
		LEFT JOIN job_activity ja ON ts.time_bucket = ja.time_bucket
		ORDER BY ts.time_bucket`

	rows, err := db.client.Query(query, args...)
	if err != nil {
		dbLog.Error("Failed to get job activity", "error", err, "organisation_id", organisationID)
		return nil, err
	}
	defer rows.Close()

	var activity []ActivityPoint
	for rows.Next() {
		var point ActivityPoint
		var timestamp time.Time

		err := rows.Scan(&timestamp, &point.JobsCount, &point.TasksCount)
		if err != nil {
			dbLog.Error("Failed to scan activity row", "error", err)
			continue
		}

		point.Timestamp = timestamp.Format(time.RFC3339)
		activity = append(activity, point)
	}

	if err = rows.Err(); err != nil {
		dbLog.Error("Error iterating activity rows", "error", err)
		return nil, err
	}

	return activity, nil
}

// JobListItem represents a job in the list view
type JobListItem struct {
	ID                    string         `json:"id"`
	Status                string         `json:"status"`
	Progress              float64        `json:"progress"`
	TotalTasks            int            `json:"total_tasks"`
	CompletedTasks        int            `json:"completed_tasks"`
	FailedTasks           int            `json:"failed_tasks"`
	SkippedTasks          int            `json:"skipped_tasks"`
	SitemapTasks          int            `json:"sitemap_tasks"`
	FoundTasks            int            `json:"found_tasks"`
	CreatedAt             string         `json:"created_at"`
	StartedAt             *string        `json:"started_at,omitempty"`
	CompletedAt           *string        `json:"completed_at,omitempty"`
	Domain                *string        `json:"domains,omitempty"` // For compatibility with frontend
	DurationSeconds       *int           `json:"duration_seconds,omitempty"`
	AvgTimePerTaskSeconds *float64       `json:"avg_time_per_task_seconds,omitempty"`
	Stats                 map[string]any `json:"stats,omitempty"`
}

// Domain represents the domain information for jobs
type Domain struct {
	Name string `json:"name"`
}

// JobWithDomain represents a job with domain information
type JobWithDomain struct {
	JobListItem
	Domains *Domain `json:"domains"`
}

// ListJobs retrieves a paginated list of jobs for an organisation
func (db *DB) ListJobs(organisationID string, limit, offset int, status, dateRange, timezone string) ([]JobWithDomain, int, error) {
	// Build the base query
	baseQuery := `
		FROM jobs j
		LEFT JOIN domains d ON j.domain_id = d.id
		WHERE j.organisation_id = $1`

	args := []any{organisationID}
	argCount := 1

	// Add status filter if provided
	if status != "" {
		argCount++
		baseQuery += fmt.Sprintf(" AND j.status = $%d", argCount)
		args = append(args, status)
	}

	// Add date range filter if provided
	if dateRange != "" {
		startDate, endDate := calculateDateRangeForList(dateRange, timezone)
		if startDate != nil {
			argCount++
			baseQuery += fmt.Sprintf(" AND j.created_at >= $%d", argCount)
			args = append(args, *startDate)
			dbLog.Debug("Date range filter start",
				"range", dateRange,
				"timezone", timezone,
				"start_date", *startDate,
				"start_utc", startDate.UTC().Format(time.RFC3339))
		}
		if endDate != nil {
			argCount++
			baseQuery += fmt.Sprintf(" AND j.created_at <= $%d", argCount)
			args = append(args, *endDate)
			dbLog.Debug("Date range filter end",
				"range", dateRange,
				"timezone", timezone,
				"end_date", *endDate,
				"end_utc", endDate.UTC().Format(time.RFC3339))
		}
	}

	// Get total count
	countQuery := "SELECT COUNT(*) " + baseQuery
	var total int
	err := db.client.QueryRow(countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count jobs: %w", err)
	}

	// Get jobs with pagination
	// #nosec G202
	selectQuery := `
	SELECT 
		j.id, j.status, j.progress, j.total_tasks, j.completed_tasks, 
		j.failed_tasks, j.sitemap_tasks, j.found_tasks, j.created_at,
		j.started_at, j.completed_at, d.name as domain_name,
		j.duration_seconds,
		CASE
			WHEN j.completed_tasks > 0 AND j.duration_seconds IS NOT NULL THEN j.duration_seconds::double precision / NULLIF(j.completed_tasks, 0)
			ELSE NULL
		END AS avg_time_per_task_seconds
	` + baseQuery
	// #nosec G202
	selectQuery += fmt.Sprintf(" ORDER BY j.created_at DESC LIMIT %d OFFSET %d", limit, offset)

	rows, err := db.client.Query(selectQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query jobs: %w", err)
	}
	defer rows.Close()

	var jobs []JobWithDomain
	for rows.Next() {
		var job JobWithDomain
		var startedAt, completedAt sql.NullString
		var domainName sql.NullString

		err := rows.Scan(
			&job.ID, &job.Status, &job.Progress, &job.TotalTasks, &job.CompletedTasks,
			&job.FailedTasks, &job.SitemapTasks, &job.FoundTasks, &job.CreatedAt,
			&startedAt, &completedAt, &domainName,
			&job.DurationSeconds, &job.AvgTimePerTaskSeconds,
		)
		if err != nil {
			dbLog.Error("Failed to scan job row", "error", err)
			continue
		}

		// Handle nullable fields
		if startedAt.Valid {
			job.StartedAt = &startedAt.String
		}
		if completedAt.Valid {
			job.CompletedAt = &completedAt.String
		}
		if domainName.Valid {
			job.Domains = &Domain{Name: domainName.String}
		}

		jobs = append(jobs, job)
	}

	if err = rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("error iterating job rows: %w", err)
	}

	return jobs, total, nil
}

// ListJobsWithOffset lists jobs for an organisation with timezone offset-based date filtering
func (db *DB) ListJobsWithOffset(organisationID string, limit, offset int, status, dateRange string, tzOffsetMinutes int, includeStats bool) ([]JobWithDomain, int, error) {
	// Build the base query
	baseQuery := `
		FROM jobs j
		LEFT JOIN domains d ON j.domain_id = d.id
		WHERE j.organisation_id = $1`

	args := []any{organisationID}
	argCount := 1

	// Add status filter if provided
	if status != "" {
		argCount++
		baseQuery += fmt.Sprintf(" AND j.status = $%d", argCount)
		args = append(args, status)
	}

	// Add date range filter if provided
	if dateRange != "" {
		startDate, endDate := calculateDateRangeWithOffset(dateRange, tzOffsetMinutes)
		if startDate != nil {
			argCount++
			baseQuery += fmt.Sprintf(" AND j.created_at >= $%d", argCount)
			args = append(args, *startDate)
		}
		if endDate != nil {
			argCount++
			baseQuery += fmt.Sprintf(" AND j.created_at <= $%d", argCount)
			args = append(args, *endDate)
		}
	}

	// Get total count
	countQuery := "SELECT COUNT(*) " + baseQuery
	var total int
	err := db.client.QueryRow(countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count jobs: %w", err)
	}

	// Get jobs with pagination
	// #nosec G202
	selectQuery := `
	SELECT
		j.id, j.status, j.progress, j.total_tasks, j.completed_tasks,
		j.failed_tasks, j.skipped_tasks, j.sitemap_tasks, j.found_tasks, j.created_at,
		j.started_at, j.completed_at, d.name as domain_name,
		j.duration_seconds,
		CASE
			WHEN j.completed_tasks > 0 AND j.duration_seconds IS NOT NULL THEN j.duration_seconds::double precision / NULLIF(j.completed_tasks, 0)
			ELSE NULL
		END AS avg_time_per_task_seconds
	` + baseQuery

	if includeStats {
		selectQuery = `
	SELECT
		j.id, j.status, j.progress, j.total_tasks, j.completed_tasks,
		j.failed_tasks, j.skipped_tasks, j.sitemap_tasks, j.found_tasks, j.created_at,
		j.started_at, j.completed_at, d.name as domain_name,
		j.duration_seconds,
		CASE
			WHEN j.completed_tasks > 0 AND j.duration_seconds IS NOT NULL THEN j.duration_seconds::double precision / NULLIF(j.completed_tasks, 0)
			ELSE NULL
		END AS avg_time_per_task_seconds,
		j.stats
	` + baseQuery
	}
	// #nosec G202
	selectQuery += fmt.Sprintf(" ORDER BY j.created_at DESC LIMIT %d OFFSET %d", limit, offset)

	rows, err := db.client.Query(selectQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query jobs: %w", err)
	}
	defer rows.Close()

	var jobs []JobWithDomain
	for rows.Next() {
		var job JobWithDomain
		var startedAt, completedAt sql.NullString
		var domainName sql.NullString
		var statsJSON []byte

		scanArgs := []any{
			&job.ID, &job.Status, &job.Progress, &job.TotalTasks, &job.CompletedTasks,
			&job.FailedTasks, &job.SkippedTasks, &job.SitemapTasks, &job.FoundTasks, &job.CreatedAt,
			&startedAt, &completedAt, &domainName,
			&job.DurationSeconds, &job.AvgTimePerTaskSeconds,
		}

		if includeStats {
			scanArgs = append(scanArgs, &statsJSON)
		}

		err := rows.Scan(scanArgs...)
		if err != nil {
			dbLog.Error("Failed to scan job row", "error", err)
			continue
		}

		// Handle nullable fields
		if startedAt.Valid {
			job.StartedAt = &startedAt.String
		}
		if completedAt.Valid {
			job.CompletedAt = &completedAt.String
		}
		if domainName.Valid {
			job.Domains = &Domain{Name: domainName.String}
		}

		if includeStats && len(statsJSON) > 0 {
			var stats map[string]any
			if err := json.Unmarshal(statsJSON, &stats); err == nil {
				job.Stats = stats
			}
		}

		jobs = append(jobs, job)
	}

	if err = rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("error iterating job rows: %w", err)
	}

	return jobs, total, nil
}

// calculateDateRangeWithOffset calculates date range using UTC offset in minutes
// offsetMinutes: negative for ahead of UTC (e.g., -660 for AEDT/UTC+11), positive for behind
func calculateDateRangeWithOffset(dateRange string, offsetMinutes int) (*time.Time, *time.Time) {
	// Get current time in UTC
	now := time.Now().UTC()

	// Create a fixed offset location for user's timezone
	// JavaScript returns negative for east of UTC, Go wants positive, so negate
	userLoc := time.FixedZone("User", -offsetMinutes*60)

	// Get current time in user's timezone
	userNow := now.In(userLoc)

	var startDate, endDate *time.Time

	switch dateRange {
	case "last_hour":
		// Rolling 1 hour window in UTC
		start := now.Add(-1 * time.Hour)
		startDate = &start
		endDate = &now
	case "today":
		// Calendar day boundaries in user's timezone, converted to UTC
		startLocal := time.Date(userNow.Year(), userNow.Month(), userNow.Day(), 0, 0, 0, 0, userLoc)
		endLocal := time.Date(userNow.Year(), userNow.Month(), userNow.Day(), 23, 59, 59, 999999999, userLoc)
		startUTC := startLocal.UTC()
		endUTC := endLocal.UTC()
		startDate = &startUTC
		endDate = &endUTC
	case "last_24_hours":
		// Rolling 24 hour window in UTC
		start := now.Add(-24 * time.Hour)
		startDate = &start
		endDate = &now
	case "yesterday":
		// Previous calendar day in user's timezone, converted to UTC
		yesterday := userNow.AddDate(0, 0, -1)
		startLocal := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, userLoc)
		endLocal := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 23, 59, 59, 999999999, userLoc)
		startUTC := startLocal.UTC()
		endUTC := endLocal.UTC()
		startDate = &startUTC
		endDate = &endUTC
	case "7days", "last7":
		start := now.AddDate(0, 0, -7)
		startDate = &start
		endDate = &now
	case "30days", "last30":
		start := now.AddDate(0, 0, -30)
		startDate = &start
		endDate = &now
	case "last90":
		start := now.AddDate(0, 0, -90)
		startDate = &start
		endDate = &now
	case "all":
		return nil, nil
	default:
		// Default to today
		startLocal := time.Date(userNow.Year(), userNow.Month(), userNow.Day(), 0, 0, 0, 0, userLoc)
		endLocal := time.Date(userNow.Year(), userNow.Month(), userNow.Day(), 23, 59, 59, 999999999, userLoc)
		startUTC := startLocal.UTC()
		endUTC := endLocal.UTC()
		startDate = &startUTC
		endDate = &endUTC
	}

	return startDate, endDate
}

// calculateDateRangeForList is a helper function for list queries
func calculateDateRangeForList(dateRange, timezone string) (*time.Time, *time.Time) {
	// Map common timezone aliases to canonical IANA names
	timezoneAliases := map[string]string{
		"Australia/Melbourne": "Australia/Sydney", // Melbourne uses Sydney timezone (AEST/AEDT)
		"Australia/ACT":       "Australia/Sydney", // ACT uses Sydney timezone
		"Australia/Canberra":  "Australia/Sydney", // Canberra uses Sydney timezone
		"Australia/NSW":       "Australia/Sydney", // NSW uses Sydney timezone
		"Australia/Victoria":  "Australia/Sydney", // Victoria uses Sydney timezone
	}

	// Check if timezone needs aliasing
	if canonical, exists := timezoneAliases[timezone]; exists {
		dbLog.Debug("Mapping timezone alias", "original", timezone, "canonical", canonical)
		timezone = canonical
	}

	// Load timezone location, fall back to UTC if invalid
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		dbLog.Warn("Invalid timezone, falling back to UTC", "error", err, "timezone", timezone)
		loc = time.UTC
	}

	// Get current time in user's timezone
	now := time.Now().In(loc)
	var startDate, endDate *time.Time

	switch dateRange {
	case "last_hour":
		// Rolling 1 hour window from now
		start := now.Add(-1 * time.Hour)
		startDate = &start
		endDate = &now
	case "today":
		// Calendar day boundaries in user's timezone
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		end := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 999999999, loc)
		startDate = &start
		endDate = &end
	case "last_24_hours":
		// Rolling 24 hour window from now
		start := now.Add(-24 * time.Hour)
		startDate = &start
		endDate = &now
	case "yesterday":
		// Previous calendar day in user's timezone
		yesterday := now.AddDate(0, 0, -1)
		start := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, loc)
		end := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 23, 59, 59, 999999999, loc)
		startDate = &start
		endDate = &end
	case "7days", "last7":
		// Last 7 days from now
		start := now.AddDate(0, 0, -7)
		startDate = &start
		endDate = &now
	case "30days", "last30":
		// Last 30 days from now
		start := now.AddDate(0, 0, -30)
		startDate = &start
		endDate = &now
	case "last90":
		// Last 90 days from now
		start := now.AddDate(0, 0, -90)
		startDate = &start
		endDate = &now
	case "all":
		// Return nil for both to indicate no date filtering
		return nil, nil
	default:
		// Default to today in user's timezone
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		end := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 999999999, loc)
		startDate = &start
		endDate = &end
	}

	return startDate, endDate
}

// SlowPage represents a slow-loading page for dashboard analysis
type SlowPage struct {
	URL                string  `json:"url"`
	Domain             string  `json:"domain"`
	Host               *string `json:"host,omitempty"`
	Path               string  `json:"path"`
	SecondResponseTime int64   `json:"second_response_time"` // milliseconds after cache retry
	JobID              string  `json:"job_id"`
	CompletedAt        string  `json:"completed_at"`
}

// ExternalRedirect represents a page that redirects to an external domain
type ExternalRedirect struct {
	URL         string  `json:"url"`
	Domain      string  `json:"domain"`
	Host        *string `json:"host,omitempty"`
	Path        string  `json:"path"`
	RedirectURL string  `json:"redirect_url"`
	JobID       string  `json:"job_id"`
	CompletedAt string  `json:"completed_at"`
}

// GetSlowPages retrieves the slowest pages after cache retry attempts
// Returns top 10 absolute slowest and 10% slowest from user's organisation
func (db *DB) GetSlowPages(organisationID string, startDate, endDate *time.Time) ([]SlowPage, error) {
	query := `
		WITH user_tasks AS (
			SELECT 
				'https://' || p.host || p.path as url,
				d.name as domain,
				CASE WHEN COALESCE(dh.host_count, 1) > 1 THEN p.host ELSE NULL END as host,
				p.path,
				t.second_response_time,
				t.job_id,
				t.completed_at
			FROM tasks t
			JOIN jobs j ON t.job_id = j.id
			JOIN pages p ON t.page_id = p.id
			JOIN domains d ON p.domain_id = d.id
			LEFT JOIN (
				SELECT domain_id, COUNT(DISTINCT LOWER(REGEXP_REPLACE(host, '^www\\.', '')))::int AS host_count
				FROM domain_hosts
				GROUP BY domain_id
			) dh ON dh.domain_id = p.domain_id
			WHERE j.organisation_id = $1
				AND t.status = 'completed'
				AND t.second_response_time IS NOT NULL
				AND t.second_response_time > 0
				AND ($2::timestamp IS NULL OR t.completed_at >= $2)
				AND ($3::timestamp IS NULL OR t.completed_at <= $3)
		),
		top_10_absolute AS (
			SELECT *, 'absolute' as category
			FROM user_tasks
			ORDER BY second_response_time DESC
			LIMIT 10
		),
		slowest_percentile AS (
			SELECT *, 'percentile' as category
			FROM user_tasks
			WHERE second_response_time >= (
				SELECT PERCENTILE_CONT(0.9) WITHIN GROUP (ORDER BY second_response_time)
				FROM user_tasks
			)
			ORDER BY second_response_time DESC
			LIMIT 10
		)
		SELECT DISTINCT 
			url, domain, host, path, second_response_time, job_id, 
			completed_at::timestamp AT TIME ZONE 'UTC' as completed_at
		FROM (
			SELECT * FROM top_10_absolute
			UNION ALL
			SELECT * FROM slowest_percentile
		) combined
		ORDER BY second_response_time DESC
		LIMIT 20;
	`

	rows, err := db.client.Query(query, organisationID, startDate, endDate)
	if err != nil {
		dbLog.Error("Failed to query slow pages", "error", err)
		return nil, err
	}
	defer rows.Close()

	var slowPages []SlowPage
	for rows.Next() {
		var page SlowPage
		var completedAt sql.NullTime
		var host sql.NullString

		err := rows.Scan(
			&page.URL,
			&page.Domain,
			&host,
			&page.Path,
			&page.SecondResponseTime,
			&page.JobID,
			&completedAt,
		)
		if err != nil {
			dbLog.Error("Failed to scan slow page row", "error", err)
			return nil, err
		}

		if completedAt.Valid {
			page.CompletedAt = completedAt.Time.Format(time.RFC3339)
		}
		if host.Valid {
			h := host.String
			page.Host = &h
		}

		slowPages = append(slowPages, page)
	}

	if err = rows.Err(); err != nil {
		dbLog.Error("Error iterating slow pages rows", "error", err)
		return nil, err
	}

	return slowPages, nil
}

// GetExternalRedirects retrieves pages that redirect to external domains
func (db *DB) GetExternalRedirects(organisationID string, startDate, endDate *time.Time) ([]ExternalRedirect, error) {
	query := `
		SELECT 
			'https://' || p.host || p.path as url,
			d.name as domain,
			CASE WHEN COALESCE(dh.host_count, 1) > 1 THEN p.host ELSE NULL END as host,
			p.path,
			t.redirect_url,
			t.job_id,
			t.completed_at::timestamp AT TIME ZONE 'UTC' as completed_at
		FROM tasks t
		JOIN jobs j ON t.job_id = j.id
		JOIN pages p ON t.page_id = p.id
		JOIN domains d ON p.domain_id = d.id
		LEFT JOIN (
			SELECT domain_id, COUNT(DISTINCT LOWER(REGEXP_REPLACE(host, '^www\\.', '')))::int AS host_count
			FROM domain_hosts
			GROUP BY domain_id
		) dh ON dh.domain_id = p.domain_id
		WHERE j.organisation_id = $1
			AND t.status = 'completed'
			AND t.redirect_url IS NOT NULL
			AND t.redirect_url != ''
			-- Check if redirect URL is external (different domain)
			AND NOT (
				t.redirect_url LIKE 'http://' || d.name || '%' OR
				t.redirect_url LIKE 'https://' || d.name || '%' OR
				t.redirect_url LIKE '//%' || d.name || '%' OR
				t.redirect_url LIKE '/' || '%'  -- relative paths
			)
			AND ($2::timestamp IS NULL OR t.completed_at >= $2)
			AND ($3::timestamp IS NULL OR t.completed_at <= $3)
		ORDER BY t.completed_at DESC
		LIMIT 100;
	`

	rows, err := db.client.Query(query, organisationID, startDate, endDate)
	if err != nil {
		dbLog.Error("Failed to query external redirects", "error", err)
		return nil, err
	}
	defer rows.Close()

	var redirects []ExternalRedirect
	for rows.Next() {
		var redirect ExternalRedirect
		var completedAt sql.NullTime
		var host sql.NullString

		err := rows.Scan(
			&redirect.URL,
			&redirect.Domain,
			&host,
			&redirect.Path,
			&redirect.RedirectURL,
			&redirect.JobID,
			&completedAt,
		)
		if err != nil {
			dbLog.Error("Failed to scan external redirect row", "error", err)
			return nil, err
		}

		if completedAt.Valid {
			redirect.CompletedAt = completedAt.Time.Format(time.RFC3339)
		}
		if host.Valid {
			h := host.String
			redirect.Host = &h
		}

		redirects = append(redirects, redirect)
	}

	if err = rows.Err(); err != nil {
		dbLog.Error("Error iterating external redirects rows", "error", err)
		return nil, err
	}

	return redirects, nil
}
