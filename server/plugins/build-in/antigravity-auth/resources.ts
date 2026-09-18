import type { JsonValue, NetworkRequestInit, PluginContext } from "cursor-byok:plugin";
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
import {
  ANTIGRAVITY_CLIENT_HEADERS,
  ANTIGRAVITY_ENDPOINTS,
  ANTIGRAVITY_USER_AGENT,
} from "./models.ts";
import { CLIENT_ID, CLIENT_SECRET } from "./google_oauth.ts";

export const RESOURCE_TYPE = "antigravity-account";

const REFRESH_TOKEN_URL = "https://oauth2.googleapis.com/token";
const RETRIEVE_QUOTA_SUMMARY_PATH = "/v1internal:retrieveUserQuotaSummary";

/** 已知通用配额桶的展示名;未知桶回退上游 displayName 或 bucketId。 */
const QUOTA_BUCKET_LABELS: Record<string, string> = {
  "gemini-5h": "Gemini (5h)",
  "gemini-weekly": "Gemini (Weekly)",
  "3p-5h": "Claude (5h)",
  "3p-weekly": "Claude (Weekly)",
};

const QUOTA_BUCKET_ORDER = ["gemini-5h", "gemini-weekly", "3p-5h", "3p-weekly"];

async function fetchText(
  network: PluginContext["network"] | undefined,
  url: string,
  init: NetworkRequestInit,
): Promise<{ status: number; body: string }> {
  if (network) {
    const response = await network.fetch(url, init);
    return { status: response.status, body: response.body };
  }
  const response = await fetch(url, init);
  return { status: response.status, body: await response.text() };
}

export type QuotaMetric = {
  remainingPercent: number;
  resetAtMs: number | null;
};

/** retrieveUserQuotaSummary 返回的通用配额桶(5h / Weekly)。 */
export type QuotaBucket = {
  remainingPercent: number;
  resetAtMs: number | null;
  /** 上游 displayName;已知桶由固定映射提供展示名。 */
  label: string | null;
};

export type AccountQuota = {
  planLabel: string | null;
  limitReached: boolean;
  coolingUntilMs: number | null;
  updatedAtMs: number;
  /** 通用配额桶(bucketId -> 桶);缺失时展示回退 claude/gemini。 */
  buckets?: Record<string, QuotaBucket> | null;
  claude?: QuotaMetric | null;
  gemini?: QuotaMetric | null;
};

export type AccountData = {
  accessToken: string;
  refreshToken: string | null;
  displayName: string;
  projectId?: string | null;
  expiresAtMs?: number | null;
  quota: AccountQuota | null;
};

export type CredentialCandidate = {
  accessToken: string;
  refreshToken: string | null;
  displayName: string | null;
  projectId?: string | null;
  expiresAtMs?: number | null;
  quota?: AccountQuota | null;
};

export async function fetchAccountProjectAndTier(
  accessToken: string,
  network: PluginContext["network"],
): Promise<{ projectId: string; planLabel: string }> {
  for (const endpoint of ANTIGRAVITY_ENDPOINTS) {
    try {
      const assistRes = await network.fetch(`${endpoint}/v1internal:loadCodeAssist`, {
        method: "POST",
        headers: {
          authorization: `Bearer ${accessToken}`,
          "content-type": "application/json",
          "user-agent": ANTIGRAVITY_USER_AGENT,
          ...ANTIGRAVITY_CLIENT_HEADERS,
        },
        body: JSON.stringify({ metadata: { ideType: "ANTIGRAVITY" } }),
      });
      if (assistRes.status >= 200 && assistRes.status < 300) {
        const body = object(JSON.parse(assistRes.body));
        const project = text(body?.cloudaicompanionProject);
        const paid = object(body?.paidTier);
        const current = object(body?.currentTier);
        const tierName = text(paid?.name) ?? text(paid?.id) ?? text(current?.name) ??
          text(current?.id);
        let planLabel = "FREE";
        if (tierName) {
          const lower = tierName.toLowerCase();
          if (lower.includes("ultra")) planLabel = "ULTRA";
          else if (
            lower.includes("pro") || lower.includes("premium") || lower.includes("advanced")
          ) planLabel = "PRO";
        }
        return { projectId: project ?? "bamboo-precept-lgxtn", planLabel };
      }
    } catch {
      // Continue next endpoint
    }
  }
  return { projectId: "bamboo-precept-lgxtn", planLabel: "FREE" };
}

