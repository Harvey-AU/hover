# Lighthouse Performance Reports

Status: Phase 1 (Foundations) shipped in PR #353. Phase 2 (milestone
scheduling + skeleton `hover-analysis` app) shipped in PR #356; producer →
stream → consumer pipeline verified end-to-end on the review app via the
StubRunner. Phase 3 (real Lighthouse audits) implemented on
`work/confident-banzai-4b367c`, awaiting review. Phases 4–5 still to do. Last
updated: 2026-04-26.

## Goal

Capture Lighthouse performance audits for a small sample of pages on every
crawl, scheduled progressively while the crawl runs (every 10% of progress)
rather than as a separate post-job phase. The sample is drawn from the extremes
of the HTTP response time distribution so customers see best case and worst case
Core Web Vitals without auditing the full site.

## Scope

**In scope (MVP):**

- Lighthouse Performance category only — performance score, LCP, CLS, INP, TBT,
  FCP, Speed Index, TTFB, total byte weight.
- Mobile profile.
- **Self-hosted Lighthouse** — Chromium plus the `lighthouse` npm package run
  in-process via a Go sidecar; no external API dependency.
- Sample size: 2.5% of completed pages per extreme band (fastest + slowest) by
  `tasks.response_time`. Single formula floored at 1, capped at 50 per band —
  roughly 5% of pages on small/medium sites, capped at 100 audits per job on
  large sites.
- Scheduling waves at every 10% milestone of crawl progress; sampler dedupes so
  the same page is not audited twice within one job.
- Persistent storage of headline metrics plus the full Lighthouse JSON.
- Surface results on the existing job detail surface and CSV export.

**Out of scope (initial release):**

- Accessibility, Best Practices, SEO categories.
- Desktop profile (deferred).
- User-flow / journey audits.
- Automatic re-runs across scheduled crawls (handled by Phase 5).
- Real User Monitoring / CrUX field data fusion.

## Confirmed decisions

1. **Chromium delivery** — self-host. Bundle Chromium plus `lighthouse` into the
   analysis-app image; no PageSpeed Insights API.
2. **Sampling** — 2.5% of completed pages per extreme band (fastest + slowest),
   floored at 1, capped at 50 per band. Total audits ≈ 5% of pages, capped at
   100 per job.
3. **Per-job run cap** — 100 audits/job (50 fastest + 50 slowest), enforced by
   the per-band cap of 50.
4. **Profile** — mobile only for v1; desktop deferred.
5. **Tenant budget** — handled by plan-tier max-audits-per-day, not a separate
   knob in this feature. The scheduler honours whatever the billing/plan layer
   reports as remaining quota.
6. **Storage** — full Lighthouse JSON in R2, co-located with
   `page-content.html.gz` under
   `jobs/{job_id}/tasks/{task_id}/lighthouse-mobile.json.gz`. `lighthouse_runs`
   row stores headline metrics + R2 key.
7. **Concurrency model** — fixed `LIGHTHOUSE_MAX_CONCURRENCY` per machine (start
   at 1, smoke test on `performance-cpu-1x` / 8 GB). No autoscaling in v1;
   revisit after observing real load.

## Architecture

### Sampling

Source signal: `tasks.response_time` (BIGINT, milliseconds). The pages table has
no timing data, so sampling has to join through `tasks`.

**Single formula, applied at every milestone over the set of completed,
successful tasks for the job:**

```go
perBand := int(math.Round(float64(completedPages) * 0.025))
if perBand < 1 {
    perBand = 1
}
if perBand > 50 {
    perBand = 50
}
```

That's it — no piecewise tiers, no skip threshold. Take the top `perBand`
fastest pages by `response_time` and the top `perBand` slowest. Enqueue
Lighthouse runs for both bands.

| Pages  | per_band | Total audits | % audited |
| ------ | -------- | ------------ | --------- |
| 10     | 1        | 2            | 20%       |
| 50     | 1        | 2            | 4%        |
| 100    | 3        | 6            | 6%        |
| 200    | 5        | 10           | 5%        |
| 500    | 13       | 26           | 5.2%      |
| 1,000  | 25       | 50           | 5%        |
| 2,000  | 50 (cap) | 100          | 5%        |
| 5,000+ | 50 (cap) | 100          | ≤2%       |

Properties: floor of 1 per band means even a 5-page site gets 1 fastest + 1
slowest. Cap of 50 binds at ~2,000 pages, where 100 audits is still 5%. Beyond
that, audit count plateaus while the percentage tapers — the cost ceiling we
want.

**Dedupe and reconciliation:**

- Page-level dedupe: a page already scheduled in `lighthouse_runs` for this job
  is excluded from later milestones, even if its band membership shifts.
- Final reconciliation at 100%: rerun the sampler against the complete dataset
  to cover any late-arriving extremes that weren't visible during earlier
  milestones. Bounded by the same 50+50 cap.

### When to schedule

Today, `internal/jobs/manager.go:1004` recomputes `Job.Progress` from
`(completed + failed + skipped) / total`. There is no event listener; progress
is poll-based.

Add a milestone hook fired from the path that updates job stats after a batch
flush completes. The hook compares the previous and new progress values and,
when `floor(new/10) > floor(old/10)`, calls into a new
`lighthouse.Scheduler.OnMilestone(jobID, milestone)`.

The scheduler:

1. Loads completed tasks for the job.
2. Runs the sampler to produce the current fastest/slowest candidate set.
3. Inserts new `lighthouse_runs` rows with `status='pending'` and the matching
   `selection_band` and `selection_milestone`.
4. Enqueues a `lighthouse` task per row through the existing outbox → Redis ZSET
   → stream pipeline.

Trade-off: opportunistic per-decade scheduling means early milestones see only
early pages, so the "fastest" and "slowest" sets shift over time. The 100%
reconciliation pass closes that gap. Alternative — schedule everything at 100%
only — is simpler but loses the "results coming in while the crawl runs"
behaviour the brief asks for.

### New task type

