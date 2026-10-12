import type {
  JsonValue,
  NetworkEventStream,
  NetworkResponse,
  PluginContext,
} from "cursor-byok:plugin";
import type { LlmRequest, ModelEvent } from "cursor-byok:provider";
import type { ResourceSnapshot } from "cursor-byok:resource";
import { grokDeviceOAuth } from "./oauth.ts";
import { FALLBACK_MODELS, grokModels, parseGrokModels } from "./models.ts";
import { grokProvider } from "./provider.ts";
import {
  accountIdentity,
  credentialDraft,
  parseCredentialFiles,
  parseGrokUsage,
  presentAccount,
  quotaState,
  RESOURCE_TYPE,
} from "./resources.ts";

function assert(condition: unknown, message = "assertion failed"): asserts condition {
  if (!condition) throw new Error(message);
}

function assertEquals(actual: unknown, expected: unknown): void {
  const left = JSON.stringify(actual);
  const right = JSON.stringify(expected);
  if (left !== right) throw new Error(`expected ${right}, received ${left}`);
}

function jwt(payload: Record<string, unknown>): string {
  const encoded = btoa(JSON.stringify(payload)).replace(/=/g, "").replace(/\+/g, "-").replace(
    /\//g,
    "_",
  );
  return `header.${encoded}.signature`;
}

type RequestInit = { body?: string; headers?: Record<string, string> };
type FetchHandler = (url: string, init?: RequestInit) => NetworkResponse;
type StreamHandler = (url: string, init?: RequestInit) => NetworkEventStream;

function context(handlers: { fetch?: FetchHandler; stream?: StreamHandler }): PluginContext {
  return {
    network: {
      fetch: (url, init) => {
        if (!handlers.fetch) throw new Error("fetch was not expected");
        return Promise.resolve(handlers.fetch(url, init));
      },
      stream: (url, init) => {
        if (!handlers.stream) throw new Error("stream was not expected");
        return Promise.resolve(handlers.stream(url, init));
      },
    },
    signal: new AbortController().signal,
  };
}

function snapshot(privateData: JsonValue): ResourceSnapshot {
  return {
    id: "resource-1",
    type: RESOURCE_TYPE,
    key: "grok:user-1",
    privateData,
    state: { status: "ready" },
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
    maxOutputTokens: 32_000,
    cacheKey: "conversation-1",
  };
}

Deno.test("account identity uses the JWT subject and drafts keep tokens private-side", async () => {
  const token = jwt({ sub: "user-1", email: "person@x.ai" });
  assertEquals(await accountIdentity(token), {
    key: "grok:user-1",
    displayName: "person@x.ai",
  });
  const draft = await credentialDraft({
    accessToken: token,
    refreshToken: null,
    displayName: null,
  });
  assertEquals(draft.key, "grok:user-1");
  const view = presentAccount(snapshot(draft.privateData));
  assert(!JSON.stringify(view).includes(token), "resource view exposed an access token");
  assertEquals(view.displayName, "person@x.ai");
});

Deno.test("credential import accepts Grok credential JSON files", () => {
  const { credentials, warnings } = parseCredentialFiles([
    {
      name: "accounts.json",
      content: JSON.stringify({
        accounts: [
          { access_token: "token-1", refresh_token: "refresh-1", email: "a@x.ai" },
          { access_token: "token-2", disabled: true },
        ],
      }),
    },
    { name: "broken.json", content: "{not json" },
  ]);
  assertEquals(credentials, [{
    accessToken: "token-1",
    refreshToken: "refresh-1",
    displayName: "a@x.ai",
  }]);
  assertEquals(warnings, ["broken.json: not valid JSON"]);
});

Deno.test("credit usage percent is inverted to remaining and drives cooling", () => {
  const quota = parseGrokUsage({
    config: {
      creditUsagePercent: 34,
      subscriptionTierDisplay: "SuperGrok",
      currentPeriod: { end: "2026-09-01T00:00:00Z" },
    },
  }, 1_700_000_000_000);
  assertEquals(quota.planLabel, "SuperGrok");
  assertEquals(quota.remainingPercent, 66);
  assertEquals(quota.resetAtMs, Date.parse("2026-09-01T00:00:00Z"));
  assertEquals(quotaState(quota, 1_700_000_000_000), { status: "ready" });

  const exhausted = parseGrokUsage({
    config: { creditUsagePercent: 100, currentPeriod: { end: 1_900_000_000 } },
  }, 1_700_000_000_000);
  assertEquals(quotaState(exhausted, 1_700_000_000_000), {
    status: "cooling",
    retryAtMs: 1_900_000_000_000,
    message: "Grok credits are exhausted",
  });
});

