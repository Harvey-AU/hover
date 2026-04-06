/**
 * pages/dashboard.js — module-native dashboard shell
 *
 * Uses the same site-focused shell model as the Webflow extension:
 * org switcher, quota badge, per-site scheduler, run-now action, and
 * latest/past report surfaces.
 */

import { get, post } from "/app/lib/api-client.js";
import { onAuthStateChange, getSession } from "/app/lib/auth-session.js";
import { createJobCard } from "/app/components/hover-job-card.js";
import { showToast } from "/app/components/hover-toast.js";
import { formatDateTime, getInitials } from "/app/lib/formatters.js";
import {
  loadOrganisationContext,
  switchOrganisation as switchOrganisationApi,
} from "/app/lib/organisation-api.js";
import {
  findSchedulerByDomain,
  saveSchedulerForDomain,
  disableScheduler,
} from "/app/lib/scheduler-api.js";
import { ensureSupabaseClient } from "/app/lib/supabase-client.js";
import {
  buildChartJobsSignature,
  buildCompletedJobsSignature,
  fetchJobs,
  filterJobsByDomains,
  normaliseDomain,
  pickLatestJobByDomains,
  subscribeToJobUpdates,
} from "/app/lib/site-jobs.js";
import {
  ensureDomainByName,
  getDomains,
  loadOrganisationDomains,
  setupDomainSearchInput,
} from "/app/lib/domain-search.js";

const ACTIVE_ORG_STORAGE_KEY = "gnh_active_org_id";
const SELECTED_DOMAIN_STORAGE_KEY = "gnh_dashboard_selected_domain";
const ACTIVE_JOB_STATUSES = new Set([
  "pending",
  "queued",
  "initializing",
  "running",
  "in_progress",
  "processing",
]);
const SCHEDULE_PLACEHOLDER = "off";
const SCHEDULE_OPTIONS = new Set(["off", "6", "12", "24", "48"]);
const APP_ROUTES = {
  auth: "/extension-auth.html",
  settings: "/settings/plans",
  viewJob: "/jobs",
  help: "/dashboard",
  feedback: "/dashboard",
};

let authSubscriptionCleanup = null;
let jobsSubscriptionCleanup = null;
let statusToastTimer = null;
let initialised = false;

const state = {
  session: null,
  activeOrganisationId: "",
  organisations: [],
  usage: null,
  selectedDomain: normaliseDomain(
    window.localStorage.getItem(SELECTED_DOMAIN_STORAGE_KEY) || ""
  ),
  siteDomainCandidates: [],
  currentScheduler: null,
  currentJob: null,
  userAvatarUrl: "",
  userEmail: "",
  lastCompletedJobsSignature: "",
  lastChartJobsSignature: "",
  refreshing: false,
};

const ui = {
  guestState: document.getElementById("guestState"),
  authState: document.getElementById("authState"),
  loginButton: document.getElementById("dashboardLoginButton"),
  signupButton: document.getElementById("dashboardSignupButton"),
  settingsButton: document.getElementById("settingsButton"),
  profileAvatar: document.getElementById("profileAvatar"),
  orgSelect: document.getElementById("orgSelect"),
  planNameText: document.getElementById("planNameText"),
  planRemainingValue: document.getElementById("planRemainingValue"),
  domainInput: document.getElementById("domainInput"),
  scheduleSelect: document.getElementById("scheduleSelect"),
  runNowButton: document.getElementById("runNowButton"),
  runFirstCheckButton: document.getElementById("runFirstCheckButton"),
  statusBlock: document.getElementById("statusBlock"),
  statusText: document.getElementById("statusText"),
  detailText: document.getElementById("detailText"),
  noJobState: document.getElementById("noJobState"),
  noJobText: document.getElementById("noJobText"),
  jobSection: document.getElementById("jobSection"),
  latestResultsList: document.getElementById("latestResultsList"),
  recentResultsList: document.getElementById("recentResultsList"),
  miniChart: document.getElementById("miniChart"),
  chartScaleLabels: Array.from(
    document.querySelectorAll(".chart-y-scale span")
  ),
  feedbackButton: document.getElementById("feedbackButton"),
  helpButton: document.getElementById("helpButton"),
};

