/**
 * pages/webflow-login.js — Webflow Designer extension auth screen
 *
 * Entrypoint for the extension-auth page served by the Go backend at
 * /extension-auth. Replaces the legacy core.js + gnh-auth-extension.js
 * global-script model for this surface.
 *
 * Loading contract (extension-auth.html):
 *   1. <script src="/config.js">          — sets window.GNH_CONFIG
 *   2. <script src="supabase.js">         — sets window.supabase (CDN UMD)
 *   3. <script src="/js/auth.js">         — sets window.GNHAuth helpers
 *   4. <script type="module" src="/app/pages/webflow-login.js">
 *
 * No gnh-bootstrap.js. No GNH_APP.whenReady().
 * The popup still reuses the legacy GNHAuth bundle for modal loading,
 * callback handling, and backend registration while the redirect contract
 * remains centralised in auth.js.
 *
 * Auth redirect contract (from AGENTS.md):
 *   - Deep-link URLs must return to the exact originating URL.
 *   - Extension auth uses window.GNH_APP.extensionAuth = true to signal
 *     that the OAuth redirect should return to this page, not /dashboard.
 *   - handleSocialLogin in auth.js reads window.GNH_APP.extensionAuth and
 *     sets redirectTo = window.location.href for extension flows.
 *   - This module sets that flag before any auth action.
 */

import { isConfigured } from "/app/lib/config.js";
import { getSession, onAuthStateChange } from "/app/lib/auth-session.js";
import { showToast } from "/app/components/hover-toast.js";

// ── Constants ──────────────────────────────────────────────────────────────────

/**
 * The state token passed by the extension when opening this popup.
 * Included in the postMessage payload so index.ts can verify it.
 * Passed as both ?state= and ?extension_state= in the URL.
 */
const SEARCH_PARAMS = new URLSearchParams(window.location.search);
const TARGET_ORIGIN = SEARCH_PARAMS.get("origin") || "";
const EXTENSION_STATE =
  SEARCH_PARAMS.get("extension_state") || SEARCH_PARAMS.get("state") || "";

// ── Element references ─────────────────────────────────────────────────────────

/** @returns {HTMLElement|null} */
const el = (id) => document.getElementById(id);

// ── Init ───────────────────────────────────────────────────────────────────────

async function init() {
  // Signal to auth.js (legacy, still loaded via the modal) that this is an
  // extension auth flow so OAuth redirects return here, not to /dashboard.
  window.GNH_APP = Object.assign({}, window.GNH_APP || {}, {
    extensionAuth: true,
  });

  if (!isConfigured()) {
    setStatus("Configuration error — please reload.", "error");
    return;
  }

  if (window.opener && !validatePopupContract()) {
    return;
  }

  const authReady = await ensureLegacyAuthReady();
  if (!authReady) {
    setStatus(
      "Sign-in is still loading — please refresh and try again.",
      "error"
    );
    return;
  }

  setStatus("Preparing sign-in…");

  // Load the shared auth modal (legacy component — reused as-is for Phase 1).
  // Phase 2+ will replace this with a native module component.
  try {
    await loadAuthModalFromLegacy();
  } catch (error) {
    console.error("webflow-login: failed to load auth modal", error);
    setStatus("Sign-in modal failed to load — please refresh.", "error");
    return;
  }

  // Handle any OAuth callback tokens already in the URL/hash.
  await handleCallbackIfPresent();

  // Check existing session.
  const session = await getSession().catch(() => null);
  if (session) {
    await handleAuthenticated(session);
    return;
  }

  // No session — show the login modal.
  showLogin();

  // Listen for auth state changes (sign-in after modal interaction).
  const unsubscribe = onAuthStateChange(async (event, newSession) => {
    if ((event === "SIGNED_IN" || event === "TOKEN_REFRESHED") && newSession) {
      unsubscribe();
      await handleAuthenticated(newSession);
    }
  });

  // Wire up the reopen button.
  const reopenBtn = el("reopenModalBtn");
  if (reopenBtn) {
    reopenBtn.addEventListener("click", showLogin);
  }
}

// ── Auth flow ──────────────────────────────────────────────────────────────────

/**
 * Handles a successfully authenticated session:
 * registers the user with the backend, then posts the session token
 * back to the Webflow Designer extension via postMessage.
 *
 * @param {import("@supabase/supabase-js").Session} session
 */
