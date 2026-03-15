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
 * Returns true when the body should have Content-Type: application/json
 * set automatically. FormData, Blob, URLSearchParams, and ArrayBuffer all
 * have their own content type and must not be stringified.
 * @param {unknown} body
 * @returns {boolean}
 */
function isJsonBody(body) {
  return (
    body != null &&
    !(body instanceof FormData) &&
    !(body instanceof Blob) &&
    !(body instanceof URLSearchParams) &&
    !(body instanceof ArrayBuffer) &&
    !ArrayBuffer.isView(body)
  );
}

/**
 * Serialises a body value for the fetch call.
 * Passes FormData/Blob/URLSearchParams/ArrayBuffer through unchanged.
 * Stringifies everything else.
 * @param {unknown} body
 * @returns {BodyInit}
 */
function serialiseBody(body) {
  return isJsonBody(body)
    ? JSON.stringify(body)
    : /** @type {BodyInit} */ (body);
}

/**
 * Builds the standard request headers, injecting an Authorization header
 * when the request is same-origin (prevents token leaking to third-party
 * hosts). Content-Type is set to application/json when the body warrants it.
 *
 * @param {string} path - the request path or URL
 * @param {HeadersInit} [extra] - additional headers to merge
 * @param {unknown} [body] - the request body (used to decide Content-Type)
 * @returns {Promise<Headers>}
 */
async function buildHeaders(path, extra, body) {
  const headers = new Headers(extra);

  if (!headers.has("Content-Type") && isJsonBody(body)) {
    headers.set("Content-Type", "application/json");
  }

  const token = await getBearerToken();
  if (token) {
    // Only attach the bearer token for same-origin requests to avoid
    // accidentally leaking credentials to third-party hosts.
    const requestUrl = new URL(path, window.location.origin);
    if (requestUrl.origin === window.location.origin) {
      headers.set("Authorization", `Bearer ${token}`);
    }
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
  const headers = await buildHeaders(path, init.headers, init.body);

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
 * POST request. Automatically serialises plain objects to JSON.
 * Pass FormData, Blob, or URLSearchParams to send non-JSON bodies.
 * @param {string} path
 * @param {unknown} [body]
 * @param {RequestInit} [init]
 * @returns {Promise<unknown>}
 */
export function post(path, body, init = {}) {
  return request(path, {
    ...init,
    method: "POST",
    body: body !== undefined ? serialiseBody(body) : undefined,
  });
}

/**
 * PUT request. Automatically serialises plain objects to JSON.
 * Pass FormData, Blob, or URLSearchParams to send non-JSON bodies.
 * @param {string} path
 * @param {unknown} [body]
 * @param {RequestInit} [init]
 * @returns {Promise<unknown>}
 */
export function put(path, body, init = {}) {
  return request(path, {
    ...init,
    method: "PUT",
    body: body !== undefined ? serialiseBody(body) : undefined,
  });
}

/**
 * PATCH request. Automatically serialises plain objects to JSON.
 * Pass FormData, Blob, or URLSearchParams to send non-JSON bodies.
 * @param {string} path
 * @param {unknown} [body]
 * @param {RequestInit} [init]
 * @returns {Promise<unknown>}
 */
export function patch(path, body, init = {}) {
  return request(path, {
    ...init,
    method: "PATCH",
    body: body !== undefined ? serialiseBody(body) : undefined,
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
