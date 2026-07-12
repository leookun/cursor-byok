import { defineConfig } from "vitest/config";
import path from "path";

export default defineConfig({
  test: {
    environment: "jsdom",
    globals: true,
    include: ["src/**/*.test.{js,ts}"],
    coverage: {
      provider: "v8",
      include: ["src/pet/**/*.js"],
      exclude: ["src/pet/resourceLoader.js"],
    },
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
      "@bindings": path.resolve(__dirname, "./bindings"),
    },
  },
});
