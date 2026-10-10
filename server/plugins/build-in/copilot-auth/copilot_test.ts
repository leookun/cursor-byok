import type {
  JsonValue,
  NetworkEventStream,
  NetworkResponse,
  PluginContext,
} from "cursor-byok:plugin";
import type { ModelSnapshot } from "cursor-byok:model";
import type { LlmMessage, LlmRequest, ModelEvent } from "cursor-byok:provider";
import type { ResourceSnapshot } from "cursor-byok:resource";
import {
  COPILOT_API_HOSTS,
  COPILOT_TOKEN_URL,
  COPILOT_USER_URL,
  copilotChatHeaders,
  copilotModelHeaders,
} from "./constants.ts";
import { copilotModels, parseCopilotModels, routeFor } from "./models.ts";
import { githubDeviceOAuth } from "./oauth.ts";
import { copilotProvider, initiator, isBareForbidden } from "./provider.ts";
import {
  type AccountData,
  parseCopilotUser,
  presentAccount,
  refreshAccount,
  RESOURCE_TYPE,
} from "./resources.ts";
import { exchangeCopilotToken, isFresh, NO_COPILOT_MESSAGE } from "./token.ts";

function assert(condition: unknown, message = "assertion failed"): asserts condition {
  if (!condition) throw new Error(message);
}

function assertEquals(actual: unknown, expected: unknown): void {
  const left = JSON.stringify(actual);
  const right = JSON.stringify(expected);
  if (left !== right) throw new Error(`expected ${right}, received ${left}`);
}

async function assertRejects(promise: Promise<unknown>, includes: string): Promise<void> {
  try {
    await promise;
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    assert(message.includes(includes), `expected error containing "${includes}", got "${message}"`);
    return;
  }
  throw new Error(`expected rejection containing "${includes}"`);
}

type RequestInit = { method?: string; body?: string; headers?: Record<string, string> };
type FetchHandler = (url: string, init?: RequestInit) => NetworkResponse;
type StreamHandler = (url: string, init?: RequestInit) => NetworkEventStream;

function context(handlers: { fetch?: FetchHandler; stream?: StreamHandler }): PluginContext {
  return {
    network: {
      fetch: (url, init) => {
        if (!handlers.fetch) throw new Error(`fetch was not expected: ${url}`);
        return Promise.resolve(handlers.fetch(url, init));
      },
      stream: (url, init) => {
        if (!handlers.stream) throw new Error(`stream was not expected: ${url}`);
        return Promise.resolve(handlers.stream(url, init));
      },
    },
    signal: new AbortController().signal,
  };
}

function json(status: number, body: unknown): NetworkResponse {
  return { status, headers: {}, body: typeof body === "string" ? body : JSON.stringify(body) };
}

async function* sse(lines: string[]): AsyncGenerator<string> {
  for (const line of lines) yield line;
}

function streamOf(status: number, lines: string[]): NetworkEventStream {
  return { status, headers: {}, lines: sse(lines) };
}

const CHAT_DONE = [
  'data: {"choices":[{"delta":{"content":"ok"}}]}',
  'data: {"choices":[{"delta":{},"finish_reason":"stop"}]}',
  "data: [DONE]",
];
const RESPONSES_DONE = [
  'data: {"type":"response.output_text.delta","delta":"ok"}',
  'data: {"type":"response.completed","response":{}}',
];

const API_BASE = "https://api.individual.githubcopilot.com";

function tokenResponse(token = "copilot-new"): NetworkResponse {
  return json(200, {
    token,
    expires_at: Math.floor(Date.now() / 1000) + 1800,
    refresh_in: 1500,
    endpoints: { api: API_BASE },
  });
}

function userResponse(): NetworkResponse {
  return json(200, {
    login: "Octo-Cat",
    copilot_plan: "individual",
    quota_reset_date: "2026-11-01",
    quota_snapshots: {
      premium_interactions: { percent_remaining: 72.5, unlimited: false },
    },
  });
}

function account(overrides: Partial<AccountData> = {}): AccountData {
  return {
    githubToken: "gho_secret",
    deviceId: "device-1",
    login: "Octo-Cat",
    plan: "individual",
    copilotToken: "copilot-cached",
    copilotTokenExpiresAtMs: Date.now() + 20 * 60 * 1000,
    apiBase: API_BASE,
    quota: { percentRemaining: 50, unlimited: false, resetAtMs: null },
    ...overrides,
  };
}

