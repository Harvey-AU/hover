package jobs

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/Harvey-AU/hover/internal/crawler"
)

// defaultWAFCircuitBreakerThreshold is the consecutive-WAF-response
// count that trips the breaker mid-job. Tuned conservatively: once we
// see three responses in a row carrying recognised WAF fingerprints we
// have very high confidence the domain has flipped to blocking us.
const defaultWAFCircuitBreakerThreshold = 3

// WAFCircuitBreaker tracks per-job runs of consecutive WAF-flagged
// responses and trips a callback once the threshold is reached. The
// state lives in process memory — fine for the current single-pod
// worker deployment (fly.worker.toml). If the worker ever scales out
// horizontally the counter would undercount across replicas; in that
// case migrate it to Redis (INCR job:<id>:waf_consecutive with TTL)
// alongside the existing running-counters HASH.
type WAFCircuitBreaker struct {
	threshold int

	mu      sync.Mutex
	counts  map[string]int                  // jobID -> consecutive WAF responses
	tripped map[string]struct{}             // jobIDs already tripped (single-fire)
	vendors map[string]crawler.WAFDetection // last seen detection per job
}

func NewWAFCircuitBreaker() *WAFCircuitBreaker {
	return &WAFCircuitBreaker{
		threshold: wafCircuitBreakerThreshold(),
		counts:    make(map[string]int),
		tripped:   make(map[string]struct{}),
		vendors:   make(map[string]crawler.WAFDetection),
	}
}

func wafCircuitBreakerThreshold() int {
	if v := strings.TrimSpace(os.Getenv("GNH_WAF_CIRCUIT_BREAKER_THRESHOLD")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		jobsLog.Warn("invalid GNH_WAF_CIRCUIT_BREAKER_THRESHOLD; using default",
			"value", v, "default", defaultWAFCircuitBreakerThreshold)
	}
	return defaultWAFCircuitBreakerThreshold
}

// Observe records the WAF status of a single task outcome. When det is
// nil or det.Blocked is false the per-job counter resets, ensuring the
// breaker only fires on truly consecutive blocks rather than a sparse
// sprinkle. Returns true exactly once per job — the moment the
// threshold is first crossed — so callers can fire BlockJob without
// guarding against duplicates.
func (b *WAFCircuitBreaker) Observe(jobID string, det *crawler.WAFDetection) (tripped bool, vendor crawler.WAFDetection) {
	if b == nil || jobID == "" {
		return false, crawler.WAFDetection{}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, alreadyTripped := b.tripped[jobID]; alreadyTripped {
		return false, crawler.WAFDetection{}
	}

	if det == nil || !det.Blocked {
		delete(b.counts, jobID)
		delete(b.vendors, jobID)
		return false, crawler.WAFDetection{}
	}

	b.counts[jobID]++
	b.vendors[jobID] = *det

	if b.counts[jobID] >= b.threshold {
		b.tripped[jobID] = struct{}{}
		v := b.vendors[jobID]
		delete(b.counts, jobID)
		delete(b.vendors, jobID)
		return true, v
	}
	return false, crawler.WAFDetection{}
}

// Forget drops all per-job state. Called from OnJobTerminated so a
// long-running worker doesn't accumulate per-job map entries.
func (b *WAFCircuitBreaker) Forget(jobID string) {
	if b == nil || jobID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.counts, jobID)
	delete(b.vendors, jobID)
	delete(b.tripped, jobID)
}

// Threshold exposes the configured trip count for telemetry/logging.
func (b *WAFCircuitBreaker) Threshold() int {
	if b == nil {
		return 0
	}
	return b.threshold
}

// MaybeTripFromOutcome is the convenience wrapper used from the stream
// worker hot path. It pulls the WAF detection from the outcome, calls
// Observe, and on a trip dispatches BlockJob in a fresh detached
// context (the caller's context is the per-task one and may be on its
// way out).
func (b *WAFCircuitBreaker) MaybeTripFromOutcome(ctx context.Context, jm JobManagerInterface, outcome *TaskOutcome) {
	if b == nil || jm == nil || outcome == nil || outcome.Task == nil {
		return
	}
	var det *crawler.WAFDetection
	if outcome.CrawlResult != nil {
		det = outcome.CrawlResult.WAF
	}
	tripped, vendor := b.Observe(outcome.Task.JobID, det)
	if !tripped {
		return
	}

	jobsLog.Info("WAF circuit breaker tripped",
		"job_id", outcome.Task.JobID,
		"threshold", b.Threshold(),
		"vendor", vendor.Vendor,
		"reason", vendor.Reason)

	// Detach so the per-task ctx cancellation doesn't truncate the
	// terminal-state writes.
	bgCtx := context.WithoutCancel(ctx)
	if err := jm.BlockJob(bgCtx, outcome.Task.JobID, vendor.Vendor, "circuit breaker: "+vendor.Reason); err != nil {
		jobsLog.Error("BlockJob from circuit breaker failed",
			"error", err, "job_id", outcome.Task.JobID)
	}
}