export async function queryAccountQuota(
  accessToken: string,
  network: PluginContext["network"],
): Promise<{ quota: AccountQuota | null; projectId: string }> {
  const { projectId, planLabel } = await fetchAccountProjectAndTier(accessToken, network);

  // 优先获取官方 IDE 口径的通用配额桶(5h / Weekly);失败时回退模型级推导。
  const buckets = await fetchQuotaSummaryBuckets(accessToken, projectId, network);
  const modelQuota = buckets ? null : await fetchModelQuota(accessToken, projectId, network);

  return {
    projectId,
    quota: {
      planLabel,
      limitReached: false,
      coolingUntilMs: null,
      updatedAtMs: Date.now(),
      buckets,
      claude: modelQuota?.claude ?? null,
      gemini: modelQuota?.gemini ?? null,
    },
  };
}

/** 调用 v1internal:retrieveUserQuotaSummary 获取通用配额桶(daily 优先,与官方 IDE 一致)。 */
async function fetchQuotaSummaryBuckets(
  accessToken: string,
  projectId: string,
  network: PluginContext["network"],
): Promise<Record<string, QuotaBucket> | null> {
  for (const endpoint of ANTIGRAVITY_ENDPOINTS) {
    try {
      const response = await network.fetch(`${endpoint}${RETRIEVE_QUOTA_SUMMARY_PATH}`, {
        method: "POST",
        headers: {
          authorization: `Bearer ${accessToken}`,
          "content-type": "application/json",
          accept: "application/json",
          "user-agent": ANTIGRAVITY_USER_AGENT,
          ...ANTIGRAVITY_CLIENT_HEADERS,
        },
        body: JSON.stringify({ project: projectId }),
      });
      if (response.status < 200 || response.status >= 300) continue;
      const root = object(JSON.parse(response.body));
      const groups = Array.isArray(root?.groups) ? root.groups : [];
      const buckets: Record<string, QuotaBucket> = {};
      for (const group of groups) {
        const list = object(group)?.buckets;
        if (!Array.isArray(list)) continue;
        for (const raw of list) {
          const bucket = object(raw);
          const bucketId = text(bucket?.bucketId);
          const fraction = typeof bucket?.remainingFraction === "number"
            ? bucket.remainingFraction
            : null;
          if (!bucketId || fraction === null) continue;
          const resetTime = text(bucket?.resetTime);
          const parsedReset = resetTime ? Date.parse(resetTime) : NaN;
          buckets[bucketId] = {
            remainingPercent: Math.max(0, Math.min(100, Math.round(fraction * 100))),
            resetAtMs: Number.isFinite(parsedReset) ? parsedReset : null,
            label: text(bucket?.displayName),
          };
        }
      }
      if (Object.keys(buckets).length > 0) return buckets;
    } catch {
      // Continue next endpoint
    }
  }
  return null;
}

