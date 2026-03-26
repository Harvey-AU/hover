# Load Test Issues - 18 October 2025

## Executive Summary

Load testing on 18 October revealed 4 critical issues affecting cache warming
functionality:

1. **Crawler Timeout Bypass**: Tasks running for 11+ hours despite 2-minute
   timeout
   - **1a. Cache Warming Loop Timeout**: 65-second delay for uncacheable pages
     (`BYPASS`/`DYNAMIC`) directly causes timeout issues
2. **Rate Limiting**: No per-domain concurrency control causing HTTP 429 errors
3. **Sitemap Processing Timeout**: Large sitemaps (100k+ pages) exceeding
   30-minute timeout

**Impact**: 4 of 6 test jobs failed or stuck, demonstrating core functionality
failures.

**Root Cause Discovery**: Issue #1 is **directly caused** by Issue #1a.
Homepages with `BYPASS` or `DYNAMIC` cache status (aesop.com, realestate.com.au)
spend 65+ seconds in the cache warming loop, approaching the 2-minute task
timeout. This explains why fallback homepage tasks are stuck while sitemap-based
tasks work fine.

---

## Test Results Summary

### Jobs Created

Load test created 6 jobs at 09:45 AEDT on 18 Oct 2025:

| Domain                | Status     | Progress          | Issue                     |
| --------------------- | ---------- | ----------------- | ------------------------- |
| **awwwards.com**      | ✅ Running | 929/15,059 (6%)   | None - working correctly  |
| **creativebloq.com**  | ✅ Running | 187/30,578 (0.6%) | None - working correctly  |
| **everlane.com**      | ❌ Stuck   | 26/7,855 (0.3%)   | Rate limited (HTTP 429)   |
| **aesop.com**         | ❌ Hung    | 0/1 (0%)          | Crawler hung 11+ hours    |
| **realestate.com.au** | ❌ Hung    | 0/1 (0%)          | Crawler hung 11+ hours    |
| **lifehacker.com**    | ❌ Pending | 0/0 (no tasks)    | Sitemap timeout (27+ min) |

---

## Issue #1: Crawler Timeout Bypass (CRITICAL)

### Symptom

- Tasks stuck in `running` state for **6+ minutes** without completing
- No timeout firing despite 2-minute `taskProcessingTimeout`
- Affects: **aesop.com**, **realestate.com.au** (both fallback homepage tasks)

### Evidence

**Database Query Results**:

```sql
-- Task for aesop.com (Job: 5a9d6992-d24b-4361-a7cd-edd757ff0633)
task_id: aa71ded7-bcd6-45a3-8c1e-1424397bef9e
status: running
source_type: fallback
path: /
created_at: 2025-10-18 09:45:30
started_at: 2025-10-18 10:06:32  -- Started 6 minutes ago
completed_at: null
retry_count: 1
```

**Timeout Configuration**:

```go
// internal/jobs/worker.go:28
const taskProcessingTimeout = 2 * time.Minute

// internal/jobs/worker.go:536
taskCtx, cancel := context.WithTimeout(ctx, taskProcessingTimeout)
defer cancel()
result, err := wp.processTask(taskCtx, jobsTask)
```

**Crawler Timeout**:

```go
// internal/crawler/config.go:28
DefaultTimeout: 30 * time.Second

// internal/crawler/crawler.go:140
httpClient := &http.Client{
    Timeout: config.DefaultTimeout,  // 30 seconds
    Transport: tracingTransport,
}
```

### Root Cause Analysis

