function normalizeChannelURL(value) {
  const text = String(value || "").trim().replace(/\/+$/, "");
  if (!text) {
    return "";
  }
  try {
    const parsed = new URL(text);
    parsed.protocol = parsed.protocol.toLowerCase();
    parsed.hostname = parsed.hostname.toLowerCase();
    return parsed.toString().replace(/\/+$/, "");
  } catch {
    return text.toLowerCase();
  }
}

function normalizeHeadersJSON(value) {
  const text = String(value || "").trim();
  if (!text) {
    return "";
  }
  try {
    const parsed = JSON.parse(text);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      return text;
    }
    const entries = Object.entries(parsed)
      .map(([key, item]) => [key.trim().toLowerCase(), item])
      .sort(([leftKey, leftValue], [rightKey, rightValue]) => {
        const keyOrder = leftKey.localeCompare(rightKey);
        return keyOrder || String(leftValue).localeCompare(String(rightValue));
      });
    return JSON.stringify(entries);
  } catch {
    return text;
  }
}

function buildChannelIdentity(adapter) {
  const customHeadersEnabled = Boolean(adapter?.customHeadersEnabled);
  return JSON.stringify([
    String(adapter?.type || "").trim().toLowerCase(),
    normalizeChannelURL(adapter?.baseURL),
    String(adapter?.apiKey || "").trim(),
    customHeadersEnabled,
    customHeadersEnabled ? normalizeHeadersJSON(adapter?.customHeadersJSON) : "",
  ]);
}

function hashStringFNV32a(value) {
  let hash = 0x811c9dc5;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return hash.toString(16).padStart(8, "0");
}

export function buildModelAdapterGroups(modelGroups, adapters) {
  if (adapters === undefined) {
    adapters = modelGroups;
    modelGroups = [];
  }
  const groupsByIdentity = new Map();
  for (const source of Array.isArray(modelGroups) ? modelGroups : []) {
    const groupID = String(source?.id || source?.groupID || "").trim();
    if (!groupID) {
      continue;
    }
    groupsByIdentity.set(`backend:${groupID}`, {
      key: groupID,
      groupID,
      name: String(source?.name || "").trim(),
      type: source?.type,
      baseURL: source?.baseURL,
      apiKey: source?.apiKey,
      openAIEndpoint: source?.openAIEndpoint,
      customHeadersEnabled: Boolean(source?.customHeadersEnabled),
      customHeadersJSON: source?.customHeadersJSON,
      adapters: [],
    });
  }
  for (const adapter of Array.isArray(adapters) ? adapters : []) {
    const backendGroupID = String(adapter?.groupID || "").trim();
    const identity = backendGroupID ? `backend:${backendGroupID}` : buildChannelIdentity(adapter);
    let group = groupsByIdentity.get(identity);
    if (!group) {
      group = {
        key: backendGroupID || `channel-${hashStringFNV32a(identity)}-${groupsByIdentity.size}`,
        groupID: backendGroupID,
        name: "",
        type: adapter.type,
        baseURL: adapter.baseURL,
        apiKey: adapter.apiKey,
        openAIEndpoint: adapter.openAIEndpoint,
        customHeadersEnabled: Boolean(adapter.customHeadersEnabled),
        customHeadersJSON: adapter.customHeadersJSON,
        adapters: [],
      };
      groupsByIdentity.set(identity, group);
    }
    group.adapters.push(adapter);
  }
  return Array.from(groupsByIdentity.values());
}

export function buildModelGroupBaseURL(address) {
  let text = String(address || "").trim();
  if (!text) {
    return { baseURL: "", error: "请求地址不能为空" };
  }
  if (!/^https?:\/\//i.test(text)) {
    text = `https://${text}`;
  }
  let parsed;
  try {
    parsed = new URL(text);
  } catch {
    return { baseURL: "", error: "请求地址格式不正确" };
  }
  if (!parsed.hostname || parsed.username || parsed.password || parsed.search || parsed.hash) {
    return { baseURL: "", error: "请求地址不能包含认证信息、查询参数或锚点" };
  }
  if (parsed.port && Number(parsed.port) < 1) {
    return { baseURL: "", error: "请求地址中的端口必须在 1-65535 之间" };
  }
  return { baseURL: parsed.toString().replace(/\/+$/, ""), error: "" };
}

export function modelAdaptersShareChannel(left, right) {
  const leftGroupID = String(left?.groupID || "").trim();
  const rightGroupID = String(right?.groupID || "").trim();
  if (leftGroupID && rightGroupID) {
    return leftGroupID === rightGroupID;
  }
  return buildChannelIdentity(left) === buildChannelIdentity(right);
}
