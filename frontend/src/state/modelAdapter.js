/**
 * modelAdapter.js — Model adapter normalization, validation, and CRUD
 *
 * Includes: AOS config helpers, adapter identity key, adapter template,
 * normalization/validation, CRUD operations (save/delete/duplicate),
 * and AOS config persistence.
 */
import {
  asString,
  asBoolean,
  asArray,
  asPositiveInteger,
} from "@/utils/typeCast";
import {
  ANTHROPIC_THINKING_EFFORT_DEFAULT,
  OPENAI_EXTRA_PARAMS_DEFAULT_JSON,
  CUSTOM_HEADERS_DEFAULT_JSON,
  EXTRA_PARAMS_DEFAULT_JSON,
  SUPPORTED_MODEL_ADAPTER_TYPES,
  SUPPORTED_REASONING_EFFORTS,
  SUPPORTED_ANTHROPIC_THINKING_EFFORTS,
  SUPPORTED_ROUTE_MODES,
  normalizeBaseURL,
  normalizeOpenAIEndpoint,
  isValidOpenAIEndpoint,
  validateHeadersJSON,
  validateOpenAIExtraParamsJSON,
  validateAnthropicExtraParamsJSON,
  normalizeRouteMode,
  hashStringFNV32a,
} from "./utils";
import {
  loadPersistedUserConfig,
  persistConfigPayload,
} from "./configPersistence";
import { appState } from "./appState";

// ===== Model Adapter Identity =====

function buildModelAdapterIdentityKey(adapter) {
  return [
    normalizeBaseURL(adapter.baseURL),
    asString(adapter.modelID),
    asString(adapter.apiKey),
    asString(adapter.displayName),
    adapter.type === "openai" ? normalizeOpenAIEndpoint(adapter.openAIEndpoint) : "",
  ].join("\n");
}

export function buildModelAdapterTestRequestHash(source) {
  const adapter = normalizeModelAdapter(source);
  return hashStringFNV32a([
    asString(adapter.type),
    normalizeBaseURL(adapter.baseURL),
    asString(adapter.apiKey),
    asString(adapter.modelID),
    adapter.type === "openai" ? asString(adapter.reasoningEffort || "medium") : "",
    adapter.type === "openai" ? normalizeOpenAIEndpoint(adapter.openAIEndpoint) : "",
    adapter.type === "openai" ? String(Boolean(adapter.openAIExtraParamsEnabled)) : "false",
    adapter.type === "openai" && adapter.openAIExtraParamsEnabled ? asString(adapter.openAIExtraParamsJSON) : "",
    String(Boolean(adapter.customHeadersEnabled)),
    adapter.customHeadersEnabled ? asString(adapter.customHeadersJSON) : "",
    adapter.type === "anthropic" ? String(Boolean(adapter.anthropicExtraParamsEnabled)) : "false",
    adapter.type === "anthropic" && adapter.anthropicExtraParamsEnabled ? asString(adapter.anthropicExtraParamsJSON) : "",
    String(asPositiveInteger(adapter.contextWindowTokens)),
    String(asPositiveInteger(adapter.maxCompletionTokens)),
    String(asPositiveInteger(adapter.anthropicMaxTokens)),
    adapter.type === "anthropic" ? asString(adapter.anthropicThinkingEffort || ANTHROPIC_THINKING_EFFORT_DEFAULT) : "",
  ].join("\n"));
}

// ===== Model Adapter Template =====

export function createEmptyModelAdapter() {
  return {
    id: "",
    ref: "",
    displayName: "",
    type: "openai",
    baseURL: "",
    apiKey: "",
    tooltipData: "备注",
    modelID: "",
    reasoningEffort: "medium",
    openAIEndpoint: "/v1/responses",
    openAIExtraParamsEnabled: false,
    openAIExtraParamsJSON: OPENAI_EXTRA_PARAMS_DEFAULT_JSON,
    customHeadersEnabled: false,
    customHeadersJSON: CUSTOM_HEADERS_DEFAULT_JSON,
    anthropicExtraParamsEnabled: false,
    anthropicExtraParamsJSON: EXTRA_PARAMS_DEFAULT_JSON,
    contextWindowTokens: 0,
    maxCompletionTokens: 0,
    anthropicMaxTokens: 0,
    anthropicThinkingEffort: ANTHROPIC_THINKING_EFFORT_DEFAULT,
    thinkingBudgetTokens: 0,
  };
}

