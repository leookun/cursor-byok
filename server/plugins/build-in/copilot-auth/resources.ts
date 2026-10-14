import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import type {
  ResourceDraft,
  ResourceMetric,
  ResourcePatch,
  ResourceSnapshot,
  ResourceView,
} from "cursor-byok:resource";
import { COPILOT_USER_URL, githubHeaders } from "./constants.ts";
import {
  authError,
  copilotApiBase,
  CopilotAuthError,
  exchangeCopilotToken,
  isFresh,
} from "./token.ts";

export const RESOURCE_TYPE = "github-copilot-account";

/** premium_interactions 额度快照;percentRemaining 为 0..100。 */
export type AccountQuota = {
  percentRemaining: number;
  unlimited: boolean;
  resetAtMs: number | null;
};

/** 单条 github-copilot-account 资源的 privateData 形状。 */
export type AccountData = {
  githubToken: string;
  /** 登录时生成,作为 editor-device-id 固定发送。 */
  deviceId: string;
  login: string;
  plan: string | null;
  copilotToken: string | null;
  copilotTokenExpiresAtMs: number | null;
  apiBase: string | null;
  quota: AccountQuota | null;
};

export type CopilotUser = {
  login: string;
  plan: string | null;
  quota: AccountQuota | null;
};

/** invoke / 模型发现使用的可用 token;refreshed 表示需要把 data 写回资源。 */
export type ActiveToken = {
  data: AccountData;
  token: string;
  apiBase: string;
  refreshed: boolean;
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

function clampPercent(value: number): number {
  return Math.max(0, Math.min(100, value));
}

function storedQuota(value: unknown): AccountQuota | null {
  const quota = object(value);
  const percentRemaining = number(quota?.percentRemaining);
  if (!quota || percentRemaining === null) return null;
  return {
    percentRemaining,
    unlimited: quota.unlimited === true,
    resetAtMs: number(quota.resetAtMs),
  };
}

export function accountData(resource: ResourceSnapshot): AccountData {
  const data = object(resource.privateData);
  const githubToken = text(data?.githubToken);
  const deviceId = text(data?.deviceId);
  const login = text(data?.login);
  if (!githubToken || !deviceId || !login) {
    throw new Error("GitHub Copilot account resource is incomplete; sign in again");
  }
  return {
    githubToken,
    deviceId,
    login,
    plan: text(data?.plan),
    copilotToken: text(data?.copilotToken),
    copilotTokenExpiresAtMs: number(data?.copilotTokenExpiresAtMs),
    apiBase: text(data?.apiBase),
    quota: storedQuota(data?.quota),
  };
}

/** 解析 `GET /copilot_internal/user`:登录名、套餐与 premium 请求额度。 */
export function parseCopilotUser(body: unknown): CopilotUser {
  const root = object(body);
  const login = text(root?.login);
  if (!login) throw new Error("Copilot user response is missing the GitHub login");
  const premium = object(object(root?.quota_snapshots)?.premium_interactions);
  const unlimited = premium?.unlimited === true;
  const percent = number(premium?.percent_remaining) ?? (unlimited ? 100 : null);
  const resetAtMs = typeof root?.quota_reset_date === "string"
    ? Date.parse(root.quota_reset_date)
    : NaN;
  return {
    login,
    plan: text(root?.copilot_plan),
    quota: premium && percent !== null
      ? {
        percentRemaining: clampPercent(percent),
        unlimited,
        resetAtMs: Number.isFinite(resetAtMs) ? resetAtMs : null,
      }
      : null,
  };
}

export async function fetchCopilotUser(
  githubToken: string,
  context: PluginContext,
): Promise<CopilotUser> {
  const response = await context.network.fetch(COPILOT_USER_URL, {
    method: "GET",
    headers: githubHeaders(githubToken),
  });
  if (response.status < 200 || response.status >= 300) {
    throw authError(response.status, response.body) ??
      new Error(`Copilot account lookup failed (HTTP ${response.status}): ${response.body}`);
  }
  let body: unknown;
  try {
    body = JSON.parse(response.body);
  } catch {
    throw new Error("Copilot account lookup returned invalid JSON");
  }
  return parseCopilotUser(body);
}

/** 设备码授权完成后:读取账号信息并做一次 token 交换,确认订阅可用。 */
export async function accountDraft(
  githubToken: string,
  context: PluginContext,
): Promise<ResourceDraft> {
  const user = await fetchCopilotUser(githubToken, context);
  const token = await exchangeCopilotToken(githubToken, context);
  const data: AccountData = {
    githubToken,
    deviceId: crypto.randomUUID(),
    login: user.login,
    plan: user.plan,
    copilotToken: token.token,
    copilotTokenExpiresAtMs: token.expiresAtMs,
    apiBase: token.apiBase,
    quota: user.quota,
  };
  return { key: user.login.toLowerCase(), privateData: data as unknown as JsonValue };
}

/** 缓存的 Copilot token 仍新鲜时直接使用,否则(或 force 时)重新交换。 */
export async function ensureToken(
  data: AccountData,
  context: PluginContext,
  force = false,
): Promise<ActiveToken> {
  if (
    !force && data.copilotToken !== null &&
    isFresh(data.copilotToken, data.copilotTokenExpiresAtMs)
  ) {
    return {
      data,
      token: data.copilotToken,
      apiBase: copilotApiBase(data.apiBase),
      refreshed: false,
    };
  }
  const token = await exchangeCopilotToken(data.githubToken, context);
  return {
    data: {
      ...data,
      copilotToken: token.token,
      copilotTokenExpiresAtMs: token.expiresAtMs,
      apiBase: token.apiBase,
    },
    token: token.token,
    apiBase: token.apiBase,
    refreshed: true,
  };
}

export function presentAccount(resource: ResourceSnapshot): ResourceView {
  const data = accountData(resource);
  const metrics: ResourceMetric[] = [];
  if (data.quota && !data.quota.unlimited) {
    metrics.push({
      id: "premium",
      label: { "zh-CN": "Premium 请求剩余", "en-US": "Premium requests left" },
      unit: "percent",
      value: data.quota.percentRemaining,
      ...(data.quota.resetAtMs !== null ? { resetAtMs: data.quota.resetAtMs } : {}),
    });
  }
  return {
    displayName: data.login,
    description: data.plan ? `Copilot ${data.plan}` : "GitHub Copilot",
    ...(metrics.length > 0 ? { metrics } : {}),
  };
}

export async function refreshAccount(
  resource: ResourceSnapshot,
  context: PluginContext,
): Promise<ResourcePatch> {
  const data = accountData(resource);
  try {
    const user = await fetchCopilotUser(data.githubToken, context);
    const active = await ensureToken(data, context, true);
    const next: AccountData = {
      ...active.data,
      login: user.login,
      plan: user.plan,
      quota: user.quota,
    };
    return { privateData: next as unknown as JsonValue, state: { status: "ready" } };
  } catch (error) {
    if (error instanceof CopilotAuthError) {
      return { state: { status: "invalid", message: error.message } };
    }
    throw error;
  }
}
