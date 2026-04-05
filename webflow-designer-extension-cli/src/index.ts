declare const supabase: {
  createClient: (
    url: string,
    key: string,
    options?: Record<string, unknown>
  ) => SupabaseClient;
};

type SupabaseClient = {
  auth: {
    setSession: (params: {
      access_token: string;
      refresh_token: string;
    }) => Promise<unknown>;
  };
  channel: (name: string) => RealtimeChannel;
  removeChannel: (channel: RealtimeChannel) => Promise<unknown>;
};

type RealtimeChannel = {
  on: (
    event: string,
    filter: Record<string, string>,
    callback: (payload: unknown) => void
  ) => RealtimeChannel;
  subscribe: (
    callback?: (status: string, err?: Error) => void
  ) => RealtimeChannel;
};

const API_BASE_STORAGE_KEY = "gnh_extension_api_base";
const API_TOKEN_STORAGE_KEY = "gnh_extension_api_token_session";
const AUTH_POPUP_WIDTH = 520;
const AUTH_POPUP_HEIGHT = 760;
const DEFAULT_GNH_APP_ORIGIN = "https://hover.app.goodnative.co";
const LEGACY_EXTENSION_APP_ORIGINS = new Set(["https://hover-pr-255.fly.dev"]);
const AUTH_POPUP_NAME = "bbbExtensionAuth";
const SCHEDULE_PLACEHOLDER = "off";
const SCHEDULE_OPTIONS = ["off", "6", "12", "24", "48"] as const;
const JOB_POLLING_INTERVAL_MS = 6000;
const FALLBACK_POLLING_INTERVAL_MS = 1000;

const APP_ROUTES = {
  dashboard: "/dashboard",
  viewJob: "/jobs",
  changePlan: "/settings/plans",
  manageTeam: "/settings/team",
} as const;
const UNAUTHENTICATED_EXTENSION_SIZE = { width: 240, height: 407 } as const;
const AUTHENTICATED_EXTENSION_SIZE = { width: 450, height: 500 } as const;
type ExtensionPanelSize =
  | "default"
  | "compact"
  | "comfortable"
  | "large"
  | { width: number; height: number };
const ACTIVE_JOB_STATUSES = new Set<string>([
  "pending",
  "queued",
  "initializing",
  "running",
  "in_progress",
  "processing",
]);

declare const webflow: {
  getSiteInfo: () => Promise<{
    siteId: string;
    siteName: string;
    shortName: string;
    isPasswordProtected: boolean;
    isPrivateStaging: boolean;
    workspaceId: string;
    workspaceSlug: string;
    domains: Array<{
      url: string;
      lastPublished: string | null;
      default: boolean;
      stage: "staging" | "production";
    }>;
  }>;
  setExtensionSize: (size: ExtensionPanelSize) => Promise<null>;
};

// Shared modules exposed by lib/bridge.js via window.HoverLib
declare const HoverLib: {
  api: {
    configure: (config: {
      baseUrl?: string;
      tokenProvider?: () => Promise<string | null>;
    }) => void;
    get: (path: string, init?: RequestInit) => Promise<unknown>;
    post: (
      path: string,
      body?: unknown,
      init?: RequestInit
    ) => Promise<unknown>;
    patch: (
      path: string,
      body?: unknown,
      init?: RequestInit
    ) => Promise<unknown>;
    put: (path: string, body?: unknown, init?: RequestInit) => Promise<unknown>;
    del: (path: string, init?: RequestInit) => Promise<unknown>;
    request: (path: string, init?: RequestInit) => Promise<unknown>;
  };
  fmt: {
    formatDate: (value: string | null | undefined) => string;
    formatDateTime: (value: string | null | undefined) => string;
    formatRelativeTime: (value: string | null | undefined) => string;
    formatDuration: (ms: number | null | undefined) => string;
    formatCount: (value: number | null | undefined) => string;
    formatPercent: (
      value: number | null | undefined,
      decimals?: number
    ) => string;
    formatStatus: (status: string) => string;
    statusCategory: (status: string) => string;
    formatUrl: (url: string | null | undefined) => string;
    escapeCSVValue: (value: unknown) => string;
    sanitiseForFilename: (value: string) => string;
    triggerFileDownload: (
      content: string,
      mimeType: string,
      filename: string
    ) => void;
    getInitials: (value: string) => string;
  };
  http: {
    fetchWithTimeout: (
      url: string,
      options?: RequestInit,
      context?: string
    ) => Promise<Response>;
  };
  jobs: {
    fetchJobs: (options?: {
      limit?: number;
      range?: string;
      include?: string;
    }) => Promise<unknown[]>;
    normaliseDomain: (input: string) => string;
    filterJobsByDomains: (
      jobs: unknown[],
      options?: { siteDomain?: string | null; siteDomainCandidates?: string[] }
    ) => unknown[];
    pickLatestJobByDomains: (
      jobs: unknown[],
      options?: { siteDomain?: string | null; siteDomainCandidates?: string[] }
    ) => unknown | null;
    buildCompletedJobsSignature: (
      jobs: unknown[],
      options?: { siteDomain?: string | null; siteDomainCandidates?: string[] },
      isActiveJobStatus?: (status: string) => boolean
    ) => string;
    buildChartJobsSignature: (
      jobs: unknown[],
      options?: { siteDomain?: string | null; siteDomainCandidates?: string[] }
    ) => string;
    subscribeToJobUpdates: (options: {
      orgId: string;
      onUpdate: () => void;
      supabaseClient?: SupabaseClient | null;
      channelName?: string;
      getFallbackInterval?: () => number;
      onSubscriptionIssue?: (status?: string, err?: Error) => void;
    }) => () => void;
  };
};

type ScheduleOption = (typeof SCHEDULE_OPTIONS)[number] | "";

type ApiError = {
  status: number;
  message: string;
  body?: string;
};

type Organisation = {
  id: string;
  name: string;
};

type OrganisationsResponse = {
  organisations: Organisation[];
  active_organisation_id?: string;
};

type UsageStats = {
  daily_limit: number;
  daily_used: number;
  daily_remaining: number;
  usage_percentage: number;
  plan_name: string;
  plan_display_name: string;
};

type UsageResponse = {
  usage: UsageStats;
};

type JobItem = {
  id: string;
  status: string;
  total_tasks: number;
  completed_tasks: number;
  failed_tasks: number;
  skipped_tasks: number;
  progress: number;
  created_at: string;
  started_at?: string;
  completed_at?: string;
  duration_seconds?: number | null;
  avg_time_per_task_seconds?: number | null;
  domain?: string;
  stats?: {
    total_broken_links?: number;
    slow_page_buckets?: {
      over_10s?: number;
      "5_to_10s"?: number;
      "3_to_5s"?: number;
    };
    cache_warming_effect?: {
      total_time_saved_ms?: number;
      total_time_saved_seconds?: number;
    };
  };
  domains?: {
    name: string;
  };
};

type ExportColumn = {
  key: string;
  label: string;
};

type JobExportPayload = {
  job_id: string;
  domain?: string;
  export_time?: string;
  completed_at?: string | null;
  export_type?: string;
  columns?: ExportColumn[];
  tasks?: Record<string, unknown>[];
};

type Scheduler = {
  id: string;
  domain: string;
  schedule_interval_hours: number;
  is_enabled: boolean;
};

