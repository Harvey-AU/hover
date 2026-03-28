# 1Password Secrets Integration Plan

## Overview

Sync secrets from 1Password across local, preview, and production environments.

**Tools:**

- **Local**:
  [flyctl shell plugin](https://developer.1password.com/docs/cli/shell-plugins/flyctl/) -
  biometric auth
- **Fly.io sync**:
  [1password-secrets](https://github.com/significa/1password-secrets) - syncs
  1Password → Fly apps
- **GitHub Actions**:
  [1password/load-secrets-action](https://github.com/1password/load-secrets-action) -
  Service Account for CI

---

## 1. 1Password Structure

### Vault: "Good Native"

| Item Type   | Item Name         | Fields                                                                                                          |
| ----------- | ----------------- | --------------------------------------------------------------------------------------------------------------- |
| Secure Note | `hover:fly`       | `FLY_API_TOKEN`                                                                                                 |
| Secure Note | `hover:runtime`   | `SLACK_CLIENT_SECRET`, `WEBFLOW_CLIENT_SECRET`, `GOOGLE_CLIENT_SECRET`, `LOOPS_API_KEY`, `SENTRY_DSN`, `OTEL_EXPORTER_OTLP_HEADERS` |
| Secure Note | `hover:supabase`  | `DATABASE_URL`, `DATABASE_DIRECT_URL`, `SUPABASE_JWT_SECRET`, `SUPABASE_SERVICE_ROLE_KEY`, `SUPABASE_ACCESS_TOKEN` |
| Secure Note | `hover:codecov`   | `CODECOV_TOKEN`, `CODECOV_STATIC_TOKEN`                                                                         |
| Secure Note | `hover:github`    | `PAT_TOKEN`                                                                                                     |

---

## 2. Local Development

### Flyctl Shell Plugin (biometric auth)

```bash
# Install 1Password CLI
brew install 1password-cli

# Connect CLI to 1Password app (Settings → Developer → CLI)
op signin

# Initialise flyctl plugin
op plugin init fly
# → Import your FLY_API_TOKEN or select existing item
# → Choose "Use automatically when in this directory" for project scope

# Add to shell RC file
echo 'source ~/.config/op/plugins.sh' >> ~/.zshrc
source ~/.zshrc

# Now flyctl uses biometric auth - no plaintext tokens
flyctl status  # Prompts for Touch ID
```

### For app secrets locally (optional)

Local dev uses local Supabase (`postgres:postgres`). For testing production
integrations:

```bash
# Install 1password-secrets
pipx install 1password-secrets

# Create .env from 1Password (one-time or on-demand)
op inject -i .env.1password -o .env.local
```

---

## 3. Fly.io Production Secrets

### Using 1password-secrets CLI

```bash
# Install
pipx install 1password-secrets

# Secrets are spread across hover:runtime and hover:supabase
# in the Good Native vault. See docs/operations/ENV_VARS.md for the
# full field list per item.

# Sync to Fly.io (uses biometric auth)
1password-secrets fly import hover

# Edit secrets (opens in 1Password)
1password-secrets fly edit hover
```

**Benefits:**

- No secrets in shell history
- Single source of truth in 1Password
- Audit trail for all changes

---

## 4. GitHub Actions CI/CD

For automated deployments, use 1Password Service Account (no biometrics
available).

### Setup

1. Create Service Account in 1Password Business console
2. Grant read access to "Good Native" vault
3. Store token as `OP_SERVICE_ACCOUNT_TOKEN` in GitHub Secrets

### Workflow Updates

#### `.github/workflows/fly-deploy.yml`

```yaml
- name: Load secrets from 1Password
  uses: 1password/load-secrets-action@v2
  with:
    export-env: true
  env:
    OP_SERVICE_ACCOUNT_TOKEN: ${{ secrets.OP_SERVICE_ACCOUNT_TOKEN }}
    FLY_API_TOKEN: op://Good Native/hover:fly/FLY_API_TOKEN

- uses: superfly/flyctl-actions/setup-flyctl@master
- run: flyctl deploy --remote-only
```

#### `.github/workflows/test.yml`

```yaml
- name: Load secrets from 1Password
  uses: 1password/load-secrets-action@v2
  with:
    export-env: true
  env:
    OP_SERVICE_ACCOUNT_TOKEN: ${{ secrets.OP_SERVICE_ACCOUNT_TOKEN }}
    CODECOV_TOKEN: op://Good Native/hover:codecov/CODECOV_TOKEN
```

#### `.github/workflows/review-apps.yml`

```yaml
- name: Load secrets from 1Password
  uses: 1password/load-secrets-action@v2
  with:
    export-env: true
  env:
    OP_SERVICE_ACCOUNT_TOKEN: ${{ secrets.OP_SERVICE_ACCOUNT_TOKEN }}
    # CI/CD
    FLY_API_TOKEN: op://Good Native/hover:fly/FLY_API_TOKEN
    SUPABASE_ACCESS_TOKEN: op://Good Native/hover:supabase/SUPABASE_ACCESS_TOKEN
    # SUPABASE_PROJECT_REF is non-secret config in fly.toml / review_apps.toml
    # Runtime secrets for review apps
    SENTRY_DSN: op://Good Native/hover:runtime/SENTRY_DSN
    LOOPS_API_KEY: op://Good Native/hover:runtime/LOOPS_API_KEY
    SLACK_CLIENT_SECRET: op://Good Native/hover:runtime/SLACK_CLIENT_SECRET
    WEBFLOW_CLIENT_SECRET: op://Good Native/hover:runtime/WEBFLOW_CLIENT_SECRET
    GOOGLE_CLIENT_SECRET: op://Good Native/hover:runtime/GOOGLE_CLIENT_SECRET
    SUPABASE_JWT_SECRET: op://Good Native/hover:supabase/SUPABASE_JWT_SECRET
    SUPABASE_SERVICE_ROLE_KEY: op://Good Native/hover:supabase/SUPABASE_SERVICE_ROLE_KEY

# DATABASE_URL comes from Supabase preview branch (per-PR)
- name: Deploy with secrets
  run: |
    flyctl secrets set \
      DATABASE_URL="${{ steps.supabase.outputs.preview_db_url }}" \
      SUPABASE_JWT_SECRET="$SUPABASE_JWT_SECRET" \
      ... \
      --app hover-pr-${{ github.event.pull_request.number }} --stage
```

---

## 5. Preview Environment Isolation

| Secret                    | Source                      | Isolation              |
| ------------------------- | --------------------------- | ---------------------- |
| DATABASE_URL              | Supabase preview branch API | Per-PR                 |
| SUPABASE_JWT_SECRET       | 1Password                   | Shared (project-level) |
| SUPABASE_SERVICE_ROLE_KEY | 1Password                   | Shared (project-level) |
| SENTRY_DSN                | 1Password                   | Shared                 |
| SLACK_CLIENT_SECRET       | 1Password                   | Shared                 |
| WEBFLOW_CLIENT_SECRET     | 1Password                   | Shared                 |
| GOOGLE_CLIENT_SECRET      | 1Password                   | Shared                 |
| LOOPS_API_KEY             | 1Password                   | Shared                 |

> **Note:** OAuth client IDs (`SLACK_CLIENT_ID`, `WEBFLOW_CLIENT_ID`,
> `GOOGLE_CLIENT_ID`) and auth URLs are non-secret config in `fly.toml` /
> `review_apps.toml`, not in 1Password. See `docs/operations/ENV_VARS.md`.

---

## 6. Migration Steps

### Phase 0: Env Var Classification (done — issue #273)

1. Classified every env var into non-secret config, runtime secrets, or CI-only
2. Moved misplaced non-secrets (OAuth client IDs, auth URLs) to `fly.toml` [env]
3. Removed dead config (`GOOGLE_REDIRECT_URI`)
4. Fixed `ALLOW_DB_RESET` in production (was `true`, now `false`)
5. Documented classification in `docs/operations/ENV_VARS.md`

### Phase 1: 1Password Setup

1. Use existing vault "Good Native"
2. Create items per the table in section 1 above
3. Create Service Account `GitHub Actions - Hover` with read-only vault access
4. Add `OP_SERVICE_ACCOUNT_TOKEN` to GitHub Secrets

### Phase 2: Local Dev

1. Install 1Password CLI: `brew install 1password-cli`
2. Setup flyctl plugin: `op plugin init fly`
3. Source plugins: add to `~/.zshrc`
4. Install 1password-secrets: `pipx install 1password-secrets`

### Phase 3: GitHub Actions (done — workflows updated)

1. All 5 workflows now use `1password/load-secrets-action@v2`
2. Test by triggering each workflow after Phase 1 setup is complete

### Phase 4: Cleanup

1. Remove old GitHub Secrets one by one (keep only `OP_SERVICE_ACCOUNT_TOKEN`)
2. Test one removal at a time by triggering the relevant workflow

---

## 7. Secret Rotation

1. Update secret in 1Password
2. For Fly.io: `1password-secrets fly import hover`
3. For CI: Next workflow run picks up new value automatically

---

## Sources

- [flyctl shell plugin](https://developer.1password.com/docs/cli/shell-plugins/flyctl/)
- [1password-secrets CLI](https://github.com/significa/1password-secrets)
- [1Password GitHub Action](https://github.com/1password/load-secrets-action)
- [Fly.io Secrets](https://fly.io/docs/apps/secrets/)