Deno.test("missing usage with a billing period counts as unused", () => {
  const quota = parseGrokUsage({ config: { currentPeriod: { end: 1_900_000_000 } } });
  assertEquals(quota.remainingPercent, 100);
  assertEquals(quota.limitReached, false);
});

Deno.test("model discovery parses both language-models and standard list shapes", () => {
  const richModels = parseGrokModels({
    models: [
      { id: "grok-4", input_modalities: ["text", "image"], context_window: 256_000 },
      { id: "grok-3-mini", input_modalities: ["text"] },
      { id: "grok-4" },
    ],
  });
  assertEquals(richModels.map((model) => model.id), ["grok-4", "grok-3-mini"]);
  assertEquals(richModels[0].displayName, "Grok 4");
  assertEquals(richModels[0].contextWindowTokens, 256_000);
  assertEquals(richModels[0].capabilities, { images: true });
  assertEquals(richModels[1].capabilities, { images: false });

  const plainModels = parseGrokModels({ data: [{ id: "grok-4-fast" }] });
  assertEquals(plainModels.map((model) => model.id), ["grok-4-fast"]);
  assertEquals(plainModels[0].displayName, "Grok 4 Fast");
  assertEquals(plainModels[0].capabilities, {});
  assertEquals(plainModels[0].images, true);
  assertEquals(plainModels[0].contextWindowTokens, undefined);
});

Deno.test("model discovery falls back to known models when the account cannot list", async () => {
  const token = jwt({ sub: "user-1" });
  const draft = await credentialDraft({
    accessToken: token,
    refreshToken: null,
    displayName: null,
  });
  const models = await grokModels.list(
    { resource: snapshot(draft.privateData) },
    context({
      fetch: () => ({
        status: 403,
        headers: {},
        body: JSON.stringify({ code: "personal-team-blocked:spending-limit" }),
      }),
    }),
  );
  assertEquals(models, FALLBACK_MODELS);
});

Deno.test("device OAuth begins with a host-held session and completes with a resource draft", async () => {
  const accessToken = jwt({ sub: "user-oauth", email: "oauth@x.ai" });
  let requestNumber = 0;
  const flowContext = context({
    fetch: (url, init) => {
      requestNumber += 1;
      if (requestNumber === 1) {
        assertEquals(url, "https://auth.x.ai/oauth2/device/code");
        assert(init?.body?.includes("scope="), "device code request must carry the scope");
        return {
          status: 200,
          headers: {},
          body: JSON.stringify({
            device_code: "private-device-code",
            user_code: "ABCD-EFGH",
            verification_uri: "https://accounts.x.ai/activate",
            verification_uri_complete: "https://accounts.x.ai/activate?code=ABCD-EFGH",
            expires_in: 900,
            interval: 5,
          }),
        };
      }
      assertEquals(url, "https://auth.x.ai/oauth2/token");
      assert(init?.body?.includes("device_code=private-device-code"));
      if (requestNumber === 2) {
        return {
          status: 400,
          headers: {},
          body: JSON.stringify({ error: "authorization_pending" }),
        };
      }
      return {
        status: 200,
        headers: {},
        body: JSON.stringify({ access_token: accessToken, refresh_token: "refresh-secret" }),
      };
    },
  });

  const begun = await grokDeviceOAuth.begin(flowContext);
  assertEquals(begun.userCode, "ABCD-EFGH");
  assertEquals(begun.pollIntervalMs, 5000);

  const pending = await grokDeviceOAuth.poll(begun.session, flowContext);
  assertEquals(pending.status, "pending");

  const polled = await grokDeviceOAuth.poll(begun.session, flowContext);
  assert(polled.status === "completed", `expected completed, received ${polled.status}`);
  assertEquals(polled.resources[0].key, "grok:user-oauth");
  assertEquals(requestNumber, 3);
});

