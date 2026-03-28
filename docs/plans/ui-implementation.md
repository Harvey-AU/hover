# UI Implementation Plan

## Overview

Hover provides a template + data binding system that allows users to build
custom HTML layouts whilst the JavaScript handles data fetching, authentication,
and real-time updates.

## User Interface Strategy

**Primary Interface:** Template + data binding system for building Blue Banded
Bee's own product dashboard **Secondary Interfaces:**

- **Webflow App:** Installed in user's Webflow workspace, shows crawl status and
  controls for their sites
- **Slack Bot:** Simple commands (`/crawl sitedomain.com`) with threaded
  progress updates

**Integration Philosophy:**

- **Template binding** allows flexible dashboard design for Hover's own product
- **External integrations** provide simple, focused functionality within user's
  existing workflows

## Architecture Approach

### Template + Data Binding System

Hover's own dashboard pages use `data-gnh-bind` attributes. The JavaScript
library finds these elements and populates them with live data from the API.

**Template system controls:**

- All HTML structure and CSS styling for Hover's dashboard
- Page layout and design positioning
- Visual appearance and branding

**JavaScript handles:**

- Data fetching from API endpoints
- Authentication with Supabase
- Real-time updates and live syncing
- Finding and populating template elements

### Integration Method

```html
<!-- Hover dashboard HTML design -->
<div class="gnh-dashboard-design">
  <div class="stat-card">
    <h3>Total Jobs</h3>
    <span class="big-number" data-gnh-bind="total_jobs">0</span>
  </div>

  <div class="job-list">
    <div class="job-template" data-gnh-template="job">
      <h4 data-gnh-bind="domain">Domain loading...</h4>
      <div class="progress-bar">
        <div class="fill" data-gnh-bind-style="width:{progress}%"></div>
      </div>
      <span data-gnh-bind="status">pending</span>
    </div>
  </div>
</div>

<!-- Single script inclusion -->
<script src="https://hover.app.goodnative.co/js/gnh-data-binder.js"></script>
```

## Data Binding Attributes

### Basic Data Binding

- `data-gnh-bind="field_name"` - Binds element's text content to API data field
- `data-gnh-bind-attr="href:{url}"` - Binds element attributes
- `data-gnh-bind-style="width:{progress}%"` - Binds CSS styles with formatting

### Template Binding

- `data-gnh-template="template_name"` - Marks element as template for repeated
  data
- Templates are cloned and populated for each data item

### Authentication Elements

- `data-gnh-auth="required"` - Shows element only when authenticated
- `data-gnh-auth="guest"` - Shows element only when not authenticated

## API Integration

### Data Sources

The JavaScript automatically fetches data from these endpoints:

**Dashboard Data:**

- `/v1/dashboard/stats` - Job statistics and counts
- `/v1/jobs` - Recent jobs list with progress

**Real-time Updates:**

- Supabase Realtime for live job progress
- Automatic re-fetch on data changes

### Authentication Flow

1. Supabase Auth handles login/logout
2. JWT tokens automatically included in API requests
3. Page elements shown/hidden based on auth state
4. Template binding paused until authenticated

## Implementation Phases

### ✅ Phase 1: Attribute-Based Event Handling (Completed v0.5.3)

- Event delegation system with `gnh-action` attributes
- Dashboard demonstration with vanilla JavaScript
- API integration for stats and job data
- Foundation for template-based approach

### ✅ Phase 2: Data Binding Implementation (Completed v0.5.4)

- ✅ `data-gnh-bind` attribute processing for dynamic content
- ✅ Template engine for `data-gnh-template` repeated elements
- ✅ Authentication integration with conditional displays (`data-gnh-auth`)
- ✅ Form handling and user input processing (`data-gnh-form`)
- ✅ Real-time validation with `data-gnh-validate` attributes
- ✅ Style and attribute binding with `data-gnh-bind-style` and
  `data-gnh-bind-attr`
- ✅ Built and deployed `gnh-data-binder.min.js` library

### 🔴 Phase 3: Advanced Features

- Real-time updates via Supabase Realtime
- Progress indicators and live job updates
- Error handling and user feedback systems
- Enhanced template features and custom events

## Development Approach

### JavaScript Library Structure

```javascript
class BBDataBinder {
  constructor() {
    this.authManager = new AuthManager();
    this.apiClient = new APIClient();
    this.templateEngine = new TemplateEngine();
  }

  async init() {
    await this.authManager.init();
    this.bindElements();
    this.setupRealtime();
  }

  bindElements() {
    // Find and bind data-gnh-bind elements
    const elements = document.querySelectorAll("[data-gnh-bind]");
    elements.forEach((el) => this.bindElement(el));
  }

  async fetchData(endpoint) {
    return this.apiClient.get(endpoint);
  }
}
```

### Real-time Updates

```javascript
// Real-time communication flow
User Action → Template Update → API Request → Database
     ↓              ↑              ↓           ↓
UI Update ← Supabase Realtime ← Database Trigger
```

## External Integrations

### Webflow App Integration

**User Experience:**

1. User installs Hover app in their Webflow workspace
2. Opens app within Webflow Designer interface
3. Logs in with existing Supabase Auth (same as main website)
4. Views last crawl status for current Webflow site
5. Can trigger "Crawl Now" or enable "Auto-crawl on publish"

**Technical Implementation:**

- Uses existing `/v1/jobs` API endpoints
- Integrates with Supabase Auth (no separate auth system)
- Webhook integration for automatic crawling on site publish
- Site detection from Webflow context

### Slack Bot Integration

**User Experience:**

1. Install Hover Slack app in workspace
2. Use `/crawl sitedomain.com` command to start cache warming
3. Receive progress updates as thread replies
4. Get completion summary with link to main dashboard

**Technical Implementation:**

- Uses existing `/v1/jobs` API endpoints
- Integrates with Supabase Auth system
- Real-time updates via existing job status APIs

## Performance Considerations

### Loading Strategy

- Lightweight JavaScript library (~50KB)
- Progressive enhancement approach
- Only fetches data when elements are present
- Efficient DOM querying and updates

### API Optimisation

- Intelligent caching of static data
- Batched API requests where possible
- Debounced real-time updates
- Minimal DOM manipulation

## Security Implementation

### Authentication

- JWT tokens with automatic refresh
- Secure token storage
- Authentication state management
- Protected data binding (auth-only content)

### Data Protection

- Input sanitisation for all bound data
- XSS protection in template rendering
- CSRF protection for API requests

## Launch Checklist

### Core Features

- [ ] Data binding system implementation
- [ ] Authentication integration
- [ ] Template engine for repeated content
- [ ] Real-time updates via Supabase

### Webflow Integration

- [ ] Test embedding in Webflow pages
- [ ] Verify no CSS conflicts
- [ ] Responsive design compatibility
- [ ] Performance optimisation

### Documentation

- [ ] Integration guide for users
- [ ] Data binding reference
- [ ] Example templates and layouts
- [ ] Troubleshooting guide
