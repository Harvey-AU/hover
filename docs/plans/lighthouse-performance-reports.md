# Lighthouse Performance Reports

Status: Proposal — not yet approved.
Last updated: 2026-04-26.

## Goal

Capture Lighthouse performance audits for a representative sample of pages on
every crawl, scheduled progressively while the crawl runs (every 10% of
progress) rather than as a separate post-job phase. The sample is drawn from
the fastest and slowest pages by HTTP response time so customers can see best
case and worst case Core Web Vitals without paying for a full-site audit.

## Scope

**In scope (MVP):**

- Lighthouse Performance category only — performance score, LCP, CLS, INP, TBT,
  FCP, Speed Index, TTFB, total byte weight.
- Mobile profile by default.
- Sample size: roughly 10% of the crawled pages, made up of ~5% fastest and ~5%
  slowest by `tasks.response_time`.
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

## Decisions required before implementation

1. **Chromium delivery strategy** — Phase 1 should use Google PageSpeed Insights
   (PSI) API. See "Architecture" below for the rationale and Phase-2 fallback.
2. **Sampling interpretation** — read of the brief: 5% fastest plus 5% slowest,
   totalling roughly 10%. Confirm before code lands.
3. **Per-job run cap** — proposed default 100 runs/job (50 fastest + 50
   slowest). Smaller sites get the floor of 5 + 5.
4. **Profile** — mobile only for v1.
5. **Tenant budget** — daily PSI quota allocation per tenant before the
   sampler stops scheduling new audits.

## Architecture

### Sampling

- Source signal: `tasks.response_time` (BIGINT, milliseconds). The pages table
  has no timing data, so sampling has to join through `tasks`.
- Selection band per milestone: top N by ascending `response_time` (fastest
  band) and top N by descending `response_time` (slowest band), where
  `N = clamp(round(completed_pages * 0.05), 5, 50)`.
- Page-level dedupe: a page already scheduled in `lighthouse_runs` for this
  job is excluded.
- Final reconciliation at 100%: rerun the sampler against the complete dataset
  to cover any late-arriving fastest/slowest pages that didn't appear in
  earlier milestones.

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

The crawler is pure Colly + goquery — there is no headless browser anywhere in
the project today. Lighthouse needs Chromium, so we have to choose how to
provide it.

**Option A — PageSpeed Insights API (recommended for Phase 1).**
Free tier, ~25k requests/day with an API key, no infrastructure to host.
External dependency, rate-limited (currently ~400/100s/key), and we can't
customise emulation. Each call is ~30s, returns JSON shaped almost identically
to local Lighthouse.
*Pros:* fastest path to value; no Dockerfile or Fly machine impact.
*Cons:* external SLA; quota risk; lab-only data.

**Option B — Local Lighthouse sidecar (Phase 2 fallback).**
Add `node` plus the `lighthouse` npm package to the Dockerfile. Run via
`os/exec` from a Go executor, capture stdout JSON. ~150MB resident per
instance, ~5–10s CPU per run.
*Pros:* deterministic, no external dependency, full emulation control.
*Cons:* container size, machine memory pressure, packaging complexity.

**Option C — Managed service (WebPageTest, BrowserStack, Speedlify).**
Paid offload of infrastructure. Useful at scale; not recommended for v1.

**Recommendation:** ship Phase 1 on PSI. The `lighthouse.Runner` interface
should be designed so swapping in a local executor is a one-file change.

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
                       CHECK (status IN ('pending','running','succeeded','failed','skipped_budget')),
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
- `internal/lighthouse/` (new) — `Runner` interface, `psi.go` PSI client,
  `sampler.go` selection logic, `scheduler.go` milestone-driven enqueue.
- `internal/db/lighthouse.go` (new) — insert/update/list helpers.
- `internal/api/jobs.go` — extend job detail response; add
  `GET /v1/jobs/:id/lighthouse`.
- `web/static/js/jobs/*` — new tab on the job detail page.
- `Dockerfile` — Phase 1: no change. Phase 2: add `node` + `lighthouse`.
- Secrets: `LIGHTHOUSE_PSI_API_KEY` via 1Password, plumbed through the
  existing config loader.

## Rollout phases

### Phase 0 — Decisions

- Confirm Chromium strategy (PSI for v1).
- Confirm sampling interpretation, run cap, profile.
- Confirm tenant-level daily budget.

### Phase 1 — Foundations (no Chromium yet)

- Migrations: `lighthouse_runs`, `task_type` on `tasks`/`task_outbox`.
- DB layer in `internal/db/lighthouse.go`.
- Sampler in `internal/lighthouse/sampler.go` with unit tests covering
  small-N floor (≤5 candidates per band), dedupe, and reconcile pass.
- `LighthouseExecutor` stub returning canned data so the full pipeline can be
  exercised end-to-end without real Lighthouse calls.

### Phase 2 — Milestone scheduling

- `OnProgressMilestone` hook in `JobManager`.
- `lighthouse.Scheduler` enqueues sampled rows + outbox entries.
- Lighthouse worker pool consuming the dedicated stream.
- Integration test: drive a synthetic job from 0% to 100%, assert correct
  number of `lighthouse_runs` rows per milestone, no duplicates.

### Phase 3 — Real Lighthouse audits

- PSI client in `internal/lighthouse/psi.go` with retries, backoff, and
  per-tenant budget enforcement.
- Map PSI response into the `lighthouse_runs` columns.
- Failure handling: one retry, then `status='failed'` with `error_message`;
  failure does not block job completion.

### Phase 4 — Surfacing

- API: aggregated metrics, distribution, per-page report download.
- Frontend: tab on job detail with histogram + fastest/slowest comparison.
- CSV export columns alongside existing task data.

### Phase 5 — Hardening (post-MVP)

- Cold-storage `report_json` to S3/R2 above a size threshold.
- Configurable sample size and cap per plan tier.
- Trend view across multiple crawls per domain (chart of perf score over
  time, regressions highlighted).
- Optional swap to local Lighthouse sidecar (Option B) if PSI quota or SLA
  becomes a problem.

## Risks and mitigations

- **PSI quota exhaustion.** Mitigation: per-tenant daily budget; per-job hard
  cap; sampler records `status='skipped_budget'` once exhausted so the UI can
  explain why a page is missing.
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
- **Schema drift on `task_type`.** Add nullable with default `'crawl'`; no
  backfill required because the default applies on read.
- **External API determinism.** PSI results vary across runs. Mitigation:
  store every report; future trend view aggregates over time rather than
  trusting a single run.
- **Cost (Phase 2 path).** Local Chromium adds ~150MB resident per worker;
  decide based on Fly machine size at the time we move off PSI.
- **Privacy.** Lighthouse JSON includes URLs and resource lists. No PII
  beyond what's already captured for the crawl itself, but the report column
  must be excluded from any future "share read-only" surface.

## Open questions

1. PSI vs local Chromium for Phase 1 — confirm PSI.
2. Run cap per job — confirm 100 (50 + 50).
3. Mobile-only or also desktop — confirm mobile-only.
4. Sampling interpretation — confirm "5% fastest + 5% slowest = ~10% total".
5. Per-tenant daily PSI budget — pick a starting number.
