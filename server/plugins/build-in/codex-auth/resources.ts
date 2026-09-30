import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import type {
  ResourceAction,
  ResourceActionCard,
  ResourceActionResult,
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

export const RESOURCE_TYPE = "chatgpt-account";

const USAGE_URL = "https://chatgpt.com/backend-api/wham/usage";
const RESET_CREDITS_URL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits";
const RESET_CREDITS_CONSUME_URL = `${RESET_CREDITS_URL}/consume`;
const FIVE_HOURS_MS = 5 * 60 * 60 * 1000;

export const LIST_RESET_CARDS_ACTION_ID = "list-reset-cards";
export const CONSUME_RESET_CARD_ACTION_ID = "consume-reset-card";

export type QuotaWindow = {
  usedPercent: number | null;
  remainingPercent: number | null;
  resetAtMs: number | null;
};

export type AccountQuota = {
  planLabel: string | null;
  weekly: QuotaWindow | null;
  fiveHour: QuotaWindow | null;
  resetCreditsAvailable: number | null;
  /** Earliest known future expiry of one available card, not of the entire balance. */
  resetCreditsExpiresAtMs: number | null;
  limitReached: boolean;
  updatedAtMs: number;
};

/** 单条 chatgpt-account 资源的 privateData 形状。 */
export type AccountData = {
  accessToken: string;
  refreshToken: string | null;
  accountId: string | null;
  displayName: string;
  quota: AccountQuota | null;
};

export type CredentialCandidate = {
  accessToken: string;
  refreshToken: string | null;
  accountId?: string | null;
  displayName: string | null;
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

export function chatGptAccountId(accessToken: string): string | null {
  const payload = decodeJwtPayload(accessToken);
  const auth = object(payload?.["https://api.openai.com/auth"]);
  return text(auth?.chatgpt_account_id) ?? claim(payload, "chatgpt_account_id");
}

async function tokenFingerprint(token: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(token));
  return Array.from(
    new Uint8Array(digest).slice(0, 8),
    (byte) => byte.toString(16).padStart(2, "0"),
  ).join("");
}

/** ChatGPT access token 的邮箱通常在 OpenAI 的 profile 声明里,而不是顶层 email。 */
function profileEmail(payload: Record<string, unknown> | null): string | null {
  const profile = object(payload?.["https://api.openai.com/profile"]);
  return text(profile?.email);
}

export async function accountIdentity(
  accessToken: string,
  accountId?: string | null,
): Promise<{ key: string; displayName: string }> {
  const payload = decodeJwtPayload(accessToken);
  const identity = accountId ?? chatGptAccountId(accessToken) ??
    claim(payload, "sub") ??
    claim(payload, "email") ??
    await tokenFingerprint(accessToken);
  const displayName = claim(payload, "email") ??
    profileEmail(payload) ??
    claim(payload, "preferred_username") ??
    claim(payload, "name") ??
    identity;
  return { key: `codex:${identity}`, displayName };
}

export async function credentialDraft(credential: CredentialCandidate): Promise<ResourceDraft> {
  const identity = await accountIdentity(credential.accessToken, credential.accountId);
  const data: AccountData = {
    accessToken: credential.accessToken,
    refreshToken: credential.refreshToken,
    accountId: credential.accountId ?? chatGptAccountId(credential.accessToken),
    displayName: credential.displayName ?? identity.displayName,
    quota: null,
  };
  return { key: identity.key, privateData: data as unknown as JsonValue };
}

export function accountData(resource: ResourceSnapshot): AccountData {
  const data = object(resource.privateData);
  const accessToken = text(data?.accessToken);
  if (!accessToken) throw new Error("ChatGPT account resource is missing its access token");
  return {
    accessToken,
    refreshToken: text(data?.refreshToken),
    accountId: text(data?.accountId) ?? chatGptAccountId(accessToken),
    displayName: text(data?.displayName) ?? "ChatGPT account",
    quota: (data?.quota ?? null) as AccountQuota | null,
  };
}

export function accountHeaders(data: AccountData): Record<string, string> {
  const headers: Record<string, string> = {
    accept: "application/json",
    originator: "codex_cli_rs",
    authorization: `Bearer ${data.accessToken}`,
  };
  const accountId = data.accountId ?? chatGptAccountId(data.accessToken);
  if (accountId) headers["ChatGPT-Account-Id"] = accountId;
  return headers;
}

function clampPercent(value: number): number {
  return Math.max(0, Math.min(100, value));
}

function resetAtMs(window: Record<string, unknown>, nowMs: number): number | null {
  const resetAt = window.reset_at ?? window.resetAt;
  const numeric = number(resetAt);
  if (numeric !== null) return numeric > 10_000_000_000 ? numeric : numeric * 1000;
  if (typeof resetAt === "string") {
    const parsed = Date.parse(resetAt);
    if (Number.isFinite(parsed)) return parsed;
  }
  const afterSeconds = number(window.reset_after_seconds ?? window.resetAfterSeconds);
  return afterSeconds === null ? null : nowMs + afterSeconds * 1000;
}

function quotaWindow(value: unknown, nowMs: number): QuotaWindow | null {
  const window = object(value);
  if (!window) return null;
  const used = number(window.used_percent ?? window.usedPercent);
  const remaining = used === null
    ? number(window.remaining_percent ?? window.remainingPercent)
    : clampPercent(100 - used);
  return {
    usedPercent: used === null
      ? (remaining === null ? null : clampPercent(100 - remaining))
      : clampPercent(used),
    remainingPercent: remaining === null ? null : clampPercent(remaining),
    resetAtMs: resetAtMs(window, nowMs),
  };
}

function planLabel(value: unknown): string | null {
  const plan = text(value);
  if (!plan) return null;
  const labels: Record<string, string> = {
    plus: "ChatGPT Plus",
    pro: "ChatGPT Pro",
    team: "ChatGPT Team",
    business: "ChatGPT Business",
    enterprise: "ChatGPT Enterprise",
    free: "ChatGPT Free",
    go: "ChatGPT Go",
  };
  return labels[plan.toLowerCase()] ?? plan;
}

export function parseCodexUsage(body: unknown, nowMs = Date.now()): AccountQuota {
  const root = object(body) ?? {};
  const rateLimit = object(root.rate_limit ?? root.rateLimit) ?? root;
  const primary = rateLimit.primary_window ?? rateLimit.primaryWindow;
  const secondary = rateLimit.secondary_window ?? rateLimit.secondaryWindow;
  const weekly = quotaWindow(secondary ?? primary, nowMs);
  const fiveHour = secondary === undefined || secondary === null
    ? null
    : quotaWindow(primary, nowMs);
  const explicitLimit = rateLimit.limit_reached ?? rateLimit.limitReached;
  const resetCredits = object(root.rate_limit_reset_credits ?? root.rateLimitResetCredits);
  const resetCreditsAvailable = number(
    resetCredits?.available_count ?? resetCredits?.availableCount,
  );
  return {
    planLabel: planLabel(root.plan_type ?? root.planType),
    weekly,
    fiveHour,
    resetCreditsAvailable: resetCreditsAvailable === null
      ? null
      : Math.max(0, Math.floor(resetCreditsAvailable)),
    resetCreditsExpiresAtMs: null,
    limitReached: typeof explicitLimit === "boolean" ? explicitLimit : [weekly, fiveHour].some(
      (window) => window?.remainingPercent !== null && window?.remainingPercent === 0,
    ),
    updatedAtMs: nowMs,
  };
}

function windowCoolingUntil(window: QuotaWindow | null, nowMs: number): number | null {
  if (!window || window.remainingPercent === null || window.remainingPercent > 0) return null;
  if (window.resetAtMs !== null && window.resetAtMs <= nowMs) return null;
  return window.resetAtMs ?? nowMs + FIVE_HOURS_MS;
}

export function quotaCoolingUntil(quota: AccountQuota, nowMs = Date.now()): number | null {
  const resets = [
    windowCoolingUntil(quota.weekly, nowMs),
    windowCoolingUntil(quota.fiveHour, nowMs),
  ].filter((value): value is number => value !== null);
  if (resets.length > 0) return Math.max(...resets);
  return quota.limitReached ? nowMs + FIVE_HOURS_MS : null;
}

export function quotaState(quota: AccountQuota | null, nowMs = Date.now()): ResourceState {
  if (!quota) return { status: "ready" };
  const coolingUntil = quotaCoolingUntil(quota, nowMs);
  return coolingUntil === null
    ? { status: "ready" }
    : { status: "cooling", retryAtMs: coolingUntil, message: "ChatGPT quota is exhausted" };
}

/** 从上游错误文本中提取重置时间;拿不到时回退 5 小时。 */
function resetFromError(error: string, nowMs: number): number {
  const resetAt = error.match(/["']?reset_at["']?\s*[:=]\s*["']?(\d+(?:\.\d+)?)/i)?.[1];
  if (resetAt) {
    const value = Number(resetAt);
    if (Number.isFinite(value)) return value > 10_000_000_000 ? value : value * 1000;
  }
  const resetAfter = error.match(/["']?reset_after_seconds["']?\s*[:=]\s*["']?(\d+(?:\.\d+)?)/i)
    ?.[1];
  if (resetAfter) {
    const value = Number(resetAfter);
    if (Number.isFinite(value)) return nowMs + value * 1000;
  }
  return nowMs + FIVE_HOURS_MS;
}

/** 额度耗尽时的资源补丁:标记 5 小时窗口耗尽并按重置时间进入冷却。 */
export function quotaExhaustedPatch(
  data: AccountData,
  error: string,
  nowMs = Date.now(),
): ResourcePatch {
  const quota: AccountQuota = {
    planLabel: data.quota?.planLabel ?? null,
    weekly: data.quota?.weekly ?? null,
    fiveHour: {
      usedPercent: 100,
      remainingPercent: 0,
      resetAtMs: resetFromError(error, nowMs),
    },
    resetCreditsAvailable: data.quota?.resetCreditsAvailable ?? null,
    resetCreditsExpiresAtMs: data.quota?.resetCreditsExpiresAtMs ?? null,
    limitReached: true,
    updatedAtMs: nowMs,
  };
  return {
    privateData: { ...data, quota } as unknown as JsonValue,
    state: quotaState(quota, nowMs),
  };
}

async function fetchResetCredits(
  data: AccountData,
  context: PluginContext,
): Promise<{ cards: ResourceActionCard[]; availableCount: number | null }> {
  const accountId = data.accountId ?? chatGptAccountId(data.accessToken);
  if (!accountId) throw new Error("ChatGPT account is missing its account ID");
  const response = await context.network.fetch(RESET_CREDITS_URL, {
    method: "GET",
    headers: accountHeaders(data),
  });
  if (response.status < 200 || response.status >= 300) {
    throw new Error(
      `Codex reset card lookup failed (HTTP ${response.status}): ${response.body}`,
    );
  }
  let body: unknown;
  try {
    body = JSON.parse(response.body);
  } catch {
    throw new Error("Codex reset card lookup returned invalid JSON");
  }
  const root = object(body);
  if (!root || !Array.isArray(root.credits)) {
    throw new Error("Codex reset card lookup returned invalid card details");
  }
  const rawCredits = root.credits;
  const cards = rawCredits.map((value, index): ResourceActionCard => {
    const credit = object(value);
    const id = text(credit?.id);
    const status = text(credit?.status);
    if (!id || !status) {
      throw new Error("Codex reset card lookup returned invalid card details");
    }
    const resetType = text(credit?.reset_type ?? credit?.resetType);
    const grantedAt = timestampMs(credit?.granted_at ?? credit?.grantedAt);
    const expiresAt = timestampMs(credit?.expires_at ?? credit?.expiresAt);
    return {
      id,
      title: text(credit?.title) ?? resetType ?? `Codex reset card ${index + 1}`,
      status,
      ...(grantedAt !== null ? { grantedAtMs: grantedAt } : {}),
      ...(expiresAt !== null ? { expiresAtMs: expiresAt } : {}),
      fields: resetType
        ? [{
          id: "reset-type",
          label: { "en-US": "Reset type", "zh-CN": "重置类型" },
          value: resetType,
        }]
        : [],
    };
  });
  const rawCount = root.available_count ?? root.availableCount;
  const availableCount = number(rawCount);
  if (
    rawCount !== undefined && rawCount !== null &&
    (availableCount === null || !Number.isInteger(availableCount) || availableCount < 0)
  ) {
    throw new Error("Codex reset card lookup returned an invalid available count");
  }
  return {
    cards,
    // An empty list without a count may be an incomplete response, not a zero balance.
    availableCount: availableCount ??
      (cards.length > 0 ? cards.filter((card) => card.status === "available").length : null),
  };
}

function timestampMs(value: unknown): number | null {
  const numeric = number(value);
  if (numeric !== null) return numeric > 10_000_000_000 ? numeric : numeric * 1000;
  if (typeof value === "string") {
    const parsed = Date.parse(value);
    if (Number.isFinite(parsed)) return parsed;
  }
  return null;
}

function actionDescription(availableCount: number | null): ResourceActionResult["description"] {
  if (availableCount === null) {
    return { "en-US": "Available reset card count unknown", "zh-CN": "可用重置卡数量未知" };
  }
  return {
    "en-US": `${availableCount} reset card${availableCount === 1 ? "" : "s"} available`,
    "zh-CN": `可用重置卡 ${availableCount} 张`,
  };
}

async function listResetCards(
  resource: ResourceSnapshot,
  _input: JsonValue,
  context: PluginContext,
): Promise<ResourceActionResult> {
  const result = await fetchResetCredits(accountData(resource), context);
  return {
    title: { "en-US": "Codex reset cards", "zh-CN": "Codex 重置卡" },
    description: actionDescription(result.availableCount),
    cards: result.cards,
  };
}

async function consumeResetCard(
  resource: ResourceSnapshot,
  input: JsonValue,
  context: PluginContext,
): Promise<ResourceActionResult> {
  const inputObject = object(input);
  const creditId = text(inputObject?.creditId ?? inputObject?.cardId);
  if (!creditId) throw new Error("A reset card ID is required");

  const data = accountData(resource);
  const available = await fetchResetCredits(data, context);
  const card = available.cards.find((item) =>
    item.id === creditId && item.status === "available" &&
    (item.expiresAtMs === undefined || item.expiresAtMs > Date.now())
  );
  if (!card) throw new Error("The selected reset card is not available");

  const response = await context.network.fetch(RESET_CREDITS_CONSUME_URL, {
    method: "POST",
    headers: { ...accountHeaders(data), "content-type": "application/json" },
    body: JSON.stringify({ credit_id: creditId, redeem_request_id: crypto.randomUUID() }),
  });
  if (response.status < 200 || response.status >= 300) {
    throw new Error(
      `Codex reset card consumption failed (HTTP ${response.status}): ${response.body}`,
    );
  }

  let result: unknown;
  try {
    result = JSON.parse(response.body);
  } catch {
    throw new Error("Codex reset card consumption returned invalid JSON");
  }
  if (object(result)?.code !== "reset") {
    throw new Error("Codex reset card consumption did not confirm a quota reset");
  }

  const patch = await refreshAccount(resource, context);
  const refreshedResource: ResourceSnapshot = {
    ...resource,
    ...(patch.privateData ? { privateData: patch.privateData } : {}),
  };
  const refreshed = await fetchResetCredits(accountData(refreshedResource), context);
  return {
    title: { "en-US": "Codex reset card used", "zh-CN": "Codex 重置卡已使用" },
    description: actionDescription(refreshed.availableCount),
    cards: refreshed.cards,
    patch,
  };
}

export const listResetCardsAction: ResourceAction = {
  id: LIST_RESET_CARDS_ACTION_ID,
  displayName: { "en-US": "View reset cards", "zh-CN": "查看重置卡" },
  description: {
    "en-US": "List available Codex reset cards.",
    "zh-CN": "查看当前账号的 Codex 重置卡。",
  },
  target: "resource",
  run: listResetCards,
};

export const consumeResetCardAction: ResourceAction = {
  id: CONSUME_RESET_CARD_ACTION_ID,
  displayName: { "en-US": "Use reset card", "zh-CN": "使用重置卡" },
  description: {
    "en-US": "Redeem one available Codex reset card.",
    "zh-CN": "消耗一张可用的 Codex 重置卡。",
  },
  target: "card",
  destructive: true,
  run: consumeResetCard,
};

export function presentAccount(resource: ResourceSnapshot): ResourceView {
  const data = accountData(resource);
  const metrics: ResourceMetric[] = [];
  const weekly = data.quota?.weekly;
  if (weekly && weekly.remainingPercent !== null) {
    metrics.push({
      id: "weekly",
      label: { "en-US": "Weekly quota", "zh-CN": "周额度" },
      unit: "percent",
      value: weekly.remainingPercent,
      ...(weekly.resetAtMs !== null ? { resetAtMs: weekly.resetAtMs } : {}),
    });
  }
  const fiveHour = data.quota?.fiveHour;
  if (fiveHour && fiveHour.remainingPercent !== null) {
    metrics.push({
      id: "five-hour",
      label: { "en-US": "5-hour window", "zh-CN": "5 小时窗口" },
      unit: "percent",
      value: fiveHour.remainingPercent,
      ...(fiveHour.resetAtMs !== null ? { resetAtMs: fiveHour.resetAtMs } : {}),
    });
  }
  const resetCreditsAvailable = data.quota?.resetCreditsAvailable;
  if (resetCreditsAvailable !== null && resetCreditsAvailable !== undefined) {
    metrics.push({
      id: "reset-credits",
      label: { "en-US": "Reset cards", "zh-CN": "重置卡" },
      unit: "count",
      value: resetCreditsAvailable,
      ...(resetCreditsAvailable > 0 &&
          typeof data.quota?.resetCreditsExpiresAtMs === "number" &&
          data.quota.resetCreditsExpiresAtMs > Date.now()
        ? { expiresAtMs: data.quota.resetCreditsExpiresAtMs }
        : {}),
    });
  }
  return {
    // 旧记录可能存的是账号 ID;展示时优先从 token 现算邮箱。
    displayName: jwtDisplayName(data.accessToken) ?? data.displayName,
    ...(data.quota?.planLabel ? { description: data.quota.planLabel } : {}),
    ...(metrics.length > 0 ? { metrics } : {}),
  };
}

export async function refreshAccount(
  resource: ResourceSnapshot,
  context: PluginContext,
): Promise<ResourcePatch> {
  const data = accountData(resource);
  const response = await context.network.fetch(USAGE_URL, {
    method: "GET",
    headers: accountHeaders(data),
  });
  if (response.status < 200 || response.status >= 300) {
    if (response.status === 401) {
      return {
        state: { status: "invalid", message: "ChatGPT authorization expired; sign in again" },
      };
    }
    throw new Error(`Codex usage lookup failed (HTTP ${response.status}): ${response.body}`);
  }
  let body: unknown;
  try {
    body = JSON.parse(response.body);
  } catch {
    throw new Error("Codex usage lookup returned invalid JSON");
  }
  const quota = parseCodexUsage(body);
  try {
    const details = await fetchResetCredits(data, context);
    quota.resetCreditsAvailable = details.availableCount ?? quota.resetCreditsAvailable;
    const nowMs = Date.now();
    const expiries = details.cards.flatMap((card) =>
      card.status === "available" && card.expiresAtMs !== undefined && card.expiresAtMs > nowMs
        ? [card.expiresAtMs]
        : []
    );
    if (quota.resetCreditsAvailable !== 0 && expiries.length > 0) {
      quota.resetCreditsExpiresAtMs = Math.min(...expiries);
    }
  } catch {
    // Usage remains valid even if optional card details fail. The freshly parsed quota
    // intentionally keeps expiry unknown rather than claiming old details are current.
  }
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

function jwtDisplayName(token: string | null): string | null {
  if (!token) return null;
  const payload = decodeJwtPayload(token);
  return claim(payload, "email") ?? profileEmail(payload) ??
    claim(payload, "preferred_username") ?? claim(payload, "name");
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
    firstText(item, ["access_token", "accessToken", "token", "key", "OPENAI_API_KEY"]);
  if (!accessToken) return;
  const refreshToken = firstText(tokens, ["refresh_token", "refreshToken"]) ??
    firstText(item, ["refresh_token", "refreshToken"]);
  const accountId = firstText(tokens, ["account_id", "accountId", "chatgpt_account_id"]) ??
    firstText(item, ["account_id", "accountId", "chatgpt_account_id"]);
  const idToken = firstText(tokens, ["id_token", "idToken"]) ??
    firstText(item, ["id_token", "idToken"]);
  const displayName = firstText(item, ["email", "display_name", "displayName", "name"]) ??
    firstText(tokens, ["email", "display_name", "displayName", "name"]) ??
    jwtDisplayName(idToken);
  output.push({ accessToken, refreshToken, ...(accountId ? { accountId } : {}), displayName });
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
      warnings.push(`${file.name}: no ChatGPT access token found`);
      continue;
    }
    credentials.push(...found);
  }
  return { credentials, warnings };
}

export const credentialImport: ResourceImportSupport = {
  displayName: {
    "en-US": "Import Codex credentials",
    "zh-CN": "导入 Codex 凭证",
  },
  description: {
    "en-US": "Import one or more Codex JSON credential files.",
    "zh-CN": "导入一个或多个 Codex JSON 凭证文件。",
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
