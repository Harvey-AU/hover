# Hover Architecture

## System Overview

Hover is a web cache warming service built in Go. It crawls URLs across one or
more domains, warms caches, and records performance metrics. A worker pool
architecture handles concurrent crawling, backed by PostgreSQL (via Supabase)
for state and a Fly.io single-machine deployment.

---

## Core Components

### Worker Pool System

The worker pool is the engine of the application. It scales dynamically between
a base count and a configured maximum based on how many jobs are active and what
their per-job concurrency settings are.

- **Task claiming**: Workers claim pending tasks atomically using PostgreSQL's
  `FOR UPDATE SKIP LOCKED` — no application-level locking required
- **Concurrency model**: Each worker goroutine runs up to `WORKER_CONCURRENCY`
  (20) tasks simultaneously via an in-process semaphore
- **Dynamic scaling**: Pool size =
  `ceil(∑ job concurrency / WORKER_CONCURRENCY × 1.1)`, capped at
  `maxWorkersProduction` (160)
- **Task lifecycle**: Tasks progress
  `waiting → pending → running → completed/failed/skipped`. The `waiting` state
  is a holding area; tasks are promoted to `pending` by a background monitor
  every 5 seconds
- **Domain rate limiting**: An adaptive per-domain delay backs off when sites
  signal rate limits and recovers after sustained successes. High delays reduce
  the job's effective concurrency fed into the scaling formula
- **Recovery**: Periodic cleanup of tasks stuck in `running` state; graceful
  re-queue on restart

For all configurable values, see [CONFIG_REFERENCE.md](CONFIG_REFERENCE.md).

### Database Layer (PostgreSQL via Supabase)

- **Normalised schema**: `domains` → `pages` → `jobs` → `tasks` hierarchy
  reduces storage redundancy and simplifies constraint enforcement
- **Row-level locking**: `FOR UPDATE SKIP LOCKED` for contention-free task
  claiming under high worker concurrency
- **Incremental counters**: Job progress fields (`completed_tasks`,
  `failed_tasks`, `skipped_tasks`, `progress`, `status`) are maintained by
  row-level triggers using O(1) delta arithmetic — no COUNT(\*) scans
- **Connection pooling**: 150 max open / 30 idle connections to Supabase
  pgBouncer; queue operations gated by a 120-slot semaphore
- **Row Level Security**: Enforced at the database layer for multi-tenant data
  isolation

### API Layer

- **RESTful design**: `/v1/*` endpoints with standardised JSON responses
- **Authentication**: JWT validation via Supabase Auth; user and organisation
  context extracted per request
- **Middleware**: CORS, structured logging, IP-based rate limiting, request ID
  tracking
- **Dashboard API**: Serves pre-aggregated stats, job lists, and task-level
  detail to the frontend

### Crawler

- **HTTP transport**: Connection-pooled `net/http` client (150 global / 50
  per-host)
- **Cache validation**: Inspects `X-Cache`, `CF-Cache-Status`, and similar
  headers; records hit/miss/stale
- **Link discovery**: Optional HTML parsing to extract additional URLs and
  enqueue them as new tasks within the same job
- **Robots.txt compliance**: Fetched and cached per domain; `Crawl-Delay`
  honoured via the domain rate limiter
- **Sitemap processing**: Parses XML sitemaps (including sitemap indexes) to
  seed jobs with a full URL list before crawling begins

---

## Application Entry Points (`cmd/`)

| Directory                  | Purpose                                     |
| -------------------------- | ------------------------------------------- |
| `cmd/app/`                 | Main server: HTTP API + worker pool         |
| `cmd/hover/`               | CLI tool for job management and diagnostics |
| `cmd/test_jobs/`           | Load-testing utility for the job queue      |
| `cmd/archive_key_migrate/` | One-off migration for archive storage keys  |

---

## Internal Packages (`internal/`)

