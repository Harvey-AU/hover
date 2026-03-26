# Hover Attribute System

## Overview

Hover uses a custom attribute system for data binding, templating, and
interactions. All custom attributes use the `bbb-` namespace prefix (Blue Banded
Bee).

## Design Principles

1. **Consistent `bbb-` prefix** - All custom attributes use the same namespace
2. **No category prefixes** - Follow Alpine.js/Vue.js convention: `bbb-text` not
   `bbb-bind-text`
3. **Semantic naming** - Attribute names clearly indicate their purpose
4. **Modern HTML5** - No `data-` prefix for cleaner syntax
5. **Progressive enhancement** - Works without JavaScript (where possible)

## Quick Reference

### Data Binding

| Attribute        | Purpose                 | Example                                        | Actual Output                              |
| ---------------- | ----------------------- | ---------------------------------------------- | ------------------------------------------ |
| `bbb-text`       | Bind to text content    | `<div bbb-text="stats.total_jobs">-</div>`     | `<div>283</div>`                           |
| `bbb-class`      | Bind to class attribute | `<div bbb-class="status-{status}">Text</div>`  | `<div class="status-completed">Text</div>` |
| `bbb-href`       | Bind to href attribute  | `<a bbb-href="/jobs/{id}">View</a>`            | `<a href="/jobs/abc-123">View</a>`         |
| `bbb-attr:name`  | Bind to any attribute   | `<div bbb-attr:data-id="{id}">Item</div>`      | `<div data-id="abc-123">Item</div>`        |
| `bbb-style:prop` | Bind to CSS property    | `<div bbb-style:width="{progress}%">Bar</div>` | `<div style="width: 75%">Bar</div>`        |

### Templates & Conditionals

| Attribute      | Purpose                   | Example                                       | Behavior                                   |
| -------------- | ------------------------- | --------------------------------------------- | ------------------------------------------ |
| `bbb-template` | Define reusable template  | `<div bbb-template="job">...</div>`           | Cloned for each item in data array         |
| `bbb-show`     | Show if condition matches | `<div bbb-show="status=completed">Done</div>` | Visible when true, hidden when false       |
| `bbb-hide`     | Hide if condition matches | `<div bbb-hide="status=pending">Text</div>`   | Hidden when true, visible when false       |
| `bbb-if`       | Render if condition true  | `<div bbb-if="count>0">Items</div>`           | Added to DOM when true, removed when false |

### Interactions

| Attribute       | Purpose                | Example                                         | Behavior                      |
| --------------- | ---------------------- | ----------------------------------------------- | ----------------------------- |
| `bbb-action`    | Handle click events    | `<button bbb-action="refresh">Refresh</button>` | Calls action handler on click |
| `bbb-on:click`  | Explicit click handler | `<button bbb-on:click="save">Save</button>`     | Calls handler on click        |
| `bbb-on:submit` | Form submission        | `<form bbb-on:submit="create-job">...</form>`   | Calls handler on submit       |
| `bbb-submit`    | Form submit shorthand  | `<form bbb-submit="create-job">...</form>`      | Calls handler on submit       |

### Domain Search

| Attribute           | Purpose                           | Example                              | Behaviour                                                            |
| ------------------- | --------------------------------- | ------------------------------------ | -------------------------------------------------------------------- |
| `bbb-domain-create` | Control domain creation behaviour | `<input bbb-domain-create="auto" />` | auto: Enter creates, option: shows create option, block: no creation |
| `bbb-domain-search` | Enable domain search dropdown     | `<input bbb-domain-search="off" />`  | on (default): enable search, off/disabled: no search wiring          |

### Metadata & Help

| Attribute     | Purpose                | Example                                       | Behavior                                     |
| ------------- | ---------------------- | --------------------------------------------- | -------------------------------------------- |
| `bbb-help`    | Reference metadata key | `<div bbb-help="cache_hit_rate">Label</div>`  | Adds (i) icon with tooltip from metadata API |
| `bbb-tooltip` | Direct tooltip text    | `<div bbb-tooltip="Helpful text">Label</div>` | Adds (i) icon with literal text              |

### Authentication

| Attribute  | Purpose                  | Example                                  | Behavior                                  |
| ---------- | ------------------------ | ---------------------------------------- | ----------------------------------------- |
| `bbb-auth` | Show based on auth state | `<div bbb-auth="required">Content</div>` | Visible only if user is authenticated     |
| `bbb-auth` | Show for guests          | `<div bbb-auth="guest">Login</div>`      | Visible only if user is NOT authenticated |

### Data Storage

| Attribute   | Purpose                 | Example                                                     | Behavior                    |
| ----------- | ----------------------- | ----------------------------------------------------------- | --------------------------- |
| `bbb-id`    | Store ID for handler    | `<button bbb-action="view" bbb-id="{job.id}">View</button>` | Passes ID to action handler |
| `bbb-value` | Store value for handler | `<option bbb-value="{limit}">50 items</option>`             | Stores value for retrieval  |

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
<div bbb-text="stats.total_jobs">-</div>
<!-- Renders: <div>283</div> -->

