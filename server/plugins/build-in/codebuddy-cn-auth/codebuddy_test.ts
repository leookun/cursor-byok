import type {
  JsonValue,
  NetworkEventStream,
  NetworkResponse,
  PluginContext,
} from "cursor-byok:plugin";
import type { LlmRequest, ModelEvent } from "cursor-byok:provider";
import type { ResourceSnapshot } from "cursor-byok:resource";
import { checkInAction, parseCheckinResult } from "./checkin.ts";
import { parseCodeBuddyModels, reasoningEfforts } from "./models.ts";
import { codeBuddyOAuth } from "./oauth.ts";
import { codeBuddyProvider } from "./provider.ts";
import {
  accountData,
  credentialDraft,
  parseCodeBuddyQuota,
  parseCredentialFiles,
  quotaExhaustedPatch,
  quotaState,
  refreshAccount,
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
      fetch: (url: string, init?: RequestInit) =>
        handlers.fetch
          ? Promise.resolve(handlers.fetch(url, init))
          : Promise.reject(new Error("fetch was not expected")),
      stream: (url: string, init?: RequestInit) =>
        handlers.stream
          ? Promise.resolve(handlers.stream(url, init))
          : Promise.reject(new Error("stream was not expected")),
    },
    signal: new AbortController().signal,
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

async function* sse(lines: string[]): AsyncGenerator<string> {
  for (const line of lines) yield line;
}

function request(): LlmRequest {
  return {
    instructions: "You are a helpful AI assistant that helps with software engineering tasks.",
    messages: [{ role: "user", content: [{ type: "text", text: "hello" }] }],
    tools: [],
    reasoning: { enabled: true, effort: "high" },
    latency: "fast",
    maxOutputTokens: 32_000,
    cacheKey: "conversation-1",
  };
}

Deno.test("account identity and import accept account arrays and private tokens", async () => {
  const token = jwt({ sub: "user-1", email: "person@example.com" });
  const draft = await credentialDraft({
    accessToken: token,
    refreshToken: "refresh-1",
    expiresAtMs: Date.now() + 60_000,
    refreshExpiresAtMs: null,
    uid: "user-1",
    email: "person@example.com",
    nickname: "Person",
    enterpriseName: null,
    enterpriseId: null,
    domain: null,
  });
  assertEquals(draft.key, "codebuddy:user-1");
  assertEquals(accountData(snapshot(draft.privateData)).email, "person@example.com");
  const parsed = parseCredentialFiles([{
    name: "accounts.json",
    content: JSON.stringify({
      accounts: [{ access_token: "token-1", email: "a@example.com" }, { token: "token-2" }],
    }),
  }]);
  assertEquals(parsed.credentials.map((item) => item.accessToken), ["token-1", "token-2"]);
  assertEquals(parsed.warnings, []);
});

Deno.test("dynamic model parser handles nested CLI metadata and filters generative models", () => {
  const models = parseCodeBuddyModels({
    code: 0,
    data: {
      models: [
        {
          id: "hy3",
          name: "Hy3",
          supportsToolCall: true,
          supportsImages: true,
          maxOutputTokens: 32_000,
          contextWindow: { defaultLength: 192_000 },
          reasoning: { supportedEfforts: ["low", "medium", "xhigh"] },
        },
        { id: "image", supportsToolCall: true, tags: ["text-to-image"] },
        { id: "disabled", supportsToolCall: true, disabled: true },
      ],
      agent: { agents: [{ name: "cli", models: ["hy3"] }] },
    },
  });
  assertEquals(models.map((model) => model.id), ["hy3"]);
  assertEquals(models[0].maxOutputTokens, 32_000);
  assertEquals(models[0].privateData, { reasoningEfforts: ["low", "medium", "xhigh"] });
  assertEquals(reasoningEfforts(models[0]), ["low", "medium", "xhigh"]);
});

