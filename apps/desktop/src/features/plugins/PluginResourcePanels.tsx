import { useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  pluginText,
  type PluginAddMethod,
  type PluginDescriptor,
  type PluginOAuthBegin,
  type PluginProviderDescriptor,
  type PluginResourceAction,
  type PluginResourceActionCard,
  type PluginResourceActionResult,
  type PluginResourceDescriptor,
  type PluginResourceView,
} from "../../shared/api";
import { useI18n } from "../../i18n/store";
import { appStore } from "../../shared/store/appStore";
import { Button } from "../../shared/ui/Button";
import { Card } from "../../shared/ui/Card";
import { ConfirmDialog } from "../../shared/ui/ConfirmDialog";
import { FormField, TextInput } from "../../shared/ui/FormControls";
import { Modal } from "../../shared/ui/Modal";
import { Switch } from "../../shared/ui/Switch";
import styles from "./PluginResourcePanels.module.scss";

const PAGE_SIZE = 10;
const ANTIGRAVITY_PLUGIN_ID = "dev.cursorbyok.plugins.antigravity-auth";
const ANTIGRAVITY_RESOURCE_TYPE = "antigravity-account";

function isAntigravityAccountResource(pluginId: string, resourceType: string) {
  return pluginId === ANTIGRAVITY_PLUGIN_ID && resourceType === ANTIGRAVITY_RESOURCE_TYPE;
}

export function PluginAddPanel({ plugin, onConfigured }: { plugin: PluginDescriptor; onConfigured: () => void }) {
  return <div className={styles.panel}>
    {plugin.resources.map((resource) => <ResourceAddSection
      key={resource.type}
      plugin={plugin}
      resource={resource}
      onConfigured={onConfigured}
    />)}
    {plugin.resources.length === 0 && <span className={styles.empty}>{t("该插件不需要添加资源")}</span>}
  </div>;
}

function ResourceAddSection({ plugin, resource, onConfigured }: {
  plugin: PluginDescriptor;
  resource: PluginResourceDescriptor;
  onConfigured: () => void;
}) {
  return <>
    {resource.add.map((method) => <OAuthMethodCard
      key={method.id}
      pluginId={plugin.id}
      resourceType={resource.type}
      method={method}
      onConfigured={onConfigured}
    />)}
  </>;
}