// ===== Model Adapter Normalization & Validation =====

export function normalizeModelAdapter(source) {
  const raw = source && typeof source === "object" ? source : {};
  const normalizedType = asString(raw.type).toLowerCase();
  const normalizedReasoningEffort = asString(raw.reasoningEffort || raw.reasoning_effort).toLowerCase();
  const normalizedAnthropicThinkingEffort = asString(
    raw.anthropicThinkingEffort
      ?? raw.anthropic_thinking_effort
      ?? raw.outputConfigEffort
      ?? raw.output_config_effort,
  ).toLowerCase();
  const normalizedOpenAIEndpoint = normalizeOpenAIEndpoint(
    raw.openAIEndpoint ?? raw.openaiEndpoint ?? raw.open_ai_endpoint ?? raw.endpoint,
  );
  const openAIExtraParamsEnabled = normalizedType === "openai"
    ? asBoolean(raw.openAIExtraParamsEnabled ?? raw.openaiExtraParamsEnabled ?? raw.open_ai_extra_params_enabled)
    : false;
  const openAIExtraParamsJSON = normalizedType === "openai"
    ? asString(raw.openAIExtraParamsJSON ?? raw.openaiExtraParamsJSON ?? raw.open_ai_extra_params_json) || OPENAI_EXTRA_PARAMS_DEFAULT_JSON
    : "";
  const customHeadersEnabled = asBoolean(raw.customHeadersEnabled ?? raw.custom_headers_enabled);
  const customHeadersJSON = asString(raw.customHeadersJSON ?? raw.custom_headers_json) || CUSTOM_HEADERS_DEFAULT_JSON;
  const anthropicExtraParamsEnabled = normalizedType === "anthropic"
    ? asBoolean(raw.anthropicExtraParamsEnabled ?? raw.anthropic_extra_params_enabled)
    : false;
  const anthropicExtraParamsJSON = normalizedType === "anthropic"
    ? asString(raw.anthropicExtraParamsJSON ?? raw.anthropic_extra_params_json) || EXTRA_PARAMS_DEFAULT_JSON
    : "";
  return {
    id: asString(raw.id),
    ref: asString(raw.ref),
    displayName: asString(raw.displayName || raw.name),
    type: SUPPORTED_MODEL_ADAPTER_TYPES.has(normalizedType) ? normalizedType : "",
    baseURL: normalizeBaseURL(raw.baseURL || raw.url),
    apiKey: asString(raw.apiKey || raw.key),
    tooltipData: asString(raw.tooltipData),
    modelID: asString(raw.modelID),
    reasoningEffort: SUPPORTED_REASONING_EFFORTS.has(normalizedReasoningEffort)
      ? normalizedReasoningEffort
      : "medium",
    openAIEndpoint: normalizedType === "openai" ? normalizedOpenAIEndpoint : "",
    openAIExtraParamsEnabled,
    openAIExtraParamsJSON,
    customHeadersEnabled,
    customHeadersJSON,
    anthropicExtraParamsEnabled,
    anthropicExtraParamsJSON,
    contextWindowTokens: asPositiveInteger(
      raw.contextWindowTokens ?? raw.context_window_tokens ?? raw.maxInputTokens ?? raw.max_input_tokens,
    ),
    maxCompletionTokens: asPositiveInteger(
      raw.maxCompletionTokens ?? raw.max_completion_tokens ?? raw.max_tokens ?? raw.max_token,
    ),
    anthropicMaxTokens: asPositiveInteger(
      raw.anthropicMaxTokens ?? raw.anthropic_max_tokens ?? raw.max_tokens,
    ),
    anthropicThinkingEffort: normalizedType === "anthropic"
      ? (SUPPORTED_ANTHROPIC_THINKING_EFFORTS.has(normalizedAnthropicThinkingEffort)
        ? normalizedAnthropicThinkingEffort
        : ANTHROPIC_THINKING_EFFORT_DEFAULT)
      : "",
    thinkingBudgetTokens: asPositiveInteger(
      raw.thinkingBudgetTokens ?? raw.thinking_budget_tokens,
    ),
  };
}

