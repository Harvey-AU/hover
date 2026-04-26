// Package lighthouse implements the sampler, scheduler, and runner used
// to capture Lighthouse performance audits for a small subset of pages
// during each crawl. See docs/plans/lighthouse-performance-reports.md
// for the full design.
package lighthouse

import (
	"math"
	"sort"
)

// Per-band sampling parameters. The formula picks 2.5% of completed
// pages per extreme band (fastest + slowest), floored at 1 and capped
// at 50, so a typical crawl is audited at roughly 5% of pages with a
// hard ceiling of 100 audits per job. Tunable here so we can adjust
// the mix without touching the rest of the codebase.
const (
	bandFraction = 0.025
	bandFloor    = 1
	bandCap      = 50
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
// for a job with completedPages successful tasks so far. Floored at
// 1 (so even a 1-page site gets one audit per band) and capped at 50
// (so a 10,000-page crawl never queues more than 100 audits per job).
func PerBand(completedPages int) int {
	if completedPages <= 0 {
		return 0
	}

	n := int(math.Round(float64(completedPages) * bandFraction))
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
