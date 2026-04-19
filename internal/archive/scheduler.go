package archive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Harvey-AU/hover/internal/logging"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

var archiveLog = logging.Component("archive")

const archiveCandidateTimeout = 2 * time.Minute

// storageDownloader abstracts downloading from hot storage (Supabase).
type storageDownloader interface {
	Download(ctx context.Context, bucket, path string) ([]byte, error)
	Delete(ctx context.Context, bucket, path string) error
}

// JobMarkerFunc marks fully-archived jobs. Called at the start of each sweep.
type JobMarkerFunc func(ctx context.Context) (int64, error)

// Archiver runs periodic sweeps to move data from hot to cold storage.
type Archiver struct {
	provider     ColdStorageProvider
	storage      storageDownloader
	sources      []ArchiveSource
	cfg          Config
	markJobsDone JobMarkerFunc
}

// NewArchiver creates an archiver with the given provider, hot-storage client,
// and one or more archive sources.
func NewArchiver(provider ColdStorageProvider, storage storageDownloader, cfg Config, markJobsDone JobMarkerFunc, sources ...ArchiveSource) *Archiver {
	return &Archiver{
		provider:     provider,
		storage:      storage,
		sources:      sources,
		cfg:          cfg,
		markJobsDone: markJobsDone,
	}
}

// Run blocks until ctx is cancelled or stopCh is closed, running archive
// sweeps at the configured interval.
func (a *Archiver) Run(ctx context.Context, stopCh <-chan struct{}) {
	interval := a.cfg.Interval
	if interval <= 0 {
		interval = time.Hour
		archiveLog.Warn("Archive interval was non-positive, using default", "fallback_interval", interval)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	archiveLog.Info("Archive scheduler started",
		"provider", a.cfg.Provider,
		"bucket", a.cfg.Bucket,
		"interval", interval,
		"batch_size", a.cfg.BatchSize,
		"concurrency", a.cfg.Concurrency,
	)

	// Derive a context that is cancelled when stopCh closes, so sweep
	// internals (markJobsDone, FindCandidates, goroutines) all respect shutdown.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-stopCh:
			cancel()
		case <-runCtx.Done():
		}
	}()

	// Run first sweep immediately rather than waiting for the first tick.
	a.sweep(runCtx, stopCh)

	for {
		select {
		case <-ticker.C:
			a.sweep(runCtx, stopCh)
		case <-stopCh:
			archiveLog.Info("Archive scheduler stopping (stop signal)")
			return
		case <-ctx.Done():
			archiveLog.Info("Archive scheduler stopping (context cancelled)")
			return
		}
	}
}

func (a *Archiver) sweep(ctx context.Context, stopCh <-chan struct{}) {
	// Mark jobs as 'archived' when all their HTML has been moved to cold storage.
	if a.markJobsDone != nil {
		if n, err := a.markJobsDone(ctx); err != nil {
			archiveLog.Error("Failed to mark fully archived jobs", "error", err)
		} else if n > 0 {
			archiveLog.Info("Jobs marked as archived", "jobs_marked", n)
		}
	}

	for _, src := range a.sources {
		candidates, err := src.FindCandidates(ctx, a.cfg.BatchSize)
		if err != nil {
			archiveLog.Error("Failed to find archive candidates", "error", err, "source", src.Name())
			continue
		}
		if len(candidates) == 0 {
			archiveLog.Debug("No archive candidates", "source", src.Name())
			continue
		}

		archiveLog.Info("Archiving candidates",
			"source", src.Name(),
			"candidates", len(candidates),
		)

		concurrency := a.cfg.Concurrency
		if concurrency <= 0 {
			concurrency = 1
		}
		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup

		sweepCtx, sweepCancel := context.WithCancel(ctx)
		stopped := false
		for _, c := range candidates {
			// Check for shutdown between dispatches.
			select {
			case <-ctx.Done():
				sweepCancel()
				stopped = true
			case <-stopCh:
				sweepCancel()
				stopped = true
			default:
			}
			if stopped {
				break
			}

			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				sweepCancel()
				stopped = true
			case <-stopCh:
				sweepCancel()
				stopped = true
			}
			if stopped {
				break
			}

			wg.Add(1)
			go func(candidate ArchiveCandidate) {
				defer wg.Done()
				defer func() { <-sem }()
				candidateCtx, cancel := context.WithTimeout(sweepCtx, archiveCandidateTimeout)
				defer cancel()
				a.archiveOne(candidateCtx, src, candidate)
			}(c)
		}

		wg.Wait()
		sweepCancel()
	}
}

