import { getLocale, setLocale, t, translateDocument } from "./i18n.js";

const state = {
  status: null,
  exchanges: [],
  selectedId: null,
  selected: null,
  search: "",
  requestId: "",
  endpoint: "all",
  sortOrder: "desc",
  paused: false,
  pendingRefresh: false,
  connection: { connected: false, key: "status.connecting", values: {} },
  tabs: {
    request: "body",
    response: "frames",
  },
};

const elements = {
  statusDot: document.querySelector("#status-dot"),
  statusText: document.querySelector("#status-text"),
  proxyAddress: document.querySelector("#proxy-address"),
  targetHost: document.querySelector("#target-host"),
  connectionLabel: document.querySelector("#connection-label"),
  trafficSummary: document.querySelector("#traffic-summary"),
  searchInput: document.querySelector("#search-input"),
  requestIdInput: document.querySelector("#request-id-input"),
  endpointFilter: document.querySelector("#endpoint-filter"),
  sortOrder: document.querySelector("#sort-order"),
  requestCount: document.querySelector("#request-count"),
  requestList: document.querySelector("#request-list"),
  emptyState: document.querySelector("#empty-state"),
  selectionSummary: document.querySelector("#selection-summary"),
  requestContent: document.querySelector("#request-content"),
  responseContent: document.querySelector("#response-content"),
  pauseButton: document.querySelector("#pause-button"),
  clearButton: document.querySelector("#clear-button"),
  localeSelect: document.querySelector("#locale-select"),
  workspace: document.querySelector("#workspace"),
  splitter: document.querySelector("#horizontal-splitter"),
};

async function fetchJSON(url, options) {
  const response = await fetch(url, options);
  if (!response.ok) {
    throw new Error(`${response.status} ${response.statusText}`);
  }
  if (response.status === 204) return null;
  return response.json();
}

async function loadStatus() {
  state.status = await fetchJSON("/api/status");
  elements.statusDot.classList.toggle("online", Boolean(state.status.running));
  renderRuntimeStatus();
  elements.proxyAddress.textContent = `http://${state.status.proxyAddr}`;
  elements.targetHost.textContent = state.status.targetHost;
}

async function refreshList() {
  state.exchanges = await fetchJSON("/api/exchanges");
  renderList();
  renderTrafficSummary();
  if (state.selectedId && state.exchanges.some((item) => item.id === state.selectedId)) {
    await refreshDetail(state.selectedId);
  } else if (state.selectedId) {
    state.selectedId = null;
    state.selected = null;
    renderDetail();
  }
}

async function refreshDetail(id) {
  if (!id) return;
  try {
    const detail = await fetchJSON(`/api/exchanges/${encodeURIComponent(id)}`);
    if (state.selectedId !== id) return;
    state.selected = detail;
    renderDetail();
  } catch (error) {
    if (state.selectedId === id) {
      state.selected = null;
      renderDetailError(error);
    }
  }
}

function scheduleRefresh() {
  if (state.paused) {
    state.pendingRefresh = true;
    return;
  }
  if (state.pendingRefresh) return;
  state.pendingRefresh = true;
  window.setTimeout(async () => {
    state.pendingRefresh = false;
    try {
      await refreshList();
    } catch (error) {
      setConnectionState(false, "connection.refreshFailed", { message: error.message });
    }
  }, 90);
}

function connectEvents() {
  const events = new EventSource("/api/events");
  events.addEventListener("open", () => setConnectionState(true, "connection.live"));
  events.addEventListener("update", scheduleRefresh);
  events.addEventListener("error", () => setConnectionState(false, "connection.retrying"));
}

function setConnectionState(connected, key, values = {}) {
  state.connection = { connected, key, values };
  renderConnectionState();
}

function renderRuntimeStatus() {
  if (!state.status) {
    elements.statusText.textContent = t("status.connecting");
    return;
  }
  elements.statusText.textContent = t(state.status.running ? "status.running" : "status.stopped");
}

function renderConnectionState() {
  const { connected, key, values } = state.connection;
  elements.connectionLabel.textContent = t(key, values);
  elements.statusDot.classList.toggle("online", connected && Boolean(state.status?.running));
}

