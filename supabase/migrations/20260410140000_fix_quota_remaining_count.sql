-- Migration: fix get_daily_quota_remaining performance
--
-- The previous implementation counted in-flight tasks via:
--   SELECT COUNT(*) FROM tasks JOIN jobs WHERE org_id = ? AND status IN ('pending','running')
--
-- With 400k+ tasks this is a full table scan on every link-discovery enqueue call,
-- causing 2–46 second query times under load.
--
-- Fix: use the denormalised pending_tasks + running_tasks counters on the jobs row,
-- which are maintained incrementally by triggers. O(active jobs) instead of O(all tasks).

CREATE OR REPLACE FUNCTION get_daily_quota_remaining(p_org_id UUID)
RETURNS INTEGER
LANGUAGE plpgsql
STABLE
AS $$
DECLARE
    v_limit     INTEGER;
    v_used      INTEGER;
    v_in_flight INTEGER;
BEGIN
    -- Get the org's plan limit
    SELECT p.daily_page_limit INTO v_limit
    FROM organisations o
    JOIN plans p ON o.plan_id = p.id
    WHERE o.id = p_org_id;

    IF v_limit IS NULL THEN
        -- No plan found, default to free plan limit
        SELECT daily_page_limit INTO v_limit
        FROM plans
        WHERE name = 'free'
        LIMIT 1;

        IF v_limit IS NULL THEN
            RETURN 999999;  -- No free plan exists, allow unlimited
        END IF;
    END IF;

    -- Get today's completed usage (UTC date)
    SELECT COALESCE(pages_processed, 0) INTO v_used
    FROM daily_usage
    WHERE organisation_id = p_org_id
      AND usage_date = (NOW() AT TIME ZONE 'UTC')::DATE;

    IF v_used IS NULL THEN
        v_used := 0;
    END IF;

    -- Use denormalised counters on the jobs row instead of COUNT(*) on tasks.
    -- pending_tasks and running_tasks are maintained incrementally by triggers,
    -- making this O(active jobs) rather than O(all tasks).
    SELECT COALESCE(SUM(j.pending_tasks + j.running_tasks), 0) INTO v_in_flight
    FROM jobs j
    WHERE j.organisation_id = p_org_id
      AND j.status = 'running';

    RETURN GREATEST(0, v_limit - v_used - v_in_flight);
END;
$$;

COMMENT ON FUNCTION get_daily_quota_remaining IS
'Returns the number of pages remaining in the organisation''s daily quota.
Accounts for both completed usage and in-flight tasks (pending + running).
In-flight count uses denormalised job counters (O(jobs)) rather than
scanning the tasks table (O(tasks)) for performance under high load.';
