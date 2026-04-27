// Package watchdog forces process exit when the worker heartbeat
// goes flat with active workload, letting the platform's restart
// policy recover from latent Go-side wedges (blocked pipe, mutex
// deadlock, exhausted resource pool).
package watchdog

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"
)

type Heartbeat struct {
	beats atomic.Uint64
}

// Lock-free; safe from hot paths.
func (h *Heartbeat) Tick()        { h.beats.Add(1) }
func (h *Heartbeat) Read() uint64 { return h.beats.Load() }

type Options struct {
	// Defaults: 90s / 15s / 2m.
	StallThreshold time.Duration
	CheckInterval  time.Duration
	GracePeriod    time.Duration

	// HasWork=nil → assume work always exists. Returning false skips
	// the staleness check (no jobs alive → can't be wedged).
	HasWork func(ctx context.Context) bool

	Logger *slog.Logger
	// Tests substitute a non-fatal handler. Default os.Exit.
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
				// Bound so the check can't wedge the watchdog itself.
				checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				hasWork = opts.HasWork(checkCtx)
				cancel()
			}
			if !hasWork {
				// Reset so we don't trip the moment work resumes.
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
