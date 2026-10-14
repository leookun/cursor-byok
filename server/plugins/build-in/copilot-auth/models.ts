import type { ModelDefinition, ModelSnapshot, ModelSupport } from "cursor-byok:model";
import { copilotModelHeaders } from "./constants.ts";
import { accountData, ensureToken } from "./resources.ts";

/** 模型走哪个 Copilot 端点;v1 不支持仅 /v1/messages 的模型。 */
export type ModelRoute = "chat" | "responses";

/** 存进 ModelDefinition.privateData,invoke 时原样传回。 */
export type CopilotModelData = {
  route: ModelRoute;
  vendor: string | null;
  reasoningEfforts: string[];
};

function object(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function text(value: unknown): string | null {
  return typeof value === "string" && value.trim() ? value.trim() : null;
}

function number(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function strings(value: unknown): string[] {
  return Array.isArray(value)
    ? value.flatMap((item) => (typeof item === "string" ? [item] : []))
    : [];
}

/**
 * 缺少 supported_endpoints 的旧模型走 chat;GPT 系(含仅支持 responses 的 codex)走 responses;
 * Claude 等走 chat;只剩 /v1/messages 的模型返回 null 并跳过。
 */
export function routeFor(supportedEndpoints: unknown, vendor: string | null): ModelRoute | null {
  if (!Array.isArray(supportedEndpoints)) return "chat";
  const endpoints = strings(supportedEndpoints);
  if (endpoints.includes("/responses") && vendor?.toLowerCase() !== "anthropic") {
    return "responses";
  }
  if (endpoints.includes("/chat/completions")) return "chat";
  return null;
}

/** 解析 `GET {apiBase}/models`,只保留模型选择器中已启用的对话模型。 */
export function parseCopilotModels(body: unknown): ModelDefinition[] {
  const source = object(body)?.data;
  if (!Array.isArray(source)) {
    throw new Error("Copilot model list response does not contain a data array");
  }
  const seen = new Set<string>();
  const models: ModelDefinition[] = [];
  for (const raw of source) {
    const model = object(raw);
    const id = text(model?.id);
    if (!model || !id || seen.has(id)) continue;
    const capabilities = object(model.capabilities);
    const policy = object(model.policy);
    if (capabilities?.type !== "chat" || model.model_picker_enabled !== true) continue;
    if (policy && policy.state !== "enabled") continue;
    const vendor = text(model.vendor);
    const route = routeFor(model.supported_endpoints, vendor);
    if (!route) continue;
    seen.add(id);
    const limits = object(capabilities.limits);
    const supports = object(capabilities.supports);
    const maxOutputTokens = number(limits?.max_output_tokens);
    const privateData: CopilotModelData = {
      route,
      vendor,
      reasoningEfforts: strings(supports?.reasoning_effort),
    };
    models.push({
      id,
      displayName: text(model.name) ?? id,
      ...(maxOutputTokens !== null ? { maxOutputTokens } : {}),
      capabilities: { images: supports?.vision === true },
      privateData,
    });
  }
  return models;
}

export function modelRoute(model: ModelSnapshot): ModelRoute | null {
  const route = object(model.privateData)?.route;
  return route === "chat" || route === "responses" ? route : null;
}

export function reasoningEfforts(model: ModelSnapshot): string[] {
  return strings(object(model.privateData)?.reasoningEfforts);
}

export const copilotModels: ModelSupport = {
  list: async ({ resource }, context): Promise<ModelDefinition[]> => {
    if (!resource) throw new Error("add a GitHub Copilot account before syncing models");
    // list 无法写回资源补丁,续期得到的 token 只在本次使用。
    const { data, token, apiBase } = await ensureToken(accountData(resource), context);
    const response = await context.network.fetch(`${apiBase}/models`, {
      method: "GET",
      headers: copilotModelHeaders(token, data.deviceId),
    });
    if (response.status < 200 || response.status >= 300) {
      throw new Error(`Copilot model discovery failed (HTTP ${response.status}): ${response.body}`);
    }
    let body: unknown;
    try {
      body = JSON.parse(response.body);
    } catch {
      throw new Error("Copilot model discovery returned invalid JSON");
    }
    const models = parseCopilotModels(body);
    // 空列表会清空模型目录,宁可报错保留旧目录。
    if (models.length === 0) throw new Error("Copilot returned no chat models for this account");
    return models;
  },
};
