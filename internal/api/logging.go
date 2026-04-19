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

// loggerWithRequestPath returns a request-scoped logger but overrides the
// `path` field with the caller-supplied value. Use this for routes where the
// raw URL path contains a secret (e.g. legacy webhook-token URLs) so bearer
// credentials do not end up in logs or Sentry breadcrumbs.
func loggerWithRequestPath(r *http.Request, path string) *logging.Logger {
	if r == nil {
		return apiLog.With("path", path)
	}
	return apiLog.With(
		"request_id", GetRequestID(r),
		"method", r.Method,
		"path", path,
	)
}