Deno.test("model catalog drops upstream descriptions and separates duplicate names", () => {
  // Live /v3/config ships localized descriptions and reuses one display name
  // across two models; the host catalog must stay uniform across plugins.
  const models = parseCodeBuddyModels({
    code: 0,
    data: {
      models: [
        {
          id: "hy4-preview-f",
          name: "Hy4 preview",
          description: "混元思考模型，具有增强的推理能力",
        },
        { id: "hy3", name: "Hy3" },
        { id: "hy3-x", name: "Hy3", description: "混元思考模型，具有增强的推理能力" },
      ],
    },
  });
  assertEquals(models.map((model) => model.displayName), ["Hy4 preview", "Hy3", "Hy3 X"]);
  assertEquals(models.filter((model) => model.description).length, 0);
});

Deno.test("model catalog follows the upstream CLI allowlist and drops image models", () => {
  // Live /v3/config ships every model the product has, plus a CLI agent
  // allowlist naming the ones the CLI can actually call.
  const models = parseCodeBuddyModels({
    code: 0,
    data: {
      models: [
        { id: "hy3", name: "Hy3" },
        { id: "hy3-x", name: "Hy3" },
        { id: "glm-4.7", name: "GLM-4.7" },
        { id: "hunyuan-image-alpha", name: "Hunyuan Image Alpha", tags: ["text-to-image"] },
        { id: "hunyuan-image-alpha-edit", name: "Edit", tags: ["image-to-image"] },
      ],
      agents: [{
        name: "cli",
        tags: ["cli", "default", "model:craft"],
        models: ["hy3", "hy3-x"],
      }],
    },
  });
  assertEquals(models.map((model) => model.id), ["hy3", "hy3-x"]);
  assertEquals(models.map((model) => model.displayName), ["Hy3", "Hy3 X"]);
});

Deno.test("quota reads the spendable pool and skips expired grants", () => {
  // Mirrors the live get-user-resource payload: Capacity* is spendable,
  // CycleCapacity* is the renewing subset, and an expired grant still ships.
  const quota = parseCodeBuddyQuota({
    code: 0,
    data: {
      Response: {
        Data: {
          Accounts: [
            {
              PackageName: "Pro",
              Status: 0,
              DeductionEndTime: 2042502323000,
              CycleEndTime: "2026-09-30 23:59:59",
              CapacitySizePrecise: 500,
              CapacityUsedPrecise: 0,
              CapacityRemainPrecise: 500,
              CycleCapacitySizePrecise: 500,
              CycleCapacityUsedPrecise: 500,
              CycleCapacityRemainPrecise: 0,
            },
            {
              PackageName: "Expired pack",
              Status: 3,
              ExpiredTime: "2026-09-16 10:43:26",
              CycleEndTime: "2026-12-01 13:30:24",
              CapacitySizePrecise: 1000,
              CapacityUsedPrecise: 1000,
              CapacityRemainPrecise: 0,
            },
            {
              PackageName: "Grant",
              Status: 0,
              CycleEndTime: "2026-10-23 05:18:52",
              DeductionEndTime: 1792703932000,
              CapacitySizePrecise: 100,
              CapacityUsedPrecise: 2.25,
              CapacityRemainPrecise: 97.75,
            },
          ],
        },
      },
    },
  }, Date.parse("2026-09-28T00:00:00Z"));
  // The 1000-credit expired pack must not be counted, and the Pro cycle is
  // already spent even though its lifetime pool is untouched.
  assertEquals(quota.total, 600);
  assertEquals(quota.used, 502.25);
  assertEquals(quota.remaining, 97.75);
  assertEquals(quota.plan, "Pro");
  assertEquals(quota.subscription?.remaining, 0);
  assertEquals(quota.subscription?.used, 500);
  assertEquals(quota.subscription?.remainingPercent, 0);
  assertEquals(quota.grants.length, 1);
  assertEquals(quota.grants[0].name, "Grant");
  assertEquals(quota.grants[0].remaining, 97.75);
});

