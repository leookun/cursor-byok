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

export const RESOURCE_TYPE = "codebuddy-cn-account";
export const BASE_URL = "https://copilot.tencent.com";
export const DEFAULT_DOMAIN = "copilot.tencent.com";
export const CLIENT_VERSION = "2.63.2";
export const USER_AGENT = `CLI/${CLIENT_VERSION} CodeBuddy/${CLIENT_VERSION}`;
export const AUTH_STATE_URL = `${BASE_URL}/v2/plugin/auth/state?platform=CLI`;
export const AUTH_TOKEN_URL = `${BASE_URL}/v2/plugin/auth/token`;
export const AUTH_REFRESH_URL = `${BASE_URL}/v2/plugin/auth/token/refresh`;
export const PROFILE_URL = `${BASE_URL}/v2/plugin/login/account`;
export const BILLING_BASE_URL = "https://www.codebuddy.cn";
export const QUOTA_PATH = "/v2/billing/meter/get-user-resource";
export const CHECKIN_STATUS_PATH = "/v2/billing/meter/checkin-activity-status";
export const CHECKIN_STATUS_FALLBACK_PATH = "/v2/billing/meter/checkin-status";
export const CHECKIN_PATH = "/v2/billing/meter/daily-checkin";
export const CONFIG_URL = `${BASE_URL}/v3/config`;

export type LastCheckin = {
  date: string | null;
  status: string | null;
  reward: number | null;
  balance: number | null;
  message: string | null;
};

/** One billable pool: the monthly subscription plus each promotional grant. */
export type QuotaWindow = {
  name: string;
  total: number;
  used: number;
  remaining: number;
  remainingPercent: number | null;
  resetAtMs: number | null;
};

export type AccountQuota = {
  plan: string | null;
  subscription: QuotaWindow | null;
  grants: QuotaWindow[];
  remaining: number;
  total: number;
  used: number;
  resetAtMs: number | null;
  updatedAtMs: number;
};

export type AccountData = {
  accessToken: string;
  refreshToken: string | null;
  expiresAtMs: number | null;
  refreshExpiresAtMs: number | null;
  uid: string | null;
  email: string | null;
  nickname: string | null;
  enterpriseName: string | null;
  enterpriseId: string | null;
  domain: string | null;
  quota: AccountQuota | null;
  lastCheckin: LastCheckin | null;
};