| Package          | Responsibility                                                            |
| ---------------- | ------------------------------------------------------------------------- |
| `api/`           | HTTP handlers, middleware, request/response types                         |
| `archive/`       | Cold-storage archival to Cloudflare R2 (active background job)            |
| `auth/`          | JWT validation, session management                                        |
| `benchmarks/`    | Go benchmark helpers for performance regression testing                   |
| `cache/`         | In-process caching utilities                                              |
| `crawler/`       | HTTP crawl logic, sitemap parsing, cache validation, link extraction      |
| `db/`            | PostgreSQL connection, queue semaphore, batch writer, page/domain records |
| `jobs/`          | Worker pool, job manager, domain rate limiter, task promotion             |
| `loops/`         | Background loop helpers (ticker management, graceful stop)                |
| `mocks/`         | Test doubles for DB and crawler interfaces                                |
| `notifications/` | In-app notification creation and delivery                                 |
| `observability/` | OpenTelemetry metrics, Grafana/OTLP export, Sentry integration            |
| `storage/`       | Supabase Storage client for HTML upload and retrieval                     |
| `techdetect/`    | Technology fingerprinting from crawled page content                       |
| `testutil/`      | Shared test helpers and fixtures                                          |
| `util/`          | URL normalisation, string helpers                                         |

---

## Data Model

### Core Tables

**`domains`** — one row per unique domain name (e.g. `example.com`). Shared
across jobs and organisations.

**`pages`** — one row per unique `(domain_id, host, path)`. Page-level metadata
and crawl metrics accumulate here across multiple jobs.

**`jobs`** — one row per crawl run. Tracks lifecycle status, progress counters
(total/completed/failed/skipped/running/pending/waiting tasks), concurrency
settings, adaptive delay state, and timing.

**`tasks`** — one row per URL × job. The hot table: all status transitions, HTTP
results, timing, and HTML storage references live here. See the migrations for
the current column set — the schema has grown significantly from the initial
version.

### Task Lifecycle

```
waiting → pending → running → completed
                           ↘ failed
                           ↘ skipped
```

- **waiting**: Created but not yet eligible for claiming (quota or concurrency
  limit reached). Promoted to `pending` by the quota monitor every 5 seconds.
- **pending**: Ready to be claimed by a worker.
- **running**: Claimed; HTTP request in flight.
- **completed / failed / skipped**: Terminal states. Trigger the incremental
  counter update on the parent job row.

### Job Counters (Trigger-Maintained)

`jobs.completed_tasks`, `failed_tasks`, `skipped_tasks`, `running_tasks`,
`pending_tasks`, `waiting_tasks`, `progress`, `status`, `started_at`, and
`completed_at` are all maintained by row-level triggers on the `tasks` table. Go
code never manually increments these — the DB is the source of truth.

`running_tasks` is a special case: it is incremented asynchronously in memory
and flushed to the DB in batches to avoid per-task hot-row contention.

---

## Key Design Decisions

### O(1) Trigger Counters

All job progress fields use incremental delta arithmetic in trigger functions
(`update_job_counters`, `update_job_queue_counters`) rather than correlated
COUNT(\*) subqueries. This keeps trigger overhead constant regardless of job
size. See
`supabase/migrations/20260409111417_incremental_update_job_counters.sql` and
`20260409120000_unify_job_progress_triggers.sql`.

### Waiting → Pending Promotion

Tasks are created in `waiting` state and promoted to `pending` by
`StartQuotaPromotionMonitor` every 5 seconds. This decouples URL discovery from
worker saturation: a sitemap with 50,000 URLs can be inserted immediately
without flooding the pending queue. Per-job concurrency limits are enforced at
promotion time, not at claim time.

### Domain Rate Limiter

An in-process `DomainLimiter` tracks per-domain adaptive delays. When a site
returns rate-limit signals (429, 503, specific headers), the delay increases by
500ms steps up to 60s. After 5 consecutive successes the delay steps down. This
state also feeds `calculateConcurrencyTarget()` — a heavily throttled domain
reduces the job's effective worker allocation automatically.

### Batch Running-Task Increments

