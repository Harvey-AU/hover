# Webflow Extension: Binding System Migration

Date: 2026-03-15 Status: Ready for implementation Branch: create from `main` or
current `chore/webflow-hover-auth-refresh`

## Context

Read these first:

- `docs/plans/unified-frontend-es-modules-plan.md` — overarching frontend
  architecture direction
- `docs/plans/ui-implementation.md` — binding system design intent
- `web/static/js/bb-data-binder.js` — the existing binding implementation
- `webflow-designer-extension-cli/src/index.ts` — the extension (2,983 lines,
  all imperative DOM)
- `webflow-designer-extension-cli/public/index.html` — the extension HTML shell

## The problem

The main app (`dashboard.html`, `job-details.html`) is built on a declarative
binding system:

1. Embed JS in page
2. Add binding attributes to HTML elements (`bbb-text`, `bbb-template`,
   `bbb-auth`, `bbb-action`, etc.)
3. Core JS scans and populates everything — data, auth state, templates, actions

The Webflow Designer Extension was built entirely outside this system. It has:

- **0** binding attributes in `index.html`
- **189** direct DOM manipulation calls in `index.ts`
- Every piece of UI — job cards, status pills, plan badge, org selector, chart
  bars, result cards, action bar, loading states — hand-built imperatively in
  TypeScript

This means every future UI change requires TypeScript changes. Nothing is
declarative. Nothing is reusable. The extension cannot share UI patterns with
the main app or future surfaces (WordPress, Shopify).

## The goal

Rebuild the extension UI layer to follow the same binding convention as the main
app:

- `index.html` declares the structure and bindings
- a portable binder (`src/binder.ts`) scans and populates
- `index.ts` becomes orchestration only: API calls, state, Webflow SDK, realtime
  — no direct DOM manipulation

---

## Key constraint

The extension is a sandboxed Webflow Designer Extension iframe. It **cannot**
load scripts from the main app. `bb-data-binder.js`, `auth.js`, `core.js` are
not available. The extension loads only:

1. Supabase UMD SDK from unpkg
2. `public/index.js` (compiled from `src/index.ts`)

The solution is a portable `src/binder.ts` — a self-contained subset of
`bb-data-binder.js` with no dependency on `window.BBAuth`, `window.supabase`,
`window.BB_APP`, or any main app global.

---

## Binding attribute vocabulary

Use the same attributes as the main app. Do not invent new ones.

| Attribute                      | What it does                                                  |
| ------------------------------ | ------------------------------------------------------------- |
| `bbb-text="path"`              | Sets `element.textContent` from a dot-path on the data object |
| `bbb-class="template {token}"` | Sets `class` attribute using string interpolation             |
| `bbb-style:prop="{token}"`     | Sets inline style property                                    |
| `bbb-attr:name="{token}"`      | Sets any named HTML attribute                                 |
| `bbb-href="{token}"`           | Sets `href` attribute                                         |
| `bbb-src="{token}"`            | Sets `src` attribute                                          |
| `bbb-template="name"`          | Declares a hidden repeating row template                      |
| `bbb-auth="required"`          | Show only when authenticated                                  |
| `bbb-auth="guest"`             | Show only when not authenticated                              |
| `bbb-show="condition"`         | Show when condition is true                                   |
| `bbb-hide="condition"`         | Hide when condition is true                                   |
| `bbb-action="name"`            | Fires a named action when clicked                             |

Condition syntax: `path=value`, `path!=value`, comma-separated values for OR:
`status=completed,failed`.

---

## Step 1 — Create `src/binder.ts`

Extract a portable subset of `bb-data-binder.js`. This file must:

- have zero dependencies on main app globals
- accept config via constructor: `new Binder({ debug?: boolean })`
- expose these methods:

```ts
class Binder {
  // Scan DOM for binding attributes and register elements
  scan(): void;

  // Update all bound elements with new data
  bind(data: Record<string, unknown>): void;

  // Render a named template with an array of items
  renderTemplate(name: string, items: Record<string, unknown>[]): void;

  // Set auth state — controls bbb-auth="required" / bbb-auth="guest" visibility
  setAuth(isAuthenticated: boolean): void;

  // Register a handler for a bbb-action name
  on(action: string, handler: (el: HTMLElement) => void): void;
}
```

Port directly from `bb-data-binder.js`:

- `registerBindElement` / `updateBoundElement` → `bbb-text`
- `registerStyleElement` / `updateStyleElement` → `bbb-style:*`
- `registerAttrElement` / `updateAttrElement` → `bbb-class`, `bbb-href`,
  `bbb-attr:*`
- `registerTemplate` / `renderTemplate` / `createTemplateInstance` →
  `bbb-template`
- `updateAuthElements` → `bbb-auth`
- `evaluateCondition` → `bbb-show` / `bbb-hide` inside templates
- `getValueByPath` → dot-path resolution
- `interpolateTemplate` → `{token}` string interpolation
- action delegation → `bbb-action` via a single `click` event listener on
  `document`

Do **not** port:

- `initAuth` — the extension handles auth via postMessage, not Supabase session
- `fetchData` / `loadAndBind` — API calls stay in `index.ts`
- `registerFormElement` — forms are not used in the extension UI

**Fix the known bug from `bb-data-binder.js`:** `bbb-show` / `bbb-hide`
currently only work inside `createTemplateInstance`. In `binder.ts`, make them
work on any element — evaluate conditions against the last bound data at scan
time and re-evaluate on every `bind()` call.

---

## Step 2 — Rewrite `index.html` declaratively

The authenticated panel section of `index.html` needs to be rebuilt with binding
attributes. The unauthenticated panel is mostly static and needs less change.

### Topbar

```html
<select class="topbar-org-select" bbb-action="switch-org">
  <!-- options rendered via bbb-template="org-option" -->
</select>

<span bbb-text="usage.plan_display_name" class="plan-badge-title"></span>
<span
  bbb-text="usage.daily_remaining_display"
  class="plan-badge-remaining"
></span>

<button class="topbar-profile btn btn--icon-square" bbb-action="open-settings">
  <span class="topbar-profile-avatar" bbb-user-avatar></span>
</button>
```

### Action bar

```html
<select class="action-bar-schedule" bbb-action="change-schedule">
  <!-- static options already in HTML -->
</select>

<label class="toggle">
  <input type="checkbox" bbb-action="toggle-run-on-publish" />
</label>

<button
  class="btn btn--primary"
  bbb-action="run-now"
  bbb-show="webflow.connected=true"
>
  Run now
</button>
```

### Job state — active

```html
<div bbb-auth="required">
  <div bbb-show="job.is_active=true">
    <span bbb-text="job.status_label"></span>
    <span bbb-text="job.progress_display"></span>
    <!-- spinner, etc. -->
  </div>

  <div bbb-show="job.is_active=false">
    <!-- no active job state -->
  </div>
</div>
```

### Latest result card

```html
<div bbb-template="latest-result">
  <span bbb-text="status_label"></span>
  <span bbb-text="completed_at_formatted"></span>
  <span bbb-text="counts.good"></span>
  <span bbb-text="counts.ok"></span>
  <span bbb-text="counts.error"></span>
  <span bbb-text="metrics.avg_display"></span>
  <span bbb-text="metrics.saved_display"></span>
</div>
```

### Past results list

```html
<div id="recentResultsList">
  <div bbb-template="past-result">
    <span bbb-text="status_label"></span>
    <span bbb-text="completed_at_formatted"></span>
    <span bbb-text="counts.good"></span>
    <span bbb-text="counts.error"></span>
  </div>
</div>
```

### Chart

The mini chart bars are currently built imperatively. Declare a template:

```html
<div id="miniChart" class="chart-bars">
  <div bbb-template="chart-bar">
    <div
      class="chart-bar"
      bbb-style:height="{height_pct}%"
      bbb-class="chart-bar {bar_class}"
    ></div>
  </div>
</div>
```