type CreateJobRequest = {
  domain: string;
  source_type: string;
  source_detail: string;
};

type WebflowConnection = {
  id: string;
  webflow_workspace_id?: string;
  workspace_name?: string;
};

type WebflowSiteSetting = {
  webflow_site_id: string;
  site_name: string;
  primary_domain: string;
  connection_id?: string;
  auto_publish_enabled: boolean;
  schedule_interval_hours?: number;
  scheduler_id?: string;
};

type WebflowSitesResponse = {
  sites: WebflowSiteSetting[];
  pagination?: {
    has_next: boolean;
  };
};

type AuthMessage = {
  source?: string;
  type?: string;
  state?: string;
  extensionState?: string;
  accessToken?: string;
  user?: {
    id?: string;
    email?: string;
    avatarUrl?: string;
  };
};

type ErrorPayload = {
  code?: string;
  message?: string;
};

function extractErrorMessage(rawBody?: string): string {
  if (!rawBody) {
    return "";
  }

  try {
    const parsed = JSON.parse(rawBody) as ErrorPayload;
    if (parsed?.message) {
      return parsed.message;
    }
  } catch (_error) {
    // ignore parse failures
  }

  return rawBody;
}

// ---------------------------------------------------------------------------
// Avatar helpers
// ---------------------------------------------------------------------------

async function getGravatarUrl(email: string, size = 80): Promise<string> {
  const normalised = (email || "").trim().toLowerCase();
  if (!normalised || !globalThis.crypto?.subtle) return "";
  try {
    const data = new TextEncoder().encode(normalised);
    const digest = await globalThis.crypto.subtle.digest("SHA-256", data);
    const hash = [...new Uint8Array(digest)]
      .map((b) => b.toString(16).padStart(2, "0"))
      .join("");
    const params = new URLSearchParams({ s: String(size), d: "404" });
    return `https://www.gravatar.com/avatar/${hash}?${params.toString()}`;
  } catch {
    return "";
  }
}

async function renderAvatar(
  target: HTMLElement,
  email: string,
  initials: string
): Promise<void> {
  const existingImg = target.querySelector("img");
  if (existingImg) existingImg.remove();

  target.textContent = initials;

  const url = await getGravatarUrl(email, 80);
  if (!url) return;

  const img = document.createElement("img");
  img.src = url;
  img.alt = "User avatar";
  img.loading = "lazy";
  img.decoding = "async";
  img.addEventListener(
    "load",
    () => {
      target.textContent = "";
      target.appendChild(img);
    },
    { once: true }
  );
  img.addEventListener(
    "error",
    () => {
      if (img.parentNode) img.parentNode.removeChild(img);
      target.textContent = initials;
    },
    { once: true }
  );
}

async function updateAvatarFromState(): Promise<void> {
  const avatarEl = document.querySelector<HTMLElement>(
    ".topbar-profile-avatar"
  );
  if (!avatarEl) return;

  const displayName = state.userDisplayName || state.userEmail || "";
  const initials = displayName ? HoverLib.fmt.getInitials(displayName) : "?";

  // Use the OAuth avatar_url from the auth postMessage if available,
  // otherwise fall back to Gravatar via the shared renderAvatar helper.
  if (state.userAvatarUrl) {
    const existingImg = avatarEl.querySelector("img");
    if (existingImg) existingImg.remove();
    avatarEl.textContent = initials;

    const img = document.createElement("img");
    img.src = state.userAvatarUrl;
    img.alt = "User avatar";
    img.loading = "lazy";
    img.decoding = "async";
    img.addEventListener(
      "load",
      () => {
        avatarEl.textContent = "";
        avatarEl.appendChild(img);
      },
      { once: true }
    );
    img.addEventListener(
      "error",
      () => {
        if (img.parentNode) img.parentNode.removeChild(img);
        avatarEl.textContent = initials;
      },
      { once: true }
    );
    return;
  }

  await renderAvatar(avatarEl, state.userEmail ?? "", initials);
}

const ui = {
  // Status messages
  statusBlock: document.querySelector(".status-block"),
  statusText: document.getElementById("statusText"),
  detailText: document.getElementById("detailText"),

  // Auth states
  unauthState: document.getElementById("unauthState"),
  authState: document.getElementById("authState"),

  // Unauth buttons
  checkSiteButton: document.getElementById("checkSiteButton"),
  signInButton: document.getElementById("signInButton"),

  // Top bar
  orgSelect: document.getElementById("orgSelect") as HTMLSelectElement | null,
  planNameText: document.getElementById("planNameText"),
  planRemainingText: document.getElementById("planRemainingText"),
  planRemainingValue: document.getElementById("planRemainingValue"),
  settingsButton: document.getElementById("settingsButton"),

  // Action bar
  runNowButton: document.getElementById("runNowButton"),
  scheduleSelect: document.getElementById(
    "scheduleSelect"
  ) as HTMLSelectElement | null,
  webflowPublishToggle: document.getElementById(
    "runPublishToggle"
  ) as HTMLInputElement | null,

  // Job card
  jobSection: document.getElementById("jobSection"),
  noJobState: document.getElementById("noJobState"),
  checkSiteAuthButton: document.getElementById("checkSiteAuthButton"),

  // Recent results
  latestResultsList: document.getElementById("latestResultsList"),
  recentResultsList: document.getElementById("recentResultsList"),

  // Mini chart
  miniChart: document.getElementById("miniChart"),
  chartScaleLabels: Array.from(
    document.querySelectorAll("#chartSection .chart-y-scale span")
  ),

  // Footer
  feedbackButton: document.getElementById("feedbackButton"),
  helpButton: document.getElementById("helpButton"),
};

type ExtensionState = {
  apiBaseUrl: string;
  token: string | null;
  siteDomain: string | null;
  siteName: string | null;
  siteDomainCandidates: string[];
  pendingAuthAction?: () => Promise<void> | void;
  organisations: Organisation[];
  activeOrganisationId: string;
  currentJob: JobItem | null;
  usage: UsageStats | null;
  currentScheduler: Scheduler | null;
  webflowConnected: boolean;
  webflowAutoPublishEnabled: boolean;
  userEmail: string | null;
  userDisplayName: string | null;
  userAvatarUrl: string | null;
};

const state: ExtensionState = {
  apiBaseUrl: getStoredBaseUrl(),
  token: getStoredToken(),
  siteDomain: null,
  siteName: null,
  siteDomainCandidates: [],
  organisations: [],
  activeOrganisationId: "",
  currentJob: null,
  usage: null,
  currentScheduler: null,
  webflowConnected: false,
  webflowAutoPublishEnabled: false,
  userEmail: null,
  userDisplayName: null,
  userAvatarUrl: null,
};

let statusToastTimer: ReturnType<typeof setTimeout> | null = null;
let jobStatusPoller: number | null = null;
let jobPollInFlight = false;
let lastCompletedJobsSignature = "";
let lastChartJobsSignature = "";

// Supabase realtime state
let supabaseClient: SupabaseClient | null = null;
let isRealtimeRefreshing = false;
let jobsSubscriptionCleanup: (() => void) | null = null;

