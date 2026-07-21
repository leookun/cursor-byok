// pluginApi.js — 与后端 Phase 8 Plugin Marketplace REST API 通信。
// 后端监听地址来自 appState.backendListenAddr（默认 127.0.0.1:18090）。
import { appState } from "@/state/appState";

function baseURL() {
  const addr = (appState.backendListenAddr || "").trim();
  return `http://${addr || "127.0.0.1:18090"}`;
}

async function request(method, path, body) {
  const url = `${baseURL()}${path}`;
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(url, opts);
  const text = await res.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }
  if (!res.ok) {
    const msg = (data && (data.error || data.message)) || `HTTP ${res.status}`;
    throw new Error(String(msg));
  }
  return data;
}

export function listPlugins() {
  return request("GET", "/api/plugins");
}

export function installPlugin(name) {
  return request("POST", `/api/plugins/${encodeURIComponent(name)}/install`, {});
}

export function uninstallPlugin(name) {
  return request("POST", `/api/plugins/${encodeURIComponent(name)}/uninstall`);
}

export function togglePlugin(name) {
  return request("POST", `/api/plugins/${encodeURIComponent(name)}/toggle`);
}

export function callPlugin(name, input = {}) {
  return request("POST", `/api/plugins/${encodeURIComponent(name)}/call`, { input });
}