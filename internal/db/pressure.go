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

// High/low marks are env-overridable; the rest are hardcoded.
const (
	pressureHighMarkDefaultMs = 500.0
	pressureLowMarkDefaultMs  = 100.0
	pressureEMAAlpha          = 0.15
	pressureStepDownDefault   = int32(5)
	pressureStepUp            = int32(3)
	pressureMinLimitDefault   = int32(30)
	pressureCooldownDown      = 10 * time.Second
	pressureCooldownUp        = 30 * time.Second
	pressureWarmupSamples     = 5
)

// Adjusts queue concurrency from per-transaction exec_total via an EMA:
// shed fast (10s cooldown) to protect Supabase, restore slow (30s cooldown).
type PressureController struct {
	mu            sync.Mutex
	ema           float64
	samples       int
	lastScaleDown time.Time
	lastScaleUp   time.Time

	// Read on every ensurePoolCapacity call; written at most once per cooldown.
	limit    atomic.Int32
	maxLimit int32

	highMark     float64
	lowMark      float64
	stepDown     int32
	stepUp       int32
	cooldownDown time.Duration
	cooldownUp   time.Duration
	minLimit     int32

	// Must not block. Set once before concurrent use.
	OnAdjust func(direction string)
}

func newPressureController(maxLimit int) *PressureController {
	safeMax := int32(math.MaxInt32)
	if maxLimit <= math.MaxInt32 {
		safeMax = int32(maxLimit) //nolint:gosec // G115: bounds-checked immediately above
	}
	highMark := parsePressureFloat("GNH_PRESSURE_HIGH_MARK_MS", pressureHighMarkDefaultMs)
	lowMark := parsePressureFloat("GNH_PRESSURE_LOW_MARK_MS", pressureLowMarkDefaultMs)

	// Inverted/collapsed deadband would oscillate or stall — restore defaults.
	if lowMark >= highMark {
		dbLog.Warn("GNH_PRESSURE_LOW_MARK_MS >= GNH_PRESSURE_HIGH_MARK_MS — falling back to defaults",
			"low_mark", lowMark,
			"high_mark", highMark,
			"default_low", pressureLowMarkDefaultMs,
			"default_high", pressureHighMarkDefaultMs)
		lowMark = pressureLowMarkDefaultMs
		highMark = pressureHighMarkDefaultMs
	}

	// floor > cap would prevent any shedding — clamp.
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

func (pc *PressureController) EffectiveLimit() int32 {
	return pc.limit.Load()
}

// Record adds a per-transaction exec_total sample (ms) and adjusts the limit
// when thresholds are crossed. Concurrent-safe.
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

func (pc *PressureController) EMA() float64 {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.ema
}

// Caller must hold pc.mu.
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
