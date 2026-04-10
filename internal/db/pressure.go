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
// Override via GNH_PRESSURE_HIGH_MARK_MS and GNH_PRESSURE_LOW_MARK_MS.
const (
	pressureHighMarkDefaultMs = 300.0           // EMA above this → shed load
	pressureLowMarkDefaultMs  = 50.0            // EMA below this → restore capacity
	pressureEMAAlpha          = 0.15            // smoothing factor (lower = smoother)
	pressureStep              = int32(5)        // slots to add/remove per adjustment
	pressureMinLimit          = int32(10)       // never drop below this
	pressureCooldown          = 15 * time.Second // min gap between adjustments
	pressureWarmupSamples     = 5               // samples before acting
)

// PressureController adaptively adjusts the queue semaphore's effective limit
// based on observed pool_wait_total per transaction.
//
// Signal: every completed Execute / ExecuteWithContext call reports its
// cumulative pool_wait_total. An exponential moving average of those samples
// is compared against highMark / lowMark:
//
//   - EMA > highMark → reduce effective limit by step (floor: minLimit)
//   - EMA < lowMark  → restore effective limit by step (ceiling: maxLimit)
//
// Adjustments are rate-limited by cooldown to prevent thrashing.
type PressureController struct {
	mu         sync.Mutex
	ema        float64
	samples    int
	lastAdjust time.Time

	// limit is read on every hot-path call to ensurePoolCapacity, so it must
	// be accessed atomically. Writes happen at most once per cooldown period.
	limit    atomic.Int32
	maxLimit int32

	// tuning
	highMark float64
	lowMark  float64
	step     int32
	minLimit int32
	cooldown time.Duration
}

// newPressureController creates a controller whose effective limit starts at
// maxLimit and is adjusted dynamically as pool-wait observations arrive.
func newPressureController(maxLimit int) *PressureController {
	pc := &PressureController{
		maxLimit: int32(maxLimit),
		highMark: parsePressureFloat("GNH_PRESSURE_HIGH_MARK_MS", pressureHighMarkDefaultMs),
		lowMark:  parsePressureFloat("GNH_PRESSURE_LOW_MARK_MS", pressureLowMarkDefaultMs),
		step:     pressureStep,
		minLimit: pressureMinLimit,
		cooldown: pressureCooldown,
	}
	pc.limit.Store(int32(maxLimit))
	return pc
}

// EffectiveLimit returns the current pressure-adjusted concurrency ceiling.
// This is safe to call from multiple goroutines concurrently.
func (pc *PressureController) EffectiveLimit() int32 {
	return pc.limit.Load()
}

// Record adds a pool_wait observation (cumulative milliseconds for one
// transaction) and adjusts the effective limit when thresholds are crossed.
// It is safe to call concurrently.
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
// adjustment if the cooldown period has elapsed. Must be called with mu held.
func (pc *PressureController) maybeAdjust() {
	if time.Since(pc.lastAdjust) < pc.cooldown {
		return
	}

	current := pc.limit.Load()

	switch {
	case pc.ema > pc.highMark && current > pc.minLimit:
		newLimit := max(current-pc.step, pc.minLimit)
		pc.limit.Store(newLimit)
		pc.lastAdjust = time.Now()
		log.Warn().
			Float64("pool_wait_ema_ms", pc.ema).
			Int32("limit_before", current).
			Int32("limit_after", newLimit).
			Int32("limit_ceiling", pc.maxLimit).
			Msg("DB pressure high — reducing queue concurrency")

	case pc.ema < pc.lowMark && current < pc.maxLimit:
		newLimit := min(current+pc.step, pc.maxLimit)
		pc.limit.Store(newLimit)
		pc.lastAdjust = time.Now()
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
