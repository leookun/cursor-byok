import { useState } from "react";
import { api, type AliasInput, type AliasView } from "../../shared/api";
import { PageActions } from "../../shell/PageActions";
import { PageContent } from "../../shell/layout/PageContent";
import { Button } from "../../shared/ui/Button";
import { Card } from "../../shared/ui/Card";
import { ConfirmDialog } from "../../shared/ui/ConfirmDialog";
import { Modal } from "../../shared/ui/Modal";
import { Switch } from "../../shared/ui/Switch";
import { AliasEditor, localTestText } from "./AliasEditor";
import { duplicateAliasName, validateAlias } from "./aliasDraft";
import { AliasSettingsDialog } from "./AliasSettingsDialog";
import { aliasInput, aliasReason, aliasStatus, aliasWarning, capabilitiesText, emptyAlias, errorText, targetKey } from "./aliasPresentation";
import { useAliases } from "./useAliases";
import styles from "./Aliases.module.scss";

export function AliasesPage() {
  const state = useAliases();
  const [draft, setDraft] = useState<AliasInput | null>(null);
  const [editing, setEditing] = useState<AliasView | null>(null);
  const [deleting, setDeleting] = useState<AliasView | null>(null);
  const [settings, setSettings] = useState(false);
  const [busy, setBusy] = useState(false);
  const [submitted, setSubmitted] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const mutate = async (task: () => Promise<unknown>, done?: () => void) => {
    setBusy(true); setError(null);
    try { await task(); done?.(); await state.refresh(); }
    catch (cause) { setError(errorText(cause)); }
    finally { setBusy(false); }
  };
  const open = (alias: AliasView | null, duplicate = false) => {
    setEditing(duplicate ? null : alias); setSubmitted(false); setError(null);
    const next = alias ? aliasInput(alias) : emptyAlias();
    if (duplicate) next.name = duplicateAliasName(next.name, state.aliases);
    setDraft(next);
  };
  const save = () => {
    if (!draft) return;
    setSubmitted(true);
    const errors = validateAlias(draft, state.aliases, editing?.id, state.sources);
    if (errors.name) return;
    const input = { ...draft, name: draft.name.trim().toLowerCase(), description: draft.description.trim() };
    void mutate(() => editing ? api.updateAlias(editing.id, input) : api.createAlias(input), () => setDraft(null));
  };
  const sections = [
    ...((error || state.error || state.loading || state.aliases.length === 0) ? [{ key: "status", estimatedHeight: 100, content: <Card className={styles.card}>
      {(error || state.error) && <p role="alert" className={styles.error}>{error || state.error}</p>}
      {state.loading ? <p role="status">{t("正在加载…")}</p> : !state.aliases.length && <p>{t("还没有别名。创建别名后，可在一个模型名称下按优先级切换多个来源。")}</p>}
    </Card> }] : []),
    ...state.aliases.map((alias) => {
      const result = state.routeTests[alias.id];
      const sourceLabel = (key: string) => { const source = state.sources.find((item) => targetKey(item.target) === key); return source ? `${source.source_name} · ${source.label} · ${source.model_id}` : alias.targets.find((item) => targetKey(item) === key)?.source_id ?? t("来源已失效"); };
      return { key: alias.id, estimatedHeight: 240 + alias.targets.length * 65, content: <Card className={styles.card}>
        <div className={styles.row}><strong className={styles.identity}>{alias.name}</strong><span>{aliasStatus(alias.status)}</span><Switch label={t("启用别名 {name}", { name: alias.name })} checked={alias.enabled} disabled={busy} onChange={(enabled) => void mutate(() => api.updateAlias(alias.id, { ...aliasInput(alias), enabled }))} /></div>
        {alias.description && <p className={styles.muted}>{alias.description}</p>}
        <p className={styles.muted}>{t("{count} 个目标", { count: alias.targets.length })}</p>
        <p className={styles.muted}>{capabilitiesText(alias.parameters)}</p>
        {alias.warnings.map((warning, index) => <p key={`${warning}-${index}`} className={styles.warning}>{aliasWarning(warning)}</p>)}
        {alias.active_target && <span>{t("当前来源：{name}", { name: sourceLabel(alias.active_target) })}</span>}
        <ol className={styles.targets}>{alias.targets.map((target) => {
          const key = targetKey(target);
          const status = alias.target_statuses.find((item) => item.key === key);
          const remaining = status?.retry_at_ms ? Math.max(0, Math.ceil((status.retry_at_ms - state.now) / 1000)) : 0;
          return <li key={key} className={styles.target}>
            <div className={styles.row}><span className={styles.identity}>{sourceLabel(key)}</span><span>{aliasStatus(status?.status ?? (target.enabled ? "unavailable" : "disabled"))}</span>{remaining > 0 && <span>{t("剩余 {seconds} 秒", { seconds: remaining })}</span>}</div>
            {status?.reason && <span className={styles.muted}>{aliasReason(status.reason)}</span>}
            <span className={styles.muted}>{localTestText(state.lastTests[key])}{state.lastTests[key] && ` · ${new Date(state.lastTests[key].at).toLocaleTimeString()}`}</span>
          </li>;
        })}</ol>
        {result && <div className={styles.target} role="status"><span>{result.error ? t("测试失败：{error}", { error: aliasReason(result.error) }) : t("路由测试完成，切换 {count} 次", { count: result.switches })}</span>{result.target_id && <span>{t("当前来源：{name}", { name: sourceLabel(result.target_id) })}</span>}{result.result && <span>{t("测试成功：{duration} ms", { duration: result.result.duration_ms })}</span>}{result.attempts.map((attempt, index) => <span key={index}>{sourceLabel(attempt.target_id)} · {aliasReason(attempt.error) || t("成功")}</span>)}</div>}
        <div className={styles.actions}>
          <Button size="small" disabled={busy} onClick={() => open(alias)}>{t("编辑")}</Button>
          <Button size="small" disabled={busy} onClick={() => open(alias, true)}>{t("复制")}</Button>
          <Button size="small" disabled={busy || Boolean(state.testing) || !alias.enabled} onClick={() => void state.test(alias, false)}>{t("测试别名路由")}</Button>
          <Button size="small" disabled={busy || Boolean(state.testing)} onClick={() => void state.test(alias, true)}>{t("逐个测试来源")}</Button>
          {state.testing === alias.id && <Button size="small" onClick={() => void state.cancelTest()}>{t("取消测试")}</Button>}
          <Button size="small" className={styles.danger} disabled={busy || state.testing === alias.id} onClick={() => { setError(null); setDeleting(alias); }}>{t("删除")}</Button>
        </div>
      </Card> };
    }),
  ];
  return <>
    <PageActions position="left"><Button disabled={state.loading} onClick={() => void state.refresh()}>{t("刷新别名")}</Button><Button onClick={() => setSettings(true)}>{t("冷却与超时")}</Button></PageActions>
    <PageActions><Button variant="primary" disabled={busy} onClick={() => open(null)}>{t("创建别名")}</Button></PageActions>
    <PageContent title={t("模型别名")} sections={sections} />
    <Modal fullHeight open={draft !== null} title={editing ? t("编辑别名") : t("创建别名")} busy={busy} onClose={() => setDraft(null)} onSubmit={save}>
      {error && <p role="alert" className={styles.error}>{error}</p>}
      {draft && <AliasEditor draft={draft} original={editing} now={state.now} aliases={state.aliases} sources={state.sources} lastTests={state.lastTests} submitted={submitted} onChange={setDraft} />}
    </Modal>
    <ConfirmDialog destructive open={deleting !== null} title={t("删除别名")} confirmLabel={t("删除别名")} busy={busy} onCancel={() => setDeleting(null)} onConfirm={() => { if (deleting) void mutate(() => api.deleteAlias(deleting.id), () => setDeleting(null)); }}><p>{t("删除别名 {name} 后，Cursor 将无法继续通过此名称请求模型。来源配置不会被删除。", { name: deleting?.name ?? "" })}</p>{error && <p role="alert" className={styles.error}>{error}</p>}</ConfirmDialog>
    {settings && <AliasSettingsDialog onClose={() => setSettings(false)} />}
  </>;
}