**Theory 1: Context Not Propagated** The context with timeout is created at
[worker.go:536](internal/jobs/worker.go#L536) but may not be properly propagated
through the call chain:

```
processTaskFromQueue (creates 2-min timeout)
  → processTask
    → crawler.WarmURL
      → executeCollyRequest (respects ctx.Done())
```

**Theory 2: Colly Not Respecting Context** Colly's `Visit()` and `Wait()` calls
at [crawler.go:491-497](internal/crawler/crawler.go#L491-L497) may not respect
the context cancellation:

```go
go func() {
    visitErr := collyClone.Visit(targetURL)  // Blocking call
    if visitErr != nil {
        done <- visitErr
        return
    }
    collyClone.Wait()  // May wait indefinitely
    done <- nil
}()
```

**Theory 3: HTTP Client Timeout Not Working** The HTTP client has a 30-second
timeout, but this may not apply to:

- DNS resolution failures that hang
- TCP connection attempts to unresponsive servers
- TLS handshakes that stall
- Redirect loops (though limited to 10)

### URLs Affected

- `https://aesop.com/` (redirects to `https://www.aesop.com/`)
- `https://realestate.com.au/`

Both are homepage fallback URLs (sitemap discovery returned 0 results).

### Potential Solutions

#### Option A: Add Defensive Timeout Layer

Wrap the entire `WarmURL` call with an additional timeout:

```go
// In worker.go processTask()
crawlCtx, crawlCancel := context.WithTimeout(ctx, 90*time.Second)
defer crawlCancel()

result, err := wp.crawler.WarmURL(crawlCtx, urlStr, task.FindLinks)
if errors.Is(err, context.DeadlineExceeded) {
    return nil, fmt.Errorf("crawler exceeded 90s timeout")
}
```

#### Option B: Fix Colly Context Handling

Modify `executeCollyRequest` to forcibly cancel Colly:

```go
func executeCollyRequest(ctx context.Context, collyClone *colly.Collector, ...) error {
    done := make(chan error, 1)

    go func() {
        visitErr := collyClone.Visit(targetURL)
        if visitErr != nil {
            done <- visitErr
            return
        }
        collyClone.Wait()
        done <- nil
    }()

    select {
    case err := <-done:
        return err
    case <-ctx.Done():
        // FORCE STOP COLLY
        collyClone.Collector.Abort()  // If available
        return ctx.Err()
    }
}
```

#### Option C: Use HTTP Client Context Directly

Instead of relying on Colly's timeout, use Go's native HTTP client with context:

```go
req, _ := http.NewRequestWithContext(ctx, "GET", targetURL, nil)
resp, err := client.Do(req)
```

This guarantees context cancellation propagates to the HTTP layer.

### Testing Required

1. Test timeout with slow/hanging servers
2. Verify context cancellation propagates through Colly
3. Test with redirect chains
4. Verify DNS timeout handling
5. Test pages with persistent `DYNAMIC` or `BYPASS` cache status

---

## Issue #1a: Cache Warming Loop Timeout (RELATED TO #1)

### Symptom

- Tasks for pages with persistent `DYNAMIC` or `BYPASS` cache status may consume
  65+ seconds in cache warming loop
- Combined with initial request time, this approaches or exceeds the 2-minute
  task timeout
- Affects pages that never cache (e.g., personalised homepages, dynamic content)

### Evidence

**Cache Warming Loop**
([crawler.go:319-368](../internal/crawler/crawler.go#L319-L368)):

```go
maxChecks := 10
checkDelay := 2000 // Initial 2 seconds delay

for i := 0; i < maxChecks; i++ {
    cacheStatus, err := c.CheckCacheStatus(ctx, targetURL)

    // Only breaks if cache status is HIT, STALE, or REVALIDATED
    if cacheStatus == "HIT" || cacheStatus == "STALE" || cacheStatus == "REVALIDATED" {
        cacheHit = true
        break
    }

    // Wait with increasing delay: 2s, 3s, 4s, 5s, 6s, 7s, 8s, 9s, 10s, 11s
    time.After(time.Duration(checkDelay) * time.Millisecond)
    checkDelay += 1000
}
```

**Total worst-case time**: 2+3+4+5+6+7+8+9+10+11 = **65 seconds** just in
delays, plus 10× HEAD request times.

### Root Cause

**Cache Status Handling**: Pages returning `BYPASS` or `DYNAMIC` status from
Cloudflare/Akamai will:

1. Trigger cache warming because `shouldMakeSecondRequest("BYPASS")` returns
   `true` ([crawler.go:595-603](../internal/crawler/crawler.go#L595-L603))
2. Enter the 10-iteration loop
3. Each HEAD request returns `BYPASS` or `DYNAMIC` again (page is configured to
   never cache)
4. Never hit the break condition (line 350-352)
5. Run all 10 iterations consuming 65+ seconds
6. Return normally (no error), but task has consumed most of 2-minute timeout

**Cloudflare Cache Statuses** that indicate uncacheable content:

- `DYNAMIC` - Page has dynamic content, bypasses cache
- `BYPASS` - Cache was bypassed due to origin cache-control headers
- Neither of these will ever become `HIT` no matter how many times you check

**Timeline for stuck tasks**:

- aesop.com homepage: likely returns `BYPASS` or `DYNAMIC` (e-commerce homepage
  with personalised content)
- realestate.com.au homepage: likely returns `BYPASS` (real estate site with
  location-based content)
- Initial request: ~2-5 seconds
- Cache warming loop: 65+ seconds
- Context timeout (2 minutes): Task should be cancelled, but if Colly doesn't
  respect context, it hangs

### Why This Relates to Issue #1

The 65-second cache warming loop **consumes most of the 2-minute timeout**,
leaving little buffer for:

- Slow initial requests
- Network delays
- Colly not respecting context cancellation

If the initial request takes 30+ seconds (slow server, redirects, TLS
handshake), plus 65-second cache loop = 95+ seconds, the task is dangerously
close to the 2-minute limit.

### Potential Solutions

#### Option A: Detect Uncacheable Status Early

Don't attempt cache warming for `DYNAMIC` or `BYPASS` statuses:

```go
func shouldMakeSecondRequest(cacheStatus string) bool {
    switch strings.ToUpper(cacheStatus) {
    case "MISS", "EXPIRED":
        return true
    case "BYPASS", "DYNAMIC":
        return false  // Don't attempt to warm uncacheable content
    default:
        return false
    }
}
```

#### Option B: Add Break Condition for Persistent Non-HIT Status

If cache status is same for 3+ consecutive checks, assume page is uncacheable:

```go
consecutiveSameStatus := 0
lastStatus := ""

for i := 0; i < maxChecks; i++ {
    cacheStatus, err := c.CheckCacheStatus(ctx, targetURL)

    if cacheStatus == lastStatus {
        consecutiveSameStatus++
        if consecutiveSameStatus >= 3 {
            log.Info().
                Str("url", targetURL).
                Str("cache_status", cacheStatus).
                Msg("Cache status unchanged after 3 checks, assuming uncacheable")
            break
        }
    } else {
        consecutiveSameStatus = 0
        lastStatus = cacheStatus
    }

    // Existing break logic...
}
```

#### Option C: Reduce Max Checks and Delays

Current worst-case 65 seconds is too long. Reduce to 5 checks with shorter
delays:

```go
maxChecks := 5
checkDelay := 1000 // 1 second initial

// Worst case: 1+2+3+4+5 = 15 seconds
```

#### Option D: Respect Context Deadline in Loop

Calculate remaining time and exit early if approaching timeout:

```go
deadline, ok := ctx.Deadline()
if ok {
    remaining := time.Until(deadline)
    if remaining < 30*time.Second {
        log.Warn().
            Str("url", targetURL).
            Dur("remaining_time", remaining).
            Msg("Insufficient time remaining for cache warming, exiting early")
        return nil
    }
}
```

### Recommended Approach

**Combination of A + C + D**:

1. Don't attempt cache warming for `BYPASS` or `DYNAMIC` statuses
2. Reduce max checks to 5 with shorter delays (15s worst-case)
3. Check context deadline before entering loop

### URLs Likely Affected

- `https://aesop.com/` - E-commerce homepage with personalised content →
  `DYNAMIC`
- `https://realestate.com.au/` - Location-based content → `BYPASS`
- Any homepage with:
  - Personalisation (logged-in state, cookies)
  - Location-based content (geo-IP)
  - A/B testing
  - Dynamic pricing
  - Session-based content

---

## Issue #2: Rate Limiting (HIGH PRIORITY)

### Symptom

- **everlane.com** returning HTTP 429 "Too Many Requests"
- Job stuck at 26 completed tasks despite 7,855 total
- 22 tasks continuously running and failing

### Evidence

**Production Logs** (10:04 AEDT):

```
{"level":"error","error":"Too Many Requests","task_id":"8e61fa48-...","message":"Crawler failed"}
{"level":"warn","error":"crawler error: Too Many Requests","retry_count":2,"message":"Task blocked (403/429), limited retry scheduled"}
{"level":"warn","status":429,"url":"https://everlane.com/products/...","message":"URL warming returned non-success status"}
```

**Task Status Breakdown**:

```sql
-- everlane.com (Job: e7d513ac-6a13-4f93-aab4-a23fcb6ca2f8)
pending: 4,940
running: 34
completed: 26
skipped: 2,855  -- max_pages limit (5000)
```

### Root Cause Analysis

**Global Rate Limiting Only** Current implementation has **global** rate
limiting across all domains:

```go
// internal/crawler/crawler.go:111-117
if err := c.Limit(&colly.LimitRule{
    DomainGlob:  "*",               // All domains share this limit
    Parallelism: config.MaxConcurrency,  // 10 concurrent
    RandomDelay: time.Second / time.Duration(config.RateLimit),  // 333ms delay
}); err != nil {
```

**No Per-Domain Concurrency** When 22 workers all try to warm everlane.com URLs
simultaneously:

- 22 concurrent requests to everlane.com
- Exceeds their rate limits
- All get HTTP 429
- Retry logic attempts same URLs again
- Infinite retry loop

**Crawl Delay Ignored for Concurrency** While individual crawl delays ARE
respected ([worker.go:1203-1212](internal/jobs/worker.go#L1203-L1212)):

```go
func applyCrawlDelay(task *Task) {
    if task.CrawlDelay > 0 {
        time.Sleep(time.Duration(task.CrawlDelay) * time.Second)
    }
}
```

This only delays _individual_ tasks, not the _number_ of concurrent tasks per
domain.

### Impact

- Sites with aggressive rate limiting (everlane, shopify sites) will fail
- Wasted worker time retrying rate-limited requests
- Poor cache warming completion rates

### Potential Solutions

#### Option A: Per-Domain Concurrency Limits

Implement domain-specific rate limiting:

```go
// Add to Colly LimitRule
c.Limit(&colly.LimitRule{
    DomainGlob:  "everlane.com",
    Parallelism: 2,  // Max 2 concurrent requests
    RandomDelay: 2 * time.Second,
})

c.Limit(&colly.LimitRule{
    DomainGlob:  "*",
    Parallelism: 10,
    RandomDelay: 333 * time.Millisecond,
})
```

#### Option B: Worker Pool Per-Domain Semaphore

Add semaphore in worker pool:

```go
type WorkerPool struct {
    // ...
    domainSemaphores map[string]*semaphore.Weighted
    semaphoreMutex   sync.RWMutex
}

func (wp *WorkerPool) processTask(ctx context.Context, task *Task) {
    // Acquire domain-specific semaphore
    sem := wp.getDomainSemaphore(task.DomainName, 3)  // Max 3 concurrent
    if err := sem.Acquire(ctx, 1); err != nil {
        return nil, err
    }
    defer sem.Release(1)

    // Process task...
}
```

#### Option C: Respect robots.txt Crawl-Delay for Concurrency

Use `crawl_delay_seconds` from robots.txt to also limit concurrency:

```go
func determineDomainConcurrency(crawlDelay int) int {
    if crawlDelay >= 5 {
        return 1  // Very slow crawl
    } else if crawlDelay >= 2 {
        return 2
    } else {
        return 5  // Default
    }
}
```

#### Option D: Exponential Backoff on 429

When receiving 429, back off exponentially:

```go
if result.StatusCode == 429 {
    backoff := time.Duration(math.Pow(2, float64(task.RetryCount))) * time.Second
    time.Sleep(backoff)
    // Retry with increased delay
}
```

### Recommended Approach

**Combination of A + D**:

1. Implement per-domain concurrency limits (start conservative: 2-3 per domain)
2. Add exponential backoff for HTTP 429 responses
3. Make limits configurable via robots.txt `Crawl-delay`

---

## Issue #3: Sitemap Processing Timeout (MEDIUM PRIORITY)

### Symptom

- **lifehacker.com** stuck in `pending` status for 27+ minutes
- 0 tasks created
- Sitemap processing goroutine hasn't completed or failed

### Evidence

**Database State**:

```sql
-- lifehacker.com (Job: ce1cae2f-6796-4477-b093-fdc66bd524c8)
status: pending
created_at: 2025-10-18 09:45:24
started_at: null  -- Never started!
total_tasks: 0
sitemap_tasks: 0
```

**Domain has 104,654 existing pages** from previous (cancelled) job.

**Sitemap Structure**:

```xml
<!-- https://lifehacker.com/sitemap-index.xml -->
<sitemapindex>
    <sitemap><loc>https://lifehacker.com/sitemap-google-news-0.xml</loc></sitemap>
    <sitemap><loc>https://lifehacker.com/sitemap-articles-news-0.xml</loc></sitemap>
    <sitemap><loc>https://lifehacker.com/sitemap-articles-hacks-0.xml</loc></sitemap>
    <!-- ... 12 total sub-sitemaps -->
</sitemapindex>
```

### Root Cause Analysis

**Sitemap Processing Timeout**:

```go
// internal/jobs/manager.go:298
backgroundCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
go func() {
    defer cancel()
    jm.processSitemap(backgroundCtx, job.ID, normalisedDomain, options.IncludePaths, options.ExcludePaths)
}()
```

**Timeline**:

- Job created: 09:45:24
- Current time: 10:12+ (27 minutes elapsed)
- Timeout: 30 minutes
- **Still 3 minutes until timeout**

**Sitemap Processing Steps**:

1. Discover sitemap URLs (from robots.txt or common paths) - Fast
2. Parse sitemap index (12 sub-sitemaps) - Fast
3. **For each sub-sitemap:**
   - Fetch sitemap XML
   - Parse URLs
   - Filter against robots.txt
   - Filter against include/exclude paths
4. Enqueue URLs to database - **Slow with 100k+ URLs**

### Why It's Taking So Long

Lifehacker sitemap likely contains:

- 100k+ article URLs
- Each sub-sitemap: 10-20k URLs
- Database insertion: Creating 100k page records + 100k task records
- **But max_pages is 5,000** - so it should stop early!

**Bug**: Sitemap processing doesn't respect `max_pages` limit until AFTER
parsing all sitemaps.

```go
// internal/jobs/manager.go:1067-1077
if len(urls) > 0 {
    if err := jm.enqueueSitemapURLs(ctx, jobID, domain, urls); err != nil {
        return
    }
} else {
    if err := jm.enqueueFallbackURL(ctx, jobID, domain); err != nil {
        return
    }
}
```

The URLs are only limited when enqueueing, not during parsing. So even though
only 5,000 tasks will be created, the system still:

1. Fetches all 12 sub-sitemaps
2. Parses 100k+ URLs
3. Filters 100k+ URLs
4. Then limits to 5,000 when enqueueing

### Potential Solutions

#### Option A: Early Termination on max_pages

Stop parsing sitemaps once URL count exceeds max_pages:

```go
func (jm *JobManager) discoverAndParseSitemaps(...) ([]string, *crawler.RobotsRules, error) {
    var urls []string
    maxPages := jm.getJobMaxPages(jobID)

    for _, sitemapURL := range sitemaps {
        if len(urls) >= maxPages {
            log.Info().
                Int("max_pages", maxPages).
                Int("urls_collected", len(urls)).
                Msg("Reached max_pages limit, stopping sitemap parsing")
            break
        }

        sitemapURLs, err := sitemapCrawler.ParseSitemap(ctx, sitemapURL)
        // ...
        urls = append(urls, sitemapURLs...)
    }

    return urls[:min(len(urls), maxPages)], robotsRules, nil
}
```

#### Option B: Streaming Sitemap Processing

Instead of collecting all URLs then enqueueing, enqueue in batches:

```go
for _, sitemapURL := range sitemaps {
    sitemapURLs, err := sitemapCrawler.ParseSitemap(ctx, sitemapURL)

    // Enqueue immediately
    jm.enqueueSitemapURLs(ctx, jobID, domain, sitemapURLs)

    // Check if job has enough tasks
    if jm.getJobTaskCount(jobID) >= maxPages {
        break
    }
}
```

#### Option C: Reduce Timeout for Large Sitemaps

30 minutes is too long. Reduce to 5-10 minutes and rely on fallback:

```go
backgroundCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
```

If sitemap takes >10 minutes, something is wrong - fall back to homepage
crawling.

#### Option D: Background Sitemap Processing with Immediate Fallback

Start job immediately with fallback, then add sitemap URLs as they're
discovered:

```go
// Create fallback task immediately
jm.enqueueFallbackURL(ctx, jobID, domain)

// Process sitemap in background, adding URLs as discovered
go jm.processSitemap(backgroundCtx, jobID, domain, ...)
```

This ensures the job starts working immediately while sitemap processing
continues.

### Recommended Approach

**Combination of A + C**:

1. Stop parsing sitemaps once max_pages URLs collected
2. Reduce timeout to 10 minutes
3. Add logging to track sitemap processing progress

---

## Additional Observations

### CDN Distribution Analysis

All 6 test domains use different CDN providers, proving issues are NOT caused by
shared CDN rate limiting:

| Domain            | CDN                  | Status       | Issue           |
| ----------------- | -------------------- | ------------ | --------------- |
| aesop.com         | Cloudflare           | Stuck        | Crawler hung    |
| everlane.com      | Cloudflare (Shopify) | Rate limited | HTTP 429        |
| realestate.com.au | Akamai               | Stuck        | Crawler hung    |
| lifehacker.com    | Cloudflare           | Pending      | Sitemap timeout |
| awwwards.com      | nginx (custom)       | ✅ Working   | None            |
| creativebloq.com  | Fastly (Varnish)     | ✅ Working   | None            |

**Key Insights**:

- **3/6 sites use Cloudflare** - but they each have independent rate limits per
  domain
- **everlane.com** is the only one returning 429s because it's on **Shopify**,
  which has aggressive per-IP, per-store rate limiting
- **Cross-domain rate limiting is NOT happening** - aesop.com and lifehacker.com
  (also Cloudflare) are not rate limited
- **Stuck crawler issue affects both Cloudflare (aesop) and Akamai
  (realestate.com)** - proving it's a crawler bug, not CDN-specific

**Cloudflare Bot Detection Note**: While Cloudflare doesn't apply global rate
limits, it CAN fingerprint bots across domains using:

- User-Agent: `HoverBot/1.0`
- TLS fingerprint
- Request patterns
- Source IP

If Cloudflare flags your bot signature as malicious, individual sites MAY
increase scrutiny, but this is per-site configuration, not automatic
cross-domain blocking.

**Shopify Rate Limiting** (everlane.com):

- Shopify enforces **per-IP, per-store** rate limits via Cloudflare
- 22 concurrent workers from single Fly.io IP triggered 429 responses
- Shopify limits are typically 2-4 requests/second per IP
- This is **NOT** Cloudflare's doing - it's Shopify's application-layer rate
  limiting

**Recommendation**: Per-domain concurrency limits (Issue #2) will solve the
Shopify rate limiting problem. No special Cloudflare handling needed.

### Database Connection Issues

Production logs show intermittent Supabase connection failures:

```
{"error":"failed to connect to `user=postgres.gpzjtbgtdjxnacdfujvx database=postgres`:
    timeout: context deadline exceeded"}
```

This may be exacerbating the stuck task issue - workers can't update task status
if DB connections are timing out.

**Recommendation**: Investigate Supabase connection pool settings and retry
logic.

### Health Check Failures

```
Health check on port 8080 has failed. Your app is not responding properly.
```

This occurred at 05:16 UTC (hours after job creation), suggesting the
application may have become unresponsive due to resource exhaustion from stuck
tasks.

---

## Testing Plan

### Unit Tests Needed

1. **Context Cancellation Test**: Verify WarmURL respects context timeout
   - File: `internal/crawler/crawler_test.go`
   - Test: Mock slow HTTP server, verify timeout fires

2. **Rate Limiting Test**: Verify per-domain concurrency limits
   - File: `internal/jobs/worker_test.go`
   - Test: Multiple tasks for same domain, verify max concurrency

3. **Sitemap Timeout Test**: Verify early termination at max_pages
   - File: `internal/jobs/manager_sitemap_test.go`
   - Test: Large sitemap, verify stops parsing at limit

### Integration Tests Needed

1. **Hung Server Test**: Test with server that accepts connection but never
   responds
2. **Rate Limited Server Test**: Server returning 429, verify backoff
3. **Large Sitemap Test**: 100k+ URL sitemap, verify completes in <10min

### Production Validation

After fixes deployed:

1. Re-run load test with same 6 domains
2. Monitor task durations (should all complete within 2 minutes)
3. Check everlane.com completion rate (should reach 100% with no 429s)
4. Verify lifehacker.com creates tasks within 10 minutes

---

## Priority Ranking

### P0 (Critical - Fix Immediately)

**Issue #1: Crawler Timeout Bypass**

- Blocks core functionality
- Wastes worker resources for hours
- Prevents job completion

**Recommended Fix**: Option A (Defensive Timeout Layer) + Option C (HTTP Client
Context)

**Issue #1a: Cache Warming Loop Timeout**

- Directly causes Issue #1 for pages with `BYPASS`/`DYNAMIC` status
- Consumes 65+ seconds per task, approaching 2-minute timeout
- Affects all e-commerce and personalised homepages

**Recommended Fix**: Option A (Detect Uncacheable Early) + Option C (Reduce
Checks) + Option D (Respect Deadline)

### P1 (High - Fix Next Sprint)

**Issue #2: Rate Limiting**

- Affects popular e-commerce sites (Shopify, etc.)
- Causes job failures
- Damages relationship with sites (excessive requests)

**Recommended Fix**: Option A (Per-Domain Limits) + Option D (Exponential
Backoff)

### P2 (Medium - Fix When Possible)

**Issue #3: Sitemap Processing Timeout**

- Less common (only huge sitemaps)
- Has workaround (fallback to homepage)
- Can be mitigated with timeout reduction

**Recommended Fix**: Option A (Early Termination) + Option C (Reduce Timeout)

---

## Files to Modify

### For Issue #1 (Timeout Bypass)

1. `internal/crawler/crawler.go` - Add defensive timeout in
   `executeCollyRequest`
2. `internal/jobs/worker.go` - Add timeout logging
3. `internal/crawler/crawler_test.go` - Add timeout test

### For Issue #1a (Cache Warming Loop Timeout)

1. `internal/crawler/crawler.go` - Modify `shouldMakeSecondRequest` to exclude
   `BYPASS`/`DYNAMIC`
2. `internal/crawler/crawler.go` - Reduce `maxChecks` from 10 to 5, `checkDelay`
   from 2000ms to 1000ms
3. `internal/crawler/crawler.go` - Add context deadline check before cache
   warming loop
4. `internal/crawler/perform_cache_validation_test.go` - Add test for
   `BYPASS`/`DYNAMIC` handling

### For Issue #2 (Rate Limiting)

1. `internal/crawler/crawler.go` - Add per-domain Colly limits
2. `internal/jobs/worker.go` - Add domain semaphore
3. `internal/crawler/config.go` - Add domain concurrency config

### For Issue #3 (Sitemap Timeout)

1. `internal/jobs/manager.go` - Add early termination in
   `discoverAndParseSitemaps`
2. `internal/jobs/manager.go` - Reduce timeout to 10 minutes

---

## Metrics to Add

### Crawler Metrics

- `crawler_timeout_count` - Count of context deadline exceeded errors
- `crawler_duration_seconds` - Histogram of actual crawl times
- `crawler_retry_count` - Count of retries by HTTP status code

### Rate Limiting Metrics

- `rate_limit_429_count` - Count of 429 responses by domain
- `domain_concurrent_tasks` - Gauge of active tasks per domain

### Sitemap Metrics

- `sitemap_parse_duration_seconds` - Time to parse all sitemaps
- `sitemap_url_count` - Total URLs discovered
- `sitemap_timeout_count` - Count of sitemap processing timeouts

---

## Existing Rate Limiting & Adaptive Mechanisms

### What Already Exists in the Codebase

The user expected per-domain rate limiting, backoff, task spacing, and adaptive
scaling. Here's what actually exists:

#### ✅ 1. Adaptive Worker Scaling

**Location**:
[internal/jobs/worker.go:1674-1756](../internal/jobs/worker.go#L1674-L1756)

**How it works**:

- Tracks last 5 task response times per job in a sliding window
- Evaluates performance after every 3+ tasks
- Dynamically scales workers from **0 to +20** based on average response time:
  - 0-1000ms → 0 boost workers
  - 1000-2000ms → +5 workers
  - 2000-3000ms → +10 workers
  - 3000-4000ms → +15 workers
  - 4000ms+ → +20 workers

**Code**:

```go
type JobPerformance struct {
    RecentTasks  []int64   // Last 5 task response times
    CurrentBoost int       // Current performance boost workers
    LastCheck    time.Time // When we last evaluated
}

func (wp *WorkerPool) evaluateJobPerformance(jobID string, responseTime int64) {
    // Calculate average from last 5 tasks
    avgResponseTime := total / int64(len(perf.RecentTasks))

    // Determine needed boost workers
    var neededBoost int
    switch {
    case avgResponseTime >= 4000: neededBoost = 20
    case avgResponseTime >= 3000: neededBoost = 15
    case avgResponseTime >= 2000: neededBoost = 10
    case avgResponseTime >= 1000: neededBoost = 5
    default: neededBoost = 0
    }

    // Apply scaling up or down
    boostDiff := neededBoost - perf.CurrentBoost
    if boostDiff > 0 {
        wp.scaleUp(boostDiff)
    } else if boostDiff < 0 {
        wp.scaleDown(-boostDiff)
    }
}
```

**Limitation**: This **increases** concurrency for slow sites, which is
counterproductive for rate limiting. Slow responses often mean the site is
struggling or rate limiting you - adding more workers makes it worse.

#### ✅ 2. Blocking Error Backoff (403/429)

**Location**:

- [internal/jobs/worker.go:1336-1360](../internal/jobs/worker.go#L1336-L1360) -
  Error handling
- [internal/jobs/worker.go:1582-1595](../internal/jobs/worker.go#L1582-L1595) -
  Error detection

**How it works**:

- Detects HTTP 403/429 responses and rate limit errors
- Limits retries to **2 attempts** (vs. 3 for normal errors)
- Marks task as `failed` after 2 blocked attempts

**Code**:

```go
func isBlockingError(err error) bool {
    errorStr := strings.ToLower(err.Error())
    return strings.Contains(errorStr, "403") ||
           strings.Contains(errorStr, "forbidden") ||
           strings.Contains(errorStr, "429") ||
           strings.Contains(errorStr, "too many requests") ||
           strings.Contains(errorStr, "rate limit")
}

func (wp *WorkerPool) handleTaskError(ctx context.Context, task *db.Task, taskErr error) error {
    if isBlockingError(taskErr) {
        if task.RetryCount < 2 {  // Only 2 retries for blocking errors
            task.RetryCount++
            task.Status = string(TaskStatusPending)
            log.Warn().Msg("Task blocked (403/429), limited retry scheduled")
        } else {
            task.Status = string(TaskStatusFailed)
            log.Error().Msg("Task permanently failed after 2 blocked retries")
        }
    }
}
```

**Limitation**: No exponential backoff delay - failed tasks immediately go back
to `pending` and can be retried instantly.

#### ✅ 3. Per-Task Crawl Delay

**Location**:
[internal/jobs/worker.go:1203-1212](../internal/jobs/worker.go#L1203-L1212)

**How it works**:

- Reads `crawl_delay_seconds` from `robots.txt`
- Sleeps for that duration **before** starting each task
- Applied to individual tasks, not domain-wide

**Code**:

```go
func applyCrawlDelay(task *Task) {
    if task.CrawlDelay > 0 {
        log.Debug().
            Str("domain", task.DomainName).
            Int("crawl_delay_seconds", task.CrawlDelay).
            Msg("Applying crawl delay from robots.txt")
        time.Sleep(time.Duration(task.CrawlDelay) * time.Second)
    }
}
```

**Limitation**: This delays the **start** of a task, but doesn't limit **how
many** concurrent tasks can run for the same domain.

Example: If `crawl_delay = 2` seconds:

- Task 1 starts at 0s
- Task 2 waits 2s, starts at 2s
- Task 3 waits 2s, starts at 4s
- **All 3 can be running concurrently**

This is correct behaviour for crawl-delay, but doesn't prevent rate limiting.

#### ✅ 4. Global Rate Limiting

**Location**:
[internal/crawler/crawler.go:111-117](../internal/crawler/crawler.go#L111-L117)

**How it works**:

- Colly's `LimitRule` with global domain glob `"*"`
- Max 10 concurrent requests **across all domains**
- 333ms random delay between requests

**Code**:

```go
c.Limit(&colly.LimitRule{
    DomainGlob:  "*",  // Applies to ALL domains
    Parallelism: config.MaxConcurrency,  // 10 concurrent
    RandomDelay: time.Second / time.Duration(config.RateLimit),  // 333ms
})
```

**Limitation**: This is a **global** limit, not per-domain. If 22 workers all
claim tasks from everlane.com, they all try to crawl concurrently, ignoring the
Colly limit.

### ❌ What's Missing

Based on the user's expectations, these mechanisms are **NOT** implemented:

#### 1. Per-Domain Concurrency Control

**Expected**: Maximum N concurrent tasks per domain (e.g., max 2-3 for
everlane.com)

**Reality**: Task claiming in
[internal/db/queue.go:260-333](../internal/db/queue.go#L260-L333) uses only:

```sql
ORDER BY priority_score DESC, created_at ASC
FOR UPDATE SKIP LOCKED
```

No domain-based locking or semaphore. If 22 workers all call
`GetNextTask(jobID)` for everlane.com, they get 22 tasks instantly.

#### 2. Per-Domain Task Spacing

**Expected**: Don't start a new task from domain X if we just finished one
within the last Y seconds

**Reality**: No time-based spacing exists. Tasks are claimed immediately when
workers are idle, regardless of when the last task from that domain completed.

#### 3. Adaptive Slowdown

**Expected**: Scale up workers until rate limited, then slow down

**Reality**: Adaptive scaling (mechanism #1 above) only **increases** workers
for slow sites. There's no code path that:

- Detects sustained 429 errors
- Reduces concurrency per domain
- Gradually increases again when errors stop

### What the User Thought Existed vs. Reality

| Feature                    | Expected                          | Reality                                         |
| -------------------------- | --------------------------------- | ----------------------------------------------- |
| **Per-domain concurrency** | ✅ Max 2-3 per domain             | ❌ No limit - all workers can claim same domain |
| **Task spacing**           | ✅ X seconds between domain tasks | ❌ No spacing - instant claiming                |
| **Adaptive slowdown**      | ✅ Speed up until 429, then slow  | ❌ Only speeds up, never slows                  |
| **Backoff on 429**         | ✅ Exponential backoff            | ⚠️ Partial - limits retries but no delay        |
| **Crawl delay**            | ✅ Per-task delays                | ✅ Works correctly                              |

### Why Issue #2 (Rate Limiting) Still Occurs

Despite the existing mechanisms, everlane.com gets rate limited because:

1. **22 workers** all call `GetNextTask("everlane-job-id")`
2. **No per-domain lock** → all 22 get everlane.com tasks
3. **Global Colly limit (10)** is ignored because each worker has its own Colly
   instance
4. **22 concurrent requests** hit everlane.com from same IP
5. **Shopify rate limit (2-4 req/sec)** triggers HTTP 429
6. **Blocking error handler** marks tasks as failed after 2 retries
7. **No delay** → failed tasks immediately go back to pending
8. **Infinite retry loop** → workers keep claiming same failed tasks

### Recommendations for Next Developer

When fixing Issue #2 (Rate Limiting), the codebase needs:

1. **Domain-based semaphore** in worker pool to limit concurrent tasks per
   domain
2. **Time-based task spacing** in `GetNextTask()` query (e.g.,
   `WHERE last_domain_task_time < now() - interval '2 seconds'`)
3. **Exponential backoff** when `isBlockingError()` returns true
4. **Adaptive scaling fix** - reverse the logic: slow sites should get FEWER
   workers, not more
5. **Per-domain Colly instances** or better context propagation to respect Colly
   limits

---

## Related Issues

- Recent commit: "Avoid bad connection on stuck tasks" (45c8fcf)
- Recent commit: "Maintenance transaction path to recover stuck tasks" (b9d8355)
- Stuck task monitoring already logs "CRITICAL: Task stuck in running state
  for >3 minutes"
- Worker pool cleanup runs every 5 minutes to identify stuck jobs

These existing mechanisms can detect the symptoms but don't prevent the root
causes.

---

## Conclusion

All issues stem from **missing defensive programming around timeouts and
resource limits**:

1. **Timeouts configured but not enforced** (context not respected through
   Colly)
2. **Cache warming loop for uncacheable content** (65-second delay for
   `BYPASS`/`DYNAMIC` pages)
3. **Global rate limits without per-domain controls** (22 workers hitting same
   domain)
4. **Large dataset processing without pagination/early termination** (100k+
   sitemap URLs parsed before limiting)

**Critical Discovery**: Issue #1 (Crawler Timeout) is likely **directly caused**
by Issue #1a (Cache Warming Loop Timeout). Pages with `BYPASS` or `DYNAMIC`
cache status (aesop.com, realestate.com.au homepages) spend 65+ seconds in the
cache warming loop, approaching the 2-minute task timeout. This explains why
fallback homepage tasks are the ones stuck.

Fixing these will significantly improve reliability and completion rates for
cache warming jobs.

**Next Steps**:

1. Reproduce issues in local environment
2. Implement fixes in order of priority (P0 → P1 → P2)
3. Add comprehensive tests
4. Deploy and re-test with original 6 domains
5. Monitor metrics for 48 hours to validate fixes

---

**Document Created**: 18 October 2025 **Load Test Date**: 18 October 2025
09:45-10:15 AEDT **Production Environment**: hover.fly.dev **Test Jobs**: 6
domains, 5,000 max pages each
