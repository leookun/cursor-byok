import type {
  ProviderInvokeInput,
  ProviderOutput,
  ProviderResult,
  ProviderSupport,
} from "cursor-byok:provider";
import type { PluginContext } from "cursor-byok:plugin";
import { HttpError, failedProviderResult } from "cursor-byok:provider";
import { streamOpenAiChat } from "cursor-byok:protocol/openai-chat";
import { grokModels } from "./models.ts";
import { type AccountData, accountData, quotaExhaustedPatch, RESOURCE_TYPE } from "./resources.ts";

const CHAT_URL = "https://api.x.ai/v1/chat/completions";

function isQuotaHttpError(error: HttpError): boolean {
  return error.failure.kind === "rate_limit";
}

function invalidResult(message: string, stateMessage: string): Extract<ProviderResult, { status: "resource-error" }> {
  return {
    status: "resource-error",
    message,
    patch: { state: { status: "invalid", message: stateMessage } },
  };
}

async function invoke(
  input: ProviderInvokeInput,
  output: ProviderOutput,
  context: PluginContext,
): Promise<ProviderResult> {
  if (!input.resource) {
    return { status: "request-error", message: "add a Grok account before calling Grok" };
  }
  let data: AccountData;
  try {
    data = accountData(input.resource);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    return invalidResult(message, message);
  }
  try {
    await streamOpenAiChat(
      {
        url: CHAT_URL,
        model: input.model.id,
        // xAI 不接受 reasoning_effort 与 service_tier;思考由模型自身决定。
        request: {
          ...input.request,
          reasoning: { enabled: false, effort: null },
          latency: "standard",
        },
        headers: { authorization: `Bearer ${data.accessToken}` },
      },
      output,
      context,
    );
    return { status: "completed" };
  } catch (error) {
    if (error instanceof HttpError) {
      if ((error.status === 401 || error.status === 403) && !isQuotaHttpError(error)) {
        return {
          ...invalidResult(error.message, "Grok authorization expired; sign in again"),
          failure: error.failure,
        };
      }
      if (isQuotaHttpError(error)) {
        return {
          status: "resource-error",
          message: error.message,
          failure: { ...error.failure, kind: "rate_limit" },
          patch: quotaExhaustedPatch(data),
        };
      }
      return failedProviderResult(error);
    }
    return failedProviderResult(error);
  }
}

export const grokProvider: ProviderSupport = {
  id: "grok",
  displayName: "xAI Grok",
  description: {
    "en-US": "SuperGrok subscription access through the official Grok CLI endpoint.",
    "zh-CN": "通过官方 Grok CLI 接口使用 SuperGrok 订阅。",
  },
  providerType: "xai",
  resourceType: RESOURCE_TYPE,
  models: grokModels,
  invoke,
};
