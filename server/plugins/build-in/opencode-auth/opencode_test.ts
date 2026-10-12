import type { NetworkEventStream, PluginContext } from "cursor-byok:plugin";
import type { LlmRequest, ModelEvent } from "cursor-byok:provider";
import { isFreeChatModel, isResponsesModel, opencodeModels } from "./models.ts";
import { opencodeProvider } from "./provider.ts";
function assert(condition: unknown, message = "assertion failed"): asserts condition {
  if (!condition) throw new Error(message);
}

function assertEquals(actual: unknown, expected: unknown): void {
  const left = JSON.stringify(actual);
  const right = JSON.stringify(expected);
  if (left !== right) throw new Error(`expected ${right}, received ${left}`);
}

type RequestInit = { body?: string; headers?: Record<string, string> };
type StreamHandler = (url: string, init?: RequestInit) => NetworkEventStream;

function context(stream: StreamHandler): PluginContext {
  return {
    network: {
      fetch: () => {
        throw new Error("fetch was not expected");
      },
      stream: (url, init) => Promise.resolve(stream(url, init)),
    },
    signal: new AbortController().signal,
  };
}

async function* sse(lines: string[]): AsyncGenerator<string> {
  for (const line of lines) yield line;
}

function request(): LlmRequest {
  return {
    instructions: "You are a coding assistant.",
    messages: [{ role: "user", content: [{ type: "text", text: "hi" }] }],
    tools: [],
    reasoning: { enabled: true, effort: "medium" },
    latency: "fast",
    maxOutputTokens: 128_000,
    cacheKey: "conversation-1",
  };
}

function snapshot(modelId: string) {
  return {
    id: modelId,
    displayName: modelId,
    privateData: {},
  };
}

Deno.test("free model gate auto-admits free tiers minus exclusions", () => {
  assert(isFreeChatModel("big-pickle"), "big-pickle is the suffix-less free model");
  assert(isFreeChatModel("mimo-v2.6-flash-free"), "-free suffix is auto-admitted");
  assert(isFreeChatModel("space-bunny-free"), "space-bunny is auto-admitted");
  assert(
    isFreeChatModel("some-brand-new-free"),
    "new upstream -free models are admitted without a code change",
  );
  assert(
    !isFreeChatModel("muse-spark-1.3-contributor-free"),
    "responses-only muse excluded from chat lane",
  );
  assert(
    !isFreeChatModel("muse-spark-1.2-contributor-free"),
    "responses-only muse excluded from chat lane",
  );
  assert(
    isResponsesModel("muse-spark-1.3-contributor-free"),
    "responses-only muse accepted on responses lane",
  );
  assert(!isResponsesModel("big-pickle"), "chat models are not responses-only");
  assert(!isFreeChatModel("union-alpha"), "messages-only / no free tier excluded");
  assert(!isFreeChatModel("gpt-5.6-luna"), "paid model excluded");
  assert(!isFreeChatModel("jev-1.13-free"), "jev free is out of scope");
  assert(!isFreeChatModel("jev-1.14-free"), "later jev versions stay excluded");
  assert(!isFreeChatModel("deepseek-v4-flash-free"), "dead free model excluded");
});

Deno.test("provider uses a stable lowercase product type", () => {
  assertEquals(opencodeProvider.providerType, "opencode");
});

Deno.test("models.list returns upstream free models without a static catalog", async () => {
  const models = await opencodeModels.list(
    { resource: null },
    {
      network: {
        fetch: async () => ({
          status: 200,
          headers: {},
          body: JSON.stringify({
            data: [
              { id: "big-pickle", name: "Big Pickle" },
              { id: "mimo-v2.5-free" },
              { id: "space-bunny-free" },
              { id: "muse-spark-1.3-contributor-free" },
              { id: "muse-spark-1.2-contributor-free" },
              { id: "brand-new-2.0-free" },
              { id: "jev-1.13-free" },
              { id: "jev-2.0-free" },
              { id: "deepseek-v4-flash-free" },
              { id: "gpt-5.6-luna", name: "GPT 5.6 Luna" },
              { id: "union-alpha" },
            ],
          }),
        }),
        stream: async () => {
          throw new Error("stream not expected in model discovery");
        },
      },
      signal: new AbortController().signal,
    },
  );
  assertEquals(models.map((model) => model.id), [
    "big-pickle",
    "mimo-v2.5-free",
    "space-bunny-free",
    "muse-spark-1.3-contributor-free",
    "muse-spark-1.2-contributor-free",
    "brand-new-2.0-free",
  ]);
  assertEquals(models[1].displayName, "Mimo V2.5 Free");
});

