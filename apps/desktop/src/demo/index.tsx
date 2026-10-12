import { installDemoApi } from "./api";

installDemoApi();

const params = new URLSearchParams(window.location.search);
const requestedLocale = params.get("locale");
const locale = requestedLocale === "en-US" || requestedLocale === "pt-BR" ? requestedLocale : "zh-CN";
const theme = params.get("theme") === "default-light" ? "default-light" : "default-dark";

document.documentElement.dataset.platform = "macos";
localStorage.setItem("cursor-byok.locale", locale);
localStorage.setItem("cursor-byok.theme", theme);

void import("../index");