<!-- Dynamic classes -->
<div bbb-class="bb-status-{status}" bbb-text="status">pending</div>
<!-- Renders: <div class="bb-status-completed">completed</div> -->

<!-- Multiple styles -->
<div bbb-style:width="{progress}%" bbb-style:background-color="{color}"></div>
<!-- Renders: <div style="width: 75%; background-color: #10b981"></div> -->
```

### Templates & Conditionals

```html
<!-- Template cloned for each job -->
<div bbb-template="job">
  <div bbb-text="domain">example.com</div>
  <button bbb-action="view-job-details" bbb-id="{id}">View</button>
</div>

<!-- Conditional visibility -->
<button bbb-show="status=completed,failed">Restart</button>
<div bbb-if="stats.failed_jobs>0">
  <span bbb-text="stats.failed_jobs">0</span> jobs failed
</div>
```

### Interactions

```html
<!-- Button actions -->
<button bbb-action="refresh-dashboard">Refresh</button>
<button bbb-action="view-job-details" bbb-id="{id}">View</button>

<!-- Form submission -->
<form bbb-submit="create-job">
  <input name="domain" required />
  <button type="submit">Create</button>
</form>
```

### Metadata & Help

```html
<!-- Metadata-driven tooltips -->
<th bbb-help="task_response_time">Response Time</th>
<div bbb-help="cache_hit_rate">Cache Hit Rate</div>

<!-- Direct tooltips -->
<div bbb-tooltip="Click to refresh">
  <button bbb-action="refresh">↻</button>
</div>
```

### Authentication

```html
<!-- Protected content -->
<div bbb-auth="required">
  <span bbb-text="user.email">-</span>
  <button bbb-action="logout">Logout</button>
</div>

<!-- Guest content -->
<div bbb-auth="guest">
  <button bbb-action="show-login">Login</button>
</div>
```

## Complete Example

See `/dashboard.html` for a full working implementation showing all patterns in
context.

Key sections to review:

- **Stats cards** (lines ~1407-1419) - Data binding with `bbb-text`
- **Job cards** (lines ~1436-1467) - Templates with `bbb-template`
- **Task table** - Dynamic table generation (rendered in JavaScript)
- **Auth flow** (lines ~1273-1278) - Auth-based visibility with `bbb-auth`
- **Modals** (lines ~1537+) - Complex binding with nested fields

## Migration from Old System

### Attribute Mapping

| Old Attribute        | New Attribute                            | Notes                 |
| -------------------- | ---------------------------------------- | --------------------- |
| `data-bb-bind`       | `bbb-text`                               | More semantic name    |
| `data-bb-bind-attr`  | `bbb-class`, `bbb-href`, `bbb-attr:name` | Simpler syntax        |
| `data-bb-bind-style` | `bbb-style:prop`                         | Property-specific     |
| `data-bb-template`   | `bbb-template`                           | Remove `data-` prefix |
| `data-bb-show-if`    | `bbb-show`                               | Shorter, clearer      |
| `data-bb-auth`       | `bbb-auth`                               | Remove `data-` prefix |
| `bb-action`          | `bbb-action`                             | Consistent prefix     |
| `data-bb-info`       | `bbb-help`                               | More accurate name    |

### Migration Strategy

**Phase 1: Update JavaScript** (backwards compatible)

- Modify `bb-data-binder.js` to recognise both old and new attributes
- Update `bb-dashboard-actions.js` action delegation
- Update `bb-metadata.js` to use `bbb-help`

**Phase 2: Migrate HTML** (incremental)

- Update one section at a time
- Test thoroughly after each section
- Keep old attributes working during migration

**Phase 3: Cleanup** (once migration complete)

- Remove old attribute support from JavaScript
- Verify all HTML uses new attributes

## Best Practices

1. **Use semantic names** - `bbb-text` not `bbb-bind`
2. **Keep conditions simple** - `status=completed` not complex expressions
3. **Templates for lists** - More efficient than manual DOM manipulation
4. **Add help tooltips** - Use `bbb-help` for all metrics and table headers
5. **Test without JavaScript** - Ensure graceful degradation with static content
6. **Prefer `bbb-show` for visibility** - Use `bbb-if` only when DOM removal
   needed
7. **Combine attributes** - `bbb-action` + `bbb-id` + `bbb-show` work well
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

- Implementation: `/web/static/js/bb-data-binder.js`
- Actions: `/web/static/js/bb-dashboard-actions.js`
- Metadata: `/web/static/js/bb-metadata.js`, `/internal/api/metadata.go`
- Example: `/dashboard.html`
