# Lighthouse Performance Reports

Status: Proposal — decisions confirmed 2026-04-26, ready for implementation.
Last updated: 2026-04-26.

## Goal

Capture Lighthouse performance audits for a small sample of pages on every
crawl, scheduled progressively while the crawl runs (every 10% of progress)
rather than as a separate post-job phase. The sample is drawn from the
extremes of the HTTP response time distribution so customers see best case
and worst case Core Web Vitals without auditing the full site.

## Scope

**In scope (MVP):**

- Lighthouse Performance category only — performance score, LCP, CLS, INP, TBT,
  FCP, Speed Index, TTFB, total byte weight.
- Mobile profile.
- **Self-hosted Lighthouse** — Chromium plus the `lighthouse` npm package run
  in-process via a Go sidecar; no external API dependency.
- Sample size: 10% of the fastest 5% and 10% of the slowest 5% by
  `tasks.response_time` — i.e. roughly 1% of total pages, drawn from the
  extremes.
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

1. **Chromium delivery** — self-host. Bundle Chromium plus `lighthouse` into
   the Fly image; no PageSpeed Insights API.
2. **Sampling** — 10% of the top 5% and 10% of the bottom 5% by HTTP response
   time. Roughly 1% of pages total.
3. **Per-job run cap** — default 100 audits/job (50 fastest band + 50 slowest
   band). With the new sampling math the cap is rarely binding (10k+ pages
   to hit it), but it's a hard ceiling against runaway tenants.
4. **Profile** — mobile only for v1; desktop deferred.
5. **Tenant budget** — handled by plan-tier max-audits-per-day, not a separate
   knob in this feature. The scheduler honours whatever the billing/plan
   layer reports as remaining quota.

## Architecture

### Sampling

- Source signal: `tasks.response_time` (BIGINT, milliseconds). The pages table
  has no timing data, so sampling has to join through `tasks`.
- Two-step selection per milestone, applied to the set of completed,
  successful tasks for the job:
  1. **Identify the extreme bands.** Take the fastest 5% by ascending
     `response_time` (fastest band) and the slowest 5% by descending
     `response_time` (slowest band).
  2. **Audit 10% of each band.** Within each band, sample 10% of the pages
     and enqueue Lighthouse runs for those.
- Per-band sample size: `M = clamp(round(band_size * 0.10), 1, 50)`. The
  floor of 1 means tiny sites still get one fastest and one slowest audit;
  the ceiling of 50 enforces the per-job cap (50 + 50 = 100).
- Skip threshold: if `completed_pages < 20`, the bands are too narrow to be
  meaningful — skip until the next milestone, and at the final 100%
  reconciliation pass force at least 1 + 1 if any results exist at all.
- Page-level dedupe: a page already scheduled in `lighthouse_runs` for this
  job is excluded from later milestones, even if its band membership shifts.
- Final reconciliation at 100%: rerun the sampler against the complete
  dataset to cover any late-arriving extremes that weren't visible during
  earlier milestones. Reconciliation is bounded by the same 50+50 cap.

Worked example for a 1,000-page crawl at 100%: fastest band = 50 pages, audit
5; slowest band = 50 pages, audit 5; total 10 Lighthouse runs. For 100 pages:
fastest band = 5, audit 1; slowest band = 5, audit 1; total 2 runs.

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
4. Enqueues a `lighthouse` task per row through the existing outbox →
   Redis ZSET → stream pipeline.

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

Lighthouse runs in a third Fly app deployed from this same repo, mirroring
the existing `hover` / `hover-worker` split:

```
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

- Chromium adds ~300 MB to the image and 200–500 MB resident per audit.
  Loading that into `hover-worker` would force every crawl machine to carry
  it for nothing.
- Crawl machines want many small CPU-bound boxes; analysis machines want
  fewer big memory-bound boxes. Different Fly machine classes.
- Lighthouse npm version bumps deploy independently of the crawler.
- A wedged Chromium can't OOM the crawl service.

Why same repo, not a new repo:

- `internal/db`, `internal/config`, `internal/broker`, observability sidecar,
  secrets handling — all shared, no duplication.
- One `supabase/migrations/` directory, one schema owner.
- One `/dev/auto-login` flow; analysis just connects to the same local Redis
  and Postgres.
- Local dev: another `air` target, no new repo to clone.

Inter-service contract (over the existing Redis):

- Stream key: `stream:{jobID}:lh`.
- Payload: `{ run_id, job_id, page_id, url, profile, timeout_ms }`.
- Result: hover-analysis updates the matching `lighthouse_runs` row in
  Postgres directly. The row update is the signal — no callback API.
- Backpressure: bounded consumer concurrency in hover-analysis;
  Redis stream depth absorbs the queue.

### Chromium delivery

Self-hosted, packaged into the `hover-analysis` image only. The `hover` and
`hover-worker` images stay unchanged.

**Packaging (`Dockerfile.analysis`):**

- Multi-stage build derived from the same `golang:1.26.2-alpine` builder used
  by `Dockerfile`, but with a Lighthouse stage layered on top of the runtime
  image:
  - Base the runtime stage on `node:20-slim` (or alpine + chromium package).
  - Install Chromium via the system package (`chromium` on Debian-based,
    `chromium` on Alpine via `apk`). Pin the package version.
  - `npm install -g lighthouse@<pinned>` for the CLI binary.
  - Copy the `analysis` Go binary in alongside Chromium and `lighthouse`.
- `fly.analysis.toml` points at `Dockerfile.analysis` via
  `[build] dockerfile = "Dockerfile.analysis"`.
- Env: `LIGHTHOUSE_BIN`, `CHROMIUM_BIN`, `LIGHTHOUSE_MAX_CONCURRENCY`,
  `LIGHTHOUSE_AUDIT_TIMEOUT_MS` plumbed through the existing config loader,
  set via `fly secrets` on the analysis app.

**Execution:**

- `internal/lighthouse/runner.go` defines a `Runner` interface; the default
  implementation is `localRunner` which shells out via `os/exec`:

  ```
  lighthouse <url> \
      --output=json \
      --quiet \
      --chrome-flags="--headless=new --no-sandbox --disable-gpu" \
      --preset=desktop|mobile \
      --max-wait-for-load=45000
  ```

- Captures stdout into a `LighthouseReport` struct, parses headline metrics,
  persists. Errors capture stderr in `lighthouse_runs.error_message`.

**Run duration (mobile preset):**

| Phase | Time |
| --- | --- |
| Cold Chromium launch | 1–3 s |
| Page load + audit | 15–25 s |
| JSON serialisation | ~1 s |
| **Typical total** | **20–30 s** |
| P95 | ~45 s |
| Heavy/JS-rich pages | 60–90 s |

Desktop preset is roughly half of mobile because it skips the simulated 4×
CPU throttling.

**Resource shape and concurrency sizing:**

Default `LIGHTHOUSE_MAX_CONCURRENCY=5` on a 32 GB / 16 vCPU analysis
machine. Sizing guidance:

| Machine class | Safe concurrency | Notes |
| --- | --- | --- |
| 8 GB / 4 core | 1–2 | OOM risk above; metric quality degrades fast |
| 16 GB / 8 core | 3–4 | Tight; ~7 GB Chromium + headroom for Go server |
| 32 GB / 16 core | **5** (default) | Sweet spot; comfortable headroom |
| 64 GB / 32 core | 8 | Returns diminish past this |

Running more than ~8 concurrent on one host degrades Lighthouse score
validity because the host CPU contention breaks the simulated-throttling
model. Scale horizontally instead — multiple analysis machines, each with a
modest cap.

**Sample wall-time at 5 concurrent, 25 s avg per audit:**

| Crawl size | Audits (1% sample) | Wall time |
| --- | --- | --- |
| 100 pages | 2 | ~25 s |
| 1,000 | 10 | ~50 s |
| 10,000 | 100 | ~8 min |
| 100,000 | 1,000 | ~83 min |

In every realistic case, audits finish *inside* the crawl wall time rather
than gating job completion.

**Failure handling:**

- One retry on transient Chromium crashes (exit code != 0, recognisable
  stderr patterns).
- After retry, mark `status='failed'`, store stderr, log, move on. Lighthouse
  failure must never block crawl job completion.
- Watchdog: per-run hard timeout of 90 s — kill the Chromium process tree
  on overrun.
- Per-domain pacing: reuse the existing `DomainPacer` from
  `internal/broker/dispatcher.go` so 5 audits don't simultaneously hammer a
  single customer domain.

**Interface design:**

The runner is behind an interface so we can later add `psiRunner` or
`webpagetestRunner` if we ever want to offload at peak scale, but the
default and only Phase 1 implementation is local.

### Storage

Use a dedicated `lighthouse_runs` table rather than columns on `tasks`. Phase 5
wants trend lines across crawls, which a separate table supports cleanly. The
table is foreign-keyed to `pages` (logical page) and `tasks` (the specific
crawl run that justified the sample).

Full Lighthouse JSON lives in `report_json JSONB` initially. Typical payload is
50–100 KB; if the table grows past, say, 5 GB we move `report_json` to cold
storage (S3/R2 via the existing `ColdStorageProvider` interface in
`internal/archive/`) and replace the column with a pointer.

## Data model

```sql
-- supabase/migrations/20260427000000_add_lighthouse_runs.sql

