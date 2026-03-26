# Webflow Integration Strategy

## Overview

Hover integrates with Webflow to automatically warm site caches after
publishing, ensuring fast load times for real visitors and detecting errors
before users encounter them.

## Core Value Proposition

**The Problem:**

- Webflow sites clear CDN cache when published
- First visitors experience slow load times while pages regenerate
- Publishing errors aren't immediately apparent
- Manual cache warming is time-consuming and unreliable

**Our Solution:**

- Automatically crawl entire site immediately after publishing
- Warm cache so real visitors get fast load times from first click
- Detect errors before real users encounter them
- Real-time progress display in Webflow Designer
- Support for scheduled maintenance crawls

## Technical Implementation

### 1. Webflow App Registration

**Developer Setup:**

```bash
# Register as Webflow developer
# Create Data Client App with OAuth support
# Request minimal scope: sites:read
```

**Required Permissions:**

- `sites:read` - Access site structure and URLs
- `sites:publish` - Subscribe to publish events (webhook)

### 2. Automatic Trigger System

**Webhook Implementation:**

```javascript
// Webhook endpoint: POST /webhooks/webflow
app.post("/webhooks/webflow", async (req, res) => {
  // Verify webhook signature
  const signature = req.headers["x-webflow-signature"];
  if (!verifyWebflowSignature(req.body, signature)) {
    return res.status(401).send("Invalid signature");
  }

  // Handle site_publish event
  if (req.body.triggerType === "site_publish") {
    const { siteId, domain } = req.body;
    await createAutomaticCacheWarmingJob(siteId, domain);
  }

  res.status(200).send("OK");
});
```

**Event Types:**

- `site_publish` - Triggered when site is published
- `domain_connect` - When custom domain is connected
- `domain_disconnect` - When domain is removed

### 3. Webflow Designer Extension

**Extension Components:**

_Progress Indicator:_

```javascript
// Shows in Designer sidebar during cache warming
class CacheWarmingProgress extends HTMLElement {
  connectedCallback() {
    this.innerHTML = `
      <div class="cache-warming-panel">
        <h3>Cache Warming Progress</h3>
        <div class="progress-bar">
          <div class="progress-fill" style="width: ${this.progress}%"></div>
        </div>
        <div class="stats">
          <span>${this.completed}/${this.total} pages complete</span>
          <span>ETA: ${this.eta}</span>
        </div>
      </div>
    `;
  }
}
```

_Configuration Panel:_

```javascript
// Settings for cache warming behavior
class CacheWarmingSettings extends HTMLElement {
  render() {
    return `
      <div class="settings-panel">
        <label>
          <input type="checkbox" checked> Auto-warm on publish
        </label>
        <label>
          Schedule: 
          <select>
            <option value="off">Off</option>
            <option value="daily">Daily</option>
            <option value="weekly">Weekly</option>
          </select>
        </label>
        <label>
          Max pages: <input type="number" value="100">
        </label>
      </div>
    `;
  }
}
```

### 4. OAuth Authentication Flow

**User Setup Process:**

1. User installs Hover from Webflow marketplace
2. OAuth flow grants access to user's Webflow sites
3. User selects sites to enable cache warming
4. Webhook subscriptions created automatically

**OAuth Implementation:**

```javascript
// OAuth callback handler
app.get("/auth/webflow/callback", async (req, res) => {
  const { code, state } = req.query;

  // Exchange code for access token
  const tokenResponse = await fetch(
    "https://api.webflow.com/oauth/access_token",
    {
      method: "POST",
      body: JSON.stringify({
        client_id: process.env.WEBFLOW_CLIENT_ID,
        client_secret: process.env.WEBFLOW_CLIENT_SECRET,
        code: code,
        grant_type: "authorization_code",
      }),
    }
  );

  const { access_token } = await tokenResponse.json();

  // Store token and create webhook subscriptions
  await storeWebflowCredentials(userId, access_token);
  await createWebhookSubscriptions(access_token);

  res.redirect("/dashboard?setup=complete");
});
```

## Cache Warming Strategy

### 1. Intelligent URL Discovery

**Multi-Source Approach:**

