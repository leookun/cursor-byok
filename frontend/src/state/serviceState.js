/**
 * serviceState.js — Service state sync, update management, and bootstrap
 *
 * Handles: proxy/backend state sync, service start/stop, window operations,
 * update state management, event handlers, and bootstrapAppState.
 */
import {
  asString,
  asBoolean,
  asNumber,
  asPositiveInteger,
} from "@/utils/typeCast";
import {
  GENERIC_SERVICE_ERROR,
  HOME_METRICS_MIN_LOADING_MS,
  DEFAULT_OPTIMIZATION,
  normalizeQualityTier,
  formatReleaseDate,
  toUserError,
  delay,
} from "./utils";
import { normalizeModelAdapter } from "./modelAdapter";
import { handleModelAdapterTestUpdatedEvent, refreshModelAdapterTestResults } from "./modelAdapterTest";
import {
  applyConfigToState,
  applyHomeMetrics,
  persistUserConfig,
  reloadUserConfig as reloadUserConfigFn,
} from "./configPersistence";
import { appState } from "./appState";
import {
  checkForUpdates,
  getAppVersion,
  getHomeMetricsSummary,
  getOptimizationCostSummary,
  getProxyState,
  installReadyUpdate,
  openConfigWindow as openConfig,
  openLogsDirectory,
  openModelConfig,
  openModelEditor,
  startProxyService,
  stopProxyService,
} from "@/services/clientApi";

// ===== Service State =====

function applyProxyState(raw) {
  const state = raw && typeof raw === "object" ? raw : {};
  appState.backendRunning = asBoolean(state.backendRunning);
  appState.proxyRunning = asBoolean(state.proxyRunning ?? state.running);
  appState.serviceRunning = appState.proxyRunning;
  appState.serviceLastError = asString(state.lastError);
  appState.backendListenAddr = asString(state.backendListenAddr);
  appState.proxyListenAddr = asString(state.proxyListenAddr || state.listenAddr);
  appState.serviceListenAddr = appState.proxyListenAddr;
  appState.cursorSettingsApplied = asBoolean(state.cursorSettingsApplied);
  appState.netProxySource = asString(state.netProxySource);
  appState.netProxyActive = asBoolean(state.netProxyActive);
  appState.netProxyUsingSystem = asBoolean(state.netProxyUsingSystem);
  appState.netProxyUsingEnv = asBoolean(state.netProxyUsingEnv);
  appState.netProxyHttp = asString(state.netProxyHttp);
  appState.netProxyHttps = asString(state.netProxyHttps);
  appState.netProxyPacIgnored = asBoolean(state.netProxyPacIgnored);
  appState.netProxyDescription = asString(state.netProxyDescription);
}

export function handleProxyStateEvent(event) {
  if (event?.data && typeof event.data === "object") {
    applyProxyState(event.data);
    return;
  }
  void syncServiceState().catch(() => {});
}

export function handleUserConfigChangedEvent(event) {
  if (event?.data && typeof event.data === "object") {
    applyConfigToState(event.data);
    return;
  }
  void reloadUserConfigFn().catch(() => {});
}

// ===== Update State =====

function normalizeUpdateState(value) {
  const text = asString(value).toLowerCase();
  if (["idle", "checking", "downloading", "ready", "installing", "error"].includes(text)) {
    return text;
  }
  return "idle";
}

function applyUpdateSnapshot(raw) {
  const data = raw && typeof raw === "object" ? raw : {};
  const nextState = normalizeUpdateState(data.state ?? appState.updateState);
  appState.updateState = nextState;

  const version = asString(data.version);
  if (version) {
    appState.updateVersion = version;
  } else if (nextState === "idle") {
    appState.updateVersion = "";
  }

  const releaseDate = asString(data.releaseDate);
  if (releaseDate) {
    appState.updateReleaseDate = releaseDate;
  } else if (nextState === "idle") {
    appState.updateReleaseDate = "";
  }

  if (typeof data.releaseNotes === "string") {
    appState.updateReleaseNotes = data.releaseNotes.replace(/\r\n/g, "\n");
  } else if (nextState === "idle") {
    appState.updateReleaseNotes = "";
  }

  if (typeof data.error === "string") {
    appState.updateError = data.error.trim();
  } else if (nextState !== "error") {
    appState.updateError = "";
  }

  if (typeof data.message === "string") {
    appState.updateMessage = data.message.trim();
  } else if (!data.prompt) {
    appState.updateMessage = "";
  }

  if (typeof data.downloaded === "number") {
    appState.updateProgressDownloaded = data.downloaded;
  } else if (nextState !== "downloading") {
    appState.updateProgressDownloaded = 0;
  }

  if (typeof data.total === "number") {
    appState.updateProgressTotal = data.total;
  } else if (nextState !== "downloading") {
    appState.updateProgressTotal = 0;
  }

  if (typeof data.percentage === "number") {
    appState.updateProgressPercent = Math.max(0, Math.min(100, data.percentage));
  } else if (nextState !== "downloading") {
    appState.updateProgressPercent = 0;
  }
}

function openUpdatePrompt(kind, payload = {}) {
  appState.updatePromptKind = asString(kind) || "idle";
  appState.updatePromptVisible = true;
  appState.updatePromptBusy = false;
  if (typeof payload.message === "string") {
    appState.updateMessage = payload.message.trim();
  }
  if (typeof payload.error === "string") {
    appState.updateError = payload.error.trim();
  }
}

