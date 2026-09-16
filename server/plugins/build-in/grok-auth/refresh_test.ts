import type { JsonValue, NetworkResponse, PluginContext } from "cursor-byok:plugin";
import type { ResourcePatch, ResourceSnapshot } from "cursor-byok:resource";
import { prepareAccount, refreshAccount, RESOURCE_TYPE } from "./resources.ts";
import { grokProvider } from "./provider.ts";

function assert(value: unknown, message = "assertion failed"): asserts value {
  if (!value) throw new Error(message);
}
function jwt(seconds: number): string {
  return `header.${
    btoa(JSON.stringify({ sub: "test", exp: Math.floor(Date.now() / 1000) + seconds }))
  }.sig`;
}
function account(seconds: number): ResourceSnapshot {
  return {
    id: "test",
    type: RESOURCE_TYPE,
    key: "grok:test",
    state: { status: "ready" },
    privateData: {
      accessToken: jwt(seconds),
      refreshToken: "refresh-old",
      displayName: "test",
      quota: null,
    },
  };
}
function data(value: ResourceSnapshot | ResourcePatch): Record<string, JsonValue> {
  return value.privateData as Record<string, JsonValue>;
}
function apply(resource: ResourceSnapshot, patch: ResourcePatch): ResourceSnapshot {
  return {
    ...resource,
    privateData: patch.privateData ?? resource.privateData,
    state: patch.state ?? resource.state,
  };
}
function context(fetch: PluginContext["network"]["fetch"]): PluginContext {
  return {
    signal: new AbortController().signal,
    network: {
      fetch,
      stream: () => {
        throw new Error("unexpected stream");
      },
    },
  };
}
function response(status: number, body: unknown): NetworkResponse {
  return { status, body: JSON.stringify(body), headers: {} };
}
const unexpected = context(() => {
  throw new Error("unexpected network request");
});

Deno.test("healthy tokens do not need a refresh", async () => {
  assert(await prepareAccount(account(3600), null, unexpected) === null);
});

Deno.test("expired account recovers and persists both rotated tokens in one patch", async () => {
  const resource = account(-10);
  resource.state = { status: "invalid", message: "expired" };
  let calls = 0;
  const nextAccess = jwt(21_600);
  const patch = await prepareAccount(
    resource,
    null,
    context(async (url, init) => {
      calls++;
      assert(url === "https://auth.x.ai/oauth2/token");
      const params = new URLSearchParams(init?.body);
      assert(params.get("grant_type") === "refresh_token");
      assert(params.get("refresh_token") === "refresh-old");
      return response(200, {
        access_token: nextAccess,
        refresh_token: "refresh-new",
        expires_in: 21600,
      });
    }),
  );
  assert(patch && calls === 1 && patch.state?.status === "ready");
  assert(data(patch).accessToken === nextAccess && data(patch).refreshToken === "refresh-new");
  assert(Number(data(patch).expiresAtMs) > Date.now() + 20_000_000);
  assert(await prepareAccount(apply(resource, patch), null, unexpected) === null);
});

Deno.test("near-expiry refresh retains the old refresh token when no rotation is returned", async () => {
  const patch = await prepareAccount(
    account(120),
    null,
    context(async () => response(200, { access_token: jwt(3600) })),
  );
  assert(patch && data(patch).refreshToken === "refresh-old" && patch.state?.status === "ready");
});

Deno.test("concurrent stale 401 does not rotate an already refreshed token again", async () => {
  const old = account(-10), current = account(3600);
  assert(await prepareAccount(current, old, unexpected) === null);
  let calls = 0;
  const patch = await prepareAccount(
    current,
    current,
    context(async () => {
      calls++;
      return response(200, { access_token: jwt(7200), refresh_token: "new" });
    }),
  );
  assert(calls === 1 && patch?.state?.status === "ready");
});

Deno.test("terminal refresh rejection is recorded and never retried without a new login", async () => {
  const resource = account(-10);
  const patch = await prepareAccount(
    resource,
    null,
    context(async () => response(400, { error: "invalid_grant" })),
  );
  assert(patch && patch.state?.status === "invalid");
  assert(String(data(patch).refreshError).includes("invalid_grant"));
  const again = await prepareAccount(apply(resource, patch), null, unexpected);
  assert(again?.state?.status === "invalid");
});

