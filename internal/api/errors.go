package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/Harvey-AU/hover/internal/db"
)

// ErrorResponse represents a standardised error response
type ErrorResponse struct {
	Status    int    `json:"status"`
	Message   string `json:"message"`
	Code      string `json:"code,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// ErrorCode represents standard error codes
type ErrorCode string

const (
	// Client errors (4xx)
	ErrCodeBadRequest       ErrorCode = "BAD_REQUEST"
	ErrCodeUnauthorised     ErrorCode = "UNAUTHORISED"
	ErrCodeForbidden        ErrorCode = "FORBIDDEN"
	ErrCodeNotFound         ErrorCode = "NOT_FOUND"
	ErrCodeMethodNotAllowed ErrorCode = "METHOD_NOT_ALLOWED"
	ErrCodeConflict         ErrorCode = "CONFLICT"
	ErrCodeValidation       ErrorCode = "VALIDATION_ERROR"
	ErrCodeRateLimit        ErrorCode = "RATE_LIMIT_EXCEEDED"

	// Server errors (5xx)
	ErrCodeInternal           ErrorCode = "INTERNAL_ERROR"
	ErrCodeServiceUnavailable ErrorCode = "SERVICE_UNAVAILABLE"
	ErrCodeDatabaseError      ErrorCode = "DATABASE_ERROR"
)

// WriteError writes a standardised error response
func WriteError(w http.ResponseWriter, r *http.Request, err error, status int, code ErrorCode) {
	requestID := GetRequestID(r)

	errResp := ErrorResponse{
		Status:    status,
		Message:   err.Error(),
		Code:      string(code),
		RequestID: requestID,
	}

	// Log the error with context - 4xx are client errors (debug), 5xx are server errors (error)
	logger := loggerWithRequest(r)
	if status >= 500 {
		logger.Error("API error response", "error", err, "request_id", requestID, "status", status, "code", string(code))
	} else {
		logger.Debug("API client error response", "error", err, "request_id", requestID, "status", status, "code", string(code))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errResp); err != nil {
		logger.Error("Failed to encode error response", "error", err)
	}
}

// WriteErrorMessage writes a standardised error response with a custom message
func WriteErrorMessage(w http.ResponseWriter, r *http.Request, message string, status int, code ErrorCode) {
	requestID := GetRequestID(r)

	errResp := ErrorResponse{
		Status:    status,
		Message:   message,
		Code:      string(code),
		RequestID: requestID,
	}

	// Log the error with context - 4xx are client errors (debug), 5xx are server errors (error)
	logger := loggerWithRequest(r)
	if status >= 500 {
		logger.Error("API error response", "request_id", requestID, "status", status, "code", string(code), "message", message)
	} else {
		logger.Debug("API client error response", "request_id", requestID, "status", status, "code", string(code), "message", message)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errResp); err != nil {
		logger.Error("Failed to encode error response", "error", err)
	}
}

// Common error helpers for frequent use cases

// BadRequest responds with a 400 Bad Request error
func BadRequest(w http.ResponseWriter, r *http.Request, message string) {
	WriteErrorMessage(w, r, message, http.StatusBadRequest, ErrCodeBadRequest)
}

// Unauthorised responds with a 401 Unauthorised error
func Unauthorised(w http.ResponseWriter, r *http.Request, message string) {
	WriteErrorMessage(w, r, message, http.StatusUnauthorized, ErrCodeUnauthorised)
}

// Forbidden responds with a 403 Forbidden error
func Forbidden(w http.ResponseWriter, r *http.Request, message string) {
	WriteErrorMessage(w, r, message, http.StatusForbidden, ErrCodeForbidden)
}

// NotFound responds with a 404 Not Found error
func NotFound(w http.ResponseWriter, r *http.Request, message string) {
	WriteErrorMessage(w, r, message, http.StatusNotFound, ErrCodeNotFound)
}

// MethodNotAllowed responds with a 405 Method Not Allowed error
func MethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	WriteErrorMessage(w, r, "Method not allowed", http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed)
}

// InternalError responds with a 500 Internal Server Error
func InternalError(w http.ResponseWriter, r *http.Request, err error) {
	WriteError(w, r, err, http.StatusInternalServerError, ErrCodeInternal)
}

// DatabaseError responds with a 500 error for database issues
func DatabaseError(w http.ResponseWriter, r *http.Request, err error) {
	WriteError(w, r, err, http.StatusInternalServerError, ErrCodeDatabaseError)
}

// ServiceUnavailable responds with a 503 Service Unavailable error
func ServiceUnavailable(w http.ResponseWriter, r *http.Request, message string) {
	WriteErrorMessage(w, r, message, http.StatusServiceUnavailable, ErrCodeServiceUnavailable)
}

// TooManyRequests responds with 429 and Retry-After header
func TooManyRequests(w http.ResponseWriter, r *http.Request, message string, retryAfter time.Duration) {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds <= 0 {
		seconds = 3
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	WriteErrorMessage(w, r, message, http.StatusTooManyRequests, ErrCodeRateLimit)
}

// HandlePoolSaturation writes a 429 when the error indicates pool exhaustion.
func HandlePoolSaturation(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, db.ErrPoolSaturated) {
		TooManyRequests(w, r, "Database is busy, please retry shortly", 3*time.Second)
		return true
	}
	return false
}
