import type { JsonValue, PluginContext } from "../plugin.ts";
import type { LlmContentPart, LlmRequest, ModelEvent, ProviderOutput } from "../provider.ts";

/** 本协议产生的回放状态种类;与宿主内置 Responses Provider 一致,可互相回放。 */
export const REPLAY_KIND = "openai_responses";

import { HttpError, providerEventError } from "../provider.ts";

export type OpenAiResponsesCall = {
  url: string;
  model: string;
  request: LlmRequest;
  headers?: Record<string, string>;
  /** 最后合并进请求体,如 { store: false }。 */
  extraBody?: Record<string, JsonValue>;
};

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null;
}

function text(value: unknown): string | null {
  return typeof value === "string" ? value : null;
}

function count(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function contentParts(parts: LlmContentPart[], textType: "input_text" | "output_text"): JsonValue[] {
  const content: JsonValue[] = [];
  for (const part of parts) {
    if (part.type === "text") {
      if (part.text) content.push({ type: textType, text: part.text });
    } else {
      content.push({
        type: "input_image",
        detail: "auto",
        image_url: `data:${part.mediaType};base64,${part.dataBase64}`,
      });
    }
  }
  return content;
}

function replayItems(value: JsonValue): JsonValue[] {
  const items = record(value)?.items;
  if (!Array.isArray(items)) {
    throw new Error("OpenAI Responses replay state is missing items");
  }
  return items.map((item) => {
    const source = record(item);
    if (source?.type !== "reasoning") {
      throw new Error("OpenAI Responses replay state contains a non-reasoning item");
    }
    const projected: Record<string, JsonValue> = { type: "reasoning" };
    for (const field of ["id", "summary", "content", "encrypted_content"] as const) {
      if (field in source) projected[field] = source[field] as JsonValue;
    }
    return projected;
  });
}

export function buildResponsesBody(call: OpenAiResponsesCall): Record<string, JsonValue> {
  const input: JsonValue[] = [];
  for (const message of call.request.messages) {
    if (message.role === "assistant") {
      if (message.replayState?.providerKind === REPLAY_KIND) {
        input.push(...replayItems(message.replayState.value));
      }
      if (message.text) {
        input.push({
          type: "message",
          role: "assistant",
          content: [{ type: "output_text", text: message.text }],
        });
      }
      for (const toolCall of message.toolCalls) {
        input.push({
          type: "function_call",
          call_id: toolCall.callId,
          name: toolCall.name,
          arguments: JSON.stringify(toolCall.arguments),
        });
      }
    } else if (message.role === "tool") {
      input.push({
        type: "function_call_output",
        call_id: message.callId,
        output: message.parts.length === 0 ? message.content : contentParts(message.parts, "input_text"),
      });
    } else {
      const content = contentParts(message.content, "input_text");
      if (content.length > 0) input.push({ type: "message", role: message.role, content });
    }
  }
  const body: Record<string, JsonValue> = {
    model: call.model,
    input,
    stream: true,
    instructions: call.request.instructions,
    include: ["reasoning.encrypted_content"],
  };
  if (call.request.tools.length > 0) {
    body.tools = call.request.tools.map((tool) => ({
      type: "function",
      name: tool.name,
      description: tool.description,
      parameters: tool.parameters,
      strict: false,
    }));
  }
  if (call.request.maxOutputTokens !== null) body.max_output_tokens = call.request.maxOutputTokens;
  const reasoning = call.request.reasoning;
  if (reasoning.enabled || reasoning.effort !== null) {
    body.reasoning = {
      summary: "auto",
      ...(reasoning.effort !== null ? { effort: reasoning.effort } : {}),
    };
  }
  // OpenAI 的规范 tier 值是 priority;"fast" 只是客户端别名,上游不接受。
  if (call.request.latency === "fast") body.service_tier = "priority";
  // 会话级缓存键把请求钉到同一缓存分片,前缀缓存才能稳定命中。
  if (call.request.cacheKey !== null) body.prompt_cache_key = call.request.cacheKey;
  return { ...body, ...call.extraBody };
}

type ToolState = {
  callId: string | null;
  name: string | null;
  arguments: string;
  emitted: number;
  started: boolean;
  ended: boolean;
};

type ToolArguments =
  | { kind: "none" }
  | { kind: "delta"; delta: string }
  | { kind: "snapshot"; snapshot: string };

function updateTool(
  index: number,
  item: Record<string, unknown> | null,
  args: ToolArguments,
  done: boolean,
  tools: Map<number, ToolState>,
): ModelEvent[] {
  let tool = tools.get(index);
  if (!tool) {
    tool = { callId: null, name: null, arguments: "", emitted: 0, started: false, ended: false };
    tools.set(index, tool);
  }
  tool.callId ??= text(item?.call_id);
  tool.name ??= text(item?.name);
  if (args.kind === "delta") {
    tool.arguments += args.delta;
  } else if (args.kind === "snapshot" && args.snapshot !== tool.arguments) {
    if (!args.snapshot.startsWith(tool.arguments)) {
      throw new Error("OpenAI Responses final tool arguments do not match streamed arguments");
    }
    tool.arguments += args.snapshot.slice(tool.arguments.length);
  }

  const events: ModelEvent[] = [];
  if (!tool.started && tool.callId !== null && tool.name !== null) {
    tool.started = true;
    events.push({ type: "tool-call-start", index, callId: tool.callId, name: tool.name });
  }
  if (tool.started && tool.emitted < tool.arguments.length) {
    events.push({ type: "tool-call-arguments-delta", index, delta: tool.arguments.slice(tool.emitted) });
    tool.emitted = tool.arguments.length;
  }
  if (done && !tool.ended) {
    if (!tool.started) {
      throw new Error("OpenAI Responses function call is missing call_id or name");
    }
    tool.ended = true;
    events.push({ type: "tool-call-end", index });
  }
  return events;
}

function itemText(item: Record<string, unknown>): string | null {
  const content = item.content;
  if (!Array.isArray(content)) return null;
  return content
    .map((part) => record(part))
    .filter((part) => part?.type === "output_text")
    .map((part) => text(part?.text) ?? "")
    .join("");
}

function requiredIndex(value: Record<string, unknown>): number {
  const index = count(value.output_index);
  if (index === null) throw new Error("OpenAI Responses event is missing output_index");
  return index;
}

function usageEvent(value: unknown): ModelEvent {
  const usage = record(value) ?? {};
  return {
    type: "usage",
    usage: {
      inputTokens: count(usage.input_tokens),
      outputTokens: count(usage.output_tokens),
      totalTokens: count(usage.total_tokens),
      cacheReadTokens: count(record(usage.input_tokens_details)?.cached_tokens),
      cacheWriteTokens: null,
      reasoningTokens: count(record(usage.output_tokens_details)?.reasoning_tokens),
    },
  };
}

async function readBody(lines: AsyncIterable<string>): Promise<string> {
  const collected: string[] = [];
  for await (const line of lines) collected.push(line);
  return collected.join("\n");
}

/**
 * 执行一次 Responses API 流式调用,发出与宿主统一事件集一致的标准化事件,
 * 包括文本/思考边界、工具参数增量与加密推理回放状态。非 2xx 响应抛出
 * `HttpError`,流内失败抛出 `Error`,由调用方分类额度与授权问题。
 */
export async function streamOpenAiResponses(
  call: OpenAiResponsesCall,
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
    body: JSON.stringify(buildResponsesBody(call)),
  });
  if (response.status < 200 || response.status >= 300) {
    throw new HttpError(response.status, await readBody(response.lines), response.headers);
  }

  let textOpen = false;
  let streamedText = "";
  let thinkingOpen = false;
  const tools = new Map<number, ToolState>();
  const reasoningItems: JsonValue[] = [];
  let sawTool = false;
  let sawCompletedItem = false;
  let terminal = false;

  const closeThinking = () => {
    if (thinkingOpen) {
      thinkingOpen = false;
      output.emit({ type: "thinking-end" });
    }
  };
  const closeText = () => {
    if (textOpen) {
      textOpen = false;
      output.emit({ type: "text-end" });
    }
  };
  // 流式增量可能落后于最终文本;补发缺失的后缀。
  const reconcileText = (finalText: string) => {
    if (finalText.startsWith(streamedText) && finalText.length > streamedText.length) {
      if (!textOpen) {
        textOpen = true;
        output.emit({ type: "text-start" });
      }
      output.emit({ type: "text-delta", text: finalText.slice(streamedText.length) });
      streamedText = finalText;
    }
  };
  const endStartedTools = () => {
    for (const [index, tool] of tools) {
      if (tool.started && !tool.ended) {
        tool.ended = true;
        output.emit({ type: "tool-call-end", index });
      }
    }
  };
  const emitReplayState = () => {
    if (reasoningItems.length > 0) {
      output.emit({ type: "replay-state", providerKind: REPLAY_KIND, value: { items: reasoningItems.slice() } });
      reasoningItems.length = 0;
    }
  };

  for await (const line of response.lines) {
    if (!line.startsWith("data:")) continue;
    const payload = line.slice(5).trim();
    if (!payload) continue;
    if (payload === "[DONE]") break;
    let value: Record<string, unknown>;
    try {
      value = record(JSON.parse(payload)) ?? {};
    } catch {
      throw new Error("OpenAI Responses SSE returned invalid JSON");
    }
    if (value.type === "error" || (value.error !== undefined && value.error !== null)) {
      const message = text(record(value.error)?.message) ?? text(value.message) ?? payload;
      throw providerEventError(value, `OpenAI Responses error: ${message}`, response.headers);
    }
    switch (value.type) {
      case "response.output_text.delta": {
        closeThinking();
        if (!textOpen) {
          textOpen = true;
          output.emit({ type: "text-start" });
        }
        const delta = text(value.delta);
        if (delta !== null) {
          streamedText += delta;
          output.emit({ type: "text-delta", text: delta });
        }
        break;
      }
      case "response.output_text.done": {
        const finalText = text(value.text);
        if (finalText !== null) reconcileText(finalText);
        closeText();
        break;
      }
      case "response.reasoning_summary_text.delta":
      case "response.reasoning_text.delta": {
        if (!thinkingOpen) {
          thinkingOpen = true;
          output.emit({ type: "thinking-start" });
        }
        const delta = text(value.delta);
        if (delta !== null) output.emit({ type: "thinking-delta", text: delta });
        break;
      }
      case "response.reasoning_summary_text.done":
      case "response.reasoning_text.done":
        closeThinking();
        break;
      case "response.output_item.added": {
        const item = record(value.item);
        if (item?.type !== "function_call") break;
        sawTool = true;
        for (const event of updateTool(requiredIndex(value), item, { kind: "none" }, false, tools)) {
          output.emit(event);
        }
        break;
      }
      case "response.output_item.done": {
        const item = record(value.item);
        if (item?.type === "reasoning") {
          closeThinking();
          reasoningItems.push(item as JsonValue);
        } else if (item?.type === "message") {
          sawCompletedItem = true;
          const finalText = itemText(item);
          if (finalText !== null) reconcileText(finalText);
          closeText();
        } else if (item?.type === "function_call") {
          sawCompletedItem = true;
          sawTool = true;
          const snapshot = text(item.arguments);
          const args: ToolArguments = snapshot === null ? { kind: "none" } : { kind: "snapshot", snapshot };
          for (const event of updateTool(requiredIndex(value), item, args, true, tools)) {
            output.emit(event);
          }
        }
        break;
      }
      case "response.function_call_arguments.delta": {
        const delta = text(value.delta);
        if (delta === null) break;
        sawTool = true;
        for (const event of updateTool(requiredIndex(value), null, { kind: "delta", delta }, false, tools)) {
          output.emit(event);
        }
        break;
      }
      case "response.function_call_arguments.done": {
        const snapshot = text(value.arguments);
        // 空快照不代表结束;等 output_item.done 收尾。
        const args: ToolArguments = snapshot === null || snapshot === "" ? { kind: "none" } : { kind: "snapshot", snapshot };
        const done = snapshot !== null && snapshot !== "";
        for (const event of updateTool(requiredIndex(value), null, args, done, tools)) {
          output.emit(event);
        }
        break;
      }
      case "response.completed": {
        const usage = record(value.response)?.usage;
        if (usage !== undefined) output.emit(usageEvent(usage));
        closeThinking();
        closeText();
        endStartedTools();
        for (const tool of tools.values()) {
          if (!tool.started) {
            throw new Error("OpenAI Responses completed with incomplete tool metadata");
          }
        }
        terminal = true;
        emitReplayState();
        output.emit({ type: "done", reason: sawTool ? "tool-use" : "stop" });
        break;
      }
      case "response.incomplete": {
        closeThinking();
        closeText();
        endStartedTools();
        terminal = true;
        output.emit({ type: "done", reason: "length" });
        break;
      }
      case "response.failed":
        throw providerEventError(value, `OpenAI Responses failed: ${payload}`, response.headers);
    }
    if (terminal) break;
  }

  if (!terminal && sawCompletedItem) {
    closeThinking();
    closeText();
    for (const tool of tools.values()) {
      if (!tool.ended) {
        throw new Error("OpenAI Responses stream ended with an incomplete tool call");
      }
    }
    terminal = true;
    emitReplayState();
    output.emit({ type: "done", reason: sawTool ? "tool-use" : "stop" });
  }
  if (!terminal) {
    throw new Error("OpenAI Responses stream ended without response.completed or response.incomplete");
  }
}
