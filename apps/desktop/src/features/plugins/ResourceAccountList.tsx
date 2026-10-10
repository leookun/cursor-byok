import { useId, useMemo, useState } from "react";
import { api, pluginText, type PluginResourceAction, type PluginResourceDescriptor, type PluginResourceView } from "../../shared/api";
import { useI18n } from "../../i18n/store";
import { appStore, useAppStore } from "../../shared/store/appStore";
import { Button } from "../../shared/ui/Button";
import { Card } from "../../shared/ui/Card";
import { ConfirmDialog } from "../../shared/ui/ConfirmDialog";
import { FormField, TextInput } from "../../shared/ui/FormControls";
import { Switch } from "../../shared/ui/Switch";
import { remainingSeconds, resourceKey, showsMetricDeadline } from "./accountLifecycle";
import type { AccountTask, useAccountManager } from "./useAccountManager";
import styles from "./PluginResourcePanels.module.scss";

const PAGE_SIZE = 10;

export function ResourceAccountList({ pluginId, resource, manager, onAction }: {
  pluginId: string;
  resource: PluginResourceDescriptor;
  manager: ReturnType<typeof useAccountManager>;
  onAction: (item: PluginResourceView, action: PluginResourceAction) => void;
}) {
  const { locale } = useI18n();
  const [query, setQuery] = useState("");
  const automaticSwitchingDescriptionId = useId();
  const [page, setPage] = useState(1);
  const [selectionTarget, setSelectionTarget] = useState<string | null>(null);
  const [pendingDelete, setPendingDelete] = useState<PluginResourceView | null>(null);
  const filtered = useMemo(() => resource.resources.filter((item) => item.displayName.toLowerCase().includes(query.trim().toLowerCase())), [resource.resources, query]);
  const pageCount = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const currentPage = Math.min(page, pageCount);
  const visible = filtered.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE);
  const selected = resource.resources.find((item) => item.id === resource.selection.activeResourceId);
  const selectionTask = manager.tasks[resourceKey("selection", resource.type)];
  const { pluginSelectionPending } = useAppStore();
  const selectionPending = Boolean(selectionTask?.pending) || pluginSelectionPending.includes(JSON.stringify([pluginId, resource.type]));
  const select = (patch: { activeResourceId?: string; automaticSwitching?: boolean }) => {
    setSelectionTarget(patch.activeResourceId ?? null);
    void manager.run("selection", resource.type, "selection", async () => {
      const current = appStore.getSnapshot().plugins.find((plugin) => plugin.id === pluginId)?.resources.find((item) => item.type === resource.type)?.selection;
      if (!current) throw new Error(t("账号配置已发生变化，请重新打开账号管理。"));
      await appStore.setPluginResourceSelection(pluginId, resource.type, {
        activeResourceId: patch.activeResourceId ?? current.activeResourceId,
        automaticSwitching: patch.automaticSwitching ?? current.automaticSwitching,
      });
    });
  };

  return <FormField label={pluginText(resource.displayName, locale)}>
    <div className={styles.resourceSection}>
      <div className={styles.selectionSummary}>
        <strong>{t("当前账号：{name}", { name: selected?.displayName ?? (resource.selection.activeResourceId ? t("账号不可用") : t("未选择账号")) })}</strong>
        <div className={styles.actions}>
          <span>{t("自动切换账号")}</span>
          <Switch checked={resource.selection.automaticSwitching} disabled={selectionPending} aria-busy={selectionPending} aria-describedby={automaticSwitchingDescriptionId} label={t("自动切换账号")} onChange={(automaticSwitching) => select({ automaticSwitching })} />
          {selectionPending && <span role="status">{t("正在保存…")}</span>}
        </div>
        <span id={automaticSwitchingDescriptionId} className={styles.actionDescription}>{t("开启后，当前账号不可用时自动切换到其他可用账号；关闭后仅使用当前账号。")}</span>
        {selectionTask?.error && !visible.some((item) => item.id === selectionTarget) && <span className={styles.error} role="alert">{selectionTask.error}</span>}
      </div>
      {(resource.resources.length > PAGE_SIZE || query) && <div className={styles.toolbar}>
        <TextInput aria-label={t("搜索资源")} placeholder={t("搜索资源")} value={query} onChange={(event) => { setQuery(event.target.value); setPage(1); }} />
      </div>}
      <div className={styles.resourceList}>
        {visible.map((item) => <ResourceRow key={item.id} item={item} resource={resource}
          active={item.id === resource.selection.activeResourceId}
          selectionPending={selectionPending} selectionError={selectionTarget === item.id ? selectionTask?.error : null}
          selecting={selectionPending && selectionTarget === item.id}
          task={manager.tasks[resourceKey(resource.type, item.id)]} now={manager.now}
          onSelect={() => select({ activeResourceId: item.id })}
          onAction={(action) => onAction(item, action)}
          onRefresh={() => void manager.refresh(resource.type, item.id)}
          onDelete={() => setPendingDelete(item)}
        />)}
        {visible.length === 0 && <span className={styles.empty}>{query ? t("没有匹配的账号") : t("还没有资源，请先添加。")}</span>}
      </div>
      {pageCount > 1 && <div className={styles.pagination}>
        <Button size="small" disabled={currentPage <= 1} onClick={() => setPage(currentPage - 1)}>{t("上一页")}</Button>
        <span>{t("第 {page} / {total} 页", { page: currentPage, total: pageCount })}</span>
        <Button size="small" disabled={currentPage >= pageCount} onClick={() => setPage(currentPage + 1)}>{t("下一页")}</Button>
      </div>}
    </div>
    {pendingDelete && <ConfirmDialog
      open
      title={t("删除账号")}
      confirmLabel={t("删除")}
      busy={Boolean(manager.tasks[resourceKey(resource.type, pendingDelete.id)]?.pending)}
      onCancel={() => setPendingDelete(null)}
      onConfirm={() => {
        const item = pendingDelete;
        setPendingDelete(null);
        void manager.run(resource.type, item.id, "delete", async () => {
          await api.deletePluginResource(pluginId, resource.type, item.id);
          await appStore.readPlugins(true);
        });
      }}
    >
      <p>{t("确定删除账号 {name} 吗？删除后需要重新添加才能使用。", { name: pendingDelete.displayName })}</p>
    </ConfirmDialog>}
  </FormField>;
}

