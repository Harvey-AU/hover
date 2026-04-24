//go:build integration

package broker

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/lib/pq"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outboxTestSetup opens a DB connection, starts miniredis, and wires
// a Scheduler. Skips the test if DATABASE_URL is unset so CI without a
// Postgres-backed preview branch still passes.
func outboxTestSetup(t *testing.T) (*sql.DB, *miniredis.Miniredis, *Scheduler, func()) {
	t.Helper()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL not set, skipping outbox integration test")
	}

	db, err := sql.Open("postgres", databaseURL)
	require.NoError(t, err)

	// Ensure migration has been applied. Surface a clear failure if not.
	var exists bool
	err = db.QueryRowContext(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'task_outbox'
		)
	`).Scan(&exists)
	require.NoError(t, err)
	require.True(t, exists, "task_outbox table missing — run migrations first")

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client := &Client{rdb: rdb}
	scheduler := NewScheduler(client)

	cleanup := func() {
		_ = rdb.Close()
		_ = db.Close()
	}
	return db, mr, scheduler, cleanup
}

// insertOutboxFixture writes a single outbox row directly (bypassing
// EnqueueURLs so the test doesn't need a full jobs/domains fixture).
func insertOutboxFixture(t *testing.T, db *sql.DB, jobID string, runAt time.Time) int64 {
	t.Helper()

	var id int64
	err := db.QueryRowContext(context.Background(), `
		INSERT INTO task_outbox (
			task_id, job_id, page_id, host, path,
			priority, retry_count, source_type, source_url, run_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id
	`,
		uuid.New().String(),
		jobID,
		1,
		"example.com",
		"/",
		1.0,
		0,
		"manual",
		"",
		runAt,
	).Scan(&id)
	require.NoError(t, err)
	return id
}

// cleanupOutboxJob removes any outbox rows left behind by a test.
func cleanupOutboxJob(t *testing.T, db *sql.DB, jobID string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		DELETE FROM task_outbox WHERE job_id = $1
	`, jobID)
	require.NoError(t, err)
}

func TestOutboxSweeper_HappyPath(t *testing.T) {
	db, _, scheduler, cleanup := outboxTestSetup(t)
	defer cleanup()

	jobID := uuid.New().String()
	t.Cleanup(func() { cleanupOutboxJob(t, db, jobID) })

	// Insert a due outbox row.
	id := insertOutboxFixture(t, db, jobID, time.Now().Add(-time.Second))

	sweeper := NewOutboxSweeper(db, scheduler, OutboxSweeperOpts{
		Interval:    100 * time.Millisecond,
		BatchSize:   50,
		BaseBackoff: time.Second,
		MaxBackoff:  time.Minute,
	})

	ctx := context.Background()
	require.NoError(t, sweeper.Tick(ctx))

	// Assert: Redis ZSET has one entry for this job.
	count, err := scheduler.PendingCount(ctx, jobID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "expected exactly one ZSET entry after sweep")

	// Assert: outbox row is gone.
	var remaining int
	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_outbox WHERE id = $1`, id,
	).Scan(&remaining)
	require.NoError(t, err)
	assert.Equal(t, 0, remaining, "expected outbox row to be deleted after successful dispatch")
}

func TestOutboxSweeper_RedisDown_RetriesSucceed(t *testing.T) {
	db, mr, scheduler, cleanup := outboxTestSetup(t)
	defer cleanup()

	jobID := uuid.New().String()
	t.Cleanup(func() { cleanupOutboxJob(t, db, jobID) })

	id := insertOutboxFixture(t, db, jobID, time.Now().Add(-time.Second))

	sweeper := NewOutboxSweeper(db, scheduler, OutboxSweeperOpts{
		Interval:    100 * time.Millisecond,
		BatchSize:   50,
		BaseBackoff: time.Millisecond, // small so the retry window is short
		MaxBackoff:  10 * time.Millisecond,
	})

	ctx := context.Background()

	// Simulate Redis down.
	mr.Close()

	// First tick: expect an error, outbox row should still exist with
	// attempts incremented.
	err := sweeper.Tick(ctx)
	require.Error(t, err, "tick should fail while Redis is down")

	var attempts int
	var count int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT attempts FROM task_outbox WHERE id = $1`, id,
	).Scan(&attempts))
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_outbox WHERE id = $1`, id,
	).Scan(&count))
	assert.Equal(t, 1, count, "outbox row must still exist after Redis failure")
	assert.Equal(t, 1, attempts, "attempts must increment on failure")

	// Wait for backoff to elapse, then bring Redis back up and retry.
	time.Sleep(20 * time.Millisecond)

	newMr := miniredis.RunT(t)
	// Repoint the scheduler's client at the new miniredis instance.
	newRdb := redis.NewClient(&redis.Options{Addr: newMr.Addr()})
	t.Cleanup(func() { _ = newRdb.Close() })
	scheduler.client.rdb = newRdb

	// Second tick: should dispatch successfully.
	require.NoError(t, sweeper.Tick(ctx))

	zcount, err := scheduler.PendingCount(ctx, jobID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), zcount, "expected one ZSET entry after retry succeeded")

	var remaining int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_outbox WHERE id = $1`, id,
	).Scan(&remaining))
	assert.Equal(t, 0, remaining, "outbox row should be deleted after successful retry")
}

