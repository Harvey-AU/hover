# Unified Frontend ES Modules Plan

Date: 2026-03-15 Status: Proposed Scope: Webflow extension screens,
`/dashboard`, job details, and settings screens

## Summary

Adapt should standardise on one no-build frontend architecture now, before
active users arrive and before more pages are added to both the Webflow
extension and the main app.

The recommended direction is:

- use native ES modules as the default frontend architecture
- keep the no-build approach
- use Web Components selectively for shared UI primitives, not as the whole app
- rebuild the Webflow extension screens first, then apply the same patterns into
  `/dashboard`, job details, and settings as each screen is refactored
- keep both surfaces aligned so design, behaviour, and maintenance costs do not
  drift apart

This plan assumes we are comfortable with short-term breakage while the new UI
system is being established, because there are currently no active users.

## Source references

This plan is informed by:

- the current frontend architecture in `web/static/js/core.js`,
  `web/static/js/auth.js`, `web/static/js/bb-data-binder.js`,
  `web/static/js/bb-settings.js`, `web/static/js/bb-dashboard-actions.js`, and
  `web/static/js/job-page.js`
- the current HTML loading patterns in `dashboard.html`, `settings.html`,
  `homepage.html`, `welcome.html`, `invite-welcome.html`, `cli-login.html`,
  `auth-callback.html`, and `web/templates/job-details.html`
