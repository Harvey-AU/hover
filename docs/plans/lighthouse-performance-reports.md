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
- `internal/jobs/executor.go` (`TaskExecutor.Execute`) dispatches to a new
  `LighthouseExecutor` when `task.TaskType == "lighthouse"`. Crawl logic is
  untouched.
- A separate Lighthouse worker pool with bounded concurrency (default 2)
  prevents 30-second Lighthouse runs from wedging the sub-second crawl
  workers. Implementation: a second `StreamWorkerPool` consuming a dedicated
  `stream:{jobID}:lh` stream, fed by the dispatcher when it sees a
  `lighthouse` task in the outbox.

### Chromium delivery

Self-hosted. The crawler is pure Colly + goquery today, so this is the first
time Chromium enters the image.

**Packaging:**

- Add a Lighthouse build stage to the existing multi-stage Dockerfile:
  - Base on `node:20-slim` for the Lighthouse layer.
  - Install Chromium via the system package (`chromium` on Debian) so we
    don't depend on Puppeteer's auto-download. Pin the package version.
  - `npm install -g lighthouse@<pinned>` for the CLI binary.
  - Copy the resulting `node`, `chromium`, and `lighthouse` artefacts into
    the final runtime image alongside the Go binary.
- Wire `lighthouse` and `chromium` paths through env (`LIGHTHOUSE_BIN`,
  `CHROMIUM_BIN`) so local dev and CI can override them.
- Triple-surface rule does not apply (no new HTML pages), but the Dockerfile
  edit is essential — without it, the executor will 404 the binary at
  runtime.

**Execution:**

- New `internal/lighthouse/runner.go` defines a `Runner` interface; the
  default implementation is `localRunner` which shells out via `os/exec`:

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

**Resource shape:**

- ~150–250 MB resident per concurrent Chromium instance.
- ~10–30 s CPU per audit (mobile emulation is heavier than desktop).
- Expect to run on Fly machines sized for the load. Concurrency cap on the
  Lighthouse worker pool defaults to **2** to bound peak memory; tunable via
  env (`LIGHTHOUSE_MAX_CONCURRENCY`).

**Failure handling:**

- One retry on transient Chromium crashes (exit code != 0, recognisable
  stderr patterns).
- After retry, mark `status='failed'`, store stderr, log, move on. Lighthouse
  failure must never block crawl job completion.
- Watchdog: per-run hard timeout of 90 s — kill the Chromium process tree
  on overrun.

**Interface design:**

The runner is behind an interface so we can later add `psiRunner` or
`webpagetestRunner` if we ever want to offload at scale, but the default and
only Phase 1 implementation is local.

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

- `internal/jobs/types.go` — extend `Task` with `TaskType string`.
- `internal/jobs/manager.go` — add milestone detection where stats update
  after batch flush; emit `OnProgressMilestone(job, oldPct, newPct)`.
- `internal/jobs/executor.go` — branch on `TaskType` to route to either the
  existing crawler path or the new `LighthouseExecutor`.
- `internal/jobs/stream_worker.go` — second worker pool for the
  `stream:{jobID}:lh` stream with its own concurrency cap.
- `internal/lighthouse/` (new):
  - `runner.go` — `Runner` interface plus `localRunner` shelling out to the
    bundled `lighthouse` binary.
  - `sampler.go` — fastest/slowest band identification + 10%-of-band sampling.
  - `scheduler.go` — milestone-driven enqueue with quota enforcement and
    dedupe.
  - `report.go` — Lighthouse JSON parsing + metric extraction.
- `internal/db/lighthouse.go` (new) — insert/update/list helpers.
- `internal/api/jobs.go` — extend job detail response; add
  `GET /v1/jobs/:id/lighthouse`.
- `web/static/js/jobs/*` — new tab on the job detail page.
- `Dockerfile` — Lighthouse build stage adds `node`, `chromium`, and the
  pinned `lighthouse` npm package; copies binaries into the runtime image.
- Config: `LIGHTHOUSE_BIN`, `CHROMIUM_BIN`, `LIGHTHOUSE_MAX_CONCURRENCY`,
  `LIGHTHOUSE_AUDIT_TIMEOUT_MS` env vars in the existing config loader.
- Quota plumbing: read remaining audit budget from the plan-tier layer
  (existing billing/usage path) before each milestone; record
  `status='skipped_quota'` for any pages that overflow.

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

### Phase 2 — Milestone scheduling

- `OnProgressMilestone` hook in `JobManager`, fired from the post-batch-flush
  path.
- `lighthouse.Scheduler` enqueues sampled rows and outbox entries; honours
  plan-tier remaining quota.
- Dedicated Lighthouse worker pool consuming the `stream:{jobID}:lh` stream
  with `LIGHTHOUSE_MAX_CONCURRENCY` cap.
- Integration test: drive a synthetic job from 0% to 100%, assert correct
  band sizes per milestone, no duplicates, reconciliation behaviour at 100%.

### Phase 3 — Real Lighthouse audits

- Add Lighthouse build stage to the Dockerfile (Chromium + `lighthouse` npm
  package, version pinned).
- `localRunner` in `internal/lighthouse/runner.go` shells out to the binary
  with mobile preset and the documented Chrome flags.
- 90-second per-run hard timeout; one retry on transient failures; stderr
  captured to `error_message` on permanent failure.
- Verify on a Fly staging machine: peak memory at concurrency=2, average
  audit duration, failure rate.

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
- **Long Lighthouse runs blocking crawl workers.** Mitigation: dedicated
  Lighthouse worker pool with low concurrency cap (default 2); does not share
  the crawl stream.
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
- **Container size.** Adding Chromium + Node bumps the image substantially
  (~300 MB before squashing). Mitigation: multi-stage build, install the
  system Chromium package, prune npm dev dependencies, use slim base.
- **Memory pressure at concurrency.** Two Chromium instances can peak ~500
  MB. Mitigation: concurrency cap is a single env knob; sized against the
  Fly machine memory at deploy time.
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

1. Pinned versions for `chromium` and `lighthouse` npm package — choose at
   the time the Dockerfile change lands.
2. Exact Fly machine sizing implications once Phase 3 runs on staging —
   measure peak RSS and adjust `LIGHTHOUSE_MAX_CONCURRENCY` defaults.
3. API shape for `GET /v1/jobs/:id/lighthouse` — finalise during Phase 4
   alongside the frontend tab.
