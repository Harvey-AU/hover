# Issue #8: Randomised Delays

**Priority:** ✅ **ALREADY IMPLEMENTED** - No action needed **Cost:** None
(already exists) **Status:** Current implementation is adequate

## Current Behaviour

**Colly LimitRule already has RandomDelay:**

```go
// internal/crawler/crawler.go:111-115
c.Limit(&colly.LimitRule{
    DomainGlob:  "*",
    Parallelism: config.MaxConcurrency,  // 10
    RandomDelay: time.Second / time.Duration(config.RateLimit),  // 333ms
})

// internal/crawler/config.go
RateLimit: 3  // 3 requests per second
// → RandomDelay = 1000ms / 3 = 333ms
```

**What this means:**

- Colly adds a random delay up to 333ms between requests
- Delays range from 0ms to 333ms (uniform distribution)
- **Already "human-like" - not perfectly consistent timing**

## Problem Statement

Some best practices suggest:

- Random delays prevent bot detection via timing analysis
- Vary delays to mimic human reading/clicking patterns
- Add depth-based pauses (longer on deeper pages)

## Why Current Implementation is Fine

### 1. We Already Have RandomDelay

**Current behaviour:**

```
Request 1: delay 127ms
Request 2: delay 284ms
Request 3: delay 56ms
Request 4: delay 312ms
```

**This is random.** Timing patterns aren't monotonic.

### 2. Cache Warming Doesn't Need Human Simulation

**We're not scraping:**

- Don't need to "read" pages
- Don't need to "click" links
- Don't need to mimic human behaviour

**We're warming caches:**

- CDNs don't care about timing patterns
- They care about cache hit rates
- Our timing is already respectful (333ms max + domain rate limiter)

### 3. Larger Random Delays Hurt Performance

**Proposed change (from other agent):**

```go
Delay = 200ms        // Minimum delay
RandomDelay = 400ms  // Random 0-400ms on top
// → Total: 200-600ms per request
```

**Impact:**

- Current: 0-333ms (avg ~167ms)
- Proposed: 200-600ms (avg ~400ms)
- **Performance drop: 2.4x slower** (167ms → 400ms avg)

**For no benefit** (we're not avoiding bot detection, we're cache warming).

## What We Actually Need

**Issue #3 (Domain Rate Limiter) provides:**

- Minimum time between requests to same domain
- Based on robots.txt Crawl-delay
- Already includes randomness from workers claiming tasks at different times

**Together:**

- Colly's RandomDelay: 0-333ms jitter per request
- Domain Rate Limiter: 1+ second minimum between same-domain requests
- Worker scheduling: Natural randomness from async task claiming

**This is plenty of variation.**

## When Increased Delays WOULD Matter

**Scenarios that justify longer/random delays:**

1. **Scraping sites with bot detection** (not our use case)
2. **Simulating user behaviour for testing** (not cache warming)
3. **Avoiding pattern-based blocking** (we identify ourselves as HoverBot)

**None apply to cache warming.**

## If You Want to Tune Delays (Optional)

### Make Delays Configurable (~10 Lines)

```go
// internal/crawler/config.go
type Config struct {
    // ... existing fields
    MinDelay    time.Duration  // Minimum delay (default: 0)
    RandomDelay time.Duration  // Max random delay (default: 333ms)
}

// internal/crawler/crawler.go
c.Limit(&colly.LimitRule{
    DomainGlob:  "*",
    Parallelism: config.MaxConcurrency,
    Delay:       config.MinDelay,      // NEW: minimum delay
    RandomDelay: config.RandomDelay,   // Already exists
})
```

**Then operators can tune via env vars if needed.**

**But there's no evidence this is needed.**

## Recommendation

### ✅ **KEEP CURRENT IMPLEMENTATION**

**Reasons:**

1. **Already have RandomDelay** - 0-333ms jitter exists
2. **Already respectful** - Combined with domain rate limiter (Issue #3)
3. **Good performance** - Avg 167ms delay vs proposed 400ms
4. **Cache warming doesn't need it** - We're not avoiding bot detection
5. **No problem to solve** - Not seeing blocks due to timing patterns

### ⚠️ **Optional: Make Configurable**

**Only if you want operators to tune delays:**

- Add `MIN_DELAY` and `RANDOM_DELAY` env vars
- Default to current behaviour (0ms min, 333ms random)
- Allows experimentation without code changes

**But this is very low priority.**

## Cost-Benefit Analysis

| Aspect                    | Cost                    | Benefit                |
| ------------------------- | ----------------------- | ---------------------- |
| Current state             | Already implemented     | Adequate randomness    |
| Larger delays (200-600ms) | 2.4x slower performance | None for cache warming |
| Configurable delays       | 1 hour dev time         | Flexibility (unneeded) |
| Depth-based delays        | High complexity         | None for cache warming |

**Verdict:** ✅ Keep current implementation (no changes needed)

## Performance Impact Analysis

### Current (0-333ms RandomDelay)

- Avg delay: ~167ms
- Throughput: ~6 req/sec (1000ms / 167ms)
- With Parallelism: 10 → ~60 req/sec

### Proposed (200-600ms Delay + RandomDelay)

- Avg delay: ~400ms
- Throughput: ~2.5 req/sec (1000ms / 400ms)
- With Parallelism: 10 → ~25 req/sec

**Performance loss: 58% slower** (60 req/sec → 25 req/sec)

**For what benefit?** Making timing patterns "more human"... for a crawler that
identifies itself as a crawler.

## Related Issues

- **Issue #3 (Domain Rate Limiter)** - Provides per-domain timing control (more
  important)
- **Issue #6 (User-Agent)** - Similar "pretend to be human" idea (also
  unnecessary)
- **Issue #7 (Referer)** - Similar "look more browser-like" idea (low priority)
