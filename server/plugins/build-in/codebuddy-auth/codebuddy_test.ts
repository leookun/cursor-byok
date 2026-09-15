import type {
  JsonValue,
  NetworkEventStream,
  NetworkResponse,
  PluginContext,
} from "cursor-byok:plugin";
import type { LlmRequest, ModelEvent } from "cursor-byok:provider";
import type { ResourceSnapshot } from "cursor-byok:resource";
import { codeBuddyModels, FALLBACK_MODELS, parseCodeBuddyConfig } from "./models.ts";
import { codeBuddyOAuth } from "./oauth.ts";
import { codeBuddyProvider, isQuotaError } from "./provider.ts";
import {
  accountData,
  credentialDraft,
  parseAccountProfile,
  parseAuthToken,
  presentAccount,
  RESOURCE_TYPE,
  shouldRefresh,
} from "./resources.ts";

function assert(condition: unknown, message = "assertion failed"): asserts condition {
  if (!condition) throw new Error(message);
}

function assertEquals(actual: unknown, expected: unknown): void {
  const left = JSON.stringify(actual);
  const right = JSON.stringify(expected);
  if (left !== right) throw new Error(`expected ${right}, received ${left}`);
}

type RequestInit = { method?: string; body?: string; headers?: Record<string, string> };
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

async function* sse(lines: string[]): AsyncGenerator<string> {
  for (const line of lines) yield line;
}

function request(): LlmRequest {
  return {
    instructions: "You are a coding assistant.",
    messages: [{ role: "user", content: [{ type: "text", text: "hi" }] }],
    tools: [{ name: "read_file", description: "Read a file", parameters: { type: "object" } }],
    reasoning: { enabled: true, effort: null },
    latency: "fast",
    maxOutputTokens: 4096,
    cacheKey: "conversation-1",
  };
}

function snapshot(privateData: JsonValue): ResourceSnapshot {
  return {
    id: "resource-1",
    type: RESOURCE_TYPE,
    key: "codebuddy:user-1",
    privateData,
    state: { status: "ready" },
  };
}

Deno.test("CodeBuddy token and account payloads normalize camel and snake case", () => {
  const now = Date.now();
  const token = parseAuthToken({
    access_token: "access-secret",
    refresh_token: "refresh-secret",
    expires_in: 3600,
  });
  assertEquals(token.accessToken, "access-secret");
  assertEquals(token.refreshToken, "refresh-secret");
  assert(token.expiresAtMs !== null && token.expiresAtMs >= now + 3_500_000);
  assertEquals(parseAccountProfile({ uid: "user-1", nickname: "Alice", type: "personal" }), {
    userId: "user-1",
    displayName: "Alice",
    accountType: "personal",
    enterpriseId: null,
    enterpriseName: null,
  });
});

Deno.test("OAuth opens the CodeBuddy CLI login page, handles pending, and saves the account", async () => {
  let requestNumber = 0;
  const flowContext = context({
    fetch: (url, init) => {
      requestNumber += 1;
      if (requestNumber === 1) {
        assertEquals(url, "https://copilot.tencent.com/v2/plugin/auth/state?platform=CLI");
        assertEquals(init?.method, "POST");
        return {
          status: 200,
          headers: {},
          body: JSON.stringify({
            code: 0,
            data: { state: "state-1", authUrl: "https://copilot.tencent.com/login?state=state-1" },
          }),
        };
      }
      if (requestNumber === 2) {
        assert(url.includes("/auth/token?state=state-1"));
        return {
          status: 200,
          headers: {},
          body: JSON.stringify({ code: 11217, msg: "11217:login ing..." }),
        };
      }
      if (requestNumber === 3) {
        return {
          status: 200,
          headers: {},
          body: JSON.stringify({
            code: 0,
            data: {
              accessToken: "access-secret",
              refreshToken: "refresh-secret",
              expiresIn: 3600,
            },
          }),
        };
      }
      assert(url.includes("/login/account?state=state-1"));
      assertEquals(init?.headers?.authorization, "Bearer access-secret");
      return {
        status: 200,
        headers: {},
        body: JSON.stringify({
          code: 0,
          data: { uid: "user-1", nickname: "Alice", type: "personal" },
        }),
      };
    },
  });

  const begun = await codeBuddyOAuth.begin(flowContext);
  assertEquals(begun.userCode, "");
  assertEquals(begun.verificationUrl, "https://copilot.tencent.com/login?state=state-1");
  const pending = await codeBuddyOAuth.poll(begun.session, flowContext);
  assertEquals(pending.status, "pending");
  const completed = await codeBuddyOAuth.poll(begun.session, flowContext);
  assert(completed.status === "completed", `expected completed, received ${completed.status}`);
  assertEquals(completed.resources[0].key, "codebuddy:user-1");
  const view = presentAccount(snapshot(completed.resources[0].privateData));
  assertEquals(view.displayName, "Alice");
  assert(!JSON.stringify(view).includes("access-secret"), "resource view exposed a token");
});