export type CredentialCandidate = {
  accessToken: string;
  refreshToken: string | null;
  expiresAtMs: number | null;
  refreshExpiresAtMs: number | null;
  uid: string | null;
  email: string | null;
  nickname: string | null;
  enterpriseName: string | null;
  enterpriseId: string | null;
  domain: string | null;
  quota?: AccountQuota | null;
  lastCheckin?: LastCheckin | null;
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

function firstText(source: Record<string, unknown>, keys: string[]): string | null {
  for (const key of keys) {
    const value = text(source[key]);
    if (value) return value;
  }
  return null;
}

function parseMilliseconds(value: unknown): number | null {
  return number(value);
}

function parseTimestamp(value: unknown): number | null {
  const numeric = number(value);
  if (numeric !== null) {
    if (numeric > 10_000_000_000) return numeric;
    if (numeric > 1_000_000_000) return numeric * 1000;
    return null;
  }
  if (typeof value === "string") {
    const parsed = Date.parse(value);
    if (Number.isFinite(parsed)) return parsed;
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

function claim(payload: Record<string, unknown> | null, ...keys: string[]): string | null {
  if (!payload) return null;
  for (const key of keys) {
    const value = text(payload[key]);
    if (value) return value;
  }
  return null;
}

async function tokenFingerprint(token: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(token));
  return Array.from(
    new Uint8Array(digest).slice(0, 8),
    (byte) => byte.toString(16).padStart(2, "0"),
  ).join("");
}

export function jwtUid(accessToken: string): string | null {
  return claim(decodeJwtPayload(accessToken), "uid", "user_id", "userId", "sub");
}

export function jwtEmail(accessToken: string): string | null {
  return claim(decodeJwtPayload(accessToken), "email", "preferred_username");
}

export function jwtNickname(accessToken: string): string | null {
  return claim(decodeJwtPayload(accessToken), "nickname", "name", "preferred_username");
}

export function jwtExpiry(accessToken: string): number | null {
  const exp = number(decodeJwtPayload(accessToken)?.exp);
  return exp === null ? null : exp * 1000;
}

export async function accountIdentity(
  accessToken: string,
  fields: Partial<Pick<AccountData, "uid" | "email" | "nickname">> = {},
): Promise<{ key: string; displayName: string }> {
  const payload = decodeJwtPayload(accessToken);
  const uid = fields.uid ?? claim(payload, "uid", "user_id", "userId", "sub");
  const email = fields.email ?? claim(payload, "email", "preferred_username");
  const nickname = fields.nickname ?? claim(payload, "nickname", "name", "preferred_username");
  const identity = uid ?? email ?? await tokenFingerprint(accessToken);
  return {
    key: `codebuddy:${identity}`,
    displayName: email ?? nickname ?? uid ?? "CodeBuddy account",
  };
}

export function isSupportedCodeBuddyDomain(value: unknown): boolean {
  const candidate = text(value);
  return candidate === null || candidate === "copilot.tencent.com" ||
    candidate === "www.codebuddy.cn";
}

export function normalizeCodeBuddyDomain(value: unknown): string {
  return text(value) === "www.codebuddy.cn" ? "www.codebuddy.cn" : DEFAULT_DOMAIN;
}

export function billingUrl(data: AccountData, path: string): string {
  const origin = normalizeCodeBuddyDomain(data.domain) === "www.codebuddy.cn"
    ? BILLING_BASE_URL
    : BASE_URL;
  return `${origin}${path}`;
}

function normalizeCandidate(credential: CredentialCandidate): CredentialCandidate {
  const payload = decodeJwtPayload(credential.accessToken);
  const domain = credential.domain ?? payload?.domain;
  if (!isSupportedCodeBuddyDomain(domain)) {
    throw new Error("CodeBuddy international account is not supported");
  }
  return {
    accessToken: credential.accessToken,
    refreshToken: credential.refreshToken,
    expiresAtMs: parseMilliseconds(credential.expiresAtMs) ?? parseTimestamp(payload?.exp) ??
      jwtExpiry(credential.accessToken),
    refreshExpiresAtMs: parseMilliseconds(credential.refreshExpiresAtMs),
    uid: credential.uid ?? claim(payload, "uid", "user_id", "userId", "sub"),
    email: credential.email ?? claim(payload, "email", "preferred_username"),
    nickname: credential.nickname ?? claim(payload, "nickname", "name", "preferred_username"),
    enterpriseName: credential.enterpriseName,
    enterpriseId: credential.enterpriseId,
    domain: normalizeCodeBuddyDomain(domain),
    quota: credential.quota ?? null,
    lastCheckin: credential.lastCheckin ?? null,
  };
}

export async function credentialDraft(credential: CredentialCandidate): Promise<ResourceDraft> {
  const normalized = normalizeCandidate(credential);
  const identity = await accountIdentity(normalized.accessToken, normalized);
  const data: AccountData = {
    accessToken: normalized.accessToken,
    refreshToken: normalized.refreshToken,
    expiresAtMs: normalized.expiresAtMs,
    refreshExpiresAtMs: normalized.refreshExpiresAtMs,
    uid: normalized.uid,
    email: normalized.email,
    nickname: normalized.nickname,
    enterpriseName: normalized.enterpriseName,
    enterpriseId: normalized.enterpriseId,
    domain: normalized.domain,
    quota: normalized.quota ?? null,
    lastCheckin: normalized.lastCheckin ?? null,
  };
  return { key: identity.key, privateData: data as unknown as JsonValue };
}

export function accountData(resource: ResourceSnapshot): AccountData {
  const data = object(resource.privateData);
  const accessToken = text(data?.accessToken) ?? text(data?.access_token) ?? text(data?.token);
  if (!accessToken) throw new Error("CodeBuddy account resource is missing its access token");
  const payload = decodeJwtPayload(accessToken);
  const quotaValue = data?.quota;
  const quota = quotaValue && object(quotaValue) ? quotaValue as unknown as AccountQuota : null;
  return {
    accessToken,
    refreshToken: text(data?.refreshToken) ?? text(data?.refresh_token),
    expiresAtMs: parseMilliseconds(data?.expiresAtMs) ?? parseTimestamp(data?.expiresAt) ??
      jwtExpiry(accessToken),
    refreshExpiresAtMs: parseMilliseconds(data?.refreshExpiresAtMs) ??
      parseTimestamp(data?.refreshExpiresAt),
    uid: text(data?.uid) ?? text(data?.userId) ?? text(data?.user_id) ??
      claim(payload, "uid", "user_id", "userId", "sub"),
    email: text(data?.email) ?? jwtEmail(accessToken),
    nickname: text(data?.nickname) ?? text(data?.name) ?? jwtNickname(accessToken),
    enterpriseName: text(data?.enterpriseName) ?? text(data?.enterprise_name),
    enterpriseId: text(data?.enterpriseId) ?? text(data?.enterprise_id) ??
      text(data?.entId) ?? text(data?.ent_id),
    domain: normalizeCodeBuddyDomain(data?.domain ?? payload?.domain),
    quota,
    lastCheckin: (data?.lastCheckin ?? null) as LastCheckin | null,
  };
}

export function accountHeaders(data: AccountData): Record<string, string> {
  const headers: Record<string, string> = {
    Accept: "application/json, text/plain, */*",
    "Content-Type": "application/json",
    "X-Requested-With": "XMLHttpRequest",
    Authorization: `Bearer ${data.accessToken}`,
    "X-Domain": normalizeCodeBuddyDomain(data.domain),
    "X-Product": "SaaS",
    "X-IDE-Type": "CLI",
    "X-IDE-Name": "CLI",
    "X-IDE-Version": CLIENT_VERSION,
    "X-Product-Version": CLIENT_VERSION,
    "X-Env-ID": "production",
    "User-Agent": USER_AGENT,
  };
  if (data.uid) headers["X-User-Id"] = data.uid;
  if (data.enterpriseId) {
    headers["X-Enterprise-Id"] = data.enterpriseId;
    headers["X-Tenant-Id"] = data.enterpriseId;
  }
  return headers;
}

export function oauthHeaders(): Record<string, string> {
  return {
    Accept: "application/json",
    "Content-Type": "application/json",
    "X-Requested-With": "XMLHttpRequest",
    "X-No-Authorization": "true",
    "X-No-User-Id": "true",
    "X-No-Enterprise-Id": "true",
    "X-No-Department-Info": "true",
    "X-Domain": DEFAULT_DOMAIN,
    "X-Product": "SaaS",
    "User-Agent": USER_AGENT,
  };
}

export function refreshHeaders(data: AccountData): Record<string, string> {
  const headers = accountHeaders(data);
  headers["X-Refresh-Token"] = data.refreshToken ?? "";
  headers["X-Auth-Refresh-Source"] = "plugin";
  return headers;
}

export function isAuthStatus(status: number): boolean {
  return status === 401 || status === 403;
}

function responseCode(body: unknown): number | null {
  const root = object(body) ?? {};
  const data = object(root.data) ?? root;
  const response = object(data.Response) ?? data;
  const responseData = object(response.Data) ?? response;
  return number(root.code) ?? number(data.code) ?? number(response.code) ??
    number(responseData.code);
}

export function isBusinessError(body: unknown): boolean {
  const code = responseCode(body);
  return code !== null && code !== 0 && code !== 200;
}

export function isAuthError(status: number, body?: unknown): boolean {
  if (isAuthStatus(status)) return true;
  const code = responseCode(body);
  return code === 401 || code === 403;
}

export function parseTokenData(
  body: unknown,
  previous?: Partial<AccountData>,
  nowMs = Date.now(),
): {
  accessToken: string;
  refreshToken: string | null;
  expiresAtMs: number | null;
  refreshExpiresAtMs: number | null;
  domain: string | null;
} {
  const root = object(body);
  const data = object(root?.data) ?? root ?? {};
  const accessToken = firstText(data, ["accessToken", "access_token", "token"]);
  if (!accessToken) throw new Error("CodeBuddy token response is missing accessToken");
  const expiresIn = number(data.expiresIn ?? data.expires_in);
  const refreshExpiresIn = number(data.refreshExpiresIn ?? data.refresh_expires_in);
  const explicitExpiry = data.expiresAtMs !== undefined
    ? parseMilliseconds(data.expiresAtMs)
    : parseTimestamp(data.expiresAt ?? data.expireAt);
  const explicitRefreshExpiry = data.refreshExpiresAtMs !== undefined
    ? parseMilliseconds(data.refreshExpiresAtMs)
    : parseTimestamp(data.refreshExpiresAt ?? data.refresh_expires_at);
  return {
    accessToken,
    refreshToken: firstText(data, ["refreshToken", "refresh_token"]) ?? previous?.refreshToken ??
      null,
    expiresAtMs: explicitExpiry ??
      (expiresIn === null ? previous?.expiresAtMs ?? null : nowMs + expiresIn * 1000),
    refreshExpiresAtMs: explicitRefreshExpiry ??
      (refreshExpiresIn === null
        ? previous?.refreshExpiresAtMs ?? null
        : nowMs + refreshExpiresIn * 1000),
    domain: firstText(data, ["domain"]) ?? previous?.domain ?? null,
  };
}

export class CodeBuddyHttpError extends Error {
  constructor(readonly status: number, readonly detail: string | null = null) {
    super(`CodeBuddy request failed (HTTP ${status}${detail ? `: ${detail}` : ""})`);
    this.name = "CodeBuddyHttpError";
  }
}

function errorDetail(body: unknown): string | null {
  const root = object(body) ?? {};
  const data = object(root.data) ?? root;
  const message = firstText(root, ["message", "msg", "error_description"]) ??
    firstText(data, ["message", "msg", "error_description"]);
  const code = responseCode(body);
  return message ?? (code === null ? null : `code ${code}`);
}

function requestError(status: number, body: unknown): CodeBuddyHttpError {
  return new CodeBuddyHttpError(status, errorDetail(body));
}

export async function refreshAccountToken(
  data: AccountData,
  context: PluginContext,
  nowMs = Date.now(),
): Promise<AccountData> {
  if (!data.refreshToken) return data;
  if (data.refreshExpiresAtMs !== null && data.refreshExpiresAtMs <= nowMs) {
    throw new CodeBuddyHttpError(401);
  }
  const response = await context.network.fetch(AUTH_REFRESH_URL, {
    method: "POST",
    headers: refreshHeaders(data),
    body: "{}",
    sensitive: true,
  });
  let body: unknown;
  try {
    body = JSON.parse(response.body);
  } catch {
    body = {};
  }
  if (
    response.status < 200 || response.status >= 300 || isAuthError(response.status, body) ||
    isBusinessError(body)
  ) {
    throw requestError(response.status, body);
  }
  const token = parseTokenData(body, data, nowMs);
  if (token.domain && !isSupportedCodeBuddyDomain(token.domain)) {
    throw new Error("CodeBuddy international account is not supported");
  }
  return {
    ...data,
    accessToken: token.accessToken,
    refreshToken: token.refreshToken,
    expiresAtMs: token.expiresAtMs,
    refreshExpiresAtMs: token.refreshExpiresAtMs,
    domain: normalizeCodeBuddyDomain(token.domain ?? data.domain),
  };
}

export function isTokenExpired(
  data: AccountData,
  nowMs = Date.now(),
  bufferMs = 5 * 60 * 1000,
): boolean {
  if (data.expiresAtMs !== null && data.expiresAtMs > 0) {
    return data.expiresAtMs <= nowMs + bufferMs;
  }
  const exp = number(decodeJwtPayload(data.accessToken)?.exp);
  return exp !== null && exp * 1000 <= nowMs + bufferMs;
}

export async function ensureFreshAccount(
  data: AccountData,
  context: PluginContext,
  nowMs = Date.now(),
): Promise<{ data: AccountData; refreshed: boolean }> {
  if (!isTokenExpired(data, nowMs) || !data.refreshToken) return { data, refreshed: false };
  return { data: await refreshAccountToken(data, context, nowMs), refreshed: true };
}

export function parseAccountProfile(body: unknown): Partial<AccountData> {
  const root = object(body) ?? {};
  const data = object(root.data) ?? root;
  const account = object(data.account) ?? object(data.user) ?? object(data.profile) ??
    object(data.userInfo) ?? object(data.result) ?? data;
  const domain = firstText(account, ["domain"]);
  if (domain && !isSupportedCodeBuddyDomain(domain)) {
    throw new Error("CodeBuddy international account is not supported");
  }
  const result: Partial<AccountData> = {};
  const uid = firstText(account, ["uid", "userId", "user_id", "id"]);
  const email = firstText(account, ["email", "mail"]);
  const nickname = firstText(account, ["nickname", "name", "displayName", "display_name"]);
  const enterpriseName = firstText(account, ["enterpriseName", "enterprise_name", "companyName"]);
  const enterpriseId = firstText(account, ["enterpriseId", "enterprise_id", "entId", "ent_id"]);
  if (uid) result.uid = uid;
  if (email) result.email = email;
  if (nickname) result.nickname = nickname;
  if (enterpriseName) result.enterpriseName = enterpriseName;
  if (enterpriseId) result.enterpriseId = enterpriseId;
  return result;
}

export async function fetchAccountProfile(
  state: string,
  data: AccountData,
  context: PluginContext,
): Promise<Partial<AccountData>> {
  const response = await context.network.fetch(
    `${PROFILE_URL}?state=${encodeURIComponent(state)}`,
    {
      method: "GET",
      headers: {
        Authorization: `Bearer ${data.accessToken}`,
        "X-Domain": normalizeCodeBuddyDomain(data.domain),
        Accept: "application/json, text/plain, */*",
        "X-Requested-With": "XMLHttpRequest",
        "User-Agent": USER_AGENT,
      },
    },
  );
  let body: unknown;
  try {
    body = JSON.parse(response.body);
  } catch {
    body = {};
  }
  if (
    response.status < 200 || response.status >= 300 || isAuthError(response.status, body) ||
    isBusinessError(body)
  ) {
    throw requestError(response.status, body);
  }
  return parseAccountProfile(body);
}

function looksLikePlainToken(value: string): boolean {
  const token = value.trim();
  return token.length >= 12 && !/[\s{}[\]]/.test(token);
}

function candidateFromItem(item: unknown): CredentialCandidate | null {
  if (typeof item === "string") {
    const token = item.trim();
    return looksLikePlainToken(token)
      ? {
        accessToken: token,
        refreshToken: null,
        expiresAtMs: null,
        refreshExpiresAtMs: null,
        uid: null,
        email: null,
        nickname: null,
        enterpriseName: null,
        enterpriseId: null,
        domain: null,
      }
      : null;
  }
  const source = object(item);
  if (!source || source.disabled === true) return null;
  const tokens = object(source.tokens) ?? source;
  const accessToken =
    firstText(tokens, ["accessToken", "access_token", "token", "access", "key"]) ??
      firstText(source, ["accessToken", "access_token", "token", "access", "key"]);
  if (!accessToken) return null;
  return {
    accessToken,
    refreshToken: firstText(tokens, ["refreshToken", "refresh_token", "refresh"]) ??
      firstText(source, ["refreshToken", "refresh_token", "refresh"]),
    expiresAtMs: parseMilliseconds(tokens.expiresAtMs ?? source.expiresAtMs) ??
      parseTimestamp(tokens.expiresAt ?? source.expiresAt),
    refreshExpiresAtMs: parseMilliseconds(tokens.refreshExpiresAtMs ?? source.refreshExpiresAtMs) ??
      parseTimestamp(tokens.refreshExpiresAt ?? source.refreshExpiresAt),
    uid: firstText(tokens, ["uid", "userId", "user_id"]) ??
      firstText(source, ["uid", "userId", "user_id"]),
    email: firstText(tokens, ["email"]) ?? firstText(source, ["email"]),
    nickname: firstText(tokens, ["nickname", "name", "displayName", "display_name"]) ??
      firstText(source, ["nickname", "name", "displayName", "display_name"]),
    enterpriseName: firstText(tokens, ["enterpriseName", "enterprise_name"]) ??
      firstText(source, ["enterpriseName", "enterprise_name"]),
    enterpriseId: firstText(tokens, ["enterpriseId", "enterprise_id", "entId", "ent_id"]) ??
      firstText(source, ["enterpriseId", "enterprise_id", "entId", "ent_id"]),
    domain: firstText(tokens, ["domain"]) ?? firstText(source, ["domain"]),
  };
}

function collectCredentials(value: unknown, output: CredentialCandidate[]): void {
  if (Array.isArray(value)) {
    for (const item of value) collectCredentials(item, output);
    return;
  }
  if (typeof value === "string") {
    const candidate = candidateFromItem(value);
    if (candidate) output.push(candidate);
    return;
  }
  const source = object(value);
  if (!source) return;
  const candidate = candidateFromItem(source);
  if (candidate) {
    output.push(candidate);
    return;
  }
  let traversed = false;
  for (const key of ["accounts", "credentials", "items", "data", "users"]) {
    if (Array.isArray(source[key])) {
      traversed = true;
      collectCredentials(source[key], output);
    }
  }
  if (!traversed && object(source.data)) collectCredentials(source.data, output);
  if (object(source.tokens)) collectCredentials(source.tokens, output);
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
      if (looksLikePlainToken(file.content)) {
        const candidate = candidateFromItem(file.content);
        if (candidate) {
          credentials.push(candidate);
          continue;
        }
      }
      warnings.push(`${file.name}: not valid JSON or access token`);
      continue;
    }
    const found: CredentialCandidate[] = [];
    collectCredentials(content, found);
    if (found.length === 0) {
      warnings.push(`${file.name}: no CodeBuddy access token found`);
      continue;
    }
    credentials.push(...found);
  }
  return { credentials, warnings };
}

