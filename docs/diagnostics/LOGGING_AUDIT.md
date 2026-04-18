# Zerolog Call Site Audit

Generated: 2026-04-11

## Summary

| Metric                                 | Value    |
| -------------------------------------- | -------- |
| Total log calls                        | ~1,024   |
| Files with log calls                   | 48       |
| Component field usage                  | 0 (none) |
| log.Error with adjacent Sentry capture | 13       |
| log.Error without Sentry capture       | ~357     |

## Level Distribution

| Level | Count | %     |
| ----- | ----- | ----- |
| Error | 370   | 36.1% |
| Warn  | 242   | 23.6% |
| Info  | 222   | 21.7% |
| Debug | 176   | 17.2% |
| Fatal | 12    | 1.2%  |
| Trace | 2     | 0.2%  |

## Files Ranked by Log Density (Top 20)

| File                                 | Total | Info | Debug | Warn | Error | Fatal | Sentry |
| ------------------------------------ | ----- | ---- | ----- | ---- | ----- | ----- | ------ |
| `internal/jobs/worker.go`            | 195   | 40   | 61    | 41   | 51    | 0     | 6      |
| `internal/api/auth_google.go`        | 70    | 12   | 4     | 14   | 40    | 0     | 0      |
| `cmd/app/main.go`                    | 68    | 33   | 1     | 12   | 17    | 5     | 12     |
| `internal/jobs/manager.go`           | 54    | 16   | 5     | 16   | 17    | 0     | 5      |
| `internal/db/queue.go`               | 42    | 2    | 14    | 16   | 10    | 0     | 8      |
| `internal/db/reset_migrations.go`    | 40    | 22   | 0     | 14   | 4     | 0     | 1      |
| `internal/api/google_data_api.go`    | 33    | 11   | 4     | 8    | 10    | 0     | 0      |
| `internal/db/db.go`                  | 32    | 16   | 2     | 11   | 3     | 0     | 0      |
| `internal/api/webflow_sites.go`      | 32    | 4    | 0     | 9    | 19    | 0     | 0      |
| `internal/api/slack.go`              | 32    | 5    | 1     | 3    | 23    | 0     | 0      |
| `internal/db/batch.go`               | 30    | 7    | 6     | 11   | 6     | 0     | 5      |
| `internal/db/google_connections.go`  | 27    | 0    | 3     | 1    | 23    | 0     | 0      |
| `internal/crawler/crawler.go`        | 26    | 0    | 23    | 2    | 1     | 0     | 0      |
| `internal/api/handlers.go`           | 25    | 3    | 2     | 13   | 7     | 0     | 0      |
| `internal/db/schedulers.go`          | 23    | 0    | 0     | 9    | 14    | 0     | 0      |
| `internal/api/auth_webflow.go`       | 22    | 3    | 0     | 6    | 13    | 0     | 0      |
| `internal/crawler/sitemap.go`        | 21    | 0    | 16    | 5    | 0     | 0     | 0      |
| `internal/api/schedulers.go`         | 17    | 0    | 0     | 1    | 16    | 0     | 0      |
| `internal/api/jobs.go`               | 17    | 1    | 1     | 2    | 13    | 0     | 0      |
| `internal/notifications/listener.go` | 16    | 6    | 3     | 6    | 1     | 0     | 0      |

## Common Structured Fields

**String fields (most common):**

- `job_id` (60), `connection_id` (37), `scheduler_id` (35), `user_id` (33),
  `organisation_id` (32), `task_id` (22), `url` (13), `account_id` (10),
  `site_id` (8), `domain` (8), `host` (7)

**Integer fields:** `domain_id` (8), `worker_id` (3), `workers` (3), `count` (3)

**Other:** `.Err()` used throughout, `.Dur()` not found, `.Float64()` used
sparingly for progress tracking.

## Logger Patterns

- **Global logger only** — all calls use `log.Info()`, `log.Error()`, etc. from
  `rs/zerolog/log`. No file-scoped logger variables.
- **Request-context enrichment** — `internal/api/logging.go` provides
  `loggerWithRequest(r)` returning a zerolog.Logger with `request_id`, `method`,
  `path`.
- **No component taxonomy** — zero calls include a `component`, `service`, or
  `subsystem` field.

## Sentry/Log Pairing Patterns

**Adjacent pairs (Sentry capture within 1–2 lines of log.Error):** 13 instances

These are the primary consolidation targets — one unified call should replace
the sentry.CaptureException + log.Error pair.

| File                      | Lines     | Pattern                      |
| ------------------------- | --------- | ---------------------------- |
| `cmd/app/main.go`         | 183–184   | CaptureException → log.Error |
| `cmd/app/main.go`         | 508–509   | CaptureException → log.Error |
| `cmd/app/main.go`         | 767–768   | CaptureException → log.Error |
| `internal/jobs/worker.go` | 754–755   | CaptureException → log.Error |
| `internal/jobs/worker.go` | 774–775   | CaptureException → log.Error |
| `internal/jobs/worker.go` | 780–781   | CaptureException → log.Error |
| `internal/jobs/worker.go` | 1143–1144 | CaptureException → log.Error |
| `internal/db/queue.go`    | 489–490   | CaptureException → log.Error |
| `internal/db/queue.go`    | 521–522   | CaptureException → log.Error |
| `internal/db/queue.go`    | 553–554   | CaptureException → log.Error |
| `internal/db/queue.go`    | 585–586   | CaptureException → log.Error |
| `internal/db/batch.go`    | 349–351   | CaptureException → log.Error |
| `internal/db/batch.go`    | 371–373   | CaptureException → log.Error |

**Orphaned Sentry calls (no adjacent log):** 10 instances — these capture to
Sentry but produce no local log line.

**Error logs without Sentry:** ~357 instances — these log locally but never
reach Sentry. Most are appropriate (expected errors, HTTP 4xx), but some
infrastructure errors should be promoted.

## Files Without Sentry Instrumentation (High Error Volume)

These files have high error log density but zero Sentry calls:

| File                                | Error Calls | Candidate for Sentry?      |
| ----------------------------------- | ----------- | -------------------------- |
| `internal/api/auth_google.go`       | 40          | Selective — auth failures  |
| `internal/api/slack.go`             | 23          | Yes — integration errors   |
| `internal/db/google_connections.go` | 23          | Yes — connection failures  |
| `internal/api/webflow_sites.go`     | 19          | Selective — API errors     |
| `internal/api/schedulers.go`        | 16          | Selective — config errors  |
| `internal/db/schedulers.go`         | 14          | Yes — persistence failures |
| `internal/api/auth_webflow.go`      | 13          | Selective — auth failures  |
| `internal/api/jobs.go`              | 13          | No — client errors         |

## Migration Priority

Based on log volume + Sentry call density:

1. `internal/jobs/worker.go` — 195 log + 6 Sentry
2. `cmd/app/main.go` — 68 log + 12 Sentry
3. `internal/db/queue.go` — 42 log + 8 Sentry
4. `internal/db/batch.go` — 30 log + 5 Sentry
5. `internal/archive/scheduler.go` — log calls, 0 Sentry
6. `internal/db/pressure.go` — 1 Sentry (well-structured)
7. `internal/api/admin.go` — 6 Sentry WithScope blocks
8. `internal/api/*` — update loggerWithRequest
9. `internal/auth/middleware.go` — 2 Sentry calls
10. Remaining packages
