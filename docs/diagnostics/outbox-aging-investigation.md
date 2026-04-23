# Outbox Oldest-Age Growth Investigation

Status: open — investigation only, no code changes in this PR.

## Signal

`bee.broker.outbox_age_seconds` (the age-of-oldest-due-row gauge on
`public.task_outbox`) climbs linearly up to ~2.78 hours during a 3-hour
production run, then sawtooths with spikes to 5–11 hours afterwards.

Sweep throughput is otherwise fine: `task_outbox` row count stays bounded, so
the issue is a subset of rows that fail to drain rather than a volume problem.

## Baseline facts

- Sweep cadence (`fly.worker.toml`): `OUTBOX_SWEEP_INTERVAL_MS=200`,
  `OUTBOX_SWEEP_BATCH_SIZE=500` → 150 k rows/minute headroom.
- Sweep query:
  `SELECT … FROM task_outbox WHERE run_at <= NOW() ORDER BY run_at LIMIT $1 FOR UPDATE SKIP LOCKED`
  ([`internal/broker/outbox.go:120`][outbox-select]).
- On `ScheduleBatch` success: `DELETE` the claimed rows.
- On `ScheduleBatch` failure: `bumpAttempts` sets
  `run_at = NOW() + LEAST(base * 2^attempts, MaxBackoff)`; `MaxBackoff = 5 min`
  ([`internal/broker/outbox.go:209`][outbox-bump]).
- Gauge sampler filters `run_at <= NOW()`, so future-dated retry rows cannot
  inflate it ([`internal/broker/probe.go:120`][probe]).

Because the backoff is capped at 5 minutes, legitimate retry aging cannot
explain an 11-hour oldest-age. Some rows are either (a) repeatedly claimed and
re-bumped, or (b) locked/skipped by SKIP LOCKED across every sweep.

[outbox-select]: ../../internal/broker/outbox.go
[outbox-bump]: ../../internal/broker/outbox.go
[probe]: ../../internal/broker/probe.go

## Lifecycle summary

Writers to `task_outbox`:

| Path                                   | Location                                                             | On-conflict                        |
| -------------------------------------- | -------------------------------------------------------------------- | ---------------------------------- |
| Bulk enqueue (pending + waiting tasks) | `internal/db/queue.go:1251`                                          | `ON CONFLICT (task_id) DO NOTHING` |
| Single-row helper (manual root task)   | `internal/db/outbox.go:29`                                           | none                               |
| Waiting → pending promotion            | `supabase/migrations/20260423000001_promote_waiting_with_outbox.sql` | `ON CONFLICT (task_id) DO NOTHING` |

Deleters:

| Path                                 | Location                        | Trigger                                                            |
| ------------------------------------ | ------------------------------- | ------------------------------------------------------------------ |
| Sweeper success                      | `internal/broker/outbox.go:192` | after `ScheduleBatch` returns nil                                  |
| _(none on cancel)_                   | `internal/jobs/manager.go:654`  | `CancelJob` marks tasks `skipped` but does not touch `task_outbox` |
| _(none on archive / pause / delete)_ | —                               | no job-lifecycle cleanup of `task_outbox` anywhere                 |

Retries do **not** create new outbox rows: `ScheduleAndAck` writes the retry
directly to the Redis ZSET and XACKs the original stream message in a single
`MULTI/EXEC` ([`internal/broker/scheduler.go:169`][sched-and-ack]).

[sched-and-ack]: ../../internal/broker/scheduler.go

## Hypotheses (in priority order)

### H1 — ScheduleBatch partial-failure amplifier (strong)

`Scheduler.ScheduleBatch` collects ZADD results from the pipeline and, if
**any** command returns an error, returns a single aggregate error without
per-entry information ([`internal/broker/scheduler.go:140`][sched-batch]).

The sweeper treats that aggregate as a full-batch failure and calls
`bumpAttempts` on every claimed id, even the ones whose ZADDs actually succeeded
([`internal/broker/outbox.go:182`][outbox-fail]).

Consequence:

- A single flaky ZADD (oversized ZSET, network hiccup, OOM on one shard) pushes
  the other 499 rows forward by 2 s … 5 min.
- On the next tick those 499 rows are claimed again. If the flakiness persists,
  they keep being re-bumped without ever being properly dispatched (they are
  already in the ZSET, but the outbox row survives and will happily re-ZADD them
  next sweep).
- The oldest-age gauge then ticks upward in lockstep with the backoff cap.
- When the flakiness clears, the whole backlog deletes in one sweep — a sawtooth
  drop.

This matches the observed pattern: linear climb during the problem window, sharp
drop when dispatch succeeds, repeats.

[sched-batch]: ../../internal/broker/scheduler.go
[outbox-fail]: ../../internal/broker/outbox.go

### H2 — SKIP LOCKED starvation by a long transaction (possible)

`pg_stat_activity` showed two ~30 s transactions at 17:15 and 19:50 during the
observed window. A single long-running transaction holding an exclusive lock on
the oldest row would cause SKIP LOCKED to repeatedly skip it — the sweeper
always prefers younger rows (`ORDER BY run_at` takes whichever 500 rows it can
lock), so one sticky row at the head can pin the gauge at its age indefinitely.

