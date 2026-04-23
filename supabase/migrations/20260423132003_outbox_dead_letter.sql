-- task_outbox_dead: terminal home for outbox rows that exceeded the
-- retry cap. Kept as a separate table (not a status column on task_outbox)
-- so the sweeper's hot-path query stays a simple index-only scan and the
-- dead-letter rows never contribute to the oldest-age gauge.
--
-- The sweeper moves rows here when attempts >= MaxAttempts (see
-- internal/broker/outbox.go). Rows are retained for manual triage; a
-- follow-up job can purge them once the underlying cause is understood.
--
-- Schema mirrors task_outbox plus:
--   * dead_lettered_at: when the sweeper gave up
--   * last_error:       the ScheduleBatch error at the final attempt
--
-- No FK to jobs/tasks deliberately — by the time a row is dead-lettered
-- the underlying job may already be archived or deleted, and we still
-- want the forensic trail.

CREATE TABLE IF NOT EXISTS public.task_outbox_dead (
  id               BIGSERIAL        PRIMARY KEY,
  original_id      BIGINT           NOT NULL,
  task_id          UUID             NOT NULL,
  job_id           UUID             NOT NULL,
  page_id          INT              NOT NULL,
  host             TEXT             NOT NULL,
  path             TEXT             NOT NULL,
  priority         DOUBLE PRECISION NOT NULL,
  retry_count      INT              NOT NULL DEFAULT 0,
  source_type      TEXT             NOT NULL,
  source_url       TEXT             NOT NULL DEFAULT '',
  run_at           TIMESTAMPTZ      NOT NULL,
  attempts         INT              NOT NULL,
  created_at       TIMESTAMPTZ      NOT NULL,
  dead_lettered_at TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
  last_error       TEXT             NOT NULL DEFAULT ''
);

-- Triage indexes. Lookups by job (what jobs produced dead letters?) and
-- by dead_lettered_at (recent activity) are the expected queries.
CREATE INDEX IF NOT EXISTS idx_task_outbox_dead_job_id
  ON public.task_outbox_dead (job_id);

CREATE INDEX IF NOT EXISTS idx_task_outbox_dead_dead_lettered_at
  ON public.task_outbox_dead (dead_lettered_at DESC);
