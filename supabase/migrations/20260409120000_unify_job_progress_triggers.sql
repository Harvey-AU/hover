-- Unify job progress triggers: drop legacy full-scan trigger, add O(1) job
-- status transition to update_job_counters(), narrow queue-counter trigger.
--
-- Root cause of 100% Supabase CPU: trigger_update_job_progress fires on every
-- task status change and runs a full COUNT(*) scan across all tasks for the job
-- (3× subqueries: completed, failed, skipped). The update_job_counters()
-- function added in 20260409111417 already handles counters and progress with
-- O(1) incremental deltas, but trigger_update_job_progress was never removed.
-- Both triggers fired simultaneously, so every status change produced:
--   • 2× UPDATE on the jobs row (hot-row lock contention)
--   • 3× full tasks table scans (sequential scan under concurrent workers)
--   • realtime WAL amplification (REPLICA IDENTITY FULL × 2 UPDATEs)
--
-- This migration:
--   1. Extends update_job_counters() with the missing job status transition
--      (running → completed, preserving cancelled/failed terminal states)
--   2. Drops trigger_update_job_progress and update_job_progress()
--   3. Narrows trg_update_job_queue_counters from AFTER UPDATE (all columns)
--      to AFTER UPDATE OF status — eliminates function-call overhead on
--      metadata-only updates (response_time, priority_score, etc.)

-- Step 1: Extend update_job_counters() with job status transition.
-- This is a drop-in replacement for the version in 20260409111417 — all
-- counter logic is identical; only the jobs UPDATE gains a status column.
CREATE OR REPLACE FUNCTION update_job_counters()
RETURNS TRIGGER LANGUAGE plpgsql SET search_path = public AS $$
DECLARE
    v_completed_delta INTEGER;
    v_failed_delta    INTEGER;
    v_skipped_delta   INTEGER;
