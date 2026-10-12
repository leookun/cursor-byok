import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import type {
  ResourceDraft,
  ResourceImportFile,
  ResourceImportResult,
  ResourceImportSupport,
  ResourceMetric,
  ResourcePatch,
  ResourceSnapshot,
  ResourceState,
  ResourceView,
} from "cursor-byok:resource";

export const RESOURCE_TYPE = "grok-account";

const CREDITS_URL = "https://cli-chat-proxy.grok.com/v1/billing?format=credits";
const ONE_HOUR_MS = 60 * 60 * 1000;
const TOKEN_URL = "https://auth.x.ai/oauth2/token";
const CLIENT_ID = "b1a00492-073a-47ea-816f-4c329264a828";
const REFRESH_BUFFER_MS = 5 * 60 * 1000;

export type AccountQuota = {
  planLabel: string | null;
  usedPercent: number | null;
  remainingPercent: number | null;
  resetAtMs: number | null;
  limitReached: boolean;
  updatedAtMs: number;
};

/** 单条 grok-account 资源的 privateData 形状。 */
export type AccountData = {
  accessToken: string;
  refreshToken: string | null;
  displayName: string;
  quota: AccountQuota | null;
  expiresAtMs?: number;
  refreshError?: string;
  refreshRetryAtMs?: number;
};

export type CredentialCandidate = {
  accessToken: string;
  refreshToken: string | null;
  displayName: string | null;
  expiresAtMs?: number;
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

function decodeJwtPayload(token: string): Record<string, unknown> | null {
  const encoded = token.split(".")[1];
  if (!encoded) return null;
  try {
    const normalized = encoded.replace(/-/g, "+").replace(/_/g, "/");
    const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "=");
    const bytes = Uint8Array.from(atob(padded), (character) => character.charCodeAt(0));
    return object(JSON.parse(new TextDecoder().decode(bytes)));
  } catch {
    return null;
  }
}

function claim(payload: Record<string, unknown> | null, key: string): string | null {
  return payload ? text(payload[key]) : null;
}

async function tokenFingerprint(token: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(token));
  return Array.from(
    new Uint8Array(digest).slice(0, 8),
    (byte) => byte.toString(16).padStart(2, "0"),
  ).join("");
}

export async function accountIdentity(
  accessToken: string,
): Promise<{ key: string; displayName: string }> {
  const payload = decodeJwtPayload(accessToken);
  const identity = claim(payload, "sub") ??
    claim(payload, "email") ??
    await tokenFingerprint(accessToken);
  const displayName = claim(payload, "email") ??
    claim(payload, "preferred_username") ??
    claim(payload, "name") ??
    identity;
  return { key: `grok:${identity}`, displayName };
}

export async function credentialDraft(credential: CredentialCandidate): Promise<ResourceDraft> {
  const identity = await accountIdentity(credential.accessToken);
  const data: AccountData = {
    accessToken: credential.accessToken,
    refreshToken: credential.refreshToken,
    displayName: credential.displayName ?? identity.displayName,
    quota: null,
    ...(credential.expiresAtMs ? { expiresAtMs: credential.expiresAtMs } : {}),
  };
  return { key: identity.key, privateData: data as unknown as JsonValue };
}

export function accountData(resource: ResourceSnapshot): AccountData {
  const data = object(resource.privateData);
  const accessToken = text(data?.accessToken);
  if (!accessToken) throw new Error("Grok account resource is missing its access token");
  return {
    accessToken,
    refreshToken: text(data?.refreshToken),
    displayName: text(data?.displayName) ?? "Grok account",
    quota: (data?.quota ?? null) as AccountQuota | null,
    ...(number(data?.expiresAtMs) !== null ? { expiresAtMs: number(data?.expiresAtMs)! } : {}),
    ...(text(data?.refreshError) ? { refreshError: text(data?.refreshError)! } : {}),
    ...(number(data?.refreshRetryAtMs) !== null
      ? { refreshRetryAtMs: number(data?.refreshRetryAtMs)! }
      : {}),
  };
}

export function tokenExpiresAt(data: AccountData): number | null {
  const exp = number(decodeJwtPayload(data.accessToken)?.exp);
  return data.expiresAtMs ?? (exp !== null ? exp * 1000 : null);
}

