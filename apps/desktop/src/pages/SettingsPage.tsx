import { useEffect, useState } from "react";
import { api, type CursorAccountStatus, type ProxySettings, type ProxySettingsInput, type StatisticsStorage, type StatisticsStorageScope, type TabSettings } from "../api";
import { PageContent } from "../components/layout/PageContent";
import { LegacyModelImport } from "../components/models/LegacyModelImport";
import { AppLifecycleSettingsCard } from "../components/settings/AppLifecycleSettingsCard";
import { ProxySettingsCard } from "../components/settings/ProxySettingsCard";
import { CursorAccountCard } from "../components/settings/CursorAccountCard";
import { TabSettingsCard } from "../components/settings/TabSettingsCard";
import { Button } from "../components/ui/Button";
import { Checkbox } from "../components/ui/Checkbox";
import { ConfirmDialog } from "../components/ui/ConfirmDialog";
import { FormField, TextInput } from "../components/ui/FormControls";
import { Select } from "../components/ui/Select";
import { TitledCard } from "../components/ui/TitledCard";
import { setLocalePreference, useI18n, type LocalePreference } from "../i18n/store";
import { useMessage } from "../components/ui/message";
import { appStore, useAppStore } from "../store/appStore";
import { themeOptions } from "../theme/theme";
import styles from "./SettingsPage.module.scss";

