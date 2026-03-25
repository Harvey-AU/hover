/**
 * lib/formatters.js — pure display formatting utilities
 *
 * No DOM dependency. No global side effects. Safe to import anywhere.
 *
 * All functions accept raw values from API responses and return
 * display-ready strings. Localisation uses the browser's built-in
 * Intl API.
 *
 * Usage:
 *   import { formatDate, formatDuration, formatStatus } from "/app/lib/formatters.js";
 */

// ─── Date and time ────────────────────────────────────────────────────────────

/**
 * Formats an ISO date string as a localised short date.
 * e.g. "15 Mar 2026"
 *
 * @param {string|Date|null} value
 * @param {Intl.DateTimeFormatOptions} [options]
 * @returns {string}
 */
export function formatDate(value, options) {
  if (!value) return "—";
  try {
    const date = value instanceof Date ? value : new Date(value);
    if (isNaN(date.getTime())) return "—";
    return date.toLocaleDateString("en-AU", {
      day: "numeric",
      month: "short",
      year: "numeric",
      ...options,
    });
  } catch {
    return "—";
  }
}

/**
 * Formats an ISO date string as a localised short datetime.
 * e.g. "15 Mar 2026, 3:42 pm"
 *
 * @param {string|Date|null} value
 * @returns {string}
 */
