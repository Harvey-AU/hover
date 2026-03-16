# Unified Frontend: ES Modules and Naming Convention

Date: 2026-03-15 Status: **In progress (Phase 4 complete)** Scope: Webflow
extension screens, `/dashboard`, job details, and settings screens

## Progress summary (as of 2026-03-16)

| Phase                       | Status         | Notes                                                                                                                                                        |
| --------------------------- | -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Phase 0 — Foundations       | ✅ Complete    | `app/` structure, tokens, base, lib utilities, test page; `base.css` now loaded on all migrated pages                                                        |
| Phase 1 — Webflow auth      | ✅ Complete    | `webflow-login.js`, `hover-toast`, `extension-auth.html` migrated; postMessage contract fixed                                                                |
| Phase 2 — Webflow job list  | ✅ Complete    | `webflow-jobs.js`, `hover-data-table`, `hover-status-pill`, `hover-job-card` wired into extension; `buildResultCard` retired; `sync:components` script added |
| Phase 3 — Dashboard         | ✅ Complete    | `dashboard.js`, `hover-job-card` job list, restart/cancel wired via card events, `bb-bootstrap.js` and `bb-dashboard-actions.js` removed                     |
| Phase 4 — Job details       | ✅ Complete    | Tasks table, filter tabs, per-tab columns, `hover-job-card`, performance API filter, `bb-bootstrap.js` removed                                               |
| Phase 5 — Settings          | 🔲 Not started | `bb-settings.js` 2,293 lines, 8 legacy scripts to remove                                                                                                     |
| Phase 6 — Dashboard cleanup | 🔲 Not started | `bb-domain-search`, integrations scripts still loaded                                                                                                        |
| Phase 7 — Global nav + auth | 🔲 Not started | `bb-global-nav.js`, `auth.js` on extension-auth                                                                                                              |

---

## Summary

Hover should standardise on one no-build frontend architecture now, before
active users arrive and before more pages are added to either the Webflow
extension or the main app.

The two changes covered in this plan are:

1. **ES modules migration** — adopt native `type="module"` as the default
   frontend architecture across both surfaces, replacing the current
   global-script model
2. **Legacy naming migration** — retire `bb`, `BB`, `BBB`, and `bbb` prefixes
   inherited from the original Blue Banded Bee product name, replacing them with
   generic internal names and `hover-*` for browser-facing contracts

These run in parallel but are not equal. The ES modules migration leads. The
naming migration rides along with it, screen by screen.

Short-term breakage is acceptable because there are no active users.

---

## Quick-reference decisions

| Topic                          | Decision                                             |
| ------------------------------ | ---------------------------------------------------- |
| JavaScript architecture        | Native ES modules, no build tool                     |
| Web Components                 | Selectively, for shared UI primitives only           |
| Build tool                     | No                                                   |
| Framework                      | No                                                   |
| Internal module names          | Generic and descriptive                              |
| Browser-facing component names | `hover-*` prefix                                     |
| Company prefix in UI code      | Avoid — `Good Native` is for org-level concerns only |
| Legacy `bb` names              | Retire screen-by-screen, never extend                |
| Migration order                | Webflow screens first, dashboard follows each time   |
| Rename timing                  | During migration, not before                         |

---

## Source references

This plan is informed by:

- current frontend architecture in `web/static/js/core.js`,
  `web/static/js/auth.js`, `web/static/js/bb-data-binder.js`,
  `web/static/js/bb-settings.js`, `web/static/js/bb-dashboard-actions.js`, and
  `web/static/js/job-page.js`
- current HTML loading patterns in `dashboard.html`, `settings.html`,
  `homepage.html`, `welcome.html`, `invite-welcome.html`, `cli-login.html`,
  `auth-callback.html`, and `web/templates/job-details.html`
