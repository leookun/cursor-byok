import type {
  ProviderInvokeInput,
  ProviderOutput,
  ProviderResult,
  ProviderSupport,
} from "cursor-byok:provider";
import type { PluginContext } from "cursor-byok:plugin";
import { HttpError, failedProviderResult } from "cursor-byok:provider";
import { streamOpenAiResponses } from "cursor-byok:protocol/openai-responses";
import { codexModels, reasoningEfforts } from "./models.ts";
import {
  type AccountData,
  accountData,
  chatGptAccountId,
  quotaExhaustedPatch,
  RESOURCE_TYPE,
} from "./resources.ts";

const RESPONSES_URL = "https://chatgpt.com/backend-api/codex/responses";

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

function headers(data: AccountData, cacheKey: string | null): Record<string, string> {
  const result: Record<string, string> = {
    authorization: `Bearer ${data.accessToken}`,
    originator: "codex_cli_rs",
  };
  const accountId = chatGptAccountId(data.accessToken);
  if (accountId) result["ChatGPT-Account-Id"] = accountId;
  // Codex 后端的缓存亲和契约:session-id / thread-id / prompt_cache_key
  // 三者同源(见 codex-rs client.rs);缺头会导致请求落在随机分片上。
  if (cacheKey !== null) {
    result["session-id"] = cacheKey;
    result["thread-id"] = cacheKey;
    result["x-client-request-id"] = cacheKey;
  }
  return result;
}

async function invoke(
  input: ProviderInvokeInput,
  output: ProviderOutput,
  context: PluginContext,
): Promise<ProviderResult> {
  if (!input.resource) {
    return { status: "request-error", message: "add a ChatGPT account before calling Codex" };
  }
  let data: AccountData;
  try {
    data = accountData(input.resource);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    return invalidResult(message, message);
  }
  const efforts = reasoningEfforts(input.model);
  const reasoning = input.request.reasoning;
  const effort = reasoning.effort !== null && efforts.includes(reasoning.effort)
    ? reasoning.effort
    : null;
  try {
    await streamOpenAiResponses(
      {
        url: RESPONSES_URL,
        model: input.model.id,
        // Codex 订阅端点不接受 max_output_tokens;fast 档位经协议库映射为
        // service_tier: "priority" 后透传。
        request: {
          ...input.request,
          reasoning: { enabled: reasoning.enabled, effort },
          maxOutputTokens: null,
        },
        headers: headers(data, input.request.cacheKey),
        extraBody: { store: false },
      },
      output,
      context,
    );
    return { status: "completed" };
  } catch (error) {
    if (error instanceof HttpError) {
      if (error.status === 401) {
        return {
          ...invalidResult(error.message, "ChatGPT authorization expired; sign in again"),
          failure: error.failure,
        };
      }
      if (isQuotaHttpError(error)) {
        return {
          status: "resource-error",
          message: error.message,
          failure: { ...error.failure, kind: "rate_limit" },
          patch: quotaExhaustedPatch(data, error.body),
        };
      }
      return failedProviderResult(error);
    }
    return failedProviderResult(error);
  }
}

export const codexProvider: ProviderSupport = {
  id: "codex",
  displayName: "OpenAI Codex",
  description: {
    "en-US": "ChatGPT subscription access through the official Codex Responses API.",
    "zh-CN": "通过官方 Codex Responses API 使用 ChatGPT 订阅。",
  },
  providerType: "openai",
  resourceType: RESOURCE_TYPE,
  models: codexModels,
  invoke,
};
