import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import type { OAuth2AddMethod, OAuth2Begin, OAuth2Poll } from "cursor-byok:resource";
import { type AuthToken, credentialDraft, parseAuthToken } from "./resources.ts";

const AUTH_STATE_URL = "https://copilot.tencent.com/v2/plugin/auth/state?platform=CLI";
const AUTH_TOKEN_URL = "https://copilot.tencent.com/v2/plugin/auth/token";
const LOGIN_ACCOUNT_URL = "https://copilot.tencent.com/v2/plugin/login/account";
const LOGIN_TIMEOUT_MS = 5 * 60 * 1000;
const TOKEN_PENDING_CODE = 11217;
const ACCOUNT_PENDING_CODE = 12151;

type Session = {
  state: string;
  token: AuthToken | null;
};

const NO_AUTH_HEADERS: Record<string, string> = {
  accept: "application/json",
  "X-No-Authorization": "true",
  "X-No-User-Id": "true",
  "X-No-Enterprise-Id": "true",
  "X-No-Department-Info": "true",
};

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

function parseBody(body: string): Record<string, unknown> {
  try {
    return object(JSON.parse(body)) ?? {};
  } catch {
    return {};
  }
}

function bodyMessage(body: Record<string, unknown>): string {
  return text(body.msg ?? body.message) ?? "unknown CodeBuddy response";
}

function businessCode(body: Record<string, unknown>): number | null {
  return number(body.code);
}

function pending(body: Record<string, unknown>, code: number): boolean {
  const message = bodyMessage(body).toLowerCase();
  return businessCode(body) === code || message.includes("login ing") ||
    message.includes("authorization pending");
}

function parseSession(value: JsonValue): Session {
  const session = object(value);
  const state = text(session?.state);
  if (!state) throw new Error("CodeBuddy OAuth session is invalid");
  const token = object(session?.token);
  return { state, token: token ? parseAuthToken(token) : null };
}

async function begin(context: PluginContext): Promise<OAuth2Begin> {
  const response = await context.network.fetch(AUTH_STATE_URL, {
    method: "POST",
    headers: { ...NO_AUTH_HEADERS, "content-type": "application/json" },
    body: "{}",
  });
  const body = parseBody(response.body);
  const data = object(body.data);
  const state = text(data?.state);
  const authUrl = text(data?.authUrl ?? data?.auth_url);
  if (response.status < 200 || response.status >= 300 || !state || !authUrl) {
    throw new Error(
      `Failed to start CodeBuddy login (HTTP ${response.status}): ${bodyMessage(body)}`,
    );
  }
  return {
    session: { state, token: null } as unknown as JsonValue,
    // CodeBuddy embeds the state in authUrl and does not require a user-entered code.
    userCode: "",
    verificationUrl: authUrl,
    verificationUrlComplete: authUrl,
    expiresAtMs: Date.now() + LOGIN_TIMEOUT_MS,
    pollIntervalMs: 1000,
  };
}

async function pollToken(
  session: Session,
  context: PluginContext,
): Promise<AuthToken | OAuth2Poll> {
  if (session.token) return session.token;
  const response = await context.network.fetch(
    `${AUTH_TOKEN_URL}?state=${encodeURIComponent(session.state)}`,
    { method: "GET", headers: NO_AUTH_HEADERS },
  );
  const body = parseBody(response.body);
  if (pending(body, TOKEN_PENDING_CODE)) return { status: "pending" };
  const data = object(body.data);
  if (response.status < 200 || response.status >= 300 || !data) {
    return {
      status: "failed",
      message: `CodeBuddy login failed (HTTP ${response.status}): ${bodyMessage(body)}`,
    };
  }
  try {
    return parseAuthToken(data);
  } catch (error) {
    return {
      status: "failed",
      message: error instanceof Error ? error.message : String(error),
    };
  }
}

async function pollAccount(
  session: Session,
  token: AuthToken,
  context: PluginContext,
): Promise<OAuth2Poll> {
  const response = await context.network.fetch(
    `${LOGIN_ACCOUNT_URL}?state=${encodeURIComponent(session.state)}`,
    {
      method: "GET",
      headers: { ...NO_AUTH_HEADERS, authorization: `Bearer ${token.accessToken}` },
    },
  );
  const body = parseBody(response.body);
  const nextSession = { ...session, token } as unknown as JsonValue;
  if (pending(body, ACCOUNT_PENDING_CODE)) return { status: "pending", session: nextSession };
  const account = object(body.data);
  if (response.status < 200 || response.status >= 300 || !account) {
    return {
      status: "failed",
      message: `CodeBuddy account lookup failed (HTTP ${response.status}): ${bodyMessage(body)}`,
    };
  }
  try {
    return { status: "completed", resources: [await credentialDraft(token, account)] };
  } catch (error) {
    return {
      status: "failed",
      message: error instanceof Error ? error.message : String(error),
    };
  }
}

async function poll(sessionValue: JsonValue, context: PluginContext): Promise<OAuth2Poll> {
  const session = parseSession(sessionValue);
  const token = await pollToken(session, context);
  if ("status" in token) return token;
  return await pollAccount(session, token, context);
}

export const codeBuddyOAuth: OAuth2AddMethod = {
  type: "oauth2.0",
  id: "codebuddy-cli-login",
  displayName: {
    "en-US": "Sign in with CodeBuddy China",
    "zh-CN": "使用 CodeBuddy 国内版登录",
  },
  description: {
    "en-US": "Sign in on the official CodeBuddy page and use the account's existing credits.",
    "zh-CN": "在 CodeBuddy 官方网页完成登录，并使用该账号已有积分。",
  },
  begin,
  poll,
};
