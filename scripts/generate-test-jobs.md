# Simple Load Test Script

Thin wrapper around the native `hover` CLI binary. See `cmd/hover/` for the
full implementation.

## Quick Start

```bash
# 1. Build the CLI
go build -o hover ./cmd/hover/

# 2. Run (auth handled automatically via browser login)
./hover jobs generate --pr 288 --anon-key <your-key> --interval 30s --jobs 10
```

Or via the legacy wrapper script:

```bash
./scripts/generate-test-jobs.sh pr:288 anon-key:xxx jobs:10 interval:30s
```

## What It Does

1. Authenticates via local loopback OAuth (opens browser, caches session)
2. Shuffles 115 test domains
3. Creates `--jobs` jobs per batch via `POST /v1/jobs`
4. Waits `--interval` between batches
5. Repeats until all domains are covered

## CLI Flags

| Flag            | Default   | Description                           |
| --------------- | --------- | ------------------------------------- |
| `--pr N`        | —         | Target preview app hover-pr-N.fly.dev |
| `--anon-key K`  | built-in  | Supabase publishable key              |
| `--interval D`  | 3m        | Batch interval (e.g. 30s, 2m)        |
| `--jobs N`      | 3         | Jobs per batch                        |
| `--concurrency` | random    | Per-job concurrency (1-50 or random)  |
| `--auth-url U`  | default   | Override Supabase auth base URL       |
| `--api-url U`   | derived   | Override API base URL                 |

## Session Management

Sessions are cached under `~/.config/hover/auth/`. Preview PRs use separate
session files (`session-pr-<N>.json`) so different previews don't collide.
The CLI automatically refreshes expired sessions when a refresh token is
available, and falls back to a new browser login when needed.

## Monitor Jobs

```bash
curl -H "Authorization: Bearer $TOKEN" https://hover-pr-288.fly.dev/v1/jobs | jq '.data[] | {id, domain, status}'
```

## Stop Early

Press `Ctrl+C` — the current batch finishes, then the CLI exits with a summary.
