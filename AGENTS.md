# AGENTS.md

This file is the compact instruction source for OpenAI Codex and OpenCode when
project instructions are loaded.

## Hard rules

- Australian English in outputs, docs, comments, and generated text.
- Preserve existing behaviour unless explicitly requested to change it.
- Ask one focused confirmation question only when correctness or safety is
  blocked.
- Never expose or invent secrets, credentials, JWTs, or end-user private data.
- Ask for explicit confirmation before destructive steps such as schema changes,
  credentials or config changes, or data-impacting actions.
- For destructive actions, state the risk before execution.
- If a safety limit is reached in a tool, pause and continue with the best
  available path.

## Execution defaults

- Use bounded, incremental edits.
- Prefer gofmt/goimports + target checks on Go files.
- Prefer `go test` and targeted checks before broader validation.
- Keep commit messages short (about five to six words), no AI attribution.

## Technical baseline

- Language stack: Go 1.26 backend, Vue-free frontend, Supabase-backed data.
- Run formatting (`gofmt`, `goimports`) on touched Go files.

## Code navigation

- Prefer symbol-aware or structural code navigation for Go code when available.
- Use `rg`/`grep`/`glob` for non-Go files such as YAML, shell scripts, HTML,
  JSON, and config.

## Work approach

- For small tasks, keep read-plan-implement cycles minimal.
- For larger changes, confirm scope, prepare a staged plan, and implement in
  bounded increments.
- Report blockers clearly with concrete risk and proposed mitigation.

## Project-specific rules

**Auth redirect contract:** OAuth redirects are centralised in
`web/static/js/auth.js` (`handleSocialLogin`). Deep-link URLs must return to the
exact originating URL. Invite acceptance routes to `/welcome`. Homepage auth may
route to the default app landing page.

**Dockerfile triple-surface rule:** Every new top-level HTML page requires three
changes — HTTP route in `internal/api/handlers.go`, the page file on disk, and a
`COPY` line in `Dockerfile`. Missing the Dockerfile copy causes a runtime 404 on
Fly.

**Database migrations:** Use `supabase migration new <name>`. Never edit or
rename deployed migrations. Keep migrations additive and avoid destructive
schema changes.

**Local dev auth:** Use `GET /dev/auto-login` to sign in during local
development — no OAuth flow or manual credential entry required. The Go server
fetches a session server-side and injects it into `localStorage`, then redirects
to `/dashboard`. Only active when `APP_ENV=development` (returns 404 otherwise).
After `supabase db reset`, the dev user (`dev@example.com` / `devpassword`) is
re-seeded automatically. See
`docs/development/DEVELOPMENT.md#local-authentication` for full detail.

## Automated review gates

- Treat `scripts/security-check.sh` and Coderabbit as mandatory pre-merge
  checks.
- Do not request or attempt bypasses unless explicitly approved by maintainers.
- Before risky edits, call out likely gate failures and mitigation.

## Source-of-truth docs

- `README.md`
- `CHANGELOG.md`
- `SECURITY.md`
- `docs/architecture/ARCHITECTURE.md`
- `docs/architecture/DATABASE.md`
- `docs/architecture/API.md`
- `docs/development/DEVELOPMENT.md`
- `docs/development/BRANCHING.md`
- `docs/TEST_PLAN.md`

## Skills location (tool-native)

- OpenCode: `.opencode/skills/<skill-name>/SKILL.md`
- Codex: `.agents/skills/<skill-name>/SKILL.md`
- Claude-compatible skill fallback: `.claude/skills/<skill-name>/SKILL.md`

Available review skill in this repo:

- `pr-review` — fetches PR status, CI checks, and CodeRabbit comments via
  `scripts/pr-status-check.sh`, replies to and resolves threads via
  `scripts/pr-comment-reply.sh`, then resolves actionable items with one commit
  per fix. Do not use raw `gh api` commands — use the scripts.

Keep this file as the first fallback, and use skills only for high-leverage
workflows.
