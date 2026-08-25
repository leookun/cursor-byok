import { useSyncExternalStore } from "react";
import { api, type CursorHarnessStatus, type LlmCall, type Model, type ModelInput, type Overview, type PortSettings, type Provider, type ProviderInput, type ProviderSelection } from "../api";
import { applyTheme, isThemeId, type ThemeId } from "../theme/theme";

type Discovery = { provider: Provider; modelIds: string[] };

export type AppSnapshot = {
  providers: Provider[];
  models: Model[];
  calls: LlmCall[];
  overview: Overview;
  detailed: boolean;
  ports: PortSettings;
  busy: boolean;
  error: string | null;
  discoveringProviderId: number | null;
  discovery: Discovery | null;
  theme: ThemeId;
  cursorHarness: CursorHarnessStatus | null;
  cursorBusy: boolean;
};

const savedTheme = (): ThemeId => {
  const saved = localStorage.getItem("cursor-byok.theme");
  return isThemeId(saved) ? saved : "default-dark";
};

let snapshot: AppSnapshot = {
  providers: [],
  models: [],
  calls: [],
  overview: {
    metrics: {
      llm_calls: 0,
      successful_calls: 0,
      failed_calls: 0,
      token_usage: 0,
      prompt_tokens: 0,
      input_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      output_tokens: 0,
    },
    token_usage_granularity: "day",
    token_usage_series: [],
  },
  detailed: false,
  ports: { proxy_port: 0, service_port: 0 },
  busy: false,
  error: null,
  discoveringProviderId: null,
  discovery: null,
  theme: savedTheme(),
  cursorHarness: null,
  cursorBusy: false,
};

const listeners = new Set<() => void>();

function update(patch: Partial<AppSnapshot>) {
  snapshot = { ...snapshot, ...patch };
  listeners.forEach((listener) => listener());
}

async function perform(task: () => Promise<void>) {
  update({ error: null });
  try {
    await task();
  } catch (cause) {
    update({ error: cause instanceof Error ? cause.message : String(cause) });
  }
}

export const appStore = {
  subscribe(listener: () => void) {
    listeners.add(listener);
    return () => listeners.delete(listener);
  },
  getSnapshot: () => snapshot,

  async refresh() {
    update({ busy: true, error: null });
    try {
      const [providers, models, calls, overview, settings, ports, cursorHarness] = await Promise.all([
        api.providers(),
        api.models(),
        api.calls(),
        api.overview(),
        api.observability(),
        api.ports(),
        api.cursorHarness(),
      ]);
      update({ providers, models, calls, overview, detailed: settings.detailed, ports, cursorHarness });
    } catch (cause) {
      update({ error: cause instanceof Error ? cause.message : String(cause) });
    } finally {
      update({ busy: false });
    }
  },

  async createProvider(input: ProviderInput) {
    try {
      update({ error: null });
      await api.createProvider(input);
      await appStore.refresh();
      return true;
    } catch (cause) {
      update({ error: cause instanceof Error ? cause.message : String(cause) });
      return false;
    }
  },
  async updateProvider(providerId: number, input: ProviderInput) {
    try {
      update({ error: null });
      await api.updateProvider(providerId, input);
      await appStore.refresh();
      return true;
    } catch (cause) {
      update({ error: cause instanceof Error ? cause.message : String(cause) });
      return false;
    }
  },
  async deleteProvider(providerId: number) {
    await perform(async () => {
      await api.deleteProvider(providerId);
      await appStore.refresh();
    });
  },

  async discoverModels(provider: Provider) {
    update({ discoveringProviderId: provider.provider_id, error: null });
    try {
      const result = await api.discoverModels(provider.provider_id);
      const existing = new Set(snapshot.models.filter((model) => model.provider_id === provider.provider_id).map((model) => model.model_id));
      update({ discovery: { provider, modelIds: result.models.filter((id) => !existing.has(id)) } });
    } catch (cause) {
      update({ error: cause instanceof Error ? cause.message : String(cause) });
    } finally {
      update({ discoveringProviderId: null });
    }
  },
  async addModel(modelId: string) {
    const discovery = snapshot.discovery;
    if (!discovery) return;
    await perform(async () => {
      await api.saveModels(discovery.provider.provider_id, [{
        model_id: modelId,
        display_name: modelId,
        endpoint_type: discovery.provider.provider_type,
        request_url: "",
        enabled: true,
        sort_order: snapshot.models.length,
        context_window_tokens: null,
        max_output_tokens: null,
        reasoning_enabled: false,
        reasoning_effort: null,
        supports_image_generation: false,
      }]);
      update({ discovery: { ...discovery, modelIds: discovery.modelIds.filter((id) => id !== modelId) } });
      await appStore.refresh();
    });
  },
  async deleteModel(modelHash: string) {
    await perform(async () => {
      await api.deleteModel(modelHash);
      await appStore.refresh();
    });
  },

  async initializeCursorCa() {
    update({ cursorBusy: true, error: null });
    try {
      const status = await api.initializeCursorCa();
      update({ cursorHarness: status });
      return status;
    } catch (cause) {
      update({ error: cause instanceof Error ? cause.message : String(cause) });
      return null;
    } finally { update({ cursorBusy: false }); }
  },
  async setCursorEnabled(enabled: boolean) {
    update({ cursorBusy: true, error: null });
    try { update({ cursorHarness: await api.setCursorEnabled(enabled) }); }
    catch (cause) { update({ error: cause instanceof Error ? cause.message : String(cause) }); }
    finally { update({ cursorBusy: false }); }
  },
  async createCursorModels(provider: ProviderSelection, models: ModelInput[]) {
    update({ cursorBusy: true, error: null });
    try {
      await api.createCursorModels(provider, models);
      await appStore.refresh();
      return true;
    } catch (cause) {
      update({ error: cause instanceof Error ? cause.message : String(cause) });
      return false;
    } finally { update({ cursorBusy: false }); }
  },
  async updateCursorModel(hash: string, model: ModelInput) {
    update({ cursorBusy: true, error: null });
    try {
      const updated = await api.updateModel(hash, model);
      update({
        models: snapshot.models.map((current) => current.model_hash === hash ? updated : current),
      });
      return updated;
    } catch (cause) {
      update({ error: cause instanceof Error ? cause.message : String(cause) });
      return null;
    } finally { update({ cursorBusy: false }); }
  },

  async openCallDetails(callId: string) {
    await perform(() => api.openCallDetails(callId));
  },
  async updateDetailed(detailed: boolean) {
    await perform(async () => update(await api.setObservability(detailed)));
  },
  async updatePorts(ports: PortSettings) {
    try {
      update({ error: null });
      update({ ports: await api.setPorts(ports) });
      return true;
    } catch (cause) {
      update({ error: cause instanceof Error ? cause.message : String(cause) });
      return false;
    }
  },
  selectTheme(theme: ThemeId) {
    localStorage.setItem("cursor-byok.theme", theme);
    applyTheme(theme);
    update({ theme });
  },
};

export function useAppStore() {
  return useSyncExternalStore(appStore.subscribe, appStore.getSnapshot);
}
