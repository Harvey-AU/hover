package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrWebflowSiteSettingNotFound is returned when a site setting is not found
var ErrWebflowSiteSettingNotFound = errors.New("webflow site setting not found")

// WebflowSiteSetting represents per-site configuration for a Webflow site
type WebflowSiteSetting struct {
	ID                    string
	ConnectionID          string
	OrganisationID        string
	WebflowSiteID         string
	SiteName              string
	PrimaryDomain         string
	ScheduleIntervalHours *int
	AutoPublishEnabled    bool
	WebhookID             string
	WebhookRegisteredAt   *time.Time
	SchedulerID           string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// mapNullFieldsToSetting maps nullable SQL types to WebflowSiteSetting fields
func mapNullFieldsToSetting(setting *WebflowSiteSetting, siteName, primaryDomain, webhookID, schedulerID sql.NullString, webhookRegisteredAt sql.NullTime, scheduleIntervalHours sql.NullInt32) {
	if siteName.Valid {
		setting.SiteName = siteName.String
	}
	if primaryDomain.Valid {
		setting.PrimaryDomain = primaryDomain.String
	}
	if webhookID.Valid {
		setting.WebhookID = webhookID.String
	}
	if schedulerID.Valid {
		setting.SchedulerID = schedulerID.String
	}
	if webhookRegisteredAt.Valid {
		setting.WebhookRegisteredAt = &webhookRegisteredAt.Time
	}
	if scheduleIntervalHours.Valid {
		hours := int(scheduleIntervalHours.Int32)
		setting.ScheduleIntervalHours = &hours
	}
}

// CreateOrUpdateSiteSetting creates or updates a Webflow site setting
func (db *DB) CreateOrUpdateSiteSetting(ctx context.Context, setting *WebflowSiteSetting) error {
	query := `
		INSERT INTO webflow_site_settings (
			connection_id, organisation_id, webflow_site_id, site_name, primary_domain,
			schedule_interval_hours, auto_publish_enabled, webhook_id, webhook_registered_at,
			scheduler_id, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW(), NOW())
		ON CONFLICT (organisation_id, webflow_site_id)
		DO UPDATE SET
			connection_id = EXCLUDED.connection_id,
			site_name = EXCLUDED.site_name,
			primary_domain = EXCLUDED.primary_domain,
			schedule_interval_hours = EXCLUDED.schedule_interval_hours,
			auto_publish_enabled = EXCLUDED.auto_publish_enabled,
			webhook_id = EXCLUDED.webhook_id,
			webhook_registered_at = EXCLUDED.webhook_registered_at,
			scheduler_id = EXCLUDED.scheduler_id,
			updated_at = NOW()
		RETURNING id, created_at, updated_at
	`

	var schedulerID, webhookID sql.NullString
	var webhookRegisteredAt sql.NullTime
	var scheduleIntervalHours sql.NullInt32

	if setting.SchedulerID != "" {
		schedulerID = sql.NullString{String: setting.SchedulerID, Valid: true}
	}
	if setting.WebhookID != "" {
		webhookID = sql.NullString{String: setting.WebhookID, Valid: true}
	}
	if setting.WebhookRegisteredAt != nil {
		webhookRegisteredAt = sql.NullTime{Time: *setting.WebhookRegisteredAt, Valid: true}
	}
	if setting.ScheduleIntervalHours != nil {
		// #nosec G115 -- schedule interval is bounded to valid values (6, 12, 24, 48)
		scheduleIntervalHours = sql.NullInt32{Int32: int32(*setting.ScheduleIntervalHours), Valid: true}
	}

	err := db.client.QueryRowContext(ctx, query,
		setting.ConnectionID, setting.OrganisationID, setting.WebflowSiteID,
		setting.SiteName, setting.PrimaryDomain,
		scheduleIntervalHours, setting.AutoPublishEnabled,
		webhookID, webhookRegisteredAt, schedulerID,
	).Scan(&setting.ID, &setting.CreatedAt, &setting.UpdatedAt)

	if err != nil {
		dbLog.Error("Failed to create/update webflow site setting",
			"error", err,
			"organisation_id", setting.OrganisationID,
			"webflow_site_id", setting.WebflowSiteID)
		return fmt.Errorf("failed to create/update webflow site setting: %w", err)
	}

	return nil
}

// GetSiteSetting retrieves a site setting by organisation and Webflow site ID
func (db *DB) GetSiteSetting(ctx context.Context, organisationID, webflowSiteID string) (*WebflowSiteSetting, error) {
	setting := &WebflowSiteSetting{}
	var siteName, primaryDomain, webhookID, schedulerID sql.NullString
	var webhookRegisteredAt sql.NullTime
	var scheduleIntervalHours sql.NullInt32

	query := `
		SELECT id, connection_id, organisation_id, webflow_site_id, site_name, primary_domain,
		       schedule_interval_hours, auto_publish_enabled, webhook_id, webhook_registered_at,
		       scheduler_id, created_at, updated_at
		FROM webflow_site_settings
		WHERE organisation_id = $1 AND webflow_site_id = $2
	`

	err := db.client.QueryRowContext(ctx, query, organisationID, webflowSiteID).Scan(
		&setting.ID, &setting.ConnectionID, &setting.OrganisationID, &setting.WebflowSiteID,
		&siteName, &primaryDomain, &scheduleIntervalHours, &setting.AutoPublishEnabled,
		&webhookID, &webhookRegisteredAt, &schedulerID, &setting.CreatedAt, &setting.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrWebflowSiteSettingNotFound
		}
		dbLog.Error("Failed to get webflow site setting",
			"error", err,
			"organisation_id", organisationID,
			"webflow_site_id", webflowSiteID)
		return nil, fmt.Errorf("failed to get webflow site setting: %w", err)
	}

	mapNullFieldsToSetting(setting, siteName, primaryDomain, webhookID, schedulerID, webhookRegisteredAt, scheduleIntervalHours)

	return setting, nil
}

