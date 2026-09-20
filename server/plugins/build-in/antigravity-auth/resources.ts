import type { JsonValue, NetworkRequestInit, PluginContext } from "cursor-byok:plugin";
import type {
  ResourceDraft,
  ResourceImportFile,
  ResourceImportResult,
  ResourceImportSupport,
  ResourceMetric,
  ResourcePatch,
  ResourceSnapshot,
  ResourceView,
} from "cursor-byok:resource";
import {
  ANTIGRAVITY_ENDPOINTS,
  ANTIGRAVITY_OAUTH_USER_AGENT,
  ANTIGRAVITY_SANDBOX_ENDPOINT,
  antigravityRequestHeaders,
} from "./models.ts";
import { CLIENT_ID, CLIENT_SECRET } from "./google_oauth.ts";

export const RESOURCE_TYPE = "antigravity-account";

const REFRESH_TOKEN_URL = "https://oauth2.googleapis.com/token";

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

type QuotaWindow = "5h" | "weekly";

type QuotaBucket = {
  window: QuotaWindow;
  remainingPercent: number;
  resetAtMs: number | null;
};

type QuotaPoolId = "gemini" | "claude-gpt";
type QuotaPool = { id: QuotaPoolId; buckets: QuotaBucket[] };

type AccountQuota = {
  planLabel: string | null;
  pools: QuotaPool[];
  stale?: boolean;
};

export type AccountData = {
  accessToken: string;
  refreshToken: string | null;
  displayName: string;
  projectId?: string | null;
  expiresAtMs?: number | null;
  quota: AccountQuota | null;
};

type CredentialCandidate = {
  accessToken: string;
  refreshToken: string | null;
  displayName: string | null;
  projectId?: string | null;
  expiresAtMs?: number | null;
  quota?: AccountQuota | null;
};

async function fetchAccountProjectAndTier(
  accessToken: string,
  network: PluginContext["network"],
): Promise<{ projectId: string | null; planLabel: string | null }> {
  try {
    const response = await network.fetch(
      `${ANTIGRAVITY_SANDBOX_ENDPOINT}/v1internal:loadCodeAssist`,
      {
        method: "POST",
        headers: {
          authorization: `Bearer ${accessToken}`,
          "content-type": "application/json",
          "user-agent": ANTIGRAVITY_OAUTH_USER_AGENT,
        },
        body: JSON.stringify({ metadata: { ideType: "ANTIGRAVITY" } }),
      },
    );
    if (response.status >= 200 && response.status < 300) {
      const body = object(JSON.parse(response.body));
      if (body) {
        return {
          projectId: text(body.cloudaicompanionProject),
          planLabel: subscriptionTier(body),
        };
      }
    }
  } catch {
    // Model discovery can still succeed without a project ID.
  }
  return { projectId: null, planLabel: null };
}

function tierName(value: unknown): string | null {
  const tier = object(value);
  return text(tier?.name) ?? text(tier?.id);
}

function subscriptionTier(body: Record<string, unknown>): string | null {
  const paid = tierName(body.paidTier);
  if (paid) return paid;

  const ineligible = Array.isArray(body.ineligibleTiers) && body.ineligibleTiers.length > 0;
  if (!ineligible) return tierName(body.currentTier);

  const allowed = Array.isArray(body.allowedTiers) ? body.allowedTiers : [];
  const defaultTier = allowed.map(object).find((tier) => tier?.isDefault === true);
  const fallback = tierName(defaultTier);
  return fallback ? `${fallback} (Restricted)` : null;
}

const QUOTA_SUMMARY_PATH = "/v1internal:retrieveUserQuotaSummary";

function quotaWindow(bucket: Record<string, unknown>): QuotaWindow | null {
  const source = [
    text(bucket.window),
    text(bucket.bucketId),
    text(bucket.id),
    text(bucket.displayName),
    text(bucket.description),
  ].filter(Boolean).join(" ").toLowerCase();
  if (source.includes("week")) return "weekly";
  if (/\b5\s*(?:h|hour)/.test(source) || source.includes("five hour")) return "5h";
  return null;
}

