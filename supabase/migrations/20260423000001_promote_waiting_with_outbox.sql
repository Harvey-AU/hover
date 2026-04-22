-- Atomic "promote waiting tasks and enqueue outbox rows" helper.
--
-- Replaces the two-statement path in DbQueue.PromoteWaitingToPending:
--   1. UPDATE tasks ... RETURNING 10 columns
--   2. INSERT INTO task_outbox SELECT unnest(...arrays...)
--
-- Running both legs inside a single server-side CTE removes:
--   - one round-trip
--   - ten column values over the wire per promoted row
--   - ten pq.Array allocations plus the outbound binary encode
--
-- At 4k completions/min the old path was averaging ~43 ms and dominated
-- the bulk-lane EMA, which tripped the DB pressure controller's 80 ms
-- high mark and throttled concurrency down to the 30 floor. Collapsing
-- to a single round-trip returns per-call cost to the ~10-20 ms range.
--
-- Semantics preserved:
--   * FOR UPDATE SKIP LOCKED on the pick so concurrent promoters for the
--     same job never collide on the same waiting row.
--   * ON CONFLICT (task_id) DO NOTHING on the outbox insert to tolerate
--     the race with EnqueueURLs placing an outbox row for the same task.
--   * Return value is the count of waiting->pending transitions, not the
--     count of newly-inserted outbox rows (unchanged from the Go path).
--
-- Safe to re-run; uses CREATE OR REPLACE.

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

COMMENT ON FUNCTION promote_waiting_with_outbox(UUID, INTEGER) IS
    'Atomically promote up to p_slots waiting tasks for a job to pending '
    'and enqueue corresponding task_outbox rows. Called from DbQueue.'
    'PromoteWaitingToPending on every stream-worker completion.';
