# Hover Architecture

## System Overview

Hover is a web cache warming service built in Go, designed for Webflow sites and
other web applications. It uses a worker pool architecture for efficient URL
crawling and cache warming, with a focus on reliability, performance, and
observability.

## Core Components

### Worker Pool System

- **Concurrent Processing**: Multiple workers process tasks simultaneously using
  PostgreSQL's `FOR UPDATE SKIP LOCKED`
- **Job Management**: Jobs are broken down into individual URL tasks and
  distributed across workers
- **Recovery System**: Automatic recovery of stalled or failed tasks with
  exponential backoff
- **Task Monitoring**: Real-time monitoring of task progress and status

### Database Layer (PostgreSQL)

- **Normalised Schema**: Separate tables for domains, pages, jobs, and tasks to
  reduce redundancy
- **Row-Level Locking**: Uses `FOR UPDATE SKIP LOCKED` for efficient concurrent
  task acquisition
- **Connection Pooling**: Optimised pool settings (25 max open, 10 max idle
  connections)
- **Data Integrity**: Maintains job history, statistics, and task relationships

### API Layer

- **RESTful Design**: `/v1/*` endpoints with standardised responses and error
  handling
- **Authentication**: JWT-based auth with Supabase Auth integration
- **Middleware Stack**: CORS, logging, rate limiting, request tracking
- **Request IDs**: Every request tracked with unique identifier

### Crawler System

- **Concurrent URL Processing**: Configurable concurrency with rate limiting
- **Cache Validation**: Monitors cache status and performance metrics
- **Response Tracking**: Records response times, status codes, and cache hits
- **Link Discovery**: Optional extraction of additional URLs from crawled pages

## Technical Concepts

### Jobs and Tasks

**Job**: A collection of URLs from a single domain to be crawled

- Contains metadata: domain, user/organisation, concurrency settings
- Tracks progress: total/completed/failed task counts
- Has lifecycle: pending → running → completed/cancelled

**Task**: Individual URL processing unit within a job

- References a specific page within the job's domain
- Tracks execution: status, timing, response metrics, errors
- Can be: pending → running → completed/failed/skipped

**Worker**: Process that executes tasks concurrently

- Claims tasks atomically using database locking
- Handles retries and error reporting
- Updates task and job progress

### Database Schema

#### Normalised Structure

```sql
-- Domains table stores unique domain names
CREATE TABLE domains (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) UNIQUE NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Pages table stores paths with domain references
CREATE TABLE pages (
    id SERIAL PRIMARY KEY,
    domain_id INTEGER REFERENCES domains(id),
    path TEXT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(domain_id, path)
);

-- Jobs table stores job metadata
CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    domain_id INTEGER REFERENCES domains(id),
    user_id TEXT,
    organisation_id TEXT,
    status TEXT NOT NULL,
    progress REAL DEFAULT 0.0,
    total_tasks INTEGER DEFAULT 0,
    completed_tasks INTEGER DEFAULT 0,
    failed_tasks INTEGER DEFAULT 0,
    skipped_tasks INTEGER DEFAULT 0,
    found_tasks INTEGER DEFAULT 0,
    sitemap_tasks INTEGER DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP WITH TIME ZONE,
    completed_at TIMESTAMP WITH TIME ZONE,
    concurrency INTEGER DEFAULT 1,
    find_links BOOLEAN DEFAULT FALSE,
    max_pages INTEGER DEFAULT 100,
    include_paths TEXT,
    exclude_paths TEXT,
    required_workers INTEGER DEFAULT 1
);

-- Tasks table stores individual crawl tasks
CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    job_id TEXT REFERENCES jobs(id),
    domain_id INTEGER REFERENCES domains(id),
    page_id INTEGER REFERENCES pages(id),
    status TEXT NOT NULL,
    source_type TEXT,
    source_url TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP WITH TIME ZONE,
    completed_at TIMESTAMP WITH TIME ZONE,
    status_code INTEGER,
    response_time INTEGER,
    cache_status TEXT,
    content_type TEXT,
    error TEXT,
    retry_count INTEGER DEFAULT 0
);
```

#### Key Design Decisions

- **Domain/Page Normalisation**: Reduces storage and improves data integrity
- **No Depth Tracking**: Removed in v0.3.8 as unnecessary complexity
- **PostgreSQL Features**: Leverages `FOR UPDATE SKIP LOCKED` for lock-free task
  claiming
