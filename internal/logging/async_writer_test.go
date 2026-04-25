package logging

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockingWriter blocks every Write until release is closed.
type blockingWriter struct {
	released chan struct{}
	written  atomic.Uint64
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{released: make(chan struct{})}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	<-b.released
	b.written.Add(1)
	return len(p), nil
}

func (b *blockingWriter) Release() { close(b.released) }

func TestAsyncWriter_WriteIsNonBlocking(t *testing.T) {
	t.Parallel()

	bw := newBlockingWriter()
	aw := NewAsyncWriter(bw, 4) // tiny buffer
	defer aw.Close()

	// First few writes should buffer fine. Subsequent writes (after the
	// underlying blocks) should drop rather than block the caller. We
	// fire many writes in succession and verify none take longer than a
	// generous threshold — proving the caller never blocked on the
	// underlying writer.
	const totalWrites = 100
	start := time.Now()
	for i := 0; i < totalWrites; i++ {
		n, err := aw.Write([]byte("log line\n"))
		if err != nil {
			t.Fatalf("Write returned error: %v", err)
		}
		if n != len("log line\n") {
			t.Fatalf("Write returned partial length %d", n)
		}
	}
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Fatalf("Writes took %v, expected near-instant since underlying is blocked", elapsed)
	}

	// Most writes should have been dropped (only the buffer's worth queued).
	dropped := aw.Dropped()
	if dropped == 0 {
		t.Fatal("expected drops when underlying blocked, got 0")
	}
	if dropped > totalWrites {
		t.Fatalf("dropped %d > total %d, accounting bug", dropped, totalWrites)
	}

	// Releasing the underlying lets the drain goroutine catch up.
	bw.Release()
	// Close flushes the queue and waits for drain to exit.
	aw.Close()

	written := aw.Written()
	if written == 0 {
		t.Fatal("expected some writes to reach the underlying after release")
	}
	if written+dropped < totalWrites {
		t.Fatalf("written(%d)+dropped(%d) = %d < total(%d)", written, dropped, written+dropped, totalWrites)
	}
}

func TestAsyncWriter_DeliversToUnderlying(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var mu sync.Mutex
	w := writeFn(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	})
	aw := NewAsyncWriter(w, 64)

	for i := 0; i < 10; i++ {
		_, _ = aw.Write([]byte("line\n"))
	}
	aw.Close()

	mu.Lock()
	defer mu.Unlock()
	got := buf.String()
	if got != "line\nline\nline\nline\nline\nline\nline\nline\nline\nline\n" {
		t.Fatalf("unexpected delivered output: %q", got)
	}
}

func TestAsyncWriter_CloseIsIdempotent(t *testing.T) {
	t.Parallel()

	aw := NewAsyncWriter(writeFn(func(p []byte) (int, error) { return len(p), nil }), 4)
	aw.Close()
	aw.Close() // must not panic
}

func TestAsyncWriter_CopiesPayload(t *testing.T) {
	t.Parallel()

	var seen [][]byte
	var mu sync.Mutex
	w := writeFn(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		// Copy so a later reuse of p doesn't corrupt our record.
		cp := make([]byte, len(p))
		copy(cp, p)
		seen = append(seen, cp)
		return len(p), nil
	})
	aw := NewAsyncWriter(w, 64)

	// Reuse the same buffer (mimicking slog handler behaviour) — verifying
	// that the async writer copies and isn't subject to the in-place
	// mutation.
	buf := []byte("first ")
	_, _ = aw.Write(buf)
	buf[0] = 'X'
	_, _ = aw.Write([]byte("second"))
	aw.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("expected at least 2 deliveries, got %d", len(seen))
	}
	if string(seen[0]) != "first " {
		t.Fatalf("first delivery mutated: %q", seen[0])
	}
}

// writeFn adapts a function into an io.Writer.
type writeFn func(p []byte) (int, error)

func (f writeFn) Write(p []byte) (int, error) { return f(p) }
