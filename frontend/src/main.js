import { createApp } from "vue";
import ResizeObserver from "resize-observer-polyfill";
import App from "@/App.vue";
import { installI18nRuntime } from "@/i18n/runtime";
import router from "@/router";
import { bootstrapAppState } from "@/state/appState";
import "@/style/global.css";
import "@/style/tailwind.css";

if (typeof window !== "undefined" && typeof window.ResizeObserver === "undefined") {
  window.ResizeObserver = ResizeObserver;
}

function updateFlexGapSupportClass() {
  if (typeof document === "undefined" || !document.body) {
    return;
  }
  const flex = document.createElement("div");
  flex.style.position = "absolute";
  flex.style.visibility = "hidden";
  flex.style.display = "flex";
  flex.style.flexDirection = "column";
  flex.style.rowGap = "1px";
  flex.appendChild(document.createElement("div"));
  flex.appendChild(document.createElement("div"));
  document.body.appendChild(flex);
  document.documentElement.classList.toggle("no-flex-gap", flex.scrollHeight !== 1);
  flex.parentNode?.removeChild(flex);
}

updateFlexGapSupportClass();

const app = createApp(App);
installI18nRuntime(app);
app.use(router);
app.mount("#root");

bootstrapAppState().catch(() => {
  // 启动阶段失败时保持界面可用，错误在业务交互中再提示。
});
