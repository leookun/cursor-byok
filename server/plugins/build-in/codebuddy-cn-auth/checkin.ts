import type { JsonValue, NetworkResponse, PluginContext } from "cursor-byok:plugin";
import type {
  ResourceAction,
  ResourceActionCard,
  ResourceActionResult,
  ResourcePatch,
  ResourceSnapshot,
} from "cursor-byok:resource";
import {
  type AccountData,
  accountData,
  accountHeaders,
  type AccountQuota,
  billingUrl,
  CHECKIN_PATH,
  CHECKIN_STATUS_FALLBACK_PATH,
  CHECKIN_STATUS_PATH,
  CodeBuddyHttpError,
  ensureFreshAccount,
  fetchQuota,
  isAuthError,
  isAuthStatus,
  isBusinessError,
  type LastCheckin,
  quotaState,
} from "./resources.ts";

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

function nestedObject(value: unknown): Record<string, unknown> {
  const root = object(value) ?? {};
  const data = object(root.data) ?? root;
  return object(data.Response) ?? object(data.Result) ?? object(data.data) ?? data;
}

function parseResponse(body: string): unknown {
  try {
    return JSON.parse(body) as unknown;
  } catch {
    return {};
  }
}

export interface CheckinStatus {
  already: boolean;
  date: string | null;
  reward: number | null;
  balance: number | null;
  message: string | null;
}

export function parseCheckinStatus(body: unknown, nowMs = Date.now()): CheckinStatus {
  const data = nestedObject(body);
  const status = text(data.status);
  const already = data.today_checked_in === true || data.todayCheckedIn === true ||
    data.checked_in === true || data.checkedIn === true || data.isCheckedIn === true ||
    status === "already" || status === "已签到" || status === "repeat";
  return {
    already,
    date: text(data.date) ?? text(data.checkinDate) ?? new Date(nowMs).toISOString().slice(0, 10),
    reward: number(data.reward ?? data.rewardAmount ?? data.rewardCredits),
    balance: number(data.balance ?? data.remaining ?? data.remain),
    message: text(data.message ?? data.msg),
  };
}

export function parseCheckinResult(body: unknown, nowMs = Date.now()): CheckinStatus {
  const status = parseCheckinStatus(body, nowMs);
  const data = nestedObject(body);
  const code = number(object(body)?.code);
  const message = status.message ?? text(data.message ?? data.msg) ?? "Daily check-in completed";
  if (code !== null && code !== 0 && code !== 200) {
    const lower = message.toLowerCase();
    if (lower.includes("repeat") || lower.includes("already") || message.includes("已签到")) {
      return { ...status, already: true, message: "Already checked in" };
    }
  }
  return { ...status, message };
}

function responseMessage(body: unknown, fallback: string): string {
  const root = object(body) ?? {};
  const data = nestedObject(body);
  return text(root.message ?? root.msg ?? data.message ?? data.msg) ?? fallback;
}

function checkinSubmissionResult(
  result: { response: NetworkResponse; body: unknown },
): CheckinStatus {
  if (result.response.status < 200 || result.response.status >= 300) {
    throw new Error(`CodeBuddy check-in failed (HTTP ${result.response.status})`);
  }
  const status = parseCheckinResult(result.body);
  const code = number(object(result.body)?.code);
  const envelopeFailed = code !== null && code !== 0 && code !== 200;
  // A 2xx with only an OK envelope is not proof of a completed check-in; the
  // body has to carry a signal we can act on.
  const explicitSuccess = !envelopeFailed &&
    (status.already || status.reward !== null || status.balance !== null);
  if (!explicitSuccess) {
    throw new Error(responseMessage(result.body, "CodeBuddy check-in response was not recognized"));
  }
  return status;
}

function checkinPatch(
  data: AccountData,
  status: CheckinStatus,
  quota: AccountQuota | null,
): ResourcePatch {
  const lastCheckin: LastCheckin = {
    date: status.date,
    status: status.already ? "already" : "success",
    reward: status.reward,
    balance: status.balance,
    message: status.message,
  };
  return {
    privateData: { ...data, lastCheckin, quota: quota ?? data.quota } as unknown as JsonValue,
    state: quotaState(quota ?? data.quota),
  };
}

function checkinCard(status: CheckinStatus): ResourceActionCard[] {
  const fields = [];
  if (status.reward !== null) {
    fields.push({
      id: "reward",
      label: { "en-US": "Reward", "zh-CN": "奖励" },
      value: String(status.reward),
    });
  }
  if (status.balance !== null) {
    fields.push({
      id: "balance",
      label: { "en-US": "Balance", "zh-CN": "余额" },
      value: String(status.balance),
    });
  }
  return [{
    id: "daily-checkin",
    title: { "en-US": "CodeBuddy daily check-in", "zh-CN": "CodeBuddy 每日签到" },
    status: status.already
      ? { "en-US": "Already checked in", "zh-CN": "已签到" }
      : { "en-US": "Checked in", "zh-CN": "签到成功" },
    ...(fields.length > 0 ? { fields } : {}),
  }];
}

function invalidResult(message: string): ResourceActionResult {
  return {
    title: { "en-US": "CodeBuddy authorization required", "zh-CN": "需要重新授权 CodeBuddy" },
    description: message,
    succeeded: false,
    patch: { state: { status: "invalid", message } },
  };
}

