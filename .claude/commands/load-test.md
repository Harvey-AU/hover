# Run Load Test

Execute the load test via the native hover CLI.

## Prerequisites

1. Build the CLI:

   ```bash
   go build -o hover ./cmd/hover/
   ```

2. Run your first command — the CLI handles auth automatically:

   ```bash
   ./hover jobs generate --pr 288 --anon-key <your-anon-key> --interval 30s --jobs 10
   ```

   On first run (or if your session has expired), the CLI opens your browser for
   Supabase OAuth and caches the session for reuse.

## Quick presets

**Quick test against a preview PR:**

```bash
./hover jobs generate --pr 288 --anon-key <key> --interval 30s --jobs 5 --concurrency 3
```

**Production (gentle):**

```bash
./hover jobs generate --interval 5m --jobs 3 --concurrency random
```

## Legacy wrapper

The old shell script still works as a thin wrapper:

```bash
./scripts/generate-test-jobs.sh pr:288 anon-key:xxx jobs:5 interval:30s
```

## Output

The CLI prints batch progress and a throughput summary when complete.
Press `Ctrl+C` to stop early.