- **Separate Task Counts**: Tracks `sitemap_tasks` vs `found_tasks` for
  analytics

### Job Lifecycle

1. **Job Creation**
   - Validate domain and create domain/page records
   - Insert job with pending status
   - Optionally process sitemap or create root task

2. **Job Start**
   - Update status to running
   - Reset any stalled tasks from previous runs
   - Add job to worker pool for processing

3. **Task Processing**
   - Workers claim pending tasks atomically
   - Crawl URLs with retry logic and rate limiting
   - Store results and update task status
   - Update job progress counters

4. **Job Completion**
   - Automatic detection when all tasks finished
   - Calculate final statistics
   - Mark job as completed with timestamp

5. **Recovery & Cleanup**
   - Periodic cleanup of stuck jobs
   - Task recovery for server restarts
   - Failed task retry with exponential backoff

## Codebase Structure

### Architectural Principles

Hover follows **focused, testable function design** established through
systematic refactoring:

- **Function Size**: Functions kept under 50 lines where possible
- **Single Responsibility**: Each function has one clear purpose
- **Testing**: Strategic test coverage for critical paths and complex logic
- **Extract + Test + Commit**: Proven methodology for safe refactoring

### Application Entry Points (`cmd/`)

- `cmd/app/main.go` - Main service entry point with server setup
- `cmd/test_jobs/main.go` - Job queue testing utility

### Core Business Logic (`internal/`)

#### API Layer (`internal/api/`)

- `handlers.go` - HTTP route handlers and middleware
- `auth.go` - JWT authentication and user validation
- `jobs.go` - **Refactored**: Job management endpoints with focused functions
  - `parseTaskQueryParams()` - Query parameter validation
  - `validateJobAccess()` - Authentication and authorization
  - `buildTaskQuery()` - SQL query construction
  - `formatTasksFromRows()` - Response formatting
- `response.go` - Standardised response formats
- `errors.go` - Error handling and codes

#### Database Layer (`internal/db/`)

- `db.go` - **Refactored**: PostgreSQL connection and setup
  - `createCoreTables()` - Table creation with dependency management
  - `createPerformanceIndexes()` - Index management and optimization
  - `enableRowLevelSecurity()` - Security policy setup
- `queue.go` - Database queue operations and transactions
- `pages.go` - Page and domain record management
- `users.go` - User and organisation data
- `health.go` - Database health monitoring

#### Job System (`internal/jobs/`)

- `manager.go` - **Refactored**: Job lifecycle management
  - `handleExistingJobs()` - Existing job conflict resolution
  - `createJobObject()` - Job instance creation
  - `setupJobDatabase()` - Database record creation
  - `setupJobURLDiscovery()` - URL discovery coordination
  - `validateRootURLAccess()` - Robots.txt validation
  - `createManualRootTask()` - Manual URL task creation
- `worker.go` - **Partially Refactored**: Worker pool and task processing
  - `claimPendingTask()` - Task claiming logic
  - `prepareTaskForProcessing()` - Task preparation and enrichment
- `types.go` - Job and task type definitions

#### Crawler (`internal/crawler/`)

- `crawler.go` - **Refactored**: HTTP client and URL processing
  - `validateCrawlRequest()` - URL validation and parsing
  - `setupResponseHandlers()` - Colly response/error handling
  - `performCacheValidation()` - Cache warming logic
  - `setupLinkExtraction()` - HTML link categorization
  - `executeCollyRequest()` - HTTP request execution
- `sitemap.go` - Sitemap parsing and URL extraction
- `config.go` - Crawler configuration and rate limiting
- `types.go` - Crawler response types

#### Utilities (`internal/util/`)

- `url.go` - URL normalisation and validation

## System Monitoring

### Sentry Integration Strategy

Hover uses Sentry for both error tracking and performance monitoring with a
strategic approach to avoid over-logging.

#### Configuration

```go
sentry.Init(sentry.ClientOptions{
    Dsn:              config.SentryDSN,
    Environment:      config.Env,
    TracesSampleRate: 0.1, // 10% in production, 100% in development
    AttachStacktrace: true,
    Debug:           config.Env == "development",
})
```

#### Error Capture Strategy

**Critical Business Logic Failures:**

- Job creation, start, and cancellation failures
- Worker startup failures and task status update failures
- Transaction failures and stuck job cleanup failures
- Database connection and server startup/shutdown failures

