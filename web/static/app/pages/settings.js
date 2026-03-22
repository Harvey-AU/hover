/**
 * pages/settings.js — settings page module entrypoint
 *
 * Phase 5: orchestrates settings sections as ES modules. Co-exists with
 * remaining legacy bb-* scripts during migration.
 *
 * Loading contract (settings.html):
 *   1. /js/bb-bootstrap.js         — sets window.BB_APP
 *   2. /js/core.js defer           — Supabase init, window.BBAuth, window.BBB_CONFIG
 *   3. <script type="module">      — this file (runs after all deferred scripts)
 *
 * Responsibilities:
 *   - Wait for core readiness, then initialise settings sections
 *   - Section navigation (hash routing, active link, tab groups)
 *   - Coordinate data refresh across all sections
 *   - Register hover-* Web Components used on settings page
 *
 * What this does NOT touch yet (still handled by legacy scripts):
 *   - Auth modal and session management (auth.js, bb-data-binder.js)
 *   - Global nav rendering (bb-global-nav.js)
 *   - All settings sections (bb-settings.js) — migrated incrementally
 */

import { showToast } from "/app/components/hover-toast.js";

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

function setupNavigation() {
  setActiveSettingsLink();
  setActiveSection();
  window.addEventListener("hashchange", setActiveSection);
  window.addEventListener("popstate", () => {
    setActiveSettingsLink();
    setActiveSection();
  });
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

// ── Bootstrap ──────────────────────────────────────────────────────────────────

let _initialised = false;

async function init() {
  if (_initialised) return;
  _initialised = true;

  // Navigation is currently handled by bb-settings.js (legacy).
  // The functions above are ready to take over once bb-settings.js is removed
  // in Step 8. Until then, this module is a passive shell that section
  // modules will import from.
  //
  // As sections are migrated (Steps 2–8), their init calls move here.
}

// ── Entry point ────────────────────────────────────────────────────────────────

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () =>
    init().catch(console.error)
  );
} else {
  init().catch(console.error);
}
