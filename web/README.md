# Hover Frontend Libraries

Frontend components and data binding library for Hover dashboard integration
with Webflow.

## Available Libraries

- **Web Components** (`bb-components.js`): Custom elements for authentication
  and data display
- **Data Binding Library** (`bb-data-binder.js`): Template + data binding system
  for flexible dashboard development
- **Dashboard Actions** (`bb-dashboard-actions.js`): Dashboard-specific
  functionality and interactions
- **Auth Extension** (`bb-auth-extension.js`): Extended authentication features
- **Auth** (`auth.js`): Core authentication system

## Quick Start

### For Webflow Integration

1. **Add scripts to your Webflow page head:**

```html
<!-- Supabase Authentication -->
<script src="https://cdn.jsdelivr.net/npm/@supabase/supabase-js@2"></script>

<!-- Initialize Supabase -->
<script>
  window.supabase = window.supabase.createClient(
    "YOUR_SUPABASE_URL",
    "YOUR_SUPABASE_ANON_KEY"
  );
</script>

<!-- Hover Libraries -->
<script src="https://hover.app.goodnative.co/js/bb-components.js"></script>
<!-- OR for data binding approach -->
<script src="https://hover.app.goodnative.co/js/bb-data-binder.js"></script>
```

2. **Design your templates in Webflow Designer:**

```html
<div class="jobs-grid">
  <!-- Design this card visually in Webflow -->
  <div class="job-card template">
    <h3 data-bind="domain">example.com</h3>
    <span data-bind="status">running</span>
    <div class="progress-bar">
      <div data-style-bind="width:progress.percentage%"></div>
    </div>
    <p data-bind="progress_text">Loading...</p>
  </div>
</div>
```

3. **Add data component via HTML Embed:**

```html
<bb-data-loader
  endpoint="/v1/jobs"
  template=".job-card.template"
  target=".jobs-grid"
  auto-load="true"
  require-auth="true"
>
</bb-data-loader>
```

## Development Workflow

### Making Changes

1. **Edit components:** Modify files in `/src/`
2. **Build:** Run `npm run build`
3. **Commit:** Push built files to GitHub
4. **Deploy:** Fly builds and serves files automatically

### Local Development

```bash
# Install dependencies
npm install

# Build for production
npm run build

# Serve locally for testing
npm run serve
```

### File Structure

```
web/
├── static/
│   └── js/                     # All JavaScript files
│       ├── bb-components.js        # Web Components
│       ├── bb-data-binder.js       # Data binding library
│       ├── bb-dashboard-actions.js # Dashboard functionality
│       ├── bb-auth-extension.js    # Auth extensions
│       └── auth.js                 # Core authentication
└── examples/                   # Integration examples
    ├── data-binding-example.html
    ├── form-example.html
    └── webflow-integration.html
```

## Data Binding Library (v0.5.4)

The `BBDataBinder` library provides a template + data binding system that allows
you to create flexible HTML layouts while JavaScript automatically handles data
fetching, authentication, and real-time updates.

### Quick Example

```html
<!-- Include the library -->
<script src="https://hover.app.goodnative.co/js/bb-data-binder.js"></script>

<!-- Dashboard stats with data binding -->
<div class="stats">
  <span data-bb-bind="stats.total_jobs">0</span>
  <span data-bb-bind="stats.running_jobs">0</span>
</div>

<!-- Job list with templates -->
<div data-bb-template="job">
  <h4 data-bb-bind="domain">Loading...</h4>
  <div data-bb-bind-style="width:{progress}%"></div>
  <span data-bb-bind="status">pending</span>
</div>

<!-- Forms with validation -->
<form data-bb-form="create-job" data-bb-validate="live">
  <input name="domain" required data-bb-validate-type="url" />
  <button type="submit">Create Job</button>
</form>

<!-- Initialize -->
<script>
  const binder = new BBDataBinder({ debug: true });
  await binder.init();
</script>
```

### Data Binding Attributes

- **`data-bb-bind="field"`** - Bind element text content to data field
- **`data-bb-bind-style="property:{field}"`** - Bind CSS styles with formatting
- **`data-bb-bind-attr="attribute:{field}"`** - Bind element attributes
- **`data-bb-template="name"`** - Mark element as template for repeated data
- **`data-bb-auth="required|guest"`** - Conditional rendering based on auth
  state
- **`data-bb-form="action"`** - Enable form handling with validation and API
  submission

### Form Validation Attributes

- **`data-bb-validate="live"`** - Enable real-time validation
- **`data-bb-validate-type="email|url|number"`** - Field type validation
- **`data-bb-validate-min="N"`** - Minimum length/value
- **`data-bb-validate-max="N"`** - Maximum length/value
- **`data-bb-validate-pattern="regex"`** - Custom pattern validation

## Available Components

### bb-data-loader

Core component for loading data from API and populating Webflow templates.

**Attributes:**

- `endpoint` - API endpoint to fetch from
- `template` - CSS selector for template element
- `target` - CSS selector for container to populate
- `auto-load` - Load data automatically on page load
- `require-auth` - Require user authentication
- `refresh-interval` - Auto-refresh interval in seconds

**Data Binding:**

- `data-bind="field"` - Bind text content to data field
- `data-style-bind="property:field"` - Bind CSS property to data field

### bb-auth-login

Authentication component with Supabase integration.

**Attributes:**

- `show-providers` - Show social login buttons
- `redirect-url` - URL to redirect after login
- `compact` - Use compact layout

## Production Deployment

The components are served as static files from your Fly.io app:

- Production: `https://hover.app.goodnative.co/js/bb-components.js`

## Architecture

- **Runtime:** Pure vanilla JavaScript Web Components
- **Build:** Node.js bundling (development only)
- **Dependencies:** Supabase loaded via CDN
- **Integration:** Template + data slots pattern with Webflow

## Examples

See `/examples/` directory for complete integration examples:

- `webflow-integration.html` - Production-ready Webflow example
- `complete-example.html` - Full-featured demo
- `dashboard-page.html` - Job dashboard example
- `job-details-page.html` - Job details page example
