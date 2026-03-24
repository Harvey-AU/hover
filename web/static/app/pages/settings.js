/**
 * pages/settings.js — settings page orchestrator
 *
 * Imports section modules from lib/settings/ and wires them into
 * settings.html containers. Each section module is surface-agnostic
 * (accepts a container param) so the same logic can render in the
 * Webflow extension or other surfaces.
 */

import {
  loadAccountDetails,
  setupAccountActions,
} from "/app/lib/settings/account.js";
import {
  loadMembers,
  loadInvites,
  setupTeamActions,
  getTeamState,
} from "/app/lib/settings/team.js";
import {
  loadPlansAndUsage,
  loadUsageHistory,
} from "/app/lib/settings/plans.js";
import {
  loadSchedules,
  setupSchedulesActions,
} from "/app/lib/settings/schedules.js";
import {
  setupSlackIntegration,
  loadSlackConnections,
  handleSlackOAuthCallback,
} from "/app/lib/settings/integrations/slack.js";
import {
  setupWebflowIntegration,
  loadWebflowConnections,
  handleWebflowOAuthCallback,
} from "/app/lib/settings/integrations/webflow.js";
import {
  setupGoogleIntegration,
  loadGoogleConnections,
  handleGoogleOAuthCallback,
} from "/app/lib/settings/integrations/google.js";
import { initAdminResetButton } from "/app/lib/admin.js";
import { showToast as _showToast } from "/app/components/hover-toast.js";

function toast(variant, message) {
  _showToast(message, { variant });
}

// ── Section containers ──────────────────────────────────────────────────────────

function getContainers() {
  return {
    account: document.getElementById("account"),
    team: document.getElementById("team"),
    plans: document.getElementById("plans"),
    schedules: document.getElementById("automated-jobs"),
  };
}

// ── Refresh (called by bb-settings.js on org-switch) ────────────────────────────

async function refreshSections() {
  const c = getContainers();
  const teamState = getTeamState();
  try {
    await loadAccountDetails(c.account);
    await loadMembers(c.team);
    await loadInvites(c.team);
    await loadPlansAndUsage(c.plans, {
      currentUserRole: teamState.currentUserRole,
    });
    await loadUsageHistory(c.plans);
    await loadSchedules(c.schedules);
    await loadSlackConnections();
    await loadWebflowConnections();
    await loadGoogleConnections();
  } catch (err) {
    console.error("ES module refresh failed:", err);
  }
}

// Expose for org-switch refresh (called by bb-settings.js).
window.__esRefreshSections = refreshSections;

// ── Bootstrap ──────────────────────────────────────────────────────────────────

let _initialised = false;

async function init() {
  if (_initialised) return;
  _initialised = true;

  // Wait for core readiness (Supabase, org init, dataBinder).
  if (window.BB_APP?.coreReady) {
    await window.BB_APP.coreReady;
  }

  // Wait for supabase auth to be available (polling with timeout).
  const supabaseReady = async (maxWait = 5000, interval = 100) => {
    const start = Date.now();
    while (!window.supabase?.auth && Date.now() - start < maxWait) {
      await new Promise((r) => setTimeout(r, interval));
    }
  };
  await supabaseReady();

  const session = await window.supabase?.auth?.getSession?.();
  if (!session?.data?.session?.user) return;

  const c = getContainers();

  // Wire up event listeners for migrated sections.
  setupAccountActions(c.account, {
    onNameSaved: () => loadMembers(c.team),
  });
  setupTeamActions(c.team);
  setupSchedulesActions(c.schedules);

  // Load migrated section data.
  try {
    await loadAccountDetails(c.account);

    // Team must load first (sets currentUserRole for plans).
    await loadMembers(c.team);
    await loadInvites(c.team);

    const teamState = getTeamState();
    await loadPlansAndUsage(c.plans, {
      currentUserRole: teamState.currentUserRole,
    });
    await loadUsageHistory(c.plans);
    await loadSchedules(c.schedules);
  } catch (err) {
    console.error("Failed to initialise ES settings sections:", err);
    toast("error", "Some settings sections failed to load.");
  }

  // Integration setup and OAuth callbacks.
  setupSlackIntegration();
  setupWebflowIntegration();
  setupGoogleIntegration();
  handleSlackOAuthCallback();
  handleWebflowOAuthCallback();
  await handleGoogleOAuthCallback();

  // Load integration connections.
  try {
    await loadSlackConnections();
    await loadWebflowConnections();
    await loadGoogleConnections();
  } catch (err) {
    console.error("Failed to load integration connections:", err);
  }

  // Admin section (only visible to system_admin users).
  await initAdminResetButton("resetDbBtn", {
    containerSelector: "#adminGroup",
  });
}

// ── Entry point ────────────────────────────────────────────────────────────────

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () =>
    init().catch(console.error)
  );
} else {
  init().catch(console.error);
}
