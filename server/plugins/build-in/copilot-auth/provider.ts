import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import type {
  LlmMessage,
  LlmRequest,
  ProviderInvokeInput,
  ProviderOutput,
  ProviderResult,
  ProviderSupport,
} from "cursor-byok:provider";
import type { ResourcePatch } from "cursor-byok:resource";
import { HttpError as ChatHttpError, streamOpenAiChat } from "cursor-byok:protocol/openai-chat";
import {
  HttpError as ResponsesHttpError,
  streamOpenAiResponses,
} from "cursor-byok:protocol/openai-responses";
import { copilotChatHeaders, type Initiator } from "./constants.ts";
import { copilotModels, type ModelRoute, modelRoute, reasoningEfforts } from "./models.ts";
import {
  type AccountData,
  accountData,
  type ActiveToken,
  ensureToken,
  RESOURCE_TYPE,
} from "./resources.ts";
import { AUTH_EXPIRED_MESSAGE, CopilotAuthError } from "./token.ts";

const MAX_ATTEMPTS = 3;
const RETRY_BASE_DELAY_MS = 500;
const ONE_HOUR_MS = 60 * 60 * 1000;

type HttpError = ChatHttpError | ResponsesHttpError;

function object(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function text(value: unknown): string | null {
  return typeof value === "string" && value.trim() ? value.trim() : null;
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/**
 * 计费关键:最后一条是 user(或没有消息)时由用户发起,消耗 premium request;
 * 最后一条是 tool / assistant 时是 Agent 自动续跑的回合,标记为 agent 不额外计费。
 */
export function initiator(messages: LlmMessage[]): Initiator {
  const last = messages.at(-1);
  return last?.role === "tool" || last?.role === "assistant" ? "agent" : "user";
}

export function hasImages(messages: LlmMessage[]): boolean {
  return messages.some((message) => {
    if (message.role === "assistant") return false;
    const parts = message.role === "tool" ? message.parts : message.content;
    return parts.some((part) => part.type === "image");
  });
}

/**
 * Copilot 不支持 service_tier,latency 一律降为 standard;只保留模型声明的推理档位。
 * Chat 路径沿用 max_tokens,且暂不发送 prompt_cache_key;Responses 路径与 VS Code 一致发送 store: false。
 */
export function copilotRequest(
  request: LlmRequest,
  route: ModelRoute,
  efforts: string[],
): { request: LlmRequest; extraBody: Record<string, JsonValue> } {
  const effort = request.reasoning.effort !== null && efforts.includes(request.reasoning.effort)
    ? request.reasoning.effort
    : null;
  const adjusted: LlmRequest = {
    ...request,
    reasoning: { enabled: request.reasoning.enabled, effort },
    latency: "standard",
  };
  if (route === "responses") return { request: adjusted, extraBody: { store: false } };
  return {
    request: { ...adjusted, maxOutputTokens: null, cacheKey: null },
    extraBody: request.maxOutputTokens !== null ? { max_tokens: request.maxOutputTokens } : {},
  };
}

/** Copilot 边缘限流会返回空 body 或只有 "forbidden" 的 403,可以重试。 */
export function isBareForbidden(body: string): boolean {
  const trimmed = body.trim();
  let message = trimmed;
  try {
    const root = object(JSON.parse(trimmed));
    message = text(object(root?.error)?.message) ?? text(root?.error) ?? text(root?.message) ??
      trimmed;
  } catch {
    message = trimmed;
  }
  return message === "" || /^forbidden[.!]?$/i.test(message);
}

function isRetryable(error: HttpError): boolean {
  const status = error.status;
  return status === 408 || status === 425 || status === 429 || status >= 500 ||
    (status === 403 && isBareForbidden(error.body));
}

function isQuotaError(error: HttpError): boolean {
  if (error.status !== 429) return false;
  const body = error.body.toLowerCase();
  return body.includes("quota") || body.includes("premium") || body.includes("exceeded");
}

function httpError(error: unknown): HttpError | null {
  return error instanceof ChatHttpError || error instanceof ResponsesHttpError ? error : null;
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(signal.reason);
      return;
    }
    const onAbort = () => {
      clearTimeout(timer);
      reject(signal.reason);
    };
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

function invalidResult(message: string, stateMessage: string): ProviderResult {
  return {
    status: "resource-error",
    message,
    patch: { state: { status: "invalid", message: stateMessage } },
  };
}

function tokenFailure(error: unknown): ProviderResult {
  if (error instanceof CopilotAuthError) return invalidResult(error.message, error.message);
  return { status: "request-error", message: errorText(error) };
}

function classify(error: HttpError, data: AccountData): ProviderResult {
  if (error.status === 401) return invalidResult(error.message, AUTH_EXPIRED_MESSAGE);
  if (isQuotaError(error)) {
    const now = Date.now();
    const resetAtMs = data.quota?.resetAtMs ?? null;
    return {
      status: "resource-error",
      message: error.message,
      patch: {
        state: {
          status: "cooling",
          retryAtMs: resetAtMs !== null && resetAtMs > now ? resetAtMs : now + ONE_HOUR_MS,
          message: "Copilot premium requests are exhausted",
        },
      },
    };
  }
  // 带具体原因的 403(模型策略未启用等)只影响本次请求,不能把账号标为 invalid。
  return { status: "request-error", message: error.message };
}

async function invoke(
  input: ProviderInvokeInput,
  output: ProviderOutput,
  context: PluginContext,
): Promise<ProviderResult> {
  if (!input.resource) {
    return {
      status: "request-error",
      message: "add a GitHub Copilot account before calling Copilot",
    };
  }
  let data: AccountData;
  try {
    data = accountData(input.resource);
  } catch (error) {
    const message = errorText(error);
    return invalidResult(message, message);
  }
  const route = modelRoute(input.model);
  if (!route) {
    return { status: "request-error", message: "Copilot model metadata is outdated; sync models" };
  }

  let active: ActiveToken;
  try {
    active = await ensureToken(data, context);
  } catch (error) {
    return tokenFailure(error);
  }
  data = active.data;
  let dirty = active.refreshed;
  // 续期后的 token 随每个返回分支写回资源,避免下次调用重复交换。
  const finish = (result: ProviderResult): ProviderResult => {
    if (!dirty) return result;
    const patch: ResourcePatch = { ...result.patch, privateData: data as unknown as JsonValue };
    return { ...result, patch } as ProviderResult;
  };

  const { request, extraBody } = copilotRequest(
    input.request,
    route,
    reasoningEfforts(input.model),
  );
  const messages = input.request.messages;
  let reexchanged = false;
  for (let attempt = 1;; attempt++) {
    const call = {
      url: `${active.apiBase}/${route === "responses" ? "responses" : "chat/completions"}`,
      model: input.model.id,
      request,
      headers: copilotChatHeaders({
        copilotToken: active.token,
        deviceId: data.deviceId,
        cacheKey: input.request.cacheKey,
        initiator: initiator(messages),
        vision: hasImages(messages),
      }),
      extraBody,
    };
    try {
      if (route === "responses") await streamOpenAiResponses(call, output, context);
      else await streamOpenAiChat(call, output, context);
      return finish({ status: "completed" });
    } catch (error) {
      const failure = httpError(error);
      // 流内错误可能已发出部分事件,不能重试。
      if (!failure) return finish({ status: "request-error", message: errorText(error) });
      // HttpError 一定发生在任何事件之前,重试是安全的。
      if (failure.status === 401 && !reexchanged) {
        reexchanged = true;
        try {
          active = await ensureToken(data, context, true);
        } catch (renewError) {
          return tokenFailure(renewError);
        }
        data = active.data;
        dirty = true;
        continue;
      }
      if (attempt < MAX_ATTEMPTS && isRetryable(failure)) {
        try {
          await sleep(RETRY_BASE_DELAY_MS * 2 ** (attempt - 1), context.signal);
        } catch {
          return finish({ status: "request-error", message: failure.message });
        }
        continue;
      }
      return finish(classify(failure, data));
    }
  }
}

export const copilotProvider: ProviderSupport = {
  id: "copilot",
  displayName: "GitHub Copilot",
  description: {
    "en-US": "GitHub Copilot subscription access through the Copilot Chat API.",
    "zh-CN": "通过 Copilot Chat API 使用 GitHub Copilot 订阅。",
  },
  providerType: "github",
  resourceType: RESOURCE_TYPE,
  models: copilotModels,
  invoke,
};
