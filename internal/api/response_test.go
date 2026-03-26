package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestResponseHelpers verifies core response helper functions (WriteJSON, WriteSuccess, WriteHealthy) using table-driven tests
func TestResponseHelpers(t *testing.T) {
	tests := []struct {
		name         string
		testFunc     func(*httptest.ResponseRecorder, *http.Request)
		validateFunc func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name: "write_json_with_data",
			testFunc: func(w *httptest.ResponseRecorder, r *http.Request) {
				data := map[string]string{"message": "test"}
				WriteJSON(w, r, data, http.StatusOK)
			},
			validateFunc: func(t *testing.T, w *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusOK, w.Code)
				assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

				var result map[string]string
				err := json.Unmarshal(w.Body.Bytes(), &result)
				assert.NoError(t, err)
				assert.Equal(t, "test", result["message"])
			},
		},
		{
			name: "write_json_with_nil",
			testFunc: func(w *httptest.ResponseRecorder, r *http.Request) {
				WriteJSON(w, r, nil, http.StatusOK)
			},
			validateFunc: func(t *testing.T, w *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusOK, w.Code)
				assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
			},
		},
		{
			name: "write_json_with_unserialisable_data",
			testFunc: func(w *httptest.ResponseRecorder, r *http.Request) {
				WriteJSON(w, r, make(chan int), http.StatusOK)
			},
			validateFunc: func(t *testing.T, w *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusOK, w.Code)
				assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

				// Verify JSON encoding failed - body should not be valid JSON or empty
				var result map[string]any
				err := json.Unmarshal(w.Body.Bytes(), &result)
				assert.Error(t, err, "unserialisable data should fail JSON decoding")
			},
		},
		{
			name: "write_success_with_data",
			testFunc: func(w *httptest.ResponseRecorder, r *http.Request) {
				data := map[string]string{"result": "ok"}
				WriteSuccess(w, r, data, "operation completed")
			},
			validateFunc: func(t *testing.T, w *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusOK, w.Code)
				assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

				var response SuccessResponse
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.NoError(t, err)

				assert.Equal(t, "success", response.Status)
				assert.Equal(t, "operation completed", response.Message)

				dataMap, ok := response.Data.(map[string]any)
				assert.True(t, ok, "response.Data should be map[string]any")
				assert.Equal(t, "ok", dataMap["result"])
			},
		},
		{
			name: "write_success_with_empty_message",
			testFunc: func(w *httptest.ResponseRecorder, r *http.Request) {
				WriteSuccess(w, r, map[string]string{"key": "value"}, "")
			},
			validateFunc: func(t *testing.T, w *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusOK, w.Code)
				assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

				var response SuccessResponse
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.NoError(t, err)

				assert.Equal(t, "success", response.Status)
				assert.Equal(t, "", response.Message)
			},
		},
		{
			name: "write_healthy",
			testFunc: func(w *httptest.ResponseRecorder, r *http.Request) {
				WriteHealthy(w, r, "hover", "1.0.0")
			},
			validateFunc: func(t *testing.T, w *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusOK, w.Code)
				assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

				var response HealthResponse
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.NoError(t, err)

				assert.Equal(t, "healthy", response.Status)
				assert.Equal(t, "hover", response.Service)
				assert.Equal(t, "1.0.0", response.Version)
				assert.NotEmpty(t, response.Timestamp)
			},
		},
		{
			name: "write_healthy_with_empty_service",
			testFunc: func(w *httptest.ResponseRecorder, r *http.Request) {
				WriteHealthy(w, r, "", "2.0.0")
			},
			validateFunc: func(t *testing.T, w *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusOK, w.Code)
				assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

				var response HealthResponse
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.NoError(t, err)

				assert.Equal(t, "healthy", response.Status)
				assert.Equal(t, "", response.Service)
				assert.Equal(t, "2.0.0", response.Version)
				assert.NotEmpty(t, response.Timestamp)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/test", nil)

			tt.testFunc(w, r)
			tt.validateFunc(t, w)
		})
	}
}
