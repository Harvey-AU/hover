// Deadlock reproduction harness for the jobs/tasks counter triggers.
//
// Drives K goroutines doing concurrent batched UPDATEs against overlapping
// random subsets of tasks across N jobs. Counts:
//
//   - 40P01 (deadlock_detected) errors
//   - 55P03 (lock_not_available) and 57014 (statement_timeout) outliers
//   - per-statement latency (mean / p95 / max)
//   - final counter consistency: SUM(jobs.completed_tasks) vs the actual
//     COUNT(tasks WHERE status='completed').
//
// Designed to be run pre- and post-migration to compare 40P01 rate and
// latency. Pre-migration the row-level triggers must be active; post-
// migration the statement-level triggers (Phase 3) are active.
//
// Usage:
//
//	go run ./scripts/deadlock_repro \
//	    --jobs=8 --tasks-per-job=400 --workers=12 \
//	    --batches=300 --batch-size=40 \
//	    --dsn=postgres://postgres:postgres@127.0.0.1:54322/postgres
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type config struct {
	dsn         string
	jobs        int
	tasksPerJob int
	workers     int
	batches     int
	batchSize   int
	seed        int64
	label       string
}

type stats struct {
	completedStatements atomic.Int64
	deadlocks           atomic.Int64
	lockTimeouts        atomic.Int64
	statementTimeouts   atomic.Int64
	otherErrors         atomic.Int64

	latMu      sync.Mutex
	latencies  []time.Duration
	maxLatency time.Duration
}

func (s *stats) record(d time.Duration) {
	s.latMu.Lock()
	defer s.latMu.Unlock()
	s.latencies = append(s.latencies, d)
	if d > s.maxLatency {
		s.maxLatency = d
	}
}

type report struct {
	Label                 string  `json:"label"`
	Workers               int     `json:"workers"`
	Jobs                  int     `json:"jobs"`
	TasksPerJob           int     `json:"tasks_per_job"`
	BatchesPerWorker      int     `json:"batches_per_worker"`
	BatchSize             int     `json:"batch_size"`
	TotalAttempted        int64   `json:"total_attempted"`
	CompletedStatements   int64   `json:"completed_statements"`
	Deadlocks             int64   `json:"deadlocks_40p01"`
	LockTimeouts          int64   `json:"lock_timeouts_55p03"`
	StatementTimeouts     int64   `json:"statement_timeouts_57014"`
	OtherErrors           int64   `json:"other_errors"`
	WallSeconds           float64 `json:"wall_seconds"`
	StatementsPerSec      float64 `json:"statements_per_sec"`
	MeanLatencyMs         float64 `json:"mean_latency_ms"`
	P95LatencyMs          float64 `json:"p95_latency_ms"`
	MaxLatencyMs          float64 `json:"max_latency_ms"`
	JobsCounterConsistent bool    `json:"jobs_counter_consistent"`
	JobsCounterDelta      int     `json:"jobs_counter_delta"`
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.dsn, "dsn", "postgres://postgres:postgres@127.0.0.1:54322/postgres", "Postgres DSN")
	flag.IntVar(&cfg.jobs, "jobs", 8, "number of jobs to seed")
	flag.IntVar(&cfg.tasksPerJob, "tasks-per-job", 400, "tasks per job")
	flag.IntVar(&cfg.workers, "workers", 12, "concurrent worker goroutines")
	flag.IntVar(&cfg.batches, "batches", 300, "batched UPDATEs per worker")
	flag.IntVar(&cfg.batchSize, "batch-size", 40, "tasks per UPDATE batch")
	flag.Int64Var(&cfg.seed, "seed", 1, "RNG seed for reproducibility")
	flag.StringVar(&cfg.label, "label", "harness", "label for the JSON report")
	flag.Parse()

	if err := run(context.Background(), cfg); err != nil {
		log.Fatalf("harness failed: %v", err)
	}
}