- Webflow extension implementation in `webflow-designer-extension-cli/`
- `docs/plans/modern-javascript-opportunities.md`
- `docs/plans/ui-implementation.md`
- external reference:
  [16 Modern JavaScript Features That Might Blow Your Mind](https://dev.to/sylwia-lask/16-modern-javascript-features-that-might-blow-your-mind-4h5e)

---

## Why now

Two frontend surfaces are both actively being refactored:

- the Webflow extension interface and job list screens
- the main app surfaces: `/dashboard`, job details, and settings

Continuing to refactor each surface in different JavaScript styles creates a
launch-stage maintenance problem:

- duplicated UI work
- inconsistent behaviour between surfaces
- shared auth and data logic split across incompatible patterns
- harder debugging at launch
- slower onboarding as more pages are added

Because there are no active users, this is the right time to unify the frontend
direction even if some existing pages temporarily break.

---

## Goals

- establish one frontend method for all new and refactored screens
- make Webflow and main app screens feel like the same product
- create a reusable UI layer that compounds screen by screen
- reduce dependence on `window.*` global contracts over time
- retire legacy Blue Banded Bee naming as screens are migrated
- avoid introducing a build tool

## Non-goals

- full SPA conversion
- introducing React, Vue, Svelte, or any other framework
- bundling, transpilation, or a build pipeline for web app assets
- rewriting every existing page in one release
- replacing working backend APIs as part of this work

---

## Decision

### Primary: native ES modules

Use native `type="module"` as the shared frontend architecture across both the
Webflow extension web pages and the main app.

This means:

- each page has one explicit module entrypoint
- dependencies are imported, not globally injected
- shared logic lives in `lib/` modules
- startup order is explicit, not implicit through script ordering

### Supporting: selective Web Components

Use Web Components only where they provide strong reuse or clear encapsulation
value across multiple screens. Good candidates are listed in the architecture
section below.

Do not make Web Components the architecture for entire pages. Page orchestration
should live in plain ES modules. Web Components are view primitives inside that
system.

### Not chosen

- build tools, bundlers, transpilers
- React, Vue, Svelte, or equivalent
- Web Components as the primary page abstraction

### Extension CLI note

The `webflow-designer-extension-cli/` directory already uses TypeScript with a
build step, as required by the Webflow Designer Extension SDK. That is a
separate constraint and is not affected by this plan. The plan applies to the
Hover web app pages, including the Webflow extension auth and login screens
served by the Go backend.

---

## Naming conventions

### Background: `bb`, `BB`, `BBB`, and `bbb`

The existing `bb` family of prefixes is legacy naming from Blue Banded Bee, the
original product name before Adapt and Hover.

Examples throughout the current codebase:

| Type            | Examples                                                 |
| --------------- | -------------------------------------------------------- |
| File names      | `bb-settings.js`, `bb-webflow.js`, `bb-data-binder.js`   |
| Browser globals | `window.BB_APP`, `window.BBAuth`, `window.BBB_CONFIG`    |
| HTML attributes | `bbb-action`, `bbb-template`, `bbb-text`, `data-bb-bind` |
| CSS identifiers | `.bb-*` class names and data attributes                  |

These should be treated as legacy. Do not extend them. Retire them as each
screen migrates.

### Convention: three tiers

| Tier          | When to use                                                                       | Examples                                            |
| ------------- | --------------------------------------------------------------------------------- | --------------------------------------------------- |
| Generic       | Internal modules and utilities with no need for product branding                  | `api-client.js`, `auth-session.js`, `formatters.js` |
| `hover-*`     | Browser-facing contracts: custom elements, component files, namespaced attributes | `hover-status-pill`, `hover-toast.js`               |
| `Good Native` | Company-level or multi-product concerns only                                      | Documentation ownership, shared platform tooling    |

The default should be generic. Use `hover-*` when a stable browser-level
namespace is needed, primarily for custom element names and their source files.
Avoid reaching for the company name in ordinary UI code.

### Specific replacement rules

#### File names

| Legacy                    | Replacement                                    |
| ------------------------- | ---------------------------------------------- |
| `bb-settings.js`          | `settings-page.js` (internal)                  |
| `bb-webflow.js`           | `webflow-page.js` (internal)                   |
| `bb-dashboard-actions.js` | `dashboard-actions.js` (internal)              |
| `bb-data-binder.js`       | `template-binder.js` (internal)                |
| `bb-global-nav.js`        | `global-nav.js` (internal)                     |
| `bb-bootstrap.js`         | removed — replaced by ES module entrypoints    |
| Component files           | `hover-status-pill.js`, `hover-toast.js`, etc. |

Rule: internal logic files become generic. Browser-facing reusable component
files become `hover-*`.

#### Browser globals

The preferred direction is to remove globals entirely in favour of imports. For
the period when a temporary bridge global is still required:

| Legacy                | Temporary bridge       | Final state         |
| --------------------- | ---------------------- | ------------------- |
| `window.BB_APP`       | `window.HoverApp`      | removed via imports |
| `window.BBB_CONFIG`   | `window.HoverConfig`   | removed via imports |
| `window.BBAuth`       | `window.HoverAuth`     | removed via imports |
| `window.BB_ORG_READY` | `window.HoverOrgReady` | removed via imports |

Do not create new `BB*` or `BBB*` globals. Use `Hover*` only as a short-lived
migration bridge.

#### HTML attributes

| Legacy         | Replacement     |
| -------------- | --------------- |
| `bbb-action`   | `data-action`   |
| `bbb-template` | `data-template` |
| `bbb-text`     | `data-bind`     |
| `data-bb-bind` | `data-bind`     |
| `bbb-id`       | `data-id`       |

Default to generic `data-*` attributes for internal page wiring. Use
`data-hover-*` only where collision risk or a component's public API makes a
namespace necessary.

#### Custom elements

Custom elements require a namespace by the HTML spec (single-word names are
reserved). Use `hover-*` for all new custom element names:

```js
customElements.define("hover-status-pill", HoverStatusPill);
customElements.define("hover-toast", HoverToast);
customElements.define("hover-tabs", HoverTabs);
```

Do not register custom elements with generic hyphenated names that lack the app
namespace (for example `status-pill` or `app-toast`).

### Migration rule of thumb

- **New code**: never use `bb`
- **Migrated code**: rename while migrating
- **Untouched legacy code**: leave it until its turn

---

## ES modules migration and `bb` renaming: phasing

These two changes run in parallel but are not equal tracks.

### Why ES modules leads

The ES modules migration solves the structural problem first:

- removes implicit script ordering and fragile startup timing
- eliminates most of the reason browser globals exist
- creates proper dependency boundaries
- gives a natural moment to rename every touched file correctly

Renaming inside the legacy architecture without migrating the architecture first
creates churn without improving the underlying problem.

### Why renaming does not happen first

A repo-wide rename-first pass would:

- touch every active file and create merge conflicts
- leave the structural problems exactly as they were
- require a second major pass to migrate the architecture anyway

### The correct sequence

1. Establish the ES module structure and naming rules (Phase 0)
2. Migrate each screen to ES modules
3. Rename `bb` artefacts in the same touched screen at the same time
4. Remove compatibility bridges once the screen replacement is stable
5. Repeat until no active frontend paths use `bb` names

---

## Target architecture

### Directory structure

```text
web/static/
  app/
    icons/                        ← SVG icon files (synced from extension)
    lib/
      api-client.js               ✅
      auth-session.js             ✅
      config.js                   ✅
      formatters.js               ✅
    components/
      hover-data-table.js         ✅ (with sort support)
      hover-job-card.js           ✅ (domain component, Phase 4)
      hover-status-pill.js        ✅
      hover-tabs.js               ✅ (Phase 4)
      hover-toast.js              ✅
      hover-button.js             🔲 planned
      hover-modal.js              🔲 planned
      hover-empty-state.js        🔲 planned
    pages/
      dashboard.js                ✅
      job-details.js              ✅
      webflow-jobs.js             ✅
      webflow-login.js            ✅
      settings-account.js         🔲 Phase 5
      settings-billing.js         🔲 Phase 5
      settings-integrations.js    🔲 Phase 5
      settings-team.js            🔲 Phase 5
    styles/
      base.css                    ✅
      components.css              ✅
      tokens.css                  ✅
```

Structural rules that should not change:

| Directory     | Purpose                                          |
| ------------- | ------------------------------------------------ |
| `lib/`        | Shared logic with no page or UI dependency       |
| `components/` | Reusable UI primitives with a stable public API  |
| `pages/`      | Per-page orchestration modules                   |
| `styles/`     | Shared tokens, base styles, and component styles |

### Page loading pattern

Each page uses one explicit module entrypoint:

```html
<link rel="stylesheet" href="/app/styles/tokens.css" />
<link rel="stylesheet" href="/app/styles/base.css" />
<link rel="stylesheet" href="/app/styles/components.css" />
<script type="module" src="/app/pages/dashboard.js"></script>
```

This replaces the current pattern of multiple ordered `<script defer>` tags,
`bb-bootstrap.js` polling, and `window.BB_APP.whenReady()` chains.

### Third-party dependencies (Supabase and others)

The current architecture loads Supabase from a CDN with SRI hashes via a
dynamically injected `<script>` tag in `core.js`. ES module `import` statements
do not mix with CDN UMD bundles in the same way.

Options during migration:

- continue loading Supabase via a `<script>` tag before the module entrypoint,
  then access `window.supabase` inside the module
- switch to the Supabase ESM build from a CDN (`esm.sh` or similar) and import
  it directly inside the module

The recommended approach for Phase 0 is the first option (keep Supabase as a
pre-loaded script) and then evaluate the ESM import option once the module
structure is established and the SRI/CDN policy is reviewed.

### `lib/` responsibilities

Shared logic that should have no page or UI dependency:

- API request wrappers and response normalisation
- auth and session helpers
- organisation state and switching logic
- URL and query parameter helpers
- error normalisation and `Error.cause` wrapping
- formatting helpers: dates, durations, counts, status labels
- shared event bus or pub/sub helpers
- storage helpers: `localStorage` and `sessionStorage` with error boundaries

### `components/` responsibilities

Reusable UI primitives with a defined attribute and event API:

- `hover-status-pill` — job and task status badges
- `hover-card` — surface container with standard padding and shadow
- `hover-toast` — feedback toasts (success, warning, error)
- `hover-modal` — modal/dialog shell
- `hover-tabs` — tabbed panel switching
- `hover-empty-state` — empty and error placeholders
- `hover-data-table` — sortable, filterable table shell
- progress indicator

### `pages/` responsibilities

Each page module:

- imports only what it needs from `lib/` and `components/`
- initialises screen-level state
- wires page-specific event handlers
- fetches and renders screen data
- owns only behaviour specific to that screen

### When to use Web Components

Use Web Components when all of the following are true:

- the UI is reused across multiple screens
- it has a clear attribute/property input and event output contract
- encapsulation makes it easier to maintain and test in isolation
- it can be styled within the shared token system

Do not use Web Components for:

- one-off page layout or wrappers
- complex business workflows that require heavy orchestration
- every form, card, or list by default

---

## Styling strategy

### Rules

- define CSS custom property tokens before any component styling
- use tokens for colour, spacing, type scale, radius, and shadow — not
  hard-coded values
- create shared layout primitives rather than per-page hacks
- avoid inline `element.style.cssText` construction for new work
- mobile and desktop behaviour should be intentional from the start, not
  retrofitted

### Token categories

| Category     | Examples                                                                                                                                                          |
| ------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Colour roles | `--color-bg`, `--color-surface`, `--color-text`, `--color-text-muted`, `--color-border`, `--color-accent`, `--color-danger`, `--color-success`, `--color-warning` |
| Spacing      | `--space-1` through `--space-8`                                                                                                                                   |
| Type scale   | `--text-heading`, `--text-body`, `--text-caption`, `--text-label`                                                                                                 |
| Radius       | `--radius-sm`, `--radius-md`, `--radius-lg`                                                                                                                       |
| Shadow       | `--shadow-sm`, `--shadow-md`                                                                                                                                      |

### UI objective

The Webflow extension screens define the cleaner visual language. The dashboard
and settings surfaces inherit it — not the other way around.

---

## Migration strategy

### Overarching loop

The migration follows a repeating cycle:

1. Modernise a Webflow extension screen using ES modules and new naming
2. Extract any reusable modules, styles, and components from that work
3. Apply the same patterns to the equivalent or adjacent dashboard screen
4. Carry the shared assets forward into the next Webflow screen
5. Repeat until both surfaces operate from the same system

This keeps both surfaces aligned while delivering incrementally.

### Bridging during migration

**Acceptable temporary bridges:**

- small compatibility wrappers for existing auth and session calls
- adapting current API response shapes into new page modules
- temporary re-exports from old globals into new modules while a screen is being
  replaced

**Not acceptable as long-term patterns:**

- adding new `window.*` dependencies to new screens
- mixing old and new loading models on the same screen across releases
- copying UI code across surfaces instead of extracting it to shared modules

---

## Phases

### Phase 0 — Foundations

Objective: establish the shared frontend base before screen rebuilds begin.

Tasks:

- create the `web/static/app/` directory structure
- define `tokens.css` and `base.css`
- create baseline `lib/` utilities: `api-client.js`, `auth-session.js`,
  `config.js`, `formatters.js`
- define the HTML loading pattern using `type="module"`
- document the component contract format (attributes, events, slots)
- decide Supabase loading approach for module context (see third-party note
  above)

Validation:

- a simple page loads from the new module structure with no `bb-bootstrap.js`
  polling chain
- shared styles render consistently in both Webflow extension and dashboard
  contexts

Rollback point: old pages remain untouched until Phase 1 is ready.

---

### Phase 1 — Webflow login and auth flow

Objective: migrate the Webflow login/auth screens as the first module-first
surface.

Tasks:

- create `pages/webflow-login.js` as the module entrypoint
- move auth and session logic into `lib/auth-session.js`
- create initial reusable auth UI primitives
- standardise loading states, error feedback, and toast patterns
- apply new naming conventions; retire `bb` names in all touched files

Reusable outputs from this phase:

- `lib/auth-session.js`
- `hover-toast` component
- form field and button patterns
- shared error presentation

Dashboard follow-through:

- apply auth feedback and session helpers to dashboard entry and gated screens
- reuse `lib/auth-session.js` instead of page-local globals

Validation:

- login, logout, session restore, and OAuth redirect handling all work
- no `BB_APP`, `BBAuth`, or `bbb-*` attributes remain in migrated files

---

### Phase 2 — Webflow job list screen ✅ Complete

Objective: establish the list and status language for operational screens.

Tasks:

- create `pages/webflow-jobs.js` as the module entrypoint
- create `hover-data-table`, `hover-status-pill`, and filter bar patterns
- centralise formatting helpers for status, counts, dates, and durations in
  `lib/formatters.js`
- normalise loading, polling, and realtime indicator patterns

Reusable outputs from this phase:

- `hover-data-table`
- `hover-status-pill`
- filter bar pattern
- skeleton and loading states
- empty and error states

Dashboard follow-through:

- apply the same list and table language to `/dashboard`
- use the same status and card components in job summaries

Validation:

- Webflow job list and dashboard job listing feel like the same product surface
- state transitions are visually consistent

**Completed (2026-03-16):**

- `hover-job-card` wired into extension `index.js` via `window.HoverJobCard`
  bridge
- `buildResultCard`, `fetchIssueTasks`, `renderIssuesTable` removed from
  `index.js`
- `sync:components` npm script added — copies all 4 shared components from
  `web/static/app/components/` to `webflow-designer-extension-cli/public/` and
  patches `hover-job-card.js` for the extension context (no `/app/` path)
- `scripts/patch-extension-components.js` handles the extension-specific
  `defaultFetcher` override automatically on each sync

---

### Phase 3 — `/dashboard` ✅ Complete (2026-03-16)

Objective: rebuild the main dashboard using the system established in Phases 1
and 2.

Tasks:

- create `pages/dashboard.js` as the module entrypoint
- replace ad hoc dashboard UI with shared layout and component patterns
- migrate stats cards, jobs list, and actions to shared modules
- remove `bb-bootstrap.js`, `bb-dashboard-actions.js`, and fragile script
  ordering

Validation:

- dashboard renders from `pages/dashboard.js`
- primary dashboard actions (run scan, restart job, cancel job) still work
- dashboard styling matches the Webflow surface

**Completed (2026-03-16):**

- Restart/cancel wired via `hover-job-card:restart` and `hover-job-card:cancel`
  events; `dashboard.js` handles both with `showToast` feedback
- `base.css` added to `dashboard.html`
- Remaining legacy scripts (`bb-global-nav.js`, integrations, etc.) explicitly
  deferred to Phase 5–7 — not a Phase 3 gap

---

### Phase 4 — Job details ✅ Complete (2026-03-16)

Objective: bring the detail view onto the same visual and structural system.

**What was originally planned:**

- `pages/job-details.js` module entrypoint
- `hover-data-table` for task rows, `hover-tabs`, `hover-status-pill`

**What was actually built (scope expanded significantly):**

The discovery that the dashboard and Webflow extension were independently
rendering job cards with duplicated logic drove a broader piece of work:

1. **`hover-tabs`** — new Web Component; tab bar with `tabs` property, `active`
   attribute, `hover-tabs:change` event, keyboard navigation
2. **`hover-data-table` sort support** — added `sortable: true` column option,
   emits `hover-data-table:sort` event
3. **`pages/job-details.js`** — full tasks section ownership: fetch, sort, 6
   filter tabs (All / Broken Links / Success / Slow / Very Slow / In Progress)
   with per-tab column sets, pagination, realtime + adaptive polling, analytics
   columns, status pill upgrade
4. **`hover-job-card`** — domain-level Web Component, single source of truth for
   job card rendering across dashboard and extension; ported from
   `buildResultCard()` in the extension; `context` attribute for layout
   differences; emits `hover-job-card:view` and `hover-job-card:export`
5. **Dashboard job list** — replaced flat `hover-data-table` with
   `hover-job-card` list; in-place card updates via Map, no flicker
6. **Go API** — `performance` query param (`slow` >1,500ms / `very_slow`
   > 4,000ms) using `COALESCE(NULLIF(second_response_time, 0), response_time)`
7. **SVG icons + CSS** — icons copied to `web/static/app/icons/`; full button,
   dot, and icon CSS ported from extension `styles.css` into `components.css`

**Key architectural decision:**

Domain components (`hover-job-card`) sit above generic primitives. They accept a
plain data object and own all render logic. A `context` attribute handles the
1–5% of layout differences between surfaces. CSS class selectors provide all
per-context overrides — no Shadow DOM, no `part` attributes needed.

**Extension sync approach:**

`web/static/app/components/` is the source of truth. Extension has its own
copies of components in `public/` (existing pattern for `hover-status-pill`,
`hover-data-table`, `hover-toast`). A `sync:components` npm script will be added
to automate copying when the extension rebuild PR lands.

**Still outstanding from Phase 4:**

- `job-page.js` header, stats, action buttons still legacy —
  `window.__hoverTasksOwned` gate prevents double-render but `job-page.js` is
  not yet retired
- `bb-global-nav.js`, `bb-data-binder.js`, `bb-auth-extension.js`,
  `bb-metadata.js` still loaded on `job-details.html`

Validation:

- ✅ Tasks table: `hover-tabs` + `hover-data-table` with per-tab column sets
- ✅ Performance filter tabs backed by Go API filter
- ✅ Dashboard job list uses `hover-job-card`, matches extension visual output
- ✅ Adaptive fallback polling: 500ms active, 1s idle

---

### Phase 5 — Settings

Objective: make settings feel like part of the same product, not a separate UI.

**Scope:** `settings.html` currently loads 8 legacy scripts: `bb-data-binder`,
`bb-auth-extension`, `bb-integration-http`, `bb-slack`, `bb-webflow`,
`bb-google`, `bb-invite-flow`, `bb-settings` (2,293 lines).

Tasks:

- create per-section page modules: `settings-account.js`, `settings-team.js`,
  `settings-billing.js`, `settings-integrations.js`
- the integrations section (Slack, Webflow, Google) each have their own OAuth
  flows — extract each into its own module carefully
- reuse `hover-modal`, `hover-tabs`, `hover-toast`, and card primitives already
  established
- retire `bb-settings.js` once all sections are migrated

Risk: highest-risk phase — OAuth callback wiring in `bb-settings.js` is tightly
coupled and must be extracted carefully to avoid breaking OAuth flows.

---

### Phase 6 — Dashboard cleanup

Objective: remove remaining legacy scripts from `dashboard.html`.

**Still loaded after Phase 3/4:** `bb-domain-search`, `bb-integration-http`,
`bb-slack`, `bb-webflow`, `bb-google`, `bb-admin`, `bb-data-binder`,
`bb-auth-extension`, `bb-metadata`.

Tasks:

- domain search widget → `hover-combobox` or equivalent component
- integration scripts → moved into `settings-integrations.js` (done in Phase 5)
- `bb-admin` → `admin.js` module
- retire `bb-data-binder` from dashboard once all binder-driven UI is replaced

---

### Phase 7 — Global nav + auth

Objective: complete the migration; no active page depends on legacy scripts.

Tasks:

- `bb-global-nav.js` (742 lines) → `hover-nav` component or `global-nav.js`
  module; owns org-switcher, quota bar, notifications
- `auth.js` on `extension-auth.html` → migrate last; owns OAuth redirect
  contract (AGENTS.md); must migrate together with extension auth rebuild
- retire `core.js` global nav dependency once `bb-global-nav.js` is replaced

Note: `auth.js` redirect contract (`handleSocialLogin`) must be preserved
exactly — deep-link URLs return to the originating URL, invites route to
`/welcome`. Do not touch until auth is fully migrated end-to-end.

---

### Ongoing — New screens

Any screen added after Phase 7 must use the ES module architecture and new
naming conventions.

Standing rules:

- new screens use the shared module architecture from day one
- any pattern repeated a third time must be extracted to `lib/` or `components/`
  before that third use is merged
- the legacy global-script model must not be expanded for new screens
- `sync:components` script must be run before any extension rebuild PR that
  consumes updated components

---

### Ongoing — New screens

Any screen added after Phase 5 must use the ES module architecture and new
naming conventions. This is not a phase with a completion date; it is a standing
rule.

Standing rules:

- new Webflow screens use the shared module architecture from day one
- new dashboard screens use the same shared module architecture from day one
- any pattern repeated a third time must be extracted to `lib/` or `components/`
  before that third use is merged
- the legacy global-script model must not be expanded for new screens

---

## Implementation order by surface

### Webflow-first path

1. Webflow login/auth flow (Phase 1)
2. Webflow job list (Phase 2)
3. Future Webflow operational screens — apply the same system at the start, not
   at the end

### Dashboard and main app follow-through

1. `/dashboard` (Phase 3)
2. Job details (Phase 4)
3. Settings — navigation shell first, then each subsection (Phase 5)

The smaller, fresher Webflow screens establish the system. The larger dashboard
surfaces then use that proven system rather than starting from scratch.

---

## Risks

### 1. Mixed-architecture drift

Risk: old and new patterns coexist for too long as new features keep landing in
the legacy model.

Mitigation:

- new major functionality must not be added to the old pattern
- all new screen work starts on the new module structure

### 2. Styling divergence between surfaces

Risk: Webflow and dashboard screens are refactored independently and end up
looking unrelated.

Mitigation:

- tokens and shared component styles must exist before broad UI rollout
- new screen work should be reviewed against both surfaces, not in isolation

### 3. Overusing Web Components

Risk: too much orchestration logic ends up inside custom elements, making
debugging harder and styling more constrained.

Mitigation:

- keep components small, composable, and presentation-focused
- keep business logic in `lib/` modules, not inside component classes

### 4. Migration stalls after the Webflow screens

Risk: the Webflow surfaces are modernised but the dashboard remains on the old
system because it is more complex.

Mitigation:

- dashboard follow-through is part of each phase's definition of done, not a
  separate optional tidy-up
- each Webflow phase must identify and hand off what is immediately reusable in
  the dashboard

### 5. Supabase CDN loading conflicts with ES modules

Risk: the current Supabase SDK is loaded as a UMD CDN bundle, which does not
import cleanly into a native module context.

Mitigation:

- resolve the Supabase loading approach in Phase 0 before any screen migration
  begins (see third-party dependencies note in the architecture section)

---

## Delivery checkpoints

| Checkpoint            | Criteria                                                                                                                 |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| A — Foundation ready  | `app/` structure exists, tokens and base styles in place, first page module loads without `bb-bootstrap.js`              |
| B — Webflow aligned   | Webflow auth and job list run on the new architecture, core reusable components exist                                    |
| C — Dashboard aligned | `/dashboard` uses the new module loading model and matches the Webflow surface visually                                  |
| D — Full coverage     | Job details and all settings sections use the shared system, legacy `bb` names are absent from all active frontend paths |

---

## Definition of done

This initiative is complete when:

- all new and refactored screens use native ES module entrypoints
- the Webflow extension and main app share one visible design language
- main reusable UI primitives live in `components/` as `hover-*` elements
- internal modules are generic and descriptive
- no active frontend page entrypoint depends on `bb-bootstrap.js`, `BB_APP`,
  `BBAuth`, `BBB_CONFIG`, or `bbb-*` attributes for new work
- future screens can be added without inventing a third frontend style

---

## Immediate next actions

1. Resolve the Supabase loading approach for a module context
2. Create `web/static/app/` with the directory structure defined above
3. Write `tokens.css`, `base.css`, and the baseline `lib/` modules
4. Migrate the Webflow login screen as the first complete Phase 1 screen
5. Identify which dashboard screen directly inherits Phase 1 work and plan it
   concurrently

---

## Implementation notes

- Preserve the existing auth redirect contract (`web/static/js/auth.js` —
  `handleSocialLogin`) while refactoring auth-related screens. Deep-link URLs
  must return to the originating URL. Invite acceptance routes to `/welcome`.
- Any new top-level HTML page must follow the Dockerfile triple-surface rule:
  HTTP route in `internal/api/handlers.go`, the file on disk, and a `COPY` line
  in `Dockerfile`.
- Keep changes incremental by screen but directional at the architecture level.
- Delete old patterns once replacement screens are stable. Do not maintain
  parallel systems indefinitely.