function show(node) {
  node?.classList.remove("hidden");
}

function hide(node) {
  node?.classList.add("hidden");
}

function setText(node, value) {
  if (node) {
    node.textContent = value;
  }
}

function normaliseJobStatus(status) {
  return String(status || "")
    .trim()
    .toLowerCase();
}

function isActiveJobStatus(status) {
  return ACTIVE_JOB_STATUSES.has(normaliseJobStatus(status));
}

function asCount(value) {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return 0;
  }
  return Math.max(0, Math.floor(value));
}

function getIssueCounts(job) {
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

function buildAuthUrl(mode = "login") {
  const authUrl = new URL(APP_ROUTES.auth, window.location.origin);
  authUrl.searchParams.set("return_to", window.location.href);
  authUrl.searchParams.set("mode", mode);
  return authUrl.toString();
}

function openAuth(mode = "login") {
  window.location.assign(buildAuthUrl(mode));
}

function setStatus(message, detail = "") {
  if (statusToastTimer !== null) {
    clearTimeout(statusToastTimer);
    statusToastTimer = null;
  }

  ui.statusBlock?.classList.remove("status-block--fading");
  setText(ui.statusText, message);
  setText(ui.detailText, detail);

  if (!message && !detail) {
    return;
  }

  statusToastTimer = window.setTimeout(() => {
    ui.statusBlock?.classList.add("status-block--fading");
    statusToastTimer = window.setTimeout(() => {
      ui.statusBlock?.classList.remove("status-block--fading");
      setText(ui.statusText, "");
      setText(ui.detailText, "");
      statusToastTimer = null;
    }, 500);
  }, 3000);
}

function renderAuthState(isAuthed) {
  if (isAuthed) {
    hide(ui.guestState);
    show(ui.authState);
    return;
  }

  show(ui.guestState);
  hide(ui.authState);
}

function updateAvatarFromState() {
  if (!ui.profileAvatar) {
    return;
  }

  ui.profileAvatar.innerHTML = "";
  if (state.userAvatarUrl) {
    const img = document.createElement("img");
    img.src = state.userAvatarUrl;
    img.alt = state.userEmail || "Account";
    ui.profileAvatar.appendChild(img);
    return;
  }

  ui.profileAvatar.textContent = getInitials(state.userEmail || "Hover");
}

function renderUsage() {
  if (!state.usage) {
    if (ui.planNameText) {
      ui.planNameText.innerHTML = "<strong>Plan:</strong> \u2014";
    }
    setText(ui.planRemainingValue, "\u2014");
    return;
  }

  const plan = state.usage.plan_display_name || state.usage.plan_name || "Plan";
  const limit = Number(state.usage.daily_limit || 0).toLocaleString();
  const remaining = Number(state.usage.daily_remaining || 0).toLocaleString();

  if (ui.planNameText) {
    ui.planNameText.innerHTML = `<strong>Plan:</strong> <strong>${plan}</strong> (${limit} / day)`;
  }
  setText(ui.planRemainingValue, `${remaining} remaining`);
}

function renderOrganisations() {
  const select = ui.orgSelect;
  if (!(select instanceof HTMLSelectElement)) {
    return;
  }

  select.innerHTML = "";

  if (!state.organisations.length) {
    const option = document.createElement("option");
    option.value = "";
    option.textContent = "No organisations";
    select.appendChild(option);
    select.disabled = true;
    return;
  }

  select.disabled = false;
  state.organisations.forEach((organisation) => {
    const option = document.createElement("option");
    option.value = organisation.id;
    option.textContent = organisation.name;
    option.selected = organisation.id === state.activeOrganisationId;
    select.appendChild(option);
  });
}

function renderScheduleState() {
  const select = ui.scheduleSelect;
  if (!(select instanceof HTMLSelectElement)) {
    return;
  }

  if (!state.currentScheduler || !state.currentScheduler.is_enabled) {
    select.value = SCHEDULE_PLACEHOLDER;
    return;
  }

  const hours = String(state.currentScheduler.schedule_interval_hours);
  select.value = SCHEDULE_OPTIONS.has(hours) ? hours : SCHEDULE_PLACEHOLDER;
}

function clearNode(node) {
  if (!node) return;
  while (node.firstChild) {
    node.removeChild(node.firstChild);
  }
}

function renderNoJobState(message, canRun = false) {
  setText(ui.noJobText, message);
  if (ui.runFirstCheckButton) {
    ui.runFirstCheckButton.hidden = !canRun;
  }
  show(ui.noJobState);
}

function hideNoJobState() {
  if (ui.runFirstCheckButton) {
    ui.runFirstCheckButton.hidden = false;
  }
  hide(ui.noJobState);
}

function renderJobState(job) {
  const section = ui.jobSection;
  if (!section) {
    return;
  }

  clearNode(section);

  if (!job || !isActiveJobStatus(job.status)) {
    hide(section);
    return;
  }

  const card = createJobCard(job, { context: "extension" });
  card.addEventListener("hover-job-card:view", (event) => {
    window.location.href = event.detail.path;
  });
  card.addEventListener("hover-job-card:export", (event) => {
    void exportJob(event.detail.jobId);
  });
  section.appendChild(card);
  show(section);
}

function renderRecentResults(jobs) {
  const latestContainer = ui.latestResultsList;
  const recentContainer = ui.recentResultsList;

  if (!latestContainer || !recentContainer) {
    return;
  }

  clearNode(latestContainer);
  clearNode(recentContainer);

  const siteJobs = filterJobsByDomains(jobs, {
    siteDomain: state.selectedDomain,
    siteDomainCandidates: state.siteDomainCandidates,
  });

  if (!state.selectedDomain) {
    renderNoJobState("Select a site to review its latest report.");
    return;
  }

  if (siteJobs.length === 0) {
    renderNoJobState(`No runs yet for ${state.selectedDomain}.`, true);
    return;
  }

  hideNoJobState();

  const completedJobs = siteJobs.filter(
    (job) => !isActiveJobStatus(job.status)
  );

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

  function makeCard(cardJob, compact) {
    const card = createJobCard(cardJob, {
      context: "extension",
      compact,
    });
    card.addEventListener("hover-job-card:view", (event) => {
      window.location.href = event.detail.path;
    });
    card.addEventListener("hover-job-card:export", (event) => {
      void exportJob(event.detail.jobId);
    });
    return card;
  }

  latestContainer.appendChild(makeCard(latestJob, false));
  recentJobs.forEach((job) => {
    recentContainer.appendChild(makeCard(job, true));
  });
}

function renderMiniChart(jobs) {
  const container = ui.miniChart;
  if (!container) {
    return;
  }

  clearNode(container);

  const completedJobs = filterJobsByDomains(jobs, {
    siteDomain: state.selectedDomain,
    siteDomainCandidates: state.siteDomainCandidates,
  })
    .filter((job) => normaliseJobStatus(job.status) === "completed")
    .slice(0, 12);

  if (completedJobs.length === 0) {
    ui.chartScaleLabels.forEach((label) => {
      label.textContent = "0";
    });
    return;
  }

  const chartRows = completedJobs
    .filter((job) => Boolean(job.stats))
    .map((job) => {
      const { brokenLinks, verySlow, slow } = getIssueCounts(job);
      const errorCount = brokenLinks;
      const okCount = verySlow + slow;
      const totalPages = Math.max(0, Number(job.total_tasks || 0));
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

  if (!chartRows.length) {
    ui.chartScaleLabels.forEach((label) => {
      label.textContent = "0";
    });
    return;
  }

  const maxIssues = Math.max(...chartRows.map((row) => row.issueTotal), 1);
  const ticks = [
    maxIssues,
    Math.round(maxIssues * 0.5),
    Math.round(maxIssues * 0.25),
    0,
  ];

  ui.chartScaleLabels.forEach((label, index) => {
    label.textContent = String(ticks[index] ?? 0);
  });

  for (const row of chartRows) {
    const bar = document.createElement("div");
    bar.className = "chart-bar";
    bar.role = "button";
    bar.tabIndex = 0;
    bar.title = `${formatDateTime(row.job.completed_at || row.job.created_at)}\nStatus: Completed\nOK: ${row.okCount}\nError: ${row.errorCount}\nTotal pages: ${Number(row.job.total_tasks || 0).toLocaleString()}`;

    const detailPath = `${APP_ROUTES.viewJob}/${encodeURIComponent(row.job.id)}`;
    const openDetail = () => {
      window.location.href = detailPath;
    };

    bar.addEventListener("click", openDetail);
    bar.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        openDetail();
      }
    });

    if (row.okCount > 0) {
      const okSegment = document.createElement("div");
      okSegment.className = "chart-bar--warning";
      okSegment.style.height = `${Math.max(2, Math.min((row.okCount / maxIssues) * 100, 100))}%`;
      bar.appendChild(okSegment);
    }

    if (row.errorCount > 0) {
      const errorSegment = document.createElement("div");
      errorSegment.className = "chart-bar--danger";
      errorSegment.style.height = `${Math.max(2, Math.min((row.errorCount / maxIssues) * 100, 100))}%`;
      bar.appendChild(errorSegment);
    }

    if (bar.children.length > 0) {
      container.appendChild(bar);
    }
  }
}

