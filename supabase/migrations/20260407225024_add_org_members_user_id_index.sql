-- Index for looking up an organisation's members by user_id.
-- The existing idx_org_members_org covers org→members lookups; this covers
-- user→memberships queries (RLS checks, membership listing, active org resolution).
CREATE INDEX IF NOT EXISTS idx_org_members_user_id
ON organisation_members(user_id);