/** 从 fetchAvailableModels 的模型级 quotaInfo 推导 Claude / Gemini 剩余(回退路径)。 */
async function fetchModelQuota(
  accessToken: string,
  projectId: string,
  network: PluginContext["network"],
): Promise<{ claude: QuotaMetric | null; gemini: QuotaMetric | null } | null> {
  for (const endpoint of ANTIGRAVITY_ENDPOINTS) {
    try {
      const response = await network.fetch(`${endpoint}/v1internal:fetchAvailableModels`, {
        method: "POST",
        headers: {
          authorization: `Bearer ${accessToken}`,
          "content-type": "application/json",
          accept: "application/json",
          "user-agent": ANTIGRAVITY_USER_AGENT,
          ...ANTIGRAVITY_CLIENT_HEADERS,
        },
        body: JSON.stringify({ project: projectId }),
      });
      if (response.status < 200 || response.status >= 300) continue;
      const root = object(JSON.parse(response.body));
      const models = object(root?.models);
      if (!models) continue;

      let claudeFraction: number | null = null;
      let claudeResetAtMs: number | null = null;
      let geminiFraction: number | null = null;
      let geminiResetAtMs: number | null = null;

      for (const [key, value] of Object.entries(models)) {
        const info = object(value);
        const quota = object(info?.quotaInfo);
        const fraction = typeof quota?.remainingFraction === "number"
          ? quota.remainingFraction
          : null;
        const resetTime = text(quota?.resetTime);
        const resetAtMs = resetTime ? Date.parse(resetTime) : null;
        if (fraction === null) continue;

        const k = key.toLowerCase();
        if (k.includes("claude") || k.includes("sonnet") || k.includes("opus")) {
          if (claudeFraction === null || fraction < claudeFraction) {
            claudeFraction = fraction;
            claudeResetAtMs = resetAtMs;
          }
        } else if (k.includes("gemini") || k.includes("flash") || k.includes("pro")) {
          if (geminiFraction === null || fraction < geminiFraction) {
            geminiFraction = fraction;
            geminiResetAtMs = resetAtMs;
          }
        }
      }

      return {
        claude: claudeFraction !== null
          ? { remainingPercent: Math.round(claudeFraction * 100), resetAtMs: claudeResetAtMs }
          : null,
        gemini: geminiFraction !== null
          ? { remainingPercent: Math.round(geminiFraction * 100), resetAtMs: geminiResetAtMs }
          : null,
      };
    } catch {
      // Continue next endpoint
    }
  }
  return null;
}

function object(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function text(value: unknown): string | null {
  return typeof value === "string" && value.trim() ? value.trim() : null;
}

function decodeJwtPayload(token: string): Record<string, unknown> | null {
  const parts = token.split(".");
  if (parts.length < 2) return null;
  try {
    const normalized = parts[1].replace(/-/g, "+").replace(/_/g, "/");
    const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "=");
    const bytes = Uint8Array.from(atob(padded), (char) => char.charCodeAt(0));
    return object(JSON.parse(new TextDecoder().decode(bytes)));
  } catch {
    return null;
  }
}

function claim(payload: Record<string, unknown> | null, key: string): string | null {
  return payload ? text(payload[key]) : null;
}

export function isJwtExpired(token: string, bufferSeconds = 300): boolean {
  if (token.startsWith("AIza") || !token.includes(".")) return false;
  const payload = decodeJwtPayload(token);
  if (!payload) return false;
  const exp = typeof payload.exp === "number" ? payload.exp : null;
  if (!exp) return false;
  const nowSeconds = Math.floor(Date.now() / 1000);
  return exp <= (nowSeconds + bufferSeconds);
}

export function isTokenExpired(data: AccountData, bufferSeconds = 300): boolean {
  if (!data.refreshToken) return false;
  if (typeof data.expiresAtMs === "number" && data.expiresAtMs > 0) {
    return Date.now() >= data.expiresAtMs - bufferSeconds * 1000;
  }
  return isJwtExpired(data.accessToken, bufferSeconds);
}

async function tokenFingerprint(token: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(token));
  return Array.from(
    new Uint8Array(digest).slice(0, 8),
    (byte) => byte.toString(16).padStart(2, "0"),
  ).join("");
}

