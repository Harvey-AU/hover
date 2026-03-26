# Issue #5: Proxy Support

**Priority:** ⚠️ **LOW** - No IP ban problem exists **Cost:** ⚠️ **HIGH** -
$10-50/month ongoing + development time **Status:** Not needed for current use
case

## Problem Statement

Proxy support distributes requests across multiple IP addresses to avoid:

- IP-based rate limiting
- IP bans from aggressive sites
- Geographical restrictions

## Current Situation Analysis

### Do We Have an IP Ban Problem?

**No.** Our current issues are:

- ✅ **Rate limiting (HTTP 429)** - Fixed by Issue #3 (Domain Rate Limiter)
- ✅ **Timeout issues** - Fixed by Issue #1 (Cache Warming Timeout)
- ❌ **IP bans** - Not occurring

**Evidence:**

- everlane.com returns HTTP 429 (rate limit), not 403 (IP ban)
- aesop.com tasks timeout (not blocked)
- realestate.com.au tasks timeout (not blocked)

**Root cause:** Too many concurrent requests to same domain, not IP address
blocking.

### Why Cache Warming Doesn't Trigger IP Bans

**Cache warming is CDN-friendly:**

- We're helping CDNs by pre-warming caches
- CDNs **want** traffic (that's their business model)
- We respect robots.txt and rate limits
- We identify ourselves (HoverBot user-agent)
- Public content only (no scraping competitor data)

**Contrast with scraping:**

- Scraping extracts data competitively
- Often violates ToS
- Hides identity
- Triggers anti-bot defences

## Proxy Cost Analysis

### Option 1: Paid Proxy Services

**Residential Proxies:**

- Cost: $5-15/GB
- Realistic usage: 3-10 GB/month
- **Monthly cost: $15-150**

**Datacenter Proxies:**

- Cost: $1-3/GB
- Realistic usage: 5-20 GB/month
- **Monthly cost: $5-60**

**ScrapingBee (mentioned in requirements):**

- Cost: $124/month for 3M requests
- **Overkill:** Includes JS rendering, CAPTCHA solving, etc. (not needed for
  cache warming)

**Cheapest viable:** ~$10-15/month for basic datacenter proxies

### Option 2: Self-Hosted Proxies

**"Self-hosted Fly micro-VM proxies"** - This is architectural overkill.

**Why it's overcomplicated:**

1. Need to manage proxy infrastructure
2. Fly.io costs per region (~$5-10/month per proxy instance)
3. Still need to route traffic through proxies (same code as paid service)
4. Maintenance burden (health checks, restarts, monitoring)
5. **Total cost: $50-100/month** (10 regions × $5-10 each)

**Simpler alternative:** If you need multi-region, just deploy the main app in
multiple Fly regions and DNS round-robin.

## Implementation Complexity

### Lightweight Colly Integration (~30 Lines)

**If you absolutely need proxies:**

```go
// internal/crawler/config.go
type Config struct {
    // ... existing fields
    ProxyList []string  // HTTP proxy URLs
}

// internal/crawler/crawler.go
func New(config *Config, id ...string) *Crawler {
    // ... existing setup

    if len(config.ProxyList) > 0 {
        proxySwitcher, _ := proxy.RoundRobinProxySwitcher(config.ProxyList...)
        c.SetProxyFunc(proxySwitcher)
    }

    // ... rest of setup
}

// cmd/app/main.go - Load from env
crawlerConfig := crawler.DefaultConfig()
if proxyEnv := os.Getenv("HTTP_PROXY_LIST"); proxyEnv != "" {
    crawlerConfig.ProxyList = strings.Split(proxyEnv, ",")
}
```

**That's it.** Colly handles proxy rotation natively.

### Health Monitoring (Optional +50 Lines)

```go
type ProxyHealth struct {
    url         string
    failCount   int
    lastSuccess time.Time
    mu          sync.RWMutex
}

// Mark proxy unhealthy after 3 consecutive failures
// Retry after 5 minutes
```

## When You WOULD Need Proxies

**Scenarios that justify proxies:**

1. **Scraping competitor data** (not our use case)
2. **Bypassing geo-restrictions** (not applicable to cache warming)
3. **High-volume crawling of anti-bot sites** (already solved by rate limiting)
4. **IP bans observed in logs** (not happening)

**After fixing Issue #3:**

- Domain rate limiter enforces 1 req/sec per domain
- This is **extremely polite** for cache warming
- Unlikely to trigger any IP-based blocking

## Recommendation

### ❌ **DO NOT IMPLEMENT** (Current State)

**Reasons:**

1. **No IP ban problem exists** - Current issues are rate limiting (Issue #3
   fixes this)
2. **Ongoing cost** - $10-150/month for proxies you don't need
3. **Complexity** - Proxy health monitoring, rotation logic, auth
4. **Cache warming is CDN-friendly** - You're helping CDNs, not attacking them

### ⚠️ **Implement Only If** (Future State)

You observe IP bans after:

1. ✅ Issue #3 (Domain Rate Limiter) is deployed
2. ✅ Issue #1 (Timeout fixes) is deployed
3. ❌ Still seeing HTTP 403 or IP blocks in logs

**Then:**

- Start with cheapest datacenter proxies ($10/month)
- Use Colly's built-in `SetProxyFunc()` (30 lines of code)
- Monitor for 1 month to confirm it solves the problem
- Only then consider scaling up proxy pool

## Implementation Plan (If Needed Later)

### Phase 1: Minimal Viable Proxy (1 Hour)

1. Add `ProxyList []string` to crawler config
2. Load from `HTTP_PROXY_LIST` env var (comma-separated URLs)
3. Use `proxy.RoundRobinProxySwitcher()` in crawler setup
4. Deploy and monitor

**Cost:** $10/month (3-5 datacenter proxies) **Code:** ~30 lines

### Phase 2: Health Monitoring (Optional, 2 Hours)

1. Track proxy success/failure rates
2. Mark unhealthy proxies (3 consecutive failures)
3. Retry unhealthy proxies after 5 minutes
4. Log proxy performance metrics

**Code:** +50 lines

### Phase 3: Scaling (If Justified by Data)

1. Add more proxies based on observed ban rates
2. Consider residential proxies if datacenter IPs get blocked
3. Implement geo-targeting if needed

**Cost:** Scale from $10/month to $50-100/month

## Cost-Benefit Analysis

| Aspect             | Cost                       | Benefit           |
| ------------------ | -------------------------- | ----------------- |
| Development time   | 1-3 hours                  | None currently    |
| Monthly proxy cost | $10-150/month              | None currently    |
| Code complexity    | Low (30 lines)             | None currently    |
| Maintenance        | Medium (health monitoring) | None currently    |
| Problem solved     | IP bans                    | **Not occurring** |

**Verdict:** ❌ Not worth implementing until IP bans are observed

## Monitoring Strategy (Before Implementing)

**Track these metrics for 1 month after Issue #3 deploys:**

1. **HTTP 403 errors** - Indicates IP bans (should be near-zero)
2. **HTTP 429 errors** - Should drop to near-zero with rate limiter
3. **CAPTCHA responses** - Would indicate aggressive bot detection
4. **Request success rate** - Should be >95% for public URLs

**Only implement proxies if:**

- HTTP 403 rate > 1% of requests
- OR specific domains consistently block our IP
- OR evidence of IP-based throttling beyond rate limits

## Related Issues

- **Issue #3 (Domain Rate Limiter)** - Solves rate limiting properly (no proxies
  needed)
- **Issue #2 (Exponential Backoff)** - Handles temporary blocks gracefully
