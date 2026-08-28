import type { CursorAccountStatus } from "../../api";
import { Button } from "../ui/Button";
import { TitledCard } from "../ui/TitledCard";
import styles from "./TabSettingsCard.module.scss";

export function CursorAccountCard({
  account,
  restoring,
  onRestore,
}: {
  account: CursorAccountStatus | null;
  restoring: boolean;
  onRestore: () => void;
}) {
  const sourceLabel = () => {
    if (!account) return t("加载中…");
    if (account.source === "original") return t("Cursor 原账号");
    if (account.source === "local") return t("本地 Ultra 模拟账号");
    return t("未登录");
  };
  const canRestore = Boolean(account?.has_backup && account.source !== "original");

  return (
    <TitledCard title={t("Cursor 账号")}>
      <div className={styles.content}>
        <div className={styles.row}>
          <div className={styles.description}>
            <strong>{t("当前账号")}</strong>
            <small>{t("TAB 直连会使用这个 Cursor 登录态访问官方补全服务。")}</small>
          </div>
          <span className={styles.value}>{account?.email || sourceLabel()}</span>
        </div>
        <div className={styles.row}>
          <strong>{t("账号来源")}</strong>
          <span className={styles.value}>{sourceLabel()}</span>
        </div>
        <div className={styles.row}>
          <div className={styles.description}>
            <strong>{t("原账号备份")}</strong>
            <small>
              {account?.has_backup
                ? t("已备份 {email}，可一键切回。切回后请完全退出并重新打开 Cursor。", {
                    email: account.backup_email || t("原账号"),
                  })
                : t("还没有原账号备份。请先在 Cursor 中登录原账号，或从旧版助手备份中恢复。")}
            </small>
          </div>
          <Button size="small" disabled={!canRestore || restoring} onClick={onRestore}>
            {restoring ? t("恢复中…") : t("切换回原账号")}
          </Button>
        </div>
      </div>
    </TitledCard>
  );
}
