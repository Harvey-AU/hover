-- Stop promote_waiting_with_outbox from baking stale timestamps into
-- task_outbox.run_at.
--
-- Background:
--   The function previously inserted `COALESCE(t.run_at, NOW())` into
--   task_outbox.run_at. tasks.run_at is NOT NULL and, for tasks in the
--   `waiting` state, equals tasks.created_at — the task is never in
--   waiting because of a future schedule, only because the upstream
--   admission loop hasn't promoted it yet. Production held ~881k
--   waiting tasks with run_at older than 30 min (oldest > 3 days),
--   so every fresh outbox row inherited an arbitrarily old run_at.
--
--   Downstream effects:
--     * Sweeper still picks the row immediately (run_at <= NOW()), so
--       no functional dispatch delay — but ORDER BY run_at biases
--       picks toward whichever waiting task happened to be queued
--       longest, not toward the order rows entered the outbox.
--     * Probe metric `bee.broker.outbox_age_seconds` is computed as
--       NOW() - MIN(run_at), which then reports hours/days even when
--       the actual outbox dwell time is sub-second. The companion
--       commit fixes the metric to use created_at.
--
-- Fix:
--   Insert NOW() unconditionally. The outbox row's run_at semantically
--   means "earliest dispatch time" — for a fresh promote that's now.
--   Retry/back-off paths (Sweeper.bumpAttempts) keep setting future
--   run_at values; this only changes the initial-insert value.
--
-- Semantics preserved:
--   * Picked rows still ordered by priority_score DESC, created_at ASC,
--     id ASC (deadlock-safe ordering from 20260425000001).
--   * Outbox uses ON CONFLICT (task_id) DO NOTHING.
--   * Function still returns the count of waiting->pending transitions.

CREATE OR REPLACE FUNCTION promote_waiting_with_outbox(
    p_job_id UUID,
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
         ORDER BY priority_score DESC, created_at ASC, id ASC
         LIMIT p_slots
         FOR UPDATE SKIP LOCKED
    ),
    picked_ordered AS (
        SELECT id FROM picked ORDER BY id
    ),
    updated AS (
        UPDATE tasks t
           SET status = 'pending'
          FROM picked_ordered
         WHERE t.id = picked_ordered.id
     RETURNING t.id, t.job_id, t.page_id, t.host, t.path,
               t.priority_score, t.retry_count,
               t.source_type,
               COALESCE(t.source_url, '') AS source_url
    ),
    inserted AS (
        INSERT INTO task_outbox (
            task_id, job_id, page_id, host, path, priority,
            retry_count, source_type, source_url, run_at,
            attempts, created_at
        )
        SELECT id, job_id, page_id, host, path, priority_score,
               retry_count, source_type, source_url, NOW(),
               0, NOW()
          FROM updated
         ORDER BY id
        ON CONFLICT (task_id) DO NOTHING
        RETURNING 1
    )
    SELECT COUNT(*)::INTEGER INTO promoted FROM updated;

    RETURN COALESCE(promoted, 0);
END;
$$;

COMMENT ON FUNCTION promote_waiting_with_outbox(UUID, INTEGER) IS
    'Atomically promote up to p_slots waiting tasks for a job to pending '
    'and enqueue corresponding task_outbox rows. Locks task rows in id '
    'order (after priority/created tie-breakers) to keep the row-lock '
    'graph acyclic across concurrent promoters and avoid 40P01 deadlocks '
    'against the trg_update_job_queue_counters AFTER trigger. The outbox '
    'row''s run_at is set to NOW() — the parent task''s run_at is not '
    'inherited because waiting tasks always carry their created_at as '
    'run_at (no future-schedule path sets a waiting task''s run_at), so '
    'inheriting it would bake task age into the outbox dispatch time.';
