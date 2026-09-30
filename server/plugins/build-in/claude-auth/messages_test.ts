import type { JsonValue, NetworkEventStream, PluginContext } from "cursor-byok:plugin";
import type { ModelSnapshot } from "cursor-byok:model";
import type { LlmRequest, ModelEvent } from "cursor-byok:provider";
import { buildMessagesBody, HttpError, streamMessages } from "./messages.ts";

function assert(value: unknown, message = "assertion failed"): asserts value {
  if (!value) throw new Error(message);
}
function equal(actual: unknown, expected: unknown): void {
  assert(
    JSON.stringify(actual) === JSON.stringify(expected),
    `expected ${JSON.stringify(expected)}, received ${JSON.stringify(actual)}`,
  );
}
async function rejects(run: () => Promise<unknown>): Promise<unknown> {
  try {
    await run();
  } catch (error) {
    return error;
  }
  throw new Error("expected rejection");
}
const model: ModelSnapshot = {
  id: "claude-test",
  displayName: "Claude",
  maxOutputTokens: 16000,
  privateData: { thinking: "adaptive", efforts: ["low", "medium", "high"] },
};
function request(): LlmRequest {
  return {
    instructions: "Exact system\nDo not modify.",
    messages: [{ role: "system", content: [{ type: "text", text: "context A" }] }, {
      role: "user",
      content: [{ type: "text", text: "hello" }],
    }],
    tools: [],
    reasoning: { enabled: false, effort: null },
    latency: "standard",
    maxOutputTokens: null,
    cacheKey: "conversation",
  };
}
function event(type: string, fields: Record<string, unknown> = {}): string[] {
  return [`event: ${type}`, `data: ${JSON.stringify({ type, ...fields })}`, ""];
}
const start = () =>
  event("message_start", {
    message: {
      usage: {
        input_tokens: 10,
        cache_read_input_tokens: 30,
        cache_creation_input_tokens: 20,
        output_tokens: 1,
      },
    },
  });
const block = (index: number, content_block: unknown) =>
  event("content_block_start", { index, content_block });
const delta = (index: number, delta: unknown) => event("content_block_delta", { index, delta });
const stop = (index: number) => event("content_block_stop", { index });
const end = () => [
  ...event("message_delta", { delta: { stop_reason: "end_turn" }, usage: { output_tokens: 7 } }),
  ...event("message_stop"),
];
async function* lines(values: string[]) {
  for (const value of values) yield value;
}
function context(
  response: NetworkEventStream,
  signal = new AbortController().signal,
): PluginContext {
  return {
    signal,
    network: {
      fetch: () => {
        throw new Error("unexpected fetch");
      },
      stream: (url, init) => {
        equal(url, "https://api.anthropic.com/v1/messages");
        equal(init?.headers?.authorization, "Bearer private-token");
        return Promise.resolve(response);
      },
    },
  };
}
async function run(values: string[], events: ModelEvent[] = []): Promise<ModelEvent[]> {
  await streamMessages(model, request(), { authorization: "Bearer private-token" }, {
    emit: (event) => events.push(event),
  }, context({ status: 200, headers: {}, lines: lines(values) }));
  return events;
}

Deno.test("messages preserve append-only prefix, system content, and exact tool schemas", () => {
  const first = request();
  first.tools = [{
    name: "custom.Tool",
    description: "Exact description",
    parameters: { type: "object", additionalProperties: false },
  }];
  const before = buildMessagesBody(model, first);
  const next = structuredClone(first);
  next.messages.push(
    { role: "assistant", text: "answer", thinking: "not signed", replayState: null, toolCalls: [] },
    { role: "system", content: [{ type: "text", text: "context B" }] },
    { role: "user", content: [{ type: "text", text: "next" }] },
  );
  const after = buildMessagesBody(model, next);
  equal((after.messages as JsonValue[]).slice(0, 2), before.messages);
  equal(after.system, before.system);
  equal(before.system, first.instructions);
  equal(before.messages, [
    { role: "user", content: [{ type: "text", text: "context A" }] },
    { role: "user", content: [{ type: "text", text: "hello" }] },
  ]);
  equal(before.tools, [{
    name: "custom.Tool",
    description: "Exact description",
    input_schema: first.tools[0].parameters,
  }]);
  equal(after.cache_control, { type: "ephemeral" });
  assert(!JSON.stringify(after).includes("not signed"));
  next.messages.push({ role: "system", content: [{ type: "text", text: "context A" }] });
  equal((buildMessagesBody(model, next).messages as JsonValue[]).slice(0, 5), after.messages);
});