export function normalizeModelAdapters(source) {
  return asArray(source).map((item) => normalizeModelAdapter(item));
}

export function validateModelAdapters(source) {
  const adapters = normalizeModelAdapters(source);
  const seenIdentityKeys = new Set();
  for (const [index, adapter] of adapters.entries()) {
    const prefix = `模型 ${index + 1}`;
    if (!adapter.displayName) {
      return `${prefix} 的显示名称不能为空`;
    }
    if (!SUPPORTED_MODEL_ADAPTER_TYPES.has(adapter.type)) {
      return `${prefix} 的类型仅支持 OpenAI 或 Anthropic`;
    }
    if (!adapter.baseURL) {
      return `${prefix} 的接口地址不能为空`;
    }
    if (!adapter.apiKey) {
      return `${prefix} 的访问密钥不能为空`;
    }
    if (!adapter.tooltipData) {
      return `${prefix} 的悬停提示不能为空`;
    }
    if (!adapter.modelID) {
      return `${prefix} 的模型标识不能为空`;
    }
    if (adapter.type === "openai" && !SUPPORTED_REASONING_EFFORTS.has(adapter.reasoningEffort)) {
      return `${prefix} 的推理强度仅支持 low、medium、high、xhigh`;
    }
    if (adapter.type === "openai" && !isValidOpenAIEndpoint(adapter.openAIEndpoint)) {
      return `${prefix} 的 OpenAI 端点仅支持 /v1/responses、/v1/chat/completions 或以 / 开头的自定义路径`;
    }
    if (adapter.type === "openai" && adapter.openAIExtraParamsEnabled) {
      const extraParamsError = validateOpenAIExtraParamsJSON(adapter.openAIExtraParamsJSON);
      if (extraParamsError) {
        return `${prefix} 的 ${extraParamsError}`;
      }
    }
    if (adapter.customHeadersEnabled) {
      const customHeadersError = validateHeadersJSON(adapter.customHeadersJSON);
      if (customHeadersError) {
        return `${prefix} 的 ${customHeadersError}`;
      }
    }
    if (adapter.type === "anthropic" && adapter.anthropicExtraParamsEnabled) {
      const extraParamsError = validateAnthropicExtraParamsJSON(adapter.anthropicExtraParamsJSON);
      if (extraParamsError) {
        return `${prefix} 的 ${extraParamsError}`;
      }
    }
    if (adapter.type === "anthropic" && !SUPPORTED_ANTHROPIC_THINKING_EFFORTS.has(adapter.anthropicThinkingEffort)) {
      return `${prefix} 的 Anthropic 思考强度仅支持 low、medium、high、xhigh、max`;
    }
    if (adapter.contextWindowTokens && (!Number.isInteger(adapter.contextWindowTokens) || adapter.contextWindowTokens <= 0)) {
      return `${prefix} 的上下文窗口必须为正整数`;
    }
    if (adapter.maxCompletionTokens && (!Number.isInteger(adapter.maxCompletionTokens) || adapter.maxCompletionTokens <= 0)) {
      return `${prefix} 的最大输出 Token 必须为正整数`;
    }
    if (adapter.anthropicMaxTokens && (!Number.isInteger(adapter.anthropicMaxTokens) || adapter.anthropicMaxTokens <= 0)) {
      return `${prefix} 的最大输出 Token 必须为正整数`;
    }
    if (adapter.thinkingBudgetTokens && (!Number.isInteger(adapter.thinkingBudgetTokens) || adapter.thinkingBudgetTokens <= 0)) {
      return `${prefix} 的思考预算 Token 必须为正整数`;
    }
    const dedupeKey = buildModelAdapterIdentityKey(adapter);
    if (seenIdentityKeys.has(dedupeKey)) {
      return `模型渠道重复，请检查 url、modelID、apiKey、displayName、endpoint 组合`;
    }
    seenIdentityKeys.add(dedupeKey);
  }
  return "";
}

