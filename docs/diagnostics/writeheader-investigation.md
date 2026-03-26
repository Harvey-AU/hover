# WriteHeader Duplicate Call Investigation

## Problem Statement

Production logs show repetitive warnings:

```
http: superfluous response.WriteHeader call from
go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp/internal/request
```

This indicates that `response.WriteHeader()` is being called multiple times per
request, which shouldn't happen.

## Diagnostic Instrumentation Added

### Changes to `internal/api/middleware.go`

Enhanced the `responseWrapper` to capture and log duplicate WriteHeader calls:

1. **Added request context tracking**:
   - `requestID` - for correlation with other logs
   - `requestPath` - to identify which routes trigger the issue
   - `requestMethod` - for complete request context

2. **Enhanced WriteHeader logging**:
   - **WARN level**: Logs duplicate WriteHeader attempts with full stack trace
   - **DEBUG level**: Logs all WriteHeader calls for diagnostic purposes

### Log Messages to Monitor

#### Duplicate Call Warning (The smoking gun we're looking for)

```json
{
  "level": "warn",
  "request_id": "...",
  "method": "GET",
  "path": "/",
  "previous_code": 200,
  "attempted_code": 200,
  "stack_trace": "goroutine 123 [running]:\n...",
  "message": "DIAGNOSTIC: Blocked duplicate WriteHeader call"
}
```

The `stack_trace` field will show exactly which code path is calling WriteHeader
the second time.

#### First WriteHeader Call (DEBUG level)

```json
{
  "level": "debug",
  "request_id": "...",
  "method": "GET",
  "path": "/",
  "status_code": 200,
  "message": "DIAGNOSTIC: WriteHeader called"
}
```

## How to Capture Evidence

### Step 1: Enable Debug Logging

Add to your environment:

```bash
LOG_LEVEL=debug
```

Or use the CLI flag:

```bash
./hover --log-level debug
```

### Step 2: Monitor Production Logs

```bash
flyctl logs --app hover | grep -E "DIAGNOSTIC|superfluous"
```

### Step 3: Analyse Stack Traces

When a duplicate call warning appears, the stack trace will show:

1. The originating goroutine
2. The full call chain leading to WriteHeader
3. Which middleware or handler initiated the second call

### Step 4: Identify Patterns

Look for:

- **Route patterns**: Does it only affect certain paths? (e.g., `/`, static
  files)
- **Request types**: GET vs POST, authenticated vs public
- **Common stack frames**: otelhttp, http.ServeFile, CORS preflight, etc.

## Expected Findings

Based on the middleware stack order:

```
observability.WrapHandler (otelhttp)    ← Outermost
  ↓
CORSMiddleware
  ↓
CrossOriginProtectionMiddleware
  ↓
SecurityHeadersMiddleware
  ↓
RequestIDMiddleware
  ↓
LoggingMiddleware (responseWrapper)      ← Our instrumentation
  ↓
Handler code
```

### Hypothesis 1: Static File Handlers

`http.ServeFile` internally calls WriteHeader. If otelhttp's wrapper tries to
ensure WriteHeader was called, we might see duplicates.

**Evidence to look for**:

- Path: `/`, `/dashboard`, `/test-*.html`
- Stack trace includes: `http.ServeFile` or `http.serveContent`

### Hypothesis 2: CORS Preflight

CORS middleware explicitly calls `WriteHeader(200)` for OPTIONS requests.

**Evidence to look for**:

- Method: `OPTIONS`
- Path: Any
- Stack trace includes: `CORSMiddleware`

### Hypothesis 3: otelhttp Internal Logic

The otelhttp wrapper might call WriteHeader to ensure a status is set before the
request completes.

**Evidence to look for**:

- Stack trace includes: `otelhttp.NewHandler` or `otelhttp.Handler.ServeHTTP`
- Happens across all request types

## Next Steps After Capturing Evidence

### If Static Files Are the Culprit

Option 1: Exclude static file routes from otelhttp instrumentation Option 2: Use
a separate handler for static files without middleware stack Option 3: Create a
custom static file handler that plays nicely with otelhttp

### If CORS Preflight Is the Issue

Ensure CORS middleware returns immediately after WriteHeader, preventing further
processing:

```go
if r.Method == http.MethodOptions {
    w.WriteHeader(http.StatusOK)
    return  // ← Already present, but verify it's working
}
```

### If otelhttp Is the Issue

Check otelhttp configuration options:

- Filter certain routes from instrumentation
- Configure span options differently
- Update to latest otelhttp version with fixes

## Isolation Tests

### Test 1: Disable otelhttp

```go
// In cmd/app/main.go, comment out:
// handler = observability.WrapHandler(handler, obsProviders)
```

If warnings disappear → otelhttp interaction confirmed

### Test 2: Disable Static File Handlers

Temporarily return 404 for all static paths to see if warnings stop

### Test 3: Minimal Middleware Stack

Strip down to just LoggingMiddleware + bare handlers to isolate the issue

## Cleanup

Once the root cause is identified and fixed, the diagnostic logging can be:

1. **Downgraded**: Change WARN to DEBUG for blocked duplicates
2. **Removed**: Remove the debug logging entirely
3. **Kept**: Keep as a permanent safeguard against regressions

The instrumentation has zero performance impact when LOG_LEVEL != debug.