function filteredExchanges() {
  const query = state.search.trim().toLowerCase();
  const requestId = state.requestId.trim().toLowerCase();
  const direction = state.sortOrder === "asc" ? 1 : -1;
  return state.exchanges
    .filter((item) => {
      if (state.endpoint === "runsse" && !item.path.toLowerCase().includes("runsse")) return false;
      if (state.endpoint === "bidiappend" && !item.path.toLowerCase().includes("bidiappend")) return false;
      if (state.endpoint === "fork" && !isForkTrafficPath(item.path)) return false;
      if (requestId && !String(item.requestId || "").toLowerCase().includes(requestId)) return false;
      if (!query) return true;
      return [item.url, item.requestId, item.requestKind, item.responseKind, item.state, String(item.status)]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(query));
    })
    .sort((left, right) => {
      const startedAtDelta = new Date(left.startedAt).getTime() - new Date(right.startedAt).getTime();
      if (startedAtDelta !== 0) return startedAtDelta * direction;
      return left.id.localeCompare(right.id, undefined, { numeric: true }) * direction;
    });
}

function isForkTrafficPath(path) {
  const normalized = String(path || "").toLowerCase();
  return ["forkbackgroundcomposer", "notifyconversationclone", "uploadconversationblobs"].some((endpoint) =>
    normalized.includes(endpoint),
  );
}

function renderList() {
  const exchanges = filteredExchanges();
  elements.requestCount.textContent = t("count.requests", { count: exchanges.length });
  elements.emptyState.classList.toggle("hidden", exchanges.length > 0);
  elements.requestList.innerHTML = exchanges
    .map((item) => {
      const selected = item.id === state.selectedId ? " selected" : "";
      const statusClass = item.status >= 400 ? "error" : item.status ? "success" : "";
      const kind = item.responseKind || item.requestKind || "-";
      return `<tr class="${selected.trim()}" data-id="${escapeHTML(item.id)}">
        <td><span class="row-state ${escapeHTML(item.state)}"></span></td>
        <td><code>${escapeHTML(item.id)}</code></td>
        <td title="${escapeHTML(item.url)}"><code>${escapeHTML(item.url)}</code></td>
        <td title="${escapeHTML(item.requestId || "")}"><code class="request-id-text">${escapeHTML(item.requestId || "-")}</code></td>
        <td><span class="kind-text">${escapeHTML(kind)}</span></td>
        <td><span class="method-text">${escapeHTML(item.method)}</span></td>
        <td><span class="status-text ${statusClass}">${item.status || "-"}</span></td>
        <td>${formatBytes(item.responseBytes)}</td>
        <td>${formatDuration(item.durationMs)}</td>
      </tr>`;
    })
    .join("");
}

function renderTrafficSummary() {
  const totals = state.exchanges.reduce(
    (result, item) => {
      result.up += item.requestBytes || 0;
      result.down += item.responseBytes || 0;
      return result;
    },
    { up: 0, down: 0 },
  );
  elements.trafficSummary.textContent = `↑ ${formatBytes(totals.up)}　↓ ${formatBytes(totals.down)}`;
}

function renderDetail() {
  if (!state.selected) {
    elements.selectionSummary.innerHTML = `<span class="method-badge">POST</span><span class="status-badge">${escapeHTML(t("selection.waiting"))}</span><code>${escapeHTML(t("selection.prompt"))}</code>`;
    elements.requestContent.innerHTML = `<div class="notice">${escapeHTML(t("notices.noRequest"))}</div>`;
    elements.responseContent.innerHTML = `<div class="notice">${escapeHTML(t("notices.noResponse"))}</div>`;
    return;
  }
  const item = state.selected;
  const statusClass = item.status >= 200 && item.status < 400 ? "success" : "";
  elements.selectionSummary.innerHTML = `<span class="method-badge">${escapeHTML(item.method)}</span><span class="status-badge ${statusClass}">${escapeHTML(item.status || formatState(item.state))}</span><code>${escapeHTML(item.url)}</code>`;
  elements.requestContent.innerHTML = renderPayload(item.request, state.tabs.request);
  elements.responseContent.innerHTML = renderPayload(item.response, state.tabs.response);
}

function renderDetailError(error) {
  elements.requestContent.innerHTML = `<div class="notice error">${escapeHTML(error.message)}</div>`;
  elements.responseContent.innerHTML = `<div class="notice error">${escapeHTML(error.message)}</div>`;
}

