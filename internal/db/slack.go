package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrSlackConnectionNotFound is returned when a slack connection is not found
var ErrSlackConnectionNotFound = errors.New("slack connection not found")

// ErrSlackUserLinkNotFound is returned when a slack user link is not found
var ErrSlackUserLinkNotFound = errors.New("slack user link not found")

// SlackConnection represents an organisation's connection to a Slack workspace
type SlackConnection struct {
	ID               string
	OrganisationID   string
	WorkspaceID      string
	WorkspaceName    string
	VaultSecretName  string // Name of the secret in Supabase Vault
	BotUserID        string
	InstallingUserID *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// SlackUserLink represents a GNH user linked to their Slack identity
type SlackUserLink struct {
	ID                string
	UserID            string
	SlackConnectionID string
	SlackUserID       string
	DMNotifications   bool
	CreatedAt         time.Time
}

// CreateSlackConnection creates a new Slack connection for an organisation
// Note: Use StoreSlackToken after creating the connection to store the access token in Vault
func (db *DB) CreateSlackConnection(ctx context.Context, conn *SlackConnection) error {
	query := `
		INSERT INTO slack_connections (
			id, organisation_id, workspace_id, workspace_name,
			bot_user_id, installing_user_id, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (organisation_id, workspace_id)
		DO UPDATE SET
			workspace_name = EXCLUDED.workspace_name,
			bot_user_id = EXCLUDED.bot_user_id,
			updated_at = EXCLUDED.updated_at
		RETURNING id
	`

	err := db.client.QueryRowContext(ctx, query,
		conn.ID, conn.OrganisationID, conn.WorkspaceID, conn.WorkspaceName,
		conn.BotUserID, conn.InstallingUserID, conn.CreatedAt, conn.UpdatedAt,
	).Scan(&conn.ID)
	if err != nil {
		dbLog.Error("Failed to create slack connection", "error", err, "organisation_id", conn.OrganisationID, "workspace_id", conn.WorkspaceID)
		return fmt.Errorf("failed to create slack connection: %w", err)
	}

	return nil
}

// StoreSlackToken stores a Slack access token in Supabase Vault
func (db *DB) StoreSlackToken(ctx context.Context, connectionID, token string) error {
	query := `SELECT store_slack_token($1::uuid, $2)`

	// Function returns secret name but we don't need it - just scan to consume the result
	if err := db.client.QueryRowContext(ctx, query, connectionID, token).Scan(new(string)); err != nil {
		dbLog.Error("Failed to store slack token in vault", "error", err, "connection_id", connectionID)
		return fmt.Errorf("failed to store slack token: %w", err)
	}

	return nil
}

// GetSlackToken retrieves a Slack access token from Supabase Vault
func (db *DB) GetSlackToken(ctx context.Context, connectionID string) (string, error) {
	query := `SELECT get_slack_token($1::uuid)`

	var token sql.NullString
	err := db.client.QueryRowContext(ctx, query, connectionID).Scan(&token)
	if err != nil {
		dbLog.Error("Failed to get slack token from vault", "error", err, "connection_id", connectionID)
		return "", fmt.Errorf("failed to get slack token: %w", err)
	}

	if !token.Valid {
		return "", fmt.Errorf("slack token not found for connection %s", connectionID)
	}

	return token.String, nil
}

// GetSlackConnection retrieves a Slack connection by ID
func (db *DB) GetSlackConnection(ctx context.Context, connectionID string) (*SlackConnection, error) {
	conn := &SlackConnection{}
	var installingUserID sql.NullString
	var workspaceName, botUserID, vaultSecretName sql.NullString

	query := `
		SELECT id, organisation_id, workspace_id, workspace_name,
		       vault_secret_name, bot_user_id, installing_user_id,
		       created_at, updated_at
		FROM slack_connections
		WHERE id = $1
	`

	err := db.client.QueryRowContext(ctx, query, connectionID).Scan(
		&conn.ID, &conn.OrganisationID, &conn.WorkspaceID, &workspaceName,
		&vaultSecretName, &botUserID, &installingUserID,
		&conn.CreatedAt, &conn.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrSlackConnectionNotFound
		}
		dbLog.Error("Failed to get slack connection", "error", err, "connection_id", connectionID)
		return nil, fmt.Errorf("failed to get slack connection: %w", err)
	}

	if workspaceName.Valid {
		conn.WorkspaceName = workspaceName.String
	}
	if vaultSecretName.Valid {
		conn.VaultSecretName = vaultSecretName.String
	}
	if botUserID.Valid {
		conn.BotUserID = botUserID.String
	}
	if installingUserID.Valid {
		conn.InstallingUserID = &installingUserID.String
	}

	return conn, nil
}

