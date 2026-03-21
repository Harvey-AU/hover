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
	assert.Equal(t, "text/html", upload.ContentType)
	assert.Equal(t, taskHTMLContentEncoding, upload.ContentEncoding)
	assert.Equal(t, int64(len(body)), upload.SizeBytes)
	assert.Equal(t, capturedAt, upload.CapturedAt)
	assert.Len(t, upload.SHA256, 64)
	assert.Equal(t, body, gunzipTestPayload(t, upload.Payload))
	assert.Equal(t, int64(len(upload.Payload)), upload.CompressedSizeBytes)
}

func TestBuildTaskHTMLUploadSkipsIneligibleBodies(t *testing.T) {
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

func TestPersistTaskHTMLPersistsMetadataOnSuccess(t *testing.T) {
	task := &db.Task{ID: "task-123", JobID: "job-456"}
	result := &crawler.CrawlResult{
		ContentType: "text/html; charset=utf-8",
		Body:        []byte("<html><body>ok</body></html>"),
	}
	capturedAt := time.Date(2026, time.March, 21, 8, 0, 0, 0, time.UTC)

	var capturedOptions storage.UploadOptions
	var capturedPayload []byte
	var persistedTask *db.Task
	wp := &WorkerPool{
		dbQueue: &MockDbQueue{UpdateTaskStatusFunc: func(ctx context.Context, task *db.Task) error {
			persisted := *task
			persistedTask = &persisted
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

	wp.persistTaskHTML(context.Background(), task, result, capturedAt)

	require.NotNil(t, persistedTask)
	assert.Equal(t, taskHTMLStorageBucket, persistedTask.HTMLStorageBucket)
	assert.Equal(t, "jobs/job-456/tasks/page-path/task-123.html.gz", persistedTask.HTMLStoragePath)
	assert.Equal(t, "text/html", persistedTask.HTMLContentType)
	assert.Equal(t, taskHTMLContentEncoding, persistedTask.HTMLContentEncoding)
	assert.Equal(t, int64(len(result.Body)), persistedTask.HTMLSizeBytes)
	assert.Equal(t, int64(len(capturedPayload)), persistedTask.HTMLCompressedSizeBytes)
	assert.Equal(t, capturedAt, persistedTask.HTMLCapturedAt)
	assert.Len(t, persistedTask.HTMLSHA256, 64)
	assert.Equal(t, "text/html", capturedOptions.ContentType)
	assert.Equal(t, taskHTMLContentEncoding, capturedOptions.ContentEncoding)
	assert.Equal(t, result.Body, gunzipTestPayload(t, capturedPayload))
	assert.Empty(t, task.HTMLStorageBucket)
	assert.True(t, task.HTMLCapturedAt.IsZero())
}

func TestPersistTaskHTMLLeavesMetadataEmptyOnUploadFailure(t *testing.T) {
	task := &db.Task{ID: "task-123", JobID: "job-456"}
	wp := &WorkerPool{
		dbQueue: &MockDbQueue{},
		storageClient: &stubStorageUploader{uploadWithOptionsFunc: func(ctx context.Context, bucket, path string, data []byte, options storage.UploadOptions) (string, error) {
			return "", errors.New("upload failed")
		}},
	}

	wp.persistTaskHTML(context.Background(), task, &crawler.CrawlResult{
		ContentType: "text/html",
		Body:        []byte("<html></html>"),
	}, time.Now().UTC())

	assert.Empty(t, task.HTMLStorageBucket)
	assert.Empty(t, task.HTMLStoragePath)
	assert.Zero(t, task.HTMLSizeBytes)
	assert.Zero(t, task.HTMLCompressedSizeBytes)
	assert.Empty(t, task.HTMLSHA256)
	assert.True(t, task.HTMLCapturedAt.IsZero())
}

func TestPersistTaskHTMLDeletesUploadWhenMetadataPersistenceFails(t *testing.T) {
	task := &db.Task{ID: "task-123", JobID: "job-456"}
	deleted := false
	wp := &WorkerPool{
		dbQueue: &MockDbQueue{UpdateTaskStatusFunc: func(ctx context.Context, task *db.Task) error {
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

	wp.persistTaskHTML(context.Background(), task, &crawler.CrawlResult{
		ContentType: "text/html",
		Body:        []byte("<html></html>"),
	}, time.Now().UTC())

	assert.True(t, deleted)
	assert.Empty(t, task.HTMLStoragePath)
}