function snapshot(data: AccountData): ResourceSnapshot {
  return {
    id: "resource-1",
    type: RESOURCE_TYPE,
    key: data.login.toLowerCase(),
    privateData: data as unknown as JsonValue,
    state: { status: "ready" },
  };
}

const chatModel: ModelSnapshot = {
  id: "claude-sonnet-4.5",
  displayName: "Claude Sonnet 4.5",
  privateData: { route: "chat", vendor: "Anthropic", reasoningEfforts: [] },
};

const responsesModel: ModelSnapshot = {
  id: "gpt-5.2",
  displayName: "GPT-5.2",
  privateData: {
    route: "responses",
    vendor: "OpenAI",
    reasoningEfforts: ["low", "medium", "high"],
  },
};

function request(overrides: Partial<LlmRequest> = {}): LlmRequest {
  return {
    instructions: "You are a coding assistant.",
    messages: [{ role: "user", content: [{ type: "text", text: "hi" }] }],
    tools: [],
    reasoning: { enabled: true, effort: "xhigh" },
    latency: "fast",
    maxOutputTokens: 32_000,
    cacheKey: "conversation-1",
    ...overrides,
  };
}

function output(events: ModelEvent[] = []) {
  return { emit: (event: ModelEvent) => void events.push(event) };
}

const assistantTurn: LlmMessage = {
  role: "assistant",
  text: "",
  thinking: "",
  replayState: null,
  toolCalls: [],
};

Deno.test("device-code begin posts JSON with the VS Code client id", async () => {
  let sent: RequestInit | undefined;
  const begin = await githubDeviceOAuth.begin(context({
    fetch: (url, init) => {
      assertEquals(url, "https://github.com/login/device/code");
      sent = init;
      return json(200, {
        device_code: "device-code",
        user_code: "ABCD-1234",
        verification_uri: "https://github.com/login/device",
        expires_in: 900,
        interval: 0,
      });
    },
  }));
  assertEquals(JSON.parse(sent?.body ?? "{}"), {
    client_id: "Iv1.b507a08c87ecfe98",
    scope: "read:user",
  });
  assertEquals(sent?.headers?.["content-type"], "application/json");
  assertEquals(begin.session, { deviceCode: "device-code" });
  assertEquals(begin.userCode, "ABCD-1234");
  assertEquals(begin.verificationUrl, "https://github.com/login/device");
  assertEquals(begin.pollIntervalMs, 1000);
});

Deno.test("device-code poll maps GitHub 200 error bodies to host states", async () => {
  const pollWith = (response: NetworkResponse) =>
    githubDeviceOAuth.poll({ deviceCode: "device-code" }, context({ fetch: () => response }));
  assertEquals(await pollWith(json(200, { error: "authorization_pending" })), {
    status: "pending",
  });
  assertEquals(await pollWith(json(200, { error: "slow_down" })), { status: "slow-down" });
  assertEquals(await pollWith(json(200, { error: "access_denied" })), { status: "denied" });
  assertEquals((await pollWith(json(200, { error: "expired_token" }))).status, "failed");
  assertEquals(
    await pollWith(json(200, { error: "unsupported_grant_type", error_description: "bad grant" })),
    { status: "failed", message: "bad grant" },
  );
  assertEquals(await pollWith(json(502, "<html>bad gateway</html>")), { status: "pending" });
});

Deno.test("device-code poll completes with a full account draft keyed by lowercase login", async () => {
  const result = await githubDeviceOAuth.poll(
    { deviceCode: "device-code" },
    context({
      fetch: (url, init) => {
        if (url === "https://github.com/login/oauth/access_token") {
          assertEquals(JSON.parse(init?.body ?? "{}").device_code, "device-code");
          return json(200, { access_token: "gho_secret", token_type: "bearer" });
        }
        assertEquals(init?.headers?.authorization, "token gho_secret");
        if (url === COPILOT_USER_URL) return userResponse();
        if (url === COPILOT_TOKEN_URL) return tokenResponse();
        throw new Error(`unexpected ${url}`);
      },
    }),
  );
  assert(result.status === "completed");
  assertEquals(result.resources.length, 1);
  const draft = result.resources[0];
  assertEquals(draft.key, "octo-cat");
  const data = draft.privateData as unknown as AccountData;
  assertEquals(data.githubToken, "gho_secret");
  assertEquals(data.login, "Octo-Cat");
  assertEquals(data.plan, "individual");
  assertEquals(data.copilotToken, "copilot-new");
  assertEquals(data.apiBase, API_BASE);
  assertEquals(data.quota, {
    percentRemaining: 72.5,
    unlimited: false,
    resetAtMs: Date.parse("2026-11-01"),
  });
  assert(typeof data.deviceId === "string" && data.deviceId.length > 0);
  assert(typeof data.copilotTokenExpiresAtMs === "number");
});

