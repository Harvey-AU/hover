-- Drop redundant and unused indexes on the tasks table.
--
-- Background: tasks has 17 indexes. Every status transition (pending→running,
-- running→completed, etc.) defeats PostgreSQL's HOT (Heap-Only Tuple)
-- optimisation because idx_tasks_job_id_status is a full non-partial index
-- that includes the status column. When HOT is defeated, all 17 indexes are
-- maintained on every update — not just the ones that changed.
--
-- Dropping these 7 indexes:
--   1. Re-enables HOT for status-only updates (idx_tasks_job_id_status removal)
--   2. Removes ~63MB of index storage
--   3. Reduces index maintenance overhead on every task status transition
--
-- Each index below is either superseded by a more specific composite index,
-- unused in any query (idx_tasks_job_host, idx_tasks_running_started_at), or
-- obsolete from an earlier schema iteration (idx_tasks_pending_claim_order).

-- Full non-partial index on (job_id, status): defeats HOT on every status
-- change. Superseded by idx_tasks_claim_optimised, idx_tasks_waiting_by_job,
-- and other status-filtered partial composites. Highest-priority drop.
DROP INDEX IF EXISTS idx_tasks_job_id_status;

-- (job_id, host): host is set at insert and never mutated, but no query in
-- the codebase filters tasks by (job_id, host) — host is read in SELECT but
-- never used as a WHERE predicate. Pure maintenance overhead.
DROP INDEX IF EXISTS idx_tasks_job_host;

-- Plain (job_id): job_id never changes after insert (HOT-safe), but every
-- composite partial index already covers job_id-based lookups with status
-- filtering. Superseded by idx_tasks_waiting_by_job, idx_tasks_claim_optimised,
-- idx_tasks_job_activity_times, and the archive workflow indexes.
DROP INDEX IF EXISTS idx_tasks_job_id;

-- (job_id, status, priority_score, created_at) WHERE pending: redundant with
-- idx_tasks_claim_optimised which covers the same query pattern (GetNextTask).
DROP INDEX IF EXISTS idx_tasks_job_status_priority_pending;

-- (job_id, priority_score, created_at) WHERE pending: also redundant with
-- idx_tasks_claim_optimised.
DROP INDEX IF EXISTS idx_tasks_pending_by_job_priority;

-- (created_at) WHERE pending: original claim ordering index from initial
-- schema — no priority_score column. All claim queries now order by
-- priority_score first. Entirely obsolete.
DROP INDEX IF EXISTS idx_tasks_pending_claim_order;

-- (started_at) WHERE running: Supabase advisor suggestion, but no query
-- in the codebase filters by (status = 'running', started_at). Stuck job
-- detection uses idx_tasks_job_activity_times instead.
DROP INDEX IF EXISTS idx_tasks_running_started_at;
