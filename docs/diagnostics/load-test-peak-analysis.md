# Load Test Peak Analysis

Date: 2026-04-07 Test duration: ~10 hrs (20:00–06:00 AEST) · 158,000 log lines ·
271 minutes sampled

---

## Recommendations and actions

### Immediate (no infrastructure cost)

1. **Fix Sentry transaction race bugs** — `internal/db/*.go` (Sentry issues
   BH/BG/C6/CP): goroutines retaining `*sql.Tx` references after
   timeout/rollback. Code bugs that worsen under any scale. Do not bisect for a
   recent commit — these are latent races exposed by load, not a regression.

2. **Cap dynamic worker scale at 40** — `worker.go:488` currently allows scaling
   to 50, which exceeds the 45-connection pool. Lower `maxWorkers` to 40 to
   match the pool budget.

3. **Raise `DB_QUEUE_MAX_CONCURRENCY`** — default of 12 is too conservative
   against a 45-connection pool. Set to 20–25 in `fly.toml` (leaving headroom
   for non-semaphored direct calls and reserved connections).

4. **Increase `GNH_BATCH_CHANNEL_SIZE`** — the 2,000-slot buffer was exhausted
   during Peak 1. Set to 5,000 in `fly.toml` as a buffer against DB stall
   events.

### Low cost, high impact

5. **Enable Supabase CPU spend cap** — the DB CPU was at 75–100% with 14% IOWait
   throughout the test. The CPU cap is a burst allowance; enabling it absorbs
   load spikes at variable cost (~$10–30/mo depending on usage). It will not fix
   the IOPS ceiling but reduces stall frequency immediately.

### Required for 1M tasks/day

6. **Upgrade Supabase compute to Large tier** — the IOPS cap (3,300/s) is the
   fundamental throughput ceiling. At current load it was already hitting the
   limit. 1M/day requires sustained ~41,667 tasks/hr (~5× current average); this
   needs ~5× IOPS = ~16,000+ IOPS, which is the Large compute tier (~+$100/mo).

7. **2× Fly VM** — after the DB is no longer the bottleneck, the app CPU will
   limit at ~27+ concurrent jobs. Upgrade to 2× CPU/RAM (~+$20–40/mo).

8. **Profile app CPU hotspot** before upgrading Fly compute — understand what is
   burning CPU at Peak 2 (likely link discovery / HTML processing) to confirm
   the compute investment is warranted.

### Estimated cost to deliver 1M tasks/day reliably

| Component              | Current (est.) | Required   | Delta                 |
| ---------------------- | -------------- | ---------- | --------------------- |
| Supabase compute       | ~$25–50/mo     | Large tier | +$100–150/mo          |
| Supabase CPU burst cap | $0             | Enable     | +$10–30/mo (variable) |
| Fly VM                 | ~$10–20/mo     | 2× CPU/RAM | +$20–40/mo            |
| **Total delta**        |                |            | **+$130–220/mo**      |

Code fixes alone (items 1–4) could realistically deliver 20,000–25,000 tasks/hr
on current hardware (~500–600K/day). The remaining gap to 1M/day is a
hardware/cost decision.

---

## Throughput summary

| Metric              | Value                                          |
| ------------------- | ---------------------------------------------- |
| Peak                | ~11,118 tasks/hr (02:00 AEST, ~31 active jobs) |
| Average (sustained) | ~7,000–8,000/hr (23:00–04:00 AEST)             |
| Low (under stress)  | ~3,625/hr (01:00 AEST — Peak 1 DB collapse)    |
| Target for 1M/day   | ~41,667/hr sustained                           |
| Gap to target       | ~5× current average                            |

---

## Timezone alignment

Log run was UTC; charts are AEST (UTC+10):

- **Log Peak 1** (14:45–15:30 UTC) = 00:45–01:30 AEST
- **Log Peak 2** (16:15–17:45 UTC) = 02:15–03:45 AEST

---

## Root cause summary

| Issue                                    | Evidence                                                                         | Location                                | Severity                                     |
| ---------------------------------------- | -------------------------------------------------------------------------------- | --------------------------------------- | -------------------------------------------- |
| Supabase IOPS cap hit                    | IOPS chart touching 3,300/s ceiling during test; 14% IOWait on DB CPU            | Supabase compute tier                   | Hard ceiling — dominant constraint for scale |
| Supabase CPU saturated                   | DB CPU 75–100% throughout test; Supabase warning active                          | Supabase compute tier                   | Active right now — enable CPU spend cap      |
| DB transaction race (use-after-rollback) | Sentry BH/BG/C6/CP — "Regressed", multiple weeks old                             | `internal/db/*.go`                      | Bug to fix — predates load test              |
| Workers scaling beyond pool capacity     | Dynamic scale to 50 workers; pool capped at 45; direct DB calls bypass semaphore | `worker.go:488`, `fly.toml`             | Causes pool saturation above ~40 active jobs |
| Queue semaphore too conservative         | `DB_QUEUE_MAX_CONCURRENCY=12` against a 45-connection pool                       | `queue.go:66`                           | Tuning — raise alongside pool budget         |
| Batch write channel exhausted            | 2,130 "Update batch channel full" in Peak 1; 2,000-slot buffer filled            | `batch.go:25`; `GNH_BATCH_CHANNEL_SIZE` | Symptom of DB stall upstream                 |
| App CPU saturation at ~27+ jobs          | 100% Fly CPU from 02:00 AEST (Peak 2)                                            | `worker.go`                             | Profile before adding compute                |

