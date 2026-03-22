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

// ── Section navigation ─────────────────────────────────────────────────────────

const SECTION_MAP = {
  "/settings": "account",
  "/settings/account": "account",
  "/settings/team": "team",
  "/settings/plans": "plans",
  "/settings/billing": "billing",
  "/settings/notifications": "notifications",
  "/settings/analytics": "analytics",
  "/settings/auto-crawl": "automated-jobs",
  "/settings/automation": "automated-jobs",
  "/settings/automated-jobs": "automated-jobs",
};

function setActiveSettingsLink() {
  const path = window.location.pathname.replace(/\/$/, "");
  const currentPath = path === "/settings" ? "/settings/account" : path;

  document.querySelectorAll(".settings-link").forEach((link) => {
    try {
      const linkPath = new URL(link.href).pathname.replace(/\/$/, "");
      if (linkPath === currentPath) {
        link.classList.add("active");
        link.setAttribute("aria-current", "page");
      } else {
        link.classList.remove("active");
        link.removeAttribute("aria-current");
      }
    } catch {
      link.classList.remove("active");
      link.removeAttribute("aria-current");
    }
  });
}

function resolveTargetSectionId() {
  const hash = window.location.hash.replace("#", "");
  if (hash) {
    const hashTarget = document.getElementById(hash);
    if (hashTarget) {
      const section = hashTarget.closest(".settings-section");
      if (section?.id) return section.id;
    }
  }
  const path = window.location.pathname.replace(/\/$/, "");
  return SECTION_MAP[path] || "account";
}

function activateTabGroup(sectionId, tabAttribute, panelId) {
  const section = document.getElementById(sectionId);
  if (!section) return;

  section.querySelectorAll(".settings-tab-panel").forEach((panel) => {
    const isActive = panel.id === panelId;
    panel.classList.toggle("active", isActive);
    panel.setAttribute("aria-hidden", isActive ? "false" : "true");
  });

  section.querySelectorAll(`.settings-tab[${tabAttribute}]`).forEach((tab) => {
    const isActive = tab.getAttribute(tabAttribute) === panelId;
    tab.classList.toggle("active", isActive);
    tab.setAttribute("aria-selected", isActive ? "true" : "false");
    tab.setAttribute("tabindex", isActive ? "0" : "-1");
  });
}

function activatePlanTab(panelId) {
  activateTabGroup("plans", "data-tab-target", panelId);
}

function activateAutomationTab(panelId) {
  activateTabGroup("automated-jobs", "data-auto-crawl-tab-target", panelId);
}

function activateTabFromHash() {
  const hash = window.location.hash.replace("#", "");
  if (!hash) return;
  const target = document.getElementById(hash);
  const panel = target?.closest(".settings-tab-panel");
  if (panel?.id) {
    if (panel.id.startsWith("planTab")) {
      activatePlanTab(panel.id);
    } else if (panel.id.startsWith("autoCrawl")) {
      activateAutomationTab(panel.id);
    }
  }
}

function setActiveSection() {
  const targetId = resolveTargetSectionId();
  const hash = window.location.hash.replace("#", "");
  const hashTarget = hash ? document.getElementById(hash) : null;

  document.querySelectorAll(".settings-section").forEach((section) => {
    section.classList.toggle("active", section.id === targetId);
  });

  if (hashTarget) {
    hashTarget.scrollIntoView({ behavior: "smooth", block: "start" });
  } else {
    const target = document.getElementById(targetId);
    if (target) target.scrollIntoView({ behavior: "smooth", block: "start" });
  }

  if (targetId === "plans" && !window.location.hash) {
    activatePlanTab("planTabCurrent");
  }
  if (targetId === "automated-jobs" && !window.location.hash) {
    activateAutomationTab("autoCrawlWebflowPanel");
  }
  activateTabFromHash();
}

function setupPlanTabs() {
  const section = document.getElementById("plans");
  if (!section) return;
  section.querySelectorAll(".settings-tab[data-tab-target]").forEach((tab) => {
    tab.addEventListener("click", () => {
      const targetId = tab.dataset.tabTarget;
      if (targetId) activatePlanTab(targetId);
    });
  });
}

function setupAutomationTabs() {
  const section = document.getElementById("automated-jobs");
  if (!section) return;
  section
    .querySelectorAll(".settings-tab[data-auto-crawl-tab-target]")
    .forEach((tab) => {
      tab.addEventListener("click", () => {
        const targetId = tab.dataset.autoCrawlTabTarget;
        if (targetId) activateAutomationTab(targetId);
      });
    });
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

  // Wait for supabase session to be available.
  if (!window.supabase?.auth) {
    await new Promise((resolve) => setTimeout(resolve, 500));
  }

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
