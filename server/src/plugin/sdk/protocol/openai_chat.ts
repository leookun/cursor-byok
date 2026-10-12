import type { JsonValue, PluginContext } from "../plugin.ts";
import type { LlmContentPart, LlmRequest, ModelEvent, ProviderOutput } from "../provider.ts";

/** 本协议产生的回放状态种类;与宿主内置 Chat Provider 一致,可互相回放。 */
export const REPLAY_KIND = "openai_chat";

import { HttpError, providerEventError } from "../provider.ts";

export type OpenAiChatCall = {
  url: string;
  model: string;
  request: LlmRequest;
  headers?: Record<string, string>;
  /** 最后合并进请求体。 */
  extraBody?: Record<string, JsonValue>;
};

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function text(value: unknown): string | null {
  return typeof value === "string" ? value : null;
}

function count(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

/** 纯文本消息保持字符串形式;混合图片时展开为分块数组。 */
function chatContent(parts: LlmContentPart[]): JsonValue {
  if (parts.every((part) => part.type === "text")) {
    return parts.map((part) => part.type === "text" ? part.text : "").join("");
  }
  return parts.map((part): JsonValue =>
    part.type === "text" ? { type: "text", text: part.text } : {
      type: "image_url",
      image_url: { url: `data:${part.mediaType};base64,${part.dataBase64}` },
    }
  );
}

export function buildChatBody(call: OpenAiChatCall): Record<string, JsonValue> {
  const messages: JsonValue[] = [];
  if (call.request.instructions) {
    messages.push({ role: "system", content: call.request.instructions });
  }
  for (const message of call.request.messages) {
    if (message.role === "assistant") {
      const reasoning = message.replayState?.providerKind === REPLAY_KIND
        ? text(record(message.replayState.value)?.reasoning_content)
        : null;
      // Chat Completions 拒绝空字符串的 assistant content;完全无可见
      // 内容的 assistant 消息不需要发送。
      if (!message.text && message.toolCalls.length === 0 && !reasoning) continue;
      const value: Record<string, JsonValue> = {
        role: "assistant",
        content: message.text ? message.text : null,
      };
      if (reasoning) value.reasoning_content = reasoning;
      if (message.toolCalls.length > 0) {
        value.tool_calls = message.toolCalls.map((toolCall) => ({
          id: toolCall.callId,
          type: "function",
          function: { name: toolCall.name, arguments: JSON.stringify(toolCall.arguments) },
        }));
      }
      messages.push(value);
    } else if (message.role === "tool") {
      messages.push({
        role: "tool",
        content: message.parts.length === 0 ? message.content : chatContent(message.parts),
        tool_call_id: message.callId,
      });
    } else {
      messages.push({ role: message.role, content: chatContent(message.content) });
    }
  }
  const body: Record<string, JsonValue> = {
    model: call.model,
    messages,
    stream: true,
    stream_options: { include_usage: true },
  };
  if (call.request.tools.length > 0) {
    body.tools = call.request.tools.map((tool) => ({
      type: "function",
      function: { name: tool.name, description: tool.description, parameters: tool.parameters },
    }));
  }
  if (call.request.maxOutputTokens !== null) {
    body.max_completion_tokens = call.request.maxOutputTokens;
  }
  if (call.request.reasoning.effort !== null) {
    body.reasoning_effort = call.request.reasoning.effort;
  }
  if (call.request.latency === "fast") body.service_tier = "fast";
  if (call.request.cacheKey !== null) body.prompt_cache_key = call.request.cacheKey;
  return { ...body, ...call.extraBody };
}

type ToolState = {
  callId: string;
  name: string;
  arguments: string;
  emitted: number;
  started: boolean;
};

/** 部分上游会重发完整片段而不是增量;去重后再拼接。 */
function mergeFragment(target: string, fragment: string): string {
  if (target === fragment || target.endsWith(fragment)) return target;
  if (fragment.startsWith(target)) return fragment;
  return target + fragment;
}

function updateTool(
  index: number,
  callId: string | null,
  name: string | null,
  argumentsDelta: string | null,
  tools: Map<number, ToolState>,
): ModelEvent[] {
  let tool = tools.get(index);
  if (!tool) {
    tool = { callId: "", name: "", arguments: "", emitted: 0, started: false };
    tools.set(index, tool);
  }
  if (callId !== null) tool.callId = mergeFragment(tool.callId, callId);
  if (name !== null) tool.name = mergeFragment(tool.name, name);
  if (argumentsDelta !== null) tool.arguments += argumentsDelta;

  const events: ModelEvent[] = [];
  if (!tool.started && tool.callId && tool.name) {
    tool.started = true;
    events.push({ type: "tool-call-start", index, callId: tool.callId, name: tool.name });
  }
  if (tool.started && tool.emitted < tool.arguments.length) {
    events.push({
      type: "tool-call-arguments-delta",
      index,
      delta: tool.arguments.slice(tool.emitted),
    });
    tool.emitted = tool.arguments.length;
  }
  return events;
}

function eventError(value: Record<string, unknown>): string | null {
  const error = value.error;
  if (error === undefined || error === null) {
    return value.type === "error" ? text(value.message) ?? JSON.stringify(value) : null;
  }
  if (typeof error === "string") return error;
  return text(record(error)?.message) ?? JSON.stringify(error);
}

function usageEvent(value: unknown): ModelEvent {
  const usage = record(value) ?? {};
  return {
    type: "usage",
    usage: {
      inputTokens: count(usage.prompt_tokens),
      outputTokens: count(usage.completion_tokens),
      totalTokens: count(usage.total_tokens),
      cacheReadTokens: count(record(usage.prompt_tokens_details)?.cached_tokens),
      cacheWriteTokens: null,
      reasoningTokens: count(record(usage.completion_tokens_details)?.reasoning_tokens),
    },
  };
}

function mapFinish(reason: string, hasTools: boolean): "stop" | "length" | "tool-use" {
  if (reason === "tool_calls" || reason === "function_call") return "tool-use";
  if (reason === "length") return "length";
  if (reason === "stop" || reason === "content_filter") return "stop";
  return hasTools ? "tool-use" : "stop";
}

async function readBody(lines: AsyncIterable<string>): Promise<string> {
  const collected: string[] = [];
  for await (const line of lines) collected.push(line);
  return collected.join("\n");
}

/**
 * 执行一次 Chat Completions 流式调用,发出与宿主统一事件集一致的标准化事件,
 * 包括文本/思考边界与工具参数增量。非 2xx 响应抛出 `HttpError`,
 * 流内失败抛出 `Error`,由调用方分类额度与授权问题。
 */
export async function streamOpenAiChat(
  call: OpenAiChatCall,
  output: ProviderOutput,
  context: PluginContext,
): Promise<void> {
  const response = await context.network.stream(call.url, {
    method: "POST",
    headers: {
      accept: "text/event-stream",
      "content-type": "application/json",
      ...call.headers,
    },
    body: JSON.stringify(buildChatBody(call)),
  });
  if (response.status < 200 || response.status >= 300) {
    throw new HttpError(response.status, await readBody(response.lines), response.headers);
  }

  let textOpen = false;
  let thinkingOpen = false;
  let reasoning = "";
  const tools = new Map<number, ToolState>();
  let finalUsage: ModelEvent | null = null;
  let finish: "stop" | "length" | "tool-use" | null = null;
  let sawDoneMarker = false;

  for await (const line of response.lines) {
    if (!line.startsWith("data:")) continue;
    const payload = line.slice(5).trim();
    if (!payload) continue;
    if (payload === "[DONE]") {
      sawDoneMarker = true;
      break;
    }
    let value: Record<string, unknown>;
    try {
      value = record(JSON.parse(payload)) ?? {};
    } catch {
      throw new Error("OpenAI Chat SSE returned invalid JSON");
    }
    const error = eventError(value);
    if (error !== null) throw providerEventError(value, `OpenAI Chat error: ${error}`, response.headers);
    if (value.usage !== undefined && value.usage !== null) {
      finalUsage = usageEvent(value.usage);
    }
    const choice = Array.isArray(value.choices) ? record(value.choices[0]) : null;
    if (!choice) continue;
    const delta = record(choice.delta) ?? {};
    const reasoningDelta = text(delta.reasoning_content) ?? text(delta.reasoning);
    if (reasoningDelta) {
      if (!thinkingOpen) {
        thinkingOpen = true;
        output.emit({ type: "thinking-start" });
      }
      reasoning += reasoningDelta;
      output.emit({ type: "thinking-delta", text: reasoningDelta });
    }
    const content = text(delta.content);
    if (content) {
      if (thinkingOpen) {
        thinkingOpen = false;
        output.emit({ type: "thinking-end" });
      }
      if (!textOpen) {
        textOpen = true;
        output.emit({ type: "text-start" });
      }
      output.emit({ type: "text-delta", text: content });
    }
    if (Array.isArray(delta.tool_calls)) {
      for (const [position, rawTool] of delta.tool_calls.entries()) {
        const toolDelta = record(rawTool);
        if (!toolDelta) continue;
        const index = count(toolDelta.index) ?? position;
        const fn = record(toolDelta.function) ?? {};
        for (
          const event of updateTool(
            index,
            text(toolDelta.id),
            text(fn.name),
            text(fn.arguments),
            tools,
          )
        ) {
          output.emit(event);
        }
      }
    }
    const finishReason = text(choice.finish_reason);
    if (finishReason !== null) finish = mapFinish(finishReason, tools.size > 0);
  }

  if (thinkingOpen) output.emit({ type: "thinking-end" });
  if (textOpen) output.emit({ type: "text-end" });
  for (const [index, tool] of tools) {
    if (!tool.started) {
      if (!tool.name) throw new Error("OpenAI Chat tool call is missing name");
      if (!tool.callId) tool.callId = `call-${index}`;
      tool.started = true;
      output.emit({ type: "tool-call-start", index, callId: tool.callId, name: tool.name });
      if (tool.arguments) {
        tool.emitted = tool.arguments.length;
        output.emit({ type: "tool-call-arguments-delta", index, delta: tool.arguments });
      }
    }
    output.emit({ type: "tool-call-end", index });
  }
  if (finalUsage !== null) output.emit(finalUsage);
  if (reasoning) {
    output.emit({
      type: "replay-state",
      providerKind: REPLAY_KIND,
      value: { reasoning_content: reasoning },
    });
  }
  const reason = finish ??
    (sawDoneMarker ? (tools.size > 0 ? "tool-use" : "stop") : null);
  if (reason === null) {
    throw new Error("OpenAI Chat stream ended without finish_reason");
  }
  output.emit({ type: "done", reason });
}
