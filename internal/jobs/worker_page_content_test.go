package jobs

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Harvey-AU/adapt/internal/crawler"
	"github.com/Harvey-AU/adapt/internal/db"
	"github.com/Harvey-AU/adapt/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubStorageUploader struct {
	uploadWithOptionsFunc func(ctx context.Context, bucket, path string, data []byte, options storage.UploadOptions) (string, error)
	deleteFunc            func(ctx context.Context, bucket, path string) error
}

func (s *stubStorageUploader) Upload(ctx context.Context, bucket, path string, data []byte, contentType string) (string, error) {
	return s.UploadWithOptions(ctx, bucket, path, data, storage.UploadOptions{ContentType: contentType})
}

func (s *stubStorageUploader) UploadWithOptions(ctx context.Context, bucket, path string, data []byte, options storage.UploadOptions) (string, error) {
	if s.uploadWithOptionsFunc != nil {
		return s.uploadWithOptionsFunc(ctx, bucket, path, data, options)
	}
	return bucket + "/" + path, nil
}

func (s *stubStorageUploader) Delete(ctx context.Context, bucket, path string) error {
	if s.deleteFunc != nil {
		return s.deleteFunc(ctx, bucket, path)
	}
	return nil
}

func gunzipTestPayload(t *testing.T, payload []byte) []byte {
	t.Helper()

	reader, err := gzip.NewReader(bytes.NewReader(payload))
	require.NoError(t, err)
	defer reader.Close()

	decoded, err := io.ReadAll(reader)
	require.NoError(t, err)
	return decoded
}

func TestBuildTaskHTMLUpload(t *testing.T) {
	task := &db.Task{ID: "task-123", JobID: "job-456"}
	body := []byte("<html><body>Hello</body></html>")
	capturedAt := time.Date(2026, time.March, 21, 7, 0, 0, 0, time.UTC)

	upload, ok, err := buildTaskHTMLUpload(task, &crawler.CrawlResult{
		ContentType: "text/html; charset=utf-8",
		Body:        body,
	}, capturedAt)

	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, upload)

	assert.Equal(t, taskHTMLStorageBucket, upload.Bucket)
	assert.Equal(t, "jobs/job-456/tasks/page-path/task-123.html.gz", upload.Path)
	assert.Equal(t, "text/html; charset=utf-8", upload.ContentType)
	assert.Equal(t, taskHTMLContentEncoding, upload.ContentEncoding)
	assert.Equal(t, int64(len(body)), upload.SizeBytes)
	assert.Equal(t, capturedAt, upload.CapturedAt)
	assert.Len(t, upload.SHA256, 64)
	assert.Equal(t, body, gunzipTestPayload(t, upload.Payload))
	assert.Equal(t, int64(len(upload.Payload)), upload.CompressedSizeBytes)
}

func TestBuildTaskHTMLUploadSkipsIneligibleBodies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{name: "empty body", contentType: "text/html", body: nil},
		{name: "binary content", contentType: "image/png", body: []byte("binary")},
		{name: "non-html text content", contentType: "text/plain", body: []byte("hello")},
		{name: "missing content type", contentType: "", body: []byte("hello")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			upload, ok, err := buildTaskHTMLUpload(&db.Task{ID: "task", JobID: "job"}, &crawler.CrawlResult{
				ContentType: tt.contentType,
				Body:        tt.body,
			}, time.Now().UTC())

			require.NoError(t, err)
			assert.False(t, ok)
			assert.Nil(t, upload)
		})
	}
}

