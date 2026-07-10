import { modelAdaptersShareChannel } from "./modelAdapterGroups.js";

function asText(value) {
  return typeof value === "string" ? value.trim() : "";
}

export function buildDiscoveredModelAdditions(existingAdapters, template, discoveredModels) {
  const existingModelIDs = new Set(
    (Array.isArray(existingAdapters) ? existingAdapters : [])
      .filter((item) => modelAdaptersShareChannel(item, template))
      .map((item) => asText(item?.modelID))
      .filter(Boolean),
  );
  const normalizedDiscovered = [];
  const additions = [];
  const seenDiscovered = new Set();
  for (const raw of Array.isArray(discoveredModels) ? discoveredModels : []) {
    const id = asText(raw?.id);
    if (!id || seenDiscovered.has(id)) {
      continue;
    }
    seenDiscovered.add(id);
    const displayName = asText(raw?.displayName) || id;
    normalizedDiscovered.push({ id, displayName });
    if (existingModelIDs.has(id)) {
      continue;
    }
    existingModelIDs.add(id);
    additions.push({
      ...template,
      id: "",
      displayName,
      tooltipData: displayName,
      modelID: id,
    });
  }
  return {
    discovered: normalizedDiscovered.length,
    skipped: normalizedDiscovered.length - additions.length,
    additions,
  };
}

export function resolveCurrentDiscoveryTemplate(existingAdapters, requestedTemplate) {
  const adapters = Array.isArray(existingAdapters) ? existingAdapters : [];
  const requestedID = asText(requestedTemplate?.id);
  const exact = requestedID ? adapters.find((item) => asText(item?.id) === requestedID) : null;
  if (exact) {
    return modelAdaptersShareChannel(exact, requestedTemplate) ? exact : null;
  }
  return adapters.find((item) => modelAdaptersShareChannel(item, requestedTemplate)) ?? null;
}
