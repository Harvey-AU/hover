package lighthouse

import (
	"fmt"
	"testing"
)

func TestPerBandFloor(t *testing.T) {
	// floor(sqrt(pages) * 0.15) = 0 for everything below ~50, so the
	// floor binds and even a 1-page site gets 1 fastest + 1 slowest.
	cases := []struct {
		pages int
		want  int
	}{
		{1, 1},  // sqrt(1)*0.15 = 0.15  → floor 0 → floor binds
		{5, 1},  // sqrt(5)*0.15 = 0.34
		{10, 1}, // sqrt(10)*0.15 = 0.47
		{20, 1}, // sqrt(20)*0.15 = 0.67
		{39, 1}, // sqrt(39)*0.15 = 0.94
		{40, 1}, // anchor: 40 → 1 (floor)
		{44, 1}, // sqrt(44)*0.15 = 0.99
	}
	for _, tc := range cases {
		if got := PerBand(tc.pages); got != tc.want {
			t.Errorf("PerBand(%d) = %d; want %d", tc.pages, got, tc.want)
		}
	}
}

func TestPerBandSquareRootScaling(t *testing.T) {
	// Mid range: floor(sqrt(pages) * 0.15) yields the documented
	// anchor points exactly. Values in between fall on the curve
	// without surprise rounding.
	cases := []struct {
		pages int
		want  int
	}{
		{45, 1},     // sqrt(45)*0.15 = 1.005 → 1 (just hits floor 1 organically)
		{100, 1},    // sqrt(100)*0.15 = 1.5 → floor 1
		{178, 2},    // sqrt(178)*0.15 = 2.0 → floor 2
		{200, 2},    // ANCHOR: 200 → 2
		{500, 3},    // sqrt(500)*0.15 = 3.35 → floor 3
		{1000, 4},   // ANCHOR: 1,000 → 4
		{2000, 6},   // sqrt(2000)*0.15 = 6.7 → floor 6
		{5000, 10},  // sqrt(5000)*0.15 = 10.6 → floor 10
		{8000, 13},  // sqrt(8000)*0.15 = 13.4 → floor 13
		{9999, 14},  // sqrt(9999)*0.15 ≈ 14.99 → floor 14
		{10000, 15}, // ANCHOR: 10,000 → 15 (exact)
	}
	for _, tc := range cases {
		if got := PerBand(tc.pages); got != tc.want {
			t.Errorf("PerBand(%d) = %d; want %d", tc.pages, got, tc.want)
		}
	}
}

func TestPerBandCap(t *testing.T) {
	// Cap binds at exactly 10,000 pages and never exceeds 15. The
	// sub-linear curve means audit count plateaus while %-audited
	// keeps tapering, which is the intended cost ceiling shape.
	cases := []struct {
		pages int
		want  int
	}{
		{10000, 15},
		{12000, 15},
		{50000, 15},
		{1_000_000, 15},
	}
	for _, tc := range cases {
		if got := PerBand(tc.pages); got != tc.want {
			t.Errorf("PerBand(%d) = %d; want %d", tc.pages, got, tc.want)
		}
	}
}

func TestPerBandZeroOrNegative(t *testing.T) {
	for _, n := range []int{0, -1, -100} {
		if got := PerBand(n); got != 0 {
			t.Errorf("PerBand(%d) = %d; want 0", n, got)
		}
	}
}

// makeTasks builds n synthetic completed tasks with response_time = i*10
// (so PageID 0 is fastest, PageID n-1 is slowest).
func makeTasks(n int) []CompletedTask {
	tasks := make([]CompletedTask, n)
	for i := 0; i < n; i++ {
		tasks[i] = CompletedTask{
			PageID:       i + 1,
			TaskID:       fmt.Sprintf("task-%d", i+1),
			ResponseTime: int64((i + 1) * 10),
		}
	}
	return tasks
}

func TestSampleEmptyInput(t *testing.T) {
	if got := SelectSamples(nil, 10, nil); got != nil {
		t.Errorf("expected nil for empty input, got %v", got)
	}
	if got := SelectSamples([]CompletedTask{}, 10, nil); got != nil {
		t.Errorf("expected nil for empty slice, got %v", got)
	}
}

func TestSampleSingleTask(t *testing.T) {
	// 1 task, perBand = 1: fastest takes the only candidate, slowest
	// gets nothing because the bands must be disjoint.
	tasks := makeTasks(1)
	got := SelectSamples(tasks, 10, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 sample, got %d (%+v)", len(got), got)
	}
	if got[0].Band != BandFastest {
		t.Errorf("expected fastest band, got %s", got[0].Band)
	}
	if got[0].Task.PageID != 1 {
		t.Errorf("expected PageID 1, got %d", got[0].Task.PageID)
	}
}

