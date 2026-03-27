# Webflow Designer Extension Architecture

## Scope

This extension is a standalone frontend surface that runs inside Webflow
Designer. It does not contain backend business logic.

## Runtime model

1. Webflow loads the extension UI.
2. Extension reads app context from Webflow APIs.
3. Extension calls Hover backend APIs for data and actions.
4. Backend handles authentication, token storage, scheduling, jobs, and
   webhooks.

## Auth model

- Extension initiates auth via popup to BBB-hosted `/extension-auth.html`.
- Popup reuses existing shared auth system in `web/static/js/auth.js`.
- First-time users are created via existing `POST /v1/auth/register` path.
- Token handoff returns to extension using `postMessage` with origin/state
  validation.
- Extension keeps auth token in session scope (`sessionStorage`) rather than
  persistent local storage.

## Repository boundaries

- Extension code: `/webflow-designer-extension-cli`
- Backend/API code: `/internal`, `/cmd`, `/supabase`

The extension should not access database or secrets directly.

## CI boundaries

- Backend workflows ignore extension-only changes
  (`/webflow-designer-extension-cli/**`).
- Extension checks run in dedicated workflow:
  `/.github/workflows/webflow-extension.yml`

## API contract (initial)

Extension should use existing authenticated endpoints:

- `GET /v1/integrations/webflow`
- `GET /v1/integrations/webflow/{connection_id}/sites`
- `PUT /v1/integrations/webflow/sites/{site_id}/schedule`
- `PUT /v1/integrations/webflow/sites/{site_id}/auto-publish`

Future extension features should add endpoints only when needed and keep them
interface-agnostic.
