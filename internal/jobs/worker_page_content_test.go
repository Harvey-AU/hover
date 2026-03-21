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

func TestStoreTaskHTMLPersistsMetadataOnSuccess(t *testing.T) {
	task := &db.Task{ID: "task-123", JobID: "job-456"}
	result := &crawler.CrawlResult{
		ContentType: "text/plain; charset=utf-8",
		Body:        []byte("plain text body"),
	}
	capturedAt := time.Date(2026, time.March, 21, 8, 0, 0, 0, time.UTC)

	var capturedOptions storage.UploadOptions
	var capturedPayload []byte
	wp := &WorkerPool{
		storageClient: &stubStorageUploader{uploadWithOptionsFunc: func(ctx context.Context, bucket, path string, data []byte, options storage.UploadOptions) (string, error) {
			capturedOptions = options
			capturedPayload = append([]byte(nil), data...)
			assert.Equal(t, taskHTMLStorageBucket, bucket)
			assert.Equal(t, "jobs/job-456/tasks/page-path/task-123.html.gz", path)
			return bucket + "/" + path, nil
		}},
	}

	wp.storeTaskHTML(context.Background(), task, result, capturedAt)

	assert.Equal(t, taskHTMLStorageBucket, task.HTMLStorageBucket)
	assert.Equal(t, "jobs/job-456/tasks/page-path/task-123.html.gz", task.HTMLStoragePath)
	assert.Equal(t, "text/plain", task.HTMLContentType)
	assert.Equal(t, taskHTMLContentEncoding, task.HTMLContentEncoding)
	assert.Equal(t, int64(len(result.Body)), task.HTMLSizeBytes)
	assert.Equal(t, int64(len(capturedPayload)), task.HTMLCompressedSizeBytes)
	assert.Equal(t, capturedAt, task.HTMLCapturedAt)
	assert.Len(t, task.HTMLSHA256, 64)
	assert.Equal(t, "text/plain", capturedOptions.ContentType)
	assert.Equal(t, taskHTMLContentEncoding, capturedOptions.ContentEncoding)
	assert.Equal(t, result.Body, gunzipTestPayload(t, capturedPayload))
}

func TestStoreTaskHTMLLeavesMetadataEmptyOnUploadFailure(t *testing.T) {
	task := &db.Task{ID: "task-123", JobID: "job-456"}
	wp := &WorkerPool{
		storageClient: &stubStorageUploader{uploadWithOptionsFunc: func(ctx context.Context, bucket, path string, data []byte, options storage.UploadOptions) (string, error) {
			return "", errors.New("upload failed")
		}},
	}

	wp.storeTaskHTML(context.Background(), task, &crawler.CrawlResult{
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
