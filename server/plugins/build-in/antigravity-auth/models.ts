import type { PluginContext } from "cursor-byok:plugin";
import type { ModelDefinition, ModelSupport } from "cursor-byok:model";
import { accountData } from "./resources.ts";
import { callableModels } from "./model_routes.ts";
import { fetchPublicModelNames } from "./public_models.ts";

export const ANTIGRAVITY_SANDBOX_ENDPOINT = "https://daily-cloudcode-pa.sandbox.googleapis.com";
const ANTIGRAVITY_DAILY_ENDPOINT = "https://daily-cloudcode-pa.googleapis.com";
const ANTIGRAVITY_PROD_ENDPOINT = "https://cloudcode-pa.googleapis.com";

/** Antigravity-Manager fallback order: sandbox -> daily -> production. */
export const ANTIGRAVITY_ENDPOINTS = [
  ANTIGRAVITY_SANDBOX_ENDPOINT,
  ANTIGRAVITY_DAILY_ENDPOINT,
  ANTIGRAVITY_PROD_ENDPOINT,
];

const FETCH_AVAILABLE_MODELS_PATH = "/v1internal:fetchAvailableModels";

export const ANTIGRAVITY_USER_AGENT =
  "Antigravity/4.3.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/132.0.6834.160 Electron/39.2.3";
export const ANTIGRAVITY_OAUTH_USER_AGENT = "vscode/1.X.X (Antigravity/4.3.0)";

export const ANTIGRAVITY_CLIENT_HEADERS: Record<string, string> = {
  "x-client-name": "antigravity",
  "x-client-version": "4.3.0",
};

function object(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function text(value: unknown): string | null {
  return typeof value === "string" && value.trim() ? value.trim() : null;
}

function positiveInteger(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) && value > 0
    ? Math.floor(value)
    : null;
}

function supportsImages(model: Record<string, unknown>): boolean {
  if (model.supportsImages === true) return true;
  const mimeTypes = object(model.supportedMimeTypes);
  return Object.entries(mimeTypes ?? {}).some(([mimeType, supported]) =>
    mimeType.toLowerCase().startsWith("image/") && supported === true
  );
}

/** 账号真实可调用的目录;公开清单的交集留给 callableModels 处理。 */
export function parseAntigravityCatalog(payload: unknown): ModelDefinition[] {
  const root = object(payload);
  const rawModels = object(root?.models);
  if (!root || !rawModels) {
    throw new Error("Antigravity model discovery response does not contain a model map");
  }

  const deprecated = object(root.deprecatedModelIds) ?? {};
  const models: ModelDefinition[] = [];
  for (const [rawId, rawModel] of Object.entries(rawModels)) {
    let id = rawId.trim();
    const visited = new Set<string>();
    while (object(deprecated[id])) {
      if (visited.has(id)) throw new Error("Antigravity model forwarding contains a cycle");
      visited.add(id);
      const next = text(object(deprecated[id])?.newModelId);
      if (!next) break;
      id = next;
    }
    // Prefer the current model's own metadata when both IDs are reported.
    if (id !== rawId.trim() && rawModels[id]) continue;
    const model = object(rawModel);
    if (!id || !model) continue;

    const maxOutputTokens = positiveInteger(model.maxOutputTokens ?? model.maxTokens);
    models.push({
      id,
      displayName: text(model.displayName) ?? id,
      capabilities: { images: supportsImages(model) },
      ...(maxOutputTokens !== null ? { maxOutputTokens } : {}),
      privateData: { thinkingBudget: positiveInteger(model.thinkingBudget) ?? 0 },
    });
  }
  return [...new Map(models.map((model) => [model.id, model])).values()];
}

export function antigravityRequestHeaders(accessToken: string): Record<string, string> {
  return {
    authorization: `Bearer ${accessToken}`,
    accept: "application/json",
    "content-type": "application/json",
    "user-agent": ANTIGRAVITY_OAUTH_USER_AGENT,
  };
}

export async function fetchAntigravityCatalog(
  accessToken: string,
  projectId: string | null,
  network: PluginContext["network"],
): Promise<ModelDefinition[]> {
  let lastFailure = "all endpoints failed";

  for (const endpoint of ANTIGRAVITY_ENDPOINTS) {
    let body = projectId ? JSON.stringify({ project: projectId }) : JSON.stringify({});
    let retriedWithoutProject = false;

    while (true) {
      let response;
      try {
        response = await network.fetch(`${endpoint}${FETCH_AVAILABLE_MODELS_PATH}`, {
          method: "POST",
          headers: antigravityRequestHeaders(accessToken),
          body,
        });
      } catch (error) {
        lastFailure = error instanceof Error ? error.message : String(error);
        break;
      }

      if (response.status >= 200 && response.status < 300) {
        let payload: unknown;
        try {
          payload = JSON.parse(response.body);
        } catch {
          throw new Error("Antigravity model discovery returned invalid JSON");
        }
        return parseAntigravityCatalog(payload);
      }

      if (response.status === 403 && projectId && !retriedWithoutProject) {
        body = JSON.stringify({});
        retriedWithoutProject = true;
        continue;
      }
      if (response.status === 403) {
        return [];
      }

      lastFailure = `HTTP ${response.status}: ${response.body}`;
      if (response.status === 429 || response.status >= 500) break;
      throw new Error(`Antigravity model discovery failed (${lastFailure})`);
    }
  }

  throw new Error(`Antigravity model discovery failed (${lastFailure})`);
}

export const antigravityModels: ModelSupport = {
  list: async ({ resource }, context): Promise<ModelDefinition[]> => {
    if (!resource) throw new Error("add a Google Antigravity account before syncing models");
    const data = accountData(resource);
    const [discovered, publicNames] = await Promise.all([
      fetchAntigravityCatalog(data.accessToken, data.projectId ?? null, context.network),
      fetchPublicModelNames(context.network),
    ]);
    return callableModels(discovered, publicNames);
  },
};
