/**
 * Hover Authentication Extension
 * Data binding integration for the unified authentication system
 *
 * This module provides integration between the core auth system (auth.js)
 * and the GNHDataBinder for seamless authentication in dashboard applications.
 *
 * Features:
 * - Auth state monitoring and data binder integration
 * - Automatic dashboard refresh on auth state changes
 * - Pending domain handling for homepage-to-dashboard flow
 * - Network status monitoring
 * - Dashboard-specific auth UI updates
 */

/**
 * Initialise authentication with data binder integration
 * @param {Object} dataBinder - GNHDataBinder instance
 * @param {Object} options - Configuration options
 * @returns {Promise<void>}
 */
async function initializeAuthWithDataBinder(dataBinder, options = {}) {
  const {
    debug = false,
    autoRefresh = true,
    networkMonitoring = true,
  } = options;

  // Handle auth callback tokens
  const hasToken = await window.GNHAuth.handleAuthCallback();

  // Use the session already retrieved by dataBinder.init() instead of fetching again
  const session = dataBinder.authManager?.session;

  if (session?.user) {
    await window.GNHAuth.registerUserWithBackend(session.user);
  }

  // Update user info in header
  window.GNHAuth.updateUserInfo();

  // Set initial auth state
  window.GNHAuth.updateAuthState(!!session?.user);

  // Set up auth state change listener for UI updates and backend registration
  // Note: dataBinder.initAuth() already handles updating authManager.session
  if (window.supabase) {
    window.supabase.auth.onAuthStateChange(async (event, session) => {
      // Register user with backend on sign in (handles OAuth returns)
      if (
        (event === "SIGNED_IN" || event === "USER_UPDATED") &&
        session?.user
      ) {
        await window.GNHAuth.registerUserWithBackend(session.user);
      }

      // Update auth state in UI
      window.GNHAuth.updateAuthState(!!session?.user);
      window.GNHAuth.updateUserInfo();

      // Handle pending domain after successful auth
      if (session?.user) {
        await window.GNHAuth.handlePendingDomain();
      }
    });
  }

  // Log auth state for debugging
  // Update auth state after data binder init
  const currentSession = await window.supabase.auth.getSession();
  window.GNHAuth.updateAuthState(!!currentSession.data.session?.user);

  // Set up network monitoring if enabled
  if (networkMonitoring) {
    setupNetworkMonitoring(dataBinder);
  }
}

/**
 * Setup dashboard-specific refresh method override
 * @param {Object} dataBinder - GNHDataBinder instance
 */
