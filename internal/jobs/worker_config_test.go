package jobs

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestMaxWorkersFromEnv_UsesStagingFallbackWhenUnset(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	t.Setenv("GNH_MAX_WORKERS", "")

	if got := maxWorkersFromEnv(); got != maxWorkersStaging {
		t.Fatalf("maxWorkersFromEnv() = %d, want %d", got, maxWorkersStaging)
	}
}

func TestMaxWorkersFromEnv_UsesEnvOverrideInStaging(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	t.Setenv("GNH_MAX_WORKERS", "130")

	if got := maxWorkersFromEnv(); got != 130 {
		t.Fatalf("maxWorkersFromEnv() = %d, want 130", got)
	}
}

func TestMaxWorkersFromEnv_UsesProductionFallbackWhenUnset(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("GNH_MAX_WORKERS", "")

	if got := maxWorkersFromEnv(); got != maxWorkersProduction {
		t.Fatalf("maxWorkersFromEnv() = %d, want %d", got, maxWorkersProduction)
	}
}

func TestMaxWorkersFromEnv_InvalidOverrideFallsBackToEnvironmentDefault(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	t.Setenv("GNH_MAX_WORKERS", "invalid")

	if got := maxWorkersFromEnv(); got != maxWorkersStaging {
		t.Fatalf("maxWorkersFromEnv() = %d, want %d", got, maxWorkersStaging)
	}
}

func TestMaxWorkersFromEnv_InvalidOverrideProductionFallsBackToEnvironmentDefault(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("GNH_MAX_WORKERS", "invalid")

	if got := maxWorkersFromEnv(); got != maxWorkersProduction {
		t.Fatalf("maxWorkersFromEnv() = %d, want %d", got, maxWorkersProduction)
	}
}

func TestDisableRuntimeScaleDownFromEnv_DefaultsToFalse(t *testing.T) {
	t.Setenv("GNH_DISABLE_RUNTIME_SCALE_DOWN", "")

	if got := disableRuntimeScaleDownFromEnv(); got {
		t.Fatal("disableRuntimeScaleDownFromEnv() = true, want false")
	}
}

func TestDisableRuntimeScaleDownFromEnv_UsesParsedBool(t *testing.T) {
	t.Setenv("GNH_DISABLE_RUNTIME_SCALE_DOWN", "true")

	if got := disableRuntimeScaleDownFromEnv(); !got {
		t.Fatal("disableRuntimeScaleDownFromEnv() = false, want true")
	}
}

func TestDisableRuntimeScaleDownFromEnv_InvalidFallsBackToFalse(t *testing.T) {
	t.Setenv("GNH_DISABLE_RUNTIME_SCALE_DOWN", "invalid")

	if got := disableRuntimeScaleDownFromEnv(); got {
		t.Fatal("disableRuntimeScaleDownFromEnv() = true, want false")
	}
}

func TestRemoveJob_SkipsScaleDownWhenDisabled(t *testing.T) {
	const jobID = "job-1"
	jobPerformance := &JobPerformance{CurrentBoost: 3}
	jobInfo := &JobInfo{}
	failureState := &jobFailureState{}
	runningTasks := new(atomic.Int64)

	wp := &WorkerPool{
		dbQueue:                   &MockDbQueue{},
		jobs:                      map[string]bool{jobID: true},
		jobPerformance:            map[string]*JobPerformance{jobID: jobPerformance},
		jobInfoCache:              map[string]*JobInfo{jobID: jobInfo},
		jobFailureCounters:        map[string]*jobFailureState{jobID: failureState},
		runningTasksInMem:         map[string]*atomic.Int64{jobID: runningTasks},
		runningTasksIncrPending:   map[string]int{},
		currentWorkers:            40,
		baseWorkerCount:           20,
		maxWorkers:                130,
		disableRuntimeScaleDown:   true,
		concurrencyBlockDebugLast: make(map[string]time.Time),
	}

	wp.runningTasksInMem[jobID].Store(5)

	wp.RemoveJob(jobID)

	if got := wp.currentWorkers; got != 40 {
		t.Fatalf("currentWorkers = %d, want 40", got)
	}
}

func TestRemoveJob_ScalesDownWhenEnabled(t *testing.T) {
	const jobID = "job-1"
	jobPerformance := &JobPerformance{CurrentBoost: 3}
	jobInfo := &JobInfo{}
	failureState := &jobFailureState{}
	runningTasks := new(atomic.Int64)

	wp := &WorkerPool{
		dbQueue:                   &MockDbQueue{},
		jobs:                      map[string]bool{jobID: true},
		jobPerformance:            map[string]*JobPerformance{jobID: jobPerformance},
		jobInfoCache:              map[string]*JobInfo{jobID: jobInfo},
		jobFailureCounters:        map[string]*jobFailureState{jobID: failureState},
		runningTasksInMem:         map[string]*atomic.Int64{jobID: runningTasks},
		runningTasksIncrPending:   map[string]int{},
		currentWorkers:            40,
		baseWorkerCount:           20,
		maxWorkers:                130,
		concurrencyBlockDebugLast: make(map[string]time.Time),
	}

	wp.runningTasksInMem[jobID].Store(5)

	wp.RemoveJob(jobID)

	if got := wp.currentWorkers; got != 32 {
		t.Fatalf("currentWorkers = %d, want 32", got)
	}
}

func TestReplaceWorkerSlotLocked_ReplacesExistingSlot(t *testing.T) {
	wp := &WorkerPool{
		workerConcurrency: 3,
		workerSlots: []*workerSlot{
			newWorkerSlot(3),
			newWorkerSlot(3),
		},
	}

	original := wp.workerSlots[1]
	replacement := wp.replaceWorkerSlotLocked(1)

	if replacement == nil {
		t.Fatal("replaceWorkerSlotLocked() returned nil")
	}

	if replacement == original {
		t.Fatal("replaceWorkerSlotLocked() reused the existing slot")
	}

	if cap(replacement.semaphore) != 3 {
		t.Fatalf("replacement semaphore capacity = %d, want 3", cap(replacement.semaphore))
	}
}

func TestShouldExitWorker_RejectsStaleReusedSlot(t *testing.T) {
	wp := &WorkerPool{
		workerConcurrency: 2,
		currentWorkers:    2,
		workerSlots: []*workerSlot{
			newWorkerSlot(2),
			newWorkerSlot(2),
		},
	}

	original := wp.workerSlots[1]
	if wp.shouldExitWorker(1, original) {
		t.Fatal("shouldExitWorker() = true for active slot, want false")
	}

	wp.workersMutex.Lock()
	wp.currentWorkers = 1
	wp.workersMutex.Unlock()

	if !wp.shouldExitWorker(1, original) {
		t.Fatal("shouldExitWorker() = false after scale-down, want true")
	}

	wp.workersMutex.Lock()
	wp.currentWorkers = 2
	replacement := wp.replaceWorkerSlotLocked(1)
	wp.workersMutex.Unlock()

	if replacement == original {
		t.Fatal("replaceWorkerSlotLocked() did not replace the stale slot")
	}

	if !wp.shouldExitWorker(1, original) {
		t.Fatal("shouldExitWorker() = false for stale reused slot, want true")
	}

	if wp.shouldExitWorker(1, replacement) {
		t.Fatal("shouldExitWorker() = true for replacement slot, want false")
	}
}
