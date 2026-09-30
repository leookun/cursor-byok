import type { PluginDescriptor, PluginResourceSelection } from "../shared/api.ts";

/** In-memory browser/test fixture, installed only by the demo entry point. */
export function createPluginFixture(now = Date.now(), delayMs = 350) {
  const calls: { method: string; path: string }[] = [];
  const plugins: PluginDescriptor[] = ["codex", "grok", "antigravity"].map((name, index) => ({
    id: `fixture-${name}`,
    name: `${name[0].toUpperCase()}${name.slice(1)} fixture`,
    version: "1.0.0",
    author: "Local test data",
    icon: "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'%3E%3Crect width='32' height='32' rx='8' fill='%23666'/%3E%3Cpath d='M8 16h16M16 8v16' stroke='white' stroke-width='2'/%3E%3C/svg%3E",
    providers: [{
      id: name, pluginId: `fixture-${name}`, displayName: name, description: null,
      providerType: "openai", resourceType: "accounts", hasModels: true, configured: false, models: [],
    }],
    resources: [{
      type: "accounts", displayName: { "en-US": "Accounts", "zh-CN": "账号", "pt-BR": "Contas" },
      selection: { activeResourceId: index === 0 ? "account-12" : "account-1", automaticSwitching: index !== 1, revision: 1 },
      canRefresh: true, canRemove: true,
      import: { displayName: "Import fixture", description: null, accept: [".json"], multiple: true },
      add: [{ id: "fixture-oauth", type: "oauth2.0", displayName: "Demo OAuth", description: "Local fixture only; no external authorization." }],
      actions: [],
      resources: Array.from({ length: index === 0 ? 12 : 2 }, (_, account) => ({
        id: `account-${account + 1}`, displayName: `${name}-${account + 1}@example.test`,
        description: account === 2 ? "Fixture: refresh and selection fail for this account." : null,
        createdAtMs: now - account * 3600000,
        state: account === 1 ? { status: "cooling" as const, retryAtMs: now + 15000 }
          : account === 2 ? { status: "invalid" as const, message: "Fixture authorization expired" }
          : { status: "ready" as const },
        metrics: [
          { id: "quota", label: { "en-US": "Weekly quota", "zh-CN": "每周额度", "pt-BR": "Cota semanal" }, unit: "percent" as const, value: account === 1 ? 0 : 78 - account, resetAtMs: account === 3 ? null : now + 10000 + account * 3000 },
          ...(index === 0 ? [{ id: "cards", label: { "en-US": "Reset cards", "zh-CN": "重置卡", "pt-BR": "Cartões de redefinição" }, unit: "count" as const, value: 2, expiresAtMs: account === 3 ? null : now + 60000 }] : []),
        ],
      })),
    }],
  }));
  const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "content-type": "application/json" } });
  const empty = () => new Response(null, { status: 204 });
  const wait = () => new Promise<void>((resolve) => setTimeout(resolve, delayMs));
  let failSnapshot = false;
  let failSelection = false;
  let oauthPlugin: PluginDescriptor | undefined;

  const addAccount = (pluginId = plugins[0].id) => {
    const resource = plugins.find((plugin) => plugin.id === pluginId)!.resources[0];
    const id = `added-${resource.resources.length + 1}`;
    resource.resources.push({ id, displayName: `${id}@example.test`, description: null, createdAtMs: Date.now(), state: { status: "ready" }, metrics: [] });
    return id;
  };

  return {
    calls, plugins, addAccount,
    setSnapshotFailure(value: boolean) { failSnapshot = value; },
    setSelectionFailure(value: boolean) { failSelection = value; },
    async handle(path: string, method: string, body: unknown): Promise<Response | null> {
      if (!path.startsWith("/plugins")) return null;
      calls.push({ method, path });
      if (path === "/plugins/runtime") return json({ state: "ready", version: "fixture", target: null, phase: null, downloaded_bytes: 0, total_bytes: null, error: null });
      if (path === "/plugins") return failSnapshot ? json({ message: "Fixture local snapshot failure" }, 503) : json(plugins);
      if (path.startsWith("/plugins/oauth/")) {
        await wait();
        if (oauthPlugin) addAccount(oauthPlugin.id);
        oauthPlugin = undefined;
        return json({ status: "completed", added: 1, updated: 0, modelSyncError: "Fixture model sync failure" });
      }
      const parts = path.split("/").filter(Boolean).map(decodeURIComponent);
      const plugin = plugins.find((item) => item.id === parts[1]);
      if (!plugin) return json({ message: "Fixture plugin not found" }, 404);
      const resource = plugin.resources[0];
      await wait();
      if (parts.at(-1) === "selection" && method === "PUT") {
        const next = body as Omit<PluginResourceSelection, "revision">;
        if (failSelection || next.activeResourceId === "account-3") return json({ message: "Fixture selection rejected" }, 409);
        resource.selection = { ...next, revision: resource.selection.revision + 1 };
        return json(resource.selection);
      }
      if (parts.at(-1) === "refresh") {
        if (parts[4] === "account-3") return json({ message: "Fixture quota refresh failed; previous data retained" }, 503);
        return empty();
      }
      if (parts.at(-1) === "sync") return json({ message: "Fixture model sync failed; accounts remain manageable" }, 503);
      if (parts.at(-1) === "begin") {
        oauthPlugin = plugin;
        return json({ sessionId: "fixture-session", userCode: "DEMO-CODE", verificationUrl: "https://example.test/fixture", verificationUrlComplete: null, expiresAtMs: now + 60000, pollIntervalMs: 1000 });
      }
      if (parts.at(-1) === "import") {
        addAccount(plugin.id);
        return json({ added: 1, updated: 0, warnings: [], modelSyncError: "Fixture model sync failure" });
      }
      if (method === "DELETE" && parts.length === 5) {
        resource.resources = resource.resources.filter((item) => item.id !== parts[4]);
        if (resource.selection.activeResourceId === parts[4]) resource.selection = { ...resource.selection, activeResourceId: null, revision: resource.selection.revision + 1 };
        return empty();
      }
      return json({ message: `Unhandled plugin fixture: ${method} ${path}` }, 404);
    },
  };
}
