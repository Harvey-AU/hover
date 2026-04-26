-- task_type: routing tag used by the dispatcher to decide which Redis
-- stream a task is enqueued onto. See docs/plans/lighthouse-performance-reports.md.
--
-- Existing rows backfill to 'crawl' so behaviour is unchanged. The
-- new value is 'lighthouse', which the dispatcher routes onto
-- stream:{jobID}:lh for the hover-analysis service to consume.
--
-- The column lives on both tasks (long-lived row) and task_outbox
-- (short-lived row deleted after dispatch) so the dispatcher can
-- decide the destination stream without a join back to tasks.

ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS task_type TEXT NOT NULL DEFAULT 'crawl';

ALTER TABLE public.task_outbox
  ADD COLUMN IF NOT EXISTS task_type TEXT NOT NULL DEFAULT 'crawl';

-- Constrain the routing values so a typo can't silently flow through to
-- the dispatcher. Adding a new task type is a one-line migration: drop
-- the constraint, re-add with the new value included.
ALTER TABLE public.tasks
  DROP CONSTRAINT IF EXISTS tasks_task_type_check;
ALTER TABLE public.tasks
  ADD CONSTRAINT tasks_task_type_check
  CHECK (task_type IN ('crawl', 'lighthouse'));

ALTER TABLE public.task_outbox
  DROP CONSTRAINT IF EXISTS task_outbox_task_type_check;
ALTER TABLE public.task_outbox
  ADD CONSTRAINT task_outbox_task_type_check
  CHECK (task_type IN ('crawl', 'lighthouse'));