Deno.test("models.list surfaces HTTP errors instead of returning a built-in catalog", async () => {
  let failure: unknown;
  try {
    await opencodeModels.list(
      { resource: null },
      {
        network: {
          fetch: async () => ({
            status: 500,
            headers: {},
            body: "boom",
          }),
          stream: async () => {
            throw new Error("stream not expected in model discovery");
          },
        },
        signal: new AbortController().signal,
      },
    );
  } catch (error) {
    failure = error;
  }
  assert(failure instanceof Error);
  assertEquals(failure.message, "OpenCode model discovery failed with HTTP 500");
});

Deno.test("models.list rejects an empty free-model result", async () => {
  let failure: unknown;
  try {
    await opencodeModels.list(
      { resource: null },
      {
        network: {
          fetch: async () => ({
            status: 200,
            headers: {},
            body: JSON.stringify({ data: [] }),
          }),
          stream: async () => {
            throw new Error("stream not expected in model discovery");
          },
        },
        signal: new AbortController().signal,
      },
    );
  } catch (error) {
    failure = error;
  }
  assert(failure instanceof Error);
  assertEquals(failure.message, "OpenCode model discovery returned no free models");
});

Deno.test("invoke builds the OpenCode chat request with fingerprint tools and session headers", async () => {
  let requestBody = "";
  let requestHeaders: Record<string, string> = {};
  const events: ModelEvent[] = [];
  const result = await opencodeProvider.invoke(
    { model: snapshot("big-pickle"), resource: null, request: request() },
    { emit: (event) => events.push(event) },
    context((url, init) => {
      assertEquals(url, "https://opencode.ai/zen/v1/chat/completions");
      requestBody = init?.body ?? "";
      requestHeaders = init?.headers ?? {};
      return {
        status: 200,
        headers: {},
        lines: sse([
          'data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_content":"Let me check."}}]}',
          'data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hel"}}]}',
          'data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"lo"}}]}',
          'data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}',
          "data: [DONE]",
        ]),
      };
    }),
  );

  assertEquals(result, { status: "completed" });
  const body = JSON.parse(requestBody) as Record<string, unknown>;
  assertEquals(body.model, "big-pickle");
  assertEquals(body.stream, true);
  assertEquals(body.stream_options, { include_usage: true });
  const toolNames = (body.tools as Array<{ function: { name: string } }>)
    .map((tool) => tool.function.name)
    .sort();
  assertEquals(toolNames, ["bash", "glob", "grep", "read"]);
  assertEquals(body.tool_choice, "none");
  assertEquals(body.reasoning_effort, "medium");
  assertEquals(body.prompt_cache_key, "conversation-1");
  assert(requestHeaders.authorization === "Bearer public", "no account, public bearer");
  assert(requestHeaders["user-agent"].startsWith("opencode/"), "official CLI UA");
  assert(requestHeaders["x-opencode-session"].startsWith("ses_"), "stable session header");
  assert(requestHeaders["x-opencode-request"].startsWith("msg_"), "stable request header");
  assertEquals(requestHeaders["x-opencode-project"], "global");
  assertEquals(events, [
    { type: "thinking-start" },
    { type: "thinking-delta", text: "Let me check." },
    { type: "thinking-end" },
    { type: "text-start" },
    { type: "text-delta", text: "Hel" },
    { type: "text-delta", text: "lo" },
    { type: "text-end" },
    {
      type: "usage",
      usage: {
        inputTokens: 10,
        outputTokens: 4,
        totalTokens: 14,
        cacheReadTokens: null,
        cacheWriteTokens: null,
        reasoningTokens: null,
      },
    },
    {
      type: "replay-state",
      providerKind: "openai_chat",
      value: { reasoning_content: "Let me check." },
    },
    { type: "done", reason: "stop" },
  ]);
});

