/**
 * Google Analytics Integration Handler
 * Handles GA4 property connections with two-step account/property selection.
 * Flow: Connect -> OAuth -> Select Account (if multiple) -> Review Properties -> Save All
 */

/**
 * Formats a timestamp as a relative date string
 * @param {string} timestamp - ISO timestamp string
 * @returns {string} Formatted date string
 */
function formatGoogleDate(timestamp) {
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
    "Missing or incompatible integration HTTP helpers. Load /js/bb-integration-http.js before bb-google.js."
  );
}

var fetchWithTimeout = integrationHttp.fetchWithTimeout;
var normaliseIntegrationError = integrationHttp.normaliseIntegrationError;

/**
 * Initialise Google Analytics integration UI handlers
 */
function setupGoogleIntegration() {
  document.addEventListener("click", (event) => {
    const element = event.target.closest("[bbb-action]");
    if (!element) {
      return;
    }

    const action = element.getAttribute("bbb-action");
    if (!action || !action.startsWith("google-")) {
      return;
    }

    event.preventDefault();
    handleGoogleAction(action, element);
  });
}

/**
 * Handle Google Analytics-specific actions
 * @param {string} action - The action to perform
 * @param {HTMLElement} element - The element that triggered the action
 */
function handleGoogleAction(action, element) {
  switch (action) {
    case "google-connect":
      connectGoogle();
      break;

    case "google-disconnect": {
      const connectionId = element.getAttribute("bbb-id");
      if (connectionId) {
        disconnectGoogle(connectionId);
      } else {
        console.warn("google-disconnect: missing bbb-id attribute");
      }
      break;
    }

    case "google-refresh":
      loadGoogleConnections();
      break;

    case "google-select-account": {
      const accountId = element.getAttribute("data-account-id");
      if (accountId) {
        selectGoogleAccount(accountId);
      }
      break;
    }

    case "google-save-properties":
      saveGoogleProperties();
      break;

    case "google-cancel-selection":
      hidePropertySelection();
      hideAccountSelection();
      loadGoogleConnections();
      break;

    default:
      break;
  }
}

/**
 * Load and display Google Analytics connections for the current organisation
 */
async function loadGoogleConnections() {
  try {
    resetSelectedGoogleProperties();
    if (!window.dataBinder?.fetchData) {
      console.warn(
        "dataBinder not available, skipping Google connections load"
      );
      return;
    }
    const connections = await window.dataBinder.fetchData(
      "/v1/integrations/google"
    );

    const connectionsList = document.getElementById("googleConnectionsList");
    const emptyState = document.getElementById("googleEmptyState");
    const propertySelection = document.getElementById(
      "googlePropertySelection"
    );

    if (!connectionsList) {
      return;
    }

    const template = connectionsList.querySelector(
      '[bbb-template="google-connection"]'
    );

    if (!template) {
      console.error("Google connection template not found");
      return;
    }

    // Clear existing connections (except template)
    const existingConnections =
      connectionsList.querySelectorAll(".google-connection");
    existingConnections.forEach((el) => el.remove());

    if (!connections || connections.length === 0) {
      // No connections - show empty state message, hide property selection
      if (propertySelection) propertySelection.style.display = "none";
      if (emptyState) emptyState.style.display = "block";
      return;
    }

    // Has connections - hide empty state message AND property selection, show connections
    if (emptyState) emptyState.style.display = "none";
    if (propertySelection) propertySelection.style.display = "none";

    // Build connection elements
    for (const conn of connections) {
      const clone = template.cloneNode(true);
      clone.style.display = "block";
      clone.removeAttribute("bbb-template");
      clone.classList.add("google-connection");

      // Set property name
      const nameEl = clone.querySelector(".google-name");
      if (nameEl) {
        if (conn.ga4_property_name) {
          nameEl.textContent = conn.ga4_property_name;
        } else if (conn.ga4_property_id) {
          nameEl.textContent = `Property ${conn.ga4_property_id}`;
        } else {
          nameEl.textContent = "Google Analytics Connection";
        }
      }

      // Set Google email
      const emailEl = clone.querySelector(".google-email");
      if (emailEl && conn.google_email) {
        emailEl.textContent = conn.google_email;
      }

      // Set connected date
      const dateEl = clone.querySelector(".google-connected-date");
      if (dateEl) {
        dateEl.textContent = `Connected ${formatGoogleDate(conn.created_at)}`;
      }

      // Set status indicator
      const statusEl = clone.querySelector(".google-status");
      if (statusEl) {
        const isActive = conn.status === "active";
        statusEl.textContent = isActive ? "Active" : "Inactive";
        statusEl.classList.toggle("status-active", isActive);
        statusEl.classList.toggle("status-inactive", !isActive);
      }

      // Set connection ID on disconnect button
      const disconnectBtn = clone.querySelector(
        '[bbb-action="google-disconnect"]'
      );
      if (disconnectBtn) {
        disconnectBtn.setAttribute("bbb-id", conn.id);
      }

      // Set up status toggle (uses bb-toggle pattern - CSS handles visual state)
      const statusToggle = clone.querySelector(".google-status-toggle");
      if (statusToggle) {
        statusToggle.checked = conn.status === "active";
        statusToggle.setAttribute("data-connection-id", conn.id);

        statusToggle.addEventListener("change", async () => {
          await toggleConnectionStatus(conn.id, statusToggle.checked);
        });
      }

      connectionsList.appendChild(clone);
    }
  } catch (error) {
    console.error("Failed to load Google connections:", error);
  }
}

