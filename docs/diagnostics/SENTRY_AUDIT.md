# Sentry Call Site Audit

Generated: 2026-04-11 (partial refresh 2026-04-19 after Redis broker merge —
line numbers in this file are pre-merge and may have shifted; symbols and files
are accurate. Worker-related entries previously in `internal/jobs/worker.go`
have been removed because that file no longer exists; the worker lifecycle now
lives in `cmd/worker/main.go` and `internal/jobs/stream_worker.go` with
automatic slog→Sentry fanout.)

## Summary

| Metric                           | Count |
| -------------------------------- | ----- |
| Files with Sentry calls          | 11    |
| Total call sites                 | 57    |
| CaptureException                 | 27    |
| CaptureMessage                   | 6     |
| WithScope blocks                 | 12    |
| StartSpan                        | 6     |
| Init / Flush                     | 2     |
| Bare CaptureException (no scope) | 20    |
| Dynamic message fragmentation    | 9     |

## Fingerprint Fragmentation Issues

These call sites bake dynamic values into messages or tags, creating unique
Sentry issues per value instead of grouping.

| #   | File                       | Line | Function                 | Dynamic Value                                             | Impact                          |
| --- | -------------------------- | ---- | ------------------------ | --------------------------------------------------------- | ------------------------------- |
| 1   | `cmd/app/main.go`          | 262  | `startHealthMonitoring`  | `totalStuckJobs`, `len(stuckJobs)` in Sprintf             | One issue per count combination |
| 2   | `cmd/app/main.go`          | 344  | `startHealthMonitoring`  | `totalStuckTasks`, `totalAffectedJobs`, `len(stuckTasks)` | One issue per count combination |
| 3   | `internal/db/batch.go`     | 252  | `Flush`                  | `failCount` in Errorf                                     | One issue per failure count     |
| 4   | `internal/db/batch.go`     | 349  | `Flush`                  | `len(batch)` in Errorf                                    | One issue per batch size        |
| 5   | `internal/db/batch.go`     | 371  | `Flush`                  | `skippedCount` in Errorf                                  | One issue per skip count        |
| 6   | `internal/db/batch.go`     | 604  | `flushIndividualUpdates` | `task.ID`, `task.Status` in Errorf                        | One issue per task              |
| 7   | `internal/db/dashboard.go` | 76   | `GetJobStats`            | `organisationID` in Errorf                                | One issue per org               |

## Bare CaptureException Calls (No Scope/Tags/Context)

These calls capture errors with no tags, no context, and no fingerprint — making
them hard to triage in Sentry.

| #   | File                          | Line | Function                 | Error Source                  |
| --- | ----------------------------- | ---- | ------------------------ | ----------------------------- |
| 1   | `cmd/app/main.go`             | 183  | `startHealthMonitoring`  | DB job update query           |
| 2   | `cmd/app/main.go`             | 508  | `main` (goroutine)       | Metrics HTTP server           |
| 3   | `cmd/app/main.go`             | 533  | `main`                   | PostgreSQL close              |
| 4   | `cmd/app/main.go`             | 558  | `main`                   | Queue DB close                |
| 5   | `cmd/app/main.go`             | 767  | `main`                   | Server.Shutdown               |
| 6   | `cmd/app/main.go`             | 818  | `main`                   | Final server error            |
| 7   | `internal/auth/middleware.go` | 103  | `AuthMiddleware`         | Token signature invalid       |
| 8   | `internal/auth/middleware.go` | 108  | `AuthMiddleware`         | JWKS/keyfunc config           |
| 9   | `internal/api/auth.go`        | 132  | `SignupUser`             | CreateUser DB failure         |
| 10  | `internal/api/auth.go`        | 216  | `GetCurrentUser`         | GetOrCreateUser DB failure    |
| 11  | `internal/db/batch.go`        | 252  | `Flush`                  | Poison pill detection         |
| 12  | `internal/db/batch.go`        | 349  | `Flush`                  | DB unavailable on shutdown    |
| 13  | `internal/db/batch.go`        | 371  | `Flush`                  | Shutdown data errors          |
| 14  | `internal/db/batch.go`        | 604  | `flushIndividualUpdates` | Poison pill individual update |
| 15  | `internal/db/dashboard.go`    | 76   | `GetJobStats`            | Dashboard stats query         |
| 16  | `internal/db/queue.go`        | 489  | `executeOnce`            | Begin transaction             |
| 17  | `internal/db/queue.go`        | 521  | `executeOnce`            | Commit transaction            |
| 18  | `internal/db/queue.go`        | 553  | `executeOnceWithContext` | Begin transaction             |
| 19  | `internal/db/queue.go`        | 585  | `executeOnceWithContext` | Commit transaction            |
| 20  | `internal/db/queue.go`        | 762  | `ExecuteMaintenance`     | Maintenance begin             |