function poolId(
  group: Record<string, unknown>,
  bucket: Record<string, unknown>,
): QuotaPoolId | null {
  // Use only explicit upstream identifiers and labels. Unknown groups remain unclassified.
  const source = [
    text(group.id),
    text(group.groupId),
    text(group.name),
    text(group.displayName),
    text(group.description),
    text(bucket.displayName),
    text(bucket.description),
    text(bucket.bucketId),
    text(bucket.id),
  ].filter(Boolean).join(" ").toLowerCase();
  if (source.includes("gemini")) return "gemini";
  if (source.includes("claude") || /\bgpt\b/.test(source) || source.includes("gpt-")) {
    return "claude-gpt";
  }
  return null;
}

function percent(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value)
    ? Math.round(Math.min(1, Math.max(0, value)) * 100)
    : null;
}

export function parseQuotaPools(payload: unknown): QuotaPool[] {
  const root = object(payload);
  const groups = Array.isArray(root?.groups) ? root.groups : [];
  const pools = new Map<QuotaPoolId, Map<QuotaWindow, QuotaBucket>>();
  const ambiguous = new Set<string>();
  for (const rawGroup of groups) {
    const group = object(rawGroup);
    if (!group || !Array.isArray(group.buckets)) continue;
    for (const rawBucket of group.buckets) {
      const bucket = object(rawBucket);
      if (!bucket) continue;
      const id = poolId(group, bucket);
      const window = quotaWindow(bucket);
      const remainingPercent = percent(bucket.remainingFraction);
      if (!id || !window || remainingPercent === null) continue;
      const reset = text(bucket.resetTime);
      const resetAtMs = reset && Number.isFinite(Date.parse(reset)) ? Date.parse(reset) : null;
      const entries = pools.get(id) ?? new Map<QuotaWindow, QuotaBucket>();
      const key = `${id}:${window}`;
      const previous = entries.get(window);
      if (ambiguous.has(key)) continue;
      if (
        previous &&
        (previous.remainingPercent !== remainingPercent || previous.resetAtMs !== resetAtMs)
      ) {
        entries.delete(window);
        ambiguous.add(key);
      } else {
        entries.set(window, { window, remainingPercent, resetAtMs });
      }
      pools.set(id, entries);
    }
  }
  return [...pools.entries()].map(([id, buckets]) => ({
    id,
    buckets: (["5h", "weekly"] as QuotaWindow[]).flatMap((window) => {
      const bucket = buckets.get(window);
      return bucket ? [bucket] : [];
    }),
  }));
}

function mergeQuotaPools(current: QuotaPool[], previous: QuotaPool[] | undefined): {
  pools: QuotaPool[];
  stale: boolean;
} {
  const merged = new Map<QuotaPoolId, Map<QuotaWindow, QuotaBucket>>();
  for (const pool of current) {
    merged.set(pool.id, new Map(pool.buckets.map((bucket) => [bucket.window, bucket])));
  }
  let stale = false;
  for (const pool of previous ?? []) {
    const entries = merged.get(pool.id) ?? new Map<QuotaWindow, QuotaBucket>();
    for (const bucket of pool.buckets) {
      if (!entries.has(bucket.window)) {
        entries.set(bucket.window, bucket);
        stale = true;
      }
    }
    if (entries.size > 0) merged.set(pool.id, entries);
  }
  return {
    pools: (["gemini", "claude-gpt"] as QuotaPoolId[]).flatMap((id) => {
      const buckets = merged.get(id);
      return buckets ? [{ id, buckets: [...buckets.values()] }] : [];
    }),
    stale,
  };
}

