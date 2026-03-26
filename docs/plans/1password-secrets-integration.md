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

### Vault: "Hover"

| Item Type   | Item Name               | Fields                                                                                    |
| ----------- | ----------------------- | ----------------------------------------------------------------------------------------- |
| Login       | `flyctl`                | Token: `<FLY_API_TOKEN>`                                                                  |
| Secure Note | `fly:hover`             | DATABASE_URL, SUPABASE_JWT_SECRET, SENTRY_DSN, SLACK_CLIENT_ID, SLACK_CLIENT_SECRET, etc. |
| Secure Note | `fly:hover-pr-template` | Shared preview secrets (no DATABASE_URL - comes from Supabase)                            |
| Login       | `supabase-management`   | access-token, project-ref                                                                 |
| Login       | `codecov`               | token                                                                                     |

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

# Create Secure Note in 1Password titled: fly:hover
# Add all secrets as fields:
#   DATABASE_URL = postgresql://...
#   SUPABASE_JWT_SECRET = ...
#   SENTRY_DSN = ...
#   etc.

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
2. Grant read access to "Hover" vault
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
    FLY_API_TOKEN: op://Hover/flyctl/token

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
    CODECOV_TOKEN: op://Hover/codecov/token
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
    FLY_API_TOKEN: op://Hover/flyctl/token
    SUPABASE_ACCESS_TOKEN: op://Hover/supabase-management/access-token
    # Shared app secrets
    SENTRY_DSN: op://Hover/fly:hover/SENTRY_DSN
    SLACK_CLIENT_ID: op://Hover/fly:hover/SLACK_CLIENT_ID
    SLACK_CLIENT_SECRET: op://Hover/fly:hover/SLACK_CLIENT_SECRET
    SUPABASE_JWT_SECRET: op://Hover/fly:hover/SUPABASE_JWT_SECRET
    SUPABASE_SERVICE_ROLE_KEY: op://Hover/fly:hover/SUPABASE_SERVICE_ROLE_KEY

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
| SLACK_CLIENT_ID/SECRET    | 1Password                   | Shared                 |

---

## 6. Migration Steps

### Phase 1: 1Password Setup

1. Create vault "Hover"
2. Create `flyctl` Login item with Token field
3. Create `fly:hover` Secure Note with all production secrets
4. Create Service Account for GitHub Actions

### Phase 2: Local Dev

1. Install 1Password CLI: `brew install 1password-cli`
2. Setup flyctl plugin: `op plugin init fly`
3. Source plugins: add to `~/.zshrc`
4. Install 1password-secrets: `pipx install 1password-secrets`

### Phase 3: GitHub Actions

1. Add `OP_SERVICE_ACCOUNT_TOKEN` to GitHub Secrets
2. Update workflow files with 1Password action
3. Test with a PR

### Phase 4: Cleanup

1. Remove old GitHub Secrets (keep only `OP_SERVICE_ACCOUNT_TOKEN`)
2. Update `docs/development/DEVELOPMENT.md`

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
