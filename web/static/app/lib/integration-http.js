/**
 * lib/integration-http.js — integration HTTP utilities
 *
 * Provides timeout-aware fetch and standardised error handling for
 * third-party integration requests (Slack, Webflow, Google).
 *
 * Extracted from bb-integration-http.js (legacy IIFE) as an ES module
 * so integration modules can import directly without globals.
 *
 * Usage:
 *   import { fetchWithTimeout, normaliseIntegrationError } from "/app/lib/integration-http.js";
 *
 *   const res = await fetchWithTimeout(url, { method: "POST", headers, body }, { provider: "slack" });
 *   if (!res.ok) throw normaliseIntegrationError(res, await res.text(), { provider: "slack" });
 */

const INTEGRATION_REQUEST_TIMEOUT_MS = 15_000;

/**
 * Typed error class for integration HTTP failures.
 * Carries status, body, and context for structured error handling.
 */
export class IntegrationHttpError extends Error {
  /**
   * @param {string} message
   * @param {object} [details]
   * @param {Error} [details.cause]
   * @param {number} [details.status]
   * @param {string} [details.statusText]
   * @param {string} [details.url]
   * @param {string} [details.body]
   * @param {object} [details.context]
   */
  constructor(message, details = {}) {
    super(message, { cause: details.cause });
    this.name = "IntegrationHttpError";
    this.status = details.status;
    this.statusText = details.statusText;
    this.url = details.url;
    this.body = details.body;
    this.context = details.context;
  }
}

/**
 * Creates an AbortController with a timeout.
 * @param {number} [timeoutMs]
 * @returns {{ signal: AbortSignal, timeoutId: number }}
 */
export function withTimeoutSignal(timeoutMs = INTEGRATION_REQUEST_TIMEOUT_MS) {
  const controller = new AbortController();
  const timeoutId = window.setTimeout(() => {
    controller.abort("Request timed out");
  }, timeoutMs);
  return { signal: controller.signal, timeoutId };
}

/**
 * Fetch with automatic timeout. Aborts after 15 s by default.
 * @param {string} url
 * @param {RequestInit} [options]
 * @param {object} [context] — metadata for error reporting
 * @returns {Promise<Response>}
 */
export async function fetchWithTimeout(url, options = {}, context = {}) {
  const { signal, timeoutId } = withTimeoutSignal();
  try {
    return await fetch(url, { ...options, signal });
  } catch (error) {
    if (error?.name === "AbortError") {
      throw new IntegrationHttpError("Request timed out", {
        cause: error,
        context,
      });
    }
    throw error;
  } finally {
    window.clearTimeout(timeoutId);
  }
}

/**
 * Wraps a non-ok Response into an IntegrationHttpError.
 * @param {Response} response
 * @param {string} body — already-read response body text
 * @param {object} [context]
 * @returns {IntegrationHttpError}
 */
export function normaliseIntegrationError(response, body, context = {}) {
  return new IntegrationHttpError(body || `HTTP ${response.status}`, {
    status: response.status,
    statusText: response.statusText,
    url: response.url,
    body,
    context,
  });
}
