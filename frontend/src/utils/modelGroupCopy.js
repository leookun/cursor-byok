export const COPIED_MODEL_GROUP_ID_PREFIX = "grp_copy_";

export function isCopiedModelGroupID(value) {
  return new RegExp(`^${COPIED_MODEL_GROUP_ID_PREFIX}[A-Za-z0-9_-]+$`).test(String(value || "").trim());
}

function defaultIDFactory() {
  const randomUUID = globalThis.crypto?.randomUUID;
  if (typeof randomUUID === "function") {
    return `${COPIED_MODEL_GROUP_ID_PREFIX}${randomUUID.call(globalThis.crypto)}`;
  }
  return `${COPIED_MODEL_GROUP_ID_PREFIX}${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 10)}`;
}

export function createCopiedModelGroupID(existingIDs = [], { idFactory = defaultIDFactory } = {}) {
  const taken = new Set(
    (Array.isArray(existingIDs) ? existingIDs : [])
      .map((value) => String(value || "").trim())
      .filter(Boolean),
  );
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const raw = String(idFactory() || "").trim();
    const candidate = raw.startsWith(COPIED_MODEL_GROUP_ID_PREFIX)
      ? raw
      : `${COPIED_MODEL_GROUP_ID_PREFIX}${raw || attempt.toString(36)}`;
    if (isCopiedModelGroupID(candidate) && !taken.has(candidate)) {
      return candidate;
    }
  }
  throw new Error("无法生成唯一的分组副本 ID");
}

function collectNames(items, field = "name") {
  return new Set(
    (Array.isArray(items) ? items : [])
      .map((item) => String(item?.[field] || "").trim())
      .filter(Boolean),
  );
}

export function buildUniqueModelGroupName(existingGroups, sourceName) {
  const taken = collectNames(existingGroups);
  const base = String(sourceName || "").trim() || "分组";
  const first = `${base} - 副本`;
  if (!taken.has(first)) {
    return first;
  }
  let suffix = 2;
  while (taken.has(`${first} (${suffix})`)) {
    suffix += 1;
  }
  return `${first} (${suffix})`;
}

function buildUniqueModelDisplayName(existingAdapters, sourceName) {
  const taken = collectNames(existingAdapters, "displayName");
  const base = String(sourceName || "").trim() || "模型";
  const first = `${base} - 副本`;
  if (!taken.has(first)) {
    return first;
  }
  let suffix = 2;
  while (taken.has(`${first} (${suffix})`)) {
    suffix += 1;
  }
  return `${first} (${suffix})`;
}

export function buildModelGroupCopy(
  sourceGroup,
  sourceAdapters,
  existingGroups = [],
  existingAdapters = [],
  options = {},
) {
  const source = sourceGroup && typeof sourceGroup === "object" ? sourceGroup : {};
  const sourceModelAdapters = Array.isArray(sourceAdapters) ? sourceAdapters : [];
  const existingGroupIDs = (Array.isArray(existingGroups) ? existingGroups : [])
    .map((group) => group?.id || group?.groupID)
    .filter(Boolean);
  const copyID = createCopiedModelGroupID(existingGroupIDs, options);
  const { adapters: _ignoredAdapters, groupID: _ignoredGroupID, ...groupFields } = source;
  const copiedGroup = {
    ...groupFields,
    id: copyID,
    groupID: copyID,
    name: buildUniqueModelGroupName(existingGroups, source.name),
  };

  const copiedAdapters = [];
  const adaptersWithCopies = [...(Array.isArray(existingAdapters) ? existingAdapters : [])];
  for (const adapter of sourceModelAdapters) {
    const displayName = buildUniqueModelDisplayName(
      adaptersWithCopies,
      adapter?.displayName || adapter?.modelID,
    );
    copiedAdapters.push({
      ...adapter,
      id: "",
      groupID: copyID,
      displayName,
    });
    adaptersWithCopies.push({ displayName });
  }

  return {
    group: copiedGroup,
    adapters: copiedAdapters,
  };
}
