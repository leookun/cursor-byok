import { callableModels, resolveModelRoute } from "./model_routes.ts";

const PUBLIC_NAMES = [
  "Gemini 3.8 Flash",
  "Gemini 3.6 Flash",
  "Gemini 3.1 Pro",
  "Claude Sonnet 4.6 (thinking)",
  "Claude Opus 4.6 (thinking)",
  "GPT-OSS-120b",
];

function equal(actual: unknown, expected: unknown) {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`expected ${JSON.stringify(expected)}, received ${JSON.stringify(actual)}`);
  }
}
const model = (id: string, thinkingBudget = 1000) => ({
  id,
  displayName: id,
  maxOutputTokens: 8192,
  privateData: { thinkingBudget },
});

Deno.test("only models that the docs publish as reasoning models survive", () => {
  equal(
    callableModels([
      model("gemini-2.5-flash"),
      model("gemini-3.1-flash-lite"),
      model("gemini-3.5-flash"),
      model("gemini-3.1-flash-image"),
      model("gemini-3.8-flash-high"),
      model("claude-sonnet-4-6-thinking"),
      model("gpt-oss-120b-medium"),
    ], PUBLIC_NAMES).map((entry) => entry.id),
    ["gemini-3.8-flash", "claude-sonnet-4-6", "gpt-oss-120b"],
  );
});

Deno.test("discovered variants collapse into one public family in docs order", () => {
  const entries = callableModels([
    model("gemini-3.6-flash-low", 10),
    model("gemini-3.6-flash-medium", 20),
    model("gemini-3.6-flash-high", 30),
    model("gemini-3.8-flash-high", 40),
  ], PUBLIC_NAMES);
  equal(entries.map((entry) => entry.id), ["gemini-3.8-flash", "gemini-3.6-flash"]);
  equal(entries[1].displayName, "Gemini 3.6 Flash");
  equal(entries[1].privateData, {
    routes: [
      { id: "gemini-3.6-flash-high", tier: "high", thinkingBudget: 30, maxOutputTokens: 8192 },
      { id: "gemini-3.6-flash-medium", tier: "medium", thinkingBudget: 20, maxOutputTokens: 8192 },
      { id: "gemini-3.6-flash-low", tier: "low", thinkingBudget: 10, maxOutputTokens: 8192 },
    ],
  });
  equal(resolveModelRoute(entries[1], null).id, "gemini-3.6-flash-high");
  equal(resolveModelRoute(entries[1], "medium").id, "gemini-3.6-flash-medium");
  equal(resolveModelRoute(entries[1], "low").id, "gemini-3.6-flash-low");
});

Deno.test("a docs entry added later is picked up without code changes", () => {
  const entries = callableModels([
    model("gemini-3.9-flash-high", 50),
    model("gemini-3.8-flash-high", 40),
  ], ["Gemini 3.9 Flash", ...PUBLIC_NAMES]);

  equal(entries.map((entry) => [entry.id, entry.displayName]), [
    ["gemini-3.9-flash", "Gemini 3.9 Flash"],
    ["gemini-3.8-flash", "Gemini 3.8 Flash"],
  ]);
  equal(resolveModelRoute(entries[0], "low").id, "gemini-3.9-flash-high");
});

Deno.test("docs names differing only by separators still match upstream IDs", () => {
  const entries = callableModels([
    model("claude-opus-4-6", 1),
    model("claude-opus-4-6-tiered", 2),
    model("claude-opus-4-6-thinking", 3),
  ], PUBLIC_NAMES);

  equal(entries.length, 1);
  equal(entries[0].id, "claude-opus-4-6");
  equal(entries[0].displayName, "Claude Opus 4.6 (thinking)");
  equal(resolveModelRoute(entries[0], null).id, "claude-opus-4-6-thinking");
  equal(resolveModelRoute(entries[0], "medium").id, "claude-opus-4-6-tiered");
  equal(resolveModelRoute(entries[0], "high").id, "claude-opus-4-6-thinking");
});

Deno.test("unknown effort and stale route data are rejected", () => {
  const entry = callableModels([model("gemini-3.8-flash-high")], PUBLIC_NAMES)[0];
  let unsupported = false;
  try {
    resolveModelRoute(entry, "unknown");
  } catch {
    unsupported = true;
  }
  equal(unsupported, true);

  let rejected = false;
  try {
    resolveModelRoute({
      ...entry,
      privateData: {
        routes: [{
          id: "gemini-3.6-flash-high",
          tier: "high",
          thinkingBudget: 1,
          maxOutputTokens: 1,
        }],
      },
    }, null);
  } catch {
    rejected = true;
  }
  equal(rejected, true);

  let aliased = false;
  try {
    resolveModelRoute({ ...entry, privateData: { routes: ["gemini-3.8-flash-high"] } }, null);
  } catch {
    aliased = true;
  }
  equal(aliased, true);
});
