-- Add columns to track HTML archived to cold storage (R2/S3/B2).
ALTER TABLE tasks
    ADD COLUMN IF NOT EXISTS html_archive_provider TEXT,
    ADD COLUMN IF NOT EXISTS html_archive_bucket TEXT,
    ADD COLUMN IF NOT EXISTS html_archive_key TEXT,
    ADD COLUMN IF NOT EXISTS html_archived_at TIMESTAMPTZ;

-- Partial index for the candidate query: tasks with HTML in hot storage
-- that have not yet been archived.
CREATE INDEX IF NOT EXISTS idx_tasks_archive_candidates
    ON tasks (job_id)
    WHERE html_storage_path IS NOT NULL
      AND html_archived_at IS NULL;
