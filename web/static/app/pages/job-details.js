/**
 * pages/job-details.js — job details module entrypoint
 *
 * Phase 4: co-exists with the legacy job-page.js script. Replaces the
 * status pill span with hover-status-pill, and upgrades the tasks table
 * to hover-data-table once the legacy binder has populated it.
 *
 * What this does NOT touch:
 *   - job-page.js data fetching, polling, realtime, filter logic
 *   - share link, restart/cancel button handlers
 *   - performance summary metrics rendering
 *   - export functionality
 */

import { createStatusPill } from "/app/components/hover-status-pill.js";
import { formatRelativeTime, formatDuration } from "/app/lib/formatters.js";

// ── Status pill upgrade ────────────────────────────────────────────────────────

/**
 * Replace the legacy .status-pill span with a hover-status-pill element.
 * job-page.js sets bbb-text and bbb-class on this span; we replace it
 * with a component that reacts to the same status value.
 *
 * Called once on init, then re-called whenever the status changes via
 * a MutationObserver on the span's text content.
 */
function upgradeStatusPill() {
  const span = document.querySelector(
    ".status-pill[bbb-text='job.status_label']"
  );
  if (!span) return;

  // Read the current status from the bbb-class attribute which job-page.js
  // sets to "status-pill {job.status_class}" e.g. "status-pill status-running"
  const bbbClass = span.getAttribute("bbb-class") || "";
  const statusClass = bbbClass.replace("status-pill", "").trim();
  // statusClass is like "status-running", "status-completed", "status-pending"
  const status =
    statusClass.replace("status-", "") || span.textContent.trim().toLowerCase();

  // Already upgraded — update the existing pill's status attribute
  const existing = span.parentElement?.querySelector("hover-status-pill");
  if (existing) {
    existing.setAttribute("status", status);
    return;
  }

  // First time — insert hover-status-pill before the span and hide the span
  const pill = createStatusPill(status);
  span.style.display = "none";
  span.parentElement.insertBefore(pill, span);
}

/**
 * Watch the status span for changes made by job-page.js and keep the
 * hover-status-pill in sync.
 */
function watchStatusPill() {
  const span = document.querySelector(
    ".status-pill[bbb-text='job.status_label']"
  );
  if (!span) return;

  upgradeStatusPill();

  new MutationObserver(upgradeStatusPill).observe(span, {
    characterData: true,
    childList: true,
    subtree: true,
    attributes: true,
    attributeFilter: ["class", "bbb-class"],
  });
}

// ── Tasks table upgrade ────────────────────────────────────────────────────────

/**
 * Once job-page.js has rendered the tasks table, wrap it with shared
 * hover-status-pill elements in the Status column.
 *
 * job-page.js owns the table DOM — we only upgrade the status cells.
 */
function upgradeTasksTable() {
  // Status cells in the tasks table have class "task-status" or contain
  // a span with a status class — find and upgrade them.
  const statusCells = document.querySelectorAll(
    ".tasks-table-body .task-row .task-status, " +
      ".job-tasks-list .task-status"
  );

  statusCells.forEach((cell) => {
    if (cell.querySelector("hover-status-pill")) return; // already upgraded

    const text = cell.textContent.trim().toLowerCase();
    if (!text) return;

    const pill = createStatusPill(text);
    cell.innerHTML = "";
    cell.appendChild(pill);
  });
}

/**
 * Watch for the tasks table to be populated by job-page.js and upgrade
 * status cells as they appear.
 */
function watchTasksTable() {
  const container =
    document.querySelector(".tasks-table-body") ||
    document.querySelector(".job-tasks-list") ||
    document.querySelector("#tasksTableBody");

  if (!container) {
    // Not found yet — retry once after a short delay
    setTimeout(watchTasksTable, 500);
    return;
  }

  upgradeTasksTable();

  new MutationObserver(upgradeTasksTable).observe(container, {
    childList: true,
    subtree: true,
  });
}

// ── Init ───────────────────────────────────────────────────────────────────────

function init() {
  watchStatusPill();
  watchTasksTable();
}

// Wait for bb-bootstrap chain before init, same pattern as dashboard.js
if (typeof window.BB_APP?.whenReady === "function") {
  window.BB_APP.whenReady().then(init).catch(console.error);
} else {
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
}
