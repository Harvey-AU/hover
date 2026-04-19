-- Add run_at column to tasks for durable pacing-delay persistence.
--
-- Motivation: when the Redis dispatcher reschedules a task because of a
-- domain pacing push-back (see internal/broker/dispatcher.go), the new
-- run-at time is currently only written to the Redis ZSET score. If Redis
-- is flushed or the instance is rebuilt, those pacing delays are lost and
-- tasks stampede against the paced domain.
--
-- This column mirrors the ZSET score into Postgres so pacing push-backs
-- survive a Redis flush. The Scheduler.Reschedule path is updated to
-- dual-write.
--
-- Existing rows are backfilled to created_at (their original logical run
-- time). Going forward, the column defaults to now() for new inserts.
--
-- Note: a reconcile sweep that re-seeds the ZSET from Postgres on worker
-- startup is deliberately out of scope for this PR. It requires a
-- dedicated task-lifecycle status (e.g. 'scheduled') to avoid re-adding
-- tasks that are already in a Redis Stream PEL. That work is sequenced
-- after the outbox-pattern PR.

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS run_at TIMESTAMPTZ;

UPDATE tasks SET run_at = created_at WHERE run_at IS NULL;

ALTER TABLE tasks ALTER COLUMN run_at SET NOT NULL;
ALTER TABLE tasks ALTER COLUMN run_at SET DEFAULT now();

COMMENT ON COLUMN tasks.run_at IS
    'Earliest time at which the task may be dispatched. Mirrored from the Redis broker schedule ZSET score by Scheduler.Reschedule (internal/broker/scheduler.go) so pacing push-backs survive a Redis flush.';
