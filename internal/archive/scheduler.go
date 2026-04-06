package archive

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

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
		log.Warn().Dur("fallback_interval", interval).Msg("Archive interval was non-positive, using default")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Info().
		Str("provider", a.cfg.Provider).
		Str("bucket", a.cfg.Bucket).
		Dur("interval", a.cfg.Interval).
		Int("batch_size", a.cfg.BatchSize).
		Int("concurrency", a.cfg.Concurrency).
		Msg("Archive scheduler started")

	// Run first sweep immediately rather than waiting for the first tick.
	a.sweep(ctx, stopCh)

	for {
		select {
		case <-ticker.C:
			a.sweep(ctx, stopCh)
		case <-stopCh:
			log.Info().Msg("Archive scheduler stopping (stop signal)")
			return
		case <-ctx.Done():
			log.Info().Msg("Archive scheduler stopping (context cancelled)")
			return
		}
	}
}

func (a *Archiver) sweep(ctx context.Context, stopCh <-chan struct{}) {
	// Mark jobs as 'archived' when all their HTML has been moved to cold storage.
	if a.markJobsDone != nil {
		if n, err := a.markJobsDone(ctx); err != nil {
			log.Error().Err(err).Msg("Failed to mark fully archived jobs")
		} else if n > 0 {
			log.Info().Int64("jobs_marked", n).Msg("Jobs marked as archived")
		}
	}

	for _, src := range a.sources {
		candidates, err := src.FindCandidates(ctx, a.cfg.BatchSize)
		if err != nil {
			log.Error().Err(err).Str("source", src.Name()).Msg("Failed to find archive candidates")
			continue
		}
		if len(candidates) == 0 {
			log.Debug().Str("source", src.Name()).Msg("No archive candidates")
			continue
		}

		log.Info().
			Str("source", src.Name()).
			Int("candidates", len(candidates)).
			Msg("Archiving candidates")

		concurrency := a.cfg.Concurrency
		if concurrency <= 0 {
			concurrency = 1
		}
		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup

		for _, c := range candidates {
			// Check for shutdown between dispatches.
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			default:
			}

			sem <- struct{}{}
			wg.Add(1)
			go func(candidate ArchiveCandidate) {
				defer wg.Done()
				defer func() { <-sem }()
				a.archiveOne(ctx, src, candidate)
			}(c)
		}

		wg.Wait()
	}
}

func (a *Archiver) archiveOne(ctx context.Context, src ArchiveSource, c ArchiveCandidate) {
	lg := log.With().
		Str("task_id", c.TaskID).
		Str("job_id", c.JobID).
		Str("source", src.Name()).
		Logger()

	// 1. Download from hot storage, falling back to cold storage if the hot
	// copy is already gone (e.g. a previous run deleted it but OnArchived failed).
	key := ColdKey(c.StoragePath)
	data, err := a.storage.Download(ctx, c.StorageBucket, c.StoragePath)
	if err != nil {
		lg.Warn().Err(err).Msg("Hot-storage download failed, attempting cold-storage fallback")
		rc, coldErr := a.provider.Download(ctx, a.cfg.Bucket, key)
		if coldErr != nil {
			lg.Error().Err(coldErr).Msg("Cold-storage fallback also failed — candidate unrecoverable this cycle")
			return
		}
		var readErr error
		data, readErr = io.ReadAll(rc)
		closeErr := rc.Close()
		if readErr != nil {
			lg.Error().Err(readErr).Msg("Failed to read cold-storage fallback body")
			return
		}
		if closeErr != nil {
			lg.Warn().Err(closeErr).Msg("Failed to close cold-storage fallback body")
			return
		}
		lg.Info().Msg("Recovered data from cold storage — proceeding to mark archived")
	}

	// 2. Upload to cold storage
	contentType := c.ContentType
	if contentType == "" {
		contentType = "text/html"
	}
	contentEncoding := c.ContentEncoding
	if contentEncoding == "" {
		contentEncoding = "gzip"
	}
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
		lg.Error().Err(err).Msg("Failed to upload to cold storage")
		return
	}

	// 3. Verify existence in cold storage
	exists, err := a.provider.Exists(ctx, a.cfg.Bucket, key)
	if err != nil {
		lg.Error().Err(err).Msg("Failed to verify cold storage upload")
		return
	}
	if !exists {
		lg.Error().Msg("Object not found in cold storage after upload")
		return
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
			lg.Info().Err(err).Msg("Hot storage object already deleted (404) — proceeding to mark archived")
		} else {
			lg.Error().Err(err).Msg("Failed to delete from hot storage — skipping DB mark to allow retry")
			return
		}
	}

	// 5. Mark archived in DB (clears hot-storage columns)
	if err := src.OnArchived(ctx, c, a.provider.Provider(), a.cfg.Bucket, key); err != nil {
		lg.Error().Err(err).Str("key", key).Msg("Failed to mark task as archived after hot-storage delete")
		return
	}

	lg.Debug().Str("key", key).Msg("Task HTML archived successfully")
}
