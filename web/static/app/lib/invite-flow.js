/**
 * lib/invite-flow.js — invite token handling
 *
 * Handles invite token detection, preview, acceptance, and auth gating.
 * Replaces bb-invite-flow.js (legacy IIFE).
 */

import { getAccessToken } from "/app/lib/auth-session.js";

const SESSION_RETRY_ATTEMPTS = 12;
const SESSION_RETRY_DELAY_MS = 150;

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

export function getInviteToken(paramName = "invite_token") {
  return new URLSearchParams(window.location.search).get(paramName);
}

export function clearInviteTokenFromURL(paramName = "invite_token") {
  const params = new URLSearchParams(window.location.search);
  if (!params.has(paramName)) return;
  params.delete(paramName);
  const url = new URL(window.location.href);
  url.search = params.toString();
  window.history.replaceState({}, "", url.toString());
}

export async function fetchInvitePreview(token) {
  const response = await fetch(
    `/v1/organisations/invites/preview?token=${encodeURIComponent(token)}`,
    { method: "GET" }
  );
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(payload?.message || "Failed to load invite details");
  }
  return payload?.data?.invite || null;
}

async function acceptInvite(token) {
  const accessToken = await getAccessToken();
  if (!accessToken) throw new Error("Authentication is required");

  const response = await fetch("/v1/organisations/invites/accept", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${accessToken}`,
    },
    body: JSON.stringify({ token }),
  });

  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(payload?.message || "Failed to accept invite");
  }
  return payload?.data ?? null;
}

async function getSessionWithRetry() {
  if (!window.supabase?.auth) return null;

  let session = null;
  for (let attempt = 0; attempt < SESSION_RETRY_ATTEMPTS; attempt += 1) {
    const sessionResult = await window.supabase.auth.getSession();
    session = sessionResult?.data?.session || null;
    if (session?.user) return session;
    await sleep(SESSION_RETRY_DELAY_MS);
  }
  return session;
}

let authListenerAttached = false;
let authSubscription = null;

function ensureAuthModalAndReloadOnSignIn() {
  if (typeof window.showAuthModal === "function") {
    window.showAuthModal();
  }

  const auth = window.supabase?.auth;
  if (authListenerAttached || !auth) return;

  authListenerAttached = true;
  const result = auth.onAuthStateChange((event) => {
    if (event === "SIGNED_IN") {
      authSubscription?.unsubscribe?.();
      authSubscription = null;
      authListenerAttached = false;
      window.location.reload();
    }
  });
  authSubscription = result?.data?.subscription || null;
  if (!authSubscription?.unsubscribe && result?.unsubscribe) {
    authSubscription = result;
  }
  if (!authSubscription?.unsubscribe) {
    authListenerAttached = false;
  }
}

/**
 * Handle the full invite token flow: detect token, check auth, accept.
 * @param {object} options
 * @returns {Promise<{status: string, token?: string, result?: any, error?: Error}>}
 */
export async function handleInviteTokenFlow(options = {}) {
  const {
    tokenParamName = "invite_token",
    clearTokenOnSuccess = true,
    redirectTo = "",
    onAuthRequired,
    onAccepted,
    onError,
  } = options;

  const token = getInviteToken(tokenParamName);
  if (!token) return { status: "no_token" };

  // Process any pending auth callback first
  if (typeof window.BBAuth?.handleAuthCallback === "function") {
    try {
      await window.BBAuth.handleAuthCallback();
    } catch (error) {
      console.warn("Invite flow auth callback processing failed:", error);
    }
  }

  const session = await getSessionWithRetry();
  if (!session?.user) {
    ensureAuthModalAndReloadOnSignIn();
    if (typeof onAuthRequired === "function") onAuthRequired(token);
    return { status: "auth_required", token };
  }

  try {
    const result = await acceptInvite(token);
    if (clearTokenOnSuccess) clearInviteTokenFromURL(tokenParamName);
    if (typeof window.BBAuth?.clearPendingInviteToken === "function") {
      window.BBAuth.clearPendingInviteToken();
    }
    if (typeof onAccepted === "function") await onAccepted(result);
    if (redirectTo) window.location.assign(redirectTo);
    return { status: "accepted", token, result };
  } catch (error) {
    if (typeof onError === "function") onError(error);
    return { status: "error", token, error };
  }
}
