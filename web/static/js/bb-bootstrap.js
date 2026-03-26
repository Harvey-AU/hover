/**
 * Hover — Bootstrap
 *
 * Loaded WITHOUT defer so it executes immediately, before core.js.
 * Provides BB_APP.whenReady() so DOMContentLoaded handlers can safely
 * wait for core.js even on cold-cache first loads where deferred
 * scripts have not yet executed.
 */
window.BB_APP = window.BB_APP || {};

/**
 * Wait for core.js initialisation to complete.
 * Polls for window.BB_APP.coreReady (set by the deferred core.js IIFE)
 * then awaits the promise. Throws if core.js does not initialise within
 * the timeout.
 * @param {number} [timeoutMs=5000] - Maximum time to wait in milliseconds
 * @returns {Promise<void>}
 */
window.BB_APP.whenReady = async function whenReady(timeoutMs) {
  if (timeoutMs === undefined) {
    timeoutMs = 5000;
  }
  var pollMs = 50;
  var waited = 0;
  while (!window.BB_APP.coreReady && waited < timeoutMs) {
    await new Promise(function (r) {
      setTimeout(r, pollMs);
    });
    waited += pollMs;
  }
  if (!window.BB_APP.coreReady) {
    throw new Error("core.js did not initialise within " + timeoutMs + "ms");
  }
  await window.BB_APP.coreReady;
};
