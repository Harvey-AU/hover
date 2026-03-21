package storage

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUploadWithOptionsSetsContentEncoding(t *testing.T) {
	t.Parallel()

	var receivedContentType string
	var receivedContentEncoding string
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		receivedContentEncoding = r.Header.Get("Content-Encoding")

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		receivedBody = body

		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := New(server.URL, "service-role-key")
	path, err := client.UploadWithOptions(t.Context(), "task-html", "jobs/job/tasks/task.html.gz", []byte("payload"), UploadOptions{
		ContentType:     "text/html",
		ContentEncoding: "gzip",
	})

	require.NoError(t, err)
	assert.Equal(t, "task-html/jobs/job/tasks/task.html.gz", path)
	assert.Equal(t, "text/html", receivedContentType)
	assert.Equal(t, "gzip", receivedContentEncoding)
	assert.Equal(t, []byte("payload"), receivedBody)
}
