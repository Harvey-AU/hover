# Configuration Reference

Last reviewed: 2026-04-07

Every configurable dial in the application — env vars, hardcoded constants, and
their relationships. Values reflect production unless noted. For the flat
inventory of env vars and their classification (secret vs non-secret), see
[`docs/operations/ENV_VARS.md`](../operations/ENV_VARS.md).

---

## Key relationships

```
DB_MAX_OPEN_CONNS (50)
  └── DB_POOL_RESERVED_CONNECTIONS (4)  →  available = 46
        └── DB_QUEUE_MAX_CONCURRENCY (30)  →  semaphore = min(30, 46) = 30
              └── base workers (30) × WORKER_CONCURRENCY (1) = 30 task slots
                    └── max workers (40) — capped below the 50-conn pool budget
```

Direct DB calls (page writes, domain lookups, etc.) bypass the queue semaphore
and draw from the shared pool. With 30 semaphore slots out of 46 available, 16
connections remain for non-semaphored direct calls.

---

## DB connection pool

**Source:** `internal/db/db.go`, `fly.toml`

| Env var / constant       | Production value    | Code default | What it controls                           |
| ------------------------ | ------------------- | ------------ | ------------------------------------------ |
| `DB_MAX_OPEN_CONNS`      | **50** (`fly.toml`) | 70           | Hard cap on open connections to pgBouncer  |
| `DB_MAX_IDLE_CONNS`      | **15** (`fly.toml`) | 20           | Idle connections kept warm                 |
| `defaultConnMaxLifetime` | hardcoded           | 5 min        | Max connection lifetime                    |
| `defaultConnMaxIdleTime` | hardcoded           | 2 min        | Idle connection eviction                   |
| `statementTimeoutMs`     | hardcoded           | 60s          | Per-statement timeout (added to DSN)       |
| `idleInTxnTimeoutMs`     | hardcoded           | 30s          | Idle-in-transaction timeout (added to DSN) |

Supabase pooler is auto-detected via the `pooler.supabase.com` hostname in
`DATABASE_URL`. When detected, `simple_protocol` and `pgbouncer=true` are
appended to the DSN automatically.

---

## Queue semaphore

**Source:** `internal/db/queue.go`

Wraps a semaphore around all task-claim and batch-update DB operations. Direct
DB calls (page writes, domain lookups, etc.) bypass this gate and draw directly
from the pool.

| Env var / constant             | Production value    | Default | What it controls                                                            |
| ------------------------------ | ------------------- | ------- | --------------------------------------------------------------------------- |
| `DB_QUEUE_MAX_CONCURRENCY`     | **30** (`fly.toml`) | 12      | Semaphore slots for queue ops; effective = `min(this, MAX_OPEN − RESERVED)` |
| `DB_POOL_RESERVED_CONNECTIONS` | **4** (unset)       | 4       | Connections held back from the semaphore budget                             |
| `DB_POOL_WARN_THRESHOLD`       | **0.90** (unset)    | 0.90    | Log warn at 90% pool usage (>=45/50 open)                                   |
| `DB_POOL_REJECT_THRESHOLD`     | **0.95** (unset)    | 0.95    | Fire Sentry "DB pool saturated" at 95% (>=48/50)                            |
| `defaultExecuteTimeout`        | hardcoded           | 30s     | Context timeout for `Execute`/`ExecuteWithContext` when caller has none     |
| `DB_TX_MAX_RETRIES`            | **3** (unset)       | 3       | Transaction retry attempts on retryable errors                              |
| `DB_TX_BACKOFF_BASE_MS`        | **200ms** (unset)   | 200ms   | Initial TX retry backoff                                                    |
| `DB_TX_BACKOFF_MAX_MS`         | **1500ms** (unset)  | 1500ms  | Max TX retry backoff                                                        |

---

## Batch write channel

**Source:** `internal/db/batch.go`

Buffers task status updates to avoid a DB write on every individual task
completion.

| Env var / constant          | Production value      | Default | What it controls                                               |
| --------------------------- | --------------------- | ------- | -------------------------------------------------------------- |
| `GNH_BATCH_CHANNEL_SIZE`    | **5000** (`fly.toml`) | 2000    | Channel buffer depth (range 500-20,000)                        |
| `GNH_BATCH_MAX_INTERVAL_MS` | **2000ms** (unset)    | 2000ms  | Max time before forcing a flush (range 100-10,000ms)           |
| `MaxBatchSize`              | hardcoded             | 100     | Max tasks per batch write                                      |
| `MaxConsecutiveFailures`    | hardcoded             | 3       | Consecutive failures before falling back to individual updates |

---

## Worker pool base size

**Source:** `cmd/app/main.go`

| Env var / constant   | Production value              | Default                    | What it controls                                     |
| -------------------- | ----------------------------- | -------------------------- | ---------------------------------------------------- |
| —                    | **30** hardcoded by `APP_ENV` | prod=30, staging=10, dev=5 | Base worker count — no env var; requires code change |
| `WORKER_CONCURRENCY` | **1** (unset)                 | 1                          | Tasks per worker goroutine (range 1-20)              |

Total base capacity = `base workers x WORKER_CONCURRENCY` = 30 x 1 = **30
concurrent task slots**.

---

## Worker pool scaling

**Source:** `internal/jobs/worker.go`

The pool scales dynamically between base and max based on active job
concurrency. Scale decisions are governed by the settings below.

| Env var / constant                  | Production value         | Default             | What it controls                                                    |
| ----------------------------------- | ------------------------ | ------------------- | ------------------------------------------------------------------- |
| `maxWorkersProduction`              | **40** hardcoded         | prod=40, staging=10 | Max workers ceiling — no env var; requires code change              |
| `GNH_WORKER_SCALE_COOLDOWN_SECONDS` | **15s** (unset)          | 15s                 | Minimum time between scale decisions                                |
| `GNH_WORKER_IDLE_THRESHOLD`         | **0 / disabled** (unset) | 0                   | Idle worker count before scale-down; 0 = disabled                   |
| `GNH_HEALTH_PROBE_INTERVAL_SECONDS` | **0 / disabled** (unset) | 0                   | Health probe interval (min 10s); 0 = disabled                       |
| `GNH_JOB_FAILURE_THRESHOLD`         | **20** (unset)           | 20                  | Consecutive task failures before a job is marked permanently failed |

---

## Running task tracking

**Source:** `internal/jobs/worker.go`

Controls how the `running_tasks` counter is batched and flushed to the DB.

| Env var / constant                   | Production value | Default | What it controls                                         |
| ------------------------------------ | ---------------- | ------- | -------------------------------------------------------- |
| `GNH_RUNNING_TASK_BATCH_SIZE`        | **4** (unset)    | 4       | Tasks batched per `running_tasks` flush                  |
| `GNH_RUNNING_TASK_FLUSH_INTERVAL_MS` | **50ms** (unset) | 50ms    | Max interval before flushing the `running_tasks` counter |

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