- the Webflow extension implementation in `webflow-designer-extension-cli/`
- `docs/plans/modern-javascript-opportunities.md`
- `docs/plans/ui-implementation.md`
- the external reference article:
  [16 Modern JavaScript Features That Might Blow Your Mind](https://dev.to/sylwia-lask/16-modern-javascript-features-that-might-blow-your-mind-4h5e)

## Naming note: `bb`, `BBB`, and `bbb`

The existing `bb`, `BB`, `BBB`, and `bbb` prefixes are legacy naming from Blue
Banded Bee, the original product name.

Examples in the current codebase include:

- file prefixes such as `bb-settings.js` and `bb-webflow.js`
- globals such as `window.BB_APP` and `window.BBB_CONFIG`
- HTML attributes such as `bbb-action` and `bbb-template`

As Adapt is being renamed to Hover, new frontend modules, components, CSS
classes, and browser globals created through this migration should move away
from the Blue Banded Bee prefixes.

## Naming convention decision

We should use a layered naming convention:

- use generic, capability-based names wherever the code does not need product
  branding
- use `hover-` where the name is browser-facing and needs a stable app-level
  namespace
- reserve `goodnative` or `gn-` only for company-level or multi-product
  concerns, not ordinary app UI code

This gives us the best long-term balance:

- generic names keep the codebase cleaner and easier to reuse
- `hover-*` gives us a safe namespace for custom elements, CSS hooks, and
  browser-exposed assets
- we avoid coupling ordinary UI implementation to the company name when the app
  name is the user-facing product

### Recommended naming rules

Use generic names for internal modules and utilities:

- `api-client.js`
- `auth-session.js`
- `format-date.js`
- `organisation-store.js`
- `error-normaliser.js`

Use `hover-*` for browser-facing primitives and assets:

- custom elements such as `hover-status-pill`
- component files such as `hover-status-pill.js`
- CSS utility hooks only where a namespace is needed
- page-level assets that are clearly app-facing

Use `Good Native` naming only where the concern is organisational rather than
product-facing, for example:

- shared internal tooling
- multi-product platform utilities
- company-level documentation or asset ownership

### Migration guidance

When replacing legacy `bb` names:

- prefer a generic rename if the code is internal-only
- prefer a `hover-*` rename if the code is rendered into the browser as a public
  contract
- avoid introducing `gn-*` or `goodnative-*` in normal page UI unless there is a
  clear multi-product need

Examples:

- do not create new `bb-*`, `BB_*`, `BBB_*`, or `bbb-*` names unless required as
  temporary compatibility bridges
- prefer generic names for internal modules and `hover-*` for browser-facing
  contracts
- document any temporary bridges clearly and remove them as old screens are
  retired

## `bb` to new convention migration

The `bb` family should be treated as legacy and removed incrementally as screens
move onto the new architecture.

### Migration targets

Legacy patterns to retire over time:

- file names such as `bb-settings.js`, `bb-webflow.js`, and `bb-data-binder.js`
- globals such as `window.BB_APP`, `window.BBAuth`, `window.BB_ORG_READY`, and
  `window.BBB_CONFIG`
- attributes such as `bbb-action`, `bbb-template`, `bbb-text`, and related
  `data-bb-*` or `bbb-*` bindings
- CSS classes and identifiers that are only preserving the Blue Banded Bee name

### Replacement rules

#### File names

- `bb-settings.js` -> `settings-page.js` or `hover-settings-panel.js`
- `bb-webflow.js` -> `webflow-page.js` or `hover-webflow-panel.js`
- `bb-dashboard-actions.js` -> `dashboard-actions.js`
- `bb-data-binder.js` -> `template-binder.js` or `view-binder.js`

Rule:

- internal logic files should become generic
- browser-facing reusable component files should become `hover-*`

#### Browser globals

Preferred direction is to remove globals altogether in favour of imports.

Where a temporary browser global is still required during migration:

- `window.BB_APP` -> `window.HoverApp`
- `window.BBB_CONFIG` -> `window.HoverConfig`
- `window.BBAuth` -> `window.HoverAuth`

Rule:

- do not create new `BB*` or `BBB*` globals
- use `Hover*` only as a temporary bridge until imports fully replace globals

#### HTML attributes

Legacy:

- `bbb-action`
- `bbb-template`
- `bbb-text`

Recommended replacements:

- generic attributes such as `data-action`, `data-template`, `data-bind`
- if a namespace is required, use `data-hover-*`

Rule:

- default to generic `data-*` attributes for internal page wiring
- use `data-hover-*` only if collision risk or public component API clarity
  makes it useful

#### Custom elements

Custom elements must be namespaced and should use the app name:

- `hover-status-pill`
- `hover-card`
- `hover-tabs`

Rule:

- use `hover-*` for all new custom element names
- do not create custom elements with generic single-word names

### Screen-by-screen migration approach

For each migrated screen:

1. build the new screen on ES modules
2. use generic internal module names plus `hover-*` browser-facing names
3. add a temporary bridge to old `bb` hooks only if needed for transition
4. remove the old `bb` hook once the screen replacement is stable

### End-state naming goal

At the end of this migration:

- internal modules are mostly generic and descriptive
- browser-facing UI primitives use `hover-*`
- legacy `bb`, `BB`, `BBB`, and `bbb` names are removed from active frontend
  paths
- `Good Native` remains the company identity, but not the default prefix for app
  UI implementation

## Why now

We currently have two frontend surfaces that are both in motion:

- the Webflow extension interface and job list screens
- the main app surfaces including `/dashboard`, job details, and settings

If we continue refactoring each surface with different JavaScript patterns,
different loading contracts, or different UI abstractions, we will create a
launch-stage maintenance problem:

- duplicated UI work
- inconsistent behaviours between surfaces
- shared auth and data logic split across styles
- harder bug fixes during launch
- slower onboarding for future pages and contributors

Because the app has no active users yet, this is the best time to unify the
frontend direction even if some existing pages temporarily break.

## Goals

- establish one frontend method for all new and refactored screens
- avoid introducing a build tool at this stage
- make Webflow and main app screens feel like the same product
- create a reusable UI layer that can be applied screen-by-screen
- reduce dependence on large `window.*` global contracts over time
- improve maintainability, page startup clarity, and UI consistency

## Non-goals

- full SPA conversion
- introducing React, Vue, Svelte, or another framework
- introducing bundling, transpilation, or a build pipeline for frontend assets
- rewriting every existing page in one release
- replacing working backend APIs as part of this effort

## Decision

### Chosen direction

Use native ES modules as the shared frontend architecture across both the
Webflow extension and the main app.

### Supporting direction

Use Web Components only where they provide strong reuse or encapsulation value,
for example:

- buttons and icon buttons
- cards and panels
- tabs
- modals
- toast messages
- status pills and badges
- reusable data tables or list shells
- auth panels and empty states

### Explicitly not chosen

Do not make Web Components the only architecture for entire pages.

Page logic should primarily live in plain ES modules. Web Components should sit
inside that module system as reusable view primitives.

## Principles

### 1. One architecture across both surfaces

The Webflow extension and the main app should use the same structure, naming,
state patterns, and UI primitives.

### 2. Webflow leads, dashboard follows

The Webflow extension screens are the first proving ground. As each Webflow
screen is modernised, the same patterns should then be applied into the
equivalent or adjacent dashboard screens.

### 3. Screen-by-screen rollout

We should refactor incrementally by screen, not by theoretical layer only. Every
new screen should leave behind reusable tokens, modules, components, and
interaction patterns for the next screen.

### 4. No-build discipline

Everything must run directly in the browser via native HTML, CSS, and JS.
Anything that depends on bundling, transpiling, or code generation should be
avoided.

### 5. Shared UI language

Both surfaces should draw from the same design tokens, layout rules, spacing,
typography, feedback states, and interaction patterns.

### 6. Keep business logic outside components where possible

Fetching, auth, formatting, and orchestration logic should live in plain
modules. Components should focus on presentation and well-defined interactions.

## Target architecture

## Directory structure

Suggested browser-native structure under `web/static/`:

```text
web/static/
  app/
    lib/
      api.js
      auth.js
      config.js
      events.js
      formatters.js
      state.js
      urls.js
    components/
      hover-button.js
      hover-card.js
      hover-modal.js
      hover-status-pill.js
      hover-tabs.js
      hover-toast.js
      hover-data-table.js
      hover-empty-state.js
    pages/
      webflow-login.js
      webflow-jobs.js
      dashboard.js
      job-details.js
      settings-account.js
      settings-team.js
      settings-billing.js
      settings-integrations.js
    styles/
      tokens.css
      base.css
      layout.css
      components.css
      utilities.css
```

This structure can evolve, but the separation should stay stable:

- `lib/` for shared logic
- `components/` for reusable UI primitives
- `pages/` for page orchestration
- `styles/` for shared styling foundations

## Loading model

Each page should have a small, explicit module entrypoint.

Example pattern:

```html
<link rel="stylesheet" href="/app/styles/tokens.css" />
<link rel="stylesheet" href="/app/styles/base.css" />
<link rel="stylesheet" href="/app/styles/components.css" />
<script type="module" src="/app/pages/dashboard.js"></script>
```

This replaces implicit script ordering and sprawling global readiness
dependencies with:

- explicit imports
- explicit startup order
- page-local orchestration
- smaller failure surfaces

## Module responsibilities

### `lib/`

Shared logic that should not be tied to any single page:

- API wrappers
- auth/session helpers
- organisation switching logic
- URL and query parameter helpers
- error normalisation
- formatting helpers
- shared event bus or pub/sub helpers
- local storage/session storage helpers

### `components/`

Reusable interface primitives with a stable public API.

Good component candidates:

- status pill
- card shell
- empty state
- toast container
- modal/dialog shell
- tabs
- data table shell
- progress indicator
- settings section header

### `pages/`

Each page module should:

- import only what it needs
- initialise screen state
- wire page-specific events
- fetch screen data
- render or hydrate the page
- own screen-specific behaviour only

## When to use Web Components

Use Web Components when all of the following are true:

- the UI is reused across multiple screens
- the component has a clear input/output contract
- encapsulation improves maintainability
- the component can still be styled within our shared design system

Examples that likely justify Web Components:

- `hover-status-pill`
- `hover-card`
- `hover-toast`
- `hover-modal`
- `hover-tabs`
- `hover-empty-state`
- `hover-data-table`

Do not use Web Components for:

- one-off page wrappers
- complex business workflows that need heavy orchestration
- every form or layout block by default

## Styling strategy

This migration should also become the foundation for UI quality improvements.

### Shared styling rules

- define design tokens first
- use CSS custom properties for colour, spacing, type scale, radius, and shadow
- create shared layout primitives rather than page-specific hacks
- avoid inline style-heavy UI construction for new work
- keep desktop and mobile behaviour intentional from the start

### Design token examples

- colour roles: background, surface, surface-muted, text, text-muted, border,
  accent, danger, warning, success
- spacing scale: `--space-1` through `--space-8`
- type scale: heading, body, caption, label
- radius scale and shadow scale

### Immediate UI objective

The new Webflow extension screens should define the cleaner visual language that
the dashboard and settings surfaces inherit.

## Migration strategy

## Overarching rollout model

The migration should follow this repeating loop:

1. update an existing Webflow extension screen
2. extract reusable modules, styles, and components from that work
3. apply the same concepts to the relevant dashboard or settings screen
4. carry those shared assets into the next Webflow screen
5. repeat until both surfaces are operating from the same frontend system

This keeps the Webflow interface and the main app aligned while still allowing
incremental delivery.

## Relationship between ES modules migration and `bb` renaming

These two changes should run in parallel, but they are not equal tracks.

### Primary track: ES modules and unified frontend structure

The ES modules migration is the main architectural change. It should lead the
work because it solves the bigger problem:

- inconsistent startup patterns
- too many browser globals
- weak dependency boundaries
- duplicated UI logic across surfaces

### Secondary track: `bb` to new naming migration

The `bb` renaming should happen as part of each screen migration, not as a
separate repo-wide rename first.

This means:

- new code must not introduce new `bb` names
- migrated screens should adopt the new naming convention as they move to ES
  modules
- untouched legacy screens can keep existing `bb` names until their migration
  starts

### Why this order matters

If we rename first inside the legacy architecture, we create churn without
solving the underlying structural problem.

If we move to ES modules but keep introducing `bb` names in new code, we miss
the clean break we are trying to create.

The correct approach is therefore:

1. establish the ES module architecture and naming rules
2. migrate screens onto the new architecture
3. rename `bb` to the new convention in the same touched screens
4. remove compatibility bridges once those screens are stable

### Practical rule of thumb

- new code: never use `bb`
- migrated code: rename while migrating
- untouched legacy code: leave it alone until its turn

## Phase 0 - Foundations

Objective: establish the shared frontend base before major screen rebuilds.

Tasks:

- create the new `web/static/app/` structure
- define the initial CSS token files and base styles
- create baseline `lib/` utilities for API, auth, config, and error handling
- agree naming conventions for modules and components
- define one HTML loading pattern using `type="module"`
- document screen state conventions and component contracts

Validation:

- a trivial page can load from the new module structure without any global
  bootstrap chain
- shared styles apply consistently in both the Webflow and dashboard contexts

Rollback point:

- old pages can remain untouched until the first migrated screen is ready

## Phase 1 - Rebuild the Webflow login/auth flow

Objective: use the Webflow login flow as the first real module-first screen.

Tasks:

- move page orchestration into a dedicated module entrypoint
- isolate shared auth/session logic into `lib/auth.js`
- create reusable auth UI primitives
- standardise loading, error states, and feedback patterns
- reduce direct reliance on page-local globals where practical

Expected reusable outputs:

- auth shell
- form field patterns
- button and loading states
- toast/feedback system
- shared error presentation

Dashboard application after this phase:

- apply auth shell and feedback patterns to dashboard entry and gated screens
- reuse the same auth/session helpers instead of separate page logic

Validation:

- login, logout, session restore, and redirect handling still work
- visual and interaction patterns can be reused in dashboard/settings screens

## Phase 2 - Rebuild the Webflow job list screen

Objective: establish the list/detail language for operational screens.

Tasks:

- create a shared data table or list-shell pattern
- create reusable status pills, filters, empty states, and action bars
- centralise formatting helpers for status, counts, dates, and durations
- normalise loading, refreshing, and polling/realtime indicators

Expected reusable outputs:

- `hover-data-table`
- `hover-status-pill`
- filter bar patterns
- skeleton/loading states
- empty/error states

Dashboard application after this phase:

- apply the same table/list language to `/dashboard`
- use the same card and status components in job summaries
- reduce one-off HTML/DOM logic in dashboard lists

Validation:

- Webflow job list and dashboard job listing feel like the same product surface
- state transitions are visually consistent across both screens

## Phase 3 - Apply to `/dashboard`

Objective: rebuild the main dashboard using the Webflow-proven system.

Tasks:

- replace ad hoc dashboard-specific UI with shared layout and component patterns
- refactor dashboard entrypoint into `app/pages/dashboard.js`
- migrate dashboard stats, cards, jobs list, and actions to shared modules
- simplify page startup and remove dependence on fragile script order

Validation:

- dashboard renders from the new module entrypoint
- primary dashboard actions still work
- dashboard styling now matches the Webflow surface

## Phase 4 - Apply to job details

Objective: bring detail views onto the same visual and structural system.

Tasks:

- create shared detail-page primitives
- reuse the table/list shell for task rows and issue groupings
- reuse badges, tabs, summary cards, and action patterns
- standardise detail loading and error states

Validation:

- job details uses the same language as dashboard and Webflow job list
- shared table/status/tab components work in both contexts

## Phase 5 - Apply to settings

Objective: make settings feel like part of the same product, not a separate UI.

Tasks:

- create settings navigation and panel primitives
- standardise section headers, form spacing, field messaging, and success/error
  feedback
- split settings by screen-level page modules instead of one large script over
  time
- reuse the auth, card, tabs, toast, and modal primitives already established

Validation:

- account, team, billing, and integrations use one shared design system
- settings interactions follow the same feedback and loading conventions as the
  rest of the product

## Phase 6 - Continue screen-by-screen convergence

Objective: ensure every new screen extends the same system rather than creating
new local patterns.

Rules for future screens:

- new Webflow screens must use the shared module architecture
- new dashboard screens must use the same shared module architecture
- any new reusable pattern must be extracted before the third repeated use
- old global-script patterns should not be expanded unless needed as a temporary
  bridge

## Implementation order by surface

### Webflow-first path

1. Webflow login/auth flow
2. Webflow job list
3. next Webflow operational screens as they are introduced

### Dashboard/main app follow-through

1. `/dashboard`
2. job details
3. settings navigation and top-level settings shells
4. each settings subsection incrementally

This order allows the smaller, newer Webflow screens to establish the system,
then uses that system to clean up the larger dashboard surfaces.

## Bridging strategy during migration

While the migration is in progress, we may need temporary bridges.

Acceptable temporary bridges:

- small compatibility wrappers for existing auth/session calls
- adapting current API response shapes into the new page modules
- temporary exports from old globals into new modules while screens are being
  replaced

Not acceptable as a long-term pattern:

- adding more page-specific `window.*` dependencies to new screens
- mixing old and new loading models repeatedly on the same screen
- copying UI code across surfaces instead of extracting it

## Risks

### 1. Mixed-architecture drift

Risk: old and new patterns coexist for too long.

Mitigation:

- stop adding new major functionality to old page patterns
- use the new module structure for all new screen work

### 2. Styling divergence between surfaces

Risk: Webflow and dashboard refactors still look unrelated.

Mitigation:

- create tokens and shared component styling before broad UI rollout
- review new screen work against both surfaces, not in isolation

### 3. Overusing Web Components

Risk: too much logic gets hidden inside custom elements, making debugging and
styling harder.

Mitigation:

- keep components small and composable
- keep business logic in modules

### 4. Migration stalls after the first few screens

Risk: the app ends up with a shiny new Webflow surface and an old dashboard.

Mitigation:

- treat dashboard follow-through as part of the same work, not a later optional
  tidy-up
- require each Webflow refactor to identify what is immediately reusable in the
  dashboard

## Delivery checkpoints

### Checkpoint A - foundation ready

- `app/` structure exists
- tokens and base styles exist
- first page module loads successfully

### Checkpoint B - Webflow auth and jobs aligned

- Webflow auth and job list run on the new architecture
- core reusable components exist

### Checkpoint C - dashboard aligned

- `/dashboard` uses the same design language and module loading model
- dashboard and Webflow job views share common primitives

### Checkpoint D - detail and settings aligned

- job details and settings use the same shared system
- old page-global patterns are shrinking rather than expanding

## Definition of done

This initiative is complete when:

- all new and refactored screens use native ES module entrypoints
- the Webflow extension and main app share one visible design language
- the main reusable UI primitives live in shared modules/components
- settings, dashboard, and job details no longer depend on ad hoc legacy page
  patterns for new work
- future screens can be added without inventing a third frontend style

## Immediate next actions

1. create the `web/static/app/` structure
2. define tokens, base styles, and shared utility modules
3. choose the first Webflow screen to migrate fully
4. identify the first dashboard screen that should directly inherit that work
5. stop adding new major UI work to the legacy script model except as a
   migration bridge

## Notes for implementation

- preserve the existing auth redirect contract while refactoring auth-related
  screens
- if new top-level HTML pages are introduced, follow the Dockerfile
  triple-surface rule
- keep changes incremental by screen, but directional at the architecture level
- prefer deletion of old patterns once replacement screens are stable, rather
  than maintaining dual systems indefinitely
