/**
 * Slack Integration Handler
 * Handles Slack workspace connections for notifications.
 * Flow: Connect workspace → auto-links user with notifications enabled → Disconnect
 */

/**
 * Formats a timestamp as a relative date string
 * @param {string} timestamp - ISO timestamp string
 * @returns {string} Formatted date string
 */
function formatSlackDate(timestamp) {
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
    "Missing or incompatible integration HTTP helpers. Load /js/bb-integration-http.js before bb-slack.js."
  );
}

var fetchWithTimeout = integrationHttp.fetchWithTimeout;
var normaliseIntegrationError = integrationHttp.normaliseIntegrationError;

/**
 * Initialise Slack integration UI handlers
 */
function setupSlackIntegration() {
  document.addEventListener("click", (event) => {
    const element = event.target.closest("[bbb-action]");
    if (!element) {
      return;
    }

    const action = element.getAttribute("bbb-action");
    if (!action || !action.startsWith("slack-")) {
      return;
    }

    event.preventDefault();
    handleSlackAction(action, element);
  });
}

/**
 * Handle Slack-specific actions
 * @param {string} action - The action to perform
 * @param {HTMLElement} element - The element that triggered the action
 */
function handleSlackAction(action, element) {
  switch (action) {
    case "slack-connect":
      connectSlackWorkspace();
      break;

    case "slack-disconnect": {
      const connectionId = element.getAttribute("bbb-id");
      if (connectionId) {
        disconnectSlackWorkspace(connectionId);
      } else {
        console.warn("slack-disconnect: missing bbb-id attribute");
      }
      break;
    }

    case "slack-refresh":
      loadSlackConnections();
      break;

    default:
      break;
  }
}

/**
 * Load and display Slack connections for the current organisation
 */
async function loadSlackConnections() {
  try {
    const connections = await window.dataBinder.fetchData(
      "/v1/integrations/slack"
    );

    const connectionsList = document.getElementById("slackConnectionsList");
    const emptyState = document.getElementById("slackEmptyState");

    if (!connectionsList) {
      console.error("Slack connections list element not found");
      return;
    }

    const template = connectionsList.querySelector(
      '[bbb-template="slack-connection"]'
    );

    if (!template) {
      console.error("Slack connection template not found");
      return;
    }

    // Clear existing connections (except template)
    const existingConnections =
      connectionsList.querySelectorAll(".slack-connection");
    existingConnections.forEach((el) => el.remove());

    if (!connections || connections.length === 0) {
      if (emptyState) emptyState.style.display = "block";
      return;
    }

    if (emptyState) emptyState.style.display = "none";

    // Build connection elements
    for (const conn of connections) {
      const clone = template.cloneNode(true);
      clone.style.display = "block";
      clone.removeAttribute("bbb-template");
      clone.classList.add("slack-connection");

      // Set workspace name
      const nameEl = clone.querySelector(".slack-workspace-name");
      if (nameEl) {
        nameEl.textContent = conn.workspace_name || "Unknown Workspace";
      }

      // Set connected date
      const dateEl = clone.querySelector(".slack-connected-date");
      if (dateEl) {
        dateEl.textContent = `Connected ${formatSlackDate(conn.created_at)}`;
      }

      // Set connection ID on disconnect button
      const disconnectBtn = clone.querySelector(
        '[bbb-action="slack-disconnect"]'
      );
      if (disconnectBtn) {
        disconnectBtn.setAttribute("bbb-id", conn.id);
      }

      connectionsList.appendChild(clone);
    }
  } catch (error) {
    console.error("Failed to load Slack connections:", error);
    showSlackError("Failed to load Slack connections");
  }
}

/**
 * Initiate Slack OAuth flow to connect a new workspace
 */
async function connectSlackWorkspace() {
  try {
    const response = await window.dataBinder.fetchData(
      "/v1/integrations/slack/connect",
      { method: "POST" }
    );

    if (response && response.auth_url) {
      // Redirect to Slack OAuth
      window.location.href = response.auth_url;
    } else {
      throw new Error("No OAuth URL returned");
    }
  } catch (error) {
    console.error("Failed to start Slack OAuth:", error);
    showSlackError("Failed to connect to Slack. Please try again.");
  }
}

