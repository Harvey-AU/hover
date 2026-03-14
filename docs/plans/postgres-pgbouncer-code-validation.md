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

This review is based on concrete code and config only.

Included:

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

### What exists today

- Local Supabase config defines a pooler block with `pool_mode = "transaction"`,
  `default_pool_size = 20`, and `max_client_conn = 100`, but local pooler is
  disabled with `enabled = false` in `supabase/config.toml`.
- App database pools are capped in code by environment in `internal/db/db.go`:
  - production: `MaxOpenConns = 70`, `MaxIdleConns = 20`
  - staging: `MaxOpenConns = 5`, `MaxIdleConns = 2`
  - default/dev: `MaxOpenConns = 2`, `MaxIdleConns = 1`
- `DB_MAX_OPEN_CONNS` and `DB_MAX_IDLE_CONNS` can override those defaults in
  `internal/db/db.go`.
- Pooler connections are detected by host matching and adjusted for PgBouncer
  compatibility in `internal/db/db.go` by adding:
  - `default_query_exec_mode=simple_protocol`
  - `pgbouncer=true` when using `DATABASE_URL` initialisation
- Queue traffic can use a separate database URL via `DATABASE_QUEUE_URL` in
  `cmd/app/main.go`.
- Queue execution also reserves part of the pool through
  `DB_POOL_RESERVED_CONNECTIONS` in `internal/db/queue.go` and uses that reserve
  when calculating allowed queue concurrency.
- LISTEN/NOTIFY can bypass the pooler via `DATABASE_DIRECT_URL` in
  `internal/notifications/listener.go`.
- Preview environments also wire both pooled and direct URLs separately in
  `.github/workflows/review-apps.yml`.

### Important gap in current implementation

The app has its own pool limits, but there is no committed end-to-end budget
that proves those limits fit safely under the real hosted Postgres
`max_connections` after accounting for:

- web traffic
- queue traffic
- direct LISTEN/NOTIFY connections
- preview/review environments
- admin/manual sessions
- migrations and monitoring

## Validation by concept

## 1. Explicit PgBouncer backend caps with `max_db_connections` and/or `max_user_connections`

### Present?

No in committed config.

Evidence:

- `supabase/config.toml` only sets `pool_mode`, `default_pool_size`, and
  `max_client_conn`.
- No committed file contains `max_db_connections` or `max_user_connections`.

### Beneficial?

Yes, if the hosted pooler exposes equivalent controls.

### Expected impact

- Prevents the pooler from pushing too many backend connections into Postgres.
- Makes overload fail as queueing/backpressure instead of backend exhaustion.
- Reduces risk of bursty workers or many app instances stampeding the database.

### Notes for this repo

This app already allows fairly high logical concurrency in production through
`MaxOpenConns = 70` plus optional separate queue traffic. That makes backend
safety caps more valuable, not less.

## 2. Direct-connection reserve budget on the hosted Postgres side

### Present?

Not concretely enforced in committed config.

Evidence:

- There is queue-side reserve logic via `DB_POOL_RESERVED_CONNECTIONS` in
  `internal/db/queue.go`, but that only protects capacity inside the app's own
  usage model.
- `DATABASE_DIRECT_URL` is supported for direct session features in
  `internal/notifications/listener.go`, but no repo-managed config allocates a
  fixed hosted-Postgres reserve budget for direct/admin/emergency access.

### Beneficial?

Yes.

### Expected impact

- Preserves emergency access when pooled traffic spikes.
- Reduces risk that LISTEN/NOTIFY or admin tasks fail because every real server
  slot is already consumed.
- Improves operational recovery during incidents, deploys, and migrations.

### Notes for this repo

Because this repo already uses a direct connection lane, leaving explicit
real-Postgres headroom is a practical requirement rather than theoretical best
practice.

## 3. End-to-end pool-budget formula tying app limits to real `max_connections`

### Present?

Partially.

Evidence:

- App-side caps exist in `internal/db/db.go`.
- Queue isolation exists optionally in `cmd/app/main.go`.
- Queue reserve logic exists in `internal/db/queue.go`.
- What is missing is a concrete formula or enforced calculation that sums all
  services and traffic classes against the real database limit.

### Beneficial?

Yes.

### Expected impact

- Prevents accidental oversubscription across multiple lanes.
- Makes environment sizing and incident response much more predictable.
- Lets you safely change worker counts, review apps, or queue routing without
  guessing.

### Notes for this repo

Right now the code contains sensible local safeguards, but they are local
safeguards. They do not guarantee that the whole deployed topology remains
inside the real database budget.

## 4. More explicit workload isolation for queue traffic

### Present?

Partially.

Evidence:

- `cmd/app/main.go` supports `DATABASE_QUEUE_URL` and creates a separate
  `queueDB` when it is set.
