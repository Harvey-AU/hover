---
name: preview
description: Start the local dev server in Claude preview and authenticate.
---

Start the local development server and verify it is working.

## Steps

1. **Ensure Supabase is running**

   Run `supabase status` to check. If it is not running, run `supabase start`
   before proceeding.

2. **Start the preview server**

   Call `preview_start` with name `go-server`. This starts Air (hot reloading)
   via `.claude/launch.json`. If already running it will be reused.

3. **Check server logs**

   Call `preview_logs` to confirm Air built and started successfully. Look for
   `running...` in the output. If the build failed, read the error and fix it.

4. **Authenticate**

   Navigate to `/dev/auto-login` using `preview_eval`:

   ```javascript
   window.location.href = "/dev/auto-login";
   ```

   This signs in as `dev@example.com` server-side and redirects to `/dashboard`.

5. **Confirm**

   Take a `preview_screenshot` to confirm the dashboard loaded correctly.

## Notes

- The preview browser cannot reach `127.0.0.1:54321` (local Supabase) directly —
  always use `/dev/auto-login` instead of the normal sign-in modal.
- After `supabase db reset`, just navigate to `/dev/auto-login` again.
- Port is `8847`.
