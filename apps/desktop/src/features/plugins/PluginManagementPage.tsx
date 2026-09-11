import { useEffect, useRef, useState } from "react";
import { api, pluginText, type PluginDescriptor, type PluginImportFile, type PluginRuntimePhase, type PluginRuntimeStatus } from "../../shared/api";
import { useI18n } from "../../i18n/store";
import { PageContent } from "../../shell/layout/PageContent";
import { appStore, useAppStore } from "../../shared/store/appStore";
import { ActionMenu, type ActionMenuItem } from "../../shared/ui/ActionMenu";
import { Button } from "../../shared/ui/Button";
import { Card } from "../../shared/ui/Card";
import { Modal } from "../../shared/ui/Modal";
import { useMessage } from "../../shared/ui/message";
import { TruncatedButton } from "../../shared/ui/TruncatedButton";
import { PluginAddPanel, PluginSettingsPanel } from "./PluginResourcePanels";
import styles from "./PluginManagementPage.module.scss";

export function PluginManagementPage() {
  const { pluginRuntime, plugins } = useAppStore();
  const [progressOpen, setProgressOpen] = useState(false);
  const [starting, setStarting] = useState(false);
  const [selected, setSelected] = useState<{ pluginId: string; mode: "add" | "settings" } | null>(null);
  const cancelRequested = useRef(false);
  const selectedPlugin = selected ? plugins.find((plugin) => plugin.id === selected.pluginId) ?? null : null;

  useEffect(() => {
    if (!pluginRuntime) void appStore.refreshPluginRuntime();
  }, [pluginRuntime]);

  useEffect(() => {
    if (pluginRuntime?.state !== "initializing") return;
    if (!cancelRequested.current) setProgressOpen(true);
    const timer = window.setInterval(() => void appStore.refreshPluginRuntime(), 300);
    return () => window.clearInterval(timer);
  }, [pluginRuntime?.state]);

  const initialize = async () => {
    if (starting) return;
    cancelRequested.current = false;
    setStarting(true);
    setProgressOpen(true);
    const status = await appStore.initializePluginRuntime();
    setStarting(false);
    if (!status) {
      setProgressOpen(false);
    } else if (cancelRequested.current && status.state === "initializing") {
      void appStore.cancelPluginRuntimeInitialization();
    }
  };

  const closeProgress = () => {
    setProgressOpen(false);
    cancelRequested.current = true;
    if (pluginRuntime?.state === "initializing") {
      void appStore.cancelPluginRuntimeInitialization();
    }
  };

  const content = pluginRuntime?.state === "ready"
    ? <PluginCards plugins={plugins} onOpen={(pluginId, mode) => setSelected({ pluginId, mode })} />
    : <RuntimeGate status={pluginRuntime} starting={starting} onInitialize={() => void initialize()} />;
  const estimatedHeight = plugins.length > 0
    ? Math.max(320, Math.ceil(plugins.length / 3) * 180)
    : 320;

  return <>
    <PageContent
      title={t("插件配置")}
      sections={[{ key: "installed-plugins", estimatedHeight, content }]}
    />
    <RuntimeProgressModal
      open={progressOpen}
      status={pluginRuntime}
      starting={starting}
      onClose={closeProgress}
    />
    <Modal
      fullHeight
      open={selectedPlugin !== null}
      title={selected?.mode === "settings"
        ? t("{name} 账号管理", { name: selectedPlugin?.name ?? "" })
        : t("添加 {name} 账号", { name: selectedPlugin?.name ?? "" })}
      onClose={() => setSelected(null)}
      onSubmit={() => setSelected(null)}
      submitLabel={t("确定")}
    >
      {selected?.mode === "add" && selectedPlugin && <PluginAddPanel plugin={selectedPlugin} onConfigured={() => setSelected(null)} />}
      {selected?.mode === "settings" && selectedPlugin && <PluginSettingsPanel plugin={selectedPlugin} onResourcesEmpty={() => setSelected(null)} />}
    </Modal>
  </>;
}