Deno.test("invoke streams normalized events from the xAI Chat Completions API", async () => {
  const token = jwt({ sub: "user-1" });
  const draft = await credentialDraft({
    accessToken: token,
    refreshToken: null,
    displayName: null,
  });
  let requestBody = "";
  let requestHeaders: Record<string, string> = {};
  const events: ModelEvent[] = [];
  const result = await grokProvider.invoke(
    {
      model: { id: "grok-4", displayName: "Grok 4" },
      resource: snapshot(draft.privateData),
      request: request(),
    },
    { emit: (event) => events.push(event) },
    context({
      stream: (url, init) => {
        assertEquals(url, "https://api.x.ai/v1/chat/completions");
        requestBody = init?.body ?? "";
        requestHeaders = init?.headers ?? {};
        return {
          status: 200,
          headers: {},
          lines: sse([
            'data: {"choices":[{"delta":{"content":"Hel"}}]}',
            'data: {"choices":[{"delta":{"content":"lo"}}]}',
            'data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":4}}}',
            "data: [DONE]",
          ]),
        };
      },
    }),
  );
  assertEquals(result, { status: "completed" });
  const body = JSON.parse(requestBody) as Record<string, unknown>;
  assertEquals(body.model, "grok-4");
  assertEquals(body.stream, true);
  assertEquals(body.prompt_cache_key, "conversation-1");
  assert(!("reasoning_effort" in body), "xAI endpoint rejects reasoning_effort");
  assert(!("service_tier" in body), "xAI endpoint rejects service_tier");
  assertEquals(requestHeaders["authorization"], `Bearer ${token}`);
  assertEquals(events, [
    { type: "text-start" },
    { type: "text-delta", text: "Hel" },
    { type: "text-delta", text: "lo" },
    { type: "text-end" },
    {
      type: "usage",
      usage: {
        inputTokens: 10,
        outputTokens: 2,
        totalTokens: null,
        cacheReadTokens: 4,
        cacheWriteTokens: null,
        reasoningTokens: null,
      },
    },
    { type: "done", reason: "stop" },
  ]);
});

Deno.test("invoke streams incremental tool calls and reasoning replay state", async () => {
  const token = jwt({ sub: "user-1" });
  const draft = await credentialDraft({
    accessToken: token,
    refreshToken: null,
    displayName: null,
  });
  const events: ModelEvent[] = [];
  const result = await grokProvider.invoke(
    {
      model: { id: "grok-4", displayName: "Grok 4" },
      resource: snapshot(draft.privateData),
      request: request(),
    },
    { emit: (event) => events.push(event) },
    context({
      stream: () => ({
        status: 200,
        headers: {},
        lines: sse([
          'data: {"choices":[{"delta":{"reasoning_content":"thinking"}}]}',
          'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"read_file","arguments":"{\\"path\\":"}}]}}]}',
          'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\\"a.ts\\"}"}}]}}]}',
          'data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}',
          "data: [DONE]",
        ]),
      }),
    }),
  );
  assertEquals(result, { status: "completed" });
  assertEquals(events, [
    { type: "thinking-start" },
    { type: "thinking-delta", text: "thinking" },
    { type: "tool-call-start", index: 0, callId: "call-1", name: "read_file" },
    { type: "tool-call-arguments-delta", index: 0, delta: '{"path":' },
    { type: "tool-call-arguments-delta", index: 0, delta: '"a.ts"}' },
    { type: "thinking-end" },
    { type: "tool-call-end", index: 0 },
    {
      type: "replay-state",
      providerKind: "openai_chat",
      value: { reasoning_content: "thinking" },
    },
    { type: "done", reason: "tool-use" },
  ]);
});

Deno.test("invoke maps quota failures to a cooling resource error", async () => {
  const token = jwt({ sub: "user-1" });
  const draft = await credentialDraft({
    accessToken: token,
    refreshToken: null,
    displayName: null,
  });
  const result = await grokProvider.invoke(
    {
      model: { id: "grok-4", displayName: "Grok 4" },
      resource: snapshot(draft.privateData),
      request: request(),
    },
    { emit: () => {} },
    context({
      stream: () => ({
        status: 429,
        headers: { "retry-after": "10" },
        lines: sse(['{"error":"credits exhausted"}']),
      }),
    }),
  );
  assert(result.status === "resource-error", `expected resource-error, received ${result.status}`);
  assertEquals(result.failure, { kind: "rate_limit", status: 429, retryAfterMs: 10_000 });
  assert(result.patch.state?.status === "cooling", "quota failure should cool the resource");
});
