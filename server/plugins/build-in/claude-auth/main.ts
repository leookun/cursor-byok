import { defineProviderPlugin } from "cursor-byok:plugin";
import { claudeProvider } from "./provider.ts";
import { claudeAccounts } from "./resources.ts";

export default defineProviderPlugin({
  providers: [claudeProvider],
  resources: [claudeAccounts],
});
