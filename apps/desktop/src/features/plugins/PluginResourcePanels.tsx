import { useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  pluginText,
  type PluginAddMethod,
  type PluginAutomationDescriptor,
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
const ALL_RESOURCE_ACTION_BUSY_PREFIX = "action-all:";

type ResourceBatchActionItemResult = {
  id: string;
  displayName: string;
  succeeded: boolean;
  description: PluginResourceActionResult["description"];
  error: string | null;
};

type ResourceBatchActionSummary = {
  action: PluginResourceAction;
  items: ResourceBatchActionItemResult[];
};

function resourceBatchBusyKey(resource: PluginResourceDescriptor, action: PluginResourceAction) {
  return `${ALL_RESOURCE_ACTION_BUSY_PREFIX}${resource.type}:${action.id}`;
}

function requestPluginResourceAction(
  pluginId: string,
  target: { resource: PluginResourceDescriptor; item: PluginResourceView },
  action: PluginResourceAction,
  input: unknown,
) {
  return api.pluginResourceAction(
    pluginId,
    target.resource.type,
    target.item.id,
    action.id,
    input,
  );
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

export function PluginSettingsPanel({ plugin }: { plugin: PluginDescriptor }) {
  const { locale } = useI18n();
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [modelProviderId, setModelProviderId] = useState<string | null>(null);
  const [resourceAction, setResourceAction] = useState<{
    resource: PluginResourceDescriptor;
    item: PluginResourceView;
  } | null>(null);
  const [resourceActionResult, setResourceActionResult] = useState<PluginResourceActionResult | null>(null);
  const [resourceBatchSummary, setResourceBatchSummary] = useState<ResourceBatchActionSummary | null>(null);
  const [automationOverrides, setAutomationOverrides] = useState<
    Record<string, PluginAutomationDescriptor>
  >({});
  const [resourceActionError, setResourceActionError] = useState<string | null>(null);
  const [resourceDelete, setResourceDelete] = useState<{
    resource: PluginResourceDescriptor;
    item: PluginResourceView;
  } | null>(null);
  const modelProvider = modelProviderId ? plugin.providers.find((provider) => provider.id === modelProviderId) ?? null : null;

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
      const result = await requestPluginResourceAction(plugin.id, target, action, input);
      setResourceActionResult(result);
      await appStore.refreshPlugins();
    } catch (cause) {
      setResourceActionError(errorText(cause));
    } finally {
      setBusy(null);
    }
  };

  const runAllResourceActions = async (resource: PluginResourceDescriptor, action: PluginResourceAction) => {
    if (busy !== null) return;
    const items: ResourceBatchActionItemResult[] = [];
    setBusy(resourceBatchBusyKey(resource, action));
    setResourceActionError(null);
    setResourceBatchSummary(null);
    try {
      for (const item of resource.resources) {
        try {
          const result = await requestPluginResourceAction(plugin.id, { resource, item }, action, {});
          items.push({
            id: item.id,
            displayName: item.displayName,
            succeeded: result.succeeded,
            description: result.succeeded ? result.description : null,
            error: result.succeeded ? null : result.description
              ? pluginText(result.description, locale)
              : t("操作未成功"),
          });
        } catch (cause) {
          items.push({
            id: item.id,
            displayName: item.displayName,
            succeeded: false,
            description: null,
            error: errorText(cause),
          });
        }
      }
      setResourceBatchSummary({ action, items });
      try {
        await appStore.refreshPlugins();
      } catch (cause) {
        setResourceActionError(errorText(cause));
      }
    } finally {
      setBusy(null);
    }
  };

  const updateResourceAutomation = async (
    resource: PluginResourceDescriptor,
    automation: PluginAutomationDescriptor,
    enabled: boolean,
  ) => {
    const key = `automation:${resource.type}:${automation.actionId}`;
    if (busy !== null) return;
    setBusy(key);
    setError(null);
    try {
      const updated = await api.setPluginResourceAutomationEnabled(
        plugin.id,
        resource.type,
        automation.actionId,
        enabled,
      );
      const overrideKey = `${resource.type}:${automation.actionId}`;
      // Optimistic only: the refreshed descriptor is authoritative, so drop the
      // override instead of shadowing later scheduler updates indefinitely.
      setAutomationOverrides((current) => ({ ...current, [overrideKey]: updated }));
      await appStore.refreshPlugins();
      setAutomationOverrides((current) => {
        const { [overrideKey]: _dropped, ...rest } = current;
        return rest;
      });
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusy(null);
    }
  };

  const openResourceAction = (resource: PluginResourceDescriptor, item: PluginResourceView, action: PluginResourceAction) => {
    if (busy !== null) return;
    setResourceAction({ resource, item });
    setResourceActionResult(null);
    setResourceActionError(null);
    void executeResourceAction({ resource, item }, action);
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
      automationOverrides={automationOverrides}
      resource={resource}
      busy={busy}
      onAction={(item, action) => openResourceAction(resource, item, action)}
      onAllAction={(action) => void runAllResourceActions(resource, action)}
      onAutomationChange={(automation, enabled) => void updateResourceAutomation(resource, automation, enabled)}
      onRefresh={(item) => void run(`refresh:${item.id}`, async () => {
        await api.refreshPluginResource(plugin.id, resource.type, item.id);
      })}
      onDelete={(item) => setResourceDelete({ resource, item })}
    />)}
    {error && <span className={styles.error} role="alert">{error}</span>}
    {modelProvider && <ModelManagementModal
      provider={modelProvider}
      busy={busy !== null}
      onClose={() => setModelProviderId(null)}
      onSubmit={(enabledByModel) => void run("models", async () => {
        for (const model of modelProvider.models) {
          const enabled = enabledByModel[model.id] ?? model.enabled;
          if (model.enabled !== enabled) await api.setPluginModelEnabled(plugin.id, modelProvider.id, model.modelId, enabled);
        }
      })}
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
    {resourceBatchSummary && <ResourceBatchActionModal
      summary={resourceBatchSummary}
      onClose={() => setResourceBatchSummary(null)}
    />}
    {resourceDelete && <ConfirmDialog
      open
      title={t("删除资源")}
      confirmLabel={t("删除")}
      busy={busy !== null}
      onCancel={() => setResourceDelete(null)}
      onConfirm={() => {
        const target = resourceDelete;
        setResourceDelete(null);
        void run(`delete:${target.item.id}`, async () => {
          await api.deletePluginResource(plugin.id, target.resource.type, target.item.id);
        });
      }}
    >
      <span>{t("确定要删除 {resource} 吗？此操作无法撤销。", { resource: resourceDelete.item.displayName })}</span>
    </ConfirmDialog>}
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
  const [enabledByModel, setEnabledByModel] = useState<Record<string, boolean>>({});

  useEffect(() => {
    setEnabledByModel(Object.fromEntries(provider.models.map((model) => [model.id, model.enabled])));
  }, [provider.models]);

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
              {model.description && <span>{model.description}</span>}
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

function ResourceList({ resource, automationOverrides, busy, onAction, onAllAction, onAutomationChange, onRefresh, onDelete }: {
  resource: PluginResourceDescriptor;
  automationOverrides: Record<string, PluginAutomationDescriptor>;
  busy: string | null;
  onAction: (item: PluginResourceView, action: PluginResourceAction) => void;
  onAllAction: (action: PluginResourceAction) => void;
  onAutomationChange: (automation: PluginAutomationDescriptor, enabled: boolean) => void;
  onRefresh: (item: PluginResourceView) => void;
  onDelete: (item: PluginResourceView) => void;
}) {
  const { locale } = useI18n();
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(1);
  const resourceActions = resource.actions.filter((action) => action.target === "resource");
  const onlyResourceAction = resourceActions.length === 1 ? resourceActions[0] : null;
  const allResourceAction = resource.resources.length > 1
    && onlyResourceAction !== null
    && onlyResourceAction.destructive !== true
    ? onlyResourceAction
    : null;
  const allActionRunning = allResourceAction !== null && busy === resourceBatchBusyKey(resource, allResourceAction);
  const filtered = useMemo(
    () => resource.resources.filter((item) => item.displayName.toLowerCase().includes(query.trim().toLowerCase())),
    [resource.resources, query],
  );
  const pageCount = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const visible = filtered.slice((Math.min(page, pageCount) - 1) * PAGE_SIZE, Math.min(page, pageCount) * PAGE_SIZE);

  useEffect(() => setPage(1), [query]);

  return <FormField label={pluginText(resource.displayName, locale)}>
    <div className={styles.resourceSection}>
      {(resource.automations ?? []).map((automation) => {
        const override = automationOverrides[`${resource.type}:${automation.actionId}`];
        const current = override ?? automation;
        const action = resource.actions.find((candidate) => candidate.id === current.actionId) ?? null;
        if (!action) return null;
        return <Card key={automation.actionId} className={styles.automationCard}>
          <div className={styles.automationMain}>
            <strong>{pluginText(action.displayName, locale)}</strong>
            {action.description && <span>{pluginText(action.description, locale)}</span>}
            <span>{formatAutomationLastRun(current, locale)}</span>
          </div>
          <Switch
            checked={current.enabled}
            disabled={busy !== null}
            label={t("启用 {action}", { action: pluginText(action.displayName, locale) })}
            onChange={(enabled) => onAutomationChange(automation, enabled)}
          />
        </Card>;
      })}
      {(resource.resources.length > PAGE_SIZE || allResourceAction) && <div className={styles.toolbar}>
        {resource.resources.length > PAGE_SIZE && <TextInput aria-label={t("搜索资源")} placeholder={t("搜索资源")} value={query} onChange={(event) => setQuery(event.target.value)} />}
        {allResourceAction && <Button
          size="small"
          disabled={busy !== null}
          onClick={() => onAllAction(allResourceAction)}
        >{allActionRunning
          ? t("全部账号 · 正在执行{action}…", { action: pluginText(allResourceAction.displayName, locale) })
          : t("全部账号 · {action}", { action: pluginText(allResourceAction.displayName, locale) })}</Button>}
      </div>}
      <div className={styles.resourceList}>
        {visible.map((item) => <ResourceRow
          key={item.id}
          item={item}
          actions={resourceActions}
          canRefresh={resource.canRefresh}
          disabled={busy !== null}
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
    </div>
  </FormField>;
}

function ResourceRow({ item, actions, canRefresh, disabled, onAction, onRefresh, onDelete }: {
  item: PluginResourceView;
  actions: PluginResourceAction[];
  canRefresh: boolean;
  disabled: boolean;
  onAction: (action: PluginResourceAction) => void;
  onRefresh: () => void;
  onDelete: () => void;
}) {
  const { locale } = useI18n();
  return <Card className={styles.resourceRow}>
    <div>
      <strong>{item.displayName}</strong>
      {item.description && <span>{pluginText(item.description, locale)}</span>}
      {item.metrics.map((metric) => <span key={metric.id}>
        {metric.unit === "percent"
          ? t("{label} 剩余 {percent}%", { label: pluginText(metric.label, locale), percent: Math.round(metric.value) })
          : `${pluginText(metric.label, locale)}: ${metric.value}`}
      </span>)}
    </div>
    <div className={styles.actions}>
      <StateBadge state={item.state} />
      {actions.map((action) => <Button key={action.id} size="small" disabled={disabled} onClick={() => onAction(action)}>{pluginText(action.displayName, locale)}</Button>)}
      {canRefresh && <Button size="small" disabled={disabled} onClick={onRefresh}>{t("刷新")}</Button>}
      <Button size="small" disabled={disabled} onClick={onDelete}>{t("删除")}</Button>
    </div>
  </Card>;
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

function ResourceBatchActionModal({ summary, onClose }: {
  summary: ResourceBatchActionSummary;
  onClose: () => void;
}) {
  const { locale } = useI18n();
  const successful = summary.items.filter((item) => item.succeeded).length;
  const failed = summary.items.length - successful;
  return <Modal
    compact
    open
    title={t("全部账号执行结果：{action}", { action: pluginText(summary.action.displayName, locale) })}
    onClose={onClose}
    submitLabel={t("关闭")}
    onSubmit={onClose}
  >
    <div className={styles.actionBody}>
      <span className={styles.actionDescription}>
        {t("共 {total} 个账号：成功 {successful}，失败 {failed}", {
          total: summary.items.length,
          successful,
          failed,
        })}
      </span>
      <div className={styles.actionCardList}>
        {summary.items.map((item) => <Card key={item.id} className={styles.actionCard}>
          <div className={styles.actionCardMain}>
            <strong>{item.displayName}</strong>
            <span
              className={item.succeeded ? styles.success : styles.error}
              role={item.succeeded ? undefined : "status"}
            >{item.succeeded
              ? item.description ? pluginText(item.description, locale) : t("成功")
              : item.error ?? t("失败")}</span>
          </div>
        </Card>)}
      </div>
    </div>
  </Modal>;
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


function formatAutomationLastRun(automation: PluginAutomationDescriptor, locale: string) {
  if (automation.lastRunAtMs === null) return t("尚未自动执行");
  const time = new Date(automation.lastRunAtMs).toLocaleString(locale);
  if (automation.lastRunSucceeded === true) return t("上次自动执行成功：{time}", { time });
  if (automation.lastRunFailed === true) return t("上次自动执行失败：{time}", { time });
  return t("上次自动执行：{time}", { time });
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