function setupDashboardRefresh(dataBinder) {
  const ACTIVE_JOB_STATUSES = new Set([
    "running",
    "pending",
    "in_progress",
    "in-progress",
    "active",
  ]);

  // Override the refresh method to load dashboard data
  dataBinder.refresh = async function () {
    // Only load dashboard data if user is authenticated
    if (!this.authManager || !this.authManager.isAuthenticated) {
      return;
    }

    try {
      // Show refresh indicator
      const statusIndicator = document.querySelector(".status-indicator");
      if (statusIndicator) {
        statusIndicator.innerHTML =
          '<span class="status-dot"></span><span>Live</span>';
      }

      // Get user's timezone offset in minutes (e.g., -660 for AEDT/UTC+11)
      const tzOffset = getTimezoneOffset();

      // Get current filter range (defaults to 'today')
      const currentRange = this.currentRange || "today";

      // Load stats and jobs data
      let data;
      try {
        data = await this.loadAndBind({
          stats: `/v1/dashboard/stats?range=${currentRange}&tzOffset=${tzOffset}`,
        });
      } catch (error) {
        // Handle stats API errors gracefully
        data = {
          stats: {
            total_jobs: 0,
            running_jobs: 0,
            completed_jobs: 0,
            failed_jobs: 0,
          },
        };
      }

      // Load jobs separately for template binding
      let jobsResponse, jobs;
      try {
        jobsResponse = await this.fetchData(
          `/v1/jobs?limit=10&range=${currentRange}&tzOffset=${tzOffset}`
        );
        jobs = jobsResponse.jobs || [];
      } catch (error) {
        jobs = [];
      }

      // Process jobs data for better display
      const processedJobs = jobs.map((job) => ({
        ...job,
        domain: job.domains?.name || "Unknown Domain",
        progress: Math.round(job.progress || 0),
        started_at_formatted: job.started_at
          ? new Date(job.started_at).toLocaleString()
          : "-",
      }));

      this.hasRealtimeActiveJobs = processedJobs.some((job) =>
        ACTIVE_JOB_STATUSES.has(String(job.status || "").toLowerCase())
      );

      if (fallbackPollingIntervalId) {
        startFallbackPolling();
      }

      // Clear any existing empty state before binding
      const existingEmptyState = document.querySelector(
        ".gnh-jobs-empty-state"
      );
      if (existingEmptyState) {
        existingEmptyState.remove();
      }

      // Bind all templates
      this.bindTemplates({
        job: processedJobs,
      });

      // Show simple empty state if no jobs
      if (processedJobs.length === 0) {
        const jobsList = document.querySelector(".gnh-jobs-list");
        if (jobsList) {
          const emptyState = document.createElement("div");
          emptyState.className = "gnh-jobs-empty-state";
          emptyState.style.cssText =
            "text-align: center; padding: 40px 20px; color: #6b7280;";

          const icon = document.createElement("div");
          icon.style.cssText = "font-size: 48px; margin-bottom: 16px;";
          icon.textContent = "🐝";
          emptyState.appendChild(icon);

          const heading = document.createElement("h3");
          heading.style.cssText = "margin: 0 0 8px 0; color: #374151;";
          heading.textContent = "No jobs yet";
          emptyState.appendChild(heading);

          const message = document.createElement("p");
          message.style.cssText = "margin: 0; font-size: 14px;";
          message.textContent =
            "Use the form above to start your first cache warming job";
          emptyState.appendChild(message);

          // Clear the list and add empty state
          while (jobsList.firstChild) {
            jobsList.removeChild(jobsList.firstChild);
          }
          jobsList.appendChild(emptyState);
        }
      } else {
        // Update job action visibility and visual states
        setTimeout(() => {
          if (window.updateJobActionVisibility) {
            window.updateJobActionVisibility();
          }
          if (window.updateJobVisualStates) {
            window.updateJobVisualStates();
          }
        }, 100); // Small delay to ensure DOM updates are complete
      }

      // Load metrics metadata after successful data load (only once)
      if (window.metricsMetadata && !window.metricsMetadata.isLoaded()) {
        try {
          await window.metricsMetadata.load();
          window.metricsMetadata.initializeInfoIcons();
        } catch (metadataError) {
          console.warn(
            "Failed to load metrics metadata (non-critical):",
            metadataError
          );
        }
      }
    } catch (error) {
      console.error("Dashboard refresh failed:", error);

      // Only show error if it's not a 404 or empty data response
      if (error.status !== 404 && !error.message?.includes("No jobs found")) {
        if (window.showDashboardError) {
          window.showDashboardError(
            "Unable to refresh dashboard data. Please check your connection and try again."
          );
        }
      }

      // Set error state for stats only if there's a real error
      if (error.status !== 404) {
        this.updateElements({
          stats: {
            total_jobs: "–",
            running_jobs: "–",
            completed_jobs: "–",
            failed_jobs: "–",
          },
        });
      } else {
        // For 404/no data, show zero stats instead of error state
        this.updateElements({
          stats: {
            total_jobs: "0",
            running_jobs: "0",
            completed_jobs: "0",
            failed_jobs: "0",
          },
        });
      }
    } finally {
      // Reset status indicator
      const statusIndicator = document.querySelector(".status-indicator");
      if (statusIndicator) {
        statusIndicator.innerHTML =
          '<span class="status-dot"></span><span>Live</span>';
      }
    }
  };
}

// Track forms that have handlers attached (WeakSet for automatic cleanup)
const formsWithHandlers = new WeakSet();