export const credentialImport: ResourceImportSupport = {
  displayName: {
    "en-US": "Import CodeBuddy credentials",
    "zh-CN": "导入 CodeBuddy 凭证",
  },
  description: {
    "en-US": "Import a CodeBuddy account export, account array, or access token.",
    "zh-CN": "导入 CodeBuddy 账号导出文件、账号数组或访问令牌。",
  },
  accept: [".json", ".txt"],
  multiple: true,
  parse: async (files: ResourceImportFile[]): Promise<ResourceImportResult> => {
    const { credentials, warnings } = parseCredentialFiles(files);
    if (credentials.length === 0) {
      throw new Error(warnings.join("; ") || "credential file does not contain an access token");
    }
    return {
      resources: await Promise.all(credentials.map((credential) => credentialDraft(credential))),
      ...(warnings.length > 0 ? { warnings } : {}),
    };
  },
};

function quotaRequestError(status: number, body: unknown): CodeBuddyHttpError {
  const root = object(body) ?? {};
  const message = text(
    root.message ?? root.msg ?? object(root.data)?.message ?? object(root.data)?.msg,
  );
  const code = number(root.code ?? object(root.data)?.code);
  return new CodeBuddyHttpError(status, message ?? (code === null ? null : `code ${code}`));
}

const HOUR_MS = 60 * 60 * 1000;
const TWO_DAYS_MS = 2 * 24 * HOUR_MS;

