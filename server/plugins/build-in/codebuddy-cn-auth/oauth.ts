import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import type { OAuth2AddMethod, OAuth2Begin, OAuth2Poll } from "cursor-byok:resource";
import {
  type AccountData,
  AUTH_STATE_URL,
  AUTH_TOKEN_URL,
  type CredentialCandidate,
  credentialDraft,
  fetchAccountProfile,
  isSupportedCodeBuddyDomain,
  normalizeCodeBuddyDomain,
  oauthHeaders,
  parseTokenData,
} from "./resources.ts";

type OAuthSession = { state: string };

function object(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function text(value: unknown): string | null {
  return typeof value === "string" && value.trim() ? value.trim() : null;
}

function number(value: unknown): number | null {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string" && value.trim()) {
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : null;
  }
  return null;
}

function authorizationUrl(value: string, state: string): string {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new Error("CodeBuddy authorization URL is invalid");
  }
  if (
    url.protocol !== "https:" || url.username || url.password || url.port ||
    !["copilot.tencent.com", "www.codebuddy.cn"].includes(url.hostname) ||
    url.pathname !== "/login" ||
    url.searchParams.getAll("state").length !== 1 ||
    url.searchParams.get("state") !== state || url.hash
  ) {
    throw new Error("CodeBuddy authorization URL is invalid");
  }
  return url.toString();
}
function parseBody(body: string): Record<string, unknown> {
  try {
    return object(JSON.parse(body)) ?? {};
  } catch {
    return {};
  }
}

function parseSession(value: JsonValue): OAuthSession {
  const state = text(object(value)?.state);
  if (!state) throw new Error("CodeBuddy OAuth session is invalid");
  return { state };
}

function tokenCandidate(body: unknown): CredentialCandidate {
  const root = object(body) ?? {};
  const data = object(root.data) ?? root;
  const domain = text(data.domain);
  if (domain && !isSupportedCodeBuddyDomain(domain)) {
    throw new Error("CodeBuddy international account is not supported");
  }
  const token = parseTokenData(body);
  return {
    accessToken: token.accessToken,
    refreshToken: token.refreshToken,
    expiresAtMs: token.expiresAtMs,
    refreshExpiresAtMs: token.refreshExpiresAtMs,
    uid: text(data.uid ?? data.userId ?? data.user_id),
    email: text(data.email ?? data.mail),
    nickname: text(data.nickname ?? data.name ?? data.displayName),
    enterpriseName: null,
    enterpriseId: text(data.enterpriseId ?? data.enterprise_id ?? data.entId ?? data.ent_id),
    domain: normalizeCodeBuddyDomain(domain),
  };
}

async function begin(context: PluginContext): Promise<OAuth2Begin> {
  const response = await context.network.fetch(AUTH_STATE_URL, {
    method: "POST",
    headers: {
      ...oauthHeaders(),
      "X-Request-ID": crypto.randomUUID().replaceAll("-", ""),
    },
    body: "{}",
  });
  const body = parseBody(response.body);
  const data = object(body.data);
  const state = text(data?.state);
  const authUrl = text(data?.authUrl ?? data?.authURL ?? data?.loginUrl);
  if (response.status < 200 || response.status >= 300 || number(body.code) !== 0) {
    throw new Error("CodeBuddy auth state request failed");
  }
  if (!state || !authUrl) {
    throw new Error("CodeBuddy auth state response is missing state or authUrl");
  }
  const verificationUrl = authorizationUrl(authUrl, state);
  return {
    session: { state } as unknown as JsonValue,
    userCode: "",
    verificationUrl,
    expiresAtMs: Date.now() + 10 * 60 * 1000,
    pollIntervalMs: 5 * 1000,
  };
}

async function poll(sessionValue: JsonValue, context: PluginContext): Promise<OAuth2Poll> {
  const session = parseSession(sessionValue);
  const response = await context.network.fetch(
    `${AUTH_TOKEN_URL}?state=${encodeURIComponent(session.state)}`,
    { method: "GET", headers: oauthHeaders() },
  );
  const body = parseBody(response.body);
  const code = number(body.code);
  if (code === 11217) return { status: "pending" };
  if (response.status < 200 || response.status >= 300 || code !== 0) {
    return {
      status: "failed",
      message: "CodeBuddy auth token request failed",
    };
  }
  let candidate: CredentialCandidate;
  try {
    candidate = tokenCandidate(body);
  } catch (error) {
    return { status: "failed", message: error instanceof Error ? error.message : String(error) };
  }
  let account: AccountData = {
    accessToken: candidate.accessToken,
    refreshToken: candidate.refreshToken,
    expiresAtMs: candidate.expiresAtMs,
    refreshExpiresAtMs: candidate.refreshExpiresAtMs,
    uid: candidate.uid,
    email: candidate.email,
    nickname: candidate.nickname,
    enterpriseName: candidate.enterpriseName,
    enterpriseId: candidate.enterpriseId,
    domain: candidate.domain,
    quota: null,
    lastCheckin: null,
  };
  try {
    const profile = await fetchAccountProfile(session.state, account, context);
    account = { ...account, ...profile };
  } catch (error) {
    return { status: "failed", message: error instanceof Error ? error.message : String(error) };
  }
  return { status: "completed", resources: [await credentialDraft(account)] };
}

export const codeBuddyOAuth: OAuth2AddMethod = {
  type: "oauth2.0",
  id: "codebuddy-cn-device",
  displayName: {
    "en-US": "Sign in with CodeBuddy CN",
    "zh-CN": "使用 CodeBuddy CN 登录",
  },
  description: {
    "en-US": "Authorize CodeBuddy CN in a browser, then add the account.",
    "zh-CN": "在浏览器中完成 CodeBuddy CN 授权后自动添加账号。",
  },
  begin,
  poll,
};