func TestProcessTaskHTMLPersistencePersistsMetadataOnSuccess(t *testing.T) {
	request := &taskHTMLPersistRequest{
		Task:        db.Task{ID: "task-123", JobID: "job-456"},
		ContentType: "text/html; charset=utf-8",
		Body:        []byte("<html><body>ok</body></html>"),
	}
	capturedAt := time.Date(2026, time.March, 21, 8, 0, 0, 0, time.UTC)
	request.CapturedAt = capturedAt

	var capturedOptions storage.UploadOptions
	var capturedPayload []byte
	var persistedMetadata db.TaskHTMLMetadata
	var persistedTaskID string
	wp := &WorkerPool{
		dbQueue: &MockDbQueue{UpdateTaskHTMLMetadataFunc: func(ctx context.Context, taskID string, metadata db.TaskHTMLMetadata) error {
			persistedTaskID = taskID
			persistedMetadata = metadata
			return nil
		}},
		storageClient: &stubStorageUploader{uploadWithOptionsFunc: func(ctx context.Context, bucket, path string, data []byte, options storage.UploadOptions) (string, error) {
			capturedOptions = options
			capturedPayload = append([]byte(nil), data...)
			assert.Equal(t, taskHTMLStorageBucket, bucket)
			assert.Equal(t, "jobs/job-456/tasks/page-path/task-123.html.gz", path)
			return bucket + "/" + path, nil
		}},
	}

	wp.processTaskHTMLPersistence(context.Background(), request)

	assert.Equal(t, "task-123", persistedTaskID)
	assert.Equal(t, taskHTMLStorageBucket, persistedMetadata.StorageBucket)
	assert.Equal(t, "jobs/job-456/tasks/page-path/task-123.html.gz", persistedMetadata.StoragePath)
	assert.Equal(t, "text/html; charset=utf-8", persistedMetadata.ContentType)
	assert.Equal(t, taskHTMLContentEncoding, persistedMetadata.ContentEncoding)
	assert.Equal(t, int64(len(request.Body)), persistedMetadata.SizeBytes)
	assert.Equal(t, int64(len(capturedPayload)), persistedMetadata.CompressedSizeBytes)
	assert.Equal(t, capturedAt, persistedMetadata.CapturedAt)
	assert.Len(t, persistedMetadata.SHA256, 64)
	assert.Equal(t, "text/html; charset=utf-8", capturedOptions.ContentType)
	assert.Equal(t, taskHTMLContentEncoding, capturedOptions.ContentEncoding)
	assert.Equal(t, request.Body, gunzipTestPayload(t, capturedPayload))
}

func TestProcessTaskHTMLPersistenceLeavesMetadataEmptyOnUploadFailure(t *testing.T) {
	metadataCalled := false

	wp := &WorkerPool{
		dbQueue: &MockDbQueue{UpdateTaskHTMLMetadataFunc: func(ctx context.Context, taskID string, metadata db.TaskHTMLMetadata) error {
			metadataCalled = true
			return nil
		}},
		storageClient: &stubStorageUploader{uploadWithOptionsFunc: func(ctx context.Context, bucket, path string, data []byte, options storage.UploadOptions) (string, error) {
			return "", errors.New("upload failed")
		}},
	}

	wp.processTaskHTMLPersistence(context.Background(), &taskHTMLPersistRequest{
		Task:        db.Task{ID: "task-123", JobID: "job-456"},
		ContentType: "text/html",
		Body:        []byte("<html></html>"),
		CapturedAt:  time.Now().UTC(),
	})

	assert.False(t, metadataCalled)
}

func TestProcessTaskHTMLPersistenceDeletesUploadWhenMetadataPersistenceFails(t *testing.T) {
	deleted := false
	wp := &WorkerPool{
		dbQueue: &MockDbQueue{UpdateTaskHTMLMetadataFunc: func(ctx context.Context, taskID string, metadata db.TaskHTMLMetadata) error {
			return errors.New("write failed")
		}},
		storageClient: &stubStorageUploader{
			uploadWithOptionsFunc: func(ctx context.Context, bucket, path string, data []byte, options storage.UploadOptions) (string, error) {
				return bucket + "/" + path, nil
			},
			deleteFunc: func(ctx context.Context, bucket, path string) error {
				deleted = true
				assert.Equal(t, taskHTMLStorageBucket, bucket)
				assert.Equal(t, "jobs/job-456/tasks/page-path/task-123.html.gz", path)
				return nil
			},
		},
	}

	wp.processTaskHTMLPersistence(context.Background(), &taskHTMLPersistRequest{
		Task:        db.Task{ID: "task-123", JobID: "job-456"},
		ContentType: "text/html",
		Body:        []byte("<html></html>"),
		CapturedAt:  time.Now().UTC(),
	})

	assert.True(t, deleted)
}
