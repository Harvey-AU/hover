package jobs

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Harvey-AU/hover/internal/archive"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/logging"
	"github.com/Harvey-AU/hover/internal/observability"
)

// HTMLPersisterConfig holds the runtime knobs for the persister pool.
// Values come from cmd/worker, which reads HTML_PERSIST_* env vars.
type HTMLPersisterConfig struct {
	// Workers is the number of upload goroutines draining the queue.
	Workers int
	// QueueSize is the buffered channel capacity. When full, new enqueues
	// drop the payload (with a metric increment) rather than blocking the
	// stream worker loop — HTML capture is best-effort.
	QueueSize int
	// BatchSize bounds how many successfully uploaded rows accumulate per
	// worker before a metadata UPDATE is flushed.
	BatchSize int
	// FlushInterval forces a metadata flush even when the per-worker batch
	// is short of BatchSize, so a quiet tail doesn't sit on staged rows.
	FlushInterval time.Duration
	// UploadTimeout caps a single PutObject call. R2 can stall under
	// network turbulence; without a per-call cap a hung connection would
	// occupy a worker indefinitely and starve the queue.
	UploadTimeout time.Duration
	// Bucket is the destination R2 bucket (read from archive config).
	Bucket string
	// Provider name (e.g. "r2") — copied onto each persisted row.
	Provider string
}

// DefaultHTMLPersisterConfig returns persister defaults tuned for the
// current ~4k tasks/min throughput baseline. The 256-deep queue keeps
// memory bounded while a transient R2 hiccup drains; 8 workers match
// the historical pre-Redis pool size that ran cleanly under load.
func DefaultHTMLPersisterConfig() HTMLPersisterConfig {
	return HTMLPersisterConfig{
		Workers:       8,
		QueueSize:     256,
		BatchSize:     16,
		FlushInterval: 250 * time.Millisecond,
		UploadTimeout: 30 * time.Second,
	}
}

// HTMLPersisterDeps wires the persister to its collaborators. Injected so
// tests can swap fakes in for the cold-storage provider and DB writer.
type HTMLPersisterDeps struct {
	Provider archive.ColdStorageProvider
	DBQueue  DbQueueInterface
}

// persistJob carries one task's payload through the queue.
type persistJob struct {
	taskID string
	jobID  string
	upload *TaskHTMLUpload
}

// HTMLPersister streams completed-task HTML payloads directly to R2 and
// stamps the resulting metadata onto the task row. It is the Stage 2
// replacement for the deleted Supabase Storage hop — see issue #332 and
// CHANGELOG (2026-04-25 entry).
//
// The pool is intentionally simple: a single bounded channel feeds N
// upload workers, each of which accumulates successful uploads into a
// per-worker buffer and flushes a single metadata UPDATE when the buffer
// fills or a flush tick fires. Failed uploads are logged and dropped;
// HTML capture is best-effort and must not block the hot worker loop.
type HTMLPersister struct {
	cfg  HTMLPersisterConfig
	deps HTMLPersisterDeps

	queue chan persistJob

	// uploadWG tracks the upload workers separately from probeWG so Stop
	// can wait for the queue to drain before tearing down the probe loop
	// and cancelling the shared context.
	uploadWG sync.WaitGroup
	probeWG  sync.WaitGroup
	cancel   context.CancelFunc

	// stopped gates Enqueue once Stop begins, so callers see a clean false
	// instead of a panic on a closed channel.
	stopped atomic.Bool

	// stopCh signals the probe loop to exit; closed during Stop after the
	// queue has drained. Worker loops use the closed-queue signal instead.
	stopCh chan struct{}

	// startOnce / stopOnce keep Start/Stop idempotent so a graceful
	// shutdown that races with a context cancellation can't panic on
	// closed channels.
	startOnce sync.Once
	stopOnce  sync.Once
}

// NewHTMLPersister constructs a persister but does not start its
// goroutines. Call Start to begin draining the queue.
func NewHTMLPersister(cfg HTMLPersisterConfig, deps HTMLPersisterDeps) (*HTMLPersister, error) {
	if deps.Provider == nil {
		return nil, errors.New("html persister: cold storage provider is required")
	}
	if deps.DBQueue == nil {
		return nil, errors.New("html persister: db queue is required")
	}
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	if cfg.QueueSize < 1 {
		cfg.QueueSize = 1
	}
	if cfg.BatchSize < 1 {
		cfg.BatchSize = 1
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 250 * time.Millisecond
	}
	if cfg.UploadTimeout <= 0 {
		cfg.UploadTimeout = 30 * time.Second
	}
	if cfg.Bucket == "" {
		return nil, errors.New("html persister: bucket is required")
	}
	if cfg.Provider == "" {
		cfg.Provider = deps.Provider.Provider()
	}

	return &HTMLPersister{
		cfg:    cfg,
		deps:   deps,
		queue:  make(chan persistJob, cfg.QueueSize),
		stopCh: make(chan struct{}),
	}, nil
}

