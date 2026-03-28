package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Harvey-AU/hover/internal/auth"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/jobs"
	"github.com/Harvey-AU/hover/internal/loops"
	"github.com/rs/zerolog/log"
)

// Version is the current API version (can be set via ldflags at build time)
var Version = "0.4.1"

func buildConfigSnippet() ([]byte, error) {
	appEnv := os.Getenv("APP_ENV")
	authURL := strings.TrimSuffix(os.Getenv("SUPABASE_AUTH_URL"), "/")
	if authURL == "" {
		legacyURL := strings.TrimSuffix(os.Getenv("SUPABASE_URL"), "/")
		if legacyURL != "" {
			log.Warn().Msg("SUPABASE_AUTH_URL missing; using legacy SUPABASE_URL fallback")
			authURL = legacyURL
		}
	}
	if authURL == "" {
		return nil, fmt.Errorf("SUPABASE_AUTH_URL not set")
	}
	parsedURL, err := url.ParseRequestURI(authURL)
	if err != nil {
		return nil, fmt.Errorf("invalid SUPABASE_AUTH_URL: %w", err)
	}
	if appEnv == "production" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("SUPABASE_AUTH_URL must use https in production")
	}
	key := os.Getenv("SUPABASE_PUBLISHABLE_KEY")
	if key == "" {
		legacyKey := os.Getenv("SUPABASE_ANON_KEY")
		if legacyKey != "" {
			log.Warn().Msg("SUPABASE_PUBLISHABLE_KEY missing; using legacy SUPABASE_ANON_KEY fallback")
			key = legacyKey
		}
	}
	if key == "" {
		return nil, fmt.Errorf("SUPABASE_PUBLISHABLE_KEY not set")
	}
	validKey := false
	for _, prefix := range []string{"sb_", "sbp_", "eyJ", "public-"} {
		if strings.HasPrefix(key, prefix) {
			validKey = true
			break
		}
	}
	if !validKey {
		log.Warn().Msg("SUPABASE_PUBLISHABLE_KEY has unexpected format; proceeding anyway")
	}
	config := map[string]any{
		"supabaseUrl":     authURL,
		"supabaseAnonKey": key,
	}
	if appEnv != "" {
		config["environment"] = appEnv
	}
	if raw := os.Getenv("GNH_ENABLE_TURNSTILE"); raw != "" {
		if enabled, err := strconv.ParseBool(raw); err == nil {
			config["enableTurnstile"] = enabled
		} else {
			log.Warn().
				Str("value", raw).
				Msg("invalid GNH_ENABLE_TURNSTILE value; falling back to default")
		}
	} else {
		// Default: enable Turnstile only in production unless explicitly overridden
		config["enableTurnstile"] = appEnv == "production"
	}
	bytes, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}
	return fmt.Appendf(nil, "window.GNH_CONFIG=%s;", bytes), nil
}

