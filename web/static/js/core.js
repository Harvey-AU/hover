(function () {
  const loadedScripts = new Map();
  window.GNH_APP = window.GNH_APP || {};

  function promiseWithResolvers() {
    if (typeof Promise.withResolvers === "function") {
      return Promise.withResolvers();
    }
    let resolve;
    let reject;
    const promise = new Promise((resolveRef, rejectRef) => {
      resolve = resolveRef;
      reject = rejectRef;
    });
    return { promise, resolve, reject };
  }

  function loadScript(src, attrs = {}) {
    if (loadedScripts.has(src)) {
      return loadedScripts.get(src);
    }

    const existing = document.querySelector(`script[src="${src}"]`);
    if (existing) {
      if (
        existing.dataset.gnhReady === "true" ||
        existing.dataset.gnhLoader === "true" ||
        existing.readyState === "complete"
      ) {
        const promise = Promise.resolve();
        loadedScripts.set(src, promise);
        return promise;
      }
      const {
        promise,
        resolve: resolveExisting,
        reject: rejectExisting,
      } = promiseWithResolvers();
      const onLoad = () => {
        existing.removeEventListener("load", onLoad);
        existing.removeEventListener("error", onError);
        resolveExisting();
      };
      const onError = (err) => {
        existing.removeEventListener("load", onLoad);
        existing.removeEventListener("error", onError);
        rejectExisting(err);
      };
      existing.addEventListener("load", onLoad);
      existing.addEventListener("error", onError);
      loadedScripts.set(src, promise);
      return promise;
    }

    const {
      promise,
      resolve: resolveScript,
      reject: rejectScript,
    } = promiseWithResolvers();
    const script = document.createElement("script");
    script.src = src;
    script.dataset.gnhLoader = "true";
    Object.entries(attrs).forEach(([key, value]) => {
      if (value === undefined || value === null) return;
      script.setAttribute(key, value);
    });
    script.onload = () => {
      script.dataset.gnhReady = "true";
      resolveScript();
    };
    script.onerror = (error) => rejectScript(error);
    document.head.appendChild(script);

    loadedScripts.set(src, promise);
    return promise;
  }

  async function ensureConfig() {
    if (window.GNH_CONFIG) {
      return;
    }
    try {
      await loadScript("/config.js");
    } catch (error) {
      throw new Error("Failed to load /config.js", {
        cause: error,
      });
    }
    if (!window.GNH_CONFIG) {
      throw new Error("GNH_CONFIG missing after loading /config.js");
    }
  }

  function ensureSupabase() {
    const overrideSrc = window.GNH_APP?.scripts?.supabase;
    const src =
      overrideSrc ||
      "https://unpkg.com/@supabase/supabase-js@2.80.0/dist/umd/supabase.js";
    const attrs = overrideSrc
      ? {}
      : {
          integrity:
            "sha384-i0m00Vn1ERlKXxNWSa87g6OUB7eLxpmsQoNF68IHuQVtfJTebIca7XhFsYt9h/gN",
          crossorigin: "anonymous",
        };
    return loadScript(src, attrs);
  }

  function ensurePasswordStrength() {
    const overrideSrc = window.GNH_APP?.scripts?.passwordStrength;
    const src =
      overrideSrc || "https://cdn.jsdelivr.net/npm/zxcvbn@4.4.2/dist/zxcvbn.js";
    const attrs = overrideSrc
      ? {}
      : {
          integrity:
            "sha384-LXuP8lknSGBOLVn4fwVOl+rWR+zOEtZx6CF9ZLaN6gKBgLByU4D79VWWjV4/gefq",
          crossorigin: "anonymous",
        };
    return loadScript(src, attrs);
  }

  function ensureTurnstile() {
    const config = window.GNH_CONFIG || {};
    const shouldLoadTurnstile =
      window.GNH_APP?.enableTurnstile ?? config.enableTurnstile ?? false;
    if (!shouldLoadTurnstile) {
      return Promise.resolve();
    }
    const overrideSrc = window.GNH_APP?.scripts?.turnstile;
    const src =
      overrideSrc || "https://challenges.cloudflare.com/turnstile/v0/api.js";
    const attrs = overrideSrc
      ? { async: true, defer: true }
      : {
          crossorigin: "anonymous",
          async: true,
          defer: true,
        };
    return loadScript(src, attrs);
  }

  function ensureAuthBundle() {
    return loadScript("/js/auth.js");
  }

  async function initialise() {
    await ensureConfig();
    await ensureSupabase();
    const isAuthCallbackPage = Boolean(window.GNH_APP?.authCallback);
    const isExtensionAuthPage = Boolean(window.GNH_APP?.extensionAuth);

    // Callback/extension auth pages must not block on optional third-party scripts.
    if (!isAuthCallbackPage && !isExtensionAuthPage) {
      const optionalScriptResults = await Promise.allSettled([
        ensurePasswordStrength(),
        ensureTurnstile(),
      ]);
      optionalScriptResults.forEach((result, index) => {
        if (result.status === "rejected") {
          const scriptName = index === 0 ? "password-strength" : "turnstile";
          console.warn(
            `Optional script failed to load: ${scriptName}`,
            result.reason
          );
        }
      });
    }
    await ensureAuthBundle();

    // Initialise Supabase client after loading SDK and auth bundle
    if (typeof window.GNHAuth?.initialiseSupabase === "function") {
      const initialised = window.GNHAuth.initialiseSupabase();
      if (!initialised) {
        console.error("Failed to initialise Supabase client");
      }
    }

    if (isAuthCallbackPage && window.GNHAuth?.initAuthCallbackPage) {
      window.GNHAuth.initAuthCallbackPage();
      return;
    }

    if (isExtensionAuthPage && window.GNHAuth?.initExtensionAuthPage) {
      window.GNHAuth.initExtensionAuthPage();
      return;
    }

    if (typeof window.GNHAuth?.setupAuthHandlers === "function") {
      window.GNHAuth.setupAuthHandlers();
    }
  }

  const coreReady = (async () => {
    try {
      await initialise();
      window.GNH_APP = window.GNH_APP || {};
      window.GNH_APP.coreReadyState = "ready";
    } catch (error) {
      window.GNH_APP = window.GNH_APP || {};
      window.GNH_APP.coreReadyState = "error";
      console.error("Failed to initialise Hover core scripts", error);
      throw error;
    }
  })();

  window.GNH_APP = window.GNH_APP || {};
  window.GNH_APP.coreReady = coreReady;

  // ========================================
  // Unified Organisation Initialisation
  // ========================================
  // Single source of truth for active organisation.
  // All code should await GNH_ORG_READY before accessing GNH_ACTIVE_ORG.

  let orgReadyResolve = null;
  let orgReadyReject = null;
  let orgInitialised = false;

  const {
    promise: orgReady,
    resolve: orgReadyResolveRef,
    reject: orgReadyRejectRef,
  } = promiseWithResolvers();
  window.GNH_ORG_READY = orgReady;
  orgReadyResolve = orgReadyResolveRef;
  orgReadyReject = orgReadyRejectRef;

  /**
   * Initialise the active organisation. Called once after auth is confirmed.
   * Sets window.GNH_ACTIVE_ORG and resolves GNH_ORG_READY.
   * @returns {Promise<Object|null>} The active organisation or null
   */
  window.GNH_APP.initialiseOrg = async function () {
    // Return cached result if we have a valid org
    if (
      orgInitialised &&
      window.GNH_ACTIVE_ORG?.id &&
      window.GNH_ACTIVE_ORG?.name
    ) {
      return window.GNH_ACTIVE_ORG;
    }

    try {
      if (!window.supabase?.auth) {
        throw new Error("Supabase not initialised");
      }

      const { data: sessionData } = await window.supabase.auth.getSession();
      const session = sessionData?.session;
      if (!session) {
        // No session - leave GNH_ORG_READY pending so it resolves on sign-in
        window.GNH_ACTIVE_ORG = null;
        window.GNH_ORGANISATIONS = [];
        return null;
      }

      // Fetch organisations from API
      const response = await fetch("/v1/organisations", {
        headers: { Authorization: `Bearer ${session.access_token}` },
      });

      if (!response.ok) {
        throw new Error(`Failed to fetch organisations: ${response.status}`);
      }

      const data = await response.json();
      const organisations = data.data?.organisations || [];
      const serverActiveOrgId = data.data?.active_organisation_id || null;

      if (organisations.length === 0) {
        orgInitialised = true;
        window.GNH_ACTIVE_ORG = null;
        window.GNH_ORGANISATIONS = [];
        orgReadyResolve(null);
        return null;
      }

      // Get active org ID from API first (authoritative), then localStorage.
      let activeOrgId = null;

      // API /v1/organisations includes the user's current active org.
      activeOrgId = serverActiveOrgId;

      // Fall back to localStorage only if API value is absent.
      if (!activeOrgId) {
        try {
          activeOrgId = localStorage.getItem("gnh_active_org_id");
        } catch (e) {
          // localStorage might be blocked
        }
      }

      // Find active org in list, fall back to first
      const activeOrg =
        organisations.find((org) => org.id === activeOrgId) || organisations[0];

      // Store in localStorage for faster future loads
      try {
        localStorage.setItem("gnh_active_org_id", activeOrg.id);
      } catch (e) {
        // localStorage might be blocked
      }

      // Set globals
      window.GNH_ACTIVE_ORG = activeOrg;
      window.GNH_ORGANISATIONS = organisations;
      orgInitialised = true;

      orgReadyResolve(activeOrg);
      return activeOrg;
    } catch (err) {
      console.error("Failed to initialise organisation:", err);
      orgInitialised = true;
      window.GNH_ACTIVE_ORG = null;
      orgReadyReject(err);
      throw err;
    }
  };

  /**
   * Switch to a different organisation. Updates DB, globals, and notifies listeners.
   * @param {string} orgId - The organisation ID to switch to
   * @returns {Promise<Object>} The new active organisation
   */
  // Listen for auth state changes to re-init org when user signs in
  window.GNH_APP.coreReady.then(() => {
    if (window.supabase?.auth) {
      window.supabase.auth.onAuthStateChange((event, session) => {
        if (event === "SIGNED_OUT") {
          // Clear org state on sign out
          window.GNH_ACTIVE_ORG = null;
          window.GNH_ORGANISATIONS = [];
          try {
            localStorage.removeItem("gnh_active_org_id");
          } catch (e) {
            // localStorage might be blocked
          }
        } else if (event === "SIGNED_IN" || event === "TOKEN_REFRESHED") {
          // Re-init org if we don't have one yet
          if (!window.GNH_ACTIVE_ORG?.id) {
            window.GNH_APP.initialiseOrg()
              .then((org) => {
                if (org) {
                  document.dispatchEvent(
                    new CustomEvent("gnh:org-ready", {
                      detail: { organisation: org },
                    })
                  );
                }
              })
              .catch((err) => {
                console.warn("Failed to init org after auth change:", err);
              });
          }
        }
      });
    }
  });

  window.GNH_APP.switchOrg = async function (orgId) {
    if (!window.supabase?.auth) {
      throw new Error("Supabase not initialised");
    }

    const { data: sessionData } = await window.supabase.auth.getSession();
    const session = sessionData?.session;
    if (!session) {
      throw new Error("Not authenticated");
    }

    const response = await fetch("/v1/organisations/switch", {
      method: "POST",
      headers: {
        Authorization: `Bearer ${session.access_token}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ organisation_id: orgId }),
    });

    if (!response.ok) {
      const err = await response.json().catch(() => ({}));
      throw new Error(err.message || "Failed to switch organisation", {
        cause: { status: response.status, payload: err },
      });
    }

    const switchData = await response.json();
    const newOrg = switchData.data?.organisation;
    if (!newOrg?.id) {
      throw new Error("Failed to switch organisation");
    }

    // Update global
    window.GNH_ACTIVE_ORG = newOrg;

    // Store in localStorage for persistence
    try {
      localStorage.setItem("gnh_active_org_id", newOrg.id);
    } catch (e) {
      // localStorage might be blocked
    }

    // Dispatch event for listeners
    document.dispatchEvent(
      new CustomEvent("gnh:org-switched", { detail: { organisation: newOrg } })
    );

    return newOrg;
  };

  /**
   * Builds the payload for restarting a job with the same configuration.
   * @param {Object} job - The job object to extract config from
   * @returns {Object} Payload for POST /v1/jobs
   */
  window.GNH_APP.buildRestartJobPayload = function (job) {
    return {
      domain: job.domain ?? job.domains?.name ?? job.domain_name,
      use_sitemap: true,
      find_links: job.find_links ?? true,
      concurrency: job.concurrency ?? 20,
      max_pages: job.max_pages ?? 0,
    };
  };

  coreReady.catch((err) => {
    if (typeof window !== "undefined" && window.console) {
      window.console.debug("coreReady rejected", err);
    }
  });

  if (document.readyState === "loading") {
    document.addEventListener(
      "DOMContentLoaded",
      () => {
        coreReady.catch((err) => {
          console.error(
            "Core initialisation failed after DOMContentLoaded",
            err
          );
        });
      },
      { once: true }
    );
  }
})();
