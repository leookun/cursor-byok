const SOURCE_LOCALE = "zh-CN";
const DEFAULT_LOCALE = "en-US";
const STORAGE_KEY = "cursor-proxy-debugger:locale:v1";
const SUPPORTED_LOCALES = [SOURCE_LOCALE, DEFAULT_LOCALE];

const messages = {
  "zh-CN": {
    "app.title": "Cursor 协议调试器",
    "status.connecting": "正在连接",
    "status.running": "代理运行中",
    "status.stopped": "代理已停止",
    "actions.downloadCA": "下载代理 CA 证书",
    "actions.caCertificate": "CA 证书",
    "actions.pause": "暂停界面更新",
    "actions.resume": "继续界面更新",
    "actions.clear": "清空",
    "actions.copy": "复制",
    "actions.copied": "已复制",
    "language.label": "界面语言",
    "filters.region": "请求过滤器",
    "filters.urlPlaceholder": "过滤 URL、请求类型或状态",
    "filters.requestIdPlaceholder": "按 Request ID 过滤",
    "filters.endpoint": "接口过滤",
    "filters.all": "全部",
    "filters.sort": "排序方向",
    "filters.ascending": "正序",
    "filters.descending": "倒序",
    "count.requests": "{count} 条",
    "table.url": "网址",
    "table.message": "消息",
    "table.method": "方法",
    "table.status": "状态",
    "table.response": "响应",
    "table.duration": "耗时",
    "empty.waitingForCursor": "等待来自 Cursor 的请求",
    "splitter.resize": "调整详情区域高度",
    "selection.waiting": "等待选择",
    "selection.prompt": "选择一条请求查看详情",
    "panel.request": "请求",
    "panel.response": "响应",
    "panel.requestDetails": "请求详情",
    "panel.responseDetails": "响应详情",
    "tabs.headers": "标头",
    "tabs.body": "正文",
    "tabs.frames": "帧",
    "tabs.raw": "原始",
    "notices.noRequest": "暂无请求内容",
    "notices.noResponse": "暂无响应内容",
    "notices.noContent": "暂无内容",
    "notices.noRaw": "暂无原始数据",
    "notices.noBody": "暂无可显示的正文",
    "notices.noHeaders": "暂无标头",
    "notices.noFrames": "尚未收到完整帧",
    "notices.unknown": "未识别",
    "notices.truncated": "原始正文已达到本地抓取上限，转发内容未被截断",
    "connection.live": "实时连接中",
    "connection.retrying": "实时连接正在重试",
    "connection.refreshFailed": "刷新失败：{message}",
    "connection.paused": "界面更新已暂停",
    "connection.connectFailed": "连接失败：{message}",
    "state.pending": "等待中",
    "state.streaming": "传输中",
    "state.completed": "已完成",
    "state.error": "错误",
  },
  "en-US": {
    "app.title": "Cursor Protocol Debugger",
    "status.connecting": "Connecting",
    "status.running": "Proxy running",
    "status.stopped": "Proxy stopped",
    "actions.downloadCA": "Download proxy CA certificate",
    "actions.caCertificate": "CA Certificate",
    "actions.pause": "Pause UI updates",
    "actions.resume": "Resume UI updates",
    "actions.clear": "Clear",
    "actions.copy": "Copy",
    "actions.copied": "Copied",
    "language.label": "Interface language",
    "filters.region": "Request filters",
    "filters.urlPlaceholder": "Filter by URL, message type, or status",
    "filters.requestIdPlaceholder": "Filter by Request ID",
    "filters.endpoint": "Endpoint filter",
    "filters.all": "All",
    "filters.sort": "Sort order",
    "filters.ascending": "Oldest first",
    "filters.descending": "Newest first",
    "count.requests": "{count} requests",
    "table.url": "URL",
    "table.message": "Message",
    "table.method": "Method",
    "table.status": "Status",
    "table.response": "Response",
    "table.duration": "Duration",
    "empty.waitingForCursor": "Waiting for requests from Cursor",
    "splitter.resize": "Resize details area",
    "selection.waiting": "No selection",
    "selection.prompt": "Select a request to inspect its details",
    "panel.request": "Request",
    "panel.response": "Response",
    "panel.requestDetails": "Request details",
    "panel.responseDetails": "Response details",
    "tabs.headers": "Headers",
    "tabs.body": "Body",
    "tabs.frames": "Frames",
    "tabs.raw": "Raw",
    "notices.noRequest": "No request content",
    "notices.noResponse": "No response content",
    "notices.noContent": "No content",
    "notices.noRaw": "No raw data",
    "notices.noBody": "No body available",
    "notices.noHeaders": "No headers",
    "notices.noFrames": "No complete frames received yet",
    "notices.unknown": "Unknown",
    "notices.truncated": "Raw body reached the local capture limit; forwarded data was not truncated",
    "connection.live": "Live connection",
    "connection.retrying": "Reconnecting live updates",
    "connection.refreshFailed": "Refresh failed: {message}",
    "connection.paused": "UI updates paused",
    "connection.connectFailed": "Connection failed: {message}",
    "state.pending": "Pending",
    "state.streaming": "Streaming",
    "state.completed": "Completed",
    "state.error": "Error",
  },
};

function matchLocale(locale) {
  const normalized = String(locale || "").trim().replaceAll("_", "-").toLowerCase();
  if (!normalized) return "";
  const exact = SUPPORTED_LOCALES.find((candidate) => candidate.toLowerCase() === normalized);
  if (exact) return exact;
  return normalized.split("-")[0] === "zh" ? SOURCE_LOCALE : normalized.split("-")[0] === "en" ? DEFAULT_LOCALE : "";
}

function resolveInitialLocale() {
  const stored = matchLocale(window.localStorage.getItem(STORAGE_KEY));
  if (stored) return stored;
  for (const candidate of navigator.languages || [navigator.language]) {
    const matched = matchLocale(candidate);
    if (matched) return matched;
  }
  return DEFAULT_LOCALE;
}

let currentLocale = resolveInitialLocale();

export function getLocale() {
  return currentLocale;
}

export function t(key, values = {}) {
  const template = messages[currentLocale]?.[key] || messages[SOURCE_LOCALE][key] || key;
  return template.replace(/\{(\w+)\}/g, (_match, name) => String(values[name] ?? ""));
}

export function translateDocument(root = document) {
  document.documentElement.lang = currentLocale;
  document.title = t("app.title");
  for (const element of root.querySelectorAll("[data-i18n]")) {
    element.textContent = t(element.dataset.i18n);
  }
  for (const [attribute, dataAttribute] of [
    ["aria-label", "i18nAriaLabel"],
    ["placeholder", "i18nPlaceholder"],
    ["title", "i18nTitle"],
  ]) {
    for (const element of root.querySelectorAll(`[data-${dataAttribute.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)}]`)) {
      element.setAttribute(attribute, t(element.dataset[dataAttribute]));
    }
  }
}

export function setLocale(locale) {
  currentLocale = matchLocale(locale) || DEFAULT_LOCALE;
  window.localStorage.setItem(STORAGE_KEY, currentLocale);
  translateDocument();
  return currentLocale;
}
