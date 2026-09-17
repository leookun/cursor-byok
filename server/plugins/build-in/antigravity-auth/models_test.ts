import type { PluginContext } from "cursor-byok:plugin";
import type { ResourceSnapshot } from "cursor-byok:resource";
import { antigravityModels, fetchAntigravityCatalog, parseAntigravityCatalog } from "./models.ts";
import { RESOURCE_TYPE } from "./resources.ts";

function assert(condition: unknown, message = "assertion failed"): asserts condition {
  if (!condition) throw new Error(message);
}

function assertEquals(actual: unknown, expected: unknown): void {
  const left = JSON.stringify(actual);
  const right = JSON.stringify(expected);
  if (left !== right) throw new Error(`expected ${right}, received ${left}`);
}

const DOCS_PAGE = `| Model | Free & Google AI Plus |
| --- | --- |
| [Gemini 3.8 Flash](/blog/gemini-3-8-flash-in-google-antigravity) | ✅ |
| Claude Sonnet 4.6 (thinking) | ✅ |
`;

const DISCOVERY = JSON.stringify({
  models: {
    "gemini-3.8-flash-high": {
      displayName: "Gemini 3.8 Flash High",
      supportsThinking: true,
      supportedMimeTypes: { "image/png": true },
      maxOutputTokens: 32768,
      quotaInfo: { remainingFraction: 0.734, resetTime: "2026-09-10T12:00:00Z" },
    },
    "claude-sonnet-4-6-thinking": {
      displayName: "Claude Sonnet 4.6 (Thinking)",
      maxOutputTokens: 65535,
      thinkingBudget: 10001,
    },
    "gemini-3.1-flash-image": {
      displayName: "Gemini 3.1 Flash Image",
      supportedMimeTypes: { "image/png": true },
    },
    "chat_20706": { displayName: "Internal Chat Model" },
  },
});

function context(
  fetch: PluginContext["network"]["fetch"],
): PluginContext {
  return {
    network: {
      fetch,
      stream: () => Promise.reject(new Error("stream is not expected")),
    },
    signal: new AbortController().signal,
  };
}

function snapshot(): ResourceSnapshot {
  return {
    id: "resource-1",
    type: RESOURCE_TYPE,
    key: "antigravity:user@example.com",
    privateData: {
      accessToken: "access-token",
      refreshToken: "refresh-token",
      displayName: "user@example.com",
      projectId: "real-project",
      quota: null,
    },
    state: { status: "ready" },
  };
}

Deno.test("fetchAvailableModels keeps only real IDs and metadata", () => {
  const models = parseAntigravityCatalog(JSON.parse(DISCOVERY));
  assertEquals(models.map((model) => model.id), [
    "gemini-3.8-flash-high",
    "claude-sonnet-4-6-thinking",
    "gemini-3.1-flash-image",
    "chat_20706",
  ]);
  assertEquals(models[0], {
    id: "gemini-3.8-flash-high",
    displayName: "Gemini 3.8 Flash High",
    capabilities: { images: true },
    maxOutputTokens: 32768,
    privateData: { thinkingBudget: 0 },
  });
  assertEquals(models[1].privateData, { thinkingBudget: 10001 });
  assertEquals(parseAntigravityCatalog({ models: {} }), []);
});

Deno.test("official deprecated model forwarding uses the current ID and deduplicates", () => {
  const models = parseAntigravityCatalog({
    deprecatedModelIds: { "gemini-old": { newModelId: "gemini-current" } },
    models: {
      "gemini-old": { displayName: "Old" },
      "gemini-current": { displayName: "Current" },
    },
  });
  assertEquals(models.map((model) => [model.id, model.displayName]), [
    ["gemini-current", "Current"],
  ]);
});

Deno.test("403 model discovery clears the catalog after retrying without project", async () => {
  const bodies: string[] = [];
  const models = await fetchAntigravityCatalog(
    "access-token",
    "real-project",
    context((_, init = {}) => {
      bodies.push(init.body ?? "");
      return Promise.resolve({ status: 403, headers: {}, body: "forbidden" });
    }).network,
  );

  assertEquals(bodies, [JSON.stringify({ project: "real-project" }), JSON.stringify({})]);
  assertEquals(models, []);
});

Deno.test("model listing requires an account and returns the docs-verified catalog", async () => {
  let rejected = false;
  try {
    await antigravityModels.list(
      { resource: null },
      context(() => {
        throw new Error("fetch is not expected");
      }),
    );
  } catch {
    rejected = true;
  }
  assert(rejected, "listing without an account must fail before network access");

  const urls: string[] = [];
  const models = await antigravityModels.list(
    { resource: snapshot() },
    context((url) => {
      urls.push(url);
      return Promise.resolve(
        url.includes("/docs/models")
          ? { status: 200, headers: {}, body: DOCS_PAGE }
          : { status: 200, headers: {}, body: DISCOVERY },
      );
    }),
  );

  assertEquals(urls.sort(), [
    "https://antigravity.google/docs/models.md",
    "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:fetchAvailableModels",
  ]);
  assertEquals(models.map((model) => [model.id, model.displayName]), [
    ["gemini-3.8-flash", "Gemini 3.8 Flash"],
    ["claude-sonnet-4-6", "Claude Sonnet 4.6 (thinking)"],
  ]);

  const empty = await antigravityModels.list(
    { resource: snapshot() },
    context((url) =>
      Promise.resolve({
        status: 200,
        headers: {},
        body: url.includes("/docs/models") ? DOCS_PAGE : JSON.stringify({ models: {} }),
      })
    ),
  );
  assertEquals(empty, []);
});