Deno.test("a spent cycle reports zero even when the lifetime pool is full", () => {
  // Upstream tracks both: `Capacity*` is the lifetime pool and can stay at 500
  // forever, while `CycleCapacity*` is what is spendable until the cycle
  // resets. Reading the wrong pair shows credits the account cannot spend.
  const quota = parseCodeBuddyQuota({
    data: {
      Response: {
        Data: {
          Accounts: [{
            PackageName: "Pro",
            Status: 0,
            CycleEndTime: "2026-09-30 23:59:59",
            DeductionEndTime: 2042502323000,
            CapacitySizePrecise: 500,
            CapacityUsedPrecise: 0,
            CapacityRemainPrecise: 500,
            CycleCapacitySizePrecise: 500,
            CycleCapacityUsedPrecise: 500,
            CycleCapacityRemainPrecise: 0,
          }],
        },
      },
    },
  }, Date.parse("2026-09-28T00:00:00Z"));

  assertEquals(quota.subscription?.remaining, 0);
  assertEquals(quota.subscription?.used, 500);
  assertEquals(quota.subscription?.total, 500);
  assertEquals(quota.remaining, 0);
});

Deno.test("quota cools only when nothing is left to spend", () => {
  const exhausted = {
    plan: "Pro",
    subscription: null,
    grants: [],
    total: 100,
    used: 100,
    remaining: 0,
    resetAtMs: 1_800_000_000_000,
    updatedAtMs: 1_700_000_000_000,
  };
  assertEquals(quotaState(exhausted, 1_700_000_000_000).status, "cooling");
  assertEquals(quotaState({ ...exhausted, remaining: 1 }, 1_700_000_000_000).status, "ready");
});

Deno.test("device OAuth state poll and profile are state-based", async () => {
  const accessToken = jwt({ sub: "oauth-user", email: "oauth@example.com" });
  let call = 0;
  const flow = context({
    fetch: (url, init) => {
      call += 1;
      if (call === 1) {
        assertEquals(url, "https://copilot.tencent.com/v2/plugin/auth/state?platform=CLI");
        assertEquals(init?.body, "{}");
        assertEquals(init?.headers?.Accept, "application/json");
        assertEquals(init?.headers?.["X-No-Authorization"], "true");
        assertEquals(init?.headers?.["X-No-Enterprise-Id"], "true");
        return {
          status: 200,
          headers: {},
          body: JSON.stringify({
            code: 0,
            data: {
              state: "state-1",
              authUrl: "https://copilot.tencent.com/login?platform=CLI&state=state-1",
            },
          }),
        };
      }
      if (call === 2) {
        assert(url.includes("/v2/plugin/auth/token?state=state-1"));
        assertEquals(init?.headers?.["X-No-Authorization"], "true");
        return { status: 200, headers: {}, body: JSON.stringify({ code: 11217 }) };
      }
      if (call === 3) {
        assert(url.includes("/v2/plugin/auth/token?state=state-1"));
        return {
          status: 200,
          headers: {},
          body: JSON.stringify({
            code: 0,
            data: { accessToken, refreshToken: "refresh-1", expiresIn: 3600 },
          }),
        };
      }
      assert(url.includes("/v2/plugin/login/account?state=state-1"));
      return {
        status: 200,
        headers: {},
        body: JSON.stringify({
          code: 0,
          data: { account: { uid: "oauth-user", email: "oauth@example.com" } },
        }),
      };
    },
  });
  const begun = await codeBuddyOAuth.begin(flow);
  assertEquals(begun.userCode, "");
  assertEquals((await codeBuddyOAuth.poll(begun.session, flow)).status, "pending");
  const completed = await codeBuddyOAuth.poll(begun.session, flow);
  assert(completed.status === "completed");
  assertEquals(completed.resources[0].key, "codebuddy:oauth-user");
});

Deno.test("device OAuth rejects an authorization URL outside CodeBuddy CN", async () => {
  const flow = context({
    fetch: () => ({
      status: 200,
      headers: {},
      body: JSON.stringify({
        code: 0,
        data: { state: "state-1", authUrl: "https://example.com/login?state=state-1" },
      }),
    }),
  });
  let message = "";
  try {
    await codeBuddyOAuth.begin(flow);
  } catch (error) {
    message = error instanceof Error ? error.message : String(error);
  }
  assertEquals(message, "CodeBuddy authorization URL is invalid");
});