Lighthouse work reuses the worker pipeline rather than introducing a parallel
queue.

- Add `task_type TEXT NOT NULL DEFAULT 'crawl'` to the `tasks` table (and
  `task_outbox`). Existing rows backfill to `'crawl'`; the new value is
  `'lighthouse'`.
- The crawl-side dispatcher routes `lighthouse` outbox rows onto a dedicated
  `stream:{jobID}:lh` stream rather than the crawl stream.
- The `hover-analysis` app (see "Service split" below) consumes that stream,
  runs the Runner, and writes results back to `lighthouse_runs` keyed by
  `run_id`. The crawl service never links Chromium.

### Service split — `hover-analysis` app

Lighthouse runs in a third Fly app deployed from this same repo, mirroring the
existing `hover` / `hover-worker` split:

```text
hover repo
├── cmd/
│   ├── app/main.go           → image used by `hover`           (fly.toml)
│   ├── worker/main.go        → image used by `hover-worker`    (fly.worker.toml)
│   └── analysis/main.go      → image used by `hover-analysis`  (fly.analysis.toml)  ← new
├── internal/
│   ├── db/                   ← shared
│   ├── config/               ← shared
│   ├── broker/               ← shared (Redis client, stream consumer)
│   ├── lighthouse/           ← new
│   │   ├── sampler.go        ← imported by crawl-side scheduler only
│   │   ├── scheduler.go      ← imported by crawl-side scheduler only
│   │   ├── runner.go         ← imported by cmd/analysis only
│   │   └── report.go         ← shared (DTOs)
│   └── ...
├── supabase/migrations/      ← shared single source of truth
├── Dockerfile                ← unchanged: builds main + worker, lean image
└── Dockerfile.analysis       ← new: layers Chromium + lighthouse npm
```

Why a third app rather than folding into `hover-worker`:

- Chromium adds ~300 MB to the image and 200–500 MB resident per audit. Loading
  that into `hover-worker` would force every crawl machine to carry it for
  nothing.
- Crawl machines want many small CPU-bound boxes; analysis machines want fewer
  big memory-bound boxes. Different Fly machine classes.
- Lighthouse npm version bumps deploy independently of the crawler.
- A wedged Chromium can't OOM the crawl service.

Why same repo, not a new repo:

- `internal/db`, `internal/config`, `internal/broker`, observability sidecar,
  secrets handling — all shared, no duplication.
- One `supabase/migrations/` directory, one schema owner.
- One `/dev/auto-login` flow; analysis just connects to the same local Redis and
  Postgres.
- Local dev: another `air` target, no new repo to clone.

Inter-service contract (over the existing Redis):

- Stream key: `stream:{jobID}:lh`.
- Payload: `{ run_id, job_id, page_id, url, profile, timeout_ms }`.
- Result: hover-analysis updates the matching `lighthouse_runs` row in Postgres
  directly. The row update is the signal — no callback API.
- Backpressure: bounded consumer concurrency in hover-analysis; Redis stream
  depth absorbs the queue.

### Deployment, CI/CD, and review apps

`hover-analysis` mirrors the existing `hover-worker` deployment surface exactly.
New files:

| File                             | Purpose                                                                                                                                               |
| -------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| `fly.analysis.toml`              | Production app config; `app = 'hover-analysis'`, `primary_region = 'syd'`, `[build] dockerfile = "Dockerfile.analysis"`, smoke-test `[[vm]]` defaults |
| `.fly/review_apps.analysis.toml` | Review-app overrides — smaller machine, lower concurrency, points at the PR's Supabase preview branch                                                 |
| `Dockerfile.analysis`            | Multi-stage build with Chromium + `lighthouse` npm, runs the `cmd/analysis` binary                                                                    |
| `cmd/analysis/main.go`           | Service entrypoint                                                                                                                                    |

**Production deploy (`.github/workflows/fly-deploy.yml`):**

A new step pair, parallel to the existing worker steps:

```yaml
- name: Sync secrets to Fly (analysis app)
  run: |
    flyctl secrets set --app hover-analysis \
      DATABASE_URL="$DATABASE_URL" \
      REDIS_URL="$REDIS_URL" \
      ARCHIVE_ACCESS_KEY_ID="$ARCHIVE_ACCESS_KEY_ID" \
      ARCHIVE_SECRET_ACCESS_KEY="$ARCHIVE_SECRET_ACCESS_KEY" \
      ARCHIVE_ENDPOINT="$ARCHIVE_ENDPOINT" \
      SENTRY_DSN="$SENTRY_DSN" \
      OTEL_EXPORTER_OTLP_HEADERS="$OTEL_EXPORTER_OTLP_HEADERS" \
      GRAFANA_CLOUD_USER="$GRAFANA_CLOUD_USER" \
      GRAFANA_CLOUD_API_KEY="$GRAFANA_CLOUD_API_KEY"

- name: Deploy analysis app
  run: |
    flyctl deploy --config fly.analysis.toml --app hover-analysis
```

All secrets reuse existing 1Password entries (`hover-supabase`, `hover-runtime`,
`hover-archive`) — no new vault items. The new `LIGHTHOUSE_*` env vars
(`LIGHTHOUSE_BIN`, `CHROMIUM_BIN`, `LIGHTHOUSE_MAX_CONCURRENCY`,
`LIGHTHOUSE_AUDIT_TIMEOUT_MS`) are baked into `fly.analysis.toml` `[env]` with
sensible defaults; not secrets.

**Deploy ordering:** analysis → worker → API. Consumer up before producer starts
enqueueing for it. Mirrors the existing "worker before API" rule.

**Path-based service tagging** (Grafana annotation step, end of
`fly-deploy.yml`): touching `cmd/analysis/`, `Dockerfile.analysis`,
`fly.analysis.toml`, or `internal/lighthouse/runner.go` adds
`service:hover-analysis`. Touching shared `internal/` packages already triggers
all three services; the existing `if printf ... grep` block needs one extra
`cmd/analysis/` clause.

