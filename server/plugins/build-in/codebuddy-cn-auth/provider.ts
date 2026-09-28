import type { ModelSnapshot } from "cursor-byok:model";
import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import type {
  LlmRequest,
  ProviderInvokeInput,
  ProviderOutput,
  ProviderResult,
  ProviderSupport,
} from "cursor-byok:provider";
import { HttpError, type OpenAiChatCall, streamOpenAiChat } from "cursor-byok:protocol/openai-chat";
import { codeBuddyModels, reasoningEfforts } from "./models.ts";
import {
  type AccountData,
  accountData,
  accountHeaders,
  CodeBuddyHttpError,
  ensureFreshAccount,
  isAuthStatus,
  quotaExhaustedPatch,
  RESOURCE_TYPE,
} from "./resources.ts";

const CHAT_URL = "https://copilot.tencent.com/v2/chat/completions";
const NEUTRAL_INSTRUCTIONS =
  "You are a helpful AI assistant that helps with software engineering tasks.";

function isReasoningOff(value: string | null): boolean {
  if (value === null) return true;
  const normalized = value.trim().toLowerCase();
  return normalized === "none" || normalized === "off" || normalized === "disabled" ||
    normalized === "";
}

function effortRank(value: string): number {
  const normalized = value.toLowerCase();
  const aliases: Record<string, number> = {
    minimal: 0,
    low: 1,
    medium: 2,
    high: 3,
    xhigh: 4,
    max: 5,
    maximum: 5,
  };
  return aliases[normalized] ?? Number(normalized);
}

function chooseEffort(requested: string, supported: string[]): string {
  if (supported.includes(requested)) return requested;
  const requestedRank = effortRank(requested);
  const lower = supported.filter((effort) => effortRank(effort) <= requestedRank);
  if (lower.length > 0) return lower.sort((left, right) => effortRank(right) - effortRank(left))[0];
  return supported[0];
}

function selectedReasoningEffort(request: LlmRequest, model: ModelSnapshot): string | null {
  const requested = request.reasoning.effort;
  if (!request.reasoning.enabled || isReasoningOff(requested)) return null;
  const supported = reasoningEfforts(model);
  return supported.length > 0 && requested !== null
    ? chooseEffort(requested, supported)
    : requested;
}

function hasAgentIdentity(instructions: string): boolean {
  if (instructions.length > 2000) return true;
  return /you\s+are\s+(?:an?\s+)?(?:ai\s+)?coding\s+assistant.*(?:cursor|claude\s+code|windsurf|cline|aider|continue|copilot|cody)/i
    .test(
      instructions,
    ) ||
    /you\s+operate\s+in\s+(?:cursor|claude\s+code|windsurf|cline|aider|continue|copilot|cody)/i
      .test(instructions) ||
    /you\s+are\s+(?:cursor|claude\s+code|windsurf|cline|aider|continue|copilot|cody)/i.test(
      instructions,
    ) ||
    /<agent-identity>|<role>|<behavior_instructions>/i.test(instructions) ||
    /ohmyopencode/i.test(instructions) ||
    /\b(?:windsurf|cline|aider|continue|copilot|cody)\b[^\n]*(?:agent|assistant)/i.test(
      instructions,
    );
}

function providerInstructions(instructions: string): string {
  return hasAgentIdentity(instructions) ? NEUTRAL_INSTRUCTIONS : instructions;
}

function providerHeaders(data: AccountData): Record<string, string> {
  return {
    ...accountHeaders(data),
    Accept: "text/event-stream",
    "Content-Type": "application/json",
    "x-codebuddy-request": "1",
    Authorization: `Bearer ${data.accessToken}`,
  };
}

function isQuotaText(value: string): boolean {
  const message = value.toLowerCase();
  return message.includes("429") || message.includes("quota") ||
    message.includes("resource_exhausted") ||
    message.includes("insufficient") || message.includes("rate limit") ||
    message.includes("rate_limit") ||
    message.includes("too many requests") || message.includes("out of credits") ||
    message.includes("spending-limit") || message.includes("欠费");
}

function isQuotaHttpError(error: HttpError): boolean {
  return error.status === 429 || isQuotaText(error.body);
}

function invalidResult(message: string): ProviderResult {
  return {
    status: "resource-error",
    message,
    patch: {
      state: { status: "invalid", message: "CodeBuddy authorization expired; sign in again" },
    },
  };
}

