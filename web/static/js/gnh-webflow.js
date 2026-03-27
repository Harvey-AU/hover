/**
 * Webflow Integration Handler
 * Handles Webflow workspace/site connections and per-site configuration.
 * Flow: Connect -> OAuth -> Return to settings -> Configure sites
 */

// State for site configuration
let webflowSitesState = {
  connectionId: null,
  sites: [],
  filteredSites: [],
  currentPage: 1,
  sitesPerPage: 10,
  searchQuery: "",
  loading: false,
};

/**
 * Formats a timestamp as a relative date string
 * @param {string} timestamp - ISO timestamp string
 * @returns {string} Formatted date string
 */
function formatWebflowDate(timestamp) {
  const date = new Date(timestamp);
  const now = new Date();
  const diffMs = now - date;
  const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));

  if (diffDays === 0) {
    return "today";
  } else if (diffDays === 1) {
    return "yesterday";
  } else if (diffDays < 7) {
    return `${diffDays} days ago`;
  } else {
    return date.toLocaleDateString("en-AU", {
      day: "numeric",
      month: "short",
      year: "numeric",
    });
  }
}

var integrationHttp = window.BBIntegrationHttp;
if (
  !integrationHttp ||
  typeof integrationHttp.fetchWithTimeout !== "function" ||
  typeof integrationHttp.normaliseIntegrationError !== "function"
) {
  throw new Error(
    "Missing or incompatible integration HTTP helpers. Load /js/bb-integration-http.js before bb-webflow.js."
  );
}

var fetchWithTimeout = integrationHttp.fetchWithTimeout;
var normaliseIntegrationError = integrationHttp.normaliseIntegrationError;

/**
 * Initialise Webflow integration UI handlers
 */
function setupWebflowIntegration() {
  // Click handlers for webflow actions
  document.addEventListener("click", (event) => {
    const element = event.target.closest("[bbb-action]");
    if (!element) {
      return;
    }

    const action = element.getAttribute("bbb-action");
    if (!action || !action.startsWith("webflow-")) {
      return;
    }

    event.preventDefault();
    handleWebflowAction(action, element);
  });

  // Search input handler
  const searchInput = document.getElementById("webflowSiteSearch");
  if (searchInput) {
    let debounceTimer;
    searchInput.addEventListener("input", (event) => {
      clearTimeout(debounceTimer);
      debounceTimer = setTimeout(() => handleSiteSearch(event), 200);
    });
  }
}

/**
 * Handle Webflow-specific actions
 * @param {string} action - The action to perform
 * @param {HTMLElement} element - The element that triggered the action
 */
function handleWebflowAction(action, element) {
  switch (action) {
    case "webflow-connect":
      connectWebflow();
      break;

    case "webflow-disconnect": {
      const connectionId = element.getAttribute("bbb-id");
      if (connectionId) {
        disconnectWebflow(connectionId);
      } else {
        console.warn("webflow-disconnect: missing bbb-id attribute");
      }
      break;
    }

    case "webflow-refresh":
      loadWebflowConnections();
      break;

    case "webflow-sites-refresh":
      if (webflowSitesState.connectionId) {
        loadWebflowSites(webflowSitesState.connectionId, 1);
      }
      break;

    case "webflow-sites-prev":
      if (webflowSitesState.currentPage > 1) {
        renderWebflowSites(webflowSitesState.currentPage - 1);
      }
      break;

    case "webflow-sites-next": {
      const totalPages = Math.ceil(
        webflowSitesState.filteredSites.length / webflowSitesState.sitesPerPage
      );
      if (webflowSitesState.currentPage < totalPages) {
        renderWebflowSites(webflowSitesState.currentPage + 1);
      }
      break;
    }

    default:
      break;
  }
}

/**
 * Load and display Webflow connections for the current organisation
 */