/** The host holds the account lock and commits this patch BEFORE any inference begins. */
export async function prepareAccount(
  resource: ResourceSnapshot,
  rejectedResource: ResourceSnapshot | null,
  context: PluginContext,
): Promise<ResourcePatch | null> {
  const data = accountData(resource);
  const now = Date.now();
  const expiry = tokenExpiresAt(data);
  const rejected = rejectedResource !== null &&
    accountData(rejectedResource).accessToken === data.accessToken;
  const expired = () => expiry !== null && expiry <= Date.now();
  const stateInvalid = resource.state.status === "invalid";
  const failure = (message: string, terminal: boolean): ResourcePatch => {
    const next = { ...data };
    if (terminal) next.refreshError = message;
    else next.refreshRetryAtMs = Date.now() + 15_000;
    return {
      privateData: next as unknown as JsonValue,
      state: terminal
        ? { status: "invalid", message }
        : (expired() || rejected || stateInvalid)
        ? { status: "cooling", retryAtMs: next.refreshRetryAtMs!, message }
        : { status: "ready" },
    };
  };
  if (data.refreshError) return failure(data.refreshError, true);
  if (!rejected && !stateInvalid && (expiry === null || expiry > now + REFRESH_BUFFER_MS)) {
    return null;
  }
  if (data.refreshRetryAtMs && data.refreshRetryAtMs > now) {
    // Keep the original deadline; repeated calls must not postpone recovery indefinitely.
    return (expired() || rejected || stateInvalid)
      ? {
        state: {
          status: "cooling",
          retryAtMs: data.refreshRetryAtMs,
          message: "Grok token refresh temporarily unavailable; retry shortly",
        },
      }
      : null;
  }
  if (!data.refreshToken) return failure("Grok refresh token is missing; sign in again", true);
  for (let attempt = 0; attempt < 3; attempt++) {
    let response;
    try {
      response = await context.network.fetch(TOKEN_URL, {
        method: "POST",
        headers: {
          accept: "application/json",
          "content-type": "application/x-www-form-urlencoded",
        },
        body: new URLSearchParams({
          grant_type: "refresh_token",
          client_id: CLIENT_ID,
          refresh_token: data.refreshToken,
        }).toString(),
      });
    } catch {
      // A lost response may have rotated the token. Do not repeatedly replay it in this call.
      return failure("Grok token refresh network error; retry shortly", false);
    }
    let body: Record<string, unknown> = {};
    try {
      body = object(JSON.parse(response.body)) ?? {};
    } catch { /* classified below */ }
    if (response.status >= 200 && response.status < 300) {
      const accessToken = text(body.access_token);
      const refreshToken = text(body.refresh_token) ?? data.refreshToken;
      // Even a malformed successful response must not discard a rotated refresh token.
      if (!accessToken) {
        data.refreshToken = refreshToken;
        return failure("Grok token refresh returned no access token; retry shortly", false);
      }
      const seconds = number(body.expires_in);
      const jwtExpiry = number(decodeJwtPayload(accessToken)?.exp);
      const expiresAtMs = seconds !== null && seconds > 0
        ? Date.now() + seconds * 1000
        : jwtExpiry !== null
        ? jwtExpiry * 1000
        : undefined;
      const { refreshError: _error, refreshRetryAtMs: _retry, expiresAtMs: _expiry, ...previous } =
        data;
      return {
        privateData: {
          ...previous,
          accessToken,
          refreshToken,
          ...(expiresAtMs ? { expiresAtMs } : {}),
        } as unknown as JsonValue,
        state: { status: "ready" },
      };
    }
    const code = text(body.error);
    if (code === "invalid_grant" || code === "invalid_client") {
      return failure(`Grok refresh authorization rejected (${code}); sign in again`, true);
    }
    if ((response.status === 429 || response.status >= 500) && attempt < 2) {
      await new Promise((resolve) => setTimeout(resolve, 500 * 2 ** attempt));
      continue;
    }
    return failure(
      `Grok token refresh temporarily failed (HTTP ${response.status}); retry shortly`,
      false,
    );
  }
  return failure("Grok token refresh temporarily unavailable; retry shortly", false);
}

