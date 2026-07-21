/**
 * appState.js — Core reactive state definition, event wiring, and re-exports
 *
 * This is the barrel file for the state module. It defines the reactive
 * appState object, sets up event listeners and localStorage persistence,
 * and re-exports everything from sub-modules for backward compatibility.
 *
 * Sub-modules:
 *   utils.js              — Pure utility functions and constants
 *   modelAdapter.js       — Model adapter normalization, validation, CRUD
 *   modelAdapterTest.js   — Test result management
 *   configPersistence.js  — Config normalization and persistence
 *   serviceState.js       — Service state sync, update management, bootstrap
 */
import { computed, reactive, watchSyncEffect } from "vue";
import { Events } from "@wailsio/runtime";
import { obfuscate } from "@/utils/storageObfuscate";
import { asBoolean, asString } from "@/utils/typeCast";
import {
  APP_STATE_STORAGE_KEY,
  createEmptyHomeMetrics,
  canUseLocalStorage,
  PROXY_STATE_EVENT,
  USER_CONFIG_CHANGED_EVENT,
  MODEL_ADAPTER_TEST_UPDATED_EVENT,
  UPDATE_STATE_EVENT,
  UPDATE_PROGRESS_EVENT,
  UPDATE_READY_EVENT,
  UPDATE_ERROR_EVENT,
} from "./utils";
import { formatReleaseDate } from "./utils";
import { loadCachedState, normalizeConfig, buildConfigPayload, normalizeAOSConfig } from "./configPersistence";
import {
  handleProxyStateEvent,
  handleUserConfigChangedEvent,
  handleUpdateStateEvent,
  handleUpdateProgressEvent,
  handleUpdateReadyEvent,
  handleUpdateErrorEvent,
} from "./serviceState";
import { handleModelAdapterTestUpdatedEvent } from "./modelAdapterTest";

// ===== Initialize from cached state =====

const cachedState = loadCachedState();
const cachedConfig = normalizeConfig(cachedState);

// ===== Core Reactive State =====

export const appState = reactive({
  appVersion: "",
  modelAdapters: cachedConfig.modelAdapters,
  providers: cachedConfig.providers || [],
  modelAdapterTestResults: {},
  configBackendListenAddr: cachedConfig.backendListenAddr,
  configProxyListenAddr: cachedConfig.proxyListenAddr,
  routingMode: cachedConfig.routing.mode,
  includeCacheWriteInHitRate: cachedConfig.homeMetrics.includeCacheWriteInHitRate,
  optimizationEnabled: cachedConfig.optimization?.enabled ?? true,
  optimizationQualityTier: cachedConfig.optimization?.qualityTier ?? "balanced",
  optimizationMonthlyBudgetUSD:
    cachedConfig.optimization?.monthlyBudgetUSD ?? 50,
  virtualModels: cachedConfig.virtualModels || {},
  aosConfig: normalizeAOSConfig(cachedConfig.virtualModels?.aos),
  aosRecognition: { members: [], error: "" },
  optimizationCost: {
    enabled: true,
    qualityTier: "balanced",
    monthlyBudgetUSD: 50,
    spentThisMonthUSD: 0,
    turnsThisMonth: 0,
    estimatedRemainingTurns: 0,
  },

  serviceRunning: asBoolean(cachedState.serviceRunning),
  backendRunning: asBoolean(cachedState.backendRunning),
  proxyRunning: asBoolean(cachedState.proxyRunning),
  serviceBusy: false,
  serviceLastError: asString(cachedState.serviceLastError),
  serviceListenAddr: asString(cachedState.serviceListenAddr),
  backendListenAddr: asString(cachedState.backendListenAddr),
  proxyListenAddr: asString(cachedState.proxyListenAddr),
  cursorSettingsApplied: asBoolean(cachedState.cursorSettingsApplied),
  netProxySource: asString(cachedState.netProxySource),
  netProxyActive: asBoolean(cachedState.netProxyActive),
  netProxyUsingSystem: asBoolean(cachedState.netProxyUsingSystem),
  netProxyUsingEnv: asBoolean(cachedState.netProxyUsingEnv),
  netProxyHttp: asString(cachedState.netProxyHttp),
  netProxyHttps: asString(cachedState.netProxyHttps),
  netProxyPacIgnored: asBoolean(cachedState.netProxyPacIgnored),
  netProxyDescription: asString(cachedState.netProxyDescription),

  configSaving: false,
  homeMetrics: createEmptyHomeMetrics(),
  homeMetricsLoading: false,
  homeMetricsError: "",

  updateState: "idle",
  updateVersion: "",
  updateReleaseDate: "",
  updateReleaseNotes: "",
  updateProgressDownloaded: 0,
  updateProgressTotal: 0,
  updateProgressPercent: 0,
  updateError: "",
  updateMessage: "",
  updatePromptVisible: false,
  updatePromptKind: "idle",
  updatePromptBusy: false,
});

// ===== localStorage Persistence =====