function renderPayload(payload, tab) {
  if (!payload) return `<div class="notice">${escapeHTML(t("notices.noContent"))}</div>`;
  if (tab === "headers") return renderHeaders(payload.headers);
  if (tab === "frames") return renderFrames(payload.frames);
  if (tab === "raw") {
    const body = payload.rawHex ? formatHex(payload.rawHex) : t("notices.noRaw");
    return `<pre class="hex-view">${escapeHTML(body)}</pre>${renderTruncated(payload.rawTruncated)}`;
  }
  if (payload.decodedJson) {
    return `<pre class="code-view">${escapeHTML(payload.decodedJson)}</pre>${renderDecodeError(payload.decodeError)}${renderTruncated(payload.rawTruncated)}`;
  }
  if (payload.frames?.length) return renderFrames(payload.frames);
  if (payload.decodeError) return `<div class="notice error">${escapeHTML(payload.decodeError)}</div>`;
  return `<div class="notice">${escapeHTML(t("notices.noBody"))}</div>`;
}

function renderHeaders(headers = []) {
  const items = Array.isArray(headers) ? headers : [];
  if (!items.length) return `<div class="notice">${escapeHTML(t("notices.noHeaders"))}</div>`;
  return `<table class="headers-table"><tbody>${items
    .map((header) => `<tr><th>${escapeHTML(header.name)}</th><td>${escapeHTML(header.value)}</td></tr>`)
    .join("")}</tbody></table>`;
}

function renderFrames(frames = []) {
  const items = Array.isArray(frames) ? frames : [];
  if (!items.length) return `<div class="notice">${escapeHTML(t("notices.noFrames"))}</div>`;
  return `<div class="frame-list">${items
    .map((frame) => {
      const kind = frame.kind || frame.messageType || t("notices.unknown");
      const flags = `0x${Number(frame.flags || 0).toString(16).padStart(2, "0")}`;
      const content = frame.json
        ? `<pre class="code-view">${escapeHTML(frame.json)}</pre>`
        : `<pre class="hex-view">${escapeHTML(formatHex(frame.rawHex || ""))}</pre>`;
      return `<details class="frame-item"${frame.index === items.length - 1 ? " open" : ""}>
        <summary>
          <span class="frame-index">#${frame.index}</span>
          <span class="frame-kind" title="${escapeHTML(kind)}">${escapeHTML(kind)}</span>
          <span class="frame-size">${formatBytes(frame.length)}</span>
          <span class="frame-flags">${flags}${frame.compressed ? " gzip" : ""}</span>
        </summary>
        ${frame.error ? `<div class="frame-error">${escapeHTML(frame.error)}</div>` : content}
      </details>`;
    })
    .join("")}</div>`;
}

function renderDecodeError(error) {
  return error ? `<div class="frame-error">${escapeHTML(error)}</div>` : "";
}

function renderTruncated(truncated) {
  return truncated ? `<div class="truncated-notice">${escapeHTML(t("notices.truncated"))}</div>` : "";
}

function formatState(value) {
  const key = {
    pending: "state.pending",
    streaming: "state.streaming",
    completed: "state.completed",
    error: "state.error",
  }[value];
  return key ? t(key) : value || "-";
}

function currentCopyText(side) {
  const payload = state.selected?.[side];
  if (!payload) return "";
  const tab = state.tabs[side];
  if (tab === "headers") return (payload.headers || []).map((item) => `${item.name}: ${item.value}`).join("\n");
  if (tab === "raw") return payload.rawHex || "";
  if (tab === "frames") return (payload.frames || []).map((frame) => frame.json || frame.rawHex || frame.error || "").join("\n\n");
  return payload.decodedJson || "";
}

function formatHex(value) {
  const hex = String(value || "").replace(/[^0-9a-f]/gi, "");
  const lines = [];
  for (let index = 0; index < hex.length; index += 32) {
    const chunk = hex.slice(index, index + 32);
    const bytes = chunk.match(/.{1,2}/g) || [];
    lines.push(`${(index / 2).toString(16).padStart(8, "0")}  ${bytes.join(" ")}`);
  }
  return lines.join("\n");
}