**Review apps (`.github/workflows/review-apps.yml`):**

Mirror the existing worker review-app block:

1. Build the analysis image:
   `docker build -f Dockerfile.analysis -t hover-analysis-pr-${{ github.event.number }} .`.
   (Separate from the main image because the Dockerfile is different — can't be
   tagged twice from one build.)
2. Provision Fly app `hover-analysis-pr-${PR}` with strict-prefix get-or-create
   (avoid `hover-analysis-pr-1` falsely matching PR 12).
3. Set secrets pointing at the PR's Supabase preview branch + the same shared
   Redis and R2 used by the API/worker review apps.
4. Deploy:
   `flyctl deploy --config .fly/review_apps.analysis.toml --app hover-analysis-pr-${PR} --image hover-analysis-pr-${PR} --local-only`.
5. Order: analysis review app → worker review app → API review app.

**Cleanup (`.github/workflows/cleanup-orphaned-apps.yml` and the on-PR-close
branch of `review-apps.yml`):**

Add `hover-analysis-pr-*` to the destroy sweep. Destroy order on PR close:
analysis first (consumer detaches before producer keeps queueing), then worker,
then API. Mirrors the existing rule that destroys the worker before the API +
Redis.

**Local dev:**

- Add an `air` target for `cmd/analysis/main.go` in `.air.toml`, or document
  running it via a separate `air` invocation.
- `.env.example` gains `LIGHTHOUSE_BIN`, `CHROMIUM_BIN`,
  `LIGHTHOUSE_MAX_CONCURRENCY`, `LIGHTHOUSE_AUDIT_TIMEOUT_MS`. Devs without
  Chromium installed locally can run the analysis service against a stub runner
  (`LIGHTHOUSE_RUNNER=stub`) for end-to-end testing without real audits.

### Chromium delivery

Self-hosted, packaged into the `hover-analysis` image only. The `hover` and
`hover-worker` images stay unchanged.

**Packaging (`Dockerfile.analysis`):**

- Multi-stage build derived from the same `golang:1.26.2-alpine` builder used by
  `Dockerfile`, but with a Lighthouse stage layered on top of the runtime image:
  - Base the runtime stage on `node:20-slim` (or alpine + chromium package).
  - Install Chromium via the system package (`chromium` on Debian-based,
    `chromium` on Alpine via `apk`). Pin the package version.
  - `npm install -g lighthouse@<pinned>` for the CLI binary.
  - Copy the `analysis` Go binary in alongside Chromium and `lighthouse`.
- `fly.analysis.toml` points at `Dockerfile.analysis` via
  `[build] dockerfile = "Dockerfile.analysis"`.
- Env: `LIGHTHOUSE_BIN`, `CHROMIUM_BIN`, `LIGHTHOUSE_MAX_CONCURRENCY`,
  `LIGHTHOUSE_AUDIT_TIMEOUT_MS` plumbed through the existing config loader, set
  via `fly secrets` on the analysis app.

**Execution:**

- `internal/lighthouse/runner.go` defines a `Runner` interface; the default
  implementation is `localRunner` which shells out via `os/exec`:

  ```
  lighthouse <url> \
      --output=json \
      --quiet \
      --chrome-flags="--headless=new --no-sandbox --disable-gpu" \
      [--preset=desktop] \
      --max-wait-for-load=45000
  ```

  Lighthouse 12.x's `--preset` flag only accepts `desktop`, `experimental`, or
  `perf`. Mobile is the implicit default and the flag is omitted entirely; pass
  `--preset=desktop` only for the desktop profile.

- Captures stdout into a `LighthouseReport` struct, parses headline metrics,
  persists. Errors capture stderr in `lighthouse_runs.error_message`.

**Run duration (mobile preset):**

| Phase                | Time        |
| -------------------- | ----------- |
| Cold Chromium launch | 1–3 s       |
| Page load + audit    | 15–25 s     |
| JSON serialisation   | ~1 s        |
| **Typical total**    | **20–30 s** |
| P95                  | ~45 s       |
| Heavy/JS-rich pages  | 60–90 s     |

Desktop preset is roughly half of mobile because it skips the simulated 4× CPU
throttling.

**Resource shape and concurrency sizing:**

Default `LIGHTHOUSE_MAX_CONCURRENCY=1` on `performance-cpu-1x` / 8 GB. We start
small, observe real resource usage on production traffic, then dial up. All
knobs live in `fly.analysis.toml` (production) and
`.fly/review_apps.analysis.toml` (review apps), not in code, so tuning is a
deploy not a code change.

Sizing ladder for when we want to scale up:

| Stage          | Machine              | Mem   | `LIGHTHOUSE_MAX_CONCURRENCY` | Throughput |
| -------------- | -------------------- | ----- | ---------------------------- | ---------- |
| **v1 default** | `performance-cpu-1x` | 8 GB  | **1**                        | ~2–3/min   |
| Light prod     | `performance-cpu-2x` | 8 GB  | 2                            | ~4–6/min   |
| Default prod   | `performance-cpu-4x` | 8 GB  | 4                            | ~8–12/min  |
| Heavy prod     | `performance-cpu-8x` | 16 GB | 8                            | ~16–25/min |

Rules of thumb:

- Never set concurrency higher than vCPU count — each Lighthouse run pegs ~1
  core, and Lighthouse's CPU-throttling model produces unreliable scores once
  the host is contended.
- Above ~8 concurrent on one host, score validity collapses regardless of CPU
  count. Past that, scale horizontally (more machines), not vertically.

**Soft memory shedding (in-process safety, v1):**

Before starting a Lighthouse run, the runner reads `/proc/meminfo` (or cgroup
memory limits where available). If free memory is below
`2 × <expected RSS per audit>` (configurable, default 600 MB), the run is
deferred — re-queued with a small delay rather than risking OOM. This is local
safety, not autoscaling.

**Horizontal autoscaling: deferred.**

