/**
 * lib/settings/integrations/slack.js — Slack integration module
 *
 * Handles Slack workspace connections for notifications.
 * Flow: Connect workspace -> auto-link user -> Disconnect
 */

import { get, post } from "/app/lib/api-client.js";
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

// ── Setup ───────────────────────────────────────────────────────────────────────

/**
 * Wire up Slack click delegation on [bbb-action] elements.
 */
export function setupSlackIntegration() {
  document.addEventListener("click", (event) => {
    const element = event.target.closest("[bbb-action]");
    if (!element) return;
    const action = element.getAttribute("bbb-action");
    if (!action || !action.startsWith("slack-")) return;
    event.preventDefault();
    handleSlackAction(action, element);
  });
}

function handleSlackAction(action, element) {
  switch (action) {
    case "slack-connect":
      connectSlackWorkspace();
      break;
    case "slack-disconnect": {
      const connectionId = element.getAttribute("bbb-id");
      if (connectionId) disconnectSlackWorkspace(connectionId);
      else console.warn("slack-disconnect: missing bbb-id attribute");
      break;
    }
    case "slack-refresh":
      loadSlackConnections();
      break;
    default:
      break;
  }
}

// ── Data loading ────────────────────────────────────────────────────────────────

/**
 * Load and render Slack connections for the current organisation.
 */
export async function loadSlackConnections() {
  try {
    const connections = await get("/v1/integrations/slack");

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

    // Clear existing connections (keep template)
    connectionsList
      .querySelectorAll(".slack-connection")
      .forEach((el) => el.remove());

    if (!connections || connections.length === 0) {
      if (emptyState) emptyState.style.display = "block";
      return;
    }
    if (emptyState) emptyState.style.display = "none";

    for (const conn of connections) {
      const clone = template.cloneNode(true);
      clone.style.display = "block";
      clone.removeAttribute("bbb-template");
      clone.classList.add("slack-connection");

      const nameEl = clone.querySelector(".slack-workspace-name");
      if (nameEl)
        nameEl.textContent = conn.workspace_name || "Unknown Workspace";

      const dateEl = clone.querySelector(".slack-connected-date");
      if (dateEl)
        dateEl.textContent = `Connected ${formatRelativeDate(conn.created_at)}`;

      const disconnectBtn = clone.querySelector(
        '[bbb-action="slack-disconnect"]'
      );
      if (disconnectBtn) disconnectBtn.setAttribute("bbb-id", conn.id);

      connectionsList.appendChild(clone);
    }
  } catch (error) {
    console.error("Failed to load Slack connections:", error);
    toast("error", "Failed to load Slack connections");
  }
}

// ── Actions ─────────────────────────────────────────────────────────────────────

async function connectSlackWorkspace() {
  try {
    const response = await post("/v1/integrations/slack/connect");
    if (response?.auth_url) {
      window.location.href = response.auth_url;
    } else {
      throw new Error("No OAuth URL returned");
    }
  } catch (error) {
    console.error("Failed to start Slack OAuth:", error);
    toast("error", "Failed to connect to Slack. Please try again.");
  }
}

async function disconnectSlackWorkspace(connectionId) {
  if (
    !confirm(
      "Are you sure you want to disconnect this Slack workspace? You will no longer receive notifications."
    )
  )
    return;

  try {
    const token = await getAccessToken();
    if (!token) {
      toast("error", "Not authenticated. Please sign in.");
      return;
    }

    const response = await fetchWithTimeout(
      `/v1/integrations/slack/${encodeURIComponent(connectionId)}`,
      { method: "DELETE", headers: { Authorization: `Bearer ${token}` } },
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

    toast("success", "Slack workspace disconnected");
    loadSlackConnections();
  } catch (error) {
    console.error("Failed to disconnect Slack workspace:", error);
    toast("error", "Failed to disconnect Slack workspace");
  }
}

// ── OAuth callback ──────────────────────────────────────────────────────────────

/**
 * Check URL params for Slack OAuth callback results.
 */
export async function handleSlackOAuthCallback() {
  const params = new URLSearchParams(window.location.search);
  const slackConnected = params.get("slack_connected");
  const slackConnectionId = params.get("slack_connection_id");
  const slackError = params.get("slack_error");

  if (slackConnected) {
    const url = new URL(window.location.href);
    url.searchParams.delete("slack_connected");
    url.searchParams.delete("slack_connection_id");
    window.history.replaceState({}, "", url.toString());

    if (slackConnectionId) {
      try {
        const token = await getAccessToken();
        if (!token) {
          console.warn("slack link-user: missing auth token");
          toast("success", `Slack workspace "${slackConnected}" connected!`);
          return;
        }

        const response = await fetchWithTimeout(
          `/v1/integrations/slack/${encodeURIComponent(slackConnectionId)}/link-user`,
          {
            method: "POST",
            headers: { Authorization: `Bearer ${token}` },
          },
          {
            module: "slack",
            action: "link-user",
            connectionId: slackConnectionId,
          }
        );

        if (response.ok) {
          toast(
            "success",
            `Slack workspace "${slackConnected}" connected! You'll receive job notifications.`
          );
        } else {
          console.warn("Auto-link failed after OAuth");
          toast("success", `Slack workspace "${slackConnected}" connected!`);
        }
      } catch (linkError) {
        console.warn("Auto-link failed after OAuth:", linkError);
        toast("success", `Slack workspace "${slackConnected}" connected!`);
      }
    } else {
      toast("success", `Slack workspace "${slackConnected}" connected!`);
    }
  } else if (slackError) {
    toast("error", `Failed to connect Slack: ${slackError}`);
    const url = new URL(window.location.href);
    url.searchParams.delete("slack_error");
    window.history.replaceState({}, "", url.toString());
  }
}
