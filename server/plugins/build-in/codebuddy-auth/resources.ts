import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import type {
  ResourceDraft,
  ResourcePatch,
  ResourceSnapshot,
  ResourceView,
} from "cursor-byok:resource";

export const RESOURCE_TYPE = "codebuddy-account";

const TOKEN_REFRESH_URL = "https://copilot.tencent.com/v2/plugin/auth/token/refresh";
const ACCOUNTS_URL = "https://copilot.tencent.com/v2/plugin/accounts";
const REFRESH_MARGIN_MS = 5 * 60 * 1000;
const QUOTA_COOLING_MS = 30 * 60 * 1000;

export type AuthToken = {
  accessToken: string;
  refreshToken: string | null;
  expiresAtMs: number | null;
  refreshExpiresAtMs: number | null;
};

export type AccountProfile = {
  userId: string;
  displayName: string;
  accountType: string | null;
  enterpriseId: string | null;
  enterpriseName: string | null;
};

export type AccountData = AuthToken & AccountProfile;

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

function responseData(body: Record<string, unknown>): Record<string, unknown> | null {
  return object(body.data);
}

function responseMessage(body: Record<string, unknown>): string {
  return text(body.msg ?? body.message) ?? "unknown CodeBuddy response";
}

function absoluteTime(value: unknown): number | null {
  const parsed = number(value);
  if (parsed === null || parsed <= 0) return null;
  return parsed < 10_000_000_000 ? parsed * 1000 : parsed;
}

function expiryAt(
  value: unknown,
  durationSeconds: unknown,
  nowMs = Date.now(),
): number | null {
  return absoluteTime(value) ??
    (number(durationSeconds) !== null
      ? nowMs + Math.max(0, number(durationSeconds)!) * 1000
      : null);
}

export function parseAuthToken(value: unknown, previous?: AccountData | null): AuthToken {
  const token = object(value) ?? {};
  const accessToken = text(token.accessToken ?? token.access_token) ?? previous?.accessToken;
  if (!accessToken) throw new Error("CodeBuddy token response is missing accessToken");
  return {
    accessToken,
    refreshToken: text(token.refreshToken ?? token.refresh_token) ?? previous?.refreshToken ?? null,
    expiresAtMs: expiryAt(
      token.expiresAt ?? token.expires_at,
      token.expiresIn ?? token.expires_in,
    ) ?? previous?.expiresAtMs ?? null,
    refreshExpiresAtMs: expiryAt(
      token.refreshExpiresAt ?? token.refresh_expires_at,
      token.refreshExpiresIn ?? token.refresh_expires_in,
    ) ?? previous?.refreshExpiresAtMs ?? null,
  };
}

export function parseAccountProfile(value: unknown): AccountProfile {
  const account = object(value) ?? {};
  const userId = text(account.uid ?? account.userId ?? account.user_id ?? account.sub);
  if (!userId) throw new Error("CodeBuddy account response is missing uid");
  return {
    userId,
    displayName: text(
      account.nickname ?? account.userName ?? account.user_name ?? account.name ?? account.email,
    ) ?? userId,
    accountType: text(account.type ?? account.accountType ?? account.account_type),
    enterpriseId: text(account.enterpriseId ?? account.enterprise_id),
    enterpriseName: text(
      account.enterpriseName ?? account.enterprise_name ?? account.departmentFullName ??
        account.department_full_name,
    ),
  };
}

async function tokenFingerprint(token: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(token));
  return Array.from(
    new Uint8Array(digest).slice(0, 8),
    (byte) => byte.toString(16).padStart(2, "0"),
  ).join("");
}

export async function credentialDraft(
  tokenValue: unknown,
  accountValue: unknown,
): Promise<ResourceDraft> {
  const token = parseAuthToken(tokenValue);
  const account = parseAccountProfile(accountValue);
  const identity = account.userId || await tokenFingerprint(token.accessToken);
  const data: AccountData = { ...token, ...account };
  return { key: `codebuddy:${identity}`, privateData: data as unknown as JsonValue };
}

export function accountData(resource: ResourceSnapshot): AccountData {
  const data = object(resource.privateData);
  if (!data) throw new Error("CodeBuddy account resource is invalid");
  const accessToken = text(data.accessToken);
  const userId = text(data.userId);
  if (!accessToken || !userId) {
    throw new Error("CodeBuddy account resource is missing credentials");
  }
  return {
    accessToken,
    refreshToken: text(data.refreshToken),
    expiresAtMs: absoluteTime(data.expiresAtMs),
    refreshExpiresAtMs: absoluteTime(data.refreshExpiresAtMs),
    userId,
    displayName: text(data.displayName) ?? userId,
    accountType: text(data.accountType),
    enterpriseId: text(data.enterpriseId),
    enterpriseName: text(data.enterpriseName),
  };
}

