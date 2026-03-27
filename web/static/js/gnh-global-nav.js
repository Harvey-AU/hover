(function () {
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

  let resolveNavReady = null;
  if (!window.BB_NAV_READY) {
    const { promise, resolve } = promiseWithResolvers();
    window.BB_NAV_READY = promise;
    resolveNavReady = resolve;
  }

  const finishNavReady = () => {
    if (resolveNavReady) {
      resolveNavReady();
    }
    document.dispatchEvent(new CustomEvent("bb:nav-ready"));
  };

  if (document.querySelector(".global-nav")) {
    finishNavReady();
    return;
  }

  if (window.location.pathname.startsWith("/shared/jobs/")) {
    finishNavReady();
    return;
  }

  const mountNav = async () => {
    let navElement;

    try {
      const res = await fetch("/web/partials/global-nav.html");
      if (!res.ok)
        throw new Error(`Failed to fetch nav partial: ${res.status}`);
      const html = await res.text();
      const navWrapper = document.createElement("div");
      navWrapper.innerHTML = html.trim();
      navElement = navWrapper.firstElementChild;
    } catch (err) {
      console.error("[gnh-global-nav] Could not load nav partial:", err);
      finishNavReady();
      return;
    }

    if (!navElement || !document.body) {
      finishNavReady();
      return;
    }

    document.body.prepend(navElement);

    const titleEl = navElement.querySelector("#globalNavTitle");
    const separatorEl = navElement.querySelector("#globalNavSeparator");
    const currentOrgName = navElement.querySelector("#currentOrgName");
    const settingsOrgName = document.getElementById("settingsOrgName");
    const path = window.location.pathname.replace(/\/$/, "");
    const navLinks = navElement.querySelectorAll(".nav-link");
    const closeNavOverlays = ({ except } = {}) => {
      const orgSwitcher = navElement.querySelector("#orgSwitcher");
      const orgBtn = navElement.querySelector("#orgSwitcherBtn");
      const userMenuDropdown = navElement.querySelector("#userMenuDropdown");
      const userAvatar = navElement.querySelector("#userAvatar");
      const notificationsContainer = navElement.querySelector(
        "#notificationsContainer"
      );
      const notificationsBtn = navElement.querySelector("#notificationsBtn");

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
    };

    const titleMap = [
      { match: (p) => p === "/dashboard", title: "Dashboard" },
      { match: (p) => p.startsWith("/settings"), title: "Settings" },
    ];

    const titleMatch = titleMap.find((entry) => entry.match(path));
    if (titleEl) {
      titleEl.textContent = titleMatch ? titleMatch.title : "";
    }
    if (separatorEl) {
      separatorEl.style.display = titleMatch ? "inline" : "none";
    }

    navLinks.forEach((link) => {
      try {
        const linkPath = new URL(link.href).pathname.replace(/\/$/, "");
        const isDashboard = linkPath === "/dashboard";
        const isSettings = linkPath.startsWith("/settings");

        const active =
          (isDashboard &&
            (path === "/dashboard" || path.startsWith("/jobs"))) ||
          (isSettings && path.startsWith("/settings"));

        link.classList.toggle("active", active);
        if (active) {
          link.setAttribute("aria-current", "page");
        } else {
          link.removeAttribute("aria-current");
        }
      } catch (err) {
        console.warn("Failed to resolve nav link state:", err);
        link.classList.remove("active");
        link.removeAttribute("aria-current");
      }
    });

    const initNavOrgSwitcher = async () => {
      if (!currentOrgName) return;

      const orgListEl = navElement.querySelector("#orgList");
      const orgSwitcher = navElement.querySelector("#orgSwitcher");
      const orgBtn = navElement.querySelector("#orgSwitcherBtn");

      // Helper to update the nav display
      const updateNavOrgDisplay = (activeOrg, organisations) => {
        if (!activeOrg) {
          currentOrgName.textContent = "No Organisation";
          return;
        }

        currentOrgName.textContent = activeOrg.name || "Organisation";

        if (orgListEl) {
          // Clear existing items safely
          while (orgListEl.firstChild) {
            orgListEl.removeChild(orgListEl.firstChild);
          }
          (organisations || []).forEach((org) => {
            const button = document.createElement("button");
            button.className = "gnh-org-item";
            button.dataset.orgId = org.id;
            button.dataset.orgName = org.name;
            button.textContent = org.name;
            if (org.id === activeOrg?.id) {
              button.classList.add("active");
            }
            orgListEl.appendChild(button);
          });
        }
      };

      // Handle org item clicks (delegated)
      if (orgListEl) {
        orgListEl.addEventListener("click", async (e) => {
          const item = e.target.closest(".gnh-org-item");
          if (!item || item.classList.contains("active")) {
            orgSwitcher?.classList.remove("open");
            return;
          }

          const orgId = item.dataset.orgId;

          // Show loading state
          currentOrgName.textContent = "Switching...";
          orgSwitcher?.classList.remove("open");
          orgBtn?.setAttribute("aria-expanded", "false");

          try {
            // Use shared switch function
            await window.GNH_APP.switchOrg(orgId);
            // bb:org-switched event will update UI
          } catch (err) {
            console.warn("Failed to switch organisation:", err);
            // Restore previous name
            currentOrgName.textContent =
              window.BB_ACTIVE_ORG?.name || "Organisation";
          }
        });
      }

      // Toggle dropdown
      if (orgSwitcher && orgBtn) {
        orgBtn.addEventListener("click", (event) => {
          event.stopPropagation();
          const willOpen = !orgSwitcher.classList.contains("open");
          closeNavOverlays({ except: willOpen ? "org" : undefined });
          orgSwitcher.classList.toggle("open", willOpen);
          orgBtn.setAttribute("aria-expanded", willOpen ? "true" : "false");
        });

        document.addEventListener("click", () => {
          orgSwitcher.classList.remove("open");
          orgBtn.setAttribute("aria-expanded", "false");
        });
      }

      // Listen for org switches (from anywhere) — re-render full list so newly
      // created orgs appear without a page reload.
      document.addEventListener("bb:org-switched", (e) => {
        const newOrg = e.detail?.organisation;
        if (newOrg) {
          updateNavOrgDisplay(newOrg, window.BB_ORGANISATIONS);
        }
      });

      // Listen for org ready (after auth state restored)
      document.addEventListener("bb:org-ready", () => {
        updateNavOrgDisplay(window.BB_ACTIVE_ORG, window.BB_ORGANISATIONS);
      });

      // Sync with settings page org name if present
      if (settingsOrgName) {
        const orgNameObserver = new MutationObserver(() => {
          const nextName = settingsOrgName.textContent?.trim();
          if (nextName && nextName !== currentOrgName.textContent) {
            currentOrgName.textContent = nextName;
          }
        });
        orgNameObserver.observe(settingsOrgName, {
          childList: true,
          characterData: true,
          subtree: true,
        });
        window.addEventListener(
          "beforeunload",
          () => orgNameObserver.disconnect(),
          { once: true }
        );
      }

      // Wait for core (Supabase) to be ready, then init org
      try {
        if (window.GNH_APP?.coreReady) {
          await window.GNH_APP.coreReady;
        }
        if (window.GNH_APP?.initialiseOrg) {
          await window.GNH_APP.initialiseOrg();
        }
        updateNavOrgDisplay(window.BB_ACTIVE_ORG, window.BB_ORGANISATIONS);
      } catch (err) {
        console.warn("Organisation initialisation failed:", err);
        currentOrgName.textContent = "Organisation";
      }
    };

    initNavOrgSwitcher();

    const initUserMenuDropdown = () => {
      const userMenu = navElement.querySelector("#userMenu");
      const userAvatar = navElement.querySelector("#userAvatar");
      const userMenuDropdown = navElement.querySelector("#userMenuDropdown");
      const currentOrgNameEl = currentOrgName;
      const userMenuOrgNameEl = navElement.querySelector("#userMenuOrgName");
      if (!userMenu || !userAvatar || !userMenuDropdown) return;

      const syncUserMenuOrgName = () => {
        if (!currentOrgNameEl || !userMenuOrgNameEl) return;
        const name = currentOrgNameEl.textContent?.trim();
        if (name) {
          userMenuOrgNameEl.textContent = name;
        }
      };

      if (currentOrgNameEl) {
        const orgNameObserver = new MutationObserver(syncUserMenuOrgName);
        orgNameObserver.observe(currentOrgNameEl, {
          childList: true,
          characterData: true,
          subtree: true,
        });
        window.addEventListener(
          "beforeunload",
          () => orgNameObserver.disconnect(),
          { once: true }
        );
      }

      userAvatar.addEventListener("click", (event) => {
        event.stopPropagation();
        const willOpen = !userMenuDropdown.classList.contains("show");
        closeNavOverlays({ except: willOpen ? "user" : undefined });
        userMenuDropdown.classList.toggle("show", willOpen);
        userAvatar.setAttribute("aria-expanded", willOpen ? "true" : "false");
      });

      userMenuDropdown.addEventListener("click", (event) => {
        event.stopPropagation();
      });

      document.addEventListener("click", (event) => {
        if (!userMenu.contains(event.target)) {
          userMenuDropdown.classList.remove("show");
          userAvatar.setAttribute("aria-expanded", "false");
        }
      });

      document.addEventListener("keydown", (event) => {
        if (
          event.key === "Escape" &&
          userMenuDropdown.classList.contains("show")
        ) {
          userMenuDropdown.classList.remove("show");
          userAvatar.setAttribute("aria-expanded", "false");
          userAvatar.focus();
        }
      });

      document.addEventListener("bb:org-switched", syncUserMenuOrgName);
      document.addEventListener("bb:org-ready", syncUserMenuOrgName);
      syncUserMenuOrgName();
    };

    initUserMenuDropdown();

    const initNavNotifications = () => {
      const notificationsContainer = navElement.querySelector(
        "#notificationsContainer"
      );
      const notificationsBtn = navElement.querySelector("#notificationsBtn");
      const notificationsDropdown = navElement.querySelector(
        "#notificationsDropdown"
      );
      const notificationsList = navElement.querySelector("#notificationsList");
      const notificationsBadge = navElement.querySelector(
        "#notificationsBadge"
      );
      const markAllReadBtn = navElement.querySelector("#markAllReadBtn");
      if (!notificationsContainer || !notificationsBtn || !notificationsBadge) {
        return;
      }
      notificationsBtn.setAttribute("aria-haspopup", "true");
      notificationsBtn.setAttribute("aria-expanded", "false");

      const getAccessToken = async () => {
        return (
          window.dataBinder?.authManager?.session?.access_token ||
          (await window.supabase?.auth?.getSession?.())?.data?.session
            ?.access_token ||
          null
        );
      };

      const updateBadge = (count) => {
        const unreadCount = Number(count) || 0;
        notificationsBadge.textContent =
          unreadCount > 9 ? "9+" : unreadCount || "";
        notificationsBadge.dataset.count = unreadCount;
      };

      const fetchNotifications = async (limit = 10) => {
        const token = await getAccessToken();
        if (!token) return null;
        const response = await fetch(`/v1/notifications?limit=${limit}`, {
          headers: { Authorization: `Bearer ${token}` },
          signal: AbortSignal.timeout(15000),
        });
        if (!response.ok) return null;
        return response.json();
      };

      const refreshBadge = async () => {
        try {
          const data = await fetchNotifications(1);
          if (!data) return;
          const unreadCount = data.unread_count ?? data.data?.unread_count ?? 0;
          updateBadge(unreadCount);
        } catch (error) {
          console.warn("Failed to refresh notifications badge:", error);
        }
      };

      const renderList = (notifications) => {
        const escapeHtml = (value) =>
          String(value ?? "")
            .replace(/&/g, "&amp;")
            .replace(/</g, "&lt;")
            .replace(/>/g, "&gt;")
            .replace(/"/g, "&quot;");

        if (!notificationsList) return;
        if (!notifications || notifications.length === 0) {
          notificationsList.innerHTML = `
            <div class="gnh-notifications-empty">
              <div class="gnh-notifications-empty-icon">🔔</div>
              <div>No notifications yet</div>
            </div>
          `;
          return;
        }

        notificationsList.innerHTML = notifications
          .map((n) => {
            const preview = escapeHtml(n.preview);
            const subject = escapeHtml(n.subject);
            const notificationId = escapeHtml(n.id);
            const notificationLink = escapeHtml(n.link);
            return `
              <button
                type="button"
                class="gnh-notification-item ${!n.read_at ? "unread" : ""}"
                data-id="${notificationId}"
                data-link="${notificationLink}"
              >
                <div class="gnh-notification-item-content">
                  <div class="gnh-notification-item-subject">${subject}</div>
                  <div class="gnh-notification-item-preview">${preview}</div>
                </div>
              </button>
            `;
          })
          .join("");
      };

      const loadDropdown = async () => {
        try {
          const data = await fetchNotifications(10);
          if (!data) return;
          updateBadge(data.unread_count ?? data.data?.unread_count ?? 0);
          renderList(data.notifications ?? data.data?.notifications ?? []);
        } catch (error) {
          console.warn("Failed to load notifications:", error);
        }
      };

      const markNotificationRead = async (id) => {
        if (!id) return;
        const token = await getAccessToken();
        if (!token) return;
        const response = await fetch(`/v1/notifications/${id}/read`, {
          method: "POST",
          headers: { Authorization: `Bearer ${token}` },
        });
        if (!response.ok) {
          throw new Error("Failed to mark notification as read");
        }
      };

      const notificationsChannelKey = "__bbNavNotificationsChannel";
      const subscribeRealtime = async () => {
        const orgId = window.BB_ACTIVE_ORG?.id;
        const supabaseAuth = window.supabase?.auth;
        if (!orgId || !supabaseAuth || !window.supabase?.channel) {
          return;
        }

        if (window[notificationsChannelKey]) {
          window.supabase.removeChannel(window[notificationsChannelKey]);
          window[notificationsChannelKey] = null;
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
                  if (notificationsContainer.classList.contains("open")) {
                    loadDropdown();
                  }
                }, 200);
              }
            )
            .subscribe();
          window[notificationsChannelKey] = channel;
        } catch (error) {
          console.warn("Failed to subscribe to notifications:", error);
        }
      };

      notificationsBtn.addEventListener("click", async (event) => {
        event.stopPropagation();
        const willOpen = !notificationsContainer.classList.contains("open");
        closeNavOverlays({ except: willOpen ? "notifications" : undefined });
        notificationsContainer.classList.toggle("open", willOpen);
        notificationsBtn.setAttribute(
          "aria-expanded",
          willOpen ? "true" : "false"
        );
        if (willOpen) {
          await loadDropdown();
        }
      });

      notificationsDropdown?.addEventListener("click", (event) => {
        event.stopPropagation();
      });

      notificationsList?.addEventListener("click", async (event) => {
        const item = event.target.closest(".gnh-notification-item");
        if (!item) return;

        const id = item.dataset.id;
        const link = item.dataset.link || "";
        try {
          await markNotificationRead(id);
          await refreshBadge();
          item.classList.remove("unread");
        } catch (error) {
          console.warn("Failed to mark notification as read:", error);
        }

        if (!link) return;
        notificationsContainer.classList.remove("open");
        notificationsBtn.setAttribute("aria-expanded", "false");
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
        } catch (_error) {
          console.warn("Invalid notification link:", link);
        }
      });

      document.addEventListener("click", (event) => {
        if (!notificationsContainer.contains(event.target)) {
          notificationsContainer.classList.remove("open");
          notificationsBtn.setAttribute("aria-expanded", "false");
        }
      });

      markAllReadBtn?.addEventListener("click", async () => {
        try {
          const token = await getAccessToken();
          if (!token) return;
          const response = await fetch("/v1/notifications/read-all", {
            method: "POST",
            headers: { Authorization: `Bearer ${token}` },
            signal: AbortSignal.timeout(15000),
          });
          if (response.ok) {
            updateBadge(0);
            notificationsList
              ?.querySelectorAll(".gnh-notification-item.unread")
              .forEach((el) => {
                el.classList.remove("unread");
              });
          }
        } catch (error) {
          console.warn("Failed to mark notifications as read:", error);
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
    };

    initNavNotifications();

    // Quota display logic (global, runs on all pages)
    const initQuota = () => {
      let quotaInterval = null;
      let quotaVisibilityListener = null;
      let isQuotaRefreshing = false;

      // Cache DOM elements once (they're static in the nav template)
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
        if (hours > 0) {
          return `Resets in ${hours}h ${minutes}m`;
        }
        if (minutes <= 0) return "Resets soon";
        return `Resets in ${minutes}m`;
      }

      async function fetchAndDisplayQuota() {
        if (isQuotaRefreshing) return;
        if (
          !quotaDisplay ||
          !quotaPlan ||
          !quotaUsage ||
          !quotaReset ||
          !window.supabase
        )
          return;

        isQuotaRefreshing = true;
        try {
          // Use cached session from dataBinder if available, fallback to getSession()
          const token =
            window.dataBinder?.authManager?.session?.access_token ||
            (await window.supabase.auth.getSession())?.data?.session
              ?.access_token;
          if (!token) return;

          const response = await fetch("/v1/usage", {
            headers: { Authorization: `Bearer ${token}` },
            signal: AbortSignal.timeout(15000), // 15s ceiling
          });

          if (!response.ok) {
            console.warn("Failed to fetch quota:", response.status);
            return;
          }

          const data = await response.json();
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
          isQuotaRefreshing = false;
        }
      }

      function startQuotaPolling() {
        if (quotaInterval) clearInterval(quotaInterval);
        quotaInterval = null;
        if (quotaVisibilityListener) {
          document.removeEventListener(
            "visibilitychange",
            quotaVisibilityListener
          );
        }

        quotaVisibilityListener = () => {
          if (document.visibilityState === "visible") {
            fetchAndDisplayQuota();
            if (!quotaInterval) {
              quotaInterval = setInterval(fetchAndDisplayQuota, 30000);
            }
          } else if (quotaInterval) {
            clearInterval(quotaInterval);
            quotaInterval = null;
          }
        };

        document.addEventListener("visibilitychange", quotaVisibilityListener);
        quotaVisibilityListener();
      }

      // Refresh quota immediately when organisation changes (registered once)
      document.addEventListener("bb:org-switched", () => {
        fetchAndDisplayQuota();
      });

      // Expose globally for settings page to trigger refresh
      window.BBQuota = {
        refresh: fetchAndDisplayQuota,
        start: startQuotaPolling,
        formatTimeUntilReset,
      };

      // Start after core (Supabase) is ready
      if (window.GNH_APP?.coreReady) {
        window.GNH_APP.coreReady
          .then(startQuotaPolling)
          .catch(() => startQuotaPolling()); // still attempt polling on failure
      } else {
        // Fallback: wait for supabase to be defined
        const checkSupabase = setInterval(() => {
          if (window.supabase) {
            clearInterval(checkSupabase);
            startQuotaPolling();
          }
        }, 100);
        // Stop checking after 10 seconds
        setTimeout(() => clearInterval(checkSupabase), 10000);
      }
    };

    initQuota();

    finishNavReady();
  };

  if (document.body) {
    mountNav();
  } else {
    document.addEventListener("DOMContentLoaded", mountNav, { once: true });
  }
})();