**Avoided**: Individual task processing errors, expected/handled errors, normal
operational events

#### Performance Monitoring Spans

- `manager.create_job`, `manager.start_job`, `manager.cancel_job` - Job
  operations
- `manager.get_job`, `manager.get_job_status` - Job queries
- `manager.process_sitemap` - Sitemap processing
- `db.cleanup_stuck_jobs`, `db.create_page_records` - Database operations

### Health Monitoring

- **Database Health**: Connection status and query performance
- **Worker Status**: Active worker count and task processing rates
- **Job Progress**: Real-time completion tracking and statistics
- **API Performance**: Request timing and error rates

### Performance Debugging

For detailed runtime performance analysis, the application includes a
[Flight Recorder](flight-recorder.md) that captures Go runtime trace data. This
helps diagnose:

- Goroutine scheduling and concurrency issues
- Memory allocation patterns and GC pressure
- CPU usage hotspots and bottlenecks
- Lock contention and synchronisation problems

## Frontend Integration

### Template + Data Binding System

Hover uses a template-based approach that allows flexible HTML layouts whilst
JavaScript provides functionality through attribute-based event handling.

**Current Implementation (v0.5.3):**

```html
<!-- Dashboard with attribute-based event handling -->
<div class="dashboard">
  <button bb-action="refresh-dashboard">↻ Refresh</button>
  <button bb-action="create-job">+ New Job</button>
  <div bb-action="view-job-details" bb-data-job-id="123">View Details</div>
</div>

<!-- JavaScript automatically handles bb-action attributes -->
<script src="/dashboard.html"></script>
```

**Current Data Binding (v0.5.4):**

```html
<!-- Template binding for dynamic content -->
<div class="stats">
  <span data-bb-bind="stats.total_jobs">0</span>
  <div data-bb-template="job">
    <h4 data-bb-bind="domain">Loading...</h4>
    <div data-bb-bind-style="width:{progress}%"></div>
    <span data-bb-bind="status">pending</span>
  </div>
</div>

<!-- Authentication conditional rendering -->
<div data-bb-auth="required">
  <form data-bb-form="create-job" data-bb-validate="live">
    <input name="domain" required data-bb-validate-type="url" />
    <button type="submit">Create Job</button>
  </form>
</div>

<!-- Data binding library (production ready) -->
<script src="/js/bb-data-binder.min.js"></script>
```

**Data Flow:**

- Event delegation scans DOM for `bb-action` attributes
- Data binding scans DOM for `data-bb-bind`, `data-bb-template`, `data-bb-form`
  attributes
- JavaScript handles clicks, form submissions, and data population automatically
- API endpoints (`/v1/dashboard/stats`, `/v1/jobs`) provide data
- Real-time data binding populates `data-bb-bind` elements with live API data

**Integration Benefits:**

- Users control all HTML structure and CSS styling
- No CSS conflicts with existing designs
- Works with any frontend framework (Webflow, custom sites)
- Lightweight JavaScript library (~50KB)
- Complete form handling with validation and authentication
- Real-time data binding with template engine for repeated content
- Conditional rendering based on authentication state

## Security & Authentication

### JWT Authentication

- **Supabase Auth Integration**: Validates JWT tokens from Supabase
- **User Context**: Extracts user and organisation IDs from tokens
- **Protected Endpoints**: Requires authentication for job operations
- **Row Level Security**: PostgreSQL RLS policies for data isolation

### Planned: Multi-Platform Support

- Extending beyond direct dashboard access
- Shopify and Webflow apps will authenticate through their platforms
- Organisation-based context switching
- See `/plans/platform-auth-architecture.md` for architectural approach

### Rate Limiting

- **IP-Based Limiting**: Token bucket algorithm (5 requests/second default)
- **Client IP Detection**: Supports X-Forwarded-For headers for proxies
- **Crawler Rate Limiting**: Configurable delays between URL requests
- **Concurrency Controls**: Per-job worker limits

### Request Security

- **Input Validation**: URL and parameter sanitisation
- **Error Sanitisation**: Prevents information leakage
- **CORS Configuration**: Controlled cross-origin access
- **Request Tracking**: Unique request IDs for audit trails

## Deployment Architecture

### Infrastructure

