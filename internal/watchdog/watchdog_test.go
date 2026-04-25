package watchdog

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRun_TripsWhenHeartbeatStallsAndWorkExists(t *testing.T) {
	t.Parallel()

	hb := &Heartbeat{}
	var exitCode atomic.Int32
	exitCode.Store(-1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		Run(ctx, hb, Options{
			StallThreshold: 30 * time.Millisecond,
			CheckInterval:  10 * time.Millisecond,
			GracePeriod:    1 * time.Millisecond,
			HasWork:        func(_ context.Context) bool { return true },
			Logger:         discardLogger(),
			Exit: func(code int) {
				// Exit codes are bounded to small positive
				// integers; the conversion is safe and avoids
				// the gosec G115 false-positive on int->int32.
				if code >= 0 && code <= 255 {
					exitCode.Store(int32(code))
				}
				cancel()
			},
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not trip within timeout")
	}

	if exitCode.Load() != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode.Load())
	}
}

func TestRun_DoesNotTripWhileHeartbeatAdvances(t *testing.T) {
	t.Parallel()

	hb := &Heartbeat{}
	var tripped atomic.Bool

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Tick at 5ms while watchdog stall threshold is 50ms — heartbeat
	// always fresh, so the watchdog must not trip.
	tickerStop := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-tickerStop:
				return
			case <-t.C:
				hb.Tick()
			}
		}
	}()
	defer close(tickerStop)

	go Run(ctx, hb, Options{
		StallThreshold: 50 * time.Millisecond,
		CheckInterval:  10 * time.Millisecond,
		GracePeriod:    1 * time.Millisecond,
		HasWork:        func(_ context.Context) bool { return true },
		Logger:         discardLogger(),
		Exit: func(code int) {
			tripped.Store(true)
		},
	})

	time.Sleep(300 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)

	if tripped.Load() {
		t.Fatal("watchdog tripped despite advancing heartbeat")
	}
}

func TestRun_DoesNotTripDuringGracePeriod(t *testing.T) {
	t.Parallel()

	hb := &Heartbeat{}
	var tripped atomic.Bool

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go Run(ctx, hb, Options{
		StallThreshold: 5 * time.Millisecond,
		CheckInterval:  5 * time.Millisecond,
		GracePeriod:    200 * time.Millisecond,
		HasWork:        func(_ context.Context) bool { return true },
		Logger:         discardLogger(),
		Exit: func(code int) {
			tripped.Store(true)
		},
	})

	// Sleep for less than the grace period — heartbeat is flat, but
	// the watchdog should hold off.
	time.Sleep(100 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)

	if tripped.Load() {
		t.Fatal("watchdog tripped inside grace period")
	}
}

func TestRun_DoesNotTripWhenNoWork(t *testing.T) {
	t.Parallel()

	hb := &Heartbeat{}
	var tripped atomic.Bool

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go Run(ctx, hb, Options{
		StallThreshold: 30 * time.Millisecond,
		CheckInterval:  10 * time.Millisecond,
		GracePeriod:    1 * time.Millisecond,
		HasWork:        func(_ context.Context) bool { return false },
		Logger:         discardLogger(),
		Exit: func(code int) {
			tripped.Store(true)
		},
	})

	time.Sleep(200 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)

	if tripped.Load() {
		t.Fatal("watchdog tripped despite HasWork returning false")
	}
}

func TestHeartbeat_TickAdvances(t *testing.T) {
	t.Parallel()

	hb := &Heartbeat{}
	if hb.Read() != 0 {
		t.Fatalf("expected initial heartbeat 0, got %d", hb.Read())
	}
	hb.Tick()
	hb.Tick()
	if hb.Read() != 2 {
		t.Fatalf("expected heartbeat 2 after two ticks, got %d", hb.Read())
	}
}
