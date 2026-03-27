### ✅ Stage 0: Project Setup & Infrastructure

#### ✅ Development Environment Setup

- [x] Initialise GitHub repository
- [x] Set up branch protection
- [x] Resolve naming issues and override branch protection for admins
- [x] Create dev/prod branches
- [x] Set up local development environment
- [x] Add initial documentation

#### ✅ Go Project Structure

- [x] Initialise Go project
- [x] Set up dependency management
- [x] Create project structure
- [x] Add basic configs
- [x] Set up testing framework

#### ✅ Production Infrastructure Setup

- [x] Set up dev/prod environments
- [x] Configure environment variables
- [x] Set up secrets management
- [x] Create Dockerfile and container setup
- [x] Configure Fly.io
  - [x] Set up Fly.io account and project
  - [x] Configure deployment settings
  - [x] Set up environment variables in Fly.io
  - [x] Create deployment workflow
  - [x] Add health check endpoint monitoring
- [x] Test production deployment
- [x] Initial Sentry.io connection

## ✅ Stage 1: Core Setup & Basic Crawling

### ✅ Core API Implementation

- [x] Initialise Go project structure and dependencies
- [x] Set up basic API endpoints
- [x] Set up environment variables and configs
- [x] Implement basic health checks and monitoring
- [x] Add basic error monitoring with Sentry
- [x] Set up endpoint performance tracking
- [x] Add graceful shutdown handling
- [x] Implement configuration validation

### ✅ Enhance Crawler Results

- [x] Set up Colly crawler configuration
- [x] Implement concurrent crawling logic
- [x] Add basic error handling
- [x] Add rate limiting (fixed client IP detection)
- [x] Add retry logic
- [x] Handle different response types/errors
- [x] Implement cache validation checks
- [x] Add crawler-specific error tracking
- [x] Set up crawler performance monitoring

### ✅ Set up Turso for storing results

- [x] Design database schema
- [x] Set up Turso connection and config
- [x] Implement data models and queries
- [x] Add basic error handling
- [x] Add retry logic
- [x] Add database performance monitoring
- [x] Set up query error tracking

## ✅ Stage 2: Multi-domain Support & Job Queue Architecture

### ✅ Job Queue Architecture

- [x] Design job and task data structures
- [x] Implement persistent job storage in database
- [x] Create worker pool for concurrent URL processing
- [x] Add job management API (create, start, cancel, status)
- [x] Implement database retry logic for job operations to handle transient
      errors
- [x] Enhance error reporting and monitoring

### ✅ Sitemap Integration

- [x] Implement sitemap.xml parser
- [x] Add URL filtering based on path patterns
- [x] Handle sitemap index files
- [x] Process multiple sitemaps
- [x] Implement robust URL normalisation in sitemap processing
- [x] Add improved error handling for malformed URLs

### ✅ Link Discovery & Crawling

- [x] Extract links from crawled pages
- [x] Filter links to stay within target domain
- [x] Basic link discovery logic
- [x] Queue discovered links for processing

### ✅ Job Management API

- [x] Create job endpoints (create/list/get/cancel)
- [x] Add progress calculation and reporting
- [x] Store recent crawled pages in job history
- [x] Implement multi-domain support

## ✅ Stage 3: PostgreSQL Migration & Performance Optimisation

### ✅ Fly.io Production Setup

- [x] Set up production environment on Fly.io
- [x] Deploy and test rate limiting in production
- [x] Configure auto-scaling rules
- [x] Set up production logging
- [x] Implement monitoring alerts
- [x] Configure backup strategies (Supabase handles automatically)

### ✅ Performance Optimisation

- [x] Implement caching layer
- [x] Optimise database queries
- [x] Configure rate limiting with proper client IP detection
- [x] Add performance monitoring
- [x] Made decision to switch to postgres at this point

### ✅ PostgreSQL Migration

#### ✅ PostgreSQL Setup and Infrastructure

- [x] Set up PostgreSQL on Fly.io
  - [x] Create database instance
  - [x] Configure connection settings
  - [x] Configure security settings

#### ✅ Database Layer Replacement

