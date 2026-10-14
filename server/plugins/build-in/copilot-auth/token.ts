import type { PluginContext } from "cursor-byok:plugin";
import {
  COPILOT_API_HOSTS,
  COPILOT_TOKEN_URL,
  DEFAULT_COPILOT_BASE,
  githubHeaders,
} from "./constants.ts";

/** Copilot token 提前 5 分钟续期。 */
const RENEW_MARGIN_MS = 5 * 60 * 1000;

export const AUTH_EXPIRED_MESSAGE = "GitHub authorization expired; sign in again";
export const NO_COPILOT_MESSAGE = "This GitHub account has no active Copilot subscription";

/** api.github.com 拒绝了 GitHub token:资源已不可用,需要重新登录。 */
export class CopilotAuthError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

export type CopilotToken = {
  token: string;
  expiresAtMs: number;
  apiBase: string;
};

function object(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function text(value: unknown): string | null {
  return typeof value === "string" && value.trim() ? value.trim() : null;
}

/** api.github.com 授权失败归类:401 = token 失效,403/404 = 没有 Copilot 席位。 */
export function authError(status: number, body: string): CopilotAuthError | null {
  if (status === 401) return new CopilotAuthError(status, AUTH_EXPIRED_MESSAGE);
  if (status === 403 || status === 404) {
    let detail: string | null = null;
    try {
      detail = text(object(JSON.parse(body))?.message);
    } catch {
      detail = text(body);
    }
    return new CopilotAuthError(
      status,
      detail ? `${NO_COPILOT_MESSAGE}: ${detail}` : NO_COPILOT_MESSAGE,
    );
  }
  return null;
}

/** 宿主只放行白名单主机,未知的 Copilot 端点在此给出明确错误。 */
export function copilotApiBase(raw: string | null): string {
  if (raw === null) return DEFAULT_COPILOT_BASE;
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new Error(`Copilot returned an invalid API endpoint: ${raw}`);
  }
  if (url.protocol !== "https:" || !COPILOT_API_HOSTS.includes(url.hostname)) {
    throw new Error(
      `Copilot API host ${url.host} is not allowed by this plugin; update plugin.json network permissions`,
    );
  }
  return `https://${url.hostname}`;
}

export function isFresh(
  token: string | null,
  expiresAtMs: number | null,
  nowMs = Date.now(),
): boolean {
  return token !== null && expiresAtMs !== null && expiresAtMs - RENEW_MARGIN_MS > nowMs;
}

/** 用长期 GitHub OAuth token 换取约 30 分钟有效的 Copilot API token。 */
export async function exchangeCopilotToken(
  githubToken: string,
  context: PluginContext,
): Promise<CopilotToken> {
  const response = await context.network.fetch(COPILOT_TOKEN_URL, {
    method: "GET",
    headers: githubHeaders(githubToken),
  });
  if (response.status < 200 || response.status >= 300) {
    throw authError(response.status, response.body) ??
      new Error(`Copilot token exchange failed (HTTP ${response.status}): ${response.body}`);
  }
  let body: Record<string, unknown> | null;
  try {
    body = object(JSON.parse(response.body));
  } catch {
    body = null;
  }
  const token = text(body?.token);
  const expiresAt = body?.expires_at;
  if (!token || typeof expiresAt !== "number" || !Number.isFinite(expiresAt)) {
    throw new Error("Copilot token exchange returned an incomplete response");
  }
  return {
    token,
    expiresAtMs: expiresAt * 1000,
    apiBase: copilotApiBase(text(object(body?.endpoints)?.api)),
  };
}
