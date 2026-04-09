-- Merge the two row-level INSERT triggers into a single statement-level trigger.
--
-- Background: EnqueueURLs inserts tasks in batches (often 100–500 rows per
-- call). With two row-level triggers (update_job_counters and
-- update_job_queue_counters), each INSERT fires two UPDATE jobs statements —
-- one per trigger, per row. A 500-row batch therefore emits 1,000 UPDATE jobs
-- statements under the same job lock, creating heavy contention.
--
-- Fix: add a single AFTER INSERT FOR EACH STATEMENT trigger that aggregates
-- all deltas from the inserted batch and applies them in one UPDATE per job.
-- Strip the INSERT branch from both existing row-level functions so they
-- become no-ops for INSERT (UPDATE and DELETE paths are unchanged).
--
-- Side benefit: the new statement-level function now increments skipped_tasks
-- on INSERT, which the old row-level function never did. This makes
-- j.total_tasks - j.skipped_tasks a reliable replacement for the correlated
-- COUNT(*) subquery used in EnqueueURLs.

-- 1. Statement-level handler for batch inserts --

CREATE OR REPLACE FUNCTION update_job_counters_insert_batch()
RETURNS TRIGGER LANGUAGE plpgsql SET search_path = public AS $$
BEGIN
    -- Aggregate all inserted rows per job and apply in one UPDATE each.
    UPDATE jobs j
    SET
        total_tasks   = j.total_tasks   + delta.total_count,
        sitemap_tasks = j.sitemap_tasks + delta.sitemap_count,
        found_tasks   = j.found_tasks   + delta.found_count,
        pending_tasks = j.pending_tasks + delta.pending_count,
        waiting_tasks = j.waiting_tasks + delta.waiting_count,
        skipped_tasks = j.skipped_tasks + delta.skipped_count
    FROM (
        SELECT
            job_id,
            COUNT(*)                                                 AS total_count,
            COUNT(*) FILTER (WHERE source_type = 'sitemap')         AS sitemap_count,
            COUNT(*) FILTER (WHERE source_type IS DISTINCT FROM 'sitemap') AS found_count,
            COUNT(*) FILTER (WHERE status = 'pending')              AS pending_count,
            COUNT(*) FILTER (WHERE status = 'waiting')              AS waiting_count,
            COUNT(*) FILTER (WHERE status = 'skipped')              AS skipped_count
        FROM new_tasks
        GROUP BY job_id
    ) AS delta
    WHERE j.id = delta.job_id;

    RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS trg_job_counters_insert ON public.tasks;
CREATE TRIGGER trg_job_counters_insert
    AFTER INSERT ON public.tasks
    REFERENCING NEW TABLE AS new_tasks
    FOR EACH STATEMENT
    EXECUTE FUNCTION update_job_counters_insert_batch();

-- 2. Strip INSERT branch from existing row-level functions --
-- Both functions now return immediately for INSERT; UPDATE and DELETE are unchanged.