/**
 * Setup dashboard form handler for job creation
 */
function setupDashboardFormHandler() {
  const dashboardForm = document.getElementById("dashboardJobForm");
  if (dashboardForm && !formsWithHandlers.has(dashboardForm)) {
    dashboardForm.addEventListener("submit", handleDashboardJobCreation);
    formsWithHandlers.add(dashboardForm);
  }
}

function setupJobDomainSearch() {
  const domainInput = document.getElementById("jobDomain");
  if (!domainInput || !window.GNHDomainSearch) {
    return;
  }

  const container = domainInput.closest(".gnh-domain-search");

  window.GNHDomainSearch.setupDomainSearchInput({
    input: domainInput,
    container: container || domainInput.parentElement,
    clearOnSelect: false,
    onSelectDomain: (domain) => {
      domainInput.value = domain.name;
    },
    onCreateDomain: (domain) => {
      domainInput.value = domain.name;
    },
    onError: (message) => {
      if (window.showDashboardError) {
        window.showDashboardError(
          message || "Failed to create domain. Please try again."
        );
      }
    },
  });
}

/**
 * Handle dashboard job creation form
 * @param {Event} event - Form submit event
 */
async function handleDashboardJobCreation(event) {
  event.preventDefault();
  const formData = new FormData(event.target);

  let domain = formData.get("domain");
  const maxPages = parseInt(formData.get("max_pages"));
  const concurrencyValue = formData.get("concurrency");
  const scheduleInterval = formData.get("schedule_interval_hours");

  // Basic validation
  if (!domain) {
    if (window.showDashboardError) {
      window.showDashboardError("Domain is required");
    }
    return;
  }

  if (window.GNHDomainSearch) {
    try {
      const ensuredDomain = await window.GNHDomainSearch.ensureDomainByName(
        domain,
        { allowCreate: true }
      );
      if (ensuredDomain?.name) {
        domain = ensuredDomain.name;
      }
    } catch (error) {
      if (window.showDashboardError) {
        window.showDashboardError(
          error.message || "Failed to create domain. Please try again."
        );
      }
      return;
    }
  }

  const domainField = document.getElementById("jobDomain");
  if (domainField) {
    domainField.value = domain;
  }

  if (maxPages < 0 || maxPages > 10000) {
    if (window.showDashboardError) {
      window.showDashboardError("Maximum pages must be between 0 and 10000");
    }
    return;
  }

  // Build request body - only include concurrency if explicitly set
  const requestBody = {
    domain: domain,
    max_pages: maxPages,
    use_sitemap: true,
    find_links: true,
  };
  if (
    concurrencyValue &&
    concurrencyValue !== "" &&
    concurrencyValue !== "default"
  ) {
    requestBody.concurrency = parseInt(concurrencyValue);
  }

  try {
    // If schedule is selected, create scheduler first, then create job
    if (scheduleInterval && scheduleInterval !== "") {
      const scheduleIntervalHours = parseInt(scheduleInterval);

      // Validate schedule interval
      if (
        isNaN(scheduleIntervalHours) ||
        ![6, 12, 24, 48].includes(scheduleIntervalHours)
      ) {
        if (window.showDashboardError) {
          window.showDashboardError(
            "Invalid schedule interval. Must be 6, 12, 24, or 48 hours."
          );
        }
        return;
      }

      // Create scheduler
      const schedulerResponse = await window.dataBinder.fetchData(
        "/v1/schedulers",
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            domain: domain,
            schedule_interval_hours: scheduleIntervalHours,
            max_pages: maxPages,
            find_links: true,
            concurrency: requestBody.concurrency || 20,
          }),
        }
      );

      // Create job immediately linked to the scheduler
      try {
        const jobResponse = await window.dataBinder.fetchData("/v1/jobs", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            ...requestBody,
            scheduler_id: schedulerResponse.id,
          }),
        });
      } catch (jobError) {
        // If job creation fails, attempt to clean up the scheduler
        console.error(
          "Failed to create initial job, cleaning up scheduler:",
          jobError
        );
        try {
          await window.dataBinder.fetchData(
            `/v1/schedulers/${encodeURIComponent(schedulerResponse.id)}`,
            { method: "DELETE" }
          );
        } catch (cleanupError) {
          console.error("Failed to clean up scheduler:", cleanupError);
        }
        // Re-throw the original error
        throw jobError;
      }

      // Refresh schedules (settings page) and dashboard data if present
      if (window.loadSettingsSchedules) {
        await window.loadSettingsSchedules();
      }
      if (window.dataBinder) {
        await window.dataBinder.refresh();
      }

      // Close modal and show success
      if (window.closeCreateJobModal) {
        window.closeCreateJobModal();
      }

      if (window.showSuccessMessage) {
        window.showSuccessMessage(
          `Scheduled job created for ${domain} (runs every ${scheduleIntervalHours} hours)`
        );
      }
    } else {
      // Regular one-time job creation
      const response = await window.dataBinder.fetchData("/v1/jobs", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(requestBody),
      });

      // Clear the form
      const domainField = document.getElementById("jobDomain");
      const maxPagesField = document.getElementById("maxPages");
      const scheduleField = document.getElementById("scheduleInterval");
      if (domainField) domainField.value = "";
      if (maxPagesField) maxPagesField.value = "0";
      if (scheduleField) scheduleField.value = "";

      // Close modal
      if (window.closeCreateJobModal) {
        window.closeCreateJobModal();
      }

      // Refresh dashboard to show new job
      if (window.dataBinder) {
        await window.dataBinder.refresh();
      }

      // Show success message
      if (window.showSuccessMessage) {
        window.showSuccessMessage(`Job created successfully for ${domain}`);
      }
    }
  } catch (error) {
    console.error("Failed to create job:", error);
    if (window.showDashboardError) {
      window.showDashboardError(
        error.message || "Failed to create job. Please try again."
      );
    }
  }
}