- [x] Implement PostgreSQL schema
  - [x] Convert SQLite schema to PostgreSQL syntax
  - [x] Add proper indexes
  - [x] Implement connection pooling
- [x] Replace database access layer
  - [x] Update db package to use PostgreSQL
  - [x] Add health checks and monitoring
  - [x] Implement efficient error handling

#### ✅ Task Queue and Worker Redesign

- [x] Implement PostgreSQL-based task queue
  - [x] Use row-level locking with SELECT FOR UPDATE SKIP LOCKED
  - [x] Optimise for concurrent access
  - [x] Plan task prioritisation implementation (docs created)
- [x] Redesign worker pool
  - [x] Create single global worker pool
  - [x] Implement optimised task acquisition

#### ✅ URL Processing Improvements

- [x] Enhanced sitemap processing
  - [x] Implement robust URL normalisation
  - [x] Add support for relative URLs in sitemaps
  - [x] Improve error handling for malformed URLs
- [x] Improve URL validation
  - [x] Better handling of URL variations
  - [x] Consistent URL formatting throughout the codebase

#### ✅ Code Refactoring

- [x] Eliminate duplicate code
  - [x] Move database operations to a unified interface
  - [x] Consolidate similar functions into single implementations
  - [x] Move functions to appropriate packages
- [x] Remove global state
  - [x] Implement proper dependency injection
  - [x] Replace global DB instance with passed parameters
  - [x] Improve transaction management with DbQueue
- [x] Standardise naming conventions
  - [x] Use consistent function names across packages
  - [x] Clarify responsibilities between packages

#### ✅ Code Cleanup

- [x] Remove redundant worker pool creation
  - [x] Eliminate duplicate worker pools in API handlers
  - [x] Ensure single global worker pool is used consistently
- [x] Simplify middleware stack
  - [x] Reduce excessive transaction monitoring
  - [x] Optimise Sentry integrations
  - [x] Remove unnecessary wrapping functions
- [x] Clean up API endpoints
  - [x] Document endpoints to consolidate or remove
  - [x] Plan endpoint implementation simplification
  - [x] Standardise error handling approach
  - [x] Implementation plan completed in
        [docs/plans/api-cleanup.md](docs/plans/api-cleanup.md)
- [x] Fix metrics collection (plan created)
  - [x] Document metrics to expose
  - [x] Plan for unused metrics tracking removal
  - [x] Identify relevant PostgreSQL metrics to add
- [x] Remove depth functionality
  - [x] Remove `depth` column from `tasks` table
  - [x] Remove `max_depth` column from `jobs` table
  - [x] Update `EnqueueURLs` function to remove depth parameter
  - [x] Update type definitions to remove depth fields
  - [x] Remove depth-related logic from link discovery process
  - [x] Update documentation to remove depth references

#### ✅ Final Transition

- [x] Update core endpoints to use new implementation
- [x] Remove SQLite-specific code
- [x] Clean up dependencies and imports
- [x] Update configuration and documentation

## 🟡 Stage 4: Core Authentication & MVP Interface

### ✅ Implement Supabase Authentication

- [x] Configure Supabase Auth settings
- [x] Implement JWT validation middleware in Go
- [x] Add social login providers configuration (Google, Facebook, Slack, GitHub,
      Microsoft, Figma, LinkedIn + Email)
- [x] Set up user session handling and token validation
- [x] Implement comprehensive auth error handling
- [x] Create user registration with auto-organisation creation
- [x] Configure custom domain authentication (hover.auth.goodnative.co)
- [x] Implement account linking for multiple auth providers per user (handled by
      Supabase Auth via auth.identities table)

### ✅ Connect user data to PostgreSQL

- [x] Design user data schema with Row Level Security
- [x] Implement user profile storage
- [x] Add user preferences handling
- [x] Configure PostgreSQL policies for data access
- [x] Create database operations for users and organisations

### ✅ Simple Organisation Sharing

Organisation model implemented:

- [x] Auto-create organisation when user signs up
- [x] Create shared access to all jobs/tasks/reports within organisation

### ✅ API-First Architecture Development (Completed v0.4.2)

