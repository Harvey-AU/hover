// Package archive moves aged data from hot storage (Supabase) to cold
// storage (R2, S3, B2, etc.) to stay within quota limits.
package archive

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config controls the archiver's runtime behaviour.
type Config struct {
	Provider      string        // "r2", "s3", "b2"
	Bucket        string        // cold-storage bucket name
	RetentionJobs int           // keep this many recent jobs hot per domain/org
	Interval      time.Duration // time between archive sweeps
	BatchSize     int           // candidates per sweep
	Concurrency   int           // parallel upload workers
}

// ArchiveCandidate represents a single item eligible for archival.
type ArchiveCandidate struct {
	TaskID              string
	JobID               string
	StorageBucket       string
	StoragePath         string
	SHA256              string
	CompressedSizeBytes int64
	ContentType         string
	ContentEncoding     string
}

// ArchiveSource abstracts a category of data that can be archived.
// Each implementation knows how to find candidates and mark them done.
type ArchiveSource interface {
	// Name returns a human-readable label, e.g. "task_html".
	Name() string
	// FindCandidates returns up to batchSize items eligible for archival.
	FindCandidates(ctx context.Context, batchSize int) ([]ArchiveCandidate, error)
	// OnArchived is called after the candidate has been safely persisted
	// in cold storage and verified.
	OnArchived(ctx context.Context, candidate ArchiveCandidate, provider, bucket, key string) error
}

// DefaultConfig returns sensible defaults, overridable via environment.
func DefaultConfig() Config {
	return Config{
		Provider:      "r2",
		Bucket:        "hover-archive",
		RetentionJobs: 3,
		Interval:      1 * time.Hour,
		BatchSize:     50,
		Concurrency:   5,
	}
}

// ConfigFromEnv builds a Config from ARCHIVE_* environment variables.
// Returns nil if ARCHIVE_PROVIDER is unset (feature disabled).
func ConfigFromEnv() *Config {
	provider := os.Getenv("ARCHIVE_PROVIDER")
	if provider == "" {
		return nil
	}

	cfg := DefaultConfig()
	cfg.Provider = provider

	if v := os.Getenv("ARCHIVE_BUCKET"); v != "" {
		cfg.Bucket = v
	}
	if v := os.Getenv("ARCHIVE_RETENTION_JOBS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.RetentionJobs = n
		}
	}
	if v := os.Getenv("ARCHIVE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Interval = d
		}
	}
	if v := os.Getenv("ARCHIVE_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.BatchSize = n
		}
	}
	if v := os.Getenv("ARCHIVE_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Concurrency = n
		}
	}

	return &cfg
}

// ColdKey builds the canonical object key for a task HTML blob.
func ColdKey(jobID, storagePath, taskID string) string {
	return fmt.Sprintf("jobs/%s/tasks/%s/%s.html.gz", jobID, storagePath, taskID)
}
