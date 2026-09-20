import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import type {
  OAuth2AuthorizationCodeAddMethod,
  OAuth2AuthorizationCodeBegin,
  ResourceDraft,
} from "cursor-byok:resource";
import { credentialDraft, queryAccountQuota } from "./resources.ts";
import { ANTIGRAVITY_OAUTH_USER_AGENT } from "./models.ts";
import {
  CLIENT_ID,
  CLIENT_SECRET,
  GOOGLE_AUTHORIZATION_URL,
  GOOGLE_TOKEN_URL,
  SCOPES,
} from "./google_oauth.ts";

const AUTHORIZATION_LIFETIME_MS = 5 * 60 * 1000;

type Session = { createdAtMs: number };

function object(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function text(value: unknown): string | null {
  return typeof value === "string" && value.trim() ? value.trim() : null;
}

function parseBody(body: string): Record<string, unknown> {
  try {
    return object(JSON.parse(body)) ?? {};
  } catch {
    return {};
  }
}

function parseSession(value: JsonValue): Session {
  const session = object(value);
  const createdAtMs = session?.createdAtMs;
  if (typeof createdAtMs !== "number") throw new Error("Antigravity OAuth session is invalid");
  if (Date.now() - createdAtMs > AUTHORIZATION_LIFETIME_MS) {
    throw new Error("Google authorization expired. Please try again.");
  }
  return { createdAtMs };
}

function begin(
  input: { redirectUri: string; state: string; codeChallenge: string },
  _context: PluginContext,
): Promise<OAuth2AuthorizationCodeBegin> {
  const authParams = new URLSearchParams({
    client_id: CLIENT_ID,
    response_type: "code",
    redirect_uri: input.redirectUri,
    scope: SCOPES.join(" "),
    state: input.state,
    code_challenge: input.codeChallenge,
    code_challenge_method: "S256",
    access_type: "offline",
    prompt: "consent",
    include_granted_scopes: "true",
  });
  return Promise.resolve({
    session: { createdAtMs: Date.now() },
    authorizationUrl: `${GOOGLE_AUTHORIZATION_URL}?${authParams.toString()}`,
    expiresAtMs: Date.now() + AUTHORIZATION_LIFETIME_MS,
  });
}

async function complete(
  sessionValue: JsonValue,
  input: { code: string; redirectUri: string; codeVerifier: string },
  context: PluginContext,
): Promise<ResourceDraft[]> {
  parseSession(sessionValue);
  const tokenResponse = await context.network.fetch(GOOGLE_TOKEN_URL, {
    method: "POST",
    headers: {
      accept: "application/json",
      "content-type": "application/x-www-form-urlencoded",
      "user-agent": ANTIGRAVITY_OAUTH_USER_AGENT,
    },
    body: new URLSearchParams({
      client_id: CLIENT_ID,
      client_secret: CLIENT_SECRET,
      code: input.code,
      code_verifier: input.codeVerifier,
      grant_type: "authorization_code",
      redirect_uri: input.redirectUri,
    }).toString(),
  });
  const tokenBody = parseBody(tokenResponse.body);
  if (tokenResponse.status < 200 || tokenResponse.status >= 300) {
    const detail = text(tokenBody.error_description) ?? text(tokenBody.error) ??
      `HTTP ${tokenResponse.status}`;
    throw new Error(`Google token exchange failed: ${detail}`);
  }

  const accessToken = text(tokenBody.access_token);
  if (!accessToken) throw new Error("Google token response did not include an access token");

  let displayName = text(tokenBody.email);
  try {
    const userInfoResponse = await context.network.fetch(
      "https://www.googleapis.com/oauth2/v2/userinfo",
      {
        method: "GET",
        headers: { authorization: `Bearer ${accessToken}`, accept: "application/json" },
      },
    );
    if (userInfoResponse.status >= 200 && userInfoResponse.status < 300) {
      displayName = text(parseBody(userInfoResponse.body).email) ?? displayName;
    }
  } catch {
    // Account identity has a token fingerprint fallback.
  }

  const result = await queryAccountQuota(accessToken, context.network);
  const expiresIn = typeof tokenBody.expires_in === "number" ? tokenBody.expires_in : null;
  return [
    await credentialDraft({
      accessToken,
      refreshToken: text(tokenBody.refresh_token),
      displayName: displayName ?? "Google Antigravity",
      projectId: result.projectId,
      expiresAtMs: expiresIn === null ? null : Date.now() + expiresIn * 1000,
      quota: result.quota,
    }),
  ];
}

export const antigravityAuthorizationCodeOAuth: OAuth2AuthorizationCodeAddMethod = {
  type: "oauth2.authorization-code",
  id: "google-antigravity",
  displayName: {
    "en-US": "Sign in with Google (Antigravity)",
    "zh-CN": "使用 Google (Antigravity) 登录",
  },
  description: {
    "en-US": "Authorize Antigravity with your Google Account for Gemini and Claude models.",
    "zh-CN": "使用 Google 账号完成 Antigravity 授权，以使用 Gemini 与 Claude 模型。",
  },
  begin,
  complete,
};
