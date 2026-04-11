# Configuration Reference

Last reviewed: 2026-04-10

Every configurable dial in the application — env vars, hardcoded constants, and
their relationships. Values reflect production unless noted. For the flat
inventory of env vars and their classification (secret vs non-secret), see
[`docs/operations/ENV_VARS.md`](../operations/ENV_VARS.md).

---

## Key relationships

```
DB_MAX_OPEN_CONNS (110)
  └── DB_POOL_RESERVED_CONNECTIONS (4)  →  available = 106
        └── DB_QUEUE_MAX_CONCURRENCY (88)  →  hard cap = min(88, 106) = 88
              └── PressureController soft limit  →  88 down to 10, self-tuning
                    └── base workers (30) × WORKER_CONCURRENCY (20) = 600 task slots
                          └── max workers (130) — capped below the 180-conn Supabase pool
```

Direct DB calls (page writes, domain lookups, etc.) bypass the queue semaphore
and draw from the shared pool. With 88 semaphore slots out of 110 total, 22
connections remain for non-semaphored direct calls.

---

## DB connection pool

**Source:** `internal/db/db.go`, `fly.toml`

| Env var / constant       | Production value     | Code default | What it controls                           |
| ------------------------ | -------------------- | ------------ | ------------------------------------------ |
| `DB_MAX_OPEN_CONNS`      | **110** (`fly.toml`) | 70           | Hard cap on open connections to pgBouncer  |
| `DB_MAX_IDLE_CONNS`      | **25** (`fly.toml`)  | 20           | Idle connections kept warm                 |
| `defaultConnMaxLifetime` | hardcoded            | 5 min        | Max connection lifetime                    |
| `defaultConnMaxIdleTime` | hardcoded            | 2 min        | Idle connection eviction                   |
| `statementTimeoutMs`     | hardcoded            | 60s          | Per-statement timeout (added to DSN)       |
| `idleInTxnTimeoutMs`     | hardcoded            | 30s          | Idle-in-transaction timeout (added to DSN) |

Supabase pooler is auto-detected via the `pooler.supabase.com` hostname in
`DATABASE_URL`. When detected, `simple_protocol` and `pgbouncer=true` are
appended to the DSN automatically.

Supabase connection pool size is configured on the Supabase dashboard (currently
180). Leave headroom above `DB_MAX_OPEN_CONNS` for Supabase-internal connections
(realtime, auth, API, migrations).

---

## Queue semaphore

**Source:** `internal/db/queue.go`, `internal/db/pressure.go`

Wraps a semaphore around all task-claim and batch-update DB operations. Direct
DB calls (page writes, domain lookups, etc.) bypass this gate and draw directly
from the pool.

The semaphore has two limits:

- **Hard limit** — `min(DB_QUEUE_MAX_CONCURRENCY, MAX_OPEN − RESERVED)`, set at
  startup. The channel capacity never exceeds this.
- **Soft limit** — the pressure-adjusted effective limit maintained by
  `PressureController`. Starts at `pressureInitialLimit` (55) and moves between
  `minLimit` (10) and the hard limit based on observed `pool_wait_total`.

| Env var / constant             | Production value    | Default | What it controls                                                        |
| ------------------------------ | ------------------- | ------- | ----------------------------------------------------------------------- |
| `DB_QUEUE_MAX_CONCURRENCY`     | **88** (`fly.toml`) | 12      | Semaphore hard cap; effective = `min(this, MAX_OPEN − RESERVED)`        |
| `DB_POOL_RESERVED_CONNECTIONS` | **4** (unset)       | 4       | Connections held back from the semaphore budget                         |
| `DB_POOL_WARN_THRESHOLD`       | **0.90** (unset)    | 0.90    | Log warn at 90% pool usage                                              |
| `DB_POOL_REJECT_THRESHOLD`     | **0.95** (unset)    | 0.95    | Fire Sentry "DB pool saturated" at 95%                                  |
| `defaultExecuteTimeout`        | hardcoded           | 30s     | Context timeout for `Execute`/`ExecuteWithContext` when caller has none |
| `DB_TX_MAX_RETRIES`            | **5** (`fly.toml`)  | 3       | Transaction retry attempts on retryable errors                          |
| `DB_TX_BACKOFF_BASE_MS`        | **200ms** (unset)   | 200ms   | Initial TX retry backoff                                                |
| `DB_TX_BACKOFF_MAX_MS`         | **1500ms** (unset)  | 1500ms  | Max TX retry backoff                                                    |

### Adaptive pressure controller

**Source:** `internal/db/pressure.go`

Automatically reduces the semaphore soft limit when Supabase is under load and
restores it when pressure eases. Signal is `exec_total` per transaction — the
cumulative time spent actually executing DB queries (not semaphore wait time).
Slow queries indicate Supabase is overloaded; fast queries indicate headroom.

