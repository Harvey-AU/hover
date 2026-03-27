(function () {
  let authListenerAttached = false;
  let authSubscription = null;
  const SESSION_RETRY_ATTEMPTS = 12;
  const SESSION_RETRY_DELAY_MS = 150;

  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const getSupabaseAuth = () => window.supabase?.auth || null;

  function getInviteToken(paramName = "invite_token") {
    return new URLSearchParams(window.location.search).get(paramName);
  }

  function clearInviteTokenFromURL(paramName = "invite_token") {
    const params = new URLSearchParams(window.location.search);
    if (!params.has(paramName)) return;

    params.delete(paramName);
    const url = new URL(window.location.href);
    url.search = params.toString();
    window.history.replaceState({}, "", url.toString());
  }

  async function fetchInvitePreview(token) {
    const response = await fetch(
      `/v1/organisations/invites/preview?token=${encodeURIComponent(token)}`,
      {
        method: "GET",
      }
    );

    const payload = await response.json().catch(() => ({}));
    if (!response.ok) {
      const message = payload?.message || "Failed to load invite details";
      throw new Error(message);
    }

    return payload?.data?.invite || null;
  }

  async function acceptInvite(token) {
    if (window.dataBinder?.fetchData) {
      return window.dataBinder.fetchData("/v1/organisations/invites/accept", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token }),
      });
    }

    const auth = getSupabaseAuth();
    if (!auth) {
      throw new Error("Authentication is unavailable");
    }
    const sessionResult = await auth.getSession();
    const session = sessionResult?.data?.session;
    if (!session?.access_token) {
      throw new Error("Authentication is required");
    }

    const response = await fetch("/v1/organisations/invites/accept", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${session.access_token}`,
      },
      body: JSON.stringify({ token }),
    });

    const payload = await response.json().catch(() => ({}));
    if (!response.ok) {
      const message = payload?.message || "Failed to accept invite";
      throw new Error(message);
    }

    return payload?.data ?? null;
  }

  async function getSessionWithRetry() {
    const auth = getSupabaseAuth();
    if (!auth) {
      return null;
    }

    let session = null;
    for (let attempt = 0; attempt < SESSION_RETRY_ATTEMPTS; attempt += 1) {
      const sessionResult = await auth.getSession();
      session = sessionResult?.data?.session || null;
      if (session?.user) {
        return session;
      }
      await sleep(SESSION_RETRY_DELAY_MS);
    }
    return session;
  }

  function ensureAuthModalAndReloadOnSignIn() {
    if (typeof window.showAuthModal === "function") {
      window.showAuthModal();
    }

    const auth = getSupabaseAuth();
    if (authListenerAttached || !auth) {
      return;
    }

    authListenerAttached = true;
    const authStateChangeResult = auth.onAuthStateChange((event) => {
      if (event === "SIGNED_IN") {
        authSubscription?.unsubscribe?.();
        authSubscription = null;
        authListenerAttached = false;
        window.location.reload();
      }
    });
    authSubscription = authStateChangeResult?.data?.subscription || null;
    if (!authSubscription?.unsubscribe && authStateChangeResult?.unsubscribe) {
      authSubscription = authStateChangeResult;
    }
    if (!authSubscription?.unsubscribe) {
      authListenerAttached = false;
    }
  }

  async function handleInviteTokenFlow(options = {}) {
    const {
      tokenParamName = "invite_token",
      clearTokenOnSuccess = true,
      redirectTo = "",
      onAuthRequired,
      onAccepted,
      onError,
    } = options;

    const token = getInviteToken(tokenParamName);
    if (!token) {
      return { status: "no_token" };
    }

    if (typeof window.BBAuth?.handleAuthCallback === "function") {
      try {
        await window.BBAuth.handleAuthCallback();
      } catch (error) {
        console.warn("Invite flow auth callback processing failed:", error);
      }
    }

    const session = await getSessionWithRetry();
    if (!session?.user) {
      ensureAuthModalAndReloadOnSignIn();
      if (typeof onAuthRequired === "function") {
        onAuthRequired(token);
      }
      return { status: "auth_required", token };
    }

    try {
      const result = await acceptInvite(token);

      if (clearTokenOnSuccess) {
        clearInviteTokenFromURL(tokenParamName);
      }
      if (typeof window.BBAuth?.clearPendingInviteToken === "function") {
        window.BBAuth.clearPendingInviteToken();
      }

      if (typeof onAccepted === "function") {
        await onAccepted(result);
      }

      if (redirectTo) {
        window.location.assign(redirectTo);
      }

      return { status: "accepted", token, result };
    } catch (error) {
      if (typeof onError === "function") {
        onError(error);
      }
      return { status: "error", token, error };
    }
  }

  window.BBInviteFlow = {
    getInviteToken,
    clearInviteTokenFromURL,
    fetchInvitePreview,
    handleInviteTokenFlow,
  };
})();
