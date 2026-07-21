/**
 * utils.js — Pure utility functions and constants
 *
 * No appState dependency. All functions are pure or depend only on
 * external utilities (typeCast, dayjs).
 */
import {
  asString,
  asNumber,
  asBoolean,
  asPositiveInteger,
} from "@/utils/typeCast";
import dayjs from "dayjs";

// ===== Constants =====

export const APP_STATE_STORAGE_KEY = "cursor-client:runtime-state:v2";
export const GENERIC_SERVICE_ERROR = "服务错误";
export const SUPPORTED_MODEL_ADAPTER_TYPES = new Set(["openai", "anthropic"]);
export const SUPPORTED_REASONING_EFFORTS = new Set(["low", "medium", "high", "xhigh"]);
export const SUPPORTED_ANTHROPIC_THINKING_EFFORTS = new Set(["low", "medium", "high", "xhigh", "max"]);
export const ANTHROPIC_THINKING_EFFORT_DEFAULT = "xhigh";
export const OPENAI_ENDPOINT_RESPONSES = "/v1/responses";
export const OPENAI_ENDPOINT_CHAT_COMPLETIONS = "/v1/chat/completions";
export const OPENAI_ENDPOINT_CUSTOM = "/custom";
export const OPENAI_EXTRA_PARAMS_DEFAULT_JSON = `{
  "service_tier": "priority"
}`;
export const EXTRA_PARAMS_DEFAULT_JSON = `{
}`;
export const CUSTOM_HEADERS_DEFAULT_JSON = `{
}`;
export const SUPPORTED_OPENAI_ENDPOINTS = new Set([
  OPENAI_ENDPOINT_RESPONSES,
  OPENAI_ENDPOINT_CHAT_COMPLETIONS,
  OPENAI_ENDPOINT_CUSTOM,
]);
export const SUPPORTED_ROUTE_MODES = new Set(["local", "upstream"]);
export const SUPPORTED_QUALITY_TIERS = new Set(["fast", "balanced", "quality", "ultra"]);
export const QUALITY_TIER_OPTIONS = [
  { value: "fast", label: "Fast（低成本/低延迟）" },
  { value: "balanced", label: "Balanced（默认）" },
  { value: "quality", label: "Quality（偏质量）" },
  { value: "ultra", label: "Ultra（最高质量）" },
];
export const DEFAULT_OPTIMIZATION = {
  enabled: true,
  qualityTier: "balanced",
  monthlyBudgetUSD: 50,
};
export const ROUTE_MODE_OPTIONS = [
  { label: "本地服务模式", value: "local" },
  { label: "直连 Cursor 模式", value: "upstream" },
];

// Event names
export const PROXY_STATE_EVENT = "proxy:state";
export const USER_CONFIG_CHANGED_EVENT = "user-config:changed";
export const UPDATE_STATE_EVENT = "update:state";
export const UPDATE_PROGRESS_EVENT = "update:progress";
export const UPDATE_READY_EVENT = "update:ready";
export const UPDATE_ERROR_EVENT = "update:error";
export const MODEL_ADAPTER_TEST_UPDATED_EVENT = "model-adapter-test:updated";
export const SUPPORTED_MODEL_ADAPTER_TEST_STATUSES = new Set(["idle", "running", "success", "error"]);
export const HOME_METRICS_MIN_LOADING_MS = 600;

// ===== Utility Functions =====

export function formatReleaseDate(value) {
  const text = asString(value);
  if (!text) {
    return "未知";
  }
  const parsed = dayjs(text);
  if (!parsed.isValid()) {
    return text;
  }
  return parsed.format("YYYY-MM-DD HH:mm");
}

export function normalizeRouteMode(value, fallback = "local") {
  const text = asString(value).toLowerCase();
  if (SUPPORTED_ROUTE_MODES.has(text)) {
    return text;
  }
  return fallback;
}

export function normalizeBaseURL(value) {
  const text = asString(value);
  if (!text) {
    return "";
  }
  try {
    const parsed = new URL(text);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return "";
    }
    parsed.protocol = parsed.protocol.toLowerCase();
    parsed.hostname = parsed.hostname.toLowerCase();
    const normalized = parsed.toString().replace(/\/+$/, "");
    return normalized || parsed.toString();
  } catch (_error) {
    return text;
  }
}

export function normalizeOpenAIEndpoint(value) {
  const text = asString(value).toLowerCase();
  if (!text) {
    return OPENAI_ENDPOINT_RESPONSES;
  }
  return SUPPORTED_OPENAI_ENDPOINTS.has(text) ? text : "";
}

export function isValidOpenAIEndpoint(value) {
  return normalizeOpenAIEndpoint(value) !== "";
}

export function validateJSONObject(value, label) {
  const text = asString(value);
  if (!text) {
    return `${label}不能为空`;
  }
  try {
    const parsed = JSON.parse(text);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      return `${label}必须是 JSON 对象`;
    }
  } catch (_error) {
    return `${label}必须是合法 JSON 对象`;
  }
  return "";
}

export function validateHeadersJSON(value) {
  const objectError = validateJSONObject(value, "自定义请求头 JSON");
  if (objectError) {
    return objectError;
  }
  const parsed = JSON.parse(asString(value));
  for (const [key, item] of Object.entries(parsed)) {
    if (!asString(key)) {
      return "自定义请求头名称不能为空";
    }
    if (typeof item !== "string") {
      return `自定义请求头 ${key} 的值必须是字符串`;
    }
  }
  return "";
}

export function validateOpenAIExtraParamsJSON(value) {
  return validateJSONObject(value, "额外参数 JSON");
}

export function validateAnthropicExtraParamsJSON(value) {
  return validateJSONObject(value, "Anthropic 额外参数 JSON");
}

export function canUseLocalStorage() {
  return typeof window !== "undefined" && typeof window.localStorage !== "undefined";
}

export function delay(ms) {
  return new Promise((resolve) => {
    window.setTimeout(resolve, Math.max(0, ms));
  });
}

export function extractErrorMessage(error) {
  if (typeof error === "string") {
    return error.trim();
  }
  if (error && typeof error === "object") {
    return asString(error.message) || asString(error.error);
  }
  return "";
}

export function toUserError(error) {
  const message = extractErrorMessage(error);
  return message || GENERIC_SERVICE_ERROR;
}

export function hashStringFNV32a(value) {
  let hash = 0x811c9dc5;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return hash.toString(16).padStart(8, "0");
}

export function formatDuration(value) {
  const durationMS = Math.max(0, Math.round(asNumber(value)));
  if (durationMS < 1000) {
    return `${durationMS} ms`;
  }
  return `${(durationMS / 1000).toFixed(1)} s`;
}

export function normalizeQualityTier(value) {
  const tier = asString(value).toLowerCase();
  if (SUPPORTED_QUALITY_TIERS.has(tier)) {
    return tier;
  }
  return DEFAULT_OPTIMIZATION.qualityTier;
}

export function createEmptyHomeMetrics() {
  return {
    turnsTotal: 0,
    validTurnsTotal: 0,
    invalidTurnsTotal: 0,
    requestTokensTotal: 0,
    promptTokensTotal: 0,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    cacheHitRate: null,
  };
}
