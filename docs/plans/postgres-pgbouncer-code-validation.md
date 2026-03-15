# Postgres and PgBouncer Code Validation

**Date**: 2026-03-15 **Status**: Analysis only **Source**:
https://medium.com/@Nexumo_/7-postgres-pool-fixes-for-sudden-traffic-spikes-f54d149d1036

## Summary

This document validates the Postgres and PgBouncer concepts raised from the
source article against the actual Adapt codebase and committed configuration. It
checks whether each concept is concretely present, whether it would be
beneficial here, and what practical impact or improvement would be expected if
applied. The conclusions are based on code and config paths only, not repo docs
or roadmap material.

## Scope

Reviewed files:

- `supabase/config.toml`
- `internal/db/db.go`
- `internal/db/queue.go`
- `cmd/app/main.go`
- `internal/notifications/listener.go`
- `.github/workflows/review-apps.yml`

Excluded from conclusions:

- repo docs
- changelog entries
- roadmap items

## Current concrete implementation

- Local Supabase config defines a pooler block with `pool_mode = "transaction"`,
  `default_pool_size = 20`, and `max_client_conn = 100`, but the local pooler is
  disabled via `enabled = false` (`supabase/config.toml:34`).
- App database pools are capped in code by environment (`internal/db/db.go:47`):
  - production: `MaxOpenConns = 70`, `MaxIdleConns = 20`
  - staging: `MaxOpenConns = 5`, `MaxIdleConns = 2`
  - default/dev: `MaxOpenConns = 2`, `MaxIdleConns = 1`
- `DB_MAX_OPEN_CONNS` and `DB_MAX_IDLE_CONNS` can override those defaults.
- Pooler connections are detected by host matching and adjusted for PgBouncer
  compatibility (`internal/db/db.go:252`) by appending:
  - `default_query_exec_mode=simple_protocol`
  - `pgbouncer=true` (on `DATABASE_URL` initialisation path)
- Queue traffic can use a separate database URL via `DATABASE_QUEUE_URL`
  (`cmd/app/main.go:543`).
- Queue execution reserves part of the pool via `DB_POOL_RESERVED_CONNECTIONS`
  (`internal/db/queue.go:144`) and uses that reserve when calculating allowed
  queue concurrency.
- LISTEN/NOTIFY can bypass the pooler via `DATABASE_DIRECT_URL`
  (`internal/notifications/listener.go:117`).
- Preview environments wire both pooled and direct URLs separately
  (`.github/workflows/review-apps.yml:69-73`).

### Key gap

The app has its own pool limits per lane, but there is no committed end-to-end
budget proving those limits fit safely under the real hosted Postgres
`max_connections` after accounting for all concurrent consumers:

- web traffic
- queue traffic
- direct LISTEN/NOTIFY connections
- preview/review environments
- admin/manual sessions
- migrations and monitoring

---

## 1. Explicit PgBouncer backend caps (`max_db_connections` / `max_user_connections`)

### Present?

No.

Evidence:

- `supabase/config.toml` sets only `pool_mode`, `default_pool_size`, and
  `max_client_conn`.
- No committed file contains `max_db_connections` or `max_user_connections`.

### Beneficial?

Yes, if the hosted pooler exposes equivalent controls. Supavisor (Supabase's
pooler) does not expose a direct `max_db_connections` parameter in
`supabase/config.toml`, so this would need to be enforced at the app level or
via Supabase dashboard settings.

### Expected impact

- Prevents the pooler from pushing too many backend connections into Postgres.
- Makes overload manifest as queueing/backpressure instead of backend
  exhaustion.
- Reduces risk of bursty workers or multiple app instances stampeding the
  database.

### Notes

With `MaxOpenConns = 70` in production plus optional separate queue traffic,
backend safety caps are more valuable here, not less.

---

## 2. Direct-connection reserve budget on the hosted Postgres side

### Present?

No, not concretely enforced in committed config.

Evidence:

- `DB_POOL_RESERVED_CONNECTIONS` in `internal/db/queue.go:144` reserves capacity
  inside the app's own queue model only — it does not guarantee headroom at the
  real Postgres level.
- `DATABASE_DIRECT_URL` is wired for LISTEN/NOTIFY
  (`internal/notifications/listener.go:117`) but no repo-managed config
  allocates a fixed Postgres-side reserve for direct/admin/emergency access.

### Beneficial?

Yes.

### Expected impact

- Preserves emergency access when pooled traffic spikes.
- Reduces the risk that LISTEN/NOTIFY or admin tasks fail because every real
  server slot is consumed.
- Improves operational recovery during incidents, deploys, and migrations.

### Notes

Because the repo already uses a direct connection lane, explicit real-Postgres
headroom is a practical requirement rather than a theoretical best practice.

---

## 3. End-to-end pool-budget formula tying app limits to real `max_connections`

### Present?

Partially.

Evidence:

- App-side caps exist in `internal/db/db.go:47`.
- Queue isolation exists optionally in `cmd/app/main.go:543`.
- Queue reserve logic exists in `internal/db/queue.go:144`.
- No concrete formula or enforced calculation sums all services and traffic
  classes against the real database limit.

### Beneficial?

Yes.

### Expected impact

- Prevents accidental oversubscription across multiple lanes.
- Makes environment sizing and incident response more predictable.
- Lets worker counts, review apps, or queue routing be changed safely without
  guessing.

### Notes

The code contains sensible per-component safeguards, but they are local. They do
not guarantee the deployed topology as a whole stays inside the real database
budget.