Claiming a task (setting `status = 'running'`) is an O(1) DB operation, but
updating `jobs.running_tasks` on every claim would cause hot-row contention
under high concurrency. Instead, increments are buffered in memory and flushed
in batches every 200ms (`GNH_RUNNING_TASK_FLUSH_INTERVAL_MS`). A reconciliation
loop corrects any drift periodically.

### HTML Cold Storage

Crawled HTML is optionally captured and written to Supabase Storage (hot) by an
async worker pool (`GNH_HTML_PERSIST_WORKERS = 32`). A background archival job
(`internal/archive/`) sweeps completed-job HTML to Cloudflare R2 at a
configurable interval, then marks tasks as archived. Jobs are promoted to
`archived` status once all their tasks are confirmed in cold storage.

---

## Observability

### Metrics (OpenTelemetry → Grafana)

The application exports metrics via OTLP to Grafana Cloud. Key signals:

- Task claim latency (`GetNextTask` p50/p95/p99)
- Tasks completed / failed / skipped per minute
- Active job and worker counts
- DB pool utilisation and queue semaphore wait time
- Domain limiter delay distribution

### Error Tracking (Sentry)

Sentry captures unexpected errors with stack traces. Routine failures (404s,
timeouts on individual tasks) are not sent to Sentry — only structural failures
(job creation, worker startup, DB connectivity, stuck-job recovery) that
indicate system-level problems.

Sample rate: 10% traces in production, 100% in development.

### Flight Recorder

When `FLIGHT_RECORDER_ENABLED=true`, the application captures Go runtime trace
data on request. Useful for diagnosing goroutine scheduling, GC pressure, and
lock contention. See [flight-recorder.md](flight-recorder.md).

---

## Real-time Frontend Updates

Both the dashboard and job detail page use **Supabase Realtime Postgres
Changes** subscriptions to receive live updates without polling:

- **Dashboard** (`gnh-auth-extension.js`): Subscribes to INSERT/UPDATE/DELETE on
  `jobs` filtered by `organisation_id`. Uses a 250ms debounce to coalesce rapid
  updates, with 1s active / 10s idle fallback polling.
- **Job page** (`job-page.js`): Subscribes to UPDATE on `jobs` for the specific
  `job_id`. Task-level progress is derived from job counter fields — there is no
  separate tasks subscription. Same 250ms debounce + 1s fallback.

Both subscribers ignore the realtime payload content entirely and use the event
purely as a signal to re-fetch current state from the API. This means
`REPLICA IDENTITY DEFAULT` is safe (only the PK is needed in the `old` record).

---

## Frontend

The dashboard UI uses a vanilla JS template-and-data-binding system with no
build step. JS files are served from `web/static/js/` and use `gnh-` prefixed
attribute conventions and file names. The system handles:

- Attribute-based event delegation (`gnh-action`)
- Data binding and template rendering (`data-gnh-bind`, `data-gnh-template`)
- Authentication state gating
- Real-time data refresh via the Supabase subscription above

---

## Deployment

| Concern      | Solution                                              |
| ------------ | ----------------------------------------------------- |
| Hosting      | Fly.io, `syd` region, single machine (4 CPU / 8GB)    |
| Database     | Supabase PostgreSQL with pgBouncer pooler             |
| Auth         | Supabase Auth on custom domain                        |
| Real-time    | Supabase Realtime (Postgres Changes)                  |
| Hot storage  | Supabase Storage (HTML captures, assets)              |
| Cold storage | Cloudflare R2 (archived HTML, via `internal/archive`) |
| CDN          | Cloudflare                                            |
| Monitoring   | Grafana Cloud (OTLP metrics) + Sentry                 |
| CI/CD        | GitHub Actions → Fly.io deploy                        |

### Schema Management

All schema changes go through numbered migration files in
`supabase/migrations/`. Migrations are additive only — no destructive changes to
deployed columns or indexes without explicit review. The migration filename
timestamp is the authoritative ordering key.

### Health Checks

Fly.io polls `GET /health` every 15s. The handler verifies DB connectivity and
returns 200/503. Graceful shutdown drains in-flight tasks before termination.
