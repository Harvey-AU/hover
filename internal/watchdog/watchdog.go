// Package watchdog detects worker process wedges and forcibly exits so
// the platform's restart policy can recover the service.
//
// The pattern is deliberately simple: a heartbeat counter is bumped from
// the hot path (per-task completion in the worker), and a background
// loop verifies the counter has advanced within the configured stall
// window. If the counter has not advanced AND a "should be working"
// predicate returns true, the process logs a high-priority message and
// calls os.Exit(1).
//
// This is the last line of defence against latent Go-side wedges (e.g.
// blocked stdout pipe, mutex deadlock, exhausted resource pool) that no
// amount of in-process recovery can clear. It does not replace fixing
// the underlying cause — it bounds the blast radius while the real fix
// is being developed.
package watchdog

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"
)

// Failure to increase across the stall window combined with active
// workload triggers a forced exit.
type Heartbeat struct {
	beats atomic.Uint64
}

// Cheap and lock-free; safe from hot paths.
func (h *Heartbeat) Tick() { h.beats.Add(1) }

func (h *Heartbeat) Read() uint64 { return h.beats.Load() }

type Options struct {
	// StallThreshold is how long the heartbeat may stay flat before
	// the watchdog considers the worker wedged. Default 90s.
	StallThreshold time.Duration

	// CheckInterval is how often the watchdog samples. Default 15s.
	CheckInterval time.Duration

	// GracePeriod after Run starts before any check fires; protects
	// against trip during slow startup. Default 2 minutes.
	GracePeriod time.Duration

	// HasWork returns true if the worker should be doing something.
	// When false (no jobs alive), heartbeat staleness is ignored.
	// If nil, the watchdog assumes work always exists.
	HasWork func(ctx context.Context) bool

	// Logger receives the pre-exit message; required.
	Logger *slog.Logger

	// Exit is called when a wedge is detected. Tests substitute a
	// non-fatal handler; production leaves it nil so the default
	// (os.Exit(1)) runs.
	Exit func(code int)
}

func Run(ctx context.Context, hb *Heartbeat, opts Options) {
	if hb == nil {
		return
	}
	if opts.StallThreshold <= 0 {
		opts.StallThreshold = 90 * time.Second
	}
	if opts.CheckInterval <= 0 {
		opts.CheckInterval = 15 * time.Second
	}
	if opts.GracePeriod <= 0 {
		opts.GracePeriod = 2 * time.Minute
	}
	if opts.Exit == nil {
		opts.Exit = os.Exit
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	startedAt := time.Now()
	lastBeats := hb.Read()
	lastChange := time.Now()

	timer := time.NewTicker(opts.CheckInterval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-timer.C:
			cur := hb.Read()
			if cur != lastBeats {
				lastBeats = cur
				lastChange = now
				continue
			}

			if now.Sub(startedAt) < opts.GracePeriod {
				continue
			}

			stallFor := now.Sub(lastChange)
			if stallFor < opts.StallThreshold {
				continue
			}

			hasWork := true
			if opts.HasWork != nil {
				// Bounded so the check can't itself wedge the watchdog.
				checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				hasWork = opts.HasWork(checkCtx)
				cancel()
			}
			if !hasWork {
				// Reset so we don't trip immediately when work resumes.
				lastChange = now
				continue
			}

			opts.Logger.Error(
				"worker wedge detected — heartbeat stale with active workload, forcing process exit so platform restarts",
				"stall_for", stallFor.String(),
				"stall_threshold", opts.StallThreshold.String(),
				"heartbeat", cur,
			)
			// Best-effort flush so the message reaches the log shipper
			// before exit.
			_ = os.Stdout.Sync()
			opts.Exit(1)
			return
		}
	}
}
