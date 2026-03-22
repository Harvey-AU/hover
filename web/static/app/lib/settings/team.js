/**
 * lib/settings/team.js — team section logic
 *
 * Handles organisation member listing, role changes, member removal,
 * invitations (send, revoke, copy link), and admin visibility toggles.
 * Surface-agnostic: all render/action functions accept a container element.
 *
 * Usage:
 *   import { loadMembers, loadInvites, setupTeamActions } from "/app/lib/settings/team.js";
 *
 *   const section = document.getElementById("team");
 *   await loadMembers(section);
 *   await loadInvites(section);
 *   setupTeamActions(section);
 */

import { get, post, patch, del } from "/app/lib/api-client.js";
import { showToast as _showToast } from "/app/components/hover-toast.js";

/** Adapter: (variant, message) → showToast(message, { variant }) */
function toast(variant, message) {
  _showToast(message, { variant });
}

// ── Module state ───────────────────────────────────────────────────────────────

let currentUserRole = "member";
let currentUserId = null;

export function getTeamState() {
  return { currentUserRole, currentUserId };
}

// ── Members ────────────────────────────────────────────────────────────────────

/**
 * Load and render organisation members into a container.
 * @param {HTMLElement} container — the team section element
 */
export async function loadMembers(container) {
  const root = container || document;
  const membersList = root.querySelector("#teamMembersList");
  // Templates live outside the section in settings.html — always use document.
  const memberTemplate = document.querySelector("#teamMemberTemplate");
  const emptyState = root.querySelector("#teamMembersEmpty");
  if (!membersList || !memberTemplate) return;

  membersList.replaceChildren();

  try {
    const response = await get("/v1/organisations/members");
    const members = response.members || [];
    currentUserRole = response.current_user_role || "member";
    currentUserId = response.current_user_id || null;

    if (members.length === 0) {
      if (emptyState) emptyState.style.display = "block";
      return;
    }
    if (emptyState) emptyState.style.display = "none";

    members.forEach((member) => {
      const clone = memberTemplate.content.cloneNode(true);
      const row = clone.querySelector(".settings-member-row");
      const avatarEl = clone.querySelector(".settings-member-avatar");
      const nameEl = clone.querySelector(".settings-member-name");
      const emailEl = clone.querySelector(".settings-member-email");
      const roleSelect = clone.querySelector(".settings-member-role-select");
      const removeBtn = clone.querySelector(".settings-member-remove");

      if (row) row.dataset.memberId = member.id;
      if (nameEl) nameEl.textContent = member.full_name || "Unnamed";
      if (emailEl) emailEl.textContent = member.email || "";

      if (avatarEl) {
        const initialsSource = member.full_name || member.email || "";
        const initials =
          window.BBAvatar?.getInitials?.(initialsSource) ||
          window.BBAuth?.getInitials?.(initialsSource) ||
          "?";
        const avatarSize = Math.ceil(34 * (window.devicePixelRatio || 1));
        window.BBAvatar?.setUserAvatar?.(
          avatarEl,
          member.email || "",
          initials,
          {
            size: avatarSize,
            alt: `${member.full_name || member.email || "Member"} avatar`,
          }
        );
      }

      if (roleSelect) {
        roleSelect.value = member.role || "member";
        const canEditRole =
          currentUserRole === "admin" && member.id !== currentUserId;
        roleSelect.disabled = !canEditRole;
        roleSelect.addEventListener("change", async () => {
          const previousValue = member.role || "member";
          try {
            await updateMemberRole(member.id, roleSelect.value, container);
            member.role = roleSelect.value;
          } catch {
            roleSelect.value = previousValue;
          }
        });
      }

      if (removeBtn) {
        removeBtn.dataset.memberId = member.id;
        removeBtn.addEventListener("click", () =>
          removeMember(member.id, container)
        );
        const canRemove =
          currentUserRole === "admin" && member.id !== currentUserId;
        if (!canRemove) removeBtn.disabled = true;
      }

      membersList.appendChild(clone);
    });

    updateAdminVisibility(container);
  } catch (err) {
    console.error("Failed to load members:", err);
    toast("error", "Failed to load members");
  }
}

async function removeMember(memberId, container) {
  if (!memberId) return;
  if (!confirm("Remove this member from the organisation?")) return;

  try {
    await del(`/v1/organisations/members/${memberId}`);
    toast("success", "Member removed");
    loadMembers(container);
  } catch (err) {
    console.error("Failed to remove member:", err);
    toast("error", "Failed to remove member");
  }
}

async function updateMemberRole(memberId, role, container) {
  if (!memberId) return;

  try {
    await patch(`/v1/organisations/members/${memberId}`, { role });
    toast("success", "Member role updated");
    await loadMembers(container);
  } catch (err) {
    console.error("Failed to update member role.");
    toast("error", "Failed to update member role");
    throw err;
  }
}

// ── Invites ────────────────────────────────────────────────────────────────────

/**
 * Load and render pending invites into a container.
 * @param {HTMLElement} container — the team section element
 */