Deno.test("provider streams CodeBuddy chat with selected effort and safe headers", async () => {
  const token = jwt({ sub: "user-1" });
  const draft = await credentialDraft({
    accessToken: token,
    refreshToken: null,
    expiresAtMs: Date.now() + 3_600_000,
    refreshExpiresAtMs: null,
    uid: "user-1",
    email: "user@example.com",
    nickname: null,
    enterpriseName: null,
    enterpriseId: null,
    domain: null,
  });
  let body = "";
  let headers: Record<string, string> = {};
  const events: ModelEvent[] = [];
  const result = await codeBuddyProvider.invoke(
    {
      model: { id: "hy3", displayName: "Hy3", privateData: { reasoningEfforts: ["low", "high"] } },
      resource: snapshot(draft.privateData),
      request: request(),
    },
    { emit: (event: ModelEvent) => events.push(event) },
    context({
      stream: (url, init) => {
        assertEquals(url, "https://copilot.tencent.com/v2/chat/completions");
        body = init?.body ?? "";
        headers = init?.headers ?? {};
        return {
          status: 200,
          headers: {},
          lines: sse([
            'data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}',
            "data: [DONE]",
          ]),
        };
      },
    }),
  );
  assertEquals(result.status, "completed");
  const parsed = JSON.parse(body) as Record<string, unknown>;
  assertEquals(parsed.model, "hy3");
  assertEquals(parsed.stream, true);
  assertEquals(parsed.reasoning_effort, "high");
  assertEquals(parsed.reasoning_summary, "auto");
  assert(!("service_tier" in parsed));
  assert(!("max_completion_tokens" in parsed));
  assertEquals(headers["x-codebuddy-request"], "1");
  assertEquals(headers.Authorization, `Bearer ${token}`);
  assertEquals(headers.authorization, undefined);
  assert(events.some((event) => event.type === "done"));
});

Deno.test("provider maps invalid credentials and quota responses", async () => {
  const token = jwt({ sub: "user-1" });
  const draft = await credentialDraft({
    accessToken: token,
    refreshToken: null,
    expiresAtMs: Date.now() + 3_600_000,
    refreshExpiresAtMs: null,
    uid: "user-1",
    email: "user@example.com",
    nickname: null,
    enterpriseName: null,
    enterpriseId: null,
    domain: null,
  });
  const invalid = await codeBuddyProvider.invoke(
    {
      model: { id: "hy3", displayName: "Hy3" },
      resource: snapshot(draft.privateData),
      request: request(),
    },
    { emit: () => {} },
    context({ stream: () => ({ status: 401, headers: {}, lines: sse(["authorization denied"]) }) }),
  );
  assertEquals(invalid.status, "resource-error");
  const quota = await codeBuddyProvider.invoke(
    {
      model: { id: "hy3", displayName: "Hy3" },
      resource: snapshot(draft.privateData),
      request: request(),
    },
    { emit: () => {} },
    context({ stream: () => ({ status: 429, headers: {}, lines: sse(["quota exhausted"]) }) }),
  );
  assertEquals(quota.status, "resource-error");
});

