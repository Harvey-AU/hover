package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// ErrDuplicateOrganisationName is returned when a user already has an organisation
// with the same name (case-insensitive).
var ErrDuplicateOrganisationName = errors.New("an organisation with that name already exists")

// User represents a user in the system
type User struct {
	ID                   string    `json:"id"`
	Email                string    `json:"email"`
	FirstName            *string   `json:"first_name,omitempty"`
	LastName             *string   `json:"last_name,omitempty"`
	FullName             *string   `json:"full_name,omitempty"`
	OrganisationID       *string   `json:"organisation_id,omitempty"`
	ActiveOrganisationID *string   `json:"active_organisation_id,omitempty"`
	SlackUserID          *string   `json:"slack_user_id,omitempty"`
	WebhookToken         *string   `json:"-"` // Excluded from JSON - sensitive credential
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// Organisation represents an organisation in the system
type Organisation struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GetUser retrieves a user by ID
func (db *DB) GetUser(userID string) (*User, error) {
	user := &User{}

	query := `
		SELECT id, email, first_name, last_name, full_name, organisation_id, active_organisation_id, slack_user_id, webhook_token, created_at, updated_at
		FROM users
		WHERE id = $1
	`

	err := db.client.QueryRow(query, userID).Scan(
		&user.ID, &user.Email, &user.FirstName, &user.LastName, &user.FullName, &user.OrganisationID,
		&user.ActiveOrganisationID, &user.SlackUserID, &user.WebhookToken, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	return user, nil
}

// GetUserByWebhookToken retrieves a user by their webhook token
func (db *DB) GetUserByWebhookToken(webhookToken string) (*User, error) {
	user := &User{}

	query := `
		SELECT id, email, first_name, last_name, full_name, organisation_id, active_organisation_id, slack_user_id, webhook_token, created_at, updated_at
		FROM users
		WHERE webhook_token = $1
	`

	err := db.client.QueryRow(query, webhookToken).Scan(
		&user.ID, &user.Email, &user.FirstName, &user.LastName, &user.FullName, &user.OrganisationID,
		&user.ActiveOrganisationID, &user.SlackUserID, &user.WebhookToken, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("user not found with webhook token")
		}
		return nil, fmt.Errorf("failed to get user by webhook token: %w", err)
	}

	return user, nil
}

// GetOrCreateUser retrieves a user by ID, creating them if they don't exist
// This is used for auto-creating users from valid JWT tokens
func (db *DB) GetOrCreateUser(userID, email string, fullName *string) (*User, error) {
	// First try to get the existing user
	user, err := db.GetUser(userID)
	if err == nil {
		// User exists, return them
		return user, nil
	}

	// User doesn't exist, auto-create them with a default organisation
	log.Info().
		Str("user_id", userID).
		Msg("Auto-creating user from JWT token")

	// Determine organisation name based on email domain
	orgName := deriveOrganisationName(email, fullName)

	// Create the user
	newUser, _, err := db.CreateUser(userID, email, nil, nil, fullName, orgName)
	if err != nil {
		return nil, fmt.Errorf("failed to auto-create user: %w", err)
	}

	return newUser, nil
}

// deriveOrganisationName extracts an organisation name from email and fullName
// Business emails (non-common providers) use domain as org name
// Personal emails (gmail, outlook, etc.) use fullName or "Personal Organisation"
func deriveOrganisationName(email string, fullName *string) string {
	// Common personal email providers
	personalProviders := []string{
		"gmail.com", "googlemail.com",
		"outlook.com", "hotmail.com", "live.com",
		"yahoo.com", "ymail.com",
		"icloud.com", "me.com", "mac.com",
		"protonmail.com", "proton.me",
		"aol.com",
		"zoho.com",
		"fastmail.com",
	}

	// Extract domain from email
	atIndex := strings.LastIndex(email, "@")
	if atIndex == -1 {
		// Invalid email format, fall back to personal
		if fullName != nil && *fullName != "" {
			return *fullName
		}
		return "Personal Organisation"
	}

	emailPrefix := email[:atIndex]
	domain := strings.ToLower(email[atIndex+1:])

	// Check for empty domain (e.g., "user@")
	if domain == "" {
		if fullName != nil && *fullName != "" {
			return *fullName
		}
		// Use email prefix + " Organisation"
		return titleCaseEmailPrefix(emailPrefix) + " Organisation"
	}

	// Check if it's a personal email provider
	for _, provider := range personalProviders {
		if domain == provider {
			// Personal email - use fullName or email prefix
			if fullName != nil && *fullName != "" {
				return *fullName
			}
			return titleCaseEmailPrefix(emailPrefix) + " Organisation"
		}
	}

	// Business email - derive organisation name from domain
	// Remove common TLDs and convert to title case
	orgName := domain

	// Remove TLDs (.com, .co.uk, .com.au, etc.)
	suffixes := []string{".com.au", ".co.uk", ".co.nz", ".com", ".co", ".net", ".org", ".io", ".ai", ".dev"}
	for _, suffix := range suffixes {
		if before, ok := strings.CutSuffix(orgName, suffix); ok {
			orgName = before
			break
		}
	}

	// Convert to title case (teamharvey -> Team Harvey)
	// Simple approach: capitalize first letter
	if len(orgName) > 0 {
		orgName = strings.ToUpper(orgName[:1]) + orgName[1:]
	}

	return orgName
}

// isBusinessEmail checks if an email is from a business domain (not a personal email provider)
func isBusinessEmail(email string) bool {
	personalProviders := []string{
		"gmail.com", "googlemail.com",
		"outlook.com", "hotmail.com", "live.com",
		"yahoo.com", "ymail.com",
		"icloud.com", "me.com", "mac.com",
		"protonmail.com", "proton.me",
		"aol.com",
		"zoho.com",
		"fastmail.com",
	}

	atIndex := strings.LastIndex(email, "@")
	if atIndex == -1 {
		return false
	}

	domain := strings.ToLower(email[atIndex+1:])

	return !slices.Contains(personalProviders, domain)
}

// titleCaseEmailPrefix converts email prefix to title case
// Examples: "simon.smallchua" -> "Simon.Smallchua", "user" -> "User"
func titleCaseEmailPrefix(prefix string) string {
	if prefix == "" {
		return ""
	}

	// Split on common separators (., -, _)
	parts := strings.FieldsFunc(prefix, func(r rune) bool {
		return r == '.' || r == '-' || r == '_'
	})

	// Capitalize first letter of each part
	for i, part := range parts {
		if len(part) > 0 {
			parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
		}
	}

	// Rejoin with the original separator (use . for simplicity)
	return strings.Join(parts, ".")
}

// GetOrganisationByName retrieves an organisation by name (case-insensitive)
func (db *DB) GetOrganisationByName(name string) (*Organisation, error) {
	org := &Organisation{}

	query := `
		SELECT id, name, created_at, updated_at
		FROM organisations
		WHERE LOWER(name) = LOWER($1)
		LIMIT 1
	`

	err := db.client.QueryRow(query, name).Scan(
		&org.ID, &org.Name, &org.CreatedAt, &org.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("organisation not found")
		}
		return nil, fmt.Errorf("failed to get organisation by name: %w", err)
	}

	return org, nil
}