async function refreshAfterAuth(
  data: AccountData,
  context: PluginContext,
): Promise<AccountData | null> {
  if (!data.refreshToken) return null;
  try {
    return (await ensureFreshAccount({ ...data, expiresAtMs: Date.now() - 1 }, context)).data;
  } catch (error) {
    if (error instanceof CodeBuddyHttpError && isAuthStatus(error.status)) return null;
    throw error;
  }
}

async function fetchCheckinStatus(
  data: AccountData,
  context: PluginContext,
  initialPath = CHECKIN_STATUS_PATH,
): Promise<{ response: NetworkResponse; body: unknown; path: string }> {
  let path = initialPath;
  let response = await context.network.fetch(billingUrl(data, path), {
    method: "POST",
    headers: accountHeaders(data),
    body: "{}",
  });
  if (response.status === 404 && path === CHECKIN_STATUS_PATH) {
    path = CHECKIN_STATUS_FALLBACK_PATH;
    response = await context.network.fetch(billingUrl(data, path), {
      method: "POST",
      headers: accountHeaders(data),
      body: "{}",
    });
  }
  return { response, body: parseResponse(response.body), path };
}

async function fetchDailyCheckin(
  data: AccountData,
  context: PluginContext,
): Promise<{ response: NetworkResponse; body: unknown }> {
  const response = await context.network.fetch(billingUrl(data, CHECKIN_PATH), {
    method: "POST",
    headers: accountHeaders(data),
    body: "{}",
  });
  return { response, body: parseResponse(response.body) };
}

async function checkin(
  resource: ResourceSnapshot,
  _input: JsonValue,
  context: PluginContext,
): Promise<ResourceActionResult> {
  let data = accountData(resource);
  try {
    data = (await ensureFreshAccount(data, context)).data;
  } catch (error) {
    if (error instanceof CodeBuddyHttpError && isAuthStatus(error.status)) {
      return invalidResult("CodeBuddy authorization expired; sign in again");
    }
    throw error;
  }

  let statusResult = await fetchCheckinStatus(data, context);
  if (isAuthError(statusResult.response.status, statusResult.body) && data.refreshToken) {
    const refreshed = await refreshAfterAuth(data, context);
    if (!refreshed) return invalidResult("CodeBuddy authorization expired; sign in again");
    data = refreshed;
    statusResult = await fetchCheckinStatus(data, context, statusResult.path);
  }
  if (isAuthError(statusResult.response.status, statusResult.body)) {
    return invalidResult("CodeBuddy authorization expired; sign in again");
  }
  if (statusResult.response.status < 200 || statusResult.response.status >= 300) {
    throw new Error(`CodeBuddy check-in status failed (HTTP ${statusResult.response.status})`);
  }
  if (isBusinessError(statusResult.body)) {
    throw new Error(responseMessage(statusResult.body, "CodeBuddy check-in status failed"));
  }

  const statusResponse = statusResult;

  let status = parseCheckinStatus(statusResult.body);
  if (!status.already) {
    let actionResult = await fetchDailyCheckin(data, context);
    if (!isAuthError(actionResult.response.status, actionResult.body)) {
      status = checkinSubmissionResult(actionResult);
    } else if (data.refreshToken) {
      const refreshed = await refreshAfterAuth(data, context);
      if (!refreshed) return invalidResult("CodeBuddy authorization expired; sign in again");
      data = refreshed;
      const retryStatus = await fetchCheckinStatus(data, context, statusResponse.path);
      if (isAuthError(retryStatus.response.status, retryStatus.body)) {
        return invalidResult("CodeBuddy authorization expired; sign in again");
      }
      if (retryStatus.response.status < 200 || retryStatus.response.status >= 300) {
        throw new Error(`CodeBuddy check-in status failed (HTTP ${retryStatus.response.status})`);
      }
      if (isBusinessError(retryStatus.body)) {
        throw new Error(responseMessage(retryStatus.body, "CodeBuddy check-in status failed"));
      }
      const refreshedStatus = parseCheckinStatus(retryStatus.body);
      status = refreshedStatus.already
        ? refreshedStatus
        : checkinSubmissionResult(await fetchDailyCheckin(data, context));
    } else {
      return invalidResult("CodeBuddy authorization expired; sign in again");
    }
  }

  let quota = data.quota;
  try {
    const result = await fetchQuota(data, context);
    quota = result.quota ?? quota;
  } catch {
    // Check-in success is still useful when quota is temporarily unavailable.
  }
  return {
    title: status.already
      ? { "en-US": "CodeBuddy already checked in", "zh-CN": "CodeBuddy 今日已签到" }
      : { "en-US": "CodeBuddy checked in", "zh-CN": "CodeBuddy 签到成功" },
    description: status.message ?? undefined,
    cards: checkinCard(status),
    patch: checkinPatch(data, status, quota),
  };
}

export const checkInAction = {
  id: "check-in",
  displayName: { "en-US": "Daily check-in", "zh-CN": "每日签到" },
  automation: { kind: "daily", defaultEnabled: true, hidden: true },
  run: checkin,
} as ResourceAction;
