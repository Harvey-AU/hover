# Hover Attribute System

## Overview

Hover uses a custom attribute system for data binding, templating, and
interactions. All custom attributes use the `gnh-` namespace prefix (Blue Banded
Bee).

## Design Principles

1. **Consistent `gnh-` prefix** - All custom attributes use the same namespace
2. **No category prefixes** - Follow Alpine.js/Vue.js convention: `gnh-text` not
   `gnh-bind-text`
3. **Semantic naming** - Attribute names clearly indicate their purpose
4. **Modern HTML5** - No `data-` prefix for cleaner syntax
5. **Progressive enhancement** - Works without JavaScript (where possible)

## Quick Reference

### Data Binding

| Attribute        | Purpose                 | Example                                        | Actual Output                              |
| ---------------- | ----------------------- | ---------------------------------------------- | ------------------------------------------ |
| `gnh-text`       | Bind to text content    | `<div gnh-text="stats.total_jobs">-</div>`     | `<div>283</div>`                           |
| `gnh-class`      | Bind to class attribute | `<div gnh-class="status-{status}">Text</div>`  | `<div class="status-completed">Text</div>` |
| `gnh-href`       | Bind to href attribute  | `<a gnh-href="/jobs/{id}">View</a>`            | `<a href="/jobs/abc-123">View</a>`         |
| `gnh-attr:name`  | Bind to any attribute   | `<div gnh-attr:data-id="{id}">Item</div>`      | `<div data-id="abc-123">Item</div>`        |
| `gnh-style:prop` | Bind to CSS property    | `<div gnh-style:width="{progress}%">Bar</div>` | `<div style="width: 75%">Bar</div>`        |

### Templates & Conditionals

| Attribute      | Purpose                   | Example                                       | Behavior                                   |
| -------------- | ------------------------- | --------------------------------------------- | ------------------------------------------ |
| `gnh-template` | Define reusable template  | `<div gnh-template="job">...</div>`           | Cloned for each item in data array         |
| `gnh-show`     | Show if condition matches | `<div gnh-show="status=completed">Done</div>` | Visible when true, hidden when false       |
| `gnh-hide`     | Hide if condition matches | `<div gnh-hide="status=pending">Text</div>`   | Hidden when true, visible when false       |
| `gnh-if`       | Render if condition true  | `<div gnh-if="count>0">Items</div>`           | Added to DOM when true, removed when false |

### Interactions

| Attribute       | Purpose                | Example                                         | Behavior                      |
| --------------- | ---------------------- | ----------------------------------------------- | ----------------------------- |
| `gnh-action`    | Handle click events    | `<button gnh-action="refresh">Refresh</button>` | Calls action handler on click |
| `gnh-on:click`  | Explicit click handler | `<button gnh-on:click="save">Save</button>`     | Calls handler on click        |
| `gnh-on:submit` | Form submission        | `<form gnh-on:submit="create-job">...</form>`   | Calls handler on submit       |
| `gnh-submit`    | Form submit shorthand  | `<form gnh-submit="create-job">...</form>`      | Calls handler on submit       |

### Domain Search

| Attribute           | Purpose                           | Example                              | Behaviour                                                            |
| ------------------- | --------------------------------- | ------------------------------------ | -------------------------------------------------------------------- |
| `gnh-domain-create` | Control domain creation behaviour | `<input gnh-domain-create="auto" />` | auto: Enter creates, option: shows create option, block: no creation |
| `gnh-domain-search` | Enable domain search dropdown     | `<input gnh-domain-search="off" />`  | on (default): enable search, off/disabled: no search wiring          |

### Metadata & Help

| Attribute     | Purpose                | Example                                       | Behavior                                     |
| ------------- | ---------------------- | --------------------------------------------- | -------------------------------------------- |
| `gnh-help`    | Reference metadata key | `<div gnh-help="cache_hit_rate">Label</div>`  | Adds (i) icon with tooltip from metadata API |
| `gnh-tooltip` | Direct tooltip text    | `<div gnh-tooltip="Helpful text">Label</div>` | Adds (i) icon with literal text              |

### Authentication

| Attribute  | Purpose                  | Example                                  | Behavior                                  |
| ---------- | ------------------------ | ---------------------------------------- | ----------------------------------------- |
| `gnh-auth` | Show based on auth state | `<div gnh-auth="required">Content</div>` | Visible only if user is authenticated     |
| `gnh-auth` | Show for guests          | `<div gnh-auth="guest">Login</div>`      | Visible only if user is NOT authenticated |

### Data Storage

| Attribute   | Purpose                 | Example                                                     | Behavior                    |
| ----------- | ----------------------- | ----------------------------------------------------------- | --------------------------- |
| `gnh-id`    | Store ID for handler    | `<button gnh-action="view" gnh-id="{job.id}">View</button>` | Passes ID to action handler |
| `gnh-value` | Store value for handler | `<option gnh-value="{limit}">50 items</option>`             | Stores value for retrieval  |

## Syntax Guide

### Value Formats

- **Dynamic values**: Use `{field}` → `{id}`, `{status}`, `{progress}`
- **Nested fields**: Use dots → `stats.total_jobs`, `user.email`
- **Combined**: Mix static and dynamic → `status-{status}`, `/jobs/{id}`

### Condition Formats

- **Equality**: `status=completed`
- **Multiple values**: `status=completed,failed,cancelled`
- **Comparison**: `count>0`, `tasks.length>0`
- **Not equal**: `status!=pending`