// CreateOrganisation creates a new organisation
func (db *DB) CreateOrganisation(name string) (*Organisation, error) {
	org := &Organisation{
		ID:   uuid.New().String(),
		Name: name,
	}

	query := `
		INSERT INTO organisations (id, name, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		RETURNING created_at, updated_at
	`

	err := db.client.QueryRow(query, org.ID, org.Name).Scan(
		&org.CreatedAt, &org.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create organisation: %w", err)
	}

	log.Info().
		Str("organisation_id", org.ID).
		Str("name", org.Name).
		Msg("Created new organisation")

	return org, nil
}

// CreateOrganisationForUser atomically checks for a duplicate name, creates the
// organisation, adds the user as admin, and sets it as their active organisation.
// Returns ErrDuplicateOrganisationName if the user already owns an organisation
// with the same name (case-insensitive).
func (db *DB) CreateOrganisationForUser(userID, name string) (*Organisation, error) {
	tx, err := db.client.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to start transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Lock the user row to serialise concurrent create requests for the same user.
	lockQuery := `SELECT id FROM users WHERE id = $1 FOR UPDATE`
	var lockedID string
	if err := tx.QueryRow(lockQuery, userID).Scan(&lockedID); err != nil {
		return nil, fmt.Errorf("failed to lock user row: %w", err)
	}

	// Check for duplicate name within this user's existing organisations.
	dupQuery := `
		SELECT EXISTS (
			SELECT 1
			FROM organisations o
			JOIN organisation_members om ON om.organisation_id = o.id
			WHERE om.user_id = $1
			  AND lower(o.name) = lower($2)
		)
	`
	var exists bool
	if err := tx.QueryRow(dupQuery, userID, name).Scan(&exists); err != nil {
		return nil, fmt.Errorf("failed to check duplicate organisation name: %w", err)
	}
	if exists {
		return nil, ErrDuplicateOrganisationName
	}

	// Create the organisation.
	org := &Organisation{
		ID:   uuid.New().String(),
		Name: name,
	}
	insertOrg := `
		INSERT INTO organisations (id, name, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		RETURNING created_at, updated_at
	`
	if err := tx.QueryRow(insertOrg, org.ID, org.Name).Scan(&org.CreatedAt, &org.UpdatedAt); err != nil {
		return nil, fmt.Errorf("failed to create organisation: %w", err)
	}

	// Add user as admin member.
	insertMember := `
		INSERT INTO organisation_members (user_id, organisation_id, role, created_at)
		VALUES ($1, $2, 'admin', NOW())
		ON CONFLICT (user_id, organisation_id) DO UPDATE SET role = EXCLUDED.role
	`
	if _, err := tx.Exec(insertMember, userID, org.ID); err != nil {
		return nil, fmt.Errorf("failed to add organisation member: %w", err)
	}

	// Set as active organisation.
	updateActive := `UPDATE users SET active_organisation_id = $2, updated_at = NOW() WHERE id = $1`
	if _, err := tx.Exec(updateActive, userID, org.ID); err != nil {
		return nil, fmt.Errorf("failed to set active organisation: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit organisation creation: %w", err)
	}

	log.Info().
		Str("organisation_id", org.ID).
		Str("user_id", userID).
		Str("name", org.Name).
		Msg("Created new organisation for user")

	return org, nil
}