Deno.test("device-code poll fails clearly when the account has no Copilot seat", async () => {
  const result = await githubDeviceOAuth.poll(
    { deviceCode: "device-code" },
    context({
      fetch: (url) =>
        url === COPILOT_USER_URL
          ? json(404, { message: "Not Found" })
          : json(200, { access_token: "gho_secret" }),
    }),
  );
  assertEquals(result, { status: "failed", message: NO_COPILOT_MESSAGE });
});

Deno.test("copilot token renews when fewer than five minutes remain", () => {
  const now = 1_700_000_000_000;
  assert(!isFresh("token", now + 4 * 60 * 1000, now));
  assert(isFresh("token", now + 6 * 60 * 1000, now));
  assert(!isFresh(null, now + 60 * 60 * 1000, now));
});

Deno.test("token exchange rejects Copilot API hosts outside the plugin allow-list", async () => {
  await assertRejects(
    exchangeCopilotToken(
      "gho_secret",
      context({
        fetch: () =>
          json(200, {
            token: "copilot",
            expires_at: 1,
            endpoints: { api: "https://api.unknown.githubcopilot.com" },
          }),
      }),
    ),
    "api.unknown.githubcopilot.com",
  );
  const fallback = await exchangeCopilotToken(
    "gho_secret",
    context({ fetch: () => json(200, { token: "copilot", expires_at: 2 }) }),
  );
  assertEquals(fallback, {
    token: "copilot",
    expiresAtMs: 2000,
    apiBase: "https://api.githubcopilot.com",
  });
});

Deno.test("Copilot API hosts stay in sync with plugin.json network permissions", async () => {
  const manifest = JSON.parse(await Deno.readTextFile(new URL("./plugin.json", import.meta.url)));
  for (const host of COPILOT_API_HOSTS) {
    assert(manifest.permissions.network.includes(host), `${host} missing from plugin.json`);
  }
});

Deno.test("model routing follows supported endpoints and vendor", () => {
  assertEquals(routeFor(undefined, "OpenAI"), "chat");
  assertEquals(routeFor(["/responses"], "OpenAI"), "responses");
  assertEquals(routeFor(["/chat/completions", "/responses"], "OpenAI"), "responses");
  assertEquals(routeFor(["/chat/completions", "/v1/messages"], "Anthropic"), "chat");
  assertEquals(routeFor(["/chat/completions", "/responses"], "Anthropic"), "chat");
  assertEquals(routeFor(["/v1/messages"], "Anthropic"), null);
});

Deno.test("model parsing keeps enabled picker chat models only", () => {
  const chat = (id: string, extra: Record<string, unknown> = {}) => ({
    id,
    name: id.toUpperCase(),
    vendor: "OpenAI",
    model_picker_enabled: true,
    capabilities: {
      type: "chat",
      limits: { max_output_tokens: 64_000 },
      supports: { vision: true, reasoning_effort: ["low", "high"] },
    },
    supported_endpoints: ["/chat/completions", "/responses"],
    ...extra,
  });
  const models = parseCopilotModels({
    data: [
      chat("gpt-5.2"),
      chat("gpt-5.2"),
      chat("hidden", { model_picker_enabled: false }),
      chat("disabled", { policy: { state: "disabled" } }),
      chat("enabled", { policy: { state: "enabled" } }),
      chat("embedding", { capabilities: { type: "embeddings" } }),
      chat("claude-opus", { vendor: "Anthropic", supported_endpoints: ["/v1/messages"] }),
      chat("legacy", { supported_endpoints: undefined }),
    ],
  });
  assertEquals(models.map((model) => model.id), ["gpt-5.2", "enabled", "legacy"]);
  assertEquals(models[0], {
    id: "gpt-5.2",
    displayName: "GPT-5.2",
    maxOutputTokens: 64_000,
    capabilities: { images: true },
    privateData: {
      route: "responses",
      vendor: "OpenAI",
      reasoningEfforts: ["low", "high"],
    },
  });
  assertEquals((models[2].privateData as { route: string }).route, "chat");
});