## Well-Structured Calls (Reference)

These calls use WithScope with tags and context — good patterns to replicate.

| File                      | Line    | Function             | Tags                         | Message                                         |
| ------------------------- | ------- | -------------------- | ---------------------------- | ----------------------------------------------- |
| `internal/api/admin.go`   | 66–78   | `AdminResetDatabase` | `event_type`, `action`, user | Static: "Admin database reset action"           |
| `internal/api/admin.go`   | 91–104  | `AdminResetDatabase` | `event_type`, `action`, user | Exception with context                          |
| `internal/api/admin.go`   | 117–129 | `AdminResetDatabase` | `event_type`, `action`, user | Static: "Database reset completed successfully" |
| `internal/api/admin.go`   | 189–201 | `AdminResetData`     | `event_type`, `action`, user | Static: "Admin data reset action"               |
| `internal/api/admin.go`   | 214–226 | `AdminResetData`     | `event_type`, `action`, user | Exception with context                          |
| `internal/api/admin.go`   | 240–252 | `AdminResetData`     | `event_type`, `action`, user | Static: "Data reset completed successfully"     |
| `internal/db/pressure.go` | 191–202 | `AdjustConcurrency`  | `event_type`, `state`        | Static: "DB pressure at floor"                  |
| `internal/db/queue.go`    | 882–895 | `ensurePoolCapacity` | `event_type`, `state`        | Static: "DB pool saturated (queuing)"           |

## Span Operations (Tracing)

| File                       | Line | Function           | Span Name                  | Dynamic Tags          |
| -------------------------- | ---- | ------------------ | -------------------------- | --------------------- |
| `internal/db/queue.go`     | 1669 | `CleanupStuckJobs` | `db.cleanup_stuck_jobs`    | None                  |
| `internal/jobs/manager.go` | 397  | `CreateJob`        | `manager.create_job`       | `domain`              |
| `internal/jobs/manager.go` | 484  | `EnqueueJobURLs`   | `manager.enqueue_job_urls` | `job_id`, `url_count` |
| `internal/jobs/manager.go` | 543  | `CancelJob`        | `manager.cancel_job`       | `job_id`              |
| `internal/jobs/manager.go` | 615  | `GetJob`           | `jobs.get_job`             | `job_id`              |
| `internal/jobs/manager.go` | 703  | `GetJobStatus`     | `manager.get_job_status`   | `job_id`              |
| `internal/jobs/manager.go` | 1063 | `processSitemap`   | `manager.process_sitemap`  | `job_id`, `domain`    |

## Init Configuration

**Files:** `cmd/app/main.go`, `cmd/worker/main.go`

Both entrypoints now initialise Sentry through the `logging` package fanout:

```go
sentry.Init(sentry.ClientOptions{
    Dsn:              config.SentryDSN,
    Environment:      config.Env,
    TracesSampleRate: 0.1 (prod) / 1.0 (dev),
    AttachStacktrace: true,
    BeforeSend:       logging.BeforeSend, // normalises dynamic values
})
```

**Still to consider:** `Release` tag, custom fingerprint defaults beyond what
`logging.BeforeSend` provides.
