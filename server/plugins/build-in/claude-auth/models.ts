import type { ModelDefinition, ModelSupport } from "cursor-byok:model";
import { accountData, apiHeaders, isExpiring, object, text } from "./auth.ts";

/** Discover actual account-visible models; never advertise a speculative static catalog. */
export const claudeModels: ModelSupport = {
  async list({ resource }, context): Promise<ModelDefinition[]> {
    if (!resource) throw new Error("Add a Claude account before syncing models.");
    const account = accountData(resource);
    // ModelSupport.list cannot return a resource patch, so it must not rotate credentials.
    if (isExpiring(account)) {
      throw new Error("Claude authorization is expiring. Refresh the account, then sync models.");
    }
    const models: ModelDefinition[] = [];
    const seen = new Set<string>();
    const cursors = new Set<string>();
    let after: string | null = null;
    for (;;) {
      context.signal.throwIfAborted();
      const url = new URL("https://api.anthropic.com/v1/models");
      url.searchParams.set("limit", "100");
      if (after !== null) url.searchParams.set("after_id", after);
      const response = await context.network.fetch(url.toString(), {
        headers: apiHeaders(account.accessToken),
      });
      if (response.status < 200 || response.status >= 300) {
        throw new Error(
          response.status === 401
            ? "Claude authorization was rejected. Refresh the account, then sync models."
            : `Claude model discovery failed (HTTP ${response.status}). Check account access and retry.`,
        );
      }
      let body: Record<string, unknown> | null;
      try {
        body = object(JSON.parse(response.body));
      } catch {
        body = null;
      }
      if (!body || !Array.isArray(body.data) || typeof body.has_more !== "boolean") {
        throw new Error("Claude returned an invalid model list. Retry syncing models.");
      }
      for (const raw of body.data) {
        const value = object(raw);
        const id = text(value?.id);
        const name = text(value?.display_name);
        if (!id || !name) {
          throw new Error("Claude returned an incomplete model. Retry syncing models.");
        }
        if (seen.has(id)) continue;
        seen.add(id);
        const capabilities = object(value?.capabilities);
        const thinking = object(capabilities?.thinking);
        const types = object(thinking?.types);
        const effort = object(capabilities?.effort);
        const supports = (value: unknown) => object(value)?.supported === true;
        const maxTokens = value?.max_tokens;
        models.push({
          id,
          displayName: name,
          ...(typeof maxTokens === "number" && Number.isSafeInteger(maxTokens) && maxTokens > 0
            ? { maxOutputTokens: maxTokens }
            : {}),
          capabilities: { images: supports(capabilities?.image_input) },
          privateData: {
            thinking: supports(thinking)
              ? supports(types?.adaptive) ? "adaptive" : supports(types?.enabled) ? "enabled" : null
              : null,
            efforts: supports(effort)
              ? ["low", "medium", "high", "xhigh", "max"].filter((level) =>
                supports(effort?.[level])
              )
              : [],
          },
        });
      }
      if (!body.has_more) return models;
      const cursor = text(body.last_id);
      if (!cursor || cursors.has(cursor) || body.data.length === 0) {
        throw new Error("Claude model pagination did not advance. Retry syncing models.");
      }
      cursors.add(cursor);
      after = cursor;
    }
  },
};
