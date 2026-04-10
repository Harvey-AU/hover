package db

import (
	"testing"
	"time"
)

// newTestPressureController returns a controller with shortened cooldown for
// testing. maxLimit is set to 50 to give room in both directions.
func newTestPressureController(maxLimit int) *PressureController {
	pc := newPressureController(maxLimit)
	pc.cooldown = 0 // no cooldown in tests
	return pc
}

func TestPressureController_StartsAtMaxLimit(t *testing.T) {
	pc := newTestPressureController(50)
	if got := pc.EffectiveLimit(); got != 50 {
		t.Fatalf("expected initial limit 50, got %d", got)
	}
}

func TestPressureController_ReducesOnHighPressure(t *testing.T) {
	pc := newTestPressureController(50)
	pc.highMark = 300

	// Warm up past minimum samples, then drive EMA above highMark.
	for range pressureWarmupSamples {
		pc.Record(400)
	}

	limit := pc.EffectiveLimit()
	if limit >= 50 {
		t.Fatalf("expected limit to decrease from 50, got %d", limit)
	}
}

func TestPressureController_RestoresOnLowPressure(t *testing.T) {
	pc := newTestPressureController(50)
	pc.highMark = 300
	pc.lowMark = 50

	// Drive limit down.
	for range pressureWarmupSamples * 3 {
		pc.Record(500)
	}
	reduced := pc.EffectiveLimit()
	if reduced >= 50 {
		t.Fatalf("expected limit to decrease, got %d", reduced)
	}

	// Now drive EMA down — need enough samples to shift the smoothed value.
	for range 50 {
		pc.Record(0)
	}

	restored := pc.EffectiveLimit()
	if restored <= reduced {
		t.Fatalf("expected limit to recover above %d, got %d", reduced, restored)
	}
}

func TestPressureController_NeverDropsBelowMinLimit(t *testing.T) {
	pc := newTestPressureController(50)
	pc.highMark = 1 // trigger on any non-zero wait

	for range 1000 {
		pc.Record(9999)
	}

	if got := pc.EffectiveLimit(); got < pressureMinLimit {
		t.Fatalf("limit %d dropped below minimum %d", got, pressureMinLimit)
	}
}

func TestPressureController_NeverExceedsMaxLimit(t *testing.T) {
	pc := newTestPressureController(50)
	pc.lowMark = 9999 // always below lowMark → always restore

	for range 1000 {
		pc.Record(0)
	}

	if got := pc.EffectiveLimit(); got > 50 {
		t.Fatalf("limit %d exceeded max 50", got)
	}
}

func TestPressureController_CooldownPreventsRapidAdjustment(t *testing.T) {
	pc := newPressureController(50) // real cooldown
	pc.cooldown = 10 * time.Second
	pc.highMark = 300

	// Warm up.
	for range pressureWarmupSamples {
		pc.Record(400)
	}
	afterFirst := pc.EffectiveLimit()

	// Second batch of high-pressure samples — cooldown should block another adjustment.
	for range pressureWarmupSamples {
		pc.Record(400)
	}
	afterSecond := pc.EffectiveLimit()

	if afterFirst != afterSecond {
		t.Fatalf("cooldown not respected: limit changed from %d to %d", afterFirst, afterSecond)
	}
}

func TestPressureController_WarmupSamplesRequired(t *testing.T) {
	pc := newTestPressureController(50)
	pc.highMark = 1

	// Feed fewer than warmupSamples — should not adjust yet.
	for range pressureWarmupSamples - 1 {
		pc.Record(9999)
	}

	if got := pc.EffectiveLimit(); got != 50 {
		t.Fatalf("adjusted before warmup complete: limit = %d", got)
	}
}

func TestPressureController_EMAIsSmoothed(t *testing.T) {
	pc := newTestPressureController(50)

	// Seed EMA with a stable value.
	for range 20 {
		pc.Record(100)
	}
	baseline := pc.EMA()

	// Single spike — EMA should move but not jump to 9999.
	pc.Record(9999)
	spiked := pc.EMA()

	if spiked >= 9999 {
		t.Fatalf("EMA not smoothed: jumped to %f on single spike", spiked)
	}
	if spiked <= baseline {
		t.Fatalf("EMA did not respond to spike: %f → %f", baseline, spiked)
	}
}
