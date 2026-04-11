package db

import (
	"testing"
	"time"
)

// newTestPressureController returns a controller with zero cooldowns for fast
// testing. maxLimit is set to 88 to match production hard cap.
func newTestPressureController(maxLimit int) *PressureController {
	pc := newPressureController(maxLimit)
	pc.cooldownDown = 0
	pc.cooldownUp = 0
	return pc
}

func TestPressureController_StartsAtInitialLimit(t *testing.T) {
	pc := newTestPressureController(88)
	want := int32(88)
	if got := pc.EffectiveLimit(); got != want {
		t.Fatalf("expected initial limit %d, got %d", want, got)
	}
}

func TestPressureController_InitialLimitClampedToMax(t *testing.T) {
	// When hard cap is below the default initial limit, clamp to hard cap.
	pc := newTestPressureController(20)
	if got := pc.EffectiveLimit(); got != 20 {
		t.Fatalf("expected clamped initial limit 20, got %d", got)
	}
}

func TestPressureController_ReducesOnHighPressure(t *testing.T) {
	pc := newTestPressureController(88)
	pc.highMark = 500

	for range pressureWarmupSamples {
		pc.Record(600)
	}

	if got := pc.EffectiveLimit(); got >= 88 {
		t.Fatalf("expected limit to decrease from %d, got %d", 88, got)
	}
}

func TestPressureController_ReducesByStepDown(t *testing.T) {
	pc := newTestPressureController(88)
	pc.highMark = 500

	before := pc.EffectiveLimit()
	for range pressureWarmupSamples {
		pc.Record(600)
	}
	after := pc.EffectiveLimit()

	if before-after != pressureStepDownDefault {
		t.Fatalf("expected reduction of %d, got %d (before=%d after=%d)",
			pressureStepDownDefault, before-after, before, after)
	}
}

func TestPressureController_RestoresOnLowPressure(t *testing.T) {
	pc := newTestPressureController(88)
	pc.highMark = 500
	pc.lowMark = 100

	// Drive limit down.
	for range pressureWarmupSamples * 5 {
		pc.Record(800)
	}
	reduced := pc.EffectiveLimit()
	if reduced >= 88 {
		t.Fatalf("expected limit to decrease, got %d", reduced)
	}

	// Drive EMA below lowMark.
	for range 60 {
		pc.Record(0)
	}

	restored := pc.EffectiveLimit()
	if restored <= reduced {
		t.Fatalf("expected limit to recover above %d, got %d", reduced, restored)
	}
}

func TestPressureController_RestoresByStepUp(t *testing.T) {
	t.Setenv("GNH_PRESSURE_INITIAL_LIMIT", "30")
	t.Setenv("GNH_PRESSURE_MIN_LIMIT", "30")
	pc := newTestPressureController(88)
	pc.highMark = 500
	pc.lowMark = 100

	// Settle the EMA below lowMark from the start.
	for range pressureWarmupSamples {
		pc.Record(0)
	}
	before := pc.EffectiveLimit()

	// One more sample to trigger a restore.
	pc.Record(0)
	after := pc.EffectiveLimit()

	if after-before != pressureStepUp {
		t.Fatalf("expected restore of %d, got %d (before=%d after=%d)",
			pressureStepUp, after-before, before, after)
	}
}

func TestPressureController_NeverDropsBelowMinLimit(t *testing.T) {
	pc := newTestPressureController(88)
	pc.highMark = 1

	for range 1000 {
		pc.Record(9999)
	}

	if got := pc.EffectiveLimit(); got < pressureMinLimitDefault {
		t.Fatalf("limit %d dropped below minimum %d", got, pressureMinLimitDefault)
	}
}

func TestPressureController_NeverExceedsMaxLimit(t *testing.T) {
	pc := newTestPressureController(88)
	pc.lowMark = 9999

	for range 1000 {
		pc.Record(0)
	}

	if got := pc.EffectiveLimit(); got > 88 {
		t.Fatalf("limit %d exceeded max 88", got)
	}
}

