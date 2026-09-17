import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import type { ResourceSnapshot } from "cursor-byok:resource";
import {
  parseCredentialFiles,
  parseQuotaPools,
  presentAccount,
  queryAccountQuota,
  RESOURCE_TYPE,
} from "./resources.ts";

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}
function assertEquals(actual: unknown, expected: unknown): void {
  const left = JSON.stringify(actual);
  const right = JSON.stringify(expected);
  if (left !== right) throw new Error(`expected ${right}, received ${left}`);
}
function snapshot(privateData: JsonValue): ResourceSnapshot {
  return {
    id: "resource-1",
    type: RESOURCE_TYPE,
    key: "antigravity:user@example.com",
    privateData,
    state: { status: "ready" },
  };
}

Deno.test("conflicting pool buckets are not combined into a fabricated account limit", () => {
  const pools = parseQuotaPools({
    groups: [{
      displayName: "Gemini",
      buckets: [
        { window: "5h", remainingFraction: 0.2 },
        { window: "5h", remainingFraction: 0.9 },
      ],
    }],
  });
  assertEquals(pools.flatMap((pool) => pool.buckets), []);
});

Deno.test("quota parser keeps genuine 5h and weekly buckets for the two known pools", () => {
  assertEquals(
    parseQuotaPools({
      groups: [
        {
          id: "gemini",
          displayName: "Gemini Models",
          buckets: [
            {
              bucketId: "gemini-5h",
              window: "5h",
              remainingFraction: 0.8,
              resetTime: "2026-09-10T13:00:00Z",
            },
            {
              bucketId: "gemini-weekly",
              window: "weekly",
              remainingFraction: 0.4,
              resetTime: "2026-09-14T00:00:00Z",
            },
          ],
        },
        {
          description: "Claude and GPT shared quota",
          buckets: [
            { bucketId: "third-party-5h", window: "5 hours", remainingFraction: 0.7 },
            { bucketId: "third-party-weekly", window: "weekly", remainingFraction: 0.3 },
          ],
        },
        {
          displayName: "Unknown future family",
          buckets: [{ bucketId: "unknown-weekly", window: "weekly", remainingFraction: 0.1 }],
        },
      ],
    }),
    [
      {
        id: "gemini",
        buckets: [
          { window: "5h", remainingPercent: 80, resetAtMs: Date.parse("2026-09-10T13:00:00Z") },
          { window: "weekly", remainingPercent: 40, resetAtMs: Date.parse("2026-09-14T00:00:00Z") },
        ],
      },
      {
        id: "claude-gpt",
        buckets: [
          { window: "5h", remainingPercent: 70, resetAtMs: null },
          { window: "weekly", remainingPercent: 30, resetAtMs: null },
        ],
      },
    ],
  );
});

Deno.test("missing summary buckets preserve previous values and mark the account stale", async () => {
  const previous = {
    planLabel: "Pro",
    pools: [
      {
        id: "gemini",
        buckets: [{ window: "5h", remainingPercent: 40, resetAtMs: null }, {
          window: "weekly",
          remainingPercent: 30,
          resetAtMs: null,
        }],
      },
    ],
  };
  const result = await queryAccountQuota("mock", {
    fetch: (url) =>
      Promise.resolve({
        status: url.endsWith(":retrieveUserQuotaSummary") ? 503 : 200,
        headers: {},
        body: JSON.stringify({}),
      }),
    stream: () => Promise.reject(new Error("unexpected stream")),
  }, previous as never);
  assertEquals(result.quota.pools, previous.pools);
  assertEquals(result.quota.stale, true);
  assert(
    JSON.stringify(
      presentAccount(snapshot({ accessToken: "mock", quota: result.quota })).description,
    ).includes("刷新不完整"),
    "stale quota must be disclosed",
  );
});

Deno.test("presented metrics use only the four pool contract IDs", () => {
  const view = presentAccount(snapshot({
    accessToken: "mock",
    quota: {
      planLabel: "Ultra",
      pools: [
        {
          id: "gemini",
          buckets: [{ window: "5h", remainingPercent: 80, resetAtMs: null }, {
            window: "weekly",
            remainingPercent: 70,
            resetAtMs: null,
          }],
        },
        {
          id: "claude-gpt",
          buckets: [{ window: "5h", remainingPercent: 60, resetAtMs: null }, {
            window: "weekly",
            remainingPercent: 50,
            resetAtMs: null,
          }],
        },
      ],
    },
  }));
  assertEquals(view.metrics?.map((metric) => metric.id), [
    "pool:gemini:5h",
    "pool:gemini:weekly",
    "pool:claude-gpt:5h",
    "pool:claude-gpt:weekly",
  ]);
});

Deno.test("imports an array of email and refresh_token credentials", async () => {
  const requests: string[] = [];
  const result = await parseCredentialFiles(
    [{
      name: "antigravity.json",
      content: JSON.stringify([{ email: "user@example.com", refresh_token: "refresh-token" }]),
    }],
    {
      fetch: (_, init = {}) => {
        requests.push(init.body ?? "");
        return Promise.resolve({
          status: 200,
          headers: {},
          body: JSON.stringify({
            access_token: "access-token",
            refresh_token: "refresh-token",
            expires_in: 3600,
          }),
        });
      },
      stream: () => Promise.reject(new Error("stream is not expected")),
    } as PluginContext["network"],
  );
  assert(result.warnings.length === 0, "the credential file should not produce warnings");
  assertEquals(result.credentials[0].accessToken, "access-token");
  assertEquals(requests.length, 1);
});
