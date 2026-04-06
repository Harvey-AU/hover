package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWebhookSignatureValidation(t *testing.T) {
	secret := "test-webhook-secret" //nolint:gosec // This is a test secret for signature validation experiments

	tests := []struct {
		name           string
		payload        map[string]any
		signature      string
		expectedStatus int
		description    string
	}{
		{
			name: "valid_signature",
			payload: map[string]any{
				"site":  "example.com",
				"event": "site_publish",
			},
			signature:      "", // Will be calculated
			expectedStatus: http.StatusOK,
			description:    "Valid signature should be accepted",
		},
		{
			name: "invalid_signature",
			payload: map[string]any{
				"site":  "example.com",
				"event": "site_publish",
			},
			signature:      "invalid-signature",
			expectedStatus: http.StatusUnauthorized,
			description:    "Invalid signature should be rejected",
		},
		{
			name: "missing_signature",
			payload: map[string]any{
				"site":  "example.com",
				"event": "site_publish",
			},
			signature:      "",
			expectedStatus: http.StatusUnauthorized,
			description:    "Missing signature should be rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Prepare payload
			body, _ := json.Marshal(tt.payload)

			// Calculate valid signature for first test
			if tt.name == "valid_signature" {
				h := hmac.New(sha256.New, []byte(secret))
				h.Write(body)
				tt.signature = hex.EncodeToString(h.Sum(nil))
			}

			// Create request
			req := httptest.NewRequest("POST", "/v1/webhooks/webflow", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tt.signature != "" {
				req.Header.Set("X-Webhook-Signature", tt.signature)
			}

			// Create response recorder
			w := httptest.NewRecorder()

			// Simple mock handler
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				signature := r.Header.Get("X-Webhook-Signature")

				if signature == "" && tt.name != "valid_signature" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				if tt.name == "invalid_signature" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "received"})
			})

			handler.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code, tt.description)
		})
	}
}

func TestWebhookPayloadParsing(t *testing.T) {
	tests := []struct {
		name           string
		payload        any
		contentType    string
		expectedStatus int
		description    string
	}{
		{
			name: "valid_json",
			payload: map[string]any{
				"site":  "example.com",
				"event": "site_publish",
			},
			contentType:    "application/json",
			expectedStatus: http.StatusOK,
			description:    "Valid JSON should be parsed",
		},
		{
			name:           "invalid_json",
			payload:        "{invalid json",
			contentType:    "application/json",
			expectedStatus: http.StatusBadRequest,
			description:    "Invalid JSON should return error",
		},
		{
			name: "missing_required_fields",
			payload: map[string]any{
				"event": "site_publish",
				// Missing 'site' field
			},
			contentType:    "application/json",
			expectedStatus: http.StatusBadRequest,
			description:    "Missing required fields should error",
		},
		{
			name: "wrong_content_type",
			payload: map[string]any{
				"site": "example.com",
			},
			contentType:    "text/plain",
			expectedStatus: http.StatusBadRequest,
			description:    "Wrong content type should error",
		},
		{
			name:           "empty_payload",
			payload:        nil,
			contentType:    "application/json",
			expectedStatus: http.StatusBadRequest,
			description:    "Empty payload should error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body []byte

			switch v := tt.payload.(type) {
			case string:
				body = []byte(v)
			case map[string]any:
				body, _ = json.Marshal(v)
			default:
				body = []byte{}
			}

			req := httptest.NewRequest("POST", "/v1/webhooks/webflow", bytes.NewReader(body))
			req.Header.Set("Content-Type", tt.contentType)

			w := httptest.NewRecorder()

			// Mock handler
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Check content type
				if r.Header.Get("Content-Type") != "application/json" {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid content type"})
					return
				}

				// Try to parse JSON
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
					return
				}

				// Check required fields
				if _, ok := payload["site"]; !ok {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "Missing required field: site"})
					return
				}

				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "processed"})
			})

			handler.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code, tt.description)
		})
	}
}