export function validateConfigPayload(payload) {
  if (!SUPPORTED_ROUTE_MODES.has(normalizeRouteMode(payload?.routing?.mode, ""))) {
    return "运行模式仅支持 local 或 upstream";
  }
  return "";
}

// ===== Helper Functions =====

function splitDisplayNameSeed(value) {
  const text = asString(value);
  const match = text.match(/^(.*?)(?:\s*[-+](\d+))?$/);
  if (!match) {
    return { base: text || "模型", number: 0 };
  }
  const base = asString(match[1]) || "模型";
  const number = match[2] ? Number(match[2]) : 0;
  return { base, number: Number.isFinite(number) ? number : 0 };
}

function buildNextDisplayName(existingAdapters, sourceName) {
  const { base } = splitDisplayNameSeed(sourceName);
  let next = 1;
  const taken = new Set(
    normalizeModelAdapters(existingAdapters)
      .map((adapter) => adapter.displayName)
      .filter(Boolean),
  );

  while (taken.has(`${base}-${next}`)) {
    next += 1;
  }
  return `${base}-${next}`;
}

// ===== Model Adapter CRUD =====

export async function saveModelAdapterAt(index, adapter) {
  const currentConfig = await loadPersistedUserConfig();
  const nextAdapters = normalizeModelAdapters(currentConfig.modelAdapters);
  const nextAdapter = normalizeModelAdapter(adapter);
  const targetIndex = index >= 0 && index < nextAdapters.length ? index : nextAdapters.length;

  if (index >= 0 && index < nextAdapters.length) {
    nextAdapters.splice(index, 1, nextAdapter);
  } else {
    nextAdapters.push(nextAdapter);
  }

  const result = await persistConfigPayload(
    {
      ...currentConfig,
      modelAdapters: nextAdapters,
    },
    { modelAdaptersOnly: true },
  );
  if (!result.ok) {
    return result;
  }
  return {
    ...result,
    index: targetIndex,
    adapter: appState.modelAdapters[targetIndex] ?? null,
  };
}

export async function deleteModelAdapterAt(index) {
  const currentConfig = await loadPersistedUserConfig();
  const nextAdapters = normalizeModelAdapters(currentConfig.modelAdapters);

  if (index < 0 || index >= nextAdapters.length) {
    return {
      ok: false,
      error: "模型配置不存在，无法删除",
    };
  }

  nextAdapters.splice(index, 1);

  return persistConfigPayload(
    {
      ...currentConfig,
      modelAdapters: nextAdapters,
    },
    { modelAdaptersOnly: true },
  );
}

export async function duplicateModelAdapterAt(index) {
  const currentConfig = await loadPersistedUserConfig();
  const nextAdapters = normalizeModelAdapters(currentConfig.modelAdapters);

  if (index < 0 || index >= nextAdapters.length) {
    return {
      ok: false,
      error: "模型配置不存在，无法复制",
    };
  }

  const source = normalizeModelAdapter(nextAdapters[index]);
  const duplicate = {
    ...source,
    id: "",
    displayName: buildNextDisplayName(nextAdapters, source.displayName || source.modelID || "模型"),
  };

  nextAdapters.splice(index + 1, 0, duplicate);

  return persistConfigPayload(
    {
      ...currentConfig,
      modelAdapters: nextAdapters,
    },
    { modelAdaptersOnly: true },
  );
}