/**
 * Initiate Google OAuth flow
 */
async function connectGoogle() {
  try {
    if (!window.dataBinder?.fetchData) {
      showGoogleError("System not ready. Please refresh the page.");
      return;
    }
    const response = await window.dataBinder.fetchData(
      "/v1/integrations/google",
      { method: "POST" }
    );

    if (response && response.auth_url) {
      // Redirect to Google OAuth
      window.location.href = response.auth_url;
    } else {
      throw new Error("No OAuth URL returned");
    }
  } catch (error) {
    console.error("Failed to start Google OAuth:", error);
    showGoogleError("Failed to connect to Google. Please try again.");
  }
}

/**
 * Disconnect a Google Analytics connection
 * @param {string} connectionId - The connection ID to disconnect
 */
async function disconnectGoogle(connectionId) {
  if (!confirm("Are you sure you want to disconnect Google Analytics?")) {
    return;
  }

  try {
    const { data: { session } = {} } = await window.supabase.auth.getSession();
    const token = session?.access_token;
    if (!token) {
      showGoogleError("Not authenticated. Please sign in.");
      return;
    }
    const response = await fetchWithTimeout(
      `/v1/integrations/google/${encodeURIComponent(connectionId)}`,
      {
        method: "DELETE",
        headers: {
          Authorization: `Bearer ${token}`,
        },
      },
      { module: "google", action: "disconnect", connectionId }
    );

    if (!response.ok) {
      const text = await response.text();
      throw normaliseIntegrationError(response, text, {
        module: "google",
        action: "disconnect",
        connectionId,
      });
    }

    showGoogleSuccess("Google Analytics disconnected");
    loadGoogleConnections();
  } catch (error) {
    console.error("Failed to disconnect Google:", error);
    showGoogleError("Failed to disconnect Google Analytics");
  }
}

/**
 * Select a Google Analytics account and fetch its properties
 * @param {string} accountId - The GA account ID
 */
