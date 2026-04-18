package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// SuccessResponse represents a standardised success response
type SuccessResponse struct {
	Status    string `json:"status"`
	Data      any    `json:"data,omitempty"`
	Message   string `json:"message,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteJSON writes a JSON response with the given status code
func WriteJSON(w http.ResponseWriter, r *http.Request, data any, status int) {
	requestID := GetRequestID(r)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		loggerWithRequest(r).Error("Failed to encode JSON response", "error", err, "request_id", requestID)
	}
}

// WriteSuccess writes a standardised success response
func WriteSuccess(w http.ResponseWriter, r *http.Request, data any, message string) {
	requestID := GetRequestID(r)

	response := SuccessResponse{
		Status:    "success",
		Data:      data,
		Message:   message,
		RequestID: requestID,
	}

	WriteJSON(w, r, response, http.StatusOK)
}

// WriteCreated writes a standardised success response for created resources
func WriteCreated(w http.ResponseWriter, r *http.Request, data any, message string) {
	requestID := GetRequestID(r)

	response := SuccessResponse{
		Status:    "success",
		Data:      data,
		Message:   message,
		RequestID: requestID,
	}

	WriteJSON(w, r, response, http.StatusCreated)
}

// WriteNoContent writes a 204 No Content response
func WriteNoContent(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// HealthResponse represents a health check response
type HealthResponse struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
	Service   string `json:"service"`
	Version   string `json:"version,omitempty"`
}

// WriteHealthy writes a standardised health check response
func WriteHealthy(w http.ResponseWriter, r *http.Request, service string, version string) {
	response := HealthResponse{
		Status:    "healthy",
		Timestamp: time.Now().Format(time.RFC3339),
		Service:   service,
		Version:   version,
	}

	WriteJSON(w, r, response, http.StatusOK)
}

// WriteUnhealthy writes a standardised unhealthy response
func WriteUnhealthy(w http.ResponseWriter, r *http.Request, service string, err error) {
	requestID := GetRequestID(r)

	response := map[string]any{
		"status":     "unhealthy",
		"timestamp":  time.Now().Format(time.RFC3339),
		"service":    service,
		"error":      err.Error(),
		"request_id": requestID,
	}

	WriteJSON(w, r, response, http.StatusServiceUnavailable)
}
