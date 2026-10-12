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
        "en-US": "Full Claude subscription account.",
        "ru-RU": "Полноценный аккаунт подписки Claude.",
        "zh-CN": "完整的 Claude 订阅账号。",
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
