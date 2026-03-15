/**
 * pages/dashboard.js — dashboard module entrypoint
 *
 * Phase 3: registers shared Web Components and provides the stats/jobs
 * rendering layer for the dashboard surface. Co-exists with legacy bb-*
 * scripts during migration — does not replace them wholesale yet.
 *
 * Loading contract (dashboard.html):
 *   1. /config.js              — sets window.BBB_CONFIG
 *   2. /js/bb-bootstrap.js     — BB_APP.whenReady() (legacy, still needed)
 *   3. /js/core.js defer       — Supabase init, window.BBAuth, window.BBB_CONFIG
 *   4. Supabase SDK            — loaded by core.js
 *   5. <script type="module">  — this file (runs after all deferred scripts)
 *
 * Responsibilities:
 *   - Register hover-* Web Components for use anywhere in the page
 *   - Render the jobs list using hover-data-table + hover-status-pill
 *   - Render stats cards using shared formatters
 *   - Subscribe to job updates via shared webflow-jobs.js
 *   - Replace bb-dashboard-actions.js job rendering with shared components
 *
 * What this does NOT touch (still handled by legacy scripts):
 *   - Auth modal and session management (auth.js, bb-data-binder.js)
 *   - Org switching (bb-global-nav.js, bb-data-binder.js)
 *   - Integrations (bb-slack.js, bb-webflow.js, bb-google.js)
 *   - Job creation form (bb-auth-extension.js handleDashboardJobCreation)
 *   - Admin functions (bb-admin.js)
 */

import { fetchJobs, subscribeToJobUpdates } from "/app/pages/webflow-jobs.js";
import { createStatusPill } from "/app/components/hover-status-pill.js";
import { createDataTable } from "/app/components/hover-data-table.js";
import { showToast } from "/app/components/hover-toast.js";
import {
  formatRelativeTime,
  formatDuration,
  formatCount,
} from "/app/lib/formatters.js";

// ── State ──────────────────────────────────────────────────────────────────────

let unsubscribeRealtime = null;
let currentRange = "today";

// ── Bootstrap ──────────────────────────────────────────────────────────────────

/**
 * Initialise the dashboard module layer.
 * Called once auth and org state are confirmed ready.
 */
async function init() {
  // Suppress legacy binder-inserted job cards so our hover-data-table owns
  // the jobs list. The binder clones bbb-template="job" elements and inserts
  // them as siblings — we hide them as they arrive.
  const jobsList = document.querySelector(".bb-jobs-list");
  if (jobsList) {
    new MutationObserver((mutations) => {
      mutations.forEach((m) => {
        m.addedNodes.forEach((node) => {
          if (
            node instanceof HTMLElement &&
            node.classList.contains("bb-job-card")
          ) {
            node.style.display = "none";
          }
        });
      });
    }).observe(jobsList, { childList: true });
  }

  // Wire date range selector
  const dateRange = document.getElementById("dateRange");
  if (dateRange) {
    dateRange.addEventListener("change", (e) => {
      currentRange = e.target.value;
      refresh();
    });
  }

  // Wire refresh button
  document
    .querySelectorAll("[bbb-action='refresh-dashboard']")
    .forEach((btn) => {
      btn.addEventListener("click", refresh);
    });

  // Initial render
  await refresh();

  // Subscribe to realtime job updates once org is available
  waitForOrg((orgId) => {
    if (unsubscribeRealtime) unsubscribeRealtime();
    unsubscribeRealtime = subscribeToJobUpdates(orgId, refresh);
  });

  // Re-subscribe on org switch
  document.addEventListener("bb:org-switched", (e) => {
    const orgId = e.detail?.organisation?.id;
    if (!orgId) return;
    if (unsubscribeRealtime) unsubscribeRealtime();
    unsubscribeRealtime = subscribeToJobUpdates(orgId, refresh);
    refresh();
  });

  // Clean up on unload
  window.addEventListener("beforeunload", () => {
    if (unsubscribeRealtime) unsubscribeRealtime();
  });
}

