# Environment Variable Classification

Last reviewed: 2026-03-28

This document defines where each environment variable lives and the rule for
choosing the correct layer.

## Classification Rule

| Layer                                   | What belongs here                         | Rotation                             | Examples                                   |
| --------------------------------------- | ----------------------------------------- | ------------------------------------ | ------------------------------------------ |
| `fly.toml` / `review_apps.toml` `[env]` | Non-secret, stable runtime config         | Git commit                           | `APP_ENV`, OAuth client IDs, feature flags |
| Fly secrets (synced from 1Password)     | Runtime secrets the app reads at boot     | `1password-secrets fly import hover` | `DATABASE_URL`, `*_CLIENT_SECRET`          |
| GitHub secrets (from 1Password action)  | CI-only credentials — never reach the app | Next workflow run picks up new value | `FLY_API_TOKEN`, `CODECOV_TOKEN`           |

**Decision test:** If a value would be harmless posted publicly, it belongs in
the TOML `[env]` section. If it grants access to anything, it is a secret.

---

## Full Inventory

### Non-Secret Runtime Config (`fly.toml` / `review_apps.toml` [env])

| Variable                              | Purpose                                                         | Default               |
| ------------------------------------- | --------------------------------------------------------------- | --------------------- |
| `APP_ENV`                             | Environment indicator (`development`/`staging`/`production`)    | —                     |
| `APP_URL`                             | Base application URL for redirects                              | —                     |
| `SETTINGS_URL`                        | Settings page URL                                               | —                     |
| `PORT`                                | HTTP server port                                                | `8080`                |
| `LOG_LEVEL`                           | Log verbosity                                                   | `info`                |
| `ALLOW_DB_RESET`                      | Enable admin reset endpoint (**must be `false` in production**) | `false`               |
| `WORKER_CONCURRENCY`                  | Crawler worker count                                            | `10`                  |
| `DB_QUEUE_MAX_CONCURRENCY`            | Queue connection concurrency                                    | `40`                  |
| `DB_TX_MAX_RETRIES`                   | Transaction retry limit                                         | `5`                   |
| `FLIGHT_RECORDER_ENABLED`             | Go runtime tracing                                              | `false`               |
| `OBSERVABILITY_ENABLED`               | OpenTelemetry + Prometheus                                      | `true`                |
| `GNH_ENABLE_TURNSTILE`                | Cloudflare Turnstile CAPTCHA                                    | `false`               |
| `GNH_HEALTH_PROBE_INTERVAL_SECONDS`   | Worker health probe interval                                    | disabled              |
| `GNH_WORKER_IDLE_THRESHOLD`           | Idle threshold before scale-down                                | disabled              |
| `GNH_WORKER_SCALE_COOLDOWN_SECONDS`   | Minimum time between scale-downs                                | `15`                  |
| `GNH_CRAWLER_MAX_CONCURRENCY`         | Shared crawler parallel request cap                             | `10`                  |
| `GNH_RUNNING_TASK_BATCH_SIZE`         | Batch size for task DB updates                                  | `32`                  |
| `GNH_RUNNING_TASK_FLUSH_INTERVAL_MS`  | Batch flush interval                                            | `1000`                |
| `GNH_RATE_LIMIT_BASE_DELAY_MS`        | Initial crawl request delay                                     | `500`                 |
| `GNH_RATE_LIMIT_MAX_DELAY_SECONDS`    | Maximum adaptive delay                                          | `60`                  |
| `GNH_RATE_LIMIT_SUCCESS_THRESHOLD`    | Successes before delay reduction                                | `5`                   |
| `GNH_RATE_LIMIT_DELAY_STEP_MS`        | Adaptive delay increment                                        | `500`                 |
| `GNH_RATE_LIMIT_MAX_RETRIES`          | Max blocking retries on rate limit                              | `3`                   |
| `GNH_RATE_LIMIT_CANCEL_THRESHOLD`     | Consecutive errors before cancellation                          | `20`                  |
| `GNH_RATE_LIMIT_CANCEL_DELAY_SECONDS` | Delay before auto-cancel                                        | `60`                  |
| `GNH_RATE_LIMIT_CANCEL_ENABLED`       | Enable auto-cancellation                                        | `false`               |
| `GNH_ROBOTS_DELAY_MULTIPLIER`         | Multiplier for robots.txt delays                                | `0.5`                 |
| `GNH_BATCH_CHANNEL_SIZE`              | Batch channel capacity                                          | —                     |
| `GNH_BATCH_MAX_INTERVAL_MS`           | Max batch wait time                                             | `100`                 |
| `GNH_JOB_FAILURE_THRESHOLD`           | Consecutive failures before mark-down                           | —                     |
| `SUPABASE_URL`                        | Supabase project URL                                            | —                     |
| `SUPABASE_AUTH_URL`                   | Supabase auth endpoint                                          | —                     |
| `SUPABASE_FALLBACK_AUTH_URL`          | Fallback auth URL (migration)                                   | —                     |
| `SUPABASE_LEGACY_AUTH_URL`            | Legacy auth URL (backward compat)                               | —                     |
| `SUPABASE_PUBLISHABLE_KEY`            | Public anon key (`sb_publishable_` prefix)                      | —                     |
| `SUPABASE_PROJECT_REF`                | Supabase project identifier (CI uses for branch API)            | —                     |
| `SLACK_CLIENT_ID`                     | Slack OAuth public identifier                                   | —                     |
| `WEBFLOW_CLIENT_ID`                   | Webflow OAuth public identifier                                 | —                     |
| `GOOGLE_CLIENT_ID`                    | Google OAuth public identifier                                  | —                     |
| `WEBFLOW_REDIRECT_URI`                | OAuth callback URL override                                     | defaults to `APP_URL` |
| `SLACK_REDIRECT_URI`                  | Slack OAuth callback URL override                               | —                     |
| `LOOPS_INVITE_TEMPLATE_ID`            | Transactional email template ID                                 | —                     |
| `OTEL_EXPORTER_OTLP_ENDPOINT`         | OpenTelemetry collector URL                                     | —                     |
| `METRICS_ADDR`                        | Prometheus metrics endpoint                                     | —                     |
| `DB_MAX_OPEN_CONNS`                   | Max open DB connections                                         | —                     |
| `DB_MAX_IDLE_CONNS`                   | Max idle DB connections                                         | —                     |
| `DB_POOL_RESERVED_CONNECTIONS`        | Reserved connections for queue                                  | —                     |
| `DB_APP_NAME`                         | Application name in DB connection                               | —                     |
| `FLY_MACHINE_ID`                      | Fly.io machine identifier (set by platform)                     | —                     |