- [x] **Comprehensive RESTful API Infrastructure**
  - [x] Standardised response format with request IDs and consistent error
        handling
  - [x] Interface-agnostic RESTful endpoints (`/v1/*` structure)
  - [x] Comprehensive middleware stack (CORS, logging, rate limiting)
  - [x] Proper HTTP status codes and structured error responses
- [x] **Multi-Interface Authentication Foundations**
  - [x] JWT-based authentication with Supabase integration
  - [x] Authentication middleware for protected endpoints

### ✅ MVP Interface Development (Completed v0.5.3)

- [x] **Dashboard Demonstration Infrastructure**
  - [x] Working vanilla JavaScript dashboard with modern UI design
  - [x] API integration for job statistics and progress tracking
        (`/v1/dashboard/stats`, `/v1/jobs`)
  - [x] Stable production deployment without Web Components dependencies
  - [x] Responsive design with professional styling and user experience
- [x] **Template + Data Binding Foundation**
  - [x] Architecture documentation for template-based integration approach
  - [x] Attribute-based event handling system (`gnh-action`, `gnh-data-*`)
  - [x] Event delegation framework for extensible functionality
  - [x] Demonstration of template approach in production dashboard

### 🟡 Template + Data Binding Implementation (Completed v0.5.5)

- [x] **Core Data Binding Library**
  - [x] Basic attribute-based event handling (`gnh-action="refresh-dashboard"`)
  - [x] JavaScript library for `data-gnh-bind` attribute processing
  - [x] Template engine for `data-gnh-template` repeated content
  - [x] Authentication integration with conditional element display
        (`data-gnh-auth`)
  - [x] Form handling with `data-gnh-form` and validation (`data-gnh-validate`)
  - [x] Style and attribute binding (`data-gnh-bind-style`, `data-gnh-bind-attr`)
- [x] **Enhanced Job Management**
  - [x] Real-time job progress updates via data binding
  - [x] Job creation forms with template-based validation
  - [x] Error handling and user feedback systems
  - [x] Advanced filtering and search capabilities
- [x] **User Experience Features**
  - [x] Account settings and profile management templates
  - [x] Notification system integration

### ✅ Task prioritisation & URL processing

- [x] **Stop duplicate domain crawls oncurrently, close old job**
  - [x] When creating a job, check if there's an active job for this user
  - [x] If so, cancel the old job

- [x] **Task Prioritisation**
  - [x] Prioritisation by page hierarchy and importance
  - [x] Implement link priority ordering for header links (1st: 1.000, 2nd:
        0.990, etc.)
  - [x] Apply priority ordering logic to all discovered page links

- [x] **Robots.txt Compliance**
  - [x] Parse and honour robots.txt crawl-delay directives
  - [x] Filter URLs against Disallow/Allow patterns before enqueueing
  - [x] Cache robots.txt rules at job level to prevent repeated fetches
  - [x] Fail manual URL creation if robots.txt cannot be checked
  - [x] Filter dynamically discovered links against robots rules

- [x] **URL Processing Enhancements**
  - [x] Filter out links that are hidden via inline `style` attributes.
  - [x] Remove anchor links from link discovery
  - [x] Support compressed sitemaps (.xml.gz and other formats)
  - [x] If sitemap can't be found, setup job with / page and start as normal
        finding links through pages
  - [x] Only store source_url if page was found ON a page and redirect_url if
        it's a redirect AND it doesn't match the domain/path of the task

- [x] Considering impact of and plan updates
      [Go v1.25 release](/docs/plans/Go-1.25.md)

- [x] **Blocking Avoidance**
  - [x] Series of tweaks to reduce blocking

### ✅ Recurring Job Scheduling (Completed v0.18.0)

- [x] **Scheduler System Implementation**
  - [x] Database schema with schedulers table and scheduler_id foreign key
  - [x] Support for 6, 12, 24, and 48-hour intervals
  - [x] Background service polls for ready schedules every 30 seconds
  - [x] Jobs created from schedulers marked with source_type='scheduler'
  - [x] Scheduler management API endpoints (create, update, delete, list)
  - [x] Dashboard UI for managing schedules (enable/disable, view jobs, delete)
  - [x] Schedule dropdown in job creation modal for optional recurring schedules
  - [x] Comprehensive error handling with structured logging
  - [x] Input validation and rollback logic for failed operations

