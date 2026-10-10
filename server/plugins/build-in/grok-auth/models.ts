import { modelMetadata, type ModelDefinition, type ModelSupport } from "cursor-byok:model";
import { accountData } from "./resources.ts";

const LANGUAGE_MODELS_URL = "https://api.x.ai/v1/language-models";
const MODELS_URL = "https://api.x.ai/v1/models";

/** 免费账号无权调用模型列表接口(403 spending-limit);退回已知模型。 */
export const FALLBACK_MODELS: ModelDefinition[] = [
  {
    id: "grok-4.6",
    displayName: "Grok 4.6",
    images: true,
  },
  {
    id: "grok-4.5",
    displayName: "Grok 4.5",
    images: true,
  },
];

function object(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function text(value: unknown): string | null {
  return typeof value === "string" && value.trim() ? value.trim() : null;
}

/** 把模型 ID 变成可读名称,如 grok-4-fast → Grok 4 Fast。 */
function displayName(id: string): string {
  return id
    .split("-")
    .map((part) => (/^\d/.test(part) ? part : part.charAt(0).toUpperCase() + part.slice(1)))
    .join(" ");
}

/** 兼容 /v1/language-models 的 models 数组与 /v1/models 的 data 数组。 */
export function parseGrokModels(body: unknown): ModelDefinition[] {
  const root = object(body);
  const source = root?.models ?? root?.data ?? body;
  if (!Array.isArray(source)) {
    throw new Error("Grok model discovery response does not contain a model list");
  }
  const seen = new Set<string>();
  const models: ModelDefinition[] = [];
  for (const raw of source) {
    const model = object(raw);
    const id = model ? text(model.id ?? model.name) : null;
    if (!id || seen.has(id)) continue;
    seen.add(id);
    const rawInputs = model?.input_modalities ?? model?.inputModalities;
    const inputs = Array.isArray(rawInputs)
      ? rawInputs.filter((input): input is string => typeof input === "string").map((input) => input.toLowerCase())
      : [];
    models.push({
      id,
      displayName: displayName(id),
      images: inputs.length === 0 || inputs.includes("image"),
      ...modelMetadata(model!),
    });
  }
  return models;
}

export const grokModels: ModelSupport = {
  list: async ({ resource }, context): Promise<ModelDefinition[]> => {
    if (!resource) throw new Error("add a Grok account before syncing models");
    const data = accountData(resource);
    const headers = {
      accept: "application/json",
      authorization: `Bearer ${data.accessToken}`,
    };
    // language-models 带模态与上下文元数据;不可用时回退到标准列表。
    let response = await context.network.fetch(LANGUAGE_MODELS_URL, { method: "GET", headers });
    if (response.status < 200 || response.status >= 300) {
      response = await context.network.fetch(MODELS_URL, { method: "GET", headers });
    }
    if (response.status < 200 || response.status >= 300) {
      return FALLBACK_MODELS;
    }
    let body: unknown;
    try {
      body = JSON.parse(response.body);
    } catch {
      throw new Error("Grok model discovery returned invalid JSON");
    }
    const models = parseGrokModels(body);
    return models.length > 0 ? models : FALLBACK_MODELS;
  },
};