async function handleAuthenticated(session) {
  setStatus("Signing you in…");

  // Register with backend (idempotent — 409 is success).
  if (session.user) {
    await registerUserWithBackend(session.user).catch((err) => {
      console.warn(
        "webflow-login: backend registration failed (non-fatal)",
        err
      );
    });
  }

  // Send the session back to the extension.
  // Contract must match what index.ts connectAccount() expects:
  //   { source: "gnh-extension-auth", state, extensionState, type: "success",
  //     accessToken, user: { id, email, avatarUrl } }
  if (!window.opener) {
    // Opened directly (not by the extension popup flow) — nothing to notify.
    setStatus("Signed in — you can close this window.", "success");
    showToast("Signed in successfully.", { variant: "success", duration: 0 });
    return;
  }
  try {
    window.opener.postMessage(
      {
        source: "gnh-extension-auth",
        state: EXTENSION_STATE,
        extensionState: EXTENSION_STATE,
        type: "success",
        accessToken: session.access_token,
        user: {
          id: session.user?.id ?? "",
          email: session.user?.email ?? "",
          avatarUrl: session.user?.user_metadata?.avatar_url ?? "",
        },
      },
      TARGET_ORIGIN
    );

    setStatus("Signed in — you can close this window.", "success");
    showToast("Signed in successfully.", { variant: "success", duration: 0 });
  } catch (err) {
    console.error("webflow-login: postMessage failed", err);
    setStatus(
      "Signed in, but could not notify the extension. Please close and reopen it.",
      "warning"
    );
    showToast("Sign-in complete, but the extension was not notified.", {
      variant: "warning",
      duration: 0,
    });
  }
}

/**
 * Handle OAuth callback tokens that may be present in the current URL.
 * Delegates to auth.js.handleAuthCallback if available, otherwise
 * handles the access_token hash directly.
 */
async function handleCallbackIfPresent() {
  if (typeof window.GNHAuth?.handleAuthCallback === "function") {
    await window.GNHAuth.handleAuthCallback().catch(() => {});
    return;
  }

  const hash = new URLSearchParams(window.location.hash.slice(1));
  const accessToken = hash.get("access_token");
  const refreshToken = hash.get("refresh_token");

  if (accessToken && window.supabase?.auth) {
    try {
      await window.supabase.auth.setSession({
        access_token: accessToken,
        refresh_token: refreshToken || "",
      });
      history.replaceState(null, "", window.location.pathname);
    } catch (err) {
      console.warn("webflow-login: failed to restore session from hash", err);
    }
  }
}

// ── Backend registration ───────────────────────────────────────────────────────

/**
 * @param {import("@supabase/supabase-js").User} user
 */
async function registerUserWithBackend(user) {
  if (typeof window.GNHAuth?.registerUserWithBackend === "function") {
    const registered = await window.GNHAuth.registerUserWithBackend(user);
    if (!registered) {
      throw new Error("Backend registration failed");
    }
    return;
  }

  throw new Error("Legacy backend registration helper unavailable");
}

// ── UI helpers ─────────────────────────────────────────────────────────────────

/**
 * Update the status text element.
 * @param {string} message
 * @param {"info"|"success"|"error"|"warning"} [variant]
 */
function setStatus(message, variant = "info") {
  const statusEl = el("extensionAuthStatus");
  if (!statusEl) return;
  statusEl.textContent = message;
  statusEl.dataset.variant = variant;
}

/** Load and inject the shared auth modal HTML, then show the login form. */
async function loadAuthModalFromLegacy() {
  if (typeof window.GNHAuth?.loadAuthModal === "function") {
    await window.GNHAuth.loadAuthModal();
    return;
  }

  throw new Error("Auth modal loader unavailable");
}

/** Show the auth modal in login mode. */
function showLogin() {
  setStatus("Please sign in to continue.");
  if (typeof window.showAuthModal === "function") {
    window.showAuthModal();
  }
  if (typeof window.showLoginForm === "function") {
    window.showLoginForm();
  }
}

function validatePopupContract() {
  if (!window.opener || window.opener.closed) {
    setStatus(
      "This login window must be opened from the Webflow extension.",
      "error"
    );
    return false;
  }

  if (
    typeof window.GNHAuth?.isValidExtensionTargetOrigin === "function" &&
    !window.GNHAuth.isValidExtensionTargetOrigin(TARGET_ORIGIN)
  ) {
    setStatus("Invalid extension origin. Please reopen sign-in.", "error");
    return false;
  }

  try {
    const referrerOrigin = document.referrer
      ? new URL(document.referrer).origin
      : "";
    if (referrerOrigin && TARGET_ORIGIN && referrerOrigin !== TARGET_ORIGIN) {
      setStatus(
        "Origin mismatch. Please relaunch from the extension.",
        "error"
      );
      return false;
    }
  } catch (_error) {
    setStatus("Unable to validate opener origin. Please relaunch.", "error");
    return false;
  }

  return true;
}

async function ensureLegacyAuthReady() {
  const pollIntervalMs = 50;
  const timeoutMs = 5000;
  const maxAttempts = Math.ceil(timeoutMs / pollIntervalMs);

  for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
    const auth = window.GNHAuth;
    const supabaseGlobal = window.supabase;

    if (
      auth?.initialiseSupabase &&
      (supabaseGlobal?.auth || supabaseGlobal?.createClient)
    ) {
      if (supabaseGlobal?.auth) {
        return true;
      }

      if (auth.initialiseSupabase()) {
        return Boolean(window.supabase?.auth);
      }
    }

    await new Promise((resolve) => {
      window.setTimeout(resolve, pollIntervalMs);
    });
  }

  console.error("webflow-login: legacy auth bundle did not initialise in time");
  return false;
}

// ── Bootstrap ──────────────────────────────────────────────────────────────────

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", init);
} else {
  init();
}