Deno.test("check-in action reports success and already-checked-in responses", async () => {
  const draft = await credentialDraft({
    accessToken: "access-1",
    refreshToken: null,
    expiresAtMs: Date.now() + 3_600_000,
    refreshExpiresAtMs: null,
    uid: "user-1",
    email: "user@example.com",
    nickname: null,
    enterpriseName: null,
    enterpriseId: null,
    domain: null,
  });
  const success = await checkInAction.run(
    snapshot(draft.privateData),
    null,
    context({
      fetch: (url) => {
        if (url.endsWith("checkin-activity-status")) {
          return {
            status: 200,
            headers: {},
            body: JSON.stringify({ code: 0, data: { today_checked_in: false } }),
          };
        }
        if (url.endsWith("daily-checkin")) {
          return {
            status: 200,
            headers: {},
            body: JSON.stringify({ code: 0, data: { reward: 5, balance: 20 } }),
          };
        }
        return {
          status: 200,
          headers: {},
          body: JSON.stringify({ code: 0, data: { Response: { Data: { Accounts: [] } } } }),
        };
      },
    }),
  );
  assertEquals(success.title, { "en-US": "CodeBuddy checked in", "zh-CN": "CodeBuddy 签到成功" });
  const already = await checkInAction.run(
    snapshot(draft.privateData),
    null,
    context({
      fetch: (url) =>
        url.endsWith("checkin-activity-status")
          ? {
            status: 200,
            headers: {},
            body: JSON.stringify({ code: 0, data: { today_checked_in: true } }),
          }
          : {
            status: 200,
            headers: {},
            body: JSON.stringify({ code: 0, data: { Response: { Data: { Accounts: [] } } } }),
          },
    }),
  );
  assertEquals(already.title, {
    "en-US": "CodeBuddy already checked in",
    "zh-CN": "CodeBuddy 今日已签到",
  });
  assertEquals(parseCheckinResult({ code: 0, data: { today_checked_in: true } }).already, true);
});

Deno.test("check-in rejects an unrecognized 2xx submission body", async () => {
  const draft = await credentialDraft({
    accessToken: "access-1",
    refreshToken: null,
    expiresAtMs: Date.now() + 3_600_000,
    refreshExpiresAtMs: null,
    uid: "user-1",
    email: "user@example.com",
    nickname: null,
    enterpriseName: null,
    enterpriseId: null,
    domain: null,
  });
  let message = "";
  try {
    await checkInAction.run(
      snapshot(draft.privateData),
      null,
      context({
        fetch: (url) =>
          url.endsWith("checkin-activity-status")
            ? {
              status: 200,
              headers: {},
              body: JSON.stringify({ code: 0, data: { today_checked_in: false } }),
            }
            : { status: 200, headers: {}, body: "{}" },
      }),
    );
  } catch (error) {
    message = error instanceof Error ? error.message : String(error);
  }
  assertEquals(message, "CodeBuddy check-in response was not recognized");
});

Deno.test("expired token refresh persists credentials and fetches quota", async () => {
  const draft = await credentialDraft({
    accessToken: jwt({ sub: "refresh-user", exp: Math.floor(Date.now() / 1000) + 60 }),
    refreshToken: "refresh-secret",
    expiresAtMs: Date.now() + 60_000,
    refreshExpiresAtMs: Date.now() + 86_400_000,
    uid: "refresh-user",
    email: null,
    nickname: null,
    enterpriseName: null,
    enterpriseId: null,
    domain: null,
  });
  const urls: string[] = [];
  const patch = await refreshAccount(
    snapshot(draft.privateData),
    context({
      fetch: (url, init) => {
        urls.push(url);
        if (url.endsWith("/auth/token/refresh")) {
          assertEquals(init?.body, "{}");
          assertEquals(init?.headers?.["X-Refresh-Token"], "refresh-secret");
          assertEquals(init?.headers?.["X-Auth-Refresh-Source"], "plugin");
          return {
            status: 200,
            headers: {},
            body: JSON.stringify({
              code: 0,
              data: { accessToken: "new-access", refreshToken: "new-refresh", expiresIn: 3600 },
            }),
          };
        }
        return {
          status: 200,
          headers: {},
          body: JSON.stringify({
            code: 0,
            data: {
              Response: {
                Data: {
                  Accounts: [{
                    PackageName: "Pro",
                    CapacitySizePrecise: 10,
                    CapacityUsedPrecise: 3,
                  }],
                },
              },
            },
          }),
        };
      },
    }),
  );
  const privateData = patch.privateData as Record<string, unknown>;
  assertEquals(privateData.accessToken, "new-access");
  assertEquals(privateData.refreshToken, "new-refresh");
  assertEquals(urls.length, 2);
});

