-- Phase 3: convert AFTER FOR EACH ROW triggers on tasks into AFTER FOR EACH
-- STATEMENT triggers, with deterministic lock acquisition on jobs rows.
--
-- Background:
--   Two row-level triggers (trigger_update_job_counters and
--   trg_update_job_queue_counters) fire on every task status UPDATE and DELETE.
--   For a batch UPDATE that touches N tasks, each trigger fires N times — every
--   fire UPDATEs the parent jobs row, acquiring its row lock. Concurrent
--   workers whose batches span overlapping job sets (in different orders) can
--   either deadlock (40P01) or, more commonly under the current Phase 1
--   defences, serialise through jobs row locks and inflate function latency.
--
--   pg_stat_statements (prod, 2026-04-26):
--     promote_waiting_with_outbox  ─ 5.6M calls, 45ms mean, 30s max,
--     63 % of total DB time. The function fires both row-level triggers via the
--     UPDATE tasks SET status = 'pending' statement inside it. Inflated mean is
--     the symptom of structural jobs-row contention.
--
-- Fix:
--   Merge both trigger functions into two statement-level handlers (one for
--   UPDATE OF status, one for DELETE). Each:
--     1. Aggregates per-row OLD/NEW deltas into per-job deltas using transition
--        tables.
--     2. Pre-locks the affected jobs rows in id order via SELECT ... FOR UPDATE
--        ORDER BY id, eliminating the per-row lock-ordering ambiguity that
--        causes 40P01 deadlocks.
--     3. Issues a single UPDATE jobs … FROM deltas per statement, applying all
--        counter columns (completed/failed/skipped/pending/waiting/sitemap/
--        found/total/progress/started_at/completed_at/status) in one pass.
--
-- Invariants preserved (mirrored from update_job_counters() in
-- 20260409120000_unify_job_progress_triggers.sql and
-- update_job_queue_counters() in 20251109093000_add_job_queue_counters.sql):
--   * status counter deltas (completed_tasks, failed_tasks, skipped_tasks,
--     pending_tasks, waiting_tasks) computed per row from OLD.status / NEW.status.
--   * progress recomputed from post-delta counter values.
--   * started_at = NOW() iff jobs.started_at IS NULL AND any row in batch
--     transitions to status = 'running'.
--   * completed_at = NOW() iff jobs.completed_at IS NULL AND post-delta
--     terminal sum (completed + failed + skipped) >= total_tasks.
--   * status = 'completed' iff all-terminal AND total_tasks > 0 — but never
--     overwrite jobs.status when it is already 'cancelled' or 'failed'
--     (cancel-then-complete race; see 20251224111425).
--   * Cross-job moves (NEW.job_id <> OLD.job_id) decrement counters on
--     OLD.job_id and increment on NEW.job_id, matching the existing
--     update_job_queue_counters cross-job branch.
--   * DELETE branch decrements total_tasks; sitemap_tasks / found_tasks adjust
--     by source_type as in the original DELETE branch.
--   * GREATEST(0, …) clamps preserved on every counter to absorb residual
--     under-counts.

BEGIN;

-- 1. Statement-level handler for AFTER UPDATE OF status. -------------------