CREATE TABLE IF NOT EXISTS public.lighthouse_runs (
  id                  BIGSERIAL PRIMARY KEY,
  job_id              UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
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
  report_json         JSONB,
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
- `internal/jobs/manager.go` — add milestone detection where stats update
  after batch flush; emit `OnProgressMilestone(job, oldPct, newPct)`.
- `internal/broker/dispatcher.go` — when an outbox row carries
  `task_type='lighthouse'`, route to `stream:{jobID}:lh` rather than the
  crawl stream. Reuse the existing `DomainPacer` so concurrent audits don't
  hammer one customer domain.
- `internal/lighthouse/sampler.go` (new, shared package) — fastest/slowest
  band identification plus 10%-of-band sampling.
- `internal/lighthouse/scheduler.go` (new, shared package) — milestone-driven
  enqueue with quota enforcement and dedupe.
- `internal/db/lighthouse.go` (new, shared) — insert/update/list helpers.
- `internal/api/jobs.go` — extend job detail response; add
  `GET /v1/jobs/:id/lighthouse`.
- `web/static/js/jobs/*` — new tab on the job detail page.
- Quota plumbing: read remaining audit budget from the plan-tier layer
  (existing billing/usage path) before each milestone; record
  `status='skipped_quota'` for any pages that overflow.

**Analysis-side (`hover-analysis`, new app):**

- `cmd/analysis/main.go` (new) — boots the analysis service: connects to
  Redis + Postgres using the shared config loader, starts a stream consumer
  on `stream:{jobID}:lh` for active jobs, dispatches work to the runner with
  `LIGHTHOUSE_MAX_CONCURRENCY` cap.
- `internal/lighthouse/runner.go` (new, only imported by `cmd/analysis`) —
  `Runner` interface plus `localRunner` shelling out to the bundled
  `lighthouse` binary.
- `internal/lighthouse/report.go` (new, shared) — Lighthouse JSON DTOs and
  metric extraction.
- `Dockerfile.analysis` (new) — multi-stage build that layers Chromium and
  `lighthouse@<pinned>` on top of the analysis Go binary.
- `fly.analysis.toml` (new) — Fly app config for `hover-analysis`,
  `[build] dockerfile = "Dockerfile.analysis"`, primary region `syd`,
  memory-tier machine class.
- `scripts/start.sh` — extend to handle the analysis binary if/when the
  start script becomes role-aware (or use a separate entrypoint in
  `Dockerfile.analysis`).
- Config: `LIGHTHOUSE_BIN`, `CHROMIUM_BIN`, `LIGHTHOUSE_MAX_CONCURRENCY`
  (default 5), `LIGHTHOUSE_AUDIT_TIMEOUT_MS` (default 90000) — set as Fly
  secrets on `hover-analysis`.

**Local dev:**

- `.air.toml` — add a target that builds and runs `cmd/analysis/main.go`,
  or document running it via a separate `air` invocation alongside the
  existing one.
- `.env.example` — add the new `LIGHTHOUSE_*` variables with sensible
  defaults pointing at locally-installed Chromium/Lighthouse for devs who
  want to exercise the runner locally; otherwise the analysis service can
  run with a stub runner.

## Rollout phases

### Phase 1 — Foundations

- Migrations: `lighthouse_runs`, `task_type` on `tasks`/`task_outbox`.
- DB layer in `internal/db/lighthouse.go`.
- Sampler in `internal/lighthouse/sampler.go` with unit tests covering:
  - Small-site floor (1+1 minimum from the 100% reconciliation pass).
  - Skip threshold below 20 completed pages.
  - Per-band 10% sampling with rounding behaviour.
  - Dedupe across milestones.
- `LighthouseExecutor` stub returning canned data so the full pipeline can be
  exercised end-to-end before Chromium lands.

### Phase 2 — Milestone scheduling and skeleton analysis app

- `OnProgressMilestone` hook in `JobManager`, fired from the post-batch-flush
  path.
- `lighthouse.Scheduler` enqueues sampled rows and writes outbox entries with
  `task_type='lighthouse'`; honours plan-tier remaining quota.
- Dispatcher routes `lighthouse` outbox rows onto `stream:{jobID}:lh`.
- **New `cmd/analysis/main.go` skeleton** with stream consumer, stub runner
  that writes canned data back to `lighthouse_runs`. New `Dockerfile.analysis`
  (no Chromium yet — just the Go binary), new `fly.analysis.toml`. Deploy
  `hover-analysis` to staging.
- Integration test: drive a synthetic job from 0% to 100%, assert correct
  band sizes per milestone, no duplicates, reconciliation behaviour at 100%,
  and that the analysis app picks up + persists results end-to-end.

### Phase 3 — Real Lighthouse audits

- Add the Chromium + `lighthouse@<pinned>` layers to `Dockerfile.analysis`.
  No change to `Dockerfile` — `hover` and `hover-worker` images stay lean.
- `localRunner` in `internal/lighthouse/runner.go` shells out to the binary
  with mobile preset and the documented Chrome flags.
- 90-second per-run hard timeout; one retry on transient failures; stderr
  captured to `error_message` on permanent failure.
- Verify on a Fly staging machine: peak memory at `LIGHTHOUSE_MAX_CONCURRENCY=5`,
  average audit duration, failure rate. Adjust the default if RSS or P95
  duration is out of bounds.

### Phase 4 — Surfacing

- API: aggregated metrics, score distribution, per-page report download.
- Frontend: tab on job detail with histogram + fastest/slowest comparison.
- CSV export columns alongside existing task data.

### Phase 5 — Hardening (post-MVP)

- Cold-storage `report_json` to S3/R2 above a size threshold (reuse the
  `ColdStorageProvider` interface in `internal/archive/`).
- Configurable sample percentage and per-band cap per plan tier.
- Trend view across multiple crawls per domain (chart of perf score over
  time, regressions highlighted).
- Optional desktop profile.
- Optional remote runner implementation (PSI or WebPageTest) if peak load
  ever outstrips local capacity.

## Risks and mitigations

- **Plan-tier quota exhaustion.** Mitigation: scheduler reads remaining
  daily audit budget from the plan/usage layer before enqueuing each
  milestone. Excess pages get `status='skipped_quota'` so the UI can explain
  why they are missing.
- **Skewed early milestones.** First 10% of crawled pages are typically
  navigation/landing pages and skew fast. Mitigation: 100% reconciliation pass
  reruns the sampler against the full dataset.
- **Long Lighthouse runs blocking crawl workers.** Resolved by the service
  split: Lighthouse runs in `hover-analysis`, a separate Fly app, on a
  separate Redis stream, on separate machines. Crawl workers cannot be
  starved by Chromium.
- **HTTP response time vs. user-perceived load are different things.** The
  sample is intentionally biased toward what we can measure during crawl. UI
  must label the sample as "selected by HTTP response time" so customers
  don't read it as "the slowest pages by Lighthouse".
- **Schema drift on `task_type`.** Column is `NOT NULL DEFAULT 'crawl'`, so
  existing rows backfill at write time and reads return `'crawl'` for any
  legacy in-flight inserts; no manual backfill needed.
- **Lighthouse run determinism.** Even self-hosted Lighthouse varies run to
  run (network, CPU contention, Chromium variance). Mitigation: store every
  report; future trend view aggregates over time rather than trusting a
  single run; never alert on a single bad score.
- **Container size.** Chromium + Node adds ~300 MB. Confined to
  `Dockerfile.analysis` only; `hover` and `hover-worker` images stay lean.
- **Memory pressure at concurrency.** Five Chromium instances can peak
  ~1.5 GB at the runner. Mitigation: concurrency cap is a single env knob,
  sized against the Fly machine class chosen for `hover-analysis` (32 GB
  recommended for the default of 5). Above ~8 concurrent on one host the
  CPU-throttling model breaks and scores become unreliable, so scale
  horizontally rather than vertically.
- **Per-domain pile-up.** Five concurrent audits could all target the same
  customer domain. Mitigation: the analysis-side consumer reuses the
  existing `DomainPacer` from `internal/broker/dispatcher.go` to throttle
  per-domain Lighthouse work, mirroring how crawl HTTP requests are paced.
- **Sandbox flags.** `--no-sandbox` is required inside containers without
  user namespaces. This is the standard Lighthouse-in-Docker pattern but
  weakens Chromium's isolation; we are auditing third-party URLs, so the
  mitigation is to confine the binary to a non-root user and rely on
  container isolation.
- **Privacy.** Lighthouse JSON includes URLs and resource lists. No PII
  beyond what's already captured for the crawl itself, but the report column
  must be excluded from any future "share read-only" surface.

## Open questions

All headline decisions are confirmed. Remaining items to settle during
implementation:

1. Pinned versions for the `chromium` system package and `lighthouse` npm
   package — choose at the time `Dockerfile.analysis` lands.
2. Exact Fly machine class for `hover-analysis` — start at 32 GB / 16 vCPU
   in `syd` to match the default concurrency of 5; revisit after Phase 3
   measures peak RSS on staging.
3. Whether `start.sh` becomes role-aware (one entrypoint, switches on env)
   or whether `Dockerfile.analysis` ships its own minimal entrypoint that
   only knows about the analysis binary. Probably the latter, since
   `hover-analysis` won't need the alloy sidecar / static files / migration
   files that `start.sh` currently sets up.
4. API shape for `GET /v1/jobs/:id/lighthouse` — finalise during Phase 4
   alongside the frontend tab.