- **Hosting**: Fly.io with auto-scaling
- **Database**: PostgreSQL with connection pooling
- **CDN**: Cloudflare for caching and protection
- **Monitoring**: Sentry for errors and performance
- **Authentication**: Supabase Auth with custom domain
- **Real-time**: Supabase Realtime for live job progress updates
- **Storage**: Supabase Storage for logs and file assets

### Data Storage & Archiving Strategy

The project employs a two-tier strategy for handling file storage to balance
performance, cost, and accessibility.

- **Hot Storage (Supabase Storage)**: Used for recent and frequently accessed
  files. This includes temporary assets, crawler logs for active jobs, and
  recent HTML page captures for debugging purposes. Supabase Storage provides
  fast, instant access suitable for day-to-day operations.

- **Cold Storage (Cloudflare R2)**: Planned for the long-term archival of
  historical data, primarily HTML page content. As data ages (e.g., older than
  30-90 days), an automated Go background job will move it from Supabase Storage
  to Cloudflare R2. R2 offers significantly lower storage costs with no egress
  fees, making it ideal for large volumes of data that are accessed
  infrequently. This approach ensures the main database and hot storage remain
  lean and performant, while historical data is preserved cost-effectively.

### Configuration

- **Environment Variables**: Centralised configuration
- **Database Migrations**: Schema versioning and updates
- **Health Checks**: Application and database monitoring
- **Graceful Shutdown**: Proper cleanup on termination

### Scalability Considerations

- **Worker Pool Scaling**: Configurable worker counts per job
- **Database Connection Limits**: Optimised pooling settings
- **Task Batching**: Efficient bulk operations
- **Memory Management**: Controlled resource usage

## Performance Optimisation

### Database Optimisations

- **Connection Pooling**: 25 max open, 10 max idle connections
- **Query Optimisation**: Indexed queries and efficient joins
- **Batch Operations**: Reduce individual database calls
- **Lock-Free Task Claiming**: `FOR UPDATE SKIP LOCKED` prevents contention

### Crawler Optimisations

- **Concurrent Processing**: Multiple workers process URLs simultaneously
- **Connection Reuse**: HTTP client connection pooling
- **Rate Limiting**: Prevents overwhelming target servers
- **Response Streaming**: Efficient memory usage for large responses

### Memory Management

- **Resource Cleanup**: Proper goroutine and connection cleanup
- **Buffer Management**: Controlled memory allocation
- **Garbage Collection**: Optimised for low-latency operations

## Supabase Integration Strategy

### Real-time Features

Uses **Postgres Changes** subscriptions via Supabase Realtime. See
[SUPABASE-REALTIME.md](../development/SUPABASE-REALTIME.md) for implementation
patterns and lessons learned.

**Implemented:**

- ✅ **Notification Badge**: Real-time updates when jobs complete (v0.20.0)
  - Postgres Changes subscription on `notifications` table
  - WebSocket CSP configured for `wss://hover.auth.goodnative.co`
  - 200ms query delay to avoid transaction visibility race condition

**Planned:**

- **Live Job Progress**: Postgres Changes on `jobs` table for instant updates
- **Dashboard Stats**: Real-time totals without page refresh
- **Team Presence**: Live indicators for multi-user organisations

**Key Pattern:**

```javascript
window.supabase
  .channel("table-changes")
  .on("postgres_changes", { event: "INSERT", table: "notifications" }, () => {
    setTimeout(() => refreshData(), 200); // Delay for transaction visibility
  })
  .subscribe();
```

### Database Functions (Stage 5)

- **Complex Analytics**: Move CPU-intensive queries from Go to PostgreSQL
  functions
- **Task Acquisition**: Optimise worker task claiming with database-side logic
- **Progress Calculations**: Real-time progress updates via database triggers

### Edge Functions (Stage 6+)

- **Webhook Processing**: Handle Webflow publish events without exposing main
  API
- **Scheduled Jobs**: Cron-like functionality for recurring cache warming
- **Integration Endpoints**: Lightweight processing for third-party integrations

### File Storage (Stage 5)

- **Crawler Logs**: Store detailed crawling logs and error reports
- **Sitemap Caching**: Cache parsed sitemaps for faster job processing
- **Screenshots**: Optional page screenshots for debugging failed crawls

### Row Level Security Enhancement (Stage 6)

- **Multi-tenant Data Isolation**: Replace Go auth middleware with
  database-level policies
- **Organisation Access**: Automatic data filtering based on user's organisation
- **Audit Trails**: Database-enforced access logging and compliance
