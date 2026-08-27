import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Button } from "../ui/Button";
import { ConfirmDialog } from "../ui/ConfirmDialog";
import { Icon } from "../ui/Icon";
import { Modal } from "../ui/Modal";
import { Pagination } from "../ui/Pagination";
import { TextInput } from "../ui/FormControls";
import {
  checkIcon,
  copyIcon,
  grokIcon,
  openAiIcon,
  refreshIcon,
  trashIcon,
} from "../ui/icons";
import { GrokAuthModal } from "./GrokAuthModal";
import { CodexAuthModal } from "./CodexAuthModal";
import { useAppStore, appStore } from "../../store/appStore";
import { useMessage } from "../ui/message";
import { api, type Model, type ModelInput, type SubscriptionAccount, type SubscriptionUsage } from "../../api";
import { contextWindowForModel } from "../../utils/modelContext";
import styles from "./SubscriptionAuthTab.module.scss";

const GROK_BASE_URL = "https://api.x.ai/v1";
const CODEX_BASE_URL = "https://chatgpt.com/backend-api/codex/responses";
const CODEX_DISCOVERY_URL = "https://chatgpt.com/backend-api/codex";
const IMPORT_CHUNK_SIZE = 40;

export function isSubscriptionModel(m: { base_url?: string; tooltip_data?: string }): boolean {
  return Boolean(
    m.base_url?.includes("api.x.ai") ||
    m.tooltip_data?.includes("xAI") ||
    m.tooltip_data?.includes("Grok") ||
    m.tooltip_data?.includes("Codex") ||
    m.tooltip_data?.includes("ChatGPT") ||
    m.tooltip_data?.includes("OAuth")
  );
}

