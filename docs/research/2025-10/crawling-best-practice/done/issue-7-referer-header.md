# Issue #7: Referer Header

**Priority:** ⚠️ **LOW** - Minor benefit, low priority **Cost:** Low complexity
**Status:** Could implement if time permits

## Current Behaviour

**No Referer header set:**

```go
// internal/crawler/crawler.go - OnRequest callback
// Sets Accept, Accept-Language, Accept-Encoding
// Does NOT set Referer
```

## Problem Statement

Browsers automatically set the `Referer` header when following links:

```
User on page A clicks link → request to page B includes "Referer: page A"
```

**Our crawler:** Doesn't set Referer, making requests look less browser-like.

## Why Referer Matters (Slightly)

### 1. Looks More Browser-Like

**With Referer:**

```
GET /products HTTP/1.1
Host: example.com
Referer: https://example.com/
User-Agent: HoverBot/1.0
```

**Without Referer:**

```
GET /products HTTP/1.1
Host: example.com
User-Agent: HoverBot/1.0  ← Looks like direct navigation
```

### 2. Some Sites Check Referer for Hotlinking Protection

**Rare for cache warming, but possible:**

- Image CDNs block requests without Referer
- Some sites validate Referer to prevent deep linking
- Usually only for assets (images/videos), not HTML

### 3. Analytics Tracking

**Sites use Referer for:**

- Tracking which pages link to which
- Measuring internal navigation flows
- **Not relevant for cache warming** (we don't care about their analytics)

## Why It's Low Priority

### 1. We're Not Pretending to Be Browsers

**Our identity is clear:**

- `User-Agent: HoverBot/1.0`
- We're a crawler, not a browser
- Setting Referer doesn't change that

### 2. Cache Warming Doesn't Need It

**CDN caching is independent of Referer:**

- CDNs cache based on URL, not where you came from
- Referer doesn't affect cache keys
- We're warming caches, not simulating user journeys

### 3. Minimal Bot Detection Benefit

**Modern detection doesn't care about Referer:**

- TLS fingerprints matter more
- JavaScript execution matters more
- Timing patterns matter more
- **Referer is easily faked** (not a strong signal)

## Implementation Complexity

### Simple Approach (~15 Lines)

**NOTE:** Requires API change to `WarmURL()` function signature.

**Use task's SourceURL as Referer:**

```go
// internal/jobs/worker.go - processTask()
func (wp *WorkerPool) processTask(ctx context.Context, task *Task) (*crawler.CrawlResult, error) {
    // ... existing code

    // Pass source URL as referer (requires WarmURL API change)
    result, err := wp.crawler.WarmURL(ctx, urlStr, task.FindLinks, crawler.WithReferer(task.SourceURL))
}

// internal/crawler/crawler.go - API changes needed
type WarmOptions struct {
    Referer string
}

func WithReferer(referer string) func(*WarmOptions) {
    return func(opts *WarmOptions) {
        opts.Referer = referer
    }
}

// WarmURL() signature change: add variadic options parameter
func (c *Crawler) WarmURL(ctx context.Context, url string, findLinks bool, opts ...func(*WarmOptions)) (*CrawlResult, error) {
    // Apply options
    options := &WarmOptions{}
    for _, opt := range opts {
        opt(options)
    }

    // In OnRequest callback:
    c.OnRequest(func(r *colly.Request) {
        if options.Referer != "" {
            r.Headers.Set("Referer", options.Referer)
        } else {
            // Fallback: use site homepage
            r.Headers.Set("Referer", fmt.Sprintf("https://%s/", domain))
        }
    })
}
```

**What gets set:**

- Sitemap tasks: `Referer: https://example.com/sitemap.xml`
- Link discovery tasks: `Referer: https://example.com/page-that-linked-here`
- Homepage tasks: `Referer: https://example.com/`

### Problems with This Approach

**1. SourceURL might be empty:**

- First task has no source
- Fallback to homepage works, but adds logic

**2. SourceURL might be external:**

- If we crawl a sitemap index, SourceURL could be different domain
- Need to validate same-domain

**3. Not semantically accurate:**

- Referer means "I clicked a link on this page"
- We didn't click anything (we're a crawler)
- **Setting fake Referer is slightly deceptive**

## Recommendation

### ⚠️ **IMPLEMENT ONLY IF TIME PERMITS**

**Reasons to implement:**

- ✅ Low complexity (~15 lines)
- ✅ Makes requests slightly more browser-like
- ✅ Might help with rare hotlink-protected assets

**Reasons to skip:**

- ❌ Minimal benefit for cache warming
- ❌ Slightly deceptive (faking navigation flow)
- ❌ CDN caching doesn't depend on it
- ❌ Not solving any actual problem we have

### ✅ **Alternative: Set Referer to Site Homepage**

**If you implement, use simple approach:**

```go
// Always set Referer to site homepage
r.Headers.Set("Referer", fmt.Sprintf("https://%s/", task.DomainName))
```

**Benefits:**

- Honest (we're crawling this site)
- Simple (no SourceURL logic needed)
- Same-domain (no validation needed)
- Looks like "navigating from homepage"

## Cost-Benefit Analysis

| Aspect               | Cost                           | Benefit                            |
| -------------------- | ------------------------------ | ---------------------------------- |
| Development time     | 1 hour                         | Minor (slightly more browser-like) |
| Code complexity      | Low (15 lines)                 | Minor                              |
| Deception concerns   | Low (setting homepage referer) | None                               |
| Cache warming impact | None                           | None                               |
| Bot detection bypass | Minimal                        | Minimal                            |

**Verdict:** ⚠️ Implement if time permits, but low priority

## Implementation Plan (If Implemented)

### Phase 1: Simple Homepage Referer (30 Minutes)

```go
// internal/crawler/crawler.go - in OnRequest callback
c.OnRequest(func(r *colly.Request) {
    // ... existing headers

    // Set Referer to site homepage
    if domain := extractDomain(r.URL.String()); domain != "" {
        r.Headers.Set("Referer", fmt.Sprintf("https://%s/", domain))
    }
})
```

**Test:**

- Verify Referer header in request logs
- Confirm sites still return same responses

### Phase 2: Use SourceURL (Optional, +30 Minutes)

```go
// Pass SourceURL through context
ctx = context.WithValue(ctx, "sourceURL", task.SourceURL)

// In OnRequest:
if sourceURL, ok := ctx.Value("sourceURL").(string); ok && sourceURL != "" {
    r.Headers.Set("Referer", sourceURL)
}
```

**Only if Phase 1 shows benefit.**

## Related Issues

- **Issue #6 (User-Agent Rotation)** - Similar "look more like browser" idea
  (also low priority)
- **Issue #3 (Domain Rate Limiter)** - Actual solution to blocking (not header
  tricks)