async function selectGoogleAccount(accountId) {
  try {
    const { data: { session } = {} } = await window.supabase.auth.getSession();
    const token = session?.access_token;
    if (!token) {
      showGoogleError("Not authenticated. Please sign in.");
      return;
    }

    if (!pendingGASessionData || !pendingGASessionData.session_id) {
      showGoogleError("OAuth session expired. Please reconnect.");
      hideAccountSelection();
      return;
    }

    // Show loading state
    const accountList = document.getElementById("googleAccountList");
    if (accountList) {
      accountList.innerHTML =
        '<div style="text-align: center; padding: 20px;">Loading properties...</div>';
    }

    // Fetch properties for this account
    const fetchUrl = `/v1/integrations/google/pending-session/${pendingGASessionData.session_id}/accounts/${encodeURIComponent(accountId)}/properties`;
    const response = await fetchWithTimeout(
      fetchUrl,
      {
        headers: { Authorization: `Bearer ${token}` },
      },
      { module: "google", action: "fetch-properties", accountId }
    );

    if (!response.ok) {
      const text = await response.text();
      throw normaliseIntegrationError(response, text, {
        module: "google",
        action: "fetch-properties",
        accountId,
      });
    }

    const result = await response.json();
    const properties = result.data?.properties || [];

    // Store selected account and properties
    pendingGASessionData.selected_account_id = accountId;
    pendingGASessionData.properties = properties;
    resetSelectedGoogleProperties();

    // Hide account selection, show property selection
    hideAccountSelection();
    showPropertySelection(properties);
  } catch (error) {
    console.error("Failed to fetch properties for account:", error);
    showGoogleError("Failed to load properties. Please try again.");
  }
}

/**
 * Save all properties (bulk save with active/inactive status)
 */
async function saveGoogleProperties() {
  try {
    const { data: { session } = {} } = await window.supabase.auth.getSession();
    const token = session?.access_token;
    if (!token) {
      showGoogleError("Not authenticated. Please sign in.");
      return;
    }

    if (!pendingGASessionData) {
      showGoogleError("OAuth session expired. Please reconnect.");
      hidePropertySelection();
      return;
    }

    const activePropertyIds = [...selectedGooglePropertyIds];

    // Show saving state
    const saveBtn = document.querySelector(
      '[bbb-action="google-save-properties"]'
    );
    if (saveBtn) {
      saveBtn.disabled = true;
      saveBtn.textContent = "Saving...";
    }

    const response = await fetchWithTimeout(
      "/v1/integrations/google/save-properties",
      {
        method: "POST",
        headers: {
          Authorization: `Bearer ${token}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          session_id: pendingGASessionData.session_id,
          account_id:
            pendingGASessionData.selected_account_id ||
            pendingGASessionData.accounts?.[0]?.account_id,
          active_property_ids: activePropertyIds,
        }),
      },
      { module: "google", action: "save-properties" }
    );

    if (!response.ok) {
      const text = await response.text();
      throw normaliseIntegrationError(response, text, {
        module: "google",
        action: "save-properties",
      });
    }

    // Clear stored session data
    pendingGASessionData = null;
    resetSelectedGoogleProperties();

    hidePropertySelection();
    const activeCount = activePropertyIds.length;
    const totalCount = allGoogleProperties.length;
    showGoogleSuccess(
      `Saved ${totalCount} properties (${activeCount} active, ${totalCount - activeCount} inactive)`
    );
    loadGoogleConnections();
  } catch (error) {
    console.error("Failed to save Google properties:", error);
    showGoogleError("Failed to save properties");
  } finally {
    const saveBtn = document.querySelector(
      '[bbb-action="google-save-properties"]'
    );
    if (saveBtn) {
      saveBtn.disabled = false;
      saveBtn.textContent = "Save Properties";
    }
  }
}

/**
 * Toggle an existing connection's status (active/inactive)
 * @param {string} connectionId - The connection ID
 * @param {boolean} active - Whether to set active
 */
async function toggleConnectionStatus(connectionId, active) {
  try {
    const { data: { session } = {} } = await window.supabase.auth.getSession();
    const token = session?.access_token;
    if (!token) {
      showGoogleError("Not authenticated. Please sign in.");
      return;
    }

    const response = await fetchWithTimeout(
      `/v1/integrations/google/${encodeURIComponent(connectionId)}/status`,
      {
        method: "PATCH",
        headers: {
          Authorization: `Bearer ${token}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          status: active ? "active" : "inactive",
        }),
      },
      { module: "google", action: "toggle-status", connectionId }
    );

    if (!response.ok) {
      const text = await response.text();
      throw normaliseIntegrationError(response, text, {
        module: "google",
        action: "toggle-status",
        connectionId,
      });
    }
    // Reload to update UI
    loadGoogleConnections();
  } catch (error) {
    showGoogleError("Failed to update status");
    loadGoogleConnections(); // Reload to reset toggle state
  }
}