Deno.test("invoke rejects paid models before reaching the network", async () => {
  const events: ModelEvent[] = [];
  let called = false;
  const result = await opencodeProvider.invoke(
    { model: snapshot("gpt-5.6-luna"), resource: null, request: request() },
    { emit: (event) => events.push(event) },
    context(() => {
      called = true;
      throw new Error("network must not be reached");
    }),
  );
  assert(!called, "paid model must be blocked locally");
  assertEquals(result.status, "request-error");
  assertEquals(events, []);
});

Deno.test("invoke routes muse to the responses endpoint with auto tool choice", async () => {
  let requestBody = "";
  let requestHeaders: Record<string, string> = {};
  const events: ModelEvent[] = [];
  const result = await opencodeProvider.invoke(
    { model: snapshot("muse-spark-1.3-contributor-free"), resource: null, request: request() },
    { emit: (event) => events.push(event) },
    context((url, init) => {
      assertEquals(url, "https://opencode.ai/zen/v1/responses");
      requestBody = init?.body ?? "";
      requestHeaders = init?.headers ?? {};
      return {
        status: 200,
        headers: {},
        lines: sse([
          'data: {"type":"response.created","response":{"id":"r_1","status":"in_progress"}}',
          'data: {"type":"response.reasoning_text.delta","delta":"think"}',
          'data: {"type":"response.reasoning_text.done"}',
          'data: {"type":"response.output_text.delta","delta":"Hel"}',
          'data: {"type":"response.output_text.delta","delta":"lo"}',
          'data: {"type":"response.output_text.done","text":"Hello","output_index":1}',
          'data: {"type":"response.completed","response":{"id":"r_1","status":"completed","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7,"output_tokens_details":{"reasoning_tokens":1}}}}',
          "data: [DONE]",
        ]),
      };
    }),
  );
  assertEquals(result.status, "completed");
  const body = JSON.parse(requestBody) as Record<string, unknown>;
  assertEquals(body.model, "muse-spark-1.3-contributor-free");
  assertEquals(body.tool_choice, "auto");
  assert((body.tools as Array<Record<string, unknown>>).length >= 4, "fingerprint tools present");
  assert(requestHeaders["x-opencode-session"].startsWith("ses_"), "session header");
  assert(requestHeaders["x-opencode-request"].startsWith("msg_"), "request header");
  assertEquals(events.filter((event) => event.type === "thinking-delta").length, 1);
  assertEquals(events.filter((event) => event.type === "text-delta").length, 2);
});

Deno.test("invoke clamps max effort to xhigh on the responses lane", async () => {
  let requestBody = "";
  const events: ModelEvent[] = [];
  const req = request();
  req.reasoning.effort = "max";
  const result = await opencodeProvider.invoke(
    { model: snapshot("muse-spark-1.3-contributor-free"), resource: null, request: req },
    { emit: (event) => events.push(event) },
    context((_url, init) => {
      requestBody = init?.body ?? "";
      return {
        status: 200,
        headers: {},
        lines: sse([
          'data: {"type":"response.created","response":{"id":"r_1","status":"in_progress"}}',
          'data: {"type":"response.output_text.delta","delta":"ok"}',
          'data: {"type":"response.output_text.done","text":"ok","output_index":0}',
          'data: {"type":"response.completed","response":{"id":"r_1","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}',
          "data: [DONE]",
        ]),
      };
    }),
  );
  assertEquals(result.status, "completed");
  const body = JSON.parse(requestBody) as { reasoning: { effort: string } };
  assertEquals(body.reasoning.effort, "xhigh");
});

Deno.test("invoke maps 429 and FreeUsageLimit to a request error", async () => {
  const events: ModelEvent[] = [];
  const result = await opencodeProvider.invoke(
    { model: snapshot("big-pickle"), resource: null, request: request() },
    { emit: (event) => events.push(event) },
    context(() => {
      return {
        status: 429,
        headers: {},
        lines: sse([
          'data: {"error":{"message":"FreeUsageLimitError: retry after 600s"}}',
        ]),
      };
    }),
  );
  assertEquals(result.status, "request-error");
  assertEquals(events, []);
});