Deno.test("account refresh boundary and dynamic model catalog are usable", async () => {
  const draft = await credentialDraft(
    {
      accessToken: "access-secret",
      refreshToken: "refresh-secret",
      expiresAt: Date.now() + 30_000,
    },
    { uid: "user-1", nickname: "Alice", type: "personal" },
  );
  const data = accountData(snapshot(draft.privateData));
  assert(shouldRefresh(data));

  const payload = {
    code: 0,
    data: {
      agents: [{ name: "cli", models: ["new-model", "deepseek-v4-flash"] }],
      models: [
        {
          id: "deepseek-v4-flash",
          name: "DeepSeek V4 Flash",
          credits: "x0.17 credits",
          maxOutputTokens: 50_000,
          supportsToolCall: true,
          supportsImages: true,
          supportsReasoning: true,
          reasoning: { effort: "high" },
          temperature: 1,
        },
        {
          id: "new-model",
          name: "New Model",
          credits: "x0.03 credits",
          maxOutputTokens: 128_000,
          supportsToolCall: true,
          supportsImages: false,
        },
        { id: "retired-model", name: "Retired", supportsToolCall: true },
        {
          id: "image-only",
          name: "Image",
          supportsToolCall: true,
          tags: ["text-to-image"],
        },
      ],
    },
  };
  const models = parseCodeBuddyConfig(payload);
  assertEquals(models.map((model) => model.id), ["new-model", "deepseek-v4-flash"]);
  assertEquals(models[1].maxOutputTokens, 50_000);
  assertEquals(models[1].capabilities, { images: true });
  assertEquals(models[1].privateData, {
    supportsReasoning: true,
    defaultReasoningEffort: "high",
    reasoningEfforts: ["high"],
    canDisableThinking: false,
    reasoningSummary: null,
    temperature: 1,
    topP: null,
  });

  const discovered = await codeBuddyModels.list(
    { resource: snapshot(draft.privateData) },
    context({
      fetch: (url, init) => {
        assertEquals(url, "https://copilot.tencent.com/v3/config");
        assertEquals(init?.headers?.["X-Product"], "SaaS");
        assertEquals(init?.headers?.authorization, "Bearer access-secret");
        assertEquals(init?.headers?.["User-Agent"], "CLI/0.1.7 CodeBuddy/2.148.0");
        return { status: 200, headers: {}, body: JSON.stringify(payload) };
      },
    }),
  );
  assertEquals(discovered.map((model) => model.id), ["new-model", "deepseek-v4-flash"]);
  assert(FALLBACK_MODELS.some((model) => model.id === "deepseek-v4-flash"));
});

