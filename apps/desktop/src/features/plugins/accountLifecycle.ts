import type { PluginResourceDescriptor, PluginResourceMetric } from "../../shared/api.ts";

export function showsMetricDeadline(metric: PluginResourceMetric, kind: "reset" | "expiry") {
  return kind === "reset"
    ? metric.unit === "percent" || metric.resetAtMs != null
    : metric.unit === "count" || metric.expiresAtMs != null;
}

export function resourceKey(type: string, id: string) {
  return JSON.stringify([type, id]);
}

/** The set belongs to one open manager, not to a snapshot or pagination page. */
export function takeNewRefreshTargets(seen: Set<string>, resources: PluginResourceDescriptor[]) {
  return resources.flatMap((resource) => !resource.canRefresh ? [] : resource.resources.flatMap((item) => {
    const key = resourceKey(resource.type, item.id);
    if (seen.has(key)) return [];
    seen.add(key);
    return [{ type: resource.type, id: item.id }];
  }));
}

export function remainingSeconds(timestamp: number | null | undefined, now: number): number | null {
  return timestamp == null || !Number.isFinite(timestamp) ? null : Math.max(0, Math.ceil((timestamp - now) / 1000));
}
