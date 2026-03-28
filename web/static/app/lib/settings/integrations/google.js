/**
 * lib/settings/integrations/google.js — Google Analytics integration module
 *
 * Handles GA4 property connections with two-step account/property selection.
 * Flow: Connect -> OAuth -> Select Account (if multiple) -> Review Properties -> Save All
 */

import { post } from "/app/lib/api-client.js";
import { getAccessToken } from "/app/lib/auth-session.js";
import {
  fetchWithTimeout,
  normaliseIntegrationError,
} from "/app/lib/integration-http.js";
import { showToast as _showToast } from "/app/components/hover-toast.js";
import { formatRelativeDate } from "/app/lib/settings/integrations/shared.js";

function toast(variant, message) {
  _showToast(message, { variant });
}

// ── State ───────────────────────────────────────────────────────────────────────

let pendingGASessionData = null;
const selectedPropertyIds = new Set();
let allProperties = [];

function resetSelectedProperties() {
  selectedPropertyIds.clear();
}

// ── Setup ───────────────────────────────────────────────────────────────────────

export function setupGoogleIntegration() {
  document.addEventListener("click", (event) => {
    const element = event.target.closest("[gnh-action]");
    if (!element) return;
    const action = element.getAttribute("gnh-action");
    if (!action || !action.startsWith("google-")) return;
    event.preventDefault();
    handleGoogleAction(action, element);
  });
}

