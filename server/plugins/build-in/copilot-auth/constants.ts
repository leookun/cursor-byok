/**
 * 所有「以 VS Code Copilot Chat 身份访问 GitHub」的常量集中在此,便于随上游
 * (caozhiyuan/copilot-api `src/lib/api-config.ts`,MIT)同步版本号,或整体换成自有 OAuth App。
 */
export const CLIENT_ID = "Iv1.b507a08c87ecfe98";
export const SCOPE = "read:user";

export const DEVICE_CODE_URL = "https://github.com/login/device/code";
export const ACCESS_TOKEN_URL = "https://github.com/login/oauth/access_token";
export const COPILOT_TOKEN_URL = "https://api.github.com/copilot_internal/v2/token";
export const COPILOT_USER_URL = "https://api.github.com/copilot_internal/user";
export const DEFAULT_COPILOT_BASE = "https://api.githubcopilot.com";

export const COPILOT_CHAT_VERSION = "0.68.0";
export const VSCODE_VERSION = "1.140.0";
export const COPILOT_API_VERSION = "2026-08-01";
export const GITHUB_API_VERSION = "2025-04-01";

/** token 交换可能返回的 Copilot API 主机;必须与 plugin.json 的网络白名单一致。 */
export const COPILOT_API_HOSTS: readonly string[] = [
  "api.githubcopilot.com",
  "api.individual.githubcopilot.com",
  "api.business.githubcopilot.com",
  "api.enterprise.githubcopilot.com",
];

export type Initiator = "user" | "agent";

/** 头 A:github.com 设备码 OAuth。 */
export function oauthHeaders(): Record<string, string> {
  return {
    accept: "application/json",
    "content-type": "application/json",
  };
}

/** 头 B:api.github.com 的 token 交换与账号/额度查询;前缀是 token 而非 Bearer。 */
export function githubHeaders(githubToken: string): Record<string, string> {
  return {
    authorization: `token ${githubToken}`,
    accept: "application/json",
    "user-agent": `GitHubCopilotChat/${COPILOT_CHAT_VERSION}`,
    "x-github-api-version": GITHUB_API_VERSION,
    "x-vscode-user-agent-library-version": "electron-fetch",
  };
}

function copilotBaseHeaders(
  copilotToken: string,
  deviceId: string,
  intent: string,
): Record<string, string> {
  const requestId = crypto.randomUUID();
  return {
    authorization: `Bearer ${copilotToken}`,
    "copilot-integration-id": "vscode-chat",
    "editor-version": `vscode/${VSCODE_VERSION}`,
    "editor-plugin-version": `copilot-chat/${COPILOT_CHAT_VERSION}`,
    "user-agent": `GitHubCopilotChat/${COPILOT_CHAT_VERSION}`,
    "editor-device-id": deviceId,
    "x-github-api-version": COPILOT_API_VERSION,
    "x-vscode-user-agent-library-version": "electron-fetch",
    "x-request-id": requestId,
    "x-agent-task-id": requestId,
    "openai-intent": intent,
    "x-interaction-type": intent,
  };
}

/** 头 C(模型发现):不带 content-type、x-interaction-id、x-initiator。 */
export function copilotModelHeaders(
  copilotToken: string,
  deviceId: string,
): Record<string, string> {
  return copilotBaseHeaders(copilotToken, deviceId, "model-access");
}

/** 头 C(对话):x-initiator 决定是否消耗 premium request。 */
export function copilotChatHeaders(options: {
  copilotToken: string;
  deviceId: string;
  cacheKey: string | null;
  initiator: Initiator;
  vision: boolean;
}): Record<string, string> {
  const headers = copilotBaseHeaders(options.copilotToken, options.deviceId, "conversation-agent");
  headers["content-type"] = "application/json";
  if (options.cacheKey !== null) headers["x-interaction-id"] = options.cacheKey;
  headers["x-initiator"] = options.initiator;
  if (options.vision) headers["copilot-vision-request"] = "true";
  return headers;
}
