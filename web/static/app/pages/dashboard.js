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
 *   - Render the jobs list using hover-job-card
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
import { createJobCard } from "/app/components/hover-job-card.js";
import { showToast } from "/app/components/hover-toast.js";
import { formatCount } from "/app/lib/formatters.js";

// ── State ──────────────────────────────────────────────────────────────────────

let currentRange = "today";

// ── Bootstrap ──────────────────────────────────────────────────────────────────

/**
 * Initialise the dashboard module layer.
 * Called once auth and org state are confirmed ready.
 */
async function init() {
  // Suppress legacy binder-inserted job cards — the binder clones
  // bbb-template="job" elements and inserts them as siblings; hide on arrival.
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

/** @type {Map<string, HoverJobCard>} jobId → card element, for in-place updates */
const _jobCards = new Map();

async function refreshJobs() {
  const container = document.querySelector(".bb-jobs-list");
  if (!container) return;

  const token = await waitForSession();
  if (!token) return;

  try {
    const jobs = await fetchJobs({
      limit: 10,
      range: currentRange,
      include: "stats",
    });
    renderJobCards(container, jobs);
  } catch {
    // Non-fatal — existing cards stay visible
  }
}

function renderJobCards(container, jobs) {
  // Hide legacy bbb-template cards inserted by bb-data-binder
  container
    .querySelectorAll("[bbb-template='job'], .bb-job-card")
    .forEach((el) => (el.style.display = "none"));

  // Empty state
  if (jobs.length === 0) {
    _jobCards.forEach((card) => card.remove());
    _jobCards.clear();

    let empty = container.querySelector(".jobs-empty-state");
    if (!empty) {
      empty = document.createElement("p");
      empty.className = "jobs-empty-state detail";
      empty.textContent = "No jobs yet.";
      container.appendChild(empty);
    }
    return;
  }

  // Remove empty state if present
  container.querySelector(".jobs-empty-state")?.remove();

  // Track which job IDs are in the new response
  const incoming = new Set(jobs.map((j) => j.id));

  // Remove cards no longer in the list
  _jobCards.forEach((card, id) => {
    if (!incoming.has(id)) {
      card.remove();
      _jobCards.delete(id);
    }
  });

  // Update existing cards in-place, append new ones in order
  jobs.forEach((job, index) => {
    const existing = _jobCards.get(job.id);
    if (existing) {
      // In-place update — no DOM removal, no flicker
      existing.job = job;
      // Ensure correct order
      const cards = Array.from(container.querySelectorAll("hover-job-card"));
      if (cards[index] !== existing)
        container.insertBefore(existing, cards[index] ?? null);
    } else {
      const card = createJobCard(job, { context: "dashboard" });
      card.dataset.jobId = job.id;

      // Navigation — "All" button and "View all X" in issue tables
      card.addEventListener("hover-job-card:view", (e) => {
        window.location.href = e.detail.path;
      });

      // Export
      card.addEventListener("hover-job-card:export", (e) => {
        exportJob(e.detail.jobId).catch((err) =>
          showToast(`Export failed: ${err.message}`, { variant: "error" })
        );
      });

      // Restart
      card.addEventListener("hover-job-card:restart", (e) => {
        restartJob(e.detail.job).catch((err) =>
          showToast(`Restart failed: ${err.message}`, { variant: "error" })
        );
      });

      // Cancel
      card.addEventListener("hover-job-card:cancel", (e) => {
        cancelJob(e.detail.jobId).catch((err) =>
          showToast(`Cancel failed: ${err.message}`, { variant: "error" })
        );
      });

      // Insert at correct position
      const cards = Array.from(container.querySelectorAll("hover-job-card"));
      container.insertBefore(card, cards[index] ?? null);
      _jobCards.set(job.id, card);
    }
  });
}

async function exportJob(jobId) {
  try {
    const data = await get(`/v1/jobs/${encodeURIComponent(jobId)}/export`, {
      headers: { Accept: "application/json" },
    });
    const tasks = Array.isArray(data?.tasks) ? data.tasks : [];
    if (!tasks.length) {
      showToast("No tasks to export.", { variant: "warning" });
      return;
    }

    const keys = Object.keys(tasks[0]);
    const csv = [
      keys.join(","),
      ...tasks.map((t) => keys.map((k) => csvEscape(t[k])).join(",")),
    ].join("\n");
    const blob = new Blob([csv], { type: "text/csv" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `job-${jobId}.csv`;
    a.click();
    URL.revokeObjectURL(url);
    showToast("Export downloaded.", { variant: "success" });
  } catch (err) {
    throw err;
  }
}

function csvEscape(val) {
  if (val == null) return "";
  const str = String(val);
  return str.includes(",") || str.includes('"') || str.includes("\n")
    ? `"${str.replace(/"/g, '""')}"`
    : str;
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
