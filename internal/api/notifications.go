package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Harvey-AU/hover/internal/auth"
	"github.com/Harvey-AU/hover/internal/db"
)

// NotificationResponse is the JSON response for a notification
type NotificationResponse struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	Subject   string     `json:"subject"`
	Preview   string     `json:"preview"`
	Message   string     `json:"message,omitempty"`
	Link      string     `json:"link,omitempty"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// NotificationsListResponse is the JSON response for listing notifications
type NotificationsListResponse struct {
	Notifications []NotificationResponse `json:"notifications"`
	Total         int                    `json:"total"`
	UnreadCount   int                    `json:"unread_count"`
}

// NotificationsHandler handles requests to /v1/notifications
func (h *Handler) NotificationsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listNotifications(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// NotificationHandler handles requests to /v1/notifications/{id}
func (h *Handler) NotificationHandler(w http.ResponseWriter, r *http.Request) {
	// Extract notification ID from path
	path := strings.TrimPrefix(r.URL.Path, "/v1/notifications/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		BadRequest(w, r, "notification ID required")
		return
	}
	notificationID := parts[0]

	// Check for /read action
	if len(parts) > 1 && parts[1] == "read" {
		if r.Method == http.MethodPost {
			h.markNotificationRead(w, r, notificationID)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

// NotificationsReadAllHandler handles POST /v1/notifications/read-all
func (h *Handler) NotificationsReadAllHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.markAllNotificationsRead(w, r)
}

func (h *Handler) listNotifications(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	// Get user claims
	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		logger.Warn().Msg("Failed to get user claims")
		Unauthorised(w, r, "Authentication required")
		return
	}

	// Get user and organisation
	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		logger.Warn().Err(err).Msg("User not found")
		Unauthorised(w, r, "User not found")
		return
	}
	orgID := h.DB.GetEffectiveOrganisationID(user)

	// Parse query params
	limit := 10
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 50 {
			limit = parsed
		}
	}

	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	unreadOnly := r.URL.Query().Get("unread") == "true"

	// Get notifications
	notifications, total, err := h.DB.ListNotifications(r.Context(), orgID, limit, offset, unreadOnly)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to list notifications")
		InternalError(w, r, err)
		return
	}

	// Get unread count
	unreadCount, err := h.DB.GetUnreadNotificationCount(r.Context(), orgID)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to get unread count")
		unreadCount = 0
	}

	// Build response
	response := NotificationsListResponse{
		Notifications: make([]NotificationResponse, len(notifications)),
		Total:         total,
		UnreadCount:   unreadCount,
	}

	for i, n := range notifications {
		response.Notifications[i] = notificationToResponse(n)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.Error().Err(err).Msg("Failed to encode notifications response")
	}
}

func (h *Handler) markNotificationRead(w http.ResponseWriter, r *http.Request, notificationID string) {
	logger := loggerWithRequest(r)

	// Get user claims
	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "Authentication required")
		return
	}

	// Get user and organisation
	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		Unauthorised(w, r, "User not found")
		return
	}
	orgID := h.DB.GetEffectiveOrganisationID(user)

	// Mark as read
	if err := h.DB.MarkNotificationRead(r.Context(), notificationID, orgID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			NotFound(w, r, "Notification not found")
			return
		}
		logger.Error().Err(err).Msg("Failed to mark notification read")
		InternalError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) markAllNotificationsRead(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	// Get user claims
	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "Authentication required")
		return
	}

	// Get user and organisation
	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		Unauthorised(w, r, "User not found")
		return
	}
	orgID := h.DB.GetEffectiveOrganisationID(user)

	// Mark all as read
	if err := h.DB.MarkAllNotificationsRead(r.Context(), orgID); err != nil {
		logger.Error().Err(err).Msg("Failed to mark all notifications read")
		InternalError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func notificationToResponse(n *db.Notification) NotificationResponse {
	return NotificationResponse{
		ID:        n.ID,
		Type:      string(n.Type),
		Subject:   n.Subject,
		Preview:   n.Preview,
		Message:   n.Message,
		Link:      n.Link,
		ReadAt:    n.ReadAt,
		CreatedAt: n.CreatedAt,
	}
}
