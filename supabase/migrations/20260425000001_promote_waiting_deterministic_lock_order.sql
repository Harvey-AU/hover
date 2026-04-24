-- Make promote_waiting_with_outbox lock task rows in deterministic order.
--
-- Background (HOVER-K2):
--   Sentry shows persistent 40P01 ("deadlock detected") on attempt 5/5
--   inside DbQueue.PromoteWaitingToPending. The function updates a set of
--   `tasks` rows whose AFTER trigger (trg_update_job_queue_counters) then
--   updates the parent `jobs` row. Because the picked CTE only orders by
--   priority_score / created_at, two concurrent promoters (different jobs,
--   or the same job retrying) can lock task rows in different sequences,
--   and downstream lock ordering becomes non-deterministic.
--
--   Adding `ORDER BY id` after the priority/created sort tie-breakers makes
--   the outer UPDATE always touch task rows in the same order across
--   concurrent transactions, so the row-lock graph becomes acyclic and the
--   40P01 cannot form.
--
-- Semantics preserved: the new ORDER BY uses the existing priority and
-- created_at as primary keys; `id` is only a tie-breaker, so the same set
-- of tasks gets picked. FOR UPDATE SKIP LOCKED still avoids contention on
-- the pick. ON CONFLICT (task_id) DO NOTHING in the outbox insert is
-- unchanged.

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
    'against the trg_update_job_queue_counters AFTER trigger.';
