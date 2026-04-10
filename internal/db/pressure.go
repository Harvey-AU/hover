package db

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// Default tuning constants for the pressure controller.
// High/low marks are overridable via env vars; everything else is hardcoded.
const (
	pressureHighMarkDefaultMs = 500.0            // EMA above this → shed load
	pressureLowMarkDefaultMs  = 100.0            // EMA below this → restore capacity
	pressureEMAAlpha          = 0.15             // smoothing factor (lower = smoother)
	pressureStepDown          = int32(10)        // slots removed per shed adjustment
	pressureStepUp            = int32(3)         // slots added per restore adjustment
	pressureMinLimit          = int32(10)        // never drop below this
	pressureCooldownDown      = 10 * time.Second // min gap between shed adjustments
	pressureCooldownUp        = 30 * time.Second // min gap between restore adjustments
	pressureWarmupSamples     = 5                // samples required before acting
	pressureInitialLimit      = int32(55)        // conservative start — known-safe level
)

// PressureController adaptively adjusts the queue semaphore's effective limit
// based on observed pool_wait_total per transaction.
//
// Signal: every completed Execute / ExecuteWithContext call reports its
// cumulative pool_wait_total. An EMA of those samples is compared against
// highMark / lowMark thresholds:
//
//   - EMA > highMark → reduce limit by stepDown every cooldownDown (floor: minLimit)
//   - EMA < lowMark  → restore limit by stepUp every cooldownUp (ceiling: maxLimit)
//   - Between marks  → hold steady
//
// Shedding is faster than restoring by design: react quickly to protect
// Supabase, but open capacity back up cautiously.
//
// The controller starts at pressureInitialLimit (55) rather than maxLimit so
// that a restart under load doesn't immediately saturate the DB before the
// EMA has warmed up.
type PressureController struct {
	mu            sync.Mutex
	ema           float64
	samples       int
	lastScaleDown time.Time
	lastScaleUp   time.Time

	// limit is read on every hot-path call to ensurePoolCapacity, so it must
	// be accessed atomically. Writes happen at most once per cooldown period.
	limit    atomic.Int32
	maxLimit int32

	// tuning — split by direction for asymmetric behaviour
	highMark     float64
	lowMark      float64
	stepDown     int32
	stepUp       int32
	cooldownDown time.Duration
	cooldownUp   time.Duration
	minLimit     int32
}

// newPressureController creates a controller that starts at pressureInitialLimit
// (clamped to maxLimit) and adjusts dynamically as pool-wait observations arrive.
func newPressureController(maxLimit int) *PressureController {
	initial := pressureInitialLimit
	if int32(maxLimit) < initial {
		initial = int32(maxLimit)
	}
	pc := &PressureController{
		maxLimit:     int32(maxLimit),
		highMark:     parsePressureFloat("GNH_PRESSURE_HIGH_MARK_MS", pressureHighMarkDefaultMs),
		lowMark:      parsePressureFloat("GNH_PRESSURE_LOW_MARK_MS", pressureLowMarkDefaultMs),
		stepDown:     pressureStepDown,
		stepUp:       pressureStepUp,
		minLimit:     pressureMinLimit,
		cooldownDown: pressureCooldownDown,
		cooldownUp:   pressureCooldownUp,
	}
	pc.limit.Store(initial)
	return pc
}

// EffectiveLimit returns the current pressure-adjusted concurrency ceiling.
// Safe to call from multiple goroutines concurrently.
func (pc *PressureController) EffectiveLimit() int32 {
	return pc.limit.Load()
}

// Record adds a pool_wait observation (cumulative milliseconds for one
// transaction) and adjusts the effective limit when thresholds are crossed.
// Safe to call concurrently.
func (pc *PressureController) Record(poolWaitMs float64) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pc.samples == 0 {
		pc.ema = poolWaitMs
	} else {
		pc.ema = pressureEMAAlpha*poolWaitMs + (1.0-pressureEMAAlpha)*pc.ema
	}
	pc.samples++

	if pc.samples < pressureWarmupSamples {
		return
	}
	pc.maybeAdjust()
}

// EMA returns the current smoothed pool-wait estimate in milliseconds.
func (pc *PressureController) EMA() float64 {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.ema
}

// maybeAdjust checks whether a threshold has been crossed and fires an
// adjustment if the relevant cooldown has elapsed. Must be called with mu held.
func (pc *PressureController) maybeAdjust() {
	current := pc.limit.Load()

	switch {
	case pc.ema > pc.highMark && current > pc.minLimit:
		if time.Since(pc.lastScaleDown) < pc.cooldownDown {
			return
		}
		newLimit := max(current-pc.stepDown, pc.minLimit)
		pc.limit.Store(newLimit)
		pc.lastScaleDown = time.Now()
		log.Warn().
			Float64("pool_wait_ema_ms", pc.ema).
			Int32("limit_before", current).
			Int32("limit_after", newLimit).
			Int32("limit_ceiling", pc.maxLimit).
			Msg("DB pressure high — reducing queue concurrency")

	case pc.ema < pc.lowMark && current < pc.maxLimit:
		if time.Since(pc.lastScaleUp) < pc.cooldownUp {
			return
		}
		newLimit := min(current+pc.stepUp, pc.maxLimit)
		pc.limit.Store(newLimit)
		pc.lastScaleUp = time.Now()
		log.Info().
			Float64("pool_wait_ema_ms", pc.ema).
			Int32("limit_before", current).
			Int32("limit_after", newLimit).
			Int32("limit_ceiling", pc.maxLimit).
			Msg("DB pressure eased — restoring queue concurrency")
	}
}

func parsePressureFloat(key string, fallback float64) float64 {
	if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
			return v
		}
	}
	return fallback
}