async function loadWebflowConnections() {
  try {
    if (!window.dataBinder?.fetchData) {
      console.warn(
        "dataBinder not available, skipping Webflow connections load"
      );
      return;
    }
    const connections = await window.dataBinder.fetchData(
      "/v1/integrations/webflow"
    );

    const connectionsList = document.getElementById("webflowConnectionsList");
    const emptyState = document.getElementById("webflowEmptyState");
    const sitesConfig = document.getElementById("webflowSitesConfig");

    if (!connectionsList) {
      // It's possible the user hasn't opened the modal yet or element doesn't exist
      return;
    }

    const template = connectionsList.querySelector(
      '[bbb-template="webflow-connection"]'
    );

    if (!template) {
      console.error("Webflow connection template not found");
      return;
    }

    // Clear existing connections (except template)
    const existingConnections = connectionsList.querySelectorAll(
      ".webflow-connection"
    );
    existingConnections.forEach((el) => el.remove());

    if (!connections || connections.length === 0) {
      if (emptyState) emptyState.style.display = "block";
      if (sitesConfig) sitesConfig.style.display = "none";
      return;
    }

    if (emptyState) emptyState.style.display = "none";

    // Build connection elements
    for (const conn of connections) {
      const clone = template.cloneNode(true);
      clone.style.display = "block";
      clone.removeAttribute("bbb-template");
      clone.classList.add("webflow-connection");

      // Set workspace name - prefer display name, fall back to ID
      const nameEl = clone.querySelector(".webflow-name");
      if (nameEl) {
        if (conn.workspace_name) {
          nameEl.textContent = conn.workspace_name;
        } else if (conn.webflow_workspace_id) {
          nameEl.textContent = `Workspace ${conn.webflow_workspace_id}`;
        } else {
          nameEl.textContent = "Webflow Connection";
        }
      }

      // Set connected date
      const dateEl = clone.querySelector(".webflow-connected-date");
      if (dateEl) {
        dateEl.textContent = `Connected ${formatWebflowDate(conn.created_at)}`;
      }

      // Set connection ID on disconnect button
      const disconnectBtn = clone.querySelector(
        '[bbb-action="webflow-disconnect"]'
      );
      if (disconnectBtn) {
        disconnectBtn.setAttribute("bbb-id", conn.id);
      }

      connectionsList.appendChild(clone);
    }

    // Show sites configuration and load sites for the first connection
    if (sitesConfig && connections.length > 0) {
      sitesConfig.style.display = "block";
      // Load sites for the first connection (or the one specified in state)
      const targetConnectionId =
        webflowSitesState.connectionId || connections[0].id;
      loadWebflowSites(targetConnectionId);
    }
  } catch (error) {
    console.error("Failed to load Webflow connections:", error);
    // Don't show alert flow on simple load failure, just log
  }
}

/**
 * Initiate Webflow OAuth flow
 */
async function connectWebflow() {
  try {
    if (!window.dataBinder?.fetchData) {
      showWebflowError("System not ready. Please refresh the page.");
      return;
    }
    const response = await window.dataBinder.fetchData(
      "/v1/integrations/webflow",
      { method: "POST" }
    );

    if (response && response.auth_url) {
      // Redirect to Webflow OAuth
      window.location.href = response.auth_url;
    } else {
      throw new Error("No OAuth URL returned");
    }
  } catch (error) {
    console.error("Failed to start Webflow OAuth:", error);
    showWebflowError("Failed to connect to Webflow. Please try again.");
  }
}

/**
 * Disconnect a Webflow connection
 * @param {string} connectionId - The connection ID to disconnect
 */
async function disconnectWebflow(connectionId) {
  if (
    !confirm(
      "Are you sure you want to disconnect Webflow? Run on Publish will stop working."
    )
  ) {
    return;
  }

  try {
    const { data: { session } = {} } = await window.supabase.auth.getSession();
    const token = session?.access_token;
    if (!token) {
      showWebflowError("Not authenticated. Please sign in.");
      return;
    }
    const response = await fetchWithTimeout(
      `/v1/integrations/webflow/${encodeURIComponent(connectionId)}`,
      {
        method: "DELETE",
        headers: {
          Authorization: `Bearer ${token}`,
        },
      },
      { module: "webflow", action: "disconnect", connectionId }
    );

    if (!response.ok) {
      const text = await response.text();
      throw normaliseIntegrationError(response, text, {
        module: "webflow",
        action: "disconnect",
        connectionId,
      });
    }

    showWebflowSuccess("Webflow disconnected");
    loadWebflowConnections();
  } catch (error) {
    console.error("Failed to disconnect Webflow:", error);
    showWebflowError("Failed to disconnect Webflow");
  }
}

