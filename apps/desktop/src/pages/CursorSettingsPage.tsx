import { useCallback, useEffect, useRef, useState } from "react";
import { api, type DiscoveredModel, type Model, type ModelInput } from "../api";
import { CursorCaGate, CursorCaProvider, CursorModelGate, CursorModelProvider } from "../components/cursor/CursorGates";
import { CursorModelCards, cursorModelGroups, type CursorModelGrouping } from "../components/cursor/CursorModelCards";
import { CursorModelEditor, emptyCursorModelDraft, type CursorModelDraft } from "../components/cursor/CursorModelEditor";
import { CursorModelTestResult, type CursorModelTestState } from "../components/cursor/CursorModelTestResult";
import { SubscriptionAuthTab, isSubscriptionModel } from "../components/cursor/SubscriptionAuthTab";
import styles from "../components/cursor/CursorSettings.module.scss";
import { PageContent } from "../components/layout/PageContent";
import { LegacyModelImport } from "../components/models/LegacyModelImport";
import { ConfirmDialog } from "../components/ui/ConfirmDialog";
import controls from "../components/ui/Controls.module.scss";
import { Icon } from "../components/ui/Icon";
import { Modal } from "../components/ui/Modal";
import { Tabs } from "../components/ui/Tabs";
import { TooltipTrigger } from "../components/ui/TooltipTrigger";
import { addIcon } from "../components/ui/icons";
import { useMessage } from "../components/ui/message";
import { PageActions } from "../layouts/PageActions";
import { appStore, useAppStore } from "../store/appStore";

