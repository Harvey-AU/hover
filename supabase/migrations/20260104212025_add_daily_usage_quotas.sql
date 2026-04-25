-- Migration: Add plans table and daily usage quota system
--
-- Purpose: Implement tiered daily page limits per organisation.
-- Tasks stay in 'waiting' status when daily quota is exhausted,
-- resuming automatically when quota resets at midnight UTC.
--
-- Integration points:
-- 1. EnqueueURLs() - factors quota into available slots calculation
-- 2. GetNextTask() - checks is_org_over_daily_quota() before claiming
-- 3. promote_waiting_task_for_job() - checks quota before promotion
-- 4. Task completion (Go code) - increments daily usage counter

-- =============================================================================
-- STEP 1: Create plans table
-- =============================================================================
CREATE TABLE IF NOT EXISTS plans (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL UNIQUE,              -- 'free', 'starter', 'pro', 'business', 'enterprise'
    display_name TEXT NOT NULL,             -- 'Free', 'Starter', etc.
    daily_page_limit INTEGER NOT NULL,      -- Pages per day (500, 2000, 5000, etc.)
    monthly_price_cents INTEGER NOT NULL,   -- Price in cents (0, 5000, 8000, etc.)
    features JSONB DEFAULT '{}',            -- Future: feature flags
    is_active BOOLEAN DEFAULT TRUE,         -- Can new orgs subscribe to this plan?
    sort_order INTEGER DEFAULT 0,           -- Display ordering
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

COMMENT ON TABLE plans IS
'Subscription tiers defining daily page limits and pricing.
Used by Paddle integration for subscription management.';

COMMENT ON COLUMN plans.daily_page_limit IS
'Maximum pages that can be processed per day per organisation. Resets at midnight UTC.';

-- Seed default plans
INSERT INTO plans (name, display_name, daily_page_limit, monthly_price_cents, sort_order) VALUES
    ('free', 'Free', 500, 0, 0),
    ('starter', 'Starter', 2000, 5000, 10),
    ('pro', 'Pro', 5000, 8000, 20),
    ('business', 'Business', 100000, 15000, 30),
    ('enterprise', 'Enterprise', 1000000, 40000, 40)
ON CONFLICT (name) DO NOTHING;

-- =============================================================================
-- STEP 2: Add plan_id and quota_exhausted_until to organisations
-- =============================================================================
-- Create function to get free plan ID (required because DEFAULT cannot use subquery)
CREATE OR REPLACE FUNCTION get_free_plan_id()
RETURNS UUID AS $$
    SELECT id FROM plans WHERE name = 'free' LIMIT 1;
$$ LANGUAGE SQL STABLE;

ALTER TABLE organisations
ADD COLUMN IF NOT EXISTS plan_id UUID REFERENCES plans(id);

ALTER TABLE organisations
ADD COLUMN IF NOT EXISTS quota_exhausted_until TIMESTAMPTZ;

COMMENT ON COLUMN organisations.quota_exhausted_until IS
'When set, indicates the org has exhausted their daily quota until this timestamp.
NULL means quota is available. Workers skip jobs for blocked orgs.
Typically set to next midnight UTC. Cleared by periodic quota reset check.';

-- Default all existing organisations to free plan
UPDATE organisations
SET plan_id = (SELECT id FROM plans WHERE name = 'free')
WHERE plan_id IS NULL;

-- Make plan_id NOT NULL with default for new orgs
ALTER TABLE organisations
ALTER COLUMN plan_id SET DEFAULT get_free_plan_id();

ALTER TABLE organisations
ALTER COLUMN plan_id SET NOT NULL;

COMMENT ON COLUMN organisations.plan_id IS
'Reference to the organisation''s subscription plan. Defaults to free plan.
NOT NULL constraint ensures every org has a valid plan for quota enforcement.';

-- Indexes for efficient lookups
CREATE INDEX IF NOT EXISTS idx_organisations_plan_id ON organisations(plan_id);
CREATE INDEX IF NOT EXISTS idx_organisations_quota_blocked
ON organisations(quota_exhausted_until)
WHERE quota_exhausted_until IS NOT NULL;

-- =============================================================================
-- STEP 3: Create daily usage tracking table
-- =============================================================================
CREATE TABLE IF NOT EXISTS daily_usage (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organisation_id UUID NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,
    usage_date DATE NOT NULL,               -- The day (UTC) this usage applies to
    pages_processed INTEGER DEFAULT 0,      -- Count of completed pages
    jobs_created INTEGER DEFAULT 0,         -- Count of jobs created
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(organisation_id, usage_date)
);

COMMENT ON TABLE daily_usage IS
'Tracks daily page usage per organisation for quota enforcement.
One row per org per day. Quota resets at midnight UTC.';

CREATE INDEX IF NOT EXISTS idx_daily_usage_org_date
ON daily_usage(organisation_id, usage_date DESC);

-- =============================================================================
-- STEP 4: Helper function - next_midnight_utc
-- =============================================================================
CREATE OR REPLACE FUNCTION next_midnight_utc()
RETURNS TIMESTAMPTZ
LANGUAGE sql
STABLE
AS $$
    SELECT date_trunc('day', NOW() AT TIME ZONE 'UTC' + INTERVAL '1 day') AT TIME ZONE 'UTC';
$$;

COMMENT ON FUNCTION next_midnight_utc IS
'Returns the next midnight UTC timestamp. Uses explicit UTC handling.';

-- =============================================================================
-- STEP 5: get_daily_quota_remaining - accounts for in-flight tasks
-- =============================================================================
CREATE OR REPLACE FUNCTION get_daily_quota_remaining(p_org_id UUID)
RETURNS INTEGER
LANGUAGE plpgsql
STABLE
AS $$
DECLARE
    v_limit INTEGER;
    v_used INTEGER;
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

    -- Count in-flight tasks (pending + running) for this org's jobs
    SELECT COUNT(*) INTO v_in_flight
    FROM tasks t
    JOIN jobs j ON t.job_id = j.id
    WHERE j.organisation_id = p_org_id
      AND t.status IN ('pending', 'running');

    RETURN GREATEST(0, v_limit - v_used - v_in_flight);
END;
$$;

COMMENT ON FUNCTION get_daily_quota_remaining IS
'Returns the number of pages remaining in the organisation''s daily quota.
Accounts for both completed usage and in-flight tasks (pending + running).
Used by EnqueueURLs to prevent over-queueing.';

-- =============================================================================
-- STEP 6: is_org_over_daily_quota - checks completed pages only
-- =============================================================================
CREATE OR REPLACE FUNCTION is_org_over_daily_quota(p_org_id UUID)
RETURNS BOOLEAN
LANGUAGE plpgsql
STABLE
AS $$
DECLARE
    v_limit INTEGER;
    v_used INTEGER;
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
            RETURN FALSE;  -- No free plan exists, not over quota
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

    RETURN v_used >= v_limit;
END;
$$;

COMMENT ON FUNCTION is_org_over_daily_quota IS
'Returns TRUE if the organisation has processed >= their daily limit.
Only counts completed pages (pages_processed), not pending/running tasks.
Used by GetNextTask as the last line of defence against over-processing.';

-- =============================================================================
-- STEP 7: is_org_quota_blocked - checks the blocking flag
-- =============================================================================
CREATE OR REPLACE FUNCTION is_org_quota_blocked(p_org_id UUID)
RETURNS BOOLEAN
LANGUAGE sql
STABLE
AS $$
    SELECT EXISTS (
        SELECT 1 FROM organisations
        WHERE id = p_org_id
          AND quota_exhausted_until IS NOT NULL
          AND quota_exhausted_until >= NOW()
    );
$$;

COMMENT ON FUNCTION is_org_quota_blocked IS
'Returns TRUE if the organisation is currently quota-blocked via the flag.
Used by workers and scaling calculations to skip blocked orgs.';

-- =============================================================================
-- STEP 8: increment_daily_usage - with SECURITY DEFINER for RLS bypass
-- =============================================================================
CREATE OR REPLACE FUNCTION increment_daily_usage(p_org_id UUID, p_pages INTEGER DEFAULT 1)
RETURNS VOID
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_limit INTEGER;
    v_new_usage INTEGER;
BEGIN
    -- Upsert daily usage
    INSERT INTO daily_usage (organisation_id, usage_date, pages_processed, updated_at)
    VALUES (p_org_id, (NOW() AT TIME ZONE 'UTC')::DATE, p_pages, NOW())
    ON CONFLICT (organisation_id, usage_date)
    DO UPDATE SET
        pages_processed = daily_usage.pages_processed + p_pages,
        updated_at = NOW()
    RETURNING pages_processed INTO v_new_usage;

    -- Get the org's plan limit
    SELECT p.daily_page_limit INTO v_limit
    FROM organisations o
    JOIN plans p ON o.plan_id = p.id
    WHERE o.id = p_org_id;

    -- If usage has hit or exceeded limit, set quota_exhausted_until
    IF v_limit IS NOT NULL AND v_new_usage >= v_limit THEN
        UPDATE organisations
        SET quota_exhausted_until = next_midnight_utc()
        WHERE id = p_org_id
          AND quota_exhausted_until IS NULL;
    END IF;
END;
$$;

COMMENT ON FUNCTION increment_daily_usage IS
'Atomically increments the daily page usage counter for an organisation.
Uses SECURITY DEFINER to bypass RLS when called from application code.
Sets quota_exhausted_until when limit is reached.';

-- =============================================================================
-- STEP 9: get_organisation_usage_stats - for dashboard display
-- =============================================================================
CREATE OR REPLACE FUNCTION get_organisation_usage_stats(p_org_id UUID)
RETURNS TABLE(
    daily_limit INTEGER,
    daily_used INTEGER,
    daily_remaining INTEGER,
    plan_name TEXT,
    plan_display_name TEXT,
    reset_time TIMESTAMPTZ
)
LANGUAGE plpgsql
STABLE
AS $$
DECLARE
    v_limit INTEGER;
    v_used INTEGER;
    v_plan_name TEXT;
    v_plan_display TEXT;
BEGIN
    SELECT p.daily_page_limit, p.name, p.display_name
    INTO v_limit, v_plan_name, v_plan_display
    FROM organisations o
    JOIN plans p ON o.plan_id = p.id
    WHERE o.id = p_org_id;

    IF v_limit IS NULL THEN
        v_limit := 0;
        v_plan_name := 'none';
        v_plan_display := 'No Plan';
    END IF;

    SELECT COALESCE(du.pages_processed, 0) INTO v_used
    FROM daily_usage du
    WHERE du.organisation_id = p_org_id
      AND du.usage_date = (NOW() AT TIME ZONE 'UTC')::DATE;

    IF v_used IS NULL THEN
        v_used := 0;
    END IF;

    daily_limit := v_limit;
    daily_used := v_used;
    daily_remaining := GREATEST(0, v_limit - v_used);
    plan_name := v_plan_name;
    plan_display_name := v_plan_display;
    reset_time := next_midnight_utc();

    RETURN NEXT;
END;
$$;

COMMENT ON FUNCTION get_organisation_usage_stats IS
'Returns comprehensive usage statistics for dashboard display.
Uses UTC dates for consistency.';

-- =============================================================================
-- STEP 10: promote_waiting_task_for_job - quota-aware task promotion
-- =============================================================================
DROP FUNCTION IF EXISTS promote_waiting_task_for_job(UUID);
DROP FUNCTION IF EXISTS promote_waiting_task_for_job(TEXT);

CREATE FUNCTION promote_waiting_task_for_job(p_job_id TEXT)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    v_org_id UUID;
    v_quota_remaining INTEGER;
    v_task_id UUID;
BEGIN
    -- Get the organisation for this job
    SELECT o.id INTO v_org_id
    FROM jobs j
    JOIN organisations o ON j.organisation_id = o.id
    WHERE j.id = p_job_id;

    IF v_org_id IS NULL THEN
        -- Job has no organisation, allow promotion (legacy behaviour)
        NULL;
    ELSE
        -- Check quota
        v_quota_remaining := get_daily_quota_remaining(v_org_id);

        IF v_quota_remaining <= 0 THEN
            UPDATE organisations
            SET quota_exhausted_until = next_midnight_utc()
            WHERE id = v_org_id
              AND quota_exhausted_until IS NULL;
            RETURN;
        END IF;
    END IF;

    -- Promote highest priority waiting task to pending
    UPDATE tasks
    SET status = 'pending'
    WHERE id = (
        SELECT t.id
        FROM tasks t
        INNER JOIN jobs j ON t.job_id = j.id
        WHERE t.job_id = p_job_id
          AND t.status = 'waiting'
          AND j.status = 'running'
          AND (j.concurrency IS NULL OR j.concurrency = 0 OR j.running_tasks < j.concurrency)
        ORDER BY t.priority_score DESC, t.created_at ASC
        LIMIT 1
        FOR UPDATE SKIP LOCKED
    )
    RETURNING id INTO v_task_id;

    -- NOTE: Daily usage is NOT incremented here
    -- Usage is incremented when tasks COMPLETE (in Go batch.go)
END;
$$;

COMMENT ON FUNCTION promote_waiting_task_for_job(TEXT) IS
'Promotes one waiting task to pending. Checks quota before promotion.
Quota increments on task completion, not promotion.';

-- =============================================================================
-- STEP 11: clear_expired_quota_blocks - for quota reset monitor
-- =============================================================================
CREATE OR REPLACE FUNCTION clear_expired_quota_blocks()
RETURNS TABLE(organisation_id UUID)
LANGUAGE plpgsql
AS $$
BEGIN
    RETURN QUERY
    UPDATE organisations
    SET quota_exhausted_until = NULL
    WHERE quota_exhausted_until IS NOT NULL
      AND quota_exhausted_until < NOW()
    RETURNING id AS organisation_id;
END;
$$;

COMMENT ON FUNCTION clear_expired_quota_blocks IS
'Clears quota_exhausted_until for orgs where the block has expired.
Returns the IDs of orgs that were unblocked. Called periodically by task monitor.';

-- =============================================================================
-- STEP 12: promote_waiting_tasks_for_org - batch promotion after quota reset
-- =============================================================================
CREATE OR REPLACE FUNCTION promote_waiting_tasks_for_org(p_org_id UUID, p_limit INTEGER DEFAULT 100)
RETURNS INTEGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_promoted INTEGER := 0;
    v_quota_remaining INTEGER;
    v_task_id UUID;
    v_max_to_promote INTEGER;
BEGIN
    v_quota_remaining := get_daily_quota_remaining(p_org_id);
    v_max_to_promote := LEAST(p_limit, v_quota_remaining);

    IF v_max_to_promote <= 0 THEN
        RETURN 0;
    END IF;

    FOR v_task_id IN
        SELECT t.id
        FROM tasks t
        INNER JOIN jobs j ON t.job_id = j.id
        WHERE j.organisation_id = p_org_id
          AND t.status = 'waiting'
          AND j.status = 'running'
          AND (j.concurrency IS NULL OR j.concurrency = 0 OR
               j.running_tasks + j.pending_tasks < j.concurrency)
        ORDER BY t.priority_score DESC, t.created_at ASC
        LIMIT v_max_to_promote
        FOR UPDATE OF t SKIP LOCKED
    LOOP
        UPDATE tasks SET status = 'pending' WHERE id = v_task_id;
        v_promoted := v_promoted + 1;
    END LOOP;

    -- NOTE: Don't increment usage here - it happens on completion
    RETURN v_promoted;
END;
$$;

COMMENT ON FUNCTION promote_waiting_tasks_for_org IS
'Promotes waiting tasks to pending for an organisation after quota resets.
Respects both quota limits and job concurrency limits.';

-- =============================================================================
-- STEP 13: get_quota_available_slots - for EnqueueURLs
-- =============================================================================
CREATE OR REPLACE FUNCTION get_quota_available_slots(p_org_id UUID, p_job_concurrency_slots INTEGER)
RETURNS INTEGER
LANGUAGE plpgsql
STABLE
AS $$
DECLARE
    v_quota_remaining INTEGER;
BEGIN
    v_quota_remaining := get_daily_quota_remaining(p_org_id);
    RETURN LEAST(p_job_concurrency_slots, v_quota_remaining);
END;
$$;

COMMENT ON FUNCTION get_quota_available_slots IS
'Returns the effective available slots considering both job concurrency and daily quota.
Used by EnqueueURLs to determine how many tasks can be set to pending.';

-- =============================================================================
-- STEP 14: Enable RLS on new tables
-- =============================================================================
ALTER TABLE plans ENABLE ROW LEVEL SECURITY;
ALTER TABLE daily_usage ENABLE ROW LEVEL SECURITY;

-- Plans are readable by all authenticated users (for pricing page)
DROP POLICY IF EXISTS "Plans are publicly readable" ON plans;
CREATE POLICY "Plans are publicly readable" ON plans
    FOR SELECT USING (TRUE);

-- Daily usage readable by org members
DROP POLICY IF EXISTS "Users can view their organisation usage" ON daily_usage;
CREATE POLICY "Users can view their organisation usage" ON daily_usage
    FOR SELECT USING (
        organisation_id IN (
            SELECT om.organisation_id
            FROM organisation_members om
            WHERE om.user_id = auth.uid()
        )
    );

-- Daily usage is modified by service role only (via functions)
DROP POLICY IF EXISTS "Service role can manage usage" ON daily_usage;
CREATE POLICY "Service role can manage usage" ON daily_usage
    FOR ALL USING (auth.jwt() ->> 'role' = 'service_role');

-- =============================================================================
-- STEP 15: Create monitoring view
-- =============================================================================
DROP VIEW IF EXISTS organisation_quota_status;
CREATE VIEW organisation_quota_status AS
SELECT
    o.id AS organisation_id,
    o.name AS organisation_name,
    p.name AS plan_name,
    p.display_name AS plan_display_name,
    p.daily_page_limit,
    COALESCE(du.pages_processed, 0) AS pages_used_today,
    GREATEST(0, p.daily_page_limit - COALESCE(du.pages_processed, 0)) AS pages_remaining_today,
    ROUND(COALESCE(du.pages_processed, 0)::NUMERIC / NULLIF(p.daily_page_limit, 0) * 100, 1) AS usage_percentage,
    next_midnight_utc() AS resets_at
FROM organisations o
JOIN plans p ON o.plan_id = p.id
LEFT JOIN daily_usage du ON du.organisation_id = o.id
    AND du.usage_date = (NOW() AT TIME ZONE 'UTC')::DATE;

COMMENT ON VIEW organisation_quota_status IS
'Current quota status for all organisations. Uses UTC dates for consistency.';
