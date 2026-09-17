import { defineProviderPlugin } from "cursor-byok:plugin";
import { antigravityAuthorizationCodeOAuth } from "./oauth.ts";
import { antigravityProvider } from "./provider.ts";
import { credentialImport, presentAccount, refreshAccount, RESOURCE_TYPE } from "./resources.ts";

export default defineProviderPlugin({
  providers: [antigravityProvider],
  resources: [{
    type: RESOURCE_TYPE,
    displayName: {
      "en-US": "Google accounts & API keys",
      "zh-CN": "Google 账号与 API 密钥",
    },
    add: [antigravityAuthorizationCodeOAuth],
    import: credentialImport,
    present: presentAccount,
    refresh: refreshAccount,
    refreshIntervalMs: 5 * 60 * 1000,
  }],
});
