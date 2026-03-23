/**
 * lib/global-nav.js — global navigation module
 *
 * Fetches and mounts the nav partial, then initialises:
 *   - org switcher (dropdown, switch, ready events)
 *   - user menu (dropdown, escape, click-outside)
 *   - notifications (badge, realtime, mark-read)
 *   - quota display (polling, visibility-aware)
 *
 * Still reads window.BB_APP / BB_ACTIVE_ORG / BB_ORGANISATIONS / supabase
 * from core.js. Those globals will be retired when core.js is migrated.
 */

import { getAccessToken } from "/app/lib/auth-session.js";

// ── Promise gate (replaces window.BB_NAV_READY) ───────────────────────────────

let _resolveNavReady;
const navReadyPromise = new Promise((r) => {
  _resolveNavReady = r;
});

/** Resolves the nav-ready promise and fires the legacy event. */
function finishNavReady() {
  _resolveNavReady();
  document.dispatchEvent(new CustomEvent("bb:nav-ready"));
}

/** Awaitable promise that resolves once nav is mounted and wired. */
export { navReadyPromise as ready };

// Keep the legacy global so existing code (dashboard.js etc.) still works.
window.BB_NAV_READY = navReadyPromise;

// ── Helpers ────────────────────────────────────────────────────────────────────

function closeOverlays(navEl, { except } = {}) {
  const orgSwitcher = navEl.querySelector("#orgSwitcher");
  const orgBtn = navEl.querySelector("#orgSwitcherBtn");
  const userMenuDropdown = navEl.querySelector("#userMenuDropdown");
  const userAvatar = navEl.querySelector("#userAvatar");
  const notificationsContainer = navEl.querySelector("#notificationsContainer");
  const notificationsBtn = navEl.querySelector("#notificationsBtn");

  if (except !== "org") {
    orgSwitcher?.classList.remove("open");
    orgBtn?.setAttribute("aria-expanded", "false");
  }
  if (except !== "user") {
    userMenuDropdown?.classList.remove("show");
    userAvatar?.setAttribute("aria-expanded", "false");
  }
  if (except !== "notifications") {
    notificationsContainer?.classList.remove("open");
    notificationsBtn?.setAttribute("aria-expanded", "false");
  }
}

// ── Org switcher ───────────────────────────────────────────────────────────────

function initOrgSwitcher(navEl) {
  const currentOrgName = navEl.querySelector("#currentOrgName");
  if (!currentOrgName) return;

  const orgListEl = navEl.querySelector("#orgList");
  const orgSwitcher = navEl.querySelector("#orgSwitcher");
  const orgBtn = navEl.querySelector("#orgSwitcherBtn");
  const settingsOrgName = document.getElementById("settingsOrgName");

  const updateDisplay = (activeOrg, organisations) => {
    if (!activeOrg) {
      currentOrgName.textContent = "No Organisation";
      return;
    }
    currentOrgName.textContent = activeOrg.name || "Organisation";

    if (orgListEl) {
      while (orgListEl.firstChild) orgListEl.removeChild(orgListEl.firstChild);
      (organisations || []).forEach((org) => {
        const button = document.createElement("button");
        button.className = "bb-org-item";
        button.dataset.orgId = org.id;
        button.dataset.orgName = org.name;
        button.textContent = org.name;
        if (org.id === activeOrg?.id) button.classList.add("active");
        orgListEl.appendChild(button);
      });
    }
  };

  // Delegated org-item clicks
  orgListEl?.addEventListener("click", async (e) => {
    const item = e.target.closest(".bb-org-item");
    if (!item || item.classList.contains("active")) {
      orgSwitcher?.classList.remove("open");
      return;
    }
    currentOrgName.textContent = "Switching...";
    orgSwitcher?.classList.remove("open");
    orgBtn?.setAttribute("aria-expanded", "false");

    try {
      await window.BB_APP.switchOrg(item.dataset.orgId);
    } catch (err) {
      console.warn("Failed to switch organisation:", err);
      currentOrgName.textContent = window.BB_ACTIVE_ORG?.name || "Organisation";
    }
  });

  // Toggle dropdown
  if (orgSwitcher && orgBtn) {
    orgBtn.addEventListener("click", (e) => {
      e.stopPropagation();
      const willOpen = !orgSwitcher.classList.contains("open");
      closeOverlays(navEl, { except: willOpen ? "org" : undefined });
      orgSwitcher.classList.toggle("open", willOpen);
      orgBtn.setAttribute("aria-expanded", willOpen ? "true" : "false");
    });
    document.addEventListener("click", () => {
      orgSwitcher.classList.remove("open");
      orgBtn.setAttribute("aria-expanded", "false");
    });
  }

  // Org lifecycle events
  document.addEventListener("bb:org-switched", (e) => {
    const newOrg = e.detail?.organisation;
    if (newOrg) updateDisplay(newOrg, window.BB_ORGANISATIONS);
  });
  document.addEventListener("bb:org-ready", () => {
    updateDisplay(window.BB_ACTIVE_ORG, window.BB_ORGANISATIONS);
  });

  // Sync settings page org name if present
  if (settingsOrgName) {
    const observer = new MutationObserver(() => {
      const next = settingsOrgName.textContent?.trim();
      if (next && next !== currentOrgName.textContent) {
        currentOrgName.textContent = next;
      }
    });
    observer.observe(settingsOrgName, {
      childList: true,
      characterData: true,
      subtree: true,
    });
    window.addEventListener("beforeunload", () => observer.disconnect(), {
      once: true,
    });
  }

  // Wait for core → init org → render
  (async () => {
    try {
      if (window.BB_APP?.coreReady) await window.BB_APP.coreReady;
      if (window.BB_APP?.initialiseOrg) await window.BB_APP.initialiseOrg();
      updateDisplay(window.BB_ACTIVE_ORG, window.BB_ORGANISATIONS);
    } catch (err) {
      console.warn("Organisation initialisation failed:", err);
      currentOrgName.textContent = "Organisation";
    }
  })();
}