Deno.test("model sync uses model-access headers and refuses an empty catalog", async () => {
  let headers: Record<string, string> | undefined;
  const ctx = (data: unknown[]) =>
    context({
      fetch: (url, init) => {
        assertEquals(url, `${API_BASE}/models`);
        headers = init?.headers;
        return json(200, { data });
      },
    });
  const models = await copilotModels.list(
    { resource: snapshot(account()) },
    ctx([{
      id: "gpt-4.1",
      name: "GPT-4.1",
      model_picker_enabled: true,
      capabilities: { type: "chat" },
    }]),
  );
  assertEquals(models.map((model) => model.id), ["gpt-4.1"]);
  assertEquals(headers?.authorization, "Bearer copilot-cached");
  assertEquals(headers?.["openai-intent"], "model-access");
  assertEquals(headers?.["x-interaction-type"], "model-access");
  assert(!("x-initiator" in (headers ?? {})), "/models must not send x-initiator");
  assert(!("content-type" in (headers ?? {})), "/models must not send content-type");
  assert(!("x-interaction-id" in (headers ?? {})), "/models must not send x-interaction-id");
  await assertRejects(copilotModels.list({ resource: snapshot(account()) }, ctx([])), "no chat");
  await assertRejects(copilotModels.list({ resource: null }, ctx([])), "add a GitHub Copilot");
});

Deno.test("x-initiator charges only user-initiated turns", () => {
  const user: LlmMessage = { role: "user", content: [{ type: "text", text: "hi" }] };
  const tool: LlmMessage = {
    role: "tool",
    callId: "call-1",
    name: "read_file",
    content: "ok",
    isError: false,
    parts: [],
  };
  assertEquals(initiator([]), "user");
  assertEquals(initiator([user]), "user");
  assertEquals(initiator([user, assistantTurn, tool]), "agent");
  assertEquals(initiator([user, assistantTurn]), "agent");
  assertEquals(initiator([user, assistantTurn, tool, user]), "user");
});

Deno.test("chat headers carry the VS Code identity and optional vision flag", () => {
  const plain = copilotChatHeaders({
    copilotToken: "copilot",
    deviceId: "device-1",
    cacheKey: "conversation-1",
    initiator: "agent",
    vision: false,
  });
  assertEquals(plain.authorization, "Bearer copilot");
  assertEquals(plain["copilot-integration-id"], "vscode-chat");
  assertEquals(plain["editor-device-id"], "device-1");
  assertEquals(plain["openai-intent"], "conversation-agent");
  assertEquals(plain["x-interaction-id"], "conversation-1");
  assertEquals(plain["x-initiator"], "agent");
  assertEquals(plain["x-request-id"], plain["x-agent-task-id"]);
  assert(!("copilot-vision-request" in plain));
  const vision = copilotChatHeaders({
    copilotToken: "copilot",
    deviceId: "device-1",
    cacheKey: null,
    initiator: "user",
    vision: true,
  });
  assertEquals(vision["copilot-vision-request"], "true");
  assert(!("x-interaction-id" in vision));
  assert(
    copilotModelHeaders("copilot", "device-1")["x-request-id"] !== vision["x-request-id"],
    "every request needs a fresh request id",
  );
});

Deno.test("chat route sends max_tokens, supported effort only and no service tier", async () => {
  let body: Record<string, unknown> = {};
  let headers: Record<string, string> = {};
  const events: ModelEvent[] = [];
  const result = await copilotProvider.invoke(
    {
      model: chatModel,
      resource: snapshot(account()),
      request: request({
        messages: [{
          role: "user",
          content: [{ type: "image", mediaType: "image/png", dataBase64: "AAAA" }],
        }],
      }),
    },
    output(events),
    context({
      stream: (url, init) => {
        assertEquals(url, `${API_BASE}/chat/completions`);
        body = JSON.parse(init?.body ?? "{}");
        headers = init?.headers ?? {};
        return streamOf(200, CHAT_DONE);
      },
    }),
  );
  assertEquals(result, { status: "completed" });
  assertEquals(body.max_tokens, 32_000);
  assert(!("max_completion_tokens" in body), "chat must not send max_completion_tokens");
  assert(!("prompt_cache_key" in body), "chat must not send prompt_cache_key yet");
  assert(!("service_tier" in body), "Copilot rejects service_tier");
  assert(!("reasoning_effort" in body), "unsupported effort must be dropped");
  assertEquals(headers["x-initiator"], "user");
  assertEquals(headers["copilot-vision-request"], "true");
  assertEquals(headers["x-interaction-id"], "conversation-1");
  assertEquals(events.at(-1), { type: "done", reason: "stop" });
});

