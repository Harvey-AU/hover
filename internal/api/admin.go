package api

import (
	"net/http"
	"os"
	"time"

	"github.com/Harvey-AU/hover/internal/auth"
	"github.com/getsentry/sentry-go"
)

// AdminResetDatabase handles the admin database reset endpoint
// Requires valid JWT with admin role and explicit environment enablement
func (h *Handler) AdminResetDatabase(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	// Require explicit enablement
	if os.Getenv("ALLOW_DB_RESET") != "true" {
		Forbidden(w, r, "Database reset not enabled. Set ALLOW_DB_RESET=true to enable")
		return
	}

	// Get user claims from context (set by AuthMiddleware)
	claims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "Authentication required for admin endpoint")
		return
	}

	// Verify system admin role
	if !hasSystemAdminRole(claims) {
		logger.Warn("Non-system-admin user attempted to access database reset endpoint", "user_id", claims.UserID)
		Forbidden(w, r, "System administrator privileges required")
		return
	}

	// Verify user exists in database
	user, err := h.DB.GetUser(claims.UserID)
	if err != nil {
		logger.Error("Failed to verify admin user", "error", err, "user_id", claims.UserID)
		Unauthorised(w, r, "User verification failed")
		return
	}

	// Log the admin action with full context
	orgIDStr := "none"
	if user.OrganisationID != nil {
		orgIDStr = *user.OrganisationID
	}
	logger.Warn("Admin database reset requested",
		"user_id", user.ID,
		"org_id", orgIDStr,
		"remote_addr", r.RemoteAddr,
		"user_agent", r.Header.Get("User-Agent"),
	)

	// Capture in Sentry for audit trail
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("event_type", "admin_action")
		scope.SetTag("action", "database_reset")
		scope.SetUser(sentry.User{
			ID:    user.ID,
			Email: user.Email,
		})
		scope.SetContext("admin_action", map[string]any{
			"endpoint":   "/v1/admin/reset-db",
			"user_agent": r.Header.Get("User-Agent"),
			"ip_address": r.RemoteAddr,
		})
		sentry.CaptureMessage("Admin database reset action")
	})

	// Perform the database reset
	resetStart := time.Now()
	if err := h.DB.ResetSchema(); err != nil {
		resetDuration := time.Since(resetStart)
		logger.Error("Failed to reset database schema", "error", err, "user_id", user.ID, "duration", resetDuration)

		// Capture failure in Sentry
		sentry.WithScope(func(scope *sentry.Scope) {
			scope.SetLevel(sentry.LevelError)
			scope.SetTag("event_type", "admin_action_failed")
			scope.SetTag("action", "database_reset")
			scope.SetUser(sentry.User{
				ID:    user.ID,
				Email: user.Email,
			})
			scope.SetContext("error_details", map[string]any{
				"error":    err.Error(),
				"duration": resetDuration.Milliseconds(),
			})
			sentry.CaptureException(err)
		})

		InternalError(w, r, err)
		return
	}

	resetDuration := time.Since(resetStart)
	logger.Warn("Database schema reset completed successfully by admin", "user_id", user.ID, "duration", resetDuration)

	// Capture success in Sentry for audit trail
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelInfo)
		scope.SetTag("event_type", "admin_action_success")
		scope.SetTag("action", "database_reset")
		scope.SetUser(sentry.User{
			ID:    user.ID,
			Email: user.Email,
		})
		scope.SetContext("success_details", map[string]any{
			"duration_ms": resetDuration.Milliseconds(),
			"timestamp":   time.Now().UTC().Format(time.RFC3339),
		})
		sentry.CaptureMessage("Database reset completed successfully")
	})

	WriteSuccess(w, r, nil, "Database schema reset successfully - Supabase will rebuild from migrations")
}