/**
 * Show a success message
 * Uses dashboard's generic integration feedback helper if available
 */
function showWebflowSuccess(message) {
  if (window.showIntegrationFeedback) {
    window.showIntegrationFeedback("webflow", "success", message);
  } else if (window.showDashboardSuccess) {
    window.showDashboardSuccess(message);
  } else {
    alert(message);
  }
}

/**
 * Show an error message
 * Uses dashboard's generic integration feedback helper if available
 */
function showWebflowError(message) {
  if (window.showIntegrationFeedback) {
    window.showIntegrationFeedback("webflow", "error", message);
  } else if (window.showDashboardError) {
    window.showDashboardError(message);
  } else {
    alert(message);
  }
}

/**
 * Handle OAuth callback result checks (if user returns here)
 */
function handleWebflowOAuthCallback() {
  const params = new URLSearchParams(window.location.search);
  const webflowConnected = params.get("webflow_connected");
  const webflowSetup = params.get("webflow_setup");
  const connectionId = params.get("webflow_connection_id");
  const webflowError = params.get("webflow_error");

  if (webflowSetup === "true" || webflowConnected) {
    // Clean up URL
    const url = new URL(window.location.href);
    url.searchParams.delete("webflow_connected");
    url.searchParams.delete("webflow_setup");
    url.searchParams.delete("webflow_connection_id");
    window.history.replaceState({}, "", url.toString());

    // Store connection ID if provided
    if (connectionId) {
      webflowSitesState.connectionId = connectionId;
    }

    // Show success message for new connections
    if (webflowConnected) {
      showWebflowSuccess("Webflow connected! Configure your sites below.");
    }

    // Focus the settings page and Webflow section
    focusWebflowSettings();

    // Load connections (which will also load sites)
    loadWebflowConnections();

    if (window.opener && !window.opener.closed) {
      const payload = {
        source: "bbb-webflow-connect",
        type: "webflow-connect-complete",
        connected: true,
        setup: webflowSetup === "true",
        integration: "webflow",
        connectionId,
      };

      try {
        window.opener.postMessage(payload, "*");
      } catch (error) {
        console.error(
          "Failed to post Webflow connection message to opener",
          error
        );
      }

      window.setTimeout(() => {
        window.close();
      }, 150);
    }
  } else if (webflowError) {
    showWebflowError(`Failed to connect Webflow: ${webflowError}`);
    const url = new URL(window.location.href);
    url.searchParams.delete("webflow_error");
    window.history.replaceState({}, "", url.toString());

    if (window.opener && !window.opener.closed) {
      const payload = {
        source: "bbb-webflow-connect",
        type: "webflow-connect-complete",
        connected: false,
        error: webflowError,
        integration: "webflow",
      };

      try {
        window.opener.postMessage(payload, "*");
      } catch (error) {
        console.error(
          "Failed to post Webflow connection error to opener",
          error
        );
      }
    }
  }
}

/**
 * Focus settings and the Webflow section
 */
function focusWebflowSettings() {
  const targetPath = "/settings/automated-jobs";
  const targetHash = "#webflow";
  const currentPath = window.location.pathname.replace(/\/$/, "");

  if (currentPath !== targetPath) {
    window.location.href = `${targetPath}${targetHash}`;
    return;
  }

  if (window.location.hash !== targetHash) {
    window.location.hash = targetHash;
  }

  setTimeout(() => {
    const webflowSection = document.getElementById("webflowSitesConfig");
    if (webflowSection) {
      webflowSection.scrollIntoView({ behavior: "smooth", block: "start" });
    }
  }, 200);
}

/**
 * Load sites from Webflow API for a connection
 * @param {string} connectionId - The connection ID
 * @param {number} page - Page number (default 1)
 */
