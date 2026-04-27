// Package lighthouse implements the sampler, scheduler, and runner used
// to capture Lighthouse performance audits for a small subset of pages
// during each crawl. See docs/plans/lighthouse-performance-reports.md
// for the full design.
package lighthouse

import (
	"math"
	"sort"
)

// Per-band sample count = floor(sqrt(completed_pages) * 0.15),
// floored at 1, capped at 15. Anchors:
//
//	    40 pages →  1 per band →  2 audits
//	   200 pages →  2 per band →  4 audits
//	 1,000 pages →  4 per band →  8 audits
//	10,000 pages → 15 per band → 30 audits
const (
	bandSqrtFactor = 0.15
	bandFloor      = 1
	bandCap        = 15
)

// CompletedTask carries the task fields the sampler needs.
// ResponseTime is HTTP response time in milliseconds.
type CompletedTask struct {
	PageID       int
	TaskID       string
	ResponseTime int64
}

// SelectionBand identifies which response-time extreme a sample came
// from. Reconcile is reserved for the 100% pass at job completion.
type SelectionBand string

const (
	BandFastest   SelectionBand = "fastest"
	BandSlowest   SelectionBand = "slowest"
	BandReconcile SelectionBand = "reconcile"
)

type Sample struct {
	Task CompletedTask
	Band SelectionBand
}

// PerBand uses Floor not Round so 10,000 pages lands exactly on the
// cap rather than overshooting it.
func PerBand(completedPages int) int {
	if completedPages <= 0 {
		return 0
	}

	n := int(math.Floor(math.Sqrt(float64(completedPages)) * bandSqrtFactor))
	if n < bandFloor {
		n = bandFloor
	}
	if n > bandCap {
		n = bandCap
	}
	return n
}

// SelectSamples tops up fastest/slowest quotas to PerBand(len(completed)).
// alreadySampled is consulted for both dedupe (page never appears
// twice) and quota accounting (existing band counts subtracted from
// target). BandReconcile rows count toward dedupe but not quotas.
// On contention, fastest takes priority over slowest.
func SelectSamples(completed []CompletedTask, milestone int, alreadySampled map[int]SelectionBand) []Sample {
	if len(completed) == 0 {
		return nil
	}

	target := PerBand(len(completed))
	if target == 0 {
		return nil
	}

	var fastestExisting, slowestExisting int
	for _, b := range alreadySampled {
		switch b {
		case BandFastest:
			fastestExisting++
		case BandSlowest:
			slowestExisting++
		}
	}

	fastestNeeded := target - fastestExisting
	if fastestNeeded < 0 {
		fastestNeeded = 0
	}
	slowestNeeded := target - slowestExisting
	if slowestNeeded < 0 {
		slowestNeeded = 0
	}
	if fastestNeeded == 0 && slowestNeeded == 0 {
		return nil
	}

	candidates := make([]CompletedTask, 0, len(completed))
	for _, t := range completed {
		if _, seen := alreadySampled[t.PageID]; seen {
			continue
		}
		candidates = append(candidates, t)
	}
	if len(candidates) == 0 {
		return nil
	}

	// PageID tiebreak makes selection deterministic.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].ResponseTime != candidates[j].ResponseTime {
			return candidates[i].ResponseTime < candidates[j].ResponseTime
		}
		return candidates[i].PageID < candidates[j].PageID
	})

	fastestN := fastestNeeded
	if fastestN > len(candidates) {
		fastestN = len(candidates)
	}

	remaining := len(candidates) - fastestN
	slowestN := slowestNeeded
	if slowestN > remaining {
		slowestN = remaining
	}

	out := make([]Sample, 0, fastestN+slowestN)
	for i := 0; i < fastestN; i++ {
		out = append(out, Sample{Task: candidates[i], Band: BandFastest})
	}
	for i := 0; i < slowestN; i++ {
		idx := len(candidates) - 1 - i
		out = append(out, Sample{Task: candidates[idx], Band: BandSlowest})
	}
	return out
}
