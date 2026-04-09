/**
 * lib/admin.js — system administrator utilities
 *
 * Provides database reset with triple confirmation and admin role checks.
 * Only system_admin users (via app_metadata) see admin controls.
 */

import { getSession } from "/app/lib/auth-session.js";

/**
 * Check if user has system administrator privileges.
 * Only trusts app_metadata — user_metadata is user-controlled.
 */
export function isSystemAdmin(session) {
  if (!session?.user) return false;
  return session.user.app_metadata?.system_role === "system_admin";
}

/**
 * Handle a reset action with triple confirmation.
 * @param {object} session — Supabase session object
 * @param {HTMLElement} btn — the button element (for state updates)
 * @param {string} originalText — original button text to restore on failure
 * @param {string} endpoint — API endpoint to POST to
 * @param {string} warning — first confirmation warning message
 */
async function handleReset(session, btn, originalText, endpoint, warning) {
  console.info(`reset: user clicked button (${endpoint})`);

  if (!confirm(warning)) {
    console.info("reset: first confirmation declined");
    return;
  }

  if (
    !confirm(
      'This action CANNOT be undone. All data will be permanently lost.\n\nType "DELETE" in the next prompt to confirm.'
    )
  ) {
    console.info("reset: second confirmation declined");
    return;
  }

  const typeCheck = prompt("Type DELETE to confirm:");
  if (typeCheck !== "DELETE") {
    alert("Reset cancelled - you did not type DELETE correctly.");
    console.info("reset: delete keyword mismatch");
    return;
  }

  try {
    if (btn) {
      btn.disabled = true;
      btn.textContent = "Resetting...";
    }
    console.info(`reset: sending POST ${endpoint}`, {
      user: session.user?.id ?? "unknown",
    });

    if (!session?.access_token) {
      alert("Not authenticated");
      console.warn("reset: no session – aborting");
      if (btn) {
        btn.disabled = false;
        btn.textContent = originalText;
      }
      return;
    }

    const controller = new AbortController();
    const timeoutId = window.setTimeout(() => controller.abort(), 120000);
    let response;
    try {
      response = await fetch(endpoint, {
        method: "POST",
        headers: {
          Authorization: `Bearer ${session.access_token}`,
          "Content-Type": "application/json",
        },
        signal: controller.signal,
      });
    } finally {
      window.clearTimeout(timeoutId);
    }

    if (response.ok) {
      console.info("reset: completed successfully");
      alert("Reset successful! Page will reload.");
      window.location.reload();
    } else {
      const error = await response.text();
      console.error("reset: server returned error", error);
      alert(`Reset failed: ${error}`);
      if (btn) {
        btn.disabled = false;
        btn.textContent = originalText;
      }
    }
  } catch (error) {
    const message =
      error?.name === "AbortError"
        ? "Request timed out. Please try again."
        : String(error?.message ?? error);
    console.error("reset: unexpected failure", error);
    alert(`Error: ${message}`);
    if (btn) {
      btn.disabled = false;
      btn.textContent = originalText;
    }
  }
}

/**
 * Initialise an admin reset button. Shows button if user is system admin.
 * @param {string} buttonId — ID of the reset button element
 * @param {object} options
 * @param {string} options.endpoint — API endpoint to POST to
 * @param {string} options.warning — first confirmation warning message
 * @param {string} [options.containerSelector] — selector for container to show on first call
 */
export async function initAdminResetButton(buttonId, options = {}) {
  const session = await getSession();
  if (!isSystemAdmin(session)) return;

  const btn = document.getElementById(buttonId);
  if (!btn) return;

  if (options.containerSelector) {
    const container = document.querySelector(options.containerSelector);
    if (container) {
      container.classList.remove("settings-hidden");
      container.style.display = "";
    }
  } else {
    btn.style.display = "inline-block";
  }

  const originalText = btn.textContent;
  btn.addEventListener("click", () =>
    handleReset(session, btn, originalText, options.endpoint, options.warning)
  );
}