### 🟡 Webflow App Integration (Completed v0.23.0)

- [x] **Webflow OAuth Connection**
  - [x] Register as Webflow developer and create App
  - [x] OAuth flow with HMAC-signed state for CSRF protection
  - [x] Token storage in Supabase Vault with automatic cleanup
  - [x] User identity display via `authorized_user:read` scope
  - [x] Dashboard UI showing connection status and username
  - [x] Shared OAuth utilities extracted from Slack integration
- [x] **Webflow Site Selection**
  - [x] List user's accessible Webflow sites via `/v2/sites` endpoint
  - [x] Site picker UI in dashboard connections panel with search/pagination
  - [x] Per-site settings stored in `webflow_site_settings` table
  - [x] Connection management endpoints (list/get/delete)
- [x] **Manual Job Triggering** (Completed v0.24.0)
  - [x] Jobs automatically triggered when schedule or auto-publish enabled
  - [x] Jobs can be triggered via scheduler or webhooks
  - [x] Show last crawl status (via general job list)
- [x] **Scheduling Configuration**
  - [x] Connect Webflow sites to existing scheduler system
  - [x] Schedule dropdown for recurring cache warming (None/6h/12h/24h/48h)
  - [x] Per-site schedule management in dashboard
  - [x] Automatic scheduler creation/update/deletion based on interval selection
- [x] **Run on Publish (Webhooks)**
  - [x] "Auto-crawl on publish" toggle in site configuration
  - [x] Register `site_publish` webhook with Webflow API (per-site control)
  - [x] Webhook endpoint to receive publish events (org-scoped and legacy
        token-based)
  - [x] Webhook signature verification (NOTE: Webflow v2 doesn't provide
        signatures yet)
  - [x] Trigger cache warming job on verified publish events with auto_publish
        validation
  - [x] Platform-org mapping for workspace-based webhook resolution

### ✅ Slack Integration (Completed v0.20.0)

- [x] **Slack Application Development**
  - [x] OAuth flow for installing BBB Slack app to workspaces
  - [x] Bot tokens stored securely in Supabase Vault
  - [x] Auto-linking users to Slack workspaces via database triggers
  - [x] Supabase Slack OIDC support for user authentication
- [x] **Notification Delivery**
  - [x] Job completion notifications via Slack DMs
  - [x] Error notifications when jobs fail
  - [x] API endpoints for workspace management and user preferences

### ✅ Google Analytics 4 Integration (Completed)

- [x] **OAuth Connection Setup** (Steps 1-3)
  - [x] Google OAuth 2.0 configuration and credentials
  - [x] OAuth flow implementation with state token CSRF protection
  - [x] Account and property selection functionality
  - [x] Token storage in Supabase Vault with refresh logic
  - [x] Database schema for `user_ga_connections` table
  - [x] Dashboard UI for connecting/disconnecting GA4 properties
- [x] **Analytics Data Retrieval** (Step 4)
  - [x] Implement GA4 Data API client (`analyticsdata/v1beta`)
  - [x] Fetch recent visitor/view data for each page path
  - [x] Query metric: `screenPageViews` only
  - [x] Support for 7, 28, and 180-day lookback periods
  - [x] Scheduled background sync service (opt-in per domain, no sync by
        default)
  - [x] Token refresh mechanism for expired access tokens
- [x] **Pages Table Integration** (Step 5)
  - [x] Add analytics columns to `page_analytics` table:
    - [x] `page_views_7d` - Page views (last 7 days)
    - [x] `page_views_28d` - Page views (last 28 days)
    - [x] `page_views_180d` - Page views (last 180 days)
    - [x] `fetched_at` - Timestamp of last GA sync
  - [x] Atomic upsert logic to merge GA data with existing page records
- [x] **Task Prioritisation Enhancement** (Step 6)
  - [x] Incorporate page view data into task priority calculation
  - [x] Prioritise high-traffic pages for earlier cache warming
  - [x] Automatically enabled when domain has linked GA account
- [x] **Data Export Integration** (Step 7)
  - [x] Include page view metrics in CSV/JSON/Excel exports
  - [x] Add columns: Views (7d), Views (28d), Views (180d)
  - [x] Dashboard displays page view metrics alongside performance data