Deno.test("check-in falls back on 404 and retries the selected status route after auth refresh", async () => {
  const draft = await credentialDraft({
    accessToken: "old-access",
    refreshToken: "refresh-secret",
    expiresAtMs: Date.now() + 3_600_000,
    refreshExpiresAtMs: null,
    uid: "user-1",
    email: "user@example.com",
    nickname: null,
    enterpriseName: null,
    enterpriseId: null,
    domain: null,
  });
  const urls: string[] = [];
  const result = await checkInAction.run(
    snapshot(draft.privateData),
    null,
    context({
      fetch: (url) => {
        urls.push(url);
        if (url.endsWith("checkin-activity-status")) {
          return { status: 404, headers: {}, body: "{}" };
        }
        if (url.endsWith("checkin-status")) {
          return urls.filter((item) => item.endsWith("checkin-status")).length === 1
            ? { status: 401, headers: {}, body: JSON.stringify({ code: 401 }) }
            : {
              status: 200,
              headers: {},
              body: JSON.stringify({ code: 0, data: { today_checked_in: true } }),
            };
        }
        if (url.endsWith("auth/token/refresh")) {
          return {
            status: 200,
            headers: {},
            body: JSON.stringify({ code: 0, data: { accessToken: "new-access", expiresIn: 3600 } }),
          };
        }
        if (url.endsWith("get-user-resource")) {
          return {
            status: 200,
            headers: {},
            body: JSON.stringify({ code: 0, data: { Response: { Data: { Accounts: [] } } } }),
          };
        }
        throw new Error(`unexpected URL ${url}`);
      },
    }),
  );
  assertEquals(result.title, {
    "en-US": "CodeBuddy already checked in",
    "zh-CN": "CodeBuddy 今日已签到",
  });
  assertEquals(urls.filter((url) => url.endsWith("checkin-status")).length, 2);
});

Deno.test("check-in rechecks status before retrying after auth failure", async () => {
  const draft = await credentialDraft({
    accessToken: "old-access",
    refreshToken: "refresh-secret",
    expiresAtMs: Date.now() + 3_600_000,
    refreshExpiresAtMs: null,
    uid: "user-1",
    email: "user@example.com",
    nickname: null,
    enterpriseName: null,
    enterpriseId: null,
    domain: null,
  });
  const urls: string[] = [];
  const result = await checkInAction.run(
    snapshot(draft.privateData),
    null,
    context({
      fetch: (url) => {
        urls.push(url);
        if (url.endsWith("checkin-activity-status")) {
          return urls.filter((item) => item.endsWith("checkin-activity-status")).length === 1
            ? {
              status: 200,
              headers: {},
              body: JSON.stringify({ code: 0, data: { today_checked_in: false } }),
            }
            : {
              status: 200,
              headers: {},
              body: JSON.stringify({ code: 0, data: { today_checked_in: true } }),
            };
        }
        if (url.endsWith("daily-checkin")) {
          return { status: 401, headers: {}, body: JSON.stringify({ code: 401 }) };
        }
        if (url.endsWith("auth/token/refresh")) {
          return {
            status: 200,
            headers: {},
            body: JSON.stringify({ code: 0, data: { accessToken: "new-access", expiresIn: 3600 } }),
          };
        }
        if (url.endsWith("get-user-resource")) {
          return {
            status: 200,
            headers: {},
            body: JSON.stringify({ code: 0, data: { Response: { Data: { Accounts: [] } } } }),
          };
        }
        throw new Error(`unexpected URL ${url}`);
      },
    }),
  );
  assertEquals(result.title, {
    "en-US": "CodeBuddy already checked in",
    "zh-CN": "CodeBuddy 今日已签到",
  });
  assertEquals(urls.filter((url) => url.endsWith("daily-checkin")).length, 1);
  assertEquals(urls.filter((url) => url.endsWith("checkin-activity-status")).length, 2);
});