// ── Refresh ────────────────────────────────────────────────────────────────────

async function refresh() {
  await Promise.all([refreshStats(), refreshJobs()]);
}

// ── Stats ──────────────────────────────────────────────────────────────────────

async function refreshStats() {
  try {
    const tzOffset = new Date().getTimezoneOffset();
    const res = await fetch(
      `/v1/dashboard/stats?range=${currentRange}&tzOffset=${tzOffset}`,
      { headers: await authHeaders() }
    );
    if (!res.ok) return;
    const { stats } = await res.json();
    if (!stats) return;

    setStatCard("stats.total_jobs", formatCount(stats.total_jobs));
    setStatCard("stats.running_jobs", formatCount(stats.running_jobs));
    setStatCard("stats.completed_jobs", formatCount(stats.completed_jobs));
    setStatCard("stats.failed_jobs", formatCount(stats.failed_jobs));
  } catch {
    // Non-fatal — stats cards stay at previous values
  }
}

/**
 * Update a stat card value by its bbb-text attribute selector.
 * Falls back gracefully when the element doesn't exist.
 */
function setStatCard(key, value) {
  const el = document.querySelector(`[bbb-text="${key}"]`);
  if (el) el.textContent = value;
}

// ── Jobs list ──────────────────────────────────────────────────────────────────

async function refreshJobs() {
  const container = document.querySelector(".bb-jobs-list");
  if (!container) return;

  // Show loading state on the existing table if present
  const existingTable = container.querySelector("hover-data-table");
  if (existingTable) existingTable.setAttribute("loading", "");

  try {
    const tzOffset = new Date().getTimezoneOffset();
    const jobs = await fetchJobs({
      limit: 10,
      range: currentRange,
      include: "stats",
    });

    renderJobsTable(container, jobs);
  } catch (err) {
    const existingTable = container.querySelector("hover-data-table");
    if (existingTable) {
      existingTable.removeAttribute("loading");
      existingTable.setAttribute("error", "Failed to load jobs.");
    }
  }
}

function renderJobsTable(container, jobs) {
  // Hide legacy bbb-template cards — the binder may re-insert clones but
  // they will also be hidden because they inherit style="display:none" from
  // the template element.
  container
    .querySelectorAll("[bbb-template='job']")
    .forEach((el) => (el.style.display = "none"));

  // Remove empty state if present
  container
    .querySelectorAll(".bb-jobs-empty-state")
    .forEach((el) => el.remove());

  // Remove existing hover table before re-render
  container.querySelectorAll("hover-data-table").forEach((el) => el.remove());

  if (!jobs.length) {
    const empty = document.createElement("div");
    empty.className = "bb-jobs-empty-state";
    empty.style.cssText =
      "text-align:center;padding:40px 20px;color:var(--text-colour--secondary,#6b7280)";
    empty.textContent = "No jobs yet. Use the form above to start a crawl.";
    container.appendChild(empty);
    return;
  }

  const table = createDataTable({
    columns: [
      {
        key: "domain",
        label: "Domain",
        render: (val, row) => {
          const name = val || row.domains?.name || "—";
          const a = document.createElement("a");
          a.href = `/jobs/${row.id}`;
          a.className = "bb-job-link";
          a.textContent = name;
          return a;
        },
      },
      {
        key: "status",
        label: "Status",
        render: (val) => createStatusPill(String(val || "")),
      },
      {
        key: "progress",
        label: "Progress",
        render: (val, row) => {
          const pct = Math.round(Number(val) || 0);
          const done = formatCount(row.completed_tasks);
          const total = formatCount(row.total_tasks);
          const span = document.createElement("span");
          span.textContent = `${done} / ${total} (${pct}%)`;
          return span;
        },
      },
      {
        key: "started_at",
        label: "Started",
        render: (val) => (val ? formatRelativeTime(String(val)) : "—"),
      },
      {
        key: "duration_seconds",
        label: "Duration",
        render: (val) =>
          val != null ? formatDuration(Number(val) * 1000) : "—",
      },
      {
        key: "id",
        label: "Actions",
        render: (val, row) => {
          const wrap = document.createElement("div");
          wrap.style.cssText = "display:flex;gap:8px;align-items:center";

          const status = String(row.status || "");
          const isActive = [
            "pending",
            "running",
            "queued",
            "initializing",
          ].includes(status);
          const isDone = ["completed", "failed", "cancelled"].includes(status);

          if (isDone) {
            const restart = document.createElement("button");
            restart.className = "bb-job-link";
            restart.textContent = "Restart";
            restart.setAttribute("aria-label", "Restart this job");
            restart.addEventListener("click", () => restartJob(row));
            wrap.appendChild(restart);
          }

          if (isActive) {
            const cancel = document.createElement("button");
            cancel.className = "bb-job-link";
            cancel.textContent = "Cancel";
            cancel.setAttribute("aria-label", "Cancel this job");
            cancel.addEventListener("click", () => cancelJob(String(val)));
            wrap.appendChild(cancel);
          }

          return wrap;
        },
      },
    ],
    rows: jobs.map((job) => ({
      ...job,
      domain: job.domains?.name || job.domain || "",
    })),
    emptyMessage: "No jobs found.",
  });

  container.appendChild(table);
}

