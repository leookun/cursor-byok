import { defineProviderPlugin } from "cursor-byok:plugin";
import { grokDeviceOAuth } from "./oauth.ts";
import { grokProvider } from "./provider.ts";
import {
  credentialImport,
  prepareAccount,
  presentAccount,
  refreshAccount,
  RESOURCE_TYPE,
} from "./resources.ts";

export default defineProviderPlugin({
  providers: [grokProvider],
  resources: [{
    type: RESOURCE_TYPE,
    displayName: { "en-US": "Grok accounts", "zh-CN": "Grok 账号" },
    add: [grokDeviceOAuth],
    import: credentialImport,
    present: presentAccount,
    refresh: refreshAccount,
    prepare: prepareAccount,
  }],
});