export function CursorSettingsPage() {
  const { models, cursorHarness, cursorBusy } = useAppStore();
  const message = useMessage();
  const [draft, setDraft] = useState<CursorModelDraft | null>(null);
  const [editing, setEditing] = useState<Model | null>(null);
  const [discoveredModels, setDiscoveredModels] = useState<DiscoveredModel[]>([]);
  const [discovering, setDiscovering] = useState(false);
  const [caCommand, setCaCommand] = useState<string | null>(null);
  const [waitingForCaRefresh, setWaitingForCaRefresh] = useState(false);
  const [deleting, setDeleting] = useState<Model | null>(null);
  const [testingModelHashes, setTestingModelHashes] = useState<Set<string>>(() => new Set());
  const [modelTestResults, setModelTestResults] = useState<Map<string, CursorModelTestState>>(() => new Map());
  const [savingAndTesting, setSavingAndTesting] = useState(false);
  const [batchTesting, setBatchTesting] = useState(false);
  const [grouping, setGrouping] = useState<CursorModelGrouping>("flat");
  const activeModelTests = useRef(new Map<string, { testId: string; controller: AbortController; cancelling: boolean }>());
  const caReady = cursorHarness?.ca === "ready";
  const providerGroups = cursorModelGroups(models, "provider");
  const typeGroups = cursorModelGroups(models, "type");
  const canGroupByProvider = providerGroups.length > 1;
  const canGroupByType = typeGroups.length > 1;

  useEffect(() => {
    if ((grouping === "provider" && !canGroupByProvider) || (grouping === "type" && !canGroupByType)) {
      setGrouping("flat");
    }
  }, [canGroupByProvider, canGroupByType, grouping]);

  useEffect(() => {
    if (caCommand) void api.copyCursorText(caCommand);
  }, [caCommand]);

  const initializeCa = async () => {
    const status = await appStore.initializeCursorCa();
    if (status?.ca === "untrusted" && status.ca_install_command) setCaCommand(status.ca_install_command);
  };
  const openNew = () => {
    const next = emptyCursorModelDraft();
    next.model.sort_order = models.length + 1;
    setEditing(null);
    setDiscoveredModels([]);
    setDraft(next);
  };
  const openEdit = (model: Model) => {
    setEditing(model);
    setDiscoveredModels([{ id: model.model_id, context_window_tokens: model.context_window_tokens }]);
    setDraft({
      model: modelInput(model),
      openAIExtraParamsText: JSON.stringify(model.openai_extra_params, null, 2),
      customHeadersText: JSON.stringify(model.custom_headers, null, 2),
      anthropicExtraParamsText: JSON.stringify(model.anthropic_extra_params, null, 2),
    });
  };
  const discover = async () => {
    if (!draft) return;
    setDiscovering(true);
    try {
      const custom_headers = parseHeaders(draft.customHeadersText);
      const result = await api.discoverModels({
        type: draft.model.type,
        base_url: draft.model.base_url.trim(),
        api_key: draft.model.api_key.trim(),
        custom_headers_enabled: draft.model.custom_headers_enabled,
        custom_headers,
      });
      setDiscoveredModels(result.models);
    } catch (cause) {
      message(errorText(cause));
    } finally {
      setDiscovering(false);
    }
  };
  const persist = async (): Promise<Model | null> => {
    if (!draft) return null;
    const input = draftInput(draft);
    if (editing) return appStore.updateCursorModel(editing.model_hash, input);
    return (await appStore.createModels([input]))?.[0] ?? null;
  };
  const save = async () => {
    try {
      if (await persist()) {
        setDraft(null);
        setEditing(null);
      }
    } catch (cause) {
      message(errorText(cause));
    }
  };
  const cancelModelTest = async (modelHash: string) => {
    const active = activeModelTests.current.get(modelHash);
    if (!active || active.cancelling) return;
    active.cancelling = true;
    try {
      await api.cancelModelTest(modelHash, active.testId);
      active.controller.abort();
    } catch (cause) {
      active.cancelling = false;
      message(t("取消测试失败：{error}", { error: errorText(cause) }), { duration: 5000 });
    }
  };
  const cancelAllModelTests = async () => {
    await Promise.all([...activeModelTests.current.keys()].map((modelHash) => cancelModelTest(modelHash)));
  };
  const testModel = async (model: Model, notify = true): Promise<"success" | "failure" | "cancelled"> => {
    if (activeModelTests.current.has(model.model_hash)) {
      await cancelModelTest(model.model_hash);
      return "cancelled";
    }
    const active = { testId: crypto.randomUUID(), controller: new AbortController(), cancelling: false };
    activeModelTests.current.set(model.model_hash, active);
    setTestingModelHashes((current) => new Set(current).add(model.model_hash));
    try {
      const result = await api.testModel(model.model_hash, active.testId, active.controller.signal);
      setModelTestResults((current) => new Map(current).set(model.model_hash, { status: "success", result }));
      if (notify) message(t("模型 {model} 连通性测试成功（{duration} ms）", { model: model.display_name, duration: result.duration_ms }));
      return "success";
    } catch (cause) {
      if (active.cancelling || active.controller.signal.aborted) {
        setModelTestResults((current) => new Map(current).set(model.model_hash, { status: "cancelled" }));
        return "cancelled";
      }
      const error = errorText(cause);
      setModelTestResults((current) => new Map(current).set(model.model_hash, { status: "error", error }));
      if (notify) message(t("连通性测试失败：{error}", { error }), { duration: 5000 });
      return "failure";
    } finally {
      if (activeModelTests.current.get(model.model_hash) === active) activeModelTests.current.delete(model.model_hash);
      setTestingModelHashes((current) => {
        const next = new Set(current);
        next.delete(model.model_hash);
        return next;
      });
    }
  };
  const saveAndTest = async () => {
    setSavingAndTesting(true);
    let saved: Model | null = null;
    try {
      saved = await persist();
    } catch (cause) {
      message(errorText(cause));
    } finally {
      setSavingAndTesting(false);
    }
    if (!saved) return;
    setEditing(saved);
    await testModel(saved);
    await appStore.refresh();
  };
  const testAllModels = async () => {
    if (!models.length || batchTesting) return;
    setBatchTesting(true);
    try {
      const results = await Promise.all(models.map((model) => testModel(model, false)));
      const successful = results.filter((result) => result === "success").length;
      const failed = results.filter((result) => result === "failure").length;
      const cancelled = results.filter((result) => result === "cancelled").length;
      message(cancelled > 0
        ? t("连通性测试已取消：成功 {successful}，失败 {failed}", { successful, failed })
        : failed === 0
          ? t("全部 {count} 个模型连通性测试成功", { count: models.length })
          : t("连通性测试完成：成功 {successful}，失败 {failed}", { successful, failed }),
      { duration: failed === 0 && cancelled === 0 ? 2400 : 5000 });
    } finally {
      setBatchTesting(false);
    }
  };
  const duplicateModel = async (model: Model) => {
    const names = new Set(models.map((item) => item.display_name));
    const baseName = t("{name} 副本", { name: model.display_name });
    let displayName = baseName;
    let suffix = 2;
    while (names.has(displayName)) {
      displayName = `${baseName} ${suffix}`;
      suffix += 1;
    }
    const created = await appStore.createModels([{
      ...modelInput(model),
      sort_order: models.length + 1,
      display_name: displayName,
    }]);
    if (created) message(t("模型已复制"));
  };
  const reorderModels = useCallback(async (modelHashes: string[]) => {
    if (!await appStore.reorderCursorModels(modelHashes)) {
      message(appStore.getSnapshot().error || t("排序失败"));
    }
  }, [message]);

  const customModels = models.filter((m) => !isSubscriptionModel(m));
  const subscriptionModels = models.filter((m) => isSubscriptionModel(m));

  const customList = <CursorModelCards
    models={customModels}
    grouping={grouping}
    disabled={cursorBusy}
    testingModelHashes={testingModelHashes}
    testResults={modelTestResults}
    onTest={(model) => void testModel(model)}
    onEdit={openEdit}
    onDuplicate={(model) => void duplicateModel(model)}
    onDelete={setDeleting}
    onReorder={reorderModels}
  />;

  const subscriptionList = subscriptionModels.length > 0 ? (
    <CursorModelCards
      models={subscriptionModels}
      grouping={grouping}
      disabled={cursorBusy}
      testingModelHashes={testingModelHashes}
      testResults={modelTestResults}
      onTest={(model) => void testModel(model)}
      onEdit={openEdit}
      onDuplicate={(model) => void duplicateModel(model)}
      onDelete={setDeleting}
      onReorder={reorderModels}
    />
  ) : null;

  const refreshCa = async () => {
    await appStore.refresh();
    if (appStore.getSnapshot().cursorHarness?.ca !== "ready") setWaitingForCaRefresh(false);
  };
  const openCaTerminal = () => {
    if (caCommand) void api.openCursorCaInstallTerminal(caCommand).catch((cause) => message(errorText(cause)));
    setCaCommand(null);
    setWaitingForCaRefresh(true);
  };

  const modelsContent = <CursorCaProvider><CursorCaGate busy={cursorBusy} waitingForRefresh={waitingForCaRefresh} onInitialize={() => void initializeCa()} onRefresh={() => void refreshCa()}>
    <div className={styles.page}>
      <LegacyModelImport>{({ busy: importingLegacyModels, previewing, open }) =>
        <CursorModelProvider><CursorModelGate busy={cursorBusy || importingLegacyModels} previewingImport={previewing} onAdd={openNew} onImport={open}>{customList}</CursorModelGate></CursorModelProvider>
      }</LegacyModelImport>
    </div>
  </CursorCaGate></CursorCaProvider>;

  const subscriptionsContent = <SubscriptionAuthTab onSwitchToModels={() => void appStore.refresh()}>
    {subscriptionList}
  </SubscriptionAuthTab>;

  const content = <Tabs
    defaultValue="models"
    items={[
      {
        value: "models",
        label: t("上游模型"),
        content: modelsContent,
      },
      {
        value: "subscriptions",
        label: t("订阅与认证"),
        content: subscriptionsContent,
      },
    ]}
  />;

  const editorTestState = editing ? modelTestResults.get(editing.model_hash) : undefined;
  const editorTesting = Boolean(editing && testingModelHashes.has(editing.model_hash));
  const activeGroups = grouping === "provider" ? providerGroups : typeGroups;
  const estimatedModelHeight = grouping === "flat"
    ? Math.max(380, Math.ceil(models.length / 3) * 196)
    : Math.max(380, activeGroups.reduce((height, group) => height + Math.ceil(group.models.length / 3) * 196 + 34, 0) + Math.max(0, activeGroups.length - 1) * 20);

  return <>
    {models.length > 0 && <PageActions position="left">
      <div className={styles.groupActions} role="group" aria-label={t("操作")}>
        <button type="button" aria-pressed={grouping === "flat"} onClick={() => setGrouping("flat")}>{t("默认平铺")}</button>
        {canGroupByProvider && <button type="button" aria-pressed={grouping === "provider"} onClick={() => setGrouping("provider")}>{t("按供应商")}</button>}
        {canGroupByType && <button type="button" aria-pressed={grouping === "type"} onClick={() => setGrouping("type")}>{t("按类型")}</button>}
        <button type="button" disabled={cursorBusy || (!batchTesting && testingModelHashes.size > 0)} onClick={() => void (batchTesting ? cancelAllModelTests() : testAllModels())}>{batchTesting ? t("取消全部测试") : t("一键测试")}</button>
      </div>
    </PageActions>}
    <PageActions><TooltipTrigger label={caReady ? t("添加模型") : t("请先初始化 CA")}><button className={controls.iconButton} aria-label={t("添加模型")} disabled={!caReady || cursorBusy} onClick={openNew}><Icon icon={addIcon} size="1.1em" /></button></TooltipTrigger></PageActions>
    <PageContent title={t("Cursor 配置")} sections={[{ key: "cursor-settings", estimatedHeight: estimatedModelHeight, content }]} />
    <Modal fullHeight open={draft !== null} title={editing ? t("编辑模型") : t("添加模型")} banner={draft && (editorTesting || editorTestState) ? <CursorModelTestResult state={editorTestState} testing={editorTesting} /> : undefined} busy={cursorBusy || savingAndTesting} onClose={() => { if (editing && editorTesting) void cancelModelTest(editing.model_hash); setDraft(null); setEditing(null); }} onSubmit={() => void save()} secondaryAction={<button type="button" className={controls.secondary} disabled={cursorBusy || savingAndTesting} onClick={() => void (editorTesting && editing ? cancelModelTest(editing.model_hash) : saveAndTest())}>{savingAndTesting ? t("处理中…") : editorTesting ? t("取消测试") : t("保存并测试")}</button>}>
      {draft && <>
        <CursorModelEditor
          draft={draft}
          isEditing={Boolean(editing)}
          modelOptions={discoveredModels.map((model) => model.id)}
          contextByModelId={Object.fromEntries(
            discoveredModels
              .filter((model) => model.context_window_tokens != null)
              .map((model) => [model.id, model.context_window_tokens as number]),
          )}
          discovering={discovering}
          onChange={setDraft}
          onDiscover={() => void discover()}
          onGrokAuthorized={() => {
            setDraft(null);
            setEditing(null);
          }}
        />
      </>}
    </Modal>
    <ConfirmDialog open={caCommand !== null} title={t("安装本地 CA")} cancelLabel={t("关闭")} confirmLabel={t("打开终端")} onCancel={() => setCaCommand(null)} onConfirm={openCaTerminal}>
      <div className={styles.editor}><strong>{t("需要授权安装证书")}</strong><span>{t("安装命令已自动复制。点击“打开终端”，将命令粘贴到终端中执行，并按提示输入密码。")}</span><pre className={styles.command}>{caCommand}</pre></div>
    </ConfirmDialog>
    <ConfirmDialog open={deleting !== null} title={t("删除模型")} cancelLabel={t("取消")} confirmLabel={t("删除")} onCancel={() => setDeleting(null)} onConfirm={() => { if (deleting) void appStore.deleteModel(deleting.model_hash); setDeleting(null); }}><p>{t("确定删除这个模型吗？")}</p></ConfirmDialog>
  </>;
}