---

## Step 3 — Gut `index.ts`

### Remove entirely

These functions exist solely to build or update DOM and must be deleted:

- `renderJobState` (line 1228) — replaced by `binder.bind()`
- `buildResultCard` (line 1451) — replaced by `binder.renderTemplate()`
- `renderRecentResults` (line 1395) — replaced by `binder.renderTemplate()`
- `renderMiniChart` — replaced by `binder.renderTemplate()`
- `renderUsage` — replaced by `binder.bind()`
- `renderOrganisations` — replaced by `binder.renderTemplate()`
- `renderScheduleState` — replaced by `binder.bind()`
- `renderWebflowStatus` — replaced by `binder.bind()`
- `renderAuthState` — replaced by `binder.setAuth()`
- `setText` (line 629) — replaced by binder
- `show` / `hide` (lines 617, 623) — replaced by binder
- `updateAvatarFromState` — replaced by `bbb-user-avatar` binding
- `getInitials` / `getGravatarUrl` / `renderAvatar` — avatar handled via
  postMessage `avatarUrl` field, rendered by binder

### Keep but simplify

These functions stay but stop touching the DOM directly:

- `refreshDashboard` — calls `binder.bind(buildStateData())` and
  `binder.renderTemplate(...)` instead of individual render functions
- `loadUsageAndOrgs` — fetches data, returns it, no DOM
- `loadLatestJob` — fetches data, returns it, no DOM
- `loadCurrentSchedule` — fetches data, returns it, no DOM
- `subscribeToJobUpdates` / realtime — calls `refreshDashboard` on update, no
  DOM
- `connectAccount` — calls `binder.setAuth(true)` after postMessage, no DOM
- `refreshCurrentJob` — fetches, updates state, calls `binder.bind()`
- `renderIssuesTable` — this builds a detailed task table; keep imperative for
  now, address in a follow-up

### Keep unchanged

- All API request logic (`apiRequest`, `parseApiResponse`, `fetchIssueTasks`)
- All Webflow SDK calls (`webflow.getSiteInfo`, `webflow.setExtensionSize`)
- All auth/token logic (`getStoredToken`, `setStoredToken`,
  `fetchSupabaseConfig`, `initSupabaseClient`, `connectAccount`)
- All realtime/polling logic (`subscribeToJobUpdates`, `startFallbackPolling`,
  etc.)
- All data formatting utilities (`statusLabelForJob`, `formatShortDate`,
  `getIssueCounts`, etc.)

---

## Step 4 — Wire binder into `index.ts`

Initialise the binder once at startup:

```ts
import { Binder } from "./binder";

const binder = new Binder({ debug: false });
binder.scan();

// Register all action handlers
binder.on("run-now", () => void triggerJob());
binder.on("switch-org", (el) => void switchOrganisation(el));
binder.on("change-schedule", (el) => void updateSchedule(el));
binder.on("toggle-run-on-publish", (el) => void toggleRunOnPublish(el));
binder.on("open-settings", () => openSettings());
```

After any state change, call `binder.bind()` with a flat data object built from
`state`:

```ts
function buildBindData() {
  return {
    "usage.plan_display_name": state.usage?.plan_display_name ?? "",
    "usage.daily_remaining_display": formatRemaining(state.usage),
    "job.is_active": isActiveJobStatus(state.currentJob?.status ?? ""),
    "job.status_label": statusLabelForJob(state.currentJob?.status ?? ""),
    "job.progress_display": formatProgress(state.currentJob),
    "webflow.connected": state.webflowConnected,
    // ... etc
  };
}

binder.bind(buildBindData());
```

Render templates when list data changes:

```ts
binder.renderTemplate("past-result", buildRecentResultItems(recentJobs));
binder.renderTemplate("chart-bar", buildChartBarItems(jobs));
binder.renderTemplate("org-option", buildOrgOptions(state.organisations));
```

Set auth state after postMessage and on token loss:

```ts
binder.setAuth(true); // after successful connectAccount()
binder.setAuth(false); // on token expiry or sign out
```

---

## Step 5 — Avatar via postMessage

The `auth.js` postMessage already sends `user.avatarUrl` from
`session.user.user_metadata.avatar_url`. The binder should handle
`bbb-user-avatar` as a special binding:

When `binder.setAuth(true, { avatarUrl, displayName })` is called:

- find all `[bbb-user-avatar]` elements
- set initials as `textContent` immediately
- if `avatarUrl` is provided, create an `<img>` and swap it in on load

This logic lives in `binder.ts`, not `index.ts`. It mirrors what `auth.js` does
for `#userAvatar` on the main app — same pattern, portable implementation.

---

## State data shape

`index.ts` should build a normalised flat data object for `binder.bind()`. Shape
reference:

```ts
type BindData = {
  // Usage / plan
  "usage.plan_display_name": string;
  "usage.daily_limit_display": string;
  "usage.daily_remaining_display": string;
  "usage.daily_remaining_value": number;

  // Current job
  "job.is_active": boolean;
  "job.status_label": string;
  "job.status_class": string;
  "job.progress_display": string;
  "job.domain": string;

  // Webflow connection
  "webflow.connected": boolean;
  "webflow.site_name": string;

  // Schedule
  "schedule.value": string;
  "schedule.run_on_publish": boolean;
};
```

Template item shapes are defined per template — see existing `buildResultCard`
and `renderOrganisations` in `index.ts` for the data currently being rendered;
normalise those into flat objects.

---

## Naming

All new code follows the naming conventions in
`docs/plans/unified-frontend-es-modules-plan.md`:

- internal TypeScript files: generic names (`binder.ts`, `state.ts`, etc.)
- browser-facing binding attributes: keep `bbb-*` for consistency with the main
  app during this transition
- do not introduce new `bb-*`, `BB_*` or `BBB_*` names
- the `Binder` class in `binder.ts` may be renamed `HoverBinder` if a
  browser-global export is needed, but within the module keep it as `Binder`

---

## What does NOT change in this migration

- The Webflow SDK integration (`webflow.getSiteInfo`,
  `webflow.setExtensionSize`)
- The auth popup flow and postMessage contract
- The Supabase realtime subscription pattern
- All API endpoints and request logic
- The existing `public/styles.css` — visual design is not in scope here
- The task issues table (`renderIssuesTable`) — complex enough to defer

---

## Validation

After the migration, verify:

1. Unauthenticated state shows the login panel
2. Clicking connect opens the auth popup and completes sign-in
3. Authenticated state shows the topbar with org, plan, avatar
4. Job card renders for an active job with correct status and progress
5. Latest result card renders for a completed job
6. Past results list renders multiple cards
7. Chart bars render with correct heights
8. Run Now fires a job
9. Schedule select updates and saves
10. Run on publish toggle updates and saves
11. Org switcher changes the active org
12. Sign out returns to unauthenticated state
13. Realtime update refreshes the job card without full reload

---

## Related files

| File                                               | Role                                                     |
| -------------------------------------------------- | -------------------------------------------------------- |
| `webflow-designer-extension-cli/src/index.ts`      | Main extension logic — to be gutted of DOM manipulation  |
| `webflow-designer-extension-cli/src/binder.ts`     | New — portable binding system                            |
| `webflow-designer-extension-cli/public/index.html` | Extension HTML — to be rewritten with binding attributes |
| `webflow-designer-extension-cli/public/styles.css` | Extension styles — unchanged                             |
| `web/static/js/bb-data-binder.js`                  | Reference implementation to port from                    |
| `web/static/js/auth.js`                            | Reference for avatar/user identity population pattern    |
| `dashboard.html`                                   | Reference for binding attribute usage in practice        |
| `web/templates/job-details.html`                   | Reference for binding attribute usage in practice        |
| `docs/plans/unified-frontend-es-modules-plan.md`   | Overarching frontend architecture plan                   |