type Capacity = { total: number; used: number; remaining: number };

function firstNumber(source: Record<string, unknown>, keys: string[]): number | null {
  for (const key of keys) {
    const value = number(source[key]);
    if (value !== null) return value;
  }
  return null;
}

function accountsFromResponse(body: unknown): Record<string, unknown>[] {
  const root = object(body) ?? {};
  const data = object(root.data) ?? root;
  const response = object(data.Response) ?? data;
  const responseData = object(response.Data) ?? response;
  const values = responseData.Accounts ?? responseData.accounts;
  if (!Array.isArray(values)) return [];
  return values
    .map((value) => object(value))
    .filter((value): value is Record<string, unknown> => value !== null);
}

function capacityFor(
  source: Record<string, unknown>,
  prefix: "CycleCapacity" | "Capacity",
): Capacity | null {
  const total = firstNumber(source, [
    `${prefix}SizePrecise`,
    `${prefix}Size`,
    `${prefix}Total`,
    `${prefix}TotalCapacity`,
    `${prefix}CapacityTotal`,
    `${prefix}Limit`,
    `${prefix}Quota`,
    prefix,
  ]);
  const used = firstNumber(source, [
    `${prefix}UsedPrecise`,
    `${prefix}Used`,
    `${prefix}Consumed`,
    `${prefix}Usage`,
  ]);
  const remaining = firstNumber(source, [
    `${prefix}RemainPrecise`,
    `${prefix}Remain`,
    `${prefix}RemainingPrecise`,
    `${prefix}Remaining`,
    `${prefix}Available`,
    `${prefix}Balance`,
    `${prefix}Left`,
  ]);
  const nested = object(source[prefix]);
  const nestedTotal = nested
    ? firstNumber(nested, [
      "SizePrecise",
      "sizePrecise",
      "Size",
      "size",
      "Total",
      "total",
      "Capacity",
      "capacity",
    ])
    : null;
  const nestedUsed = nested
    ? firstNumber(nested, [
      "UsedPrecise",
      "usedPrecise",
      "Used",
      "used",
      "Consumed",
      "consumed",
      "Usage",
      "usage",
    ])
    : null;
  const nestedRemaining = nested
    ? firstNumber(nested, [
      "RemainPrecise",
      "remainPrecise",
      "Remain",
      "remain",
      "RemainingPrecise",
      "remainingPrecise",
      "Remaining",
      "remaining",
      "Available",
      "available",
    ])
    : null;
  if (
    total === null && used === null && remaining === null &&
    nestedTotal === null && nestedUsed === null && nestedRemaining === null
  ) return null;
  const effectiveTotal = total ?? nestedTotal ??
    Math.max(used ?? nestedUsed ?? 0, remaining ?? nestedRemaining ?? 0);
  const effectiveUsed = used ?? nestedUsed ??
    Math.max(0, effectiveTotal - (remaining ?? nestedRemaining ?? effectiveTotal));
  const effectiveRemaining = remaining ?? nestedRemaining ??
    Math.max(0, effectiveTotal - effectiveUsed);
  return {
    total: Math.max(0, effectiveTotal),
    used: Math.max(0, effectiveUsed),
    remaining: Math.max(0, effectiveRemaining),
  };
}