function setDisabledAll(disabled) {
  [
    ui.settingsButton,
    ui.orgSelect,
    ui.domainInput,
    ui.scheduleSelect,
    ui.runNowButton,
    ui.runFirstCheckButton,
  ].forEach((control) => {
    if (
      control instanceof HTMLButtonElement ||
      control instanceof HTMLInputElement ||
      control instanceof HTMLSelectElement
    ) {
      control.disabled = disabled;
    }
  });
}

function updateDomainInput() {
  if (ui.domainInput instanceof HTMLInputElement) {
    ui.domainInput.value = state.selectedDomain || "";
  }
}

function persistSelectedDomain() {
  if (state.selectedDomain) {
    window.localStorage.setItem(
      SELECTED_DOMAIN_STORAGE_KEY,
      state.selectedDomain
    );
    return;
  }
  window.localStorage.removeItem(SELECTED_DOMAIN_STORAGE_KEY);
}

function applySelectedDomain(domain) {
  const nextDomain = normaliseDomain(domain);
  if (nextDomain !== state.selectedDomain) {
    state.lastCompletedJobsSignature = "";
    state.lastChartJobsSignature = "";
  }

  state.selectedDomain = nextDomain;
  state.siteDomainCandidates = state.selectedDomain
    ? [state.selectedDomain]
    : [];
  persistSelectedDomain();
  updateDomainInput();
}

