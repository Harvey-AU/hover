-- Fix task_outbox column types to match source tables.
--
-- task_outbox was originally declared with task_id UUID and job_id UUID,
-- but the preview/prod schema has tasks.id TEXT and jobs.id TEXT (see the
-- initial_schema migration). Under pgx's simple_protocol query mode the
-- CTE `INSERT INTO tasks ... RETURNING id` yields TEXT, which then flows
-- into task_outbox.task_id (UUID) and fails with SQLSTATE 42804:
--   "column \"task_id\" is of type uuid but expression is of type text".
--
-- This blocks every bulk URL enqueue on the producer, so the Redis flow
-- never receives work. The fix is to align task_outbox column types with
-- the upstream tables. Existing data (if any) is preserved: UUID values
-- convert cleanly to their canonical text form via the default cast.

ALTER TABLE public.task_outbox
    ALTER COLUMN task_id TYPE TEXT USING task_id::text,
    ALTER COLUMN job_id  TYPE TEXT USING job_id::text;
