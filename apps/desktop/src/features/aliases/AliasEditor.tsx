import { useEffect, useId, useRef } from "react";
import Sortable from "sortablejs";
import type { AliasInput, AliasSource, AliasView } from "../../shared/api";
import { FormField, TextInput } from "../../shared/ui/FormControls";
import { ModelSelect } from "../../shared/ui/ModelSelect";
import { Select } from "../../shared/ui/Select";
import { Switch } from "../../shared/ui/Switch";
import { Button } from "../../shared/ui/Button";
import { aliasReason, aliasStatus, aliasWarning, capabilitiesText, targetKey } from "./aliasPresentation";
import { draftTargetStatus, effectiveParameters, isReservedAliasName, validateAlias } from "./aliasDraft";
import type { LocalTest } from "./useAliases";
import styles from "./Aliases.module.scss";

export function localTestText(test?: LocalTest): string {
  if (!test) return t("尚未测试");
  if (test.cancelled) return t("测试已取消");
  if (test.skipped) return aliasStatus(test.skipped);
  if (test.error) return t("测试失败：{error}", { error: aliasReason(test.error) });
  return t("测试成功：{duration} ms", { duration: test.result?.duration_ms ?? 0 });
}
export function AliasEditor({ draft, original, aliases, sources, lastTests, submitted, now, onChange }: {
  draft: AliasInput; original: AliasView | null; aliases: AliasView[]; sources: AliasSource[];
  lastTests: Record<string, LocalTest>; submitted: boolean; now: number; onChange: (draft: AliasInput) => void;
}) {
  const list = useRef<HTMLDivElement>(null);
  const id = useId();
  const errors = validateAlias(draft, aliases, original?.id, sources);
  const parameters = effectiveParameters(draft, sources);
  const move = (from: number, to: number) => {
    const targets = [...draft.targets];
    const [item] = targets.splice(from, 1);
    targets.splice(to, 0, item);
    onChange({ ...draft, targets });
  };
  useEffect(() => {
    if (!list.current) return;
    const sortable = Sortable.create(list.current, {
      handle: "[data-drag-handle]", draggable: "[data-target]", animation: 0,
      onEnd: (event) => {
        const { oldIndex, newIndex, item, from } = event;
        if (oldIndex === undefined || newIndex === undefined || oldIndex === newIndex) return;
        from.removeChild(item);
        from.insertBefore(item, from.children[oldIndex] ?? null);
        move(oldIndex, newIndex);
      },
    });
    return () => sortable.destroy();
  }, [draft]);
  const selected = new Set(draft.targets.map(targetKey));
  const statusText = (target: AliasSource["target"]) => {
    const status = draftTargetStatus(target, sources, aliases, original?.id);
    const remaining = status.retry_at_ms ? Math.max(0, Math.ceil((status.retry_at_ms - now) / 1000)) : 0;
    return [aliasStatus(status.status), aliasReason(status.reason), remaining > 0 ? t("剩余 {seconds} 秒", { seconds: remaining }) : ""].filter(Boolean).join(" · ");
  };
  return <div className={styles.stack}>
    <FormField label={t("别名名称")} hint={t("名称需为 1–64 个英文字母、数字、点、下划线或连字符，保存时转为小写。")}> <TextInput maxLength={64} value={draft.name} aria-invalid={submitted && Boolean(errors.name)} aria-describedby={`${id}-name`} onChange={(event) => onChange({ ...draft, name: event.target.value })} /></FormField>
    <span id={`${id}-name`} className={styles.error}>{submitted && errors.name}</span>
    {isReservedAliasName(draft.name) && <p className={styles.warning}>{aliasWarning("reserved_name")}</p>}
    {original && original.name.toLowerCase() !== draft.name.trim().toLowerCase() && <p className={styles.warning}>{t("重命名后，旧会话可能出现“模型未找到”。请在旧会话中重新选择模型。")}</p>}
    <FormField label={t("描述")}><TextInput value={draft.description} onChange={(event) => onChange({ ...draft, description: event.target.value })} /></FormField>
    <div className={styles.row}><span>{t("启用别名")}</span><Switch label={t("启用别名")} checked={draft.enabled} onChange={(enabled) => onChange({ ...draft, enabled })} /></div>
    <div className={styles.target}>
      <strong>{t("生效参数预览")}</strong>
      <span>{t("{count} 个目标", { count: draft.targets.length })}</span>
      <span className={styles.muted}>{capabilitiesText(parameters)}</span>
      <span className={styles.muted}>{t("按所有启用目标的能力交集计算，未知能力不会作为保证。")}</span>
    </div>
    <ModelSelect mode="single" searchable label={t("添加来源")} value="" options={sources.map((source) => ({
      value: targetKey(source.target), label: source.label,
      group: `${source.target.source_type === "plugin" ? t("插件订阅") : t("API 渠道")} · ${source.source_name}`,
      searchText: `${source.model_id} ${source.request_model_id}`,
      metadata: `${source.model_id} · ${capabilitiesText(source.parameters)} · ${statusText(source.target)} · ${localTestText(lastTests[targetKey(source.target)])}`,
      disabled: selected.has(targetKey(source.target)),
    }))} onChange={(key) => { const source = sources.find((item) => targetKey(item.target) === key); if (source) onChange({ ...draft, targets: [...draft.targets, { ...source.target, enabled: true }] }); }} />
    <p className={styles.muted}>{t("按优先级从上到下尝试来源。拖动或使用上移、下移调整顺序。")}</p>
    {draft.targets.length === 0 ? <p className={styles.warning}>{t("没有目标时仍可保存，保存后别名将自动禁用。")}</p> : !draft.targets.some((target) => target.enabled) && <p className={styles.warning}>{t("所有目标均已禁用。可以保存，但别名当前无法处理请求。")}</p>}
    <div ref={list} className={styles.stack}>
      {draft.targets.map((target, index) => {
        const key = targetKey(target);
        const source = sources.find((item) => targetKey(item.target) === key);
        return <div key={key} data-target className={styles.target}>
          <div className={styles.row}><span data-drag-handle className={styles.drag} aria-hidden="true">{index + 1}.</span><strong className={styles.identity}>{source ? `${source.source_name} · ${source.label} · ${source.model_id}` : `${target.source_id} · ${target.model_id}`}</strong><Switch label={t("启用来源 {name}", { name: source?.label ?? target.source_id })} checked={target.enabled} onChange={(enabled) => onChange({ ...draft, targets: draft.targets.map((item, i) => i === index ? { ...item, enabled } : item) })} /></div>
          <span className={styles.muted}>{statusText(target)}</span>
          <span className={styles.muted}>{localTestText(lastTests[key])}</span>
          <div className={styles.actions}>
            <Button size="small" disabled={index === 0} onClick={() => move(index, index - 1)}>{t("上移")}</Button>
            <Button size="small" disabled={index === draft.targets.length - 1} onClick={() => move(index, index + 1)}>{t("下移")}</Button>
            <Button size="small" className={styles.danger} onClick={() => onChange({ ...draft, targets: draft.targets.filter((_, i) => i !== index) })}>{t("移除来源")}</Button>
          </div>
        </div>;
      })}
    </div>
    <div className={styles.row}><span>{t("会话固定来源")}</span><Switch label={t("会话固定来源")} checked={draft.sticky} onChange={(sticky) => onChange({ ...draft, sticky })} /></div>
    <p className={styles.muted}>{t("默认开启。同一会话继续使用成功的来源，失败后再切换。")}</p>
    <FormField label={t("高优先级来源恢复后")}><Select ariaLabel={t("高优先级来源恢复后")} value={draft.return_mode} options={[{ value: "new_sessions", label: t("仅新会话使用") }, { value: "immediate", label: t("立即切回") }]} onChange={(return_mode) => onChange({ ...draft, return_mode: return_mode as AliasInput["return_mode"] })} /></FormField>
    {draft.return_mode === "immediate" && <p className={styles.warning}>{t("立即切回可能重置现有会话的提示缓存，增加费用并影响回答一致性；正在生成的响应不会被中断。")}</p>}
  </div>;
}