function summaryCapacity(value: unknown): Capacity | null {
  const source = object(value);
  if (!source) return null;
  return capacityFor(source, "Capacity");
}

function recurringAccount(account: Record<string, unknown>): boolean {
  const deductionEnd = parseTimestamp(
    account.DeductionEndTime ?? account.deductionEndTime ?? account.deduction_end_time,
  );
  const cycleEnd = parseTimestamp(
    account.CycleEndTime ?? account.cycleEndTime ?? account.cycle_end_time,
  );
  return deductionEnd !== null && cycleEnd !== null && deductionEnd - cycleEnd > TWO_DAYS_MS;
}

/** A grant is dead once ExpiredTime passed; its credits cannot be spent. */
function expiredAccount(account: Record<string, unknown>, nowMs: number): boolean {
  const expired = parseTimestamp(
    account.ExpiredTime ?? account.expiredTime ?? account.expired_time ??
      account.ExpireTime ?? account.ExpireAt ?? account.ValidUntil,
  );
  return expired !== null && expired <= nowMs;
}

function planFor(source: Record<string, unknown>): string | null {
  return firstText(source, [
    "PackageName",
    "packageName",
    "SubProductName",
    "subProductName",
    "PlanName",
    "planName",
    "Plan",
    "plan",
  ]);
}

