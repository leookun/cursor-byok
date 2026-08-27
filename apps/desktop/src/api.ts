import type { AdRuntime } from "./components/ads/types";
import type { Locale } from "./i18n/runtime";

export type ModelType = "openai" | "anthropic";

export interface Model {
  model_hash: string;
  sort_order: number;
  display_name: string;
  type: ModelType;
  base_url: string;
  use_full_url: boolean;
  api_key: string;
  tooltip_data: string;
  model_id: string;
  reasoning_effort: string | null;
  openai_endpoint: string;
  openai_extra_params_enabled: boolean;
  openai_extra_params: Record<string, unknown>;
  custom_headers_enabled: boolean;
  custom_headers: Record<string, string>;
  anthropic_extra_params_enabled: boolean;
  anthropic_extra_params: Record<string, unknown>;
  context_window_tokens: number | null;
  max_completion_tokens: number | null;
  anthropic_max_tokens: number | null;
  anthropic_thinking_effort: string | null;
  thinking_budget_tokens: number | null;
  created_at_ms: number;
  updated_at_ms: number;
}

export interface ModelInput {
  sort_order: number;
  display_name: string;
  type: ModelType;
  base_url: string;
  use_full_url: boolean;
  api_key: string;
  tooltip_data: string;
  model_id: string;
  reasoning_effort: string | null;
  openai_endpoint: string;
  openai_extra_params_enabled: boolean;
  openai_extra_params: Record<string, unknown>;
  custom_headers_enabled: boolean;
  custom_headers: Record<string, string>;
  anthropic_extra_params_enabled: boolean;
  anthropic_extra_params: Record<string, unknown>;
  context_window_tokens: number | null;
  max_completion_tokens: number | null;
  anthropic_max_tokens: number | null;
  anthropic_thinking_effort: string | null;
  thinking_budget_tokens: number | null;
}

export interface ModelDiscoveryInput {
  type: ModelType;
  base_url: string;
  api_key: string;
  custom_headers_enabled: boolean;
  custom_headers: Record<string, string>;
}

export interface DiscoveredModel {
  id: string;
  context_window_tokens: number | null;
}

export interface LegacyModelImportPreviewItem {
  model_hash: string;
  display_name: string;
  model_id: string;
  type: ModelType;
  existing: boolean;
}

export interface LegacyModelImportPreview {
  source: string;
  total: number;
  new_models: number;
  existing_models: number;
  models: LegacyModelImportPreviewItem[];
}

export interface LegacyModelImportResult {
  imported: number;
  skipped: number;
  total: number;
}

export interface ModelConnectivityResult {
  duration_ms: number;
  first_text_ms: number | null;
  output_tokens: number;
  tokens_per_second: number;
  tokens_estimated: boolean;
  output: string;
}

export interface GrokDeviceCodeResponse {
  device_code: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete: string | null;
  expires_in: number;
  interval: number;
}

export interface GrokTokenPollResponse {
  status: "success" | "pending" | "slow_down" | "expired" | "access_denied" | "error";
  access_token: string | null;
  refresh_token: string | null;
  token_type: string | null;
  expires_in: number | null;
  error_message: string | null;
}

export interface CodexDeviceCodeResponse {
  device_code: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete: string | null;
  expires_in: number;
  interval: number;
}

export interface CodexTokenPollResponse {
  status: "success" | "pending" | "slow_down" | "expired" | "access_denied" | "error";
  access_token: string | null;
  refresh_token: string | null;
  token_type: string | null;
  expires_in: number | null;
  error_message: string | null;
}

export interface SubscriptionUsage {
  plan_label: string | null;
  remaining_percent: number | null;
  used_percent: number | null;
  reset_at_ms: number | null;
  limit_reached: boolean;
  session_remaining_percent: number | null;
  session_reset_at_ms: number | null;
}

export interface SubscriptionAccount {
  account_id: string;
  provider: string;
  display_name: string;
  plan_label: string | null;
  remaining_percent: number | null;
  used_percent: number | null;
  reset_at_ms: number | null;
  session_remaining_percent: number | null;
  session_reset_at_ms: number | null;
  limit_reached: boolean;
  active: boolean;
}