- If `DATABASE_QUEUE_URL` is not set, queue traffic falls back to the main pool.
- There is no committed environment standard that guarantees separate lanes in
  every environment.

### Beneficial?

Yes, especially if worker bursts are material.

### Expected impact

- Protects latency-sensitive web paths from queue bursts.
- Reduces contention between transactional job-claim traffic and user-facing
  queries.
- Makes queue failures or saturation less likely to degrade the whole app.

### Notes for this repo

The code is already prepared for this pattern, so the likely improvement comes
from enforcing it consistently rather than inventing a new design.

## 5. Replica routing for read-heavy workloads

### Present?

No.

Evidence:

- No committed config or code path references a read-replica URL, read-only
  pool, or replica router.
- No alternate reader connection path was found in the database initialisation
  flow.

### Beneficial?

Conditionally.

### Expected impact

- Useful if dashboard, analytics, or reporting reads become large enough to
  compete with writes.
- Can reduce primary load and improve read latency for heavy read endpoints.
- Little benefit if the workload remains write-heavy or moderate in size.

### Notes for this repo

Based on concrete code, the more immediate bottleneck to control is connection
budgeting and traffic isolation, not replica plumbing.

## 6. Multiple PgBouncer entry points for major traffic classes

### Present?

Partially.

Evidence:

- The repo already distinguishes three lanes conceptually:
  - `DATABASE_URL` for normal traffic
  - `DATABASE_QUEUE_URL` for queue traffic
  - `DATABASE_DIRECT_URL` for direct/session-bound traffic
- However, there is no committed topology proving these are always backed by
  distinct pooler endpoints or distinct pool configurations.

### Beneficial?

Yes, if traffic classes interfere with each other.

### Expected impact

- Better fairness between bursty workers and latency-sensitive user traffic.
- Better control of pool size, queue depth, and timeout policy per traffic
  class.
- Lower risk that one noisy class consumes all pooled capacity.

### Notes for this repo

The code shape already points in this direction. The missing piece is explicit
infrastructure and environment enforcement.

## 7. Operational rule for features that must bypass transaction pooling

### Present?

Partially in code, not as a formalised rule.

Evidence:

- `internal/notifications/listener.go` explicitly bypasses the pooler for
  LISTEN/NOTIFY using `DATABASE_DIRECT_URL`.
- `internal/db/db.go` disables prepared-statement behaviour on pooler
  connections using simple protocol for compatibility.
- `internal/db/queue.go` uses `SET LOCAL statement_timeout` inside transactions,
  which is transaction-scoped and generally compatible.
- I did not find a committed rule covering all session-bound features such as
  LISTEN/NOTIFY, session-level SET/RESET usage, temp tables, advisory locks held
  across transactions, cursors, or SQL PREPARE.

### Beneficial?

Yes.

### Expected impact

- Prevents future regressions when new features accidentally rely on session
  affinity.
- Makes transaction pooling safe to expand because the exceptions are explicit.
- Reduces debugging time for subtle pooler-only production failures.

### Notes for this repo

This is high value and low effort because the repo already contains one real
exception and one real compatibility workaround.

## 8. Enabling the local Supabase pooler to mirror production behaviour

### Present?

No, local pooler is disabled.

Evidence:

- `supabase/config.toml` sets `[db.pooler] enabled = false`.

### Beneficial?

Probably yes for parity testing.

### Expected impact

- Catches transaction-pooling incompatibilities earlier in development.
- Makes local and CI behaviour closer to hosted pooled environments.
- Surfaces mistakes around LISTEN/NOTIFY, prepared statements, and session-bound
  assumptions before deploy.

### Trade-off

- Adds local complexity and may make simple development flows noisier.
- Best used when the team values production-parity checks more than the lightest
  local setup.

## Priority assessment based on concrete code

### Highest value

1. Add explicit backend safety caps if the hosted pooler supports them.
2. Define a real hosted-Postgres reserve budget for direct/admin/emergency
   access.
3. Add an end-to-end pool budget covering all lanes and environments.
4. Formalise which features must bypass transaction pooling.

### Medium value

5. Enforce queue isolation consistently per environment.
6. Use multiple pooler entry points only if web and worker traffic demonstrably
   interfere.
7. Enable local pooler parity when you want earlier detection of pooler-only
   issues.

### Lower priority unless workload changes

8. Add replica routing only when read traffic starts competing materially with
   writes.

## Overall conclusion

From concrete code and config review only:

- Some of the requested concepts are already partially present in code: app-side
  caps, optional queue isolation, queue reserve logic, and direct-connection
  bypass for LISTEN/NOTIFY.
- The biggest missing pieces are not inside the Go code itself. They are the
  lack of enforced, end-to-end capacity budgeting and the lack of explicit
  infrastructure-level guarantees for each connection lane.
- The most likely improvements from applying these concepts are resilience and
  predictability under burst load, rather than dramatic steady-state speed
  gains.
