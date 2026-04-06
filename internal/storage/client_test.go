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
	var receivedAPIKey string
	var receivedAuthorization string
	var receivedBody []byte
	var receivedBodyErr error

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		receivedContentEncoding = r.Header.Get("Content-Encoding")
		receivedAPIKey = r.Header.Get("apikey")
		receivedAuthorization = r.Header.Get("Authorization")

		body, err := io.ReadAll(r.Body)
		receivedBodyErr = err
		if err == nil {
			receivedBody = body
		}

		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := New(server.URL, "sb_publishable_test", "sb_secret_test")
	path, err := client.UploadWithOptions(t.Context(), "task-html", "jobs/job/tasks/task/page-content.html.gz", []byte("payload"), UploadOptions{
		ContentType:     "text/html",
		ContentEncoding: "gzip",
	})

	require.NoError(t, err)
	require.NoError(t, receivedBodyErr)
	assert.Equal(t, "task-html/jobs/job/tasks/task/page-content.html.gz", path)
	assert.Equal(t, "text/html", receivedContentType)
	assert.Equal(t, "gzip", receivedContentEncoding)
	assert.Equal(t, "sb_publishable_test", receivedAPIKey)
	assert.Equal(t, "Bearer sb_secret_test", receivedAuthorization)
	assert.Equal(t, []byte("payload"), receivedBody)
}

func TestUploadWithOptionsFallsBackToSecretKeyForApikey(t *testing.T) {
	t.Parallel()

	var receivedAPIKey string
	var receivedAuthorization string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAPIKey = r.Header.Get("apikey")
		receivedAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	// Empty publishable key falls back to using secret key for both headers
	client := New(server.URL, "", "sb_secret_fallback")
	_, err := client.UploadWithOptions(t.Context(), "task-html", "jobs/job/tasks/task/page-content.html.gz", []byte("payload"), UploadOptions{
		ContentType: "text/html",
	})

	require.NoError(t, err)
	assert.Equal(t, "sb_secret_fallback", receivedAPIKey)
	assert.Equal(t, "Bearer sb_secret_fallback", receivedAuthorization)
}
