import type { Alias, AliasInput, AliasSettings, AliasSource, AliasView, Model } from "../shared/api";
import { targetKey } from "../features/aliases/aliasPresentation";

// Demo-only in-memory management endpoints; production always uses the Rust API.
export function demoAliases(models: Model[]) {
  const sources: AliasSource[] = models.map((model) => ({
    target: { source_type: "api", source_id: model.source_id, model_id: "", enabled: true },
    model_id: model.model_id, label: model.display_name, source_name: new URL(model.base_url).hostname, request_model_id: model.model_hash,
    available: true, reason: null,
    parameters: { context_window_tokens: model.context_window_tokens, max_output_tokens: model.max_completion_tokens ?? model.anthropic_max_tokens, images: null, tools: null },
  }));
  let aliases: Alias[] = [{ id: "demo-alias", name: "coding", description: "", enabled: true, sticky: true, return_mode: "new_sessions", targets: sources.slice(0, 2).map((source) => source.target), created_at_ms: Date.now(), updated_at_ms: Date.now() }];
  let settings: AliasSettings = { rate_limit_seconds: 60, transient_seconds: 30, authorization_seconds: 600, connect_timeout_seconds: 10, first_token_timeout_seconds: 60 };
  const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "content-type": "application/json" } });
  const view = (alias: Alias): AliasView => ({ ...alias, status: !alias.enabled ? "disabled" : alias.targets.some((target) => target.enabled) ? "working" : "unavailable", active_target: null,
    parameters: { context_window_tokens: 200000, max_output_tokens: 32000, images: null, tools: null }, warnings: ["unknown_images", "unknown_tools"],
    target_statuses: alias.targets.map((target) => ({ key: targetKey(target), status: target.enabled ? "available" : "disabled", reason: null, retry_at_ms: null })),
  });
  return (path: string, method: string, body: unknown): Response | null => {
    if (path === "/aliases/sources") return json(sources);
    if (path === "/aliases/settings") { if (method === "PUT") settings = body as AliasSettings; return json(settings); }
    if (path === "/aliases") {
      if (method === "GET") return json(aliases.map(view));
      const alias: Alias = { ...body as AliasInput, id: crypto.randomUUID(), created_at_ms: Date.now(), updated_at_ms: Date.now() };
      if (alias.targets.length === 0) alias.enabled = false;
      aliases.push(alias); return json(alias);
    }
    const match = path.match(/^\/aliases\/([^/]+)(\/test\/[^/]+)?$/);
    if (!match) return null;
    const alias = aliases.find((item) => item.id === decodeURIComponent(match[1]));
    if (!alias) return new Response("Alias not found", { status: 404 });
    if (match[2]) return method === "DELETE" ? new Response(null, { status: 204 }) : json({ result: { duration_ms: 1284, first_valid_response_ms: 418, output_tokens: 42, tokens_per_second: 38.6, tokens_estimated: false, output: "Demo" }, target_id: alias.targets[0] ? targetKey(alias.targets[0]) : null, switches: 0, attempts: alias.targets.slice(0, 1).map((target) => ({ target_id: targetKey(target), error: null })), error: null });
    if (method === "DELETE") { aliases = aliases.filter((item) => item.id !== alias.id); return new Response(null, { status: 204 }); }
    Object.assign(alias, body as AliasInput, { updated_at_ms: Date.now() });
    if (alias.targets.length === 0) alias.enabled = false;
    return json(alias);
  };
}