Deno.test("OAuth rejects international domains", async () => {
  const response = {
    status: 200,
    headers: {},
    body: JSON.stringify({
      code: 0,
      data: { accessToken: "intl-token", domain: "www.workbuddy.ai" },
    }),
  };
  const result = await codeBuddyOAuth.poll(
    { state: "intl-state" },
    context({ fetch: () => response }),
  );
  assertEquals(result.status, "failed");
});

Deno.test("provider handles xhigh selection and neutralizes long agent identity", async () => {
  const draft = await credentialDraft({
    accessToken: "access-token",
    refreshToken: null,
    expiresAtMs: Date.now() + 3_600_000,
    refreshExpiresAtMs: null,
    uid: "user-1",
    email: "user@example.com",
    nickname: null,
    enterpriseName: null,
    enterpriseId: null,
    domain: null,
  });
  let body = "";
  await codeBuddyProvider.invoke(
    {
      model: {
        id: "hy3",
        displayName: "Hy3",
        privateData: { reasoningEfforts: ["low", "high", "xhigh"] },
      },
      resource: snapshot(draft.privateData),
      request: {
        ...request(),
        instructions: "You are Cursor. " + "x".repeat(2100),
        reasoning: { enabled: true, effort: "max" },
      },
    },
    { emit: () => {} },
    context({
      stream: (_url, init) => {
        body = init?.body ?? "";
        return {
          status: 200,
          headers: {},
          lines: sse([
            'data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}',
            "data: [DONE]",
          ]),
        };
      },
    }),
  );
  const parsed = JSON.parse(body) as Record<string, unknown>;
  assertEquals(parsed.reasoning_effort, "xhigh");
  const messages = parsed.messages as Array<{ role: string; content: string }>;
  assertEquals(
    messages[0],
    {
      role: "system",
      content: "You are a helpful AI assistant that helps with software engineering tasks.",
    },
  );
});

Deno.test("provider omits reasoning fields when effort is off", async () => {
  const draft = await credentialDraft({
    accessToken: "access-token",
    refreshToken: null,
    expiresAtMs: Date.now() + 3_600_000,
    refreshExpiresAtMs: null,
    uid: "user-1",
    email: "user@example.com",
    nickname: null,
    enterpriseName: null,
    enterpriseId: null,
    domain: null,
  });
  let body = "";
  await codeBuddyProvider.invoke(
    {
      model: { id: "hy3", displayName: "Hy3" },
      resource: snapshot(draft.privateData),
      request: { ...request(), reasoning: { enabled: false, effort: "off" } },
    },
    { emit: () => {} },
    context({
      stream: (_url, init) => {
        body = init?.body ?? "";
        return {
          status: 200,
          headers: {},
          lines: sse([
            'data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}',
            "data: [DONE]",
          ]),
        };
      },
    }),
  );
  const parsed = JSON.parse(body) as Record<string, unknown>;
  assert(!("reasoning_effort" in parsed));
  assert(!("reasoning_summary" in parsed));
});

Deno.test("quota treats code zero with empty accounts as no package", async () => {
  const draft = await credentialDraft({
    accessToken: "access-token",
    refreshToken: null,
    expiresAtMs: Date.now() + 3_600_000,
    refreshExpiresAtMs: null,
    uid: "user-1",
    email: "user@example.com",
    nickname: null,
    enterpriseName: null,
    enterpriseId: null,
    domain: null,
  });
  const patch = await refreshAccount(
    snapshot(draft.privateData),
    context({
      fetch: () => ({
        status: 200,
        headers: {},
        body: JSON.stringify({ code: 0, data: { Response: { Data: { Accounts: [] } } } }),
      }),
    }),
  );
  assertEquals(patch.state, { status: "ready" });
});

Deno.test("quota exhaustion cools even without a prior balance", async () => {
  const patch = quotaExhaustedPatch(
    {
      accessToken: "access-token",
      refreshToken: null,
      expiresAtMs: Date.now() + 3_600_000,
      refreshExpiresAtMs: null,
      uid: "user-1",
      email: "user@example.com",
      nickname: null,
      enterpriseName: null,
      enterpriseId: null,
      domain: null,
      quota: null,
      lastCheckin: null,
    },
    "quota exhausted",
    1_700_000_000_000,
  );
  assertEquals(patch.state, {
    status: "cooling",
    retryAtMs: 1_700_003_600_000,
    message: "CodeBuddy credits are exhausted",
  });
});