v1 runs on a single machine with `min_machines_running = 1`. Multi-machine
autoscaling (queue-depth driven, Fly machine count) is a follow-up once we have
real production resource data. Until then, scale-up is a manual
`fly scale count` with a corresponding bump in `min_machines_running` in the
toml.

**Sample wall-time (v1 defaults: 1 concurrent, 25 s avg per audit):**

| Crawl size | Audits    | Wall time at concurrency 1 | At concurrency 5 |
| ---------- | --------- | -------------------------- | ---------------- |
| 100 pages  | 6         | ~2.5 min                   | ~30 s            |
| 200        | 10        | ~4 min                     | ~50 s            |
| 1,000      | 50        | ~21 min                    | ~4 min           |
| 5,000      | 100 (cap) | ~42 min                    | ~8 min           |
| 10,000+    | 100 (cap) | ~42 min                    | ~8 min           |

At v1 defaults a 1,000-page crawl's audits run for ~21 minutes — fine because
crawl wall time at that size is typically longer. If audits ever drag past crawl
completion, that's the signal to bump concurrency or add machines.

**Failure handling:**

- One retry on transient Chromium crashes (exit code != 0, recognisable stderr
  patterns).
- After retry, mark `status='failed'`, store stderr, log, move on. Lighthouse
  failure must never block crawl job completion.
- Watchdog: per-run hard timeout of 90 s — kill the Chromium process tree on
  overrun.
- Per-domain pacing: reuse the existing `DomainPacer` from
  `internal/broker/dispatcher.go` so 5 audits don't simultaneously hammer a
  single customer domain.

**Interface design:**

The runner is behind an interface so we can later add `psiRunner` or
`webpagetestRunner` if we ever want to offload at peak scale, but the default
and only Phase 1 implementation is local.

### Storage

Two tiers, mirroring the existing `page-content.html.gz` pattern:

**1. R2 — full Lighthouse JSON (gzipped).**

Co-located with the existing crawl artefact in the `tasks/{task_id}/` folder:

```text
native-hover-archive/jobs/{job_id}/tasks/{task_id}/
├── page-content.html.gz        ← existing
└── lighthouse-mobile.json.gz   ← new (this plan)
```

Profile-suffixed naming so future artefacts slot in cleanly without
restructuring the path:

```text
├── lighthouse-desktop.json.gz  ← future (Phase 5, deferred)
├── screenshot-mobile.png       ← future (separate plan)
└── axe-mobile.json.gz          ← future (separate plan)
```

Reuses the existing `ColdStorageProvider` interface in `internal/archive/`, the
existing R2 credentials (`ARCHIVE_*` env vars), and the existing bucket
(`native-hover-archive`). Lighthouse JSON compresses ~5–10× — a 1–3 MB raw
report becomes ~150–400 KB gzipped.

**2. Postgres — `lighthouse_runs` table.**

Headline metrics (everything you'd ever sort/aggregate by) plus the R2 key to
fetch the full report. No JSONB blob — keeps the table small enough to index and
query cheaply, and avoids paying the 1–3 MB Postgres cost for every audit.

Foreign keys to `pages` (logical page) and `tasks` (the specific crawl run that
justified the sample). Phase 5 trend lines across crawls fall out of this
naturally.

## Data model

```sql
-- supabase/migrations/20260427000000_add_lighthouse_runs.sql

CREATE TABLE IF NOT EXISTS public.lighthouse_runs (
  id                  BIGSERIAL PRIMARY KEY,
  job_id              TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  page_id             INT  NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  source_task_id      TEXT REFERENCES tasks(id) ON DELETE SET NULL,
  selection_band      TEXT NOT NULL CHECK (selection_band IN ('fastest','slowest','reconcile')),
  selection_milestone INT  NOT NULL CHECK (selection_milestone BETWEEN 0 AND 100),
  status              TEXT NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending','running','succeeded','failed','skipped_quota')),
  performance_score   INT,
  lcp_ms              INT,
  cls                 NUMERIC(5,3),
  inp_ms              INT,
  tbt_ms              INT,
  fcp_ms              INT,
  speed_index_ms      INT,
  ttfb_ms             INT,
  total_byte_weight   BIGINT,
  report_key          TEXT,    -- R2 object key, e.g. jobs/{id}/tasks/{id}/lighthouse-mobile.json.gz
  error_message       TEXT,
  scheduled_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  started_at          TIMESTAMPTZ,
  completed_at        TIMESTAMPTZ,
  duration_ms         INT,
  UNIQUE (job_id, page_id)
);

CREATE INDEX IF NOT EXISTS idx_lighthouse_runs_job_id
  ON public.lighthouse_runs(job_id);
CREATE INDEX IF NOT EXISTS idx_lighthouse_runs_page_id
  ON public.lighthouse_runs(page_id);
CREATE INDEX IF NOT EXISTS idx_lighthouse_runs_pending
  ON public.lighthouse_runs(status)
  WHERE status IN ('pending','running');
```

The `UNIQUE (job_id, page_id)` constraint is the correctness backstop for the
per-milestone dedupe.

A second migration adds `task_type` to `tasks` and `task_outbox`:

```sql
ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS task_type TEXT NOT NULL DEFAULT 'crawl';
ALTER TABLE public.task_outbox
  ADD COLUMN IF NOT EXISTS task_type TEXT NOT NULL DEFAULT 'crawl';
```

## Implementation touchpoints

**Crawl-side (`hover` / `hover-worker`, existing apps):**

- `internal/jobs/types.go` — extend `Task` with `TaskType string`.
- `internal/jobs/manager.go` — add milestone detection where stats update after
  batch flush; emit `OnProgressMilestone(job, oldPct, newPct)`.
- `internal/broker/dispatcher.go` — when an outbox row carries
  `task_type='lighthouse'`, route to `stream:{jobID}:lh` rather than the crawl
  stream. Reuse the existing `DomainPacer` so concurrent audits don't hammer one
  customer domain.
- `internal/lighthouse/sampler.go` (new, shared package) — fastest/slowest band
  selection via the single 2.5%-per-band formula, capped at 50.
- `internal/lighthouse/scheduler.go` (new, shared package) — milestone-driven
  enqueue with quota enforcement and dedupe.
- `internal/db/lighthouse.go` (new, shared) — insert/update/list helpers.
- `internal/api/jobs.go` — extend job detail response; add
  `GET /v1/jobs/:id/lighthouse`.
- `web/static/js/jobs/*` — new tab on the job detail page.
- Quota plumbing: read remaining audit budget from the plan-tier layer (existing
  billing/usage path) before each milestone; record `status='skipped_quota'` for
  any pages that overflow.

**Analysis-side (`hover-analysis`, new app):**

- `cmd/analysis/main.go` (new) — boots the analysis service: connects to Redis +
  Postgres using the shared config loader, starts a stream consumer on
  `stream:{jobID}:lh` for active jobs, dispatches work to the runner with
  `LIGHTHOUSE_MAX_CONCURRENCY` cap.
- `internal/lighthouse/runner.go` (new, only imported by `cmd/analysis`) —
  `Runner` interface plus `localRunner` shelling out to the bundled `lighthouse`
  binary.
- `internal/lighthouse/report.go` (new, shared) — Lighthouse JSON DTOs and
  metric extraction.
- `Dockerfile.analysis` (new) — multi-stage build that layers Chromium and
  `lighthouse@<pinned>` on top of the analysis Go binary.
- `fly.analysis.toml` (new) — production app config; smoke-test sized
  (`performance-cpu-1x` / 8 GB / `LIGHTHOUSE_MAX_CONCURRENCY=1`).
- `.fly/review_apps.analysis.toml` (new) — review-app overrides.
- `.github/workflows/fly-deploy.yml` — new "Sync secrets to Fly (analysis
  app)" + "Deploy analysis app" steps; new `service:hover-analysis` clause in
  the path-based service tagging block.
