-- Fix Supabase database linter advisories
-- Addresses: function_search_path_mutable, auth_rls_initplan, multiple_permissive_policies

-- ============================================================================
-- SECTION 1: Pin search_path on functions missing it
-- Prevents theoretical search_path hijacking (lint 0011)
-- ============================================================================

-- Trigger functions (no parameters)
ALTER FUNCTION public.set_job_started_at() SET search_path = public;
ALTER FUNCTION public.set_job_completed_at() SET search_path = public;
ALTER FUNCTION public.update_job_counters() SET search_path = public;
ALTER FUNCTION public.calculate_job_stats() SET search_path = public;
ALTER FUNCTION public.update_webflow_site_settings_updated_at() SET search_path = public;
ALTER FUNCTION public.update_job_queue_counters() SET search_path = public;
ALTER FUNCTION public.update_job_progress() SET search_path = public;
ALTER FUNCTION public.sync_slack_user_id() SET search_path = public;
ALTER FUNCTION public.cleanup_slack_vault_secret() SET search_path = public;
ALTER FUNCTION public.notify_job_status_change() SET search_path = public;

-- SECURITY DEFINER functions (credential-handling and auth helpers)
ALTER FUNCTION public.get_ga_token(uuid) SET search_path = public;
ALTER FUNCTION public.user_organisation_id() SET search_path = public;
ALTER FUNCTION public.user_is_member_of(uuid) SET search_path = public;
ALTER FUNCTION public.user_organisations() SET search_path = public;
ALTER FUNCTION public.store_slack_token(uuid, text) SET search_path = public;
ALTER FUNCTION public.get_slack_token(uuid) SET search_path = public;
ALTER FUNCTION public.delete_slack_token(uuid) SET search_path = public;

-- Regular functions
ALTER FUNCTION public.recalculate_job_stats(text) SET search_path = public;
ALTER FUNCTION public.get_organisation_usage_stats(uuid) SET search_path = public;
ALTER FUNCTION public.get_free_plan_id() SET search_path = public;
ALTER FUNCTION public.next_midnight_utc() SET search_path = public;
ALTER FUNCTION public.job_has_capacity(text) SET search_path = public;
ALTER FUNCTION public.get_daily_quota_remaining(uuid) SET search_path = public;
ALTER FUNCTION public.is_org_over_daily_quota(uuid) SET search_path = public;
ALTER FUNCTION public.is_org_quota_blocked(uuid) SET search_path = public;
ALTER FUNCTION public.resolve_platform_org(text, text) SET search_path = public;
ALTER FUNCTION public.promote_waiting_task_for_job(text) SET search_path = public;
ALTER FUNCTION public.clear_expired_quota_blocks() SET search_path = public;
ALTER FUNCTION public.promote_waiting_tasks_for_org(uuid, integer) SET search_path = public;
ALTER FUNCTION public.get_quota_available_slots(uuid, integer) SET search_path = public;


-- ============================================================================
-- SECTION 2: Fix RLS initplan — wrap auth.uid()/auth.role() in (SELECT ...)
-- Ensures per-query evaluation instead of per-row (lint 0003)
-- ============================================================================

-- domains: wrap auth.role() in INSERT policy
DROP POLICY IF EXISTS "Authenticated users can create domains" ON domains;
CREATE POLICY "Authenticated users can create domains"
ON domains FOR INSERT
WITH CHECK ((SELECT auth.role()) = 'authenticated');

-- pages: wrap auth.role() in INSERT policy
DROP POLICY IF EXISTS "Authenticated users can create pages" ON pages;
CREATE POLICY "Authenticated users can create pages"
ON pages FOR INSERT
WITH CHECK ((SELECT auth.role()) = 'authenticated');

-- slack_user_links: wrap auth.uid() in all four CRUD policies
DROP POLICY IF EXISTS "slack_user_links_select_own" ON slack_user_links;
CREATE POLICY "slack_user_links_select_own" ON slack_user_links
  FOR SELECT USING (user_id = (SELECT auth.uid()));

DROP POLICY IF EXISTS "slack_user_links_insert_own" ON slack_user_links;
CREATE POLICY "slack_user_links_insert_own" ON slack_user_links
  FOR INSERT WITH CHECK (user_id = (SELECT auth.uid()));

DROP POLICY IF EXISTS "slack_user_links_update_own" ON slack_user_links;
CREATE POLICY "slack_user_links_update_own" ON slack_user_links
  FOR UPDATE USING (user_id = (SELECT auth.uid()));

DROP POLICY IF EXISTS "slack_user_links_delete_own" ON slack_user_links;
CREATE POLICY "slack_user_links_delete_own" ON slack_user_links
  FOR DELETE USING (user_id = (SELECT auth.uid()));

-- notifications: wrap auth.uid() in update policy
DROP POLICY IF EXISTS "notifications_update_own" ON notifications;
CREATE POLICY "notifications_update_own" ON notifications
  FOR UPDATE USING (user_id = (SELECT auth.uid()));


-- ============================================================================
-- SECTION 3: Consolidate multiple permissive SELECT policies (lint 0006)
-- ============================================================================

-- jobs: merge users_own_jobs_simple into the org policy
-- The simple policy (user_id = auth.uid()) overlaps with the org-scoped ALL policy.
-- Replace with a single SELECT policy (with ownership fallback) plus specific write policies.
DROP POLICY IF EXISTS "users_own_jobs_simple" ON jobs;
DROP POLICY IF EXISTS "Users can access active org jobs" ON jobs;

-- SELECT: org membership OR direct ownership
CREATE POLICY "Users can access active org jobs"
ON jobs FOR SELECT
USING (
    (organisation_id = public.user_organisation_id()
     AND public.user_is_member_of(organisation_id))
    OR user_id = (SELECT auth.uid())
);

-- INSERT/UPDATE/DELETE: org-scoped only (separate policies to avoid FOR ALL overlapping SELECT)
CREATE POLICY "Users can insert active org jobs"
ON jobs FOR INSERT
WITH CHECK (
    organisation_id = public.user_organisation_id()
    AND public.user_is_member_of(organisation_id)
);

CREATE POLICY "Users can update active org jobs"
ON jobs FOR UPDATE
USING (
    organisation_id = public.user_organisation_id()
    AND public.user_is_member_of(organisation_id)
);

CREATE POLICY "Users can delete active org jobs"
ON jobs FOR DELETE
USING (
    organisation_id = public.user_organisation_id()
    AND public.user_is_member_of(organisation_id)
);

-- organisation_members: "Users can view org co-members" already covers own memberships
-- (the subquery returns all orgs the user belongs to, which includes their own rows)
DROP POLICY IF EXISTS "Users can view own memberships" ON organisation_members;
