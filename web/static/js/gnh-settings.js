/*
 * Settings page logic
 * Handles navigation, organisation controls, and settings data loads.
 * Section-specific logic (account, team, plans, schedules) is in ES modules
 * under web/static/app/lib/settings/.
 */

(function () {
  function showSettingsToast(type, message) {
    const container = document.createElement("div");
    container.setAttribute("role", "status");
    container.setAttribute(
      "aria-live",
      type === "success" ? "polite" : "assertive"
    );
    container.setAttribute("aria-atomic", "true");
    const colourMap = {
      success: { bg: "#d1fae5", text: "#065f46", border: "#a7f3d0" },
      warning: { bg: "#fef3c7", text: "#92400e", border: "#fde68a" },
      error: { bg: "#fee2e2", text: "#dc2626", border: "#fecaca" },
    };
    const colours = colourMap[type] || colourMap.error;

    container.style.cssText = `
      position: fixed; top: 20px; right: 20px; z-index: 10000;
      background: ${colours.bg}; color: ${colours.text};
      border: 1px solid ${colours.border};
      padding: 16px 20px; border-radius: 8px; max-width: 400px;
      box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
    `;

    const content = document.createElement("div");
    content.style.cssText = "display: flex; align-items: center; gap: 12px;";

    const icon = document.createElement("span");
    icon.textContent = type === "success" ? "\u2705" : "\u26a0\ufe0f";
    content.appendChild(icon);

    const messageSpan = document.createElement("span");
    messageSpan.textContent = message;
    content.appendChild(messageSpan);

    const closeButton = document.createElement("button");
    closeButton.style.cssText =
      "background: none; border: none; font-size: 18px; cursor: pointer;";
    closeButton.setAttribute("aria-label", "Dismiss");
    closeButton.textContent = "\u00d7";
    closeButton.addEventListener("click", () => container.remove());
    content.appendChild(closeButton);

    container.appendChild(content);
    document.body.appendChild(container);

    setTimeout(() => container.remove(), 5000);
  }

  window.showDashboardSuccess = function (message) {
    showSettingsToast("success", message);
  };

  window.showDashboardError = function (message) {
    showSettingsToast("error", message);
  };

  window.showIntegrationFeedback = function (integration, type, message) {
    const suffix = type === "success" ? "Success" : "Error";
    const el = document.getElementById(`${integration}${suffix}Message`);
    const textEl = document.getElementById(`${integration}${suffix}Text`);

    if (el) {
      if (textEl) {
        textEl.textContent = message;
      } else {
        el.textContent = message;
      }
      el.style.display = "block";
      setTimeout(() => {
        el.style.display = "none";
      }, 5000);
    }
  };

  window.showSlackSuccess = (msg) =>
    window.showIntegrationFeedback("slack", "success", msg);
  window.showSlackError = (msg) =>
    window.showIntegrationFeedback("slack", "error", msg);
  window.showWebflowSuccess = (msg) =>
    window.showIntegrationFeedback("webflow", "success", msg);
  window.showWebflowError = (msg) =>
    window.showIntegrationFeedback("webflow", "error", msg);
  window.showGoogleSuccess = (msg) =>
    window.showIntegrationFeedback("google", "success", msg);
  window.showGoogleError = (msg) =>
    window.showIntegrationFeedback("google", "error", msg);

  function setActiveSettingsLink() {
    const path = window.location.pathname.replace(/\/$/, "");
    const currentPath = path === "/settings" ? "/settings/account" : path;

    document.querySelectorAll(".settings-link").forEach((link) => {
      try {
        const linkPath = new URL(link.href).pathname.replace(/\/$/, "");
        if (linkPath === currentPath) {
          link.classList.add("active");
          link.setAttribute("aria-current", "page");
        } else {
          link.classList.remove("active");
          link.removeAttribute("aria-current");
        }
      } catch (err) {
        link.classList.remove("active");
        link.removeAttribute("aria-current");
      }
    });
  }

  function resolveTargetSectionId() {
    const sectionMap = {
      "/settings": "account",
      "/settings/account": "account",
      "/settings/team": "team",
      "/settings/plans": "plans",
      "/settings/billing": "billing",
      "/settings/notifications": "notifications",
      "/settings/analytics": "analytics",
      "/settings/auto-crawl": "automated-jobs",
      "/settings/automation": "automated-jobs",
      "/settings/automated-jobs": "automated-jobs",
    };

    const hash = window.location.hash.replace("#", "");
    if (hash) {
      const hashTarget = document.getElementById(hash);
      if (hashTarget) {
        const section = hashTarget.closest(".settings-section");
        if (section?.id) return section.id;
      }
    }

    const path = window.location.pathname.replace(/\/$/, "");
    return sectionMap[path] || "account";
  }

  function setActiveSection() {
    const targetId = resolveTargetSectionId();
    const sections = document.querySelectorAll(".settings-section");
    const target = document.getElementById(targetId);
    const hash = window.location.hash.replace("#", "");
    const hashTarget = hash ? document.getElementById(hash) : null;

    sections.forEach((section) => {
      section.classList.toggle("active", section.id === targetId);
    });

    if (hashTarget) {
      hashTarget.scrollIntoView({ behavior: "smooth", block: "start" });
    } else if (target) {
      target.scrollIntoView({ behavior: "smooth", block: "start" });
    }

    if (targetId === "plans" && !window.location.hash) {
      activatePlanTab("planTabCurrent");
    }
    if (targetId === "automated-jobs" && !window.location.hash) {
      activateAutomationTab("autoCrawlWebflowPanel");
    }
    activateTabFromHash();
  }

  function activateTabFromHash() {
    const hash = window.location.hash.replace("#", "");
    if (!hash) return;
    const target = document.getElementById(hash);
    const panel = target?.closest(".settings-tab-panel");
    if (panel?.id) {
      if (panel.id.startsWith("planTab")) {
        activatePlanTab(panel.id);
      } else if (panel.id.startsWith("autoCrawl")) {
        activateAutomationTab(panel.id);
      }
    }
  }

  function setupSettingsNavigation() {
    setActiveSettingsLink();
    setActiveSection();

    window.addEventListener("hashchange", () => {
      setActiveSection();
    });

    window.addEventListener("popstate", () => {
      setActiveSettingsLink();
      setActiveSection();
    });
  }

  function activateTabGroup(sectionId, tabAttribute, panelId) {
    const section = document.getElementById(sectionId);
    if (!section) return;

    const tabs = section.querySelectorAll(`.settings-tab[${tabAttribute}]`);
    const panels = section.querySelectorAll(".settings-tab-panel");

    panels.forEach((panel) => {
      const isActive = panel.id === panelId;
      panel.classList.toggle("active", isActive);
      panel.setAttribute("aria-hidden", isActive ? "false" : "true");
    });

    tabs.forEach((tab) => {
      const isActive = tab.getAttribute(tabAttribute) === panelId;
      tab.classList.toggle("active", isActive);
      tab.setAttribute("aria-selected", isActive ? "true" : "false");
      tab.setAttribute("tabindex", isActive ? "0" : "-1");
    });
  }

  function activatePlanTab(panelId) {
    activateTabGroup("plans", "data-tab-target", panelId);
  }

  function setupPlanTabs() {
    const section = document.getElementById("plans");
    if (!section) return;
    const tabs = section.querySelectorAll(".settings-tab[data-tab-target]");
    if (!tabs.length) return;

    tabs.forEach((tab) => {
      tab.addEventListener("click", () => {
        const targetId = tab.dataset.tabTarget;
        if (targetId) {
          activatePlanTab(targetId);
        }
      });
    });
  }

  function activateAutomationTab(panelId) {
    activateTabGroup("automated-jobs", "data-auto-crawl-tab-target", panelId);
  }

  function setupAutomationTabs() {
    const section = document.getElementById("automated-jobs");
    if (!section) return;
    const tabs = section.querySelectorAll(
      ".settings-tab[data-auto-crawl-tab-target]"
    );
    if (!tabs.length) return;

    tabs.forEach((tab) => {
      tab.addEventListener("click", () => {
        const targetId = tab.dataset.autoCrawlTabTarget;
        if (targetId) {
          activateAutomationTab(targetId);
        }
      });
    });
  }

  function updateAdminVisibility(role) {
    const isAdmin =
      role === "admin" || window.BB_ACTIVE_ORG?.currentUserRole === "admin";
    document.querySelectorAll("[data-admin-only]").forEach((el) => {
      el.style.display = isAdmin ? "" : "none";
    });
  }

  async function handleInviteToken() {
    if (!window.BBInviteFlow?.handleInviteTokenFlow) return;

    const result = await window.BBInviteFlow.handleInviteTokenFlow({
      onAccepted: async () => {
        showSettingsToast("success", "Invite accepted");
        await refreshSettingsData();
      },
      onError: (err) => {
        console.error("Failed to accept invite:", err);
        showSettingsToast("error", err?.message || "Failed to accept invite");
      },
    });

    if (result?.status === "auth_required") {
      showSettingsToast(
        "warning",
        "Sign in or create an account to accept this invite"
      );
    }
  }

  async function refreshSettingsData() {
    // ES modules handle all migrated sections.
    if (window.__esRefreshSections) {
      await window.__esRefreshSections();
    }

    // Integrations are NOT migrated yet — always call via window.
    if (window.loadSlackConnections) {
      await window.loadSlackConnections();
    }
    if (window.loadWebflowConnections) {
      await window.loadWebflowConnections();
    }
    if (window.loadGoogleConnections) {
      await window.loadGoogleConnections();
    }
  }

  function setupNotificationsDropdown() {
    const container = document.getElementById("notificationsContainer");
    const toggleBtn = document.getElementById("notificationsBtn");
    const settingsBtn = document.getElementById("notificationsSettingsBtn");
    const markAllReadBtn = document.getElementById("markAllReadBtn");

    if (!container || !toggleBtn) return;

    toggleBtn.addEventListener("click", async (e) => {
      e.stopPropagation();
      const isOpen = container.classList.toggle("open");
      if (isOpen) {
        await loadNotifications();
      }
    });

    if (settingsBtn) {
      settingsBtn.addEventListener("click", () => {
        container.classList.remove("open");
      });
    }

    if (markAllReadBtn) {
      markAllReadBtn.addEventListener("click", async () => {
        await markAllNotificationsRead();
      });
    }

    document.addEventListener("click", (e) => {
      if (!container.contains(e.target)) {
        container.classList.remove("open");
      }
    });

    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        if (container.classList.contains("open")) {
          container.classList.remove("open");
        }
      }
    });

    loadNotificationCount();
    subscribeToNotifications();
  }

  function setupUserMenuDropdown() {
    const dropdown = document.getElementById("userMenuDropdown");
    const button = document.getElementById("userAvatar");
    const orgName = document.getElementById("currentOrgName");
    const userMenuOrgName = document.getElementById("userMenuOrgName");

    if (!dropdown || !button) return;

    const syncOrgName = () => {
      if (!orgName || !userMenuOrgName) return;
      const name = orgName.textContent?.trim();
      if (name) {
        userMenuOrgName.textContent = name;
      }
    };

    syncOrgName();
    if (orgName) {
      const observer = new MutationObserver(syncOrgName);
      observer.observe(orgName, {
        childList: true,
        characterData: true,
        subtree: true,
      });
    }

    button.addEventListener("click", (event) => {
      event.stopPropagation();
      const isOpen = dropdown.classList.toggle("show");
      button.setAttribute("aria-expanded", isOpen ? "true" : "false");
    });

    document.addEventListener("click", (event) => {
      if (!dropdown.contains(event.target) && !button.contains(event.target)) {
        dropdown.classList.remove("show");
        button.setAttribute("aria-expanded", "false");
      }
    });

    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        dropdown.classList.remove("show");
        button.setAttribute("aria-expanded", "false");
      }
    });
  }

  let notificationsRetryCount = 0;
  const maxNotificationRetries = 30;
  async function subscribeToNotifications() {
    const orgId = window.BB_ACTIVE_ORG?.id;
    if (!orgId || !window.supabase) {
      if (notificationsRetryCount < maxNotificationRetries) {
        notificationsRetryCount += 1;
        setTimeout(subscribeToNotifications, 1000);
      }
      return;
    }
    notificationsRetryCount = 0;

    if (window.notificationsChannel) {
      window.supabase.removeChannel(window.notificationsChannel);
    }

    try {
      const channel = window.supabase
        .channel(`notifications-changes:${orgId}`)
        .on(
          "postgres_changes",
          {
            event: "INSERT",
            schema: "public",
            table: "notifications",
            filter: `organisation_id=eq.${orgId}`,
          },
          () => {
            setTimeout(() => {
              loadNotificationCount();
              if (
                document
                  .getElementById("notificationsContainer")
                  ?.classList.contains("open")
              ) {
                loadNotifications();
              }
            }, 200);
          }
        )
        .subscribe();

      window.notificationsChannel = channel;
    } catch (err) {
      console.error("Failed to subscribe to notifications:", err);
    }
  }

  async function loadNotificationCount() {
    try {
      const session = await window.supabase.auth.getSession();
      const token = session?.data?.session?.access_token;
      if (!token) return;

      const response = await fetch("/v1/notifications?limit=1", {
        headers: { Authorization: `Bearer ${token}` },
      });

      if (response.ok) {
        const data = await response.json();
        updateNotificationBadge(data.unread_count);
      }
    } catch (err) {
      console.error("Failed to load notification count:", err);
    }
  }

  async function loadNotifications() {
    const list = document.getElementById("notificationsList");
    if (!list) return;

    try {
      const session = await window.supabase.auth.getSession();
      const token = session?.data?.session?.access_token;
      if (!token) {
        list.textContent = "";
        const empty = document.createElement("div");
        empty.className = "bb-notifications-empty";
        const msg = document.createElement("div");
        msg.textContent = "Please sign in";
        empty.appendChild(msg);
        list.appendChild(empty);
        return;
      }

      const response = await fetch("/v1/notifications?limit=10", {
        headers: { Authorization: `Bearer ${token}` },
      });

      if (!response.ok) {
        throw new Error("Failed to fetch notifications");
      }

      const data = await response.json();
      updateNotificationBadge(data.unread_count);
      renderNotifications(data.notifications);
    } catch (err) {
      console.error("Failed to load notifications:", err);
      list.textContent = "";
      const empty = document.createElement("div");
      empty.className = "bb-notifications-empty";
      const msg = document.createElement("div");
      msg.textContent = "Failed to load";
      empty.appendChild(msg);
      list.appendChild(empty);
    }
  }

  function renderNotifications(notifications) {
    const list = document.getElementById("notificationsList");
    if (!list) return;

    if (!notifications || notifications.length === 0) {
      list.textContent = "";
      const empty = document.createElement("div");
      empty.className = "bb-notifications-empty";
      const iconDiv = document.createElement("div");
      iconDiv.className = "bb-notifications-empty-icon";
      iconDiv.textContent = "\ud83d\udd14";
      const msgDiv = document.createElement("div");
      msgDiv.textContent = "No notifications yet";
      empty.appendChild(iconDiv);
      empty.appendChild(msgDiv);
      list.appendChild(empty);
      return;
    }

    const typeIcons = {
      job_completed: "\u2705",
      job_failed: "\u274c",
      job_started: "\ud83d\ude80",
      system: "\u2139\ufe0f",
    };

    // Use DOM methods instead of innerHTML for XSS protection
    list.textContent = "";
    notifications.forEach((n) => {
      const isUnread = !n.read_at;
      const icon = typeIcons[n.type] || "\ud83d\udcec";
      const time = formatRelativeTime(n.created_at);

      const item = document.createElement("div");
      item.className = `bb-notification-item${isUnread ? " unread" : ""}`;
      item.dataset.id = n.id;
      item.dataset.link = n.link || "";

      const iconEl = document.createElement("div");
      iconEl.className = "bb-notification-item-icon";
      iconEl.textContent = icon;

      const content = document.createElement("div");
      content.className = "bb-notification-item-content";

      const subject = document.createElement("div");
      subject.className = "bb-notification-item-subject";
      subject.textContent = n.subject;

      const preview = document.createElement("div");
      preview.className = "bb-notification-item-preview";
      preview.textContent = n.preview;

      const timeEl = document.createElement("div");
      timeEl.className = "bb-notification-item-time";
      timeEl.textContent = time;

      content.appendChild(subject);
      content.appendChild(preview);
      content.appendChild(timeEl);
      item.appendChild(iconEl);
      item.appendChild(content);
      list.appendChild(item);
    });

    list.onclick = (e) => {
      const item = e.target.closest(".bb-notification-item");
      if (item) {
        handleNotificationClick(item.dataset.id, item.dataset.link);
      }
    };
  }

  async function handleNotificationClick(id, link) {
    try {
      const session = await window.supabase.auth.getSession();
      const token = session?.data?.session?.access_token;
      if (token) {
        await fetch(`/v1/notifications/${id}/read`, {
          method: "POST",
          headers: { Authorization: `Bearer ${token}` },
        });
        loadNotificationCount();
      }
    } catch (err) {
      console.error("Failed to mark notification read:", err);
    }

    if (link) {
      document
        .getElementById("notificationsContainer")
        ?.classList.remove("open");
      if (link.startsWith("/")) {
        window.location.href = link;
      } else {
        // Validate URL protocol to prevent javascript:/data: attacks
        let safeUrl;
        try {
          const parsed = new URL(link, window.location.origin);
          if (!["http:", "https:"].includes(parsed.protocol)) {
            throw new Error("Unsupported protocol");
          }
          safeUrl = parsed.href;
        } catch (e) {
          showSettingsToast("error", "Invalid notification link");
          return;
        }
        const newWindow = window.open(safeUrl, "_blank", "noopener,noreferrer");
        if (newWindow) {
          newWindow.opener = null;
        }
      }
    }
  }

  async function markAllNotificationsRead() {
    try {
      const session = await window.supabase.auth.getSession();
      const token = session?.data?.session?.access_token;
      if (!token) return;

      const response = await fetch("/v1/notifications/read-all", {
        method: "POST",
        headers: { Authorization: `Bearer ${token}` },
      });

      if (response.ok) {
        updateNotificationBadge(0);
        document
          .querySelectorAll(".bb-notification-item.unread")
          .forEach((el) => {
            el.classList.remove("unread");
          });
      }
    } catch (err) {
      console.error("Failed to mark all read:", err);
    }
  }

  function updateNotificationBadge(count) {
    const badge = document.getElementById("notificationsBadge");
    if (badge) {
      badge.textContent = count > 9 ? "9+" : count || "";
      badge.dataset.count = count || 0;
    }
  }

  function formatRelativeTime(dateStr) {
    const date = new Date(dateStr);
    const now = new Date();
    const diffMs = now - date;
    const diffMins = Math.floor(diffMs / 60000);
    const diffHours = Math.floor(diffMs / 3600000);
    const diffDays = Math.floor(diffMs / 86400000);

    if (diffMins < 1) return "just now";
    if (diffMins < 60) return `${diffMins}m ago`;
    if (diffHours < 24) return `${diffHours}h ago`;
    if (diffDays < 7) return `${diffDays}d ago`;
    return date.toLocaleDateString("en-AU");
  }

  function escapeHtml(text) {
    if (!text) return "";
    const div = document.createElement("div");
    div.textContent = text;
    return div.innerHTML;
  }

  async function initOrgSwitcher() {
    const switcher = document.getElementById("orgSwitcher");
    const btn = document.getElementById("orgSwitcherBtn");
    const dropdown = document.getElementById("orgDropdown");
    const orgList = document.getElementById("orgList");
    const currentOrgName = document.getElementById("currentOrgName");
    const settingsOrgName = document.getElementById("settingsOrgName");
    const settingsSwitcher = document.getElementById("settingsOrgSwitcher");
    const settingsDropdown = document.getElementById("settingsOrgDropdown");
    const settingsOrgList = document.getElementById("settingsOrgList");
    const settingsCreateOrgBtn = document.getElementById(
      "settingsCreateOrgBtn"
    );
    const settingsSwitcherBtn = document.getElementById(
      "settingsOrgSwitcherBtn"
    );
    const divider = document.querySelector(".bb-org-divider");

    if (!switcher || !btn) return;

    // Ensure org is initialised (may already be done by dashboard or nav)
    if (window.GNH_APP?.initialiseOrg && !window.BB_ACTIVE_ORG?.name) {
      try {
        await window.GNH_APP.initialiseOrg();
      } catch (err) {
        console.warn("Org init failed:", err);
      }
    }

    // Wait for shared org data
    try {
      await window.BB_ORG_READY;
    } catch (err) {
      console.warn("BB_ORG_READY failed:", err);
    }

    const organisations = window.BB_ORGANISATIONS || [];
    const activeOrg = window.BB_ACTIVE_ORG;

    // Clone elements to remove old listeners
    const newBtn = btn.cloneNode(true);
    btn.parentNode.replaceChild(newBtn, btn);
    const chevron = newBtn.querySelector(".bb-org-chevron");
    newBtn.disabled = false;
    newBtn.style.cursor = "";
    if (chevron) chevron.style.display = "";
    const newOrgList = orgList.cloneNode(false);
    orgList.parentNode.replaceChild(newOrgList, orgList);

    const btnRef = newBtn;
    const orgListRef = newOrgList;
    let currentOrgNameRef =
      newBtn.querySelector("#currentOrgName") ||
      document.getElementById("currentOrgName") ||
      currentOrgName;
    let settingsBtnRef = settingsSwitcherBtn;
    let settingsOrgListRef = settingsOrgList;
    let settingsOrgNameRef = settingsOrgName;

    if (settingsSwitcherBtn?.parentNode) {
      const newSettingsBtn = settingsSwitcherBtn.cloneNode(true);
      settingsSwitcherBtn.parentNode.replaceChild(
        newSettingsBtn,
        settingsSwitcherBtn
      );
      settingsBtnRef = newSettingsBtn;
      settingsOrgNameRef =
        newSettingsBtn.querySelector("#settingsOrgName") ||
        document.getElementById("settingsOrgName");
    }

    if (settingsOrgList?.parentNode) {
      const newSettingsOrgList = settingsOrgList.cloneNode(false);
      settingsOrgList.parentNode.replaceChild(
        newSettingsOrgList,
        settingsOrgList
      );
      settingsOrgListRef = newSettingsOrgList;
    }

    // Handle no orgs case
    if (organisations.length === 0) {
      if (currentOrgNameRef) currentOrgNameRef.textContent = "No Organisation";
      if (settingsOrgNameRef)
        settingsOrgNameRef.textContent = "No Organisation";
      return;
    }

    // Disable dropdown if only one org
    if (organisations.length <= 1) {
      if (dropdown) dropdown.style.display = "none";
      btnRef.disabled = true;
      btnRef.style.cursor = "default";
      if (chevron) chevron.style.display = "none";
      if (settingsBtnRef) {
        settingsBtnRef.disabled = true;
        settingsBtnRef.style.cursor = "default";
      }
      if (settingsDropdown) settingsDropdown.style.display = "none";
    }

    // Set display name
    const activeName = activeOrg?.name || organisations[0]?.name || "";
    if (currentOrgNameRef) {
      currentOrgNameRef.textContent = activeName || "Organisation";
    }
    if (settingsOrgNameRef) {
      settingsOrgNameRef.textContent = activeName || "Organisation";
    }

    const closeOrgDropdowns = () => {
      switcher.classList.remove("open");
      btnRef.setAttribute("aria-expanded", "false");
      if (settingsSwitcher) {
        settingsSwitcher.classList.remove("open");
        settingsBtnRef?.setAttribute("aria-expanded", "false");
      }
    };

    // Toggle dropdowns
    btnRef.addEventListener("click", (e) => {
      e.stopPropagation();
      switcher.classList.toggle("open");
      btnRef.setAttribute("aria-expanded", switcher.classList.contains("open"));
    });

    if (settingsBtnRef && settingsSwitcher) {
      settingsBtnRef.addEventListener("click", (e) => {
        e.stopPropagation();
        settingsSwitcher.classList.toggle("open");
        settingsBtnRef.setAttribute(
          "aria-expanded",
          settingsSwitcher.classList.contains("open")
        );
      });
    }

    // Handle org switch using shared function
    const handleOrgSwitch = async (org) => {
      closeOrgDropdowns();
      const previous = window.BB_ACTIVE_ORG?.id;

      // Show loading state
      if (currentOrgNameRef) currentOrgNameRef.textContent = "Switching...";
      if (settingsOrgNameRef) settingsOrgNameRef.textContent = "Switching...";

      try {
        await window.GNH_APP.switchOrg(org.id);
        // bb:org-switched event will handle UI updates
      } catch (err) {
        console.error("Error switching organisation:", err);
        if (currentOrgNameRef) {
          currentOrgNameRef.textContent =
            window.BB_ACTIVE_ORG?.name || "Unknown";
        }
        if (settingsOrgNameRef) {
          settingsOrgNameRef.textContent =
            window.BB_ACTIVE_ORG?.name || "Organisation";
        }
        showSettingsToast("error", "Failed to switch organisation");
      }
    };

    // Render org buttons
    const renderOrgButton = (listEl, org) => {
      if (!listEl) return;
      const button = document.createElement("button");
      button.className = "bb-org-item";
      button.dataset.orgId = org.id;
      button.textContent = org.name;
      if (org.id === activeOrg?.id) {
        button.classList.add("active");
      }
      button.addEventListener("click", () => handleOrgSwitch(org));
      listEl.appendChild(button);
    };

    // Clear and populate lists
    while (orgListRef.firstChild) {
      orgListRef.removeChild(orgListRef.firstChild);
    }
    if (settingsOrgListRef) {
      while (settingsOrgListRef.firstChild) {
        settingsOrgListRef.removeChild(settingsOrgListRef.firstChild);
      }
    }
    organisations.forEach((org) => {
      renderOrgButton(orgListRef, org);
      renderOrgButton(settingsOrgListRef, org);
    });

    if (settingsCreateOrgBtn) {
      settingsCreateOrgBtn.addEventListener("click", (e) => {
        e.stopPropagation();
        closeOrgDropdowns();
        document.getElementById("createOrgBtn")?.click();
      });
    }

    document.removeEventListener("click", window._closeOrgDropdown);
    window._closeOrgDropdown = closeOrgDropdowns;
    document.addEventListener("click", closeOrgDropdowns);
  }

  // Listen for org switches to update settings-specific UI and data
  document.addEventListener("bb:org-switched", async (e) => {
    const newOrg = e.detail?.organisation;
    if (!newOrg) return;

    // Update name displays
    const currentOrgNameRef = document.getElementById("currentOrgName");
    const settingsOrgNameRef = document.getElementById("settingsOrgName");
    if (currentOrgNameRef) currentOrgNameRef.textContent = newOrg.name;
    if (settingsOrgNameRef) settingsOrgNameRef.textContent = newOrg.name;

    // Update active states in dropdowns
    document.querySelectorAll(".bb-org-item").forEach((el) => {
      el.classList.toggle("active", el.dataset.orgId === newOrg.id);
    });

    // Refresh settings-specific data
    if (typeof refreshSettingsData === "function") {
      try {
        await refreshSettingsData();
      } catch (err) {
        console.warn("Failed to refresh settings data:", err);
      }
    }

    window.BBQuota?.refresh();

    showSettingsToast("success", `Switched to ${newOrg.name}`);
  });

  function initCreateOrgModal() {
    const modal = document.getElementById("createOrgModal");
    const form = document.getElementById("createOrgForm");
    const nameInput = document.getElementById("newOrgName");
    const errorDiv = document.getElementById("createOrgError");
    const createBtn = document.getElementById("createOrgBtn");
    const closeBtn = document.getElementById("closeCreateOrgModal");
    const cancelBtn = document.getElementById("cancelCreateOrg");
    const submitBtn = document.getElementById("submitCreateOrg");

    if (!modal || !form) return;

    const openModal = () => {
      modal.classList.add("show");
      nameInput.value = "";
      errorDiv.style.display = "none";
      nameInput.focus();
    };

    const closeModal = () => {
      modal.classList.remove("show");
    };

    createBtn?.addEventListener("click", (e) => {
      e.stopPropagation();
      document.getElementById("orgSwitcher")?.classList.remove("open");
      openModal();
    });

    closeBtn?.addEventListener("click", closeModal);
    cancelBtn?.addEventListener("click", closeModal);
    modal?.addEventListener("click", (e) => {
      if (e.target === modal) closeModal();
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape" && modal?.classList.contains("show")) {
        closeModal();
      }
    });

    form?.addEventListener("submit", async (e) => {
      e.preventDefault();

      const name = nameInput.value.trim();
      if (!name) {
        errorDiv.textContent = "Organisation name is required";
        errorDiv.style.display = "block";
        return;
      }

      submitBtn.disabled = true;
      submitBtn.textContent = "Creating...";
      errorDiv.style.display = "none";

      try {
        const sessionResult = await window.supabase.auth.getSession();
        const session = sessionResult?.data?.session;
        if (!session) {
          throw new Error("Not authenticated");
        }

        const response = await fetch("/v1/organisations", {
          method: "POST",
          headers: {
            Authorization: `Bearer ${session.access_token}`,
            "Content-Type": "application/json",
          },
          body: JSON.stringify({ name }),
        });

        const data = await response.json();

        if (response.ok) {
          closeModal();

          const newOrg = data.data?.organisation;

          // Update shared org data
          window.BB_ACTIVE_ORG = newOrg;
          if (Array.isArray(window.BB_ORGANISATIONS)) {
            window.BB_ORGANISATIONS.push(newOrg);
          } else {
            window.BB_ORGANISATIONS = [newOrg];
          }

          // Dispatch event for all listeners
          document.dispatchEvent(
            new CustomEvent("bb:org-switched", {
              detail: { organisation: newOrg },
            })
          );

          await initOrgSwitcher();
          await refreshSettingsData();

          showSettingsToast("success", `Organisation "${name}" created`);
        } else {
          errorDiv.textContent =
            data.message || "Failed to create organisation";
          errorDiv.style.display = "block";
        }
      } catch (err) {
        console.error("Error creating organisation:", err);
        errorDiv.textContent = "An error occurred. Please try again.";
        errorDiv.style.display = "block";
      } finally {
        submitBtn.disabled = false;
        submitBtn.textContent = "Create";
      }
    });
  }

  function initAdminSection(session) {
    if (window.BBAdmin) {
      window.BBAdmin.initAdminResetButton("settingsResetDbBtn", session, {
        containerSelector: "#adminGroup",
      });
    }
  }

  async function initSettingsPage() {
    try {
      if (window.GNH_APP?.coreReady) {
        await window.GNH_APP.coreReady;
      }

      const dataBinder = new BBDataBinder({
        apiBaseUrl: "",
        debug: false,
      });

      window.dataBinder = dataBinder;

      if (!window.BBAuth.initialiseSupabase()) {
        throw new Error("Failed to initialise Supabase client");
      }

      await dataBinder.init();
      if (window.setupQuickAuth) {
        await window.setupQuickAuth(dataBinder);
      }

      if (window.BB_NAV_READY) {
        await window.BB_NAV_READY;
      }

      setupSettingsNavigation();
      setupPlanTabs();
      setupAutomationTabs();

      const sessionResult = await window.supabase.auth.getSession();
      const session = sessionResult?.data?.session;
      if (session?.user) {
        // Initialise org using shared logic (single source of truth)
        if (window.GNH_APP?.initialiseOrg) {
          try {
            await window.GNH_APP.initialiseOrg();
          } catch (err) {
            console.warn("Failed to initialise org:", err);
          }
        }
        await initOrgSwitcher();
        initCreateOrgModal();
        initAdminSection(session);

        await handleInviteToken();
      } else if (window.BBInviteFlow?.getInviteToken?.()) {
        await handleInviteToken();
      }

      if (window.setupSlackIntegration) {
        window.setupSlackIntegration();
      }
      if (window.handleSlackOAuthCallback) {
        window.handleSlackOAuthCallback();
      }
      if (window.setupWebflowIntegration) {
        window.setupWebflowIntegration();
      }
      if (window.handleWebflowOAuthCallback) {
        window.handleWebflowOAuthCallback();
      }
      if (window.setupGoogleIntegration) {
        window.setupGoogleIntegration();
      }
      if (window.handleGoogleOAuthCallback) {
        window.handleGoogleOAuthCallback();
      }
    } catch (error) {
      console.error("Failed to initialise settings:", error);
      showSettingsToast("error", "Failed to load settings. Please refresh.");
    }
  }

  document.addEventListener("DOMContentLoaded", () => {
    initSettingsPage();
  });
})();
