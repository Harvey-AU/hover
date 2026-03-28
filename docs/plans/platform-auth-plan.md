# Platform Auth Plan

## Goal

Implement many-to-many organisation membership, active-organisation scoping, and
platform org mapping (Webflow/Shopify) so multi-org users only see data for the
org they selected.

## Decisions

- Webflow: map **workspace ID → GNH organisation**.
- Shopify: map **Plus org ID when available**, otherwise **shop/store ID**.
- Multi-org users must use **active organisation context** for all queries (no
  cross-org listings).
- Platform mappings belong to a single GNH org; users gain access only through
  org membership.

## Plan

1. Add `organisation_members` (many-to-many) and backfill from
   `users.organisation_id`.
2. Add `active_organisation_id` (user/session) and require it for all list/query
   endpoints.
3. Update RLS policies to use `organisation_members` plus
   `active_organisation_id` checks.
4. Add `platform_org_mappings` and implement Webflow/Shopify org mapping flow.
5. Ensure org switcher UX + API only returns data for the selected org.
6. Verify with targeted tests and migration checks.
