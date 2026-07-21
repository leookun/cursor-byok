// workflowApi.js — 与后端工作流 REST API 通信。
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

export function listWorkflows() {
  return request("GET", "/api/workflows");
}

export function getWorkflow(id) {
  return request("GET", `/api/workflows/${encodeURIComponent(id)}`);
}

export function createWorkflow(workflow) {
  return request("POST", "/api/workflows", workflow);
}

export function updateWorkflow(id, workflow) {
  return request("PUT", `/api/workflows/${encodeURIComponent(id)}`, workflow);
}

export function deleteWorkflow(id) {
  return request("DELETE", `/api/workflows/${encodeURIComponent(id)}`);
}

export function executeWorkflow(id, input = {}) {
  return request("POST", `/api/workflows/${encodeURIComponent(id)}/execute`, { input });
}