function formatBytes(value) {
  const bytes = Number(value || 0);
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function formatDuration(value) {
  const milliseconds = Number(value || 0);
  if (milliseconds < 1000) return `${milliseconds} ms`;
  return `${(milliseconds / 1000).toFixed(1)} s`;
}

function escapeHTML(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

elements.requestList.addEventListener("click", async (event) => {
  const row = event.target.closest("tr[data-id]");
  if (!row) return;
  state.selectedId = row.dataset.id;
  state.selected = null;
  renderList();
  renderDetail();
  await refreshDetail(state.selectedId);
});

elements.searchInput.addEventListener("input", (event) => {
  state.search = event.target.value;
  renderList();
});

elements.requestIdInput.addEventListener("input", (event) => {
  state.requestId = event.target.value;
  renderList();
});

elements.endpointFilter.addEventListener("click", (event) => {
  const button = event.target.closest("button[data-value]");
  if (!button) return;
  state.endpoint = button.dataset.value;
  for (const item of elements.endpointFilter.querySelectorAll("button")) {
    item.classList.toggle("active", item === button);
  }
  renderList();
});

elements.sortOrder.addEventListener("click", (event) => {
  const button = event.target.closest("button[data-value]");
  if (!button) return;
  state.sortOrder = button.dataset.value;
  for (const item of elements.sortOrder.querySelectorAll("button")) {
    item.classList.toggle("active", item === button);
  }
  renderList();
});

document.querySelectorAll(".payload-panel").forEach((panel) => {
  panel.querySelector(".tabs").addEventListener("click", (event) => {
    const button = event.target.closest("button[data-tab]");
    if (!button) return;
    const side = panel.dataset.side;
    state.tabs[side] = button.dataset.tab;
    panel.querySelectorAll(".tabs button").forEach((item) => item.classList.toggle("active", item === button));
    renderDetail();
  });
});

document.querySelectorAll("[data-copy-side]").forEach((button) => {
  button.addEventListener("click", async () => {
    const text = currentCopyText(button.dataset.copySide);
    if (!text) return;
    await navigator.clipboard.writeText(text);
    button.textContent = t("actions.copied");
    window.setTimeout(() => {
      button.textContent = t("actions.copy");
    }, 900);
  });
});

function renderPauseState() {
  elements.pauseButton.textContent = state.paused ? "▶" : "Ⅱ";
  const actionKey = state.paused ? "actions.resume" : "actions.pause";
  elements.pauseButton.title = t(actionKey);
  elements.pauseButton.setAttribute("aria-label", t(actionKey));
}

elements.pauseButton.addEventListener("click", async () => {
  state.paused = !state.paused;
  elements.pauseButton.classList.toggle("active", state.paused);
  renderPauseState();
  setConnectionState(!state.paused, state.paused ? "connection.paused" : "connection.live");
  if (!state.paused && state.pendingRefresh) {
    state.pendingRefresh = false;
    await refreshList();
  }
});

function applyLocale() {
  translateDocument();
  elements.localeSelect.value = getLocale();
  renderRuntimeStatus();
  renderConnectionState();
  renderPauseState();
  renderList();
  renderTrafficSummary();
  renderDetail();
}

elements.localeSelect.addEventListener("change", (event) => {
  setLocale(event.target.value);
  applyLocale();
});

elements.clearButton.addEventListener("click", async () => {
  await fetchJSON("/api/exchanges", { method: "DELETE" });
  state.selectedId = null;
  state.selected = null;
  await refreshList();
  renderDetail();
});

let draggingSplitter = false;
elements.splitter.addEventListener("pointerdown", (event) => {
  draggingSplitter = true;
  elements.splitter.classList.add("dragging");
  elements.splitter.setPointerCapture(event.pointerId);
});

elements.splitter.addEventListener("pointermove", (event) => {
  if (!draggingSplitter) return;
  const bounds = elements.workspace.getBoundingClientRect();
  const top = Math.max(180, Math.min(bounds.height - 225, event.clientY - bounds.top));
  elements.workspace.style.gridTemplateRows = `${top}px 5px minmax(220px, 1fr)`;
});

elements.splitter.addEventListener("pointerup", () => {
  draggingSplitter = false;
  elements.splitter.classList.remove("dragging");
});

async function bootstrap() {
  applyLocale();
  renderDetail();
  try {
    await Promise.all([loadStatus(), refreshList()]);
    connectEvents();
  } catch (error) {
    setConnectionState(false, "connection.connectFailed", { message: error.message });
  }
}

void bootstrap();
