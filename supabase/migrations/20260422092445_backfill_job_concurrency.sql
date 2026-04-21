-- Backfill jobs.concurrency for jobs created while the Redis-broker merge
-- was live. PR #330 (commit 5eeae29d) replaced the pre-merge default of
-- workerPool.maxWorkers (= GNH_MAX_WORKERS = 130) with a hard-coded
-- fallbackJobConcurrency of 20. Commit 77c82e04 restored the env-driven
-- default for new jobs, but existing active rows still carry 20 and the
-- dispatcher's CanDispatch caps in-flight tasks at that stored value —
-- throttling 13 active jobs to 13 × 20 = 260 concurrent tasks (≈400/min
-- observed) instead of the full 30 × 20 = 600 stream-worker capacity.
--
-- This migration re-aligns stored concurrency with the restored default.
-- No live customers yet, so unconditional bump is safe (per project
-- CLAUDE.md: skip backfill safety guards until launch).
--
-- Scope: pending/running jobs only. Completed/failed rows are historical
-- and don't feed the dispatcher, so leaving them alone keeps the audit
-- trail intact.
UPDATE jobs
SET concurrency = 130
WHERE concurrency < 130
  AND status IN ('pending', 'running');