// GetOrganisation retrieves an organisation by ID
func (db *DB) GetOrganisation(organisationID string) (*Organisation, error) {
	org := &Organisation{}

	query := `
		SELECT id, name, created_at, updated_at
		FROM organisations
		WHERE id = $1
	`

	err := db.client.QueryRow(query, organisationID).Scan(
		&org.ID, &org.Name, &org.CreatedAt, &org.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("organisation not found")
		}
		return nil, fmt.Errorf("failed to get organisation: %w", err)
	}

	return org, nil
}

func (db *DB) GetOrganisationMembers(organisationID string) ([]*User, error) {
	query := `
		SELECT u.id, u.email, u.first_name, u.last_name, u.full_name, u.organisation_id, u.active_organisation_id, u.slack_user_id, u.created_at, u.updated_at
		FROM organisation_members om
		JOIN users u ON u.id = om.user_id
		WHERE om.organisation_id = $1
		ORDER BY om.created_at ASC
	`

	rows, err := db.client.Query(query, organisationID)
	if err != nil {
		return nil, fmt.Errorf("failed to get organisation members: %w", err)
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		user := &User{}
		err := rows.Scan(
			&user.ID, &user.Email, &user.FirstName, &user.LastName, &user.FullName, &user.OrganisationID,
			&user.ActiveOrganisationID, &user.SlackUserID, &user.CreatedAt, &user.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan user: %w", err)
		}
		users = append(users, user)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate user rows: %w", err)
	}

	return users, nil
}

