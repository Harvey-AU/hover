package lighthouse

import (
	"fmt"
	"testing"
)

func TestPerBandFloor(t *testing.T) {
	// 2.5% of these would all round below 1, so the floor binds.
	cases := []struct {
		pages int
		want  int
	}{
		{1, 1},
		{5, 1},
		{10, 1},
		{20, 1},
		{39, 1}, // round(39 * 0.025) = round(0.975) = 1
	}
	for _, tc := range cases {
		if got := PerBand(tc.pages); got != tc.want {
			t.Errorf("PerBand(%d) = %d; want %d", tc.pages, got, tc.want)
		}
	}
}

func TestPerBandLinearScaling(t *testing.T) {
	// Mid range: 2.5% rounds to a meaningful integer.
	cases := []struct {
		pages int
		want  int
	}{
		{40, 1},   // round(40 * 0.025) = 1
		{50, 1},   // round(50 * 0.025) = round(1.25) = 1
		{100, 3},  // round(100 * 0.025) = round(2.5) = 3 (banker's rounding caveat: math.Round rounds half away from zero)
		{200, 5},  // round(5)
		{500, 13}, // round(12.5) = 13
		{1000, 25},
		{1500, 38}, // round(37.5) = 38
		{1900, 48}, // round(47.5) = 48
	}
	for _, tc := range cases {
		if got := PerBand(tc.pages); got != tc.want {
			t.Errorf("PerBand(%d) = %d; want %d", tc.pages, got, tc.want)
		}
	}
}

func TestPerBandCap(t *testing.T) {
	// Cap binds at ~2,000 pages and never exceeds 50.
	cases := []struct {
		pages int
		want  int
	}{
		{2000, 50},
		{5000, 50},
		{10000, 50},
		{100000, 50},
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
	// 200 tasks, perBand = 5: expect 5 fastest + 5 slowest, disjoint.
	tasks := makeTasks(200)
	got := SelectSamples(tasks, 10, nil)
	if len(got) != 10 {
		t.Fatalf("expected 10 samples, got %d", len(got))
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
	if fastestCount != 5 || slowestCount != 5 {
		t.Errorf("expected 5 fastest + 5 slowest, got %d + %d", fastestCount, slowestCount)
	}

	// Fastest band should hold pages 1..5 (lowest response_time).
	for i := 0; i < 5; i++ {
		if got[i].Band != BandFastest {
			t.Errorf("expected fastest at index %d, got %s", i, got[i].Band)
		}
		if got[i].Task.PageID != i+1 {
			t.Errorf("fastest order wrong at %d: got page %d", i, got[i].Task.PageID)
		}
	}
	// Slowest band should hold pages 200..196 (highest response_time first).
	for i := 0; i < 5; i++ {
		want := 200 - i
		if got[5+i].Band != BandSlowest {
			t.Errorf("expected slowest at index %d, got %s", 5+i, got[5+i].Band)
		}
		if got[5+i].Task.PageID != want {
			t.Errorf("slowest order wrong at %d: got page %d, want %d", 5+i, got[5+i].Task.PageID, want)
		}
	}
}

func TestSampleAtCap(t *testing.T) {
	// 10,000 tasks, perBand = 50 (cap): expect exactly 50 + 50 = 100.
	tasks := makeTasks(10000)
	got := SelectSamples(tasks, 100, nil)
	if len(got) != 100 {
		t.Fatalf("expected 100 samples at cap, got %d", len(got))
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
	if fastest != 50 || slowest != 50 {
		t.Errorf("expected 50/50 split at cap, got %d/%d", fastest, slowest)
	}
}

func TestSampleDedupeAcrossMilestones(t *testing.T) {
	// First milestone over 200 tasks samples 5 fastest + 5 slowest.
	// Second milestone over the same 200 tasks should skip the
	// pre-sampled IDs and select different ones from the remaining.
	tasks := makeTasks(200)
	first := SelectSamples(tasks, 10, nil)
	if len(first) != 10 {
		t.Fatalf("first pass expected 10 samples, got %d", len(first))
	}

	already := make(map[int]struct{})
	for _, s := range first {
		already[s.Task.PageID] = struct{}{}
	}

	second := SelectSamples(tasks, 20, already)
	if len(second) != 10 {
		t.Fatalf("second pass expected 10 samples, got %d", len(second))
	}

	for _, s := range second {
		if _, dup := already[s.Task.PageID]; dup {
			t.Errorf("second pass returned already-sampled page %d", s.Task.PageID)
		}
	}

	// Combined coverage: first 10 + next 10 = 20 distinct pages.
	all := make(map[int]struct{})
	for _, s := range first {
		all[s.Task.PageID] = struct{}{}
	}
	for _, s := range second {
		all[s.Task.PageID] = struct{}{}
	}
	if len(all) != 20 {
		t.Errorf("expected 20 distinct pages across two milestones, got %d", len(all))
	}
}

func TestSampleAllAlreadyConsumed(t *testing.T) {
	// All candidates already sampled — nothing left to schedule.
	tasks := makeTasks(20)
	already := make(map[int]struct{})
	for _, t := range tasks {
		already[t.PageID] = struct{}{}
	}
	if got := SelectSamples(tasks, 50, already); got != nil {
		t.Errorf("expected nil when nothing left, got %+v", got)
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