- `.github/workflows/review-apps.yml` — new build/provision/deploy block for
  `hover-analysis-pr-${PR}` review apps; deploy ordering analysis → worker →
  API; destroy ordering reversed on PR close.
- `.github/workflows/cleanup-orphaned-apps.yml` — add `hover-analysis-pr-*` to
  the destroy sweep with strict-prefix matching.
- Entrypoint: `Dockerfile.analysis` ships a minimal entrypoint that runs
  `./analysis` directly, since the analysis app doesn't need the alloy sidecar
  or the static HTML/migration files that the existing `start.sh` sets up.
  (Alternative: extend `start.sh` to be role-aware via env. Preferred to keep
  the analysis image minimal.)
- Config: `LIGHTHOUSE_BIN`, `CHROMIUM_BIN`, `LIGHTHOUSE_MAX_CONCURRENCY`
  (default 1), `LIGHTHOUSE_AUDIT_TIMEOUT_MS` (default 90000),
  `LIGHTHOUSE_MEMORY_SHED_THRESHOLD_MB` (default 600) — set in the toml `[env]`
  block, not as Fly secrets.

**Local dev:**

- `.air.toml` — add a target that builds and runs `cmd/analysis/main.go`, or
  document running it via a separate `air` invocation alongside the existing
  one.
- `.env.example` — add the new `LIGHTHOUSE_*` variables with sensible defaults
  pointing at locally-installed Chromium/Lighthouse for devs who want to
  exercise the runner locally; otherwise the analysis service can run with a
  stub runner.

## Rollout phases

### Phase 1 — Foundations ✅ Complete (PR #353, commit 9b4f45f)

- Migrations: `lighthouse_runs` (with `report_key TEXT`, no JSONB blob),
  `task_type` on `tasks`/`task_outbox`.
- DB layer in `internal/db/lighthouse.go`.
- Sampler in `internal/lighthouse/sampler.go` with unit tests covering:
  - Floor of 1 per band at low N (1, 5, 10, 50 completed pages).
  - Linear scaling at medium N (200, 500, 1,000).
  - Cap of 50 per band at high N (2,000, 10,000).
  - Dedupe across milestones.
- Stub runner returning canned data so the pipeline can be exercised end-to-end
  before Chromium lands.

**Phase 1 implementation notes (deviations from this plan):**

- `lighthouse_runs.job_id` is `TEXT` (not `UUID`) to match the actual `jobs.id`
  and `tasks.id` types in the existing schema. The `UUID` example earlier in
  this doc was inaccurate; the migration and the doc now agree on `TEXT`.
- The sampler's selection function is `SelectSamples(...)`, not `Sample(...)` —
  `Sample` is the struct (one tagged task), so the function needed a different
  name. The `Sample` struct itself is unchanged.
- During CodeRabbit review three small but real issues were fixed in commit
  9b4f45f:
  - `CompleteLighthouseRun` and `FailLighthouseRun` UPDATEs are gated on
    `status = 'running'` so a duplicate-delivered worker can't clobber a row
    that has already reached a terminal state. Redis stream redelivery is
    at-least-once.
  - `StubRunner.Run` derives a child context from `AuditRequest.Timeout` so the
    per-run budget is enforced even pre-Chromium. Real `localRunner` (Phase 3)
    must do the same.
  - The `task_type` migration adds
    `CHECK (task_type IN ('crawl', 'lighthouse'))` on both `tasks` and
    `task_outbox` so a typo can't silently route through to the dispatcher. New
    task types are a one-line drop+re-add.
- `task_type` is **schema-only** at end of Phase 1: the column exists with
  default `'crawl'`, but no producer enqueues `'lighthouse'` and the dispatcher
  does not route on it. Phase 2 lands the producer + dispatcher routing
  atomically. CodeRabbit flagged this as missing and accepted the Phase 1 /
  Phase 2 split when the rationale was explained — there is now a recorded
  learning instructing CodeRabbit not to re-flag it.

### Phase 2 — Milestone scheduling and skeleton analysis app ✅ Complete (PR #356)