export function accountHeaders(data: AccountData): Record<string, string> {
  const headers: Record<string, string> = {
    authorization: `Bearer ${data.accessToken}`,
    "X-User-Id": data.userId,
  };
  if (data.enterpriseId) {
    headers["X-Enterprise-Id"] = data.enterpriseId;
    headers["X-Tenant-Id"] = data.enterpriseId;
  }
  return headers;
}

function refreshedData(current: AccountData, tokenValue: unknown): AccountData {
  return { ...current, ...parseAuthToken(tokenValue, current) };
}

export function shouldRefresh(data: AccountData, nowMs = Date.now()): boolean {
  return data.refreshToken !== null && data.expiresAtMs !== null &&
    data.expiresAtMs <= nowMs + REFRESH_MARGIN_MS;
}

export async function refreshAccessToken(
  current: AccountData,
  context: PluginContext,
): Promise<AccountData> {
  if (!current.refreshToken) throw new Error("CodeBuddy account has no refresh token");
  const response = await context.network.fetch(TOKEN_REFRESH_URL, {
    method: "POST",
    headers: {
      accept: "application/json",
      "content-type": "application/json",
      "X-Refresh-Token": current.refreshToken,
      "X-Auth-Refresh-Source": "plugin",
    },
    body: "{}",
  });
  const body = parseBody(response.body);
  const data = responseData(body);
  if (response.status < 200 || response.status >= 300 || !data) {
    throw new Error(
      `CodeBuddy token refresh failed (HTTP ${response.status}): ${responseMessage(body)}`,
    );
  }
  return refreshedData(current, data);
}

function accountList(value: unknown): Record<string, unknown>[] {
  const data = object(value);
  const source = data?.accounts;
  return Array.isArray(source)
    ? source.flatMap((entry) => object(entry) ? [object(entry)!] : [])
    : [];
}

async function fetchCurrentProfile(
  data: AccountData,
  context: PluginContext,
): Promise<AccountProfile | null> {
  const response = await context.network.fetch(ACCOUNTS_URL, {
    method: "GET",
    headers: { accept: "application/json", ...accountHeaders(data) },
  });
  const body = parseBody(response.body);
  if (response.status === 401 || response.status === 403) {
    throw new Error("CodeBuddy authorization expired; sign in again");
  }
  if (response.status < 200 || response.status >= 300) {
    throw new Error(
      `CodeBuddy account lookup failed (HTTP ${response.status}): ${responseMessage(body)}`,
    );
  }
  const accounts = accountList(body.data);
  const account =
    accounts.find((value) => text(value.uid ?? value.userId ?? value.user_id) === data.userId) ??
      accounts[0];
  return account ? parseAccountProfile(account) : null;
}

export function presentAccount(resource: ResourceSnapshot): ResourceView {
  const data = accountData(resource);
  const accountLabel = data.enterpriseName ??
    (data.accountType?.toLowerCase() === "personal" ? "个人账号" : data.accountType);
  return {
    displayName: data.displayName,
    description: accountLabel
      ? {
        "en-US": `CodeBuddy China · ${accountLabel}`,
        "zh-CN": `CodeBuddy 国内版 · ${accountLabel}`,
      }
      : { "en-US": "CodeBuddy China", "zh-CN": "CodeBuddy 国内版" },
  };
}

export async function refreshAccount(
  resource: ResourceSnapshot,
  context: PluginContext,
): Promise<ResourcePatch> {
  let data = accountData(resource);
  try {
    if (shouldRefresh(data)) data = await refreshAccessToken(data, context);
    const profile = await fetchCurrentProfile(data, context);
    if (profile) data = { ...data, ...profile };
    return { privateData: data as unknown as JsonValue, state: { status: "ready" } };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    if (/authorization expired|HTTP 401|HTTP 403/i.test(message)) {
      return { state: { status: "invalid", message } };
    }
    throw error;
  }
}

export function quotaExhaustedPatch(
  data: AccountData,
  nowMs = Date.now(),
): ResourcePatch {
  return {
    privateData: data as unknown as JsonValue,
    state: {
      status: "cooling",
      retryAtMs: nowMs + QUOTA_COOLING_MS,
      message: "CodeBuddy credits are exhausted or temporarily rate limited",
    },
  };
}
