# Worker Pool Architecture

Hover uses a PostgreSQL-backed worker pool for concurrent task processing.

## Core concepts

- **Job**: Collection of URLs from a single domain (pending → running →
  completed/cancelled)
- **Task**: Individual URL processing unit (pending → running →
  completed/failed/skipped)
- **Worker**: Process that claims and executes tasks atomically

## Lock-free task claiming

Uses `FOR UPDATE SKIP LOCKED` to prevent contention:

```sql
SELECT t.id, t.job_id, d.name as domain, p.path
FROM tasks t
JOIN pages p ON t.page_id = p.id
JOIN domains d ON p.domain_id = d.id
WHERE t.job_id = ANY($1)
  AND t.status = 'pending'
ORDER BY t.created_at ASC
LIMIT 1
FOR UPDATE SKIP LOCKED;
```

## Connection pool settings

```go
client.SetMaxOpenConns(45)      // Below Supabase limit of 48
client.SetMaxIdleConns(18)      // 40% idle buffer
client.SetConnMaxLifetime(5 * time.Minute)
client.SetConnMaxIdleTime(2 * time.Minute)
```

## Recovery operations

- Stuck tasks reset on startup (running > 10 minutes → pending)
- Automatic retry with exponential backoff
- Jobs auto-complete when all tasks finished

## Key files

- `internal/jobs/worker.go` - Worker pool and task processing
- `internal/jobs/manager.go` - Job lifecycle management
- `internal/db/queue.go` - Database queue operations