// DBClient is an interface for database operations
type DBClient interface {
	GetDB() *sql.DB
	GetOrCreateUser(userID, email string, orgID *string) (*db.User, error)
	GetJobStats(organisationID string, startDate, endDate *time.Time) (*db.JobStats, error)
	GetJobActivity(organisationID string, startDate, endDate *time.Time) ([]db.ActivityPoint, error)
	GetSlowPages(organisationID string, startDate, endDate *time.Time) ([]db.SlowPage, error)
	GetExternalRedirects(organisationID string, startDate, endDate *time.Time) ([]db.ExternalRedirect, error)
	GetUserByWebhookToken(token string) (*db.User, error)
	// Additional methods used by API handlers
	GetUser(userID string) (*db.User, error)
	UpdateUserNames(userID string, firstName, lastName, fullName *string) error
	ResetSchema() error
	ResetDataOnly() error
	CreateUser(userID, email string, firstName, lastName, fullName *string, orgName string) (*db.User, *db.Organisation, error)
	GetOrganisation(organisationID string) (*db.Organisation, error)
	ListJobs(organisationID string, limit, offset int, status, dateRange, timezone string) ([]db.JobWithDomain, int, error)
	ListJobsWithOffset(organisationID string, limit, offset int, status, dateRange string, tzOffsetMinutes int, includeStats bool) ([]db.JobWithDomain, int, error)
	// Scheduler methods
	CreateScheduler(ctx context.Context, scheduler *db.Scheduler) error
	GetScheduler(ctx context.Context, schedulerID string) (*db.Scheduler, error)
	ListSchedulers(ctx context.Context, organisationID string) ([]*db.Scheduler, error)
	UpdateScheduler(ctx context.Context, schedulerID string, updates *db.Scheduler, expectedIsEnabled *bool) error
	DeleteScheduler(ctx context.Context, schedulerID string) error
	GetSchedulersReadyToRun(ctx context.Context, limit int) ([]*db.Scheduler, error)
	UpdateSchedulerNextRun(ctx context.Context, schedulerID string, nextRun time.Time) error
	GetLastJobStartTimeForScheduler(ctx context.Context, schedulerID string) (*time.Time, error)
	GetDomainNameByID(ctx context.Context, domainID int) (string, error)
	GetDomainNames(ctx context.Context, domainIDs []int) (map[int]string, error)
	// Organisation membership methods
	ListUserOrganisations(userID string) ([]db.UserOrganisation, error)
	ValidateOrganisationMembership(userID, organisationID string) (bool, error)
	SetActiveOrganisation(userID, organisationID string) error
	GetEffectiveOrganisationID(user *db.User) string
	GetOrganisationMemberRole(ctx context.Context, userID, organisationID string) (string, error)
	ListOrganisationMembers(ctx context.Context, organisationID string) ([]db.OrganisationMember, error)
	IsOrganisationMemberEmail(ctx context.Context, organisationID, email string) (bool, error)
	RemoveOrganisationMember(ctx context.Context, userID, organisationID string) error
	UpdateOrganisationMemberRole(ctx context.Context, userID, organisationID, role string) error
	CountOrganisationAdmins(ctx context.Context, organisationID string) (int, error)
	// Organisation management methods
	CreateOrganisation(name string) (*db.Organisation, error)
	CreateOrganisationForUser(userID, name string) (*db.Organisation, error)
	AddOrganisationMember(userID, organisationID, role string) error
	CreateOrganisationInvite(ctx context.Context, invite *db.OrganisationInvite) (*db.OrganisationInvite, error)
	ListOrganisationInvites(ctx context.Context, organisationID string) ([]db.OrganisationInvite, error)
	RevokeOrganisationInvite(ctx context.Context, inviteID, organisationID string) error
	GetOrganisationInviteByToken(ctx context.Context, token string) (*db.OrganisationInvite, error)
	AcceptOrganisationInvite(ctx context.Context, token, userID string) (*db.OrganisationInvite, error)
	SetOrganisationPlan(ctx context.Context, organisationID, planID string) error
	GetOrganisationPlanID(ctx context.Context, organisationID string) (string, error)
	ListDailyUsage(ctx context.Context, organisationID string, startDate, endDate time.Time) ([]db.DailyUsageEntry, error)
	// Slack integration methods
	CreateSlackConnection(ctx context.Context, conn *db.SlackConnection) error
	GetSlackConnection(ctx context.Context, connectionID string) (*db.SlackConnection, error)
	ListSlackConnections(ctx context.Context, organisationID string) ([]*db.SlackConnection, error)
	DeleteSlackConnection(ctx context.Context, connectionID, organisationID string) error
	CreateSlackUserLink(ctx context.Context, link *db.SlackUserLink) error
	GetSlackUserLink(ctx context.Context, userID, connectionID string) (*db.SlackUserLink, error)
	UpdateSlackUserLinkNotifications(ctx context.Context, userID, connectionID string, dmNotifications bool) error
	DeleteSlackUserLink(ctx context.Context, userID, connectionID string) error
	StoreSlackToken(ctx context.Context, connectionID, token string) error
	GetSlackToken(ctx context.Context, connectionID string) (string, error)
	// Notification methods
	ListNotifications(ctx context.Context, organisationID string, limit, offset int, unreadOnly bool) ([]*db.Notification, int, error)
	GetUnreadNotificationCount(ctx context.Context, organisationID string) (int, error)
	MarkNotificationRead(ctx context.Context, notificationID, organisationID string) error
	MarkAllNotificationsRead(ctx context.Context, organisationID string) error
	// Webflow integration methods
	CreateWebflowConnection(ctx context.Context, conn *db.WebflowConnection) error
	GetWebflowConnection(ctx context.Context, connectionID string) (*db.WebflowConnection, error)
	ListWebflowConnections(ctx context.Context, organisationID string) ([]*db.WebflowConnection, error)
	DeleteWebflowConnection(ctx context.Context, connectionID, organisationID string) error
	StoreWebflowToken(ctx context.Context, connectionID, token string) error
	GetWebflowToken(ctx context.Context, connectionID string) (string, error)
	// Google Analytics integration methods
	CreateGoogleConnection(ctx context.Context, conn *db.GoogleAnalyticsConnection) error
	GetGoogleConnection(ctx context.Context, connectionID string) (*db.GoogleAnalyticsConnection, error)
	ListGoogleConnections(ctx context.Context, organisationID string) ([]*db.GoogleAnalyticsConnection, error)
	DeleteGoogleConnection(ctx context.Context, connectionID, organisationID string) error
	UpdateGoogleConnectionStatus(ctx context.Context, connectionID, organisationID, status string) error
	StoreGoogleToken(ctx context.Context, connectionID, refreshToken string) error
	GetGoogleToken(ctx context.Context, connectionID string) (string, error)
	GetActiveGAConnectionForOrganisation(ctx context.Context, orgID string) (*db.GoogleAnalyticsConnection, error)
	GetActiveGAConnectionForDomain(ctx context.Context, organisationID string, domainID int) (*db.GoogleAnalyticsConnection, error)
	GetDomainsForOrganisation(ctx context.Context, organisationID string) ([]db.OrganisationDomain, error)
	UpdateConnectionLastSync(ctx context.Context, connectionID string) error
	UpdateConnectionDomains(ctx context.Context, connectionID string, domainIDs []int) error
	MarkConnectionInactive(ctx context.Context, connectionID, reason string) error
	UpsertPageWithAnalytics(ctx context.Context, organisationID string, domainID int, path string, pageViews map[string]int64, connectionID string) (int, error)
	CalculateTrafficScores(ctx context.Context, organisationID string, domainID int) error
	ApplyTrafficScoresToTasks(ctx context.Context, organisationID string, domainID int) error
	GetOrCreateDomainID(ctx context.Context, domain string) (int, error)
	UpsertOrganisationDomain(ctx context.Context, organisationID string, domainID int) error
	// Google Analytics accounts methods (for persistent account storage)
	UpsertGA4Account(ctx context.Context, account *db.GoogleAnalyticsAccount) error
	ListGA4Accounts(ctx context.Context, organisationID string) ([]*db.GoogleAnalyticsAccount, error)
	GetGA4Account(ctx context.Context, accountID string) (*db.GoogleAnalyticsAccount, error)
	GetGA4AccountByGoogleID(ctx context.Context, organisationID, googleAccountID string) (*db.GoogleAnalyticsAccount, error)
	StoreGA4AccountToken(ctx context.Context, accountID, refreshToken string) error
	GetGA4AccountToken(ctx context.Context, accountID string) (string, error)
	GetGA4AccountWithToken(ctx context.Context, organisationID string) (*db.GoogleAnalyticsAccount, error)
	GetGAConnectionWithToken(ctx context.Context, organisationID string) (*db.GoogleAnalyticsConnection, error)
	// Platform integration mappings
	UpsertPlatformOrgMapping(ctx context.Context, mapping *db.PlatformOrgMapping) error
	GetPlatformOrgMapping(ctx context.Context, platform, platformID string) (*db.PlatformOrgMapping, error)
	// Usage and plans methods
	GetOrganisationUsageStats(ctx context.Context, orgID string) (*db.UsageStats, error)
	GetActivePlans(ctx context.Context) ([]db.Plan, error)
	// Webflow site settings methods
	CreateOrUpdateSiteSetting(ctx context.Context, setting *db.WebflowSiteSetting) error
	GetSiteSetting(ctx context.Context, organisationID, webflowSiteID string) (*db.WebflowSiteSetting, error)
	GetSiteSettingByID(ctx context.Context, id string) (*db.WebflowSiteSetting, error)
	ListConfiguredSiteSettings(ctx context.Context, organisationID string) ([]*db.WebflowSiteSetting, error)
	ListAllSiteSettings(ctx context.Context, organisationID string) ([]*db.WebflowSiteSetting, error)
	ListSiteSettingsByConnection(ctx context.Context, connectionID string) ([]*db.WebflowSiteSetting, error)
	UpdateSiteSchedule(ctx context.Context, organisationID, webflowSiteID string, scheduleIntervalHours *int, schedulerID string) error
	UpdateSiteAutoPublish(ctx context.Context, organisationID, webflowSiteID string, enabled bool, webhookID string) error
	DeleteSiteSetting(ctx context.Context, organisationID, webflowSiteID string) error
	DeleteSiteSettingsByConnection(ctx context.Context, connectionID string) error
}