// TestOutboxSweeper_ConcurrentClaim verifies that SKIP LOCKED makes
// two sweepers partition rows rather than double-dispatching.
func TestOutboxSweeper_ConcurrentClaim(t *testing.T) {
	db, _, scheduler, cleanup := outboxTestSetup(t)
	defer cleanup()

	jobID := uuid.New().String()
	t.Cleanup(func() { cleanupOutboxJob(t, db, jobID) })

	// Seed 10 due rows.
	const n = 10
	now := time.Now().Add(-time.Second)
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, insertOutboxFixture(t, db, jobID, now))
	}

	// Two sweepers run one tick each concurrently.
	sweeperA := NewOutboxSweeper(db, scheduler, OutboxSweeperOpts{BatchSize: 100})
	sweeperB := NewOutboxSweeper(db, scheduler, OutboxSweeperOpts{BatchSize: 100})

	ctx := context.Background()
	errCh := make(chan error, 2)
	go func() { errCh <- sweeperA.Tick(ctx) }()
	go func() { errCh <- sweeperB.Tick(ctx) }()

	for i := 0; i < 2; i++ {
		require.NoError(t, <-errCh)
	}

	// All rows should be gone.
	var remaining int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_outbox WHERE id = ANY($1)`,
		pq.Array(ids),
	).Scan(&remaining))
	assert.Equal(t, 0, remaining, "all outbox rows should be swept")

	zcount, err := scheduler.PendingCount(ctx, jobID)
	require.NoError(t, err)
	assert.Equal(t, int64(n), zcount, "ZSET should contain n distinct members")
}

// TestOutboxSweeper_DeadLetter verifies that rows exceeding MaxAttempts
// are moved into task_outbox_dead with the failure reason, so the
// oldest-age gauge on task_outbox is bounded by MaxAttempts × MaxBackoff.
func TestOutboxSweeper_DeadLetter(t *testing.T) {
	db, mr, scheduler, cleanup := outboxTestSetup(t)
	defer cleanup()

	jobID := uuid.New().String()
	t.Cleanup(func() { cleanupOutboxJob(t, db, jobID) })
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(),
			`DELETE FROM task_outbox_dead WHERE job_id = $1`, jobID)
	})

	// Seed a row that's already at (MaxAttempts - 1) so the next
	// failed tick trips the dead-letter threshold.
	id := insertOutboxFixture(t, db, jobID, time.Now().Add(-time.Second))
	_, err := db.ExecContext(context.Background(),
		`UPDATE task_outbox SET attempts = $1 WHERE id = $2`, 9, id)
	require.NoError(t, err)

	sweeper := NewOutboxSweeper(db, scheduler, OutboxSweeperOpts{
		BatchSize:   50,
		BaseBackoff: time.Millisecond,
		MaxBackoff:  10 * time.Millisecond,
		MaxAttempts: 10,
	})
	mr.Close() // force ScheduleBatch to fail

	ctx := context.Background()
	err = sweeper.Tick(ctx)
	require.Error(t, err, "tick should report the schedule failure")

	// Row must be gone from task_outbox.
	var remaining int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_outbox WHERE id = $1`, id,
	).Scan(&remaining))
	assert.Equal(t, 0, remaining, "dead-lettered row must leave task_outbox")

	// And landed in task_outbox_dead with an error message.
	var dead int
	var lastErr string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(last_error), '')
		   FROM task_outbox_dead WHERE original_id = $1`, id,
	).Scan(&dead, &lastErr))
	assert.Equal(t, 1, dead, "dead-lettered row must appear in task_outbox_dead")
	assert.NotEmpty(t, lastErr, "last_error must capture the ScheduleBatch failure")
}

// TestOutboxSweeper_HealthyMultiRow verifies the happy-path multi-row
// sweep: with a healthy Redis, every claimed row is dispatched and
// deleted in the same tx, and no spurious attempts bumps occur.
//
// A true per-entry failure path (where ScheduleBatch returns *BatchError)
// is awkward to reproduce against miniredis because ZADDs succeed or
// fail uniformly; that branch is exercised by the unit test for
// ScheduleBatch. This test guards the sweeper's partition logic against
// regressions that would blanket-bump attempts on a successful sweep.
func TestOutboxSweeper_HealthyMultiRow(t *testing.T) {
	db, _, scheduler, cleanup := outboxTestSetup(t)
	defer cleanup()

	jobID := uuid.New().String()
	t.Cleanup(func() { cleanupOutboxJob(t, db, jobID) })

	ids := []int64{
		insertOutboxFixture(t, db, jobID, time.Now().Add(-time.Second)),
		insertOutboxFixture(t, db, jobID, time.Now().Add(-time.Second)),
		insertOutboxFixture(t, db, jobID, time.Now().Add(-time.Second)),
	}

	sweeper := NewOutboxSweeper(db, scheduler, OutboxSweeperOpts{BatchSize: 50})
	ctx := context.Background()
	require.NoError(t, sweeper.Tick(ctx))

	var remaining int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_outbox WHERE id = ANY($1)`,
		pq.Array(ids),
	).Scan(&remaining))
	assert.Equal(t, 0, remaining, "all rows should be dispatched on healthy Redis")
}
