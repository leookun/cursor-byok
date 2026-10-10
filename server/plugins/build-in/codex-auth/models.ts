import type { JsonValue } from "cursor-byok:plugin";
import { modelMetadata, type ModelDefinition, type ModelSnapshot, type ModelSupport } from "cursor-byok:model";
import { accountData, accountHeaders } from "./resources.ts";

const MODELS_URL = "https://chatgpt.com/backend-api/codex/models?client_version=1.0.0";

function object(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function text(value: unknown): string | null {
  return typeof value === "string" && value.trim() ? value.trim() : null;
}

function positiveInteger(value: unknown): number | null {
  const parsed = typeof value === "number"
    ? value
    : typeof value === "string"
    ? Number(value)
    : NaN;
  return Number.isFinite(parsed) && parsed > 0 ? Math.floor(parsed) : null;
}

function parseReasoningEfforts(model: Record<string, unknown>): string[] {
  const source = model.supported_reasoning_levels ??
    model.supportedReasoningLevels ??
    model.reasoning_levels ??
    model.reasoningLevels ??
    model.supported_reasoning_efforts ??
    model.supportedReasoningEfforts ??
    model.reasoning_efforts ??
    model.reasoningEfforts;
  if (!Array.isArray(source)) return [];
  const values = source.flatMap((item) => {
    if (typeof item === "string") return [item.trim()];
    const entry = object(item);
    const value = text(entry?.effort ?? entry?.id ?? entry?.value ?? entry?.name);
    return value ? [value] : [];
  }).filter(Boolean);
  return [...new Set(values)];
}

function modelId(value: unknown): string | null {
  if (typeof value === "string") return text(value);
  const model = object(value);
  return model ? text(model.slug ?? model.id ?? model.model ?? model.name) : null;
}

export function parseOfficialModels(body: unknown): ModelDefinition[] {
  const root = object(body);
  const source = root?.models ?? root?.data ?? body;
  if (!Array.isArray(source)) {
    throw new Error("Codex model discovery response does not contain a model list");
  }
  const seen = new Set<string>();
  const models: ModelDefinition[] = [];
  for (const raw of source) {
    const model = object(raw);
    if (!model || model.supported_in_api === false || model.supportedInApi === false) continue;
    if (text(model.visibility)?.toLowerCase() === "hidden") continue;
    const id = modelId(model);
    if (!id || seen.has(id)) continue;
    seen.add(id);
    const efforts = parseReasoningEfforts(model);
    const description = text(model.description);
    const maxOutputTokens = positiveInteger(
      model.max_output_tokens ?? model.maxOutputTokens ?? model.max_completion_tokens ??
        model.maxCompletionTokens,
    );
    models.push({
      id,
      displayName: text(model.display_name ?? model.displayName ?? model.title ?? model.name) ??
        id,
      ...(description ? { description } : {}),
      ...(maxOutputTokens !== null ? { maxOutputTokens } : {}),
      images: true,
      ...modelMetadata(model),
      privateData: { reasoningEfforts: efforts },
    });
  }
  const defaultModel = modelId(
    root?.default_model ??
      root?.defaultModel ??
      root?.default_model_slug ??
      root?.defaultModelSlug ??
      root?.primary_model ??
      root?.primaryModel,
  );
  // 把上游默认模型排在最前,让宿主自然选中它。
  if (defaultModel) {
    models.sort((left, right) =>
      Number(right.id === defaultModel) - Number(left.id === defaultModel)
    );
  }
  return models;
}

export function reasoningEfforts(model: ModelSnapshot): string[] {
  const data = object(model.privateData);
  const efforts = data?.reasoningEfforts;
  return Array.isArray(efforts) ? efforts.filter((item) => typeof item === "string") : [];
}

export const codexModels: ModelSupport = {
  list: async ({ resource }, context): Promise<ModelDefinition[]> => {
    if (!resource) throw new Error("add a ChatGPT account before syncing Codex models");
    const data = accountData(resource);
    const response = await context.network.fetch(MODELS_URL, {
      method: "GET",
      headers: accountHeaders(data),
    });
    if (response.status < 200 || response.status >= 300) {
      throw new Error(`Codex model discovery failed (HTTP ${response.status}): ${response.body}`);
    }
    let body: unknown;
    try {
      body = JSON.parse(response.body) as JsonValue;
    } catch {
      throw new Error("Codex model discovery returned invalid JSON");
    }
    const models = parseOfficialModels(body);
    if (models.length === 0) {
      throw new Error("Codex model discovery returned no supported models");
    }
    return models;
  },
};