export interface SubscriptionImportResult {
  imported: number;
  skipped: number;
  imported_names: string[];
  errors: Array<{ name: string; message: string }>;
}

export type CaState = "missing" | "untrusted" | "ready" | "invalid";
export type IntegrationState = "disabled" | "enabled" | "degraded";
export interface CursorHarnessStatus {
  platform: string;
  ca: CaState;
  configured_models: number;
  enabled_models: number;
  integration: IntegrationState;
  proxy_url: string | null;
  ca_install_command: string | null;
}

export interface PortSettings {
  proxy_port: number;
  service_port: number;
}

export interface StatisticsStorage {
  bytes: number;
  call_count: number;
  trace_count: number;
}

export type ProxyMode = "system" | "custom";

export interface ProxySettings {
  mode: ProxyMode;
  address: string;
  auth_enabled: boolean;
  username: string;
  has_password: boolean;
}

export interface ProxySettingsInput {
  mode: ProxyMode;
  address: string;
  auth_enabled: boolean;
  username: string;
  password?: string;
}

export type TabMode = "public" | "direct" | "custom";

export interface TabSettings {
  mode: TabMode;
  address: string;
}

export interface DesktopSettings {
  silent_start: boolean;
}

export interface OverviewMetrics {
  llm_calls: number;
  successful_calls: number;
  failed_calls: number;
  token_usage: number;
  prompt_tokens: number;
  input_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  output_tokens: number;
}

export type TokenUsageGranularity = "minute" | "hour" | "day";

export interface OverviewTokenUsageBucket {
  bucket_start_ms: number;
  input_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  output_tokens: number;
}

export interface Overview {
  metrics: OverviewMetrics;
  token_usage_granularity: TokenUsageGranularity;
  token_usage_series: OverviewTokenUsageBucket[];
}

export interface LlmCall {
  call_kind: "provider_llm" | "cursor_official";
  route: "local_byok" | "cursor_official";
  call_id: string;
  run_id: string;
  conversation_id: string;
  provider_call_index: number;
  model_hash: string | null;
  provider_type: string;
  provider_url: string;
  request_type: string;
  request_url: string;
  model_id: string;
  display_name: string;
  reasoning_effort: string | null;
  fast: boolean | null;
  status: string;
  finish_reason: string | null;
  created_at_ms: number;
  ttfb_ms: number | null;
  ttft_ms: number | null;
  duration_ms: number | null;
  input_tokens: number | null;
  output_tokens: number | null;
  total_tokens: number | null;
  cache_read_tokens: number | null;
  cache_write_tokens: number | null;
  reasoning_tokens: number | null;
  message_count: number;
  tool_count: number;
  http_status: number | null;
  error_kind: string | null;
  error_message: string | null;
  detailed: boolean;
}

export interface CallDetail {
  call: LlmCall;
  request: { headers: unknown; body: unknown; byte_count: number } | null;
  response_chunks: Array<{ seq: number; received_offset_ms: number; data: string; byte_count: number }>;
  cursor_trace: {
    trace: {
      request_id: string;
      conversation_id: string | null;
      route: "local_byok" | "cursor_official";
      model_id: string | null;
      status: string;
      request_bytes: number;
      response_bytes: number;
      response_event_count: number;
      http_status: number | null;
      received_at_ms: number;
      first_response_at_ms: number | null;
      finished_at_ms: number | null;
      error_message: string | null;
    };
    artifacts: Array<{
      seq: number;
      artifact_type: string;
      source: string;
      metadata: unknown;
      created_at_ms: number;
      byte_count: number;
      encoding: "utf8" | "base64";
      data: string;
    }>;
  } | null;
}

const packagedDesktop = "__TAURI_INTERNALS__" in window
  || window.location.protocol === "tauri:"
  || window.location.hostname === "tauri.localhost";