func run(ctx context.Context, cfg config) error {
	pool, err := pgxpool.New(ctx, cfg.dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()
	maxConns := cfg.workers + 2
	if maxConns < 0 || maxConns > 1<<30 {
		maxConns = 16
	}
	pool.Config().MaxConns = int32(maxConns) // #nosec G115 -- maxConns clamped to [0, 1<<30] above

	taskIDs, err := setup(ctx, pool, cfg)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	rng := rand.New(rand.NewSource(cfg.seed)) // #nosec G404 -- deterministic harness seed, not security-sensitive
	rng.Shuffle(len(taskIDs), func(i, j int) { taskIDs[i], taskIDs[j] = taskIDs[j], taskIDs[i] })

	st := &stats{latencies: make([]time.Duration, 0, cfg.workers*cfg.batches)}
	totalAttempted := int64(cfg.workers) * int64(cfg.batches)

	var wg sync.WaitGroup
	wg.Add(cfg.workers)
	start := time.Now()
	for w := 0; w < cfg.workers; w++ {
		go func(workerID int) {
			defer wg.Done()
			workerLoop(ctx, pool, cfg, workerID, taskIDs, st)
		}(w)
	}
	wg.Wait()
	wall := time.Since(start)

	consistent, delta, err := consistencyCheck(ctx, pool)
	if err != nil {
		return fmt.Errorf("consistency check: %w", err)
	}

	rep := report{
		Label:                 cfg.label,
		Workers:               cfg.workers,
		Jobs:                  cfg.jobs,
		TasksPerJob:           cfg.tasksPerJob,
		BatchesPerWorker:      cfg.batches,
		BatchSize:             cfg.batchSize,
		TotalAttempted:        totalAttempted,
		CompletedStatements:   st.completedStatements.Load(),
		Deadlocks:             st.deadlocks.Load(),
		LockTimeouts:          st.lockTimeouts.Load(),
		StatementTimeouts:     st.statementTimeouts.Load(),
		OtherErrors:           st.otherErrors.Load(),
		WallSeconds:           wall.Seconds(),
		StatementsPerSec:      float64(st.completedStatements.Load()) / wall.Seconds(),
		MeanLatencyMs:         meanMs(st.latencies),
		P95LatencyMs:          pMs(st.latencies, 0.95),
		MaxLatencyMs:          float64(st.maxLatency.Microseconds()) / 1000.0,
		JobsCounterConsistent: consistent,
		JobsCounterDelta:      delta,
	}

	out, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(out))
	return nil
}

func workerLoop(ctx context.Context, pool *pgxpool.Pool, cfg config, workerID int, taskIDs []string, st *stats) {
	rng := rand.New(rand.NewSource(cfg.seed + int64(workerID) + 1)) // #nosec G404 -- deterministic harness seed
	for i := 0; i < cfg.batches; i++ {
		batch := pickBatch(rng, taskIDs, cfg.batchSize)
		// Alternate target status to drive both pending->running and
		// running->completed transitions (and a smaller fraction toward
		// failed), which exercises every counter delta path.
		var target string
		switch i % 3 {
		case 0:
			target = "running"
		case 1:
			target = "completed"
		default:
			target = "failed"
		}
		const sql = `UPDATE tasks SET status = $1, completed_at = CASE WHEN $1 IN ('completed','failed','skipped') THEN NOW() ELSE completed_at END WHERE id = ANY($2)`

		t0 := time.Now()
		_, err := pool.Exec(ctx, sql, target, batch)
		dur := time.Since(t0)
		st.record(dur)
		if err != nil {
			classifyError(err, st)
			continue
		}
		st.completedStatements.Add(1)
	}
}

func pickBatch(rng *rand.Rand, taskIDs []string, size int) []string {
	if size > len(taskIDs) {
		size = len(taskIDs)
	}
	// Sample without replacement so each batch is a distinct subset, but
	// across workers they overlap because all workers draw from the same
	// shared taskIDs slice — that overlap is the deadlock surface area.
	idx := rng.Perm(len(taskIDs))[:size]
	out := make([]string, size)
	for i, k := range idx {
		out[i] = taskIDs[k]
	}
	return out
}

func classifyError(err error, st *stats) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40P01":
			st.deadlocks.Add(1)
			return
		case "55P03":
			st.lockTimeouts.Add(1)
			return
		case "57014":
			st.statementTimeouts.Add(1)
			return
		}
	}
	st.otherErrors.Add(1)
}

