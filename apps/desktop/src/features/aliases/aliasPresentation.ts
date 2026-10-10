import type { AliasCapabilities, AliasInput, AliasTarget, AliasTargetStatus, AliasView } from "../../shared/api";

export const targetKey = (target: AliasTarget) => JSON.stringify([target.source_type, target.source_id, target.model_id]);
export const aliasInput = (alias: AliasInput): AliasInput => ({ name: alias.name, description: alias.description, enabled: alias.enabled, targets: alias.targets.map((target) => ({ ...target })), sticky: alias.sticky, return_mode: alias.return_mode });
export const emptyAlias = (): AliasInput => ({ name: "", description: "", enabled: true, targets: [], sticky: true, return_mode: "new_sessions" });
export const errorText = (cause: unknown) => cause instanceof Error ? cause.message : String(cause);

export function aliasStatus(status: AliasView["status"] | AliasTargetStatus["status"]): string {
  switch (status) {
    case "working": return t("正常工作");
    case "partial": return t("部分可用");
    case "unavailable": return t("不可用");
    case "disabled": return t("已禁用");
    case "available": return t("可用");
    case "active": return t("当前使用");
    case "cooldown": return t("冷却中");
    case "authorization": return t("需要重新授权");
    case "broken": return t("来源已失效");
  }
}
export function aliasWarning(code: string): string {
  switch (code) {
    case "reserved_name": return t("此名称可能与内置模型名称冲突。");
    case "unknown_context": return t("部分来源的上下文窗口未知，无法保证统一上限。");
    case "unknown_output": return t("部分来源的最大输出未知，无法保证统一上限。");
    case "unknown_images": return t("部分来源的图片能力未知。");
    case "unknown_tools": return t("部分来源的工具调用能力未知。");
    case "tools_unsupported": return t("部分来源不支持工具调用，可能无法完成编程任务。");
    case "no_targets": return t("请添加至少一个来源。");
    case "no_available_targets": return t("当前没有可用来源，请检查配置或等待冷却结束。");
    default: return t("请检查别名及来源配置。");
  }
}
export function aliasReason(code: string | null): string {
  switch (code) {
    case null: return "";
    case "source_missing": return t("来源已失效");
    case "source_disabled": return t("来源已禁用");
    case "source_unavailable": return t("来源暂不可用");
    case "rate_limit": return t("来源请求限流");
    case "upstream_failure": return t("上游服务失败");
    case "authorization": return t("需要重新授权");
    default: return code;
  }
}
export function capabilitiesText(value: AliasCapabilities): string {
  const capability = (supported: boolean | null) => supported === null ? t("未知") : supported ? t("支持") : t("不支持");
  return t("上下文 {context} · 输出 {output} · 图片 {images} · 工具 {tools}", {
    context: value.context_window_tokens?.toLocaleString() ?? t("未知"),
    output: value.max_output_tokens?.toLocaleString() ?? t("未知"),
    images: capability(value.images), tools: capability(value.tools),
  });
}
