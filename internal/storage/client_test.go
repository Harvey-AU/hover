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

	client := New(server.URL, "service-role-key")
	path, err := client.UploadWithOptions(t.Context(), "task-html", "jobs/job/tasks/page-path/task.html.gz", []byte("payload"), UploadOptions{
		ContentType:     "text/html",
		ContentEncoding: "gzip",
	})

	require.NoError(t, err)
	require.NoError(t, receivedBodyErr)
	assert.Equal(t, "task-html/jobs/job/tasks/page-path/task.html.gz", path)
	assert.Equal(t, "text/html", receivedContentType)
	assert.Equal(t, "gzip", receivedContentEncoding)
	assert.Equal(t, "service-role-key", receivedAPIKey)
	assert.Empty(t, receivedAuthorization)
	assert.Equal(t, []byte("payload"), receivedBody)
}

func TestUploadWithOptionsSetsBearerForJWTKeys(t *testing.T) {
	t.Parallel()

	var receivedAPIKey string
	var receivedAuthorization string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAPIKey = r.Header.Get("apikey")
		receivedAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	jwtKey := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.test"
	client := New(server.URL, jwtKey)
	_, err := client.UploadWithOptions(t.Context(), "task-html", "jobs/job/tasks/page-path/task.html.gz", []byte("payload"), UploadOptions{
		ContentType: "text/html",
	})

	require.NoError(t, err)
	assert.Equal(t, jwtKey, receivedAPIKey)
	assert.Equal(t, "Bearer "+jwtKey, receivedAuthorization)
}