// Store all properties for filtering
let allGoogleProperties = [];
const MAX_VISIBLE_PROPERTIES = 10;

/**
 * Render filtered property list with toggle selection
 * @param {Array} properties - Filtered properties to display
 * @param {number} totalCount - Total number of properties before filtering
 */
function renderPropertyList(properties, totalCount) {
  const list = document.getElementById("googlePropertyList");
  if (!list) return;

  // Clear existing items
  while (list.firstChild) {
    list.removeChild(list.firstChild);
  }

  // Show count info and instructions
  const countInfo = document.createElement("div");
  countInfo.style.cssText =
    "color: #6b7280; font-size: 13px; margin-bottom: 12px;";
  if (properties.length === 0) {
    countInfo.textContent = "No properties match your search";
  } else if (properties.length < totalCount) {
    countInfo.textContent = `Showing ${properties.length} of ${totalCount} properties. Click to toggle active/inactive.`;
  } else {
    countInfo.textContent = `${totalCount} properties found. Click to toggle active/inactive.`;
  }
  list.appendChild(countInfo);

  // Add property options with toggle functionality
  for (const prop of properties) {
    const item = document.createElement("div");
    item.className = "bb-job-card";
    item.style.cssText =
      "display: flex; align-items: center; width: 100%; margin-bottom: 8px; padding: 12px 16px; background: #f8f9fa; border: 1px solid #e9ecef; border-radius: 8px;";
    const propertyId = prop.property_id;
    item.setAttribute("data-property-id", propertyId);

    // Property details
    const details = document.createElement("div");
    details.style.cssText = "flex: 1;";

    const strongEl = document.createElement("strong");
    strongEl.textContent = prop.display_name;
    strongEl.style.fontSize = "15px";
    details.appendChild(strongEl);

    const detailSpan = document.createElement("span");
    detailSpan.style.cssText =
      "color: #6b7280; font-size: 13px; display: block; margin-top: 2px;";
    detailSpan.textContent = `Property ID: ${prop.property_id}`;
    details.appendChild(detailSpan);

    item.appendChild(details);

    // Toggle switch
    const toggleLabel = document.createElement("label");
    toggleLabel.className = "property-toggle-container";
    toggleLabel.style.cssText =
      "display: inline-flex; align-items: center; cursor: pointer; user-select: none;";

    const toggleInput = document.createElement("input");
    toggleInput.type = "checkbox";
    toggleInput.className = "property-status-toggle";
    toggleInput.style.display = "none";
    toggleInput.setAttribute("data-property-id", propertyId);

    const track = document.createElement("div");
    track.className = "property-toggle-track";
    track.style.cssText =
      "position: relative; width: 44px; height: 24px; background-color: #d1d5db; border-radius: 12px; transition: background-color 0.2s;";

    const thumb = document.createElement("div");
    thumb.className = "property-toggle-thumb";
    thumb.style.cssText =
      "position: absolute; top: 2px; left: 2px; width: 20px; height: 20px; background-color: white; border-radius: 10px; transition: transform 0.2s; box-shadow: 0 1px 3px rgba(0, 0, 0, 0.2);";

    const isSelected = selectedGooglePropertyIds.has(propertyId);
    toggleInput.checked = isSelected;
    if (isSelected) {
      track.style.backgroundColor = "#10b981";
      thumb.style.transform = "translateX(20px)";
      item.classList.add("selected");
    }

    track.appendChild(thumb);
    toggleLabel.appendChild(toggleInput);
    toggleLabel.appendChild(track);
    item.appendChild(toggleLabel);

    // Add click handler
    toggleLabel.addEventListener("click", (e) => {
      e.preventDefault();
      const newActive = !toggleInput.checked;
      toggleInput.checked = newActive;

      if (newActive) {
        selectedGooglePropertyIds.add(propertyId);
        track.style.backgroundColor = "#10b981";
        thumb.style.transform = "translateX(20px)";
        item.classList.add("selected");
      } else {
        selectedGooglePropertyIds.delete(propertyId);
        track.style.backgroundColor = "#d1d5db";
        thumb.style.transform = "translateX(0)";
        item.classList.remove("selected");
      }
    });

    list.appendChild(item);
  }

  // Add save button if not already present
  let saveContainer = document.getElementById("googlePropertySaveContainer");
  if (!saveContainer && properties.length > 0) {
    saveContainer = document.createElement("div");
    saveContainer.id = "googlePropertySaveContainer";
    saveContainer.style.cssText =
      "margin-top: 16px; padding-top: 16px; border-top: 1px solid #e5e7eb;";

    const saveBtn = document.createElement("button");
    saveBtn.className = "bb-button bb-button-primary";
    saveBtn.setAttribute("bbb-action", "google-save-properties");
    saveBtn.style.cssText = "width: 100%; padding: 12px;";
    saveBtn.textContent = "Save Properties";
    saveContainer.appendChild(saveBtn);

    const cancelBtn = document.createElement("button");
    cancelBtn.className = "bb-button";
    cancelBtn.setAttribute("bbb-action", "google-cancel-selection");
    cancelBtn.style.cssText =
      "width: 100%; padding: 12px; margin-top: 8px; background: transparent;";
    cancelBtn.textContent = "Cancel";
    saveContainer.appendChild(cancelBtn);

    list.parentNode.appendChild(saveContainer);
  }
}

