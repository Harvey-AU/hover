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
 * Returns true only when the body is a plain object or array —
 * the only values that should be automatically JSON.stringify'd.
 * Strings, numbers, FormData, Blob, URLSearchParams, ArrayBuffer, and
 * TypedArrays are all passed through unchanged by serialiseBody().
 * @param {unknown} body
 * @returns {boolean}
 */
function isJsonBody(body) {
  if (body == null) return false;
  if (Array.isArray(body)) return true;
  return Object.prototype.toString.call(body) === "[object Object]";
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
 * Core fetch wrapper. Handles auth headers, serialisation, JSON parsing,
 * and error normalisation. All public helpers delegate here.
 *
 * The body is serialised inside this function so that buildHeaders always
 * receives the original (pre-serialisation) value and can correctly decide
 * whether to set Content-Type: application/json.
 *
 * @param {string} path - API path, e.g. "/v1/jobs"
 * @param {RequestInit & { rawBody?: unknown }} [init] - standard fetch options;
 *   pass `rawBody` instead of `body` when calling from post/put/patch so
 *   serialisation and header decisions happen in one place.
 * @returns {Promise<unknown>} parsed JSON response body
 * @throws {ApiError} on non-2xx responses
 */
export async function request(path, init = {}) {
  const { rawBody, ...fetchInit } = init;
  // rawBody is the pre-serialisation value supplied by post/put/patch.
  // When request() is called directly with init.body already set (e.g.
  // a pre-stringified string), rawBody is undefined and we use body as-is.
  const bodyToSend =
    rawBody !== undefined ? serialiseBody(rawBody) : fetchInit.body;

  // Pass the original (pre-serialisation) value to buildHeaders so it
  // can correctly decide whether to set Content-Type: application/json.
  const rawBodyForHeaders = rawBody !== undefined ? rawBody : fetchInit.body;
  const headers = await buildHeaders(
    path,
    fetchInit.headers,
    rawBodyForHeaders
  );

  const response = await fetch(path, {
    ...fetchInit,
    headers,
    body: bodyToSend,
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

  // Unwrap the standard API envelope { status, data, message, request_id }.
  // If the response has a `data` key alongside a `status` string, return
  // `data` directly so callers never need to do `res?.data?.jobs` manually.
  // Responses that are not envelopes (plain objects, arrays, strings) pass
  // through unchanged.
  if (
    body !== null &&
    typeof body === "object" &&
    !Array.isArray(body) &&
    typeof body.status === "string" &&
    Object.prototype.hasOwnProperty.call(body, "data")
  ) {
    return body.data;
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
    rawBody: body,
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
    rawBody: body,
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
    rawBody: body,
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
