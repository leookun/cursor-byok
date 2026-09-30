import type { ResourceSupport, ResourceView } from "cursor-byok:resource";
import { accountData, OAuthError, privateData, refreshTokens, RESOURCE_TYPE } from "./auth.ts";
import { claudeOAuth } from "./oauth.ts";

export const claudeAccounts: ResourceSupport = {
  type: RESOURCE_TYPE,
  displayName: { "en-US": "Claude accounts", "ru-RU": "Аккаунты Claude", "zh-CN": "Claude 账号" },
  add: [claudeOAuth],
  present(resource): ResourceView {
    const data = accountData(resource);
    return {
      displayName: data.displayName,
      description: {
        "en-US": "Experimental subscription access. Anthropic may restrict this connection.",
        "ru-RU": "Экспериментальный доступ по подписке. Anthropic может ограничить подключение.",
        "zh-CN": "实验性订阅访问。Anthropic 可能限制此连接。",
      },
    };
  },
  async refresh(resource, context) {
    const data = accountData(resource);
    try {
      const refreshed = await refreshTokens(data, context);
      return { privateData: privateData(refreshed), state: { status: "ready" } };
    } catch (error) {
      if (error instanceof OAuthError && error.invalidCredentials) {
        return { state: { status: "invalid", message: error.message } };
      }
      throw error;
    }
  },
};