function toWindow(name: string, capacity: Capacity, resetAtMs: number | null): QuotaWindow {
  return {
    name,
    total: capacity.total,
    used: capacity.used,
    remaining: capacity.remaining,
    remainingPercent: capacity.total > 0
      ? Math.max(0, Math.min(100, (capacity.remaining / capacity.total) * 100))
      : null,
    resetAtMs,
  };
}

export function parseCodeBuddyQuota(body: unknown, nowMs = Date.now()): AccountQuota {
  const root = object(body) ?? {};
  const data = object(root.data) ?? root;
  const response = object(data.Response) ?? data;
  const responseData = object(response.Data) ?? response;
  const accounts = accountsFromResponse(body);
  let plan: string | null = null;
  let subscription: QuotaWindow | null = null;
  const grants: QuotaWindow[] = [];
  const resets: number[] = [];

  for (const account of accounts) {
    plan ??= planFor(account);
    if (expiredAccount(account, nowMs)) continue;
    // CycleCapacity* is what is spendable until the cycle resets; Capacity* is
    // the lifetime pool and can stay untouched while the cycle is already spent.
    const capacity = capacityFor(account, "CycleCapacity") ?? capacityFor(account, "Capacity");
    if (!capacity) continue;
    const cycleEnd = parseTimestamp(
      account.CycleEndTime ?? account.cycleEndTime ?? account.cycle_end_time,
    );
    if (cycleEnd !== null) resets.push(cycleEnd);
    const window = toWindow(planFor(account) ?? plan ?? "CodeBuddy", capacity, cycleEnd);
    if (recurringAccount(account) && subscription === null) {
      subscription = window;
    } else {
      grants.push(window);
    }
  }

  if (accounts.length === 0) {
    const summary = summaryCapacity(
      responseData.ResourceSummary ?? responseData.resourceSummary ?? data.ResourceSummary,
    );
    if (summary) subscription = toWindow(plan ?? "CodeBuddy", summary, null);
  }
  plan ??= planFor(responseData);

  const windows = [...(subscription ? [subscription] : []), ...grants];
  const total = windows.reduce((sum, window) => sum + window.total, 0);
  const used = windows.reduce((sum, window) => sum + window.used, 0);
  const remaining = windows.reduce((sum, window) => sum + window.remaining, 0);
  return {
    plan,
    subscription,
    grants,
    total,
    used,
    remaining,
    resetAtMs: resets.length > 0 ? Math.min(...resets) : null,
    updatedAtMs: nowMs,
  };
}

