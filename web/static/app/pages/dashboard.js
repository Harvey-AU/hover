/**
 * pages/dashboard.js — dashboard module entrypoint
 *
 * Phase 3: registers shared Web Components and provides the stats/jobs
 * rendering layer for the dashboard surface. Co-exists with remaining
 * legacy bb-* scripts (bb-data-binder, bb-global-nav, integrations).
 *
 * Loading contract (dashboard.html):
 *   1. /config.js              — sets window.BBB_CONFIG
 *   2. /js/core.js defer       — Supabase init, window.BBAuth, window.BBB_CONFIG
 *   3. Supabase SDK            — loaded by core.js
 *   4. <script type="module">  — this file (runs after all deferred scripts)
 *
 * Responsibilities:
 *   - Register hover-* Web Components for use anywhere in the page
 *   - Render the jobs list using hover-data-table + hover-status-pill
 *   - Render stats cards using shared formatters
 *   - Handle create-job / close-create-job-modal / refresh-dashboard actions
 *   - restart-job and cancel-job actions
 *
 * What this does NOT touch (still handled by legacy scripts):
 *   - Auth modal and session management (auth.js, bb-data-binder.js)
 *   - Org switching (bb-global-nav.js, bb-data-binder.js)
 *   - Integrations (bb-slack.js, bb-webflow.js, bb-google.js)
 *   - Job creation form submission (bb-auth-extension.js handleDashboardJobCreation)
 *   - Admin functions (bb-admin.js)
 */

import { get, post, put } from "/app/lib/api-client.js";
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

  // Wire action buttons — refresh, create-job modal, close-create-job-modal
  document.addEventListener("click", (e) => {
    const el = e.target.closest("[bbb-action]");
    if (!el) return;
    const action = el.getAttribute("bbb-action");
    if (action === "refresh-dashboard") {
      e.preventDefault();
      refresh();
    } else if (action === "create-job") {
      e.preventDefault();
      openCreateJobModal();
    } else if (action === "close-create-job-modal") {
      e.preventDefault();
      closeCreateJobModal();
    }
  });

  // Initial render
  await refresh();

  // Subscribe to realtime job updates (falls back to 10 s polling when
  // Supabase realtime is unavailable, e.g. on preview branches).
  let unsubscribe = null;
  function startSubscription() {
    if (unsubscribe) unsubscribe();
    const orgId = window.BB_ACTIVE_ORG?.id;
    unsubscribe = subscribeToJobUpdates(orgId, () => refresh());
  }
  startSubscription();

  // Re-subscribe and refresh when the active org changes.
  document.addEventListener("bb:org-switched", () => {
    refresh();
    startSubscription();
  });
}

// ── Refresh ────────────────────────────────────────────────────────────────────

async function refresh() {
  await Promise.all([refreshStats(), refreshJobs()]);
}

// ── Stats ──────────────────────────────────────────────────────────────────────