Deno.test("messages serialize user images and rich failed tool results", () => {
  const req = request();
  const image = { type: "image" as const, mediaType: "image/png", dataBase64: "YWJj" };
  req.messages = [{ role: "user", content: [image] }, {
    role: "tool",
    callId: "call-1",
    name: "custom.Tool",
    content: "ignored",
    isError: true,
    parts: [{ type: "text", text: "failed" }, image],
  }];
  const imageBlock = {
    type: "image",
    source: { type: "base64", media_type: "image/png", data: "YWJj" },
  };
  equal(buildMessagesBody(model, req).messages, [
    { role: "user", content: [imageBlock] },
    {
      role: "user",
      content: [{
        type: "tool_result",
        tool_use_id: "call-1",
        is_error: true,
        content: [{ type: "text", text: "failed" }, imageBlock],
      }],
    },
  ]);
});

Deno.test("reasoning uses metadata, output clamp, and supported efforts only", () => {
  const req = request();
  equal(buildMessagesBody(model, req).max_tokens, 8192);
  req.reasoning = { enabled: true, effort: "high" };
  req.maxOutputTokens = 999999;
  const adaptive = buildMessagesBody(model, req);
  equal(adaptive.max_tokens, 16000);
  equal(adaptive.thinking, { type: "adaptive" });
  equal(adaptive.output_config, { effort: "high" });
  req.reasoning.effort = "unsupported";
  assert(!("output_config" in buildMessagesBody(model, req)));
  const enabled = { ...model, privateData: { thinking: "enabled", efforts: [] } };
  req.maxOutputTokens = 1025;
  equal(buildMessagesBody(enabled, req).thinking, { type: "enabled", budget_tokens: 1024 });
  req.maxOutputTokens = 100;
  assert(!("thinking" in buildMessagesBody(enabled, req)));
  assert(!("thinking" in buildMessagesBody({ ...model, privateData: null }, req)));
});

Deno.test("stream preserves signed/redacted thinking interleaving, tool indices and usage", async () => {
  const events = await run([
    ...start(),
    ": comment",
    "",
    ...event("ping"),
    ...block(0, { type: "text", text: "Initial" }),
    ...delta(0, { type: "text_delta", text: " text" }),
    ...stop(0),
    ...block(1, { type: "thinking", thinking: "", signature: "" }),
    ...delta(1, { type: "thinking_delta", thinking: "reason" }),
    ...delta(1, { type: "signature_delta", signature: "signed" }),
    ...stop(1),
    ...block(2, { type: "redacted_thinking", data: "opaque" }),
    ...stop(2),
    ...block(3, { type: "tool_use", id: "call-1", name: "custom.Tool", input: {} }),
    ...delta(3, { type: "input_json_delta", partial_json: '{"key":' }),
    ...delta(3, { type: "input_json_delta", partial_json: "1}" }),
    ...stop(3),
    ...block(4, { type: "text", text: "tail" }),
    ...stop(4),
    ...end(),
  ]);
  equal(events.filter((e) => e.type.startsWith("tool-call")), [
    { type: "tool-call-start", index: 3, callId: "call-1", name: "custom.Tool" },
    { type: "tool-call-arguments-delta", index: 3, delta: '{"key":' },
    { type: "tool-call-arguments-delta", index: 3, delta: "1}" },
    { type: "tool-call-end", index: 3 },
  ]);
  const replay = events.find((e) => e.type === "replay-state");
  assert(replay?.type === "replay-state");
  equal(replay.providerKind, "claude_oauth");
  const blocks = [
    { type: "text", text: "Initial text" },
    { type: "thinking", thinking: "reason", signature: "signed" },
    { type: "redacted_thinking", data: "opaque" },
    { type: "tool_use", id: "call-1", name: "custom.Tool", input: { key: 1 } },
    { type: "text", text: "tail" },
  ];
  equal(replay.value, { blocks });
  const req = request();
  req.messages = [{
    role: "assistant",
    text: "canonical merged text",
    thinking: "unsigned",
    replayState: replay,
    toolCalls: [],
  }];
  equal(buildMessagesBody(model, req).messages, [{ role: "assistant", content: blocks }]);
  equal(events.at(-2), {
    type: "usage",
    usage: {
      inputTokens: 10,
      outputTokens: 7,
      totalTokens: 67,
      cacheReadTokens: 30,
      cacheWriteTokens: 20,
      reasoningTokens: null,
    },
  });
  equal(events.at(-1), { type: "done", reason: "tool-use" });
  equal(events.filter((e) => e.type === "text-start").length, 2);
  equal(events.filter((e) => e.type === "text-end").length, 2);
  equal(events.filter((e) => e.type === "thinking-start").length, 1);
  equal(events.filter((e) => e.type === "thinking-end").length, 1);
});