Deno.test("responses route keeps max_output_tokens and cache key and disables storage", async () => {
  let body: Record<string, unknown> = {};
  const result = await copilotProvider.invoke(
    {
      model: responsesModel,
      resource: snapshot(account()),
      request: request({ reasoning: { enabled: true, effort: "high" } }),
    },
    output(),
    context({
      stream: (url, init) => {
        assertEquals(url, `${API_BASE}/responses`);
        body = JSON.parse(init?.body ?? "{}");
        return streamOf(200, RESPONSES_DONE);
      },
    }),
  );
  assertEquals(result, { status: "completed" });
  assertEquals(body.max_output_tokens, 32_000);
  assertEquals(body.prompt_cache_key, "conversation-1");
  assertEquals(body.store, false);
  assertEquals(body.reasoning, { summary: "auto", effort: "high" });
  assert(!("service_tier" in body), "Copilot rejects service_tier");
});

Deno.test("expired Copilot token is exchanged and written back on completion", async () => {
  let authorization = "";
  const result = await copilotProvider.invoke(
    {
      model: chatModel,
      resource: snapshot(account({ copilotTokenExpiresAtMs: Date.now() + 4 * 60 * 1000 })),
      request: request(),
    },
    output(),
    context({
      fetch: (url) => {
        assertEquals(url, COPILOT_TOKEN_URL);
        return tokenResponse("copilot-new");
      },
      stream: (_url, init) => {
        authorization = init?.headers?.authorization ?? "";
        return streamOf(200, CHAT_DONE);
      },
    }),
  );
  assertEquals(authorization, "Bearer copilot-new");
  assert(result.status === "completed" && result.patch !== undefined);
  assertEquals((result.patch.privateData as unknown as AccountData).copilotToken, "copilot-new");
  assertEquals(result.patch.state, undefined);
});

Deno.test("HTTP 401 forces one token re-exchange and retries", async () => {
  const tokens: string[] = [];
  let exchanges = 0;
  const result = await copilotProvider.invoke(
    { model: chatModel, resource: snapshot(account()), request: request() },
    output(),
    context({
      fetch: () => {
        exchanges++;
        return tokenResponse("copilot-renewed");
      },
      stream: (_url, init) => {
        tokens.push(init?.headers?.authorization ?? "");
        return tokens.length === 1
          ? streamOf(401, ['{"message":"unauthorized"}'])
          : streamOf(200, CHAT_DONE);
      },
    }),
  );
  assertEquals(exchanges, 1);
  assertEquals(tokens, ["Bearer copilot-cached", "Bearer copilot-renewed"]);
  assert(result.status === "completed" && result.patch !== undefined);
  assertEquals(
    (result.patch.privateData as unknown as AccountData).copilotToken,
    "copilot-renewed",
  );
});

Deno.test("persistent HTTP 401 marks the account invalid", async () => {
  const result = await copilotProvider.invoke(
    { model: chatModel, resource: snapshot(account()), request: request() },
    output(),
    context({
      fetch: () => tokenResponse("copilot-renewed"),
      stream: () => streamOf(401, ['{"message":"unauthorized"}']),
    }),
  );
  assert(result.status === "resource-error");
  assertEquals(result.patch.state, {
    status: "invalid",
    message: "GitHub authorization expired; sign in again",
  });
});

Deno.test("bare 403 from the Copilot edge is retried", async () => {
  assert(isBareForbidden(""));
  assert(isBareForbidden("Forbidden."));
  assert(isBareForbidden('{"error":{"message":"forbidden"}}'));
  assert(!isBareForbidden('{"error":{"message":"Model is not enabled by policy"}}'));
  let calls = 0;
  const result = await copilotProvider.invoke(
    { model: chatModel, resource: snapshot(account()), request: request() },
    output(),
    context({
      stream: () => {
        calls++;
        return calls === 1 ? streamOf(403, ["forbidden"]) : streamOf(200, CHAT_DONE);
      },
    }),
  );
  assertEquals(calls, 2);
  assertEquals(result, { status: "completed" });
});