export async function accountIdentity(
  token: string,
  providedDisplayName?: string | null,
): Promise<{ key: string; displayName: string }> {
  const payload = decodeJwtPayload(token);
  const email = claim(payload, "email");
  const sub = claim(payload, "sub");
  const name = claim(payload, "name") ?? claim(payload, "preferred_username");

  const fingerprint = await tokenFingerprint(token);
  const identity = (providedDisplayName && !providedDisplayName.includes("Antigravity"))
    ? providedDisplayName
    : (email ?? sub ?? fingerprint);
  const displayName = providedDisplayName ?? email ?? name ??
    (token.startsWith("AIza") ? `API Key (${fingerprint.slice(0, 6)})` : identity);
  return { key: `antigravity:${identity}`, displayName };
}

export async function credentialDraft(credential: CredentialCandidate): Promise<ResourceDraft> {
  const identity = await accountIdentity(credential.accessToken, credential.displayName);
  const data: AccountData = {
    accessToken: credential.accessToken,
    refreshToken: credential.refreshToken,
    displayName: credential.displayName ?? identity.displayName,
    projectId: credential.projectId ?? "bamboo-precept-lgxtn",
    expiresAtMs: credential.expiresAtMs ??
      (credential.refreshToken ? Date.now() + 3500 * 1000 : null),
    quota: credential.quota ?? null,
  };
  return { key: identity.key, privateData: data as unknown as JsonValue };
}

export function accountData(resource: ResourceSnapshot): AccountData {
  const data = object(resource.privateData);
  const accessToken = text(data?.accessToken);
  if (!accessToken) throw new Error("Antigravity account resource is missing its access token");
  return {
    accessToken,
    refreshToken: text(data?.refreshToken),
    displayName: text(data?.displayName) ?? "Antigravity account",
    projectId: text(data?.projectId) ?? "bamboo-precept-lgxtn",
    expiresAtMs: typeof data?.expiresAtMs === "number" ? data.expiresAtMs : null,
    quota: (data?.quota ?? null) as AccountQuota | null,
  };
}

export function accountHeaders(data: AccountData): Record<string, string> {
  return {
    authorization: `Bearer ${data.accessToken}`,
    accept: "application/json",
    "user-agent": ANTIGRAVITY_USER_AGENT,
    ...ANTIGRAVITY_CLIENT_HEADERS,
  };
}

export function quotaState(quota: AccountQuota | null, nowMs = Date.now()): ResourceState {
  if (!quota || !quota.limitReached) return { status: "ready" };
  const coolingUntil = quota.coolingUntilMs;
  if (coolingUntil !== null && coolingUntil > nowMs) {
    return {
      status: "cooling",
      retryAtMs: coolingUntil,
      message: "Antigravity rate limit reached; cooling down",
    };
  }
  return { status: "ready" };
}

export function quotaExhaustedPatch(
  data: AccountData,
  error?: string,
  nowMs = Date.now(),
): ResourcePatch {
  const retryAfterMs = parseQuotaRetryDelayMs(error, nowMs) ?? 60 * 1000;
  const coolingUntilMs = nowMs + Math.max(5000, retryAfterMs);
  const quota: AccountQuota = {
    planLabel: data.quota?.planLabel ?? "Antigravity / Gemini",
    limitReached: true,
    coolingUntilMs,
    updatedAtMs: nowMs,
    buckets: data.quota?.buckets ?? null,
    claude: data.quota?.claude ?? null,
    gemini: data.quota?.gemini ?? null,
  };
  return {
    privateData: { ...data, quota } as unknown as JsonValue,
    state: quotaState(quota, nowMs),
  };
}

