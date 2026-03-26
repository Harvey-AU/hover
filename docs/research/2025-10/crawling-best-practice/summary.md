# Crawling Issues Summary

## 1. Load Test Issues (18 Oct 2025)

**Executive Summary:** You tested 6 domains and discovered **4 critical issues**
affecting cache warming:

### Issue #1: Crawler Timeout Bypass (P0 - CRITICAL)

**[→ Technical Brief](issue-1-cache-warming-timeout.md)**

- **Symptom**: Tasks running 11+ hours despite 2-minute timeout
- **Affected Sites**: aesop.com, realestate.com.au (both fallback homepage
  tasks)
- **Root Cause**: Context timeout not properly propagated through Colly's
  `Visit()` and `Wait()` calls
- **Evidence**: Tasks stuck in `running` state for 6+ minutes without timeout
  firing

### Issue #1a: Cache Warming Loop Timeout (P0 - CRITICAL, **directly causes Issue #1**)

- **Symptom**: 54 second delay in cache warming loop for uncacheable pages
  (`BYPASS`/`DYNAMIC`)
- **Root Cause**: Loop runs 10 iterations checking if `BYPASS`/`DYNAMIC` becomes
  `HIT` (which never happens)
- **Math**: 2+3+4+5+6+7+8+9+10 = **54 seconds** just in delays
- **Impact**: Combined with slow initial request, this approaches/exceeds
  2-minute timeout
- **Why it matters**: aesop.com and realestate.com.au homepages return
  `BYPASS`/`DYNAMIC` (personalised content), spend 54s in the loop, then if
  Colly doesn't respect context, task hangs

### Issue #2: Rate Limiting (P1 - HIGH)

**[→ Technical Brief](issue-2-rate-limiting.md)**

- **Symptom**: everlane.com returning HTTP 429 after 26 tasks
- **Root Cause**: No per-domain concurrency control - 22 workers all hitting
  everlane.com simultaneously
- **Evidence**: 34 running tasks, all failing with 429 errors, 2 retries without
  backoff
- **Cause**: Global rate limiting only (`*` domain glob), no per-domain
  semaphore

### Issue #3: Sitemap Processing Timeout (P2 - MEDIUM)

- **Symptom**: lifehacker.com stuck in `pending` for 27+ minutes
- **Root Cause**: Parsing 100k+ URLs before limiting to `max_pages` (5,000)
- **Impact**: Sitemap processing doesn't respect `max_pages` until **after**
  parsing all sitemaps

**Key Insight**: Issue #1 is **directly caused** by Issue #1a. The 54-second
cache warming loop consumes most of the 2-minute timeout, leaving no buffer for
Colly's non-respecting context.

## 2. Crawling Best Practices Research

Your research document outlines **industry best practices** for avoiding blocks:

1. **Throttle Request Rate**: Rate limiting per domain, random delays, backoff
   on 429/503
2. **Rotate IP Addresses**: Proxies, multiple Fly.io regions, per-IP rate limits
3. **Realistic Headers**: Rotate user-agents, populate full header sets, include
   Referer
4. **Human-Like Behaviour**: Randomise timing, depth-based pauses, avoid
   monotonic sequences
5. **Handle JS/CAPTCHAs**: Headless browsers for JS-heavy sites, CAPTCHA solving
   services
6. **Manage Sessions**: Cookie jars, maintain login sessions, handle redirects
7. **Robust Error Handling**: OnError callbacks, switch tactics on high error
   rates, exponential backoff
8. **Respect robots.txt**: Crawl-delay, sitemap usage, polite crawling

## Precise/Key Issues Summary

Based on these documents, here are the **very precise issues**:

## Critical Path Issues (Must Fix First)

1. **Cache warming loop wastes 54 seconds on uncacheable pages** →
   [Technical Brief](issue-1-cache-warming-timeout.md)
   - Fix `shouldMakeSecondRequest()` to return `false` for `BYPASS`/`DYNAMIC`
2. **Context timeout not enforced in Colly** →
   [Technical Brief](issue-1-cache-warming-timeout.md)
   - Add Colly's `RequestTimeout()` to enforce 2-minute limit
3. **No per-domain rate limiting** →
   [Technical Brief](issue-3-domain-rate-limiter.md)
   - Implement domain-level rate limiter to enforce minimum time between
     requests to same domain

## High Priority Issues (Fix Next Sprint)

1. **No exponential backoff on 429** →
   [Technical Brief](issue-2-rate-limiting.md)
   - Implement backoff when `isBlockingError()` returns true
2. **Sitemap parsing ignores max_pages until end** → _Under investigation_
   - Stop parsing sitemaps once URL count exceeds limit

## Best Practice Gaps (Low Priority)

### Issues 4-5: Not Recommended

4. **Cookie Jar Support** → [Technical Brief](issue-4-cookie-jar.md) - ❌ **Not
   recommended**
   - **Priority**: Low - Security risk, no benefit for cache warming
   - **Cost**: Medium complexity, cross-job cookie leakage risk
   - **Memory**: 100 KB (not 400 MB as incorrectly estimated)
   - **Verdict**: Use request headers for auth instead

5. **Proxy Support** → [Technical Brief](issue-5-proxy-support.md) - ❌ **Not
   needed currently**
   - **Priority**: Low - No IP ban problem exists
   - **Cost**: High - $10-150/month ongoing cost
   - **Status**: Monitor for IP bans after Issue #3 deployed, only implement if
     403 errors observed

### Issues 6-8: Already Adequate or Low Value

6. **User-Agent Rotation** → [Technical Brief](issue-6-user-agent-rotation.md) -
   ✅ **Current UA is fine**
   - **Priority**: Low - Static UA follows best practices
   - **Current**: `HoverBot/1.0 (+https://goodnative.co)`
   - **Verdict**: Keep current implementation (transparent, respectful)

7. **Referer Header** → [Technical Brief](done/issue-7-referer-header.md) - ✅
   **DONE - Not implementing**
   - **Priority**: Low - Minor benefit
   - **Cost**: 1 hour (~15 lines)
   - **Decision**: Not implementing - minimal benefit for cache warming

8. **Randomised Delays** →
   [Technical Brief](done/issue-8-randomised-delays.md) - ✅ **DONE - Already
   implemented**
   - **Priority**: None - Already have 0-333ms RandomDelay in Colly
   - **Status**: Current implementation adequate
   - **Decision**: No changes needed - adequate randomness exists

## Priority Summary

### Must Fix (P0-P1)

- ✅ Issue #1 & #1a: Cache warming timeout fixes (DONE - implemented today)
- ✅ Issue #3: Domain rate limiter (already done)
- ✅ Issue #2: Exponential backoff (DONE - implemented today)

### Should Monitor

- ⚠️ Issue #5: Proxy support (only if IP bans observed after fixes)

### Not Recommended

- ❌ Issue #4: Cookie jar (security risk, no benefit)
- ❌ Issue #6: User-agent rotation (current UA is proper)

### Already Done

- ✅ Issue #7: Referer header (reviewed, not implementing - minimal benefit)
- ✅ Issue #8: Random delays (reviewed, already in Colly)