watchSyncEffect(() => {
  if (!canUseLocalStorage()) {
    return;
  }
  try {
    window.localStorage.setItem(
      APP_STATE_STORAGE_KEY,
      obfuscate(JSON.stringify({
        ...buildConfigPayload(),
        serviceRunning: appState.serviceRunning,
        backendRunning: appState.backendRunning,
        proxyRunning: appState.proxyRunning,
        serviceLastError: appState.serviceLastError,
        serviceListenAddr: appState.serviceListenAddr,
        configBackendListenAddr: appState.configBackendListenAddr,
        configProxyListenAddr: appState.configProxyListenAddr,
        backendListenAddr: appState.backendListenAddr,
        proxyListenAddr: appState.proxyListenAddr,
        cursorSettingsApplied: appState.cursorSettingsApplied,
        netProxySource: appState.netProxySource,
        netProxyActive: appState.netProxyActive,
        netProxyUsingSystem: appState.netProxyUsingSystem,
        netProxyUsingEnv: appState.netProxyUsingEnv,
        netProxyHttp: appState.netProxyHttp,
        netProxyHttps: appState.netProxyHttps,
        netProxyPacIgnored: appState.netProxyPacIgnored,
        netProxyDescription: appState.netProxyDescription,
      })),
    );
  } catch (_error) {
    // ignore local persistence failures
  }
});

// ===== Event Listeners =====

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(PROXY_STATE_EVENT, handleProxyStateEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(USER_CONFIG_CHANGED_EVENT, handleUserConfigChangedEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(MODEL_ADAPTER_TEST_UPDATED_EVENT, handleModelAdapterTestUpdatedEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(UPDATE_STATE_EVENT, handleUpdateStateEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(UPDATE_PROGRESS_EVENT, handleUpdateProgressEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(UPDATE_READY_EVENT, handleUpdateReadyEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(UPDATE_ERROR_EVENT, handleUpdateErrorEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

// ===== View Computed State =====

export const appViewState = reactive({
  serviceStatusText: computed(() => {
    if (appState.proxyRunning && appState.backendRunning) {
      return "服务运行中";
    }
    if (appState.backendRunning) {
      return "后端已启动，代理未启动";
    }
    return "服务未启动";
  }),
  serviceStatusClass: computed(() =>
    appState.serviceRunning ? "text-[#22c55e]" : "text-[#f59e0b]",
  ),
  serviceButtonText: computed(() => {
    if (appState.serviceBusy) {
      return appState.serviceRunning ? "关闭中..." : "启动中...";
    }
    return appState.serviceRunning ? "关闭服务" : "启动服务";
  }),
});

export const updateViewState = reactive({
  footerDownloading: computed(() => appState.updateState === "downloading"),
  footerBusy: computed(() => ["checking", "installing"].includes(appState.updateState)),
  footerVersionLabel: computed(() => `v${appState.appVersion || "..."}`),
  footerProgressText: computed(() => `${Math.round(appState.updateProgressPercent || 0)}%`),
  footerProgressStyle: computed(() => ({
    width: `${Math.max(0, Math.min(100, appState.updateProgressPercent || 0))}%`,
  })),
  promptTitle: computed(() => {
    switch (appState.updatePromptKind) {
      case "ready":
        return "发现新版本";
      case "error":
        return "更新失败";
      default:
        return "检查更新";
    }
  }),
  promptContent: computed(() => {
    switch (appState.updatePromptKind) {
      case "ready":
        return [
          `版本：v${appState.updateVersion || appState.appVersion || "..."}`,
          `发布时间：${formatReleaseDate(appState.updateReleaseDate)}`,
          "",
          appState.updateReleaseNotes || "无更新说明",
        ].join("\n");
      case "error":
        return appState.updateError || appState.updateMessage || "服务错误";
      default:
        return appState.updateMessage || `当前已是最新版本（v${appState.appVersion || "..."}）。`;
    }
  }),
  promptConfirmText: computed(() =>
    appState.updatePromptKind === "ready" ? "立即重启更新" : "确定",
  ),
  promptCancelText: computed(() =>
    appState.updatePromptKind === "ready" ? "稍后" : "取消",
  ),
  promptShowCancel: computed(() => appState.updatePromptKind === "ready"),
});

// ===== Re-exports =====

// utils.js
export {
  ANTHROPIC_THINKING_EFFORT_DEFAULT,
  OPENAI_ENDPOINT_RESPONSES,
  OPENAI_ENDPOINT_CHAT_COMPLETIONS,
  OPENAI_ENDPOINT_CUSTOM,
  OPENAI_EXTRA_PARAMS_DEFAULT_JSON,
  EXTRA_PARAMS_DEFAULT_JSON,
  CUSTOM_HEADERS_DEFAULT_JSON,
  QUALITY_TIER_OPTIONS,
  ROUTE_MODE_OPTIONS,
  formatDuration,
  toUserError,
} from "./utils";

// modelAdapter.js
export {
  buildModelAdapterTestRequestHash,
  createEmptyModelAdapter,
  normalizeModelAdapter,
  normalizeModelAdapters,
  validateModelAdapters,
  saveModelAdapterAt,
  deleteModelAdapterAt,
  duplicateModelAdapterAt,
} from "./modelAdapter";

// modelAdapterTest.js
export {
  formatModelAdapterTestSummary,
  getModelAdapterTestResultByID,
  getModelAdapterTestResult,
  isModelAdapterTestResultRunning,
  isModelAdapterTestResultStale,
  refreshModelAdapterTestResults,
  startModelAdapterTest,
} from "./modelAdapterTest";

// configPersistence.js
export {
  persistUserConfig,
  saveIncludeCacheWriteInHitRate,
  saveRoutingMode,
  reloadUserConfig,
  saveAOSConfig,
  recognizeAOSMembers,
} from "./configPersistence";

// serviceState.js
export {
  syncServiceState,
  syncHomeMetrics,
  startService,
  stopService,
  toggleService,
  openLocalLogsDirectory,
  openConfigWindow,
  openModelConfigWindow,
  openModelEditorWindow,
  checkForAppUpdates,
  dismissUpdatePrompt,
  confirmUpdatePrompt,
  bootstrapAppState,
} from "./serviceState";
