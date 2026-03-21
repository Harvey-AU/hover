# Colly Request Diagnostics Plan

This document outlines how to capture full non-body crawl diagnostics for each
task so request and cache behaviour can be inspected later without storing page
content in Postgres.

## 1. Goal

- Capture diagnostics for all request stages involved in a warmed crawl:
  - the primary request
  - up to three probe requests
  - the secondary request
- Preserve the existing summary columns and current behaviour.
- Store the detailed payload for manual inspection later, not for new UI work.

## 2. Request Stages To Capture

For each crawled page, the request flow can include:

1. **Primary request:** the first real page fetch.
2. **Probe requests:** up to three cache-check attempts.
3. **Secondary request:** the second real fetch after cache warming.

The diagnostics payload should keep these stages separate so we can tell which
request saw which headers, timings, and cache state.

## 3. Storage Options

There are two reasonable schema options for the new diagnostics payload.

### Option A - One top-level `JSONB` column

Example:

- `request_diagnostics JSONB`

Pros:

- simplest migration
- simplest write path
- easiest to archive later
- best fit for manual inspection

Cons:

- harder to query individual sections in SQL
- less flexible if we later want per-section indexes or retention rules

### Option B - Multiple `JSONB` columns by section

Example:

- `primary_request_diagnostics JSONB`
- `probe_diagnostics JSONB`
- `secondary_request_diagnostics JSONB`

Pros:

- cleaner separation of request stages
- easier partial reads and future indexing
- easier to prune or archive one section without touching others

Cons:

- slightly more schema and persistence complexity
- more columns on `tasks`

### Recommendation

Because the current goal is to persist diagnostics for later manual inspection,
not to power new analytics or UI filtering, a single top-level `JSONB` column is
not a bad idea. It is the simplest starting point.

If we expect to query probe data separately, index parts of the diagnostics, or
apply different retention rules to primary versus secondary data, multiple JSONB
columns would be the better long-term shape.

For now, the recommended default is:

- one `request_diagnostics JSONB` column
- one internal JSON structure containing `primary`, `probes`, and `secondary`

Keep the existing scalar and JSONB fields as they are today, including:

- `cache_status`
- `response_time`
- `status_code`
- `content_type`
- `content_length`
- `headers`
- `redirect_url`
- timing scalar fields
- `second_response_time`
- `second_cache_status`
- `second_headers`
- `cache_check_attempts`

This keeps the current API, filtering, exports, and task pages stable while the
new diagnostics blob becomes the source of truth for deeper inspection.

## 4. Why The Default Recommendation Uses One JSONB Column

- avoids adding a large number of task columns
- keeps the migration simple and additive
- makes retention and cold-storage easier later
- suits the goal of manual inspection rather than heavy SQL analytics
- still benefits from Postgres compression for repeated header structures

## 5. Proposed JSON Shape

```json
{
  "primary": {
    "request": {
      "method": "GET",
      "url": "",
      "final_url": "",
      "scheme": "",
      "host": "",
      "path": "",
      "query": "",
      "timestamp": 0,
      "source_type": "",
      "source_url": "",
      "provenance": "primary"
    },
    "response": {
      "status_code": 0,
      "content_type": "",
      "content_length": 0,
      "redirect_url": "",
      "warning": "",
      "error": ""
    },
    "headers": {
      "request": {},
      "response": {}
    },
    "timing": {
      "total_ms": 0,
      "dns_lookup_time": 0,
      "tcp_connection_time": 0,
      "tls_handshake_time": 0,
      "ttfb": 0,
      "content_transfer_time": 0
    },
    "cache": {
      "header_source": "",
      "raw_value": "",
      "normalised_status": "",
      "age": "",
      "cache_control": "",
      "vary": "",
      "cache_status": "",
      "cf_cache_status": "",
      "x_cache": "",
      "x_cache_remote": "",
      "x_vercel_cache": "",
      "x_varnish": ""
    }
  },
  "probes": [
    {
      "attempt": 1,
      "request": {
        "method": "HEAD",
        "url": "",
        "timestamp": 0,
        "provenance": "probe"
      },
      "response": {
        "status_code": 0,
        "error": ""
      },
      "cache": {
        "header_source": "",
        "raw_value": "",
        "normalised_status": ""
      },
      "delay_ms": 0
    }
  ],
  "secondary": {
    "request": {
      "method": "GET",
      "url": "",
      "final_url": "",
      "scheme": "",
      "host": "",
      "path": "",
      "query": "",
      "timestamp": 0,
      "provenance": "secondary"
    },
    "response": {
      "status_code": 0,
      "content_type": "",
      "content_length": 0,
      "redirect_url": "",
      "warning": "",
      "error": ""
    },
    "headers": {
      "request": {},
      "response": {}
    },
    "timing": {
      "total_ms": 0,
      "dns_lookup_time": 0,
      "tcp_connection_time": 0,
      "tls_handshake_time": 0,
      "ttfb": 0,
      "content_transfer_time": 0
    },
    "cache": {
      "header_source": "",
      "raw_value": "",
      "normalised_status": "",
      "age": "",
      "cache_control": "",
      "vary": "",
      "cache_status": "",
      "cf_cache_status": "",
      "x_cache": "",
      "x_cache_remote": "",
      "x_vercel_cache": "",
      "x_varnish": ""
    }
  }
}
```

