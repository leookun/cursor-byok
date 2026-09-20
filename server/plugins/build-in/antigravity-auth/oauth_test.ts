import type { PluginContext } from "cursor-byok:plugin";
import { antigravityAuthorizationCodeOAuth } from "./oauth.ts";

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

type OAuthRequest = { url: string; body?: string; headers?: Record<string, string> };

function context(requests: OAuthRequest[]): PluginContext {
  return {
    network: {
      fetch: (url, init = {}) => {
        requests.push({ url, body: init.body, headers: init.headers });
        if (url === "https://oauth2.googleapis.com/token") {
          return Promise.resolve({
            status: 200,
            headers: {},
            body: JSON.stringify({
              access_token: "access-token",
              refresh_token: "refresh-token",
              expires_in: 3600,
            }),
          });
        }
        if (url === "https://www.googleapis.com/oauth2/v2/userinfo") {
          return Promise.resolve({
            status: 200,
            headers: {},
            body: JSON.stringify({ email: "user@example.com" }),
          });
        }
        if (url.endsWith(":loadCodeAssist")) {
          return Promise.resolve({
            status: 200,
            headers: {},
            body: JSON.stringify({ cloudaicompanionProject: "real-project" }),
          });
        }
        if (url.endsWith(":fetchAvailableModels")) {
          return Promise.resolve({
            status: 200,
            headers: {},
            body: JSON.stringify({ models: {} }),
          });
        }
        return Promise.resolve({ status: 404, headers: {}, body: "{}" });
      },
      stream: () => Promise.reject(new Error("stream is not expected")),
    },
    signal: new AbortController().signal,
  };
}

Deno.test("authorization URL uses Core-owned state, callback, and PKCE challenge", async () => {
  assert(
    antigravityAuthorizationCodeOAuth.callback?.port === undefined,
    "Antigravity must let Core allocate an available loopback port",
  );
  const result = await antigravityAuthorizationCodeOAuth.begin(
    {
      redirectUri: "http://127.0.0.1:51121/oauth-callback",
      state: "core-state",
      codeChallenge: "core-challenge",
    },
    context([]),
  );
  const url = new URL(result.authorizationUrl);
  assert(url.searchParams.get("state") === "core-state", "state must come from Core");
  assert(
    url.searchParams.get("redirect_uri")?.endsWith("/oauth-callback"),
    "callback must be forwarded",
  );
  assert(
    url.searchParams.get("code_challenge") === "core-challenge",
    "PKCE challenge must be forwarded",
  );
  assert(url.searchParams.get("code_challenge_method") === "S256", "PKCE must use S256");
  assert(url.searchParams.get("scope")?.split(" ").includes("openid"), "OAuth must request openid");
  assert(
    url.searchParams.get("include_granted_scopes") === "true",
    "OAuth must preserve previously granted Google scopes",
  );
});

Deno.test("authorization completion exchanges the code with the Core PKCE verifier", async () => {
  const requests: OAuthRequest[] = [];
  const resources = await antigravityAuthorizationCodeOAuth.complete(
    { createdAtMs: Date.now() },
    {
      code: "authorization-code",
      redirectUri: "http://127.0.0.1:51121/oauth-callback",
      codeVerifier: "core-verifier",
    },
    context(requests),
  );
  const tokenRequest = requests.find((request) =>
    request.url === "https://oauth2.googleapis.com/token"
  );
  const body = new URLSearchParams(tokenRequest?.body);
  assert(body.get("code") === "authorization-code", "authorization code must be exchanged");
  assert(body.get("code_verifier") === "core-verifier", "PKCE verifier must come from Core");
  assert(
    tokenRequest?.headers?.["user-agent"] === "vscode/1.X.X (Antigravity/4.3.0)",
    "token exchange must use the native Antigravity OAuth user agent",
  );
  assert(resources.length === 1, "one Google account resource must be returned");
  assert(
    resources[0].key === "antigravity:user@example.com",
    "the account email must be the resource key",
  );
});
