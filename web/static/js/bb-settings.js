/*
 * Settings page logic
 * Handles navigation, organisation controls, and settings data loads.
 */

(function () {
  const AUTH_METHOD_DEFS = [
    {
      key: "google",
      label: "Google",
      icon_url: "/assets/auth-providers/google.svg",
      supported: true,
    },
    {
      key: "github",
      label: "GitHub",
      icon_url: "/assets/auth-providers/github.svg",
      supported: true,
    },
    {
      key: "email",
      label: "Email/Password",
      icon_url: "",
      supported: true,
    },
    {
      key: "azure",
      label: "Microsoft",
      icon_url: "/assets/auth-providers/microsoft.svg",
      supported: true,
    },
    {
      key: "facebook",
      label: "Facebook",
      icon_url: "/assets/auth-providers/facebook.png",
      supported: true,
    },
    {
      key: "slack_oidc",
      label: "Slack",
      icon_url: "/assets/auth-providers/slack.svg",
      supported: true,
    },
  ];

  const settingsState = {
    currentUserRole: "member",
    currentUserId: null,
    authMethods: [],
    authIdentities: [],
    authUserEmail: "",
  };

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
    icon.textContent = type === "success" ? "✅" : "⚠️";
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

  function normaliseAuthProvider(provider) {
    const value = (provider || "").trim().toLowerCase();
    if (value === "slack") return "slack_oidc";
    if (
      value === "google" ||
      value === "github" ||
      value === "email" ||
      value === "azure" ||
      value === "facebook" ||
      value === "slack_oidc"
    ) {
      return value;
    }
    return "";
  }

  function getAuthMethodDef(provider) {
    return (
      AUTH_METHOD_DEFS.find((method) => method.key === provider) || {
        key: provider,
        label: provider || "Unknown",
        icon: "?",
        supported: true,
      }
    );
  }

  function formatAuthMethod(method) {
    const value = normaliseAuthProvider(method) || method;
    return getAuthMethodDef(value).label;
  }

  function providerIcon(provider) {
    const method = getAuthMethodDef(provider);
    if (method.icon_url) {
      return `<img src="${method.icon_url}" alt="" loading="lazy" decoding="async" referrerpolicy="no-referrer" />`;
    }
    return `<span class="settings-auth-fallback-icon" aria-hidden="true">•</span>`;
  }

  function providerSubtitle(method) {
    if (method.connected) {
      return method.email || "Connected";
    }
    if (method.provider === "email") {
      return "Set a password to enable email sign-in";
    }
    return "Not connected";
  }

  function getOAuthQueryParams(provider) {
    switch (provider) {
      case "google":
        return { prompt: "select_account consent" };
      case "azure":
        return { prompt: "select_account" };
      case "facebook":
        return { auth_type: "reauthenticate" };
      case "slack_oidc":
        return { prompt: "consent" };
      default:
        return {};
    }
  }

  async function connectAuthMethod(provider) {
    if (!window.supabase?.auth) return;

    try {
      if (provider === "email") {
        await sendPasswordReset();
        showSettingsToast(
          "success",
          "Password setup email sent. This enables email sign-in."
        );
        return;
      }

      const currentPath = `${window.location.pathname}${window.location.search}${window.location.hash}`;
      if (currentPath && currentPath !== "/") {
        try {
          window.sessionStorage.setItem(
            "bb_post_auth_return_target",
            currentPath
          );
        } catch (_error) {
          // Ignore storage failures and continue OAuth flow.
        }
      }
      const callbackTarget = new URL(`${window.location.origin}/auth/callback`);
      if (currentPath && currentPath !== "/") {
        callbackTarget.searchParams.set("return_to", currentPath);
      }
      const callbackUrl = callbackTarget.toString();
      const queryParams = getOAuthQueryParams(provider);

      if (typeof window.supabase.auth.linkIdentity === "function") {
        const { data, error } = await window.supabase.auth.linkIdentity({
          provider,
          options: { redirectTo: callbackUrl, queryParams },
        });
        if (error) throw error;
        if (data?.url) {
          window.location.assign(data.url);
          return;
        }
      } else {
        const { data, error } = await window.supabase.auth.signInWithOAuth({
          provider,
          options: { redirectTo: callbackUrl, queryParams },
        });
        if (error) throw error;
        if (data?.url) {
          window.location.assign(data.url);
          return;
        }
      }

      showSettingsToast("success", `${formatAuthMethod(provider)} connected`);
      await loadAccountDetails();
    } catch (err) {
      console.error(`Failed to connect ${provider}:`, err);
      showSettingsToast(
        "error",
        err?.message || `Failed to connect ${formatAuthMethod(provider)}`
      );
    }
  }

  async function unlinkIdentityViaApi(identityId) {
    const sessionResult = await window.supabase.auth.getSession();
    const accessToken = sessionResult?.data?.session?.access_token;
    const authUrl = window.BBB_CONFIG?.supabaseUrl;
    const anonKey = window.BBB_CONFIG?.supabaseAnonKey;
    if (!accessToken || !authUrl || !anonKey) {
      throw new Error("Missing auth session details");
    }

    const response = await fetch(
      `${authUrl}/auth/v1/user/identities/${encodeURIComponent(identityId)}`,
      {
        method: "DELETE",
        headers: {
          Authorization: `Bearer ${accessToken}`,
          apikey: anonKey,
          "Content-Type": "application/json",
        },
      }
    );

    if (!response.ok) {
      const responseJson = await response.json().catch(() => ({}));
      throw new Error(
        responseJson?.msg || responseJson?.error || "Unlink failed"
      );
    }
  }

  async function removeAuthMethod(method, connectedCount) {
    if (connectedCount <= 1) {
      showSettingsToast("error", "You must keep at least one sign-in method.");
      return;
    }
    if (method.provider === "email") {
      showSettingsToast(
        "warning",
        "Email/password removal isn’t supported in settings yet."
      );
      return;
    }

    if (!method.identity?.identity_id) {
      showSettingsToast("error", "Unable to remove this method.");
      return;
    }

    if (!confirm(`Remove ${formatAuthMethod(method.provider)} sign-in?`)) {
      return;
    }

    try {
      if (typeof window.supabase.auth.unlinkIdentity === "function") {
        const { error } = await window.supabase.auth.unlinkIdentity(
          method.identity
        );
        if (error) throw error;
      } else {
        await unlinkIdentityViaApi(method.identity.identity_id);
      }

      try {
        await window.supabase.auth.refreshSession();
      } catch (err) {
        console.warn("Failed to refresh session after unlink:", err);
      }

      showSettingsToast(
        "success",
        `${formatAuthMethod(method.provider)} removed`
      );
      await loadAccountDetails();
    } catch (err) {
      console.error(`Failed to remove ${method.provider}:`, err);
      showSettingsToast(
        "error",
        err?.message || `Failed to remove ${formatAuthMethod(method.provider)}`
      );
    }
  }

  function renderAuthMethods(methods) {
    const authMethodsEl = document.getElementById("settingsAuthMethods");
    if (!authMethodsEl) return;

    authMethodsEl.innerHTML = "";
    if (!Array.isArray(methods)) {
      return;
    }

    const connectedCount = methods.filter((method) => method.connected).length;
    const visibleMethods = methods.filter(
      (method) => method.provider !== "email"
    );

    visibleMethods.forEach((method) => {
      const card = document.createElement("div");
      card.className = "settings-auth-method-card";

      const details = document.createElement("div");
      details.className = "settings-auth-method-details";

      const icon = document.createElement("span");
      icon.className = `settings-auth-provider-icon settings-auth-provider-${method.provider}`;
      icon.innerHTML = providerIcon(method.provider);

      const text = document.createElement("div");
      text.className = "settings-auth-method-text";

      const name = document.createElement("strong");
      name.textContent = formatAuthMethod(method.provider);

      const subtitle = document.createElement("span");
      subtitle.className = "settings-muted";
      subtitle.textContent = providerSubtitle(method);

      text.appendChild(name);
      text.appendChild(subtitle);
      details.appendChild(icon);
      details.appendChild(text);

      const actionBtn = document.createElement("button");
      actionBtn.className = "bb-button bb-button-outline settings-btn-sm";
      actionBtn.type = "button";
      actionBtn.textContent = method.connected ? "Remove" : "Connect";

      if (
        method.connected &&
        (connectedCount <= 1 || method.provider === "email")
      ) {
        actionBtn.disabled = true;
        actionBtn.title = "At least one sign-in method must remain";
      }

      actionBtn.addEventListener("click", async () => {
        const permanentlyDisabled =
          method.connected &&
          (connectedCount <= 1 || method.provider === "email");
        if (permanentlyDisabled) return;

        actionBtn.disabled = true;
        const originalText = actionBtn.textContent;
        actionBtn.textContent = method.connected
          ? "Removing..."
          : "Connecting...";
        if (method.connected) {
          await removeAuthMethod(method, connectedCount);
        } else {
          await connectAuthMethod(method.provider);
        }
        actionBtn.textContent = originalText;
        actionBtn.disabled = permanentlyDisabled;
      });

      card.appendChild(details);
      card.appendChild(actionBtn);
      authMethodsEl.appendChild(card);
    });
  }

  function splitName(fullName) {
    const value = (fullName || "").trim();
    if (!value) return { firstName: "", lastName: "" };
    const parts = value.split(/\s+/).filter(Boolean);
    if (parts.length === 0) return { firstName: "", lastName: "" };
    if (parts.length === 1) return { firstName: parts[0], lastName: "" };
    return { firstName: parts[0], lastName: parts.slice(1).join(" ") };
  }

  async function loadAccountDetails() {
    const sessionResult = await window.supabase.auth.getSession();
    const session = sessionResult?.data?.session;
    if (!session?.user) return;

    const fallbackEmail = session.user.email || "";
    const fallbackFirstName =
      session.user.user_metadata?.given_name ||
      session.user.user_metadata?.first_name ||
      "";
    const fallbackLastName =
      session.user.user_metadata?.family_name ||
      session.user.user_metadata?.last_name ||
      "";
    const fallbackFullName =
      session.user.user_metadata?.full_name ||
      session.user.user_metadata?.name ||
      "";
    const fallbackMethods = session.user.app_metadata?.providers || [];

    let email = fallbackEmail;
    let firstName = fallbackFirstName;
    let lastName = fallbackLastName;
    let fullName = fallbackFullName;
    let authMethods = fallbackMethods;
    let authIdentities = [];
    let authUser = session.user;

    try {
      const userResult = await window.supabase.auth.getUser();
      authUser = userResult?.data?.user || session.user;
      authIdentities = Array.isArray(authUser?.identities)
        ? authUser.identities
        : [];
    } catch (err) {
      console.warn("Failed to load auth identities:", err);
    }

    try {
      const response = await window.dataBinder.fetchData("/v1/auth/profile");
      const profileUser = response?.user || {};
      if (profileUser.email) {
        email = profileUser.email;
      }
      if (Object.hasOwn(profileUser, "full_name")) {
        fullName = (profileUser.full_name || "").trim();
      }
      if (Object.hasOwn(profileUser, "first_name")) {
        firstName = (profileUser.first_name || "").trim();
      }
      if (Object.hasOwn(profileUser, "last_name")) {
        lastName = (profileUser.last_name || "").trim();
      }
      if (Array.isArray(response?.auth_methods)) {
        authMethods = response.auth_methods;
      }
    } catch (_err) {
      console.warn("Failed to load profile from API.");
    }

    if (!firstName && !lastName && fullName) {
      const split = splitName(fullName);
      firstName = split.firstName;
      lastName = split.lastName;
    }

    const emailEl = document.getElementById("settingsUserEmail");
    const firstNameInputEl = document.getElementById(
      "settingsUserFirstNameInput"
    );
    const lastNameInputEl = document.getElementById(
      "settingsUserLastNameInput"
    );
    if (emailEl) emailEl.textContent = email || "Not set";
    if (firstNameInputEl) firstNameInputEl.value = firstName || "";
    if (lastNameInputEl) lastNameInputEl.value = lastName || "";

    const connectedProviders = new Set();
    const hasIdentityData =
      Array.isArray(authIdentities) && authIdentities.length > 0;
    if (hasIdentityData) {
      authIdentities.forEach((identity) => {
        const normalised = normaliseAuthProvider(identity.provider);
        if (normalised) connectedProviders.add(normalised);
      });
    } else {
      (Array.isArray(authMethods) ? authMethods : []).forEach((provider) => {
        const normalised = normaliseAuthProvider(provider);
        if (normalised) connectedProviders.add(normalised);
      });
    }

    const methodModels = AUTH_METHOD_DEFS.map((methodDef) => {
      const provider = methodDef.key;
      const identity = authIdentities.find(
        (candidate) => normaliseAuthProvider(candidate.provider) === provider
      );
      return {
        provider,
        supported: true,
        connected: connectedProviders.has(provider),
        email:
          identity?.identity_data?.email ||
          identity?.email ||
          authUser?.email ||
          email,
        identity: identity || null,
      };
    });

    settingsState.authMethods = Array.from(connectedProviders);
    settingsState.authIdentities = authIdentities;
    settingsState.authUserEmail = email;

    const passwordStatusEl = document.getElementById(
      "settingsPasswordMethodStatus"
    );
    if (passwordStatusEl) {
      const emailMethod = methodModels.find(
        (method) => method.provider === "email"
      );
      passwordStatusEl.textContent = emailMethod?.connected
        ? "Email/password sign-in enabled. Use reset email to change your password."
        : "Email/password sign-in not connected yet. Send reset email to set it up.";
    }

    renderAuthMethods(methodModels);
  }

  async function saveProfileName() {
    const firstNameInputEl = document.getElementById(
      "settingsUserFirstNameInput"
    );
    const lastNameInputEl = document.getElementById(
      "settingsUserLastNameInput"
    );
    const saveBtn = document.getElementById("settingsSaveName");
    if (!firstNameInputEl || !lastNameInputEl || !saveBtn) return;

    const firstName = firstNameInputEl.value.trim();
    const lastName = lastNameInputEl.value.trim();
    const fullName = `${firstName} ${lastName}`.trim();
    if (firstName.length > 80) {
      showSettingsToast("error", "First name must be 80 characters or fewer");
      return;
    }
    if (lastName.length > 80) {
      showSettingsToast("error", "Last name must be 80 characters or fewer");
      return;
    }

    saveBtn.disabled = true;
    const originalText = saveBtn.textContent;
    saveBtn.textContent = "Saving...";

    try {
      let metadataUpdateSucceeded = true;
      try {
        const payload = {
          first_name: firstName || "",
          last_name: lastName || "",
          given_name: firstName || "",
          family_name: lastName || "",
          full_name: fullName || "",
          name: fullName || "",
        };
        await window.supabase.auth.updateUser({
          data: payload,
        });
      } catch (_err) {
        console.warn("Failed to update auth metadata name.");
        metadataUpdateSucceeded = false;
      }

      await window.dataBinder.fetchData("/v1/auth/profile", {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          first_name: firstName,
          last_name: lastName,
          full_name: fullName,
        }),
      });

      if (metadataUpdateSucceeded) {
        showSettingsToast("success", "Name updated");
      } else {
        showSettingsToast(
          "warning",
          "Name saved, but auth metadata sync failed. Please re-login if needed."
        );
      }
      await loadAccountDetails();
      await loadOrganisationMembers();
    } catch (_err) {
      console.error("Failed to save profile name.");
      showSettingsToast("error", "Failed to update name");
    } finally {
      saveBtn.disabled = false;
      saveBtn.textContent = originalText || "Save name";
    }
  }

  async function sendPasswordReset() {
    const sessionResult = await window.supabase.auth.getSession();
    const session = sessionResult?.data?.session;
    const email = session?.user?.email;
    if (!email) {
      showSettingsToast("error", "Email address not available");
      return;
    }

    try {
      const { error } = await window.supabase.auth.resetPasswordForEmail(
        email,
        {
          redirectTo: window.location.origin + "/settings/account#security",
        }
      );
      if (error) {
        throw error;
      }
      showSettingsToast("success", "Password reset email sent");
    } catch (err) {
      console.error("Failed to send password reset:", err);
      showSettingsToast("error", "Failed to send password reset email");
    }
  }

  async function loadOrganisationMembers() {
    const membersList = document.getElementById("teamMembersList");
    const memberTemplate = document.getElementById("teamMemberTemplate");
    const emptyState = document.getElementById("teamMembersEmpty");
    if (!membersList || !memberTemplate) return;

    membersList.innerHTML = "";

    try {
      const response = await window.dataBinder.fetchData(
        "/v1/organisations/members"
      );
      const members = response.members || [];
      settingsState.currentUserRole = response.current_user_role || "member";
      settingsState.currentUserId = response.current_user_id || null;

      if (members.length === 0) {
        if (emptyState) emptyState.style.display = "block";
        return;
      }
      if (emptyState) emptyState.style.display = "none";

      members.forEach((member) => {
        const clone = memberTemplate.content.cloneNode(true);
        const row = clone.querySelector(".settings-member-row");
        const avatarEl = clone.querySelector(".settings-member-avatar");
        const nameEl = clone.querySelector(".settings-member-name");
        const emailEl = clone.querySelector(".settings-member-email");
        const roleSelect = clone.querySelector(".settings-member-role-select");
        const removeBtn = clone.querySelector(".settings-member-remove");

        if (row) row.dataset.memberId = member.id;
        if (nameEl) {
          nameEl.textContent = member.full_name || "Unnamed";
        }
        if (emailEl) emailEl.textContent = member.email || "";
        if (avatarEl) {
          const initialsSource = member.full_name || member.email || "";
          const initials =
            window.BBAvatar?.getInitials?.(initialsSource) ||
            window.BBAuth?.getInitials?.(initialsSource) ||
            "?";
          const avatarSize = Math.ceil(34 * (window.devicePixelRatio || 1));
          window.BBAvatar?.setUserAvatar?.(
            avatarEl,
            member.email || "",
            initials,
            {
              size: avatarSize,
              alt: `${member.full_name || member.email || "Member"} avatar`,
            }
          );
        }
        if (roleSelect) {
          roleSelect.value = member.role || "member";
          const canEditRole =
            settingsState.currentUserRole === "admin" &&
            member.id !== settingsState.currentUserId;
          roleSelect.disabled = !canEditRole;
          roleSelect.addEventListener("change", async () => {
            const previousValue = member.role || "member";
            try {
              await updateMemberRole(member.id, roleSelect.value);
              member.role = roleSelect.value;
            } catch {
              // Error already handled/toasted in updateMemberRole; revert UI.
              roleSelect.value = previousValue;
            }
          });
        }

        if (removeBtn) {
          removeBtn.dataset.memberId = member.id;
          removeBtn.addEventListener("click", () => removeMember(member.id));

          const canRemove =
            settingsState.currentUserRole === "admin" &&
            member.id !== settingsState.currentUserId;
          if (!canRemove) {
            removeBtn.disabled = true;
          }
        }

        membersList.appendChild(clone);
      });

      updateAdminVisibility();
    } catch (err) {
      console.error("Failed to load members:", err);
      showSettingsToast("error", "Failed to load members");
    }
  }

  async function removeMember(memberId) {
    if (!memberId) return;
    if (!confirm("Remove this member from the organisation?")) return;

    try {
      await window.dataBinder.fetchData(
        `/v1/organisations/members/${memberId}`,
        {
          method: "DELETE",
        }
      );
      showSettingsToast("success", "Member removed");
      loadOrganisationMembers();
    } catch (err) {
      console.error("Failed to remove member:", err);
      showSettingsToast("error", "Failed to remove member");
    }
  }

  async function updateMemberRole(memberId, role) {
    if (!memberId) return;

    try {
      await window.dataBinder.fetchData(
        `/v1/organisations/members/${memberId}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ role }),
        }
      );
      showSettingsToast("success", "Member role updated");
      await loadOrganisationMembers();
    } catch (err) {
      console.error("Failed to update member role.");
      showSettingsToast("error", "Failed to update member role");
      throw err;
    }
  }

  async function loadOrganisationInvites() {
    const invitesList = document.getElementById("teamInvitesList");
    const inviteTemplate = document.getElementById("teamInviteTemplate");
    const emptyState = document.getElementById("teamInvitesEmpty");
    const defaultEmptyText =
      emptyState?.textContent?.trim() || "No pending invites.";
    if (!invitesList || !inviteTemplate) return;

    invitesList.innerHTML = "";

    if (settingsState.currentUserRole !== "admin") {
      if (emptyState) {
        emptyState.style.display = "block";
        emptyState.textContent = "Only admins can view pending invites.";
      }
      return;
    }
    if (emptyState) {
      emptyState.textContent = defaultEmptyText;
    }

    try {
      const response = await window.dataBinder.fetchData(
        "/v1/organisations/invites"
      );
      const invites = response.invites || [];

      if (invites.length === 0) {
        if (emptyState) emptyState.style.display = "block";
        return;
      }
      if (emptyState) emptyState.style.display = "none";

      invites.forEach((invite) => {
        const clone = inviteTemplate.content.cloneNode(true);
        const row = clone.querySelector(".settings-invite-row");
        const emailEl = clone.querySelector(".settings-invite-email");
        const roleEl = clone.querySelector(".settings-invite-role");
        const dateEl = clone.querySelector(".settings-invite-date");
        const revokeBtn = clone.querySelector(".settings-invite-revoke");
        const copyBtn = clone.querySelector(".settings-invite-copy");

        if (row) row.dataset.inviteId = invite.id;
        if (emailEl) emailEl.textContent = invite.email;
        if (roleEl) roleEl.textContent = invite.role;
        if (dateEl) {
          const date = new Date(invite.created_at);
          dateEl.textContent = `Sent ${date.toLocaleDateString("en-AU")}`;
        }
        if (copyBtn) {
          if (invite.invite_link) {
            copyBtn.addEventListener("click", async () => {
              try {
                await navigator.clipboard.writeText(invite.invite_link);
                copyBtn.textContent = "Copied!";
                setTimeout(() => {
                  copyBtn.textContent = "Copy link";
                }, 2000);
              } catch {
                showSettingsToast("error", "Failed to copy link");
              }
            });
          } else {
            copyBtn.style.display = "none";
          }
        }
        if (revokeBtn) {
          revokeBtn.dataset.inviteId = invite.id;
          revokeBtn.addEventListener("click", () => revokeInvite(invite.id));
        }

        invitesList.appendChild(clone);
      });
    } catch (err) {
      console.error("Failed to load invites:", err);
      showSettingsToast("error", "Failed to load invites");
    }
  }

  async function sendInvite(event) {
    event.preventDefault();
    if (settingsState.currentUserRole !== "admin") {
      showSettingsToast("error", "Only admins can send invites");
      return;
    }

    const emailInput = document.getElementById("teamInviteEmail");
    const roleSelect = document.getElementById("teamInviteRole");
    if (!emailInput) return;

    const email = emailInput.value.trim();
    const role = roleSelect?.value || "member";
    if (!email) {
      showSettingsToast("error", "Email is required");
      return;
    }

    try {
      const result = await window.dataBinder.fetchData(
        "/v1/organisations/invites",
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ email, role }),
        }
      );
      const delivery = result?.invite?.email_delivery;
      if (delivery === "failed") {
        showSettingsToast(
          "warning",
          "Invite created but email failed — use the copy link button to share manually"
        );
      } else {
        showSettingsToast("success", "Invite sent");
      }
      emailInput.value = "";
      await loadOrganisationInvites();
    } catch (err) {
      console.error("Failed to send invite:", err);
      showSettingsToast("error", "Failed to send invite");
    }
  }

  async function revokeInvite(inviteId) {
    if (!inviteId) return;
    if (!confirm("Revoke this invite?")) return;

    try {
      await window.dataBinder.fetchData(
        `/v1/organisations/invites/${inviteId}`,
        {
          method: "DELETE",
        }
      );
      showSettingsToast("success", "Invite revoked");
      await loadOrganisationInvites();
    } catch (err) {
      console.error("Failed to revoke invite:", err);
      showSettingsToast("error", "Failed to revoke invite");
    }
  }

  async function loadPlansAndUsage() {
    const currentPlanName = document.getElementById("planCurrentName");
    const currentPlanLimit = document.getElementById("planCurrentLimit");
    const currentPlanUsage = document.getElementById("planCurrentUsage");
    const currentPlanReset = document.getElementById("planCurrentReset");
    const planList = document.getElementById("planCards");
    const planTemplate = document.getElementById("planCardTemplate");

    try {
      const [usageResponse, plansResponse] = await Promise.all([
        window.dataBinder.fetchData("/v1/usage"),
        window.dataBinder.fetchData("/v1/plans"),
      ]);

      const usage = usageResponse.usage || {};
      const plans = plansResponse.plans || [];

      if (currentPlanName) {
        currentPlanName.textContent = usage.plan_display_name || "Free";
      }
      if (currentPlanLimit) {
        currentPlanLimit.textContent = usage.daily_limit
          ? `${usage.daily_limit.toLocaleString()} pages/day`
          : "No limit";
      }
      if (currentPlanUsage) {
        const dailyUsed = Number.isFinite(usage.daily_used)
          ? usage.daily_used
          : 0;
        currentPlanUsage.textContent = usage.daily_limit
          ? `${dailyUsed.toLocaleString()} used today`
          : "No usage data";
      }
      if (currentPlanReset) {
        currentPlanReset.textContent = usage.resets_at
          ? window.BBQuota?.formatTimeUntilReset(usage.resets_at) || ""
          : "";
      }

      if (planList && planTemplate) {
        planList.innerHTML = "";
        plans.forEach((plan) => {
          const clone = planTemplate.content.cloneNode(true);
          const card = clone.querySelector(".settings-plan-card");
          const nameEl = clone.querySelector(".settings-plan-name");
          const priceEl = clone.querySelector(".settings-plan-price");
          const limitEl = clone.querySelector(".settings-plan-limit");
          const actionBtn = clone.querySelector(".settings-plan-action");

          if (card) {
            if (plan.id === usage.plan_id) {
              card.classList.add("current");
            }
          }
          if (nameEl) nameEl.textContent = plan.display_name;
          if (priceEl) {
            priceEl.textContent =
              plan.monthly_price_cents > 0
                ? `$${(plan.monthly_price_cents / 100).toFixed(0)}/month`
                : "Free";
          }
          if (limitEl) {
            limitEl.textContent = Number.isFinite(plan.daily_page_limit)
              ? `${plan.daily_page_limit.toLocaleString()} pages/day`
              : "No limit";
          }
          if (actionBtn) {
            actionBtn.dataset.planId = plan.id;
            if (plan.id === usage.plan_id) {
              actionBtn.textContent = "Current plan";
              actionBtn.disabled = true;
            } else if (settingsState.currentUserRole !== "admin") {
              actionBtn.textContent = "Admin only";
              actionBtn.disabled = true;
            } else {
              actionBtn.textContent = "Switch plan";
              actionBtn.disabled = false;
              actionBtn.addEventListener("click", () => switchPlan(plan.id));
            }
          }

          planList.appendChild(clone);
        });
      }
    } catch (err) {
      console.error("Failed to load plans:", err);
      showSettingsToast("error", "Failed to load plan details");
    }
  }

  async function switchPlan(planId) {
    if (!planId) return;
    if (!confirm("Switch to this plan?")) return;

    try {
      await window.dataBinder.fetchData("/v1/organisations/plan", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ plan_id: planId }),
      });
      showSettingsToast("success", "Plan updated");
      loadPlansAndUsage();
      window.BBQuota?.refresh();
    } catch (err) {
      console.error("Failed to switch plan:", err);
      showSettingsToast("error", "Failed to switch plan");
    }
  }

  async function loadUsageHistory() {
    const list = document.getElementById("usageHistoryList");
    if (!list) return;

    list.textContent = "";
    try {
      const response = await window.dataBinder.fetchData(
        "/v1/usage/history?days=30"
      );
      const entries = response.usage || [];

      if (entries.length === 0) {
        const empty = document.createElement("div");
        empty.className = "settings-muted";
        empty.textContent = "No usage history yet.";
        list.appendChild(empty);
        return;
      }

      entries.forEach((entry) => {
        const row = document.createElement("div");
        row.className = "settings-usage-row";
        const dateSpan = document.createElement("span");
        dateSpan.textContent = entry.usage_date;
        const pagesSpan = document.createElement("span");
        const pagesProcessed = Number.isFinite(entry.pages_processed)
          ? entry.pages_processed
          : 0;
        pagesSpan.textContent = `${pagesProcessed.toLocaleString()} pages`;
        row.appendChild(dateSpan);
        row.appendChild(pagesSpan);
        list.appendChild(row);
      });
    } catch (err) {
      console.error("Failed to load usage history:", err);
      const error = document.createElement("div");
      error.className = "settings-muted";
      error.textContent = "Failed to load usage history.";
      list.appendChild(error);
    }
  }

  function formatNextRunTime(timestamp) {
    if (!timestamp) return "Not scheduled";
    const nextRun = new Date(timestamp);
    if (Number.isNaN(nextRun.getTime())) return "Not scheduled";
    const now = new Date();
    const diffMs = nextRun - now;
    const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));
    const diffHours = Math.floor(
      (diffMs % (1000 * 60 * 60 * 24)) / (1000 * 60 * 60)
    );
    const diffMins = Math.floor((diffMs % (1000 * 60 * 60)) / (1000 * 60));

    if (diffMs < 0) return "Overdue";
    if (diffDays > 0) return `In ${diffDays}d ${diffHours}h`;
    if (diffHours > 0) return `In ${diffHours}h ${diffMins}m`;
    return `In ${diffMins}m`;
  }

  async function loadSettingsSchedules() {
    const schedulesList = document.getElementById("settingsSchedulesList");
    const emptyState = document.getElementById("settingsSchedulesEmpty");
    if (!schedulesList) return;

    const template = schedulesList.querySelector(
      '[data-settings-template="schedule"]'
    );
    if (!template) return;

    try {
      const schedules = await window.dataBinder.fetchData("/v1/schedulers", {
        method: "GET",
      });

      const existing = schedulesList.querySelectorAll(
        '.bb-job-card:not([data-settings-template="schedule"])'
      );
      existing.forEach((node) => {
        node.remove();
      });

      if (!schedules || schedules.length === 0) {
        if (emptyState) emptyState.style.display = "block";
        return;
      }
      if (emptyState) emptyState.style.display = "none";

      schedules.forEach((schedule) => {
        const clone = template.cloneNode(true);
        clone.style.display = "block";
        clone.removeAttribute("data-settings-template");

        const domainEl = clone.querySelector(".bb-job-domain");
        if (domainEl) domainEl.textContent = schedule.domain;

        const scheduleInfo = clone.querySelector(".bb-schedule-info");
        if (scheduleInfo) {
          scheduleInfo.textContent = "";
          const hoursSpan = document.createElement("span");
          const intervalHours = schedule.schedule_interval_hours ?? "—";
          hoursSpan.textContent = `${intervalHours} hours`;
          const statusSpan = document.createElement("span");
          statusSpan.className = `bb-schedule-status bb-schedule-${schedule.is_enabled ? "enabled" : "disabled"}`;
          statusSpan.textContent = schedule.is_enabled ? "Enabled" : "Disabled";
          scheduleInfo.appendChild(hoursSpan);
          scheduleInfo.appendChild(statusSpan);
        }

        const nextRunContainer = clone.querySelector(".bb-job-footer > div");
        if (nextRunContainer) {
          nextRunContainer.textContent = "";
          const label = document.createElement("span");
          label.style.fontWeight = "500";
          label.textContent = "Next run: ";
          const value = document.createElement("span");
          value.textContent = formatNextRunTime(schedule.next_run_at);
          nextRunContainer.appendChild(label);
          nextRunContainer.appendChild(value);
        }

        const toggleBtn = clone.querySelector(
          '[data-schedule-action="toggle"]'
        );
        const deleteBtn = clone.querySelector(
          '[data-schedule-action="delete"]'
        );
        const viewBtn = clone.querySelector(
          '[data-schedule-action="view-jobs"]'
        );
        if (toggleBtn) {
          toggleBtn.dataset.schedulerId = schedule.id;
          toggleBtn.textContent = schedule.is_enabled ? "Disable" : "Enable";
        }
        if (deleteBtn) deleteBtn.dataset.schedulerId = schedule.id;
        if (viewBtn) viewBtn.dataset.schedulerId = schedule.id;

        schedulesList.appendChild(clone);
      });
    } catch (err) {
      console.error("Failed to load schedules:", err);
      showSettingsToast("error", "Failed to load schedules");
    }
  }

  async function toggleSettingsSchedule(schedulerId) {
    try {
      const scheduler = await window.dataBinder.fetchData(
        `/v1/schedulers/${encodeURIComponent(schedulerId)}`,
        { method: "GET" }
      );
      const updated = await window.dataBinder.fetchData(
        `/v1/schedulers/${encodeURIComponent(schedulerId)}`,
        {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            is_enabled: !scheduler.is_enabled,
            expected_is_enabled: scheduler.is_enabled,
          }),
        }
      );
      showSettingsToast(
        "success",
        `Schedule ${updated.is_enabled ? "enabled" : "disabled"}`
      );
      await loadSettingsSchedules();
    } catch (err) {
      console.error("Failed to toggle schedule:", err);
      showSettingsToast("error", "Failed to toggle schedule");
    }
  }

  async function deleteSettingsSchedule(schedulerId) {
    if (!confirm("Are you sure you want to delete this schedule?")) return;

    try {
      await window.dataBinder.fetchData(
        `/v1/schedulers/${encodeURIComponent(schedulerId)}`,
        { method: "DELETE" }
      );
      showSettingsToast("success", "Schedule deleted");
      await loadSettingsSchedules();
    } catch (err) {
      console.error("Failed to delete schedule:", err);
      showSettingsToast("error", "Failed to delete schedule");
    }
  }

  function viewSettingsScheduleJobs(schedulerId) {
    window.location.href = `/jobs?scheduler_id=${encodeURIComponent(schedulerId)}`;
  }

  function setupSchedulesActions() {
    const refreshBtn = document.getElementById("autoCrawlSchedulesRefresh");
    if (refreshBtn) {
      refreshBtn.addEventListener("click", () => {
        loadSettingsSchedules();
      });
    }

    const schedulesList = document.getElementById("settingsSchedulesList");
    if (!schedulesList) return;
    schedulesList.addEventListener("click", (event) => {
      const actionEl = event.target.closest("[data-schedule-action]");
      if (!actionEl) return;

      const schedulerId = actionEl.dataset.schedulerId;
      if (!schedulerId) return;

      const action = actionEl.dataset.scheduleAction;
      if (action === "toggle") {
        toggleSettingsSchedule(schedulerId);
      } else if (action === "delete") {
        deleteSettingsSchedule(schedulerId);
      } else if (action === "view-jobs") {
        viewSettingsScheduleJobs(schedulerId);
      }
    });
  }

  window.loadSettingsSchedules = loadSettingsSchedules;

  function updateAdminVisibility() {
    document.querySelectorAll("[data-admin-only]").forEach((el) => {
      if (settingsState.currentUserRole === "admin") {
        el.style.display = "";
      } else {
        el.style.display = "none";
      }
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
    if (window.__ES_SETTINGS && window.__esRefreshSections) {
      // ES modules handle migrated sections.
      await window.__esRefreshSections();
    } else {
      await loadOrganisationMembers();
      await loadOrganisationInvites();
      await loadPlansAndUsage();
      await loadUsageHistory();
      await loadSettingsSchedules();
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
        list.innerHTML =
          '<div class="bb-notifications-empty"><div>Please sign in</div></div>';
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
      list.innerHTML =
        '<div class="bb-notifications-empty"><div>Failed to load</div></div>';
    }
  }

  function renderNotifications(notifications) {
    const list = document.getElementById("notificationsList");
    if (!list) return;

    if (!notifications || notifications.length === 0) {
      list.innerHTML = `
        <div class="bb-notifications-empty">
          <div class="bb-notifications-empty-icon">🔔</div>
          <div>No notifications yet</div>
        </div>
      `;
      return;
    }

    const typeIcons = {
      job_completed: "✅",
      job_failed: "❌",
      job_started: "🚀",
      system: "ℹ️",
    };

    // Use DOM methods instead of innerHTML for XSS protection
    list.textContent = "";
    notifications.forEach((n) => {
      const isUnread = !n.read_at;
      const icon = typeIcons[n.type] || "📬";
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
    if (window.BB_APP?.initialiseOrg && !window.BB_ACTIVE_ORG?.name) {
      try {
        await window.BB_APP.initialiseOrg();
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
        await window.BB_APP.switchOrg(org.id);
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
      if (window.BB_APP?.coreReady) {
        await window.BB_APP.coreReady;
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

      // When __ES_SETTINGS is set, ES modules handle these sections.
      const esActive = !!window.__ES_SETTINGS;
      if (!esActive) {
        setupSchedulesActions();
      }

      const sessionResult = await window.supabase.auth.getSession();
      const session = sessionResult?.data?.session;
      if (session?.user) {
        // Initialise org using shared logic (single source of truth)
        if (window.BB_APP?.initialiseOrg) {
          try {
            await window.BB_APP.initialiseOrg();
          } catch (err) {
            console.warn("Failed to initialise org:", err);
          }
        }
        await initOrgSwitcher();
        initCreateOrgModal();
        initAdminSection(session);

        if (!esActive) {
          await loadAccountDetails();
          await refreshSettingsData();
        }
        await handleInviteToken();
      } else if (window.BBInviteFlow?.getInviteToken?.()) {
        await handleInviteToken();
      }

      if (!esActive) {
        const inviteForm = document.getElementById("teamInviteForm");
        if (inviteForm) {
          inviteForm.addEventListener("submit", sendInvite);
        }

        const resetBtn = document.getElementById("settingsResetPassword");
        if (resetBtn) {
          resetBtn.addEventListener("click", sendPasswordReset);
        }
        const saveNameBtn = document.getElementById("settingsSaveName");
        if (saveNameBtn) {
          saveNameBtn.addEventListener("click", saveProfileName);
        }
      }

      // Signal that bb-settings.js init is complete (modules wait for this).
      window.__bbSettingsReady?.();

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