// ── User menu ──────────────────────────────────────────────────────────────────

function initUserMenu(navEl) {
  const userMenu = navEl.querySelector("#userMenu");
  const userAvatar = navEl.querySelector("#userAvatar");
  const userMenuDropdown = navEl.querySelector("#userMenuDropdown");
  const currentOrgNameEl = navEl.querySelector("#currentOrgName");
  const userMenuOrgNameEl = navEl.querySelector("#userMenuOrgName");
  if (!userMenu || !userAvatar || !userMenuDropdown) return;

  const syncOrgName = () => {
    if (!currentOrgNameEl || !userMenuOrgNameEl) return;
    const name = currentOrgNameEl.textContent?.trim();
    if (name) userMenuOrgNameEl.textContent = name;
  };

  if (currentOrgNameEl) {
    const observer = new MutationObserver(syncOrgName);
    observer.observe(currentOrgNameEl, {
      childList: true,
      characterData: true,
      subtree: true,
    });
    window.addEventListener("beforeunload", () => observer.disconnect(), {
      once: true,
    });
  }

  userAvatar.addEventListener("click", (e) => {
    e.stopPropagation();
    const willOpen = !userMenuDropdown.classList.contains("show");
    closeOverlays(navEl, { except: willOpen ? "user" : undefined });
    userMenuDropdown.classList.toggle("show", willOpen);
    userAvatar.setAttribute("aria-expanded", willOpen ? "true" : "false");
  });

  userMenuDropdown.addEventListener("click", (e) => e.stopPropagation());

  document.addEventListener("click", (e) => {
    if (!userMenu.contains(e.target)) {
      userMenuDropdown.classList.remove("show");
      userAvatar.setAttribute("aria-expanded", "false");
    }
  });

  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && userMenuDropdown.classList.contains("show")) {
      userMenuDropdown.classList.remove("show");
      userAvatar.setAttribute("aria-expanded", "false");
      userAvatar.focus();
    }
  });

  document.addEventListener("bb:org-switched", syncOrgName);
  document.addEventListener("bb:org-ready", syncOrgName);
  syncOrgName();
}

// ── Notifications ──────────────────────────────────────────────────────────────