async function loadWebflowSites(connectionId, page = 1) {
  if (webflowSitesState.loading) return;
  webflowSitesState.loading = true;

  const loadingEl = document.getElementById("webflowSitesLoading");
  const emptyEl = document.getElementById("webflowSitesEmpty");
  const listEl = document.getElementById("webflowSitesList");

  if (loadingEl) loadingEl.style.display = "block";
  if (emptyEl) emptyEl.style.display = "none";

  try {
    const { data: { session } = {} } = await window.supabase.auth.getSession();
    const token = session?.access_token;
    if (!token) {
      showWebflowError("Not authenticated. Please sign in.");
      webflowSitesState.loading = false;
      if (loadingEl) loadingEl.style.display = "none";
      return;
    }

    const response = await fetchWithTimeout(
      `/v1/integrations/webflow/${encodeURIComponent(connectionId)}/sites`,
      {
        headers: {
          Authorization: `Bearer ${token}`,
        },
      },
      { module: "webflow", action: "list-sites", connectionId }
    );

    if (!response.ok) {
      const text = await response.text();
      throw normaliseIntegrationError(response, text, {
        module: "webflow",
        action: "list-sites",
        connectionId,
      });
    }

    const json = await response.json();
    const data = json && json.data ? json.data : { sites: [] };
    const sites = Array.isArray(data.sites) ? data.sites : [];

    webflowSitesState.connectionId = connectionId;
    webflowSitesState.sites = sites;
    webflowSitesState.filteredSites = [...sites];
    webflowSitesState.currentPage = page;

    // Show search box if >5 sites
    const searchBox = document.getElementById("webflowSitesSearchBox");
    if (searchBox) {
      searchBox.style.display = sites.length > 5 ? "block" : "none";
    }

    renderWebflowSites(page);
  } catch (error) {
    console.error("Failed to load Webflow sites:", error);
    showWebflowError("Failed to load sites. Please try again.");
  } finally {
    webflowSitesState.loading = false;
    if (loadingEl) loadingEl.style.display = "none";
  }
}

/**
 * Render sites list with pagination
 * @param {number} page - Page number
 */
function renderWebflowSites(page = 1) {
  const listEl = document.getElementById("webflowSitesList");
  const emptyEl = document.getElementById("webflowSitesEmpty");
  const loadingEl = document.getElementById("webflowSitesLoading");
  const paginationEl = document.getElementById("webflowSitesPagination");
  const template = listEl?.querySelector('[bbb-template="webflow-site"]');

  if (!listEl || !template) return;
  if (loadingEl) loadingEl.style.display = "none";

  // Clear existing site rows (except template)
  const existingRows = listEl.querySelectorAll(
    ".webflow-site-row:not([bbb-template])"
  );
  existingRows.forEach((el) => el.remove());

  const sites = webflowSitesState.filteredSites;

  if (sites.length === 0) {
    if (emptyEl) emptyEl.style.display = "block";
    if (paginationEl) paginationEl.style.display = "none";
    return;
  }

  if (emptyEl) emptyEl.style.display = "none";
  webflowSitesState.currentPage = page;

  // Paginate
  const startIdx = (page - 1) * webflowSitesState.sitesPerPage;
  const endIdx = startIdx + webflowSitesState.sitesPerPage;
  const pageSites = sites.slice(startIdx, endIdx);
  const totalPages = Math.ceil(sites.length / webflowSitesState.sitesPerPage);

  // Build site rows
  for (const site of pageSites) {
    const clone = template.cloneNode(true);
    clone.style.display = "block";
    clone.removeAttribute("bbb-template");
    clone.dataset.siteId = site.webflow_site_id;
    clone.dataset.connectionId = webflowSitesState.connectionId;

    // Set site name
    const nameEl = clone.querySelector(".site-name");
    if (nameEl) {
      nameEl.textContent =
        site.display_name || site.site_name || "Unnamed Site";
    }

    // Set domain
    const domainEl = clone.querySelector(".site-domain");
    if (domainEl && site.primary_domain) {
      domainEl.textContent = site.primary_domain;
    }

    // Set schedule dropdown value
    const scheduleSelect = clone.querySelector(".site-schedule");
    if (scheduleSelect) {
      scheduleSelect.value = site.schedule_interval_hours ?? "";
      scheduleSelect.dataset.siteId = site.webflow_site_id;
      scheduleSelect.dataset.connectionId = webflowSitesState.connectionId;
      scheduleSelect.addEventListener("change", handleScheduleChange);
    }

    // Set auto-publish toggle (standard change event on checkbox)
    const autoPublishToggle = clone.querySelector(".site-autopublish");
    if (autoPublishToggle) {
      autoPublishToggle.checked = site.auto_publish_enabled || false;
      autoPublishToggle.dataset.siteId = site.webflow_site_id;
      autoPublishToggle.dataset.connectionId = webflowSitesState.connectionId;
      autoPublishToggle.addEventListener("change", handleAutoPublishToggle);
    }

    listEl.appendChild(clone);
  }

  // Update pagination
  if (paginationEl) {
    paginationEl.style.display = totalPages > 1 ? "flex" : "none";
    const prevBtn = document.getElementById("webflowSitesPrevPage");
    const nextBtn = document.getElementById("webflowSitesNextPage");
    const pageInfo = document.getElementById("webflowSitesPageInfo");

    if (prevBtn) prevBtn.disabled = page <= 1;
    if (nextBtn) nextBtn.disabled = page >= totalPages;
    if (pageInfo) pageInfo.textContent = `Page ${page} of ${totalPages}`;
  }
}

