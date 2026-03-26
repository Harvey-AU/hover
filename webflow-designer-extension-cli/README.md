# Blue Banded Bee Webflow Designer Extension (Smoke Test)

This is a valid Webflow CLI extension project used to smoke-test rendering in
Webflow Designer before wiring OAuth/API calls.

## Developing

```bash
npm run dev
```

The command does four things:

- Updates the local Webflow CLI when a newer allowed version is available
- Watches `src/index.ts` and compiles to `public/index.js`
- Serves the extension via `webflow extension serve`
- Keeps `public/index.js` generated from source (not committed)

## Deploying

```bash
npm run build
```

This prepares a `bundle.zip` for upload in Webflow App settings.

## Smoke-test checklist

1. Run `npm run dev`.
2. In Webflow Designer Apps panel, open your app via development mode.
3. Confirm you see:
   - "Designer Extension Smoke Test"
   - "UI loaded in Webflow Designer."
4. Click "Test Interaction" and confirm click counter increments.

## Backend read test

The panel includes a basic read-only API check:

1. Enter API base URL (for example `https://hover.app.goodnative.co`).
2. Click `Sign in / Create account` to open BBB auth popup and obtain token.
3. Click `Check API`.

Expected behaviour:

- Calls `GET /health` first to confirm reachability.
- Calls `GET /v1/integrations/webflow` and shows connection count.
- If unauthenticated, shows a clear "auth required" state.
- Auth token is stored in `sessionStorage` only (tab/session scoped).

## Popup auth bridge

- Extension opens `GET /extension-auth.html` on BBB app domain.
- Popup reuses shared auth modal (`/js/auth.js`) for sign in/sign up.
- On success, popup posts `{ source: "bbb-extension-auth", accessToken }` back
  to extension with origin and state checks.
- Popup only accepts trusted target origins (`*.webflow-ext.com` or localhost).
