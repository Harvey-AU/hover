package db

import (
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Default tuning constants for the pressure controller.
// High/low marks are overridable via env vars; everything else is hardcoded.
const (
	pressureHighMarkDefaultMs = 500.0            // EMA above this → shed load
	pressureLowMarkDefaultMs  = 100.0            // EMA below this → restore capacity
	pressureEMAAlpha          = 0.15             // smoothing factor (lower = smoother)
	pressureStepDownDefault   = int32(5)         // slots removed per shed adjustment
	pressureStepUp            = int32(3)         // slots added per restore adjustment
	pressureMinLimitDefault   = int32(30)        // never drop below this
	pressureCooldownDown      = 10 * time.Second // min gap between shed adjustments
	pressureCooldownUp        = 30 * time.Second // min gap between restore adjustments
	pressureWarmupSamples     = 5                // samples required before acting
)

// PressureController adaptively adjusts the queue semaphore's effective limit
// based on observed query execution time per transaction.
//
// Signal: every completed Execute / ExecuteWithContext call reports its
// cumulative exec_total (time spent actually running DB queries). An EMA of
// those samples is compared against highMark / lowMark thresholds:
//
//   - EMA > highMark → reduce limit by stepDown every cooldownDown (floor: minLimit)
//   - EMA < lowMark  → restore limit by stepUp every cooldownUp (ceiling: maxLimit)
//   - Between marks  → hold steady
//
// Shedding is faster than restoring by design: react quickly to protect
// Supabase, but open capacity back up cautiously.
//
// The controller starts at the configured initial limit which defaults to the
// lane's hard cap so full throughput is available unless pressure rises.
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

	// OnAdjust is called after each scale-up or scale-down with direction "up" or "down".
	// Must not block. Set once at construction time before any concurrent use.
	OnAdjust func(direction string)
}

// newPressureController creates a controller that starts at the configured
// initial limit and clamps all values to the queue lane's hard cap.
// (clamped to maxLimit) and adjusts dynamically as pool-wait observations arrive.
func newPressureController(maxLimit int) *PressureController {
	// Guard against int32 overflow — maxLimit is always a small pool size in
	// practice, but clamp explicitly to satisfy the linter and be defensive.
	safeMax := int32(math.MaxInt32)
	if maxLimit <= math.MaxInt32 {
		safeMax = int32(maxLimit) //nolint:gosec // G115: bounds-checked immediately above
	}
	highMark := parsePressureFloat("GNH_PRESSURE_HIGH_MARK_MS", pressureHighMarkDefaultMs)
	lowMark := parsePressureFloat("GNH_PRESSURE_LOW_MARK_MS", pressureLowMarkDefaultMs)

	// Ensure a valid deadband. If env vars collapse or invert the band, log and
	// fall back to defaults so the controller behaves predictably.
	if lowMark >= highMark {
		dbLog.Warn("GNH_PRESSURE_LOW_MARK_MS >= GNH_PRESSURE_HIGH_MARK_MS — falling back to defaults",
			"low_mark", lowMark,
			"high_mark", highMark,
			"default_low", pressureLowMarkDefaultMs,
			"default_high", pressureHighMarkDefaultMs)
		lowMark = pressureLowMarkDefaultMs
		highMark = pressureHighMarkDefaultMs
	}

	// If the pool is configured smaller than the default floor, clamp the floor
	// to maxLimit to avoid an unresolvable inconsistency where we can never shed.
	minLimit := parsePressureInt32("GNH_PRESSURE_MIN_LIMIT", pressureMinLimitDefault)
	if safeMax < minLimit {
		dbLog.Warn("DB_QUEUE_MAX_CONCURRENCY smaller than pressure floor — clamping floor to max",
			"max_limit", safeMax,
			"min_limit_default", minLimit)
		minLimit = safeMax
	}
	if minLimit < 1 {
		minLimit = 1
	}

	initial := parsePressureInt32("GNH_PRESSURE_INITIAL_LIMIT", safeMax)
	if initial > safeMax {
		dbLog.Warn("GNH_PRESSURE_INITIAL_LIMIT exceeds queue cap — clamping to max",
			"initial_limit", initial,
			"max_limit", safeMax)
		initial = safeMax
	}
	if initial < minLimit {
		dbLog.Warn("GNH_PRESSURE_INITIAL_LIMIT below pressure floor — clamping to floor",
			"initial_limit", initial,
			"min_limit", minLimit)
		initial = minLimit
	}

	stepDown := parsePressureInt32("GNH_PRESSURE_STEP_DOWN", pressureStepDownDefault)
	if stepDown < 1 {
		dbLog.Warn("GNH_PRESSURE_STEP_DOWN must be positive — using default", "step_down", stepDown)
		stepDown = pressureStepDownDefault
	}

	pc := &PressureController{
		maxLimit:     safeMax,
		highMark:     highMark,
		lowMark:      lowMark,
		stepDown:     stepDown,
		stepUp:       pressureStepUp,
		minLimit:     minLimit,
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

// Record adds a query execution time observation (cumulative milliseconds for
// one transaction) and adjusts the effective limit when thresholds are crossed.
// Safe to call concurrently.
func (pc *PressureController) Record(execMs float64) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pc.samples == 0 {
		pc.ema = execMs
	} else {
		pc.ema = pressureEMAAlpha*execMs + (1.0-pressureEMAAlpha)*pc.ema
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
		if pc.OnAdjust != nil {
			pc.OnAdjust("down")
		}
		dbLog.Warn("DB pressure high — reducing queue concurrency",
			"exec_ema_ms", pc.ema,
			"limit_before", current,
			"limit_after", newLimit,
			"limit_ceiling", pc.maxLimit)
		if newLimit == pc.minLimit {
			dbLog.Error("DB pressure at floor — queue concurrency fully shed, Supabase severely overloaded",
				"exec_ema_ms", pc.ema,
				"ema_high_mark_ms", pc.highMark,
				"concurrency_slots", newLimit,
				"concurrency_floor", pc.minLimit,
				"concurrency_ceiling", pc.maxLimit,
				"event_type", "db_pressure",
				"state", "floor")
		}

	case pc.ema < pc.lowMark && current < pc.maxLimit:
		if time.Since(pc.lastScaleUp) < pc.cooldownUp {
			return
		}
		newLimit := min(current+pc.stepUp, pc.maxLimit)
		pc.limit.Store(newLimit)
		pc.lastScaleUp = time.Now()
		if pc.OnAdjust != nil {
			pc.OnAdjust("up")
		}
		dbLog.Info("DB pressure eased — restoring queue concurrency",
			"exec_ema_ms", pc.ema,
			"limit_before", current,
			"limit_after", newLimit,
			"limit_ceiling", pc.maxLimit)
		if newLimit == pc.maxLimit {
			dbLog.Info("DB pressure cleared — queue concurrency fully restored",
				"exec_ema_ms", pc.ema,
				"ema_low_mark_ms", pc.lowMark,
				"concurrency_slots", newLimit,
				"concurrency_floor", pc.minLimit,
				"concurrency_ceiling", pc.maxLimit)
		}
	}
}

func parsePressureFloat(key string, fallback float64) float64 {
	if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
			return v
		}
		dbLog.Debug("Invalid pressure config value — using default",
			"key", key,
			"value", raw,
			"fallback", fallback)
	}
	return fallback
}

func parsePressureInt32(key string, fallback int32) int32 {
	if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 32)
		if err == nil && v > 0 {
			return int32(v)
		}
		dbLog.Debug("Invalid pressure config value — using default",
			"key", key,
			"value", raw,
			"fallback", fallback)
	}
	return fallback
}