function RuntimeGate({ status, starting, onInitialize }: { status: PluginRuntimeStatus | null; starting: boolean; onInitialize: () => void }) {
  const checking = status === null;
  const initializing = starting || status?.state === "initializing";
  const failed = status?.state === "failed";
  const unsupported = status?.state === "unsupported";
  const title = checking
    ? t("正在检查插件运行时")
    : failed
      ? t("插件运行时初始化失败")
      : unsupported
        ? t("当前系统不支持插件运行时")
        : t("需要先初始化插件运行时");
  const description = failed
    ? t("请重试初始化")
    : unsupported
      ? status.error ?? t("当前操作系统或 CPU 架构暂不受支持")
      : t("初始化将下载并安装插件运行时。");

  return <div className={styles.gate}>
    <strong>{title}</strong>
    <span>{description}</span>
    {!unsupported && <Button variant="primary" disabled={checking || initializing} onClick={onInitialize}>
      {checking ? t("检查中…") : initializing ? t("初始化中…") : failed ? t("重新初始化插件") : t("初始化插件")}
    </Button>}
  </div>;
}

function PluginCards({ plugins, onOpen }: {
  plugins: PluginDescriptor[];
  onOpen: (pluginId: string, mode: "add" | "settings") => void;
}) {
  if (plugins.length === 0) {
    return <div className={styles.empty}>
      <strong>{t("还没有安装插件")}</strong>
      <span>{t("安装插件后会显示在这里。")}</span>
    </div>;
  }
  return <div className={styles.pluginGrid}>
    {plugins.map((plugin) => <PluginCard key={plugin.id} plugin={plugin} onOpen={onOpen} />)}
  </div>;
}

function PluginCard({ plugin, onOpen }: {
  plugin: PluginDescriptor;
  onOpen: (pluginId: string, mode: "add" | "settings") => void;
}) {
  const { locale } = useI18n();
  const { ports } = useAppStore();
  const message = useMessage();
  const importInput = useRef<HTMLInputElement>(null);
  const [importing, setImporting] = useState(false);
  const configured = plugin.providers.some((provider) => provider.configured);
  const accountCount = plugin.resources.reduce((count, resource) => count + resource.resources.length, 0);
  const modelCount = plugin.providers.reduce((count, provider) => count + provider.models.length, 0);
  const subtitle = plugin.providers.map((provider) => pluginText(provider.displayName, locale)).join(" · ") || plugin.id;
  const importResource = plugin.resources.find((resource) => resource.import);
  const exportResource = plugin.resources.find((resource) => resource.resources.length > 0);

  const importFiles = async (files: FileList | null) => {
    if (!files?.length || !importResource) return;
    setImporting(true);
    try {
      const entries: PluginImportFile[] = await Promise.all(
        [...files].map(async (file) => ({ name: file.name, content: await file.text() })),
      );
      const result = await api.importPluginResources(plugin.id, importResource.type, entries);
      await appStore.refreshPlugins();
      const summary = t("导入完成：新增 {added}，更新 {updated}", { added: result.added, updated: result.updated });
      if (result.modelSyncError) {
        message(t("账号已保存，但同步模型失败：{error}", { error: result.modelSyncError }), { duration: 5000 });
      } else if (result.warnings.length > 0) {
        message(`${summary} · ${result.warnings.join("; ")}`, { duration: 5000 });
      } else {
        message(summary);
      }
    } catch (cause) {
      message(cause instanceof Error ? cause.message : String(cause), { duration: 5000 });
    } finally {
      setImporting(false);
      if (importInput.current) importInput.current.value = "";
    }
  };

  const moreItems: ActionMenuItem[] = [
    ...(importResource
      ? [
          {
            id: "import",
            label: importing ? t("正在导入…") : t("批量导入"),
            disabled: importing,
            onSelect: () => importInput.current?.click(),
          },
        ]
      : []),
    ...(exportResource
      ? [
          {
            id: "export",
            label: t("批量导出"),
            onSelect: () =>
              void api.openExternalUrl(
                api.pluginResourceExportUrl(
                  ports.service_port,
                  plugin.id,
                  exportResource.type,
                ),
              ),
          },
        ]
      : []),
    ...(plugin.version
      ? [{ id: "version", type: "text" as const, label: `v${plugin.version}` }]
      : []),
  ];

  return (
    <Card className={styles.pluginCard}>
      <div className={styles.pluginCardTop}>
        <img className={styles.pluginIcon} src={plugin.icon} />
        <div className={styles.pluginIdentity}>
          <span className={styles.pluginName}>{plugin.name}</span>
          <span className={styles.pluginId}>{subtitle}</span>
        </div>
        <span
          className={`${styles.stateBadge} ${configured ? styles.stateReady : ""}`}
        >
          {configured ? t("已配置") : t("未配置")}
        </span>
      </div>
      <div className={styles.pluginMeta}>
        <span>
          {t("{accounts} 个账号 · {models} 个模型", {
            accounts: accountCount,
            models: modelCount,
          })}
        </span>
        {plugin.author && (
          <span className={styles.pluginAuthor}>{plugin.author}</span>
        )}
      </div>
      <div className={styles.cardActions}>
        <TruncatedButton
          size="small"
          variant="primary"
          label={t("添加账号")}
          onClick={() => onOpen(plugin.id, "add")}
        />
        {configured && (
          <TruncatedButton
            size="small"
            label={t("账号管理")}
            onClick={() => onOpen(plugin.id, "settings")}
          />
        )}
        {moreItems.length > 0 && (
          <span className={styles.moreAction}>
            <ActionMenu label={t("更多")} items={moreItems} />
          </span>
        )}
        {importResource && (
          <input
            ref={importInput}
            type="file"
            hidden
            accept={importResource.import?.accept.join(",")}
            multiple={importResource.import?.multiple ?? false}
            onChange={(event) => void importFiles(event.target.files)}
          />
        )}
      </div>
    </Card>
  );
}

