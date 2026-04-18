package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type shareLinkRecord struct {
	JobID     string
	ExpiresAt sql.NullTime
	RevokedAt sql.NullTime
}

var (
	errShareLinkRevoked = errors.New("share link revoked")
	errShareLinkExpired = errors.New("share link expired")
)

func (h *Handler) createJobShareLink(w http.ResponseWriter, r *http.Request, jobID string) {
	logger := loggerWithRequest(r)

	user := h.validateJobAccess(w, r, jobID)
	if user == nil {
		return
	}

	ctx := r.Context()
	dbConn := h.DB.GetDB()

	var existingToken string
	err := dbConn.QueryRowContext(ctx, `
        SELECT token
        FROM job_share_links
        WHERE job_id = $1
          AND revoked_at IS NULL
          AND (expires_at IS NULL OR expires_at > NOW())
        ORDER BY created_at DESC
        LIMIT 1
    `, jobID).Scan(&existingToken)

	if err == nil {
		shareURL := buildShareURL(r, existingToken)
		WriteSuccess(w, r, map[string]any{
			"token":      existingToken,
			"share_link": shareURL,
		}, "Share link already exists")
		return
	}

	if err != nil {
		if isUndefinedRelationError(err, "job_share_links") {
			logger.Warn("Share link table missing, feature disabled", "error", err)
			ServiceUnavailable(w, r, "Share links are not available yet")
			return
		}

		if !errors.Is(err, sql.ErrNoRows) {
			logger.Error("Failed to query existing share links", "error", err)
			InternalError(w, r, err)
			return
		}
	}

	var token string
	const maxAttempts = 5
	for range maxAttempts {
		candidate, genErr := generateShareToken()
		if genErr != nil {
			logger.Error("Failed to generate share token", "error", genErr)
			InternalError(w, r, genErr)
			return
		}

		_, insertErr := dbConn.ExecContext(ctx, `
            INSERT INTO job_share_links (job_id, token, created_by)
            VALUES ($1, $2, $3)
        `, jobID, candidate, user.ID)
		if insertErr == nil {
			token = candidate
			break
		}

		if isUndefinedRelationError(insertErr, "job_share_links") {
			logger.Warn("Share link table missing during insert", "error", insertErr)
			ServiceUnavailable(w, r, "Share links are not available yet")
			return
		}

		if isUniqueViolation(insertErr) {
			continue
		}

		logger.Error("Failed to insert share link", "error", insertErr)
		InternalError(w, r, insertErr)
		return
	}

	if token == "" {
		logger.Error("exhausted attempts to generate unique share token")
		InternalError(w, r, errors.New("failed to generate share token"))
		return
	}

	shareURL := buildShareURL(r, token)
	WriteSuccess(w, r, map[string]any{
		"token":      token,
		"share_link": shareURL,
	}, "Share link created successfully")
}

func (h *Handler) getJobShareLink(w http.ResponseWriter, r *http.Request, jobID string) {
	logger := loggerWithRequest(r)

	user := h.validateJobAccess(w, r, jobID)
	if user == nil {
		return
	}

	var token string
	err := h.DB.GetDB().QueryRowContext(r.Context(), `
        SELECT token
        FROM job_share_links
        WHERE job_id = $1
          AND revoked_at IS NULL
          AND (expires_at IS NULL OR expires_at > NOW())
        ORDER BY created_at DESC
        LIMIT 1
    `, jobID).Scan(&token)
	if err != nil {
		if isUndefinedRelationError(err, "job_share_links") {
			logger.Warn("Share link table missing during fetch", "error", err)
			ServiceUnavailable(w, r, "Share links are not available yet")
			return
		}

		if errors.Is(err, sql.ErrNoRows) {
			// No share link exists - return 200 with exists:false instead of 404
			WriteSuccess(w, r, map[string]any{
				"exists": false,
			}, "No active share link")
			return
		}

		logger.Error("Failed to fetch share link", "error", err)
		InternalError(w, r, err)
		return
	}

	shareURL := buildShareURL(r, token)
	WriteSuccess(w, r, map[string]any{
		"exists":     true,
		"token":      token,
		"share_link": shareURL,
	}, "Share link active")
}

func (h *Handler) revokeJobShareLink(w http.ResponseWriter, r *http.Request, jobID, token string) {
	logger := loggerWithRequest(r)

	user := h.validateJobAccess(w, r, jobID)
	if user == nil {
		return
	}

	result, err := h.DB.GetDB().ExecContext(r.Context(), `
        UPDATE job_share_links
        SET revoked_at = NOW()
        WHERE job_id = $1 AND token = $2 AND revoked_at IS NULL
    `, jobID, token)
	if err != nil {
		if isUndefinedRelationError(err, "job_share_links") {
			logger.Warn("Share link table missing during revoke", "error", err)
			ServiceUnavailable(w, r, "Share links are not available yet")
			return
		}
		logger.Error("Failed to revoke share link", "error", err)
		InternalError(w, r, err)
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		NotFound(w, r, "Share link not found")
		return
	}

	WriteSuccess(w, r, map[string]any{
		"revoked": true,
	}, "Share link revoked")
}