function handleGoogleAction(action, element) {
  switch (action) {
    case "google-connect":
      connectGoogle();
      break;
    case "google-disconnect": {
      const connectionId = element.getAttribute("gnh-id");
      if (connectionId) disconnectGoogle(connectionId);
      else console.warn("google-disconnect: missing gnh-id attribute");
      break;
    }
    case "google-refresh":
      loadGoogleConnections();
      break;
    case "google-select-account": {
      const accountId = element.getAttribute("data-account-id");
      if (accountId) selectGoogleAccount(accountId);
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

// ── Connections ──────────────────────────────────────────────────────────────────

export async function loadGoogleConnections() {
  try {
    resetSelectedProperties();
    const token = await getAccessToken();
    if (!token) return;

    const response = await fetchWithTimeout(
      "/v1/integrations/google",
      { headers: { Authorization: `Bearer ${token}` } },
      { module: "google", action: "list" }
    );
    if (!response.ok) {
      const text = await response.text();
      throw normaliseIntegrationError(response, text, {
        module: "google",
        action: "list",
      });
    }
    const json = await response.json();
    const connections = json?.data || json || [];

    const connectionsList = document.getElementById("googleConnectionsList");
    const emptyState = document.getElementById("googleEmptyState");
    const propertySelection = document.getElementById(
      "googlePropertySelection"
    );
    if (!connectionsList) return;

    const template = connectionsList.querySelector(
      '[gnh-template="google-connection"]'
    );
    if (!template) {
      console.error("Google connection template not found");
      return;
    }

    connectionsList
      .querySelectorAll(".google-connection")
      .forEach((el) => el.remove());

    if (!connections || connections.length === 0) {
      if (propertySelection) propertySelection.style.display = "none";
      if (emptyState) emptyState.style.display = "block";
      return;
    }

    if (emptyState) emptyState.style.display = "none";
    if (propertySelection) propertySelection.style.display = "none";

    for (const conn of connections) {
      const clone = template.cloneNode(true);
      clone.style.display = "block";
      clone.removeAttribute("gnh-template");
      clone.classList.add("google-connection");

      const nameEl = clone.querySelector(".google-name");
      if (nameEl) {
        nameEl.textContent =
          conn.ga4_property_name ||
          (conn.ga4_property_id
            ? `Property ${conn.ga4_property_id}`
            : "Google Analytics Connection");
      }

      const emailEl = clone.querySelector(".google-email");
      if (emailEl && conn.google_email) emailEl.textContent = conn.google_email;

      const dateEl = clone.querySelector(".google-connected-date");
      if (dateEl)
        dateEl.textContent = `Connected ${formatRelativeDate(conn.created_at)}`;

      const statusEl = clone.querySelector(".google-status");
      if (statusEl) {
        const isActive = conn.status === "active";
        statusEl.textContent = isActive ? "Active" : "Inactive";
        statusEl.classList.toggle("status-active", isActive);
        statusEl.classList.toggle("status-inactive", !isActive);
      }

      const disconnectBtn = clone.querySelector(
        '[gnh-action="google-disconnect"]'
      );
      if (disconnectBtn) disconnectBtn.setAttribute("gnh-id", conn.id);

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

async function connectGoogle() {
  try {
    const response = await post("/v1/integrations/google");
    if (response?.auth_url) {
      window.location.href = response.auth_url;
    } else {
      throw new Error("No OAuth URL returned");
    }
  } catch (error) {
    console.error("Failed to start Google OAuth:", error);
    toast("error", "Failed to connect to Google. Please try again.");
  }
}

async function disconnectGoogle(connectionId) {
  if (!confirm("Are you sure you want to disconnect Google Analytics?")) return;

  try {
    const token = await getAccessToken();
    if (!token) {
      toast("error", "Not authenticated. Please sign in.");
      return;
    }

    const response = await fetchWithTimeout(
      `/v1/integrations/google/${encodeURIComponent(connectionId)}`,
      { method: "DELETE", headers: { Authorization: `Bearer ${token}` } },
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

    toast("success", "Google Analytics disconnected");
    loadGoogleConnections();
  } catch (error) {
    console.error("Failed to disconnect Google:", error);
    toast("error", "Failed to disconnect Google Analytics");
  }
}

async function toggleConnectionStatus(connectionId, active) {
  try {
    const token = await getAccessToken();
    if (!token) {
      toast("error", "Not authenticated. Please sign in.");
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
        body: JSON.stringify({ status: active ? "active" : "inactive" }),
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
    loadGoogleConnections();
  } catch (error) {
    toast("error", "Failed to update status");
    loadGoogleConnections();
  }
}

// ── Account selection ───────────────────────────────────────────────────────────

async function selectGoogleAccount(accountId) {
  try {
    const token = await getAccessToken();
    if (!token) {
      toast("error", "Not authenticated. Please sign in.");
      return;
    }
    if (!pendingGASessionData?.session_id) {
      toast("error", "OAuth session expired. Please reconnect.");
      hideAccountSelection();
      return;
    }

    const accountList = document.getElementById("googleAccountList");
    if (accountList) {
      accountList.textContent = "";
      const loading = document.createElement("div");
      loading.style.cssText = "text-align: center; padding: 20px;";
      loading.textContent = "Loading properties...";
      accountList.appendChild(loading);
    }

    const response = await fetchWithTimeout(
      `/v1/integrations/google/pending-session/${pendingGASessionData.session_id}/accounts/${encodeURIComponent(accountId)}/properties`,
      { headers: { Authorization: `Bearer ${token}` } },
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

    pendingGASessionData.selected_account_id = accountId;
    pendingGASessionData.properties = properties;
    resetSelectedProperties();

    hideAccountSelection();
    showPropertySelection(properties);
  } catch (error) {
    console.error("Failed to fetch properties for account:", error);
    toast("error", "Failed to load properties. Please try again.");
  }
}

// ── Property selection ──────────────────────────────────────────────────────────

async function saveGoogleProperties() {
  try {
    const token = await getAccessToken();
    if (!token) {
      toast("error", "Not authenticated. Please sign in.");
      return;
    }
    if (!pendingGASessionData) {
      toast("error", "OAuth session expired. Please reconnect.");
      hidePropertySelection();
      return;
    }

    const activePropertyIdsList = [...selectedPropertyIds];
    const saveBtn = document.querySelector(
      '[gnh-action="google-save-properties"]'
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
          active_property_ids: activePropertyIdsList,
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

    pendingGASessionData = null;
    resetSelectedProperties();
    hidePropertySelection();

    const activeCount = activePropertyIdsList.length;
    const totalCount = allProperties.length;
    toast(
      "success",
      `Saved ${totalCount} properties (${activeCount} active, ${totalCount - activeCount} inactive)`
    );
    loadGoogleConnections();
  } catch (error) {
    console.error("Failed to save Google properties:", error);
    toast("error", "Failed to save properties");
  } finally {
    const saveBtn = document.querySelector(
      '[gnh-action="google-save-properties"]'
    );
    if (saveBtn) {
      saveBtn.disabled = false;
      saveBtn.textContent = "Save Properties";
    }
  }
}

function renderPropertyList(properties, totalCount) {
  const list = document.getElementById("googlePropertyList");
  if (!list) return;

  while (list.firstChild) list.removeChild(list.firstChild);

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

  for (const prop of properties) {
    const item = document.createElement("div");
    item.className = "gnh-job-card";
    item.style.cssText =
      "display: flex; align-items: center; width: 100%; margin-bottom: 8px; padding: 12px 16px; background: #f8f9fa; border: 1px solid #e9ecef; border-radius: 8px;";
    const propertyId = prop.property_id;
    item.setAttribute("data-property-id", propertyId);

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

    const isSelected = selectedPropertyIds.has(propertyId);
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

    toggleLabel.addEventListener("click", (e) => {
      e.preventDefault();
      const newActive = !toggleInput.checked;
      toggleInput.checked = newActive;
      if (newActive) {
        selectedPropertyIds.add(propertyId);
        track.style.backgroundColor = "#10b981";
        thumb.style.transform = "translateX(20px)";
        item.classList.add("selected");
      } else {
        selectedPropertyIds.delete(propertyId);
        track.style.backgroundColor = "#d1d5db";
        thumb.style.transform = "translateX(0)";
        item.classList.remove("selected");
      }
    });

    list.appendChild(item);
  }

  let saveContainer = document.getElementById("googlePropertySaveContainer");
  if (!saveContainer && properties.length > 0) {
    saveContainer = document.createElement("div");
    saveContainer.id = "googlePropertySaveContainer";
    saveContainer.style.cssText =
      "margin-top: 16px; padding-top: 16px; border-top: 1px solid #e5e7eb;";

    const saveBtn = document.createElement("button");
    saveBtn.className = "gnh-button gnh-button-primary";
    saveBtn.setAttribute("gnh-action", "google-save-properties");
    saveBtn.style.cssText = "width: 100%; padding: 12px;";
    saveBtn.textContent = "Save Properties";
    saveContainer.appendChild(saveBtn);

    const cancelBtn = document.createElement("button");
    cancelBtn.className = "gnh-button";
    cancelBtn.setAttribute("gnh-action", "google-cancel-selection");
    cancelBtn.style.cssText =
      "width: 100%; padding: 12px; margin-top: 8px; background: transparent;";
    cancelBtn.textContent = "Cancel";
    saveContainer.appendChild(cancelBtn);

    list.parentNode.appendChild(saveContainer);
  }
}

function filterGoogleProperties(query) {
  const lowerQuery = query.toLowerCase().trim();
  if (!lowerQuery) {
    renderPropertyList(allProperties, allProperties.length);
    return;
  }
  const filtered = allProperties.filter(
    (prop) =>
      (prop.display_name || "").toLowerCase().includes(lowerQuery) ||
      (prop.property_id || "").toLowerCase().includes(lowerQuery) ||
      (prop.account_name || "").toLowerCase().includes(lowerQuery)
  );
  renderPropertyList(filtered, allProperties.length);
}

function showPropertySelection(properties) {
  const selectionUI = document.getElementById("googlePropertySelection");
  const list = document.getElementById("googlePropertyList");
  if (!selectionUI || !list) {
    console.error("Property selection UI not found");
    return;
  }

  allProperties = properties;

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
    searchInput.addEventListener("input", (e) =>
      filterGoogleProperties(e.target.value)
    );
    searchContainer.appendChild(searchInput);
    list.parentNode.insertBefore(searchContainer, list);
  } else {
    const input = searchContainer.querySelector("input");
    if (input) input.value = "";
  }

  renderPropertyList(properties, properties.length);

  const emptyState = document.getElementById("googleEmptyState");
  if (emptyState) emptyState.style.display = "none";
  selectionUI.style.display = "block";
}

function hidePropertySelection() {
  const selectionUI = document.getElementById("googlePropertySelection");
  if (selectionUI) selectionUI.style.display = "none";
  const searchInput = document.querySelector("#googlePropertySearch input");
  if (searchInput) searchInput.value = "";
  const saveContainer = document.getElementById("googlePropertySaveContainer");
  if (saveContainer) saveContainer.remove();
  allProperties = [];
  resetSelectedProperties();
}

function showAccountSelection(accounts) {
  let accountUI = document.getElementById("googleAccountSelection");
  if (!accountUI) {
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

  // Build account list with safe DOM
  accountUI.textContent = "";

  const heading = document.createElement("h3");
  heading.style.cssText =
    "margin: 0 0 8px 0; font-size: 16px; font-weight: 600;";
  heading.textContent = "Select Google Analytics Account";
  accountUI.appendChild(heading);

  const desc = document.createElement("p");
  desc.style.cssText = "color: #6b7280; font-size: 14px; margin: 0 0 16px 0;";
  desc.textContent = `You have access to ${accounts.length} accounts. Select one to view its properties.`;
  accountUI.appendChild(desc);

  const list = document.createElement("div");
  list.id = "googleAccountList";

  for (const account of accounts) {
    const item = document.createElement("button");
    item.type = "button";
    item.className = "gnh-button";
    item.style.cssText =
      "display: block; width: 100%; text-align: left; margin-bottom: 8px; padding: 12px 16px; cursor: pointer;";
    item.setAttribute("gnh-action", "google-select-account");
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

  const cancelBtn = document.createElement("button");
  cancelBtn.className = "gnh-button";
  cancelBtn.setAttribute("gnh-action", "google-cancel-selection");
  cancelBtn.style.cssText =
    "width: 100%; padding: 12px; margin-top: 8px; background: transparent;";
  cancelBtn.textContent = "Cancel";
  list.appendChild(cancelBtn);

  accountUI.appendChild(list);

  const emptyState = document.getElementById("googleEmptyState");
  if (emptyState) emptyState.style.display = "none";
  accountUI.style.display = "block";
}

function hideAccountSelection() {
  const accountUI = document.getElementById("googleAccountSelection");
  if (accountUI) accountUI.style.display = "none";
}

// ── OAuth callback ──────────────────────────────────────────────────────────────

export async function handleGoogleOAuthCallback() {
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
    const url = new URL(window.location.href);
    url.searchParams.delete("google_connected");
    window.history.replaceState({}, "", url.toString());
    toast("success", "Google Analytics connected successfully!");
    loadGoogleConnections();
  } else if (gaSession) {
    try {
      const token = await getAccessToken();
      if (!token) {
        toast("error", "Not authenticated. Please sign in.");
        return;
      }

      const response = await fetchWithTimeout(
        `/v1/integrations/google/pending-session/${gaSession}`,
        { headers: { Authorization: `Bearer ${token}` } },
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
      sessionData.session_id = gaSession;
      pendingGASessionData = sessionData;
      resetSelectedProperties();

      const analyticsSection = document.getElementById(
        "googleAnalyticsSection"
      );
      if (analyticsSection)
        analyticsSection.scrollIntoView({ behavior: "smooth", block: "start" });

      const accounts = sessionData.accounts || [];
      const properties = sessionData.properties || [];

      if (accounts.length > 1 && properties.length === 0) {
        showAccountSelection(accounts);
      } else if (properties.length > 0) {
        showPropertySelection(properties);
      } else if (accounts.length === 1) {
        selectGoogleAccount(accounts[0].account_id);
      } else {
        throw new Error("No accounts or properties found");
      }

      const url = new URL(window.location.href);
      url.searchParams.delete("ga_session");
      window.history.replaceState({}, "", url.toString());
    } catch (e) {
      console.error("Failed to load session:", e);
      toast("error", "Session expired. Please reconnect to Google Analytics.");
      const url = new URL(window.location.href);
      url.searchParams.delete("ga_session");
      window.history.replaceState({}, "", url.toString());
    }
  } else if (googleError) {
    toast("error", `Failed to connect Google Analytics: ${googleError}`);
    const url = new URL(window.location.href);
    url.searchParams.delete("google_error");
    window.history.replaceState({}, "", url.toString());
  }
}