export function SubscriptionAuthTab({
  children,
  onSwitchToModels,
}: {
  children?: ReactNode;
  onSwitchToModels?: () => void;
}) {
  const { models } = useAppStore();
  const message = useMessage();
  const [grokModalOpen, setGrokModalOpen] = useState(false);
  const [checkingGrok, setCheckingGrok] = useState(false);
  const [grokAccounts, setGrokAccounts] = useState<SubscriptionAccount[]>([]);
  const [grokUsage, setGrokUsage] = useState<SubscriptionUsage | null>(null);
  const [grokUsageError, setGrokUsageError] = useState<string | null>(null);
  const [grokPoolOpen, setGrokPoolOpen] = useState(false);

  const [codexModalOpen, setCodexModalOpen] = useState(false);
  const [checkingCodex, setCheckingCodex] = useState(false);
  const [codexAccounts, setCodexAccounts] = useState<SubscriptionAccount[]>([]);
  const [codexUsage, setCodexUsage] = useState<SubscriptionUsage | null>(null);
  const [codexUsageError, setCodexUsageError] = useState<string | null>(null);
  const [codexPoolOpen, setCodexPoolOpen] = useState(false);

  const [deletingAccount, setDeletingAccount] = useState<{ provider: "grok" | "codex"; account: SubscriptionAccount } | null>(null);
  const [clearingCooldown, setClearingCooldown] = useState<"grok" | "codex" | null>(null);
  const [importing, setImporting] = useState<"grok" | "codex" | null>(null);

  const grokImportInput = useRef<HTMLInputElement>(null);
  const codexImportInput = useRef<HTMLInputElement>(null);

  const grokModels = models.filter(
    (m) => Boolean(m.base_url?.includes("api.x.ai") || m.model_id?.toLowerCase().includes("grok"))
  );
  const isGrokConnected = grokAccounts.length > 0;
  const activeGrok = grokAccounts.find((account) => account.active) ?? grokAccounts[0];

  const codexModels = models.filter(
    (m) => Boolean(
      m.tooltip_data?.includes("Codex") ||
      m.tooltip_data?.includes("ChatGPT")
    )
  );
  const isCodexConnected = codexAccounts.length > 0;
  const activeCodex = codexAccounts.find((account) => account.active) ?? codexAccounts[0];

  const loadGrokAccounts = async () => {
    setGrokAccounts(await api.grokAccounts());
  };

  const loadCodexAccounts = async () => {
    setCodexAccounts(await api.codexAccounts());
  };

  const loadGrokUsage = async () => {
    setCheckingGrok(true);
    setGrokUsageError(null);
    try {
      setGrokUsage(await api.grokUsage());
      await loadGrokAccounts();
    } catch (cause) {
      const error = cause instanceof Error ? cause.message : String(cause);
      setGrokUsage(null);
      setGrokUsageError(error);
      message(t("额度查询失败：{error}", { error }));
    } finally {
      setCheckingGrok(false);
    }
  };

  const loadCodexUsage = async () => {
    setCheckingCodex(true);
    setCodexUsageError(null);
    try {
      setCodexUsage(await api.codexUsage());
      await loadCodexAccounts();
    } catch (cause) {
      const error = cause instanceof Error ? cause.message : String(cause);
      setCodexUsage(null);
      setCodexUsageError(error);
      message(t("额度查询失败：{error}", { error }));
    } finally {
      setCheckingCodex(false);
    }
  };

  useEffect(() => {
    void loadGrokAccounts();
    void loadCodexAccounts();
  }, []);

  useEffect(() => {
    if (isGrokConnected) void loadGrokUsage();
    else {
      setGrokUsage(null);
      setGrokUsageError(null);
    }
  }, [isGrokConnected, activeGrok?.account_id]);

  useEffect(() => {
    if (isCodexConnected) void loadCodexUsage();
    else {
      setCodexUsage(null);
      setCodexUsageError(null);
    }
  }, [isCodexConnected, activeCodex?.account_id]);

  const handleGrokAuthSuccess = async (accessToken: string, refreshToken?: string | null) => {
    try {
      await api.saveGrokAccount(accessToken, refreshToken);
      if (grokModels.length === 0) {
        const synced = await syncDiscoveredModels({
          accessToken,
          discoveryUrl: GROK_BASE_URL,
          existing: grokModels,
          allModels: models,
          defaults: {
            base_url: GROK_BASE_URL,
            use_full_url: false,
            tooltip_data: "xAI Grok",
            openai_endpoint: "/v1/chat/completions",
            context_window_tokens: null,
            max_completion_tokens: 16384,
            reasoning_effort: null,
            anthropic_max_tokens: null,
          },
        });
        message(t("Grok 账号授权成功，已同步添加 {count} 个官方模型！", { count: synced.created }));
        onSwitchToModels?.();
      } else {
        message(t("Grok 账号已保存，余额为 0 时将自动切换。"));
      }
      await loadGrokAccounts();
    } catch (cause) {
      message(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const handleCodexAuthSuccess = async (accessToken: string, refreshToken?: string | null) => {
    try {
      await api.saveCodexAccount(accessToken, refreshToken);
      if (codexModels.length === 0) {
        const synced = await syncDiscoveredModels({
          accessToken,
          discoveryUrl: CODEX_DISCOVERY_URL,
          existing: codexModels,
          allModels: models,
          defaults: {
            base_url: CODEX_BASE_URL,
            use_full_url: true,
            tooltip_data: "ChatGPT / OpenAI Codex",
            openai_endpoint: "/v1/responses",
            context_window_tokens: 272000,
            max_completion_tokens: 128000,
            reasoning_effort: null,
            anthropic_max_tokens: null,
          },
        });
        message(t("ChatGPT / Codex 账号授权成功，已同步添加 {count} 个官方模型！", { count: synced.created }));
        onSwitchToModels?.();
      } else {
        message(t("ChatGPT / Codex 账号已保存，余额为 0 时将自动切换。"));
      }
      await loadCodexAccounts();
    } catch (cause) {
      message(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const importCredentialFiles = async (provider: "grok" | "codex", fileList: FileList | null) => {
    if (!fileList || fileList.length === 0) return;
    setImporting(provider);
    try {
      const files: Array<{ name: string; content: unknown }> = [];
      const parseErrors: string[] = [];
      const seen = new Set<string>();
      let bootstrapToken: string | undefined;
      for (const file of Array.from(fileList)) {
        const seenKey = `${file.name}:${file.size}`;
        if (seen.has(seenKey)) continue;
        seen.add(seenKey);
        try {
          const text = (await file.text()).replace(/^\uFEFF/, "");
          const content = JSON.parse(text) as unknown;
          files.push({ name: file.name, content });
          bootstrapToken ??= firstAccessToken(content);
        } catch (cause) {
          parseErrors.push(`${file.name}: ${cause instanceof Error ? cause.message : String(cause)}`);
        }
      }
      const result = { imported: 0, skipped: 0, imported_names: [] as string[], errors: [] as Array<{ name: string; message: string }> };
      for (let index = 0; index < files.length; index += IMPORT_CHUNK_SIZE) {
        const chunk = await api.importAccounts(provider, files.slice(index, index + IMPORT_CHUNK_SIZE));
        result.imported += chunk.imported;
        result.skipped += chunk.skipped;
        result.imported_names.push(...(chunk.imported_names ?? []));
        result.errors.push(...chunk.errors);
      }
      if (provider === "grok") await loadGrokAccounts();
      else await loadCodexAccounts();
      if (provider === "grok" && grokModels.length === 0 && bootstrapToken) {
        await syncDiscoveredModels({
          accessToken: bootstrapToken,
          discoveryUrl: GROK_BASE_URL,
          existing: grokModels,
          allModels: models,
          defaults: {
            base_url: GROK_BASE_URL,
            use_full_url: false,
            tooltip_data: "xAI Grok",
            openai_endpoint: "/v1/chat/completions",
            context_window_tokens: null,
            max_completion_tokens: 16384,
            reasoning_effort: null,
            anthropic_max_tokens: null,
          },
        });
      }
      if (provider === "codex" && codexModels.length === 0 && bootstrapToken) {
        await syncDiscoveredModels({
          accessToken: bootstrapToken,
          discoveryUrl: CODEX_DISCOVERY_URL,
          existing: codexModels,
          allModels: models,
          defaults: {
            base_url: CODEX_BASE_URL,
            use_full_url: true,
            tooltip_data: "ChatGPT / OpenAI Codex",
            openai_endpoint: "/v1/responses",
            context_window_tokens: 272000,
            max_completion_tokens: 128000,
            reasoning_effort: null,
            anthropic_max_tokens: null,
          },
        });
      }
      const failed = result.errors.length + parseErrors.length;
      const importedNames = (result.imported_names ?? []).filter(Boolean);
      const names = importedNames.length <= 5
        ? importedNames.join("、")
        : t("{names} 等 {count} 个", { names: importedNames.slice(0, 3).join("、"), count: importedNames.length });
      const detail = [...result.errors.map(importErrorText), ...parseErrors].slice(0, 8).join("；");
      const duration = failed > 0 ? 8000 : 4000;
      if (result.imported > 0 && failed === 0) {
        message(
          names
            ? t("已导入 {count} 个账号：{names}。", { count: result.imported, names })
            : t("已导入 {count} 个账号。", { count: result.imported }),
          { duration },
        );
      } else if (result.imported > 0) {
        message(
          t("已导入 {imported} 个账号，失败 {failed} 个。{detail}", {
            imported: result.imported,
            failed,
            detail,
          }),
          { duration },
        );
      } else {
        message(t("导入失败：{error}", { error: detail || t("未找到可导入的凭证。") }), { duration });
      }
    } catch (cause) {
      message(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setImporting(null);
      if (provider === "grok" && grokImportInput.current) grokImportInput.current.value = "";
      if (provider === "codex" && codexImportInput.current) codexImportInput.current.value = "";
    }
  };

  const handleActivateAccount = async (provider: "grok" | "codex", accountId: string) => {
    if (provider === "grok") {
      await api.activateGrokAccount(accountId);
      await loadGrokAccounts();
    } else {
      await api.activateCodexAccount(accountId);
      await loadCodexAccounts();
    }
    message(t("已切换活跃账号"));
  };

  const handleDeleteConfirmed = async () => {
    if (!deletingAccount) return;
    const { provider, account } = deletingAccount;
    if (provider === "grok") {
      setGrokAccounts(await api.deleteGrokAccount(account.account_id));
    } else {
      setCodexAccounts(await api.deleteCodexAccount(account.account_id));
    }
    setDeletingAccount(null);
    message(t("账号已删除"));
  };

  const handleClearCooldownConfirmed = async () => {
    if (!clearingCooldown) return;
    const provider = clearingCooldown;
    const accounts = provider === "grok" ? grokAccounts : codexAccounts;
    const cooldownAccounts = accounts.filter(isAccountCooldown);
    for (const acc of cooldownAccounts) {
      if (provider === "grok") await api.deleteGrokAccount(acc.account_id);
      else await api.deleteCodexAccount(acc.account_id);
    }
    if (provider === "grok") await loadGrokAccounts();
    else await loadCodexAccounts();
    setClearingCooldown(null);
    message(t("已清理 {count} 个冷却账号", { count: cooldownAccounts.length }));
  };

  return (
    <div className={styles.root}>
      <div className={styles.boardList}>
        {/* Grok (xAI) 精简主卡片 */}
        <ProviderCard
          provider="grok"
          title="Grok (xAI)"
          subtitle="xAI OAuth 2.0 Device Flow"
          icon={<Icon icon={grokIcon} size="1.35em" />}
          connected={isGrokConnected}
          accounts={grokAccounts}
          activeAccount={activeGrok}
          usage={grokUsage}
          usageError={grokUsageError}
          loadingUsage={checkingGrok}
          onRefreshUsage={() => void loadGrokUsage()}
          onOpenPool={() => setGrokPoolOpen(true)}
          onOpenLoginModal={() => setGrokModalOpen(true)}
          onImportClick={() => grokImportInput.current?.click()}
          importing={importing === "grok"}
          importInputRef={grokImportInput}
          onImportFiles={(files) => void importCredentialFiles("grok", files)}
        />

        {/* Codex (ChatGPT / OpenAI) 精简主卡片 */}
        <ProviderCard
          provider="codex"
          title="Codex (ChatGPT / OpenAI)"
          subtitle="OpenAI OAuth 2.0 Device Flow"
          icon={<Icon icon={openAiIcon} size="1.35em" />}
          connected={isCodexConnected}
          accounts={codexAccounts}
          activeAccount={activeCodex}
          usage={codexUsage}
          usageError={codexUsageError}
          loadingUsage={checkingCodex}
          onRefreshUsage={() => void loadCodexUsage()}
          onOpenPool={() => setCodexPoolOpen(true)}
          onOpenLoginModal={() => setCodexModalOpen(true)}
          onImportClick={() => codexImportInput.current?.click()}
          importing={importing === "codex"}
          importInputRef={codexImportInput}
          onImportFiles={(files) => void importCredentialFiles("codex", files)}
        />
      </div>

      {children && (
        <div className={styles.modelsSection}>
          {children}
        </div>
      )}

      {/* Grok 账号池全屏/大管理面板 */}
      <AccountPoolModal
        open={grokPoolOpen}
        provider="grok"
        providerName="Grok (xAI)"
        accounts={grokAccounts}
        usage={grokUsage}
        usageError={grokUsageError}
        onClose={() => setGrokPoolOpen(false)}
        onActivate={(id) => void handleActivateAccount("grok", id)}
        onDelete={(acc) => setDeletingAccount({ provider: "grok", account: acc })}
        onClearCooldown={() => setClearingCooldown("grok")}
        onAddAccount={() => setGrokModalOpen(true)}
        onImportClick={() => grokImportInput.current?.click()}
      />

      {/* Codex 账号池全屏/大管理面板 */}
      <AccountPoolModal
        open={codexPoolOpen}
        provider="codex"
        providerName="Codex (ChatGPT / OpenAI)"
        accounts={codexAccounts}
        usage={codexUsage}
        usageError={codexUsageError}
        onClose={() => setCodexPoolOpen(false)}
        onActivate={(id) => void handleActivateAccount("codex", id)}
        onDelete={(acc) => setDeletingAccount({ provider: "codex", account: acc })}
        onClearCooldown={() => setClearingCooldown("codex")}
        onAddAccount={() => setCodexModalOpen(true)}
        onImportClick={() => codexImportInput.current?.click()}
      />

      <GrokAuthModal
        open={grokModalOpen}
        onClose={() => setGrokModalOpen(false)}
        onSuccess={handleGrokAuthSuccess}
      />

      <CodexAuthModal
        open={codexModalOpen}
        onClose={() => setCodexModalOpen(false)}
        onSuccess={handleCodexAuthSuccess}
      />

      <ConfirmDialog
        open={deletingAccount !== null}
        title={t("删除账号")}
        cancelLabel={t("取消")}
        confirmLabel={t("删除")}
        onCancel={() => setDeletingAccount(null)}
        onConfirm={() => void handleDeleteConfirmed()}
      >
        <p>
          {deletingAccount
            ? t("确定删除账号“{name}”吗？此操作不可撤销。", { name: deletingAccount.account.display_name })
            : t("确定删除此账号吗？")}
        </p>
      </ConfirmDialog>

      <ConfirmDialog
        open={clearingCooldown !== null}
        title={t("清理冷却/耗尽账号")}
        cancelLabel={t("取消")}
        confirmLabel={t("清理全部")}
        onCancel={() => setClearingCooldown(null)}
        onConfirm={() => void handleClearCooldownConfirmed()}
      >
        <p>{t("确定从账号池中清理所有额度耗尽（0%）的冷却账号吗？")}</p>
      </ConfirmDialog>
    </div>
  );
}

function isAccountCooldown(account: SubscriptionAccount): boolean {
  if (account.limit_reached) return true;
  if (account.remaining_percent !== null && account.remaining_percent <= 0) return true;
  if (account.session_remaining_percent !== null && account.session_remaining_percent <= 0) return true;
  return false;
}

function getAccountStatus(
  account: SubscriptionAccount,
  usage: SubscriptionUsage | null,
  usageError: string | null,
): "error" | "cooldown" | "ready" {
  if (account.active && usageError && !usage && account.remaining_percent === null) {
    return "error";
  }
  if (isAccountCooldown(account)) {
    return "cooldown";
  }
  return "ready";
}

/** 主页面极简供应商卡片 */
function ProviderCard({
  provider,
  title,
  subtitle,
  icon,
  connected,
  accounts,
  activeAccount,
  usage,
  loadingUsage,
  onRefreshUsage,
  onOpenPool,
  onOpenLoginModal,
  onImportClick,
  importing,
  importInputRef,
  onImportFiles,
}: {
  provider: "grok" | "codex";
  title: string;
  subtitle: string;
  icon: ReactNode;
  connected: boolean;
  accounts: SubscriptionAccount[];
  activeAccount?: SubscriptionAccount;
  usage: SubscriptionUsage | null;
  usageError: string | null;
  loadingUsage: boolean;
  onRefreshUsage: () => void;
  onOpenPool: () => void;
  onOpenLoginModal: () => void;
  onImportClick: () => void;
  importing: boolean;
  importInputRef: React.RefObject<HTMLInputElement | null>;
  onImportFiles: (files: FileList | null) => void;
}) {
  const message = useMessage();
  const readyCount = accounts.filter((a) => !isAccountCooldown(a)).length;
  const cooldownCount = accounts.filter(isAccountCooldown).length;

  const copyId = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      message(t("账号 ID 已复制到剪贴板"));
    } catch {
      message(t("复制失败"));
    }
  };

  const weekly = usage?.remaining_percent ?? activeAccount?.remaining_percent ?? null;
  const session = usage?.session_remaining_percent ?? activeAccount?.session_remaining_percent ?? null;

  return (
    <div className={styles.card}>
      <div className={styles.cardTop}>
        <div className={styles.providerInfo}>
          <div className={styles.iconWrap}>{icon}</div>
          <div className={styles.names}>
            <div className={styles.titleRow}>
              <strong>{title}</strong>
              <ConnectionBadge connected={connected} />
            </div>
            <span>{subtitle}</span>
          </div>
        </div>

        <div className={styles.headerActions}>
          <input
            ref={importInputRef}
            type="file"
            accept=".json,application/json"
            multiple
            hidden
            onChange={(event) => onImportFiles(event.target.files)}
          />
          <Button variant="secondary" size="small" disabled={importing} onClick={onImportClick}>
            {importing ? t("导入中…") : t("批量导入")}
          </Button>
          <Button variant={connected ? "secondary" : "primary"} size="small" onClick={onOpenLoginModal}>
            {connected ? t("添加账号") : t("立即登录授权")}
          </Button>
          {connected && (
            <Button variant="secondary" size="small" disabled={loadingUsage} onClick={onRefreshUsage}>
              <Icon icon={refreshIcon} size="1em" /> {loadingUsage ? t("查询中…") : t("刷新活跃额度")}
            </Button>
          )}
        </div>
      </div>

      {/* 正在使用的账号单行精炼展示 */}
      {connected && activeAccount ? (
        <div className={styles.activeRowBanner}>
          <div className={styles.activeMeta}>
            <span className={styles.activeDot} />
            <span className={styles.activePrefix}>{t("当前使用中:")}</span>
            <strong className={styles.activeAccountName} title={activeAccount.display_name}>
              {activeAccount.display_name}
            </strong>
            <button
              type="button"
              className={styles.miniBtn}
              title={t("复制账号 ID")}
              onClick={() => void copyId(activeAccount.account_id)}
            >
              <Icon icon={copyIcon} size="0.9em" />
            </button>
          </div>

          <div className={styles.activeMeters}>
            {provider === "codex" && session !== null && session !== undefined && (
              <div className={styles.compactMeter}>
                <span className={styles.meterName}>{t("5小时窗口")}</span>
                <div className={styles.meterTrack}>
                  <div
                    className={`${styles.meterFill} ${styles[`${remainingTone(session)}Fill`]}`}
                    style={{ width: `${Math.max(0, Math.min(100, session))}%` }}
                  />
                </div>
                <span className={`${styles.meterVal} ${styles[remainingTone(session)]}`}>
                  {formatPercent(session)}
                </span>
              </div>
            )}

            <div className={styles.compactMeter}>
              <span className={styles.meterName}>{t("周额度")}</span>
              <div className={styles.meterTrack}>
                <div
                  className={`${styles.meterFill} ${styles[`${remainingTone(weekly)}Fill`]}`}
                  style={{ width: `${Math.max(0, Math.min(100, weekly ?? 0))}%` }}
                />
              </div>
              <span className={`${styles.meterVal} ${styles[remainingTone(weekly)]}`}>
                {weekly !== null ? formatPercent(weekly) : t("未查询")}
              </span>
            </div>
          </div>
        </div>
      ) : null}

      {/* 底部：账号池状态概览与管理入口 */}
      <div className={styles.cardBottomBar}>
        <div className={styles.poolSummary}>
          {connected ? (
            <>
              <span className={styles.summaryItem}>
                {t("账号池: {count} 个", { count: accounts.length })}
              </span>
              <span className={styles.bullet}>•</span>
              <span className={styles.readyText}>
                {t("可用 {count}", { count: readyCount })}
              </span>
              {cooldownCount > 0 && (
                <>
                  <span className={styles.bullet}>•</span>
                  <span className={styles.cooldownText}>
                    {t("冷却 {count}", { count: cooldownCount })}
                  </span>
                </>
              )}
              <span className={styles.rotationText}>
                {provider === "codex"
                  ? t("（额度为 0 时自动平滑轮换）")
                  : t("（周额度用尽时自动轮换）")}
              </span>
            </>
          ) : (
            <span className={styles.unconnectedHint}>
              {t("尚未接入账号，点击右上角登录授权或批量导入。")}
            </span>
          )}
        </div>

        {connected && (
          <Button variant="secondary" size="small" onClick={onOpenPool}>
            {t("管理账号池 ({count})", { count: accounts.length })}
          </Button>
        )}
      </div>
    </div>
  );
}

type TabType = "all" | "ready" | "cooldown" | "error";

/** 全屏/大视图账号池管理器弹窗 */
function AccountPoolModal({
  open,
  provider,
  providerName,
  accounts,
  usage,
  usageError,
  onClose,
  onActivate,
  onDelete,
  onClearCooldown,
  onAddAccount,
  onImportClick,
}: {
  open: boolean;
  provider: "grok" | "codex";
  providerName: string;
  accounts: SubscriptionAccount[];
  usage: SubscriptionUsage | null;
  usageError: string | null;
  onClose: () => void;
  onActivate: (accountId: string) => void;
  onDelete: (account: SubscriptionAccount) => void;
  onClearCooldown: () => void;
  onAddAccount: () => void;
  onImportClick: () => void;
}) {
  const message = useMessage();
  const [tab, setTab] = useState<TabType>("all");
  const [keyword, setKeyword] = useState("");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);

  const filterKeyword = keyword.trim().toLowerCase();

  // 严格互斥分类
  const categorized = useMemo(() => {
    const ready: SubscriptionAccount[] = [];
    const cooldown: SubscriptionAccount[] = [];
    const error: SubscriptionAccount[] = [];

    for (const acc of accounts) {
      const status = getAccountStatus(acc, usage, usageError);
      if (status === "error") error.push(acc);
      else if (status === "cooldown") cooldown.push(acc);
      else ready.push(acc);
    }
    return { ready, cooldown, error };
  }, [accounts, usage, usageError]);

  const readyCount = categorized.ready.length;
  const cooldownCount = categorized.cooldown.length;
  const errorCount = categorized.error.length;

  const filtered = useMemo(() => {
    const sourceList =
      tab === "ready"
        ? categorized.ready
        : tab === "cooldown"
          ? categorized.cooldown
          : tab === "error"
            ? categorized.error
            : accounts;

    if (!filterKeyword) return sourceList;

    return sourceList.filter(
      (acc) =>
        acc.display_name.toLowerCase().includes(filterKeyword) ||
        acc.account_id.toLowerCase().includes(filterKeyword)
    );
  }, [accounts, categorized, filterKeyword, tab]);

  const total = filtered.length;
  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  const currentPage = Math.min(page, pageCount);

  const paginatedList = useMemo(() => {
    const start = (currentPage - 1) * pageSize;
    return filtered.slice(start, start + pageSize);
  }, [filtered, currentPage, pageSize]);

  const copyId = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      message(t("账号 ID 已复制到剪贴板"));
    } catch {
      message(t("复制失败"));
    }
  };

  return (
    <Modal
      open={open}
      wide
      title={t("{provider} 账号池管理", { provider: providerName })}
      onClose={onClose}
      closeLabel={t("关闭")}
      secondaryAction={
        total > pageSize ? (
          <div className={styles.modalPagination}>
            <Pagination
              page={currentPage}
              pageCount={pageCount}
              pageSize={pageSize}
              total={total}
              pageSizes={[20, 50, 100]}
              onPageChange={setPage}
              onPageSizeChange={(newSize) => {
                setPageSize(newSize);
                setPage(1);
              }}
            />
          </div>
        ) : undefined
      }
    >
      <div className={styles.modalBody}>
        {/* 工具条：Tab 过滤、搜索、批量操作 */}
        <div className={styles.modalToolbar}>
          <div className={styles.modalTabs}>
            <button
              type="button"
              className={`${styles.mTabBtn} ${tab === "all" ? styles.mTabActive : ""}`}
              onClick={() => { setTab("all"); setPage(1); }}
            >
              {t("全部")} <span className={styles.mTabCount}>{accounts.length}</span>
            </button>
            <button
              type="button"
              className={`${styles.mTabBtn} ${tab === "ready" ? styles.mTabActive : ""}`}
              onClick={() => { setTab("ready"); setPage(1); }}
            >
              {t("可用池")} <span className={styles.mTabCount}>{readyCount}</span>
            </button>
            <button
              type="button"
              className={`${styles.mTabBtn} ${tab === "cooldown" ? styles.mTabActive : ""}`}
              onClick={() => { setTab("cooldown"); setPage(1); }}
            >
              {t("冷却中")} <span className={styles.mTabCount}>{cooldownCount}</span>
            </button>
            {errorCount > 0 && (
              <button
                type="button"
                className={`${styles.mTabBtn} ${tab === "error" ? styles.mTabActive : ""}`}
                onClick={() => { setTab("error"); setPage(1); }}
              >
                {t("异常")} <span className={styles.mTabCount}>{errorCount}</span>
              </button>
            )}
          </div>

          <div className={styles.modalActions}>
            {cooldownCount > 0 && (
              <Button size="small" variant="secondary" onClick={onClearCooldown}>
                {t("一键清理冷却账号")}
              </Button>
            )}
            <Button size="small" variant="secondary" onClick={onImportClick}>
              {t("批量导入")}
            </Button>
            <Button size="small" variant="secondary" onClick={onAddAccount}>
              {t("添加账号")}
            </Button>
            <div className={styles.modalSearch}>
              <TextInput
                placeholder={t("搜索账号名称 / ID…")}
                value={keyword}
                onChange={(e) => { setKeyword(e.target.value); setPage(1); }}
              />
            </div>
          </div>
        </div>

        {/* 账号高信息密度表格 */}
        <div className={styles.tableContainer}>
          {total === 0 ? (
            <div className={styles.emptyTable}>
              <p>{t("未找到符合条件的账号")}</p>
            </div>
          ) : (
            <table className={styles.accountTable}>
              <thead>
                <tr>
                  <th style={{ width: 90 }}>{t("状态")}</th>
                  <th>{t("账号名称 / ID")}</th>
                  {provider === "codex" && <th style={{ width: 140 }}>{t("5小时窗口")}</th>}
                  <th style={{ width: 140 }}>{t("周剩余额度")}</th>
                  <th style={{ width: 170 }}>{t("预计重置时间")}</th>
                  <th style={{ width: 130, textAlign: "right" }}>{t("操作")}</th>
                </tr>
              </thead>
              <tbody>
                {paginatedList.map((acc) => {
                  const status = getAccountStatus(acc, usage, usageError);
                  const weekly = acc.remaining_percent;
                  const session = acc.session_remaining_percent;
                  const resetStr = acc.reset_at_ms
                    ? new Date(acc.reset_at_ms).toLocaleString()
                    : acc.plan_label || "--";

                  return (
                    <tr
                      key={acc.account_id}
                      className={`${styles.tableRow} ${acc.active ? styles.activeRow : ""}`}
                    >
                      <td>
                        {acc.active ? (
                          <span className={styles.tagActive}>{t("主用中")}</span>
                        ) : status === "error" ? (
                          <span className={styles.tagError}>{t("异常")}</span>
                        ) : status === "cooldown" ? (
                          <span className={styles.tagCooldown}>{t("冷却中")}</span>
                        ) : (
                          <span className={styles.tagReady}>{t("正常")}</span>
                        )}
                      </td>

                      <td>
                        <div className={styles.cellNameWrap}>
                          <strong className={styles.rowName} title={acc.display_name}>
                            {acc.display_name}
                          </strong>
                          <button
                            type="button"
                            className={styles.miniCopyBtn}
                            title={t("复制账号 ID")}
                            onClick={() => void copyId(acc.account_id)}
                          >
                            <Icon icon={copyIcon} size="0.85em" />
                          </button>
                        </div>
                      </td>

                      {provider === "codex" && (
                        <td>
                          {session !== null && session !== undefined ? (
                            <div className={styles.tableMeter}>
                              <div className={styles.tableMeterTrack}>
                                <div
                                  className={`${styles.tableMeterFill} ${styles[`${remainingTone(session)}Fill`]}`}
                                  style={{ width: `${Math.max(0, Math.min(100, session))}%` }}
                                />
                              </div>
                              <span className={styles[remainingTone(session)]}>
                                {formatPercent(session)}
                              </span>
                            </div>
                          ) : (
                            <span className={styles.textDim}>--</span>
                          )}
                        </td>
                      )}

                      <td>
                        {weekly !== null ? (
                          <div className={styles.tableMeter}>
                            <div className={styles.tableMeterTrack}>
                              <div
                                className={`${styles.tableMeterFill} ${styles[`${remainingTone(weekly)}Fill`]}`}
                                style={{ width: `${Math.max(0, Math.min(100, weekly))}%` }}
                              />
                            </div>
                            <span className={styles[remainingTone(weekly)]}>
                              {formatPercent(weekly)}
                            </span>
                          </div>
                        ) : (
                          <span className={styles.textDim}>{t("未查询")}</span>
                        )}
                      </td>

                      <td>
                        <span className={styles.textDim}>{resetStr}</span>
                      </td>

                      <td>
                        <div className={styles.rowActions}>
                          {!acc.active && (
                            <button
                              type="button"
                              className={styles.actionBtn}
                              onClick={() => onActivate(acc.account_id)}
                            >
                              {t("设为主用")}
                            </button>
                          )}
                          <button
                            type="button"
                            className={styles.actionDeleteBtn}
                            title={t("删除账号")}
                            onClick={() => onDelete(acc)}
                          >
                            <Icon icon={trashIcon} size="0.9em" />
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </Modal>
  );
}

function ConnectionBadge({ connected }: { connected: boolean }) {
  return (
    <span className={`${styles.badge} ${connected ? styles.badgeConnected : styles.badgeAvailable}`}>
      {connected ? (
        <>
          <Icon icon={checkIcon} size="0.9em" /> {t("已连接")}
        </>
      ) : (
        t("支持登录")
      )}
    </span>
  );
}

function remainingTone(remaining: number | null): "toneSuccess" | "toneWarn" | "toneDanger" | "toneNeutral" {
  if (remaining === null) return "toneNeutral";
  if (remaining > 35) return "toneSuccess";
  if (remaining > 10) return "toneWarn";
  return "toneDanger";
}

function formatPercent(value: number): string {
  return `${Math.round(value)}%`;
}

function importErrorText(error: { name: string; message: string }): string {
  const message = error.message.replace(/^(configuration error|config error):\s*/i, "");
  return message.includes(error.name) ? message : `${error.name}: ${message}`;
}

function firstAccessToken(value: unknown): string | undefined {
  if (Array.isArray(value)) {
    for (const item of value) {
      const token = firstAccessToken(item);
      if (token) return token;
    }
    return undefined;
  }
  if (!value || typeof value !== "object") return undefined;
  const record = value as Record<string, unknown>;
  if (typeof record.access_token === "string" && record.access_token.trim()) {
    return record.access_token.trim();
  }
  if (typeof record.key === "string" && record.key.trim()) {
    return record.key.trim();
  }
  if (record.tokens && typeof record.tokens === "object") {
    const tokens = record.tokens as Record<string, unknown>;
    if (typeof tokens.access_token === "string" && tokens.access_token.trim()) {
      return tokens.access_token.trim();
    }
  }
  return undefined;
}

async function syncDiscoveredModels({
  accessToken,
  discoveryUrl,
  existing,
  allModels,
  defaults,
}: {
  accessToken: string;
  discoveryUrl: string;
  existing: Model[];
  allModels: Model[];
  defaults: Omit<ModelInput, "display_name" | "model_id" | "api_key" | "sort_order" | "type" | "openai_extra_params_enabled" | "openai_extra_params" | "custom_headers_enabled" | "custom_headers" | "anthropic_extra_params_enabled" | "anthropic_extra_params" | "anthropic_thinking_effort" | "thinking_budget_tokens">;
}): Promise<{ created: number }> {
  const discovered = await api.discoverModels({
    type: "openai",
    base_url: discoveryUrl,
    api_key: accessToken,
    custom_headers_enabled: false,
    custom_headers: {},
  });
  const existingIds = new Set(existing.map((m) => m.model_id));
  const newDiscovered = discovered.models.filter((m) => !existingIds.has(m.id));
  if (newDiscovered.length === 0) return { created: 0 };
  const nextOrderStart = allModels.length + 1;
  const inputs: ModelInput[] = newDiscovered.map((m, index) => {
    const isCodex = defaults.tooltip_data?.includes("Codex") || defaults.tooltip_data?.includes("ChatGPT");
    const inferredContext = isCodex ? 272000 : contextWindowForModel(m.id, m.context_window_tokens, null);
    return {
      sort_order: nextOrderStart + index,
      display_name: m.id,
      type: "openai",
      base_url: defaults.base_url,
      use_full_url: defaults.use_full_url,
      api_key: "oauth",
      tooltip_data: defaults.tooltip_data ?? "Subscription Model",
      model_id: m.id,
      reasoning_effort: null,
      openai_endpoint: defaults.openai_endpoint,
      openai_extra_params_enabled: false,
      openai_extra_params: {},
      custom_headers_enabled: false,
      custom_headers: {},
      anthropic_extra_params_enabled: false,
      anthropic_extra_params: {},
      context_window_tokens: inferredContext,
      max_completion_tokens: defaults.max_completion_tokens,
      anthropic_max_tokens: null,
      anthropic_thinking_effort: null,
      thinking_budget_tokens: null,
    };
  });
  await api.createModels(inputs);
  await appStore.refresh();
  return { created: inputs.length };
}
