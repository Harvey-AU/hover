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

func TestBatchManagerShutdownDrainCanEmptyOverflowWhenBatchAlreadyFull(t *testing.T) {
	bm := &BatchManager{
		overflow: map[string]*TaskUpdate{
			"task-1": {Task: &Task{ID: "task-1"}},
			"task-2": {Task: &Task{ID: "task-2"}},
			"task-3": {Task: &Task{ID: "task-3"}},
		},
	}
	batch := make([]*TaskUpdate, 0, MaxBatchSize+len(bm.overflow))
	for i := 0; i < MaxBatchSize; i++ {
		batch = append(batch, &TaskUpdate{Task: &Task{ID: "existing"}})
	}

	for {
		limit := MaxBatchSize - len(batch)
		if limit <= 0 {
			limit = MaxBatchSize
		}
		overflowBatch := bm.popOverflowBatch(limit)
		if len(overflowBatch) == 0 {
			break
		}
		batch = append(batch, overflowBatch...)
	}

	if got := len(bm.overflow); got != 0 {
		t.Fatalf("expected overflow to drain completely, got %d entries remaining", got)
	}
	if got := len(batch); got != MaxBatchSize+3 {
		t.Fatalf("expected shutdown batch to include overflow updates, got %d entries", got)
	}
}