// Handler holds dependencies for API handlers
type Handler struct {
	DB                 DBClient
	JobsManager        jobs.JobManagerInterface
	Loops              *loops.Client
	GoogleClientID     string
	GoogleClientSecret string
}

// NewHandler creates a new API handler with dependencies
func NewHandler(pgDB DBClient, jobsManager jobs.JobManagerInterface, loopsClient *loops.Client, googleClientID, googleClientSecret string) *Handler {
	return &Handler{
		DB:                 pgDB,
		JobsManager:        jobsManager,
		Loops:              loopsClient,
		GoogleClientID:     googleClientID,
		GoogleClientSecret: googleClientSecret,
	}
}

// GetActiveOrganisation validates and returns the active organisation ID for the current user.
// It writes an error response and returns an empty string if authentication fails or the user
// doesn't belong to an organisation.
func (h *Handler) GetActiveOrganisation(w http.ResponseWriter, r *http.Request) string {
	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return ""
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		InternalError(w, r, err)
		return ""
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		BadRequest(w, r, "User must belong to an organisation")
		return ""
	}

	return orgID
}

// GetActiveOrganisationWithUser validates and returns both the user and active organisation ID.
// It writes an error response and returns nil, "", false if authentication fails.
func (h *Handler) GetActiveOrganisationWithUser(w http.ResponseWriter, r *http.Request) (*db.User, string, bool) {
	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return nil, "", false
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		InternalError(w, r, err)
		return nil, "", false
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		BadRequest(w, r, "User must belong to an organisation")
		return nil, "", false
	}

	return user, orgID, true
}