/**
 * Handle schedule dropdown change
 * @param {Event} event - Change event
 */
async function handleScheduleChange(event) {
  const select = event.target;
  const siteId = select.dataset.siteId;
  const connectionId = select.dataset.connectionId;
  const interval = select.value ? parseInt(select.value, 10) : null;

  // Disable while saving
  select.disabled = true;

  try {
    const { data: { session } = {} } = await window.supabase.auth.getSession();
    const token = session?.access_token;
    if (!token) {
      showWebflowError("Not authenticated. Please sign in.");
      select.disabled = false;
      return;
    }

    const response = await fetchWithTimeout(
      `/v1/integrations/webflow/sites/${encodeURIComponent(siteId)}/schedule`,
      {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${token}`,
        },
        body: JSON.stringify({
          connection_id: connectionId,
          schedule_interval_hours: interval,
        }),
      },
      { module: "webflow", action: "update-schedule", siteId, connectionId }
    );

    if (!response.ok) {
      const text = await response.text();
      throw normaliseIntegrationError(response, text, {
        module: "webflow",
        action: "update-schedule",
        siteId,
        connectionId,
      });
    }

    // Update local state
    const site = webflowSitesState.sites.find(
      (s) => s.webflow_site_id === siteId
    );
    if (site) {
      site.schedule_interval_hours = interval;
    }

    if (interval) {
      let autoPublishEnabled = false;
      try {
        await setWebflowAutoPublishForSite(siteId, connectionId, true);
        autoPublishEnabled = true;
      } catch (autoPublishError) {
        console.error(
          "Failed to auto-enable run-on-publish:",
          autoPublishError
        );
        showWebflowError(
          "Schedule saved, but run-on-publish could not be enabled automatically."
        );
      }
      if (site) {
        site.auto_publish_enabled = autoPublishEnabled;
      }
      const row = select.closest(".webflow-site-row");
      const rowToggle = row?.querySelector(".site-autopublish");
      if (rowToggle) {
        rowToggle.checked = autoPublishEnabled;
      }
    }

    // Brief visual feedback
    select.style.borderColor = "#10b981";
    setTimeout(() => {
      select.style.borderColor = "";
    }, 1000);
  } catch (error) {
    console.error("Failed to update schedule:", error);
    showWebflowError("Failed to save schedule");
    // Revert selection
    const site = webflowSitesState.sites.find(
      (s) => s.webflow_site_id === siteId
    );
    if (site) {
      select.value = site.schedule_interval_hours ?? "";
    }
  } finally {
    select.disabled = false;
  }
}

async function setWebflowAutoPublishForSite(siteId, connectionId, enabled) {
  const { data: { session } = {} } = await window.supabase.auth.getSession();
  const token = session?.access_token;
  if (!token) {
    throw new Error("Not authenticated. Please sign in.");
  }

  const response = await fetchWithTimeout(
    `/v1/integrations/webflow/sites/${encodeURIComponent(siteId)}/auto-publish`,
    {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${token}`,
      },
      body: JSON.stringify({
        connection_id: connectionId,
        enabled,
      }),
    },
    { module: "webflow", action: "set-auto-publish", siteId, connectionId }
  );

  if (!response.ok) {
    const text = await response.text();
    throw normaliseIntegrationError(response, text, {
      module: "webflow",
      action: "set-auto-publish",
      siteId,
      connectionId,
    });
  }
}