function RuntimeProgressModal({ open, status, starting, onClose }: { open: boolean; status: PluginRuntimeStatus | null; starting: boolean; onClose: () => void }) {
  const initializing = starting || status?.state === "initializing";
  const downloaded = status?.downloaded_bytes ?? 0;
  const total = status?.total_bytes ?? null;
  const percent = total && total > 0 ? Math.min(100, Math.round((downloaded / total) * 100)) : null;
  const stage = status?.state === "ready"
    ? t("插件运行时初始化完成")
    : status?.state === "failed"
      ? t("插件运行时初始化失败")
      : phaseText(status?.phase ?? null);

  return <Modal
    open={open}
    title={t("初始化插件运行时")}
    closeLabel={status?.state === "ready" ? t("完成") : initializing ? t("取消") : t("关闭")}
    onClose={onClose}
  >
    <div className={styles.progressContent} aria-live="polite">
      <strong>{stage}</strong>
      {status?.phase === "downloading" && <>
        <div
          className={styles.progressBar}
          role="progressbar"
          aria-label={t("下载进度")}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={percent ?? undefined}
        >
          <div
            className={styles.progressFill}
            style={{ width: `${percent ?? 100}%` }}
          />
        </div>
        <span>
          {total ? t("已下载 {downloaded} / {total}", { downloaded: formatBytes(downloaded), total: formatBytes(total) }) : t("已下载 {downloaded}", { downloaded: formatBytes(downloaded) })}
        </span>
      </>}
      {status?.state === "failed" && <span className={styles.error}>{t("请重试初始化")}</span>}
      {status?.state === "ready" && <span>{t("插件运行时 {version} 已安装，可以开始使用插件。", { version: status.version })}</span>}
    </div>
  </Modal>;
}

function phaseText(phase: PluginRuntimePhase | null) {
  switch (phase) {
    case "checking": return t("正在检查插件运行时");
    case "downloading": return t("正在下载插件运行时");
    case "verifying": return t("正在验证插件运行时下载文件");
    case "installing": return t("正在安装插件运行时");
    case "validating": return t("正在验证插件运行时");
    default: return t("正在准备插件运行时");
  }
}

function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value < 10 ? value.toFixed(1) : value.toFixed(0)} ${units[unit]}`;
}
