-- Replace single-task promotion with a batch variant that promotes up to N tasks.
--
-- Background: promote_waiting_task_for_job(TEXT) promotes exactly one waiting task
-- per call. DecrementRunningTasksBy may free multiple slots at once (e.g. three tasks
-- complete in one batch flush) but previously called the single-task function just
-- once, leaving N-1 slots empty. Additionally, the old function checked
-- j.running_tasks < j.concurrency, which was stale at call time because the running
-- counter is decremented asynchronously — causing most calls to be no-ops under load.
--
-- Fix: promote_waiting_tasks_for_job(TEXT, INTEGER) takes an explicit slot count, skips
-- the stale running_tasks check entirely (the caller computed the count from the number
-- of tasks that actually completed), and promotes all N eligible waiting tasks in one
-- UPDATE using a CTE with FOR UPDATE SKIP LOCKED for concurrent safety.

CREATE OR REPLACE FUNCTION promote_waiting_tasks_for_job(p_job_id TEXT, p_slots INTEGER)
RETURNS INTEGER LANGUAGE plpgsql SET search_path = public AS $$
DECLARE
    promoted INTEGER;
BEGIN
    IF p_slots <= 0 THEN
        RETURN 0;
    END IF;

    -- Select up to p_slots waiting tasks by priority, lock them without blocking
    -- concurrent GetNextTask callers (SKIP LOCKED), then promote in one statement.
    WITH to_promote AS (
        SELECT id
        FROM tasks
        WHERE job_id = p_job_id
          AND status = 'waiting'
        ORDER BY priority_score DESC, created_at ASC
        LIMIT p_slots
        FOR UPDATE SKIP LOCKED
    )
    UPDATE tasks
    SET status = 'pending'
    WHERE id IN (SELECT id FROM to_promote);

    GET DIAGNOSTICS promoted = ROW_COUNT;
    RETURN promoted;
END;
$$;

COMMENT ON FUNCTION promote_waiting_tasks_for_job(TEXT, INTEGER) IS
'Promotes up to p_slots waiting tasks to pending for the given job.
The caller supplies the exact number of slots freed (e.g. tasks completed this batch),
so no stale running_tasks join is needed. FOR UPDATE SKIP LOCKED prevents
double-promotion with concurrent callers.';