const API_ROOT = "/__byok-api__/api";

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response;
  try {
    response = await fetch(`${API_ROOT}${path}`, {
      ...init,
      headers: init?.body ? { "content-type": "application/json", ...init.headers } : init?.headers,
    });
  } catch (cause) {
    throw new Error(t("无法连接本地管理服务"), { cause });
  }
  if (!response.ok) {
    const body = await response.text();
    let message = body;
    try {
      const parsed = JSON.parse(body) as { message?: unknown };
      if (typeof parsed.message === "string") message = parsed.message;
    } catch {
      // Plain-text errors are already suitable for display.
    }
    throw new Error(message || `${response.status} ${response.statusText}`);
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export const api = {
  ads: (disabledAdIds: Iterable<string>, locale: Locale) => {
    const value = [...disabledAdIds].join(",");
    return request<AdRuntime>("/ads", {
      headers: {
        "accept-language": locale,
        ...(value ? { "disable-ad-ids": value } : {}),
      },
    });
  },
  dismissAd: (id: string, reason: string) => request<void>(`/ads/${encodeURIComponent(id)}/dismissals`, { method: "POST", body: JSON.stringify({ reason }) }),
  models: () => request<Model[]>("/models"),
  createModels: (models: ModelInput[]) => request<Model[]>("/models", { method: "POST", body: JSON.stringify({ models }) }),
  reorderModels: (modelHashes: string[]) => request<Model[]>("/models/order", { method: "PUT", body: JSON.stringify({ model_hashes: modelHashes }) }),
  discoverModels: (input: ModelDiscoveryInput) => request<{ models: DiscoveredModel[] }>("/models/discover", { method: "POST", body: JSON.stringify(input) }),
  previewV0049Models: () => request<LegacyModelImportPreview>("/models/import-v0049"),
  importV0049Models: () => request<LegacyModelImportResult>("/models/import-v0049", { method: "POST" }),
  updateModel: (hash: string, model: ModelInput) => request<Model>(`/models/${hash}`, { method: "PUT", body: JSON.stringify(model) }),
  deleteModel: (hash: string) => request<void>(`/models/${hash}`, { method: "DELETE" }),
  testModel: (hash: string, testId: string, signal?: AbortSignal) => request<ModelConnectivityResult>(`/models/${hash}/test/${encodeURIComponent(testId)}`, { method: "POST", signal }),
  cancelModelTest: (hash: string, testId: string) => request<void>(`/models/${hash}/test/${encodeURIComponent(testId)}`, { method: "DELETE" }),
  startGrokDeviceAuth: (clientId?: string) => request<GrokDeviceCodeResponse>("/auth/grok/device-code", { method: "POST", body: JSON.stringify({ client_id: clientId }) }),
  pollGrokDeviceAuth: (deviceCode: string, clientId?: string) => request<GrokTokenPollResponse>("/auth/grok/poll", { method: "POST", body: JSON.stringify({ device_code: deviceCode, client_id: clientId }) }),
  startCodexDeviceAuth: (clientId?: string) => request<CodexDeviceCodeResponse>("/auth/codex/device-code", { method: "POST", body: JSON.stringify({ client_id: clientId }) }),
  pollCodexDeviceAuth: (deviceCode: string, userCode?: string, clientId?: string) => request<CodexTokenPollResponse>("/auth/codex/poll", { method: "POST", body: JSON.stringify({ device_code: deviceCode, user_code: userCode, client_id: clientId }) }),
  grokUsage: (apiKey?: string) => request<SubscriptionUsage>("/auth/grok/usage", { method: "POST", body: JSON.stringify({ api_key: apiKey ?? null }) }),
  codexUsage: (apiKey?: string) => request<SubscriptionUsage>("/auth/codex/usage", { method: "POST", body: JSON.stringify({ api_key: apiKey ?? null }) }),
  grokAccounts: () => request<SubscriptionAccount[]>("/auth/grok/accounts"),
  saveGrokAccount: (accessToken: string, refreshToken?: string | null) => request<SubscriptionAccount>("/auth/grok/accounts", { method: "POST", body: JSON.stringify({ access_token: accessToken, refresh_token: refreshToken ?? null }) }),
  activateGrokAccount: (accountId: string) => request<SubscriptionAccount>(`/auth/grok/accounts/${encodeURIComponent(accountId)}`, { method: "PUT" }),
  deleteGrokAccount: (accountId: string) => request<SubscriptionAccount[]>(`/auth/grok/accounts/${encodeURIComponent(accountId)}`, { method: "DELETE" }),
  codexAccounts: () => request<SubscriptionAccount[]>("/auth/codex/accounts"),
  saveCodexAccount: (accessToken: string, refreshToken?: string | null) => request<SubscriptionAccount>("/auth/codex/accounts", { method: "POST", body: JSON.stringify({ access_token: accessToken, refresh_token: refreshToken ?? null }) }),
  activateCodexAccount: (accountId: string) => request<SubscriptionAccount>(`/auth/codex/accounts/${encodeURIComponent(accountId)}`, { method: "PUT" }),
  deleteCodexAccount: (accountId: string) => request<SubscriptionAccount[]>(`/auth/codex/accounts/${encodeURIComponent(accountId)}`, { method: "DELETE" }),
  importAccounts: (provider: "grok" | "codex", files: Array<{ name: string; content: unknown }>) => request<SubscriptionImportResult>("/auth/accounts/import", { method: "POST", body: JSON.stringify({ provider, files }) }),
  overview: (filter?: { startMs: number; endMs: number; modelHashes?: string[] }) => {
    const params = new URLSearchParams();
    if (filter) {
      params.set("start_ms", String(filter.startMs));
      params.set("end_ms", String(filter.endMs));
      if (filter.modelHashes?.length) params.set("model_hashes", JSON.stringify(filter.modelHashes));
    }
    const query = params.toString();
    return request<Overview>(`/overview${query ? `?${query}` : ""}`);
  },
  cursorHarness: () => request<CursorHarnessStatus>("/harness/cursor/status"),
  initializeCursorCa: () => request<CursorHarnessStatus>("/harness/cursor/ca/initialize", { method: "POST" }),
  openCursorCaInstallTerminal: async (command: string) => {
    if (!packagedDesktop) throw new Error(t("请在桌面应用中打开终端安装 CA"));
    const { invoke } = await import("@tauri-apps/api/core");
    await invoke("open_terminal_with_command", { command });
  },
  copyCursorText: async (text: string) => {
    if (!packagedDesktop) throw new Error(t("请在桌面应用中复制到系统剪贴板"));
    const { writeText } = await import("@tauri-apps/plugin-clipboard-manager");
    await writeText(text);
  },
  setCursorEnabled: (enabled: boolean) => request<CursorHarnessStatus>("/harness/cursor/enabled", { method: "PUT", body: JSON.stringify({ enabled }) }),
  calls: () => request<LlmCall[]>("/llm-calls?limit=200"),
  call: (id: string) => request<CallDetail>(`/llm-calls/${encodeURIComponent(id)}`),
  openCallDetails: async (id: string) => {
    const url = new URL(window.location.href);
    url.hash = `/calls/${encodeURIComponent(id)}`;
    await request<void>("/desktop/open-external-url", { method: "POST", body: JSON.stringify({ url: url.toString() }) });
  },
  openExternalUrl: (url: string) => request<void>("/desktop/open-external-url", { method: "POST", body: JSON.stringify({ url }) }),
  observability: () => request<{ detailed: boolean }>("/settings/observability"),
  setObservability: (detailed: boolean) => request<{ detailed: boolean }>("/settings/observability", { method: "PUT", body: JSON.stringify({ detailed }) }),
  ports: () => request<PortSettings>("/settings/ports"),
  setPorts: (settings: PortSettings) => request<PortSettings>("/settings/ports", { method: "PUT", body: JSON.stringify(settings) }),
  statisticsStorage: () => request<StatisticsStorage>("/settings/storage/statistics"),
  clearStatisticsStorage: () => request<StatisticsStorage>("/settings/storage/statistics", { method: "DELETE" }),
  proxySettings: () => request<ProxySettings>("/settings/proxy"),
  setProxySettings: (settings: ProxySettingsInput) => request<ProxySettings>("/settings/proxy", { method: "PUT", body: JSON.stringify(settings) }),
  tabSettings: () => request<TabSettings>("/settings/tab"),
  setTabSettings: (settings: TabSettings) => request<TabSettings>("/settings/tab", { method: "PUT", body: JSON.stringify(settings) }),
  desktopSettings: () => request<DesktopSettings>("/settings/desktop"),
  setDesktopSettings: (settings: DesktopSettings) => request<DesktopSettings>("/settings/desktop", { method: "PUT", body: JSON.stringify(settings) }),
};