| Env var / constant          | Default | What it controls                                                           |
| --------------------------- | ------- | -------------------------------------------------------------------------- |
| `GNH_PRESSURE_HIGH_MARK_MS` | 500ms   | EMA above this triggers a reduction (step −10, floor 10, every 10s)        |
| `GNH_PRESSURE_LOW_MARK_MS`  | 100ms   | EMA below this triggers restoration (step +3, ceiling hard cap, every 30s) |
| `pressureEMAAlpha`          | 0.15    | Smoothing factor — lower = slower to react, more stable                    |
| `pressureInitialLimit`      | 55      | Starting soft limit — conservative to protect DB on restart under load     |
| `pressureWarmupSamples`     | 5       | Observations required before the controller acts                           |

Deadband: EMA between 100ms and 500ms → limit holds steady. If
`GNH_PRESSURE_LOW_MARK_MS >= GNH_PRESSURE_HIGH_MARK_MS` the controller logs a
warning and falls back to defaults.

Typical lifecycle under load: limit 55 → 45 → 35 … → 10 (floor), then recovers 3
slots every 30s as pool wait drops back below 100ms.

---

## Worker pool base size

**Source:** `cmd/app/main.go`

| Env var / constant   | Production value              | Default                    | What it controls                                     |
| -------------------- | ----------------------------- | -------------------------- | ---------------------------------------------------- |
| —                    | **30** hardcoded by `APP_ENV` | prod=30, staging=10, dev=5 | Base worker count — no env var; requires code change |
| `WORKER_CONCURRENCY` | **20** (`fly.toml`)           | 1                          | Tasks per worker goroutine (range 1–20)              |

Total base capacity = `base workers × WORKER_CONCURRENCY` = 30 × 20 = **600
concurrent task slots**.

---

## Worker pool scaling

**Source:** `internal/jobs/worker.go`

The pool scales dynamically between base and max based on active job
concurrency. Scale target formula:
`ceil(totalJobConcurrency / WORKER_CONCURRENCY × 1.1)`, capped at
`wp.maxWorkers` (derived from `GNH_MAX_WORKERS`, or the staging hard cap when
`APP_ENV=staging`). Each job's effective concurrency is reduced by the domain
limiter when adaptive delays are active.

| Env var / constant                  | Production value      | Default           | What it controls                                                    |
| ----------------------------------- | --------------------- | ----------------- | ------------------------------------------------------------------- |
| `GNH_MAX_WORKERS`                   | **130** (`fly.toml`)  | 160 (staging: 10) | Max workers ceiling; staging env always uses 10                     |
| `GNH_WORKER_SCALE_COOLDOWN_SECONDS` | **120s** (`fly.toml`) | 15s               | Minimum time between scale decisions                                |
| `GNH_WORKER_IDLE_THRESHOLD`         | **10** (`fly.toml`)   | 0                 | Idle worker count before scale-down; 0 = disabled                   |
| `GNH_HEALTH_PROBE_INTERVAL_SECONDS` | **30s** (`fly.toml`)  | 0                 | Health probe interval (min 10s); 0 = disabled                       |
| `GNH_JOB_FAILURE_THRESHOLD`         | **20** (unset)        | 20                | Consecutive task failures before a job is marked permanently failed |

Example: 100 active jobs at concurrency=20 each → target = ceil(2000/20×1.1) =
110 workers.

---

## Task monitor and promotion

**Source:** `internal/jobs/worker.go` — `StartTaskMonitor`,
`StartQuotaPromotionMonitor`

Two background loops keep the pending task supply full. The quota promotion
monitor is the primary throughput driver for jobs with waiting tasks.

| Env var / constant                     | Production value     | Default | What it controls                                                                |
| -------------------------------------- | -------------------- | ------- | ------------------------------------------------------------------------------- |
| `GNH_TASK_MONITOR_INTERVAL_SECONDS`    | **10s** (`fly.toml`) | 10s     | Polls for jobs with `pending_tasks > 0`; adds newly-ready jobs to the work pool |
| `GNH_QUOTA_PROMOTION_INTERVAL_SECONDS` | **5s** (`fly.toml`)  | 5s      | Promotes `waiting` tasks to `pending` per job up to its concurrency limit       |
| `pendingRebalanceInterval`             | hardcoded            | 5 min   | Demotes excess pending tasks back to waiting to enforce concurrency limits      |
| `pendingRebalanceJobLimit`             | hardcoded            | 25      | Max jobs processed per rebalance sweep                                          |
| `pendingUnlimitedCap`                  | hardcoded            | 100     | Max pending+running tasks for jobs with no explicit concurrency set             |
| `fallbackJobConcurrency`               | hardcoded            | 20      | Concurrency assumed when job has no cached info yet                             |

The promotion interval (5s) is the primary throughput ceiling for fast-domain
jobs: a job with `concurrency=20` can complete at most
`20 tasks / 5s = 4 tasks/sec` regardless of how many workers are available.

---

## Running task tracking

**Source:** `internal/jobs/worker.go`

Controls how the `running_tasks` counter is batched and flushed to the DB.