### Runtime Secrets (Fly secrets / 1Password sync)

| Variable                     | Purpose                                | 1Password Item   |
| ---------------------------- | -------------------------------------- | ---------------- |
| `DATABASE_URL`               | Primary PostgreSQL connection (pooled) | `hover-supabase` |
| `DATABASE_DIRECT_URL`        | Direct connection for LISTEN/NOTIFY    | `hover-supabase` |
| `DATABASE_QUEUE_URL`         | Optional separate queue connection     | `hover-supabase` |
| `SUPABASE_JWT_SECRET`        | Auth token signing key                 | `hover-supabase` |
| `SUPABASE_SERVICE_ROLE_KEY`  | Admin API key                          | `hover-supabase` |
| `SLACK_CLIENT_SECRET`        | Slack OAuth credential                 | `hover-runtime`  |
| `WEBFLOW_CLIENT_SECRET`      | Webflow OAuth credential               | `hover-runtime`  |
| `GOOGLE_CLIENT_SECRET`       | Google OAuth credential                | `hover-runtime`  |
| `LOOPS_API_KEY`              | Email service API key                  | `hover-runtime`  |
| `SENTRY_DSN`                 | Error tracking URL (contains auth)     | `hover-runtime`  |
| `OTEL_EXPORTER_OTLP_HEADERS` | Grafana Basic auth header              | `hover-runtime`  |

### CI-Only Secrets (GitHub secrets / 1Password action)

