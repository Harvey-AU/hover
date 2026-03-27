(function () {
  async function initialiseWelcomeTitle() {
    const titleEl = document.getElementById("welcomeTitle");
    if (!titleEl) return;

    try {
      if (window.BB_APP?.coreReady) {
        await window.BB_APP.coreReady;
      }
      if (window.BB_APP?.initialiseOrg) {
        await window.BB_APP.initialiseOrg();
      }

      const orgName = window.BB_ACTIVE_ORG?.name;
      if (orgName) {
        titleEl.textContent = `Welcome to ${orgName}`;
      }
    } catch (error) {
      console.warn("Failed to set welcome organisation title:", error);
    }
  }

  document.addEventListener("DOMContentLoaded", () => {
    initialiseWelcomeTitle().catch((error) => {
      console.warn("Failed to initialise welcome title:", error);
    });
  });
})();