function getStoredBaseUrl(): string {
  const storedBaseUrl = localStorage.getItem(API_BASE_STORAGE_KEY);
  if (!storedBaseUrl) {
    return DEFAULT_GNH_APP_ORIGIN;
  }

  if (LEGACY_EXTENSION_APP_ORIGINS.has(storedBaseUrl)) {
    return DEFAULT_GNH_APP_ORIGIN;
  }

  return storedBaseUrl;
}

function getStoredToken(): string | null {
  return sessionStorage.getItem(API_TOKEN_STORAGE_KEY);
}

function setStoredToken(token: string | null): void {
  if (token) {
    sessionStorage.setItem(API_TOKEN_STORAGE_KEY, token);
  } else {
    sessionStorage.removeItem(API_TOKEN_STORAGE_KEY);
  }
  state.token = token;
}

type SupabaseConfig = {
  supabaseUrl: string;
  supabaseAnonKey: string;
};

async function fetchSupabaseConfig(): Promise<SupabaseConfig | null> {
  try {
    const response = await fetch(`${state.apiBaseUrl}/config.js`);
    if (!response.ok) {
      console.warn("Failed to fetch Supabase config:", response.status);
      return null;
    }

    const scriptText = await response.text();
    // config.js sets window.GNH_CONFIG = { supabaseUrl, supabaseAnonKey, ... }
    // Parse the JSON object from the assignment.
    const match = scriptText.match(/window\.GNH_CONFIG\s*=\s*(\{[\s\S]*?\});/);
    if (!match?.[1]) {
      console.warn("Could not parse GNH_CONFIG from config.js");
      return null;
    }

    const config = JSON.parse(match[1]) as Record<string, string>;
    if (!config.supabaseUrl || !config.supabaseAnonKey) {
      console.warn("Supabase config missing url or anon key");
      return null;
    }

    return {
      supabaseUrl: config.supabaseUrl,
      supabaseAnonKey: config.supabaseAnonKey,
    };
  } catch (error) {
    console.warn("Error fetching Supabase config:", error);
    return null;
  }
}

async function initSupabaseClient(): Promise<SupabaseClient | null> {
  if (supabaseClient) {
    return supabaseClient;
  }

  if (!state.token) {
    return null;
  }

  const config = await fetchSupabaseConfig();
  if (!config) {
    return null;
  }

  if (typeof supabase === "undefined" || !supabase?.createClient) {
    console.warn("Supabase SDK not loaded — realtime unavailable");
    return null;
  }

  supabaseClient = supabase.createClient(
    config.supabaseUrl,
    config.supabaseAnonKey,
    {
      auth: { persistSession: false, autoRefreshToken: false },
    }
  );

  // Set the session using the JWT we already have from extension auth.
  // No refresh token available — the extension auth flow only returns the access token.
  await supabaseClient.auth.setSession({
    access_token: state.token,
    refresh_token: "",
  });

  return supabaseClient;
}

function asNode(element: Element | null): HTMLElement | null {
  return element instanceof HTMLElement ? element : null;
}

function asInput(element: Element | null): HTMLInputElement | null {
  return element instanceof HTMLInputElement ? element : null;
}

function asSelect(element: Element | null): HTMLSelectElement | null {
  return element instanceof HTMLSelectElement ? element : null;
}

function getSiteDomainCandidates(): string[] {
  const normalised = new Set(
    state.siteDomainCandidates
      .map((candidate) => normalizeDomain(candidate))
      .filter(Boolean)
  );
  if (state.siteDomain) {
    normalised.add(state.siteDomain);
  }
  return [...normalised];
}

function hide(el: HTMLElement | null): void {
  if (el) {
    el.classList.add("hidden");
  }
}

function show(el: HTMLElement | null): void {
  if (el) {
    el.classList.remove("hidden");
  }
}

function setText(node: Element | null, value: string): void {
  if (node) {
    node.textContent = value;
  }
}

function normalizeDomain(input: string): string {
  return HoverLib.jobs.normaliseDomain(input);
}

function normalizeJobStatus(status: string): string {
  return status.trim().toLowerCase();
}

function isActiveJobStatus(status: string): boolean {
  return ACTIVE_JOB_STATUSES.has(normalizeJobStatus(status));
}

function pickLatestJobForCurrentSite(
  jobs: JobItem[] | undefined
): JobItem | null {
  return (HoverLib.jobs.pickLatestJobByDomains(jobs || [], {
    siteDomain: state.siteDomain,
    siteDomainCandidates: state.siteDomainCandidates,
  }) || null) as JobItem | null;
}

function buildCompletedJobsSignature(jobs: JobItem[] | undefined): string {
  return HoverLib.jobs.buildCompletedJobsSignature(
    jobs || [],
    {
      siteDomain: state.siteDomain,
      siteDomainCandidates: state.siteDomainCandidates,
    },
    isActiveJobStatus
  );
}

function buildChartJobsSignature(jobs: JobItem[] | undefined): string {
  return HoverLib.jobs.buildChartJobsSignature(jobs || [], {
    siteDomain: state.siteDomain,
    siteDomainCandidates: state.siteDomainCandidates,
  });
}

function stopJobStatusPolling(): void {
  if (jobStatusPoller !== null) {
    window.clearInterval(jobStatusPoller);
    jobStatusPoller = null;
  }
}

function startJobStatusPolling(): void {
  // When realtime is active, the shared subscription also owns fallback polling.
  // Only start the legacy 6 s poller if we have no shared subscription.
  if (jobsSubscriptionCleanup) {
    return;
  }

  stopJobStatusPolling();

  if (!state.token || !state.currentJob || !state.siteDomain) {
    return;
  }

  if (!isActiveJobStatus(state.currentJob.status)) {
    return;
  }

  jobStatusPoller = window.setInterval(() => {
    void refreshCurrentJob();
  }, JOB_POLLING_INTERVAL_MS);
}

// ---------------------------------------------------------------------------
// Realtime: throttled refresh, fallback polling, subscription, cleanup
// ---------------------------------------------------------------------------

async function realtimeRefresh(): Promise<void> {
  if (isRealtimeRefreshing) return;
  isRealtimeRefreshing = true;

  try {
    // Refresh both job state and usage stats, matching the dashboard pattern.
    await Promise.all([refreshCurrentJob(), refreshUsage()]);
  } finally {
    isRealtimeRefreshing = false;
  }
}

async function refreshUsage(): Promise<void> {
  if (!state.token) return;

  try {
    const usageData = (await HoverLib.api.get("/v1/usage")) as UsageResponse;
    state.usage = usageData.usage || null;
    renderUsage(state.usage);
  } catch (error) {
    // Non-critical — keep existing usage displayed.
    console.warn("Failed to refresh usage stats:", error);
  }
}

function cleanupRealtimeSubscription(): void {
  if (jobsSubscriptionCleanup) {
    jobsSubscriptionCleanup();
    jobsSubscriptionCleanup = null;
  }
}

function subscribeToJobUpdates(): void {
  const orgId = state.activeOrganisationId;
  if (!orgId || !supabaseClient) {
    return;
  }

  cleanupRealtimeSubscription();
  jobsSubscriptionCleanup = HoverLib.jobs.subscribeToJobUpdates({
    orgId,
    onUpdate: () => {
      void realtimeRefresh();
    },
    supabaseClient,
    channelName: `jobs-changes:${orgId}`,
    getFallbackInterval: () => FALLBACK_POLLING_INTERVAL_MS,
    onSubscriptionIssue: (status, err) => {
      if (
        status === "CHANNEL_ERROR" ||
        status === "TIMED_OUT" ||
        status === "SUBSCRIBE_FAILED" ||
        status === "MAX_RETRIES" ||
        err
      ) {
        console.warn(
          "[Realtime] Connection issue, fallback polling will continue",
          status,
          err
        );
      }
    },
  });
}

