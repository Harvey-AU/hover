-- Remove INSERT from the two row-level trigger event sets.
--
-- 20260409025105 changed update_job_counters() and update_job_queue_counters()
-- to return immediately on INSERT (now handled by the statement-level
-- trg_job_counters_insert), but left both trigger definitions firing on
-- AFTER INSERT OR ... — causing one wasted function call per inserted row.
-- Redefine both triggers to fire only on UPDATE/DELETE.

DROP TRIGGER IF EXISTS trigger_update_job_counters ON public.tasks;
CREATE TRIGGER trigger_update_job_counters
    AFTER UPDATE OF status OR DELETE ON public.tasks
    FOR EACH ROW
    EXECUTE FUNCTION update_job_counters();

DROP TRIGGER IF EXISTS trg_update_job_queue_counters ON public.tasks;
CREATE TRIGGER trg_update_job_queue_counters
    AFTER UPDATE OR DELETE ON public.tasks
    FOR EACH ROW
    EXECUTE FUNCTION update_job_queue_counters();