function initNotifications(navEl) {
  const container = navEl.querySelector("#notificationsContainer");
  const btn = navEl.querySelector("#notificationsBtn");
  const dropdown = navEl.querySelector("#notificationsDropdown");
  const list = navEl.querySelector("#notificationsList");
  const badge = navEl.querySelector("#notificationsBadge");
  const markAllBtn = navEl.querySelector("#markAllReadBtn");
  if (!container || !btn || !badge) return;

  btn.setAttribute("aria-haspopup", "true");
  btn.setAttribute("aria-expanded", "false");

  const updateBadge = (count) => {
    const n = Number(count) || 0;
    badge.textContent = n > 9 ? "9+" : n || "";
    badge.dataset.count = n;
  };

  const fetchNotifs = async (limit = 10) => {
    const token = await getAccessToken();
    if (!token) return null;
    const res = await fetch(`/v1/notifications?limit=${limit}`, {
      headers: { Authorization: `Bearer ${token}` },
      signal: AbortSignal.timeout(15000),
    });
    if (!res.ok) return null;
    return res.json();
  };

  const refreshBadge = async () => {
    try {
      const data = await fetchNotifs(1);
      if (!data) return;
      updateBadge(data.unread_count ?? data.data?.unread_count ?? 0);
    } catch (err) {
      console.warn("Failed to refresh notifications badge:", err);
    }
  };

  const escapeHtml = (v) =>
    String(v ?? "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");

  const renderList = (notifications) => {
    if (!list) return;
    if (!notifications || notifications.length === 0) {
      while (list.firstChild) list.removeChild(list.firstChild);
      const empty = document.createElement("div");
      empty.className = "bb-notifications-empty";
      empty.textContent = "No notifications yet";
      list.appendChild(empty);
      return;
    }

    while (list.firstChild) list.removeChild(list.firstChild);
    notifications.forEach((n) => {
      const item = document.createElement("button");
      item.type = "button";
      item.className = `bb-notification-item${!n.read_at ? " unread" : ""}`;
      item.dataset.id = escapeHtml(n.id);
      item.dataset.link = escapeHtml(n.link);

      const content = document.createElement("div");
      content.className = "bb-notification-item-content";

      const subject = document.createElement("div");
      subject.className = "bb-notification-item-subject";
      subject.textContent = n.subject ?? "";

      const preview = document.createElement("div");
      preview.className = "bb-notification-item-preview";
      preview.textContent = n.preview ?? "";

      content.appendChild(subject);
      content.appendChild(preview);
      item.appendChild(content);
      list.appendChild(item);
    });
  };

  const loadDropdown = async () => {
    try {
      const data = await fetchNotifs(10);
      if (!data) return;
      updateBadge(data.unread_count ?? data.data?.unread_count ?? 0);
      renderList(data.notifications ?? data.data?.notifications ?? []);
    } catch (err) {
      console.warn("Failed to load notifications:", err);
    }
  };

  const markRead = async (id) => {
    if (!id) return;
    const token = await getAccessToken();
    if (!token) return;
    const res = await fetch(`/v1/notifications/${id}/read`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!res.ok) throw new Error("Failed to mark notification as read");
  };

  // Realtime subscription
  const channelKey = "__navNotificationsChannel";
  const subscribeRealtime = async () => {
    const orgId = window.BB_ACTIVE_ORG?.id;
    if (!orgId || !window.supabase?.channel) return;

    if (window[channelKey]) {
      window.supabase.removeChannel(window[channelKey]);
      window[channelKey] = null;
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
              refreshBadge();
              if (container.classList.contains("open")) loadDropdown();
            }, 200);
          }
        )
        .subscribe();
      window[channelKey] = channel;
    } catch (err) {
      console.warn("Failed to subscribe to notifications:", err);
    }
  };

  // Event handlers
  btn.addEventListener("click", async (e) => {
    e.stopPropagation();
    const willOpen = !container.classList.contains("open");
    closeOverlays(navEl, { except: willOpen ? "notifications" : undefined });
    container.classList.toggle("open", willOpen);
    btn.setAttribute("aria-expanded", willOpen ? "true" : "false");
    if (willOpen) await loadDropdown();
  });

  dropdown?.addEventListener("click", (e) => e.stopPropagation());

  list?.addEventListener("click", async (e) => {
    const item = e.target.closest(".bb-notification-item");
    if (!item) return;

    try {
      await markRead(item.dataset.id);
      await refreshBadge();
      item.classList.remove("unread");
    } catch (err) {
      console.warn("Failed to mark notification as read:", err);
    }

    const link = item.dataset.link || "";
    if (!link) return;
    container.classList.remove("open");
    btn.setAttribute("aria-expanded", "false");

    if (link.startsWith("/")) {
      window.location.href = link;
      return;
    }
    try {
      const target = new URL(link, window.location.origin);
      if (!["http:", "https:"].includes(target.protocol)) {
        throw new Error("Unsupported protocol");
      }
      window.open(target.href, "_blank", "noopener,noreferrer");
    } catch {
      console.warn("Invalid notification link:", link);
    }
  });

  document.addEventListener("click", (e) => {
    if (!container.contains(e.target)) {
      container.classList.remove("open");
      btn.setAttribute("aria-expanded", "false");
    }
  });

  markAllBtn?.addEventListener("click", async () => {
    try {
      const token = await getAccessToken();
      if (!token) return;
      const res = await fetch("/v1/notifications/read-all", {
        method: "POST",
        headers: { Authorization: `Bearer ${token}` },
        signal: AbortSignal.timeout(15000),
      });
      if (res.ok) {
        updateBadge(0);
        list?.querySelectorAll(".bb-notification-item.unread").forEach((el) => {
          el.classList.remove("unread");
        });
      }
    } catch (err) {
      console.warn("Failed to mark notifications as read:", err);
    }
  });

  document.addEventListener("bb:org-switched", async () => {
    await refreshBadge();
    await subscribeRealtime();
  });
  document.addEventListener("bb:org-ready", async () => {
    await refreshBadge();
    await subscribeRealtime();
  });
  refreshBadge();
  subscribeRealtime();
}

