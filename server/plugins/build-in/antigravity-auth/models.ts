import type { JsonValue } from "cursor-byok:plugin";
import type { ModelDefinition, ModelSnapshot, ModelSupport } from "cursor-byok:model";
import { accountData } from "./resources.ts";

export const ANTIGRAVITY_PROD_ENDPOINT = "https://cloudcode-pa.googleapis.com";
export const ANTIGRAVITY_DAILY_ENDPOINT = "https://daily-cloudcode-pa.googleapis.com";
export const ANTIGRAVITY_SANDBOX_ENDPOINT = "https://daily-cloudcode-pa.sandbox.googleapis.com";

export const ANTIGRAVITY_ENDPOINTS = [
  ANTIGRAVITY_DAILY_ENDPOINT,
  ANTIGRAVITY_PROD_ENDPOINT,
  ANTIGRAVITY_SANDBOX_ENDPOINT,
];

const FETCH_AVAILABLE_MODELS_PATH = "/v1internal:fetchAvailableModels";
export const ANTIGRAVITY_USER_AGENT =
  "antigravity/hub/2.12.2 (aidev_client; os_type=darwin; arch=arm64; cl=975423596)";

export const ANTIGRAVITY_CLIENT_HEADERS: Record<string, string> = {};

const ANTIGRAVITY_DENYLIST = new Set(["chat_20706", "chat_23310"]);

