-- Replace 3x correlated COUNT(*) subqueries in update_job_counters() with
-- O(1) incremental deltas for the UPDATE case.
--
-- Background: every task status transition (pending→running, running→completed,
-- etc.) fires the update_job_counters row-level trigger. Previously, the UPDATE
-- branch ran three correlated COUNT(*) subqueries against the full tasks table
-- (one each for completed, failed, skipped), causing sequential scans under
-- lock contention — 3× per row × every concurrent worker.
--
-- Fix: compute the delta from OLD/NEW status in plpgsql local variables, then
-- apply a single O(1) UPDATE with no subqueries. Also add an early-exit guard:
-- if no terminal counter changes (and the task is not transitioning to running),
-- skip the UPDATE entirely — covering the common pending/waiting ↔ running
-- transitions that previously hit all three COUNT(*) scans for nothing.

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

        -- Skip the UPDATE entirely when no terminal counters change and
        -- this is not the first task transitioning to running (started_at).
        IF v_completed_delta = 0 AND v_failed_delta = 0 AND v_skipped_delta = 0
           AND NEW.status != 'running'
        THEN
            RETURN NEW;
        END IF;

        UPDATE jobs
        SET
            completed_tasks = GREATEST(0, completed_tasks + v_completed_delta),
            failed_tasks    = GREATEST(0, failed_tasks    + v_failed_delta),
            skipped_tasks   = GREATEST(0, skipped_tasks   + v_skipped_delta),
            -- Express progress in terms of the incoming new counter values so
            -- the denominator and numerator are consistent within this UPDATE.
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
                WHEN NEW.status IN ('completed', 'failed')
                 AND completed_at IS NULL
                 AND (
                     GREATEST(0, completed_tasks + v_completed_delta)
                     + GREATEST(0, failed_tasks  + v_failed_delta)
                     + GREATEST(0, skipped_tasks + v_skipped_delta)
                 ) >= total_tasks
                THEN NOW()
                ELSE completed_at
            END
        WHERE id = NEW.job_id;

    ELSIF TG_OP = 'DELETE' THEN
        -- Task deleted — decrement counters (unchanged from previous version).
        UPDATE jobs
        SET total_tasks = GREATEST(0, total_tasks - 1),
            completed_tasks = CASE
                WHEN OLD.status = 'completed' THEN GREATEST(0, completed_tasks - 1)
                ELSE completed_tasks
            END,
            failed_tasks = CASE
                WHEN OLD.status = 'failed' THEN GREATEST(0, failed_tasks - 1)
                ELSE failed_tasks
            END,
            skipped_tasks = CASE
                WHEN OLD.status = 'skipped' THEN GREATEST(0, skipped_tasks - 1)
                ELSE skipped_tasks
            END,
            sitemap_tasks = CASE
                WHEN OLD.source_type = 'sitemap' THEN GREATEST(0, sitemap_tasks - 1)
                ELSE sitemap_tasks
            END,
            found_tasks = CASE
                WHEN OLD.source_type != 'sitemap' OR OLD.source_type IS NULL
                THEN GREATEST(0, found_tasks - 1)
                ELSE found_tasks
            END,
            progress = CASE
                WHEN total_tasks > 0 AND (total_tasks - skipped_tasks) > 0 THEN
                    ((completed_tasks + failed_tasks)::REAL
                     / (total_tasks - skipped_tasks)::REAL) * 100.0
                ELSE 0.0
            END
        WHERE id = OLD.job_id;
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$;
