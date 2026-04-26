// Package lighthouse implements the sampler, scheduler, and runner used
// to capture Lighthouse performance audits for a small subset of pages
// during each crawl. See docs/plans/lighthouse-performance-reports.md
// for the full design.
package lighthouse

import (
	"math"
	"sort"
)

// Per-band sampling parameters. The formula picks
// floor(sqrt(completed_pages) * 0.15) audits per extreme band
// (fastest + slowest), floored at 1 and capped at 15. A square-root
// curve keeps small sites well-covered (every site gets at least
// 1 fastest + 1 slowest) while sub-linearly tapering on large sites
// so the lighthouse fleet doesn't scale linearly with crawl size.
//
// Anchor points the curve lands on:
//
//	    40 pages →  1 per band (floor) →  2 audits
//	   200 pages →  2 per band         →  4 audits
//	 1,000 pages →  4 per band         →  8 audits
//	10,000 pages → 15 per band (cap)   → 30 audits
//
// Tunable here so we can adjust the mix without touching the rest of
// the codebase. The previous shape (2.5%/band, cap 50) is preserved
// in git history; switching back is a single-commit change.
const (
	bandSqrtFactor = 0.15
	bandFloor      = 1
	bandCap        = 15
)

// CompletedTask is the input shape the sampler needs from a completed
// crawl task. ResponseTime is the HTTP response time in milliseconds
// (tasks.response_time, BIGINT). The sampler does not need anything
// else from the row.
type CompletedTask struct {
	PageID       int
	TaskID       string
	ResponseTime int64
}

// SelectionBand identifies which extreme of the response-time
// distribution a sampled task came from. The reconcile band is
// reserved for the 100% pass run by the scheduler at job completion.
type SelectionBand string

const (
	BandFastest   SelectionBand = "fastest"
	BandSlowest   SelectionBand = "slowest"
	BandReconcile SelectionBand = "reconcile"
)

// Sample is one task selected by the sampler, tagged with the band it
// came from. The scheduler turns each Sample into a lighthouse_runs
// row plus an outbox entry.
type Sample struct {
	Task CompletedTask
	Band SelectionBand
}

// PerBand returns the number of audits to schedule per extreme band
// for a job with completedPages successful tasks so far. Floored at 1
// (so even a 1-page site gets one audit per band) and capped at 15
// (so a 10,000-page crawl never queues more than 30 audits per job).
//
// math.Floor (rather than Round) keeps the curve from over-shooting
// the cap at exactly 10,000 pages and matches the published anchor
// points exactly.
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

// SelectSamples picks up to PerBand(len(tasks)) fastest and slowest
// tasks from completed, after excluding any whose PageID appears in
// alreadySampled. The fastest and slowest sets are guaranteed
// disjoint: when fewer than 2*perBand candidates remain, fastest
// takes priority and slowest fills from what's left.
//
// Order in the returned slice is fastest band first (ascending
// response_time), then slowest band (descending response_time). The
// scheduler treats this slice as the per-milestone work list.
//
// The function is pure — it does not touch the database or the
// network — so it is straightforward to unit test.
func SelectSamples(completed []CompletedTask, milestone int, alreadySampled map[int]struct{}) []Sample {
	if len(completed) == 0 {
		return nil
	}

	// Filter out previously-sampled pages. Defensive copy so the
	// caller's slice ordering is preserved.
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

	perBand := PerBand(len(completed))
	if perBand == 0 {
		return nil
	}

	// Sort ascending by response time. Ties broken by PageID so the
	// selection is deterministic across runs.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].ResponseTime != candidates[j].ResponseTime {
			return candidates[i].ResponseTime < candidates[j].ResponseTime
		}
		return candidates[i].PageID < candidates[j].PageID
	})

	fastestN := perBand
	if fastestN > len(candidates) {
		fastestN = len(candidates)
	}

	// Slowest pulls from the tail, but never overlaps with fastest.
	remaining := len(candidates) - fastestN
	slowestN := perBand
	if slowestN > remaining {
		slowestN = remaining
	}

	out := make([]Sample, 0, fastestN+slowestN)
	for i := 0; i < fastestN; i++ {
		out = append(out, Sample{Task: candidates[i], Band: BandFastest})
	}
	// Walk the tail in descending order so the slowest task is first
	// within the slowest band.
	for i := 0; i < slowestN; i++ {
		idx := len(candidates) - 1 - i
		out = append(out, Sample{Task: candidates[idx], Band: BandSlowest})
	}
	return out
}
