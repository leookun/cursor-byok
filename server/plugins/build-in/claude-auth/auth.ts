import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import type { ResourceSnapshot } from "cursor-byok:resource";

export const RESOURCE_TYPE = "claude-account";
export const CLIENT_ID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e";
export const TOKEN_URL = "https://platform.claude.com/v1/oauth/token";
export const SCOPES = ["user:profile", "user:inference"];

export type AccountData = {
  accessToken: string;
  refreshToken: string;
  expiresAtMs: number;
  accountId: string;
  organizationId: string;
  displayName: string;
};

export function object(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

export function text(value: unknown): string | null {
  return typeof value === "string" && value.trim() ? value.trim() : null;
}

export function apiHeaders(accessToken: string): Record<string, string> {
  return {
    accept: "application/json",
    authorization: `Bearer ${accessToken}`,
    "anthropic-version": "2023-06-01",
    "anthropic-beta": "oauth-2025-04-20",
  };
}

export function accountData(resource: ResourceSnapshot): AccountData {
  const value = object(resource.privateData);
  if (
    resource.type !== RESOURCE_TYPE || !value ||
    !text(value.accessToken) || !text(value.refreshToken) ||
    !text(value.accountId) || !text(value.organizationId) || !text(value.displayName) ||
    typeof value.expiresAtMs !== "number" || !Number.isFinite(value.expiresAtMs) ||
    value.expiresAtMs <= 0
  ) {
    throw new Error("Claude account credentials are incomplete. Sign in again.");
  }
  return value as AccountData;
}

export function isExpiring(data: AccountData, now = Date.now()): boolean {
  return data.expiresAtMs <= now + 60_000;
}

/** Do not expose token endpoint bodies: even error responses may contain credentials. */
export class OAuthError extends Error {
  constructor(readonly status: number, readonly invalidCredentials: boolean) {
    super(
      invalidCredentials
        ? "Claude authorization was rejected. Sign in again."
        : `Claude authorization failed (HTTP ${status}). Try again later.`,
    );
  }
}

export async function requestTokens(
  params: Record<string, string>,
  context: PluginContext,
): Promise<Record<string, unknown>> {
  context.signal.throwIfAborted();
  let response;
  try {
    response = await context.network.fetch(TOKEN_URL, {
      method: "POST",
      headers: { accept: "application/json", "content-type": "application/json" },
      body: JSON.stringify({ client_id: CLIENT_ID, ...params }),
    });
  } catch {
    context.signal.throwIfAborted();
    throw new Error("Could not reach Claude authorization. Check your connection and retry.");
  }
  let body: Record<string, unknown> | null;
  try {
    body = object(JSON.parse(response.body));
  } catch {
    body = null;
  }
  if (response.status < 200 || response.status >= 300) {
    throw new OAuthError(
      response.status,
      response.status === 401 || response.status === 403 ||
        (response.status === 400 && body?.error === "invalid_grant"),
    );
  }
  if (!body) throw new Error("Claude returned invalid authorization data. Sign in again.");
  return body;
}

export function parseTokens(
  body: Record<string, unknown>,
  previous?: AccountData,
  now = Date.now(),
): AccountData {
  const accessToken = text(body.access_token);
  const refreshToken = text(body.refresh_token) ?? previous?.refreshToken;
  const expiresIn = body.expires_in;
  const account = object(body.account);
  const organization = object(body.organization);
  const accountId = text(account?.uuid) ?? previous?.accountId;
  const organizationId = text(organization?.uuid) ?? previous?.organizationId;
  const tokenType = text(body.token_type);
  if (
    !accessToken || !refreshToken || !accountId || !organizationId ||
    typeof expiresIn !== "number" || !Number.isFinite(expiresIn) || expiresIn <= 0 ||
    !Number.isSafeInteger(now + expiresIn * 1000) ||
    (tokenType !== null && tokenType.toLowerCase() !== "bearer")
  ) {
    throw new Error("Claude returned incomplete authorization data. Sign in again.");
  }
  if (
    previous &&
    (accountId !== previous.accountId || organizationId !== previous.organizationId)
  ) {
    throw new Error("Claude returned a different account during refresh. Sign in again.");
  }
  if (typeof body.scope === "string" && !body.scope.split(/\s+/).includes("user:inference")) {
    throw new Error("Claude did not grant model access. Sign in again and approve model access.");
  }
  return {
    accessToken,
    refreshToken,
    expiresAtMs: now + expiresIn * 1000,
    accountId,
    organizationId,
    displayName: text(account?.email_address) ?? previous?.displayName ?? accountId,
  };
}

/** Read stable identity from the profile endpoint, not optional token response metadata. */
export async function createAccount(
  tokens: Record<string, unknown>,
  context: PluginContext,
): Promise<AccountData> {
  const accessToken = text(tokens.access_token);
  if (!accessToken) throw new Error("Claude returned no access token. Sign in again.");
  context.signal.throwIfAborted();
  let response;
  try {
    response = await context.network.fetch("https://api.anthropic.com/api/oauth/profile", {
      headers: apiHeaders(accessToken),
    });
  } catch {
    context.signal.throwIfAborted();
    throw new Error("Could not load the Claude account. Check your connection and sign in again.");
  }
  if (response.status < 200 || response.status >= 300) {
    throw new Error(`Claude account lookup failed (HTTP ${response.status}). Sign in again.`);
  }
  let profile: Record<string, unknown> | null;
  try {
    profile = object(JSON.parse(response.body));
  } catch {
    profile = null;
  }
  const account = object(profile?.account);
  return parseTokens({
    ...tokens,
    account: { uuid: account?.uuid, email_address: account?.email },
    organization: profile?.organization,
  });
}

/**
 * Worker-local single flight for rotating refresh tokens. Retain the outcome until its
 * expiry so calls holding the same old host snapshot receive the same replacement.
 * The host remains the persistence owner; every consumer returns the replacement in a patch.
 */
const refreshes = new Map<string, { expiresAtMs: number; result: Promise<AccountData> }>();

export function refreshTokens(data: AccountData, context: PluginContext): Promise<AccountData> {
  context.signal.throwIfAborted();
  const now = Date.now();
  for (const [key, value] of refreshes) {
    if (value.expiresAtMs <= now) refreshes.delete(key);
  }
  const existing = refreshes.get(data.refreshToken);
  if (existing) return existing.result;
  const entry = {
    expiresAtMs: Infinity,
    result: requestTokens(
      { grant_type: "refresh_token", refresh_token: data.refreshToken },
      context,
    )
      .then((body) => {
        const refreshed = parseTokens(body, data);
        entry.expiresAtMs = refreshed.expiresAtMs - 60_000;
        return refreshed;
      }).catch((error: unknown) => {
        refreshes.delete(data.refreshToken);
        throw error;
      }),
  };
  refreshes.set(data.refreshToken, entry);
  return entry.result;
}

export function privateData(data: AccountData): JsonValue {
  return { ...data };
}
