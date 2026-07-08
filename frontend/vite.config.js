import vue from "@vitejs/plugin-vue";
import vueJsx from "@vitejs/plugin-vue-jsx";
import wails from "@wailsio/runtime/plugins/vite";
import { codeInspectorPlugin } from "code-inspector-plugin";
import path from "path";
import { defineConfig } from "vite";
import topLevelAwait from "vite-plugin-top-level-await";
import { staticI18nPlugin } from "./plugins/static-i18n-plugin.js";

const isDev = process.env.NODE_ENV === "development";

// https://vitejs.dev/config/
export default defineConfig({
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
      "@bindings": path.resolve(__dirname, "./bindings"),
    },
  },
  build: {
    target: ["es2019", "safari13"],
    cssTarget: "safari13",
  },
  plugins: [
    isDev &&
      codeInspectorPlugin({
        bundler: "vite",
        editor: "code",
        hotKeys: ["ctrlKey"],
      }),
    wails("./bindings"),
    topLevelAwait(),
    staticI18nPlugin(),
    vue(),
    vueJsx(),
  ].filter(Boolean),
});