// AdminResetData handles the admin data-only reset endpoint
// Clears all data but preserves schema - safe option for clearing test data
func (h *Handler) AdminResetData(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	// Require explicit enablement
	if os.Getenv("ALLOW_DB_RESET") != "true" {
		Forbidden(w, r, "Database operations not enabled. Set ALLOW_DB_RESET=true to enable")
		return
	}

	// Get user claims from context (set by AuthMiddleware)
	claims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "Authentication required for admin endpoint")
		return
	}

	// Verify system admin role
	if !hasSystemAdminRole(claims) {
		logger.Warn("Non-system-admin user attempted to access data reset endpoint", "user_id", claims.UserID)
		Forbidden(w, r, "System administrator privileges required")
		return
	}

	// Verify user exists in database
	user, err := h.DB.GetUser(claims.UserID)
	if err != nil {
		logger.Error("Failed to verify admin user", "error", err, "user_id", claims.UserID)
		Unauthorised(w, r, "User verification failed")
		return
	}

	// Log the admin action with full context
	orgIDStr2 := "none"
	if user.OrganisationID != nil {
		orgIDStr2 = *user.OrganisationID
	}
	logger.Warn("Admin data-only reset requested",
		"user_id", user.ID,
		"org_id", orgIDStr2,
		"remote_addr", r.RemoteAddr,
		"user_agent", r.Header.Get("User-Agent"),
	)

	// Capture in Sentry for audit trail
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("event_type", "admin_action")
		scope.SetTag("action", "data_reset")
		scope.SetUser(sentry.User{
			ID:    user.ID,
			Email: user.Email,
		})
		scope.SetContext("admin_action", map[string]any{
			"endpoint":   "/v1/admin/reset-data",
			"user_agent": r.Header.Get("User-Agent"),
			"ip_address": r.RemoteAddr,
		})
		sentry.CaptureMessage("Admin data reset action")
	})

	// Perform the data-only reset
	resetStart := time.Now()
	if err := h.DB.ResetDataOnly(); err != nil {
		resetDuration := time.Since(resetStart)
		logger.Error("Failed to reset data", "error", err, "user_id", user.ID, "duration", resetDuration)

		// Capture failure in Sentry
		sentry.WithScope(func(scope *sentry.Scope) {
			scope.SetLevel(sentry.LevelError)
			scope.SetTag("event_type", "admin_action_failed")
			scope.SetTag("action", "data_reset")
			scope.SetUser(sentry.User{
				ID:    user.ID,
				Email: user.Email,
			})
			scope.SetContext("error_details", map[string]any{
				"error":    err.Error(),
				"duration": resetDuration.Milliseconds(),
			})
			sentry.CaptureException(err)
		})

		InternalError(w, r, err)
		return
	}

	resetDuration := time.Since(resetStart)
	logger.Warn("Data reset completed successfully by admin", "user_id", user.ID, "duration", resetDuration)

	// Capture success in Sentry for audit trail
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelInfo)
		scope.SetTag("event_type", "admin_action_success")
		scope.SetTag("action", "data_reset")
		scope.SetUser(sentry.User{
			ID:    user.ID,
			Email: user.Email,
		})
		scope.SetContext("success_details", map[string]any{
			"duration_ms": resetDuration.Milliseconds(),
			"timestamp":   time.Now().UTC().Format(time.RFC3339),
		})
		sentry.CaptureMessage("Data reset completed successfully")
	})

	WriteSuccess(w, r, nil, "Data cleared successfully - schema preserved")
}

// hasSystemAdminRole checks if the user has system administrator privileges via app_metadata
// This is distinct from organisation-level admin roles - system admins are Hover operators
// who have elevated privileges for system-level operations like database resets
func hasSystemAdminRole(claims *auth.UserClaims) bool {
	if claims == nil || claims.AppMetadata == nil {
		return false
	}

	// Check for system_role = "system_admin" in app_metadata
	if systemRole, exists := claims.AppMetadata["system_role"]; exists {
		if roleStr, ok := systemRole.(string); ok && roleStr == "system_admin" {
			return true
		}
	}

	return false
}
