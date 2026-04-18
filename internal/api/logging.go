package api

import (
	"net/http"

	"github.com/Harvey-AU/hover/internal/logging"
)

var apiLog = logging.Component("api")

// loggerWithRequest returns a logger enriched with request context so that all
// API logs include correlation identifiers without repeating boilerplate.
func loggerWithRequest(r *http.Request) *logging.Logger {
	if r == nil {
		return apiLog
	}
	return apiLog.With(
		"request_id", GetRequestID(r),
		"method", r.Method,
		"path", r.URL.Path,
	)
}