// GetSiteSettingByID retrieves a site setting by its ID
func (db *DB) GetSiteSettingByID(ctx context.Context, id string) (*WebflowSiteSetting, error) {
	setting := &WebflowSiteSetting{}
	var siteName, primaryDomain, webhookID, schedulerID sql.NullString
	var webhookRegisteredAt sql.NullTime
	var scheduleIntervalHours sql.NullInt32

	query := `
		SELECT id, connection_id, organisation_id, webflow_site_id, site_name, primary_domain,
		       schedule_interval_hours, auto_publish_enabled, webhook_id, webhook_registered_at,
		       scheduler_id, created_at, updated_at
		FROM webflow_site_settings
		WHERE id = $1
	`

	err := db.client.QueryRowContext(ctx, query, id).Scan(
		&setting.ID, &setting.ConnectionID, &setting.OrganisationID, &setting.WebflowSiteID,
		&siteName, &primaryDomain, &scheduleIntervalHours, &setting.AutoPublishEnabled,
		&webhookID, &webhookRegisteredAt, &schedulerID, &setting.CreatedAt, &setting.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrWebflowSiteSettingNotFound
		}
		dbLog.Error("Failed to get webflow site setting by ID", "error", err, "id", id)
		return nil, fmt.Errorf("failed to get webflow site setting: %w", err)
	}

	mapNullFieldsToSetting(setting, siteName, primaryDomain, webhookID, schedulerID, webhookRegisteredAt, scheduleIntervalHours)

	return setting, nil
}

// ListConfiguredSiteSettings lists all site settings that have configuration (schedule or auto-publish) for an organisation
func (db *DB) ListConfiguredSiteSettings(ctx context.Context, organisationID string) ([]*WebflowSiteSetting, error) {
	query := `
		SELECT id, connection_id, organisation_id, webflow_site_id, site_name, primary_domain,
		       schedule_interval_hours, auto_publish_enabled, webhook_id, webhook_registered_at,
		       scheduler_id, created_at, updated_at
		FROM webflow_site_settings
		WHERE organisation_id = $1
		  AND (schedule_interval_hours IS NOT NULL OR auto_publish_enabled = TRUE)
		ORDER BY updated_at DESC
	`

	return db.querySiteSettings(ctx, query, organisationID)
}

// ListAllSiteSettings lists all site settings for an organisation (including unconfigured)
func (db *DB) ListAllSiteSettings(ctx context.Context, organisationID string) ([]*WebflowSiteSetting, error) {
	query := `
		SELECT id, connection_id, organisation_id, webflow_site_id, site_name, primary_domain,
		       schedule_interval_hours, auto_publish_enabled, webhook_id, webhook_registered_at,
		       scheduler_id, created_at, updated_at
		FROM webflow_site_settings
		WHERE organisation_id = $1
		ORDER BY updated_at DESC
	`

	return db.querySiteSettings(ctx, query, organisationID)
}

// ListSiteSettingsByConnection lists all site settings for a specific connection
func (db *DB) ListSiteSettingsByConnection(ctx context.Context, connectionID string) ([]*WebflowSiteSetting, error) {
	query := `
		SELECT id, connection_id, organisation_id, webflow_site_id, site_name, primary_domain,
		       schedule_interval_hours, auto_publish_enabled, webhook_id, webhook_registered_at,
		       scheduler_id, created_at, updated_at
		FROM webflow_site_settings
		WHERE connection_id = $1
		ORDER BY updated_at DESC
	`

	return db.querySiteSettings(ctx, query, connectionID)
}