async function fetchQuotaSummary(
  accessToken: string,
  projectId: string | null,
  network: PluginContext["network"],
): Promise<QuotaPool[] | null> {
  for (const endpoint of ANTIGRAVITY_ENDPOINTS) {
    try {
      const response = await network.fetch(`${endpoint}${QUOTA_SUMMARY_PATH}`, {
        method: "POST",
        headers: antigravityRequestHeaders(accessToken),
        body: projectId ? JSON.stringify({ project: projectId }) : JSON.stringify({}),
      });
      if (response.status < 200 || response.status >= 300) {
        if (response.status >= 400 && response.status < 500 && response.status !== 429) return null;
        continue;
      }
      return parseQuotaPools(JSON.parse(response.body));
    } catch {
      // Continue with the next endpoint.
    }
  }
  return null;
}

export async function queryAccountQuota(
  accessToken: string,
  network: PluginContext["network"],
  previous: AccountQuota | null = null,
): Promise<{ quota: AccountQuota; projectId: string | null }> {
  const { projectId, planLabel } = await fetchAccountProjectAndTier(accessToken, network);
  const summary = await fetchQuotaSummary(accessToken, projectId, network);
  const merged = mergeQuotaPools(summary ?? [], previous?.pools);

  return {
    projectId,
    quota: {
      planLabel,
      pools: merged.pools,
      ...(summary === null || merged.stale ? { stale: true } : {}),
    },
  };
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

function isJwtExpired(token: string, bufferSeconds = 300): boolean {
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

async function accountIdentity(
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
    projectId: credential.projectId ?? null,
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
    projectId: text(data?.projectId),
    expiresAtMs: typeof data?.expiresAtMs === "number" ? data.expiresAtMs : null,
    quota: (data?.quota ?? null) as AccountQuota | null,
  };
}

export function presentAccount(resource: ResourceSnapshot): ResourceView {
  const data = accountData(resource);
  const labels: Record<QuotaPoolId, { "en-US": string; "zh-CN": string }> = {
    gemini: { "en-US": "Gemini", "zh-CN": "Gemini" },
    "claude-gpt": { "en-US": "Claude / GPT", "zh-CN": "Claude / GPT" },
  };
  const metrics: ResourceMetric[] = (data.quota?.pools ?? []).flatMap((pool) =>
    pool.buckets.map((bucket) => ({
      id: `pool:${pool.id}:${bucket.window}`,
      label: {
        "en-US": `${labels[pool.id]["en-US"]} · ${bucket.window === "5h" ? "5-hour" : "Weekly"}`,
        "zh-CN": `${labels[pool.id]["zh-CN"]} · ${bucket.window === "5h" ? "5 小时" : "每周"}`,
      },
      unit: "percent" as const,
      value: bucket.remainingPercent,
      ...(bucket.resetAtMs ? { resetAtMs: bucket.resetAtMs } : {}),
    }))
  );
  return {
    displayName: data.displayName,
    ...(data.quota?.stale
      ? {
        description: {
          "en-US": `${
            data.quota.planLabel ?? "Antigravity"
          } · Quota refresh was incomplete; showing last known values`,
          "zh-CN": `${data.quota.planLabel ?? "Antigravity"} · 配额刷新不完整，显示上次已知值`,
        },
      }
      : data.quota?.planLabel
      ? { description: data.quota.planLabel }
      : {}),
    ...(metrics.length > 0 ? { metrics } : {}),
  };
}

export async function refreshAccount(
  resource: ResourceSnapshot,
  context: PluginContext,
): Promise<ResourcePatch> {
  const data = accountData(resource);
  let accessToken = data.accessToken;
  let refreshToken = data.refreshToken;
  let projectId = data.projectId ?? null;
  let expiresAtMs = data.expiresAtMs ?? null;

  if (refreshToken) {
    const response = await context.network.fetch(REFRESH_TOKEN_URL, {
      method: "POST",
      headers: {
        accept: "application/json",
        "content-type": "application/x-www-form-urlencoded",
        "user-agent": ANTIGRAVITY_OAUTH_USER_AGENT,
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
  const result = await queryAccountQuota(accessToken, context.network, data.quota);
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
