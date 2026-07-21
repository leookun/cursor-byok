/**
 * configPersistence.js — Config normalization, persistence, and apply-to-state
 *
 * Handles: normalizeConfig, buildConfigPayload, persistConfigPayload,
 * loadPersistedUserConfig, persistUserConfig, saveIncludeCacheWriteInHitRate,
 * saveRoutingMode, reloadUserConfig.
 */
import {
  asString,
  asBoolean,
  asPositiveInteger,
  asNumber,
  asNullableRate,
} from "@/utils/typeCast";
import { deobfuscate } from "@/utils/storageObfuscate";
import {
  DEFAULT_OPTIMIZATION,
  normalizeRouteMode,
  canUseLocalStorage,
  APP_STATE_STORAGE_KEY,
  normalizeQualityTier,
  toUserError,
} from "./utils";
import {
  normalizeModelAdapters,
  normalizeModelAdapter,
  validateModelAdapters,
  validateConfigPayload,
} from "./modelAdapter";
import { appState } from "./appState";
import {
  loadUserConfig,
  saveUserConfig,
  recognizeAOSMembers as recognizeAOSMembersApi,
} from "@/services/clientApi";

// ===== Config Normalization =====

function normalizeOptimization(source) {
  const raw = source && typeof source === "object" ? source : {};
  const budget = asNumber(raw.monthlyBudgetUSD);
  return {
    enabled: raw.enabled === undefined ? DEFAULT_OPTIMIZATION.enabled : asBoolean(raw.enabled),
    qualityTier: normalizeQualityTier(raw.qualityTier),
    monthlyBudgetUSD:
      Number.isFinite(budget) && budget > 0 ? budget : DEFAULT_OPTIMIZATION.monthlyBudgetUSD,
  };
}

export function normalizeConfig(source) {
  const raw = source && typeof source === "object" ? source : {};
  const routing = raw.routing && typeof raw.routing === "object" ? raw.routing : {};
  const homeMetrics = raw.homeMetrics && typeof raw.homeMetrics === "object" ? raw.homeMetrics : {};
  const virtualModels =
    raw.virtualModels && typeof raw.virtualModels === "object" ? raw.virtualModels : {};
  return {
    log: asBoolean(raw.log),
    providerStreamIdleTimeout: asPositiveInteger(raw.providerStreamIdleTimeout),
    backendListenAddr: asString(raw.configBackendListenAddr) || asString(raw.backendListenAddr),
    proxyListenAddr: asString(raw.configProxyListenAddr) || asString(raw.proxyListenAddr),
    modelAdapters: normalizeModelAdapters(raw.modelAdapters),
    providers: Array.isArray(raw.providers) ? raw.providers : [],
    routing: {
      mode: normalizeRouteMode(routing.mode),
    },
    homeMetrics: {
      includeCacheWriteInHitRate: asBoolean(homeMetrics.includeCacheWriteInHitRate),
    },
    optimization: normalizeOptimization(raw.optimization),
    virtualModels,
    lastAgentModelHash: asString(raw.lastAgentModelHash),
  };
}

function normalizeHomeMetrics(source) {
  const raw = source && typeof source === "object" ? source : {};
  const turnsTotal = asPositiveInteger(
    raw.turnsTotal ?? raw.providerCallsTotal ?? raw.validTurnsTotal,
  );
  const validTurnsTotal = asPositiveInteger(raw.validTurnsTotal ?? turnsTotal);
  const invalidTurnsTotal = asPositiveInteger(
    raw.invalidTurnsTotal ?? Math.max(0, turnsTotal - validTurnsTotal),
  );
  return {
    turnsTotal,
    validTurnsTotal,
    invalidTurnsTotal,
    requestTokensTotal: asPositiveInteger(raw.requestTokensTotal),
    promptTokensTotal: asPositiveInteger(raw.promptTokensTotal),
    cacheReadTokens: asPositiveInteger(raw.cacheReadTokens),
    cacheWriteTokens: asPositiveInteger(raw.cacheWriteTokens),
    cacheHitRate: asNullableRate(raw.cacheHitRate),
  };
}

export function applyHomeMetrics(raw) {
  appState.homeMetrics = normalizeHomeMetrics(raw);
  appState.homeMetricsError = "";
}

// ===== Config Persistence Helpers =====

export function loadCachedState() {
  if (!canUseLocalStorage()) {
    return {};
  }

  try {
    const raw = window.localStorage.getItem(APP_STATE_STORAGE_KEY);
    if (!raw) {
      return {};
    }
    const parsed = JSON.parse(deobfuscate(raw));
    if (!parsed || typeof parsed !== "object") {
      return {};
    }
    return parsed;
  } catch (_error) {
    return {};
  }
}