/**
 * Filter properties based on search query
 * @param {string} query - Search query
 */
function filterGoogleProperties(query) {
  const lowerQuery = query.toLowerCase().trim();
  if (!lowerQuery) {
    renderPropertyList(allGoogleProperties, allGoogleProperties.length);
    return;
  }

  const filtered = allGoogleProperties.filter(
    (prop) =>
      (prop.display_name || "").toLowerCase().includes(lowerQuery) ||
      (prop.property_id || "").toLowerCase().includes(lowerQuery) ||
      (prop.account_name || "").toLowerCase().includes(lowerQuery)
  );
  renderPropertyList(filtered, allGoogleProperties.length);
}

/**
 * Show property selection UI when multiple properties are available
 * @param {Array} properties - Array of GA4 properties to choose from
 */
function showPropertySelection(properties) {
  const selectionUI = document.getElementById("googlePropertySelection");
  const list = document.getElementById("googlePropertyList");

  if (!selectionUI || !list) {
    console.error("Property selection UI not found");
    return;
  }

  // Store all properties for filtering
  allGoogleProperties = properties;

  // Add search input if not already present
  let searchContainer = document.getElementById("googlePropertySearch");
  if (!searchContainer) {
    searchContainer = document.createElement("div");
    searchContainer.id = "googlePropertySearch";
    searchContainer.style.cssText = "margin-bottom: 16px;";

    const searchInput = document.createElement("input");
    searchInput.type = "text";
    searchInput.placeholder = "Search properties...";
    searchInput.style.cssText =
      "width: 100%; padding: 10px 12px; border: 1px solid #d1d5db; border-radius: 6px; font-size: 14px;";
    searchInput.addEventListener("input", (e) => {
      filterGoogleProperties(e.target.value);
    });

    searchContainer.appendChild(searchInput);
    list.parentNode.insertBefore(searchContainer, list);
  } else {
    // Clear existing search
    const input = searchContainer.querySelector("input");
    if (input) input.value = "";
  }

  // Render initial list (max 10)
  renderPropertyList(properties, properties.length);

  // Hide empty state and show selection
  const emptyState = document.getElementById("googleEmptyState");
  if (emptyState) emptyState.style.display = "none";
  selectionUI.style.display = "block";
}

/**
 * Hide property selection UI
 */
