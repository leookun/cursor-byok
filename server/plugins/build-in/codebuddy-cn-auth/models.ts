import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import type {
  ModelDefinition,
  ModelListInput,
  ModelSnapshot,
  ModelSupport,
} from "cursor-byok:model";
import {
  type AccountData,
  accountData,
  accountHeaders,
  BASE_URL,
  CONFIG_URL,
  ensureFreshAccount,
} from "./resources.ts";

const GENERATIVE_TAGS = new Set([
  "text-to-image",
  "image-to-image",
  "text-to-video",
  "image-to-video",
]);

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

function strings(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value.flatMap((item) => typeof item === "string" && item.trim() ? [item.trim()] : []);
  }
  if (typeof value === "string" && value.trim()) return [value.trim()];
  return [];
}

function tags(model: Record<string, unknown>): string[] {
  return strings(model.tags).map((tag) => tag.toLowerCase());
}

function hasGenerativeTag(model: Record<string, unknown>): boolean {
  return tags(model).some((tag) => GENERATIVE_TAGS.has(tag));
}

function modelSupportsImages(model: Record<string, unknown>): boolean {
  if (model.disabledMultimodal === true || model.disabled_multimodal === true) return false;
  if (model.supportsImages === false || model.supports_images === false) return false;
  if (model.supportsImages === true || model.supports_images === true) return true;
  const modalities = [
    ...strings(model.inputModalities),
    ...strings(model.input_modalities),
    ...strings(model.inputModalities).map((item) => item.toLowerCase()),
  ];
  return modalities.some((item) =>
    item.toLowerCase() === "image" || item.toLowerCase() === "image_url"
  );
}

function reasoningMetadata(model: Record<string, unknown>): string[] {
  const reasoning = object(model.reasoning) ?? {};
  const source = reasoning.supportedEfforts ?? reasoning.supported_efforts ??
    model.supportedReasoningEfforts ?? model.supported_reasoning_efforts ??
    model.reasoningEfforts ?? model.reasoning_efforts;
  const efforts = strings(source).map((effort) => effort.trim()).filter((effort) => {
    const normalized = effort.toLowerCase();
    return normalized !== "none" && normalized !== "off" && normalized !== "disabled";
  });
  if (efforts.length > 0) return [...new Set(efforts)];
  const defaultEffort =
    text(reasoning.effort ?? reasoning.defaultEffort ?? reasoning.default_effort) ??
      text(model.defaultReasoningEffort ?? model.default_reasoning_effort);
  return defaultEffort ? [defaultEffort] : [];
}

/** Upstream display names collide (two models are both called Hy3). */
function idDisplayName(id: string): string {
  return id
    .split(/[-_]/)
    .map((part) => (/^\d/.test(part) ? part : part.charAt(0).toUpperCase() + part.slice(1)))
    .join(" ");
}

function resolveDisplayNames(models: ModelDefinition[]): void {
  const counts = new Map<string, number>();
  for (const model of models) {
    counts.set(model.displayName, (counts.get(model.displayName) ?? 0) + 1);
  }
  for (const model of models) {
    if ((counts.get(model.displayName) ?? 0) < 2) continue;
    model.displayName = idDisplayName(model.id);
  }
  // id 派生的名字之间仍可能冲突时,才用 id 兜底保证唯一。
  const seen = new Set<string>();
  for (const model of models) {
    if (seen.has(model.displayName)) model.displayName = model.id;
    seen.add(model.displayName);
  }
}

function modelOutputLimit(model: Record<string, unknown>): number | undefined {
  const context = object(model.contextWindow) ?? object(model.context_window) ?? {};
  return positiveInteger(
    model.maxOutputTokens ?? model.max_output_tokens ?? context.maxOutputTokens ??
      context.max_output_tokens ?? context.outputTokens ?? context.output_tokens,
  ) ?? undefined;
}

function parseModel(raw: unknown): ModelDefinition | null {
  const model = object(raw);
  if (!model) return null;
  const id = text(model.id ?? model.modelId ?? model.model_id);
  if (
    !id || model.disabled === true || model.supportsToolCall === false ||
    model.supports_tool_call === false ||
    hasGenerativeTag(model)
  ) return null;
  const name = text(model.name ?? model.displayName ?? model.display_name) ?? id;
  const efforts = reasoningMetadata(model);
  const privateData: Record<string, JsonValue> = { reasoningEfforts: efforts };
  return {
    id,
    displayName: name,
    ...(modelOutputLimit(model) ? { maxOutputTokens: modelOutputLimit(model) } : {}),
    capabilities: { images: modelSupportsImages(model) },
    privateData: privateData as JsonValue,
  };
}