Deno.test("network failure keeps credentials and a fixed retry deadline instead of invalidating login", async () => {
  const resource = account(-10);
  let calls = 0;
  const patch = await prepareAccount(
    resource,
    null,
    context(async () => {
      calls++;
      throw new Error("offline");
    }),
  );
  assert(patch && patch.state?.status === "cooling" && calls === 1);
  assert(data(patch).refreshToken === "refresh-old" && data(patch).refreshError === undefined);
  const again = await prepareAccount(apply(resource, patch), null, unexpected);
  assert(again?.state?.status === "cooling");
  assert(again.state.retryAtMs === patch.state.retryAtMs);
});

Deno.test("a failed proactive refresh still permits an unexpired token", async () => {
  const patch = await prepareAccount(
    account(120),
    null,
    context(async () => {
      throw new Error("offline");
    }),
  );
  assert(patch?.state?.status === "ready");
});

Deno.test("a token that expires during a failed refresh is not reused", async () => {
  const originalNow = Date.now;
  let now = originalNow();
  Date.now = () => now;
  try {
    const resource = account(120);
    const patch = await prepareAccount(resource, null, context(async () => {
      now += 180_000;
      throw new Error("network timeout after sleep");
    }));
    assert(patch?.state?.status === "cooling");
  } finally {
    Date.now = originalNow;
  }
});

Deno.test("retryable HTTP responses back off and recover within three attempts", async () => {
  let calls = 0;
  const patch = await prepareAccount(
    account(-10),
    null,
    context(async () => {
      return ++calls < 3
        ? response(503, {})
        : response(200, { access_token: jwt(3600), refresh_token: "new" });
    }),
  );
  assert(calls === 3 && patch?.state?.status === "ready");
});

Deno.test("malformed successful refresh still preserves a returned rotated refresh token", async () => {
  const patch = await prepareAccount(
    account(-10),
    null,
    context(async () => response(200, { refresh_token: "rotated" })),
  );
  assert(patch?.state?.status === "cooling" && data(patch).refreshToken === "rotated");
});

Deno.test("quota denial remains a quota error, not an expired login", async () => {
  const patch = await refreshAccount(
    account(3600),
    context(async () => response(403, { error: "spending-limit" })),
  );
  assert(patch.state?.status === "cooling" && data(patch).refreshToken === "refresh-old");
});

for (const effort of ["xhigh", "max"]) {
  Deno.test(`Grok 4.6 transmits ${effort} as xhigh`, async () => {
    const ctx = context(async () => {
      throw new Error("unexpected fetch");
    });
    ctx.network.stream = async (_url, init) => {
      assert(JSON.parse(init?.body ?? "{}").reasoning_effort === "xhigh");
      return {
        status: 200,
        headers: {},
        lines: (async function* () {
          yield "data: [DONE]";
        })(),
      };
    };
    const result = await grokProvider.invoke(
      {
        model: { id: "grok-4.6", displayName: "Grok" },
        resource: account(3600),
        request: {
          instructions: "",
          messages: [],
          tools: [],
          reasoning: { enabled: true, effort },
          latency: "standard",
          maxOutputTokens: 16,
          cacheKey: null,
        },
      },
      { emit: () => {} },
      ctx,
    );
    assert(result.status === "completed");
  });
}

Deno.test("HTTP 401 asks the host for a pre-output refresh instead of invalidating the account", async () => {
  const ctx = context(async () => {
    throw new Error("unexpected fetch");
  });
  ctx.network.stream = async () => ({
    status: 401,
    headers: {},
    lines: (async function* () {
      yield "unauthorized";
    })(),
  });
  const events: unknown[] = [];
  const result = await grokProvider.invoke(
    {
      model: { id: "grok-4.6", displayName: "Grok" },
      resource: account(3600),
      request: {
        instructions: "",
        messages: [],
        tools: [],
        reasoning: { enabled: true, effort: "xhigh" },
        latency: "standard",
        maxOutputTokens: 16,
        cacheKey: null,
      },
    },
    { emit: (event) => events.push(event) },
    ctx,
  );
  assert(result.status === "auth-error" && events.length === 0);
});
