(function () {
  function setStatus(text, type = "") {
    const statusEl = document.getElementById("inviteStatus");
    if (!statusEl) return;

    statusEl.textContent = text;
    statusEl.className = "invite-status";
    if (type) {
      statusEl.classList.add(`invite-status-${type}`);
    }
  }

  async function ensureAuthModalReady() {
    if (window.BBAuth?.loadAuthModal) {
      await window.BBAuth.loadAuthModal();
    }
  }

  function showInlineAuthForm(formType = "login") {
    if (typeof window.showAuthModal === "function") {
      // Reuse shared auth initialisation + state handling.
      // CSS on this page renders the modal inline instead of as an overlay.
      window.showAuthModal();
    }
    if (window.BBAuth?.showAuthForm) {
      window.BBAuth.showAuthForm(formType);
    }
  }

  function renderInviteDetails(invite) {
    const inviterEl = document.getElementById("inviteInviter");
    const orgEl = document.getElementById("inviteOrg");
    const roleEl = document.getElementById("inviteRole");

    if (inviterEl) {
      inviterEl.textContent = invite?.inviter_name || "a team member";
    }
    if (orgEl) {
      orgEl.textContent = invite?.organisation_name || "this organisation";
    }
    if (roleEl) {
      roleEl.textContent = invite?.role || "member";
    }
  }

  async function initialiseInviteWelcome() {
    await window.BB_APP?.coreReady;
    await ensureAuthModalReady();

    const inviteFlow = window.BBInviteFlow;
    if (!inviteFlow) {
      setStatus("Invite flow is unavailable. Please try again.", "error");
      return;
    }

    const token = inviteFlow.getInviteToken();
    if (!token) {
      setStatus("Invite link is missing a token.", "error");
      return;
    }

    const urlParams = new URLSearchParams(window.location.search);
    if (urlParams.get("auth_error")) {
      setStatus(
        "We couldn’t complete that social sign-in. Please try another sign-in method.",
        "error"
      );
      showInlineAuthForm("login");
      return;
    }

    try {
      const invite = await inviteFlow.fetchInvitePreview(token);
      renderInviteDetails(invite);
    } catch (error) {
      setStatus(error.message || "Failed to load invite details.", "error");
      return;
    }

    setStatus("Checking your session…");

    await inviteFlow.handleInviteTokenFlow({
      redirectTo: "/welcome",
      onAuthRequired: () => {
        showInlineAuthForm("login");
        setStatus("Sign in or create an account to continue.", "info");
      },
      onAccepted: () => {
        setStatus("Invite accepted. Redirecting to welcome…", "success");
      },
      onError: (error) => {
        showInlineAuthForm("login");
        setStatus(error.message || "Failed to accept invite.", "error");
      },
    });
  }

  document.addEventListener("DOMContentLoaded", () => {
    initialiseInviteWelcome().catch((error) => {
      console.error("Failed to initialise invite welcome page:", error);
      setStatus("Something went wrong loading the invite flow.", "error");
    });
  });
})();