// ── Job actions ────────────────────────────────────────────────────────────────

async function restartJob(job) {
  try {
    const res = await fetch("/v1/jobs", {
      method: "POST",
      headers: { ...(await authHeaders()), "Content-Type": "application/json" },
      body: JSON.stringify({
        domain: job.domains?.name || job.domain,
        max_pages: job.max_pages ?? 0,
        use_sitemap: true,
        find_links: job.find_links ?? true,
        concurrency: job.concurrency,
      }),
    });
    if (!res.ok) throw new Error(`${res.status}`);
    showToast("Job restarted.", { variant: "success" });
    await refresh();
  } catch (err) {
    showToast(`Failed to restart job: ${err.message}`, { variant: "error" });
  }
}

async function cancelJob(jobId) {
  try {
    const res = await fetch(`/v1/jobs/${jobId}`, {
      method: "PUT",
      headers: { ...(await authHeaders()), "Content-Type": "application/json" },
      body: JSON.stringify({ action: "cancel" }),
    });
    if (!res.ok) throw new Error(`${res.status}`);
    showToast("Job cancelled.", { variant: "warning" });
    await refresh();
  } catch (err) {
    showToast(`Failed to cancel job: ${err.message}`, { variant: "error" });
  }
}

// ── Helpers ────────────────────────────────────────────────────────────────────

/** Get auth headers from the active Supabase session. */
async function authHeaders() {
  try {
    const { data } = await window.supabase?.auth?.getSession();
    const token = data?.session?.access_token;
    return token ? { Authorization: `Bearer ${token}` } : {};
  } catch {
    return {};
  }
}

/**
 * Wait for window.BB_ACTIVE_ORG to be available, then call cb(orgId).
 * Polls at 250ms intervals for up to 10s.
 */
function waitForOrg(cb) {
  const orgId = window.BB_ACTIVE_ORG?.id;
  if (orgId) {
    cb(orgId);
    return;
  }
  let attempts = 0;
  const timer = setInterval(() => {
    const id = window.BB_ACTIVE_ORG?.id;
    if (id || attempts++ > 40) {
      clearInterval(timer);
      if (id) cb(id);
    }
  }, 250);
}

// ── Entry point ────────────────────────────────────────────────────────────────

// Wait for the legacy bb-bootstrap chain to complete, then init.
// This ensures Supabase and auth state are ready before we fetch data.
if (typeof window.BB_APP?.whenReady === "function") {
  window.BB_APP.whenReady().then(init).catch(console.error);
} else {
  // bb-bootstrap not present — wait for DOMContentLoaded then init directly
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
}