---

## 4. Explicit workload isolation for queue traffic

### Present?

Partially.

Evidence:

- `cmd/app/main.go:543` supports `DATABASE_QUEUE_URL` and creates a separate
  `queueDB` when set.
- When `DATABASE_QUEUE_URL` is absent, queue traffic falls back to the main
  pool.
- No committed environment standard guarantees separate lanes in every
  environment.

### Beneficial?

Yes, especially if worker bursts are material.

### Expected impact

- Protects latency-sensitive web paths from queue bursts.
- Reduces contention between transactional job-claim traffic and user-facing
  queries.
- Makes queue saturation less likely to degrade the whole application.

### Notes

The code is already prepared for this pattern. The improvement comes from
enforcing it consistently rather than implementing a new design.

---

## 5. Replica routing for read-heavy workloads

### Present?

No.

Evidence:

- No committed config or code path references a read-replica URL, read-only
  pool, or replica router.
- No alternate reader connection path exists in the database initialisation
  flow.

### Beneficial?

Conditionally.

### Expected impact

- Useful if dashboard, analytics, or reporting reads grow large enough to
  compete with writes.
- Can reduce primary load and improve read latency for heavy read endpoints.
- Little benefit if the workload remains write-heavy or moderate in size.

### Notes

The more immediate bottleneck is connection budgeting and traffic isolation, not
replica plumbing.

---

## 6. Multiple PgBouncer entry points for major traffic classes

### Present?

Partially.

Evidence:

- The codebase distinguishes three connection lanes:
  - `DATABASE_URL` — normal web traffic
  - `DATABASE_QUEUE_URL` — queue/worker traffic (`cmd/app/main.go:543`)
  - `DATABASE_DIRECT_URL` — direct/session-bound traffic
    (`internal/notifications/listener.go:117`)
- No committed topology proves these are always backed by distinct pooler
  endpoints or distinct pool configurations.

### Beneficial?

Yes, if traffic classes interfere with each other.

### Expected impact

- Better fairness between bursty workers and latency-sensitive user traffic.
- Better control of pool size, queue depth, and timeout policy per traffic
  class.
- Lower risk that one noisy class consumes all pooled capacity.

### Notes

The code shape already points in this direction. The missing piece is explicit
infrastructure and environment enforcement.

---

## 7. Operational rule for features that must bypass transaction pooling

### Present?

Partially — implemented in code, but not formalised as a rule.

Evidence:

- `internal/notifications/listener.go:116` explicitly bypasses the pooler for
  LISTEN/NOTIFY using `DATABASE_DIRECT_URL`.
- `internal/db/db.go:252` disables prepared-statement behaviour on pooler
  connections by switching to simple protocol.
- `internal/db/queue.go:721` uses `SET LOCAL statement_timeout` inside
  transactions, which is transaction-scoped and compatible with transaction
  pooling.
- No committed rule covers all session-bound features: session-level SET/RESET,
  temp tables, advisory locks held across transactions, cursors, or SQL PREPARE.

### Beneficial?

Yes.

### Expected impact

- Prevents future regressions when new features accidentally rely on session
  affinity.
- Makes transaction pooling safe to expand because the exceptions are explicit.
- Reduces debugging time for subtle pooler-only production failures.

### Notes

This is high value and low effort: the repo already contains one real exception
(`DATABASE_DIRECT_URL` for LISTEN/NOTIFY) and one real compatibility workaround
(simple protocol for prepared statements).

---

## 8. Enabling the local Supabase pooler to mirror production behaviour

### Present?

No — local pooler is disabled.

Evidence:

- `supabase/config.toml:34` sets `enabled = false`.

### Beneficial?

Yes, for production-parity testing.

### Expected impact

- Catches transaction-pooling incompatibilities earlier in development.
- Makes local and CI behaviour closer to hosted pooled environments.
- Surfaces issues around LISTEN/NOTIFY, prepared statements, and session-bound
  assumptions before deploy.

### Notes

Enabling adds local complexity and may make simple development flows noisier.
Worth enabling when actively developing features that interact with the
connection pool or session state. Can remain off otherwise.

---

## Priority assessment

### Highest value

1. Formalise which features must bypass transaction pooling (concept 7) — high
   value, low effort, the groundwork already exists.
2. Define a real hosted-Postgres reserve budget for direct/admin/emergency
   access (concept 2).
3. Add an end-to-end pool budget covering all lanes and environments (concept
   3).
4. Add explicit backend safety caps at the pooler level if the hosted pooler
   supports them (concept 1).

### Medium value

5. Enforce queue isolation consistently per environment (concept 4) — code is
   ready, needs environment enforcement.
6. Use multiple pooler entry points if web and worker traffic demonstrably
   interfere (concept 6).
7. Enable local pooler parity for earlier detection of pooler-only issues
   (concept 8).

### Lower priority unless workload changes

8. Add replica routing when read traffic starts competing materially with writes
   (concept 5).

---

## Overall conclusion

From concrete code and config review only:

- Several concepts are partially present: app-side pool caps, optional queue
  isolation, queue reserve logic, direct-connection bypass for LISTEN/NOTIFY,
  and prepared-statement compatibility handling.
- The biggest gaps are not inside the Go code. They are the absence of an
  enforced end-to-end capacity budget and the lack of explicit infrastructure
  guarantees for each connection lane.
- The most likely improvements from applying these concepts are resilience and
  predictability under burst load, not dramatic steady-state speed gains.
