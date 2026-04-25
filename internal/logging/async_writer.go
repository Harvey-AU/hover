package logging

import (
	"io"
	"sync"
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
	// stop is closed by Close to signal the drain goroutine that no
	// more sends will arrive. Writers also observe stop and treat a
	// closed stop as "drop the payload" instead of attempting to send,
	// so a Write racing with Close cannot panic on send-to-closed.
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
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
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	go aw.drain()
	return aw
}

// Write copies p into the channel and returns. It never blocks: when the
// channel is full or Close has been called, the payload is dropped and
// the dropped counter is incremented. The returned n is always len(p)
// so that slog handlers don't treat a drop as a partial write.
//
// Concurrency-safe with Close: instead of closing the data channel
// (which would race with concurrent Writes), Close signals via the
// stop channel and Write checks stop before attempting to send. Even
// if a Write observes stop as not-yet-closed and Close completes
// during the select, the data channel is never closed, so the send
// either succeeds (drained later by drain on stop) or hits the
// default branch (drop).
func (a *AsyncWriter) Write(p []byte) (int, error) {
	// Reject sends after Close so we don't grow the buffer indefinitely
	// once nobody is going to drain it. The check is best-effort
	// (a concurrent Close could fire after this branch is skipped) but
	// safe — the data channel is never closed.
	select {
	case <-a.stop:
		a.dropped.Add(1)
		return len(p), nil
	default:
	}

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
// full buffer or a closed writer. Exposed for metrics surface.
func (a *AsyncWriter) Dropped() uint64 { return a.dropped.Load() }

// Written returns the cumulative number of log lines successfully
// written to the underlying writer.
func (a *AsyncWriter) Written() uint64 { return a.written.Load() }

// Close stops accepting new writes, drains any already-queued lines to
// the underlying writer, and waits for the drain goroutine to exit.
// Idempotent and safe to call concurrently with Write.
func (a *AsyncWriter) Close() {
	a.closeOnce.Do(func() {
		close(a.stop)
	})
	<-a.done
}

// drain reads from a.ch until stop is signalled and the queue is
// empty, writing each payload to the underlying writer. The data
// channel is never closed, so Writes racing with Close cannot panic.
func (a *AsyncWriter) drain() {
	defer close(a.done)
	for {
		select {
		case line := <-a.ch:
			a.writeLine(line)
		case <-a.stop:
			// Flush whatever is still buffered after stop. Use a
			// non-blocking inner select so we exit promptly once
			// the buffer is empty.
			for {
				select {
				case line := <-a.ch:
					a.writeLine(line)
				default:
					return
				}
			}
		}
	}
}

// writeLine writes one payload to the underlying writer. Errors are
// ignored — there is no safer place to surface them than the writer
// that just failed. The async wrapper's only job is to keep callers
// unblocked.
func (a *AsyncWriter) writeLine(line []byte) {
	if _, err := a.underlying.Write(line); err == nil {
		a.written.Add(1)
	}
}