async function waitForSupabaseClient(timeoutMs = 5000) {
  const start = Date.now();

  while (Date.now() - start < timeoutMs) {
    try {
      return ensureSupabaseClient();
    } catch (_error) {
      await new Promise((resolve) => {
        window.setTimeout(resolve, 50);
      });
    }
  }

  throw new Error("Supabase client did not initialise in time.");
}

async function ensureSelectedDomain() {
  const availableDomains = getDomains();
  if (state.selectedDomain) {
    return;
  }

  const stored = normaliseDomain(
    window.localStorage.getItem(SELECTED_DOMAIN_STORAGE_KEY) || ""
  );
  if (stored) {
    applySelectedDomain(stored);
    return;
  }

  if (availableDomains.length > 0) {
    applySelectedDomain(availableDomains[0].name);
  }
}

async function loadOrganisationState() {
  const context = await loadOrganisationContext();
  state.organisations = Array.isArray(context.organisations)
    ? context.organisations
    : [];
  state.activeOrganisationId = context.activeOrganisationId || "";
  state.usage = context.usage || null;
  if (state.activeOrganisationId) {
    window.localStorage.setItem(
      ACTIVE_ORG_STORAGE_KEY,
      state.activeOrganisationId
    );
  }
}

async function loadCurrentSchedule() {
  if (!state.selectedDomain) {
    state.currentScheduler = null;
    renderScheduleState();
    return;
  }

  state.currentScheduler = await findSchedulerByDomain(state.selectedDomain);
  renderScheduleState();
}

