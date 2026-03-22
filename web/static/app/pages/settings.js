/**
 * pages/settings.js — settings page module entrypoint
 *
 * Orchestrates settings sections as ES modules. Co-exists with bb-settings.js
 * which still handles global UI (org switcher, notifications, user menu,
 * create org modal, integrations, admin section).
 *
 * Coordination with bb-settings.js:
 *   1. settings.html sets window.__ES_SETTINGS = true (sync script)
 *   2. bb-settings.js checks the flag and skips migrated section init
 *   3. bb-settings.js calls window.__bbSettingsReady() when done
 *   4. This module waits for that signal, then inits migrated sections
 *   5. bb-settings.js calls window.__esRefreshSections() on org-switch
 */

import {
  loadAccountDetails,
  setupAccountActions,
  getAccountState,
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
import { showToast as _showToast } from "/app/components/hover-toast.js";

function toast(variant, message) {
  _showToast(message, { variant });
}

// Navigation (tabs, section routing) is handled by bb-settings.js.
// When bb-settings.js is fully retired, move navigation here.

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
  } catch (err) {
    console.error("ES module refresh failed:", err);
  }
}

// Expose for bb-settings.js refreshSettingsData()
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
}

// ── Entry point ────────────────────────────────────────────────────────────────

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () =>
    init().catch(console.error)
  );
} else {
  init().catch(console.error);
}
