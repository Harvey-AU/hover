# AGENTS.md

This file is the compact instruction source for OpenAI Codex and OpenCode when
project instructions are loaded.

## Hard rules

- Australian English in outputs, docs, comments, and generated text.
- Preserve existing behaviour unless explicitly requested to change it.
- Ask one focused confirmation question only when correctness or safety is
  blocked.
- Never expose or invent secrets, credentials, JWTs, or end-user private data.
- For destructive actions, state the risk before execution.

## Execution defaults

- Use bounded, incremental edits.
- Prefer gofmt/goimports + target checks on Go files.
- Keep commit messages short (about five to six words), no AI attribution.

## Project-specific rules

**Auth redirect contract:** OAuth redirects are centralised in
`web/static/js/auth.js` (`handleSocialLogin`). Deep-link URLs must return to the
exact originating URL. Invite acceptance routes to `/welcome`.

**Dockerfile triple-surface rule:** Every new top-level HTML page requires three
changes — HTTP route in `internal/api/handlers.go`, the page file on disk, and a
`COPY` line in `Dockerfile`. Missing the Dockerfile copy causes a runtime 404.

**Database migrations:** Use `supabase migration new <name>`. Never edit or
rename deployed migrations. Keep migrations additive.

**Local dev auth:** Use `GET /dev/auto-login` to sign in during local
development — no OAuth flow required. The Go server fetches a session
server-side and injects it into `localStorage`, then redirects to `/dashboard`.
Only active when `APP_ENV=development` (returns 404 otherwise). The dev user
(`dev@example.com` / `devpassword`) is re-seeded on every `supabase db reset`.
See `docs/development/DEVELOPMENT.md#local-authentication` for full detail.

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