// ListSlackConnections lists all Slack connections for an organisation
func (db *DB) ListSlackConnections(ctx context.Context, organisationID string) ([]*SlackConnection, error) {
	query := `
		SELECT id, organisation_id, workspace_id, workspace_name,
		       vault_secret_name, bot_user_id, installing_user_id,
		       created_at, updated_at
		FROM slack_connections
		WHERE organisation_id = $1
		ORDER BY created_at DESC
	`

	rows, err := db.client.QueryContext(ctx, query, organisationID)
	if err != nil {
		dbLog.Error("Failed to list slack connections", "error", err, "organisation_id", organisationID)
		return nil, fmt.Errorf("failed to list slack connections: %w", err)
	}
	defer rows.Close()

	var connections []*SlackConnection
	for rows.Next() {
		conn := &SlackConnection{}
		var installingUserID sql.NullString
		var workspaceName, botUserID, vaultSecretName sql.NullString

		err := rows.Scan(
			&conn.ID, &conn.OrganisationID, &conn.WorkspaceID, &workspaceName,
			&vaultSecretName, &botUserID, &installingUserID,
			&conn.CreatedAt, &conn.UpdatedAt,
		)
		if err != nil {
			dbLog.Error("Failed to scan slack connection row", "error", err, "org_id", conn.OrganisationID)
			return nil, fmt.Errorf("failed to scan slack connection: %w", err)
		}

		if workspaceName.Valid {
			conn.WorkspaceName = workspaceName.String
		}
		if vaultSecretName.Valid {
			conn.VaultSecretName = vaultSecretName.String
		}
		if botUserID.Valid {
			conn.BotUserID = botUserID.String
		}
		if installingUserID.Valid {
			conn.InstallingUserID = &installingUserID.String
		}

		connections = append(connections, conn)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating slack connections: %w", err)
	}

	return connections, nil
}

// DeleteSlackConnection deletes a Slack connection
func (db *DB) DeleteSlackConnection(ctx context.Context, connectionID, organisationID string) error {
	query := `
		DELETE FROM slack_connections
		WHERE id = $1 AND organisation_id = $2
	`

	result, err := db.client.ExecContext(ctx, query, connectionID, organisationID)
	if err != nil {
		dbLog.Error("Failed to delete slack connection", "error", err, "connection_id", connectionID)
		return fmt.Errorf("failed to delete slack connection: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return ErrSlackConnectionNotFound
	}

	return nil
}

// CreateSlackUserLink creates a link between a GNH user and their Slack identity
func (db *DB) CreateSlackUserLink(ctx context.Context, link *SlackUserLink) error {
	query := `
		INSERT INTO slack_user_links (
			id, user_id, slack_connection_id, slack_user_id, dm_notifications, created_at
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id, slack_connection_id)
		DO UPDATE SET
			slack_user_id = EXCLUDED.slack_user_id,
			dm_notifications = EXCLUDED.dm_notifications
		RETURNING id
	`

	err := db.client.QueryRowContext(ctx, query,
		link.ID, link.UserID, link.SlackConnectionID, link.SlackUserID,
		link.DMNotifications, link.CreatedAt,
	).Scan(&link.ID)
	if err != nil {
		dbLog.Error("Failed to create slack user link", "error", err, "user_id", link.UserID, "connection_id", link.SlackConnectionID)
		return fmt.Errorf("failed to create slack user link: %w", err)
	}

	return nil
}

// GetSlackUserLink retrieves a user's Slack link for a specific connection
func (db *DB) GetSlackUserLink(ctx context.Context, userID, connectionID string) (*SlackUserLink, error) {
	link := &SlackUserLink{}

	query := `
		SELECT id, user_id, slack_connection_id, slack_user_id, dm_notifications, created_at
		FROM slack_user_links
		WHERE user_id = $1 AND slack_connection_id = $2
	`

	err := db.client.QueryRowContext(ctx, query, userID, connectionID).Scan(
		&link.ID, &link.UserID, &link.SlackConnectionID, &link.SlackUserID,
		&link.DMNotifications, &link.CreatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrSlackUserLinkNotFound
		}
		dbLog.Error("Failed to get slack user link", "error", err, "user_id", userID, "connection_id", connectionID)
		return nil, fmt.Errorf("failed to get slack user link: %w", err)
	}

	return link, nil
}