---

## 🎯 STAGE 5: MVP LAUNCH PREPARATION (Current)

### 5.0 Finalise outstanding actions above

- [x] GA
- [x] Account settings / management (settings page operational — billing awaits
      Paddle in 5.2)

### 5.1: Webflow Job Triggering & Polish

- [x] **Trigger immediate job when schedule or auto-publish enabled**
- [ ] **Extension Development**
  - [ ] Build Webflow Designer Extension using Designer Extension SDK
  - [ ] Implement site health metrics display (broken links, slow pages)
  - [ ] Add job management interface (view status, trigger crawls)
  - [ ] Configuration panel for schedule and webhook settings
- [ ] **Integration & Testing**
  - [ ] Connect extension to BBB API via OAuth
  - [ ] Test extension in Webflow Designer workspace
  - [ ] Handle error states and loading indicators

### 5.2: Payment Infrastructure

- [ ] **Paddle Integration**
  - [ ] Set up Paddle account and configuration
  - [ ] Implement subscription webhooks and payment flow
  - [ ] Create subscription plans and checkout process
- [ ] **Subscription Management**
  - [ ] Link subscriptions to organisations
  - [ ] Handle subscription updates and plan changes
  - [ ] Add subscription status checks
- [ ] **Usage Tracking & Quotas**
  - [ ] Implement usage counters and basic limits
  - [ ] Set up usage reporting functionality
  - [ ] Implement organisation-level usage quotas

### 5.3: Branding & UI Cleanup

- [ ] **Visual Design System**
  - [ ] Define colour palette, typography, spacing scales
  - [ ] Create reusable CSS variables and utility classes
  - [ ] Design logo and favicon assets
- [ ] **Dashboard Redesign & Polish**
  - [ ] Ensure responsive layout at core to everything
  - [ ] Optimise elements for dashboard vs. Webflow designer App
  - [ ] Improve nav bar, settings & notifications layout
  - [ ] Improve layout consistency and visual hierarchy
  - [ ] Refine job cards, status indicators, and data tables
  - [ ] Add loading states, empty states, and transitions
- [ ] **Error States & Messaging**
  - [ ] Design clear error messages and recovery suggestions
  - [ ] Improve validation feedback for forms
  - [ ] Create consistent notification system
- [ ] **Onboarding Flow**
  - [ ] Quick start flow - Crawl domain & create account
    - [ ] Marketing page
    - [ ] Webflow App + auth Webflow, set schedule, publish
  - [ ] Welcome screen for new users - tick box/dismiss cards
    - [ ] Quick start guide or tooltip tour
    - [ ] Crawl domain, create a schedule
    - [ ] Explain plans & update if required
    - [ ] View results, export slow and error pages
    - [ ] Integrate steps GA, Slack, Webflow

### 5.4: Marketing Page

- [ ] **Marketing Infrastructure**
  - [ ] Simple Webflow marketing page with product explanation
  - [ ] Basic navigation structure and call-to-action
    - [ ] Quick crawl & account creation
  - [ ] User documentation and help resources
  - [ ] Landing pages
    - [ ] Cache warmer - make your site load faster
    - [ ] Load speed - find slow pages
    - [ ] Broken links - find the important ones
    - [ ] Integrations - Slack, Webflow, Google Analytics
  - [ ] Pricing page with subscription tiers

### 5.5: Webflow Marketplace Submission

[Full details in Webflow Marketplace](docs/plans/webflow-marketplace.md)

- [ ] **Marketplace Preparation**
  - [ ] Complete Webflow App listing (description, screenshots, demo video)
  - [ ] Prepare support documentation and setup guide
  - [ ] Create terms of service and privacy policy
- [ ] **Submission & Approval**
  - [ ] Submit app to Webflow marketplace for review
  - [ ] Address feedback and make required changes
  - [ ] Obtain marketplace approval

### 5.6: Pre-Launch Polish & Testing

- [ ] **Alpha Testing**
  - [ ] Internal testing with team members
  - [ ] Beta testing with 3-5 friendly Webflow users
  - [ ] Collect feedback and address critical issues