export function quotaState(quota: AccountQuota | null, nowMs = Date.now()): ResourceState {
  const exhausted = quota !== null && quota.total > 0 && quota.remaining <= 0;
  if (!exhausted) return { status: "ready" };
  const resetAtMs = quota.resetAtMs;
  if (resetAtMs !== null && resetAtMs <= nowMs) return { status: "ready" };
  return {
    status: "cooling",
    retryAtMs: resetAtMs ?? nowMs + HOUR_MS,
    message: "CodeBuddy credits are exhausted",
  };
}

function retryAfterMs(message: string, nowMs: number): number | null {
  const match = message.match(/retry(?:_after|\s+after)?\s*[:=]?\s*(\d+)/i);
  if (!match?.[1]) return null;
  const parsed = Number(match[1]);
  if (!Number.isFinite(parsed) || parsed <= 0) return null;
  return parsed > 10_000_000 ? parsed - nowMs : parsed * 1000;
}

export function quotaExhaustedPatch(
  data: AccountData,
  error = "",
  nowMs = Date.now(),
): ResourcePatch {
  const old = data.quota;
  const retry = retryAfterMs(error, nowMs);
  const resetAtMs = retry !== null
    ? nowMs + Math.max(1000, retry)
    : old?.resetAtMs ?? nowMs + HOUR_MS;
  const total = old !== null && old.total > 0 ? old.total : 1;
  const quota: AccountQuota = {
    plan: old?.plan ?? null,
    subscription: old?.subscription
      ? {
        ...old.subscription,
        // Each window drains against its own total; using the aggregate here
        // would report used > total and double-count usage.
        used: old.subscription.total,
        remaining: 0,
        remainingPercent: 0,
        resetAtMs,
      }
      : null,
    grants: (old?.grants ?? []).map((grant) => ({
      ...grant,
      used: grant.total,
      remaining: 0,
      remainingPercent: 0,
    })),
    total,
    used: total,
    remaining: 0,
    resetAtMs,
    updatedAtMs: nowMs,
  };
  return {
    privateData: { ...data, quota } as unknown as JsonValue,
    state: quotaState(quota, nowMs),
  };
}

export type QuotaResult = { quota: AccountQuota | null; noPackage: boolean };

