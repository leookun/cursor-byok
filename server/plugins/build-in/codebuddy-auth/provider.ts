import type {
  ProviderInvokeInput,
  ProviderOutput,
  ProviderResult,
  ProviderSupport,
} from "cursor-byok:provider";
import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import { HttpError, streamOpenAiChat } from "cursor-byok:protocol/openai-chat";
import {
  type AccountData,
  accountData,
  accountHeaders,
  quotaExhaustedPatch,
  refreshAccessToken,
  RESOURCE_TYPE,
  shouldRefresh,
} from "./resources.ts";

const CHAT_URL = "https://copilot.tencent.com/v2/chat/completions";

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

function stringList(value: unknown): string[] {
  return Array.isArray(value)
    ? value.flatMap((entry) => typeof entry === "string" && entry.trim() ? [entry.trim()] : [])
    : [];
}

export function isQuotaError(error: string): boolean {
  const message = error.toLowerCase();
  return message.includes("insufficient_quota") ||
    message.includes("quota_exceeded") ||
    message.includes("credits exhausted") ||
    message.includes("credit balance") ||
    /"code"\s*:\s*(14003|6001|6002|6005|6006)/.test(message) ||
    message.includes("积分不足") ||
    message.includes("额度不足") ||
    message.includes("余额不足");
}

function isQuotaHttpError(error: HttpError): boolean {
  return error.status === 429 || isQuotaError(error.body);
}

function isAuthorizationHttpError(error: HttpError): boolean {
  if (error.status === 401) return true;
  if (error.status !== 403) return false;
  const body = error.body.toLowerCase();
  return body.includes("unauthorized") || body.includes("authentication") ||
    body.includes("authorization") || body.includes("token") || body.includes("登录") ||
    body.includes("鉴权");
}

function invalidResult(message: string): ProviderResult {
  return {
    status: "resource-error",
    message,
    patch: { state: { status: "invalid", message: "CodeBuddy authorization expired" } },
  };
}

function modelRequest(input: ProviderInvokeInput) {
  const privateData = object(input.model.privateData) ?? {};
  const supportsReasoning = privateData.supportsReasoning === true;
  const canDisableThinking = privateData.canDisableThinking === true;
  const reasoningEnabled = supportsReasoning &&
    (input.request.reasoning.enabled || !canDisableThinking);
  const supportedEfforts = stringList(privateData.reasoningEfforts);
  const defaultEffort = text(privateData.defaultReasoningEffort);
  const requestedEffort = input.request.reasoning.effort;
  const effort = reasoningEnabled
    ? requestedEffort && supportedEfforts.includes(requestedEffort)
      ? requestedEffort
      : defaultEffort
    : null;
  const summary = text(privateData.reasoningSummary);
  return {
    request: {
      ...input.request,
      reasoning: { enabled: reasoningEnabled, effort },
      // CodeBuddy uses session headers for affinity and does not expose service_tier.
      latency: "standard" as const,
      cacheKey: null,
    },
    extraBody: {
      ...(reasoningEnabled && summary ? { reasoning_summary: summary } : {}),
      ...(number(privateData.temperature) !== null
        ? { temperature: number(privateData.temperature)! }
        : {}),
      ...(number(privateData.topP) !== null ? { top_p: number(privateData.topP)! } : {}),
    } as Record<string, JsonValue>,
  };
}

function requestHeaders(data: AccountData, input: ProviderInvokeInput): Record<string, string> {
  const requestId = crypto.randomUUID().replaceAll("-", "");
  const conversationId = input.request.cacheKey ?? requestId;
  return {
    ...accountHeaders(data),
    "X-Conversation-ID": conversationId,
    "X-Conversation-Request-ID": requestId,
    "X-Conversation-Message-ID": requestId,
    "X-Request-ID": requestId,
    "X-Agent-Intent": "craft",
    "X-Agent-Type": "main",
    "X-IDE-Type": "Cursor",
    "X-IDE-Name": "Cursor BYOK",
    "X-IDE-Version": "0.1.7",
    "X-Private-Data": "false",
    "X-Model-ID": input.model.id,
  };
}

async function streamModel(
  data: AccountData,
  input: ProviderInvokeInput,
  output: ProviderOutput,
  context: PluginContext,
): Promise<void> {
  const model = modelRequest(input);
  await streamOpenAiChat(
    {
      url: CHAT_URL,
      model: input.model.id,
      request: model.request,
      headers: requestHeaders(data, input),
      extraBody: model.extraBody,
    },
    output,
    context,
  );
}

async function invoke(
  input: ProviderInvokeInput,
  output: ProviderOutput,
  context: PluginContext,
): Promise<ProviderResult> {
  if (!input.resource) {
    return { status: "request-error", message: "sign in to CodeBuddy before calling models" };
  }
  let data: AccountData;
  try {
    data = accountData(input.resource);
  } catch (error) {
    return invalidResult(error instanceof Error ? error.message : String(error));
  }

  let patchData: AccountData | null = null;
  if (shouldRefresh(data)) {
    try {
      data = await refreshAccessToken(data, context);
      patchData = data;
    } catch {
      // The existing access token may still be accepted; retry refresh on an auth response.
    }
  }

  try {
    await streamModel(data, input, output, context);
    return patchData
      ? { status: "completed", patch: { privateData: patchData as unknown as JsonValue } }
      : { status: "completed" };
  } catch (error) {
    if (error instanceof HttpError) {
      if (
        !isQuotaHttpError(error) && (error.status === 401 || error.status === 403) &&
        data.refreshToken
      ) {
        try {
          const freshData = await refreshAccessToken(data, context);
          await streamModel(freshData, input, output, context);
          return {
            status: "completed",
            patch: {
              privateData: freshData as unknown as JsonValue,
              state: { status: "ready" },
            },
          };
        } catch (retryError) {
          if (retryError instanceof HttpError && isQuotaHttpError(retryError)) {
            return {
              status: "resource-error",
              message: retryError.message,
              patch: quotaExhaustedPatch(data),
            };
          }
        }
      }
      if (isQuotaHttpError(error)) {
        return {
          status: "resource-error",
          message: error.message,
          patch: quotaExhaustedPatch(data),
        };
      }
      if (isAuthorizationHttpError(error)) return invalidResult(error.message);
      return {
        status: "request-error",
        message: error.message,
        ...(patchData ? { patch: { privateData: patchData as unknown as JsonValue } } : {}),
      };
    }
    const message = error instanceof Error ? error.message : String(error);
    if (isQuotaError(message)) {
      return { status: "resource-error", message, patch: quotaExhaustedPatch(data) };
    }
    return {
      status: "request-error",
      message,
      ...(patchData ? { patch: { privateData: patchData as unknown as JsonValue } } : {}),
    };
  }
}

export const codeBuddyProvider: ProviderSupport = {
  id: "codebuddy",
  displayName: "Tencent CodeBuddy",
  description: {
    "en-US": "Use CodeBuddy China account credits through the CodeBuddy CLI model service.",
    "zh-CN": "通过 CodeBuddy CLI 模型服务使用国内版账号积分。",
  },
  providerType: "tencent-codebuddy",
  resourceType: RESOURCE_TYPE,
  invoke,
};