// isPermanent404 returns true when an error indicates the object definitively
// does not exist in storage. These are not transient — retrying on subsequent
// sweeps is wasteful.
func isPermanent404(err error) bool {
	if err == nil {
		return false
	}
	// Typed S3/R2 errors (aws-sdk-go-v2)
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	// Supabase storage returns HTTP 400 with a JSON body containing statusCode 404
	// and error "not_found"; match either the parsed field or the raw string.
	msg := err.Error()
	return strings.Contains(msg, "not_found") ||
		strings.Contains(msg, "download failed with status 404")
}

func (a *Archiver) archiveOne(ctx context.Context, src ArchiveSource, c ArchiveCandidate) {
	lg := archiveLog.With("task_id", c.TaskID, "job_id", c.JobID, "source", src.Name())

	// 1. Download from hot storage, falling back to cold storage if the hot
	// copy is already gone (e.g. a previous run deleted it but OnArchived failed).
	key := ColdKey(c.JobID, c.TaskID)
	recoveredFromCold := false
	data, err := a.storage.Download(ctx, c.StorageBucket, c.StoragePath)
	if err != nil {
		lg.Warn("Hot-storage download failed, attempting cold-storage fallback", "error", err)
		rc, coldErr := a.provider.Download(ctx, a.cfg.Bucket, key)
		if coldErr != nil {
			if isPermanent404(err) && isPermanent404(coldErr) {
				lg.Warn("Both storages returned permanent 404 — marking archive as skipped", "task_id", c.TaskID)
				if markErr := src.MarkSkipped(ctx, c); markErr != nil {
					lg.Error("Failed to mark archive as skipped", "error", markErr)
				}
			} else {
				lg.Error("Cold-storage fallback also failed — candidate unrecoverable this cycle", "error", coldErr)
			}
			return
		}
		var readErr error
		data, readErr = io.ReadAll(rc)
		closeErr := rc.Close()
		if readErr != nil {
			lg.Error("Failed to read cold-storage fallback body", "error", readErr)
			return
		}
		if closeErr != nil {
			lg.Warn("Failed to close cold-storage fallback body", "error", closeErr)
			return
		}
		recoveredFromCold = true
		lg.Info("Recovered data from cold storage — skipping re-upload")
	}

	// 2. Upload to cold storage when we only have the hot copy.
	contentType := c.ContentType
	if contentType == "" {
		contentType = "text/html"
	}
	contentEncoding := c.ContentEncoding
	if contentEncoding == "" {
		contentEncoding = "gzip"
	}
	if !recoveredFromCold {
		err = a.provider.Upload(ctx, a.cfg.Bucket, key, bytes.NewReader(data), UploadOptions{
			ContentType:     contentType,
			ContentEncoding: contentEncoding,
			Metadata: map[string]string{
				"task_id": c.TaskID,
				"job_id":  c.JobID,
				"sha256":  c.SHA256,
			},
		})
		if err != nil {
			lg.Error("Failed to upload to cold storage", "error", err)
			return
		}

		// 3. Verify existence in cold storage
		exists, err := a.provider.Exists(ctx, a.cfg.Bucket, key)
		if err != nil {
			lg.Error("Failed to verify cold storage upload", "error", err)
			return
		}
		if !exists {
			lg.Error("Object not found in cold storage after upload")
			return
		}
	}

	// 4. Delete from hot storage before clearing DB references.
	// Ordering rationale: deleting first avoids permanent orphans in Supabase.
	// If Delete succeeds but OnArchived fails, the next sweep will fall back to
	// downloading from cold storage (step 1 above) and complete the mark.
	if err := a.storage.Delete(ctx, c.StorageBucket, c.StoragePath); err != nil {
		// Treat "object not found" (404) as success — the file may have been
		// manually deleted or removed by a previous run that failed at OnArchived.
		// Check for HTTP 404 status code in the error message.
		errMsg := err.Error()
		if strings.Contains(errMsg, "status 404") || strings.Contains(errMsg, "not found") {
			lg.Info("Hot storage object already deleted (404) — proceeding to mark archived", "error", err)
		} else {
			lg.Error("Failed to delete from hot storage — skipping DB mark to allow retry", "error", err)
			return
		}
	}

	// 5. Mark archived in DB (clears hot-storage columns)
	if err := src.OnArchived(ctx, c, a.provider.Provider(), a.cfg.Bucket, key); err != nil {
		lg.Error("Failed to mark task as archived after hot-storage delete", "error", err, "key", key)
		return
	}

	lg.Debug("Task HTML archived successfully", "key", key)
}
