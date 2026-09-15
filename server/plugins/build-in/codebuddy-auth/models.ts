import type { JsonValue } from "cursor-byok:plugin";
import type { ModelDefinition, ModelSupport } from "cursor-byok:model";
import { accountData, accountHeaders } from "./resources.ts";

const CONFIG_URL = "https://copilot.tencent.com/v3/config";
const IMAGE_GENERATION_TAGS = new Set(["text-to-image", "image-to-image"]);

/** Kept intentionally small: authenticated `/v3/config` is the source of truth. */
export const FALLBACK_MODELS: ModelDefinition[] = [
  {
    id: "hy3",
    displayName: "Hy3",
    description: "CodeBuddy",
    maxOutputTokens: 64_000,
    capabilities: { images: true },
    privateData: {
      supportsReasoning: true,
      defaultReasoningEffort: "high",
      reasoningEfforts: ["high"],
      canDisableThinking: false,
      reasoningSummary: "auto",
      temperature: 0.9,
      topP: 1,
    },
  },
  {
    id: "deepseek-v4-flash",
    displayName: "DeepSeek V4 Flash",
    description: "CodeBuddy",
    maxOutputTokens: 50_000,
    capabilities: { images: true },
    privateData: {
      supportsReasoning: true,
      defaultReasoningEffort: "high",
      reasoningEfforts: ["high"],
      canDisableThinking: false,
      reasoningSummary: "auto",
      temperature: 1,
      topP: null,
    },
  },
  {
    id: "deepseek-v4-pro",
    displayName: "DeepSeek V4 Pro",
    description: "CodeBuddy",
    maxOutputTokens: 128_000,
    capabilities: { images: true },
    privateData: {
      supportsReasoning: true,
      defaultReasoningEffort: "high",
      reasoningEfforts: ["high"],
      canDisableThinking: false,
      reasoningSummary: "auto",
      temperature: 1,
      topP: null,
    },
  },
  {
    id: "minimax-m3",
    displayName: "MiniMax M3",
    description: "CodeBuddy",
    maxOutputTokens: 64_000,
    capabilities: { images: true },
    privateData: {
      supportsReasoning: true,
      defaultReasoningEffort: "medium",
      reasoningEfforts: ["medium"],
      canDisableThinking: false,
      reasoningSummary: "auto",
      temperature: 1,
      topP: null,
    },
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

function positiveInteger(value: unknown): number | null {
  const parsed = typeof value === "number"
    ? value
    : typeof value === "string"
    ? Number(value)
    : NaN;
  return Number.isFinite(parsed) && parsed > 0 ? Math.floor(parsed) : null;
}

function finiteNumber(value: unknown): number | null {
  const parsed = typeof value === "number"
    ? value
    : typeof value === "string"
    ? Number(value)
    : NaN;
  return Number.isFinite(parsed) ? parsed : null;
}

function stringList(value: unknown): string[] {
  return Array.isArray(value)
    ? value.flatMap((entry) => typeof entry === "string" && entry.trim() ? [entry.trim()] : [])
    : [];
}

function isImageGenerationModel(model: Record<string, unknown>): boolean {
  return stringList(model.tags).some((tag) => IMAGE_GENERATION_TAGS.has(tag));
}

function modelDescription(model: Record<string, unknown>): string {
  const credits = text(model.credits);
  const description = text(model.descriptionZh ?? model.descriptionEn);
  return ["CodeBuddy", credits, description].filter(Boolean).join(" · ");
}

function privateData(model: Record<string, unknown>): JsonValue {
  const reasoning = object(model.reasoning);
  const defaultEffort = text(reasoning?.defaultEffort ?? reasoning?.effort);
  const declaredEfforts = stringList(reasoning?.supportedEfforts ?? model.reasoningEfforts);
  const reasoningEfforts = declaredEfforts.length > 0
    ? declaredEfforts
    : defaultEffort
    ? [defaultEffort]
    : [];
  return {
    supportsReasoning: model.supportsReasoning === true,
    defaultReasoningEffort: defaultEffort,
    reasoningEfforts,
    canDisableThinking: reasoning?.canDisableThinking === true || model.canDisableThinking === true,
    reasoningSummary: text(reasoning?.summary ?? model.summary),
    temperature: finiteNumber(model.temperature),
    topP: finiteNumber(model.top_p ?? model.topP),
  };
}

/**
 * Projects the account-scoped CodeBuddy product configuration into Cursor models.
 * The CLI agent list is authoritative; `models` also contains hidden, retired, and
 * image-generation-only entries that must not be exposed to Cursor Agent.
 */
export function parseCodeBuddyConfig(body: unknown): ModelDefinition[] {
  const root = object(body);
  const data = object(root?.data) ?? root;
  const source = data?.models;
  if (!Array.isArray(source)) return [];

  const agents = Array.isArray(data?.agents) ? data.agents : [];
  const cliAgent = agents.map(object).find((agent) => text(agent?.name) === "cli");
  const available = stringList(cliAgent?.models);
  const order = new Map(available.map((id, index) => [id, index]));
  const restrictToCli = available.length > 0;
  const seen = new Set<string>();
  const models: ModelDefinition[] = [];

  for (const raw of source) {
    const model = object(raw);
    const id = text(model?.id);
    if (!model || !id || seen.has(id)) continue;
    if (restrictToCli && !order.has(id)) continue;
    if (
      model.disabled === true || model.supportsToolCall === false || isImageGenerationModel(model)
    ) {
      continue;
    }
    seen.add(id);
    const maxOutputTokens = positiveInteger(
      model.maxOutputTokens ?? model.max_output_tokens ?? model.maxCompletionTokens,
    );
    models.push({
      id,
      displayName: text(model.name ?? model.displayName) ?? id,
      description: modelDescription(model),
      ...(maxOutputTokens !== null ? { maxOutputTokens } : {}),
      capabilities: { images: model.supportsImages === true },
      privateData: privateData(model),
    });
  }

  if (restrictToCli) {
    models.sort((left, right) =>
      (order.get(left.id) ?? Infinity) - (order.get(right.id) ?? Infinity)
    );
  }
  return models;
}

function configHeaders(resource: NonNullable<Parameters<ModelSupport["list"]>[0]["resource"]>) {
  const data = accountData(resource);
  return {
    accept: "application/json",
    ...accountHeaders(data),
    "X-Product": "SaaS",
    "X-IDE-Type": "CLI",
    "X-IDE-Name": "Cursor BYOK",
    "X-IDE-Version": "0.1.7",
    "User-Agent": "CLI/0.1.7 CodeBuddy/2.148.0",
  };
}

export const codeBuddyModels: ModelSupport = {
  list: async ({ resource }, context): Promise<ModelDefinition[]> => {
    if (!resource) throw new Error("sign in to CodeBuddy before syncing models");
    const response = await context.network.fetch(CONFIG_URL, {
      method: "GET",
      headers: configHeaders(resource),
    });
    if (response.status === 401 || response.status === 403) {
      throw new Error(`CodeBuddy model discovery authorization failed (HTTP ${response.status})`);
    }
    if (response.status < 200 || response.status >= 300) return FALLBACK_MODELS;
    let body: unknown;
    try {
      body = JSON.parse(response.body);
    } catch {
      return FALLBACK_MODELS;
    }
    const models = parseCodeBuddyConfig(body);
    return models.length > 0 ? models : FALLBACK_MODELS;
  },
};