function modelInput(model: Model): ModelInput {
  const { model_hash: _hash, created_at_ms: _created, updated_at_ms: _updated, ...input } = model;
  return input;
}

function draftInput(draft: CursorModelDraft): ModelInput {
  const model = {
    ...draft.model,
    display_name: draft.model.display_name.trim(),
    base_url: draft.model.base_url.trim(),
    api_key: draft.model.api_key.trim(),
    tooltip_data: draft.model.tooltip_data.trim(),
    model_id: draft.model.model_id.trim(),
    openai_extra_params: parseObject(draft.openAIExtraParamsText, t("OpenAI 额外参数")),
    custom_headers: parseHeaders(draft.customHeadersText),
    anthropic_extra_params: parseObject(draft.anthropicExtraParamsText, t("Anthropic 额外参数")),
  };
  if (!model.display_name || !model.base_url || !model.api_key || !model.tooltip_data || !model.model_id) throw new Error(t("服务器地址或完整请求 URL、API Key、模型名称、显示名称和备注不能为空"));
  for (const [label, value] of [[t("上下文窗口 Token"), model.context_window_tokens], [t("最大输出 Token"), model.type === "openai" ? model.max_completion_tokens : model.anthropic_max_tokens], [t("思考预算 Token"), model.thinking_budget_tokens]] as const) {
    if (value !== null && (!Number.isSafeInteger(value) || value <= 0)) throw new Error(t("{label} 必须是大于 0 的整数", { label }));
  }
  return model;
}

function parseHeaders(text: string): Record<string, string> {
  const parsed = parseObject(text, t("自定义 Headers"));
  if (Object.values(parsed).some((value) => typeof value !== "string")) throw new Error(t("自定义 Headers 的值必须都是字符串"));
  return parsed as Record<string, string>;
}

function parseObject(text: string, label: string): Record<string, unknown> {
  let parsed: unknown;
  try { parsed = JSON.parse(text || "{}"); } catch { throw new Error(t("{label} 必须是有效 JSON", { label })); }
  if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") throw new Error(t("{label} 必须是 JSON 对象", { label }));
  return parsed as Record<string, unknown>;
}

function errorText(cause: unknown) {
  return cause instanceof Error ? cause.message : String(cause);
}
