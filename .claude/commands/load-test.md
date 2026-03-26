# Run Load Test

Execute the load test script to create test jobs at regular intervals.

## Prerequisites

1. Get an auth token:

   ```bash
   python3 scripts/auth/cli_auth.py login
   ```

   Or export manually: `export AUTH_TOKEN="your-jwt-token"`

2. Configure (optional):
   ```bash
   export API_URL="https://hover.app.goodnative.co"  # Default: http://localhost:8080
   export BATCH_INTERVAL_MINUTES=30               # Default: 30
   export TEST_DURATION_HOURS=5                   # Default: 5
   export JOBS_PER_BATCH=7                        # Default: 7
   ```

## Run the test

```bash
./scripts/generate-test-jobs.sh
```

## Quick presets

**1-hour quick test:**

```bash
export BATCH_INTERVAL_MINUTES=15
export TEST_DURATION_HOURS=1
export JOBS_PER_BATCH=5
./scripts/generate-test-jobs.sh
```

**Production (gentle):**

```bash
export API_URL="https://hover.app.goodnative.co"
export BATCH_INTERVAL_MINUTES=60
export JOBS_PER_BATCH=3
./scripts/generate-test-jobs.sh
```

## Output

Creates `load_test_jobs.csv` with batch, domain, job_id, and timestamp.

Press `Ctrl+C` to stop early.
