import { defineProviderPlugin } from "cursor-byok:plugin";
import { opencodeProvider } from "./provider.ts";

export default defineProviderPlugin({
  providers: [opencodeProvider],
});
