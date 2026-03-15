/**
 * pages/webflow-jobs.js — shared job-list logic for Webflow extension and dashboard
 *
 * This module provides the data-fetching, state management, and rendering
 * helpers for the job list surface. It is imported by both the Webflow
 * Designer extension panel and the main app dashboard.
 *
 * It does NOT own a page lifecycle — pages that use it (extension index.ts,
 * future dashboard.js) call init() and supply their own DOM containers.
 *
 * Prerequisites:
 *   - window.supabase initialised before calling subscribeToJobUpdates()
 *   - api-client.js / auth-session.js available via ES module imports
 *
 * Usage:
 *   import { fetchJobs, renderJobList, subscribeToJobUpdates } from "/app/pages/webflow-jobs.js";
 *
 *   const jobs = await fetchJobs({ limit: 10 });
 *   renderJobList(container, jobs);
 *   const unsubscribe = subscribeToJobUpdates(orgId, () => refresh());
 */

import { get } from "/app/lib/api-client.js";
import {
  formatRelativeTime,
  formatDuration,
  formatCount,
  formatStatus,
  statusCategory,
} from "/app/lib/formatters.js";
import { createStatusPill } from "/app/components/hover-status-pill.js";
import { createDataTable } from "/app/components/hover-data-table.js";

// ── Constants ──────────────────────────────────────────────────────────────────

const REALTIME_DEBOUNCE_MS = 250;
const SUBSCRIBE_RETRY_INTERVAL_MS = 1000;
const MAX_SUBSCRIBE_RETRIES = 15;
const FALLBACK_POLLING_INTERVAL_MS = 10000;

// ── Data fetching ──────────────────────────────────────────────────────────────

/**
 * Fetch the job list from the API.
 *
 * @param {{ limit?: number, range?: string, include?: string }} [options]
 * @returns {Promise<import("/app/lib/api-client.js").unknown[]>}
 */
export async function fetchJobs(options = {}) {
  const params = new URLSearchParams();
  if (options.limit) params.set("limit", String(options.limit));
  if (options.range) params.set("range", options.range);
  if (options.include) params.set("include", options.include);
  const qs = params.toString();
  const res = await get(`/v1/jobs${qs ? `?${qs}` : ""}`);
  return res?.jobs ?? [];
}

// ── Rendering ──────────────────────────────────────────────────────────────────

/**
 * Render a list of jobs into a container using hover-data-table.
 * Replaces any existing table in the container.
 *
 * @param {HTMLElement} container
 * @param {Record<string,unknown>[]} jobs
 * @param {{ onRowClick?: (job: Record<string,unknown>) => void }} [options]
 */
export function renderJobList(container, jobs, options = {}) {
  // Remove previous table if any
  const existing = container.querySelector("hover-data-table");
  if (existing) existing.remove();

  const table = createDataTable({
    columns: [
      {
        key: "domain",
        label: "Domain",
        render: (val, row) => {
          const domain = val || row.domains?.name || "—";
          const span = document.createElement("span");
          span.className = "hover-jobs__domain";
          span.textContent = domain;
          return span;
        },
      },
      {
        key: "status",
        label: "Status",
        render: (val) => {
          return createStatusPill(String(val || ""));
        },
      },
      {
        key: "progress",
        label: "Progress",
        render: (val, row) => {
          const pct = Math.round(Number(val) || 0);
          const total = formatCount(row.total_tasks);
          const done = formatCount(row.completed_tasks);
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
    ],
    rows: jobs.map((job) => ({
      ...job,
      domain: job.domains?.name || job.domain || "",
    })),
    emptyMessage: "No jobs yet for this site.",
    onRowClick: options.onRowClick,
  });

  container.appendChild(table);
}

/**
 * Render an empty state message into a container.
 * @param {HTMLElement} container
 * @param {string} [message]
 */
export function renderEmptyState(container, message = "No runs yet.") {
  container.innerHTML = "";
  const div = document.createElement("div");
  div.className = "hover-jobs__empty";
  div.textContent = message;
  container.appendChild(div);
}

/**
 * Render an error state into a container.
 * @param {HTMLElement} container
 * @param {string} [message]
 */
export function renderErrorState(container, message = "Failed to load jobs.") {
  container.innerHTML = "";
  const div = document.createElement("div");
  div.className = "hover-jobs__error";
  div.textContent = message;
  container.appendChild(div);
}

// ── Realtime subscription ──────────────────────────────────────────────────────

/**
 * Subscribe to job changes for an organisation via Supabase Realtime.
 * Falls back to polling if realtime fails.
 *
 * @param {string} orgId
 * @param {() => void} onUpdate - called when jobs may have changed
 * @returns {() => void} unsubscribe / cleanup function
 */
export function subscribeToJobUpdates(orgId, onUpdate) {
  let channel = null;
  let retryCount = 0;
  let retryTimer = null;
  let fallbackTimer = null;
  let lastUpdate = 0;
  let debounceTimer = null;
  let unsubscribed = false;

  function throttledUpdate() {
    const now = Date.now();
    if (now - lastUpdate >= REALTIME_DEBOUNCE_MS) {
      lastUpdate = now;
      clearFallback();
      onUpdate();
      return;
    }
    if (!debounceTimer) {
      debounceTimer = setTimeout(() => {
        debounceTimer = null;
        lastUpdate = Date.now();
        clearFallback();
        onUpdate();
      }, REALTIME_DEBOUNCE_MS);
    }
  }

  function startFallback() {
    if (fallbackTimer) return;
    fallbackTimer = setInterval(onUpdate, FALLBACK_POLLING_INTERVAL_MS);
  }

  function clearFallback() {
    if (fallbackTimer) {
      clearInterval(fallbackTimer);
      fallbackTimer = null;
    }
  }

  function cleanup() {
    unsubscribed = true;
    if (retryTimer) {
      clearTimeout(retryTimer);
      retryTimer = null;
    }
    if (debounceTimer) {
      clearTimeout(debounceTimer);
      debounceTimer = null;
    }
    clearFallback();
    if (channel && window.supabase) {
      window.supabase.removeChannel(channel).catch(() => {});
      channel = null;
    }
  }

  function subscribe() {
    if (unsubscribed) return;
    if (!orgId || !window.supabase?.channel) {
      if (retryCount < MAX_SUBSCRIBE_RETRIES) {
        retryCount++;
        retryTimer = setTimeout(subscribe, SUBSCRIBE_RETRY_INTERVAL_MS);
      } else {
        startFallback();
      }
      return;
    }

    retryCount = 0;

    try {
      channel = window.supabase
        .channel(`hover-jobs:${orgId}`)
        .on(
          "postgres_changes",
          {
            event: "INSERT",
            schema: "public",
            table: "jobs",
            filter: `organisation_id=eq.${orgId}`,
          },
          throttledUpdate
        )
        .on(
          "postgres_changes",
          {
            event: "UPDATE",
            schema: "public",
            table: "jobs",
            filter: `organisation_id=eq.${orgId}`,
          },
          throttledUpdate
        )
        .on(
          "postgres_changes",
          {
            event: "DELETE",
            schema: "public",
            table: "jobs",
            filter: `organisation_id=eq.${orgId}`,
          },
          throttledUpdate
        )
        .subscribe((status, err) => {
          if (
            (status === "CHANNEL_ERROR" || status === "TIMED_OUT" || err) &&
            !unsubscribed
          ) {
            startFallback();
          }
        });

      // Start fallback immediately; clearFallback() stops it on first real event
      startFallback();
    } catch {
      startFallback();
    }
  }

  subscribe();
  return cleanup;
}
