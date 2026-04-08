package jobs

import (
	"testing"
	"time"
)

func newTestLimiter(nowFn func() time.Time) *DomainLimiter {
	dl := newDomainLimiter(nil)
	dl.now = nowFn
	return dl
}

func TestTryAcquire_DomainAvailable(t *testing.T) {
	now := time.Now()
	dl := newTestLimiter(func() time.Time { return now })

	permit, ok := dl.TryAcquire(DomainRequest{Domain: "example.com", JobID: "job1", JobConcurrency: 5})
	if !ok {
		t.Fatal("expected permit to be acquired")
	}
	if permit == nil {
		t.Fatal("expected non-nil permit")
	}
	permit.Release(true, false)
}

func TestTryAcquire_DomainInDelayWindow(t *testing.T) {
	now := time.Now()
	dl := newTestLimiter(func() time.Time { return now })

	// First acquire sets nextAvailable to now + adaptive delay.
	permit, ok := dl.TryAcquire(DomainRequest{Domain: "example.com", JobID: "job1", JobConcurrency: 5})
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	permit.Release(true, false)

	// Second acquire should fail immediately — domain still in delay window.
	_, ok = dl.TryAcquire(DomainRequest{Domain: "example.com", JobID: "job1", JobConcurrency: 5})
	if ok {
		t.Fatal("second acquire should fail: domain is in delay window")
	}
}

func TestTryAcquire_DomainAvailableAfterDelayElapses(t *testing.T) {
	base := time.Now()
	current := base
	dl := newTestLimiter(func() time.Time { return current })

	// Acquire once — sets nextAvailable to base + 500ms (BaseDelay).
	permit, ok := dl.TryAcquire(DomainRequest{Domain: "example.com", JobID: "job1", JobConcurrency: 5})
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	permit.Release(true, false)

	// Advance time past the delay.
	current = base.Add(dl.cfg.BaseDelay + time.Millisecond)

	permit, ok = dl.TryAcquire(DomainRequest{Domain: "example.com", JobID: "job1", JobConcurrency: 5})
	if !ok {
		t.Fatal("acquire should succeed after delay elapsed")
	}
	permit.Release(true, false)
}

func TestTryAcquire_BackoffUntilBlocks(t *testing.T) {
	now := time.Now()
	dl := newTestLimiter(func() time.Time { return now })

	// Trigger a rate-limit to set backoffUntil.
	permit, ok := dl.TryAcquire(DomainRequest{Domain: "example.com", JobID: "job1", JobConcurrency: 5})
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	permit.Release(false, true) // rate-limited — sets backoffUntil = now + adaptiveDelay

	// Immediate re-acquire should fail due to backoffUntil.
	_, ok = dl.TryAcquire(DomainRequest{Domain: "example.com", JobID: "job1", JobConcurrency: 5})
	if ok {
		t.Fatal("acquire should fail: backoffUntil is in the future")
	}
}

func TestTryAcquire_EmptyDomain(t *testing.T) {
	dl := newTestLimiter(time.Now)

	permit, ok := dl.TryAcquire(DomainRequest{Domain: "", JobID: "job1"})
	if !ok {
		t.Fatal("empty domain should always succeed")
	}
	if permit == nil {
		t.Fatal("expected non-nil permit for empty domain")
	}
}

func TestTryAcquire_MultipleCallersSecondFails(t *testing.T) {
	now := time.Now()
	dl := newTestLimiter(func() time.Time { return now })

	p1, ok := dl.TryAcquire(DomainRequest{Domain: "example.com", JobID: "job1", JobConcurrency: 5})
	if !ok {
		t.Fatal("first caller should acquire")
	}

	// Second caller at same time — delay window not elapsed.
	_, ok = dl.TryAcquire(DomainRequest{Domain: "example.com", JobID: "job2", JobConcurrency: 5})
	if ok {
		t.Fatal("second caller should fail: domain in delay window")
	}

	p1.Release(true, false)
}
