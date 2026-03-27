/**
 * Admin utilities for system administrators
 * Provides shared functionality for admin operations like database reset
 */
(function () {
  "use strict";

  /**
   * Check if user has system administrator privileges
   * @param {Object} session - Supabase session object
   * @returns {boolean}
   */
  function isSystemAdmin(session) {
    if (!session?.user) return false;
    // Only trust app_metadata - user_metadata is user-controlled
    return session.user.app_metadata?.system_role === "system_admin";
  }

  /**
   * Handle database reset with triple confirmation
   * @param {Object} session - Supabase session object
   * @param {HTMLElement} btn - The button element (for state updates)
   * @param {string} originalText - Original button text to restore on failure
   */
  async function handleResetDatabase(session, btn, originalText) {
    console.info("reset-db: user clicked reset button");

    // Triple confirmation before resetting
    if (
      !confirm(
        "⚠️ WARNING: This will DELETE ALL jobs and tasks!\n\nAre you absolutely sure you want to reset the database?"
      )
    ) {
      console.info("reset-db: first confirmation declined");
      return;
    }

    if (
      !confirm(
        'This action CANNOT be undone. All data will be permanently lost.\n\nType "DELETE" in the next prompt to confirm.'
      )
    ) {
      console.info("reset-db: second confirmation declined");
      return;
    }

    const typeCheck = prompt("Type DELETE to confirm:");
    if (typeCheck !== "DELETE") {
      alert("Reset cancelled - you did not type DELETE correctly.");
      console.info("reset-db: delete keyword mismatch");
      return;
    }

    try {
      if (btn) {
        btn.disabled = true;
        btn.textContent = "Resetting...";
      }
      console.info("reset-db: request initialised");

      if (!session?.access_token) {
        alert("Not authenticated");
        console.warn("reset-db: no session – aborting");
        if (btn) {
          btn.disabled = false;
          btn.textContent = originalText;
        }
        return;
      }

      console.info("reset-db: sending POST /v1/admin/reset-db", {
        user: session.user?.id ?? "unknown",
      });

      const controller = new AbortController();
      const timeoutId = window.setTimeout(() => controller.abort(), 15000);
      let response;
      try {
        response = await fetch("/v1/admin/reset-db", {
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
        console.info("reset-db: completed successfully");
        alert("✅ Database reset successful! Page will reload.");
        window.location.reload();
      } else {
        const error = await response.text();
        console.error("reset-db: server returned error", error);
        alert(`❌ Reset failed: ${error}`);
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
      console.error("reset-db: unexpected failure", error);
      alert(`❌ Error: ${message}`);
      if (btn) {
        btn.disabled = false;
        btn.textContent = originalText;
      }
    }
  }

  /**
   * Initialise admin reset button
   * Shows button if user is system admin and attaches click handler
   * @param {string} buttonId - ID of the reset button element
   * @param {Object} session - Supabase session object
   * @param {Object} options - Optional configuration
   * @param {string} options.containerSelector - Selector for container to show (e.g., "#adminGroup")
   */
  function initAdminResetButton(buttonId, session, options = {}) {
    if (!isSystemAdmin(session)) return;

    const btn = document.getElementById(buttonId);
    if (!btn) return;

    // Show the button (or container if specified)
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
      handleResetDatabase(session, btn, originalText)
    );
  }

  // Expose to global scope
  window.BBAdmin = {
    isSystemAdmin,
    initAdminResetButton,
    handleResetDatabase,
  };
})();
