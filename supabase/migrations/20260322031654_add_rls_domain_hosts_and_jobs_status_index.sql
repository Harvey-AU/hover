-- Enable RLS on domain_hosts (backend-only table, deny-all for non-service-role)
-- and add index advisor suggestion for jobs.status

-- domain_hosts: accessed only via service_role from Go backend.
-- No policies needed — RLS with no policies denies all non-service-role access.
ALTER TABLE domain_hosts ENABLE ROW LEVEL SECURITY;

-- jobs.status: recommended by Supabase index advisor for the quota-blocked jobs query
-- (get_daily_quota_remaining join on jobs WHERE status IN (...))
CREATE INDEX IF NOT EXISTS idx_jobs_status ON public.jobs USING btree (status);