func TestSampleSmallSetSplitsBands(t *testing.T) {
	// 5 tasks, perBand = 1: one fastest + one slowest, no overlap.
	tasks := makeTasks(5)
	got := SelectSamples(tasks, 10, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 samples, got %d (%+v)", len(got), got)
	}
	if got[0].Band != BandFastest || got[0].Task.PageID != 1 {
		t.Errorf("fastest mismatch: %+v", got[0])
	}
	if got[1].Band != BandSlowest || got[1].Task.PageID != 5 {
		t.Errorf("slowest mismatch: %+v", got[1])
	}
}

func TestSampleMidSizedDistribution(t *testing.T) {
	// 200 tasks, perBand = 2 (sqrt(200)*0.15 = 2.12 → floor 2): expect
	// 2 fastest + 2 slowest, disjoint.
	tasks := makeTasks(200)
	got := SelectSamples(tasks, 10, nil)
	if len(got) != 4 {
		t.Fatalf("expected 4 samples, got %d", len(got))
	}

	seen := make(map[int]bool)
	var fastestCount, slowestCount int
	for _, s := range got {
		if seen[s.Task.PageID] {
			t.Errorf("page %d appeared twice", s.Task.PageID)
		}
		seen[s.Task.PageID] = true
		switch s.Band {
		case BandFastest:
			fastestCount++
		case BandSlowest:
			slowestCount++
		}
	}
	if fastestCount != 2 || slowestCount != 2 {
		t.Errorf("expected 2 fastest + 2 slowest, got %d + %d", fastestCount, slowestCount)
	}

	// Fastest band should hold pages 1..2 (lowest response_time).
	for i := 0; i < 2; i++ {
		if got[i].Band != BandFastest {
			t.Errorf("expected fastest at index %d, got %s", i, got[i].Band)
		}
		if got[i].Task.PageID != i+1 {
			t.Errorf("fastest order wrong at %d: got page %d", i, got[i].Task.PageID)
		}
	}
	// Slowest band should hold pages 200..199 (highest response_time first).
	for i := 0; i < 2; i++ {
		want := 200 - i
		if got[2+i].Band != BandSlowest {
			t.Errorf("expected slowest at index %d, got %s", 2+i, got[2+i].Band)
		}
		if got[2+i].Task.PageID != want {
			t.Errorf("slowest order wrong at %d: got page %d, want %d", 2+i, got[2+i].Task.PageID, want)
		}
	}
}

func TestSampleAtCap(t *testing.T) {
	// 10,000 tasks, perBand = 15 (cap): expect exactly 15 + 15 = 30.
	tasks := makeTasks(10000)
	got := SelectSamples(tasks, 100, nil)
	if len(got) != 30 {
		t.Fatalf("expected 30 samples at cap, got %d", len(got))
	}
	var fastest, slowest int
	for _, s := range got {
		switch s.Band {
		case BandFastest:
			fastest++
		case BandSlowest:
			slowest++
		}
	}
	if fastest != 15 || slowest != 15 {
		t.Errorf("expected 15/15 split at cap, got %d/%d", fastest, slowest)
	}
}

func TestSampleDedupeAcrossMilestones(t *testing.T) {
	// Once the per-band quota for a 200-page job (perBand = 2) is
	// satisfied, subsequent milestone calls must not top up further —
	// the cap is global per-job, not per-call. PR #357 violated this:
	// each milestone happily picked another perBand fastest + slowest
	// from the not-yet-sampled pool, so a 200-page crawl could
	// accumulate ≈30 rows across 10 milestones instead of 4.
	tasks := makeTasks(200)
	first := SelectSamples(tasks, 10, nil)
	if len(first) != 4 {
		t.Fatalf("first pass expected 4 samples, got %d", len(first))
	}

	already := make(map[int]SelectionBand)
	for _, s := range first {
		already[s.Task.PageID] = s.Band
	}

	second := SelectSamples(tasks, 20, already)
	if len(second) != 0 {
		t.Fatalf("second pass expected 0 samples (quota already met), got %d (%+v)",
			len(second), second)
	}

	// Combined coverage: still 4 distinct pages, not 8.
	all := make(map[int]struct{})
	for _, s := range first {
		all[s.Task.PageID] = struct{}{}
	}
	for _, s := range second {
		all[s.Task.PageID] = struct{}{}
	}
	if len(all) != 4 {
		t.Errorf("expected 4 distinct pages across two milestones, got %d", len(all))
	}
}