/**
 * Disconnect a Slack workspace
 * @param {string} connectionId - The connection ID to disconnect
 */
async function disconnectSlackWorkspace(connectionId) {
  if (
    !confirm(
      "Are you sure you want to disconnect this Slack workspace? You will no longer receive notifications."
    )
  ) {
    return;
  }

  try {
    // Use fetchWithTimeout for DELETE; response body is not required.
    const session = await window.supabase.auth.getSession();
    const token = session?.data?.session?.access_token;
    if (!token) {
      showSlackError("Not authenticated. Please sign in.");
      return;
    }

    const response = await fetchWithTimeout(
      `/v1/integrations/slack/${encodeURIComponent(connectionId)}`,
      {
        method: "DELETE",
        headers: {
          Authorization: `Bearer ${token}`,
        },
      },
      { module: "slack", action: "disconnect", connectionId }
    );

    if (!response.ok) {
      const text = await response.text();
      throw normaliseIntegrationError(response, text, {
        module: "slack",
        action: "disconnect",
        connectionId,
      });
    }

    showSlackSuccess("Slack workspace disconnected");
    loadSlackConnections();
  } catch (error) {
    console.error("Failed to disconnect Slack workspace:", error);
    showSlackError("Failed to disconnect Slack workspace");
  }
}

/**
 * Show a success message for Slack operations
 * @param {string} message - The message to display
 */
function showSlackSuccess(message) {
  if (window.showDashboardSuccess) {
    window.showDashboardSuccess(message);
  } else {
    alert(message);
  }
}

/**
 * Show an error message for Slack operations
 * @param {string} message - The message to display
 */
function showSlackError(message) {
  if (window.showDashboardError) {
    window.showDashboardError(message);
  } else {
    alert(message);
  }
}

/**
 * Handle OAuth callback result from URL parameters
 */
async function handleSlackOAuthCallback() {
  const params = new URLSearchParams(window.location.search);
  const slackConnected = params.get("slack_connected");
  const slackConnectionId = params.get("slack_connection_id");
  const slackError = params.get("slack_error");

  if (slackConnected) {
    // Clean up URL first
    const url = new URL(window.location.href);
    url.searchParams.delete("slack_connected");
    url.searchParams.delete("slack_connection_id");
    window.history.replaceState({}, "", url.toString());

    // Auto-link user to the new connection with notifications enabled
    if (slackConnectionId) {
      try {
        const session = await window.supabase.auth.getSession();
        const token = session?.data?.session?.access_token;
        if (!token) {
          console.warn("slack link-user: missing auth token");
          showSlackSuccess(`Slack workspace "${slackConnected}" connected!`);
          return;
        }

        const response = await fetchWithTimeout(
          `/v1/integrations/slack/${encodeURIComponent(slackConnectionId)}/link-user`,
          {
            method: "POST",
            headers: {
              Authorization: `Bearer ${token}`,
            },
          },
          {
            module: "slack",
            action: "link-user",
            connectionId: slackConnectionId,
          }
        );

        if (response.ok) {
          showSlackSuccess(
            `Slack workspace "${slackConnected}" connected! You'll receive job notifications.`
          );
        } else {
          // Link failed but connection succeeded
          console.warn("Auto-link failed after OAuth");
          showSlackSuccess(`Slack workspace "${slackConnected}" connected!`);
        }
      } catch (linkError) {
        console.warn("Auto-link failed after OAuth:", linkError);
        showSlackSuccess(`Slack workspace "${slackConnected}" connected!`);
      }
    } else {
      showSlackSuccess(`Slack workspace "${slackConnected}" connected!`);
    }
  } else if (slackError) {
    showSlackError(`Failed to connect Slack: ${slackError}`);
    // Clean up URL
    const url = new URL(window.location.href);
    url.searchParams.delete("slack_error");
    window.history.replaceState({}, "", url.toString());
  }
}

// Export functions to window for use in Webflow
if (typeof window !== "undefined") {
  window.setupSlackIntegration = setupSlackIntegration;
  window.loadSlackConnections = loadSlackConnections;
  window.handleSlackOAuthCallback = handleSlackOAuthCallback;
  window.showSlackSuccess = showSlackSuccess;
  window.showSlackError = showSlackError;
}
