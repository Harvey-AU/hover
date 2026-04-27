// Package archive uploads task HTML to cold object storage (R2, S3, B2)
// and exposes the canonical key layout used by the HTML persister.
package archive

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Provider string // "r2", "s3", "b2"
	Bucket   string
}

func DefaultConfig() Config {
	return Config{
		Provider: "r2",
		Bucket:   "native-hover-archive",
	}
}

// Returns nil when ARCHIVE_PROVIDER or ARCHIVE_BUCKET is unset (feature disabled).
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

// ARCHIVE_PATH_PREFIX (when set) is prepended with a "/" join so
// review-app deployments stay siloed from the production bucket layout.
func TaskHTMLObjectPath(jobID, taskID string) string {
	base := fmt.Sprintf("jobs/%s/tasks/%s/page-content.html.gz", jobID, taskID)
	prefix := strings.Trim(strings.TrimSpace(os.Getenv("ARCHIVE_PATH_PREFIX")), "/")
	if prefix == "" {
		return base
	}
	return prefix + "/" + base
}

func ColdKey(jobID, taskID string) string {
	return TaskHTMLObjectPath(jobID, taskID)
}

// Empty taskID (parent deleted via ON DELETE SET NULL on
// lighthouse_runs.source_task_id) falls back to a run-id keyed path so
// the audit is still archived.
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
