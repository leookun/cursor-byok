import { HttpError, ProviderError, providerFailure, providerEventError, retryAfterMs } from "./provider.ts";

function equal(actual: unknown, expected: unknown) {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`expected ${JSON.stringify(expected)}, received ${JSON.stringify(actual)}`);
  }
}

Deno.test("HTTP failures preserve status, headers and retry delays", () => {
  const headers = { "Retry-After": "1.5", "x-debug": "original" };
  const error = new HttpError(429, "quota", headers);
  equal(error.headers, headers);
  equal(error.message, "HTTP 429: quota");
  equal(providerFailure(error), { kind: "rate_limit", status: 429, retryAfterMs: 1500 });
  equal(providerFailure(new HttpError(503, "down")), { kind: "transient", status: 503 });
  equal(providerFailure(new HttpError(401, "expired")), { kind: "authorization", status: 401 });
  equal(providerFailure(new HttpError(400, "bad input")), { kind: "request", status: 400 });
});

Deno.test("retry metadata handles dates, reset timestamps and durations", () => {
  const now = Date.parse("2026-01-01T00:00:00Z");
  equal(retryAfterMs({ "retry-after": "Thu, 01 Jan 2026 00:00:12 GMT" }, now), 12_000);
  equal(retryAfterMs({ "x-ratelimit-reset": String(now / 1000 + 30) }, now), 30_000);
  equal(retryAfterMs({ "ratelimit-reset": "42" }, now), 42_000);
  equal(retryAfterMs({ "x-ratelimit-reset-requests": "1m2.5s" }, now), 62_500);
  equal(retryAfterMs({ "x-ratelimit-reset-tokens": "100ms" }, now), 100);
  equal(retryAfterMs({ "retry-after": "invalid" }, now), undefined);
  equal(retryAfterMs({}, now), undefined);
});

Deno.test("structured event errors retain numeric status and explicit retry delays", () => {
  const error = providerEventError({ error: { status: "RESOURCE_EXHAUSTED", code: 429, retry_after_seconds: 3 } }, "opaque");
  equal(providerFailure(error), { kind: "rate_limit", status: 429, retryAfterMs: 3000 });
  equal(providerFailure(providerEventError({ type: "error", code: "server_error", message: "opaque" }, "opaque")), { kind: "transient" });
  equal(providerFailure(providerEventError({ error: { code: "unknown", message: "rate_limit_exceeded" } }, "opaque")), undefined);
  equal(providerFailure(new HttpError(403, JSON.stringify({ error: { code: "spending-limit" } }))), { kind: "rate_limit", status: 403 });
  equal(providerFailure(new HttpError(403, "quota exhausted 429")), { kind: "authorization", status: 403 });
});

Deno.test("only typed host errors carry transient failure classification", () => {
  equal(providerFailure(new ProviderError("network failed", { kind: "transient" })), {
    kind: "transient",
  });
  equal(providerFailure(new Error("network timeout 429")), undefined);
  equal(providerFailure(new TypeError("plugin bug")), undefined);
});
