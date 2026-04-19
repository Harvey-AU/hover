# Redis Broker Cutover Plan

Status: draft. Target: PR #330 (`feature/redis-broker-v2`). Last reviewed:
2026-04-19.

## Context

Stage 3 of the Redis-broker migration replaces the DB-polling worker
(`internal/jobs/worker.go`, ~5kloc, deleted in this PR) with a Redis ZSET +
Stream dispatcher living in `internal/broker/` and a dedicated worker binary
(`cmd/worker`).

Redis is currently _optional_ in the API server — if `REDIS_URL` is unset,
dispatch is disabled and tasks stay in Postgres. This is the rollback lever we
lean on for a safe cutover.

## Pre-cutover checklist

- [ ] CI green on PR #330 (Deploy Review App + all tests).
- [ ] Review app smoke-tested end-to-end: job create → ZSET → stream → worker →
      task completion.
- [ ] Load test: 10 concurrent jobs × 500 URLs each. Watch ZSET depth, PEL
      depth, dispatcher tick latency, dead-letter count.
- [ ] Observability gaps P0 closed (see list below).
- [ ] Runbook (`docs/operations/REDIS_BROKER_RUNBOOK.md`) reviewed.
- [ ] Upstash Redis provisioned in prod (syd region, disable-eviction, no
      ProdPack — see `.github/workflows/review-apps.yml` for the create
      command).

## Staged rollout

### Stage A — Merge with Redis OFF in prod (Day 0)

1. Merge PR #330 to `main`.
2. Deploy `hover-prod` API server with **no** `REDIS_URL` set. Dispatch is
   disabled; nothing moves. Existing jobs carry on via the legacy path. (Note:
   Stage 3 _deletes_ the legacy worker pool. If we truly want zero dispatch, we
   need to confirm the API server's behaviour when `OnTasksEnqueued` is nil and
   no worker is running. Verify before merge.)
3. Deploy worker binary to `hover-worker-prod` but keep it at 0 machines.

This is the "dark launch" — code in prod, nothing running.

### Stage B — Canary (Day 1)

1. Scale `hover-worker-prod` to 1 machine.
2. Set `REDIS_URL` on `hover-prod` API server.
3. Create one low-priority test job via the dashboard.
4. Watch in order:
   - `ZCARD sched:{jobID}` drops as dispatcher works.
   - `XLEN stream:{jobID}` fluctuates as worker consumes.
   - `GET running:{jobID}` stays under concurrency limit.
   - Sentry is quiet; Grafana shows no anomalies.
5. Let the job complete. Verify task count matches expectations.

### Stage C — Gradual ramp (Day 2-7)

1. Scale worker to 2-3 machines.
2. Un-gate job creation for real customers (if gated).
3. Monitor daily:
   - Dead-letter counter (should stay near zero).
   - XAUTOCLAIM reclaim rate (spikes = workers crashing).
   - Redis memory usage (should be bounded — we never TRIM the stream but
     messages are ACKed out of the PEL).
4. Tune `STREAM_ACTIVE_JOBS_LIMIT`, `REDIS_DISPATCH_BATCH_SIZE`,
   `REDIS_DISPATCH_INTERVAL_MS` if dispatcher tick latency or stream PEL depth
   trends up.

### Stage D — Remove fallback (Week 2+)

Only after a full week of clean operation:

1. Delete the "Redis optional" code paths in `internal/api/` and
   `internal/jobs/manager.go`.
2. Make `REDIS_URL` required at startup.
3. Delete any remaining DB-polling helpers (`GetNextTask` etc. are already gone
   in this PR, but double-check).

## Rollback paths

| Problem                     | Rollback                                                                                                                                                                                                                                       |
| --------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Worker crashes in a loop    | Scale `hover-worker-prod` to 0. Jobs stall, no loss.                                                                                                                                                                                           |
| Dispatcher not dispatching  | Unset `REDIS_URL` on API server. No new scheduling.                                                                                                                                                                                            |
| Redis data corrupted / lost | `FLUSHALL` (or recreate instance). Tasks re-sync from Postgres via existing re-enqueue paths.                                                                                                                                                  |
| Performance regression      | Revert the `main` merge — full legacy path is still in git history up to the commit prior to the worker deletion. **Note: legacy worker code is fully deleted in Stage 3**, so actual revert is a code rollback + redeploy, not a config flip. |

## Observability gaps to close before Stage D

Sourced from the audit in this session.

**P0 (before Stage B):**

- Dead-letter counter (`bee.broker.deadletter_total` per job).
- Dispatcher tick duration histogram + tasks-dispatched-per-tick counter.
- Sentry panic handlers on the three long-running goroutines (dispatcher, stream
  consumer, XAUTOCLAIM loop).

**P1 (before Stage C):**

- ZSET depth gauge + Stream PEL depth gauge per active job.
- Domain pacer acquire hit/miss rate.
- XAUTOCLAIM reclaim counter.

**P2 (before Stage D):**

- Running-counter drift detector (compare `running:{jobID}` vs `XPENDING` count;
  alert on delta > 5 for 10 minutes).
- Redis pool stats (connection errors, timeouts, pool exhaustion).
- Pacer RetryAfter distribution histogram.
- Pacer inflight gauge per domain.

## Known architectural deferrals

Surfaced during review, intentionally out of scope for Stage 3:

1. **Outbox pattern for `OnTasksEnqueued`.** Callback errors are
   logged-and-swallowed today. If Postgres commit succeeds but the Redis
   schedule call fails, the task is orphaned. Proper fix: write an `outbox` row
   in the same Postgres tx, have a sweeper mirror pending outbox rows to Redis.

2. **`RunAt` persistence for waiting tasks.** Reschedule lives only in Redis. If
   Redis is flushed, adaptive-delay pushbacks are lost and tasks either replay
   immediately or never.

Both are tracked for a follow-up PR. Neither blocks Stage 3 because: (a) outbox
failure is extremely rare (Redis call happens immediately after commit, in the
same request), and (b) Redis flush is an operational incident that requires full
re-sync anyway.
