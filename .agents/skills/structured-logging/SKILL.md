# Structured Logging

When adding logging to Go code in this project, follow these patterns.

## Use zerolog with contextual fields

```go
log.Info().
    Str("job_id", jobID).
    Str("domain", domain).
    Int("task_count", count).
    Msg("Job started processing")
```

## Level selection

- **Debug**: Noisy instrumentation, only in development (`LOG_LEVEL=debug`)
- **Info**: Expected state changes (job created, worker started, webhook
  received)
- **Warn**: Unexpected but recovered behaviour (retry scheduled, rate limited,
  fallback used)
- **Error**: Failures we can't auto-correct (500 responses, DB write rejected,
  worker panic)

## Key principles

1. One log per meaningful event - avoid logging inside tight loops
2. Always attach errors via `.Err(err)` on Error level
3. Include `request_id` in API handlers for correlation
4. Never log secrets, JWTs, credentials, or user content
5. Log once at the boundary that handles the error - don't double-log

## When to use Sentry

Only capture high-severity or security issues:

- Infrastructure faults preventing user access
- Auth/signup failures
- Database unavailable

Skip Sentry for:

- Transient warnings handled by retries
- Routine validation errors
- Expected rate limiting