Deno.test("provider sends CodeBuddy identity headers and preserves tool calls", async () => {
  const draft = await credentialDraft(
    { accessToken: "access-secret", refreshToken: null },
    { uid: "user-1", nickname: "Alice", type: "personal" },
  );
  let requestBody = "";
  let requestHeaders: Record<string, string> = {};
  const events: ModelEvent[] = [];
  const result = await codeBuddyProvider.invoke(
    {
      model: {
        id: "deepseek-v4-flash",
        displayName: "DeepSeek V4 Flash",
        privateData: {
          supportsReasoning: true,
          defaultReasoningEffort: "high",
          reasoningEfforts: ["low", "high"],
          canDisableThinking: false,
          reasoningSummary: "auto",
          temperature: 1,
          topP: 0.95,
        },
      },
      resource: snapshot(draft.privateData),
      request: request(),
    },
    { emit: (event) => events.push(event) },
    context({
      stream: (url, init) => {
        assertEquals(url, "https://copilot.tencent.com/v2/chat/completions");
        requestBody = init?.body ?? "";
        requestHeaders = init?.headers ?? {};
        return {
          status: 200,
          headers: {},
          lines: sse([
            'data: {"choices":[{"delta":{"reasoning_content":"think"}}]}',
            'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"read_file","arguments":"{\\"path\\":\\"a.ts\\"}"}}]}}]}',
            'data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}',
            "data: [DONE]",
          ]),
        };
      },
    }),
  );
  assertEquals(result, { status: "completed" });
  const body = JSON.parse(requestBody) as Record<string, unknown>;
  assertEquals(body.model, "deepseek-v4-flash");
  assertEquals(body.reasoning_effort, "high");
  assertEquals(body.reasoning_summary, "auto");
  assertEquals(body.temperature, 1);
  assertEquals(body.top_p, 0.95);
  assertEquals(body.stream, true);
  assertEquals(requestHeaders.authorization, "Bearer access-secret");
  assertEquals(requestHeaders["X-User-Id"], "user-1");
  assertEquals(requestHeaders["X-Conversation-ID"], "conversation-1");
  assert(events.some((event) => event.type === "tool-call-start"));
  assertEquals(events.at(-1), { type: "done", reason: "tool-use" });
});

Deno.test("quota failures cool the CodeBuddy account", async () => {
  assert(isQuotaError('{"code":6005,"msg":"积分不足"}'));
  assert(isQuotaError('{"code": 14003, "msg":"rate limited"}'));
  const draft = await credentialDraft(
    { accessToken: "access-secret", refreshToken: null },
    { uid: "user-1", nickname: "Alice", type: "personal" },
  );
  const result = await codeBuddyProvider.invoke(
    {
      model: { id: "default", displayName: "Default" },
      resource: snapshot(draft.privateData),
      request: request(),
    },
    { emit: () => {} },
    context({
      stream: () => ({
        status: 429,
        headers: {},
        lines: sse(['{"code":6005,"msg":"积分不足"}']),
      }),
    }),
  );
  assertEquals(result.status, "resource-error");
  assertEquals(result.patch?.state?.status, "cooling");
});

Deno.test("provider refreshes an expired authorization response and retries once", async () => {
  const draft = await credentialDraft(
    {
      accessToken: "old-access",
      refreshToken: "refresh-secret",
      expiresAt: Date.now() + 60 * 60 * 1000,
    },
    { uid: "user-1", nickname: "Alice", type: "personal" },
  );
  const authorizationHeaders: string[] = [];
  let streamNumber = 0;
  const result = await codeBuddyProvider.invoke(
    {
      model: { id: "deepseek-v4-flash", displayName: "DeepSeek V4 Flash" },
      resource: snapshot(draft.privateData),
      request: request(),
    },
    { emit: () => {} },
    context({
      fetch: (url, init) => {
        assertEquals(url, "https://copilot.tencent.com/v2/plugin/auth/token/refresh");
        assertEquals(init?.headers?.["X-Refresh-Token"], "refresh-secret");
        return {
          status: 200,
          headers: {},
          body: JSON.stringify({
            code: 0,
            data: {
              accessToken: "fresh-access",
              refreshToken: "fresh-refresh",
              expiresIn: 3600,
            },
          }),
        };
      },
      stream: (_url, init) => {
        streamNumber += 1;
        authorizationHeaders.push(init?.headers?.authorization ?? "");
        if (streamNumber === 1) {
          return {
            status: 401,
            headers: {},
            lines: sse(['{"code":401,"msg":"token expired"}']),
          };
        }
        return {
          status: 200,
          headers: {},
          lines: sse([
            'data: {"choices":[{"delta":{"content":"OK"}}]}',
            'data: {"choices":[{"delta":{},"finish_reason":"stop"}]}',
            "data: [DONE]",
          ]),
        };
      },
    }),
  );
  assertEquals(result.status, "completed");
  assertEquals(authorizationHeaders, ["Bearer old-access", "Bearer fresh-access"]);
  const patched = result.patch?.privateData as Record<string, unknown>;
  assertEquals(patched.accessToken, "fresh-access");
  assertEquals(patched.refreshToken, "fresh-refresh");
});