/**
 * Setup network status monitoring
 * @param {Object} dataBinder - GNHDataBinder instance
 */
function setupNetworkMonitoring(dataBinder) {
  // Check initial network status
  updateNetworkStatus();

  // Listen for network status changes
  window.addEventListener("online", () => {
    updateNetworkStatus();
    if (window.showInfoMessage) {
      window.showInfoMessage("Connection restored. Refreshing data...", 2000);
    }
    setTimeout(() => {
      if (dataBinder) {
        dataBinder.refresh();
      }
    }, 500);
  });

  window.addEventListener("offline", () => {
    updateNetworkStatus();
    if (window.showDashboardError) {
      window.showDashboardError(
        "Connection lost. Some features may not work.",
        "error",
        0
      );
    }
  });
}

/**
 * Update network status indicator
 */
function updateNetworkStatus() {
  const statusIndicator = document.querySelector(".status-indicator");
  if (statusIndicator && !navigator.onLine) {
    statusIndicator.innerHTML =
      '<span style="background: #ef4444;" class="status-dot"></span><span>Offline</span>';
  } else if (statusIndicator && navigator.onLine) {
    statusIndicator.innerHTML =
      '<span class="status-dot"></span><span>Live</span>';
  }
}

/**
 * Get user's timezone offset in minutes from UTC
 * @returns {number} Offset in minutes (negative for ahead of UTC, positive for behind)
 * Example: AEDT (UTC+11) returns -660
 */
function getTimezoneOffset() {
  return new Date().getTimezoneOffset();
}

/**
 * Detect the user's IANA timezone identifier (e.g. "Australia/Sydney")
 * Falls back to UTC when detection fails.
 * @returns {string} URL-encoded timezone identifier
 */
function getTimezone() {
  try {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (tz && typeof tz === "string") {
      return encodeURIComponent(tz);
    }
  } catch (error) {
    console.warn("Failed to detect timezone, defaulting to UTC", error);
  }
  return "UTC";
}

/**
 * Change the dashboard filter range and refresh data
 * @param {string} range - Range filter: 'last_hour', 'today', 'last_24_hours', 'yesterday', '7days', '30days', 'all'
 */