func (h *Handler) SharedJobHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/shared/jobs/")
	if path == "" {
		NotFound(w, r, "Share token is required")
		return
	}

	parts := strings.Split(path, "/")
	token := parts[0]

	if len(parts) > 1 {
		switch parts[1] {
		case "tasks":
			h.getSharedJobTasks(w, r, token)
			return
		case "export":
			h.exportSharedJobTasks(w, r, token)
			return
		default:
			NotFound(w, r, "Endpoint not found")
			return
		}
	}

	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	h.getSharedJob(w, r, token)
}

func (h *Handler) getSharedJob(w http.ResponseWriter, r *http.Request, token string) {
	record, err := h.lookupShareLink(r.Context(), token)
	if err != nil {
		h.handleShareLinkError(w, r, err)
		return
	}

	response, err := h.fetchJobResponse(r.Context(), record.JobID, nil)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			NotFound(w, r, "Job not found")
			return
		}
		InternalError(w, r, err)
		return
	}

	WriteSuccess(w, r, response, "Job retrieved successfully")
}

func (h *Handler) getSharedJobTasks(w http.ResponseWriter, r *http.Request, token string) {
	logger := loggerWithRequest(r)

	record, err := h.lookupShareLink(r.Context(), token)
	if err != nil {
		h.handleShareLinkError(w, r, err)
		return
	}

	params := parseTaskQueryParams(r)
	queries := buildTaskQuery(record.JobID, params)

	dbConn := h.DB.GetDB()

	var total int
	countArgs := queries.Args[:len(queries.Args)-2]
	err = dbConn.QueryRowContext(r.Context(), queries.CountQuery, countArgs...).Scan(&total)
	if err != nil {
		if HandlePoolSaturation(w, r, err) {
			return
		}
		logger.Error("Failed to count shared tasks", "error", err)
		DatabaseError(w, r, err)
		return
	}

	rows, err := dbConn.QueryContext(r.Context(), queries.SelectQuery, queries.Args...)
	if err != nil {
		if HandlePoolSaturation(w, r, err) {
			return
		}
		logger.Error("Failed to get shared tasks", "error", err)
		DatabaseError(w, r, err)
		return
	}
	defer rows.Close()

	tasks, err := formatTasksFromRows(rows)
	if err != nil {
		logger.Error("Failed to format shared tasks", "error", err)
		DatabaseError(w, r, err)
		return
	}

	hasNext := params.Offset+params.Limit < total
	hasPrev := params.Offset > 0

	response := map[string]any{
		"tasks": tasks,
		"pagination": map[string]any{
			"limit":    params.Limit,
			"offset":   params.Offset,
			"total":    total,
			"has_next": hasNext,
			"has_prev": hasPrev,
		},
	}

	WriteSuccess(w, r, response, "Tasks retrieved successfully")
}

func (h *Handler) exportSharedJobTasks(w http.ResponseWriter, r *http.Request, token string) {
	record, err := h.lookupShareLink(r.Context(), token)
	if err != nil {
		h.handleShareLinkError(w, r, err)
		return
	}

	h.serveJobExport(w, r, record.JobID, false)
}

func (h *Handler) handleShareLinkError(w http.ResponseWriter, r *http.Request, err error) {
	logger := loggerWithRequest(r)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		NotFound(w, r, "Share link not found")
	case errors.Is(err, errShareLinkRevoked):
		NotFound(w, r, "Share link has been revoked")
	case errors.Is(err, errShareLinkExpired):
		NotFound(w, r, "Share link has expired")
	default:
		if HandlePoolSaturation(w, r, err) {
			return
		}
		logger.Error("Share link error", "error", err)
		InternalError(w, r, err)
	}
}

func (h *Handler) lookupShareLink(ctx context.Context, token string) (*shareLinkRecord, error) {
	if token == "" {
		return nil, sql.ErrNoRows
	}

	var record shareLinkRecord
	err := h.DB.GetDB().QueryRowContext(ctx, `
        SELECT job_id, expires_at, revoked_at
        FROM job_share_links
        WHERE token = $1
    `, token).Scan(&record.JobID, &record.ExpiresAt, &record.RevokedAt)
	if err != nil {
		if isUndefinedRelationError(err, "job_share_links") {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}

	if record.RevokedAt.Valid {
		return nil, errShareLinkRevoked
	}
	if record.ExpiresAt.Valid && record.ExpiresAt.Time.Before(time.Now()) {
		return nil, errShareLinkExpired
	}

	return &record, nil
}

func generateShareToken() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(buf)
	return token, nil
}

func buildShareURL(r *http.Request, token string) string {
	scheme := "https"
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		scheme = forwarded
	} else if r.TLS == nil {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s/shared/jobs/%s", scheme, r.Host, token)
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	// We only need to detect uniqueness violations; defer to string check to avoid pulling in pq dependency here
	return strings.Contains(err.Error(), "unique")
}

func isUndefinedRelationError(err error, relation string) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "42p01") {
		return true
	}

	if relation != "" && strings.Contains(msg, fmt.Sprintf("relation \"%s\"", strings.ToLower(relation))) {
		return true
	}

	return false
}
