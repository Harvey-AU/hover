package archive

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// storageDownloader abstracts downloading from hot storage (Supabase).
type storageDownloader interface {
	Download(ctx context.Context, bucket, path string) ([]byte, error)
	Delete(ctx context.Context, bucket, path string) error
}

// Archiver runs periodic sweeps to move data from hot to cold storage.
type Archiver struct {
	provider ColdStorageProvider
	storage  storageDownloader
	sources  []ArchiveSource
	cfg      Config
}

// NewArchiver creates an archiver with the given provider, hot-storage client,
// and one or more archive sources.
func NewArchiver(provider ColdStorageProvider, storage storageDownloader, cfg Config, sources ...ArchiveSource) *Archiver {
	return &Archiver{
		provider: provider,
		storage:  storage,
		sources:  sources,
		cfg:      cfg,
	}
}

// Run blocks until ctx is cancelled or stopCh is closed, running archive
// sweeps at the configured interval.
func (a *Archiver) Run(ctx context.Context, stopCh <-chan struct{}) {
	ticker := time.NewTicker(a.cfg.Interval)
	defer ticker.Stop()

	log.Info().
		Str("provider", a.cfg.Provider).
		Str("bucket", a.cfg.Bucket).
		Dur("interval", a.cfg.Interval).
		Int("batch_size", a.cfg.BatchSize).
		Int("concurrency", a.cfg.Concurrency).
		Msg("Archive scheduler started")

	for {
		select {
		case <-ticker.C:
			a.sweep(ctx)
		case <-stopCh:
			log.Info().Msg("Archive scheduler stopping (stop signal)")
			return
		case <-ctx.Done():
			log.Info().Msg("Archive scheduler stopping (context cancelled)")
			return
		}
	}
}

func (a *Archiver) sweep(ctx context.Context) {
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

		sem := make(chan struct{}, a.cfg.Concurrency)
		var wg sync.WaitGroup

		for _, c := range candidates {
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

	// 1. Download from hot storage
	data, err := a.storage.Download(ctx, c.StorageBucket, c.StoragePath)
	if err != nil {
		lg.Error().Err(err).Msg("Failed to download from hot storage")
		return
	}

	// 2. Upload to cold storage
	key := ColdKey(c.JobID, c.StoragePath, c.TaskID)
	err = a.provider.Upload(ctx, a.cfg.Bucket, key, bytes.NewReader(data), UploadOptions{
		ContentType:     "text/html",
		ContentEncoding: "gzip",
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

	// 4. Mark archived in DB (clears hot-storage columns)
	if err := src.OnArchived(ctx, c, a.provider.Provider(), a.cfg.Bucket, key); err != nil {
		lg.Error().Err(err).Msg("Failed to mark task as archived")
		return
	}

	// 5. Delete from hot storage — best effort
	if err := a.storage.Delete(ctx, c.StorageBucket, c.StoragePath); err != nil {
		lg.Warn().Err(err).Msg("Failed to delete from hot storage after archival")
	}

	lg.Debug().Str("key", key).Msg("Task HTML archived successfully")
}