async function refreshSiteResults() {
  const jobs = await fetchJobs({ limit: 50, include: "stats" });

  if (!state.selectedDomain) {
    const firstJobDomain = normaliseDomain(
      jobs[0]?.domains?.name || jobs[0]?.domain || ""
    );
    if (firstJobDomain) {
      applySelectedDomain(firstJobDomain);
    }
  }

  state.currentJob = pickLatestJobByDomains(jobs, {
    siteDomain: state.selectedDomain,
    siteDomainCandidates: state.siteDomainCandidates,
  });

  renderJobState(state.currentJob);

  const completedSignature = buildCompletedJobsSignature(
    jobs,
    {
      siteDomain: state.selectedDomain,
      siteDomainCandidates: state.siteDomainCandidates,
    },
    isActiveJobStatus
  );

  if (completedSignature !== state.lastCompletedJobsSignature) {
    renderRecentResults(jobs);
    state.lastCompletedJobsSignature = completedSignature;
  }

  const chartSignature = buildChartJobsSignature(jobs, {
    siteDomain: state.selectedDomain,
    siteDomainCandidates: state.siteDomainCandidates,
  });

  if (chartSignature !== state.lastChartJobsSignature) {
    renderMiniChart(jobs);
    state.lastChartJobsSignature = chartSignature;
  }
}

function cleanupJobSubscription() {
  if (jobsSubscriptionCleanup) {
    jobsSubscriptionCleanup();
    jobsSubscriptionCleanup = null;
  }
}

function startJobSubscription() {
  cleanupJobSubscription();
  if (!state.activeOrganisationId) {
    return;
  }

  jobsSubscriptionCleanup = subscribeToJobUpdates({
    orgId: state.activeOrganisationId,
    onUpdate: () => {
      void refreshDashboard({ silent: true });
    },
  });
}

async function refreshDashboard(options = {}) {
  if (state.refreshing) {
    return;
  }

  state.refreshing = true;
  if (!options.silent) {
    setDisabledAll(true);
  }

  try {
    await loadOrganisationState();
    await loadOrganisationDomains();
    await ensureSelectedDomain();
    await Promise.all([loadCurrentSchedule(), refreshSiteResults()]);
    renderUsage();
    renderOrganisations();
    updateAvatarFromState();
    renderAuthState(true);
    startJobSubscription();
  } catch (error) {
    console.error("dashboard: failed to refresh", error);
    showToast(error.message || "Failed to refresh the dashboard.", {
      variant: "error",
    });
  } finally {
    if (!options.silent) {
      setDisabledAll(false);
    }
    state.refreshing = false;
  }
}

async function resolveSelectedDomain({ allowCreate = false } = {}) {
  const rawValue =
    ui.domainInput instanceof HTMLInputElement ? ui.domainInput.value : "";
  const nextValue = normaliseDomain(rawValue || state.selectedDomain || "");
  if (!nextValue) {
    applySelectedDomain("");
    await loadCurrentSchedule();
    renderJobState(null);
    renderRecentResults([]);
    renderMiniChart([]);
    return "";
  }

  if (allowCreate) {
    const ensured = await ensureDomainByName(nextValue, { allowCreate: true });
    applySelectedDomain(ensured?.name || nextValue);
  } else {
    applySelectedDomain(nextValue);
  }

  return state.selectedDomain;
}

