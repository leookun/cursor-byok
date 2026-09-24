import type { ModelDefinition, ModelSupport } from "cursor-byok:model";

const MODELS_URL = "https://opencode.ai/zen/v1/models";

// 目录来自上游;这里仅维护免费档例外与协议路由。
const KNOWN_FREE_MODELS: Record<string, true> = { "big-pickle": true };

// 精确排除已下线模型。
const EXCLUDED_MODELS: Record<string, true> = {
  "deepseek-v4-flash-free": true,
};

// 前缀排除覆盖该系列所有版本。
const EXCLUDED_PREFIXES = ["jev-"];

// 仅支持 Responses 端点的免费模型。
const RESPONSES_ONLY_MODELS: Record<string, true> = {
  "muse-spark-1.2-contributor-free": true,
  "muse-spark-1.3-contributor-free": true,
};

function isExcluded(id: string): boolean {
  return EXCLUDED_MODELS[id] === true || EXCLUDED_PREFIXES.some((prefix) => id.startsWith(prefix));
}

function hasFreeTier(id: string): boolean {
  return id.endsWith("-free") || KNOWN_FREE_MODELS[id] === true;
}

function object(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function text(value: unknown): string | null {
  return typeof value === "string" && value.trim() ? value.trim() : null;
}

export function isFreeChatModel(id: string): boolean {
  if (isExcluded(id)) return false;
  if (RESPONSES_ONLY_MODELS[id]) return false;
  return hasFreeTier(id);
}

export function isResponsesModel(id: string): boolean {
  return RESPONSES_ONLY_MODELS[id] === true;
}

function isFreeModel(id: string): boolean {
  return isFreeChatModel(id) || isResponsesModel(id);
}

function displayName(id: string): string {
  return id
    .split("-")
    .map((part) => (/^\d/.test(part) ? part : part.charAt(0).toUpperCase() + part.slice(1)))
    .join(" ");
}

function modelId(value: unknown): string | null {
  if (typeof value === "string") return text(value);
  const model = object(value);
  return model ? text(model.id ?? model.name) : null;
}

function parseFreeModels(body: unknown): ModelDefinition[] {
  const root = object(body);
  const source = root?.models ?? root?.data ?? body;
  if (!Array.isArray(source)) {
    throw new Error("OpenCode model discovery response does not contain a model list");
  }
  const seen = new Set<string>();
  const models: ModelDefinition[] = [];
  for (const raw of source) {
    const id = modelId(raw);
    if (!id || seen.has(id) || !isFreeModel(id)) continue;
    seen.add(id);
    const model = object(raw);
    models.push({
      id,
      displayName: (model ? text(model.name ?? model.displayName) : null) ?? displayName(id),
    });
  }
  return models;
}

export const opencodeModels: ModelSupport = {
  list: async (_input, context): Promise<ModelDefinition[]> => {
    const response = await context.network.fetch(MODELS_URL, {
      method: "GET",
      headers: {
        accept: "application/json",
        "user-agent": "opencode/1.18.31",
        "x-opencode-client": "desktop",
      },
    });
    if (response.status < 200 || response.status >= 300) {
      throw new Error(`OpenCode model discovery failed with HTTP ${response.status}`);
    }
    let body: unknown;
    try {
      body = JSON.parse(response.body);
    } catch {
      throw new Error("OpenCode model discovery returned invalid JSON");
    }
    const models = parseFreeModels(body);
    if (models.length === 0) {
      throw new Error("OpenCode model discovery returned no free models");
    }
    return models;
  },
};