function changeTimeRange(range) {
  if (window.dataBinder) {
    window.dataBinder.currentRange = range;
    window.dataBinder.refresh();
  }
}

/**
 * Enhanced dashboard initialisation with full auth integration
 * @param {Object} config - Configuration options
 * @returns {Promise<Object>} Initialised data binder
 */
async function initializeDashboard(config = {}) {
  const {
    debug = false,
    apiBaseUrl = "",
    autoRefresh = true,
    networkMonitoring = true,
  } = config;

  // Load the shared authentication modal
  await window.GNHAuth.loadAuthModal();

  // Wait for auth modal DOM to be ready
  await new Promise((resolve) => setTimeout(resolve, 50));

  // Create data binder with production config
  const dataBinder = new GNHDataBinder({
    apiBaseUrl,
    debug,
  });

  // Expose the binder globally so shared handlers (e.g. auth, forms) can reuse the instance
  if (typeof window !== "undefined") {
    window.dataBinder = dataBinder;
  }

  // Ensure Supabase is initialised BEFORE dataBinder.init() tries to use it
  if (!window.GNHAuth.initialiseSupabase()) {
    console.error("Supabase not available");
    throw new Error("Failed to initialise Supabase client");
  }

  // Initialise data binder (now Supabase is ready)
  await dataBinder.init();

  // Initialise auth with data binder integration (after auth manager is set up)
  await initializeAuthWithDataBinder(dataBinder, {
    debug,
    autoRefresh,
    networkMonitoring,
  });

  // Setup dashboard-specific refresh method
  setupDashboardRefresh(dataBinder);

  // Setup dashboard form handler
  setupDashboardFormHandler();

  // Setup authentication event handlers
  window.GNHAuth.setupAuthHandlers();

  // Setup login page handlers
  window.GNHAuth.setupLoginPageHandlers();

  // Subscribe to job updates
  if (autoRefresh) {
    await subscribeToJobUpdates();
    await dataBinder.refresh();
  }

  return dataBinder;
}

/**
 * Quick setup function for basic auth integration
 * @param {Object} dataBinder - Existing GNHDataBinder instance
 */
async function setupQuickAuth(dataBinder) {
  // Load auth modal
  await window.GNHAuth.loadAuthModal();

  // Wait for DOM to be ready
  await new Promise((resolve) => setTimeout(resolve, 50));

  // Initialise auth
  await initializeAuthWithDataBinder(dataBinder, { debug: false });

  // Setup handlers
  window.GNHAuth.setupAuthHandlers();
  window.GNHAuth.setupLoginPageHandlers();
}

// Realtime subscription constants (exposed on window for job-page.js)
const TRANSACTION_VISIBILITY_DELAY_MS = 200;
window.TRANSACTION_VISIBILITY_DELAY_MS = TRANSACTION_VISIBILITY_DELAY_MS;
const SUBSCRIBE_RETRY_INTERVAL_MS = 1000;
const FALLBACK_POLLING_INTERVAL_ACTIVE_MS = 1000;
const FALLBACK_POLLING_INTERVAL_IDLE_MS = 10000;
const MAX_SUBSCRIBE_RETRIES = 15;
const REALTIME_DEBOUNCE_MS = 250; // Throttle realtime notifications to max 4 refreshes per second

// Realtime subscription state
let subscribeRetryCount = 0;
let subscribeRetryTimeoutId = null;
let fallbackPollingIntervalId = null;
let fallbackPollingIntervalMs = null;
let cleanupHandlerRegistered = false;
let lastRealtimeRefresh = 0;
let throttleTimeoutId = null;
let isRefreshing = false;

function getFallbackPollingIntervalMs() {
  if (window.dataBinder?.hasRealtimeActiveJobs) {
    return FALLBACK_POLLING_INTERVAL_ACTIVE_MS;
  }
  return FALLBACK_POLLING_INTERVAL_IDLE_MS;
}