export function buildConfigPayload(source = appState) {
  const normalized = normalizeConfig({
    ...source,
    routing: { mode: source.routingMode ?? source.routing?.mode },
    homeMetrics: {
      includeCacheWriteInHitRate:
        source.includeCacheWriteInHitRate ?? source.homeMetrics?.includeCacheWriteInHitRate,
    },
    optimization: {
      enabled: source.optimizationEnabled ?? source.optimization?.enabled,
      qualityTier: source.optimizationQualityTier ?? source.optimization?.qualityTier,
      monthlyBudgetUSD: source.optimizationMonthlyBudgetUSD ?? source.optimization?.monthlyBudgetUSD,
    },
    virtualModels: source.virtualModels,
    backendListenAddr: source.configBackendListenAddr || source.backendListenAddr,
    proxyListenAddr: source.configProxyListenAddr || source.proxyListenAddr,
  });
  return {
    log: normalized.log,
    providerStreamIdleTimeout: normalized.providerStreamIdleTimeout,
    backendListenAddr: normalized.backendListenAddr,
    proxyListenAddr: normalized.proxyListenAddr,
    modelAdapters: normalized.modelAdapters.map(({ id, ref, ...adapter }) => adapter),
    providers: normalized.providers,
    routing: normalized.routing,
    homeMetrics: normalized.homeMetrics,
    optimization: normalized.optimization,
    virtualModels: normalized.virtualModels,
    lastAgentModelHash: normalized.lastAgentModelHash,
  };
}

export function applyConfigToState(config, { modelAdaptersOnly = false } = {}) {
  const normalized = normalizeConfig(config);
  appState.providers = normalized.providers;
  if (modelAdaptersOnly) {
    appState.modelAdapters = normalized.modelAdapters;
    return normalized;
  }
  appState.modelAdapters = normalized.modelAdapters;
  appState.providers = normalized.providers;
  appState.configBackendListenAddr = normalized.backendListenAddr;
  appState.configProxyListenAddr = normalized.proxyListenAddr;
  appState.routingMode = normalized.routing.mode;
  appState.includeCacheWriteInHitRate = normalized.homeMetrics.includeCacheWriteInHitRate;
  appState.optimizationEnabled = normalized.optimization.enabled;
  appState.optimizationQualityTier = normalized.optimization.qualityTier;
  appState.optimizationMonthlyBudgetUSD = normalized.optimization.monthlyBudgetUSD;
  if (normalized.virtualModels) {
    appState.virtualModels = normalized.virtualModels;
  }
  appState.aosConfig = normalizeAOSConfig(normalized.virtualModels?.aos);
  return normalized;
}

export async function loadPersistedUserConfig() {
  return normalizeConfig(await loadUserConfig());
}

export async function persistConfigPayload(config, { modelAdaptersOnly = false } = {}) {
  const payload = buildConfigPayload(config);
  const configValidationError = validateConfigPayload(payload);
  if (configValidationError) {
    return {
      ok: false,
      error: configValidationError,
    };
  }
  const validationError = validateModelAdapters(payload.modelAdapters);
  if (validationError) {
    return {
      ok: false,
      error: validationError,
    };
  }

  appState.configSaving = true;
  try {
    await saveUserConfig(payload);
    const persisted = await loadPersistedUserConfig();
    applyConfigToState(persisted, { modelAdaptersOnly });
    return {
      ok: true,
      error: "",
    };
  } catch (error) {
    return {
      ok: false,
      error: toUserError(error),
    };
  } finally {
    appState.configSaving = false;
  }
}

// ===== Exported Config Operations =====

export async function persistUserConfig() {
  const currentConfig = await loadPersistedUserConfig();
  return persistConfigPayload({
    ...currentConfig,
    modelAdapters: normalizeModelAdapters(appState.modelAdapters),
    providers: appState.providers || [],
    routing: {
      mode: appState.routingMode,
    },
    homeMetrics: {
      ...currentConfig.homeMetrics,
      includeCacheWriteInHitRate: appState.includeCacheWriteInHitRate,
    },
  });
}

export async function saveIncludeCacheWriteInHitRate(value) {
  const currentConfig = await loadPersistedUserConfig();
  const previousValue = appState.includeCacheWriteInHitRate;
  const nextValue = asBoolean(value);
  appState.includeCacheWriteInHitRate = nextValue;
  const result = await persistConfigPayload({
    ...currentConfig,
    homeMetrics: {
      ...currentConfig.homeMetrics,
      includeCacheWriteInHitRate: nextValue,
    },
  });
  if (!result.ok) {
    appState.includeCacheWriteInHitRate = previousValue;
  }
  return result;
}

export async function saveRoutingMode(mode) {
  const currentConfig = await loadPersistedUserConfig();
  return persistConfigPayload({
    ...currentConfig,
    routing: {
      mode: normalizeRouteMode(mode),
    },
  });
}

export async function reloadUserConfig(options = {}) {
  const config = await loadPersistedUserConfig();
  applyConfigToState(config, options);
  return config;
}