Deno.test("invoke derives a stable session for one conversation", async () => {
  const sessions: string[] = [];
  const handler = (_url: string, init?: RequestInit): NetworkEventStream => {
    sessions.push(init?.headers?.["x-opencode-session"] ?? "");
    return {
      status: 200,
      headers: {},
      lines: sse([
        'data: {"choices":[{"delta":{"content":"ok"}}]}',
        'data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}',
        "data: [DONE]",
      ]),
    };
  };
  for (let round = 0; round < 2; round++) {
    await opencodeProvider.invoke(
      { model: snapshot("big-pickle"), resource: null, request: request() },
      { emit: (_event) => undefined },
      context(handler),
    );
  }
  assertEquals(sessions.length, 2);
  assert(sessions[0].startsWith("ses_"), "session header keeps ses_ prefix");
  assertEquals(sessions[0], sessions[1]);
});
Deno.test("invoke reuses a stable session when cacheKey is absent", async () => {
  const sessions: string[] = [];
  const handler = (_url: string, init?: RequestInit): NetworkEventStream => {
    sessions.push(init?.headers?.["x-opencode-session"] ?? "");
    return {
      status: 200,
      headers: {},
      lines: sse([
        'data: {"choices":[{"delta":{"content":"ok"}}]}',
        'data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}',
        "data: [DONE]",
      ]),
    };
  };
  const base = request();
  base.cacheKey = null;
  for (let round = 0; round < 2; round++) {
    await opencodeProvider.invoke(
      { model: snapshot("big-pickle"), resource: null, request: { ...base } },
      { emit: (_event) => undefined },
      context(handler),
    );
  }
  assertEquals(sessions.length, 2);
  assert(sessions[0].startsWith("ses_"), "fallback keeps ses_ prefix");
  assert(sessions[0] === sessions[1], "same question reuses the same session");
});

Deno.test("invoke derives a deterministic request id per question", async () => {
  const requests: string[] = [];
  const handler = (_url: string, init?: RequestInit): NetworkEventStream => {
    requests.push(init?.headers?.["x-opencode-request"] ?? "");
    return {
      status: 200,
      headers: {},
      lines: sse([
        'data: {"choices":[{"delta":{"content":"ok"}}]}',
        'data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}',
        "data: [DONE]",
      ]),
    };
  };
  const base = request();
  for (let round = 0; round < 2; round++) {
    await opencodeProvider.invoke(
      { model: snapshot("big-pickle"), resource: null, request: { ...base } },
      { emit: (_event) => undefined },
      context(handler),
    );
  }
  assertEquals(requests.length, 2);
  assert(requests[0].startsWith("msg_"), "request id keeps msg_ prefix");
  assert(requests[0] === requests[1], "same question reuses the same request id");

  const changed = {
    ...base,
    messages: [{ role: "user" as const, content: [{ type: "text" as const, text: "different" }] }],
  };
  await opencodeProvider.invoke(
    { model: snapshot("big-pickle"), resource: null, request: changed },
    { emit: (_event) => undefined },
    context(handler),
  );
  assertEquals(requests.length, 3);
  assert(requests[2] !== requests[0], "different question gets a different request id");
});

Deno.test("invoke retries a 500 once and completes", async () => {
  let calls = 0;
  const handler = (_url: string, init?: RequestInit): NetworkEventStream => {
    calls++;
    if (calls === 1) {
      return {
        status: 500,
        headers: {},
        lines: sse(['data: {"error":{"message":"Internal server error"}}']),
      };
    }
    return {
      status: 200,
      headers: {},
      lines: sse([
        'data: {"choices":[{"delta":{"content":"ok"}}]}',
        'data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}',
        "data: [DONE]",
      ]),
    };
  };
  const events: ModelEvent[] = [];
  const result = await opencodeProvider.invoke(
    { model: snapshot("big-pickle"), resource: null, request: request() },
    { emit: (event) => events.push(event) },
    context(handler),
  );
  assert(calls === 2, "one retry after the 500");
  assertEquals(result.status, "completed");
});

Deno.test("invoke reports a 500 after one retry as request-error", async () => {
  let calls = 0;
  const handler = (_url: string, _init?: RequestInit): NetworkEventStream => {
    calls++;
    return {
      status: 500,
      headers: {},
      lines: sse(['data: {"error":{"message":"Internal server error"}}']),
    };
  };
  const events: ModelEvent[] = [];
  const result = await opencodeProvider.invoke(
    { model: snapshot("big-pickle"), resource: null, request: request() },
    { emit: (event) => events.push(event) },
    context(handler),
  );
  assert(calls === 2, "exactly one retry, then reports");
  assertEquals(result.status, "request-error");
  assertEquals(events, []);
});