// ---------------------------------------------------------------------------

async function refreshCurrentJob(): Promise<void> {
  if (jobPollInFlight || !state.token || !state.siteDomain) {
    stopJobStatusPolling();
    return;
  }

  try {
    jobPollInFlight = true;
    const jobs = (await HoverLib.jobs.fetchJobs({
      limit: 50,
      include: "stats",
    })) as JobItem[];
    const latest = pickLatestJobForCurrentSite(jobs);
    state.currentJob = latest;
    renderJobState(state.currentJob);

    const completedSignature = buildCompletedJobsSignature(jobs);
    if (completedSignature !== lastCompletedJobsSignature) {
      renderRecentResults(jobs);
      lastCompletedJobsSignature = completedSignature;
    }

    const chartSignature = buildChartJobsSignature(jobs);
    if (chartSignature !== lastChartJobsSignature) {
      renderMiniChart(jobs);
      lastChartJobsSignature = chartSignature;
    }

    if (!isActiveJobStatus(state.currentJob?.status || "")) {
      stopJobStatusPolling();
    }
  } catch (error) {
    if (
      typeof error === "object" &&
      error !== null &&
      "status" in error &&
      ((error as ApiError).status === 401 || (error as ApiError).status === 403)
    ) {
      stopJobStatusPolling();
      handleAuthError(error);
      return;
    }
    console.error("Failed to refresh current job", error);
  } finally {
    jobPollInFlight = false;
  }
}

// Wire hover-job-card's issue-tab fetcher to the shared API client.
// window.HoverJobCard is set by hover-job-card.js (module script, loaded before index.ts).
function initHoverJobCard(): void {
  const hoverJobCard = (window as any).HoverJobCard;
  if (hoverJobCard?.setApiFetcher) {
    hoverJobCard.setApiFetcher((path: string) => HoverLib.api.get(path));
  }
}

function getPopupPosition() {
  const left =
    window.screenX + Math.max(0, (window.outerWidth - AUTH_POPUP_WIDTH) / 2);
  const top =
    window.screenY + Math.max(0, (window.outerHeight - AUTH_POPUP_HEIGHT) / 2);
  return { left: Math.floor(left), top: Math.floor(top) };
}

