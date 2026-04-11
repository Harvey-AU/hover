package db

import (
	"testing"
	"time"
)

func TestBatchManagerQueueTaskUpdateDoesNotBlockWhenChannelFull(t *testing.T) {
	bm := &BatchManager{
		updates:  make(chan *TaskUpdate, 1),
		overflow: make(map[string]*TaskUpdate),
	}
	bm.updates <- &TaskUpdate{Task: &Task{ID: "existing"}}

	done := make(chan struct{})
	go func() {
		bm.QueueTaskUpdate(&Task{ID: "task-1", Status: "completed"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("QueueTaskUpdate blocked while the channel was full")
	}

	if got := len(bm.overflow); got != 1 {
		t.Fatalf("expected 1 overflow entry, got %d", got)
	}
	if update := bm.overflow["task-1"]; update == nil || update.Task == nil || update.Task.Status != "completed" {
		t.Fatal("expected overflow buffer to hold the queued task update")
	}
}

func TestBatchManagerQueueTaskUpdateCoalescesOverflowByTaskID(t *testing.T) {
	bm := &BatchManager{
		updates:  make(chan *TaskUpdate, 1),
		overflow: make(map[string]*TaskUpdate),
	}
	bm.updates <- &TaskUpdate{Task: &Task{ID: "existing"}}

	bm.QueueTaskUpdate(&Task{ID: "task-1", Status: "running"})
	bm.QueueTaskUpdate(&Task{ID: "task-1", Status: "completed"})

	if got := len(bm.overflow); got != 1 {
		t.Fatalf("expected overflow to coalesce to 1 entry, got %d", got)
	}
	if update := bm.overflow["task-1"]; update == nil || update.Task == nil || update.Task.Status != "completed" {
		t.Fatal("expected the latest task update to win in overflow")
	}
}

func TestBatchManagerPopOverflowBatchHonoursLimit(t *testing.T) {
	bm := &BatchManager{
		overflow: map[string]*TaskUpdate{
			"task-1": {Task: &Task{ID: "task-1"}},
			"task-2": {Task: &Task{ID: "task-2"}},
			"task-3": {Task: &Task{ID: "task-3"}},
		},
	}

	updates := bm.popOverflowBatch(2)
	if got := len(updates); got != 2 {
		t.Fatalf("expected 2 overflow updates, got %d", got)
	}
	if got := len(bm.overflow); got != 1 {
		t.Fatalf("expected 1 overflow update to remain, got %d", got)
	}
}