CREATE OR REPLACE FUNCTION update_job_counters_status_batch()
RETURNS TRIGGER LANGUAGE plpgsql SET search_path = public AS $$
BEGIN
    -- Build per-row deltas spanning both OLD and NEW sides. A cross-job move
    -- contributes a -1 entry on OLD.job_id and a +1 entry on NEW.job_id; a
    -- same-job status change contributes both at the same job_id and they
    -- aggregate to a net delta. Rows whose status AND job_id are unchanged
    -- contribute nothing (filtered by the OR predicate below).
    WITH per_row AS (
        SELECT
            o.job_id                                   AS job_id,
            CASE WHEN o.status = 'completed' THEN -1 ELSE 0 END AS completed_delta,
            CASE WHEN o.status = 'failed'    THEN -1 ELSE 0 END AS failed_delta,
            CASE WHEN o.status = 'skipped'   THEN -1 ELSE 0 END AS skipped_delta,
            CASE WHEN o.status = 'pending'   THEN -1 ELSE 0 END AS pending_delta,
            CASE WHEN o.status = 'waiting'   THEN -1 ELSE 0 END AS waiting_delta,
            FALSE                                       AS running_started
        FROM old_tasks o
        JOIN new_tasks n ON n.id = o.id
        WHERE o.status IS DISTINCT FROM n.status
           OR o.job_id IS DISTINCT FROM n.job_id

        UNION ALL

        SELECT
            n.job_id                                   AS job_id,
            CASE WHEN n.status = 'completed' THEN  1 ELSE 0 END AS completed_delta,
            CASE WHEN n.status = 'failed'    THEN  1 ELSE 0 END AS failed_delta,
            CASE WHEN n.status = 'skipped'   THEN  1 ELSE 0 END AS skipped_delta,
            CASE WHEN n.status = 'pending'   THEN  1 ELSE 0 END AS pending_delta,
            CASE WHEN n.status = 'waiting'   THEN  1 ELSE 0 END AS waiting_delta,
            (n.status = 'running')                       AS running_started
        FROM new_tasks n
        JOIN old_tasks o ON o.id = n.id
        WHERE o.status IS DISTINCT FROM n.status
           OR o.job_id IS DISTINCT FROM n.job_id
    ),
    deltas AS (
        SELECT
            job_id,
            SUM(completed_delta)::INT  AS completed_delta,
            SUM(failed_delta)::INT     AS failed_delta,
            SUM(skipped_delta)::INT    AS skipped_delta,
            SUM(pending_delta)::INT    AS pending_delta,
            SUM(waiting_delta)::INT    AS waiting_delta,
            BOOL_OR(running_started)   AS running_started
        FROM per_row
        GROUP BY job_id
    ),
    -- Acquire jobs row locks in id order, before the UPDATE. Two concurrent
    -- statement-level fires whose deltas span overlapping job sets will request
    -- the same locks in the same order, so neither deadlocks against the other.
    _locked AS (
        SELECT j.id
        FROM jobs j
        JOIN deltas d ON d.job_id = j.id
        ORDER BY j.id
        FOR UPDATE OF j
    )
    UPDATE jobs j
    SET
        completed_tasks = GREATEST(0, j.completed_tasks + d.completed_delta),
        failed_tasks    = GREATEST(0, j.failed_tasks    + d.failed_delta),
        skipped_tasks   = GREATEST(0, j.skipped_tasks   + d.skipped_delta),
        pending_tasks   = GREATEST(0, j.pending_tasks   + d.pending_delta),
        waiting_tasks   = GREATEST(0, j.waiting_tasks   + d.waiting_delta),
        progress = CASE
            WHEN j.total_tasks > 0
             AND (j.total_tasks - GREATEST(0, j.skipped_tasks + d.skipped_delta)) > 0
            THEN (
                (GREATEST(0, j.completed_tasks + d.completed_delta)
                 + GREATEST(0, j.failed_tasks  + d.failed_delta))::REAL
                / (j.total_tasks - GREATEST(0, j.skipped_tasks + d.skipped_delta))::REAL
            ) * 100.0
            ELSE 0.0
        END,
        started_at = CASE
            WHEN j.started_at IS NULL AND d.running_started THEN NOW()
            ELSE j.started_at
        END,
        completed_at = CASE
            WHEN j.completed_at IS NULL
             AND j.total_tasks > 0
             AND (
                 GREATEST(0, j.completed_tasks + d.completed_delta)
                 + GREATEST(0, j.failed_tasks  + d.failed_delta)
                 + GREATEST(0, j.skipped_tasks + d.skipped_delta)
             ) >= j.total_tasks
            THEN NOW()
            ELSE j.completed_at
        END,
        status = CASE
            WHEN j.status IN ('cancelled', 'failed') THEN j.status
            WHEN j.total_tasks > 0
             AND (
                 GREATEST(0, j.completed_tasks + d.completed_delta)
                 + GREATEST(0, j.failed_tasks  + d.failed_delta)
                 + GREATEST(0, j.skipped_tasks + d.skipped_delta)
             ) >= j.total_tasks
            THEN 'completed'
            ELSE j.status
        END
    FROM deltas d
    WHERE j.id = d.job_id
      AND j.id IN (SELECT id FROM _locked);

    RETURN NULL;
END;
$$;

-- 2. Statement-level handler for AFTER DELETE. -----------------------------