## 6. What To Capture

### Request data

- method
- original URL
- final URL
- scheme
- host
- path
- query
- timestamp
- provenance (`primary`, `probe`, `secondary`)
- source type and source URL where relevant
- request headers sent, where capturable

### Response data

- status code
- content type
- content length
- redirect URL
- warning
- error
- response headers

### Timing data

- total response time
- DNS lookup time
- TCP connection time
- TLS handshake time
- TTFB
- content transfer time

### Cache data

- header source used for interpretation
- raw cache header value
- normalised cache status
- supporting cache headers:
  - `CF-Cache-Status`
  - `Cache-Status`
  - `X-Cache`
  - `X-Cache-Remote`
  - `x-vercel-cache`
  - `X-Varnish`
  - `Age`
  - `Cache-Control`
  - `Vary`

## 7. What Not To Store

Do not store in Postgres:

- full response body
- HTML page content
- body samples used for tech detection

These are the high-cost payloads and are not required for the diagnostics goal.

## 8. Probe Data Strategy

Probe data should be stored, but in a slim structured format.

For each probe attempt, store:

- attempt number
- method
- timestamp
- status code
- error, if any
- delay before the attempt
- raw cache header source
- raw cache header value
- normalised cache status

This preserves the useful transition history without storing a full duplicate of
the main request payload three times.

## 9. Implementation Touchpoints

### Migration

Add one new `JSONB` column to `tasks`.

File:

- `supabase/migrations/<new_migration>.sql`

### Crawler types

Add structured diagnostic types covering:

- top-level request diagnostics blob
- per-request request/response/timing/cache sections
- per-probe diagnostic entries

File:

- `internal/crawler/types.go`

### Crawl capture

Populate the diagnostics structure during:

- first response capture
- cache probe loop
- second request capture

File:

- `internal/crawler/crawler.go`

### Worker persistence

Marshal the diagnostics blob safely to JSON and persist it to the task.

File:

- `internal/jobs/worker.go`

### DB model and writes

Extend the task DB model and queue/batch write paths with the new field.

Files:

- `internal/db/queue.go`
- `internal/db/batch.go`

### Job/task types

Pass the field through internal job/task structs where required.

File:

- `internal/jobs/types.go`

## 10. API And UI Scope

No UI work is required.

No default API exposure is required for the first pass either. The diagnostics
can be written to the database for later manual inspection.

Optional later, if needed:

- expose `request_diagnostics` behind an explicit diagnostics flag or on a task
  detail endpoint only

## 11. Rollout Plan

### Phase 1 - Schema and write path

- add `request_diagnostics JSONB`
- populate it when a task completes
- leave existing task columns untouched

Validation:

- crawls still complete successfully
- diagnostics JSON is stored on completed tasks
- existing task pages and exports behave as before

### Phase 2 - Probe enrichment

- include full structured probe entries
- include method and provenance explicitly

Validation:

- can distinguish `GET` primary and secondary requests from `HEAD` probes
- can inspect cache transitions clearly

### Phase 3 - Retention and cold storage

- keep diagnostics hot in Postgres initially
- later archive or prune old diagnostics if needed

## 12. Risks And Mitigations

- **Larger task rows:** mitigated by excluding page content and using one
  compressed `JSONB` blob.
- **More persistence complexity:** mitigated by centralising the marshaling in
  the worker success path.
- **Potential payload bloat if exposed later:** mitigated by keeping diagnostics
  write-only by default.

## 13. Recommendation

Proceed with:

- one new `JSONB` column on `tasks`
- full diagnostics for the primary request
- slim but structured diagnostics for up to three probes
- full diagnostics for the secondary request
- no page content stored in Postgres
- no UI work
