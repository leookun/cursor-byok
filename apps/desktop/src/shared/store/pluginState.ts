import type { PluginDescriptor, PluginResourceSelection } from "../api.ts";

/** Share only in-flight work. A later explicit operation always runs again. */
export function createSingleFlight() {
  const pending = new Map<string, Promise<unknown>>();
  return function singleFlight<T>(key: string, task: () => Promise<T>): Promise<T> {
    const existing = pending.get(key);
    if (existing) return existing as Promise<T>;
    const promise = Promise.resolve().then(task);
    pending.set(key, promise);
    const clear = () => { if (pending.get(key) === promise) pending.delete(key); };
    void promise.then(clear, clear);
    return promise;
  };
}

/** A slow local snapshot must not overwrite a newer selection mutation. */
export function mergePluginSnapshots(current: PluginDescriptor[], incoming: PluginDescriptor[]): PluginDescriptor[] {
  return incoming.map((plugin) => {
    const previous = current.find((item) => item.id === plugin.id);
    return { ...plugin, resources: plugin.resources.map((resource) => {
      const old = previous?.resources.find((item) => item.type === resource.type);
      return old && old.selection.revision > resource.selection.revision
        ? { ...resource, selection: old.selection }
        : resource;
    }) };
  });
}

export function applyPluginSelection(plugins: PluginDescriptor[], pluginId: string, type: string, selection: PluginResourceSelection) {
  return plugins.map((plugin) => plugin.id !== pluginId ? plugin : {
    ...plugin,
    resources: plugin.resources.map((resource) => resource.type !== type || resource.selection.revision > selection.revision
      ? resource : { ...resource, selection }),
  });
}
