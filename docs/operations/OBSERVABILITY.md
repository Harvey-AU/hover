# Observability

Hover uses OpenTelemetry for distributed tracing and observability, with traces
exported to Grafana Cloud Tempo.

## Overview

**Traces** provide visibility into:

- Complete request journeys with timing breakdowns
- Job processing performance (which URLs are slow, where time is spent)
- Database query performance
- HTTP request latency and failures
- Error locations and stack traces

## Configuration

### Environment Variables

Two environment variables configure OTLP (OpenTelemetry Protocol) trace export
to Grafana Cloud:

#### `OTEL_EXPORTER_OTLP_ENDPOINT`

The full OTLP endpoint URL including the path.

**Format:**

```
https://otlp-gateway-prod-{region}.grafana.net/otlp/v1/traces
```

**Example (AU region):**

```
OTEL_EXPORTER_OTLP_ENDPOINT=https://otlp-gateway-prod-au-southeast-1.grafana.net/otlp/v1/traces
```

**Important:** Must include the full path `/otlp/v1/traces`. The SDK uses
`WithEndpointURL` which expects the complete URL.

#### `OTEL_EXPORTER_OTLP_HEADERS`

Authentication header with Base64-encoded credentials.

**Format:**

```
Authorization=Basic <base64(instanceID:token)>
```

**How to generate:**

1. Get your Grafana Cloud Instance ID (e.g., `1322842`)
2. Create a Grafana Cloud Access Policy Token with `traces:write` scope
3. Encode credentials:
   ```bash
   echo -n "INSTANCE_ID:glc_YOUR_TOKEN" | base64
   ```
4. Set the header:
   ```
   OTEL_EXPORTER_OTLP_HEADERS=Authorization=Basic <base64_output>
   ```

**Example:**

```bash
# If your Instance ID is 1322842 and token is glc_abc123...
echo -n "1322842:glc_abc123..." | base64
# Output: MTMyMjg0MjpnbGNfYWJjMTIzLi4u

# Set in Fly.io:
flyctl secrets set \
  OTEL_EXPORTER_OTLP_HEADERS="Authorization=Basic MTMyMjg0MjpnbGNfYWJjMTIzLi4u" \
  --app hover
```

### Deployment on Fly.io

Set both secrets:

```bash
flyctl secrets set \
  OTEL_EXPORTER_OTLP_ENDPOINT="https://otlp-gateway-prod-au-southeast-1.grafana.net/otlp/v1/traces" \
  OTEL_EXPORTER_OTLP_HEADERS="Authorization=Basic <your_base64_credentials>" \
  --app hover
```

The app will automatically redeploy with the new configuration.

## What Gets Traced

### HTTP Requests

- Method, path, status code, duration
- Request and response headers (sanitised)
- User authentication context
- **Excluded:** Health checks (`/health`) to reduce noise

### Job Processing

- Job creation, queuing, and completion
- Task distribution to workers
- URL warming operations
- Sitemap discovery and parsing

### Database Queries

- Query execution time
- SQL statements (parameterised)
- Connection pool metrics

### External HTTP Calls

- Requests to cache warming targets
- Sitemap fetching
- Response times and status codes

## Using Traces in Grafana Cloud

### Access Traces

1. Log into Grafana Cloud
2. Navigate to **Explore** → **Tempo** (traces)
3. Select your data source

### Common Queries

**Find slow jobs:**

```
service.name = "hover" && duration > 5s
```

**Find errors:**

```
service.name = "hover" && status = error
```

**Search by job ID:**

```
service.name = "hover" && job.id = "abc123"
```

**Find slow URL warming operations:**

```
service.name = "hover" && span.name =~ "WarmURL" && duration > 10s
```

**Database performance:**

```
service.name = "hover" && span.name =~ "database"
```

### Reading a Trace

A trace shows a **waterfall view** of operations:

```
Job Processing (Total: 4m 52s)
├─ Database: Fetch URLs (0.5s)
├─ Worker Pool: Process (4m 50s)
│  ├─ URL 1: example.com/page1 (2s)
│  │  └─ HTTP GET (1.8s)
│  ├─ URL 2: example.com/page2 (180s) ← PROBLEM!
│  │  └─ HTTP GET (timeout)
│  └─ URL 3: example.com/page3 (1s)
└─ Database: Update status (1s)
```

This instantly shows URL 2 is timing out after 3 minutes.

### Real-World Examples

**Example 1: "Why did my job fail?"**

1. Search: `job_id=456`
2. Click the trace
3. See: Sitemap parsing failed at 2.3s with "403 Forbidden"

**Example 2: "Which URLs are slowest?"**

1. Search: `span.name =~ "WarmURL" AND duration > 10s`
2. Results show 5 URLs from same domain all timing out
3. Action: Check if domain has rate limiting

**Example 3: "Is the database slow?"**

1. Search: `span.name =~ "database"`
2. Check average query times
3. 50ms = good, 500ms = investigate indexes

## Trace Context Propagation

Hover propagates trace context using W3C Trace Context standard:

- Incoming requests with `traceparent` headers are automatically linked
- Outgoing HTTP requests include trace context
- Enables distributed tracing across services

## Performance Impact

- **Minimal overhead:** ~1-2% latency increase
- **Sampling:** All requests currently traced (can add sampling if needed)
- **Async export:** Traces batched and sent asynchronously
- **Graceful degradation:** App continues if OTLP endpoint unavailable

## Troubleshooting

### No traces appearing in Grafana

1. **Check secrets are set:**

   ```bash
   flyctl secrets list --app hover | grep OTEL
   ```

   (Values will be masked for security)

2. **Check app logs for OTLP errors:**

   ```bash
   flyctl logs --app hover | grep -i otlp
   ```

   Should see: `INFO: OTLP trace exporter initialised successfully`

3. **Verify endpoint format:**
   - Must end with `/otlp/v1/traces`
   - Region must match your Grafana Cloud instance

4. **Verify authentication:**
   - Must use `Authorization=Basic` (not `Bearer`)
   - Credentials must be Base64-encoded `instanceID:token`

### Common Issues

**404 errors in logs:**

```
traces export: failed to send to https://...: 404 Not Found
```

- **Cause:** Wrong endpoint URL
- **Fix:** Ensure endpoint includes `/otlp/v1/traces`

**401 Unauthorized:**

```
traces export: failed to send to https://...: 401 Unauthorized
```

- **Cause:** Wrong authentication format or invalid token
- **Fix:** Verify Base64 encoding and token permissions (needs `traces:write`)

## Metrics (Future)

Currently Hover exports traces only. Future work may add:

- **Metrics:** Worker pool utilisation, job throughput, error rates
- **Logs:** Structured log export to Grafana Loki

## References

- [OpenTelemetry Documentation](https://opentelemetry.io/docs/)
- [Grafana Cloud OTLP Integration](https://grafana.com/docs/grafana-cloud/send-data/otlp/)
- [W3C Trace Context](https://www.w3.org/TR/trace-context/)