export function handleUpdateStateEvent(event) {
  const data = event?.data && typeof event.data === "object" ? event.data : {};
  applyUpdateSnapshot(data);
  if (asBoolean(data.prompt)) {
    openUpdatePrompt(asString(data.promptKind) || "idle", data);
  }
}

export function handleUpdateProgressEvent(event) {
  const data = event?.data && typeof event.data === "object" ? event.data : {};
  applyUpdateSnapshot({
    ...data,
    state: "downloading",
  });
}

export function handleUpdateReadyEvent(event) {
  const data = event?.data && typeof event.data === "object" ? event.data : {};
  applyUpdateSnapshot({
    ...data,
    state: "ready",
  });
  if (data.prompt !== false) {
    openUpdatePrompt("ready", data);
  }
}

export function handleUpdateErrorEvent(event) {
  const data = event?.data && typeof event.data === "object" ? event.data : {};
  applyUpdateSnapshot({
    ...data,
    state: "error",
  });
  if (asBoolean(data.prompt)) {
    openUpdatePrompt("error", data);
  }
}

// ===== Service Control =====

export async function syncServiceState() {
  const state = await getProxyState();
  applyProxyState(state);
  return state;
}

export async function syncHomeMetrics() {
  const startedAt = Date.now();
  appState.homeMetricsLoading = true;
  try {
    const summary = await getHomeMetricsSummary();
    applyHomeMetrics(summary);
    try {
      const cost = await getOptimizationCostSummary();
      if (cost && typeof cost === "object") {
        appState.optimizationCost = {
          enabled: asBoolean(cost.enabled),
          qualityTier: normalizeQualityTier(cost.qualityTier),
          monthlyBudgetUSD: asNumber(cost.monthlyBudgetUSD) || DEFAULT_OPTIMIZATION.monthlyBudgetUSD,
          spentThisMonthUSD: Math.max(0, asNumber(cost.spentThisMonthUSD) || 0),
          turnsThisMonth: Math.max(0, asPositiveInteger(cost.turnsThisMonth) || 0),
          estimatedRemainingTurns: Math.max(0, asPositiveInteger(cost.estimatedRemainingTurns) || 0),
        };
      }
    } catch (_costError) {
      // Optimization 摘要为增强项，失败不影响主 metrics
    }
    return {
      ok: true,
      error: "",
    };
  } catch (error) {
    appState.homeMetricsError = toUserError(error);
    return {
      ok: false,
      error: appState.homeMetricsError,
    };
  } finally {
    const elapsed = Date.now() - startedAt;
    if (elapsed < HOME_METRICS_MIN_LOADING_MS) {
      await delay(HOME_METRICS_MIN_LOADING_MS - elapsed);
    }
    appState.homeMetricsLoading = false;
  }
}

export async function startService() {
  if (appState.serviceBusy) {
    return { ok: false, error: "服务状态更新中，请稍后再试" };
  }
  appState.serviceBusy = true;
  try {
    const saved = await persistUserConfig();
    if (!saved.ok) {
      return saved;
    }
    const state = await startProxyService();
    applyProxyState(state);
    return { ok: true, error: "" };
  } catch (error) {
    await syncServiceState().catch(() => {});
    return { ok: false, error: toUserError(error) };
  } finally {
    appState.serviceBusy = false;
  }
}

export async function stopService() {
  if (appState.serviceBusy) {
    return { ok: false, error: "服务状态更新中，请稍后再试" };
  }
  appState.serviceBusy = true;
  try {
    const state = await stopProxyService();
    applyProxyState(state);
    return { ok: true, error: "" };
  } catch (error) {
    await syncServiceState().catch(() => {});
    return { ok: false, error: toUserError(error) };
  } finally {
    appState.serviceBusy = false;
  }
}

export async function toggleService() {
  if (appState.serviceRunning) {
    return stopService();
  }
  return startService();
}

// ===== Window Operations =====

export async function openLocalLogsDirectory() {
  await openLogsDirectory();
}

export async function openConfigWindow() {
  await openConfig();
}

export async function openModelConfigWindow() {
  await openModelConfig();
}

export async function openModelEditorWindow(index, adapter) {
  const adapterJSON = JSON.stringify(normalizeModelAdapter(adapter));
  await openModelEditor(index, adapterJSON);
}

// ===== Update Management =====

export async function checkForAppUpdates() {
  await checkForUpdates();
}

export function dismissUpdatePrompt() {
  appState.updatePromptVisible = false;
  appState.updatePromptBusy = false;
}

export async function confirmUpdatePrompt() {
  if (appState.updatePromptKind !== "ready") {
    dismissUpdatePrompt();
    return;
  }
  if (appState.updatePromptBusy) {
    return;
  }
  appState.updatePromptBusy = true;
  try {
    await installReadyUpdate();
  } catch (error) {
    appState.updatePromptBusy = false;
    const message = toUserError(error);
    appState.updateError = message;
    openUpdatePrompt("error", { error: message });
  }
}

// ===== Bootstrap =====

export async function bootstrapAppState() {
  try {
    await reloadUserConfigFn();
  } catch (_error) {
    // keep cached config if loading fails
  }
  await refreshModelAdapterTestResults().catch(() => {});
  try {
    appState.appVersion = await getAppVersion();
  } catch (_error) {
    appState.appVersion = "";
  }
  await syncServiceState().catch(() => {});
  await syncHomeMetrics().catch(() => {});
}