async function handleDomainCommit({ allowCreate = false } = {}) {
  await resolveSelectedDomain({ allowCreate });
  await Promise.all([loadCurrentSchedule(), refreshSiteResults()]);
}

async function switchOrganisation() {
  if (!(ui.orgSelect instanceof HTMLSelectElement) || !ui.orgSelect.value) {
    return;
  }

  setDisabledAll(true);
  try {
    await switchOrganisationApi(ui.orgSelect.value);
    state.activeOrganisationId = ui.orgSelect.value;
    window.localStorage.setItem(ACTIVE_ORG_STORAGE_KEY, ui.orgSelect.value);
    applySelectedDomain("");
    await refreshDashboard();
  } finally {
    setDisabledAll(false);
  }
}

async function runNow() {
  const domain = await resolveSelectedDomain({ allowCreate: true });
  if (!domain) {
    showToast("Enter a site domain first.", { variant: "error" });
    return;
  }

  setDisabledAll(true);
  try {
    await post("/v1/jobs", {
      domain,
      max_pages: 0,
      use_sitemap: true,
      find_links: true,
    });
    setStatus("Run started.", `Checking ${domain}.`);
    showToast(`Run started for ${domain}`, { variant: "success" });
    await refreshDashboard({ silent: true });
  } catch (error) {
    console.error("dashboard: failed to run job", error);
    showToast(error.message || "Failed to start the run.", {
      variant: "error",
    });
  } finally {
    setDisabledAll(false);
  }
}

