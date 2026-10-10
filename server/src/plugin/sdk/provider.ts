import type { JsonValue, LocalizedText, PluginContext } from "./plugin.ts";
import type { ModelSnapshot, ModelSupport } from "./model.ts";
import type { ResourcePatch, ResourceSnapshot } from "./resource.ts";

/**
 * LLM 请求契约。宿主把它的规范会话(ProjectedMessage)投影成这个形状;
 * 插件负责把它适配成上游 Provider 的协议。
 */
export type LlmContentPart =
  | { type: "text"; text: string }
  | { type: "image"; mediaType: string; dataBase64: string };

/** 不透明的 Provider 回放状态(如加密推理项);回放时按 providerKind 过滤。 */
export type LlmReplayState = {
  providerKind: string;
  value: JsonValue;
};

export type LlmToolCall = {
  /** 同一轮内的稳定序号。 */
  index: number;
  callId: string;
  name: string;
  /** 已解析的 JSON 参数。 */
  arguments: JsonValue;
};

export type LlmMessage =
  | { role: "system" | "user"; content: LlmContentPart[] }
  | {
    role: "assistant";
    text: string;
    thinking: string;
    replayState: LlmReplayState | null;
    toolCalls: LlmToolCall[];
  }
  | {
    role: "tool";
    callId: string;
    name: string;
    content: string;
    isError: boolean;
    /** 非空时优先于 content,承载图片等富工具结果。 */
    parts: LlmContentPart[];
  };

export type LlmTool = {
  name: string;
  description: string;
  /** 工具参数的 JSON Schema。 */
  parameters: JsonValue;
};

export type LlmRequest = {
  /** 系统指令;空字符串表示没有。 */
  instructions: string;
  messages: LlmMessage[];
  tools: LlmTool[];
  reasoning: { enabled: boolean; effort: string | null };
  latency: "fast" | "standard";
  maxOutputTokens: number | null;
  /** 会话级稳定缓存键,用于上游前缀缓存的路由亲和(如 prompt_cache_key)。 */
  cacheKey: string | null;
};

export type ModelUsage = {
  inputTokens: number | null;
  outputTokens: number | null;
  totalTokens: number | null;
  cacheReadTokens: number | null;
  cacheWriteTokens: number | null;
  reasoningTokens: number | null;
};

/**
 * 标准化输出契约,与宿主统一流事件一一对应。插件边接收上游数据边发出事件;
 * 文本、思考和每个工具调用都有显式的开始/结束边界,工具参数以增量交付。
 * 回放状态在流结束前发出一次,宿主存入 assistant 消息供下一轮回放。
 */
export type ModelEvent =
  | { type: "text-start" }
  | { type: "text-delta"; text: string }
  | { type: "text-end" }
  | { type: "thinking-start" }
  | { type: "thinking-delta"; text: string }
  | { type: "thinking-end" }
  | { type: "tool-call-start"; index: number; callId: string; name: string }
  | { type: "tool-call-arguments-delta"; index: number; delta: string }
  | { type: "tool-call-end"; index: number }
  | { type: "replay-state"; providerKind: string; value: JsonValue }
  | { type: "usage"; usage: ModelUsage }
  | { type: "done"; reason: "stop" | "length" | "tool-use" };

export type ProviderOutput = {
  emit(event: ModelEvent): void;
};

export type ProviderInvokeInput = {
  model: ModelSnapshot;
  /** 宿主为本次调用选中的资源;无资源 Provider 为 null。 */
  resource: ResourceSnapshot | null;
  request: LlmRequest;
};

/**
 * `resource-error` 把失败归因到选中的资源,宿主据此更新资源状态,
 * 并可在尚未发出任何事件时(未来)换一个资源重试。`patch` 同时用于
 * 持久化成功调用的副作用,例如刷新后的 access token。
 */
export type ProviderFailure = {
  kind: "rate_limit" | "transient" | "authorization" | "request";
  status?: number;
  retryAfterMs?: number;
};

