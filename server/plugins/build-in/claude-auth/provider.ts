import type { ProviderResult, ProviderSupport } from "cursor-byok:provider";
import type { ResourcePatch, ResourceState } from "cursor-byok:resource";
import {
  type AccountData,
  accountData,
  apiHeaders,
  isExpiring,
  OAuthError,
  privateData,
  refreshTokens,
  RESOURCE_TYPE,
} from "./auth.ts";
import { HttpError, streamMessages } from "./messages.ts";
import { claudeModels } from "./models.ts";

export function retryAtMs(headers: Record<string, string>, now = Date.now()): number {
  const value = Object.entries(headers).find(([key]) => key.toLowerCase() === "retry-after")?.[1];
  if (value) {
    const seconds = Number(value);
    if (Number.isFinite(seconds) && seconds >= 0) return now + Math.max(1, seconds) * 1000;
    const date = Date.parse(value);
    if (Number.isFinite(date)) return Math.max(now + 1000, date);
  }
  return now + 60_000;
}

export const claudeProvider: ProviderSupport = {
  id: "claude",
  displayName: "Claude (experimental OAuth)",
  description: {
    "en-US": "Unofficial personal subscription access to the Anthropic Messages API.",
    "ru-RU": "Неофициальный личный доступ по подписке к Anthropic Messages API.",
    "zh-CN": "通过订阅个人访问 Anthropic Messages API 的非官方集成。",
  },
  providerType: "anthropic",
  resourceType: RESOURCE_TYPE,
  models: claudeModels,
  async invoke(input, output, context): Promise<ProviderResult> {
    if (!input.resource) {
      return {
        status: "request-error",
        message: "Add a Claude account before selecting this model.",
      };
    }
    let data: AccountData;
    try {
      data = accountData(input.resource);
    } catch {
      const message = "Claude account credentials are incomplete. Sign in again.";
      return {
        status: "resource-error",
        message,
        patch: { state: { status: "invalid", message } },
      };
    }
    let patch: ResourcePatch | undefined;
    let refreshed = false;
    let emitted = false;
    const sink = {
      emit: (event: Parameters<typeof output.emit>[0]) => {
        emitted = true;
        output.emit(event);
      },
    };
    const refresh = async () => {
      data = await refreshTokens(data, context);
      patch = { privateData: privateData(data), state: { status: "ready" } };
      refreshed = true;
    };
    const failResource = (message: string, state: ResourceState): ProviderResult => ({
      status: "resource-error",
      message,
      patch: { ...patch, state },
    });
    try {
      context.signal.throwIfAborted();
      if (isExpiring(data)) await refresh();
      try {
        await streamMessages(
          input.model,
          input.request,
          apiHeaders(data.accessToken),
          sink,
          context,
        );
      } catch (error) {
        // A single retry is safe only before any streamed event and after an HTTP 401.
        if (
          !(error instanceof HttpError) || error.errorType !== undefined || error.status !== 401 ||
          emitted || refreshed
        ) throw error;
        await refresh();
        await streamMessages(
          input.model,
          input.request,
          apiHeaders(data.accessToken),
          sink,
          context,
        );
      }
      return { status: "completed", ...(patch ? { patch } : {}) };
    } catch (error) {
      if (error instanceof OAuthError && error.invalidCredentials) {
        return failResource(error.message, { status: "invalid", message: error.message });
      }
      if (error instanceof HttpError) {
        if (error.status === 401 || error.status === 403) {
          const message = error.status === 401
            ? "Claude authorization was rejected. Sign in again."
            : "Claude denied API access. Check your subscription; third-party OAuth may be blocked.";
          return failResource(message, { status: "invalid", message });
        }
        if (error.status === 429) {
          const message = "Claude usage is temporarily limited. Wait before trying again.";
          return failResource(message, {
            status: "cooling",
            retryAtMs: retryAtMs(error.headers),
            message,
          });
        }
      }
      const message = context.signal.aborted
        ? "Claude request was cancelled."
        : error instanceof OAuthError || error instanceof HttpError
        ? error.message
        : "Claude request failed or its response was incomplete. Check the connection and retry.";
      // Rotated credentials must survive upstream errors as well as successful requests.
      return { status: "request-error", message, ...(patch ? { patch } : {}) };
    }
  },
};