async function invoke(
  input: ProviderInvokeInput,
  output: ProviderOutput,
  context: PluginContext,
): Promise<ProviderResult> {
  if (!input.resource) {
    return {
      status: "request-error",
      message: "add a CodeBuddy CN account before calling CodeBuddy",
    };
  }
  let data: AccountData;
  try {
    data = accountData(input.resource);
  } catch (error) {
    return invalidResult(error instanceof Error ? error.message : String(error));
  }

  let refreshed = false;
  try {
    const fresh = await ensureFreshAccount(data, context);
    data = fresh.data;
    refreshed = fresh.refreshed;
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    if (error instanceof CodeBuddyHttpError && isAuthStatus(error.status)) {
      return invalidResult(message);
    }
    return { status: "request-error", message };
  }

  const effort = selectedReasoningEffort(input.request, input.model);
  const request: LlmRequest = {
    ...input.request,
    instructions: providerInstructions(input.request.instructions),
    reasoning: { enabled: effort !== null, effort },
    latency: "standard",
    maxOutputTokens: null,
  };
  const call: OpenAiChatCall = {
    url: CHAT_URL,
    model: input.model.id,
    request,
    headers: providerHeaders(data),
    ...(effort !== null ? { extraBody: { reasoning_summary: "auto" } } : {}),
  };

  try {
    await streamOpenAiChat(call, output, context);
    return {
      status: "completed",
      ...(refreshed ? { patch: { privateData: data as unknown as JsonValue } } : {}),
    };
  } catch (error: unknown) {
    const httpError: HttpError | null = error instanceof HttpError ? error : null;
    if (
      httpError && (httpError.status === 401 || httpError.status === 403) &&
      !isQuotaHttpError(httpError) && !refreshed && data.refreshToken
    ) {
      let retryData: AccountData | null = null;
      try {
        const retry = await ensureFreshAccount({ ...data, expiresAtMs: Date.now() - 1 }, context);
        retryData = retry.data;
        const retryCall: OpenAiChatCall = {
          ...call,
          headers: providerHeaders(retry.data),
        };
        await streamOpenAiChat(retryCall, output, context);
        return {
          status: "completed",
          patch: { privateData: retry.data as unknown as JsonValue },
        };
      } catch (retryError: unknown) {
        const retryHttpError: HttpError | null = retryError instanceof HttpError
          ? retryError
          : null;
        if (retryHttpError && isQuotaHttpError(retryHttpError)) {
          return {
            status: "resource-error",
            message: retryHttpError.message,
            patch: quotaExhaustedPatch(retryData ?? data, retryHttpError.body),
          };
        }
        if (
          retryHttpError &&
          (retryHttpError.status === 401 || retryHttpError.status === 403)
        ) {
          return invalidResult(retryHttpError.message);
        }
        if (retryError instanceof CodeBuddyHttpError && isAuthStatus(retryError.status)) {
          return invalidResult(retryError.message);
        }
        const message = retryError instanceof Error ? retryError.message : String(retryError);
        return isQuotaText(message)
          ? { status: "resource-error", message, patch: quotaExhaustedPatch(retryData ?? data) }
          : { status: "request-error", message };
      }
    }

    if (httpError) {
      if ((httpError.status === 401 || httpError.status === 403) && !isQuotaHttpError(httpError)) {
        return invalidResult(httpError.message);
      }
      if (isQuotaHttpError(httpError)) {
        return {
          status: "resource-error",
          message: httpError.message,
          patch: quotaExhaustedPatch(data, httpError.body),
        };
      }
      return { status: "request-error", message: httpError.message };
    }
    const message = error instanceof Error ? error.message : String(error);
    if (isQuotaText(message)) {
      return { status: "resource-error", message, patch: quotaExhaustedPatch(data, message) };
    }
    return { status: "request-error", message };
  }
}

export const codeBuddyProvider: ProviderSupport = {
  id: "codebuddy-cn",
  displayName: "CodeBuddy CN",
  description: {
    "en-US": "CodeBuddy CN chat models through the Tencent CodeBuddy gateway.",
    "zh-CN": "通过腾讯 CodeBuddy 网关使用 CodeBuddy CN 对话模型。",
  },
  providerType: "tencent",
  resourceType: RESOURCE_TYPE,
  models: codeBuddyModels,
  invoke,
};