Deno.test("403 with a concrete reason is a request error and keeps the account usable", async () => {
  let calls = 0;
  const result = await copilotProvider.invoke(
    { model: chatModel, resource: snapshot(account()), request: request() },
    output(),
    context({
      stream: () => {
        calls++;
        return streamOf(403, ['{"error":{"message":"Model is not enabled by policy"}}']);
      },
    }),
  );
  assertEquals(calls, 1);
  assert(result.status === "request-error");
  assert(result.message.includes("not enabled by policy"));
  assertEquals(result.patch, undefined);
});

Deno.test("premium quota 429 cools the account after retries", async () => {
  let calls = 0;
  const resetAtMs = Date.now() + 7 * 24 * 60 * 60 * 1000;
  const result = await copilotProvider.invoke(
    {
      model: chatModel,
      resource: snapshot(account({
        quota: { percentRemaining: 0, unlimited: false, resetAtMs },
      })),
      request: request(),
    },
    output(),
    context({
      stream: () => {
        calls++;
        return streamOf(429, ['{"error":{"message":"premium request quota exceeded"}}']);
      },
    }),
  );
  assertEquals(calls, 3);
  assert(result.status === "resource-error");
  assertEquals(result.patch.state, {
    status: "cooling",
    retryAtMs: resetAtMs,
    message: "Copilot premium requests are exhausted",
  });
});

Deno.test("in-stream errors are not retried", async () => {
  let calls = 0;
  const result = await copilotProvider.invoke(
    { model: chatModel, resource: snapshot(account()), request: request() },
    output(),
    context({
      stream: () => {
        calls++;
        return streamOf(200, ['data: {"error":{"message":"boom"}}']);
      },
    }),
  );
  assertEquals(calls, 1);
  assertEquals(result.status, "request-error");
});

Deno.test("account view shows plan and premium quota without credentials", () => {
  const view = presentAccount(snapshot(account({
    quota: { percentRemaining: 40, unlimited: false, resetAtMs: 1_800_000_000_000 },
  })));
  assertEquals(view, {
    displayName: "Octo-Cat",
    description: "Copilot individual",
    metrics: [{
      id: "premium",
      label: { "zh-CN": "Premium 请求剩余", "en-US": "Premium requests left" },
      unit: "percent",
      value: 40,
      resetAtMs: 1_800_000_000_000,
    }],
  });
  const serialized = JSON.stringify(view);
  assert(!serialized.includes("gho_secret") && !serialized.includes("copilot-cached"));
  const unlimited = presentAccount(snapshot(account({
    plan: null,
    quota: { percentRemaining: 100, unlimited: true, resetAtMs: null },
  })));
  assertEquals(unlimited, { displayName: "Octo-Cat", description: "GitHub Copilot" });
});

Deno.test("refresh updates quota and token, and invalidates revoked GitHub tokens", async () => {
  const patch = await refreshAccount(
    snapshot(account()),
    context({
      fetch: (url) => url === COPILOT_USER_URL ? userResponse() : tokenResponse("copilot-fresh"),
    }),
  );
  assertEquals(patch.state, { status: "ready" });
  const data = patch.privateData as unknown as AccountData;
  assertEquals(data.copilotToken, "copilot-fresh");
  assertEquals(data.quota?.percentRemaining, 72.5);
  assertEquals(data.deviceId, "device-1");

  const revoked = await refreshAccount(
    snapshot(account()),
    context({ fetch: () => json(401, { message: "Bad credentials" }) }),
  );
  assertEquals(revoked, {
    state: { status: "invalid", message: "GitHub authorization expired; sign in again" },
  });
});

Deno.test("unlimited premium quota without a percentage is treated as full", () => {
  assertEquals(
    parseCopilotUser({
      login: "octo",
      quota_snapshots: { premium_interactions: { unlimited: true } },
    }).quota,
    { percentRemaining: 100, unlimited: true, resetAtMs: null },
  );
  assertEquals(parseCopilotUser({ login: "octo" }).quota, null);
});