/** 从上游错误文本解析冷却时长(毫秒);无法解析时返回 null。 */
function parseQuotaRetryDelayMs(error: string | undefined, nowMs: number): number | null {
  if (!error) return null;

  // "retry_after: 54" / "retry after 54" / "Please retry in 54.9s"
  const retryMatch = error.match(
    /retry(?:_after|\s+after|\s+in)?\s*[:=]?\s*(\d+(?:\.\d+)?)\s*(ms|s|m|h)?\b/i,
  );
  if (retryMatch?.[1]) {
    const value = Number(retryMatch[1]);
    if (Number.isFinite(value) && value > 0) {
      // 超大数值视为绝对时间戳(毫秒)
      if (value > 10_000_000) return Math.round(value - nowMs);
      return Math.round(value * durationUnitMs(retryMatch[2]));
    }
  }

  // Google RetryInfo: "retryDelay": "54s"
  const delayMatch = error.match(/retrydelay\D{0,4}(\d+(?:\.\d+)?)\s*(ms|s|m|h)?/i);
  if (delayMatch?.[1]) {
    const value = Number(delayMatch[1]);
    if (Number.isFinite(value) && value > 0) {
      return Math.round(value * durationUnitMs(delayMatch[2]));
    }
  }

  // "quota will reset after 4h54m36s"
  const compoundMatch = error.match(
    /(?:reset\s+after|resets?\s+in)\s+(?:(\d+(?:\.\d+)?)\s*h)?\s*(?:(\d+(?:\.\d+)?)\s*m)?\s*(?:(\d+(?:\.\d+)?)\s*s)?/i,
  );
  if (compoundMatch && (compoundMatch[1] || compoundMatch[2] || compoundMatch[3])) {
    const seconds = Number(compoundMatch[1] ?? 0) * 3600 +
      Number(compoundMatch[2] ?? 0) * 60 +
      Number(compoundMatch[3] ?? 0);
    if (seconds > 0) return Math.round(seconds * 1000);
  }

  return null;
}

function durationUnitMs(unit: string | undefined): number {
  switch ((unit ?? "s").toLowerCase()) {
    case "ms":
      return 1;
    case "m":
      return 60_000;
    case "h":
      return 3_600_000;
    default:
      return 1_000;
  }
}

export function presentAccount(resource: ResourceSnapshot): ResourceView {
  const data = accountData(resource);
  const metrics = accountMetrics(data.quota);
  return {
    displayName: data.displayName,
    ...(data.quota?.planLabel ? { description: data.quota.planLabel } : {}),
    ...(metrics.length > 0 ? { metrics } : {}),
  };
}

/** 通用配额桶优先展示;缺失时回退模型级推导的 Claude / Gemini。 */
function accountMetrics(quota: AccountQuota | null | undefined): ResourceMetric[] {
  const buckets = quota?.buckets ?? null;
  if (buckets && Object.keys(buckets).length > 0) {
    return orderedBucketEntries(buckets).map(([bucketId, bucket]) => {
      const label = QUOTA_BUCKET_LABELS[bucketId] ?? bucket.label ?? bucketId;
      return {
        id: bucketId,
        label: { "en-US": label, "zh-CN": label },
        unit: "percent" as const,
        value: bucket.remainingPercent,
        ...(bucket.resetAtMs ? { resetAtMs: bucket.resetAtMs } : {}),
      };
    });
  }
  const metrics: ResourceMetric[] = [];
  if (quota?.claude) {
    metrics.push({
      id: "claude",
      label: { "en-US": "Claude", "zh-CN": "Claude" },
      unit: "percent",
      value: quota.claude.remainingPercent,
      ...(quota.claude.resetAtMs ? { resetAtMs: quota.claude.resetAtMs } : {}),
    });
  }
  if (quota?.gemini) {
    metrics.push({
      id: "gemini",
      label: { "en-US": "Gemini", "zh-CN": "Gemini" },
      unit: "percent",
      value: quota.gemini.remainingPercent,
      ...(quota.gemini.resetAtMs ? { resetAtMs: quota.gemini.resetAtMs } : {}),
    });
  }
  return metrics;
}

function orderedBucketEntries(
  buckets: Record<string, QuotaBucket>,
): Array<[string, QuotaBucket]> {
  const known = QUOTA_BUCKET_ORDER
    .filter((bucketId) => bucketId in buckets)
    .map((bucketId): [string, QuotaBucket] => [bucketId, buckets[bucketId]]);
  const unknown = Object.entries(buckets)
    .filter(([bucketId]) => !QUOTA_BUCKET_ORDER.includes(bucketId))
    .sort(([left], [right]) => left.localeCompare(right));
  return [...known, ...unknown];
}