func TestSampleAllAlreadyConsumed(t *testing.T) {
	// All candidates already sampled — nothing left to schedule.
	tasks := makeTasks(20)
	already := make(map[int]SelectionBand)
	for _, t := range tasks {
		// Half tagged fastest, half tagged slowest; doesn't matter for
		// this test — quota and dedupe both filter the pool to empty.
		if t.PageID%2 == 0 {
			already[t.PageID] = BandFastest
		} else {
			already[t.PageID] = BandSlowest
		}
	}
	if got := SelectSamples(tasks, 50, already); got != nil {
		t.Errorf("expected nil when nothing left, got %+v", got)
	}
}

// simulateMilestoneRun walks a job through the 10%, 20%, …, 100%
// milestones the JobManager fires, threading the prior calls' picks
// (with their bands) back in as alreadySampled each iteration. It
// returns the merged result so tests can assert the global cap.
func simulateMilestoneRun(t *testing.T, totalPages int) []Sample {
	t.Helper()

	tasks := makeTasks(totalPages)
	already := make(map[int]SelectionBand)
	var combined []Sample

	for milestone := 10; milestone <= 100; milestone += 10 {
		completedSoFar := totalPages * milestone / 100
		if completedSoFar > totalPages {
			completedSoFar = totalPages
		}
		picks := SelectSamples(tasks[:completedSoFar], milestone, already)
		for _, s := range picks {
			if _, dup := already[s.Task.PageID]; dup {
				t.Errorf("milestone %d returned already-sampled page %d",
					milestone, s.Task.PageID)
			}
			already[s.Task.PageID] = s.Band
			combined = append(combined, s)
		}
	}
	return combined
}

func TestSampleGlobalCap_337Pages(t *testing.T) {
	// 337 pages → perBand = 2 → global cap = 4 audits total.
	// Pre-fix (PR #357) this scenario produced ≈30 rows because each
	// milestone re-spent the per-band quota. With the global cap the
	// total is exactly 4 (2 fastest + 2 slowest).
	picks := simulateMilestoneRun(t, 337)
	if len(picks) != 4 {
		t.Fatalf("expected exactly 4 samples for 337-page job, got %d", len(picks))
	}

	var fastest, slowest int
	seen := make(map[int]bool)
	for _, s := range picks {
		if seen[s.Task.PageID] {
			t.Errorf("page %d picked twice across milestones", s.Task.PageID)
		}
		seen[s.Task.PageID] = true
		switch s.Band {
		case BandFastest:
			fastest++
		case BandSlowest:
			slowest++
		}
	}
	if fastest != 2 || slowest != 2 {
		t.Errorf("expected 2 fastest + 2 slowest, got %d + %d", fastest, slowest)
	}
}

func TestSampleGlobalCap_200Pages(t *testing.T) {
	// 200 pages → perBand = 2 → cap = 4 audits total.
	picks := simulateMilestoneRun(t, 200)
	if len(picks) != 4 {
		t.Fatalf("expected exactly 4 samples for 200-page job, got %d", len(picks))
	}
}

func TestSampleGlobalCap_1000Pages(t *testing.T) {
	// 1,000 pages → perBand = 4 → cap = 8 audits total.
	picks := simulateMilestoneRun(t, 1000)
	if len(picks) != 8 {
		t.Fatalf("expected exactly 8 samples for 1,000-page job, got %d", len(picks))
	}
}

func TestSampleGlobalCap_10000Pages(t *testing.T) {
	// 10,000 pages → perBand = 15 (cap) → cap = 30 audits total.
	picks := simulateMilestoneRun(t, 10000)
	if len(picks) != 30 {
		t.Fatalf("expected exactly 30 samples for 10,000-page job, got %d", len(picks))
	}

	var fastest, slowest int
	for _, s := range picks {
		switch s.Band {
		case BandFastest:
			fastest++
		case BandSlowest:
			slowest++
		}
	}
	if fastest != 15 || slowest != 15 {
		t.Errorf("expected 15 fastest + 15 slowest at cap, got %d + %d", fastest, slowest)
	}
}

func TestSampleDeterministicTieBreak(t *testing.T) {
	// Two tasks with identical response_time should be ordered by
	// PageID so the sampler is deterministic across runs.
	tasks := []CompletedTask{
		{PageID: 7, TaskID: "b", ResponseTime: 100},
		{PageID: 3, TaskID: "a", ResponseTime: 100},
		{PageID: 5, TaskID: "c", ResponseTime: 200},
		{PageID: 9, TaskID: "d", ResponseTime: 50},
	}
	got := SelectSamples(tasks, 10, nil)
	// perBand for 4 tasks = 1 (floor), so 1 fastest + 1 slowest.
	if len(got) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(got))
	}
	if got[0].Task.PageID != 9 {
		t.Errorf("expected fastest PageID 9 (rt=50), got %d", got[0].Task.PageID)
	}
	if got[1].Task.PageID != 5 {
		t.Errorf("expected slowest PageID 5 (rt=200), got %d", got[1].Task.PageID)
	}
}