// Start launches the upload workers and the queue-depth probe.
// Safe to call once; subsequent calls are no-ops.
func (p *HTMLPersister) Start(ctx context.Context) {
	p.startOnce.Do(func() {
		ctx, p.cancel = context.WithCancel(ctx)

		for i := 0; i < p.cfg.Workers; i++ {
			p.uploadWG.Add(1)
			go func(id int) {
				defer p.uploadWG.Done()
				p.workerLoop(ctx, id)
			}(i)
		}

		p.probeWG.Add(1)
		go func() {
			defer p.probeWG.Done()
			p.probeLoop(ctx)
		}()

		jobsLog.Info("html persister started",
			"workers", p.cfg.Workers,
			"queue_size", p.cfg.QueueSize,
			"batch_size", p.cfg.BatchSize,
			"bucket", p.cfg.Bucket,
		)
	})
}

// Stop drains already-accepted uploads before exiting. New Enqueue calls
// are rejected immediately; the upload workers consume the rest of the
// queue under the live context (so per-upload timeouts still apply), then
// the probe loop is signalled and the shared context is cancelled.
//
// If the parent ctx passed to Start is cancelled externally before Stop
// returns, in-flight uploads abort via uploadCtx and the remaining queue
// is dropped — that is the unavoidable hard-exit path.
func (p *HTMLPersister) Stop() {
	p.stopOnce.Do(func() {
		// Reject further Enqueue calls before closing the channel so a
		// concurrent send can't race with the close and panic.
		p.stopped.Store(true)
		close(p.queue)

		// Workers see ok=false on the queue read once it's drained, flush
		// their final batch, and exit. uploadWG.Wait blocks until then.
		p.uploadWG.Wait()

		// Now that no upload work remains, tear down the probe loop and
		// the shared context together.
		close(p.stopCh)
		if p.cancel != nil {
			p.cancel()
		}
		p.probeWG.Wait()
		jobsLog.Info("html persister stopped")
	})
}

// Enqueue tries to hand a payload to a worker. The send is
// non-blocking: if the queue is full we drop the payload (HTML capture
// is best-effort) and emit an "skipped" metric so dashboards surface
// sustained backpressure. Returns true when the payload was accepted.
func (p *HTMLPersister) Enqueue(ctx context.Context, task *db.Task, upload *TaskHTMLUpload) bool {
	if p == nil || task == nil || upload == nil {
		return false
	}
	if p.stopped.Load() {
		observability.RecordHTMLPersistUpload(ctx, "skipped")
		return false
	}
	job := persistJob{
		taskID: task.ID,
		jobID:  task.JobID,
		upload: upload,
	}
	select {
	case p.queue <- job:
		return true
	default:
		observability.RecordHTMLPersistUpload(ctx, "skipped")
		return false
	}
}

// QueueDepth returns the current number of pending payloads. Exposed for
// tests; the probeLoop emits this to telemetry on its own cadence.
func (p *HTMLPersister) QueueDepth() int {
	if p == nil {
		return 0
	}
	return len(p.queue)
}