function OAuthMethodCard({ pluginId, resourceType, method, onConfigured }: {
  pluginId: string;
  resourceType: string;
  method: PluginAddMethod;
  onConfigured: () => void;
}) {
  const { locale } = useI18n();
  const [status, setStatus] = useState<"idle" | "starting" | "polling" | "success" | "error">("idle");
  const [begun, setBegun] = useState<PluginOAuthBegin | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const stopped = useRef(false);

  const copyCode = async (code: string) => {
    await api.copyCursorText(code).catch(() => undefined);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 2000);
  };

  useEffect(() => () => { stopped.current = true; }, []);

  useEffect(() => {
    if (!begun || status !== "polling") return;
    let timer = 0;
    const poll = async (intervalMs: number) => {
      if (stopped.current) return;
      try {
        const result = await api.pluginOAuthPoll(begun.sessionId);
        if (stopped.current) return;
        if (result.status === "pending") {
          timer = window.setTimeout(() => void poll(result.pollIntervalMs), Math.max(1000, result.pollIntervalMs));
          return;
        }
        if (result.status === "completed") {
          await appStore.refreshPlugins();
          if (result.modelSyncError) {
            setStatus("error");
            setError(t("账号已保存，但同步模型失败：{error}", { error: result.modelSyncError }));
            return;
          }
          setStatus("success");
          onConfigured();
          return;
        }
        setStatus("error");
        setError(result.message || t("授权被拒绝或已失败。"));
      } catch (cause) {
        if (stopped.current) return;
        setError(errorText(cause));
        timer = window.setTimeout(() => void poll(intervalMs), Math.max(1000, intervalMs));
      }
    };
    timer = window.setTimeout(() => void poll(begun.pollIntervalMs), Math.max(1000, begun.pollIntervalMs));
    return () => window.clearTimeout(timer);
  }, [begun, onConfigured, status]);

  const start = async () => {
    setStatus("starting");
    setError(null);
    try {
      const next = await api.pluginOAuthBegin(pluginId, resourceType, method.id);
      setBegun(next);
      setStatus("polling");
      if (next.userCode) await api.copyCursorText(next.userCode).catch(() => undefined);
      await api.openExternalUrl(next.verificationUrlComplete || next.verificationUrl);
    } catch (cause) {
      setStatus("error");
      setError(errorText(cause));
    }
  };

  const userCode = begun?.userCode;
  return <Card className={styles.methodCard}>
    <strong>{pluginText(method.displayName, locale)}</strong>
    {method.description && <span>{pluginText(method.description, locale)}</span>}
    {userCode && status === "polling" && <div className={styles.deviceCode}>
      <small>{t("设备验证码")}</small>
      <button type="button" onClick={() => void copyCode(userCode)}>{userCode}</button>
      <button type="button" className={styles.copy} onClick={() => void copyCode(userCode)}>
        {copied ? t("已复制") : t("复制")}
      </button>
    </div>}
    <div className={styles.actions}>
      <Button variant="primary" disabled={status === "starting" || status === "polling"} onClick={() => void start()}>
        {status === "starting" ? t("正在申请授权码…") : status === "polling" ? t("等待网页端确认授权中…") : t("开始登录")}
      </Button>
      {begun && status === "polling" && <Button onClick={() => void api.openExternalUrl(begun.verificationUrlComplete || begun.verificationUrl)}>{t("打开授权网页")}</Button>}
    </div>
    {status === "success" && <span className={styles.success}>{t("账号已保存，模型目录已同步。")}</span>}
    {error && <span className={styles.error} role="alert">{error}</span>}
  </Card>;
}

