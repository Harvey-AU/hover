package jobs

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Harvey-AU/hover/internal/crawler"
)

const (
	// fallbackJobConcurrency is used when a job does not report an explicit
	// concurrency (or the limiter has not yet seeded a value). This mirrors
	// the API default.
	fallbackJobConcurrency = 20

	// discoveredLinks* constants control timeout behaviour for link persistence.
	discoveredLinksDBTimeout  = 30 * time.Second
	discoveredLinksMinRemain  = 8 * time.Second
	discoveredLinksMinTimeout = 5 * time.Second
)

// JobInfo caches job-specific data that doesn't change during execution.
type JobInfo struct {
	DomainID                 int
	DomainName               string
	FindLinks                bool
	AllowCrossSubdomainLinks bool
	CrawlDelay               int
	Concurrency              int
	AdaptiveDelay            int
	AdaptiveDelayFloor       int
	RobotsRules              *crawler.RobotsRules // Cached robots.txt rules for URL filtering
}

// IsRateLimitError checks whether an error indicates rate limiting (429, 403,
// 503, or common rate-limit messages). This intentionally matches a broader set
// than isBlockingError in executor.go: isBlockingError drives retry/backoff
// decisions while IsRateLimitError drives domain pacer state updates.
func IsRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "429") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "403") ||
		strings.Contains(lower, "503")
}

// applyCrawlDelay sleeps for the task's robots.txt crawl delay. Crawl delay is
// now primarily enforced by the broker's DomainPacer, but this helper is kept
// for test compatibility.
func applyCrawlDelay(task *Task) {
	if task.CrawlDelay > 0 {
		time.Sleep(time.Duration(task.CrawlDelay) * time.Second)
	}
}

// classifyTaskOutcome maps a task processing result into an (outcome, reason)
// pair for telemetry. Ported from the old WorkerPool.classifyTaskOutcome.
func classifyTaskOutcome(o *TaskOutcome) (string, string) {
	if o.Success {
		return "success", "ok"
	}

	if o.RateLimited {
		if o.Retry != nil && o.Retry.ShouldRetry {
			return "retry_scheduled", "blocking"
		}
		return "failed", "blocking_exhausted"
	}

	if o.Retry != nil && o.Retry.ShouldRetry {
		return "retry_scheduled", "retryable"
	}

	return "failed", "non_retryable"
}

// linkDiscoveryMinPriorityFromEnv reads the minimum priority threshold for
// discovered links from the GNH_LINK_DISCOVERY_MIN_PRIORITY env var.
func linkDiscoveryMinPriorityFromEnv() float64 {
	const fallback = 0.5
	if raw := strings.TrimSpace(os.Getenv("GNH_LINK_DISCOVERY_MIN_PRIORITY")); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed >= 0 && parsed <= 1 {
			return parsed
		}
	}
	return fallback
}