// SetupRoutes configures all API routes with proper middleware
func (h *Handler) SetupRoutes(mux *http.ServeMux) {
	// Health check endpoints (no auth required)
	mux.HandleFunc("/health", h.HealthCheck)
	mux.HandleFunc("/health/db", h.DatabaseHealthCheck)

	// V1 API routes with authentication
	mux.Handle("/v1/jobs", auth.AuthMiddleware(http.HandlerFunc(h.JobsHandler)))
	mux.Handle("/v1/jobs/", auth.AuthMiddleware(http.HandlerFunc(h.JobHandler))) // For /v1/jobs/:id
	mux.Handle("/v1/schedulers", auth.AuthMiddleware(http.HandlerFunc(h.SchedulersHandler)))
	mux.Handle("/v1/schedulers/", auth.AuthMiddleware(http.HandlerFunc(h.SchedulerHandler))) // For /v1/schedulers/:id
	// Shared job routes (public)
	mux.HandleFunc("/v1/shared/jobs/", h.SharedJobHandler)
	mux.HandleFunc("/shared/jobs/", h.ServeSharedJobPage)

	// Dashboard API routes (require auth)
	mux.Handle("/v1/dashboard/stats", auth.AuthMiddleware(http.HandlerFunc(h.DashboardStats)))
	mux.Handle("/v1/dashboard/activity", auth.AuthMiddleware(http.HandlerFunc(h.DashboardActivity)))
	mux.Handle("/v1/dashboard/slow-pages", auth.AuthMiddleware(http.HandlerFunc(h.DashboardSlowPages)))
	mux.Handle("/v1/dashboard/external-redirects", auth.AuthMiddleware(http.HandlerFunc(h.DashboardExternalRedirects)))

	// Metadata routes (require auth)
	mux.Handle("/v1/metadata/metrics", auth.AuthMiddleware(http.HandlerFunc(h.MetadataHandler)))

	// Authentication routes (no auth middleware)
	mux.HandleFunc("/v1/auth/register", h.AuthRegister)
	mux.HandleFunc("/v1/auth/session", h.AuthSession)

	// Profile route (requires auth)
	mux.Handle("/v1/auth/profile", auth.AuthMiddleware(http.HandlerFunc(h.AuthProfile)))

	// Organisation routes (require auth)
	mux.HandleFunc("/v1/organisations/invites/preview", h.OrganisationInvitePreviewHandler)
	mux.Handle("/v1/organisations", auth.AuthMiddleware(http.HandlerFunc(h.OrganisationsHandler)))
	mux.Handle("/v1/organisations/switch", auth.AuthMiddleware(http.HandlerFunc(h.SwitchOrganisationHandler)))
	mux.Handle("/v1/organisations/members", auth.AuthMiddleware(http.HandlerFunc(h.OrganisationMembersHandler)))
	mux.Handle("/v1/organisations/members/", auth.AuthMiddleware(http.HandlerFunc(h.OrganisationMemberHandler)))
	mux.Handle("/v1/organisations/invites/accept", auth.AuthMiddleware(http.HandlerFunc(h.OrganisationInviteAcceptHandler)))
	mux.Handle("/v1/organisations/invites", auth.AuthMiddleware(http.HandlerFunc(h.OrganisationInvitesHandler)))
	mux.Handle("/v1/organisations/invites/", auth.AuthMiddleware(http.HandlerFunc(h.OrganisationInviteHandler)))
	mux.Handle("/v1/organisations/plan", auth.AuthMiddleware(http.HandlerFunc(h.OrganisationPlanHandler)))

	// Domain routes (require auth)
	mux.Handle("/v1/domains", auth.AuthMiddleware(http.HandlerFunc(h.DomainsHandler)))

	// Usage routes (require auth)
	mux.Handle("/v1/usage", auth.AuthMiddleware(http.HandlerFunc(h.UsageHandler)))
	mux.Handle("/v1/usage/history", auth.AuthMiddleware(http.HandlerFunc(h.UsageHistoryHandler)))

	// Plans route (public - for pricing page)
	mux.Handle("/v1/plans", http.HandlerFunc(h.PlansHandler))

	// Webhook endpoints (no auth required)
	mux.HandleFunc("/v1/webhooks/webflow/", h.WebflowWebhook) // Note: trailing slash for path params

	// Slack integration endpoints
	mux.Handle("/v1/integrations/slack", auth.AuthMiddleware(http.HandlerFunc(h.SlackConnectionsHandler)))
	mux.Handle("/v1/integrations/slack/", auth.AuthMiddleware(http.HandlerFunc(h.SlackConnectionHandler)))
	mux.HandleFunc("/v1/integrations/slack/callback", h.SlackOAuthCallback) // No auth - state validation

	// Webflow integration endpoints
	mux.Handle("/v1/integrations/webflow", auth.AuthMiddleware(http.HandlerFunc(h.WebflowConnectionsHandler)))
	mux.HandleFunc("/v1/integrations/webflow/callback", h.HandleWebflowOAuthCallback) // No auth - state validation
	// Webflow site settings endpoints (must be before catch-all)
	mux.Handle("/v1/integrations/webflow/sites/", auth.AuthMiddleware(http.HandlerFunc(h.webflowSitesRouter)))
	mux.Handle("/v1/integrations/webflow/", auth.AuthMiddleware(http.HandlerFunc(h.WebflowConnectionHandler)))

	// Google Analytics integration endpoints
	mux.Handle("/v1/integrations/google", auth.AuthMiddleware(http.HandlerFunc(h.GoogleConnectionsHandler)))
	mux.Handle("/v1/integrations/google/", auth.AuthMiddleware(http.HandlerFunc(h.GoogleConnectionHandler)))
	mux.HandleFunc("/v1/integrations/google/callback", h.HandleGoogleOAuthCallback) // No auth - state validation
	mux.Handle("/v1/integrations/google/save-property", auth.AuthMiddleware(http.HandlerFunc(h.SaveGoogleProperty)))

	// Notification endpoints
	mux.Handle("/v1/notifications", auth.AuthMiddleware(http.HandlerFunc(h.NotificationsHandler)))
	mux.Handle("/v1/notifications/read-all", auth.AuthMiddleware(http.HandlerFunc(h.NotificationsReadAllHandler)))
	mux.Handle("/v1/notifications/", auth.AuthMiddleware(http.HandlerFunc(h.NotificationHandler)))

	// Admin endpoints (require authentication and admin role)
	mux.Handle("/v1/admin/reset-db", auth.AuthMiddleware(http.HandlerFunc(h.AdminResetDatabase)))
	mux.Handle("/v1/admin/reset-data", auth.AuthMiddleware(http.HandlerFunc(h.AdminResetData)))

	// Protected pprof endpoints (system admin + auth required)
	pprofProtected := func(handler http.Handler) http.Handler {
		return auth.AuthMiddleware(requireSystemAdmin(handler))
	}
	mux.Handle("/debug/pprof/", pprofProtected(http.HandlerFunc(pprof.Index)))
	mux.Handle("/debug/pprof/cmdline", pprofProtected(http.HandlerFunc(pprof.Cmdline)))
	mux.Handle("/debug/pprof/profile", pprofProtected(http.HandlerFunc(pprof.Profile)))
	mux.Handle("/debug/pprof/symbol", pprofProtected(http.HandlerFunc(pprof.Symbol)))
	mux.Handle("/debug/pprof/trace", pprofProtected(http.HandlerFunc(pprof.Trace)))
	mux.Handle("/debug/pprof/heap", pprofProtected(pprof.Handler("heap")))
	mux.Handle("/debug/pprof/goroutine", pprofProtected(pprof.Handler("goroutine")))
	mux.Handle("/debug/pprof/threadcreate", pprofProtected(pprof.Handler("threadcreate")))
	mux.Handle("/debug/pprof/block", pprofProtected(pprof.Handler("block")))
	mux.Handle("/debug/pprof/mutex", pprofProtected(pprof.Handler("mutex")))

	// Dev-only: inject Supabase session into browser localStorage.
	// Lets sandboxed preview browsers (e.g. Claude app) bypass the
	// browser→Supabase network call that fails in those environments.
	if os.Getenv("APP_ENV") == "development" {
		mux.HandleFunc("/dev/auto-login", h.DevAutoLogin)
	}

	// Debug endpoints (no auth required)
	mux.HandleFunc("/debug/fgtrace", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open("trace.out")
		if err != nil {
			http.Error(w, "could not open trace file", http.StatusInternalServerError)
			return
		}
		defer f.Close()

		logger := loggerWithRequest(r)

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="trace.out"`)
		if _, err := io.Copy(w, f); err != nil {
			logger.Error().Err(err).Msg("Failed to copy trace file")
		}
	}))

	// Static files
	mux.HandleFunc("/", h.ServeHomepage) // Marketing homepage
	mux.HandleFunc("/config.js", h.ServeConfigJS)
	mux.HandleFunc("/test-login.html", h.ServeTestLogin)
	mux.HandleFunc("/test-components.html", h.ServeTestComponents)
	mux.HandleFunc("/test-data-components.html", h.ServeTestDataComponents)
	mux.HandleFunc("/dashboard", h.ServeDashboard)
	mux.HandleFunc("/dashboard-new", h.ServeNewDashboard)
	mux.HandleFunc("/welcome", h.ServeWelcome)
	mux.HandleFunc("/welcome/", h.ServeWelcome)
	mux.HandleFunc("/settings", h.ServeSettings)
	mux.HandleFunc("/settings/", h.ServeSettings)
	mux.HandleFunc("/welcome/invite", h.ServeInviteWelcome)
	mux.HandleFunc("/welcome/invite/", h.ServeInviteWelcome)
	mux.HandleFunc("/auth-modal.html", h.ServeAuthModal)
	mux.HandleFunc("/auth/callback", h.ServeAuthCallback)
	mux.HandleFunc("/auth/callback/", h.ServeAuthCallback)
	mux.HandleFunc("/extension-auth", h.ServeExtensionAuth)
	mux.HandleFunc("/extension-auth/", h.ServeExtensionAuth)
	mux.HandleFunc("/extension-auth.html", h.ServeExtensionAuth)
	mux.HandleFunc("/cli-login.html", h.ServeCliLogin)
	mux.HandleFunc("/debug-auth.html", h.ServeDebugAuth)
	mux.HandleFunc("/jobs/", h.ServeJobDetails)

	// Favicon — serve the app logo for browser tab icons.
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=604800")
		http.ServeFile(w, r, "./web/static/assets/Good-Native_Hover_App_Logo_Webflow.png")
	})

	// Static assets — served with cache headers to reduce request volume.
	mux.Handle("/js/", withCacheControl(http.StripPrefix("/js/", http.FileServer(http.Dir("./web/static/js/")))))
	mux.Handle("/styles/", withCacheControl(http.StripPrefix("/styles/", http.FileServer(http.Dir("./web/static/styles/")))))
	mux.Handle("/assets/", withCacheControl(http.StripPrefix("/assets/", http.FileServer(http.Dir("./web/static/assets/")))))
	// ES module app — new frontend architecture (Phase 0+)
	mux.Handle("/app/", withCacheControl(http.StripPrefix("/app/", http.FileServer(http.Dir("./web/static/app/")))))
	mux.Handle("/web/", withCacheControl(http.StripPrefix("/web/", h.jsFileServer(http.Dir("./web/")))))
}

// requireSystemAdmin ensures the current request is authenticated and performed by a system administrator.
func requireSystemAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.GetUserFromContext(r.Context())
		if !ok {
			Unauthorised(w, r, "Authentication required")
			return
		}

		if !hasSystemAdminRole(claims) {
			Forbidden(w, r, "System administrator privileges required")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// HealthCheck handles basic health check requests
func (h *Handler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	WriteHealthy(w, r, "hover", Version)
}

// DatabaseHealthCheck handles database health check requests
func (h *Handler) DatabaseHealthCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	// Guard against nil DB to prevent panic
	if h.DB == nil {
		WriteUnhealthy(w, r, "postgresql", fmt.Errorf("database connection not configured"))
		return
	}

	if err := h.DB.GetDB().Ping(); err != nil {
		WriteUnhealthy(w, r, "postgresql", err)
		return
	}

	WriteHealthy(w, r, "postgresql", "")
}

// ServeTestLogin serves the test login page
func (h *Handler) ServeTestLogin(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "test-login.html")
}

// ServeTestComponents serves the Web Components test page
func (h *Handler) ServeTestComponents(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "test-components.html")
}

// ServeTestDataComponents serves the data components test page
func (h *Handler) ServeTestDataComponents(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "test-data-components.html")
}

// ServeDashboard serves the dashboard page
func (h *Handler) ServeDashboard(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "dashboard.html")
}

// ServeSettings serves the settings page
func (h *Handler) ServeSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	http.ServeFile(w, r, "settings.html")
}

// ServeWelcome serves the post-sign-in welcome page.
func (h *Handler) ServeWelcome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}
	if r.URL.Path != "/welcome" && r.URL.Path != "/welcome/" {
		NotFound(w, r, "Page not found")
		return
	}

	http.ServeFile(w, r, "welcome.html")
}

// ServeInviteWelcome serves the invite welcome page.
func (h *Handler) ServeInviteWelcome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}
	if r.URL.Path != "/welcome/invite" && r.URL.Path != "/welcome/invite/" {
		http.NotFound(w, r)
		return
	}

	http.ServeFile(w, r, "invite-welcome.html")
}

// ServeNewDashboard serves the new Web Components dashboard page
func (h *Handler) ServeNewDashboard(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "dashboard.html")
}

// ServeAuthModal serves the shared authentication modal
func (h *Handler) ServeAuthModal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	http.ServeFile(w, r, "auth-modal.html")
}

// ServeAuthCallback serves the OAuth callback bridge page.
func (h *Handler) ServeAuthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	http.ServeFile(w, r, "auth-callback.html")
}

// ServeDebugAuth serves the debug auth test page
func (h *Handler) ServeDebugAuth(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "debug-auth.html")
}

// ServeCliLogin serves the CLI login page for browser-based auth flows
func (h *Handler) ServeCliLogin(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "cli-login.html")
}

// ServeExtensionAuth serves the extension auth popup bridge page.
func (h *Handler) ServeExtensionAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	http.ServeFile(w, r, "web/templates/extension-auth.html")
}

// ServeJobDetails serves the standalone job details page
func (h *Handler) ServeJobDetails(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	http.ServeFile(w, r, "web/templates/job-details.html")
}

// ServeSharedJobPage serves the public shared job view
func (h *Handler) ServeSharedJobPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	http.ServeFile(w, r, "web/templates/job-details.html")
}

// ServeHomepage serves the marketing homepage
func (h *Handler) ServeHomepage(w http.ResponseWriter, r *http.Request) {
	// Only serve homepage for exact root path
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, "homepage.html")
}

// ServeConfigJS exposes Supabase configuration to static pages
func (h *Handler) ServeConfigJS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	snippet, err := buildConfigSnippet()
	if err != nil {
		log.Error().Err(err).Msg("supabase config missing")
		message := "Supabase config unavailable"
		if os.Getenv("APP_ENV") != "production" {
			message = fmt.Sprintf("%s: %v", message, err)
		}
		http.Error(w, message, http.StatusInternalServerError)
		return
	}
	if _, err := w.Write(snippet); err != nil {
		log.Error().Err(err).Msg("failed to write config.js")
	}
}

// DashboardStats handles dashboard statistics requests
func (h *Handler) DashboardStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	// Get active organisation ID (handles auth and validation)
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return // Error already written
	}

	// Get query parameters
	dateRange := r.URL.Query().Get("range")
	if dateRange == "" {
		dateRange = "last7"
	}
	timezone := r.URL.Query().Get("tz")
	if timezone == "" {
		timezone = "UTC"
	}

	// Calculate date range for query
	startDate, endDate := calculateDateRange(dateRange, timezone)

	// Get job statistics
	stats, err := h.DB.GetJobStats(orgID, startDate, endDate)
	if err != nil {
		if HandlePoolSaturation(w, r, err) {
			return
		}
		DatabaseError(w, r, err)
		return
	}

	WriteSuccess(w, r, map[string]any{
		"total_jobs":          stats.TotalJobs,
		"running_jobs":        stats.RunningJobs,
		"completed_jobs":      stats.CompletedJobs,
		"failed_jobs":         stats.FailedJobs,
		"total_tasks":         stats.TotalTasks,
		"avg_completion_time": stats.AvgCompletionTime,
		"date_range":          dateRange,
		"period_start":        startDate,
		"period_end":          endDate,
	}, "Dashboard statistics retrieved successfully")
}

// DashboardActivity handles dashboard activity chart requests
func (h *Handler) DashboardActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	// Get active organisation ID (handles auth and validation)
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return // Error already written
	}

	// Get query parameters
	dateRange := r.URL.Query().Get("range")
	if dateRange == "" {
		dateRange = "last7"
	}
	timezone := r.URL.Query().Get("tz")
	if timezone == "" {
		timezone = "UTC"
	}

	// Calculate date range for query
	startDate, endDate := calculateDateRange(dateRange, timezone)

	// Get activity data
	activity, err := h.DB.GetJobActivity(orgID, startDate, endDate)
	if err != nil {
		DatabaseError(w, r, err)
		return
	}

	WriteSuccess(w, r, map[string]any{
		"activity":     activity,
		"date_range":   dateRange,
		"period_start": startDate,
		"period_end":   endDate,
	}, "Dashboard activity retrieved successfully")
}

// calculateDateRange converts date range string to start and end times
func calculateDateRange(dateRange, timezone string) (*time.Time, *time.Time) {
	// Map common timezone aliases to canonical IANA names
	timezoneAliases := map[string]string{
		"Australia/Melbourne": "Australia/Sydney", // Melbourne uses Sydney timezone (AEST/AEDT)
		"Australia/ACT":       "Australia/Sydney", // ACT uses Sydney timezone
		"Australia/Canberra":  "Australia/Sydney", // Canberra uses Sydney timezone
		"Australia/NSW":       "Australia/Sydney", // NSW uses Sydney timezone
		"Australia/Victoria":  "Australia/Sydney", // Victoria uses Sydney timezone
	}

	// Check if timezone needs aliasing
	if canonical, exists := timezoneAliases[timezone]; exists {
		log.Debug().Str("original", timezone).Str("canonical", canonical).Msg("Mapping timezone alias")
		timezone = canonical
	}

	// Load timezone location, fall back to UTC if invalid
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		log.Warn().Err(err).Str("timezone", timezone).Msg("Invalid timezone in calculateDateRange, falling back to UTC")
		loc = time.UTC
	}

	// Get current time in user's timezone
	now := time.Now().In(loc)
	var startDate, endDate *time.Time

	switch dateRange {
	case "last_hour":
		// Rolling 1 hour window from now
		start := now.Add(-1 * time.Hour)
		startDate = &start
		endDate = &now
	case "today":
		// Calendar day boundaries in user's timezone
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		end := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 999999999, loc)
		startDate = &start
		endDate = &end
	case "last_24_hours", "last24":
		// Rolling 24 hour window from now
		start := now.Add(-24 * time.Hour)
		startDate = &start
		endDate = &now
	case "yesterday":
		// Previous calendar day in user's timezone
		yesterday := now.AddDate(0, 0, -1)
		start := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, loc)
		end := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 23, 59, 59, 999999999, loc)
		startDate = &start
		endDate = &end
	case "7days", "last7":
		// Last 7 days from now
		start := now.AddDate(0, 0, -7)
		startDate = &start
		endDate = &now
	case "30days", "last30":
		// Last 30 days from now
		start := now.AddDate(0, 0, -30)
		startDate = &start
		endDate = &now
	case "last90":
		// Last 90 days from now
		start := now.AddDate(0, 0, -90)
		startDate = &start
		endDate = &now
	case "all":
		// Return nil for both to indicate no date filtering
		return nil, nil
	default:
		// Default to last 7 days
		start := now.AddDate(0, 0, -7)
		startDate = &start
		endDate = &now
	}

	return startDate, endDate
}

// withCacheControl wraps a handler to set Cache-Control on static assets.
// Uses a short max-age so browsers cache modules between navigations but
// still revalidate within a reasonable window on deploy.
func withCacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300, stale-while-revalidate=60")
		next.ServeHTTP(w, r)
	})
}

// jsFileServer creates a file server that sets correct MIME types for JavaScript files
func (h *Handler) jsFileServer(root http.FileSystem) http.Handler {
	fileServer := http.FileServer(root)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set correct MIME type for JavaScript files
		if strings.HasSuffix(r.URL.Path, ".js") {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		}

		fileServer.ServeHTTP(w, r)
	})
}

// DashboardSlowPages handles requests for slow-loading pages analysis
func (h *Handler) DashboardSlowPages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	// Get active organisation (validates auth and membership)
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return // Error already written
	}

	// Get query parameters
	dateRange := r.URL.Query().Get("range")
	if dateRange == "" {
		dateRange = "last7"
	}
	timezone := r.URL.Query().Get("tz")
	if timezone == "" {
		timezone = "UTC"
	}

	// Calculate date range for query
	startDate, endDate := calculateDateRange(dateRange, timezone)

	// Get slow pages data
	slowPages, err := h.DB.GetSlowPages(orgID, startDate, endDate)
	if err != nil {
		DatabaseError(w, r, err)
		return
	}

	WriteSuccess(w, r, map[string]any{
		"slow_pages":   slowPages,
		"date_range":   dateRange,
		"period_start": startDate,
		"period_end":   endDate,
		"count":        len(slowPages),
	}, "Slow pages analysis retrieved successfully")
}

// DashboardExternalRedirects handles requests for external redirect analysis
func (h *Handler) DashboardExternalRedirects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	// Get active organisation (validates auth and membership)
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return // Error already written
	}

	// Get query parameters
	dateRange := r.URL.Query().Get("range")
	if dateRange == "" {
		dateRange = "last7"
	}
	timezone := r.URL.Query().Get("tz")
	if timezone == "" {
		timezone = "UTC"
	}

	// Calculate date range for query
	startDate, endDate := calculateDateRange(dateRange, timezone)

	// Get external redirects data
	redirects, err := h.DB.GetExternalRedirects(orgID, startDate, endDate)
	if err != nil {
		DatabaseError(w, r, err)
		return
	}

	WriteSuccess(w, r, map[string]any{
		"external_redirects": redirects,
		"date_range":         dateRange,
		"period_start":       startDate,
		"period_end":         endDate,
		"count":              len(redirects),
	}, "External redirects analysis retrieved successfully")
}

// WebflowWebhookPayload represents the structure of Webflow's site publish webhook
type WebflowWebhookPayload struct {
	TriggerType string `json:"triggerType"`
	SiteID      string `json:"siteId,omitempty"` // Webflow site ID for per-site settings lookup
	Payload     struct {
		Domains     []string `json:"domains"`
		PublishedBy struct {
			DisplayName string `json:"displayName"`
		} `json:"publishedBy"`
	} `json:"payload"`
}

// WebflowWebhook handles Webflow site publish webhooks
func (h *Handler) WebflowWebhook(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	// Extract identifier from URL path:
	// Legacy: /v1/webhooks/webflow/WEBHOOK_TOKEN
	// Org-scoped: /v1/webhooks/webflow/workspaces/WORKSPACE_ID
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 4 || pathParts[3] == "" {
		logger.Warn().Str("path", r.URL.Path).Msg("Webflow webhook missing identifier in URL")
		BadRequest(w, r, "Webhook identifier required in URL path")
		return
	}

	isWorkspaceWebhook := pathParts[3] == "workspaces"
	webhookToken := ""
	workspaceID := ""
	if isWorkspaceWebhook {
		if len(pathParts) < 5 || pathParts[4] == "" {
			logger.Warn().Str("path", r.URL.Path).Msg("Webflow webhook missing workspace ID in URL")
			BadRequest(w, r, "Webflow workspace ID required in URL path")
			return
		}
		workspaceID = pathParts[4]
	} else {
		webhookToken = pathParts[3]
	}

	// Parse webhook payload
	var payload WebflowWebhookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		logger.Warn().Err(err).Msg("Failed to parse Webflow webhook payload")
		BadRequest(w, r, "Invalid webhook payload")
		return
	}

	var user *db.User
	orgID := ""
	if isWorkspaceWebhook {
		mapping, err := h.DB.GetPlatformOrgMapping(r.Context(), "webflow", workspaceID)
		if err != nil {
			logger.Warn().Err(err).Str("workspace_id", workspaceID).Msg("Failed to resolve Webflow workspace mapping")
			NotFound(w, r, "Invalid Webflow workspace")
			return
		}
		if mapping.CreatedBy == nil || *mapping.CreatedBy == "" {
			logger.Error().Str("workspace_id", workspaceID).Msg("Webflow mapping missing creator user")
			InternalError(w, r, fmt.Errorf("webflow mapping missing creator"))
			return
		}
		user, err = h.DB.GetUser(*mapping.CreatedBy)
		if err != nil {
			logger.Error().Err(err).Str("user_id", *mapping.CreatedBy).Msg("Failed to load Webflow mapping creator")
			InternalError(w, r, fmt.Errorf("failed to load mapping creator: %w", err))
			return
		}
		orgID = mapping.OrganisationID
	} else {
		// Get user from database using webhook token
		var err error
		user, err = h.DB.GetUserByWebhookToken(webhookToken)
		if err != nil {
			logger.Warn().Err(err).Msg("Failed to get user by webhook token")
			// Return 404 to avoid leaking information about valid tokens
			NotFound(w, r, "Invalid webhook token")
			return
		}
		orgID = h.DB.GetEffectiveOrganisationID(user)
	}

	// Log webhook received
	logger.Info().
		Str("user_id", user.ID).
		Str("trigger_type", payload.TriggerType).
		Str("published_by", payload.Payload.PublishedBy.DisplayName).
		Strs("domains", payload.Payload.Domains).
		Msg("Webflow webhook received")

	// Validate it's a site publish event
	if payload.TriggerType != "site_publish" {
		logger.Warn().Str("trigger_type", payload.TriggerType).Msg("Ignoring non-site-publish webhook")
		WriteSuccess(w, r, nil, "Webhook received but ignored (not site_publish)")
		return
	}

	// Check if this site has auto-publish enabled (per-site settings)
	if payload.SiteID != "" && orgID != "" {
		siteSetting, err := h.DB.GetSiteSetting(r.Context(), orgID, payload.SiteID)
		if err != nil {
			if errors.Is(err, db.ErrWebflowSiteSettingNotFound) {
				// Site not configured - expected scenario, ignore webhook
				logger.Warn().
					Str("site_id", payload.SiteID).
					Str("organisation_id", orgID).
					Msg("Site not configured for auto-publish, ignoring webhook")
				WriteSuccess(w, r, nil, "Webhook received but site not configured for auto-publish")
				return
			}
			// Unexpected database error - return 500 so Webflow can retry
			logger.Error().Err(err).Str("site_id", payload.SiteID).Msg("Failed to check site settings")
			InternalError(w, r, err)
			return
		}

		if !siteSetting.AutoPublishEnabled {
			logger.Info().
				Str("site_id", payload.SiteID).
				Str("organisation_id", orgID).
				Msg("Auto-publish disabled for site, ignoring webhook")
			WriteSuccess(w, r, nil, "Webhook received but auto-publish disabled for this site")
			return
		}

		logger.Debug().
			Str("site_id", payload.SiteID).
			Bool("auto_publish_enabled", siteSetting.AutoPublishEnabled).
			Msg("Site auto-publish check passed")
	}

	// Validate domains are provided
	if len(payload.Payload.Domains) == 0 {
		logger.Warn().Msg("Webflow webhook missing domains")
		BadRequest(w, r, "Domains are required")
		return
	}

	// Use the first domain in the list (primary/canonical domain)
	selectedDomain := payload.Payload.Domains[0]

	// Create job using shared logic with webhook defaults
	useSitemap := true
	findLinks := true
	concurrency := 20 // Default concurrency for webhook jobs
	if concurrencyParam := r.URL.Query().Get("concurrency"); concurrencyParam != "" {
		parsed, err := strconv.Atoi(concurrencyParam)
		if err != nil {
			BadRequest(w, r, "concurrency must be a positive integer")
			return
		}
		if parsed <= 0 {
			BadRequest(w, r, "concurrency must be a positive integer")
			return
		}
		concurrency = min(parsed, 100)
	}
	maxPages := 0 // Unlimited pages for webhook-triggered jobs
	sourceType := "webflow_webhook"
	sourceDetail := payload.Payload.PublishedBy.DisplayName

	// Store full webhook payload for debugging
	sourceInfoBytes, _ := json.Marshal(payload)
	sourceInfo := string(sourceInfoBytes)

	req := CreateJobRequest{
		Domain:       selectedDomain,
		UseSitemap:   &useSitemap,
		FindLinks:    &findLinks,
		Concurrency:  &concurrency,
		MaxPages:     &maxPages,
		SourceType:   &sourceType,
		SourceDetail: &sourceDetail,
		SourceInfo:   &sourceInfo,
	}

	// Shallow copy to avoid mutating the original user while injecting org context.
	userForJob := *user
	if orgID != "" {
		userForJob.ActiveOrganisationID = &orgID
		userForJob.OrganisationID = &orgID
	}
	job, err := h.createJobFromRequest(r.Context(), &userForJob, req, logger)
	if err != nil {
		logger.Error().Err(err).
			Str("user_id", user.ID).
			Str("domain", selectedDomain).
			Msg("Failed to create job from webhook")
		InternalError(w, r, err)
		return
	}

	// Job processing starts automatically via worker pool when CreateJob adds it

	logger.Info().
		Str("job_id", job.ID).
		Str("user_id", user.ID).
		Str("org_id", orgID).
		Str("domain", selectedDomain).
		Str("selected_from", strings.Join(payload.Payload.Domains, ", ")).
		Msg("Successfully created and started job from Webflow webhook")

	WriteSuccess(w, r, map[string]any{
		"job_id":  job.ID,
		"user_id": user.ID,
		"org_id":  orgID,
		"domain":  selectedDomain,
		"status":  "created",
	}, "Job created successfully from webhook")
}
