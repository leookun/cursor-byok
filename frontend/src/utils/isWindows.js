import { IsWindows } from "@bindings/cursor/internal/bridge/proxyservice.js";
import { ref } from "vue";

export const isWindows = ref(Boolean(await IsWindows()));