30 s would not produce an 11-hour reading on its own; but if a supervisor
process (e.g. a migration, an ad-hoc query, an airlocked sweep tx) held a lock
for longer and was not captured in the two samples, that would suffice.

Tested against current prod: `task_outbox` is empty and there are no
long-running transactions right now, so the state that produced the spike has
already cleared. Diagnostic queries below will be the only way to catch it next
time.

### H3 — Orphan rows from cancelled / archived jobs (weak, not root cause)

`CancelJob` does not clean `task_outbox`, so cancelled jobs leave rows behind.
However, those rows still drain on the next sweep: `ScheduleBatch` does not
verify the job exists, the ZADD against the dead ZSET succeeds, and the row is
deleted.

Orphans may contribute transient age but not sustained aging — so this is not
the primary cause. Worth fixing for hygiene once H1/H2 are resolved.

### H4 — Metric definition (ruled out)

Previously listed as a hypothesis. The probe query filters
`WHERE run_at <= NOW()`, so future-dated rows cannot inflate the gauge
([`internal/broker/probe.go:120`][probe]). The 11-hour reading is real.

## Diagnostic queries (run next time the gauge climbs)

Save the output — without a live snapshot we cannot distinguish H1 from H2.

```sql
-- 1. How many rows are due and how old is the oldest?
SELECT count(*)                 AS due_rows,
       min(run_at)              AS oldest_run_at,
       EXTRACT(EPOCH FROM NOW() - min(run_at)) AS oldest_age_sec,
       max(attempts)            AS max_attempts,
       count(*) FILTER (WHERE attempts = 0) AS never_attempted,
       count(*) FILTER (WHERE attempts > 0) AS attempted
FROM task_outbox
WHERE run_at <= NOW();

-- 2. Attempts distribution among due rows.
--    H1 predicts a long tail of rows with attempts > 5 (they have been
--    bumped repeatedly). Few rows with attempts = 0 argues against H2.
SELECT attempts, count(*)
FROM task_outbox
WHERE run_at <= NOW()
GROUP BY attempts
ORDER BY attempts DESC;

-- 3. Which jobs own the oldest rows, and are those jobs still active?
--    H3 predicts rows concentrated on jobs with status IN
--    ('cancelled', 'archived', 'failed'). H1 predicts rows concentrated on
--    *running* jobs whose ZSET is oversized or whose ZADD is failing.
SELECT o.job_id,
       j.status,
       count(*)      AS rows_due,
       min(o.run_at) AS oldest,
       max(o.attempts) AS worst_attempts
FROM task_outbox o
LEFT JOIN jobs j ON j.id = o.job_id
WHERE o.run_at <= NOW()
GROUP BY o.job_id, j.status
ORDER BY min(o.run_at) ASC
LIMIT 20;

-- 4. Are the oldest rows being skipped by locks?
--    H2 predicts the oldest rows appear in pg_locks with granted=true held
--    by a long-running backend.
SELECT a.pid,
       a.state,
       NOW() - a.xact_start AS xact_age,
       a.wait_event_type,
       a.wait_event,
       LEFT(a.query, 200) AS query_snippet
FROM pg_stat_activity a
JOIN pg_locks l ON l.pid = a.pid
JOIN pg_class c ON c.oid = l.relation
WHERE c.relname = 'task_outbox'
  AND a.state <> 'idle'
ORDER BY a.xact_start NULLS LAST;
```

## Suggested fixes (do NOT implement until root cause is confirmed)

1. **Per-entry failure tracking in `ScheduleBatch`** (addresses H1). Return the
   list of failed indices so the sweeper can `bumpAttempts(failed_ids)` and
   `DELETE` the successes in the same tx. This is the narrowest change and the
   most likely to matter.

2. **Cap attempts and dead-letter stuck rows** (defensive). After N attempts
   (e.g. 10) move the row to a `task_outbox_dead` table with the error so it
   stops contributing to the gauge and is visible for triage. Do not silently
   drop.

3. **Clean up outbox on job cancel/archive** (addresses H3, hygiene). Delete
   `task_outbox` rows for the job in the same transaction as the job status
   update. Low-risk; rows would be dispatched anyway, this just avoids the extra
   round-trip through Redis.

4. **Emit per-outcome counters from the sweeper**. Today we only record
   `outbox_backlog` and `outbox_age_seconds`. A `bee.broker.outbox_sweep_total`
   counter with `outcome={dispatched,retried,failed}` labels would tell us which
   case dominates without needing an ad-hoc SQL session.

## Not in scope here

- Crawler 10 s timeout ceiling / high cancellation rate (orthogonal; upstream
  site behaviour).
- `pg_stat_activity` 30 s+ transactions from the incident dashboards (may
  confirm H2 if seen again, but do not expand scope unless the diagnostics above
  point there).
- ScheduleBatch producing duplicate ZADDs on retry (idempotent — ZADD overwrites
  the score, same member).
