-- Enforce one outbox row per task.
--
-- task_outbox buffers tasks between Postgres (where they are authored)
-- and the Redis ZSET (where the dispatcher picks them up). The sweeper
-- deletes rows on successful ScheduleBatch, so in the steady state each
-- task_id should appear at most once.
--
-- Adding the unique index now lets the new waiting->pending promoter
-- (internal/jobs/stream_worker.go handleOutcome) safely use
-- `INSERT ... ON CONFLICT (task_id) DO NOTHING` — otherwise a race
-- between the promoter and a concurrent EnqueueURLs path could
-- double-enqueue a task into the ZSET and cause a duplicate crawl.
--
-- The index is additive and the table is small (rows are deleted on
-- each sweep tick), so the CREATE is effectively instant.

CREATE UNIQUE INDEX IF NOT EXISTS idx_task_outbox_task_id_unique
  ON public.task_outbox (task_id);