export async function refreshAccount(
  resource: ResourceSnapshot,
  context: PluginContext,
): Promise<ResourcePatch> {
  const data = accountData(resource);
  let accessToken = data.accessToken;
  let refreshToken = data.refreshToken;
  let projectId = data.projectId ?? "bamboo-precept-lgxtn";
  let expiresAtMs = data.expiresAtMs ?? null;

  if (refreshToken) {
    const response = await context.network.fetch(REFRESH_TOKEN_URL, {
      method: "POST",
      headers: {
        accept: "application/json",
        "content-type": "application/x-www-form-urlencoded",
      },
      body: new URLSearchParams({
        client_id: CLIENT_ID,
        client_secret: CLIENT_SECRET,
        grant_type: "refresh_token",
        refresh_token: refreshToken,
      }).toString(),
    });

    if (response.status < 200 || response.status >= 300) {
      const bodyText = response.body.toLowerCase();
      // Only mark invalid if token is revoked or client is invalid
      if (bodyText.includes("invalid_grant") || bodyText.includes("unauthorized_client")) {
        return {
          state: {
            status: "invalid",
            message: "Google authorization expired or revoked; please sign in again",
          },
        };
      }
      // On network glitches or temporary Google server errors, keep ready
      return {
        state: { status: "ready" },
      };
    }

    const body = object(JSON.parse(response.body));
    accessToken = text(body?.access_token) ?? accessToken;
    refreshToken = text(body?.refresh_token) ?? refreshToken;
    const expiresIn = typeof body?.expires_in === "number" ? body.expires_in : 3600;
    expiresAtMs = Date.now() + expiresIn * 1000;
  }

  // Fetch real-time quota and project ID
  const result = await queryAccountQuota(accessToken, context.network);
  projectId = result.projectId || projectId;

  const updatedData: AccountData = {
    ...data,
    accessToken,
    refreshToken,
    projectId,
    expiresAtMs,
    quota: result.quota ?? data.quota,
  };
  return {
    privateData: updatedData as unknown as JsonValue,
    state: { status: "ready" },
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
  for (const key of ["accounts", "credentials", "items", "keys"]) {
    if (Array.isArray(item[key])) {
      collectCredentials(item[key], output);
      return;
    }
  }
  const tokens = object(item.tokens) ?? item;
  let accessToken = firstText(tokens, [
    "access",
    "accessToken",
    "access_token",
    "token",
    "apiKey",
    "api_key",
    "key",
    "GEMINI_API_KEY",
    "GOOGLE_API_KEY",
    "ANTIGRAVITY_API_KEY",
  ]) ?? firstText(item, [
    "access",
    "accessToken",
    "access_token",
    "token",
    "apiKey",
    "api_key",
    "key",
    "GEMINI_API_KEY",
    "GOOGLE_API_KEY",
    "ANTIGRAVITY_API_KEY",
  ]);
  const refreshToken = firstText(tokens, ["refresh", "refresh_token", "refreshToken"]) ??
    firstText(item, ["refresh", "refresh_token", "refreshToken"]);
  const displayName = firstText(item, ["email", "display_name", "displayName", "name"]) ??
    firstText(tokens, ["email", "display_name", "displayName", "name"]);
  const projectId =
    firstText(item, ["project", "projectId", "project_id", "cloudaicompanionProject"]) ??
      firstText(tokens, ["project", "projectId", "project_id", "cloudaicompanionProject"]);

  if (!accessToken && !refreshToken) return;
  if (!accessToken && refreshToken) {
    accessToken = refreshToken;
  }
  if (!accessToken) return;
  output.push({ accessToken, refreshToken, displayName, projectId });
}

export async function parseCredentialFiles(
  files: ResourceImportFile[],
  network?: PluginContext["network"],
): Promise<{
  credentials: CredentialCandidate[];
  warnings: string[];
}> {
  const credentials: CredentialCandidate[] = [];
  const warnings: string[] = [];
  for (const file of files) {
    const raw = file.content.trim();
    if (!raw) continue;

    // Check if file is raw API key or JWT token string
    if (raw.startsWith("AIza") || (raw.split(".").length === 3 && !raw.includes(" "))) {
      credentials.push({ accessToken: raw, refreshToken: null, displayName: file.name });
      continue;
    }

    // Try parsing as JSON
    let content: unknown;
    try {
      content = JSON.parse(raw);
    } catch {
      const envMatch = raw.match(
        /(?:API_KEY|TOKEN|GEMINI_API_KEY|GOOGLE_API_KEY|ANTIGRAVITY_API_KEY)\s*=\s*["']?([^"'\r\n]+)/i,
      );
      if (envMatch?.[1]) {
        credentials.push({
          accessToken: envMatch[1].trim(),
          refreshToken: null,
          displayName: file.name,
        });
        continue;
      }
      const keyMatch = raw.match(/AIza[0-9A-Za-z-_]{35}/);
      if (keyMatch?.[0]) {
        credentials.push({ accessToken: keyMatch[0], refreshToken: null, displayName: file.name });
        continue;
      }
      warnings.push(`${file.name}: not valid JSON or API key`);
      continue;
    }

    if (typeof content === "string") {
      credentials.push({ accessToken: content.trim(), refreshToken: null, displayName: file.name });
      continue;
    }

    const found: CredentialCandidate[] = [];
    collectCredentials(content, found);
    if (found.length === 0) {
      warnings.push(`${file.name}: no Google/Antigravity API key or token found`);
      continue;
    }
    for (const candidate of found) {
      if (candidate.refreshToken && candidate.accessToken === candidate.refreshToken) {
        try {
          const response = await fetchText(network, REFRESH_TOKEN_URL, {
            method: "POST",
            headers: {
              accept: "application/json",
              "content-type": "application/x-www-form-urlencoded",
            },
            body: new URLSearchParams({
              client_id: CLIENT_ID,
              client_secret: CLIENT_SECRET,
              grant_type: "refresh_token",
              refresh_token: candidate.refreshToken,
            }).toString(),
          });
          const body = object(JSON.parse(response.body));
          if (response.status >= 200 && response.status < 300 && text(body?.access_token)) {
            candidate.accessToken = text(body?.access_token)!;
            candidate.refreshToken = text(body?.refresh_token) ?? candidate.refreshToken;
            candidate.expiresAtMs = Date.now() +
              ((typeof body?.expires_in === "number" ? body.expires_in : 3600) * 1000);
          }
        } catch {
          // Keep placeholder
        }
      }
      credentials.push(candidate);
    }
  }
  return { credentials, warnings };
}

export const credentialImport: ResourceImportSupport = {
  displayName: {
    "en-US": "Import Google / Antigravity Credentials",
    "zh-CN": "导入 Google / Antigravity 凭证",
  },
  description: {
    "en-US":
      "Import a JSON, TXT, or environment file containing Antigravity tokens or Google API keys.",
    "zh-CN": "导入包含 Antigravity Token 或 Google API Key 的 JSON、TXT 或环境变量文件。",
  },
  accept: [".json", ".txt", ".key", ".env"],
  multiple: true,
  parse: async (
    files: ResourceImportFile[],
    context: PluginContext,
  ): Promise<ResourceImportResult> => {
    const { credentials, warnings } = await parseCredentialFiles(files, context.network);
    if (credentials.length === 0) {
      throw new Error(
        warnings.join("; ") ||
          "credential file does not contain a valid token or API key",
      );
    }
    const drafts = await Promise.all(
      credentials.map(async (c) => {
        try {
          const res = await queryAccountQuota(c.accessToken, context.network);
          c.quota = res.quota;
          c.projectId = res.projectId;
        } catch {
          // ignore error
        }
        return credentialDraft(c);
      }),
    );
    return {
      resources: drafts,
      ...(warnings.length > 0 ? { warnings } : {}),
    };
  },
};