function clampPercent(value: number): number {
  return Math.max(0, Math.min(100, value));
}

function resetAtMs(value: unknown): number | null {
  const numeric = number(value);
  if (numeric !== null) return numeric > 10_000_000_000 ? numeric : numeric * 1000;
  if (typeof value === "string") {
    const parsed = Date.parse(value);
    if (Number.isFinite(parsed)) return parsed;
  }
  return null;
}

/** 解析 Grok CLI 计费接口的积分响应;creditUsagePercent 表示已用占比。 */
export function parseGrokUsage(body: unknown, nowMs = Date.now()): AccountQuota {
  const root = object(body) ?? {};
  const config = object(root.config) ?? root;
  let used = number(config.creditUsagePercent ?? config.credit_usage_percent);
  if (used === null) {
    const onDemandUsed = number(config.onDemandUsed ?? config.on_demand_used);
    const onDemandCap = number(config.onDemandCap ?? config.on_demand_cap);
    if (onDemandUsed !== null && onDemandCap !== null && onDemandCap > 0) {
      used = (onDemandUsed / onDemandCap) * 100;
    }
  }
  // 存在计费周期但没有用量字段时视为未使用。
  if (used === null && (config.currentPeriod ?? config.current_period) !== undefined) {
    used = 0;
  }
  const remaining = used === null ? null : clampPercent(100 - used);
  const period = object(config.currentPeriod ?? config.current_period);
  return {
    planLabel: text(
      config.subscriptionTierDisplay ?? config.subscription_tier_display ??
        config.subscriptionTier ?? config.product,
    ),
    usedPercent: used === null ? null : clampPercent(used),
    remainingPercent: remaining,
    resetAtMs: resetAtMs(period?.end ?? config.billingPeriodEnd ?? config.billing_period_end),
    limitReached: remaining !== null && remaining <= 0,
    updatedAtMs: nowMs,
  };
}

export function quotaState(quota: AccountQuota | null, nowMs = Date.now()): ResourceState {
  if (!quota || !quota.limitReached) return { status: "ready" };
  if (quota.resetAtMs !== null && quota.resetAtMs <= nowMs) return { status: "ready" };
  return {
    status: "cooling",
    retryAtMs: quota.resetAtMs ?? nowMs + ONE_HOUR_MS,
    message: "Grok credits are exhausted",
  };
}

/** 额度耗尽时的资源补丁:标记积分耗尽并进入冷却,重置时间未知时回退 1 小时。 */
export function quotaExhaustedPatch(data: AccountData, nowMs = Date.now()): ResourcePatch {
  const quota: AccountQuota = {
    planLabel: data.quota?.planLabel ?? null,
    usedPercent: 100,
    remainingPercent: 0,
    resetAtMs: data.quota?.resetAtMs !== undefined && data.quota?.resetAtMs !== null &&
        data.quota.resetAtMs > nowMs
      ? data.quota.resetAtMs
      : null,
    limitReached: true,
    updatedAtMs: nowMs,
  };
  return {
    privateData: { ...data, quota } as unknown as JsonValue,
    state: quotaState(quota, nowMs),
  };
}

export function accountHeaders(data: AccountData): Record<string, string> {
  return {
    accept: "application/json",
    authorization: `Bearer ${data.accessToken}`,
    // Grok CLI 计费接口要求该头标识客户端来源。
    "x-xai-token-auth": "xai-grok-cli",
  };
}

function jwtDisplayName(token: string | null): string | null {
  if (!token) return null;
  const payload = decodeJwtPayload(token);
  return claim(payload, "email") ?? claim(payload, "preferred_username") ??
    claim(payload, "name");
}

export function presentAccount(resource: ResourceSnapshot): ResourceView {
  const data = accountData(resource);
  const metrics: ResourceMetric[] = [];
  const quota = data.quota;
  if (quota && quota.remainingPercent !== null) {
    metrics.push({
      id: "credits",
      label: { "en-US": "Credits", "zh-CN": "积分额度" },
      unit: "percent",
      value: quota.remainingPercent,
      ...(quota.resetAtMs !== null ? { resetAtMs: quota.resetAtMs } : {}),
    });
  }
  return {
    // 旧记录可能存的是账号 ID;展示时优先从 token 现算邮箱。
    displayName: jwtDisplayName(data.accessToken) ?? data.displayName,
    ...(quota?.planLabel ? { description: quota.planLabel } : {}),
    ...(metrics.length > 0 ? { metrics } : {}),
  };
}