func TestPressureController_ShedCooldownPreventsRapidReduction(t *testing.T) {
	pc := newPressureController(88) // real cooldowns
	pc.cooldownDown = 10 * time.Second
	pc.highMark = 500

	for range pressureWarmupSamples {
		pc.Record(600)
	}
	afterFirst := pc.EffectiveLimit()

	// More high-pressure samples — cooldown should block a second reduction.
	for range pressureWarmupSamples {
		pc.Record(600)
	}
	afterSecond := pc.EffectiveLimit()

	if afterFirst != afterSecond {
		t.Fatalf("shed cooldown not respected: limit changed from %d to %d", afterFirst, afterSecond)
	}
}

func TestPressureController_RestoreCooldownPreventsRapidIncrease(t *testing.T) {
	pc := newPressureController(88)
	pc.cooldownUp = 30 * time.Second
	pc.cooldownDown = 0
	pc.lowMark = 100

	for range pressureWarmupSamples {
		pc.Record(0)
	}
	afterFirst := pc.EffectiveLimit()

	for range pressureWarmupSamples {
		pc.Record(0)
	}
	afterSecond := pc.EffectiveLimit()

	if afterFirst != afterSecond {
		t.Fatalf("restore cooldown not respected: limit changed from %d to %d", afterFirst, afterSecond)
	}
}

func TestPressureController_HoldsInDeadband(t *testing.T) {
	pc := newTestPressureController(88)
	pc.highMark = 500
	pc.lowMark = 100

	initial := pc.EffectiveLimit()

	// EMA in the deadband (between 100 and 500) — limit must not change.
	for range 50 {
		pc.Record(250)
	}

	if got := pc.EffectiveLimit(); got != initial {
		t.Fatalf("limit changed in deadband: %d → %d", initial, got)
	}
}

func TestPressureController_WarmupSamplesRequired(t *testing.T) {
	pc := newTestPressureController(88)
	pc.highMark = 1

	initial := pc.EffectiveLimit()
	for range pressureWarmupSamples - 1 {
		pc.Record(9999)
	}

	if got := pc.EffectiveLimit(); got != initial {
		t.Fatalf("adjusted before warmup complete: limit = %d", got)
	}
}

func TestPressureController_EMAIsSmoothed(t *testing.T) {
	pc := newTestPressureController(88)

	for range 20 {
		pc.Record(100)
	}
	baseline := pc.EMA()

	pc.Record(9999)
	spiked := pc.EMA()

	if spiked >= 9999 {
		t.Fatalf("EMA not smoothed: jumped to %f on single spike", spiked)
	}
	if spiked <= baseline {
		t.Fatalf("EMA did not respond to spike: %f → %f", baseline, spiked)
	}
}

func TestPressureController_AsymmetricSteps(t *testing.T) {
	pc := newTestPressureController(88)

	// Verify stepDown > stepUp (shed faster than restore).
	if pc.stepDown <= pc.stepUp {
		t.Fatalf("expected stepDown (%d) > stepUp (%d)", pc.stepDown, pc.stepUp)
	}
}

func TestPressureController_ClampsConfiguredInitialLimit(t *testing.T) {
	t.Setenv("GNH_PRESSURE_INITIAL_LIMIT", "200")
	t.Setenv("GNH_PRESSURE_MIN_LIMIT", "30")

	pc := newTestPressureController(88)
	if got := pc.EffectiveLimit(); got != 88 {
		t.Fatalf("expected initial limit clamped to 88, got %d", got)
	}
}

func TestPressureController_ClampsConfiguredFloorToMax(t *testing.T) {
	t.Setenv("GNH_PRESSURE_MIN_LIMIT", "60")
	t.Setenv("GNH_PRESSURE_INITIAL_LIMIT", "60")

	pc := newTestPressureController(20)
	if got := pc.minLimit; got != 20 {
		t.Fatalf("expected min limit clamped to 20, got %d", got)
	}
	if got := pc.EffectiveLimit(); got != 20 {
		t.Fatalf("expected initial limit clamped to 20, got %d", got)
	}
}

func TestPressureController_AsymmetricCooldowns(t *testing.T) {
	pc := newPressureController(88)

	// Verify cooldownUp > cooldownDown (restore more slowly than shedding).
	if pc.cooldownUp <= pc.cooldownDown {
		t.Fatalf("expected cooldownUp (%v) > cooldownDown (%v)", pc.cooldownUp, pc.cooldownDown)
	}
}