// ── Quota display ──────────────────────────────────────────────────────────────

function initQuota() {
  let quotaInterval = null;
  let quotaVisibilityListener = null;
  let isRefreshing = false;

  const quotaDisplay = document.getElementById("quotaDisplay");
  const quotaPlan = document.getElementById("quotaPlan");
  const quotaUsage = document.getElementById("quotaUsage");
  const quotaReset = document.getElementById("quotaReset");

  function formatTimeUntilReset(resetTime) {
    const now = new Date();
    const reset = new Date(resetTime);
    if (!resetTime || Number.isNaN(reset.getTime())) return "Resets soon";
    const diffMs = reset - now;
    if (diffMs <= 0) return "Resets soon";
    const hours = Math.floor(diffMs / (1000 * 60 * 60));
    const minutes = Math.floor((diffMs % (1000 * 60 * 60)) / (1000 * 60));
    if (hours > 0) return `Resets in ${hours}h ${minutes}m`;
    if (minutes <= 0) return "Resets soon";
    return `Resets in ${minutes}m`;
  }

  async function fetchAndDisplay() {
    if (isRefreshing) return;
    if (!quotaDisplay || !quotaPlan || !quotaUsage || !quotaReset) return;

    isRefreshing = true;
    try {
      const token = await getAccessToken();
      if (!token) return;

      const res = await fetch("/v1/usage", {
        headers: { Authorization: `Bearer ${token}` },
        signal: AbortSignal.timeout(15000),
      });
      if (!res.ok) {
        console.warn("Failed to fetch quota:", res.status);
        return;
      }

      const data = await res.json();
      const usage = data.data?.usage;
      if (!usage) return;

      const dailyLimit = Number.isFinite(usage.daily_limit)
        ? usage.daily_limit
        : null;
      const dailyUsed = Number.isFinite(usage.daily_used)
        ? usage.daily_used
        : 0;

      quotaPlan.textContent = usage.plan_display_name || "Free";
      const usageValue = dailyUsed.toLocaleString();
      const limitValue = Number.isFinite(dailyLimit)
        ? dailyLimit.toLocaleString()
        : "No limit";
      quotaUsage.textContent = `${usageValue}/${limitValue}`;
      quotaReset.textContent = formatTimeUntilReset(usage.resets_at);

      quotaDisplay.classList.remove("quota-warning", "quota-exhausted");
      if (usage.usage_percentage >= 100) {
        quotaDisplay.classList.add("quota-exhausted");
      } else if (usage.usage_percentage >= 80) {
        quotaDisplay.classList.add("quota-warning");
      }
      quotaDisplay.style.display = "flex";
    } catch (err) {
      console.warn("Error fetching quota:", err);
    } finally {
      isRefreshing = false;
    }
  }

  function startPolling() {
    if (quotaInterval) clearInterval(quotaInterval);
    quotaInterval = null;
    if (quotaVisibilityListener) {
      document.removeEventListener("visibilitychange", quotaVisibilityListener);
    }

    quotaVisibilityListener = () => {
      if (document.visibilityState === "visible") {
        fetchAndDisplay();
        if (!quotaInterval) {
          quotaInterval = setInterval(fetchAndDisplay, 30000);
        }
      } else if (quotaInterval) {
        clearInterval(quotaInterval);
        quotaInterval = null;
      }
    };

    document.addEventListener("visibilitychange", quotaVisibilityListener);
    quotaVisibilityListener();
  }

  document.addEventListener("bb:org-switched", () => fetchAndDisplay());

  // Expose for settings page refresh
  window.BBQuota = {
    refresh: fetchAndDisplay,
    start: startPolling,
    formatTimeUntilReset,
  };

  // Start after core is ready
  if (window.BB_APP?.coreReady) {
    window.BB_APP.coreReady.then(startPolling).catch(() => startPolling());
  } else {
    const check = setInterval(() => {
      if (window.supabase) {
        clearInterval(check);
        startPolling();
      }
    }, 100);
    setTimeout(() => clearInterval(check), 10000);
  }
}