export const STATIC_ANTIGRAVITY_MODELS: ModelDefinition[] = [
  // Gemini 3.8 Series
  {
    id: "gemini-3.8-flash-high",
    displayName: "Gemini 3.8 Flash (High)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: ["low", "medium", "high"] },
  },
  {
    id: "gemini-3.8-flash-medium",
    displayName: "Gemini 3.8 Flash (Medium)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: ["low", "medium", "high"] },
  },
  {
    id: "gemini-3.8-flash-low",
    displayName: "Gemini 3.8 Flash (Low)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: ["low", "medium", "high"] },
  },
  {
    id: "gemini-3.8-flash-tiered",
    displayName: "Gemini 3.8 Flash (Tiered)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: ["low", "medium", "high"] },
  },

  // Gemini 3.7 Series
  {
    id: "gemini-3.7-flash",
    displayName: "Gemini 3.7 Flash",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: ["low", "medium", "high"] },
  },
  {
    id: "gemini-3.7-flash-high",
    displayName: "Gemini 3.7 Flash (High)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },
  {
    id: "gemini-3.7-flash-medium",
    displayName: "Gemini 3.7 Flash (Medium)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },
  {
    id: "gemini-3.7-flash-low",
    displayName: "Gemini 3.7 Flash (Low)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },
  {
    id: "gemini-3.7-flash-tiered",
    displayName: "Gemini 3.7 Flash (Tiered)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },
  {
    id: "gemini-3.7-flash-thinking",
    displayName: "Gemini 3.7 Flash (Thinking)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },

  // Gemini 3.6 Series
  {
    id: "gemini-3.6-flash-high",
    displayName: "Gemini 3.6 Flash (High)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },
  {
    id: "gemini-3.6-flash-medium",
    displayName: "Gemini 3.6 Flash (Medium)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },
  {
    id: "gemini-3.6-flash-low",
    displayName: "Gemini 3.6 Flash (Low)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },

  // Gemini 3.1 Pro Series
  {
    id: "gemini-3.1-pro-preview",
    displayName: "Gemini 3.1 Pro Preview",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: ["low", "medium", "high"] },
  },
  {
    id: "gemini-3.1-pro-high",
    displayName: "Gemini 3.1 Pro (High)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },
  {
    id: "gemini-3.1-pro-medium",
    displayName: "Gemini 3.1 Pro (Medium)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },
  {
    id: "gemini-3.1-pro-low",
    displayName: "Gemini 3.1 Pro (Low)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },

  // Gemini 2.5 / 2.0 Series
  {
    id: "gemini-2.5-pro",
    displayName: "Gemini 2.5 Pro",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: ["low", "medium", "high"] },
  },
  {
    id: "gemini-2.5-flash",
    displayName: "Gemini 2.5 Flash",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: ["low", "medium", "high"] },
  },
  {
    id: "gemini-2.5-flash-thinking",
    displayName: "Gemini 2.5 Flash Thinking",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },
  {
    id: "gemini-2.5-flash-lite",
    displayName: "Gemini 2.5 Flash Lite",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },
  {
    id: "gemini-2.0-flash",
    displayName: "Gemini 2.0 Flash",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },
  {
    id: "gemini-2.0-flash-lite",
    displayName: "Gemini 2.0 Flash Lite",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },

  // Claude Series (via Antigravity)
  {
    id: "claude-sonnet-4-6",
    displayName: "Claude Sonnet 4.6 (Antigravity)",
    capabilities: { images: true },
    maxOutputTokens: 64000,
    privateData: { reasoningEfforts: ["low", "medium", "high"] },
  },
  {
    id: "claude-sonnet-4-6-thinking",
    displayName: "Claude Sonnet 4.6 Thinking (Antigravity)",
    capabilities: { images: true },
    maxOutputTokens: 64000,
    privateData: { reasoningEfforts: [] },
  },
  {
    id: "claude-opus-4-6-thinking",
    displayName: "Claude 3.7 Opus Thinking (Antigravity)",
    capabilities: { images: true },
    maxOutputTokens: 64000,
    privateData: { reasoningEfforts: ["low", "medium", "high"] },
  },
  {
    id: "claude-3-7-sonnet",
    displayName: "Claude 3.7 Sonnet (Antigravity)",
    capabilities: { images: true },
    maxOutputTokens: 64000,
    privateData: { reasoningEfforts: ["low", "medium", "high"] },
  },
  {
    id: "claude-3-5-sonnet",
    displayName: "Claude 3.5 Sonnet (Antigravity)",
    capabilities: { images: true },
    maxOutputTokens: 64000,
    privateData: { reasoningEfforts: ["low", "medium", "high"] },
  },
  {
    id: "claude-3-5-haiku",
    displayName: "Claude 3.5 Haiku (Antigravity)",
    capabilities: { images: true },
    maxOutputTokens: 64000,
    privateData: { reasoningEfforts: [] },
  },

  // Other Models
  {
    id: "gpt-4o",
    displayName: "GPT-4o (Antigravity / Gemini)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: ["low", "medium", "high"] },
  },
  {
    id: "gpt-4o-mini",
    displayName: "GPT-4o Mini (Antigravity / Gemini)",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
  },
  {
    id: "gpt-oss-120b-medium",
    displayName: "GPT OSS 120B Medium",
    capabilities: { images: false },
    maxOutputTokens: 32768,
    privateData: { reasoningEfforts: [] },
  },
  {
    id: "gemini-3.1-flash-image",
    displayName: "Gemini 3.1 Flash Image",
    capabilities: { images: true },
    maxOutputTokens: 65536,
    privateData: { reasoningEfforts: [] },
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

export function parseAntigravityModels(payload: unknown): ModelDefinition[] {
  const root = object(payload);
  const rawModels = object(root?.models);
  if (!rawModels) return [];

  const models: ModelDefinition[] = [];
  const seen = new Set<string>();

  for (const [modelId, raw] of Object.entries(rawModels)) {
    if (ANTIGRAVITY_DENYLIST.has(modelId)) continue;
    const model = object(raw);
    if (!model) continue;

    const id = modelId.trim();
    if (!id || seen.has(id)) continue;
    seen.add(id);

    const displayName = text(model.displayName) ?? id;
    const supportsThinking = model.supportsThinking === true;
    const reasoningEfforts = supportsThinking ? ["low", "medium", "high"] : [];
    const maxOutputTokens = typeof model.maxOutputTokens === "number" && model.maxOutputTokens > 0
      ? model.maxOutputTokens
      : 65_536;

    models.push({
      id,
      displayName,
      capabilities: {
        images: model.supportsImages === true || id.includes("gemini") || id.includes("claude"),
      },
      maxOutputTokens,
      privateData: { reasoningEfforts },
    });
  }

  // Merge static models from Antigravity catalog that might not be dynamically returned
  for (const staticModel of STATIC_ANTIGRAVITY_MODELS) {
    if (!seen.has(staticModel.id)) {
      seen.add(staticModel.id);
      models.push(staticModel);
    }
  }

  return models;
}

export function reasoningEfforts(model: ModelSnapshot): string[] {
  const data = object(model.privateData);
  const efforts = data?.reasoningEfforts;
  return Array.isArray(efforts) ? efforts.filter((item) => typeof item === "string") : [];
}

export const antigravityModels: ModelSupport = {
  list: async ({ resource }, context): Promise<ModelDefinition[]> => {
    if (!resource) return STATIC_ANTIGRAVITY_MODELS;
    let data;
    try {
      data = accountData(resource);
    } catch {
      return STATIC_ANTIGRAVITY_MODELS;
    }

    const payloads = [
      JSON.stringify({ project: data.projectId || "bamboo-precept-lgxtn" }),
      JSON.stringify({}),
    ];

    for (const endpoint of ANTIGRAVITY_ENDPOINTS) {
      for (const bodyPayload of payloads) {
        try {
          const response = await context.network.fetch(
            `${endpoint}${FETCH_AVAILABLE_MODELS_PATH}`,
            {
              method: "POST",
              headers: {
                authorization: `Bearer ${data.accessToken}`,
                "content-type": "application/json",
                "user-agent": ANTIGRAVITY_USER_AGENT,
                ...ANTIGRAVITY_CLIENT_HEADERS,
              },
              body: bodyPayload,
            },
          );
          if (response.status >= 200 && response.status < 300) {
            const body = JSON.parse(response.body);
            const models = parseAntigravityModels(body);
            if (models.length > 0) return models;
          }
        } catch {
          // Continue
        }
      }
    }

    return STATIC_ANTIGRAVITY_MODELS;
  },
};