export function PluginSettingsPanel({ plugin, onResourcesEmpty }: {
  plugin: PluginDescriptor;
  onResourcesEmpty: () => void;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [modelProviderId, setModelProviderId] = useState<string | null>(null);
  const [resourceAction, setResourceAction] = useState<{
    resource: PluginResourceDescriptor;
    item: PluginResourceView;
  } | null>(null);
  const [resourceActionResult, setResourceActionResult] = useState<PluginResourceActionResult | null>(null);
  const [resourceActionError, setResourceActionError] = useState<string | null>(null);
  const modelProvider = modelProviderId ? plugin.providers.find((provider) => provider.id === modelProviderId) ?? null : null;
  const quotaNow = useQuotaClock(plugin.resources);
  usePluginSnapshotPoll();

  const run = async (key: string, task: () => Promise<void>) => {
    setBusy(key);
    setError(null);
    try {
      await task();
      await appStore.refreshPlugins();
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusy(null);
    }
  };

  const executeResourceAction = async (
    target: { resource: PluginResourceDescriptor; item: PluginResourceView },
    action: PluginResourceAction,
    input: unknown = {},
  ) => {
    const key = `action:${target.item.id}:${action.id}`;
    setBusy(key);
    setResourceActionError(null);
    try {
      const result = await api.pluginResourceAction(
        plugin.id,
        target.resource.type,
        target.item.id,
        action.id,
        input,
      );
      setResourceActionResult(result);
      await appStore.refreshPlugins();
    } catch (cause) {
      setResourceActionError(errorText(cause));
    } finally {
      setBusy(null);
    }
  };

  const openResourceAction = (resource: PluginResourceDescriptor, item: PluginResourceView, action: PluginResourceAction) => {
    setResourceAction({ resource, item });
    setResourceActionResult(null);
    setResourceActionError(null);
    void executeResourceAction({ resource, item }, action);
  };

  const applyModels = async (provider: PluginProviderDescriptor, enabledByModel: Record<string, boolean>) => {
    await run("models", async () => {
      for (const model of provider.models) {
        const enabled = enabledByModel[model.id] ?? model.enabled;
        if (model.enabled !== enabled) await api.setPluginModelEnabled(plugin.id, provider.id, model.modelId, enabled);
      }
    });
    setModelProviderId(null);
  };

  return <div className={styles.panel}>
    {plugin.providers.map((provider) => <ProviderRow
      key={provider.id}
      provider={provider}
      busy={busy !== null}
      syncing={busy === `sync:${provider.id}`}
      onManageModels={() => setModelProviderId(provider.id)}
      onSync={() => void run(`sync:${provider.id}`, async () => {
        await api.syncPluginModels(plugin.id, provider.id);
      })}
    />)}
    {plugin.resources.map((resource) => <ResourceList
      key={resource.type}
      pluginId={plugin.id}
      resource={resource}
      busy={busy !== null}
      now={quotaNow}
      onAction={(item, action) => openResourceAction(resource, item, action)}
      onRefresh={(item) => void run(`refresh:${item.id}`, async () => {
        await api.refreshPluginResource(plugin.id, resource.type, item.id);
      })}
      onDelete={(item) => void run(`delete:${item.id}`, async () => {
        await api.deletePluginResource(plugin.id, resource.type, item.id);
        await appStore.refreshPlugins();
        const refreshed = appStore.getSnapshot().plugins.find((candidate) => candidate.id === plugin.id);
        const remaining = refreshed?.resources.find((candidate) => candidate.type === resource.type)?.resources.length ?? 0;
        if (isAntigravityAccountResource(plugin.id, resource.type) && remaining === 0) onResourcesEmpty();
      })}
    />)}
    {error && <span className={styles.error} role="alert">{error}</span>}
    {modelProvider && <ModelManagementModal
      provider={modelProvider}
      busy={busy !== null}
      onClose={() => setModelProviderId(null)}
      onSubmit={(enabledByModel) => void applyModels(modelProvider, enabledByModel)}
    />}
    {resourceAction && <ResourceActionModal
      action={resourceAction.resource.actions.find((item) => item.target === "resource") ?? null}
      cardAction={resourceAction.resource.actions.find((item) => item.target === "card") ?? null}
      result={resourceActionResult}
      busy={busy !== null}
      error={resourceActionError}
      onClose={() => setResourceAction(null)}
      onCardAction={(action, card) => void executeResourceAction(resourceAction, action, { cardId: card.id })}
    />}
  </div>;
}

function ProviderRow({ provider, busy, syncing, onManageModels, onSync }: {
  provider: PluginProviderDescriptor;
  busy: boolean;
  syncing: boolean;
  onManageModels: () => void;
  onSync: () => void;
}) {
  const { locale } = useI18n();
  return <Card className={styles.providerRow}>
    <div>
      <strong>{pluginText(provider.displayName, locale)}</strong>
      <span>
        {provider.providerType}
        {" · "}
        {provider.models.length > 0 ? t("{count} 个模型", { count: provider.models.length }) : t("尚未同步模型")}
        {" · "}
        {provider.configured ? t("可调用") : t("未就绪")}
      </span>
    </div>
    {provider.hasModels && <div className={styles.actions}>
      <Button size="small" disabled={busy || provider.models.length === 0} onClick={onManageModels}>{t("模型管理")}</Button>
      <Button size="small" disabled={busy} onClick={onSync}>
        {syncing ? t("正在同步…") : t("同步模型")}
      </Button>
    </div>}
  </Card>;
}

function ModelManagementModal({ provider, busy, onClose, onSubmit }: {
  provider: PluginProviderDescriptor;
  busy: boolean;
  onClose: () => void;
  onSubmit: (enabledByModel: Record<string, boolean>) => void;
}) {
  const { locale } = useI18n();
  const [enabledByModel, setEnabledByModel] = useState<Record<string, boolean>>(
    () => Object.fromEntries(provider.models.map((model) => [model.id, model.enabled])),
  );

  const setAll = (enabled: boolean) => {
    setEnabledByModel(Object.fromEntries(provider.models.map((model) => [model.id, enabled])));
  };

  return <Modal
    fullHeight
    open
    title={t("{name} 模型管理", { name: pluginText(provider.displayName, locale) })}
    busy={busy}
    onClose={onClose}
    onSubmit={() => onSubmit(enabledByModel)}
    submitLabel={t("确定")}
  >
    <div className={styles.modelToolbar}>
      <Button size="small" disabled={busy || provider.models.length === 0} onClick={() => setAll(true)}>{t("全选")}</Button>
      <Button size="small" disabled={busy || provider.models.length === 0} onClick={() => setAll(false)}>{t("全不选")}</Button>
    </div>
    <div className={styles.modelTableWrap}>
      <table className={styles.modelTable}>
        <thead><tr><th scope="col">{t("模型名称")}</th><th scope="col">{t("启用")}</th></tr></thead>
        <tbody>
          {provider.models.map((model) => <tr key={model.id}>
            <td><div className={styles.modelName}>
              <strong>{model.displayName}</strong>
            </div></td>
            <td><Switch
              checked={enabledByModel[model.id] ?? model.enabled}
              disabled={busy}
              label={t("启用 {model}", { model: model.displayName })}
              onChange={(enabled) => setEnabledByModel((current) => ({ ...current, [model.id]: enabled }))}
            /></td>
          </tr>)}
        </tbody>
      </table>
      {provider.models.length === 0 && <span className={styles.empty}>{t("尚未同步模型")}</span>}
    </div>
  </Modal>;
}

function ResourceList({ pluginId, resource, busy, now, onAction, onRefresh, onDelete }: {
  pluginId: string;
  resource: PluginResourceDescriptor;
  busy: boolean;
  now: number;
  onAction: (item: PluginResourceView, action: PluginResourceAction) => void;
  onRefresh: (item: PluginResourceView) => void;
  onDelete: (item: PluginResourceView) => void;
}) {
  const { locale } = useI18n();
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(1);
  const filtered = useMemo(
    () => resource.resources.filter((item) => item.displayName.toLowerCase().includes(query.trim().toLowerCase())),
    [resource.resources, query],
  );
  const pageCount = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const visible = filtered.slice((Math.min(page, pageCount) - 1) * PAGE_SIZE, Math.min(page, pageCount) * PAGE_SIZE);

  useEffect(() => setPage(1), [query]);

  const content = <div className={styles.resourceSection}>
      {resource.resources.length > PAGE_SIZE && <div className={styles.toolbar}>
        <TextInput aria-label={t("搜索资源")} placeholder={t("搜索资源")} value={query} onChange={(event) => setQuery(event.target.value)} />
      </div>}
      <div className={styles.resourceList}>
        {visible.map((item) => <ResourceRow
          key={item.id}
          isAntigravityAccount={isAntigravityAccountResource(pluginId, resource.type)}
          item={item}
          actions={resource.actions.filter((action) => action.target === "resource")}
          canRefresh={resource.canRefresh}
          disabled={busy}
          now={now}
          onAction={(action) => onAction(item, action)}
          onRefresh={() => onRefresh(item)}
          onDelete={() => onDelete(item)}
        />)}
        {visible.length === 0 && <span className={styles.empty}>{t("还没有资源，请先添加。")}</span>}
      </div>
      {pageCount > 1 && <div className={styles.pagination}>
        <Button size="small" disabled={page <= 1} onClick={() => setPage((current) => current - 1)}>{t("上一页")}</Button>
        <span>{t("第 {page} / {total} 页", { page: Math.min(page, pageCount), total: pageCount })}</span>
        <Button size="small" disabled={page >= pageCount} onClick={() => setPage((current) => current + 1)}>{t("下一页")}</Button>
      </div>}
    </div>;

  return isAntigravityAccountResource(pluginId, resource.type)
    ? content
    : <FormField label={pluginText(resource.displayName, locale)}>{content}</FormField>;
}

function ResourceRow({ isAntigravityAccount, item, actions, canRefresh, disabled, now, onAction, onRefresh, onDelete }: {
  isAntigravityAccount: boolean;
  item: PluginResourceView;
  actions: PluginResourceAction[];
  canRefresh: boolean;
  disabled: boolean;
  now: number;
  onAction: (action: PluginResourceAction) => void;
  onRefresh: () => void;
  onDelete: () => void;
}) {
  const { locale } = useI18n();
  const resourceActions = actions.length > 0 || canRefresh;
  return <Card className={styles.resourceRow}>
    <div className={styles.resourceHeader}>
      <div className={styles.resourceIdentity}>
        <div className={styles.resourceNameAndState}>
          <strong title={item.displayName}>{item.displayName}</strong>
          {isAntigravityAccount && <StateBadge state={item.state} />}
        </div>
        {item.description && <span title={pluginText(item.description, locale)}>{pluginText(item.description, locale)}</span>}
      </div>
      <div className={styles.resourceOperations}>
        {!isAntigravityAccount && <StateBadge state={item.state} />}
        {resourceActions && <div className={styles.resourceActionButtons} aria-label={t("资源操作")}>
          {actions.map((action) => <Button key={action.id} size="small" disabled={disabled} onClick={() => onAction(action)}>{pluginText(action.displayName, locale)}</Button>)}
          {canRefresh && <Button size="small" disabled={disabled} onClick={onRefresh}>{t("刷新")}</Button>}
        </div>}
        <Button size="small" disabled={disabled} onClick={onDelete}>{isAntigravityAccount ? t("删除账户") : t("删除")}</Button>
      </div>
    </div>
    <QuotaMetrics
      metrics={item.metrics}
      locale={locale}
      now={now}
      isAntigravityAccount={isAntigravityAccount}
    />
  </Card>;
}

function QuotaMetrics({ metrics, locale, now, isAntigravityAccount }: {
  metrics: PluginResourceView["metrics"];
  locale: string;
  now: number;
  isAntigravityAccount: boolean;
}) {
  const [period, setPeriod] = useState<"5h" | "weekly">("5h");

  if (isAntigravityAccount) {
    const suffix = period === "5h" ? "5h" : "weekly";
    const pools = [
      { id: "gemini", label: "Gemini" },
      { id: "claude-gpt", label: "Claude/GPT" },
    ];

    return <section className={styles.quotaGroups} aria-label={t("模型配额")}>
      <div className={styles.quotaGroupHeader}>
        <span className={styles.quotaGroupTitle}>{t("用量限额")}</span>
        <div className={styles.quotaPeriodToggle}>
          <Button size="small" variant={period === "5h" ? "primary" : "secondary"} onClick={() => setPeriod("5h")} aria-pressed={period === "5h"}>{t("5 小时")}</Button>
          <Button size="small" variant={period === "weekly" ? "primary" : "secondary"} onClick={() => setPeriod("weekly")} aria-pressed={period === "weekly"}>{t("每周")}</Button>
        </div>
      </div>
      <div className={styles.quotaGrid}>{pools.map((pool) => {
        const metric = metrics.find((entry) => entry.id === `pool:${pool.id}:${suffix}`);
        return metric
          ? <QuotaMetric key={pool.id} metric={metric} locale={locale} now={now} poolLabel={pool.label} period={period} />
          : <div key={pool.id} className={styles.quotaMetric}>
            <span>{pool.label}</span><span className={styles.quotaEmpty}>{t("暂无配额数据")}</span>
          </div>;
      })}</div>
    </section>;
  }

  const modelMetrics = metrics.filter((metric) => metric.id.startsWith("model:") || metric.id === "five-hour");
  const weeklyMetrics = metrics.filter((metric) => metric.id.startsWith("group:") || metric.id === "weekly");
  const otherMetrics = metrics.filter((metric) => !modelMetrics.includes(metric) && !weeklyMetrics.includes(metric));

  return <div className={styles.quotaGroups}>
    {modelMetrics.length > 0 && <QuotaGroup title={t("模型配额")} metrics={modelMetrics} locale={locale} now={now} />}
    {weeklyMetrics.length > 0 && <QuotaGroup title={t("每周配额")} metrics={weeklyMetrics} locale={locale} now={now} />}
    {otherMetrics.length > 0 && <QuotaGroup metrics={otherMetrics} locale={locale} now={now} />}
  </div>;
}

function QuotaGroup({ title, metrics, locale, now }: {
  title?: string;
  metrics: PluginResourceView["metrics"];
  locale: string;
  now: number;
}) {
  return <section className={styles.quotaGroup} aria-label={title}>
    {title && <span className={styles.quotaGroupTitle}>{title}</span>}
    <div className={styles.quotaGrid}>
      {metrics.map((metric) => <QuotaMetric key={metric.id} metric={metric} locale={locale} now={now} />)}
    </div>
  </section>;
}

function QuotaMetric({ metric, locale, now, poolLabel, period }: {
  metric: PluginResourceView["metrics"][number];
  locale: string;
  now: number;
  poolLabel?: string;
  period?: "5h" | "weekly";
}) {
  const label = poolLabel ?? pluginText(metric.label, locale);
  const remainingPercent = Math.max(0, Math.min(100, Math.round(metric.value)));
  const usedPercent = 100 - remainingPercent;
  const resetAt = metric.resetAtMs ? formatActionDate(metric.resetAtMs, locale) : null;
  const fullLabel = `${label} · ${metric.id.replace(/^model:/, "")}`;
  const title = resetAt ? t("{label}：已使用 {percent}%，重置时间 {resetAt}", { label: fullLabel, percent: usedPercent, resetAt }) : t("{label}：已使用 {percent}%", { label: fullLabel, percent: usedPercent });

  if (metric.unit !== "percent") return <div className={styles.quotaMetric} title={title}>
    <span className={styles.quotaName}>{label}</span>
    <strong className={styles.quotaValue}>{metric.value}</strong>
  </div>;

  return <div className={styles.quotaMetric} title={title}>
    <div className={styles.quotaMetricHeader}>
      <span className={styles.quotaName}>{label}</span>
      <span className={styles.quotaMeta}>
        <span>{t("已使用 {percent}%", { percent: usedPercent })}</span>
      </span>
    </div>
    <div className={styles.quotaTrack} role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={usedPercent} aria-label={title}>
      <span className={styles.quotaFill} style={{ width: `${usedPercent}%` }} />
    </div>
    {metric.resetAtMs && <span className={styles.quotaReset}>{t("重置时间：{time}", { time: period === "weekly" ? formatWeeklyResetDate(metric.resetAtMs, locale) : formatCountdown(metric.resetAtMs, now) })}</span>}
  </div>;
}

function ResourceActionModal({ action, cardAction, result, busy, error, onClose, onCardAction }: {
  action: PluginResourceAction | null;
  cardAction: PluginResourceAction | null;
  result: PluginResourceActionResult | null;
  busy: boolean;
  error: string | null;
  onClose: () => void;
  onCardAction: (action: PluginResourceAction, card: PluginResourceActionCard) => void;
}) {
  const { locale } = useI18n();
  const [pendingCard, setPendingCard] = useState<PluginResourceActionCard | null>(null);
  const title = result ? pluginText(result.title, locale) : action ? pluginText(action.displayName, locale) : t("资源详情");
  const cardActions = cardAction ? [cardAction] : [];

  return <>
    <Modal compact open title={title} busy={busy} onClose={onClose} submitLabel={t("关闭")} onSubmit={onClose}>
      <div className={styles.actionBody}>
        {result?.description && <span className={styles.actionDescription}>{pluginText(result.description, locale)}</span>}
        {busy && <span className={styles.empty}>{t("正在加载…")}</span>}
        {error && <span className={styles.error} role="alert">{error}</span>}
        {!busy && !error && result && result.cards.length === 0 && <span className={styles.empty}>{t("没有可用的重置卡。")}</span>}
        {!busy && !error && result && <div className={styles.actionCardList}>
        {result.cards.map((card) => <Card key={card.id} className={styles.actionCard}>
          <div className={styles.actionCardMain}>
            <strong>{pluginText(card.title, locale)}</strong>
            {card.status && <span>{formatActionStatus(card.status, locale)}</span>}
            {card.grantedAtMs !== null && card.grantedAtMs !== undefined && <span>{t("发放时间：{time}", { time: formatActionDate(card.grantedAtMs, locale) })}</span>}
            {card.expiresAtMs !== null && card.expiresAtMs !== undefined && <span>{t("到期时间：{time}", { time: formatActionDate(card.expiresAtMs, locale) })}</span>}
            {card.fields.map((field) => <span key={field.id}>{pluginText(field.label, locale)}: {field.value}</span>)}
          </div>
          {cardActions.length > 0 && <div className={styles.actions}>
            {cardActions.map((cardActionItem) => <Button
              key={cardActionItem.id}
              size="small"
              disabled={busy || card.status !== "available"}
              onClick={() => cardActionItem.destructive ? setPendingCard(card) : onCardAction(cardActionItem, card)}
            >{pluginText(cardActionItem.displayName, locale)}</Button>)}
          </div>}
        </Card>)}
      </div>}
      </div>
    </Modal>
    {pendingCard && cardAction && <ConfirmDialog
      open
      title={t("使用重置卡")}
      busy={busy}
      confirmLabel={t("确认使用")}
      onCancel={() => setPendingCard(null)}
      onConfirm={() => {
        const card = pendingCard;
        setPendingCard(null);
        onCardAction(cardAction, card);
      }}
    >
      <p>{t("使用后会立即消耗这张重置卡，且无法恢复。确定继续吗？")}</p>
      <strong>{pluginText(pendingCard.title, locale)}</strong>
    </ConfirmDialog>}
  </>;
}

function formatActionStatus(status: PluginResourceActionCard["status"], locale: string) {
  const value = typeof status === "string" ? status : pluginText(status, locale);
  switch (value.toLowerCase()) {
    case "available": return t("可用");
    case "redeemed":
    case "used": return t("已使用");
    case "expired": return t("已过期");
    default: return value;
  }
}

function usePluginSnapshotPoll() {
  useEffect(() => {
    let stopped = false;
    let timer = 0;
    const poll = async () => {
      if (document.visibilityState === "visible") await appStore.refreshPlugins();
      if (!stopped) timer = window.setTimeout(() => void poll(), 30_000);
    };
    timer = window.setTimeout(() => void poll(), 30_000);
    return () => { stopped = true; window.clearTimeout(timer); };
  }, []);
}

function useQuotaClock(resources: PluginResourceDescriptor[]) {
  const hasResetTime = resources.some((resource) => resource.resources.some((item) => item.metrics.some((metric) => metric.resetAtMs)));
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!hasResetTime) return;
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(timer);
  }, [hasResetTime]);

  return now;
}

