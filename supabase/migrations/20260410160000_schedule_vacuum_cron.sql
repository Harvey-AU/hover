-- Migration: schedule daily VACUUM ANALYZE via pg_cron
--
-- Autovacuum cannot keep pace during heavy crawl load — we observed 423K dead
-- tuples on the tasks table after a large job run. A scheduled vacuum at low
-- traffic time (03:00 AEST = 17:00 UTC) provides a reliable safety net.
--
-- Requires pg_cron extension (enabled in Supabase dashboard).
-- Uses cron.schedule() which upserts by name, so re-running is safe.

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_cron') THEN
    PERFORM cron.schedule(
      'vacuum-tasks-jobs-pages',
      '0 17 * * *',
      'VACUUM ANALYZE tasks; VACUUM ANALYZE jobs; VACUUM ANALYZE pages;'
    );
  END IF;
END $$;