Deno.test("native replay only contributes signed thinking, never foreign or unsigned text", () => {
  const req = request();
  req.messages = [{
    role: "assistant",
    text: "visible",
    thinking: "unsigned",
    toolCalls: [],
    replayState: {
      providerKind: "anthropic",
      value: {
        blocks: [
          { type: "thinking", thinking: "kept", signature: "sig" },
          { type: "thinking", thinking: "unsigned" },
          { type: "text", text: "discard" },
          { type: "redacted_thinking", data: "opaque" },
        ],
      },
    },
  }];
  equal(buildMessagesBody(model, req).messages, [{
    role: "assistant",
    content: [
      { type: "thinking", thinking: "kept", signature: "sig" },
      { type: "redacted_thinking", data: "opaque" },
      { type: "text", text: "visible" },
    ],
  }]);
});

Deno.test("SSE multiline events and empty tool input are handled at block stop", async () => {
  const events = await run([
    ...start(),
    "event: content_block_start",
    'data: {"type":"content_block_start","index":0,',
    'data: "content_block":{"type":"tool_use","id":"id","name":"tool","input":{}}}',
    "",
    ...stop(0),
    ...end(),
  ]);
  equal(events.slice(0, 3), [
    { type: "tool-call-start", index: 0, callId: "id", name: "tool" },
    { type: "tool-call-arguments-delta", index: 0, delta: "{}" },
    { type: "tool-call-end", index: 0 },
  ]);
});

Deno.test("truncated, malformed and unknown block streams never emit done", async () => {
  const cases = [
    [],
    [...start()],
    [...start(), ...event("message_delta", { delta: { stop_reason: "end_turn" } })],
    [...start(), ...block(0, { type: "future_block" }), ...stop(0), ...end()],
    [...start(), ...block(0, { type: "text", text: "" }), ...end()],
    [...start(), ...delta(0, { type: "text_delta", text: "orphan" }), ...end()],
    [...start(), "data: {broken", ""],
    [
      ...start(),
      ...block(0, { type: "tool_use", id: "id", name: "tool", input: {} }),
      ...delta(0, { type: "input_json_delta", partial_json: "{" }),
      ...stop(0),
      ...end(),
    ],
    [...start(), ...block(0, { type: "thinking", thinking: "unsigned" }), ...stop(0), ...end()],
    [...start(), ...end().slice(0, -1)],
  ];
  for (const values of cases) {
    const events: ModelEvent[] = [];
    await rejects(() => run(values, events));
    assert(!events.some((e) => e.type === "done"));
  }
});

Deno.test("HTTP and SSE errors retain safe status and Retry-After, not upstream secrets", async () => {
  const http = await rejects(() =>
    streamMessages(
      model,
      request(),
      { authorization: "Bearer private-token" },
      {
        emit: () => {
          throw new Error("unexpected output");
        },
      },
      context({
        status: 429,
        headers: { "retry-after": "30" },
        lines: lines(["secret upstream body"]),
      }),
    )
  );
  assert(http instanceof HttpError);
  equal(http.status, 429);
  equal(http.headers["retry-after"], "30");
  assert(!String(http).includes("secret"));
  assert(!("body" in http));
  const sse = await rejects(() =>
    run(event("error", { error: { type: "overloaded_error", message: "secret-token" } }))
  );
  assert(sse instanceof HttpError);
  equal(sse.status, 529);
  equal(sse.errorType, "overloaded_error");
  assert(!String(sse).includes("secret-token"));
});

