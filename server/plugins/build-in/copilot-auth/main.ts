import { defineProviderPlugin } from "cursor-byok:plugin";
import { githubDeviceOAuth } from "./oauth.ts";
import { copilotProvider } from "./provider.ts";
import { presentAccount, refreshAccount, RESOURCE_TYPE } from "./resources.ts";

export default defineProviderPlugin({
  providers: [copilotProvider],
  resources: [{
    type: RESOURCE_TYPE,
    displayName: { "en-US": "GitHub Copilot accounts", "zh-CN": "GitHub Copilot 账号" },
    add: [githubDeviceOAuth],
    present: presentAccount,
    refresh: refreshAccount,
  }],
});