End-to-end verified on the review app: a real crawl drives lighthouse audits
through `stream:{jobID}:lh`, the analysis app processes them via the
`StubRunner`, and `lighthouse_runs` rows reach `succeeded`. Phase 3 picks up
from there — only the runner backing needs replacing.

**Phase 2 implementation notes (deviations from this plan):**

- **One combined PR**, not the optional 2a/2b split — the producer surface
  turned out small enough to review in one pass.
- **No tenant quota plumbing.** Audits are inherently capped by the sampler
  (≤100/job) and by crawl page-completion rate; they don't consume the daily
  page quota and the scheduler never calls `get_daily_quota_remaining`. The
  `skipped_quota` enum value stays on `lighthouse_runs.status` from Phase 1 but
  no Phase 2 code sets it. Revisit only if v1 telemetry shows abuse (Phase 5).
- **Outbox routing uses synthetic UUIDs**, not nullable `task_id`. Each
  lighthouse outbox row carries a freshly generated `uuid.NewString()` in
  `task_id` (kept `NOT NULL UNIQUE`) plus the real link in a new
  `lighthouse_run_id BIGINT NULL` column. Rationale: avoids rewriting the
  existing `promote_waiting_with_outbox` plpgsql function's
  `ON CONFLICT (task_id) DO NOTHING` to specify a partial-index predicate. A
  CHECK on `task_outbox` enforces
  `(crawl ⇔ lighthouse_run_id IS NULL) ∧ (lighthouse ⇔ lighthouse_run_id IS NOT NULL)`;
  the mirror CHECK on `task_outbox_dead` is intentionally loosened to allow
  `lighthouse_run_id IS NULL` for `task_type='lighthouse'` so
  `ON DELETE SET NULL` preserves the forensic trail after a parent
  `lighthouse_runs` row is cleaned up.
- **Milestone hook is in-process, not event-bus.** `db.BatchManager` fires a
  `BatchFlushCallback(ctx, jobIDs)` after each successful flush;
  `JobManager.MaybeFireMilestones` reads job progress, gates against an
  in-process per-job tracker (`lastMilestoneFired`), and forwards crossings to a
  registered `OnProgressMilestone` callback. Multi-replica safety is bounded by
  `lighthouse_runs UNIQUE(job_id, page_id)` — if two workers fire the same
  milestone, the second insert is a no-op. The tracker is cleared on terminal
  `UpdateJobStatus` transitions (and on `CancelJob` alongside `processedPages`)
  so memory stays bounded.
- **ZSET wire format extended backwards-compatibly.** ScheduleEntry's
  pipe-delimited member grew from 9 to 11 fields (`taskType`,
  `lighthouseRunID`); `ParseScheduleEntry` accepts both shapes so a rolling
  deploy doesn't drop in-flight members. Legacy 9-field entries default to
  `taskType='crawl'`.
- **Dispatcher fails closed on routing.** Unknown `task_type` values and
  `task_type='lighthouse'` rows missing `lighthouse_run_id` produce hard errors
  instead of silently routing onto the crawl stream.
- **Consumer correctness for redelivery + reclaim.** `MarkLighthouseRunRunning`
  was relaxed from `status = 'pending'` to `status IN ('pending', 'running')` so
  `XAUTOCLAIM` can hand a shutdown-interrupted row to a fresh consumer cleanly.
  The `status='running'` guards on `CompleteLighthouseRun` / `FailLighthouseRun`
  bound the resulting double-run race to "first to finish wins, the other gets
  `ErrLighthouseRunNotFound` and ACKs". The consumer also distinguishes shutdown
  cancellation (`context.Canceled` → leave row in `running`, skip ACK) from
  genuine failure, and runs an `XAUTOCLAIM` recovery sweep on start + every 60s
  so a fresh pod picks up its predecessor's PEL.
- **Observability + slog covers Phase 1 too.** `internal/observability`
  registers `bee.lighthouse.runs_scheduled_total{band}`,
  `bee.lighthouse.runs_total{outcome}`, and
  `bee.lighthouse.run_duration_ms{outcome}`. `internal/lighthouse/log.go`
  introduces a package-scoped `lighthouse` logger via `logging.Component`;
  runner, scheduler, and consumer log start/finish/failures through it.
  `cmd/analysis` carries its own `analysis` component logger plus the standard
  Alloy sidecar. Audit URLs are sanitised (query/fragment stripped) before any
  log call via the exported `lighthouse.SanitiseAuditURL`.

The original Phase 2 plan and starting-point notes follow for posterity.

- `OnProgressMilestone` hook in `JobManager`, fired from the post-batch-flush
  path.
- `lighthouse.Scheduler` enqueues sampled rows and writes outbox entries with
  `task_type='lighthouse'`; honours plan-tier remaining quota.
- Dispatcher routes `lighthouse` outbox rows onto `stream:{jobID}:lh`.
- **New `cmd/analysis/main.go` skeleton** with stream consumer and stub runner
  that writes canned data back to `lighthouse_runs`. New `Dockerfile.analysis`
  (no Chromium yet — just the Go binary). New `fly.analysis.toml` and
  `.fly/review_apps.analysis.toml`.
- **CI/CD wiring** so `hover-analysis` ships through the same pipeline as
  `hover-worker`:
  - `.github/workflows/fly-deploy.yml` — new secrets-sync + deploy steps; deploy
    ordering analysis → worker → API; `service:hover-analysis` added to the
    path-based service tagging block.
  - `.github/workflows/review-apps.yml` — build with `Dockerfile.analysis`, tag
    `hover-analysis-pr-${PR}`, provision Fly app with strict-prefix matching,
    set secrets pointing at the PR's Supabase preview branch, deploy review app.
    Destroy ordering reversed on PR close.
  - `.github/workflows/cleanup-orphaned-apps.yml` — sweep `hover-analysis-pr-*`.
- Integration test: drive a synthetic job from 0% to 100%, assert correct band
  sizes per milestone, no duplicates, reconciliation behaviour at 100%, and that
  the analysis app picks up + persists results end-to-end.