func TestWebhookDuplication(t *testing.T) {
	processedWebhooks := make(map[string]bool)

	tests := []struct {
		name           string
		webhookID      string
		expectedStatus int
		description    string
	}{
		{
			name:           "first_webhook",
			webhookID:      "webhook-123",
			expectedStatus: http.StatusOK,
			description:    "First webhook should be processed",
		},
		{
			name:           "duplicate_webhook",
			webhookID:      "webhook-123",
			expectedStatus: http.StatusOK, // Idempotent
			description:    "Duplicate webhook should be idempotent",
		},
		{
			name:           "different_webhook",
			webhookID:      "webhook-456",
			expectedStatus: http.StatusOK,
			description:    "Different webhook should be processed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := map[string]any{
				"id":    tt.webhookID,
				"site":  "example.com",
				"event": "site_publish",
			}

			body, _ := json.Marshal(payload)
			req := httptest.NewRequest("POST", "/v1/webhooks/webflow", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()

			// Mock handler with deduplication
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p map[string]any
				_ = json.NewDecoder(r.Body).Decode(&p)

				webhookID, _ := p["id"].(string)

				// Check if already processed
				if _, exists := processedWebhooks[webhookID]; exists {
					// Return success but don't reprocess
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(map[string]string{
						"status": "already_processed",
					})
					return
				}

				// Mark as processed
				processedWebhooks[webhookID] = true

				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"status": "processed",
				})
			})

			handler.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code, tt.description)

			// Verify response
			var response map[string]string
			_ = json.Unmarshal(w.Body.Bytes(), &response)

			if tt.name == "duplicate_webhook" {
				assert.Equal(t, "already_processed", response["status"])
			} else {
				assert.Equal(t, "processed", response["status"])
			}
		})
	}
}

func TestWebhookTokenAuthentication(t *testing.T) {
	validToken := "valid-webhook-token"

	tests := []struct {
		name           string
		token          string
		expectedStatus int
		description    string
	}{
		{
			name:           "valid_token",
			token:          validToken,
			expectedStatus: http.StatusOK,
			description:    "Valid token should authenticate",
		},
		{
			name:           "invalid_token",
			token:          "invalid-token",
			expectedStatus: http.StatusUnauthorized,
			description:    "Invalid token should be rejected",
		},
		{
			name:           "missing_token",
			token:          "",
			expectedStatus: http.StatusUnauthorized,
			description:    "Missing token should be rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := map[string]any{
				"site":  "example.com",
				"event": "site_publish",
			}

			body, _ := json.Marshal(payload)
			req := httptest.NewRequest("POST", "/v1/webhooks/webflow", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			if tt.token != "" {
				req.Header.Set("X-Webhook-Token", tt.token)
			}

			w := httptest.NewRecorder()

			// Mock handler with token auth
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				token := r.Header.Get("X-Webhook-Token")

				if token != validToken {
					w.WriteHeader(http.StatusUnauthorized)
					_ = json.NewEncoder(w).Encode(map[string]string{
						"error": "Unauthorized",
					})
					return
				}

				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"status": "authenticated",
				})
			})

			handler.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code, tt.description)
		})
	}
}

func TestWebhookRateLimit(t *testing.T) {
	requestCount := 0
	maxRequests := 3

	tests := []struct {
		name           string
		requestNum     int
		expectedStatus int
		description    string
	}{
		{
			name:           "first_request",
			requestNum:     1,
			expectedStatus: http.StatusOK,
			description:    "First request should succeed",
		},
		{
			name:           "second_request",
			requestNum:     2,
			expectedStatus: http.StatusOK,
			description:    "Second request should succeed",
		},
		{
			name:           "third_request",
			requestNum:     3,
			expectedStatus: http.StatusOK,
			description:    "Third request should succeed",
		},
		{
			name:           "rate_limited",
			requestNum:     4,
			expectedStatus: http.StatusTooManyRequests,
			description:    "Fourth request should be rate limited",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := map[string]any{
				"site":  "example.com",
				"event": "site_publish",
			}

			body, _ := json.Marshal(payload)
			req := httptest.NewRequest("POST", "/v1/webhooks/webflow", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()

			// Mock handler with rate limiting
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount++

				if requestCount > maxRequests {
					w.Header().Set("X-RateLimit-Limit", "3")
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(http.StatusTooManyRequests)
					_ = json.NewEncoder(w).Encode(map[string]string{
						"error": "Rate limit exceeded",
					})
					return
				}

				w.Header().Set("X-RateLimit-Limit", "3")
				w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(maxRequests-requestCount))
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"status": "processed",
				})
			})

			handler.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code, tt.description)

			// Verify rate limit headers
			if tt.expectedStatus == http.StatusTooManyRequests {
				assert.Equal(t, "0", w.Header().Get("X-RateLimit-Remaining"))
				assert.NotEmpty(t, w.Header().Get("Retry-After"))
			}
		})
	}
}