export function formatDateTime(value) {
  return formatDate(value, {
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

/**
 * Formats a timestamp as a relative time string.
 * e.g. "3 hours ago", "just now", "in 5 minutes"
 *
 * Falls back to formatDateTime when the Intl.RelativeTimeFormat API is
 * unavailable (older browsers).
 *
 * @param {string|Date|null} value
 * @returns {string}
 */
export function formatRelativeTime(value) {
  if (!value) return "—";
  try {
    const date = value instanceof Date ? value : new Date(value);
    if (isNaN(date.getTime())) return "—";

    const diffMs = date.getTime() - Date.now();
    const diffSec = Math.round(diffMs / 1000);
    const absSec = Math.abs(diffSec);

    if (absSec < 45) return "just now";

    if (typeof Intl.RelativeTimeFormat !== "function") {
      return formatDateTime(value);
    }

    const rtf = new Intl.RelativeTimeFormat("en-AU", { numeric: "auto" });

    // Sign-aware rounding: past values floor, future values ceil.
    // This avoids "0 minutes ago" or "-1 hour ago" artifacts near thresholds.
    const round = (n) => (n >= 0 ? Math.floor(n) : Math.ceil(n));

    if (absSec < 3600) {
      return rtf.format(round(diffSec / 60), "minute");
    }
    if (absSec < 86400) {
      return rtf.format(round(diffSec / 3600), "hour");
    }
    if (absSec < 604800) {
      return rtf.format(round(diffSec / 86400), "day");
    }
    if (absSec < 2592000) {
      return rtf.format(round(diffSec / 604800), "week");
    }
    return formatDate(value);
  } catch {
    return "—";
  }
}

// ─── Duration ─────────────────────────────────────────────────────────────────

/**
 * Formats a duration in milliseconds as a human-readable string.
 * e.g. 90000 → "1m 30s", 500 → "0.5s", 3661000 → "1h 1m"
 *
 * @param {number|null|undefined} ms
 * @returns {string}
 */
export function formatDuration(ms) {
  if (ms == null || isNaN(ms) || ms < 0) return "—";

  const totalSeconds = ms / 1000;

  if (totalSeconds < 1) {
    return `${totalSeconds.toFixed(1)}s`;
  }

  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = Math.floor(totalSeconds % 60);

  if (h > 0) {
    return m > 0 ? `${h}h ${m}m` : `${h}h`;
  }
  if (m > 0) {
    return s > 0 ? `${m}m ${s}s` : `${m}m`;
  }
  return `${s}s`;
}

// ─── Numbers ──────────────────────────────────────────────────────────────────

/**
 * Formats a number with localised grouping separators.
 * e.g. 1234567 → "1,234,567"
 *
 * @param {number|null|undefined} value
 * @returns {string}
 */
export function formatCount(value) {
  if (value == null || isNaN(value)) return "—";
  return new Intl.NumberFormat("en-AU").format(value);
}

/**
 * Formats a number as a percentage string.
 * e.g. 0.856 → "85.6%", 1 → "100%"
 *
 * @param {number|null|undefined} value
 * @param {number} [decimals=0]
 * @returns {string}
 */
export function formatPercent(value, decimals = 0) {
  if (value == null || isNaN(value)) return "—";
  return `${(value * 100).toFixed(decimals)}%`;
}

// ─── Status ───────────────────────────────────────────────────────────────────

/** @type {Record<string, string>} */
const STATUS_LABELS = {
  pending: "Pending",
  running: "Running",
  completed: "Completed",
  failed: "Failed",
  cancelled: "Cancelled",
  skipped: "Skipped",
  queued: "Queued",
};

/**
 * Maps a raw API status string to a display label.
 * Returns the original value capitalised if not in the known set.
 *
 * @param {string|null|undefined} status
 * @returns {string}
 */
export function formatStatus(status) {
  if (!status) return "—";
  return (
    STATUS_LABELS[status.toLowerCase()] ||
    status.charAt(0).toUpperCase() + status.slice(1)
  );
}

/**
 * Returns a status category for styling purposes.
 * Maps to the token names: success, warning, danger, neutral.
 *
 * @param {string|null|undefined} status
 * @returns {"success"|"warning"|"danger"|"neutral"}
 */
export function statusCategory(status) {
  switch (status?.toLowerCase()) {
    case "completed":
      return "success";
    case "running":
      return "warning";
    case "failed":
      return "danger";
    default:
      return "neutral";
  }
}

// ─── URLs ─────────────────────────────────────────────────────────────────────

/**
 * Strips the scheme and trailing slash from a URL for compact display.
 * e.g. "https://example.com/path/" → "example.com/path"
 *
 * @param {string|null|undefined} url
 * @returns {string}
 */
export function formatUrl(url) {
  if (!url) return "—";
  return url.replace(/^https?:\/\//, "").replace(/\/$/, "");
}

// ─── CSV / Export ─────────────────────────────────────────────────────────────

/**
 * Escapes a value for safe inclusion in a CSV cell.
 * Wraps in double-quotes and escapes internal quotes when the value
 * contains commas, quotes, or newlines.
 *
 * @param {unknown} value
 * @returns {string}
 */
export function escapeCSVValue(value) {
  if (value === null || value === undefined) return "";
  const text = String(value);
  if (/[",\n]/.test(text)) {
    return `"${text.replace(/"/g, '""')}"`;
  }
  return text;
}

/**
 * Sanitises a string for use in a filename.
 * Replaces unsafe characters with hyphens and collapses runs.
 *
 * @param {string} value
 * @returns {string}
 */
export function sanitiseForFilename(value) {
  return value
    .replace(/[^a-zA-Z0-9._-]/g, "-")
    .replace(/-{2,}/g, "-")
    .replace(/^-|-$/g, "")
    .substring(0, 100);
}

/**
 * Triggers a browser file download from in-memory content.
 *
 * @param {string} content — file content
 * @param {string} mimeType — e.g. "text/csv"
 * @param {string} filename — e.g. "export.csv"
 */
export function triggerFileDownload(content, mimeType, filename) {
  const blob = new Blob([content], { type: `${mimeType};charset=utf-8` });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(url);
}

/**
 * Returns initials from a name string.
 * e.g. "Simon Smallchua" → "SS", "simon" → "S"
 *
 * @param {string} value
 * @returns {string}
 */
export function getInitials(value) {
  if (!value) return "?";
  const parts = value.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return parts[0][0].toUpperCase();
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
}

export default {
  formatDate,
  formatDateTime,
  formatRelativeTime,
  formatDuration,
  formatCount,
  formatPercent,
  formatStatus,
  statusCategory,
  formatUrl,
  escapeCSVValue,
  sanitiseForFilename,
  triggerFileDownload,
  getInitials,
};
