import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import type { OAuth2AddMethod, OAuth2Begin, OAuth2Poll } from "cursor-byok:resource";
import { ACCESS_TOKEN_URL, CLIENT_ID, DEVICE_CODE_URL, oauthHeaders, SCOPE } from "./constants.ts";
import { accountDraft } from "./resources.ts";
import { CopilotAuthError, NO_COPILOT_MESSAGE } from "./token.ts";

type Session = {
  deviceCode: string;
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
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function parseBody(body: string): Record<string, unknown> | null {
  try {
    return object(JSON.parse(body));
  } catch {
    return null;
  }
}

function parseSession(value: JsonValue): Session {
  const deviceCode = text(object(value)?.deviceCode);
  if (!deviceCode) throw new Error("GitHub OAuth session is invalid");
  return { deviceCode };
}

async function begin(context: PluginContext): Promise<OAuth2Begin> {
  const response = await context.network.fetch(DEVICE_CODE_URL, {
    method: "POST",
    headers: oauthHeaders(),
    body: JSON.stringify({ client_id: CLIENT_ID, scope: SCOPE }),
  });
  if (response.status < 200 || response.status >= 300) {
    throw new Error(
      `Failed to request a GitHub device code (HTTP ${response.status}): ${response.body}`,
    );
  }
  const body = parseBody(response.body);
  const deviceCode = text(body?.device_code);
  const userCode = text(body?.user_code);
  const verificationUrl = text(body?.verification_uri);
  if (!deviceCode || !userCode || !verificationUrl) {
    throw new Error("GitHub device authorization response is incomplete");
  }
  const session: Session = { deviceCode };
  return {
    session: session as unknown as JsonValue,
    userCode,
    verificationUrl,
    expiresAtMs: Date.now() + Math.max(1, number(body?.expires_in) ?? 900) * 1000,
    pollIntervalMs: Math.max(1, number(body?.interval) ?? 5) * 1000,
  };
}

async function poll(sessionValue: JsonValue, context: PluginContext): Promise<OAuth2Poll> {
  const session = parseSession(sessionValue);
  const response = await context.network.fetch(ACCESS_TOKEN_URL, {
    method: "POST",
    headers: oauthHeaders(),
    body: JSON.stringify({
      client_id: CLIENT_ID,
      device_code: session.deviceCode,
      grant_type: "urn:ietf:params:oauth:grant-type:device_code",
    }),
  });
  // GitHub 在待授权时也返回 HTTP 200,状态写在 body.error 里,所以先看 body。
  const body = parseBody(response.body);
  const githubToken = text(body?.access_token);
  if (githubToken) {
    try {
      return { status: "completed", resources: [await accountDraft(githubToken, context)] };
    } catch (error) {
      if (error instanceof CopilotAuthError) {
        return { status: "failed", message: NO_COPILOT_MESSAGE };
      }
      throw error;
    }
  }
  const code = text(body?.error);
  const description = text(body?.error_description);
  switch (code) {
    case "authorization_pending":
      return { status: "pending" };
    case "slow_down":
      return { status: "slow-down" };
    case "expired_token":
      return { status: "failed", message: "Device code expired; sign in again" };
    case "access_denied":
      return { status: "denied", ...(description ? { message: description } : {}) };
    case null:
      // 非 2xx 且没有可识别的 body 视为瞬时故障,交给宿主继续轮询。
      if (response.status < 200 || response.status >= 300) return { status: "pending" };
      return { status: "failed", message: "GitHub token response is missing access_token" };
    default:
      return { status: "failed", message: description ?? code };
  }
}

export const githubDeviceOAuth: OAuth2AddMethod = {
  type: "oauth2.0",
  id: "github-device",
  displayName: {
    "en-US": "Sign in with GitHub",
    "zh-CN": "使用 GitHub 登录",
  },
  description: {
    "en-US": "Authorize this device on GitHub, then add the Copilot subscription of that account.",
    "zh-CN": "在 GitHub 完成设备授权后,自动添加该账号的 Copilot 订阅。",
  },
  begin,
  poll,
};