function createAuthStateValue(): string {
  if (window.crypto?.getRandomValues) {
    const bytes = new Uint8Array(16);
    window.crypto.getRandomValues(bytes);
    return `${Date.now()}-${Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("")}`;
  }

  return `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

async function connectAccount(): Promise<string | null> {
  const authBase = new URL(state.apiBaseUrl);
  const stateToken = createAuthStateValue();
  const authUrl = `${state.apiBaseUrl}/extension-auth.html?origin=${encodeURIComponent(window.location.origin)}&extension_state=${encodeURIComponent(stateToken)}&state=${encodeURIComponent(stateToken)}`;
  const popupPosition = getPopupPosition();
  const popupFeatures = `width=${AUTH_POPUP_WIDTH},height=${AUTH_POPUP_HEIGHT},left=${popupPosition.left},top=${popupPosition.top},resizable=yes,scrollbars=yes`;

  const popup = window.open(
    authUrl,
    AUTH_POPUP_NAME,
    popupFeatures
  ) as Window | null;

  if (!popup) {
    setStatus(
      "Popup blocked. Allow popups for Webflow Designer and try again.",
      "error"
    );
    return null;
  }

  try {
    const message = await new Promise<AuthMessage>((resolve, reject) => {
      let settled = false;
      let closedTimer: number | undefined;

      const onMessage = (event: MessageEvent) => {
        if (event.source !== popup) {
          return;
        }
        if (event.origin !== authBase.origin || event.source === null) {
          return;
        }

        const payload = event.data as AuthMessage;
        const payloadState = payload?.state || payload?.extensionState;

        if (
          payload?.source !== "gnh-extension-auth" ||
          payloadState !== stateToken
        ) {
          console.warn(
            "extension auth: ignoring popup message (state mismatch)",
            {
              expected: stateToken,
              received: payload?.state,
              type: payload?.type,
            }
          );
          return;
        }

        settled = true;
        cleanup();
        resolve(payload);
      };

      const cleanup = () => {
        window.removeEventListener("message", onMessage);
        if (closedTimer) {
          window.clearInterval(closedTimer);
        }
      };

      const onClose = () => {
        if (settled) {
          return;
        }

        settled = true;
        cleanup();
        reject(new Error("Auth window closed before sign-in completed"));
      };

      window.addEventListener("message", onMessage);
      closedTimer = window.setInterval(() => {
        if (popup.closed) {
          onClose();
        }
      }, 500);
    });

    if (message.type === "success" && message.accessToken) {
      setStoredToken(message.accessToken);
      if (message.user?.email) state.userEmail = message.user.email;
      if (message.user?.avatarUrl) state.userAvatarUrl = message.user.avatarUrl;
      setStatus("", "");
      return message.accessToken;
    }

    setStatus(message.type || "Auth failed", "error");
    return null;
  } finally {
    if (popup && !popup.closed) {
      popup.close();
    }
  }
}

async function ensureSignedIn(): Promise<boolean> {
  if (state.token) {
    return true;
  }

  const token = await connectAccount();
  return Boolean(token);
}

function setStatus(message: string, detail = "") {
  // Cancel any in-flight toast and reset opacity immediately.
  if (statusToastTimer !== null) {
    clearTimeout(statusToastTimer);
    statusToastTimer = null;
  }
  ui.statusBlock?.classList.remove("status-block--fading");

  setText(ui.statusText, message);
  setText(ui.detailText, detail);

  // Auto-dismiss non-empty toasts: fade at 3 s, clear at 3.5 s.
  if (message || detail) {
    statusToastTimer = setTimeout(() => {
      ui.statusBlock?.classList.add("status-block--fading");
      statusToastTimer = setTimeout(() => {
        setText(ui.statusText, "");
        setText(ui.detailText, "");
        ui.statusBlock?.classList.remove("status-block--fading");
        statusToastTimer = null;
      }, 500);
    }, 3000);
  }
}

async function setExtensionSizeForAuthState(isAuthed: boolean): Promise<void> {
  try {
    await webflow.setExtensionSize(
      isAuthed ? AUTHENTICATED_EXTENSION_SIZE : UNAUTHENTICATED_EXTENSION_SIZE
    );
  } catch (error) {
    console.warn("Unable to set extension size", error);
  }
}

function renderAuthState(isAuthed: boolean): void {
  if (isAuthed) {
    hide(asNode(ui.unauthState));
    show(asNode(ui.authState));
    void setExtensionSizeForAuthState(true);
    return;
  }

  show(asNode(ui.unauthState));
  hide(asNode(ui.authState));
  void setExtensionSizeForAuthState(false);
}

// ---------------------------------------------------------------------------
// Rendering helpers
// ---------------------------------------------------------------------------

/** Show the in-progress card only for active jobs; hide for completed/none. */
function renderJobState(job: JobItem | null): void {
  const section = asNode(ui.jobSection);
  if (!job || !isActiveJobStatus(job.status)) {
    stopJobStatusPolling();
    hide(section);
    return;
  }

  const hoverJobCard = (window as any).HoverJobCard;
  const card: HTMLElement = hoverJobCard
    ? hoverJobCard.createJobCard(job, { context: "extension" })
    : buildResultCardFallback(job, false);

  if (section) {
    section.replaceChildren(card);
    card.addEventListener("hover-job-card:view", (e: Event) =>
      openSettingsPage((e as CustomEvent).detail.path)
    );
    card.addEventListener("hover-job-card:export", (e: Event) => {
      void exportJob((e as CustomEvent).detail.jobId);
    });
    show(section);
  }
}

function asCount(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return 0;
  }
  return Math.max(0, Math.floor(value));
}

function getIssueCounts(job: JobItem): {
  brokenLinks: number;
  verySlow: number;
  slow: number;
} {
  const buckets = job.stats?.slow_page_buckets;
  const statsBrokenLinks = asCount(job.stats?.total_broken_links);
  const fallbackBrokenLinks = asCount(job.failed_tasks);

  if (job.stats && buckets) {
    const verySlow = asCount(buckets.over_10s) + asCount(buckets["5_to_10s"]);
    const slow = asCount(buckets["3_to_5s"]);
    return {
      brokenLinks: Math.max(statsBrokenLinks, fallbackBrokenLinks),
      verySlow,
      slow,
    };
  }

  return {
    brokenLinks: fallbackBrokenLinks,
    verySlow: 0,
    slow: 0,
  };
}

// ---------------------------------------------------------------------------
// Recent results list (completed jobs only)
// ---------------------------------------------------------------------------

function filterSiteJobs(jobs: JobItem[]): JobItem[] {
  return HoverLib.jobs.filterJobsByDomains(jobs, {
    siteDomain: state.siteDomain,
    siteDomainCandidates: state.siteDomainCandidates,
  }) as JobItem[];
}

function renderRecentResults(jobs: JobItem[]): void {
  const latestContainer = ui.latestResultsList;
  const recentContainer = ui.recentResultsList;
  if (!latestContainer || !recentContainer) {
    return;
  }

  while (latestContainer.firstChild) {
    latestContainer.removeChild(latestContainer.firstChild);
  }

  while (recentContainer.firstChild) {
    recentContainer.removeChild(recentContainer.firstChild);
  }

  const siteJobs = filterSiteJobs(jobs);

  // All completed / non-active jobs go here
  const completedJobs = siteJobs.filter(
    (job) => !isActiveJobStatus(job.status)
  );

  // Show/hide no-job state based on whether there are ANY jobs
  if (siteJobs.length === 0) {
    show(asNode(ui.noJobState));
  } else {
    hide(asNode(ui.noJobState));
  }

  if (completedJobs.length === 0) {
    const empty = document.createElement("p");
    empty.className = "detail";
    empty.textContent = "No completed runs yet.";
    latestContainer.appendChild(empty);
    return;
  }

  const groupedJobs = completedJobs.slice(0, 6);
  const latestJob = groupedJobs[0] || null;
  const recentJobs = groupedJobs.slice(1, 6);

  const hoverJobCard = (window as any).HoverJobCard;

  function makeCard(cardJob: JobItem, compact: boolean): HTMLElement {
    const card: HTMLElement = hoverJobCard
      ? hoverJobCard.createJobCard(cardJob, { context: "extension", compact })
      : buildResultCardFallback(cardJob, compact);
    card.addEventListener("hover-job-card:view", (e: Event) =>
      openSettingsPage((e as CustomEvent).detail.path)
    );
    card.addEventListener("hover-job-card:export", (e: Event) => {
      void exportJob((e as CustomEvent).detail.jobId);
    });
    return card;
  }

  if (latestJob) {
    latestContainer.appendChild(makeCard(latestJob, false));
  }

  if (recentJobs.length > 0) {
    for (const job of recentJobs) {
      recentContainer.appendChild(makeCard(job, true));
    }
  }
}

// ---------------------------------------------------------------------------
// Result card fallback (used only if hover-job-card.js fails to load)
// ---------------------------------------------------------------------------

function buildResultCardFallback(job: JobItem, compact = false): HTMLElement {
  // Minimal fallback used only if hover-job-card.js fails to load.
  const card = document.createElement("div");
  card.className = compact
    ? "result-card result-card--complete result-card--compact"
    : "result-card result-card--complete";
  const label = document.createElement("p");
  label.textContent = String(job.status || "unknown");
  card.appendChild(label);
  return card;
}
// Job export
// ---------------------------------------------------------------------------

async function exportJob(jobId: string): Promise<void> {
  try {
    const payload = (await HoverLib.api.get(
      `/v1/jobs/${jobId}/export`
    )) as JobExportPayload;

    const tasks = Array.isArray(payload.tasks) ? payload.tasks : [];
    const { keys, headers } = prepareExportColumns(payload.columns, tasks);

    const csvRows = [headers.join(",")];
    for (const task of tasks) {
      const values = keys.map((key) => escapeCSVValue(task[key]));
      csvRows.push(values.join(","));
    }

    const csvContent = csvRows.join("\n");
    const filenameBase = sanitizeForFilename(payload.domain || `job-${jobId}`);
    const filename = `${filenameBase}-hover-export.csv`;
    triggerFileDownload(csvContent, "text/csv", filename);
  } catch (error) {
    setStatus(
      "Export failed",
      error instanceof Error ? error.message : "Unknown error"
    );
  }
}

function prepareExportColumns(
  columns: ExportColumn[] | undefined,
  tasks: Record<string, unknown>[]
): { keys: string[]; headers: string[] } {
  if (Array.isArray(columns) && columns.length > 0) {
    return {
      keys: columns.map((column) => column.key),
      headers: columns.map((column) => column.label || column.key),
    };
  }

  const keySet = new Set<string>();
  for (const task of tasks) {
    Object.keys(task || {}).forEach((key) => keySet.add(key));
  }

  const keys = [...keySet];
  return { keys, headers: keys };
}

// CSV/file utilities — delegated to shared formatters via HoverLib bridge
const escapeCSVValue = (value: unknown): string =>
  HoverLib?.fmt?.escapeCSVValue?.(value) ?? String(value ?? "");

function triggerFileDownload(
  content: string,
  mimeType: string,
  filename: string
): void {
  if (HoverLib?.fmt?.triggerFileDownload) {
    HoverLib.fmt.triggerFileDownload(content, mimeType, filename);
  }
}

const sanitizeForFilename = (value: string): string =>
  HoverLib?.fmt?.sanitiseForFilename?.(value) ?? value;

// ---------------------------------------------------------------------------
// Mini chart
// ---------------------------------------------------------------------------

function renderMiniChart(jobs: JobItem[]): void {
  const container = ui.miniChart;
  if (!container) {
    return;
  }

  while (container.firstChild) {
    container.removeChild(container.firstChild);
  }

  const completedJobs = filterSiteJobs(jobs)
    .filter((job) => normalizeJobStatus(job.status) === "completed")
    .slice(0, 12);

  if (completedJobs.length === 0) {
    for (const label of ui.chartScaleLabels || []) {
      label.textContent = "0";
    }
    return;
  }

  const chartRows = completedJobs
    .filter(
      (job) =>
        normalizeJobStatus(job.status) === "completed" && Boolean(job.stats)
    )
    .map((job) => {
      const { brokenLinks, verySlow, slow } = getIssueCounts(job);
      const errorCount = brokenLinks;
      const okCount = verySlow + slow;
      const totalPages = Math.max(0, job.total_tasks);
      return {
        job,
        errorCount,
        okCount,
        issueTotal: errorCount + okCount,
        totalPages,
      };
    })
    .filter((row) => row.issueTotal > 0 && row.totalPages > 0)
    .reverse();

  if (chartRows.length === 0) {
    for (const label of ui.chartScaleLabels || []) {
      label.textContent = "0";
    }
    return;
  }

  const maxIssues = Math.max(...chartRows.map((row) => row.issueTotal), 1);

  const tickTop = maxIssues;
  const tickMid = Math.round(maxIssues * 0.5);
  const tickQuarter = Math.round(maxIssues * 0.25);
  const tickValues = [tickTop, tickMid, tickQuarter, 0];

  (ui.chartScaleLabels || []).forEach((label, index) => {
    const value = tickValues[index] ?? 0;
    label.textContent = String(value);
  });

  const minSegmentHeightPercent = 2;

  for (const row of chartRows) {
    const job = row.job;
    const bar = document.createElement("div");
    bar.className = "chart-bar";
    bar.role = "button";
    bar.tabIndex = 0;
    const dateStr = HoverLib.fmt.formatDateTime(
      job.completed_at || job.created_at
    );
    bar.title = `${dateStr}\nStatus: Completed\nOK: ${row.okCount}\nError: ${row.errorCount}\nTotal pages: ${job.total_tasks.toLocaleString()}`;

    const detailPath = `${APP_ROUTES.viewJob}/${encodeURIComponent(job.id)}`;
    bar.addEventListener("click", () => {
      openSettingsPage(detailPath);
    });
    bar.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        openSettingsPage(detailPath);
      }
    });

    if (row.okCount > 0) {
      const seg = document.createElement("div");
      seg.className = "chart-bar--warning";
      const okHeight = Math.max(
        minSegmentHeightPercent,
        Math.min((row.okCount / maxIssues) * 100, 100)
      );
      seg.style.height = `${okHeight}%`;
      bar.appendChild(seg);
    }

    if (row.errorCount > 0) {
      const seg = document.createElement("div");
      seg.className = "chart-bar--danger";
      const errorHeight = Math.max(
        minSegmentHeightPercent,
        Math.min((row.errorCount / maxIssues) * 100, 100)
      );
      seg.style.height = `${errorHeight}%`;
      bar.appendChild(seg);
    }

    if (bar.children.length > 0) {
      container.appendChild(bar);
    }
  }
}

function renderUsage(usage: UsageStats | null): void {
  if (!usage) {
    if (ui.planNameText) {
      ui.planNameText.innerHTML = "<strong>Plan:</strong> \u2014";
    }
    setText(ui.planRemainingValue, "\u2014");
    return;
  }

  const plan = usage.plan_display_name || usage.plan_name || "Plan";
  const limit = usage.daily_limit.toLocaleString();

  if (ui.planNameText) {
    ui.planNameText.innerHTML = `<strong>Plan:</strong> <strong>${plan}</strong> (${limit} / day)`;
  }

  const remaining = usage.daily_remaining.toLocaleString();
  setText(ui.planRemainingValue, `${remaining} remaining`);
}

function renderOrganisations() {
  const select = asSelect(ui.orgSelect);
  if (!select) {
    return;
  }

  while (select.firstChild) {
    select.removeChild(select.firstChild);
  }

  if (state.organisations.length === 0) {
    const placeholder = document.createElement("option");
    placeholder.textContent = "No organisations";
    placeholder.value = "";
    select.appendChild(placeholder);
    select.disabled = true;
    return;
  }

  select.disabled = false;
  state.organisations.forEach((org) => {
    const option = document.createElement("option");
    option.value = org.id;
    option.textContent = org.name;
    option.selected = org.id === state.activeOrganisationId;
    select.appendChild(option);
  });
}

function renderWebflowStatus(isConnected: boolean) {
  if (!ui.webflowPublishToggle) {
    return;
  }

  ui.webflowPublishToggle.checked =
    isConnected && state.webflowAutoPublishEnabled;
}

function renderScheduleState(): void {
  const scheduleSelect = asSelect(ui.scheduleSelect);
  if (!scheduleSelect) {
    return;
  }

  if (!state.currentScheduler || !state.currentScheduler.is_enabled) {
    scheduleSelect.value = SCHEDULE_PLACEHOLDER;
    return;
  }

  const hours = String(state.currentScheduler.schedule_interval_hours);
  if (SCHEDULE_OPTIONS.includes(hours as any)) {
    scheduleSelect.value = hours;
  }
}

function buildAppUrl(path: string): string {
  try {
    const trimmedBase = state.apiBaseUrl.replace(/\/+$/, "");
    const normalizedPath = path.startsWith("/") ? path : `/${path}`;
    return new URL(normalizedPath, `${trimmedBase}/`).toString();
  } catch (error) {
    console.error("Failed to build app URL", error);
    return `${state.apiBaseUrl.replace(/\/+$/, "")}/${path}`;
  }
}

function setLoading(element: Element | null, disabled: boolean): void {
  if (
    element instanceof HTMLButtonElement ||
    element instanceof HTMLSelectElement
  ) {
    element.disabled = disabled;
  }
}

function setDisabledAll(disabled: boolean): void {
  const controls: (Element | null)[] = [
    ui.checkSiteButton,
    ui.checkSiteAuthButton,
    ui.signInButton,
    ui.runNowButton,
    ui.scheduleSelect,
    ui.orgSelect,
    ui.webflowPublishToggle,
    ui.settingsButton,
  ];

  for (const control of controls) {
    setLoading(control, disabled);
  }

  const toggle = asInput(ui.webflowPublishToggle);
  if (toggle) {
    toggle.disabled = disabled;
  }
}

async function loadCurrentSiteInfo() {
  try {
    const siteInfo = await webflow.getSiteInfo();
    const stageFiltered = siteInfo.domains.filter(
      (domain) => domain.stage === "staging" || domain.stage === "production"
    );
    state.siteDomainCandidates = stageFiltered.map(
      (candidate) => candidate.url
    );

    const preferredDomain =
      stageFiltered.find((domain) => domain.default)?.url ||
      stageFiltered.find((domain) => domain.stage === "production")?.url ||
      stageFiltered.find((domain) => domain.stage === "staging")?.url;

    state.siteDomain = preferredDomain
      ? normalizeDomain(preferredDomain)
      : stageFiltered[0]
        ? normalizeDomain(stageFiltered[0].url)
        : normalizeDomain(siteInfo.shortName);
    state.siteName = siteInfo.siteName;
    return state.siteDomain;
  } catch (error) {
    console.error("Failed to get site info", error);
    return null;
  }
}

async function loadLatestJob(): Promise<void> {
  if (!state.siteDomain || !state.token) {
    state.currentJob = null;
    renderJobState(null);
    renderRecentResults([]);
    renderMiniChart([]);
    lastCompletedJobsSignature = "";
    lastChartJobsSignature = "";
    stopJobStatusPolling();
    return;
  }

  try {
    const jobs = (await HoverLib.jobs.fetchJobs({
      limit: 50,
      include: "stats",
    })) as JobItem[];

    const latest = pickLatestJobForCurrentSite(jobs);

    state.currentJob = latest;
    renderJobState(state.currentJob);
    renderRecentResults(jobs);
    renderMiniChart(jobs);
    lastCompletedJobsSignature = buildCompletedJobsSignature(jobs);
    lastChartJobsSignature = buildChartJobsSignature(jobs);
    startJobStatusPolling();
  } catch (error) {
    state.currentJob = null;
    renderJobState(null);
    renderRecentResults([]);
    renderMiniChart([]);
    lastCompletedJobsSignature = "";
    lastChartJobsSignature = "";
    stopJobStatusPolling();
    console.error(error);
  }
}

async function loadUsageAndOrgs(): Promise<void> {
  if (!state.token) {
    state.organisations = [];
    state.usage = null;
    state.currentScheduler = null;
    return;
  }

  const [orgData, usageData] = (await Promise.all([
    HoverLib.api.get("/v1/organisations"),
    HoverLib.api.get("/v1/usage"),
  ])) as [OrganisationsResponse, UsageResponse];

  state.organisations = orgData.organisations || [];
  state.activeOrganisationId =
    orgData.active_organisation_id || state.activeOrganisationId;
  state.usage = usageData.usage || null;
}

async function loadCurrentSchedule(): Promise<void> {
  if (!state.siteDomain || !state.token) {
    state.currentScheduler = null;
    renderScheduleState();
    return;
  }

  const siteDomain = normalizeDomain(state.siteDomain);
  const schedulers = (await HoverLib.api.get("/v1/schedulers")) as Scheduler[];
  const matching = schedulers.find(
    (scheduler) => normalizeDomain(scheduler.domain) === siteDomain
  );
  state.currentScheduler = matching || null;
  renderScheduleState();
}

async function findConnectedWebflowSite(): Promise<WebflowSiteSetting | null> {
  if (!state.token || !state.siteDomain) {
    renderWebflowStatus(false);
    return null;
  }

  const connections = (await HoverLib.api.get(
    "/v1/integrations/webflow"
  )) as WebflowConnection[];

  if (!connections || connections.length === 0) {
    state.webflowConnected = false;
    state.webflowAutoPublishEnabled = false;
    renderWebflowStatus(false);
    return null;
  }

  state.webflowConnected = true;

  const candidates = getSiteDomainCandidates();
  let matched: WebflowSiteSetting | null = null;

  for (const connection of connections) {
    let page = 1;

    while (true) {
      const sites = (await HoverLib.api.get(
        `/v1/integrations/webflow/${connection.id}/sites?page=${page}&limit=50`
      )) as WebflowSitesResponse;

      const candidate = sites.sites?.find((site) => {
        const domain = normalizeDomain(site.primary_domain);
        return candidates.includes(domain);
      });

      if (candidate) {
        matched = {
          ...candidate,
          connection_id: connection.id,
        };
        break;
      }

      if (!sites.pagination?.has_next) {
        break;
      }

      page += 1;
    }

    if (matched) {
      break;
    }
  }

  if (matched) {
    state.webflowAutoPublishEnabled = Boolean(matched.auto_publish_enabled);
    state.webflowConnected = true;
    renderWebflowStatus(true);
    return matched;
  }

  state.webflowAutoPublishEnabled = false;
  renderWebflowStatus(true);
  return null;
}

async function setWebflowAutoPublish(enabled: boolean): Promise<void> {
  // Optimistically update UI before the network round-trip.
  state.webflowAutoPublishEnabled = enabled;
  renderWebflowStatus(state.webflowConnected);

  const siteSetting = await findConnectedWebflowSite();
  if (!siteSetting) {
    state.webflowAutoPublishEnabled = false;
    renderWebflowStatus(state.webflowConnected);
    setStatus("Connect Webflow and select this site, then try again.", "");
    return;
  }
  if (!siteSetting.connection_id) {
    throw new Error("Connected Webflow site missing connection id.");
  }

  const payload = {
    connection_id: siteSetting.connection_id,
    enabled,
  };

  try {
    await HoverLib.api.put(
      `/v1/integrations/webflow/sites/${siteSetting.webflow_site_id}/auto-publish`,
      payload
    );
  } catch (error) {
    // Revert on failure.
    state.webflowAutoPublishEnabled = !enabled;
    renderWebflowStatus(state.webflowConnected);
    throw error;
  }

  // Re-apply after findConnectedWebflowSite may have overwritten state.
  state.webflowAutoPublishEnabled = enabled;
  renderWebflowStatus(state.webflowConnected);

  setStatus(
    `Auto-publish ${enabled ? "enabled" : "disabled"} for ${state.siteDomain || "this site"}`,
    ""
  );
}

async function setJobSchedule(value: ScheduleOption): Promise<void> {
  if (!state.token || !state.siteDomain) {
    return;
  }

  if (!value) {
    return;
  }

  const domain = state.siteDomain;
  if (value === "off") {
    if (state.currentScheduler) {
      await HoverLib.api.put(`/v1/schedulers/${state.currentScheduler.id}`, {
        is_enabled: false,
      });
    }
    state.currentScheduler = null;
    setStatus("Scheduler disabled for this site.", "");
    renderScheduleState();
    return;
  }

  const scheduleHours = Number(value);

  if (!state.currentScheduler) {
    const created = (await HoverLib.api.post("/v1/schedulers", {
      domain,
      schedule_interval_hours: scheduleHours,
    })) as Scheduler;
    state.currentScheduler = created;
    setStatus("Schedule enabled.", "");
  } else {
    const updated = (await HoverLib.api.put(
      `/v1/schedulers/${state.currentScheduler.id}`,
      { schedule_interval_hours: scheduleHours, is_enabled: true }
    )) as Scheduler;
    state.currentScheduler = updated;
    setStatus("Schedule updated.", "");
  }

  renderScheduleState();
}

async function runScanForCurrentSite(): Promise<void> {
  if (!state.token) {
    const started = await ensureSignedIn();
    if (!started) {
      return;
    }
    await refreshDashboard();
  }

  if (!state.siteDomain) {
    setStatus(
      "Could not read current site domain.",
      "Open a site in the Designer and try again."
    );
    return;
  }

  const request: CreateJobRequest = {
    domain: state.siteDomain,
    source_type: "extension",
    source_detail: "webflow_designer_check",
  };

  const created = (await HoverLib.api.post("/v1/jobs", request)) as JobItem;

  state.currentJob = created;
  renderJobState(created);
  startJobStatusPolling();
  setStatus("Scan started.", "Use Run again to requeue a fresh run.");
  await refreshDashboard();
}

function handleAuthError(error: unknown): void {
  if (typeof error === "object" && error !== null && "status" in error) {
    const apiError = error as ApiError;
    if (apiError.status === 401) {
      setStoredToken(null);
      cleanupRealtimeSubscription();
      supabaseClient = null;
      renderAuthState(false);
      setStatus("Session expired. Sign in again.", "");
      return;
    }

    if (apiError.status === 403) {
      const message = extractErrorMessage(apiError.body);
      setStatus("Action not permitted", message);
      return;
    }

    setStatus(`API error (${apiError.status})`, apiError.body || "");
    return;
  }

  if (error instanceof Error) {
    setStatus("Request failed", error.message);
    return;
  }

  setStatus("Request failed", "Unknown error");
}

async function refreshDashboard(): Promise<void> {
  setDisabledAll(true);

  try {
    setStatus("", "");
    state.token = getStoredToken();

    renderAuthState(Boolean(state.token));

    await loadCurrentSiteInfo();
    if (!state.token) {
      state.currentJob = null;
      state.usage = null;
      state.organisations = [];
      state.currentScheduler = null;
      stopJobStatusPolling();
      cleanupRealtimeSubscription();
      supabaseClient = null;
      renderJobState(null);
      renderRecentResults([]);
      renderMiniChart([]);
      lastCompletedJobsSignature = "";
      lastChartJobsSignature = "";
      renderUsage(null);
      renderOrganisations();
      renderScheduleState();
      renderWebflowStatus(false);
      return;
    }

    try {
      await Promise.all([
        loadUsageAndOrgs(),
        loadLatestJob(),
        loadCurrentSchedule(),
        findConnectedWebflowSite(),
      ]);
      renderUsage(state.usage);
      renderOrganisations();
      void updateAvatarFromState();

      // Initialise Supabase realtime; fall back to legacy polling on failure.
      const client = await initSupabaseClient();
      if (client) {
        void subscribeToJobUpdates();
      } else {
        startJobStatusPolling();
      }
    } catch (error) {
      await handleAuthError(error);
    }
  } finally {
    setDisabledAll(false);
  }
}

async function switchOrganisation(): Promise<void> {
  const select = asSelect(ui.orgSelect);
  if (!select || !select.value) {
    return;
  }

  setDisabledAll(true);
  try {
    await HoverLib.api.post("/v1/organisations/switch", {
      organisation_id: select.value,
    });
    state.activeOrganisationId = select.value;
    await refreshDashboard();
  } finally {
    setDisabledAll(false);
  }
}

function openSettingsPage(path: string): void {
  const targetUrl = buildAppUrl(path);
  const popup = window.open(targetUrl, "_blank", "noopener,noreferrer");
  if (!popup) {
    setStatus("Popup blocked. Allow popups and try again.", "");
  }
}

async function connectWebflow(): Promise<void> {
  if (!state.token) {
    const token = await connectAccount();
    if (!token) {
      return;
    }
  }

  const response = (await HoverLib.api.post("/v1/integrations/webflow")) as {
    auth_url: string;
  };

  const popup = window.open(
    response.auth_url,
    "gnh-webflow-connect",
    `width=520,height=760,left=60,top=60`
  );
  if (!popup) {
    setStatus("Popup blocked. Allow popups and try again.", "");
    return;
  }

  const popupResult = await new Promise<{
    connected?: boolean;
    error?: string;
  }>((resolve) => {
    let timer: number | undefined;
    const origin = new URL(state.apiBaseUrl).origin;
    const handleMessage = (event: MessageEvent) => {
      if (event.source !== popup || event.origin !== origin) {
        return;
      }

      const payload = event.data as {
        source?: string;
        type?: string;
        connected?: boolean;
        error?: string;
      };

      if (
        payload?.source !== "gnh-webflow-connect" ||
        payload.type !== "webflow-connect-complete"
      ) {
        return;
      }

      if (timer) {
        window.clearInterval(timer);
      }
      window.removeEventListener("message", handleMessage);
      resolve({
        connected: payload.connected,
        error: payload.error,
      });
    };

    window.addEventListener("message", handleMessage);

    timer = window.setInterval(() => {
      if (popup.closed) {
        if (timer) {
          window.clearInterval(timer);
        }
        window.removeEventListener("message", handleMessage);
        resolve({});
      }
    }, 500);
  });

  if (!popup.closed) {
    popup.close();
  }

  setStatus("Webflow connection flow complete.", "Refreshing connections.");
  await refreshDashboard();

  if (popupResult?.connected) {
    try {
      await setWebflowAutoPublish(true);
    } catch (error) {
      console.warn("Unable to enable run-on-publish after connect:", error);
    }
    return;
  }

  if (popupResult?.error) {
    setStatus("Webflow connect failed.", popupResult.error);
  }
}

function initEventHandlers(): void {
  // Unauth: check site
  ui.checkSiteButton?.addEventListener("click", async () => {
    try {
      await runScanForCurrentSite();
    } catch (error) {
      await handleAuthError(error);
    }
  });

  // Unauth: sign in
  ui.signInButton?.addEventListener("click", async () => {
    await connectAccount();
    await refreshDashboard();
  });

  // Auth: run now (action bar)
  ui.runNowButton?.addEventListener("click", async () => {
    try {
      await runScanForCurrentSite();
    } catch (error) {
      await handleAuthError(error);
    }
  });

  // Auth: check site (no-job state)
  ui.checkSiteAuthButton?.addEventListener("click", async () => {
    try {
      await runScanForCurrentSite();
    } catch (error) {
      await handleAuthError(error);
    }
  });

  // Auth: settings gear
  ui.settingsButton?.addEventListener("click", () => {
    openSettingsPage(APP_ROUTES.changePlan);
  });

  // Auth: org switcher
  ui.orgSelect?.addEventListener("change", () => {
    void switchOrganisation();
  });

  // Auth: schedule select
  ui.scheduleSelect?.addEventListener("change", async () => {
    const select = asSelect(ui.scheduleSelect);
    if (!select) {
      return;
    }

    const requested = select.value as ScheduleOption;
    try {
      await setJobSchedule(requested);
    } catch (error) {
      await handleAuthError(error);
    }
  });

  // Auth: auto-publish toggle
  ui.webflowPublishToggle?.addEventListener("change", async (event) => {
    const target = event.target as HTMLInputElement | null;
    if (!target) {
      return;
    }

    const enabled = target.checked;
    try {
      if (enabled && !state.webflowConnected) {
        await connectWebflow();
      }
      await setWebflowAutoPublish(enabled);
    } catch (error) {
      if (target) {
        target.checked = !enabled;
      }
      await handleAuthError(error);
    }
  });

  // Footer: feedback
  // TODO: connect to feedback form or mailto link
  ui.feedbackButton?.addEventListener("click", () => {
    openSettingsPage(APP_ROUTES.dashboard);
  });

  // Footer: help
  // TODO: connect to help/docs page
  ui.helpButton?.addEventListener("click", () => {
    openSettingsPage(APP_ROUTES.dashboard);
  });
}

async function initialise(): Promise<void> {
  window.addEventListener("beforeunload", () => {
    stopJobStatusPolling();
    cleanupRealtimeSubscription();
  });
  try {
    localStorage.setItem(API_BASE_STORAGE_KEY, state.apiBaseUrl);
  } catch (_error) {
    // ignore
  }

  // Configure shared API client for cross-origin extension use.
  if (typeof HoverLib !== "undefined" && HoverLib?.api?.configure) {
    HoverLib.api.configure({
      baseUrl: state.apiBaseUrl,
      tokenProvider: () => Promise.resolve(getStoredToken()),
    });
  }

  initHoverJobCard();
  initEventHandlers();
  await refreshDashboard();
  renderAuthState(Boolean(state.token));

  setStatus("", "");
}

void initialise();