// ── Mount nav ──────────────────────────────────────────────────────────────────

async function mountNav() {
  // Already mounted
  if (document.querySelector(".global-nav")) {
    finishNavReady();
    return;
  }

  // Shared job pages have no nav
  if (window.location.pathname.startsWith("/shared/jobs/")) {
    finishNavReady();
    return;
  }

  let navElement;
  try {
    const res = await fetch("/web/partials/global-nav.html");
    if (!res.ok) throw new Error(`Failed to fetch nav partial: ${res.status}`);
    const text = await res.text();
    const wrapper = document.createElement("div");
    // Safe: nav partial is a trusted server template, not user content.
    wrapper.insertAdjacentHTML("afterbegin", text.trim());
    navElement = wrapper.firstElementChild;
  } catch (err) {
    console.error("[global-nav] Could not load nav partial:", err);
    finishNavReady();
    return;
  }

  if (!navElement || !document.body) {
    finishNavReady();
    return;
  }

  document.body.prepend(navElement);

  // Page title
  const titleEl = navElement.querySelector("#globalNavTitle");
  const separatorEl = navElement.querySelector("#globalNavSeparator");
  const path = window.location.pathname.replace(/\/$/, "");

  const titleMap = [
    { match: (p) => p === "/dashboard", title: "Dashboard" },
    { match: (p) => p.startsWith("/settings"), title: "Settings" },
  ];
  const titleMatch = titleMap.find((entry) => entry.match(path));
  if (titleEl) titleEl.textContent = titleMatch ? titleMatch.title : "";
  if (separatorEl) separatorEl.style.display = titleMatch ? "inline" : "none";

  // Active nav link
  navElement.querySelectorAll(".nav-link").forEach((link) => {
    try {
      const linkPath = new URL(link.href).pathname.replace(/\/$/, "");
      const isDashboard = linkPath === "/dashboard";
      const isSettings = linkPath.startsWith("/settings");
      const active =
        (isDashboard && (path === "/dashboard" || path.startsWith("/jobs"))) ||
        (isSettings && path.startsWith("/settings"));
      link.classList.toggle("active", active);
      if (active) {
        link.setAttribute("aria-current", "page");
      } else {
        link.removeAttribute("aria-current");
      }
    } catch {
      link.classList.remove("active");
      link.removeAttribute("aria-current");
    }
  });

  // Init subsystems
  initOrgSwitcher(navElement);
  initUserMenu(navElement);
  initNotifications(navElement);
  initQuota();

  finishNavReady();
}

// ── Auto-mount ─────────────────────────────────────────────────────────────────

export function init() {
  if (document.body) {
    mountNav();
  } else {
    document.addEventListener("DOMContentLoaded", mountNav, { once: true });
  }
}

// Auto-run on import (matches legacy behaviour of running on script load).
init();
