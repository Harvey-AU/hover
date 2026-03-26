# Issue #6: User-Agent Rotation

**Priority:** ⚠️ **LOW** - Current static UA is fine **Cost:** Low complexity,
minimal benefit **Status:** Current implementation is adequate

## Current Behaviour

**Static User-Agent:**

```go
// internal/crawler/config.go
UserAgent: "HoverBot/1.0 (+https://goodnative.co)"
```

**Production configuration:**

```go
// cmd/app/main.go - Line 175
cr := crawler.New(crawlerConfig)  // No ID parameter passed
// Results in: "HoverBot/1.0 (+https://goodnative.co)"
// (Worker-N suffix not used in production)
```

## Problem Statement

Some best practices suggest rotating user-agents to:

- Avoid fingerprinting by static UA
- Mimic different browsers/devices
- Bypass simple bot detection

## Why User-Agent Rotation is Overkill

### 1. We're Not Hiding Our Identity

**Cache warming is transparent:**

- We **want** to identify ourselves
- robots.txt compliance requires honest identification
- Webflow/CDN operators should know who's hitting their sites
- Hiding behind fake browser UAs is deceptive

### 2. Modern Bot Detection Doesn't Rely on UA

**Sophisticated detection looks at:**

- TLS fingerprints (Go's HTTP client vs browser)
- HTTP/2 frame ordering
- JavaScript execution (we don't run JS)
- Mouse movements, timing patterns
- **Not** just the User-Agent string

**Rotating UAs doesn't defeat these.**

### 3. Our Current UA is Already Good

**What we have:**

```
HoverBot/1.0 (+https://goodnative.co)
```

**This is perfect because:**

- ✅ Identifies the crawler clearly
- ✅ Provides contact URL for complaints
- ✅ Includes version number

## When UA Rotation WOULD Matter

**Scenarios that justify rotation:**

1. **Scraping sites that block crawlers** (not our use case)
2. **Evading bot detection** (we want to be identified)
3. **Testing multi-device rendering** (not cache warming)

**None of these apply to cache warming.**

## If You Still Want to Rotate (You Shouldn't)

### Simple Implementation (~20 Lines)

```go
// internal/crawler/config.go
type Config struct {
    // ... existing fields
    UserAgents []string  // List of UAs to rotate
}

var browserUAs = []string{
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
    // ... more UAs
}

// internal/crawler/crawler.go
c.OnRequest(func(r *colly.Request) {
    // Pick UA based on job ID hash (deterministic per job)
    uaIndex := hashJobID(jobID) % len(config.UserAgents)
    r.Headers.Set("User-Agent", config.UserAgents[uaIndex])
})
```

**Problems with this approach:**

- ❌ Dishonest (pretending to be browsers)
- ❌ Violates robots.txt spirit (misleading identification)
- ❌ Doesn't actually bypass modern bot detection
- ❌ Makes debugging harder (which UA made which request?)

## Recommendation

### ✅ **KEEP CURRENT IMPLEMENTATION**

**Reasons:**

1. **Current UA is proper** - Follows best practices for crawler identification
2. **Transparent** - Site operators know who we are and can contact us
3. **robots.txt compliant** - Honest identification required
4. **No benefit** - Rotation doesn't solve any actual problem

### ⚠️ **If You Get Blocked Based on UA** (Unlikely)

If a site blocks `HoverBot` specifically:

1. **First**: Check if you're violating their robots.txt
2. **Then**: Contact site operator (they may allowlist you)
3. **Last resort**: Add site-specific UA override for that domain only

```go
// Per-domain UA override
if domain == "problem-site.com" {
    ua = "HoverBot/1.0 (compatible; Cache Warmer; +https://goodnative.co)"
}
```

**Still identifies as HoverBot, just different format.**

## Cost-Benefit Analysis

| Aspect               | Cost                      | Benefit |
| -------------------- | ------------------------- | ------- |
| Development time     | 2-3 hours                 | None    |
| Code complexity      | Low (20 lines)            | None    |
| Ethical concerns     | High (deceptive practice) | None    |
| Debugging impact     | Medium (harder to trace)  | None    |
| Bot detection bypass | None (doesn't work)       | None    |

**Verdict:** ❌ Not worth implementing

## Related Issues

- **Issue #3 (Domain Rate Limiter)** - Actual solution to rate limiting (not UA
  games)
- **Issue #5 (Proxy Support)** - If IP-level blocking occurs (UA rotation won't
  help)
