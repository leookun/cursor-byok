import type { PluginContext } from "cursor-byok:plugin";
import { parseCredentialFiles, presentAccount, queryAccountQuota, quotaExhaustedPatch } from "./resources.ts";
import type { AccountData } from "./resources.ts";
import { RESOURCE_TYPE } from "./resources.ts";

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

function context(requests: string[]): PluginContext["network"] {
  return {
    fetch: async (url, init = {}) => {
      requests.push(`${url}:${init.body ?? ""}`);
      return {
        status: 200,
        headers: {},
        body: JSON.stringify({
          access_token: "access-token",
          refresh_token: "refresh-token",
          expires_in: 3600,
        }),
      };
    },
    stream: () => Promise.reject(new Error("stream is not expected")),
  };
}

Deno.test("imports an array of email and refresh_token credentials", async () => {
  const requests: string[] = [];
  const result = await parseCredentialFiles([
    {
      name: "antigravity.json",
      content: JSON.stringify([
        { email: "teocougar@gmail.com", refresh_token: "refresh-token" },
      ]),
    },
  ], context(requests));

  assert(result.warnings.length === 0, "the credential file should not produce warnings");
  assert(result.credentials.length === 1, "one credential should be imported");
  assert(result.credentials[0].displayName === "teocougar@gmail.com", "email should be used as display name");
  assert(result.credentials[0].accessToken === "access-token", "refresh token should be exchanged for an access token");
  assert(result.credentials[0].refreshToken === "refresh-token", "refresh token should be preserved");
  assert(requests.length === 1, "the refresh token should be exchanged once");
});

function quotaContext(handlers: Record<string, () => { status: number; body: string }>): PluginContext["network"] {
  return {
    fetch: (url) => {
      for (const [fragment, handler] of Object.entries(handlers)) {
        if (url.includes(fragment)) return Promise.resolve({ headers: {}, ...handler() });
      }
      return Promise.resolve({ status: 404, headers: {}, body: "not found" });
    },
    stream: () => Promise.reject(new Error("stream is not expected")),
  };
}

Deno.test("queryAccountQuota prefers retrieveUserQuotaSummary buckets over model quotas", async () => {
  const network = quotaContext({
    ":loadCodeAssist": () => ({
      status: 200,
      body: JSON.stringify({ cloudaicompanionProject: "project-1", currentTier: { id: "pro" } }),
    }),
    ":retrieveUserQuotaSummary": () => ({
      status: 200,
      body: JSON.stringify({
        groups: [
          {
            displayName: "Gemini Models",
            buckets: [
              {
                bucketId: "gemini-5h",
                displayName: "Five Hour Limit Remaining",
                remainingFraction: 0.8,
                resetTime: "2026-09-18T15:34:02Z",
              },
              {
                bucketId: "gemini-weekly",
                displayName: "Weekly Limit Remaining",
                remainingFraction: 0.6,
              },
            ],
          },
          {
            displayName: "Claude Models",
            buckets: [
              { bucketId: "3p-5h", displayName: "Five Hour Limit Remaining", remainingFraction: 0.9 },
              { bucketId: "3p-weekly", remainingFraction: 0.75 },
            ],
          },
        ],
      }),
    }),
  });

  const { quota, projectId } = await queryAccountQuota("access-token", network);
  assert(projectId === "project-1", "project id should come from loadCodeAssist");
  assert(quota !== null, "quota should be present");
  assert(quota.buckets !== null && quota.buckets !== undefined, "bucket summary should be stored");
  assert(quota.buckets["gemini-5h"].remainingPercent === 80, "gemini 5h bucket should be 80%");
  assert(quota.buckets["gemini-weekly"].remainingPercent === 60, "gemini weekly bucket should be 60%");
  assert(quota.buckets["3p-5h"].remainingPercent === 90, "claude 5h bucket should be 90%");
  assert(quota.buckets["3p-weekly"].remainingPercent === 75, "claude weekly bucket should be 75%");
  assert(quota.buckets["gemini-5h"].resetAtMs === Date.parse("2026-09-18T15:34:02Z"), "reset time should be parsed");

  const view = presentAccount({
    id: "resource-1",
    type: RESOURCE_TYPE,
    key: "antigravity:user-1",
    privateData: { accessToken: "t", quota } as never,
    state: { status: "ready" },
  });
  assert(view.metrics !== undefined && view.metrics.length === 4, "bucket metrics should be rendered");
  assert(view.metrics[0].id === "gemini-5h", "known buckets should render in stable order");
  assert(view.metrics[3].id === "3p-weekly", "claude buckets should be last");
});

Deno.test("queryAccountQuota falls back to model-level quotas when the summary is missing", async () => {
  const network = quotaContext({
    ":loadCodeAssist": () => ({ status: 200, body: JSON.stringify({}) }),
    ":fetchAvailableModels": () => ({
      status: 200,
      body: JSON.stringify({
        models: {
          "claude-sonnet-4-6": { quotaInfo: { remainingFraction: 0.4 } },
          "gemini-3.7-flash": { quotaInfo: { remainingFraction: 0.25 } },
        },
      }),
    }),
  });

  const { quota } = await queryAccountQuota("access-token", network);
  assert(quota !== null, "quota should be present");
  assert(quota.buckets === null, "no bucket summary should be stored");
  assert(quota.claude?.remainingPercent === 40, "claude fallback should use the model quota");
  assert(quota.gemini?.remainingPercent === 25, "gemini fallback should use the model quota");
});

Deno.test("quotaExhaustedPatch parses retry hints into the cooling window", () => {
  const data = {
    accessToken: "token",
    refreshToken: null,
    displayName: "user@example.com",
    quota: null,
  } as AccountData;
  const nowMs = 1_700_000_000_000;

  const fromRetryDelay = quotaExhaustedPatch(data, '{"retryDelay":"54s"}', nowMs);
  assert(
    (fromRetryDelay.privateData as { quota: { coolingUntilMs: number } }).quota.coolingUntilMs === nowMs + 54_000,
    "Google RetryInfo retryDelay should set the cooling window",
  );

  const fromResetAfter = quotaExhaustedPatch(data, "Your quota will reset after 4h54m36s.", nowMs);
  const resetAfterMs = (fromResetAfter.privateData as { quota: { coolingUntilMs: number } }).quota.coolingUntilMs - nowMs;
  assert(resetAfterMs === (4 * 3600 + 54 * 60 + 36) * 1000, "compound reset durations should be parsed");

  const fallback = quotaExhaustedPatch(data, "Resource has been exhausted.", nowMs);
  assert(
    (fallback.privateData as { quota: { coolingUntilMs: number } }).quota.coolingUntilMs === nowMs + 60_000,
    "unparseable errors should fall back to the 60s cooling window",
  );
});