function hidePropertySelection() {
  const selectionUI = document.getElementById("googlePropertySelection");
  if (selectionUI) {
    selectionUI.style.display = "none";
  }
  // Clear search input if present
  const searchInput = document.querySelector("#googlePropertySearch input");
  if (searchInput) {
    searchInput.value = "";
  }
  // Remove save container if present
  const saveContainer = document.getElementById("googlePropertySaveContainer");
  if (saveContainer) {
    saveContainer.remove();
  }
  // Clear stored properties
  allGoogleProperties = [];
  resetSelectedGoogleProperties();
}

/**
 * Show account selection UI when multiple accounts are available
 * @param {Array} accounts - Array of GA accounts to choose from
 */
function showAccountSelection(accounts) {
  // Create or get the account selection UI
  let accountUI = document.getElementById("googleAccountSelection");
  if (!accountUI) {
    // Create the UI dynamically
    const propertySelection = document.getElementById(
      "googlePropertySelection"
    );
    if (propertySelection) {
      accountUI = document.createElement("div");
      accountUI.id = "googleAccountSelection";
      accountUI.style.cssText = "padding: 16px;";
      propertySelection.parentNode.insertBefore(accountUI, propertySelection);
    } else {
      console.error("Cannot find googlePropertySelection to insert account UI");
      return;
    }
  }

  // Build the account list
  accountUI.innerHTML = `
    <h3 style="margin: 0 0 8px 0; font-size: 16px; font-weight: 600;">Select Google Analytics Account</h3>
    <p style="color: #6b7280; font-size: 14px; margin: 0 0 16px 0;">
      You have access to ${accounts.length} accounts. Select one to view its properties.
    </p>
    <div id="googleAccountList"></div>
  `;

  const list = document.getElementById("googleAccountList");
  for (const account of accounts) {
    const item = document.createElement("button");
    item.type = "button";
    item.className = "bb-button";
    item.style.cssText =
      "display: block; width: 100%; text-align: left; margin-bottom: 8px; padding: 12px 16px; cursor: pointer;";
    item.setAttribute("bbb-action", "google-select-account");
    item.setAttribute("data-account-id", account.account_id);

    const strongEl = document.createElement("strong");
    strongEl.textContent = account.display_name || account.account_id;
    item.appendChild(strongEl);

    const detailSpan = document.createElement("span");
    detailSpan.style.cssText =
      "color: #6b7280; font-size: 13px; display: block;";
    detailSpan.textContent = `Account ID: ${account.account_id.replace("accounts/", "")}`;
    item.appendChild(detailSpan);

    list.appendChild(item);
  }

  // Add cancel button
  const cancelBtn = document.createElement("button");
  cancelBtn.className = "bb-button";
  cancelBtn.setAttribute("bbb-action", "google-cancel-selection");
  cancelBtn.style.cssText =
    "width: 100%; padding: 12px; margin-top: 8px; background: transparent;";
  cancelBtn.textContent = "Cancel";
  list.appendChild(cancelBtn);

  // Hide empty state and show account selection
  const emptyState = document.getElementById("googleEmptyState");
  if (emptyState) emptyState.style.display = "none";
  accountUI.style.display = "block";
}

/**
 * Hide account selection UI
 */
function hideAccountSelection() {
  const accountUI = document.getElementById("googleAccountSelection");
  if (accountUI) {
    accountUI.style.display = "none";
  }
}

/**
 * Show a success message
 */
function showGoogleSuccess(message) {
  if (window.showIntegrationFeedback) {
    window.showIntegrationFeedback("google", "success", message);
  } else if (window.showDashboardSuccess) {
    window.showDashboardSuccess(message);
  } else {
    const successEl = document.getElementById("googleSuccessMessage");
    const textEl = document.getElementById("googleSuccessText");
    if (successEl && textEl) {
      textEl.textContent = message;
      successEl.style.display = "block";
      setTimeout(() => {
        successEl.style.display = "none";
      }, 5000);
    } else {
      alert(message);
    }
  }
}

/**
 * Show an error message
 */
function showGoogleError(message) {
  if (window.showIntegrationFeedback) {
    window.showIntegrationFeedback("google", "error", message);
  } else if (window.showDashboardError) {
    window.showDashboardError(message);
  } else {
    const errorEl = document.getElementById("googleErrorMessage");
    const textEl = document.getElementById("googleErrorText");
    if (errorEl && textEl) {
      textEl.textContent = message;
      errorEl.style.display = "block";
      setTimeout(() => {
        errorEl.style.display = "none";
      }, 5000);
    } else {
      alert(message);
    }
  }
}