// If user already exists, returns the existing user and their organisation
func (db *DB) CreateUser(userID, email string, firstName, lastName, fullName *string, orgName string) (*User, *Organisation, error) {
	// First check if user already exists
	existingUser, err := db.GetUser(userID)
	if err == nil {
		// User exists, get their organisation
		if existingUser.OrganisationID != nil {
			org, err := db.GetOrganisation(*existingUser.OrganisationID)
			if err != nil {
				log.Warn().Err(err).Str("organisation_id", *existingUser.OrganisationID).Msg("Failed to get existing user's organisation")
				// Return user without organisation rather than failing
				return existingUser, nil, nil
			}
			log.Info().
				Str("user_id", userID).
				Msg("User already exists, returning existing user and organisation")
			return existingUser, org, nil
		}
		// User exists but has no organisation - this shouldn't happen but handle gracefully
		log.Info().
			Str("user_id", userID).
			Msg("User already exists but has no organisation")
		return existingUser, nil, nil
	}

	// User doesn't exist, create new user and organisation
	// Start a transaction for automatic operation
	tx, err := db.client.Begin()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback() // Rollback is safe to call even after commit
	}()

	// For business emails, check if organisation already exists
	var org *Organisation
	createdNewOrg := false
	if isBusinessEmail(email) {
		existingOrg, err := db.GetOrganisationByName(orgName)
		if err == nil {
			// Organisation exists, use it
			org = existingOrg
			log.Info().
				Str("user_id", userID).
				Str("organisation_id", org.ID).
				Str("organisation_name", org.Name).
				Msg("Joining existing organisation (business email)")
		}
	}

	// If no existing organisation found (or personal email), create new one
	if org == nil {
		createdNewOrg = true
		org = &Organisation{
			ID:   uuid.New().String(),
			Name: orgName,
		}

		orgQuery := `
			INSERT INTO organisations (id, name, created_at, updated_at)
			VALUES ($1, $2, NOW(), NOW())
			RETURNING created_at, updated_at
		`

		err = tx.QueryRow(orgQuery, org.ID, org.Name).Scan(
			&org.CreatedAt, &org.UpdatedAt,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create organisation: %w", err)
		}
	}

	// Create user with organisation reference and active organisation
	user := &User{
		ID:                   userID,
		Email:                email,
		FirstName:            firstName,
		LastName:             lastName,
		FullName:             fullName,
		OrganisationID:       &org.ID,
		ActiveOrganisationID: &org.ID,
	}

	userQuery := `
		INSERT INTO users (id, email, first_name, last_name, full_name, organisation_id, active_organisation_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6, NOW(), NOW())
		RETURNING created_at, updated_at
	`

	err = tx.QueryRow(userQuery, user.ID, user.Email, user.FirstName, user.LastName, user.FullName, user.OrganisationID).Scan(
		&user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create user: %w", err)
	}

	memberRole := "member"
	if createdNewOrg {
		memberRole = "admin"
	}

	// Add user to organisation_members table
	memberQuery := `
		INSERT INTO organisation_members (user_id, organisation_id, role, created_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (user_id, organisation_id) DO UPDATE
		SET role = EXCLUDED.role
	`

	_, err = tx.Exec(memberQuery, user.ID, org.ID, memberRole)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to add organisation membership: %w", err)
	}

	// Commit transaction
	if err = tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	log.Info().
		Str("user_id", user.ID).
		Str("organisation_id", org.ID).
		Str("organisation_name", org.Name).
		Msg("Created new user with organisation")

	return user, org, nil
}

// UserOrganisation represents an organisation a user belongs to
type UserOrganisation struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// ListUserOrganisations returns all organisations a user is a member of
func (db *DB) ListUserOrganisations(userID string) ([]UserOrganisation, error) {
	query := `
		SELECT o.id, o.name, o.created_at
		FROM organisations o
		INNER JOIN organisation_members om ON o.id = om.organisation_id
		WHERE om.user_id = $1
		ORDER BY o.name
	`

	rows, err := db.client.Query(query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list user organisations: %w", err)
	}
	defer rows.Close()

	var orgs []UserOrganisation
	for rows.Next() {
		var org UserOrganisation
		if err := rows.Scan(&org.ID, &org.Name, &org.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan organisation: %w", err)
		}
		orgs = append(orgs, org)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate organisation rows: %w", err)
	}

	return orgs, nil
}