// querySiteSettings is a helper function to query and scan site settings
func (db *DB) querySiteSettings(ctx context.Context, query string, arg string) ([]*WebflowSiteSetting, error) {
	rows, err := db.client.QueryContext(ctx, query, arg)
	if err != nil {
		dbLog.Error("Failed to query webflow site settings", "error", err, "arg", arg)
		return nil, fmt.Errorf("failed to query webflow site settings: %w", err)
	}
	defer rows.Close()

	var settings []*WebflowSiteSetting
	for rows.Next() {
		setting := &WebflowSiteSetting{}
		var siteName, primaryDomain, webhookID, schedulerID sql.NullString
		var webhookRegisteredAt sql.NullTime
		var scheduleIntervalHours sql.NullInt32

		err := rows.Scan(
			&setting.ID, &setting.ConnectionID, &setting.OrganisationID, &setting.WebflowSiteID,
			&siteName, &primaryDomain, &scheduleIntervalHours, &setting.AutoPublishEnabled,
			&webhookID, &webhookRegisteredAt, &schedulerID, &setting.CreatedAt, &setting.UpdatedAt,
		)
		if err != nil {
			dbLog.Error("Failed to scan webflow site setting row", "error", err)
			return nil, fmt.Errorf("failed to scan webflow site setting: %w", err)
		}

		mapNullFieldsToSetting(setting, siteName, primaryDomain, webhookID, schedulerID, webhookRegisteredAt, scheduleIntervalHours)

		settings = append(settings, setting)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating webflow site settings: %w", err)
	}

	return settings, nil
}

// UpdateSiteSchedule updates only the schedule-related fields for a site setting
func (db *DB) UpdateSiteSchedule(ctx context.Context, organisationID, webflowSiteID string, scheduleIntervalHours *int, schedulerID string) error {
	var scheduleHours sql.NullInt32
	var schedulerIDNull sql.NullString

	if scheduleIntervalHours != nil {
		// #nosec G115 -- schedule interval is bounded to valid values (6, 12, 24, 48)
		scheduleHours = sql.NullInt32{Int32: int32(*scheduleIntervalHours), Valid: true}
	}
	if schedulerID != "" {
		schedulerIDNull = sql.NullString{String: schedulerID, Valid: true}
	}

	query := `
		UPDATE webflow_site_settings
		SET schedule_interval_hours = $3,
		    scheduler_id = $4,
		    updated_at = NOW()
		WHERE organisation_id = $1 AND webflow_site_id = $2
	`

	result, err := db.client.ExecContext(ctx, query, organisationID, webflowSiteID, scheduleHours, schedulerIDNull)
	if err != nil {
		dbLog.Error("Failed to update site schedule",
			"error", err,
			"organisation_id", organisationID,
			"webflow_site_id", webflowSiteID)
		return fmt.Errorf("failed to update site schedule: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return ErrWebflowSiteSettingNotFound
	}

	return nil
}

// UpdateSiteAutoPublish updates only the auto-publish-related fields for a site setting
func (db *DB) UpdateSiteAutoPublish(ctx context.Context, organisationID, webflowSiteID string, enabled bool, webhookID string) error {
	var webhookIDNull sql.NullString
	var webhookRegisteredAt sql.NullTime

	if webhookID != "" {
		webhookIDNull = sql.NullString{String: webhookID, Valid: true}
		webhookRegisteredAt = sql.NullTime{Time: time.Now(), Valid: true}
	}

	query := `
		UPDATE webflow_site_settings
		SET auto_publish_enabled = $3,
		    webhook_id = $4,
		    webhook_registered_at = $5,
		    updated_at = NOW()
		WHERE organisation_id = $1 AND webflow_site_id = $2
	`

	result, err := db.client.ExecContext(ctx, query, organisationID, webflowSiteID, enabled, webhookIDNull, webhookRegisteredAt)
	if err != nil {
		dbLog.Error("Failed to update site auto-publish", "error", err, "organisation_id", organisationID, "webflow_site_id", webflowSiteID)
		return fmt.Errorf("failed to update site auto-publish: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return ErrWebflowSiteSettingNotFound
	}

	return nil
}

// DeleteSiteSetting deletes a site setting
func (db *DB) DeleteSiteSetting(ctx context.Context, organisationID, webflowSiteID string) error {
	query := `
		DELETE FROM webflow_site_settings
		WHERE organisation_id = $1 AND webflow_site_id = $2
	`

	result, err := db.client.ExecContext(ctx, query, organisationID, webflowSiteID)
	if err != nil {
		dbLog.Error("Failed to delete webflow site setting", "error", err, "organisation_id", organisationID, "webflow_site_id", webflowSiteID)
		return fmt.Errorf("failed to delete webflow site setting: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return ErrWebflowSiteSettingNotFound
	}

	return nil
}

// DeleteSiteSettingsByConnection deletes all site settings for a connection
// This is called when a Webflow connection is disconnected
func (db *DB) DeleteSiteSettingsByConnection(ctx context.Context, connectionID string) error {
	query := `
		DELETE FROM webflow_site_settings
		WHERE connection_id = $1
	`

	_, err := db.client.ExecContext(ctx, query, connectionID)
	if err != nil {
		dbLog.Error("Failed to delete webflow site settings by connection", "error", err, "connection_id", connectionID)
		return fmt.Errorf("failed to delete webflow site settings: %w", err)
	}

	return nil
}
