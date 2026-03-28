/**
 * lib/config.js — app configuration
 *
 * Single import point for runtime config in new ES module code.
 * The server populates window.GNH_CONFIG via /config.js before any
 * module entrypoint runs. Pages using the new app/ architecture must
 * load /config.js as a plain script before their module entrypoint:
 *
 *   <script src="/config.js"></script>
 *   <script type="module" src="/app/pages/example.js"></script>
 *
 * New code imports from this module rather than reading
 * window.GNH_CONFIG directly.
 *
 * Transition note: window.GNH_CONFIG is set by the legacy server
 * endpoint. The name will change when the server config endpoint is
 * updated. At that point, only this file needs to change.
 */

const raw = (typeof window !== "undefined" && window.GNH_CONFIG) || {};

/** Supabase project URL */
export const supabaseUrl = raw.supabaseUrl || "";

/** Supabase publishable (anon) key */
export const supabaseAnonKey = raw.supabaseAnonKey || "";

/** Deployment environment: "production" | "development" | "" */
export const environment = raw.environment || "";

/** Whether Cloudflare Turnstile CAPTCHA is enabled */
export const enableTurnstile = Boolean(raw.enableTurnstile);

/**
 * Returns true when the minimum required config values are present.
 * Modules that need Supabase should call this and surface an error
 * rather than silently failing later.
 */
export function isConfigured() {
  return Boolean(supabaseUrl && supabaseAnonKey);
}

export default {
  supabaseUrl,
  supabaseAnonKey,
  environment,
  enableTurnstile,
  isConfigured,
};
