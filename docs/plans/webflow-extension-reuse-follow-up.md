# Webflow Extension Reuse Follow-up

Date: 2026-04-05 Status: Proposed Scope: Webflow Designer extension
consolidation and shared frontend reuse

## Current state

The broader ES modules migration is complete and archived in the changelog. The
main app now has an established shared frontend layer in `web/static/app/`, with
`/dashboard`, settings, and job details already migrated to the module
architecture.

Both branch checkpoints associated with that migration,
`feat/es-modules-extension-sync` and `feat/es-modules-phase-0`, are already
contained in `main`. They should be treated as historical delivery branches, not
pending work.

The Webflow Designer extension has already adopted part of that shared layer:

- shared API helpers via `app/lib/`
- shared Web Components via `app/components/`
- shared module sync into the extension build
- a bridge/import-map pattern so extension code can consume shared modules in a
  cross-origin runtime

That means the transition is partly complete. Shared primitives exist and are in
use, but the extension has not yet reached the same level of modular
consolidation as the main app.

## What is already complete

- `/dashboard`, settings, and job details are on the ES module architecture
- shared module structure exists in `web/static/app/`:
  - `lib/` for reusable logic
  - `components/` for shared UI primitives
  - `pages/` for page orchestration
- the extension reuses shared primitives and API helpers through the bridge/sync
  approach
- design tokens exist in the app layer and mirror the extension theme
- the completed migration is already documented in `CHANGELOG.md`

## Remaining gaps

- The extension still keeps most page orchestration in
  `webflow-designer-extension-cli/src/index.ts` rather than in shared `/app`
  modules.
- The current job-list sharing story is incomplete. The repository contains
  `web/static/app/pages/webflow-jobs.js`, but the live extension still owns much
  of its own job rendering and refresh flow.
- The extension auth popup still depends on legacy `/js/auth.js`.
- Extension shell styling remains separate from app styling. The app tokens
  mirror the extension theme, but the extension shell has not been migrated to
  the app style layer.
- Some documentation still overstates how far the extension has been
  consolidated into the shared module system.

## Recommended next phase

This follow-up should be treated as a JavaScript-first reuse pass, not a full UI
unification project.

- Extract surface-agnostic extension logic from
  `webflow-designer-extension-cli/src/index.ts` into shared `/app` modules.
- Make the job-list sharing story truthful and consistent:
  - either move extension job-list behaviour onto shared `pages/` modules
  - or narrow the shared module claims so the code and docs say exactly what is
    shared
- Keep the bridge/import-map approach for cross-origin extension use.
- Continue sharing reusable logic and UI primitives first, before attempting to
  merge the full extension shell layout into the main app.
- Replace the remaining legacy auth dependency in `/extension-auth` so the popup
  flow no longer relies on `/js/auth.js`.
- Update architecture and planning docs so they reflect the current state
  accurately.

## Non-goals

- Recreating the deleted March 2026 ES modules plan
- Re-running the full `/dashboard` modernisation effort
- Forcing full shell or layout convergence between the extension and the main
  app in this phase
- Replacing working backend Webflow APIs as part of this documentation update

## Acceptance criteria

- The new plan clearly states that the ES modules migration is complete and
  archived in the changelog.
- The new plan clearly states that both ES modules branch checkpoints are
  already contained in `main`.
- The new plan distinguishes between shared primitives that already exist and
  extension page orchestration that is still local.
- The new plan sets a JS-first extension consolidation direction without
  implying that the extension already shares all page-level logic with
  `/dashboard`.
- The new plan supersedes the old branch-era planning context without recreating
  archived migration history.