export async function refreshAccount(
  resource: ResourceSnapshot,
  context: PluginContext,
): Promise<ResourcePatch> {
  const data = accountData(resource);
  const response = await context.network.fetch(CREDITS_URL, {
    method: "GET",
    headers: accountHeaders(data),
  });
  if (response.status < 200 || response.status >= 300) {
    if (response.status === 401) {
      return {
        state: { status: "invalid", message: "Grok authorization expired; sign in again" },
      };
    }
    if (
      (response.status === 403 || response.status === 429) &&
      /spending-limit|quota|credits exhausted|run out of credits/i.test(response.body)
    ) {
      return quotaExhaustedPatch(data);
    }
    throw new Error(`Grok usage lookup failed (HTTP ${response.status}): ${response.body}`);
  }
  let body: unknown;
  try {
    body = JSON.parse(response.body);
  } catch {
    throw new Error("Grok usage lookup returned invalid JSON");
  }
  const quota = parseGrokUsage(body);
  return {
    privateData: { ...data, quota } as unknown as JsonValue,
    state: quotaState(quota),
  };
}

function firstText(source: Record<string, unknown>, keys: string[]): string | null {
  for (const key of keys) {
    const value = text(source[key]);
    if (value) return value;
  }
  return null;
}

function collectCredentials(value: unknown, output: CredentialCandidate[]): void {
  if (Array.isArray(value)) {
    for (const item of value) collectCredentials(item, output);
    return;
  }
  const item = object(value);
  if (!item || item.disabled === true) return;
  for (const key of ["accounts", "credentials", "items"]) {
    if (Array.isArray(item[key])) {
      collectCredentials(item[key], output);
      return;
    }
  }
  const tokens = object(item.tokens) ?? item;
  const accessToken = firstText(tokens, ["access_token", "accessToken", "token", "key"]) ??
    firstText(item, ["access_token", "accessToken", "token", "key", "XAI_API_KEY"]);
  if (!accessToken) return;
  const refreshToken = firstText(tokens, ["refresh_token", "refreshToken"]) ??
    firstText(item, ["refresh_token", "refreshToken"]);
  const displayName = firstText(item, ["email", "display_name", "displayName", "name"]) ??
    firstText(tokens, ["email", "display_name", "displayName", "name"]);
  output.push({ accessToken, refreshToken, displayName });
}

export function parseCredentialFiles(files: ResourceImportFile[]): {
  credentials: CredentialCandidate[];
  warnings: string[];
} {
  const credentials: CredentialCandidate[] = [];
  const warnings: string[] = [];
  for (const file of files) {
    let content: unknown;
    try {
      content = JSON.parse(file.content);
    } catch {
      warnings.push(`${file.name}: not valid JSON`);
      continue;
    }
    const found: CredentialCandidate[] = [];
    collectCredentials(content, found);
    if (found.length === 0) {
      warnings.push(`${file.name}: no Grok access token found`);
      continue;
    }
    credentials.push(...found);
  }
  return { credentials, warnings };
}

export const credentialImport: ResourceImportSupport = {
  displayName: {
    "en-US": "Import Grok credentials",
    "zh-CN": "导入 Grok 凭证",
  },
  description: {
    "en-US": "Import one or more Grok JSON credential files.",
    "zh-CN": "导入一个或多个 Grok JSON 凭证文件。",
  },
  accept: [".json"],
  multiple: true,
  parse: async (files: ResourceImportFile[]): Promise<ResourceImportResult> => {
    const { credentials, warnings } = parseCredentialFiles(files);
    if (credentials.length === 0) {
      throw new Error(warnings.join("; ") || "credential JSON does not contain an access token");
    }
    return {
      resources: await Promise.all(credentials.map(credentialDraft)),
      ...(warnings.length > 0 ? { warnings } : {}),
    };
  },
};
