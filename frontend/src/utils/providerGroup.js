/**
 * providerGroup.js — 按 baseURL + type 将扁平 modelAdapters 分组为 Provider 列表。
 * 支持多 API Key：同一 baseURL+type 下的所有 adapter 归为同一供应商，
 * keys 从 appState.providers（落盘源）或 adapter 去重聚合。
 */

/**
 * @param {string} baseURL
 * @returns {string}
 */
export function extractProviderHost(baseURL) {
  const text = String(baseURL || "").trim();
  if (!text) return "未知供应商";
  try {
    return new URL(text).host || text;
  } catch {
    return text.replace(/^https?:\/\//, "").split("/")[0] || text;
  }
}

/**
 * 分组键：同一供应商 = 同一 baseURL + type（不再包含 apiKey，以支持多 Key）
 * @param {object} adapter
 */
export function providerKey(adapter) {
  return [
    String(adapter?.baseURL || "").trim(),
    String(adapter?.type || "openai").trim().toLowerCase(),
  ].join("\n");
}

/**
 * 从 appState.providers 查找匹配的 keys 列表。
 * @param {Array} providers - appState.providers
 * @param {string} baseURL
 * @param {string} type
 * @returns {{ keys: string[], name: string }}
 */
function findProviderMatch(providers, baseURL, type) {
  if (!Array.isArray(providers)) return { keys: [], name: "" };
  const match = providers.find(
    (p) =>
      String(p?.baseURL || "").trim() === String(baseURL || "").trim() &&
      String(p?.type || "").trim().toLowerCase() === String(type || "").trim().toLowerCase(),
  );
  if (!match) return { keys: [], name: "" };
  const keys = (Array.isArray(match.apiKeys) && match.apiKeys.length > 0)
    ? match.apiKeys
    : match.apiKey
      ? [match.apiKey]
      : [];
  return { keys, name: match.name || "" };
}

/**
 * @param {Array} adapters
 * @param {Array} [providers] - appState.providers (optional, for key lookup)
 * @returns {Array<{host, name, baseURL, type, keys: string[], models: Array, key: string}>}
 */
export function groupAdaptersByProvider(adapters, providers) {
  const order = [];
  const map = new Map();

  for (const adapter of adapters || []) {
    const key = providerKey(adapter);
    if (!map.has(key)) {
      const baseURL = String(adapter.baseURL || "").trim();
      const type = String(adapter.type || "openai").trim().toLowerCase();
      // Collect unique keys: prefer providers source, fallback to adapter keys.
      const { keys: providerKeys, name: providerName } = findProviderMatch(providers, baseURL, type);
      const host = extractProviderHost(baseURL);
      const entry = {
        key,
        host,
        name: providerName || host,
        baseURL,
        type,
        keys: providerKeys.length > 0 ? providerKeys : [adapter.apiKey || ""],
        models: [],
      };
      map.set(key, entry);
      order.push(key);
    }
    map.get(key).models.push(adapter);
  }

  return order.map((k) => map.get(k));
}
