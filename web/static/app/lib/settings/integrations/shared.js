/**
 * lib/settings/integrations/shared.js — shared helpers for integration modules
 */

/**
 * Format a timestamp as a relative date string (Australian locale).
 * @param {string} timestamp — ISO timestamp
 * @returns {string}
 */
export function formatRelativeDate(timestamp) {
  const date = new Date(timestamp);
  const now = new Date();
  const diffMs = now - date;
  const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));

  if (diffDays === 0) return "today";
  if (diffDays === 1) return "yesterday";
  if (diffDays < 7) return `${diffDays} days ago`;
  return date.toLocaleDateString("en-AU", {
    day: "numeric",
    month: "short",
    year: "numeric",
  });
}