// workerLoop drains the shared queue, uploads each payload to R2, and
// accumulates successful rows for periodic metadata UPDATE flushes.
func (p *HTMLPersister) workerLoop(ctx context.Context, workerID int) {
	log := jobsLog.With("html_persist_worker", workerID)
	batch := make([]db.TaskHTMLMetadataRow, 0, p.cfg.BatchSize)
	ticker := time.NewTicker(p.cfg.FlushInterval)
	defer ticker.Stop()

	// Cap the retained-on-error batch so a sustained DB outage can't grow
	// per-worker memory without bound. Once we exceed this, drop the
	// oldest rows (best-effort: R2 still has the payload, we just lose
	// the metadata pointer for those).
	maxRetained := p.cfg.BatchSize * 8

	flush := func(reason string) {
		if len(batch) == 0 {
			return
		}
		// Use a detached background context for the flush so a cancellation
		// arriving between upload-success and metadata-write doesn't strand
		// just-uploaded rows without their metadata. The UPDATE itself is
		// idempotent — re-running with the same payload key is a safe
		// no-op via COALESCE/NULLIF.
		flushCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := p.deps.DBQueue.BatchUpsertTaskHTMLMetadata(flushCtx, batch); err != nil {
			// Keep the batch so the next tick/size flush retries — losing
			// the metadata UPDATE here would orphan the just-uploaded R2
			// objects (no row pointer back to them).
			observability.RecordHTMLPersistUpload(flushCtx, "flush_err")
			log.Error("html metadata flush failed — retaining batch for retry",
				"error", err, "rows", len(batch), "reason", reason)
			if len(batch) > maxRetained {
				dropped := len(batch) - maxRetained
				batch = append(batch[:0], batch[dropped:]...)
				log.Warn("html metadata retained batch capped — oldest rows dropped",
					"dropped", dropped, "kept", len(batch))
			}
			return
		}
		log.Debug("html metadata flushed",
			"rows", len(batch), "reason", reason)
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush("shutdown")
			return
		case <-ticker.C:
			flush("interval")
		case job, ok := <-p.queue:
			if !ok {
				flush("closed")
				return
			}
			row, success := p.uploadOne(ctx, log, job)
			if success {
				batch = append(batch, row)
				if len(batch) >= p.cfg.BatchSize {
					flush("size")
				}
			}
		}
	}
}

// uploadOne pushes a single payload to R2. On success it returns the
// metadata row to be batched into the next UPDATE; on failure it logs,
// emits the err counter, and returns (_, false).
func (p *HTMLPersister) uploadOne(ctx context.Context, log *logging.Logger, job persistJob) (db.TaskHTMLMetadataRow, bool) {
	uploadCtx, cancel := context.WithTimeout(ctx, p.cfg.UploadTimeout)
	defer cancel()

	upload := job.upload
	key := upload.Path
	if key == "" {
		key = archive.TaskHTMLObjectPath(job.jobID, job.taskID)
	}

	start := time.Now()
	err := p.deps.Provider.Upload(uploadCtx, p.cfg.Bucket, key, bytes.NewReader(upload.Payload), archive.UploadOptions{
		ContentType:     upload.UploadContentType,
		ContentEncoding: upload.ContentEncoding,
		Metadata: map[string]string{
			"task-id":     job.taskID,
			"job-id":      job.jobID,
			"sha256":      upload.SHA256,
			"captured-at": upload.CapturedAt.UTC().Format(time.RFC3339Nano),
		},
	})
	elapsed := time.Since(start)
	observability.RecordHTMLPersistUploadDuration(ctx, elapsed)
	observability.RecordHTMLPersistBodyBytes(ctx, upload.CompressedSizeBytes)

	if err != nil {
		observability.RecordHTMLPersistUpload(ctx, "err")
		log.Warn("html upload failed",
			"error", err,
			"task_id", job.taskID,
			"bucket", p.cfg.Bucket,
			"key", key,
			"elapsed_ms", elapsed.Milliseconds(),
		)
		return db.TaskHTMLMetadataRow{}, false
	}

	observability.RecordHTMLPersistUpload(ctx, "ok")

	return db.TaskHTMLMetadataRow{
		TaskID: job.taskID,
		Metadata: db.TaskHTMLMetadata{
			StorageBucket:       p.cfg.Bucket,
			StoragePath:         key,
			ContentType:         upload.ContentType,
			ContentEncoding:     upload.ContentEncoding,
			SizeBytes:           upload.SizeBytes,
			CompressedSizeBytes: upload.CompressedSizeBytes,
			SHA256:              upload.SHA256,
			CapturedAt:          upload.CapturedAt,
			// Stamping ArchivedAt at write time keeps the archive sweep's
			// candidate query (html_archived_at IS NULL) from re-picking
			// these rows — R2 IS the hot store now, so a sweeper-driven
			// R2-to-R2 copy would be a wasteful no-op.
			ArchivedAt: upload.CapturedAt,
		},
	}, true
}

// probeLoop emits the queue-depth gauge so dashboards can spot
// sustained backpressure before drops start. Cadence matches the
// flush tick — frequent enough to catch transient saturation, cheap
// enough that it adds no measurable overhead.
func (p *HTMLPersister) probeLoop(ctx context.Context) {
	ticker := time.NewTicker(p.cfg.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			observability.RecordHTMLPersistQueueDepth(ctx, len(p.queue))
		}
	}
}
