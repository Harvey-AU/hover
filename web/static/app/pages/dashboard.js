/**
 * pages/dashboard.js — dashboard page orchestrator
 *
 * Owns all dashboard rendering and interaction: stats cards, job list
 * (hover-job-card), job creation form, org creation modal, admin
 * actions, and realtime subscriptions.
 *
 * No remaining legacy script dependencies.
 */

import { get, post, put, del } from "/app/lib/api-client.js";
import { fetchJobs, subscribeToJobUpdates } from "/app/pages/webflow-jobs.js";
import { createJobCard } from "/app/components/hover-job-card.js";
import { showToast } from "/app/components/hover-toast.js";
import { formatCount } from "/app/lib/formatters.js";
import { initCreateOrgModal } from "/app/lib/settings/organisations.js";
import { initAdminResetButton } from "/app/lib/admin.js";
import {
  ensureDomainByName,
  setupDomainSearchInput,
} from "/app/lib/domain-search.js";

// ── State ──────────────────────────────────────────────────────────────────────

let currentRange = "today";

// ── Bootstrap ──────────────────────────────────────────────────────────────────

/**
 * Initialise the dashboard module layer.
 * Called once auth and org state are confirmed ready.
 */
let _initialised = false;
async function init() {
  if (_initialised) return;
  _initialised = true;
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
    const el = e.target.closest("[gnh-action]");
    if (!el) return;
    const action = el.getAttribute("gnh-action");
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

  // Job creation forms (inline "Start Crawl" + modal "Create Job")
  for (const formId of ["dashboardJobForm", "createJobForm"]) {
    const form = document.getElementById(formId);
    if (form) form.addEventListener("submit", handleJobCreation);
  }

  // Domain search autocomplete
  const domainInput = document.getElementById("jobDomain");
  if (domainInput) {
    const container = domainInput.closest(".gnh-domain-search");
    setupDomainSearchInput({
      input: domainInput,
      container: container || domainInput.parentElement,
      clearOnSelect: false,
      onSelectDomain: (domain) => {
        domainInput.value = domain.name;
      },
      onCreateDomain: (domain) => {
        domainInput.value = domain.name;
      },
      onError: (message) => {
        showToast(message || "Failed to create domain.", { variant: "error" });
      },
    });
  }

  // Org creation modal
  initCreateOrgModal({ onCreated: () => refresh() });

  // Network monitoring
  setupNetworkMonitoring();

  // Initial render (waitForSession inside refresh handles Supabase timing)
  await refresh();

  // Admin section (must run after refresh so Supabase session is available)
  await initAdminResetButton("resetDbBtn", {
    containerSelector: "#adminGroup",
  });

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
  document.addEventListener("gnh:org-switched", () => {
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
 * Update a stat card value by its gnh-text attribute selector.
 * Falls back gracefully when the element doesn't exist.
 */
function setStatCard(key, value) {
  const el = document.querySelector(`[gnh-text="${key}"]`);
  if (el) el.textContent = value;
}

// ── Jobs list ──────────────────────────────────────────────────────────────────

/** @type {Map<string, HoverJobCard>} jobId → card element, for in-place updates */
const _jobCards = new Map();

async function refreshJobs() {
  const container = document.querySelector(".gnh-jobs-list");
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

// ── Job creation ────────────────────────────────────────────────────────────────

async function handleJobCreation(event) {
  event.preventDefault();
  const formData = new FormData(event.target);

  let domain = formData.get("domain");
  const maxPages = parseInt(formData.get("max_pages"));
  const concurrencyValue = formData.get("concurrency");
  const scheduleInterval = formData.get("schedule_interval_hours");

  if (!domain) {
    showToast("Domain is required", { variant: "error" });
    return;
  }

  // Ensure domain exists (creates if needed)
  try {
    const ensuredDomain = await ensureDomainByName(domain, {
      allowCreate: true,
    });
    if (ensuredDomain?.name) domain = ensuredDomain.name;
  } catch (error) {
    showToast(error.message || "Failed to create domain.", {
      variant: "error",
    });
    return;
  }

  const domainField = document.getElementById("jobDomain");
  if (domainField) domainField.value = domain;

  if (maxPages < 0 || maxPages > 10000) {
    showToast("Maximum pages must be between 0 and 10,000", {
      variant: "error",
    });
    return;
  }

  const requestBody = {
    domain,
    max_pages: maxPages,
    use_sitemap: true,
    find_links: true,
  };
  if (
    concurrencyValue &&
    concurrencyValue !== "" &&
    concurrencyValue !== "default"
  ) {
    requestBody.concurrency = parseInt(concurrencyValue);
  }

  try {
    if (scheduleInterval && scheduleInterval !== "") {
      const hours = parseInt(scheduleInterval);
      if (isNaN(hours) || ![6, 12, 24, 48].includes(hours)) {
        showToast(
          "Invalid schedule interval. Must be 6, 12, 24, or 48 hours.",
          {
            variant: "error",
          }
        );
        return;
      }

      const scheduler = await post("/v1/schedulers", {
        domain,
        schedule_interval_hours: hours,
        max_pages: maxPages,
        find_links: true,
        concurrency: requestBody.concurrency || 20,
      });

      try {
        await post("/v1/jobs", { ...requestBody, scheduler_id: scheduler.id });
      } catch (jobError) {
        console.error(
          "Failed to create initial job, cleaning up scheduler:",
          jobError
        );
        try {
          await del(`/v1/schedulers/${encodeURIComponent(scheduler.id)}`);
        } catch (cleanupError) {
          console.error("Failed to clean up scheduler:", cleanupError);
        }
        throw jobError;
      }

      closeCreateJobModal();
      showToast(`Scheduled job created for ${domain} (every ${hours} hours)`, {
        variant: "success",
      });
    } else {
      await post("/v1/jobs", requestBody);

      const df = document.getElementById("jobDomain");
      const mp = document.getElementById("maxPages");
      const si = document.getElementById("scheduleInterval");
      if (df) df.value = "";
      if (mp) mp.value = "0";
      if (si) si.value = "";

      closeCreateJobModal();
      showToast(`Job created for ${domain}`, { variant: "success" });
    }

    await refresh();
  } catch (error) {
    console.error("Failed to create job:", error);
    showToast(error.message || "Failed to create job. Please try again.", {
      variant: "error",
    });
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
    await put(`/v1/jobs/${encodeURIComponent(jobId)}`, { action: "cancel" });
    showToast("Job cancelled.", { variant: "warning" });
    await refresh();
  } catch (err) {
    showToast(`Failed to cancel job: ${err.message}`, { variant: "error" });
  }
}

// ── Network monitoring ──────────────────────────────────────────────────────────

function updateNetworkStatus() {
  const indicator = document.querySelector(".status-indicator");
  if (!indicator) return;
  if (navigator.onLine) {
    indicator.textContent = "";
    const dot = document.createElement("span");
    dot.className = "status-dot";
    const label = document.createElement("span");
    label.textContent = "Live";
    indicator.appendChild(dot);
    indicator.appendChild(label);
  } else {
    indicator.textContent = "";
    const dot = document.createElement("span");
    dot.className = "status-dot";
    dot.style.background = "#ef4444";
    const label = document.createElement("span");
    label.textContent = "Offline";
    indicator.appendChild(dot);
    indicator.appendChild(label);
  }
}

function setupNetworkMonitoring() {
  updateNetworkStatus();

  window.addEventListener("online", () => {
    updateNetworkStatus();
    showToast("Connection restored. Refreshing data...", {
      variant: "success",
    });
    setTimeout(() => refresh(), 500);
  });

  window.addEventListener("offline", () => {
    updateNetworkStatus();
    showToast("Connection lost. Some features may not work.", {
      variant: "error",
    });
  });
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
// the Supabase timing — no dependency on gnh-bootstrap.js or GNH_APP.whenReady.
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () =>
    init().catch(console.error)
  );
} else {
  init().catch(console.error);
}

// ── Legacy bridges ─────────────────────────────────────────────────────────────
// Expose refresh for external callers (e.g. global-nav org-switch).
window.HoverDashboard = { refresh };
