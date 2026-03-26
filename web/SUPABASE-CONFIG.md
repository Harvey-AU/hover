# Supabase Configuration

## ⚠️ IMPORTANT: Single Source of Truth

**ALL Supabase credentials MUST use these values:**

```javascript
URL: https://hover.auth.goodnative.co
ANON_KEY: eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6Imdwemp0Ymd0ZGp4bmFjZGZ1anZ4Iiwicm9sZSI6ImFub24iLCJpYXQiOjE3NDUwNjYxNjMsImV4cCI6MjA2MDY0MjE2M30.eJjM2-3X8oXsFex_lQKvFkP1-_yLMHsueIn7_hCF6YI
```

## Files That Use These Credentials

✅ **Correct (Current):**

- `test-components.html`
- `test-login.html`
- `dashboard.html`
- `web/examples/webflow-integration.html`
- `web/examples/complete-example.html`

## When Adding New Files

**ALWAYS** copy from one of the correct files above, OR use the config file:

```javascript
import { SUPABASE_CONFIG } from "./src/config/supabase.js";

window.supabase = window.supabase.createClient(
  SUPABASE_CONFIG.url,
  SUPABASE_CONFIG.anonKey
);
```

## Why We Use Custom Domain

- **Domain**: `hover.auth.goodnative.co` (NOT supabase.co)
- **Branding**: Professional, no "supabase" in user-facing URLs
- **OAuth**: Google/social login configured for this domain
- **Security**: Proper domain verification

## ❌ NEVER Use These (Wrong)

- `ezyjufvaxepjhknqhcap.supabase.co` - This is wrong!
- `gpzjtbgtdjxnacdfujvx.supabase.co` - Database domain, not auth!
- Old/expired anon keys

## Verification

To verify config is correct, check that:

1. `https://hover.auth.goodnative.co` resolves
2. Google login works
3. API calls succeed
4. No CORS errors