/** Typed host/network failure; arbitrary plugin exceptions stay unclassified. */
export class ProviderError extends Error {
  constructor(message: string, readonly failure: ProviderFailure) {
    super(message);
  }
}

/** Convert only typed errors, never classify by exception message. */
export function providerFailure(error: unknown): ProviderFailure | undefined {
  return error instanceof ProviderError ? error.failure : undefined;
}

/** Preserve retry timing without inventing a delay when upstream omits it. */
export function retryAfterMs(headers: Record<string, string>, now = Date.now()): number | undefined {
  const normalized = Object.fromEntries(Object.entries(headers).map(([name, value]) =>
    [name.toLowerCase(), value]
  ));
  const retry = normalized["retry-after"]?.trim();
  if (retry) {
    if (/^\d+(?:\.\d+)?$/.test(retry)) {
      const delay = Math.ceil(Number(retry) * 1000);
      if (Number.isSafeInteger(delay)) return delay;
    }
    const date = Date.parse(retry);
    if (Number.isFinite(date)) return Math.max(0, date - now);
  }
  // Unix reset timestamps used by subscription APIs.
  const reset = normalized["x-ratelimit-reset"] ?? normalized["x-rate-limit-reset"];
  if (reset && /^\d+(?:\.\d+)?$/.test(reset)) {
    const timestamp = Number(reset) * 1000;
    if (Number.isFinite(timestamp)) return Math.max(0, Math.ceil(timestamp - now));
  }
  // Standard RateLimit-Reset is a delay in seconds, not an epoch timestamp.
  const delay = normalized["ratelimit-reset"];
  if (delay && /^\d+(?:\.\d+)?$/.test(delay) && Number.isFinite(Number(delay) * 1000)) {
    return Math.ceil(Number(delay) * 1000);
  }
  // OpenAI reports request/token resets as durations such as "1m2.5s".
  const durations = ["x-ratelimit-reset-requests", "x-ratelimit-reset-tokens"]
    .flatMap((name) => {
      const value = normalized[name];
      if (!value || !/^(?:\d+(?:\.\d+)?(?:ms|s|m|h))+$/.test(value)) return [];
      const units: Record<string, number> = { ms: 1, s: 1000, m: 60_000, h: 3_600_000 };
      const milliseconds = [...value.matchAll(/(\d+(?:\.\d+)?)(ms|s|m|h)/g)]
        .reduce((sum, match) => sum + Number(match[1]) * units[match[2]], 0);
      return Number.isSafeInteger(Math.ceil(milliseconds)) ? [Math.ceil(milliseconds)] : [];
    });
  return durations.length ? Math.max(...durations) : undefined;
}

/** Machine-readable upstream error envelopes, never human-readable messages. */
function eventFailure(value: unknown): ProviderFailure | undefined {
  const object = (value: unknown): Record<string, unknown> | undefined =>
    value !== null && typeof value === "object" && !Array.isArray(value)
      ? value as Record<string, unknown> : undefined;
  const root = object(value);
  const error = object(root?.error) ?? object(object(root?.response)?.error) ?? root;
  if (!error) return undefined;
  const codes = [error.code, error.type, error.status].filter((value): value is string => typeof value === "string");
  let kind: ProviderFailure["kind"] | undefined;
  for (const code of codes) {
    switch (code.toLowerCase()) {
      case "rate_limit_exceeded": case "rate_limit_reached": case "rate_limit_error":
      case "insufficient_quota": case "usage_limit_reached": case "quota_exceeded":
      case "resource_exhausted": case "too_many_requests": case "quota_exhausted":
      case "spending-limit": case "spending_limit":
        kind = "rate_limit"; break;
      case "authentication_error": case "authorization_error": case "invalid_api_key":
      case "unauthorized": case "unauthenticated": case "permission_denied": case "permission_error":
      case "access_denied": case "invalid_token": case "token_expired":
        kind = "authorization"; break;
      case "server_error": case "internal_error": case "internal_server_error": case "internal":
      case "overloaded_error": case "service_unavailable": case "unavailable":
      case "deadline_exceeded": case "timeout": case "api_error":
        kind = "transient"; break;
      case "invalid_request_error": case "invalid_request": case "bad_request":
      case "invalid_argument": case "context_length_exceeded": case "not_found":
        kind = "request"; break;
    }
    if (kind !== undefined) break;
  }
  const status = [error.status, error.status_code, error.code].find((value): value is number =>
    typeof value === "number" && Number.isInteger(value) && value >= 400 && value <= 599
  );
  if (kind === undefined && status !== undefined) kind = httpFailureKind(status);
  if (kind === undefined) return undefined;
  const milliseconds = error.retryAfterMs ?? error.retry_after_ms;
  const seconds = error.retry_after_seconds ?? error.reset_after_seconds;
  const delay = typeof milliseconds === "number" ? milliseconds :
    typeof seconds === "number" ? seconds * 1000 : undefined;
  return {
    kind,
    ...(status !== undefined ? { status } : {}),
    ...(delay !== undefined && Number.isSafeInteger(Math.ceil(delay)) && delay >= 0
      ? { retryAfterMs: Math.ceil(delay) } : {}),
  };
}