| Env var / constant                   | Production value       | Default | What it controls                                         |
| ------------------------------------ | ---------------------- | ------- | -------------------------------------------------------- |
| `GNH_RUNNING_TASK_BATCH_SIZE`        | **32** (`fly.toml`)    | 4       | Tasks batched per `running_tasks` flush                  |
| `GNH_RUNNING_TASK_FLUSH_INTERVAL_MS` | **200ms** (`fly.toml`) | 50ms    | Max interval before flushing the `running_tasks` counter |

---

## Batch write channel

**Source:** `internal/db/batch.go`

Buffers task status updates to avoid a DB write on every individual task
completion.

| Env var / constant          | Production value      | Default | What it controls                                               |
| --------------------------- | --------------------- | ------- | -------------------------------------------------------------- |
| `GNH_BATCH_CHANNEL_SIZE`    | **5000** (`fly.toml`) | 2000    | Channel buffer depth (range 500–20,000)                        |
| `GNH_BATCH_MAX_INTERVAL_MS` | **2000ms** (unset)    | 2000ms  | Max time before forcing a flush (range 100–10,000ms)           |
| `MaxBatchSize`              | hardcoded             | 100     | Max tasks per batch write                                      |
| `MaxConsecutiveFailures`    | hardcoded             | 3       | Consecutive failures before falling back to individual updates |

---

## HTML persistence

**Source:** `internal/jobs/worker.go`

Async pool for uploading raw HTML to Supabase Storage after task completion.

| Env var / constant            | Production value      | Default | What it controls                        |
| ----------------------------- | --------------------- | ------- | --------------------------------------- |
| `GNH_HTML_PERSIST_WORKERS`    | **32** (`fly.toml`)   | 8       | Goroutines uploading HTML concurrently  |
| `GNH_HTML_PERSIST_QUEUE_SIZE` | **2048** (`fly.toml`) | 64      | Channel buffer for pending HTML uploads |

---

## Domain rate limiter

**Source:** `internal/jobs/domain_limiter.go`

Adaptive per-domain delay that backs off when a site returns rate-limit signals
and recovers after sustained successes. Env var overrides apply at startup only.

| Env var / constant                 | Production value  | Default | What it controls                                        |
| ---------------------------------- | ----------------- | ------- | ------------------------------------------------------- |
| `GNH_RATE_LIMIT_BASE_DELAY_MS`     | **500ms** (unset) | 500ms   | Minimum delay between requests to a domain              |
| `GNH_RATE_LIMIT_DELAY_STEP_MS`     | **500ms** (unset) | 500ms   | Delay increment per rate-limit hit                      |
| `GNH_RATE_LIMIT_MAX_DELAY_SECONDS` | **60s** (unset)   | 60s     | Ceiling for adaptive delay                              |
| `GNH_RATE_LIMIT_SUCCESS_THRESHOLD` | **5** (unset)     | 5       | Consecutive successes before attempting delay reduction |
| `GNH_ROBOTS_DELAY_MULTIPLIER`      | **0.5** (unset)   | 0.5     | Scale factor applied to `Crawl-Delay` from robots.txt   |

The domain limiter also reduces a job's effective concurrency fed into
`calculateConcurrencyTarget()`, so heavily rate-limited jobs do not inflate the
worker count.

---

## Cold-storage archival

**Source:** `internal/archive/archive.go`, `fly.toml`

| Env var / constant       | Production value     | Default | What it controls                                                          |
| ------------------------ | -------------------- | ------- | ------------------------------------------------------------------------- |
| `ARCHIVE_PROVIDER`       | **r2** (`fly.toml`)  | —       | Storage backend (`r2` or `s3`)                                            |
| `ARCHIVE_BUCKET`         | (`fly.toml`)         | —       | Bucket name for archived HTML                                             |
| `ARCHIVE_RETENTION_JOBS` | **3** (`fly.toml`)   | —       | Last N terminal jobs (completed/failed/cancelled) kept hot per domain/org |
| `ARCHIVE_INTERVAL`       | **1m** (`fly.toml`)  | —       | Sweep frequency                                                           |
| `ARCHIVE_BATCH_SIZE`     | **100** (`fly.toml`) | —       | Archive candidates processed per sweep                                    |
| `ARCHIVE_CONCURRENCY`    | **5** (`fly.toml`)   | —       | Parallel R2 uploads per sweep                                             |

---

## Scheduler and health loops

**Source:** `cmd/app/main.go`

Background loops that run independently of the worker pool.

| Constant                  | Value | What it controls                                               |
| ------------------------- | ----- | -------------------------------------------------------------- |
| `schedulerTickInterval`   | 30s   | How often the job scheduler checks for schedulers ready to run |
| `schedulerBatchSize`      | 50    | Max schedulers fetched per tick                                |
| `completionCheckInterval` | 30s   | How often completed jobs are detected and finalised            |
| `healthCheckInterval`     | 5 min | How often stale job health is checked                          |
