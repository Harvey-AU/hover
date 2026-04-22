-- Fix promote_waiting_with_outbox argument/column types.
--
-- The prior migration (20260423000001) declared the function with
-- p_job_id UUID and UPDATE RETURNING clauses typed as UUID, but the
-- production schema has tasks.id TEXT / tasks.job_id TEXT / jobs.id TEXT
-- (confirmed by 20240101000000_initial_schema.sql and the later
-- 20260421090000_fix_task_outbox_id_types migration which widened
-- task_outbox.task_id/job_id from UUID to TEXT to match).
--
-- Result on worker startup: every call landed on
--   ERROR: operator does not exist: text = uuid (SQLSTATE 42883)
-- because "WHERE job_id = p_job_id" compared the TEXT column to a UUID
-- parameter. The Go path wraps this in an error-return, so waiting tasks
-- never got promoted to pending and the stream worker starved.
--
-- Fix: drop and recreate with TEXT throughout. CREATE OR REPLACE cannot
-- change a function's argument types, so DROP is required. IF EXISTS
-- makes this safe to re-run.
--
-- task_outbox columns (all TEXT/INT/timestamptz after 20260421090000 and
-- the inserts this function performs match those types exactly).

DROP FUNCTION IF EXISTS promote_waiting_with_outbox(UUID, INTEGER);
DROP FUNCTION IF EXISTS promote_waiting_with_outbox(TEXT, INTEGER);

CREATE OR REPLACE FUNCTION promote_waiting_with_outbox(
    p_job_id TEXT,
    p_slots INTEGER
)
RETURNS INTEGER
LANGUAGE plpgsql
SET search_path = public
AS $$
DECLARE
    promoted INTEGER;
BEGIN
    IF p_slots <= 0 THEN
        RETURN 0;
    END IF;

    WITH picked AS (
        SELECT id
          FROM tasks
         WHERE job_id = p_job_id
           AND status = 'waiting'
         ORDER BY priority_score DESC, created_at ASC
         LIMIT p_slots
         FOR UPDATE SKIP LOCKED
    ),
    updated AS (
        UPDATE tasks t
           SET status = 'pending'
          FROM picked
         WHERE t.id = picked.id
     RETURNING t.id, t.job_id, t.page_id, t.host, t.path,
               t.priority_score, t.retry_count,
               t.source_type,
               COALESCE(t.source_url, '') AS source_url,
               COALESCE(t.run_at, NOW())  AS run_at
    ),
    inserted AS (
        INSERT INTO task_outbox (
            task_id, job_id, page_id, host, path, priority,
            retry_count, source_type, source_url, run_at,
            attempts, created_at
        )
        SELECT id, job_id, page_id, host, path, priority_score,
               retry_count, source_type, source_url, run_at,
               0, NOW()
          FROM updated
        ON CONFLICT (task_id) DO NOTHING
        RETURNING 1
    )
    SELECT COUNT(*)::INTEGER INTO promoted FROM updated;

    RETURN COALESCE(promoted, 0);
END;
$$;

COMMENT ON FUNCTION promote_waiting_with_outbox(TEXT, INTEGER) IS
    'Atomically promote up to p_slots waiting tasks for a job to pending '
    'and enqueue corresponding task_outbox rows. Called from DbQueue.'
    'PromoteWaitingToPending on every stream-worker completion.';
