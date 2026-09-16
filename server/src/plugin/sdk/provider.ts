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
export type ProviderResult =
  | { status: "completed"; patch?: ResourcePatch }
  | { status: "auth-error"; message: string }
  | { status: "resource-error"; message: string; patch: ResourcePatch }
  | { status: "request-error"; message: string; patch?: ResourcePatch };

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