---

## Event timeline

| Time (AEST)     | Active jobs | Tasks/hr            | What happened                                                                                                                |
| --------------- | ----------- | ------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| 20:00           | 9           | ~4,000              | Jobs starting, DB and Fly resources idle                                                                                     |
| 23:00–00:00     | 26–45       | ~9,000              | Ramp to peak; network hit 4.46 MB/s inbound; Supabase queries jumped to 800–3,000/interval                                   |
| **00:00–01:30** | **45–50**   | **~10,000 → 3,625** | **Peak 1** — write channels saturated then DB pool collapsed; batch channel exhausted (2,130 hits); Supabase IOPS at ceiling |
| 01:30–02:00     | 31          | 3,625 → 11,118      | Recovery burst — backlog draining                                                                                            |
| **02:00–04:00** | **27–31**   | **8,000–11,000**    | **Peak 2** — Fly app CPU hit 100%; DB still slow; tasks completing but with sustained errors                                 |
| 04:00–06:00     | 19–22       | 4,000–6,000         | Job count declining naturally; Supabase CPU/IOPS still elevated                                                              |
| 06:05–06:34     | ~19         | collapsing          | Pending backlog grew 4 → 1,556; completions fell 123 → 22/min                                                                |

---

## Supabase resource findings

### CPU (binding constraint, active now)

Supabase CPU ran at 75–100% for the entire test duration with the following
breakdown at peak (11:40pm AEST):

| Component                | %   |
| ------------------------ | --- |
| User (query work)        | 52% |
| IOWait (stalled on disk) | 14% |
| System                   | 9%  |
| Idle                     | 22% |

**14% IOWait is significant** — the CPU is sitting idle waiting for disk, not
doing useful work. This confirms the DB is disk-bound, not compute-bound. Every
query needing a page not in buffer cache stalls until disk catches up.

Supabase has issued an active "high CPU usage" warning. Enabling the **CPU spend
cap** is the immediate mitigation.

### IOPS (the hard ceiling)

The IOPS chart shows peaks touching the **3,300/s hard cap** (the dashed line).
The cascade when this is hit:

> IOPS cap → disk writes queue → IOWait spikes → CPU stalls → slow transactions
> → pool backup → errors in app logs

The workload is predominantly writes (task status updates, page inserts, WAL).
To sustain 1M/day throughput, ~16,000 IOPS is required (Large compute tier).

### Memory

1.79 GB total; 517 MB used by Postgres; 1.18 GB as page cache. The large cache
is healthy and reduces read IOPS. Memory is **not a constraint** at current
scale — it will become relevant at Large tier if the working set grows
significantly.

### Database connections

- Max connections: **90**
- Permanently reserved by Supabase internals: **18** (always consumed)
- Effective available for app: ~65
- During test: climbed to **60–80 total connections** — close to the 90 cap
- The app's `DB_MAX_OPEN_CONNS=45` fits within budget but the combination of
  reserved + PostgREST + auth + other roles leaves limited headroom

### Supavisor (pgBouncer) client connections

- Before test: 5–15 connections
- During test: **30–60 connections, peaking near 60**
- Confirms the app is operating near `DB_MAX_OPEN_CONNS=45` plus service
  overhead
- No pooler saturation observed — Supavisor is handling the connection
  multiplexing correctly

---

## Fly resource findings

### CPU

- Idle during Peak 1 (app stalled waiting on DB, not burning cycles)
- Hit **100%** at Peak 2 onset (~02:00 AEST) and stayed 40–100% through 04:00
- 5-min load avg still ~60% at 06:00

### Memory

- Flat at ~512 MiB before test; jumped to 700–900 MiB at test start; settled
  ~600 MiB
- Peak: **894 MiB** — no memory leak; one-time working-set expansion as queues
  filled
- Well within current VM limits; not a constraint

### Goroutines / GC

- Goroutines steady at 32–36 throughout — no goroutine leak
- GC completely flat — no memory pressure

### Network I/O

- Spiked to **4.46 MB/s inbound** at 00:00 AEST (peak job concurrency)
- Sustained 1–3 MB/s inbound, ~0.5–1 MB/s outbound
- Inbound/outbound ratio (~3:1) consistent with a page-fetching crawler