Deno.test("billing requests follow a www.codebuddy.cn account domain", async () => {
  const draft = await credentialDraft({
    accessToken: "access-token",
    refreshToken: null,
    expiresAtMs: Date.now() + 3_600_000,
    refreshExpiresAtMs: null,
    uid: "user-1",
    email: "user@example.com",
    nickname: null,
    enterpriseName: null,
    enterpriseId: null,
    domain: "www.codebuddy.cn",
  });
  let quotaUrl = "";
  await refreshAccount(
    snapshot(draft.privateData),
    context({
      fetch: (url) => {
        quotaUrl = url;
        return {
          status: 200,
          headers: {},
          body: JSON.stringify({
            code: 0,
            data: {
              Response: {
                Data: {
                  Accounts: [{
                    CycleEndTime: "2026-10-01T00:00:00Z",
                    DeductionEndTime: "2026-09-20T00:00:00Z",
                    CycleCapacitySize: 100,
                    CycleCapacityUsed: 20,
                  }],
                },
              },
            },
          }),
        };
      },
    }),
  );
  assertEquals(quotaUrl, "https://www.codebuddy.cn/v2/billing/meter/get-user-resource");
});

Deno.test("quota refresh preserves cooling when token refresh succeeds but quota fails", async () => {
  const draft = await credentialDraft({
    accessToken: jwt({ sub: "quota-user", exp: Math.floor(Date.now() / 1000) + 60 }),
    refreshToken: "refresh-secret",
    expiresAtMs: Date.now() + 60_000,
    refreshExpiresAtMs: Date.now() + 86_400_000,
    uid: "quota-user",
    email: null,
    nickname: null,
    enterpriseName: null,
    enterpriseId: null,
    domain: null,
  });
  const retryAtMs = Date.now() + 60_000;
  const patch = await refreshAccount(
    {
      ...snapshot(draft.privateData),
      state: { status: "cooling", retryAtMs, message: "exhausted" },
    },
    context({
      fetch: (url) =>
        url.endsWith("/auth/token/refresh")
          ? {
            status: 200,
            headers: {},
            body: JSON.stringify({
              code: 0,
              data: { accessToken: "new-access", refreshToken: "new-refresh", expiresIn: 3600 },
            }),
          }
          : {
            status: 200,
            headers: {},
            body: JSON.stringify({ code: 5001, message: "quota unavailable" }),
          },
    }),
  );
  assertEquals(patch.state, { status: "cooling", retryAtMs, message: "exhausted" });
  assertEquals((patch.privateData as Record<string, unknown>).accessToken, "new-access");
});

Deno.test("quota business errors preserve upstream message", async () => {
  const draft = await credentialDraft({
    accessToken: "access-token",
    refreshToken: null,
    expiresAtMs: Date.now() + 3_600_000,
    refreshExpiresAtMs: null,
    uid: "user-1",
    email: "user@example.com",
    nickname: null,
    enterpriseName: null,
    enterpriseId: null,
    domain: null,
  });
  let message = "";
  try {
    await refreshAccount(
      snapshot(draft.privateData),
      context({
        fetch: () => ({
          status: 200,
          headers: {},
          body: JSON.stringify({ code: 5001, message: "quota unavailable" }),
        }),
      }),
    );
  } catch (error) {
    message = error instanceof Error ? error.message : String(error);
  }
  assert(message.includes("quota unavailable"), message);
});

Deno.test("check-in declares itself a hidden default-on daily automation", () => {
  // The host drops the automation from the desktop projection only when the
  // descriptor carries hidden, so both flags must survive serialization.
  assertEquals(checkInAction.automation, { kind: "daily", defaultEnabled: true, hidden: true });
});
