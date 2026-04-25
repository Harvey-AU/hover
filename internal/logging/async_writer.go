package logging

import (
	"io"
	"sync/atomic"
)

// AsyncWriter is a non-blocking io.Writer wrapper. Each Write copies the
// payload into a bounded channel and returns immediately; a single
// background goroutine drains the channel and writes to the underlying
// writer in order.
//
// When the channel is full (i.e. the underlying writer can't keep up —
// typical cause: the platform log shipper has backpressured the stdout
// pipe), Write drops the payload and increments a counter rather than
// blocking the caller.
//
// This protects every goroutine in the process from logging-induced
// wedges: a goroutine that holds a DB transaction or a mutex can no
// longer get stuck inside slog.Handler.Handle waiting on the OS pipe.
//
// Trade-off: under sustained backpressure some log lines are lost. The
// alternative — blocking — has been observed in production to wedge
// goroutines that hold DB connections, leaving Postgres sessions in
// `idle in transaction (aborted)` for tens of minutes (HOVER-K* class
// of incidents).
type AsyncWriter struct {
	ch         chan []byte
	dropped    atomic.Uint64
	written    atomic.Uint64
	underlying io.Writer
	done       chan struct{}
}

// NewAsyncWriter wraps underlying with a bounded async writer. bufferSize
// is the max number of pending log lines; choose generously enough to
// absorb burst spikes (the common case) but bounded enough to bound
// memory under sustained backpressure. 8192 is a reasonable default.
func NewAsyncWriter(underlying io.Writer, bufferSize int) *AsyncWriter {
	if bufferSize <= 0 {
		bufferSize = 8192
	}
	aw := &AsyncWriter{
		ch:         make(chan []byte, bufferSize),
		underlying: underlying,
		done:       make(chan struct{}),
	}
	go aw.drain()
	return aw
}

// Write copies p into the channel and returns. It never blocks: when the
// channel is full the payload is dropped and the dropped counter is
// incremented. The returned n is always len(p) so that slog handlers
// don't treat a drop as a partial write.
func (a *AsyncWriter) Write(p []byte) (int, error) {
	// slog handlers reuse their internal buffer across records, so we
	// must copy before queuing.
	cp := make([]byte, len(p))
	copy(cp, p)
	select {
	case a.ch <- cp:
		// queued
	default:
		a.dropped.Add(1)
	}
	return len(p), nil
}

// Dropped returns the cumulative number of log lines dropped due to a
// full buffer. Exposed for metrics surface.
func (a *AsyncWriter) Dropped() uint64 { return a.dropped.Load() }

// Written returns the cumulative number of log lines successfully
// written to the underlying writer.
func (a *AsyncWriter) Written() uint64 { return a.written.Load() }

// Close stops the drain goroutine after flushing any queued lines.
// Safe to call multiple times.
func (a *AsyncWriter) Close() {
	select {
	case <-a.done:
		return
	default:
	}
	close(a.ch)
	<-a.done
}

func (a *AsyncWriter) drain() {
	defer close(a.done)
	for line := range a.ch {
		// Errors writing to the underlying writer are ignored: we
		// can't surface them anywhere safer than the writer that
		// just failed. The async wrapper's job is only to keep
		// callers unblocked.
		if _, err := a.underlying.Write(line); err == nil {
			a.written.Add(1)
		}
	}
}