function httpFailureKind(status: number): ProviderFailure["kind"] {
  return status === 429 ? "rate_limit" :
    status === 401 || status === 403 ? "authorization" :
    status === 408 || (status >= 500 && status <= 599) ? "transient" : "request";
}

export function providerEventError(value: unknown, message: string, headers: Record<string, string> = {}): Error {
  const failure = eventFailure(value);
  if (failure === undefined) return new Error(message);
  const retry = retryAfterMs(headers) ?? failure.retryAfterMs;
  return new ProviderError(message, { ...failure, ...(retry !== undefined ? { retryAfterMs: retry } : {}) });
}

/** Build a failed result without inferring failure kinds from exception text. */
export function failedProviderResult(error: unknown): ProviderResult {
  const message = error instanceof Error ? error.message : String(error);
  const failure = providerFailure(error);
  if (failure?.kind === "authorization") {
    return { status: "resource-error", message, failure, patch: { state: { status: "invalid", message } } };
  }
  if (failure?.kind === "rate_limit") {
    return {
      status: "resource-error", message, failure,
      patch: { state: {
        status: "cooling", message,
        ...(failure.retryAfterMs !== undefined ? { retryAtMs: Date.now() + failure.retryAfterMs } : {}),
      } },
    };
  }
  return { status: "request-error", message, ...(failure !== undefined ? { failure } : {}) };
}

/** Non-2xx response with original headers, body and structured failure metadata. */
export class HttpError extends ProviderError {
  constructor(
    readonly status: number,
    readonly body: string,
    readonly headers: Record<string, string> = {},
  ) {
    let structured: ProviderFailure | undefined;
    try { structured = eventFailure(JSON.parse(body)); } catch { /* Non-JSON error bodies retain their HTTP classification. */ }
    const retry = retryAfterMs(headers) ?? structured?.retryAfterMs;
    super(`HTTP ${status}: ${body}`, {
      kind: structured?.kind ?? httpFailureKind(status),
      status,
      ...(retry !== undefined ? { retryAfterMs: retry } : {}),
    });
  }
}

export type ProviderResult =
  | { status: "completed"; patch?: ResourcePatch }
  | { status: "resource-error"; message: string; patch: ResourcePatch; failure?: ProviderFailure }
  | { status: "request-error"; message: string; patch?: ResourcePatch; failure?: ProviderFailure };

export type ProviderSupport = {
  id: string;
  displayName: LocalizedText;
  description?: LocalizedText;
  /** 产品身份,用于归类与图标,如 "openai"。 */
  providerType: string;
  /** 每次调用消费的资源类型;无资源 Provider 可省略。 */
  resourceType?: string;
  models?: ModelSupport;
  invoke(
    input: ProviderInvokeInput,
    output: ProviderOutput,
    context: PluginContext,
  ): Promise<ProviderResult>;
};