Deno.test("cancellation interrupts pending stream read and preserves abort reason", async () => {
  const controller = new AbortController();
  const reason = new Error("test cancellation");
  let reads = 0;
  const pending: AsyncIterable<string> = {
    [Symbol.asyncIterator]: () => ({
      next: () => {
        reads++;
        return new Promise<IteratorResult<string>>(() => {});
      },
    }),
  };
  const promise = streamMessages(model, request(), { authorization: "Bearer private-token" }, {
    emit: () => {
      throw new Error("unexpected output");
    },
  }, context({ status: 200, headers: {}, lines: pending }, controller.signal));
  await Promise.resolve();
  await Promise.resolve();
  controller.abort(reason);
  assert(await rejects(() => promise) === reason);
  assert(reads <= 1);
});

Deno.test("cancellation before dispatch or during pending headers never emits output", async () => {
  for (const alreadyAborted of [true, false]) {
    const controller = new AbortController();
    const reason = new Error("cancel pending headers");
    let calls = 0;
    if (alreadyAborted) controller.abort(reason);
    const ctx: PluginContext = {
      signal: controller.signal,
      network: {
        fetch: () => {
          throw new Error("unexpected fetch");
        },
        stream: () => {
          calls++;
          return new Promise(() => {});
        },
      },
    };
    const promise = streamMessages(model, request(), {}, {
      emit: () => {
        throw new Error("unexpected event");
      },
    }, ctx);
    if (!alreadyAborted) controller.abort(reason);
    assert(await rejects(() => promise) === reason);
    equal(calls, alreadyAborted ? 0 : 1);
  }
});

Deno.test("failure after partial output is propagated without retry or success", async () => {
  const emitted: ModelEvent[] = [];
  let calls = 0;
  const ctx = context({
    status: 200,
    headers: {},
    lines: lines([
      ...start(),
      ...block(0, { type: "text", text: "partial" }),
      ...stop(0),
      ...event("error", { error: { type: "rate_limit_error", message: "private detail" } }),
    ]),
  });
  const stream = ctx.network.stream;
  ctx.network.stream = (...args) => {
    calls++;
    return stream(...args);
  };
  const error = await rejects(() =>
    streamMessages(model, request(), { authorization: "Bearer private-token" }, {
      emit: (e) => emitted.push(e),
    }, ctx)
  );
  assert(error instanceof HttpError);
  equal(error.status, 429);
  equal(calls, 1);
  equal(emitted, [{ type: "text-start" }, { type: "text-delta", text: "partial" }, {
    type: "text-end",
  }]);
});

Deno.test("length finish wins over tool presence and absent usage fields stay unknown", async () => {
  const events = await run([
    ...event("message_start", { message: { usage: { output_tokens: 0 } } }),
    ...block(0, { type: "tool_use", id: "id", name: "tool", input: {} }),
    ...stop(0),
    ...event("message_delta", {
      delta: { stop_reason: "max_tokens" },
      usage: { output_tokens: 5 },
    }),
    ...event("message_stop"),
  ]);
  equal(events.at(-1), { type: "done", reason: "length" });
  equal(events.at(-2), {
    type: "usage",
    usage: {
      inputTokens: null,
      outputTokens: 5,
      totalTokens: null,
      cacheReadTokens: null,
      cacheWriteTokens: null,
      reasoningTokens: null,
    },
  });
});

Deno.test("foreign replay is ignored and canonical tool calls remain exact", () => {
  const req = request();
  req.messages = [{
    role: "assistant",
    text: "text",
    thinking: "unsigned",
    toolCalls: [
      { index: 2, callId: "id", name: "custom.Tool", arguments: { value: 1 } },
    ],
    replayState: {
      providerKind: "openai_responses",
      value: { blocks: [{ type: "text", text: "foreign" }] },
    },
  }];
  equal(buildMessagesBody(model, req).messages, [{
    role: "assistant",
    content: [
      { type: "text", text: "text" },
      { type: "tool_use", id: "id", name: "custom.Tool", input: { value: 1 } },
    ],
  }]);
});