/**
 * Throttled refresh for realtime notifications.
 * Guarantees at most one refresh per REALTIME_DEBOUNCE_MS interval,
 * but ensures refresh happens even with continuous notifications.
 * Also stops fallback polling once we receive a real event.
 */
function throttledRealtimeRefresh() {
  // Receiving a real event proves realtime works - stop fallback polling
  clearFallbackPolling();

  const now = Date.now();
  const timeSinceLastRefresh = now - lastRealtimeRefresh;

  // If enough time has passed, refresh immediately
  if (timeSinceLastRefresh >= REALTIME_DEBOUNCE_MS && !isRefreshing) {
    executeRealtimeRefresh();
    return;
  }

  // Otherwise, schedule a refresh for when the throttle window expires
  // (only if one isn't already scheduled)
  if (!throttleTimeoutId && !isRefreshing) {
    const delay = REALTIME_DEBOUNCE_MS - timeSinceLastRefresh;
    throttleTimeoutId = setTimeout(
      () => {
        throttleTimeoutId = null;
        if (!isRefreshing) {
          executeRealtimeRefresh();
        }
      },
      Math.max(delay, 100)
    );
  }
}

/**
 * Execute the actual refresh
 */
async function executeRealtimeRefresh() {
  if (isRefreshing) return;
  isRefreshing = true;
  lastRealtimeRefresh = Date.now();
  try {
    await window.dataBinder?.refresh();
  } finally {
    isRefreshing = false;
  }
}

/**
 * Start fallback polling when realtime connection fails
 */
function startFallbackPolling() {
  const nextIntervalMs = getFallbackPollingIntervalMs();
  if (
    fallbackPollingIntervalId &&
    fallbackPollingIntervalMs === nextIntervalMs
  ) {
    return;
  }

  if (fallbackPollingIntervalId) {
    clearInterval(fallbackPollingIntervalId);
  }

  fallbackPollingIntervalMs = nextIntervalMs;
  fallbackPollingIntervalId = setInterval(() => {
    if (window.dataBinder && !isRefreshing) {
      executeRealtimeRefresh();
    }
  }, fallbackPollingIntervalMs);
}

/**
 * Stop fallback polling when realtime connection is restored
 */
function clearFallbackPolling() {
  if (fallbackPollingIntervalId) {
    clearInterval(fallbackPollingIntervalId);
    fallbackPollingIntervalId = null;
    fallbackPollingIntervalMs = null;
  }
}

/**
 * Clean up realtime subscriptions and timers
 */
function cleanupRealtimeSubscription() {
  // Clear retry timeout
  if (subscribeRetryTimeoutId) {
    clearTimeout(subscribeRetryTimeoutId);
    subscribeRetryTimeoutId = null;
  }

  // Clear throttled refresh timeout
  if (throttleTimeoutId) {
    clearTimeout(throttleTimeoutId);
    throttleTimeoutId = null;
  }

  // Clear fallback polling
  clearFallbackPolling();

  // Remove channel
  if (window.jobsChannel && window.supabase) {
    window.supabase.removeChannel(window.jobsChannel);
    window.jobsChannel = null;
  }

  // Reset state for next page load / SPA navigation
  subscribeRetryCount = 0;
  cleanupHandlerRegistered = false;
}

/**
 * Subscribe to job updates via Supabase Realtime
 * Listens for INSERT and UPDATE events on the jobs table for the active organisation
 */