async function exportJob(jobId) {
  try {
    const data = await get(`/v1/jobs/${encodeURIComponent(jobId)}/export`, {
      headers: { Accept: "application/json" },
    });
    const tasks = Array.isArray(data?.tasks) ? data.tasks : [];
    if (!tasks.length) {
      showToast("No tasks to export.", { variant: "warning" });
      return;
    }

    const keys = Object.keys(tasks[0]);
    const csv = [
      keys.join(","),
      ...tasks.map((task) => keys.map((key) => csvEscape(task[key])).join(",")),
    ].join("\n");

    const blob = new Blob([csv], { type: "text/csv" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `job-${jobId}.csv`;
    link.click();
    URL.revokeObjectURL(url);
    showToast("Export downloaded.", { variant: "success" });
  } catch (error) {
    showToast(`Export failed: ${error.message}`, { variant: "error" });
  }
}

function csvEscape(value) {
  if (value == null) return "";
  const text = String(value);
  return text.includes(",") || text.includes('"') || text.includes("\n")
    ? `"${text.replace(/"/g, '""')}"`
    : text;
}

async function setJobSchedule() {
  if (!(ui.scheduleSelect instanceof HTMLSelectElement)) {
    return;
  }

  const requested = ui.scheduleSelect.value;
  if (!SCHEDULE_OPTIONS.has(requested)) {
    ui.scheduleSelect.value = SCHEDULE_PLACEHOLDER;
    return;
  }

  const domain = await resolveSelectedDomain({
    allowCreate: requested !== SCHEDULE_PLACEHOLDER,
  });

  if (!domain) {
    showToast("Enter a site domain before changing the schedule.", {
      variant: "error",
    });
    renderScheduleState();
    return;
  }

  setDisabledAll(true);
  try {
    if (requested === SCHEDULE_PLACEHOLDER) {
      if (state.currentScheduler?.id) {
        await disableScheduler(state.currentScheduler.id, {
          expectedIsEnabled: state.currentScheduler.is_enabled,
        });
      }
      state.currentScheduler = null;
      renderScheduleState();
      setStatus("Scheduler disabled.", `No recurring run for ${domain}.`);
      return;
    }

    const scheduler = await saveSchedulerForDomain(domain, Number(requested), {
      currentScheduler: state.currentScheduler,
      extra: {
        max_pages: 0,
        find_links: true,
        concurrency: 20,
      },
    });
    state.currentScheduler = scheduler;
    renderScheduleState();
    setStatus("Scheduler updated.", `Running every ${requested} hours.`);
  } catch (error) {
    console.error("dashboard: failed to save schedule", error);
    renderScheduleState();
    showToast(error.message || "Failed to save the schedule.", {
      variant: "error",
    });
  } finally {
    setDisabledAll(false);
  }
}

function bindDomainSearch() {
  if (!(ui.domainInput instanceof HTMLInputElement)) {
    return;
  }

  setupDomainSearchInput({
    input: ui.domainInput,
    container: ui.domainInput.parentElement,
    clearOnSelect: false,
    onSelectDomain: async (domain) => {
      applySelectedDomain(domain.name);
      await Promise.all([loadCurrentSchedule(), refreshSiteResults()]);
    },
    onCreateDomain: async (domain) => {
      applySelectedDomain(domain.name);
      await Promise.all([loadCurrentSchedule(), refreshSiteResults()]);
    },
    onError: (message) => {
      showToast(message || "Failed to create domain.", { variant: "error" });
    },
  });

  ui.domainInput.addEventListener("change", () => {
    void handleDomainCommit();
  });
  ui.domainInput.addEventListener("keydown", (event) => {
    if (event.key === "Enter") {
      event.preventDefault();
      void handleDomainCommit();
    }
  });
}

function bindEvents() {
  ui.loginButton?.addEventListener("click", () => openAuth("login"));
  ui.signupButton?.addEventListener("click", () => openAuth("signup"));
  ui.settingsButton?.addEventListener("click", () => {
    window.location.assign(APP_ROUTES.settings);
  });
  ui.orgSelect?.addEventListener("change", () => {
    void switchOrganisation();
  });
  ui.scheduleSelect?.addEventListener("change", () => {
    void setJobSchedule();
  });
  ui.runNowButton?.addEventListener("click", () => {
    void runNow();
  });
  ui.runFirstCheckButton?.addEventListener("click", () => {
    void runNow();
  });
  ui.feedbackButton?.addEventListener("click", () => {
    window.location.assign(APP_ROUTES.feedback);
  });
  ui.helpButton?.addEventListener("click", () => {
    window.location.assign(APP_ROUTES.help);
  });

  window.addEventListener("storage", (event) => {
    if (
      event.key === ACTIVE_ORG_STORAGE_KEY &&
      event.newValue &&
      event.newValue !== state.activeOrganisationId
    ) {
      state.activeOrganisationId = event.newValue;
      applySelectedDomain("");
      void refreshDashboard({ silent: true });
    }
  });
}

async function syncAuthState(session) {
  state.session = session;
  state.userEmail = session?.user?.email || "";
  state.userAvatarUrl = session?.user?.user_metadata?.avatar_url || "";
  updateAvatarFromState();

  if (!session) {
    cleanupJobSubscription();
    renderAuthState(false);
    return;
  }

  renderAuthState(true);
  await refreshDashboard();
}

async function init() {
  if (initialised) {
    return;
  }
  initialised = true;

  bindEvents();
  bindDomainSearch();
  await waitForSupabaseClient();

  const initialSession = await getSession().catch(() => null);
  await syncAuthState(initialSession);

  authSubscriptionCleanup = onAuthStateChange((event, nextSession) => {
    if (event === "SIGNED_OUT") {
      state.selectedDomain = "";
      persistSelectedDomain();
    }
    void syncAuthState(nextSession);
  });
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () => {
    void init();
  });
} else {
  void init();
}

window.HoverDashboard = {
  refresh: () => refreshDashboard(),
  destroy: () => {
    authSubscriptionCleanup?.();
    cleanupJobSubscription();
  },
};
