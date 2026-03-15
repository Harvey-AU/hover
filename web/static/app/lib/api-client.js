/**
 * lib/api-client.js — centralised API fetch wrapper
 *
 * All new module code calls these helpers instead of writing
 * inline fetch() calls with repeated auth header logic.
 *
 * Prerequisites:
 *   - window.supabase must be initialised before calling any function
 *     here that fetches a session token. The Supabase SDK is loaded as
 *     a plain <script> tag before module entrypoints (Phase 0 decision).
 *
 * Usage:
 *   import { get, post, del } from "/app/lib/api-client.js";
 *
 *   const data = await get("/v1/jobs");
 *   await post("/v1/jobs", { domain: "example.com" });
 */

/**
 * @typedef {Object} ApiError
 * @property {string} message
 * @property {number} status
 * @property {unknown} [body]
 */

/**
 * Typed error class for API failures. Carries the HTTP status and the
 * parsed response body so callers can branch on status codes.
 */
export class ApiError extends Error {
  /**
   * @param {string} message
   * @param {number} status
   * @param {unknown} [body]
   */
  constructor(message, status, body) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
  }
}

/**
 * Retrieves the current Supabase bearer token.
 * Returns null when no session is active (unauthenticated).
 * @returns {Promise<string|null>}
 */
async function getBearerToken() {
  if (!window.supabase?.auth) {
    return null;
  }
  const { data } = await window.supabase.auth.getSession();
  return data?.session?.access_token ?? null;
}

/**
 * Builds the standard request headers, injecting an Authorization header
 * when a session is available.
 * @param {HeadersInit} [extra] - additional headers to merge
 * @returns {Promise<Headers>}
 */
async function buildHeaders(extra) {
  const headers = new Headers(extra);

  if (!headers.has("Content-Type") && !(extra instanceof FormData)) {
    headers.set("Content-Type", "application/json");
  }

  const token = await getBearerToken();
  if (token) {
    headers.set("Authorization", `Bearer ${token}`);
  }

  return headers;
}

/**
 * Core fetch wrapper. Handles auth headers, JSON parsing, and error
 * normalisation. All public helpers delegate here.
 *
 * @param {string} path - API path, e.g. "/v1/jobs"
 * @param {RequestInit} [init] - standard fetch options
 * @returns {Promise<unknown>} parsed JSON response body
 * @throws {ApiError} on non-2xx responses
 */
export async function request(path, init = {}) {
  const headers = await buildHeaders(init.headers);

  const response = await fetch(path, {
    ...init,
    headers,
  });

  let body;
  const contentType = response.headers.get("content-type") || "";
  if (contentType.includes("application/json")) {
    body = await response.json().catch(() => null);
  } else {
    body = await response.text().catch(() => null);
  }

  if (!response.ok) {
    const message =
      (typeof body === "object" && body?.message) ||
      `Request failed: ${response.status} ${response.statusText}`;
    throw new ApiError(message, response.status, body);
  }

  return body;
}

/**
 * GET request.
 * @param {string} path
 * @param {RequestInit} [init]
 * @returns {Promise<unknown>}
 */
export function get(path, init = {}) {
  return request(path, { ...init, method: "GET" });
}

/**
 * POST request with JSON body.
 * @param {string} path
 * @param {unknown} [body]
 * @param {RequestInit} [init]
 * @returns {Promise<unknown>}
 */
export function post(path, body, init = {}) {
  return request(path, {
    ...init,
    method: "POST",
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
}

/**
 * PUT request with JSON body.
 * @param {string} path
 * @param {unknown} [body]
 * @param {RequestInit} [init]
 * @returns {Promise<unknown>}
 */
export function put(path, body, init = {}) {
  return request(path, {
    ...init,
    method: "PUT",
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
}

/**
 * PATCH request with JSON body.
 * @param {string} path
 * @param {unknown} [body]
 * @param {RequestInit} [init]
 * @returns {Promise<unknown>}
 */
export function patch(path, body, init = {}) {
  return request(path, {
    ...init,
    method: "PATCH",
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
}

/**
 * DELETE request.
 * @param {string} path
 * @param {RequestInit} [init]
 * @returns {Promise<unknown>}
 */
export function del(path, init = {}) {
  return request(path, { ...init, method: "DELETE" });
}

export default { request, get, post, put, patch, del, ApiError };
