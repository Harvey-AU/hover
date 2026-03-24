/**
 * lib/settings/organisations.js — org creation modal
 *
 * Handles the "Create Organisation" modal in settings.
 * Uses api-client for the POST request.
 */

import { getAccessToken } from "/app/lib/auth-session.js";
import { showToast as _showToast } from "/app/components/hover-toast.js";

function toast(variant, message) {
  _showToast(message, { variant });
}

/**
 * Wire up the create-organisation modal within the settings page.
 * @param {object} [options]
 * @param {function} [options.onCreated] — called after successful creation
 */
export function initCreateOrgModal(options = {}) {
  const modal = document.getElementById("createOrgModal");
  const form = document.getElementById("createOrgForm");
  const nameInput = document.getElementById("newOrgName");
  const errorDiv = document.getElementById("createOrgError");
  const createBtn = document.getElementById("createOrgBtn");
  const closeBtn = document.getElementById("closeCreateOrgModal");
  const cancelBtn = document.getElementById("cancelCreateOrg");
  const submitBtn = document.getElementById("submitCreateOrg");

  if (!modal || !form) return;

  const openModal = () => {
    modal.classList.add("show");
    nameInput.value = "";
    errorDiv.style.display = "none";
    nameInput.focus();
  };

  const closeModal = () => {
    modal.classList.remove("show");
  };

  createBtn?.addEventListener("click", (e) => {
    e.stopPropagation();
    document.getElementById("orgSwitcher")?.classList.remove("open");
    openModal();
  });

  closeBtn?.addEventListener("click", closeModal);
  cancelBtn?.addEventListener("click", closeModal);
  modal?.addEventListener("click", (e) => {
    if (e.target === modal) closeModal();
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && modal?.classList.contains("show")) {
      closeModal();
    }
  });

  form?.addEventListener("submit", async (e) => {
    e.preventDefault();

    const name = nameInput.value.trim();
    if (!name) {
      errorDiv.textContent = "Organisation name is required";
      errorDiv.style.display = "block";
      return;
    }

    submitBtn.disabled = true;
    submitBtn.textContent = "Creating...";
    errorDiv.style.display = "none";

    try {
      const token = await getAccessToken();
      if (!token) throw new Error("Not authenticated");

      const response = await fetch("/v1/organisations", {
        method: "POST",
        headers: {
          Authorization: `Bearer ${token}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ name }),
      });

      const data = await response.json();

      if (response.ok) {
        closeModal();

        const newOrg = data.data?.organisation;

        // Update shared org data (bridge to legacy globals).
        window.BB_ACTIVE_ORG = newOrg;
        if (Array.isArray(window.BB_ORGANISATIONS)) {
          window.BB_ORGANISATIONS.push(newOrg);
        } else {
          window.BB_ORGANISATIONS = [newOrg];
        }

        document.dispatchEvent(
          new CustomEvent("bb:org-switched", {
            detail: { organisation: newOrg },
          })
        );

        if (options.onCreated) await options.onCreated(newOrg);
        toast("success", `Organisation "${name}" created`);
      } else {
        errorDiv.textContent = data.message || "Failed to create organisation";
        errorDiv.style.display = "block";
      }
    } catch (err) {
      console.error("Error creating organisation:", err);
      errorDiv.textContent = "An error occurred. Please try again.";
      errorDiv.style.display = "block";
    } finally {
      submitBtn.disabled = false;
      submitBtn.textContent = "Create";
    }
  });
}