async function subscribeToJobUpdates() {
  const orgId = window.GNH_ACTIVE_ORG?.id;
  if (!orgId || !window.supabase) {
    if (subscribeRetryCount < MAX_SUBSCRIBE_RETRIES) {
      subscribeRetryCount++;
      // Retry once org/supabase is loaded - track timeout for cleanup
      subscribeRetryTimeoutId = setTimeout(
        subscribeToJobUpdates,
        SUBSCRIBE_RETRY_INTERVAL_MS
      );
    } else {
      console.warn("[Realtime] Max retries reached, enabling fallback polling");
      startFallbackPolling();
    }
    return;
  }

  // Reset retry state on success
  subscribeRetryCount = 0;
  subscribeRetryTimeoutId = null;

  // Clean up existing subscription if any (await to prevent race condition)
  if (window.jobsChannel && window.supabase) {
    try {
      await window.supabase.removeChannel(window.jobsChannel);
    } catch (e) {
      // Ignore removal errors
    }
    window.jobsChannel = null;
  }

  // Register cleanup handler once
  if (!cleanupHandlerRegistered) {
    window.addEventListener("beforeunload", cleanupRealtimeSubscription);
    cleanupHandlerRegistered = true;
  }

  try {
    const channel = window.supabase
      .channel(`jobs-changes:${orgId}`)
      .on(
        "postgres_changes",
        {
          event: "UPDATE",
          schema: "public",
          table: "jobs",
          filter: `organisation_id=eq.${orgId}`,
        },
        (payload) => {
          // Debounce coalesces rapid notifications - delay already includes transaction visibility buffer
          throttledRealtimeRefresh();
        }
      )
      .on(
        "postgres_changes",
        {
          event: "INSERT",
          schema: "public",
          table: "jobs",
          filter: `organisation_id=eq.${orgId}`,
        },
        (payload) => {
          throttledRealtimeRefresh();
        }
      )
      .on(
        "postgres_changes",
        {
          event: "DELETE",
          schema: "public",
          table: "jobs",
          filter: `organisation_id=eq.${orgId}`,
        },
        (payload) => {
          throttledRealtimeRefresh();
        }
      )
      .subscribe((status, err) => {
        if (status === "CHANNEL_ERROR" || status === "TIMED_OUT" || err) {
          console.warn(
            "[Realtime] Connection issue, fallback polling will continue"
          );
        }
        // Note: fallback polling stops only when we receive an actual realtime event
        // This ensures polling continues on staging where realtime doesn't work
      });

    // Start fallback polling immediately - it will be cleared when we receive a real event
    startFallbackPolling();

    window.jobsChannel = channel;
  } catch (err) {
    console.error("[Realtime] Failed to subscribe to jobs:", err);
    startFallbackPolling();
  }
}

// Export functions for use by other modules
if (typeof module !== "undefined" && module.exports) {
  // Node.js environment
  module.exports = {
    initializeAuthWithDataBinder,
    setupDashboardRefresh,
    setupDashboardFormHandler,
    setupJobDomainSearch,
    handleDashboardJobCreation,
    setupNetworkMonitoring,
    updateNetworkStatus,
    getTimezone,
    initializeDashboard,
    setupQuickAuth,
    subscribeToJobUpdates,
    cleanupRealtimeSubscription,
  };
} else {
  // Browser environment - make functions globally available
  window.GNHAuthExtension = {
    initializeAuthWithDataBinder,
    setupDashboardRefresh,
    setupDashboardFormHandler,
    setupJobDomainSearch,
    handleDashboardJobCreation,
    setupNetworkMonitoring,
    updateNetworkStatus,
    getTimezone,
    changeTimeRange,
    initializeDashboard,
    subscribeToJobUpdates,
    cleanupRealtimeSubscription,
    setupQuickAuth,
  };

  // Also make individual functions available globally for convenience
  window.initializeAuthWithDataBinder = initializeAuthWithDataBinder;
  window.setupDashboardRefresh = setupDashboardRefresh;
  window.setupDashboardFormHandler = setupDashboardFormHandler;
  window.setupJobDomainSearch = setupJobDomainSearch;
  window.handleDashboardJobCreation = handleDashboardJobCreation;
  window.setupNetworkMonitoring = setupNetworkMonitoring;
  window.updateNetworkStatus = updateNetworkStatus;
  window.getTimezone = getTimezone;
  window.changeTimeRange = changeTimeRange;
  window.initializeDashboard = initializeDashboard;
  window.subscribeToJobUpdates = subscribeToJobUpdates;
  window.cleanupRealtimeSubscription = cleanupRealtimeSubscription;
  window.setupQuickAuth = setupQuickAuth;
}