---

## Sentry findings

~85 total Sentry events vs thousands of log-level "errors". The disparity is
expected — most log errors are internally handled retry/backpressure signals.
Actual unhandled exceptions:

| Sentry issue                                                 | Count | Status              | What it means                                                                          |
| ------------------------------------------------------------ | ----- | ------------------- | -------------------------------------------------------------------------------------- |
| DB pool saturated (queuing) — BH + BG                        | 63    | Regressed (1–2 wks) | Pool exhaustion reaching Sentry threshold; fired from `queue.go:807` at 95% pool usage |
| `commit unexpectedly resulted in rollback` — CP              | 22    | New                 | Transaction race — goroutine using `*sql.Tx` after it was rolled back                  |
| `transaction has already been committed or rolled back` — C6 | 21    | Regressed (3d)      | Sibling race — different code path, same root cause                                    |
| Failed to parse robots.txt                                   | 7     | Escalating          | Benign; unrelated to load                                                              |

The transaction race bugs (CP + C6) are latent, load-amplified issues — not
caused by a recent change. They worsen because DB pool exhaustion widens the
window during which transactions time out while goroutines still hold
references.

---

## Actual configuration (verified from source)

### DB connection pool — `internal/db/db.go` + `fly.toml`

| Setting                               | Value                       | Source                                                                              |
| ------------------------------------- | --------------------------- | ----------------------------------------------------------------------------------- |
| `DB_MAX_OPEN_CONNS`                   | **45**                      | `fly.toml` (overrides code default of 70)                                           |
| `DB_MAX_IDLE_CONNS`                   | **15**                      | `fly.toml` (overrides code default of 20)                                           |
| `MaxLifetime`                         | 5 minutes                   | `db.go:339`                                                                         |
| `ConnMaxIdleTime`                     | 2 minutes                   | `db.go:359`                                                                         |
| `statement_timeout`                   | 60s                         | `db.go:251`                                                                         |
| `idle_in_transaction_session_timeout` | 30s                         | `db.go:250`                                                                         |
| Supabase pooler                       | pgBouncer, transaction mode | auto-detected via `pooler.supabase.com`; `simple_protocol` + `pgbouncer=true` added |

### Queue semaphore — `internal/db/queue.go`

Wraps all task-claim and batch-update operations. Direct DB calls (page writes,
domain lookups) bypass this semaphore and draw from the shared pool.

| Setting                        | Effective value        | Env var                        |
| ------------------------------ | ---------------------- | ------------------------------ |
| `DB_QUEUE_MAX_CONCURRENCY`     | **12** (default)       | `DB_QUEUE_MAX_CONCURRENCY`     |
| `DB_POOL_RESERVED_CONNECTIONS` | **4** (default)        | `DB_POOL_RESERVED_CONNECTIONS` |
| Semaphore slots                | **min(12, 45−4) = 12** | —                              |
| Pool warn threshold            | 90% (≥40/45 open)      | `DB_POOL_WARN_THRESHOLD`       |
| Pool saturated threshold       | 95% (≥43/45 open)      | `DB_POOL_REJECT_THRESHOLD`     |
| TX retries                     | 3                      | `DB_TX_MAX_RETRIES`            |

`DB_QUEUE_MAX_CONCURRENCY=12` is the binding constraint — 12 < 41 available
slots. Under 50 workers, 38 are competing for 12 queue semaphore slots at any
time.

### Worker pool — `cmd/app/main.go` + `internal/jobs/worker.go`

| Setting                   | Value           | Source                                  |
| ------------------------- | --------------- | --------------------------------------- |
| Base workers (production) | **30**          | `main.go:580`                           |
| Max workers (production)  | **50**          | `worker.go:488` — dynamic scale ceiling |
| `WORKER_CONCURRENCY`      | **1** (default) | `main.go:588`; not set in `fly.toml`    |
| Total base task capacity  | 30              | —                                       |
| Total max task capacity   | 50              | —                                       |

`main.go:580` comment: _"sized to stay under Supabase pool limits while keeping
queue saturated"_. Dynamic scaling to 50 breaks this — 50 workers making direct
DB calls exceeds the 45-connection pool.

### Batch write channel — `internal/db/batch.go`

| Setting          | Default                      | Env var                                     |
| ---------------- | ---------------------------- | ------------------------------------------- |
| Channel buffer   | **2,000**                    | `GNH_BATCH_CHANNEL_SIZE` (range 500–20,000) |
| Max batch size   | 100                          | —                                           |
| Flush interval   | 2s                           | `GNH_BATCH_MAX_INTERVAL_MS`                 |
| Failure fallback | after 3 consecutive failures | —                                           |

2,130 "Update batch channel full" events in Peak 1 = buffer fully exhausted.
Upstream DB stall caused consumers to stall, filling the channel.
