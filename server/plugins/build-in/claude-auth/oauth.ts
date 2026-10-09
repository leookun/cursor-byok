import type { OAuth2AuthorizationCodeAddMethod } from "cursor-byok:resource";
import {
  CLIENT_ID,
  createAccount,
  object,
  privateData,
  requestTokens,
  SCOPES,
  text,
} from "./auth.ts";

const AUTHORIZATION_URL = "https://claude.ai/oauth/authorize";
const SESSION_LIFETIME_MS = 10 * 60 * 1000;

function redirectUri(hostUri: string): string {
  const url = new URL(hostUri);
  if (
    url.protocol !== "http:" || url.hostname !== "127.0.0.1" || !url.port ||
    url.pathname !== "/callback" || url.search || url.hash || url.username || url.password
  ) {
    throw new Error(
      "Claude sign-in requires a local callback. Restart sign-in from the desktop app.",
    );
  }
  // Claude's registered loopback redirect uses localhost; the host listens on IPv4 loopback.
  url.hostname = "localhost";
  return url.toString();
}

export const claudeOAuth: OAuth2AuthorizationCodeAddMethod = {
  type: "oauth2.authorization-code",
  id: "claude-subscription",
  displayName: {
    "en-US": "Sign in with Claude",
    "ru-RU": "Войти через Claude",
    "zh-CN": "使用 Claude 登录",
  },
  description: {
    "en-US": "Sign in and connect your full Claude subscription.",
    "ru-RU": "Войдите и подключите полноценную подписку Claude.",
    "zh-CN": "登录并连接完整的 Claude 订阅。",
  },
  callback: { path: "/callback" },
  begin(input, context) {
    context.signal.throwIfAborted();
    const redirect = redirectUri(input.redirectUri);
    if (!text(input.state) || !text(input.codeChallenge)) {
      throw new Error("Claude sign-in protection is missing. Restart sign-in.");
    }
    const expiresAtMs = Date.now() + SESSION_LIFETIME_MS;
    const params = new URLSearchParams({
      code: "true",
      client_id: CLIENT_ID,
      response_type: "code",
      redirect_uri: redirect,
      scope: SCOPES.join(" "),
      state: input.state,
      code_challenge: input.codeChallenge,
      code_challenge_method: "S256",
    });
    return Promise.resolve({
      session: { state: input.state, redirectUri: redirect, expiresAtMs },
      authorizationUrl: `${AUTHORIZATION_URL}?${params}`,
      expiresAtMs,
    });
  },
  async complete(sessionValue, input, context) {
    context.signal.throwIfAborted();
    const session = object(sessionValue);
    const state = text(session?.state);
    const redirect = text(session?.redirectUri);
    const expiresAtMs = session?.expiresAtMs;
    if (
      !state || !redirect || typeof expiresAtMs !== "number" ||
      !Number.isFinite(expiresAtMs) || expiresAtMs <= Date.now() ||
      redirect !== redirectUri(input.redirectUri) || !text(input.code) || !text(input.codeVerifier)
    ) {
      throw new Error("Claude sign-in expired or changed. Restart sign-in.");
    }
    // The host validates callback state before invoking complete; Claude also requires it here.
    const body = await requestTokens({
      grant_type: "authorization_code",
      code: input.code,
      state,
      redirect_uri: redirect,
      code_verifier: input.codeVerifier,
    }, context);
    const data = await createAccount(body, context);
    return [{
      key: `claude:${data.organizationId}:${data.accountId}`,
      privateData: privateData(data),
      state: { status: "ready" },
    }];
  },
};