// Store pending session data for property selection
let pendingGASessionData = null;
const selectedGooglePropertyIds = new Set();

const resetSelectedGoogleProperties = () => {
  selectedGooglePropertyIds.clear();
};

/**
 * Handle OAuth callback result checks
 */
async function handleGoogleOAuthCallback() {
  const params = new URLSearchParams(window.location.search);
  const googleConnected = params.get("google_connected");
  const googleError = params.get("google_error");
  const gaSession = params.get("ga_session");
  const isSettingsPage = window.location.pathname.startsWith("/settings");

  if (gaSession && !isSettingsPage) {
    const redirectUrl = new URL("/settings/analytics", window.location.origin);
    redirectUrl.search = window.location.search;
    redirectUrl.hash = "google-analytics";
    window.location.replace(redirectUrl.toString());
    return;
  }

  if (googleConnected) {
    // Clean up URL
    const url = new URL(window.location.href);
    url.searchParams.delete("google_connected");
    window.history.replaceState({}, "", url.toString());

    showGoogleSuccess("Google Analytics connected successfully!");
    loadGoogleConnections();
  } else if (gaSession) {
    // Fetch session data from server
    try {
      const { data: { session } = {} } =
        await window.supabase.auth.getSession();
      const token = session?.access_token;
      if (!token) {
        showGoogleError("Not authenticated. Please sign in.");
        return;
      }

      const response = await fetchWithTimeout(
        `/v1/integrations/google/pending-session/${gaSession}`,
        {
          headers: { Authorization: `Bearer ${token}` },
        },
        { module: "google", action: "pending-session", gaSession }
      );

      if (!response.ok) {
        const text = await response.text();
        throw normaliseIntegrationError(response, text, {
          module: "google",
          action: "pending-session",
          gaSession,
        });
      }

      const result = await response.json();
      const sessionData = result.data;
      // Store session ID for subsequent requests
      sessionData.session_id = gaSession;
      pendingGASessionData = sessionData;
      resetSelectedGoogleProperties();

      // Ensure analytics section is visible
      const analyticsSection = document.getElementById(
        "googleAnalyticsSection"
      );
      if (analyticsSection) {
        analyticsSection.scrollIntoView({ behavior: "smooth", block: "start" });
      }

      // Determine which UI to show based on session data
      const accounts = sessionData.accounts || [];
      const properties = sessionData.properties || [];

      if (accounts.length > 1 && properties.length === 0) {
        // Multiple accounts, no properties yet - show account picker
        showAccountSelection(accounts);
      } else if (properties.length > 0) {
        // Single account with properties already fetched, or properties from selected account
        showPropertySelection(properties);
      } else if (accounts.length === 1) {
        // Single account but no properties - should not happen normally
        selectGoogleAccount(accounts[0].account_id);
      } else {
        throw new Error("No accounts or properties found");
      }

      // Clean up URL
      const url = new URL(window.location.href);
      url.searchParams.delete("ga_session");
      window.history.replaceState({}, "", url.toString());
    } catch (e) {
      console.error("Failed to load session:", e);
      showGoogleError("Session expired. Please reconnect to Google Analytics.");
      // Clean up URL
      const url = new URL(window.location.href);
      url.searchParams.delete("ga_session");
      window.history.replaceState({}, "", url.toString());
    }
  } else if (googleError) {
    showGoogleError(`Failed to connect Google Analytics: ${googleError}`);
    const url = new URL(window.location.href);
    url.searchParams.delete("google_error");
    window.history.replaceState({}, "", url.toString());
  }
}

// Export functions
if (typeof window !== "undefined") {
  window.setupGoogleIntegration = setupGoogleIntegration;
  window.loadGoogleConnections = loadGoogleConnections;
  window.handleGoogleOAuthCallback = handleGoogleOAuthCallback;
}
