import { useCallback, useEffect, useRef, useState } from "react";
import { useKeepAliveContext } from "keepalive-for-react";
import { api, type AliasSource, type AliasView, type AliasTestResult, type ModelConnectivityResult } from "../../shared/api";
import { errorText, targetKey } from "./aliasPresentation";

export type LocalTest = { at: number; result?: ModelConnectivityResult; error?: string; skipped?: "broken"; cancelled?: boolean };
export function useAliases() {
  const { active: pageActive } = useKeepAliveContext();
  const [aliases, setAliases] = useState<AliasView[]>([]);
  const [sources, setSources] = useState<AliasSource[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [now, setNow] = useState(Date.now());
  const [lastTests, setLastTests] = useState<Record<string, LocalTest>>({});
  const [routeTests, setRouteTests] = useState<Record<string, AliasTestResult>>({});
  const [testing, setTesting] = useState<string | null>(null);
  const active = useRef<{ controller: AbortController; cancel?: () => Promise<void> } | null>(null);
  const refresh = useCallback(async (signal?: AbortSignal) => {
    try {
      const [nextAliases, nextSources] = await Promise.all([api.aliases(signal), api.aliasSources(signal)]);
      if (signal?.aborted) return;
      setAliases(nextAliases); setSources(nextSources); setError(null);
    } catch (cause) { if (!signal?.aborted) setError(errorText(cause)); }
    finally { if (!signal?.aborted) setLoading(false); }
  }, []);
  useEffect(() => {
    if (!pageActive) return;
    let timer: number | undefined;
    let controller: AbortController | undefined;
    const stop = () => { window.clearTimeout(timer); controller?.abort(); };
    const poll = async () => {
      if (document.visibilityState !== "visible") return;
      const request = new AbortController();
      controller = request;
      await refresh(request.signal);
      if (!request.signal.aborted) timer = window.setTimeout(() => void poll(), 5000);
    };
    const visibility = () => { stop(); if (document.visibilityState === "visible") void poll(); };
    visibility();
    const clock = window.setInterval(() => { if (document.visibilityState === "visible") setNow(Date.now()); }, 1000);
    document.addEventListener("visibilitychange", visibility);
    return () => { stop(); window.clearInterval(clock); document.removeEventListener("visibilitychange", visibility); };
  }, [pageActive, refresh]);
  const cancelTest = useCallback(async () => {
    const current = active.current;
    if (!current) return;
    current.controller.abort();
    try { await current.cancel?.(); } catch (cause) { setError(errorText(cause)); }
  }, []);
  useEffect(() => () => { void cancelTest(); }, [cancelTest]);
  const test = async (alias: AliasView, all: boolean) => {
    if (active.current) return;
    const session = { controller: new AbortController(), cancel: undefined as (() => Promise<void>) | undefined };
    active.current = session;
    setTesting(alias.id);
    setError(null);
    try {
      if (all) {
        for (const target of alias.targets) {
          if (session.controller.signal.aborted) break;
          const key = targetKey(target);
          const source = sources.find((item) => targetKey(item.target) === key);
          let state: LocalTest = { at: Date.now() };
          if (!source) state.skipped = "broken";
          else {
            const id = crypto.randomUUID();
            session.cancel = () => api.cancelModelTest(source.request_model_id, id);
            try { state.result = await api.testModel(source.request_model_id, id, session.controller.signal); }
            catch (cause) { if (session.controller.signal.aborted) state.cancelled = true; else state.error = errorText(cause); }
            finally { session.cancel = undefined; }
          }
          setLastTests((current) => ({ ...current, [key]: state }));
        }
      } else {
        const id = crypto.randomUUID();
        session.cancel = () => api.cancelAliasTest(alias.id, id);
        const result = await api.testAlias(alias.id, id, session.controller.signal);
        setRouteTests((current) => ({ ...current, [alias.id]: result }));
      }
    } catch (cause) { if (!session.controller.signal.aborted) setError(errorText(cause)); }
    finally { active.current = null; setTesting(null); await refresh(); }
  };
  return { aliases, sources, loading, error, now, lastTests, routeTests, testing, refresh, cancelTest, test };
}
