# Grafana — dashboards as code

Grafana Cloud is configured to sync `grafana/dashboards/` into a Grafana folder
via the built-in GitHub integration (Git Sync / Provisioning). Edit the JSON
files here, commit to `main`, and Grafana picks up the change automatically.

## Layout

```
grafana/
  README.md              ← this file
  dashboards/
    hover-overview.json  ← System + crawler overview (Dashboard v2 schema)
```

One JSON file per dashboard. File name becomes the default dashboard slug.
Dashboards use the Grafana v2 schema (`kind: Panel`, `elements`,
`AnnotationQuery`) so they round-trip cleanly with Git Sync. Avoid hand-editing
unless you know the schema; prefer "Export as code → Save to Git" from the
Grafana UI.

## Configuring Git Sync in Grafana Cloud (one-off)

Do this once per stack. You need an admin role on the Grafana Cloud stack and
admin access on the GitHub repo.

Unlike some Grafana integrations, there is **no pre-published "Grafana" GitHub
App** to install on your repo. You either create your own GitHub App
(recommended for durability and fine-grained scopes) or authenticate with a
Personal Access Token (quicker, less durable). Both options are shown below —
pick one.

### Open the wizard

1. Launch into `simonsmallchua.grafana.net`.
2. Left nav → **Administration → General → Provisioning → Configure Git Sync**.
   The wizard has five steps: Connect → Configure repository → Choose what to
   synchronize → Synchronize with external storage → Choose additional settings.

### Step 1 — Connect (choose one path)

#### Path A: Connect with Personal Access Token (quickest)

1. GitHub → your avatar → **Settings → Developer settings → Personal access
   tokens → Fine-grained tokens → Generate new token**.
2. Repository access: **Only select repositories** → pick `hover`.
3. Permissions (Repository permissions):
   - **Contents**: Read and write (needed for "Save to repository" PRs)
   - **Metadata**: Read-only (auto-selected)
   - **Pull requests**: Read and write (for the PR-back flow)
   - **Webhooks**: Read and write (Grafana registers a push webhook)
4. Expiry: 1 year (set a calendar reminder to rotate).
5. Generate → copy the `github_pat_...` token once.
6. Back in Grafana: **Connect with Personal Access Token** → paste → Create
   connection.

#### Path B: Connect with GitHub App (recommended long-term)

1. GitHub → **Settings → Developer settings → GitHub Apps → New GitHub App** (or
   use your org's Developer settings if the repo is in an org).
2. Fill in:
   - GitHub App name: `grafana-sync-simonsmallchua` (must be globally unique)
   - Homepage URL: `https://simonsmallchua.grafana.net`
   - Webhook: **uncheck Active** (Grafana registers its own webhook separately)
   - Repository permissions:
     - **Contents**: Read and write
     - **Metadata**: Read-only
     - **Pull requests**: Read and write
   - Where can this GitHub App be installed: **Only on this account**
3. Create the app → note the **App ID** (shown at the top of the app page).
4. Scroll to "Private keys" → **Generate a private key** → downloads a `.pem`.
5. Left sidebar of the app page → **Install App** → install on `hover` only.
   After install, the URL becomes `.../installations/<Installation ID>` — note
   that number.
6. Back in Grafana: **Connect with GitHub App** → **Connect to a new app** →
   paste App ID, Installation ID, and the full PEM contents (including BEGIN and
   END lines) → Create connection.

### Step 2 — Configure repository

- Owner: your GitHub username/org (e.g. the repo owner of `hover`)
- Repository: `hover`
- Branch: `main`
- Path: `grafana/dashboards`

### Step 3 — Choose what to synchronize

- Sync target: **Dashboards** (not alerts, not the whole instance)
- Scope: **Instance** (pulls every `.json` file at the configured path)
- Target folder: create a new folder called **Hover** so synced dashboards live
  under one folder (not scattered at the top level).

### Step 4 — Synchronize with external storage

- Enable **Read-only** (or "Git is source of truth") — UI edits become a "Save
  to repository" PR instead of silent writes. This is the setting that makes the
  repo authoritative.
- Initial sync direction: **Pull from Git** (the repo already has
  `hover-overview.json`; you don't want Grafana overwriting it with whatever was
  last on the stack).

### Step 5 — Choose additional settings

- Webhook: leave enabled. Grafana registers a GitHub push webhook so merges to
  `main` sync within seconds instead of polling.
- Auto-sync: on.

### Finish and verify

1. **Save / Finish** the wizard. Grafana performs an initial pull (≈1 minute for
   one dashboard). Open the "Hover" folder and confirm `hover-overview` appears.
2. **Test the loop**: open the dashboard in Grafana, make a trivial change (e.g.
   rename a panel), click "Save". Grafana opens a PR back to this repo. Merge
   the PR on `main`; Grafana re-syncs within seconds.

### Troubleshooting

- **"Repository not found"**: the PAT is scoped to the wrong repo, or the GitHub
  App wasn't installed on `hover`.
- **Initial sync wipes local changes**: you picked "Push to Git" instead of
  "Pull from Git" in Step 4. Delete the repository binding and redo.
- **Dashboard schema errors**: the JSON is older Grafana v1 schema. Re-export
  from the UI using "Dashboard v2 / Git sync compatible".

## Editing workflow

Two supported paths. Pick per change:

- **Edit in Grafana UI** → "Save to repository" → PR → merge on `main`. Best for
  visual tweaks (panel layout, colours, thresholds).
- **Edit JSON in this repo** → commit → push. Best for find/replace across many
  panels or when reviewing a PR diff is more useful than the UI.

Either way, `main` is the source of truth. Do not edit dashboards directly on a
Grafana stack with Git Sync in read-only mode — the UI will block it.

## Adding a new dashboard

1. Build it in Grafana UI.
2. Export as v2 JSON (Share → Export → "Dashboard v2 / Git sync compatible").
3. Save the file as `grafana/dashboards/<slug>.json`.
4. Commit on a feature branch, open a PR. Once merged, Grafana syncs it.

## Deploy annotations

Fly deploys emit a global annotation into Grafana on success, tagged with
`deploy`, `service:hover`, `service:hover-worker`, and `env:production`. Filter
any dashboard by the `deploy` tag to correlate chart movements with releases.
Source: `.github/workflows/fly-deploy.yml` (annotation step).
