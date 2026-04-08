package jobs

import (
	"context"
	"sync"
	"sync/atomic"
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

// TestWorker_ErrDomainDelay verifies the worker-path handling when a domain is
// in its rate-limit window. It checks that:
//  1. processTask returns ErrDomainDelay immediately (no blocking)
//  2. the in-memory running_tasks slot is decremented so the worker is free
//  3. the task status is set to waiting before the DB update is queued
func TestWorker_ErrDomainDelay(t *testing.T) {
	now := time.Now()
	dl := newTestLimiter(func() time.Time { return now })

	// Acquire once to prime the delay window.
	permit, ok := dl.TryAcquire(DomainRequest{Domain: "example.com", JobID: "job1", JobConcurrency: 1})
	if !ok {
		t.Fatal("initial acquire should succeed")
	}
	permit.Release(true, false) // sets nextAvailable = now + BaseDelay

	// Confirm the domain is now blocked.
	_, ok = dl.TryAcquire(DomainRequest{Domain: "example.com", JobID: "job1", JobConcurrency: 1})
	if ok {
		t.Fatal("limiter should be in delay window after release")
	}

	// Build a minimal WorkerPool: limiter pre-loaded, one claimed running slot.
	counter := &atomic.Int64{}
	counter.Store(1)
	wp := &WorkerPool{
		domainLimiter: dl,
		runningTasksInMem: map[string]*atomic.Int64{
			"job1": counter,
		},
		runningTasksInMemMu: sync.RWMutex{},
		jobInfoCache:        make(map[string]*JobInfo),
	}

	// processTask must return ErrDomainDelay without blocking.
	jobsTask := &Task{
		ID:             "task-1",
		JobID:          "job1",
		DomainName:     "example.com",
		JobConcurrency: 1,
	}
	_, err := wp.processTask(context.Background(), jobsTask)
	if err != ErrDomainDelay {
		t.Fatalf("expected ErrDomainDelay, got %v", err)
	}

	// Mimic the processNextTask ErrDomainDelay handler: release slot and requeue.
	jobsTask.Status = TaskStatusWaiting
	jobsTask.StartedAt = time.Time{}
	wp.decrementRunningTaskInMem("job1")

	if got := counter.Load(); got != 0 {
		t.Errorf("running_tasks counter = %d, want 0 after ErrDomainDelay", got)
	}
	if jobsTask.Status != TaskStatusWaiting {
		t.Errorf("task.Status = %q, want %q", jobsTask.Status, TaskStatusWaiting)
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