BEGIN
    -- INSERT is handled by trg_job_counters_insert (statement-level).
    IF TG_OP = 'INSERT' THEN
        RETURN NEW;
    END IF;

    IF TG_OP = 'UPDATE' THEN
        -- Short-circuit: no work if status value is unchanged.
        IF OLD.status = NEW.status THEN
            RETURN NEW;
        END IF;

        -- Compute incremental deltas — O(1), no subqueries.
        v_completed_delta := CASE WHEN NEW.status = 'completed' THEN 1
                                  WHEN OLD.status = 'completed' THEN -1
                                  ELSE 0 END;
        v_failed_delta    := CASE WHEN NEW.status = 'failed'    THEN 1
                                  WHEN OLD.status = 'failed'    THEN -1
                                  ELSE 0 END;
        v_skipped_delta   := CASE WHEN NEW.status = 'skipped'   THEN 1
                                  WHEN OLD.status = 'skipped'   THEN -1
                                  ELSE 0 END;

        -- Skip the UPDATE entirely when no terminal counters change and this is
        -- not the task's first transition to running.
        IF v_completed_delta = 0 AND v_failed_delta = 0 AND v_skipped_delta = 0
           AND (NEW.status != 'running' OR OLD.started_at IS NOT NULL)
        THEN
            RETURN NEW;
        END IF;

        UPDATE jobs
        SET
            completed_tasks = GREATEST(0, completed_tasks + v_completed_delta),
            failed_tasks    = GREATEST(0, failed_tasks    + v_failed_delta),
            skipped_tasks   = GREATEST(0, skipped_tasks   + v_skipped_delta),
            progress = CASE
                WHEN total_tasks > 0
                 AND (total_tasks - GREATEST(0, skipped_tasks  + v_skipped_delta)) > 0
                THEN (
                    (GREATEST(0, completed_tasks + v_completed_delta)
                     + GREATEST(0, failed_tasks  + v_failed_delta))::REAL
                    / (total_tasks - GREATEST(0, skipped_tasks + v_skipped_delta))::REAL
                ) * 100.0
                ELSE 0.0
            END,
            started_at = CASE
                WHEN started_at IS NULL AND NEW.status = 'running' THEN NOW()
                ELSE started_at
            END,
            completed_at = CASE
                WHEN NEW.status IN ('completed', 'failed', 'skipped')
                 AND completed_at IS NULL
                 AND (
                     GREATEST(0, completed_tasks + v_completed_delta)
                     + GREATEST(0, failed_tasks  + v_failed_delta)
                     + GREATEST(0, skipped_tasks + v_skipped_delta)
                 ) >= total_tasks
                THEN NOW()
                ELSE completed_at
            END,
            -- Transition job to 'completed' once all tasks reach terminal states.
            -- Preserves 'cancelled' and 'failed' terminal states — a cancel-then-
            -- complete race must not overwrite the cancelled status
            -- (see 20251224111425_fix_cancel_job_status_preservation.sql).
            status = CASE
                WHEN status IN ('cancelled', 'failed') THEN status
                WHEN NEW.status IN ('completed', 'failed', 'skipped')
                 AND (
                     GREATEST(0, completed_tasks + v_completed_delta)
                     + GREATEST(0, failed_tasks  + v_failed_delta)
                     + GREATEST(0, skipped_tasks + v_skipped_delta)
                 ) >= total_tasks
                 AND total_tasks > 0
                THEN 'completed'
                ELSE status
            END
        WHERE id = NEW.job_id;

    ELSIF TG_OP = 'DELETE' THEN
        v_completed_delta := CASE WHEN OLD.status = 'completed' THEN 1 ELSE 0 END;
        v_failed_delta    := CASE WHEN OLD.status = 'failed'    THEN 1 ELSE 0 END;
        v_skipped_delta   := CASE WHEN OLD.status = 'skipped'   THEN 1 ELSE 0 END;

        UPDATE jobs
        SET
            total_tasks     = GREATEST(0, total_tasks     - 1),
            completed_tasks = GREATEST(0, completed_tasks - v_completed_delta),
            failed_tasks    = GREATEST(0, failed_tasks    - v_failed_delta),
            skipped_tasks   = GREATEST(0, skipped_tasks   - v_skipped_delta),
            sitemap_tasks   = CASE
                WHEN OLD.source_type = 'sitemap' THEN GREATEST(0, sitemap_tasks - 1)
                ELSE sitemap_tasks
            END,
            found_tasks     = CASE
                WHEN OLD.source_type IS DISTINCT FROM 'sitemap'
                THEN GREATEST(0, found_tasks - 1)
                ELSE found_tasks
            END,
            progress = CASE
                WHEN GREATEST(0, total_tasks - 1) > 0
                 AND (GREATEST(0, total_tasks - 1)
                      - GREATEST(0, skipped_tasks - v_skipped_delta)) > 0
                THEN (
                    (GREATEST(0, completed_tasks - v_completed_delta)
                     + GREATEST(0, failed_tasks   - v_failed_delta))::REAL
                    / (GREATEST(0, total_tasks - 1)
                       - GREATEST(0, skipped_tasks - v_skipped_delta))::REAL
                ) * 100.0
                ELSE 0.0
            END
        WHERE id = OLD.job_id;
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$;

-- Step 2: Drop the legacy full-scan trigger and function.
DROP TRIGGER IF EXISTS trigger_update_job_progress ON public.tasks;
DROP FUNCTION IF EXISTS update_job_progress();

-- Step 3: Narrow trg_update_job_queue_counters from AFTER UPDATE (all columns)
-- to AFTER UPDATE OF status. The internal guard in update_job_queue_counters()
-- already short-circuits on same-status transitions, but the trigger overhead
-- (function call setup, lock acquisition) is eliminated by narrowing here.
DROP TRIGGER IF EXISTS trg_update_job_queue_counters ON public.tasks;
CREATE TRIGGER trg_update_job_queue_counters
    AFTER UPDATE OF status OR DELETE ON public.tasks
    FOR EACH ROW
    EXECUTE FUNCTION update_job_queue_counters();
