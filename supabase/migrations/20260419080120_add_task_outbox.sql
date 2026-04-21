-- task_outbox: durable buffer for Redis ZSET scheduling.
--
-- Rows are written in the same transaction as the corresponding
-- tasks row, so a successful tasks insert guarantees a matching
-- outbox row exists. A sweeper goroutine in the worker service
-- reads due rows, calls Scheduler.ScheduleBatch, and deletes the
-- row on success (or bumps attempts + run_at on failure).
--
-- This fixes the orphan-task risk where the fire-and-forget
-- OnTasksEnqueued callback could fail after Postgres commit,
-- leaving pending tasks that no dispatcher ever sees.

CREATE TABLE IF NOT EXISTS public.task_outbox (
  id          BIGSERIAL        PRIMARY KEY,
  task_id     UUID             NOT NULL,
  job_id      UUID             NOT NULL,
  page_id     INT              NOT NULL,
  host        TEXT             NOT NULL,
  path        TEXT             NOT NULL,
  priority    DOUBLE PRECISION NOT NULL,
  retry_count INT              NOT NULL DEFAULT 0,
  source_type TEXT             NOT NULL,
  source_url  TEXT             NOT NULL DEFAULT '',
  run_at      TIMESTAMPTZ      NOT NULL,
  attempts    INT              NOT NULL DEFAULT 0,
  created_at  TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);

-- Sweeper reads rows ordered by run_at; a plain btree on run_at
-- is enough because the table is expected to stay small (rows
-- are deleted on successful dispatch, typically within one tick).
CREATE INDEX IF NOT EXISTS idx_task_outbox_run_at
  ON public.task_outbox (run_at ASC);
