package db

import "testing"

func TestClassifyEnqueuedTaskDropsOverflowAtMaxPages(t *testing.T) {
	disposition := classifyEnqueuedTask(10, 10, 0, 0, 3)
	if disposition != enqueueTaskDrop {
		t.Fatalf("expected overflow task to be dropped, got %q", disposition)
	}
}

func TestClassifyEnqueuedTaskUsesPendingThenWaitingBeforeDropping(t *testing.T) {
	availableSlots := 2
	maxPages := 4
	currentTaskCount := 0
	pendingCount := 0
	waitingCount := 0

	var got []enqueueTaskDisposition
	for range 5 {
		disposition := classifyEnqueuedTask(maxPages, currentTaskCount, pendingCount, waitingCount, availableSlots)
		got = append(got, disposition)
		switch disposition {
		case enqueueTaskPending:
			pendingCount++
		case enqueueTaskWaiting:
			waitingCount++
		case enqueueTaskDrop:
		}
	}

	want := []enqueueTaskDisposition{
		enqueueTaskPending,
		enqueueTaskPending,
		enqueueTaskWaiting,
		enqueueTaskWaiting,
		enqueueTaskDrop,
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d dispositions, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected disposition %d to be %q, got %q", i, want[i], got[i])
		}
	}
}