function ResourceRow({ item, resource, active, selectionPending, selectionError, selecting, task, now, onSelect, onAction, onRefresh, onDelete }: {
  item: PluginResourceView;
  resource: PluginResourceDescriptor;
  active: boolean;
  selectionPending: boolean;
  selectionError?: string | null;
  selecting: boolean;
  task?: AccountTask;
  now: number;
  onSelect: () => void;
  onAction: (action: PluginResourceAction) => void;
  onRefresh: () => void;
  onDelete: () => void;
}) {
  const { locale } = useI18n();
  const busy = Boolean(task?.pending);
  return <Card className={`${styles.resourceRow} ${active ? styles.activeResource : ""}`}>
    <div className={styles.accountDetails}>
      <strong>{item.displayName} {active && <span className={styles.activeLabel}>{t("当前账号")}</span>}</strong>
      {item.description && <span>{pluginText(item.description, locale)}</span>}
      <span className={styles[item.state.status]}>{item.state.status === "cooling" ? t("冷却中") : item.state.status === "invalid" ? t("已失效") : t("可用")}</span>
      {item.state.message && <span>{item.state.message}</span>}
      {item.state.status === "cooling" && <Deadline kind="retry" at={item.state.retryAtMs} now={now} />}
      {item.metrics.map((metric) => <div className={styles.metric} key={metric.id}>
        <span>{metric.unit === "percent"
          ? t("{label} 剩余 {percent}%", { label: pluginText(metric.label, locale), percent: Math.round(metric.value) })
          : `${pluginText(metric.label, locale)}: ${metric.value}`}</span>
        {showsMetricDeadline(metric, "reset") && <Deadline kind="reset" at={metric.resetAtMs} now={now} />}
        {showsMetricDeadline(metric, "expiry") && <Deadline kind="expiry" at={metric.expiresAtMs} now={now} />}
      </div>)}
      {task?.error && <span className={styles.error} role="alert">{task.error}</span>}
      {selectionError && <span className={styles.error} role="alert">{selectionError}</span>}
    </div>
    <div className={styles.actions}>
      <Button size="small" disabled={active || selectionPending || task?.pending === "delete"} aria-pressed={active} aria-busy={selecting} onClick={onSelect}>{selecting ? t("正在保存…") : t("使用此账号")}</Button>
      {resource.actions.filter((action) => action.target === "resource").map((action) => <Button key={action.id} size="small" disabled={busy} onClick={() => onAction(action)}>{pluginText(action.displayName, locale)}</Button>)}
      {resource.canRefresh && <Button size="small" disabled={busy} aria-busy={task?.pending === "refresh"} onClick={onRefresh}>{task?.pending === "refresh" ? t("正在刷新…") : t("刷新")}</Button>}
      <Button className={styles.deleteButton} size="small" disabled={busy} aria-busy={task?.pending === "delete"} onClick={onDelete}>{task?.pending === "delete" ? t("正在删除…") : t("删除")}</Button>
    </div>
  </Card>;
}

function Deadline({ kind, at, now }: { kind: "reset" | "retry" | "expiry"; at?: number | null; now: number }) {
  const { locale } = useI18n();
  const seconds = remainingSeconds(at, now);
  const label = kind === "reset" ? t("额度重置") : kind === "retry" ? t("冷却结束") : t("最近重置卡到期");
  if (seconds === null) return <span>{t("{label}：时间未知", { label })}</span>;
  const countdown = seconds === 0
    ? (kind === "expiry" ? t("已到期，等待刷新确认") : t("已到时间，等待服务端确认"))
    : t("剩余 {hours} 小时 {minutes} 分 {seconds} 秒", { hours: Math.floor(seconds / 3600), minutes: Math.floor(seconds / 60) % 60, seconds: seconds % 60 });
  return <span>{label}{": "}<time dateTime={new Date(at!).toISOString()}>{new Date(at!).toLocaleString(locale)}</time>{" · "}{countdown}</span>;
}
