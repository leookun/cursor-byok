import { defineProviderPlugin } from "cursor-byok:plugin";
import { codeBuddyOAuth } from "./oauth.ts";
import { codeBuddyModels } from "./models.ts";
import { codeBuddyProvider } from "./provider.ts";
import { presentAccount, refreshAccount, RESOURCE_TYPE } from "./resources.ts";

export default defineProviderPlugin({
  providers: [{ ...codeBuddyProvider, models: codeBuddyModels }],
  resources: [{
    type: RESOURCE_TYPE,
    displayName: { "en-US": "CodeBuddy accounts", "zh-CN": "CodeBuddy 账号" },
    add: [codeBuddyOAuth],
    present: presentAccount,
    refresh: refreshAccount,
  }],
});