CREATE OR REPLACE FUNCTION update_job_counters()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    -- INSERT is handled by trg_job_counters_insert (statement-level).
    IF TG_OP = 'INSERT' THEN
        RETURN NEW;
    END IF;

    IF TG_OP = 'UPDATE' THEN
        -- Task status changed - recalculate status counters from truth source
        UPDATE jobs
        SET
            completed_tasks = (
                SELECT COUNT(*) FROM tasks
                WHERE job_id = NEW.job_id AND status = 'completed'
            ),
            failed_tasks = (
                SELECT COUNT(*) FROM tasks
                WHERE job_id = NEW.job_id AND status = 'failed'
            ),
            skipped_tasks = (
                SELECT COUNT(*) FROM tasks
                WHERE job_id = NEW.job_id AND status = 'skipped'
            ),
            -- Update progress calculation
            progress = CASE
                WHEN total_tasks > 0 AND (total_tasks - skipped_tasks) > 0 THEN
                    ((completed_tasks + failed_tasks)::REAL / (total_tasks - skipped_tasks)::REAL) * 100.0
                ELSE 0.0
            END,
            -- Update timestamps when job starts/completes
            started_at = CASE
                WHEN started_at IS NULL AND NEW.status = 'running' THEN NOW()
                ELSE started_at
            END,
            completed_at = CASE
                WHEN NEW.status IN ('completed', 'failed') AND
                     completed_tasks + failed_tasks + skipped_tasks >= total_tasks THEN NOW()
                ELSE completed_at
            END
        WHERE id = NEW.job_id;

    ELSIF TG_OP = 'DELETE' THEN
        -- Task deleted - decrement counters
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
                WHEN OLD.source_type != 'sitemap' OR OLD.source_type IS NULL THEN GREATEST(0, found_tasks - 1)
                ELSE found_tasks
            END,
            -- Recalculate progress after deletion
            progress = CASE
                WHEN total_tasks > 0 AND (total_tasks - skipped_tasks) > 0 THEN
                    ((completed_tasks + failed_tasks)::REAL / (total_tasks - skipped_tasks)::REAL) * 100.0
                ELSE 0.0
            END
        WHERE id = OLD.job_id;
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$;

CREATE OR REPLACE FUNCTION update_job_queue_counters()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    pending_delta INTEGER := 0;
    waiting_delta INTEGER := 0;
BEGIN
    -- INSERT is handled by trg_job_counters_insert (statement-level).
    IF TG_OP = 'INSERT' THEN
        RETURN NEW;
    END IF;

    IF TG_OP = 'DELETE' THEN
        IF OLD.status = 'pending' THEN
            pending_delta := -1;
        ELSIF OLD.status = 'waiting' THEN
            waiting_delta := -1;
        END IF;

        IF pending_delta <> 0 OR waiting_delta <> 0 THEN
            UPDATE jobs
            SET pending_tasks = GREATEST(0, pending_tasks + pending_delta),
                waiting_tasks = GREATEST(0, waiting_tasks + waiting_delta)
            WHERE id = OLD.job_id;
        END IF;
        RETURN OLD;
    ELSE
        -- UPDATE
        IF NEW.job_id = OLD.job_id THEN
            IF NEW.status <> OLD.status THEN
                IF NEW.status = 'pending' THEN
                    pending_delta := pending_delta + 1;
                ELSIF NEW.status = 'waiting' THEN
                    waiting_delta := waiting_delta + 1;
                END IF;

                IF OLD.status = 'pending' THEN
                    pending_delta := pending_delta - 1;
                ELSIF OLD.status = 'waiting' THEN
                    waiting_delta := waiting_delta - 1;
                END IF;

                IF pending_delta <> 0 OR waiting_delta <> 0 THEN
                    UPDATE jobs
                    SET pending_tasks = GREATEST(0, pending_tasks + pending_delta),
                        waiting_tasks = GREATEST(0, waiting_tasks + waiting_delta)
                    WHERE id = NEW.job_id;
                END IF;
            END IF;
        ELSE
            -- Job reassignment: remove from old job, add to new job
            IF OLD.status = 'pending' THEN
                UPDATE jobs
                SET pending_tasks = GREATEST(0, pending_tasks - 1)
                WHERE id = OLD.job_id;
            ELSIF OLD.status = 'waiting' THEN
                UPDATE jobs
                SET waiting_tasks = GREATEST(0, waiting_tasks - 1)
                WHERE id = OLD.job_id;
            END IF;

            IF NEW.status = 'pending' THEN
                UPDATE jobs
                SET pending_tasks = pending_tasks + 1
                WHERE id = NEW.job_id;
            ELSIF NEW.status = 'waiting' THEN
                UPDATE jobs
                SET waiting_tasks = waiting_tasks + 1
                WHERE id = NEW.job_id;
            END IF;
        END IF;

        RETURN NEW;
    END IF;
END;
$$;