| Variable                   | Purpose                                                            | 1Password Item   |
| -------------------------- | ------------------------------------------------------------------ | ---------------- |
| `FLY_API_TOKEN`            | Fly.io deploy authentication                                       | `hover-fly`      |
| `PAT_TOKEN`                | GitHub release with branch protection                              | `hover-github`   |
| `SUPABASE_ACCESS_TOKEN`    | Supabase CLI authentication                                        | `hover-supabase` |
| `CODECOV_TOKEN`            | Coverage upload token                                              | `hover-codecov`  |
| `CODECOV_STATIC_TOKEN`     | Static analysis coverage token                                     | `hover-codecov`  |
| `GRAFANA_URL`              | Grafana Cloud stack URL for deploy annotations                     | `hover-runtime`  |
| `GRAFANA_SA_TOKEN`         | Grafana service account token for deploy annotations               | `hover-runtime`  |
| `OP_SERVICE_ACCOUNT_TOKEN` | 1Password Service Account (the only GitHub secret after migration) | —                |

---

## Adding a New Environment Variable

1. **Decide the layer** using the decision test above.
2. **Non-secret config:** Add to `fly.toml` `[env]` (and `review_apps.toml` if
   review apps need it). Commit the change.
3. **Runtime secret:** Add the field to the appropriate Secure Note in 1Password
   (`hover-runtime` for app integrations, `hover-supabase` for database/auth),
   then run `1password-secrets fly import hover` to sync.
4. **CI-only secret:** Add the field to the appropriate 1Password item
   (`hover-fly`, `hover-supabase`, `hover-codecov`, or `hover-github`) and
   reference it via `op://Good Native/<item>/<field>` in the workflow.
5. **Update this document** with the new variable.

---

## 1Password Setup

All secrets are stored in a 1Password vault and loaded into CI via a Service
Account. This section documents initial setup — see
`docs/plans/1password-secrets-integration.md` for the full design.

### Vault Structure

The **"Good Native"** vault contains five Secure Notes, grouped by concern:

| Item Name        | Fields                                                                                                                                                                 |
| ---------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `hover-fly`      | `FLY_API_TOKEN`                                                                                                                                                        |
| `hover-runtime`  | `SLACK_CLIENT_SECRET`, `WEBFLOW_CLIENT_SECRET`, `GOOGLE_CLIENT_SECRET`, `LOOPS_API_KEY`, `SENTRY_DSN`, `OTEL_EXPORTER_OTLP_HEADERS`, `GRAFANA_URL`, `GRAFANA_SA_TOKEN` |
| `hover-supabase` | `DATABASE_URL`, `DATABASE_DIRECT_URL`, `SUPABASE_JWT_SECRET`, `SUPABASE_SERVICE_ROLE_KEY`, `SUPABASE_ACCESS_TOKEN`                                                     |
| `hover-codecov`  | `CODECOV_TOKEN`, `CODECOV_STATIC_TOKEN`                                                                                                                                |
| `hover-github`   | `PAT_TOKEN`                                                                                                                                                            |

### Service Account

1. In **1Password Business console** → **Developer** → **Service Accounts**,
   create an account named `GitHub Actions - Hover`.
2. Grant **read-only** access to the **Good Native** vault.
3. Copy the service account token.
4. In GitHub repo settings → **Secrets and variables** → **Actions**, add
   `OP_SERVICE_ACCOUNT_TOKEN` with the token value.

### How Workflows Load Secrets

Each workflow has a `Load secrets from 1Password` step using
`1password/load-secrets-action@v2`. The action reads `op://` URIs and exports
them as environment variables. Example:

```yaml
- name: Load secrets from 1Password
  uses: 1password/load-secrets-action@v2
  with:
    export-env: true
  env:
    OP_SERVICE_ACCOUNT_TOKEN: ${{ secrets.OP_SERVICE_ACCOUNT_TOKEN }}
    FLY_API_TOKEN: op://Good Native/hover-fly/FLY_API_TOKEN
```

### Secret Rotation

1. Update the secret in 1Password.
2. **Fly.io production:** Secrets sync automatically on the next deploy. For
   immediate rotation without a code change, trigger the Fly Deploy workflow
   manually from GitHub Actions.
3. **CI workflows:** The next workflow run picks up the new value automatically.
4. **Fly.io preview apps:** Re-run the PR workflow to reload from 1Password.

### Removing Legacy GitHub Secrets

After confirming all workflows pass with 1Password, remove the legacy GitHub
secrets one by one — **except** `OP_SERVICE_ACCOUNT_TOKEN`, which stays
permanently. Test one removal at a time by triggering the relevant workflow.
