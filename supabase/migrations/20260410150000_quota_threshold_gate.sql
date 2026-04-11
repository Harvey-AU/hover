-- Migration: add threshold gate to get_daily_quota_remaining
--
-- If an org has more than v_threshold pages of headroom based on completed
-- usage alone, skip the in-flight job counter sum entirely. The in-flight
-- count only matters when an org is close to their daily limit.
--
-- This avoids even the O(active jobs) SUM for orgs with plenty of quota,
-- which covers the vast majority of calls during normal operation.

CREATE OR REPLACE FUNCTION get_daily_quota_remaining(p_org_id UUID)
RETURNS INTEGER
LANGUAGE plpgsql
STABLE
AS $$
DECLARE
    v_limit     INTEGER;
    v_used      INTEGER;
    v_headroom  INTEGER;
    v_in_flight INTEGER;
    -- Only compute in-flight when within this many pages of the limit.
    -- Orgs with more headroom than this cannot exhaust quota via in-flight
    -- tasks within a short window, so the expensive check is skipped.
    v_threshold CONSTANT INTEGER := 200;
BEGIN
    -- Get the org's plan limit
    SELECT p.daily_page_limit INTO v_limit
    FROM organisations o
    JOIN plans p ON o.plan_id = p.id
    WHERE o.id = p_org_id;

    IF v_limit IS NULL THEN
        SELECT daily_page_limit INTO v_limit
        FROM plans
        WHERE name = 'free'
        LIMIT 1;

        IF v_limit IS NULL THEN
            RETURN 999999;
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

    v_headroom := v_limit - v_used;

    -- Fast path: plenty of quota remaining based on completed usage alone.
    -- Skip in-flight calculation — even at peak concurrency, in-flight tasks
    -- cannot bridge this gap in any short window.
    IF v_headroom > v_threshold THEN
        RETURN v_headroom;
    END IF;

    -- Near-limit path: subtract in-flight tasks to prevent over-queueing.
    -- Uses denormalised job counters (O(active jobs)), not a tasks table scan.
    SELECT COALESCE(SUM(j.pending_tasks + j.running_tasks), 0) INTO v_in_flight
    FROM jobs j
    WHERE j.organisation_id = p_org_id
      AND j.status = 'running';

    RETURN GREATEST(0, v_headroom - v_in_flight);
END;
$$;

COMMENT ON FUNCTION get_daily_quota_remaining IS
'Returns the number of pages remaining in the organisation''s daily quota.
Fast path: if headroom from completed usage alone exceeds 200 pages, returns
immediately without computing in-flight tasks. Near-limit path: uses
denormalised job counters (O(jobs)) rather than scanning the tasks table.';