- [ ] **Security & Compliance**
  - [ ] Final security audit of authentication flows
  - [ ] Review RLS policies and data isolation
  - [ ] Confirm GDPR/privacy compliance basics
- [ ] **Responsive Design Cleanup**
  - [ ] Audit all pages/layouts at mobile (<480px), tablet (480-960px), and
        desktop (960px+) breakpoints
  - [ ] Fix dashboard, settings, job details, and nav for small screens
  - [ ] Test integration panels (Webflow sites grid, member lists, GA
        properties) at all breakpoints
  - [ ] Ensure forms, modals, and toast notifications work on touch devices

### 5.7: Launch & First Customers

- [ ] **Soft Launch**
  - [ ] Make app available to first 10 users
  - [ ] Monitor system performance and error rates
  - [ ] Provide responsive support to early adopters
- [ ] **Iterative Improvements**
  - [ ] Gather user feedback on critical issues
  - [ ] Address bugs and usability problems
  - [ ] Track key metrics (signup rate, job success, retention)

## ⚪ Stage 6: Post-MVP Enhancements

### 🔴 WordPress Integration

- [ ] **WordPress Plugin Development**
  - [ ] Create basic WordPress plugin for Hover
  - [ ] Plugin configuration interface for domain settings
  - [ ] Display crawl results and statistics in WordPress admin
  - [ ] Trigger manual crawls from WordPress dashboard
- [ ] **WordPress.org Submission**
  - [ ] Prepare plugin listing and screenshots
  - [ ] Submit plugin to WordPress plugin directory
  - [ ] Address review feedback and obtain approval

### 🔴 Shopify Integration

- [ ] **Shopify App Development**
  - [ ] OAuth integration with Shopify
  - [ ] Embedded app interface for store owners
  - [ ] Display site health metrics in Shopify admin
  - [ ] Automatic crawl triggers on theme publish
- [ ] **Shopify App Store Submission**
  - [ ] Complete app listing with demo and screenshots
  - [ ] Submit to Shopify App Store for review
  - [ ] Address feedback and obtain approval

### Slack enhancements

- [ ] Slash commands (`/crawl sitedomain.com`)
- [ ] Threading with progress updates
- [ ] Interactive message actions

### 🔴 Multi-Platform Authentication Architecture

- [x] **Organisation-Based Data Model** (Completed v0.19.0)
  - [x] Implement many-to-many user-organisation relationships
  - [x] Create organisation context switching logic
  - [x] Implement data isolation between organisations
  - [ ] Add store/site entity linked to single organisation
- [ ] **Platform Authentication Adapters**
  - [ ] Shopify OAuth and session management
  - [ ] WordPress API key integration
  - [ ] Map platform stores/sites to BB organisations
  - [ ] Progressive account creation for platform users
- [ ] **Unified User System**
  - [ ] Single BB user accessible via multiple platforms
  - [ ] Platform context determines visible organisation
  - [ ] Shadow accounts for store staff (auto-created on action)
  - [ ] Account claiming and upgrade flows

### 🔴 Platform SDK Development

- [ ] **Core JavaScript SDK**
  - [ ] Extract data-binding system into standalone library
  - [ ] Create platform-agnostic API client
  - [ ] Implement organisation context management
  - [ ] Add platform-specific authentication handlers
- [ ] **Platform Adapters**
  - [ ] Shopify app bridge integration
  - [ ] WordPress plugin integration helpers
  - [ ] Platform-specific UI component adapters
  - [ ] Event handling for platform contexts

## ⚪ Stage 7: Scale & Advanced Features

### 🔴 Supabase Platform Integration

- [ ] **Real-time Features** (See
      [SUPABASE-REALTIME.md](docs/development/SUPABASE-REALTIME.md)) - **60%
      COMPLETE**
  - [x] Real-time notification badge updates via Postgres Changes subscription
        (v0.20.1)
  - [x] Real-time dashboard job list updates via WebSocket subscriptions
        (v0.20.1)
  - [x] Real-time job detail progress updates with per-job subscriptions
        (v0.20.1)
  - [ ] Real-time dashboard stats without page refresh (requires API endpoint
        changes)
  - [ ] Live presence indicators for multi-user organisations
