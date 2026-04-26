// Package archive uploads task HTML to cold object storage (R2, S3, B2)
// and exposes the canonical key layout used by the live HTML persister.
//
// The legacy hot-to-cold sweep (Supabase Storage → R2) was removed once
// R2 became the hot store. Only the cold-storage provider plumbing and
// the path constructors remain.
package archive

import (
	"fmt"
	"os"
	"strings"
)

// Config controls the cold-storage provider used by the HTML persister.
type Config struct {
	Provider string // "r2", "s3", "b2"
	Bucket   string // cold-storage bucket name
}

// DefaultConfig returns sensible defaults, overridable via environment.
func DefaultConfig() Config {
	return Config{
		Provider: "r2",
		Bucket:   "native-hover-archive",
	}
}

// ConfigFromEnv builds a Config from ARCHIVE_* environment variables.
// Returns nil if ARCHIVE_PROVIDER or ARCHIVE_BUCKET is unset (feature disabled).
func ConfigFromEnv() *Config {
	provider := os.Getenv("ARCHIVE_PROVIDER")
	if provider == "" {
		return nil
	}
	bucket := os.Getenv("ARCHIVE_BUCKET")
	if bucket == "" {
		return nil
	}

	cfg := DefaultConfig()
	cfg.Provider = provider
	cfg.Bucket = bucket
	return &cfg
}

// TaskHTMLObjectPath returns the canonical object path for a task HTML blob.
//
// When ARCHIVE_PATH_PREFIX is set, it is prepended (with a single "/" join)
// so review-app deployments can land in their own R2 sub-tree without
// touching the production bucket layout — e.g. ARCHIVE_PATH_PREFIX=347 on
// a review app produces "347/jobs/<job>/tasks/<task>/page-content.html.gz".
// Empty prefix preserves the original production path exactly.
func TaskHTMLObjectPath(jobID, taskID string) string {
	base := fmt.Sprintf("jobs/%s/tasks/%s/page-content.html.gz", jobID, taskID)
	prefix := strings.Trim(strings.TrimSpace(os.Getenv("ARCHIVE_PATH_PREFIX")), "/")
	if prefix == "" {
		return base
	}
	return prefix + "/" + base
}

// ColdKey returns the canonical cold-storage object key for a task HTML blob.
func ColdKey(jobID, taskID string) string {
	return TaskHTMLObjectPath(jobID, taskID)
}

// LighthouseObjectPath returns the canonical object path for a
// lighthouse audit's gzipped JSON report. When taskID is non-empty the
// report is co-located with the matching crawl artefact under
// "jobs/{jobID}/tasks/{taskID}/lighthouse-{profile}.json.gz" so the
// two blobs can be discovered together. When taskID is empty (the
// parent task was deleted via ON DELETE SET NULL on
// lighthouse_runs.source_task_id) the path falls back to
// "jobs/{jobID}/runs/{runID}/lighthouse-{profile}.json.gz" so the
// audit is still archived.
//
// ARCHIVE_PATH_PREFIX prepends in the same way as TaskHTMLObjectPath so
// review-app deployments stay siloed from production.
func LighthouseObjectPath(jobID, taskID, profile string, runID int64) string {
	var base string
	if taskID == "" {
		base = fmt.Sprintf("jobs/%s/runs/%d/lighthouse-%s.json.gz", jobID, runID, profile)
	} else {
		base = fmt.Sprintf("jobs/%s/tasks/%s/lighthouse-%s.json.gz", jobID, taskID, profile)
	}
	prefix := strings.Trim(strings.TrimSpace(os.Getenv("ARCHIVE_PATH_PREFIX")), "/")
	if prefix == "" {
		return base
	}
	return prefix + "/" + base
}