- Land a no-op review app on a real PR to validate the pipeline end-to-end
  before Phase 3.

**Phase 2 starting point (post-Phase-1):**

- Branch off `main` after PR #353 is merged.
- The shapes Phase 2 plugs into are already in the tree:
  - `internal/db.InsertLighthouseRun` is `tx`-scoped — the scheduler can insert
    the `lighthouse_runs` row in the same transaction as the `task_outbox`
    entry, mirroring the existing crawl-side CTE pattern.
  - `internal/db.GetLighthouseRunPageIDs(ctx, jobID)` returns the dedupe set the
    sampler needs across milestones.
  - `internal/lighthouse.SelectSamples(completed, milestone, alreadySampled)` is
    pure and ready to call.
  - `internal/lighthouse.StubRunner` is the consumer-side stub for the skeleton
    `cmd/analysis/main.go` until Phase 3 swaps in `localRunner`.
  - `internal/jobs.TaskType` + `TaskTypeCrawl` / `TaskTypeLighthouse` constants
    are defined; the column on `tasks` and `task_outbox` is `CHECK`-constrained
    to those two values.

**Optional split**: Phase 2 is large enough that splitting it into 2a
(scheduler + dispatcher routing in the existing apps, still consumed by a test)
and 2b (skeleton `hover-analysis` app + CI wiring) keeps each PR reviewable.
Either ordering works; 2a-then-2b lets us prove the producer side end-to-end
against an in-process consumer before the new app's CI surface lands.

### Phase 3 — Real Lighthouse audits

- Add the Chromium + `lighthouse@<pinned>` layers to `Dockerfile.analysis`. No
  change to `Dockerfile` — `hover` and `hover-worker` images stay lean.
- `localRunner` in `internal/lighthouse/runner.go` shells out to the binary with
  mobile preset and the documented Chrome flags.
- Soft memory-shed circuit breaker: skip new runs when free memory is below
  `LIGHTHOUSE_MEMORY_SHED_THRESHOLD_MB` (default 600).
- R2 upload: gzip the Lighthouse JSON, upload to
  `jobs/{job_id}/tasks/{task_id}/lighthouse-mobile.json.gz` via the existing
  `ColdStorageProvider`, store key in `lighthouse_runs.report_key`.
- 90-second per-run hard timeout; one retry on transient failures; stderr
  captured to `error_message` on permanent failure.
- Verify on the production-shaped review app: peak memory at the v1 default of
  `LIGHTHOUSE_MAX_CONCURRENCY=1`, average audit duration, failure rate. Document
  numbers; only then consider raising the default.

**Phase 3 implementation notes (deviations from this plan):**

- **Image base switched to Debian (`node:20-slim`)**, not Alpine. Alpine's
  `chromium` package consistently lags upstream; Debian bookworm tracks Chromium
  stable within days, so the security/stability call goes to Debian. Currently
  resolved versions: Chromium 147.0.7727.116 (the version Debian bookworm
  shipped at first build — `apt-get install chromium` is intentionally unpinned
  for now, with a `TODO(phase3)` in `Dockerfile.analysis` to capture the exact
  pin once the first prod build resolves it) and pinned `lighthouse@12.2.1`.
  Image size lands at ~2 GB — within Fly's pull window but worth a future trim
  pass.
- **`source_task_id` plumbed via DB rather than the wire format.**
  `MarkLighthouseRunRunning` was changed to `RETURNING source_task_id` so the
  consumer learns the parent task at audit start time. The Phase 2 ZSET wire
  format (now 11 fields) was left untouched — bumping it again on the back of
  Phase 2's bump would have been churn. The runner falls back to
  `jobs/{job_id}/runs/{run_id}/lighthouse-mobile.json.gz` when `source_task_id`
  is NULL (parent task deleted via `ON DELETE SET NULL`).
- **`ErrMemoryShed` is a sentinel, not a permanent failure.** The consumer
  treats it like shutdown cancellation: leave the row in `running`, skip `XAck`,
  let `XAUTOCLAIM` redeliver once memory recovers. Treating it as `failed` would
  burn the audit slot unrecoverably.
- **Process-tree kill via `Setpgid` + `Kill(-pgid, SIGKILL)`.** Without this a
  context-cancelled audit orphans Chromium renderers that keep eating memory.
  Covered by a unit test that asserts a 30s `sleep` fake binary dies within the
  200ms timeout.
- **Stderr capped via a ring buffer** (16 KiB tail) before being embedded in the
  returned error and ultimately `lighthouse_runs.error_message`. A wedged
  Chromium can emit megabytes of debug output; the column is plain TEXT and
  would otherwise grow unbounded.
- **`LIGHTHOUSE_RUNNER` stays `stub` on review apps and on the merge commit.**
  The Chromium layers ship in the image regardless, but flipping the runner
  default waits until the first production smoke test confirms the binary boots
  cleanly. A follow-up commit on the same PR flips production once the
  review-app numbers look healthy.
- **`dumb-init` reaps Chromium zombies.** Renderer crashes leave defunct
  processes; without dumb-init the analysis container eventually exhausts its
  PID table.
- **Archive provider boots in `cmd/analysis`.** Phase 2 didn't need it (stub
  runner has no R2 upload). Phase 3 adds an `archive.ProviderFromEnv()` call
  gated on `LIGHTHOUSE_RUNNER=local` so review apps without R2 credentials still
  boot the stub runner.

**Phase 3 starting point (post-Phase-2):**

