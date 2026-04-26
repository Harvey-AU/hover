-- lighthouse_runs: persisted Lighthouse audit results, one row per
-- (job_id, page_id) sample. Headline metrics live here so we can sort
-- and aggregate cheaply; the full Lighthouse JSON sits in R2 and is
-- referenced by report_key. See docs/plans/lighthouse-performance-reports.md.
--
-- The crawl-side scheduler inserts rows with status='pending' at every
-- 10% milestone of crawl progress. The hover-analysis service consumes
-- the lighthouse stream, runs the audit, then updates the row with
-- metrics and report_key. The UNIQUE (job_id, page_id) constraint is
-- the correctness backstop for per-milestone dedupe.
--
-- Note on types: jobs.id and tasks.id are TEXT in this schema (see
-- 20240101000000_initial_schema.sql), so the foreign keys here must
-- match. The plan document's UUID example was inaccurate.

CREATE TABLE IF NOT EXISTS public.lighthouse_runs (
  id                  BIGSERIAL PRIMARY KEY,
  job_id              TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  page_id             INT  NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  source_task_id      TEXT REFERENCES tasks(id) ON DELETE SET NULL,
  selection_band      TEXT NOT NULL
                       CHECK (selection_band IN ('fastest','slowest','reconcile')),
  selection_milestone INT  NOT NULL
                       CHECK (selection_milestone BETWEEN 0 AND 100),
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
  report_key          TEXT,
  error_message       TEXT,
  scheduled_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  started_at          TIMESTAMPTZ,
  completed_at        TIMESTAMPTZ,
  duration_ms         INT,
  UNIQUE (job_id, page_id)
);

CREATE INDEX IF NOT EXISTS idx_lighthouse_runs_job_id
  ON public.lighthouse_runs (job_id);

CREATE INDEX IF NOT EXISTS idx_lighthouse_runs_page_id
  ON public.lighthouse_runs (page_id);

-- Partial index narrows the analysis-side claim query to in-flight rows.
CREATE INDEX IF NOT EXISTS idx_lighthouse_runs_pending
  ON public.lighthouse_runs (status)
  WHERE status IN ('pending', 'running');
