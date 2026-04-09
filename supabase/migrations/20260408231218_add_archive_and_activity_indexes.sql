-- Add indexes to reduce CPU load during:
--   1. MarkFullyArchivedJobs: two correlated EXISTS/NOT EXISTS subqueries over tasks
--   2. CleanupStuckJobs: MAX(GREATEST(started_at, completed_at)) correlated subquery per running job

-- For the NOT EXISTS check in MarkFullyArchivedJobs:
--   NOT EXISTS (SELECT 1 FROM tasks t WHERE t.job_id = jobs.id AND t.html_storage_path IS NOT NULL)
-- Without this, each candidate job requires a heap scan of all its tasks filtered by html_storage_path.
CREATE INDEX IF NOT EXISTS idx_tasks_job_has_html_storage
    ON public.tasks (job_id)
    WHERE html_storage_path IS NOT NULL;

-- For the EXISTS check in MarkFullyArchivedJobs:
--   EXISTS (SELECT 1 FROM tasks t WHERE t.job_id = jobs.id AND t.html_archived_at IS NOT NULL)
-- Allows the planner to short-circuit as soon as one archived task is found per job.
CREATE INDEX IF NOT EXISTS idx_tasks_job_html_archived
    ON public.tasks (job_id)
    WHERE html_archived_at IS NOT NULL;

-- Covering index for the stuck-job MAX timestamp scan in CleanupStuckJobs (runs every minute):
--   SELECT MAX(GREATEST(started_at, completed_at)) FROM tasks WHERE job_id = X
-- With job_id as the leading column, the planner restricts to the target job and retrieves
-- started_at and completed_at from the index without heap access.
CREATE INDEX IF NOT EXISTS idx_tasks_job_activity_times
    ON public.tasks (job_id, started_at DESC NULLS LAST, completed_at DESC NULLS LAST);
