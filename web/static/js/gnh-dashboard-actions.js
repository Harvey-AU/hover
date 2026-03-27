/**
 * Dashboard Actions Handler
 * Handles lightweight interactions on the dashboard page.
 */

function setupDashboardActions() {
  document.addEventListener("click", (event) => {
    const element = event.target.closest("[bb-action], [bbb-action]");
    if (!element) {
      return;
    }

    const action =
      element.getAttribute("bbb-action") || element.getAttribute("bb-action");
    if (!action) {
      return;
    }

    // Skip actions handled by other modules (google, slack, webflow)
    if (
      action.startsWith("google-") ||
      action.startsWith("slack-") ||
      action.startsWith("webflow-")
    ) {
      return;
    }

    event.preventDefault();
    handleDashboardAction(action, element);
  });

  // Setup date range filter dropdown
  const dateRangeSelect = document.getElementById("dateRange");
  if (dateRangeSelect) {
    dateRangeSelect.addEventListener("change", (event) => {
      const range = event.target.value;
      if (window.changeTimeRange) {
        window.changeTimeRange(range);
      } else if (window.dataBinder) {
        window.dataBinder.currentRange = range;
        window.dataBinder.refresh();
      }
    });
  }
}

function handleDashboardAction(action, element) {
  // Skip slack-* actions - handled by bb-slack.js
  if (action.startsWith("slack-")) {
    return;
  }

  // Skip webflow-* and site-* actions - handled by bb-webflow.js
  if (action.startsWith("webflow-") || action.startsWith("site-")) {
    return;
  }

  switch (action) {
    case "refresh-dashboard":
      if (window.dataBinder) {
        window.dataBinder.refresh();
      }
      break;

    case "restart-job": {
      const jobId =
        element.getAttribute("bbb-id") ||
        element.getAttribute("bb-data-job-id");
      if (jobId) {
        restartJob(jobId);
      }
      break;
    }

    case "cancel-job": {
      const jobId =
        element.getAttribute("bbb-id") ||
        element.getAttribute("bb-data-job-id");
      if (jobId) {
        cancelJob(jobId);
      }
      break;
    }

    case "create-job":
      openCreateJobModal();
      break;

    case "close-create-job-modal":
      closeCreateJobModal();
      break;

    default:
      break;
  }
}

async function restartJob(jobId) {
  try {
    // Fetch job config first
    const job = await window.dataBinder.fetchData(`/v1/jobs/${jobId}`);
    if (!job) {
      throw new Error("Failed to load job");
    }

    // Create new job with same config
    const payload = window.BB_APP.buildRestartJobPayload(job);
    await window.dataBinder.fetchData("/v1/jobs", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });

    showDashboardSuccess("Job restarted successfully.");
    if (window.dataBinder) {
      window.dataBinder.refresh();
    }
  } catch (error) {
    console.error("Failed to restart job:", error);
    showDashboardError("Failed to restart job");
  }
}

async function cancelJob(jobId) {
  try {
    await window.dataBinder.fetchData(`/v1/jobs/${jobId}/cancel`, {
      method: "POST",
    });
    showDashboardError("Job cancel requested.");
    if (window.dataBinder) {
      window.dataBinder.refresh();
    }
  } catch (error) {
    console.error("Failed to cancel job:", error);
    showDashboardError("Failed to cancel job");
  }
}

function openCreateJobModal() {
  const modal = document.getElementById("createJobModal");
  if (modal) {
    modal.style.display = "flex";
  }
}

function closeCreateJobModal() {
  const modal = document.getElementById("createJobModal");
  if (modal) {
    modal.style.display = "none";
  }
  // Clear form
  const form = document.getElementById("createJobForm");
  if (form) {
    form.reset();
    const maxPagesField = document.getElementById("maxPages");
    if (maxPagesField) maxPagesField.value = "0";
  }
}

function showDashboardError(message) {
  const container = document.createElement("div");
  container.style.cssText = `
    position: fixed; top: 20px; right: 20px; z-index: 10000;
    background: #fee2e2; color: #dc2626; border: 1px solid #fecaca;
    padding: 16px 20px; border-radius: 8px; max-width: 400px;
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
  `;

  const content = document.createElement("div");
  content.style.cssText = "display: flex; align-items: center; gap: 12px;";

  const icon = document.createElement("span");
  icon.textContent = "⚠️";
  content.appendChild(icon);

  const messageSpan = document.createElement("span");
  messageSpan.textContent = message;
  content.appendChild(messageSpan);

  const closeButton = document.createElement("button");
  closeButton.style.cssText =
    "background: none; border: none; font-size: 18px; cursor: pointer;";
  closeButton.setAttribute("aria-label", "Dismiss");
  closeButton.textContent = "×";
  closeButton.addEventListener("click", () => container.remove());
  content.appendChild(closeButton);

  container.appendChild(content);
  document.body.appendChild(container);

  setTimeout(() => container.remove(), 5000);
}

function showDashboardSuccess(message) {
  const container = document.createElement("div");
  container.style.cssText = `
    position: fixed; top: 20px; right: 20px; z-index: 10000;
    background: #d1fae5; color: #065f46; border: 1px solid #a7f3d0;
    padding: 16px 20px; border-radius: 8px; max-width: 400px;
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
  `;

  const content = document.createElement("div");
  content.style.cssText = "display: flex; align-items: center; gap: 12px;";

  const icon = document.createElement("span");
  icon.textContent = "✓";
  content.appendChild(icon);

  const messageSpan = document.createElement("span");
  messageSpan.textContent = message;
  content.appendChild(messageSpan);

  const closeButton = document.createElement("button");
  closeButton.style.cssText =
    "background: none; border: none; font-size: 18px; cursor: pointer;";
  closeButton.setAttribute("aria-label", "Dismiss");
  closeButton.textContent = "×";
  closeButton.addEventListener("click", () => container.remove());
  content.appendChild(closeButton);

  container.appendChild(content);
  document.body.appendChild(container);

  setTimeout(() => container.remove(), 5000);
}

if (typeof window !== "undefined") {
  window.setupDashboardActions = setupDashboardActions;
  window.showDashboardError = showDashboardError;
  window.showDashboardSuccess = showDashboardSuccess;
  window.closeCreateJobModal = closeCreateJobModal;
}