// ListSlackUserLinksForConnection lists all user links for a Slack connection
func (db *DB) ListSlackUserLinksForConnection(ctx context.Context, connectionID string) ([]*SlackUserLink, error) {
	query := `
		SELECT id, user_id, slack_connection_id, slack_user_id, dm_notifications, created_at
		FROM slack_user_links
		WHERE slack_connection_id = $1
		ORDER BY created_at DESC
	`

	rows, err := db.client.QueryContext(ctx, query, connectionID)
	if err != nil {
		dbLog.Error("Failed to list slack user links", "error", err, "connection_id", connectionID)
		return nil, fmt.Errorf("failed to list slack user links: %w", err)
	}
	defer rows.Close()

	var links []*SlackUserLink
	for rows.Next() {
		link := &SlackUserLink{}
		err := rows.Scan(
			&link.ID, &link.UserID, &link.SlackConnectionID, &link.SlackUserID,
			&link.DMNotifications, &link.CreatedAt,
		)
		if err != nil {
			dbLog.Error("Failed to scan slack user link row", "error", err)
			return nil, fmt.Errorf("failed to scan slack user link: %w", err)
		}
		links = append(links, link)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating slack user links: %w", err)
	}

	return links, nil
}

// UpdateSlackUserLinkNotifications updates the DM notification preference for a user link
func (db *DB) UpdateSlackUserLinkNotifications(ctx context.Context, userID, connectionID string, dmNotifications bool) error {
	query := `
		UPDATE slack_user_links
		SET dm_notifications = $3
		WHERE user_id = $1 AND slack_connection_id = $2
	`

	result, err := db.client.ExecContext(ctx, query, userID, connectionID, dmNotifications)
	if err != nil {
		dbLog.Error("Failed to update slack user link notifications", "error", err, "user_id", userID, "connection_id", connectionID)
		return fmt.Errorf("failed to update slack user link notifications: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return ErrSlackUserLinkNotFound
	}

	return nil
}

// DeleteSlackUserLink deletes a user's Slack link
func (db *DB) DeleteSlackUserLink(ctx context.Context, userID, connectionID string) error {
	query := `
		DELETE FROM slack_user_links
		WHERE user_id = $1 AND slack_connection_id = $2
	`

	result, err := db.client.ExecContext(ctx, query, userID, connectionID)
	if err != nil {
		dbLog.Error("Failed to delete slack user link", "error", err, "user_id", userID, "connection_id", connectionID)
		return fmt.Errorf("failed to delete slack user link: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return ErrSlackUserLinkNotFound
	}

	return nil
}

// GetSlackConnectionsForOrg returns all Slack connections for an organisation
func (db *DB) GetSlackConnectionsForOrg(ctx context.Context, organisationID string) ([]*SlackConnection, error) {
	return db.ListSlackConnections(ctx, organisationID)
}

// GetEnabledUserLinksForConnection returns user links with DM notifications enabled
func (db *DB) GetEnabledUserLinksForConnection(ctx context.Context, connectionID string) ([]*SlackUserLink, error) {
	query := `
		SELECT id, user_id, slack_connection_id, slack_user_id, dm_notifications, created_at
		FROM slack_user_links
		WHERE slack_connection_id = $1 AND dm_notifications = true
		ORDER BY created_at DESC
	`

	rows, err := db.client.QueryContext(ctx, query, connectionID)
	if err != nil {
		dbLog.Error("Failed to list enabled slack user links", "error", err, "connection_id", connectionID)
		return nil, fmt.Errorf("failed to list enabled slack user links: %w", err)
	}
	defer rows.Close()

	var links []*SlackUserLink
	for rows.Next() {
		link := &SlackUserLink{}
		err := rows.Scan(
			&link.ID, &link.UserID, &link.SlackConnectionID, &link.SlackUserID,
			&link.DMNotifications, &link.CreatedAt,
		)
		if err != nil {
			dbLog.Error("Failed to scan enabled slack user link row", "error", err)
			return nil, fmt.Errorf("failed to scan enabled slack user link: %w", err)
		}
		links = append(links, link)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating enabled slack user links: %w", err)
	}

	return links, nil
}
