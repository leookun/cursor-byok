import { ProviderError, type ProviderInvokeInput, type ProviderSupport } from "cursor-byok:provider";
import type { PluginContext } from "cursor-byok:plugin";
import { codexProvider } from "./codex-auth/provider.ts";
import { grokProvider } from "./grok-auth/provider.ts";
import { antigravityProvider } from "./antigravity-auth/provider.ts";
import { parseAntigravityModels } from "./antigravity-auth/models.ts";

function equal(actual: unknown, expected: unknown) {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`expected ${JSON.stringify(expected)}, received ${JSON.stringify(actual)}`);
  }
}

const input: ProviderInvokeInput = {
  model: { id: "model", displayName: "Model" },
  resource: {
    id: "account", key: "account", type: "account", state: { status: "ready" },
    privateData: { accessToken: "test", accountId: "test", projectId: "test" },
  },
  request: {
    instructions: "", messages: [], tools: [], reasoning: { enabled: false, effort: null },
    latency: "standard", maxOutputTokens: null, cacheKey: null,
  },
};

function context(stream: PluginContext["network"]["stream"]): PluginContext {
  return {
    signal: new AbortController().signal,
    network: { stream, fetch: () => Promise.reject(new Error("unexpected fetch")) },
  };
}

for (const provider of [codexProvider, grokProvider, antigravityProvider] as ProviderSupport[]) {
  Deno.test(`${provider.id} preserves typed transport errors and leaves plugin exceptions untyped`, async () => {
    for (const error of [new ProviderError("connection closed", { kind: "transient" }), new Error("plugin exception")]) {
      const result = await provider.invoke(input, { emit() {} }, context(() => Promise.reject(error)));
      if (result.status === "completed") throw new Error("expected failure");
      equal(result.failure, error instanceof ProviderError ? { kind: "transient" } : undefined);
    }
  });

  Deno.test(`${provider.id} classifies SSE machine codes without reading messages`, async () => {
    for (const [code, kind] of [
      ["rate_limit_exceeded", "rate_limit"],
      ["authentication_error", "authorization"],
      ["server_error", "transient"],
      ["invalid_request_error", "request"],
      ["unknown_error", undefined],
    ] as const) {
      const error = { code, message: "429 quota exceeded unauthorized timeout" };
      const envelope = provider.id === "codex"
        ? { type: "response.failed", response: { error } }
        : { error };
      const result = await provider.invoke(input, { emit() {} }, context(() => Promise.resolve({
        status: 200, headers: { "retry-after": "2" },
        lines: (async function* () { yield `data: ${JSON.stringify(envelope)}`; })(),
      })));
      if (result.status === "completed") throw new Error("expected SSE failure");
      equal(result.failure, kind === undefined ? undefined : { kind, retryAfterMs: 2000 });
      equal(result.status, kind === "rate_limit" || kind === "authorization" ? "resource-error" : "request-error");
      if (result.status === "resource-error") {
        equal(result.patch.state?.status, kind === "rate_limit" ? "cooling" : "invalid");
      }
    }
  });

  Deno.test(`${provider.id} retains HTTP status and Retry-After across its catch`, async () => {
    const result = await provider.invoke(input, { emit() {} }, context(() => Promise.resolve({
      status: 429,
      headers: { "retry-after": "3" },
      lines: (async function* () { yield '{"error":"limited"}'; })(),
    })));
    if (result.status === "completed") throw new Error("expected failure");
    equal(result.failure, { kind: "rate_limit", status: 429, retryAfterMs: 3000 });
  });
}

Deno.test("Antigravity preserves a new failure after authorization refresh", async () => {
  const retryInput: ProviderInvokeInput = {
    ...input,
    resource: { ...input.resource!, privateData: {
      accessToken: "old", refreshToken: "refresh", projectId: "test", expiresAtMs: Date.now() + 3_600_000,
    } },
  };
  const result = await antigravityProvider.invoke(retryInput, { emit() {} }, {
    signal: new AbortController().signal,
    network: {
      fetch: () => Promise.resolve({ status: 200, headers: {}, body: JSON.stringify({
        access_token: "fresh", expires_in: 3600, cloudaicompanionProject: "test", models: {},
      }) }),
      stream: (_url, init) => Promise.resolve({
        status: init?.headers?.authorization === "Bearer fresh" ? 503 : 401,
        headers: { "retry-after": "5" },
        lines: (async function* () { yield "opaque failure"; })(),
      }),
    },
  });
  if (result.status === "completed") throw new Error("expected failure");
  equal(result.failure, { kind: "transient", status: 503, retryAfterMs: 5000 });
});

Deno.test("Antigravity discovery preserves explicit metadata and does not infer it from names", () => {
  const models = parseAntigravityModels({ models: {
    "gemini-unknown-test": { displayName: "Unknown" },
    "reported-test": { contextWindow: 64_000, supportsTools: false, supportsImages: true, maxOutputTokens: 4096 },
  } });
  const unknown = models.find((model) => model.id === "gemini-unknown-test")!;
  equal(unknown.capabilities, {});
  equal(unknown.images, true);
  equal(unknown.contextWindowTokens, undefined);
  equal(unknown.maxOutputTokens, undefined);
  const known = models.find((model) => model.id === "reported-test")!;
  equal(known.contextWindowTokens, 64_000);
  equal(known.capabilities, { tools: false, images: true });
  equal(known.maxOutputTokens, 4096);
});
