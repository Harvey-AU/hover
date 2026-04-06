package archive

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS3ProviderUploadAvoidsAutomaticChecksumHeaders(t *testing.T) {
	t.Parallel()

	var receivedPath string
	var contentSHA256 string
	var checksumHeader string
	var sdkChecksumAlgorithm string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		contentSHA256 = r.Header.Get("X-Amz-Content-Sha256")
		checksumHeader = r.Header.Get("X-Amz-Checksum-Crc32")
		sdkChecksumAlgorithm = r.Header.Get("X-Amz-Sdk-Checksum-Algorithm")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider, err := NewS3Provider(server.URL, "test-access-key", "test-secret-key", "auto", "r2")
	require.NoError(t, err)

	err = provider.Upload(context.Background(), "native-hover-archive", "jobs/job-123/tasks/page/task-123.html.gz", bytes.NewReader([]byte("hello world")), UploadOptions{
		ContentType:     "text/html",
		ContentEncoding: "gzip",
	})
	require.NoError(t, err)

	assert.Equal(t, "/native-hover-archive/jobs/job-123/tasks/page/task-123.html.gz", receivedPath)
	assert.Equal(t, "UNSIGNED-PAYLOAD", contentSHA256)
	assert.Empty(t, checksumHeader)
	assert.Empty(t, sdkChecksumAlgorithm)
}