CREATE OR REPLACE FUNCTION update_job_counters_delete_batch()
RETURNS TRIGGER LANGUAGE plpgsql SET search_path = public AS $$
BEGIN
    WITH deltas AS (
        SELECT
            job_id,
            COUNT(*)::INT                                                  AS total_delta,
            COUNT(*) FILTER (WHERE status = 'completed')::INT              AS completed_delta,
            COUNT(*) FILTER (WHERE status = 'failed')::INT                 AS failed_delta,
            COUNT(*) FILTER (WHERE status = 'skipped')::INT                AS skipped_delta,
            COUNT(*) FILTER (WHERE status = 'pending')::INT                AS pending_delta,
            COUNT(*) FILTER (WHERE status = 'waiting')::INT                AS waiting_delta,
            COUNT(*) FILTER (WHERE source_type = 'sitemap')::INT           AS sitemap_delta,
            COUNT(*) FILTER (WHERE source_type IS DISTINCT FROM 'sitemap')::INT
                                                                            AS found_delta
        FROM old_tasks
        GROUP BY job_id
    ),
    _locked AS (
        SELECT j.id
        FROM jobs j
        JOIN deltas d ON d.job_id = j.id
        ORDER BY j.id
        FOR UPDATE OF j
    )
    UPDATE jobs j
    SET
        total_tasks     = GREATEST(0, j.total_tasks     - d.total_delta),
        completed_tasks = GREATEST(0, j.completed_tasks - d.completed_delta),
        failed_tasks    = GREATEST(0, j.failed_tasks    - d.failed_delta),
        skipped_tasks   = GREATEST(0, j.skipped_tasks   - d.skipped_delta),
        pending_tasks   = GREATEST(0, j.pending_tasks   - d.pending_delta),
        waiting_tasks   = GREATEST(0, j.waiting_tasks   - d.waiting_delta),
        sitemap_tasks   = GREATEST(0, j.sitemap_tasks   - d.sitemap_delta),
        found_tasks     = GREATEST(0, j.found_tasks     - d.found_delta),
        progress = CASE
            WHEN GREATEST(0, j.total_tasks - d.total_delta) > 0
             AND (GREATEST(0, j.total_tasks - d.total_delta)
                  - GREATEST(0, j.skipped_tasks - d.skipped_delta)) > 0
            THEN (
                (GREATEST(0, j.completed_tasks - d.completed_delta)
                 + GREATEST(0, j.failed_tasks  - d.failed_delta))::REAL
                / (GREATEST(0, j.total_tasks - d.total_delta)
                   - GREATEST(0, j.skipped_tasks - d.skipped_delta))::REAL
            ) * 100.0
            ELSE 0.0
        END
    FROM deltas d
    WHERE j.id = d.job_id
      AND j.id IN (SELECT id FROM _locked);

    RETURN NULL;
END;
$$;

-- 3. Replace the row-level triggers with statement-level equivalents. -------

DROP TRIGGER IF EXISTS trigger_update_job_counters    ON public.tasks;
DROP TRIGGER IF EXISTS trg_update_job_queue_counters  ON public.tasks;

-- Note: Postgres rejects a column-list (AFTER UPDATE OF status) when
-- transition tables are referenced — the trigger must fire on AFTER UPDATE.
-- The aggregation CTE inside update_job_counters_status_batch filters out
-- rows whose status AND job_id are both unchanged, so metadata-only UPDATEs
-- (response_time, priority_score, retry_count, …) trigger an empty CTE walk
-- and a no-op UPDATE, paying only one statement-level function call.
CREATE TRIGGER trg_job_counters_status_update
    AFTER UPDATE ON public.tasks
    REFERENCING OLD TABLE AS old_tasks NEW TABLE AS new_tasks
    FOR EACH STATEMENT
    EXECUTE FUNCTION update_job_counters_status_batch();

CREATE TRIGGER trg_job_counters_delete
    AFTER DELETE ON public.tasks
    REFERENCING OLD TABLE AS old_tasks
    FOR EACH STATEMENT
    EXECUTE FUNCTION update_job_counters_delete_batch();

-- 4. Retain (but no longer wired) the previous row-level functions for
--    rollback safety. They are intentionally not dropped here so that an
--    emergency revert can re-CREATE the row-level triggers without first
--    re-running the older migrations. A follow-up migration may drop them
--    once Phase 3 has soaked.
COMMENT ON FUNCTION update_job_counters() IS
    'Legacy row-level handler retained for rollback safety only. The active '
    'path is update_job_counters_status_batch() / _delete_batch() (statement-level, '
    'see migration 20260426013451).';
COMMENT ON FUNCTION update_job_queue_counters() IS
    'Legacy row-level handler retained for rollback safety only. The active '
    'path is update_job_counters_status_batch() / _delete_batch() (statement-level, '
    'see migration 20260426013451).';

COMMENT ON FUNCTION update_job_counters_status_batch() IS
    'Statement-level handler aggregating per-row task status transitions into '
    'per-job counter deltas. Acquires jobs row locks in id order to keep the '
    'lock graph acyclic across concurrent batch UPDATEs and avoid 40P01 '
    'deadlocks. Replaces the per-row trigger_update_job_counters and '
    'trg_update_job_queue_counters fires for the UPDATE OF status path.';
COMMENT ON FUNCTION update_job_counters_delete_batch() IS
    'Statement-level handler aggregating task DELETEs into per-job counter '
    'deltas. Mirrors update_job_counters_status_batch() lock ordering for '
    'symmetric deadlock-safety.';

COMMIT;