### Action Names

- Use kebab-case: `refresh-dashboard`, `view-job-details`, `create-job`
- Handlers defined in JavaScript: `actions.refreshDashboard()`,
  `actions.viewJobDetails()`

## Common Patterns

### Data Binding

```html
<!-- Text content -->
<div gnh-text="stats.total_jobs">-</div>
<!-- Renders: <div>283</div> -->

<!-- Dynamic classes -->
<div gnh-class="gnh-status-{status}" gnh-text="status">pending</div>
<!-- Renders: <div class="gnh-status-completed">completed</div> -->

<!-- Multiple styles -->
<div gnh-style:width="{progress}%" gnh-style:background-color="{color}"></div>
<!-- Renders: <div style="width: 75%; background-color: #10b981"></div> -->
```

### Templates & Conditionals

```html
<!-- Template cloned for each job -->
<div gnh-template="job">
  <div gnh-text="domain">example.com</div>
  <button gnh-action="view-job-details" gnh-id="{id}">View</button>
</div>

<!-- Conditional visibility -->
<button gnh-show="status=completed,failed">Restart</button>
<div gnh-if="stats.failed_jobs>0">
  <span gnh-text="stats.failed_jobs">0</span> jobs failed
</div>
```

### Interactions

```html
<!-- Button actions -->
<button gnh-action="refresh-dashboard">Refresh</button>
<button gnh-action="view-job-details" gnh-id="{id}">View</button>

<!-- Form submission -->
<form gnh-submit="create-job">
  <input name="domain" required />
  <button type="submit">Create</button>
</form>
```

### Metadata & Help

```html
<!-- Metadata-driven tooltips -->
<th gnh-help="task_response_time">Response Time</th>
<div gnh-help="cache_hit_rate">Cache Hit Rate</div>

<!-- Direct tooltips -->
<div gnh-tooltip="Click to refresh">
  <button gnh-action="refresh">↻</button>
</div>
```

### Authentication

```html
<!-- Protected content -->
<div gnh-auth="required">
  <span gnh-text="user.email">-</span>
  <button gnh-action="logout">Logout</button>
</div>

<!-- Guest content -->
<div gnh-auth="guest">
  <button gnh-action="show-login">Login</button>
</div>
```

## Complete Example

See `/dashboard.html` for a full working implementation showing all patterns in
context.

Key sections to review:

- **Stats cards** (lines ~1407-1419) - Data binding with `gnh-text`
- **Job cards** (lines ~1436-1467) - Templates with `gnh-template`
- **Task table** - Dynamic table generation (rendered in JavaScript)
- **Auth flow** (lines ~1273-1278) - Auth-based visibility with `gnh-auth`
- **Modals** (lines ~1537+) - Complex binding with nested fields

## Migration from Old System

### Attribute Mapping

| Old Attribute        | New Attribute                            | Notes                 |
| -------------------- | ---------------------------------------- | --------------------- |
| `data-gnh-bind`       | `gnh-text`                               | More semantic name    |
| `data-gnh-bind-attr`  | `gnh-class`, `gnh-href`, `gnh-attr:name` | Simpler syntax        |
| `data-gnh-bind-style` | `gnh-style:prop`                         | Property-specific     |
| `data-gnh-template`   | `gnh-template`                           | Remove `data-` prefix |
| `data-gnh-show-if`    | `gnh-show`                               | Shorter, clearer      |
| `data-gnh-auth`       | `gnh-auth`                               | Remove `data-` prefix |
| `gnh-action`          | `gnh-action`                             | Consistent prefix     |
| `data-gnh-info`       | `gnh-help`                               | More accurate name    |

### Migration Strategy

**Phase 1: Update JavaScript** (backwards compatible)

- Modify `gnh-data-binder.js` to recognise both old and new attributes
- Update `gnh-dashboard-actions.js` action delegation
- Update `gnh-metadata.js` to use `gnh-help`

**Phase 2: Migrate HTML** (incremental)

- Update one section at a time
- Test thoroughly after each section
- Keep old attributes working during migration

**Phase 3: Cleanup** (once migration complete)

- Remove old attribute support from JavaScript
- Verify all HTML uses new attributes

## Best Practices

1. **Use semantic names** - `gnh-text` not `gnh-bind`
2. **Keep conditions simple** - `status=completed` not complex expressions
3. **Templates for lists** - More efficient than manual DOM manipulation
4. **Add help tooltips** - Use `gnh-help` for all metrics and table headers
5. **Test without JavaScript** - Ensure graceful degradation with static content
6. **Prefer `gnh-show` for visibility** - Use `gnh-if` only when DOM removal
   needed
7. **Combine attributes** - `gnh-action` + `gnh-id` + `gnh-show` work well
   together

## Technical Details

**Performance:**

- Metadata loaded once on page load and cached
- Templates cloned efficiently using `cloneNode()`
- Event delegation for actions (single listener per page)
- Minimal DOM queries using attribute selectors

**Browser Support:**

- Modern browsers with ES6 support
- Custom attributes work in all browsers
- Graceful degradation: Static content shows without JavaScript

**Resources:**

- Implementation: `/web/static/js/gnh-data-binder.js`
- Actions: `/web/static/js/gnh-dashboard-actions.js`
- Metadata: `/web/static/js/gnh-metadata.js`, `/internal/api/metadata.go`
- Example: `/dashboard.html`