// ValidateOrganisationMembership checks if a user is a member of an organisation
func (db *DB) ValidateOrganisationMembership(userID, organisationID string) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1 FROM organisation_members
			WHERE user_id = $1 AND organisation_id = $2
		)
	`

	var exists bool
	err := db.client.QueryRow(query, userID, organisationID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to validate organisation membership: %w", err)
	}

	return exists, nil
}

// SetActiveOrganisation sets the user's active organisation
func (db *DB) SetActiveOrganisation(userID, organisationID string) error {
	// First validate membership
	isMember, err := db.ValidateOrganisationMembership(userID, organisationID)
	if err != nil {
		return err
	}
	if !isMember {
		return fmt.Errorf("user is not a member of organisation")
	}

	query := `
		UPDATE users
		SET active_organisation_id = $2, updated_at = NOW()
		WHERE id = $1
	`

	result, err := db.client.Exec(query, userID, organisationID)
	if err != nil {
		return fmt.Errorf("failed to set active organisation: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("user not found")
	}

	log.Info().
		Str("user_id", userID).
		Str("organisation_id", organisationID).
		Msg("Set active organisation")

	return nil
}

// UpdateUserNames updates the user's name fields.
func (db *DB) UpdateUserNames(userID string, firstName, lastName, fullName *string) error {
	query := `
		UPDATE users
		SET first_name = $2,
		    last_name = $3,
		    full_name = $4,
		    updated_at = NOW()
		WHERE id = $1
	`

	result, err := db.client.Exec(query, userID, firstName, lastName, fullName)
	if err != nil {
		return fmt.Errorf("failed to update user names: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("user not found")
	}

	return nil
}

// AddOrganisationMember adds a user as a member of an organisation
func (db *DB) AddOrganisationMember(userID, organisationID, role string) error {
	role = strings.TrimSpace(strings.ToLower(role))
	if role == "" {
		role = "member"
	}
	if role != "admin" && role != "member" {
		return fmt.Errorf("invalid role: %s", role)
	}

	query := `
		INSERT INTO organisation_members (user_id, organisation_id, role, created_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (user_id, organisation_id) DO UPDATE
		SET role = EXCLUDED.role
	`

	_, err := db.client.Exec(query, userID, organisationID, role)
	if err != nil {
		return fmt.Errorf("failed to add organisation member: %w", err)
	}

	log.Info().
		Str("user_id", userID).
		Str("organisation_id", organisationID).
		Msg("Added organisation member")

	return nil
}

// GetEffectiveOrganisationID returns the user's effective organisation ID
// (active_organisation_id if set, otherwise organisation_id for backward compatibility)
func (db *DB) GetEffectiveOrganisationID(user *User) string {
	if user.ActiveOrganisationID != nil && *user.ActiveOrganisationID != "" {
		return *user.ActiveOrganisationID
	}
	if user.OrganisationID != nil {
		return *user.OrganisationID
	}
	return ""
}

// GetOrganisationUsageStats returns current usage statistics for an organisation
func (db *DB) GetOrganisationUsageStats(ctx context.Context, orgID string) (*UsageStats, error) {
	query := `SELECT daily_limit, daily_used, daily_remaining, plan_id, plan_name, plan_display_name, reset_time
	          FROM get_organisation_usage_stats($1)`

	var stats UsageStats
	err := db.client.QueryRowContext(ctx, query, orgID).Scan(
		&stats.DailyLimit,
		&stats.DailyUsed,
		&stats.DailyRemaining,
		&stats.PlanID,
		&stats.PlanName,
		&stats.PlanDisplayName,
		&stats.ResetsAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("organisation not found or has no plan: %s", orgID)
		}
		return nil, fmt.Errorf("failed to get organisation usage stats: %w", err)
	}

	// Calculate percentage
	if stats.DailyLimit > 0 {
		stats.UsagePercentage = float64(stats.DailyUsed) / float64(stats.DailyLimit) * 100
	}

	return &stats, nil
}

// UsageStats represents current usage statistics for an organisation
type UsageStats struct {
	DailyLimit      int       `json:"daily_limit"`
	DailyUsed       int       `json:"daily_used"`
	DailyRemaining  int       `json:"daily_remaining"`
	UsagePercentage float64   `json:"usage_percentage"`
	PlanID          string    `json:"plan_id"`
	PlanName        string    `json:"plan_name"`
	PlanDisplayName string    `json:"plan_display_name"`
	ResetsAt        time.Time `json:"resets_at"`
}

// Plan represents a subscription tier
type Plan struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	DisplayName       string    `json:"display_name"`
	DailyPageLimit    int       `json:"daily_page_limit"`
	MonthlyPriceCents int       `json:"monthly_price_cents"`
	IsActive          bool      `json:"is_active"`
	SortOrder         int       `json:"sort_order"`
	CreatedAt         time.Time `json:"created_at"`
}

// GetActivePlans returns all active subscription plans
func (db *DB) GetActivePlans(ctx context.Context) ([]Plan, error) {
	query := `
		SELECT id, name, display_name, daily_page_limit, monthly_price_cents,
		       is_active, sort_order, created_at
		FROM plans
		WHERE is_active = true
		ORDER BY sort_order
	`

	rows, err := db.client.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get active plans: %w", err)
	}
	defer rows.Close()

	var plans []Plan
	for rows.Next() {
		var p Plan
		if err := rows.Scan(
			&p.ID, &p.Name, &p.DisplayName, &p.DailyPageLimit, &p.MonthlyPriceCents,
			&p.IsActive, &p.SortOrder, &p.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan plan: %w", err)
		}
		plans = append(plans, p)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate plan rows: %w", err)
	}

	return plans, nil
}
