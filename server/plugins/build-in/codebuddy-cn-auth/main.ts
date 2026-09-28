import { defineProviderPlugin } from "cursor-byok:plugin";
import { checkInAction } from "./checkin.ts";
import { codeBuddyOAuth } from "./oauth.ts";
import { codeBuddyProvider } from "./provider.ts";
import { credentialImport, presentAccount, refreshAccount, RESOURCE_TYPE } from "./resources.ts";

export default defineProviderPlugin({
  providers: [codeBuddyProvider],
  resources: [{
    type: RESOURCE_TYPE,
    displayName: { "en-US": "CodeBuddy CN accounts", "zh-CN": "CodeBuddy CN 账号" },
    add: [codeBuddyOAuth],
    import: credentialImport,
    present: presentAccount,
    actions: [checkInAction],
    refresh: refreshAccount,
    export: { enabled: true },
  }],
});
