package logging

import (
	"io"
	"sync"
	"sync/atomic"
)

// Drops on full buffer rather than blocking. Blocking the caller has
// been observed to wedge goroutines holding DB transactions, leaving
// Postgres sessions `idle in transaction (aborted)` for tens of minutes
// (HOVER-K* class of incidents).
type AsyncWriter struct {
	ch         chan []byte
	dropped    atomic.Uint64
	written    atomic.Uint64
	underlying io.Writer
	// Closed by Close; Write checks stop before sending so a Write
	// racing with Close cannot panic on send-to-closed (the data
	// channel is never closed).
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// 8192 default absorbs burst spikes while bounding memory under
// sustained backpressure.
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

// Returned n is always len(p) so slog handlers don't treat a drop as a
// partial write.
func (a *AsyncWriter) Write(p []byte) (int, error) {
	select {
	case <-a.stop:
		a.dropped.Add(1)
		return len(p), nil
	default:
	}

	// slog handlers reuse their internal buffer; copy before queuing.
	cp := make([]byte, len(p))
	copy(cp, p)
	select {
	case a.ch <- cp:
	default:
		a.dropped.Add(1)
	}
	return len(p), nil
}

func (a *AsyncWriter) Dropped() uint64 { return a.dropped.Load() }

func (a *AsyncWriter) Written() uint64 { return a.written.Load() }

// Idempotent and safe to call concurrently with Write.
func (a *AsyncWriter) Close() {
	a.closeOnce.Do(func() {
		close(a.stop)
	})
	<-a.done
}

// Data channel is never closed, so Writes racing with Close cannot panic.
func (a *AsyncWriter) drain() {
	defer close(a.done)
	for {
		select {
		case line := <-a.ch:
			a.writeLine(line)
		case <-a.stop:
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

// Errors ignored — no safer place to surface than the writer that
// just failed.
func (a *AsyncWriter) writeLine(line []byte) {
	if _, err := a.underlying.Write(line); err == nil {
		a.written.Add(1)
	}
}