```javascript
async function discoverSiteUrls(domain, options = {}) {
  const urls = new Set();

  // 1. Sitemap.xml processing
  const sitemapUrls = await processSitemap(`https://${domain}/sitemap.xml`);
  sitemapUrls.forEach((url) => urls.add(url));

  // 2. Webflow API site structure
  const webflowPages = await getWebflowSitePages(siteId);
  webflowPages.forEach((page) => urls.add(`https://${domain}${page.slug}`));

  // 3. Link discovery crawling (if enabled)
  if (options.discoverLinks) {
    const discoveredUrls = await crawlForLinks(domain, Array.from(urls));
    discoveredUrls.forEach((url) => urls.add(url));
  }

  return Array.from(urls);
}
```

### 2. Prioritised Crawling

**Page Priority Algorithm:**

```javascript
function prioritiseUrls(urls) {
  return urls.sort((a, b) => {
    // 1. Homepage first
    if (a.pathname === "/") return -1;
    if (b.pathname === "/") return 1;

    // 2. Main pages (single level) before sub-pages
    const aDepth = a.pathname.split("/").length;
    const bDepth = b.pathname.split("/").length;
    if (aDepth !== bDepth) return aDepth - bDepth;

    // 3. Alphabetical for consistent ordering
    return a.pathname.localeCompare(b.pathname);
  });
}
```

### 3. Performance Monitoring

**Real-time Metrics:**

```javascript
class CacheWarmingJob {
  async processUrl(url) {
    const startTime = Date.now();

    try {
      const response = await fetch(url, {
        headers: { "User-Agent": "Blue-Banded-Bee/1.0 Cache-Warmer" },
      });

      const metrics = {
        url,
        statusCode: response.status,
        responseTime: Date.now() - startTime,
        cacheStatus: response.headers.get("cf-cache-status"),
        contentType: response.headers.get("content-type"),
        success: response.ok,
      };

      // Real-time update to Webflow Designer
      await this.sendProgressUpdate(metrics);

      return metrics;
    } catch (error) {
      return {
        url,
        error: error.message,
        responseTime: Date.now() - startTime,
        success: false,
      };
    }
  }
}
```

## Webflow Marketplace Integration

### 1. App Listing Requirements

**App Description:** "Automatically warm your site's cache after publishing to
ensure fast load times for all visitors. Monitor performance, detect errors, and
schedule regular maintenance crawls."

**Key Features:**

- ✅ Automatic cache warming on publish
- ✅ Real-time progress in Designer
- ✅ Performance monitoring and reporting
- ✅ Error detection and alerts
- ✅ Scheduled maintenance crawls
- ✅ Works with any hosting provider

### 2. Installation Flow

**One-Click Setup:**

1. User clicks "Install" in Webflow marketplace
2. OAuth flow grants necessary permissions
3. Hover automatically detects all user's sites
4. User selects which sites to enable
5. Webhook subscriptions created
6. First cache warming job triggered

### 3. Pricing Integration

**Webflow Marketplace Billing:**

```javascript
// Handle billing through Webflow's system
app.post("/webhooks/webflow/billing", async (req, res) => {
  const { event, user, subscription } = req.body;

  switch (event) {
    case "subscription.created":
      await enablePremiumFeatures(user.id);
      break;
    case "subscription.cancelled":
      await revertToFreeFeatures(user.id);
      break;
  }

  res.status(200).send("OK");
});
```

## Monitoring & Analytics

### 1. Performance Tracking

**Key Metrics:**

- Cache warming completion time
- Error detection rate
- Performance improvement metrics
- User engagement with Designer extension

### 2. Error Reporting

**Automatic Error Detection:**

```javascript
function categoriseErrors(results) {
  const errors = {
    notFound: [], // 404 errors
    serverErrors: [], // 5xx errors
    slowPages: [], // >3s response time
    redirects: [], // 3xx status codes
  };

  results.forEach((result) => {
    if (result.statusCode === 404) {
      errors.notFound.push(result);
    } else if (result.statusCode >= 500) {
      errors.serverErrors.push(result);
    } else if (result.responseTime > 3000) {
      errors.slowPages.push(result);
    } else if (result.statusCode >= 300 && result.statusCode < 400) {
      errors.redirects.push(result);
    }
  });

  return errors;
}
```

## Security & Privacy

### 1. Data Protection

**Minimal Data Collection:**

- Only site URLs and performance metrics
- No personal content or user data stored
- Automatic data retention policies

### 2. Webflow API Security

**Token Management:**

```javascript
// Encrypt and store Webflow access tokens
const encryptedToken = await encrypt(accessToken, process.env.ENCRYPTION_KEY);
await storeCredentials(userId, encryptedToken);

// Use tokens securely
const decryptedToken = await decrypt(storedToken, process.env.ENCRYPTION_KEY);
const webflowClient = new WebflowClient(decryptedToken);
```

## Launch Strategy

### Phase 1: Beta Release

- Limited beta with select Webflow agencies
- Designer extension with basic progress display
- Automatic publish triggering

### Phase 2: Marketplace Launch

- Full Webflow marketplace listing
- Complete Designer extension features
- Comprehensive error reporting

### Phase 3: Advanced Features

- Scheduled crawling options
- Performance analytics dashboard
- Integration with Webflow's new features

This integration strategy positions Hover as an essential tool for Webflow
users, providing immediate value while building toward more advanced features.