export function SettingsPage() {
  const { detailed, ports, theme } = useAppStore();
  const { preference, locale } = useI18n();
  const message = useMessage();
  const [proxyPort, setProxyPort] = useState(String(ports.proxy_port));
  const [servicePort, setServicePort] = useState(String(ports.service_port));
  const [editingPorts, setEditingPorts] = useState(false);
  const [savingPorts, setSavingPorts] = useState(false);
  const [storage, setStorage] = useState<StatisticsStorage | null>(null);
  const [confirmClear, setConfirmClear] = useState(false);
  const [clearScope, setClearScope] = useState<StatisticsStorageScope>("details");
  const [clearing, setClearing] = useState(false);
  const [outboundProxy, setOutboundProxy] = useState<ProxySettings | null>(null);
  const [proxyDraft, setProxyDraft] = useState<ProxySettingsInput>({ mode: "system", address: "", auth_enabled: false, username: "", password: "" });
  const [editingProxy, setEditingProxy] = useState(false);
  const [savingProxy, setSavingProxy] = useState(false);
  const [tabSettings, setTabSettings] = useState<TabSettings | null>(null);
  const [tabDraft, setTabDraft] = useState<TabSettings>({ mode: "public", address: "" });
  const [editingTab, setEditingTab] = useState(false);
  const [savingTab, setSavingTab] = useState(false);
  const [cursorAccount, setCursorAccount] = useState<CursorAccountStatus | null>(null);
  const [restoringAccount, setRestoringAccount] = useState(false);
  useEffect(() => {
    void Promise.all([api.statisticsStorage(), api.proxySettings(), api.tabSettings(), api.cursorAccount()]).then(([nextStorage, nextProxy, nextTab, nextAccount]) => {
      setStorage(nextStorage);
      setOutboundProxy(nextProxy);
      setProxyDraft({ mode: nextProxy.mode, address: nextProxy.address, auth_enabled: nextProxy.auth_enabled, username: nextProxy.username, password: "" });
      setTabSettings(nextTab);
      setTabDraft(nextTab);
      setCursorAccount(nextAccount);
    }).catch((cause) => message(cause instanceof Error ? cause.message : String(cause)));
  }, [message]);
  useEffect(() => {
    setProxyPort(String(ports.proxy_port));
    setServicePort(String(ports.service_port));
  }, [ports.proxy_port, ports.service_port]);

  const parsePort = (value: string, label: string) => {
    const port = Number(value);
    if (!Number.isInteger(port) || port < 0 || port > 65_535) {
      throw new Error(`${label}${t("必须是 0–65535 之间的整数")}`);
    }
    return port;
  };
  const savePorts = async () => {
    try {
      const next = {
        proxy_port: parsePort(proxyPort, t("代理端口")),
        service_port: parsePort(servicePort, t("服务端口")),
      };
      setSavingPorts(true);
      if (await appStore.updatePorts(next)) {
        setEditingPorts(false);
        message(t("端口设置已保存，重启软件后生效"), { duration: 4_000 });
      }
    } catch (cause) {
      message(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSavingPorts(false);
    }
  };
  const editPorts = () => {
    setProxyPort(String(ports.proxy_port));
    setServicePort(String(ports.service_port));
    setEditingPorts(true);
  };
  const cancelPortEdit = () => {
    setProxyPort(String(ports.proxy_port));
    setServicePort(String(ports.service_port));
    setEditingPorts(false);
  };
  const clearStorage = async () => {
    try {
      setClearing(true);
      setStorage(await api.clearStatisticsStorage(clearScope));
      setConfirmClear(false);
      await appStore.refresh();
      message(clearScope === "all" ? t("全部统计数据已清理") : t("详细记录已清理"));
    } catch (cause) {
      message(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setClearing(false);
    }
  };
  const editProxy = () => {
    if (!outboundProxy) return;
    setProxyDraft({ mode: outboundProxy.mode, address: outboundProxy.address, auth_enabled: outboundProxy.auth_enabled, username: outboundProxy.username, password: "" });
    setEditingProxy(true);
  };
  const cancelProxyEdit = () => {
    if (outboundProxy) {
      setProxyDraft({ mode: outboundProxy.mode, address: outboundProxy.address, auth_enabled: outboundProxy.auth_enabled, username: outboundProxy.username, password: "" });
    }
    setEditingProxy(false);
  };
  const saveProxy = async () => {
    try {
      setSavingProxy(true);
      const saved = await api.setProxySettings({ ...proxyDraft, password: proxyDraft.password || undefined });
      setOutboundProxy(saved);
      setProxyDraft({ mode: saved.mode, address: saved.address, auth_enabled: saved.auth_enabled, username: saved.username, password: "" });
      setEditingProxy(false);
      message(t("代理设置已保存"));
    } catch (cause) {
      message(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSavingProxy(false);
    }
  };
  const editTab = () => {
    if (!tabSettings) return;
    setTabDraft(tabSettings);
    setEditingTab(true);
  };
  const cancelTabEdit = () => {
    if (tabSettings) setTabDraft(tabSettings);
    setEditingTab(false);
  };
  const saveTab = async () => {
    try {
      if (tabDraft.mode === "custom" && !tabDraft.address.trim()) throw new Error(t("TAB 服务地址不能为空"));
      setSavingTab(true);
      const saved = await api.setTabSettings({ ...tabDraft, address: tabDraft.address.trim() });
      setTabSettings(saved);
      setTabDraft(saved);
      setEditingTab(false);
      const account = await api.cursorAccount().catch(() => null);
      if (account) setCursorAccount(account);
      message(saved.mode === "direct"
        ? t("TAB 已直连。已切到 Cursor 原账号时，请完全退出并重新打开 Cursor。")
        : t("TAB 设置已保存"));
    } catch (cause) {
      message(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSavingTab(false);
    }
  };
  const restoreCursorAccount = async () => {
    try {
      setRestoringAccount(true);
      setCursorAccount(await api.restoreCursorAccount());
      message(t("已恢复 Cursor 原账号。请完全退出并重新打开 Cursor。"), { duration: 4_000 });
    } catch (cause) {
      message(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setRestoringAccount(false);
    }
  };
  const formatBytes = (bytes: number) => {
    if (bytes < 1024) return `${bytes} B`;
    const units = ["KB", "MB", "GB", "TB"];
    let value = bytes / 1024;
    let unit = 0;
    while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit += 1; }
    return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
  };
  const clearTitle = clearScope === "all" ? t("确定要清理全部统计数据吗？") : t("确定要清理详细记录吗？");
  const clearDescription = clearScope === "all"
    ? t("所有调用汇总、详细内容和追踪记录都会被删除。模型配置、CA 和应用设置不会受到影响，此操作无法撤销。")
    : t("仅删除请求、响应和追踪附件等详细内容，保留调用汇总、统计指标和配置。");
  const content = (
    <div className={styles.page}>
      <TitledCard title={t("调用观测")}>
        <div className={styles.settingRow}>
          <div>
            <strong>{t("详细模式")}</strong>
            <small>
              {t("额外保存完整请求和流响应；默认只保存时间、状态与用量。")}
            </small>
          </div>
          <Checkbox
            label={t("详细模式")}
            checked={detailed}
            onChange={(checked) => void appStore.updateDetailed(checked)}
          />
        </div>
      </TitledCard>
      <TitledCard title={t("端口设置")} action={editingPorts ? (
        <div className={styles.cardActions}>
          <Button size="small" disabled={savingPorts} onClick={cancelPortEdit}>{t("取消")}</Button>
          <Button variant="primary" size="small" disabled={savingPorts} onClick={() => void savePorts()}>{savingPorts ? t("保存中…") : t("保存")}</Button>
        </div>
      ) : (
        <button type="button" className={styles.textButton} onClick={editPorts}>{t("编辑")}</button>
      )}>
        <div className={styles.portSettings}>
          <div className={styles.portFields}>
            {editingPorts ? <><FormField
              label={t("代理端口")}
              hint={t("Cursor 使用的本地代理端口；填写 0 时启动时随机选择。")}
            >
              <TextInput
                type="number"
                min={0}
                max={65535}
                step={1}
                value={proxyPort}
                onChange={(event) => setProxyPort(event.target.value)}
              />
            </FormField>
            <FormField
              label={t("服务端口")}
              hint={t(
                "桌面前端连接的本地管理服务端口；填写 0 时启动时随机选择。",
              )}
            >
              <TextInput
                type="number"
                min={0}
                max={65535}
                step={1}
                value={servicePort}
                onChange={(event) => setServicePort(event.target.value)}
              />
            </FormField></> : <>
              <div className={styles.portValue}><strong>{t("代理端口")}</strong><span>{ports.proxy_port}</span></div>
              <div className={styles.portValue}><strong>{t("服务端口")}</strong><span>{ports.service_port}</span></div>
            </>}
          </div>
          <div className={styles.portFooter}>
            <small>
              {t(
                "端口被占用时会自动选择新的随机端口并保存。修改后需要重启软件才会生效。",
              )}
            </small>
          </div>
        </div>
      </TitledCard>
      <ProxySettingsCard settings={outboundProxy} draft={proxyDraft} editing={editingProxy} saving={savingProxy} onDraftChange={setProxyDraft} onEdit={editProxy} onCancel={cancelProxyEdit} onSave={() => void saveProxy()} />
      <TabSettingsCard settings={tabSettings} draft={tabDraft} editing={editingTab} saving={savingTab} onDraftChange={setTabDraft} onEdit={editTab} onCancel={cancelTabEdit} onSave={() => void saveTab()} />
      <CursorAccountCard account={cursorAccount} restoring={restoringAccount} onRestore={() => void restoreCursorAccount()} />
      <AppLifecycleSettingsCard />
      <LegacyModelImport>{({ busy, previewing, open }) => <TitledCard title={t("导入")}>
        <div className={styles.importRow}>
          <div>
            <strong>{t("旧版配置")}</strong>
            <small>{t("从本机旧版配置读取模型；确认前会显示新增和已存在的模型。")}</small>
          </div>
          <Button size="small" disabled={busy} onClick={open}>
            {previewing ? t("读取中…") : t("查看并导入")}
          </Button>
        </div>
      </TitledCard>}</LegacyModelImport>
      <TitledCard title={t("语言")}>
        <div className={styles.settingRow}>
          <div>
            <strong>{t("界面语言")}</strong>
            <small>{t("默认跟随操作系统；不支持的系统语言使用英文。当前：{language}", { language: locale === "zh-CN" ? "简体中文" : "English" })}</small>
          </div>
          <div className={styles.languageControl}>
            <Select
              value={preference}
              ariaLabel={t("界面语言")}
              options={[
                { value: "system", label: t("跟随系统") },
                { value: "zh-CN", label: "简体中文" },
                { value: "en-US", label: "English" },
              ]}
              onChange={(value) => setLocalePreference(value as LocalePreference)}
            />
          </div>
        </div>
      </TitledCard>
      <TitledCard title={t("主题")}>
        <div className={styles.themeActions}>
          {themeOptions.map(({ id }) => (
            <button
              className={theme === id ? styles.selected : ""}
              key={id}
              onClick={() => appStore.selectTheme(id)}
            >
              {id === "default-dark" ? t("默认暗色") : t("默认亮色")}
            </button>
          ))}
        </div>
      </TitledCard>
      <TitledCard title={t("存储管理")}>
        <div className={styles.storageRow}>
          <div>
            <strong>{t("统计数据")}</strong>
            <small>{storage ? formatBytes(storage.bytes) : t("计算中…")}</small>
          </div>
          <button
            type="button"
            className={styles.textButton}
            onClick={() => { setClearScope("details"); setConfirmClear(true); }}
          >
            {t("清理存储空间")}
          </button>
        </div>
      </TitledCard>
      <ConfirmDialog
        open={confirmClear}
        title={clearTitle}
        busy={clearing}
        cancelLabel={t("取消")}
        confirmLabel={t("确认清理")}
        onCancel={() => setConfirmClear(false)}
        onConfirm={() => void clearStorage()}
      >
        <div className={styles.confirmContent}>
          <Select
            value={clearScope}
            ariaLabel={t("清理范围")}
            options={[
              { value: "details", label: t("仅清理详细记录") },
              { value: "all", label: t("清理全部统计数据") },
            ]}
            onChange={(value) => setClearScope(value as StatisticsStorageScope)}
          />
          <small>{clearDescription}</small>
        </div>
      </ConfirmDialog>
    </div>
  );
  return <PageContent title={t("设置")} sections={[{ key: "settings", estimatedHeight: 1200, content }]} />;
}
