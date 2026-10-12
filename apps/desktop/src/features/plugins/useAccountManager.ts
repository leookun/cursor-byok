import { useCallback, useEffect, useRef, useState } from "react";
import { api, type PluginDescriptor } from "../../shared/api";
import { appStore } from "../../shared/store/appStore";
import { createSingleFlight } from "../../shared/store/pluginState";
import { resourceKey, takeNewRefreshTargets } from "./accountLifecycle";

// Shared across actual close/reopen, but only while a request is in flight.
const refreshRequests = createSingleFlight();
export type AccountTask = { pending: string | null; error: string | null };

export function useAccountManager(plugin: PluginDescriptor) {
  const [tasks, setTasks] = useState<Record<string, AccountTask>>({});
  const [snapshotError, setSnapshotError] = useState<string | null>(null);
  const [visible, setVisible] = useState(document.visibilityState === "visible");
  const [now, setNow] = useState(Date.now);
  const mounted = useRef(false);
  const seen = useRef(new Set<string>());
  const running = useRef(new Set<string>());

  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);

  const run = useCallback(async <T,>(type: string, id: string, operation: string, task: () => Promise<T>) => {
    const key = resourceKey(type, id);
    if (running.current.has(key)) return;
    running.current.add(key);
    setTasks((current) => ({ ...current, [key]: { pending: operation, error: null } }));
    try {
      return await task();
    } catch (cause) {
      if (mounted.current) setTasks((current) => ({ ...current, [key]: { pending: null, error: String(cause instanceof Error ? cause.message : cause) } }));
    } finally {
      running.current.delete(key);
      if (mounted.current) setTasks((current) => ({ ...current, [key]: { pending: null, error: current[key]?.error ?? null } }));
    }
  }, []);

  const refresh = useCallback((type: string, id: string) => run(type, id, "refresh", () =>
    refreshRequests(JSON.stringify([plugin.id, type, id]), async () => {
      await api.refreshPluginResource(plugin.id, type, id);
      await appStore.readPlugins(true);
    })), [plugin.id, run]);

  useEffect(() => {
    const onVisibility = () => setVisible(document.visibilityState === "visible");
    document.addEventListener("visibilitychange", onVisibility);
    return () => document.removeEventListener("visibilitychange", onVisibility);
  }, []);

  useEffect(() => {
    if (!visible) return;
    for (const target of takeNewRefreshTargets(seen.current, plugin.resources)) void refresh(target.type, target.id);
  }, [plugin.resources, refresh, visible]);

  useEffect(() => {
    if (!visible) return;
    let stopped = false;
    let polling = false;
    const poll = async () => {
      if (polling) return;
      polling = true;
      try {
        await appStore.readPlugins();
        if (!stopped) setSnapshotError(null);
      } catch (cause) {
        if (!stopped) setSnapshotError(String(cause instanceof Error ? cause.message : cause));
      } finally { polling = false; }
    };
    void poll();
    setNow(Date.now());
    const snapshotTimer = window.setInterval(() => void poll(), 5000);
    const clockTimer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => {
      stopped = true;
      window.clearInterval(snapshotTimer);
      window.clearInterval(clockTimer);
    };
  }, [visible]);

  return { tasks, run, refresh, now, snapshotError };
}