export async function fetchQuota(
  data: AccountData,
  context: PluginContext,
  nowMs = Date.now(),
): Promise<QuotaResult> {
  const response = await context.network.fetch(billingUrl(data, QUOTA_PATH), {
    method: "POST",
    headers: accountHeaders(data),
    body: "{}",
  });
  let body: unknown;
  try {
    body = JSON.parse(response.body);
  } catch {
    body = {};
  }
  if (isAuthError(response.status, body)) throw quotaRequestError(response.status, body);
  if (response.status === 404) return { quota: null, noPackage: true };
  if (response.status < 200 || response.status >= 300 || isBusinessError(body)) {
    const lower = response.body.toLowerCase();
    if (
      lower.includes("no package") || lower.includes("not found") || lower.includes("resource not")
    ) {
      return { quota: null, noPackage: true };
    }
    throw quotaRequestError(response.status, body);
  }
  if (accountsFromResponse(body).length === 0) return { quota: null, noPackage: true };
  return { quota: parseCodeBuddyQuota(body, nowMs), noPackage: false };
}

export async function refreshAccount(
  resource: ResourceSnapshot,
  context: PluginContext,
): Promise<ResourcePatch> {
  const original = accountData(resource);
  let data = original;
  let refreshed = false;
  try {
    const fresh = await ensureFreshAccount(data, context);
    data = fresh.data;
    refreshed = fresh.refreshed;
  } catch (error) {
    if (error instanceof CodeBuddyHttpError && isAuthStatus(error.status)) {
      return {
        state: { status: "invalid", message: "CodeBuddy authorization expired; sign in again" },
      };
    }
    throw error;
  }
  try {
    const result = await fetchQuota(data, context);
    if (result.noPackage) {
      return {
        privateData: { ...data, quota: data.quota } as unknown as JsonValue,
        state: quotaState(data.quota),
      };
    }
    return {
      privateData: { ...data, quota: result.quota } as unknown as JsonValue,
      state: quotaState(result.quota),
    };
  } catch (error) {
    if (error instanceof CodeBuddyHttpError && isAuthStatus(error.status)) {
      return {
        state: { status: "invalid", message: "CodeBuddy authorization expired; sign in again" },
      };
    }
    if (refreshed) {
      const state: ResourceState = resource.state.status === "cooling" ? resource.state : {
        status: "cooling",
        retryAtMs: Date.now() + HOUR_MS,
        message: error instanceof Error ? error.message : String(error),
      };
      return {
        privateData: { ...data, quota: data.quota } as unknown as JsonValue,
        state,
      };
    }
    throw error;
  }
}

export function presentAccount(resource: ResourceSnapshot): ResourceView {
  const data = accountData(resource);
  const metrics: ResourceMetric[] = [];
  const subscription = data.quota?.subscription;
  if (subscription) {
    metrics.push({
      id: "subscription",
      label: { "en-US": "Fixed credits", "zh-CN": "固定额度" },
      unit: "count",
      value: Math.round(subscription.remaining),
      ...(subscription.resetAtMs !== null ? { resetAtMs: subscription.resetAtMs } : {}),
    });
  }
  // Grants are additive bonus credits; the package name is upstream copy the
  // user never chose, so show the pool size instead.
  const grants = data.quota?.grants ?? [];
  if (grants.length > 0) {
    metrics.push({
      id: "grants",
      label: { "en-US": "Bonus credits", "zh-CN": "赠送额度" },
      unit: "count",
      value: Math.round(grants.reduce((sum, grant) => sum + grant.remaining, 0)),
      ...(grants.some((grant) => grant.resetAtMs !== null)
        ? { resetAtMs: Math.min(...grants.map((grant) => grant.resetAtMs ?? Infinity)) }
        : {}),
    });
  }
  if (data.quota !== null && data.quota.total > 0) {
    metrics.push({
      id: "total",
      label: { "en-US": "Total available", "zh-CN": "总额度" },
      unit: "count",
      value: Math.round(data.quota.remaining),
      ...(data.quota.resetAtMs !== null ? { resetAtMs: data.quota.resetAtMs } : {}),
    });
  }
  if (metrics.length === 0 && data.quota !== null && data.quota.remaining > 0) {
    metrics.push({
      id: "credits",
      label: { "en-US": "Credits", "zh-CN": "可用额度" },
      unit: "count",
      value: data.quota.remaining,
    });
  }
  const descriptions: string[] = [];
  if (data.quota?.plan) descriptions.push(data.quota.plan);
  if (data.lastCheckin?.message) descriptions.push(data.lastCheckin.message);
  return {
    displayName: data.email ?? data.nickname ?? data.uid ?? "CodeBuddy account",
    ...(descriptions.length > 0 ? { description: descriptions.join(" · ") } : {}),
    ...(metrics.length > 0 ? { metrics } : {}),
  };
}