function agentName(agent: Record<string, unknown>): string {
  return (text(agent.name) ?? text(agent.id) ?? "").toLowerCase();
}

function agentTags(agent: Record<string, unknown>): string[] {
  return strings(agent.tags).map((tag) => tag.toLowerCase());
}

function isCliAgent(agent: Record<string, unknown>): boolean {
  const name = agentName(agent);
  if (name === "cli") return true;
  const tags = agentTags(agent);
  return name === "craft" || (tags.includes("cli") && tags.includes("default"));
}

function chooseAgent(data: Record<string, unknown>): Record<string, unknown> | null {
  const agentRoot = object(data.agent);
  const source = Array.isArray(data.agents) ? data.agents : agentRoot?.agents;
  const agents = Array.isArray(source)
    ? source.map(object).filter((item): item is Record<string, unknown> => item !== null)
    : [];
  for (const preferred of ["cli", "craft"]) {
    const found = agents.find((agent) => agentName(agent) === preferred);
    if (found) return found;
  }
  return agents.find(isCliAgent) ?? null;
}

export function parseCodeBuddyModels(body: unknown): ModelDefinition[] {
  const root = object(body) ?? {};
  const data = object(root.data) ?? root;
  const agentRoot = object(data.agent);
  const rawModels = Array.isArray(data.models)
    ? data.models
    : Array.isArray(agentRoot?.models)
    ? agentRoot.models
    : [];
  const byId = new Map<string, ModelDefinition>();
  for (const raw of rawModels) {
    const parsed = parseModel(raw);
    if (parsed && !byId.has(parsed.id)) byId.set(parsed.id, parsed);
  }
  const catalog = [...byId.values()];
  const agent = chooseAgent(data);
  const allowlist = agent && Array.isArray(agent.models)
    ? strings(agent.models).filter((id) => byId.has(id))
    : [];
  if (allowlist.length === 0) {
    resolveDisplayNames(catalog);
    return catalog;
  }
  const ordered: ModelDefinition[] = [];
  const seen = new Set<string>();
  for (const id of allowlist) {
    if (seen.has(id)) continue;
    seen.add(id);
    ordered.push(byId.get(id)!);
  }
  resolveDisplayNames(ordered);
  return ordered;
}

function parseResponse(body: string): unknown {
  try {
    return JSON.parse(body);
  } catch {
    throw new Error("CodeBuddy model discovery returned invalid JSON");
  }
}

function enterpriseModelUrl(data: AccountData): string {
  return `${BASE_URL}/console/enterprises/${
    encodeURIComponent(data.enterpriseId ?? "personal")
  }/models`;
}

async function fetchModels(
  data: AccountData,
  context: PluginContext,
): Promise<ModelDefinition[]> {
  const headers = accountHeaders(data);
  const response = await context.network.fetch(CONFIG_URL, { method: "GET", headers });
  if (response.status === 400 || response.status === 404 || response.status === 405) {
    const fallback = await context.network.fetch(enterpriseModelUrl(data), {
      method: "GET",
      headers,
    });
    if (fallback.status < 200 || fallback.status >= 300) {
      throw new Error(`CodeBuddy model discovery failed (HTTP ${fallback.status})`);
    }
    return parseCodeBuddyModels(parseResponse(fallback.body));
  }
  if (response.status < 200 || response.status >= 300) {
    throw new Error(`CodeBuddy model discovery failed (HTTP ${response.status})`);
  }
  return parseCodeBuddyModels(parseResponse(response.body));
}

export const codeBuddyModels: ModelSupport = {
  list: async (
    { resource }: ModelListInput,
    context: PluginContext,
  ): Promise<
    ModelDefinition[] | { models: ModelDefinition[]; patch?: { privateData?: JsonValue } }
  > => {
    if (!resource) throw new Error("add a CodeBuddy CN account before listing models");
    const fresh = await ensureFreshAccount(accountData(resource), context);
    const models = await fetchModels(fresh.data, context);
    if (models.length === 0) throw new Error("CodeBuddy model discovery returned no usable models");
    return fresh.refreshed
      ? { models, patch: { privateData: fresh.data as unknown as JsonValue } }
      : models;
  },
};

export function reasoningEfforts(model: ModelSnapshot): string[] {
  const privateData = object(model.privateData);
  return strings(privateData?.reasoningEfforts).filter((effort) => {
    const normalized = effort.toLowerCase();
    return normalized !== "none" && normalized !== "off" && normalized !== "disabled";
  });
}