- Branch off `main` after PR #356 is merged.
- The shapes Phase 3 plugs into are already in the tree:
  - `Dockerfile.analysis` builds the Go binary only — Phase 3 adds the
    Chromium + `lighthouse@<pinned>` layers on top of it; no other image is
    touched.
  - `internal/lighthouse.Runner` interface + `StubRunner` are in place — drop a
    `localRunner` next to `StubRunner` and register it via the existing
    `selectRunner` switch in `cmd/analysis/main.go`.
  - `LIGHTHOUSE_RUNNER` env (`stub`|`local`) is the dispatch knob already
    consulted at boot; flip the production default to `local` once Chromium
    proves out, but leave `.fly/review_apps.analysis.toml` on `stub` until then.
  - `LIGHTHOUSE_AUDIT_TIMEOUT_MS` (default 90s), `LIGHTHOUSE_MAX_CONCURRENCY`
    (default 1) and `LIGHTHOUSE_MEMORY_SHED_THRESHOLD_MB` (default 600) are
    plumbed through; the local runner just needs to consult them.
  - `internal/archive.ColdStorageProvider` is the existing R2 path — the
    `report_key` column on `lighthouse_runs` is already populated by
    `CompleteLighthouseRun` from the runner's `AuditResult.ReportKey`, so the
    local runner only needs to upload + return the key.
  - Observability surface (`bee.lighthouse.runs_total{outcome}`,
    `bee.lighthouse.run_duration_ms`, `bee.lighthouse.runs_scheduled_total`) is
    wired and emits through the Alloy sidecar; the local runner uses the same
    helpers, no new metric registrations needed.
  - `lighthouse.SanitiseAuditURL` is exported — keep using it for any log lines
    that include the audit URL.

**Open before coding:**

1. Pin the Alpine `chromium` package and `lighthouse` npm versions (open
   question still listed in § Open questions below).
2. `fly.analysis.toml` VM size: stays at smoke-test `performance-cpu-1x` / 8 GB
   / `LIGHTHOUSE_MAX_CONCURRENCY=1` until measurements land, or bump
   pre-emptively?
3. Keep the 90 s per-run hard timeout, or tune from stub-side telemetry?

### Phase 4 — Surfacing

- API: aggregated metrics, score distribution, per-page report download.
- Frontend: tab on job detail with histogram + fastest/slowest comparison.
- CSV export columns alongside existing task data.

### Phase 5 — Hardening (post-MVP)

- Horizontal autoscaling: queue-depth-driven `fly scale count` for
  `hover-analysis`, with `min_machines_running` and a hard ceiling to prevent
  runaway scale-out.
- Per-tenant configurable sample percentage and per-band cap.
- Trend view across multiple crawls per domain (chart of perf score over time,
  regressions highlighted).
- Optional desktop profile (adds `lighthouse-desktop.json.gz` artefact).
- Optional remote runner implementation (PSI or WebPageTest) if peak load ever
  outstrips local capacity.

## Risks and mitigations

- **Plan-tier quota exhaustion.** Mitigation: scheduler reads remaining daily
  audit budget from the plan/usage layer before enqueuing each milestone. Excess
  pages get `status='skipped_quota'` so the UI can explain why they are missing.
- **Skewed early milestones.** First 10% of crawled pages are typically
  navigation/landing pages and skew fast. Mitigation: 100% reconciliation pass
  reruns the sampler against the full dataset.
- **Long Lighthouse runs blocking crawl workers.** Resolved by the service
  split: Lighthouse runs in `hover-analysis`, a separate Fly app, on a separate
  Redis stream, on separate machines. Crawl workers cannot be starved by
  Chromium.
- **HTTP response time vs. user-perceived load are different things.** The
  sample is intentionally biased toward what we can measure during crawl. UI
  must label the sample as "selected by HTTP response time" so customers don't
  read it as "the slowest pages by Lighthouse".
- **Schema drift on `task_type`.** Column is `NOT NULL DEFAULT 'crawl'`, so
  existing rows backfill at write time and reads return `'crawl'` for any legacy
  in-flight inserts; no manual backfill needed.
- **Lighthouse run determinism.** Even self-hosted Lighthouse varies run to run
  (network, CPU contention, Chromium variance). Mitigation: store every report;
  future trend view aggregates over time rather than trusting a single run;
  never alert on a single bad score.
- **Container size.** Chromium + Node adds ~300 MB. Confined to
  `Dockerfile.analysis` only; `hover` and `hover-worker` images stay lean.
- **Memory pressure at concurrency.** Five Chromium instances can peak ~1.5 GB
  at the runner. Mitigation: concurrency cap is a single env knob, sized against
  the Fly machine class chosen for `hover-analysis` (32 GB recommended for the
  default of 5). Above ~8 concurrent on one host the CPU-throttling model breaks
  and scores become unreliable, so scale horizontally rather than vertically.
- **Per-domain pile-up.** Five concurrent audits could all target the same
  customer domain. Mitigation: the analysis-side consumer reuses the existing
  `DomainPacer` from `internal/broker/dispatcher.go` to throttle per-domain
  Lighthouse work, mirroring how crawl HTTP requests are paced.
- **Sandbox flags.** `--no-sandbox` is required inside containers without user
  namespaces. This is the standard Lighthouse-in-Docker pattern but weakens
  Chromium's isolation; we are auditing third-party URLs, so the mitigation is
  to confine the binary to a non-root user and rely on container isolation.
- **Privacy.** Lighthouse JSON includes URLs and resource lists. No PII beyond
  what's already captured for the crawl itself, but the report column must be
  excluded from any future "share read-only" surface.

## Open questions

All headline decisions are confirmed. Remaining items to settle during
implementation:

1. Pinned versions for the `chromium` system package and `lighthouse` npm
   package — choose at the time `Dockerfile.analysis` lands.
2. Exact Fly machine class for `hover-analysis` — start at 32 GB / 16 vCPU in
   `syd` to match the default concurrency of 5; revisit after Phase 3 measures
   peak RSS on staging.
3. Whether `start.sh` becomes role-aware (one entrypoint, switches on env) or
   whether `Dockerfile.analysis` ships its own minimal entrypoint that only
   knows about the analysis binary. Probably the latter, since `hover-analysis`
   won't need the alloy sidecar / static files / migration files that `start.sh`
   currently sets up.
4. API shape for `GET /v1/jobs/:id/lighthouse` — finalise during Phase 4
   alongside the frontend tab.