// ===== AOS Config =====

const AOS_DEFAULT_MEMBERS = [
  {
    id: "default-architect",
    name: "架构师",
    adapterID: "",
    systemPrompt:
      "你是一名首席架构师，负责系统设计和关键技术决策。评估需求后给出架构方案（含组件图、数据流、技术选型理由），标注风险点和 trade-off。在每个方案末尾给出推荐选项及其理由。",
    tags: ["architecture", "design"],
  },
  {
    id: "default-frontend",
    name: "前端工程师",
    adapterID: "",
    systemPrompt:
      "你是一名资深前端工程师，擅长 Vue/React/TypeScript。编写生产级 UI 代码，注重可访问性、响应式设计和性能优化。代码需包含错误边界和 loading 状态。",
    tags: ["frontend", "vue", "react"],
  },
  {
    id: "default-backend",
    name: "后端工程师",
    adapterID: "",
    systemPrompt:
      "你是一名资深后端工程师，擅长 Go/Python/Node。编写高性能、可扩展的后端服务，包含错误处理、日志、监控埋点和单元测试。API 设计遵循 RESTful 规范。",
    tags: ["backend", "api", "go", "python"],
  },
  {
    id: "default-tester",
    name: "测试工程师",
    adapterID: "",
    systemPrompt:
      "你是一名资深测试工程师，专注于质量保障。为代码编写单元测试、集成测试和 E2E 测试用例，覆盖正常路径、边界条件和异常场景。给出测试覆盖率报告和改进建议。",
    tags: ["testing", "qa"],
  },
  {
    id: "default-devops",
    name: "运维工程师",
    adapterID: "",
    systemPrompt:
      "你是一名 DevOps 工程师，负责 CI/CD、容器化和部署。提供 Dockerfile/docker-compose/k8s 配置，CI 流水线配置，并确保安全性和可观测性（日志/指标/追踪）。",
    tags: ["devops", "docker", "k8s"],
  },
];

export function normalizeAOSConfig(source) {
  const raw = source && typeof source === "object" ? source : {};
  const leader = raw.leader && typeof raw.leader === "object" ? raw.leader : {};
  const members = Array.isArray(raw.members) && raw.members.length > 0
    ? raw.members
    : rawIsEmpty(raw) ? AOS_DEFAULT_MEMBERS : [];
  return {
    enabled: asBoolean(raw.enabled) || (rawIsEmpty(raw) ? true : false),
    executionMode: normalizeAOSExecutionMode(raw.executionMode),
    leader: {
      adapterID: asString(leader.adapterID),
    },
    members: members.map((m) => ({
      id: asString(m.id) || `member-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
      name: asString(m.name),
      adapterID: asString(m.adapterID),
      systemPrompt: asString(m.systemPrompt || m.system_prompt),
    })),
  };
}

function normalizeAOSExecutionMode(value) {
  return asString(value).toLowerCase() === "internal" ? "internal" : "cursor_task";
}

function rawIsEmpty(raw) {
  if (!raw || typeof raw !== "object") return true;
  if (raw.enabled != null) return false;
  if (raw.executionMode != null) return false;
  if (raw.leader && (raw.leader.adapterID || raw.leader.adapter_id)) return false;
  if (Array.isArray(raw.members) && raw.members.length > 0) return false;
  return true;
}

export async function saveAOSConfig(config) {
  const currentConfig = await loadPersistedUserConfig();
  const nextAOS = normalizeAOSConfig(config);
  const nextVirtualModels = {
    ...currentConfig.virtualModels,
    aos: {
      enabled: nextAOS.enabled,
      executionMode: nextAOS.executionMode,
      leader: { adapterID: nextAOS.leader.adapterID },
      members: nextAOS.members.map((m) => ({
        id: m.id,
        name: m.name,
        adapterID: m.adapterID,
        systemPrompt: m.systemPrompt,
      })),
    },
  };
  appState.virtualModels = nextVirtualModels;
  appState.aosConfig = nextAOS;
  const result = await persistConfigPayload({
    ...currentConfig,
    optimization: {
      ...currentConfig.optimization,
      enabled: appState.optimizationEnabled,
      qualityTier: appState.optimizationQualityTier,
      monthlyBudgetUSD: appState.optimizationMonthlyBudgetUSD,
    },
    virtualModels: nextVirtualModels,
  });
  return result;
}

export async function recognizeAOSMembers() {
  try {
    const raw = await recognizeAOSMembersApi();
    const members = Array.isArray(raw?.members) ? raw.members : [];
    const error = typeof raw?.error === "string" ? raw.error : "";
    appState.aosRecognition = { members, error };
    return { ok: !error, members, error };
  } catch (err) {
    const msg = String(err?.message || err || "服务错误");
    appState.aosRecognition = { members: [], error: msg };
    return { ok: false, members: [], error: msg };
  }
}