/**
 * Handle auto-publish toggle change
 * @param {Event} event - Change event
 */
async function handleAutoPublishToggle(event) {
  const toggle = event.target;
  const siteId = toggle.dataset.siteId;
  const connectionId = toggle.dataset.connectionId;
  const enabled = toggle.checked;

  // Disable while saving
  toggle.disabled = true;

  // Show status
  const row = toggle.closest(".webflow-site-row");
  const statusEl = row?.querySelector(".site-status");
  if (statusEl) {
    statusEl.style.display = "block";
    statusEl.textContent = enabled
      ? "Registering webhook..."
      : "Removing webhook...";
    statusEl.style.color = "#6b7280";
  }

  try {
    const { data: { session } = {} } = await window.supabase.auth.getSession();
    const token = session?.access_token;
    if (!token) {
      showWebflowError("Not authenticated. Please sign in.");
      toggle.checked = !enabled;
      toggle.disabled = false;
      if (statusEl) {
        statusEl.textContent = "Not authenticated";
        statusEl.style.color = "#ef4444";
      }
      return;
    }

    const response = await fetchWithTimeout(
      `/v1/integrations/webflow/sites/${encodeURIComponent(siteId)}/auto-publish`,
      {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${token}`,
        },
        body: JSON.stringify({
          connection_id: connectionId,
          enabled: enabled,
        }),
      },
      { module: "webflow", action: "toggle-auto-publish", siteId, connectionId }
    );

    if (!response.ok) {
      const text = await response.text();
      throw normaliseIntegrationError(response, text, {
        module: "webflow",
        action: "toggle-auto-publish",
        siteId,
        connectionId,
      });
    }

    // Update local state
    const site = webflowSitesState.sites.find(
      (s) => s.webflow_site_id === siteId
    );
    if (site) {
      site.auto_publish_enabled = enabled;
    }

    // Success feedback
    if (statusEl) {
      statusEl.textContent = enabled ? "Webhook active" : "";
      statusEl.style.color = "#10b981";
      setTimeout(() => {
        statusEl.style.display = "none";
      }, 2000);
    }
  } catch (error) {
    console.error("Failed to toggle auto-publish:", error);
    showWebflowError("Failed to update Run on Publish");
    // Revert toggle
    toggle.checked = !enabled;
    if (statusEl) {
      statusEl.textContent = "Failed to update";
      statusEl.style.color = "#dc2626";
    }
  } finally {
    toggle.disabled = false;
  }
}

/**
 * Handle site search input
 * @param {Event} event - Input event
 */
function handleSiteSearch(event) {
  const query = event.target.value.toLowerCase().trim();
  webflowSitesState.searchQuery = query;

  if (!query) {
    webflowSitesState.filteredSites = [...webflowSitesState.sites];
  } else {
    webflowSitesState.filteredSites = webflowSitesState.sites.filter(
      (site) =>
        (site.display_name || site.site_name || "")
          .toLowerCase()
          .includes(query) ||
        (site.primary_domain || "").toLowerCase().includes(query)
    );
  }

  renderWebflowSites(1);
}

// Export functions
if (typeof window !== "undefined") {
  window.setupWebflowIntegration = setupWebflowIntegration;
  window.loadWebflowConnections = loadWebflowConnections;
  window.loadWebflowSites = loadWebflowSites;
  window.handleWebflowOAuthCallback = handleWebflowOAuthCallback;
}