async function refreshStats() {
  // Gate behind session — avoids a 401 when the module runs before core.js
  // has signed in.
  const token = await waitForSession();
  if (!token) return;
  try {
    const tzOffset = new Date().getTimezoneOffset();
    // api-client auto-unwraps the { status, data } envelope
    const data = await get(
      `/v1/dashboard/stats?range=${currentRange}&tzOffset=${tzOffset}`
    );
    const stats = data?.stats;
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

// Column definitions are stable — defined once so the table header is never
// rebuilt on refresh. Only rows are swapped in place via table.rows = [...].
const JOB_COLUMNS = [
  {
    key: "domain",
    label: "Domain",
    render: (val, row) => {
      const name = val || row.domains?.name || "—";
      const span = document.createElement("span");
      span.textContent = name;
      return span;
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
    render: (val) => (val != null ? formatDuration(Number(val) * 1000) : "—"),
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
        "processing",
        "cancelling",
      ].includes(status);
      const isDone = ["completed", "failed", "cancelled"].includes(status);

      const view = document.createElement("a");
      view.href = `/jobs/${val}`;
      view.className = "bb-job-link";
      view.textContent = "View";
      view.setAttribute("aria-label", "View job details");
      wrap.appendChild(view);

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
];

async function refreshJobs() {
  const container = document.querySelector(".bb-jobs-list");
  if (!container) return;

  // Create the table once — subsequent calls update rows in place to avoid
  // tearing down and rebuilding the DOM (which causes visible flicker).
  let table = container.querySelector("hover-data-table");
  if (!table) {
    table = createDataTable({
      columns: JOB_COLUMNS,
      rows: [],
      emptyMessage: "No jobs found.",
    });
    table.setAttribute("loading", "");
    container.appendChild(table);
  }

  // Wait for a valid Supabase session before fetching — the module may run
  // before core.js has called initialiseSupabase() and signed in.
  const token = await waitForSession();
  if (!token) {
    table.removeAttribute("loading");
    table.setAttribute("error", "Not signed in.");
    return;
  }

  try {
    const jobs = await fetchJobs({
      limit: 10,
      range: currentRange,
      include: "stats",
    });
    renderJobsTable(container, table, jobs);
  } catch {
    table.removeAttribute("loading");
    table.setAttribute("error", "Failed to load jobs.");
  }
}

function renderJobsTable(container, table, jobs) {
  // Hide legacy bbb-template cards — the binder may re-insert clones but
  // they will also be hidden because they inherit style="display:none" from
  // the template element.
  container
    .querySelectorAll("[bbb-template='job']")
    .forEach((el) => (el.style.display = "none"));

  table.removeAttribute("loading");
  table.removeAttribute("error");

  // Update rows in place — _renderBody() runs without touching the header,
  // so there is no flicker on polling updates.
  table.rows = jobs.map((job) => ({
    ...job,
    domain: job.domains?.name || job.domain || "",
  }));
}

// ── Create job modal ───────────────────────────────────────────────────────────

function openCreateJobModal() {
  const modal = document.getElementById("createJobModal");
  if (modal) modal.style.display = "flex";
}

function closeCreateJobModal() {
  const modal = document.getElementById("createJobModal");
  if (modal) modal.style.display = "none";
  const form = document.getElementById("createJobForm");
  if (form) {
    form.reset();
    const maxPages = document.getElementById("maxPages");
    if (maxPages) maxPages.value = "0";
  }
}

// ── Job actions ────────────────────────────────────────────────────────────────

async function restartJob(job) {
  try {
    await post("/v1/jobs", {
      domain: job.domains?.name || job.domain,
      max_pages: job.max_pages ?? 0,
      use_sitemap: true,
      find_links: job.find_links ?? true,
      concurrency: job.concurrency,
    });
    showToast("Job restarted.", { variant: "success" });
    await refresh();
  } catch (err) {
    showToast(`Failed to restart job: ${err.message}`, { variant: "error" });
  }
}

async function cancelJob(jobId) {
  try {
    await put(`/v1/jobs/${jobId}`, { action: "cancel" });
    showToast("Job cancelled.", { variant: "warning" });
    await refresh();
  } catch (err) {
    showToast(`Failed to cancel job: ${err.message}`, { variant: "error" });
  }
}

// ── Helpers ────────────────────────────────────────────────────────────────────

/**
 * Wait for window.supabase to be initialised and have an active session.
 * Returns the access token, or null if no session within the timeout.
 * @param {number} [timeoutMs=8000]
 * @returns {Promise<string|null>}
 */
function waitForSession(timeoutMs = 8000) {
  return new Promise((resolve) => {
    const start = Date.now();
    const check = async () => {
      try {
        const { data } = await window.supabase?.auth?.getSession();
        const token = data?.session?.access_token;
        if (token) {
          resolve(token);
          return;
        }
      } catch {
        /* not ready yet */
      }
      if (Date.now() - start > timeoutMs) {
        resolve(null);
        return;
      }
      setTimeout(check, 200);
    };
    check();
  });
}

// ── Entry point ────────────────────────────────────────────────────────────────

// Initialise after DOM is ready. waitForSession() inside refresh() handles
// the Supabase timing — no dependency on bb-bootstrap.js or BB_APP.whenReady.
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () =>
    init().catch(console.error)
  );
} else {
  init().catch(console.error);
}

// ── Legacy bridges ─────────────────────────────────────────────────────────────
// bb-auth-extension.js calls these globals after job creation.
// Expose them so the legacy script can close the modal and trigger a refresh
// without depending on bb-dashboard-actions.js.
window.closeCreateJobModal = closeCreateJobModal;
window.HoverDashboard = { refresh };