export async function loadInvites(container) {
  const root = container || document;
  const invitesList = root.querySelector("#teamInvitesList");
  const inviteTemplate = document.querySelector("#teamInviteTemplate");
  const emptyState = root.querySelector("#teamInvitesEmpty");
  const defaultEmptyText =
    emptyState?.textContent?.trim() || "No pending invites.";
  if (!invitesList || !inviteTemplate) return;

  invitesList.replaceChildren();

  if (currentUserRole !== "admin") {
    if (emptyState) {
      emptyState.style.display = "block";
      emptyState.textContent = "Only admins can view pending invites.";
    }
    return;
  }
  if (emptyState) emptyState.textContent = defaultEmptyText;

  try {
    const response = await get("/v1/organisations/invites");
    const invites = response.invites || [];

    if (invites.length === 0) {
      if (emptyState) emptyState.style.display = "block";
      return;
    }
    if (emptyState) emptyState.style.display = "none";

    invites.forEach((invite) => {
      const clone = inviteTemplate.content.cloneNode(true);
      const row = clone.querySelector(".settings-invite-row");
      const emailEl = clone.querySelector(".settings-invite-email");
      const roleEl = clone.querySelector(".settings-invite-role");
      const dateEl = clone.querySelector(".settings-invite-date");
      const revokeBtn = clone.querySelector(".settings-invite-revoke");
      const copyBtn = clone.querySelector(".settings-invite-copy");

      if (row) row.dataset.inviteId = invite.id;
      if (emailEl) emailEl.textContent = invite.email;
      if (roleEl) roleEl.textContent = invite.role;
      if (dateEl) {
        const date = new Date(invite.created_at);
        dateEl.textContent = `Sent ${date.toLocaleDateString("en-AU")}`;
      }

      if (copyBtn) {
        if (invite.invite_link) {
          copyBtn.addEventListener("click", async () => {
            try {
              await navigator.clipboard.writeText(invite.invite_link);
              copyBtn.textContent = "Copied!";
              setTimeout(() => {
                copyBtn.textContent = "Copy link";
              }, 2000);
            } catch {
              toast("error", "Failed to copy link");
            }
          });
        } else {
          copyBtn.style.display = "none";
        }
      }

      if (revokeBtn) {
        revokeBtn.dataset.inviteId = invite.id;
        revokeBtn.addEventListener("click", () =>
          revokeInvite(invite.id, container)
        );
      }

      invitesList.appendChild(clone);
    });
  } catch (err) {
    console.error("Failed to load invites:", err);
    toast("error", "Failed to load invites");
  }
}

/**
 * Send an invite from the invite form within a container.
 * @param {Event} event — form submit event
 * @param {HTMLElement} container — the team section element
 */
export async function sendInvite(event, container) {
  event.preventDefault();
  if (currentUserRole !== "admin") {
    toast("error", "Only admins can send invites");
    return;
  }

  const root = container || document;
  const emailInput = root.querySelector("#teamInviteEmail");
  const roleSelect = root.querySelector("#teamInviteRole");
  if (!emailInput) return;

  const email = emailInput.value.trim();
  const role = roleSelect?.value || "member";
  if (!email) {
    toast("error", "Email is required");
    return;
  }

  try {
    const result = await post("/v1/organisations/invites", { email, role });
    const delivery = result?.invite?.email_delivery;
    if (delivery === "failed") {
      toast(
        "warning",
        "Invite created but email failed \u2014 use the copy link button to share manually"
      );
    } else {
      toast("success", "Invite sent");
    }
    emailInput.value = "";
    await loadInvites(container);
  } catch (err) {
    console.error("Failed to send invite:", err);
    toast("error", "Failed to send invite");
  }
}

async function revokeInvite(inviteId, container) {
  if (!inviteId) return;
  if (!confirm("Revoke this invite?")) return;

  try {
    await del(`/v1/organisations/invites/${inviteId}`);
    toast("success", "Invite revoked");
    await loadInvites(container);
  } catch (err) {
    console.error("Failed to revoke invite:", err);
    toast("error", "Failed to revoke invite");
  }
}

// ── Admin visibility ───────────────────────────────────────────────────────────

/**
 * Show/hide admin-only elements within a container.
 * @param {HTMLElement} container
 */
export function updateAdminVisibility(container) {
  const root = container || document;
  const isAdmin = currentUserRole === "admin";
  root.querySelectorAll(".settings-admin-only").forEach((el) => {
    el.style.display = isAdmin ? "" : "none";
  });
}

// ── Setup ──────────────────────────────────────────────────────────────────────

/**
 * Wire up team section event listeners within a container.
 * @param {HTMLElement} container — the team section element
 */
export function setupTeamActions(container) {
  const root = container || document;

  const inviteForm = root.querySelector("#teamInviteForm");
  if (inviteForm) {
    inviteForm.addEventListener("submit", (event) =>
      sendInvite(event, container)
    );
  }
}
