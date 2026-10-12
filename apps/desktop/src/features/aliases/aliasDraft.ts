import type { AliasCapabilities, AliasInput, AliasSource, AliasTarget, AliasTargetStatus, AliasView } from "../../shared/api";
import { targetKey } from "./aliasPresentation";

export function validateAlias(draft: AliasInput, aliases: AliasView[], id?: string, sources: AliasSource[] = []): { name?: string } {
  const name = draft.name.trim();
  return {
    name: !name ? t("请输入别名名称。")
      : !/^[a-z0-9._-]{1,64}$/i.test(name) ? t("名称需为 1–64 个英文字母、数字、点、下划线或连字符，保存时转为小写。")
      : aliases.some((item) => item.id !== id && item.name.toLowerCase() === name.toLowerCase()) ? t("别名名称已存在。")
      : sources.some((source) => source.request_model_id.toLowerCase() === name.toLowerCase() || source.model_id.toLowerCase() === name.toLowerCase()) ? t("别名名称不能与已有模型 ID 相同。") : undefined,
  };
}

export function duplicateAliasName(name: string, aliases: AliasView[]): string {
  const used = new Set(aliases.map((alias) => alias.name.toLowerCase()));
  let suffix = "-copy";
  let next = `${name.slice(0, 64 - suffix.length)}${suffix}`;
  let number = 2;
  while (used.has(next.toLowerCase())) {
    suffix = `-copy-${number++}`;
    next = `${name.slice(0, 64 - suffix.length)}${suffix}`;
  }
  return next;
}

export function isReservedAliasName(value: string): boolean {
  const name = value.trim().toLowerCase();
  return ["gpt-", "claude-", "gemini-", "glm-", "kimi-", "grok-", "deepseek-"].some((prefix) => name.startsWith(prefix))
    || /^o\d/.test(name) || ["auto", "composer", "default"].includes(name);
}

export function effectiveParameters(draft: AliasInput, sources: AliasSource[]): AliasCapabilities {
  const values = draft.targets.filter((target) => target.enabled).map((target) => sources.find((source) => targetKey(source.target) === targetKey(target))?.parameters);
  const minimum = (key: "context_window_tokens" | "max_output_tokens") => {
    const numbers = values.map((value) => value?.[key] ?? null);
    return !numbers.length || numbers.includes(null) ? null : Math.min(...numbers as number[]);
  };
  const all = (key: "images" | "tools") => {
    const flags = values.map((value) => value?.[key] ?? null);
    return flags.includes(false) ? false : !flags.length || flags.includes(null) ? null : true;
  };
  return { context_window_tokens: minimum("context_window_tokens"), max_output_tokens: minimum("max_output_tokens"), images: all("images"), tools: all("tools") };
}

export function draftTargetStatus(target: AliasTarget, sources: AliasSource[], aliases: AliasView[], aliasId?: string): AliasTargetStatus {
  const key = targetKey(target);
  const source = sources.find((item) => targetKey(item.target) === key);
  const basic = (status: AliasTargetStatus["status"], reason: string | null = null): AliasTargetStatus => ({ key, status, reason, retry_at_ms: null });
  if (!target.enabled) return basic("disabled");
  if (!source) return basic("broken", "source_missing");
  if (!source.available) return basic("unavailable", source.reason);
  const health = aliases.flatMap((alias) => alias.target_statuses).find((item) => item.key === key && (item.status === "cooldown" || item.status === "authorization"));
  if (health) return health;
  return basic(aliases.find((alias) => alias.id === aliasId)?.active_target === key ? "active" : "available");
}