- [ ] **Database Optimisation**
  - [x] Move CPU-intensive analytics queries to PostgreSQL functions
  - [ ] Optimise task acquisition with database-side logic
  - [x] Enhance Row Level Security policies for multi-tenant usage
  - [x] Consolidate database connection settings into single configuration
        location and make them configurable via environment variables
        ([internal/db/db.go:113-115](./internal/db/db.go#L113))
- [ ] **Backend Simplification via Supabase** (See
      [supabase-simplification.md](docs/plans/supabase-simplification.md))
  - [ ] Phase 1: Migrate stuck job cleanup to pg_cron
    - [ ] Create `run_job_cleanup()` PostgreSQL function
    - [ ] Schedule with `cron.schedule('job-cleanup', '* * * * *', ...)`
    - [ ] Remove `CleanupStuckJobs()` from Go worker monitors (~100 lines)
  - [ ] Phase 2: Migrate notification delivery to Edge Functions
    - [ ] Create `deliver-notification` Edge Function
    - [ ] Update `notify_job_status_change()` trigger to call via pg_net
    - [ ] Remove Go notification listener and Slack delivery code (~451 lines)
    - [ ] Remove `slack-go/slack` dependency
- [ ] **File Storage & Edge Functions**
  - [ ] Store crawler logs, sitemap caches, and error reports in Supabase
        Storage
  - [ ] Create Edge Functions for webhook handling and scheduled tasks
  - [ ] Handle Webflow publish events via Edge Functions
  - [ ] Add managed Postgres proxy in front of edge/serverless workloads to
        shield the primary pool

### 🔴 API & Integration Enhancements

- [ ] **API Client Libraries**
  - [ ] Enhance core JavaScript client with advanced authentication
  - [ ] Create interface-specific adapters
  - [ ] Document API with OpenAPI specification
- [ ] **Webhook System**
  - [ ] Implement webhook subscription for `site_publish` events
  - [ ] Verify webhook signatures using `x-webflow-signature` headers
  - [ ] Create webhook system for job completion notifications
- [ ] **API Key Management**
  - [ ] Create API key system for integrations
  - [ ] Implement scoped permissions for different interfaces

### 🔴 Infrastructure & Operations

- [ ] **1Password Secrets Management** -
      [Implementation Plan](./docs/plans/1password-secrets-integration.md)
  - [ ] Set up 1Password vault structure for Hover
  - [ ] Configure flyctl shell plugin for local development
  - [ ] Implement 1Password Service Account for GitHub Actions CI/CD
  - [ ] Migrate secrets from GitHub Secrets to 1Password
- [ ] **Database Management**
  - [ ] Set up backup schedule and automated recovery testing
  - [ ] Implement data retention policies
  - [ ] Create comprehensive database health monitoring
  - [ ] Implement burst-protected connection classes (separate Supabase
        roles/DSNs for batch vs interactive traffic)
  - [ ] Introduce read replica routing with lag monitoring and primary fallbacks
  - [ ] Add tenant-level pool quotas with schema/role isolation to enforce
        fairness
- [x] **Scheduling & Automation**
  - [x] Create configuration UI for scheduling options (completed v0.18.0)
  - [x] Implement recurring job scheduler for 6/12/24/48 hour intervals
        (completed v0.18.0)
  - [x] Background service checks for ready schedules every 30 seconds
        (completed v0.18.0)
  - [ ] Automatic cache warming based on Webflow publish events
- [ ] **Monitoring & Reporting**
  - [x] Fix completion percentage to reflect actual completed vs skipped tasks
        (not always 100%) ([internal/db/db.go:404](./internal/db/db.go#L404))
  - [ ] Publish OTEL metrics for connection pool saturation and wire Grafana
        alerts
  - [ ] Incident runbook and escalation checklist
  - [ ] Minimal status page for alpha
- [ ] **Frontend Architecture Consideration**
  - [ ] Evaluate Vue/Svelte framework migration if dashboard exceeds 8000 LOC or
        team scaling requires modern framework (current: 4000 LOC vanilla JS
        with custom data binding, no build process - consider migration only if
        actual pain points emerge)

## ⚪ Stage 7: Feature Refinement & Launch Preparation

### 🔴 Security & Compliance

- [ ] **Core app functionality**
  - [ ] Path inclusion/exclusion rules
  - [ ] Domain blocklist/allowlist for crawler (prevent crawling specific
        domains)
- [ ] **Enhanced Authentication**
  - [ ] Test and refine multi-provider account linking
  - [ ] Member invitation system for organisations
- [ ] **Audit & Security Features**
  - [x] Secure admin endpoints properly with system_role authentication
        ([internal/api/admin.go:11,25](./internal/api/admin.go#L11))
  - [ ] GDPR compliance features (data export, deletion audit trails)

### 🔴 Launch & Marketing

- [ ] **Marketing Infrastructure**
  - [ ] Simple Webflow marketing page with product explanation
  - [ ] Basic navigation structure and call-to-action
  - [ ] User documentation and help resources
- [ ] **Launch Preparation**
  - [ ] Complete marketplace submission process
  - [ ] Set up support channels and user onboarding
  - [ ] Implement usage analytics and tracking

### 🔴 Data Archiving & Retention

- [ ] **Implement two-tier data storage strategy**
  - [ ] Use Supabase Storage for "hot" data (recent logs, debug files)
  - [ ] Implement Cloudflare R2 for "cold" storage of historical HTML page
        captures
  - [ ] Create automated Go job to handle data lifecycle (e.g., move files > 30
        days to R2)
  - [ ] Update database to track storage location (hot/cold) for each archived
        file

### 🟡 Alpha Data Retention

- [ ] **Retention policy for alpha**
  - [ ] Auto-delete crawler logs and stored HTML older than 90 days

### 🔴 Content Storage & Change Tracking

- [ ] **Implement Semantic Hashing for change detection** -
      [Implementation Plan](./docs/plans/content-storage-and-change-tracking.md)
  - [ ] Add `content_hash` and `html_storage_path` columns to `tasks` table
  - [ ] Add `latest_content_hash` column to `pages` table
  - [ ] Implement HTML parsing and canonical content extraction in Go worker
  - [ ] Store HTML in Supabase Storage only when semantic hash changes

### ✅ Code Quality & Maintenance (Completed)

- [x] **Increase Test Coverage** -
      [Implementation Plan](./docs/plans/increase-test-coverage.md)
  - [x] Set up Supabase test branch database infrastructure
  - [x] Add testify testing framework
  - [x] Create simplified test plan (Phase 1: 80-115 lines)
  - [x] Implement Phase 1 tests (GetJob, CreateJob, CancelJob,
        ProcessSitemapFallback)
  - [x] Implement integration tests (EnqueueJobURLs)
  - [x] Implement unit tests with mocks (CrawlerInterface refactoring)
  - [x] Enable Codecov reporting and Test Analytics
  - [x] Set up CI/CD with Supabase pooler URLs for IPv4 compatibility
  - [x] Fix test environment loading to use .env.test file
  - [x] Reorganise testing documentation into modular structure
  - [x] Fix critical test issues from expert review (P0/P1 priorities)
  - [x] Implement sqlmock tests for database operations
  - [x] Create comprehensive mock infrastructure (MockDB, DSN helpers)
  - [x] **Implement Comprehensive API Testing** - ✅ **COMPLETED**
- [x] **Code Quality Improvement** - core quality gates now enforced in CI
  - [x] Phase 1: Automated formatting and ineffectual assignments cleanup
  - [x] Phase 2: Refactor high-complexity functions (processTask,
        processNextTask completed)
  - [x] Add golangci-lint to CI/CD pipeline with Go 1.25 compatibility
  - [x] Improve Go Report Card score from C to A

### 🔴 Robots.txt Compliance Auditing

- [ ] **Track and audit robots.txt filtering decisions**
  - [ ] Add optional logging table for blocked URLs during job processing
  - [ ] Record URL, path, matching disallow pattern, and job context
  - [ ] Create admin endpoint to review filtering decisions
  - [ ] Add metrics for blocked vs allowed URL ratios per domain
  - [ ] Enable/disable audit logging per job for performance
