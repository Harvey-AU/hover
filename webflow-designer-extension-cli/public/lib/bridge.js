/**
 * lib/bridge.js — bridge between shared ES modules and the extension's index.js
 *
 * Loads shared modules and exposes key functions on window.HoverLib
 * so the non-module index.js can consume them.
 *
 * Import map in index.html remaps /app/lib/ → ./lib/ and /app/components/ → ./
 * so the shared modules' internal imports resolve correctly.
 */

import * as apiClient from "/app/lib/api-client.js";
import * as formatters from "/app/lib/formatters.js";
import * as integrationHttp from "/app/lib/integration-http.js";

// Expose shared modules for index.js consumption
window.HoverLib = {
  api: apiClient,
  fmt: formatters,
  http: integrationHttp,
};

// Signal that shared libs are ready
window.dispatchEvent(new Event("hover-lib-ready"));