function formatCountdown(resetAtMs: number, now: number) {
  const remainingMinutes = Math.ceil((resetAtMs - now) / 60_000);
  if (remainingMinutes <= 0) return t("即将重置");
  const hours = Math.floor(remainingMinutes / 60);
  const minutes = remainingMinutes % 60;
  if (hours > 0) return minutes > 0
    ? t("{hours} 小时 {minutes} 分钟", { hours, minutes })
    : t("{hours} 小时", { hours });
  return t("{minutes} 分钟", { minutes });
}

function formatWeeklyResetDate(value: number, locale: string) {
  const parts = new Intl.DateTimeFormat(locale, {
    month: "numeric",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).formatToParts(new Date(value));
  const get = (type: Intl.DateTimeFormatPartTypes) => parts.find((part) => part.type === type)?.value ?? "";
  return locale.startsWith("zh")
    ? `${get("month")}月${get("day")}日${get("hour")}:${get("minute")}`
    : `${get("month")}/${get("day")} ${get("hour")}:${get("minute")}`;
}

function formatActionDate(value: number, locale: string) {
  return new Date(value).toLocaleString(locale);
}

function StateBadge({ state }: { state: PluginResourceView["state"] }) {
  if (state.status === "cooling") {
    return <span className={styles.cooling} title={state.message ?? undefined}>{t("冷却中")}</span>;
  }
  if (state.status === "invalid") {
    return <span className={styles.invalid} title={state.message ?? undefined}>{t("已失效")}</span>;
  }
  return <span className={styles.ready}>{t("可用")}</span>;
}

function errorText(cause: unknown) {
  return cause instanceof Error ? cause.message : String(cause);
}
