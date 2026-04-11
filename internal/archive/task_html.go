package archive

import (
	"context"

	"github.com/Harvey-AU/hover/internal/db"
)

// TaskHTMLSource finds and marks task HTML blobs for archival.
type TaskHTMLSource struct {
	dbQueue   archiveDB
	retention int // number of recent jobs to keep hot
}

// archiveDB is the subset of the DB layer the source needs.
type archiveDB interface {
	FindArchiveCandidates(ctx context.Context, retentionJobs, limit int) ([]db.ArchiveCandidate, error)
	MarkTaskArchived(ctx context.Context, taskID, provider, bucket, key string) error
	MarkArchiveSkipped(ctx context.Context, taskID string) error
}

// NewTaskHTMLSource creates an ArchiveSource backed by the given DB queue.
func NewTaskHTMLSource(dbQueue archiveDB, retentionJobs int) *TaskHTMLSource {
	return &TaskHTMLSource{dbQueue: dbQueue, retention: retentionJobs}
}

func (s *TaskHTMLSource) Name() string { return "task_html" }

func (s *TaskHTMLSource) FindCandidates(ctx context.Context, batchSize int) ([]ArchiveCandidate, error) {
	rows, err := s.dbQueue.FindArchiveCandidates(ctx, s.retention, batchSize)
	if err != nil {
		return nil, err
	}

	candidates := make([]ArchiveCandidate, len(rows))
	for i, r := range rows {
		candidates[i] = ArchiveCandidate{
			TaskID:              r.TaskID,
			JobID:               r.JobID,
			StorageBucket:       r.StorageBucket,
			StoragePath:         r.StoragePath,
			SHA256:              r.SHA256,
			CompressedSizeBytes: r.CompressedSizeBytes,
			ContentType:         r.ContentType,
			ContentEncoding:     r.ContentEncoding,
		}
	}
	return candidates, nil
}

func (s *TaskHTMLSource) OnArchived(ctx context.Context, c ArchiveCandidate, provider, bucket, key string) error {
	return s.dbQueue.MarkTaskArchived(ctx, c.TaskID, provider, bucket, key)
}

func (s *TaskHTMLSource) MarkSkipped(ctx context.Context, c ArchiveCandidate) error {
	return s.dbQueue.MarkArchiveSkipped(ctx, c.TaskID)
}