func setup(ctx context.Context, pool *pgxpool.Pool, cfg config) ([]string, error) {
	if _, err := pool.Exec(ctx, `DELETE FROM tasks WHERE id LIKE 'repro-%'`); err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM jobs WHERE id LIKE 'repro-%'`); err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM pages WHERE host = 'repro.local'`); err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, `INSERT INTO domains (id, name) VALUES (999999, 'repro.local') ON CONFLICT (id) DO NOTHING`); err != nil {
		return nil, err
	}
	// Seed enough pages for tasksPerJob — the (job_id, page_id) unique
	// constraint forces a unique page per task within a job.
	pageIDs := make([]int, cfg.tasksPerJob)
	for i := 0; i < cfg.tasksPerJob; i++ {
		var pid int
		if err := pool.QueryRow(ctx,
			`INSERT INTO pages (domain_id, host, path) VALUES (999999, 'repro.local', $1)
			 ON CONFLICT (domain_id, host, path) DO UPDATE SET host = EXCLUDED.host RETURNING id`,
			fmt.Sprintf("/repro/%d", i),
		).Scan(&pid); err != nil {
			return nil, fmt.Errorf("seed page %d: %w", i, err)
		}
		pageIDs[i] = pid
	}

	var taskIDs []string
	for j := 0; j < cfg.jobs; j++ {
		jobID := fmt.Sprintf("repro-job-%d", j)
		_, err := pool.Exec(ctx, `
			INSERT INTO jobs (id, domain_id, status, progress, total_tasks,
			                  completed_tasks, failed_tasks, skipped_tasks,
			                  pending_tasks, waiting_tasks, running_tasks,
			                  created_at, concurrency, find_links, max_pages)
			VALUES ($1, 999999, 'pending', 0, $2, 0, 0, 0, $2, 0, 0, NOW(), 20, false, 1000)`,
			jobID, cfg.tasksPerJob)
		if err != nil {
			return nil, err
		}
		// Seed tasks in pending status. Using INSERT ... we hit the
		// statement-level INSERT trigger (existing) — fine; it just bumps
		// total_tasks and pending_tasks. Reset counters explicitly afterwards
		// so the harness starts from a known state.
		batch := &pgx.Batch{}
		for t := 0; t < cfg.tasksPerJob; t++ {
			tid := fmt.Sprintf("repro-task-%s-%s", jobID, uuid.NewString())
			batch.Queue(`INSERT INTO tasks (id, job_id, page_id, host, path, status, created_at, retry_count, source_type) VALUES ($1, $2, $3, 'repro.local', $4, 'pending', NOW(), 0, 'sitemap')`, tid, jobID, pageIDs[t], fmt.Sprintf("/repro/%d", t))
			taskIDs = append(taskIDs, tid)
		}
		br := pool.SendBatch(ctx, batch)
		for t := 0; t < cfg.tasksPerJob; t++ {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return nil, err
			}
		}
		if err := br.Close(); err != nil {
			return nil, err
		}
		// Reset counters to a clean baseline; the INSERT trigger has bumped
		// pending_tasks, total_tasks, sitemap_tasks already.
	}
	return taskIDs, nil
}

func consistencyCheck(ctx context.Context, pool *pgxpool.Pool) (bool, int, error) {
	const sql = `
		SELECT COALESCE(SUM(j.completed_tasks), 0) AS sum_jobs,
		       (SELECT COUNT(*)
		          FROM tasks
		         WHERE id LIKE 'repro-%' AND status = 'completed') AS sum_tasks
		FROM jobs j
		WHERE j.id LIKE 'repro-%'`
	var sumJobs, sumTasks int
	if err := pool.QueryRow(ctx, sql).Scan(&sumJobs, &sumTasks); err != nil {
		return false, 0, err
	}
	return sumJobs == sumTasks, sumJobs - sumTasks, nil
}

func meanMs(xs []time.Duration) float64 {
	if len(xs) == 0 {
		return 0
	}
	var total time.Duration
	for _, x := range xs {
		total += x
	}
	return float64(total.Microseconds()) / float64(len(xs)) / 1000.0
}

func pMs(xs []time.Duration, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := make([]time.Duration, len(xs))
	copy(cp, xs)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := int(float64(len(cp)) * q)
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return float64(cp[idx].Microseconds()) / 1000.0
}

// pin os import; otherwise unused on some platforms.
var _ = os.Stderr
