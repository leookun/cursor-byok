import cursorIconUrl from "../../assets/icons/cursor.svg";
import type { TabMode, TabSettings } from "../../api";
import { Button } from "../ui/Button";
import { TextInput } from "../ui/FormControls";
import { Icon } from "../ui/Icon";
import { Select } from "../ui/Select";
import { TitledCard } from "../ui/TitledCard";
import styles from "./TabSettingsCard.module.scss";

export function TabSettingsCard({
  settings,
  draft,
  editing,
  saving,
  onDraftChange,
  onEdit,
  onCancel,
  onSave,
}: {
  settings: TabSettings | null;
  draft: TabSettings;
  editing: boolean;
  saving: boolean;
  onDraftChange: (settings: TabSettings) => void;
  onEdit: () => void;
  onCancel: () => void;
  onSave: () => void;
}) {
  const modeLabel = (mode: TabMode) => {
    if (mode === "public") return t("使用公益服务");
    if (mode === "direct") return t("直连");
    return t("自定义");
  };
  const action = editing ? (
    <div className={styles.actionGroup}>
      <Button size="small" disabled={saving} onClick={onCancel}>{t("取消")}</Button>
      <Button variant="primary" size="small" disabled={saving} onClick={onSave}>{saving ? t("保存中…") : t("保存")}</Button>
    </div>
  ) : (
    <button type="button" className={styles.headerAction} disabled={!settings} onClick={onEdit}>{t("编辑")}</button>
  );

  return <TitledCard
    title={<div className={styles.title}><Icon src={cursorIconUrl} size="1.1em" /><span>{t("TAB 设置")}</span></div>}
    action={action}
  >
    <div className={styles.content}>
      {editing ? <>
        <div className={styles.row}>
          <div className={styles.description}>
            <strong>{t("TAB 选择")}</strong>
            <small>{t("直连会使用 Cursor 原账号访问官方 TAB 服务；公益/自定义则走本地模拟账号。")}</small>
          </div>
          <div className={styles.control}><Select
            value={draft.mode}
            ariaLabel={t("TAB 选择")}
            options={[
              { value: "public", label: t("使用公益服务") },
              { value: "direct", label: t("直连") },
              { value: "custom", label: t("自定义") },
            ]}
            onChange={(mode) => onDraftChange({ ...draft, mode: mode as TabMode })}
          /></div>
        </div>
        {draft.mode === "custom" && <div className={styles.row}>
          <div className={styles.description}>
            <strong>{t("TAB 服务地址")}</strong>
            <small>{t("原接口路径会追加到此服务地址。")}</small>
          </div>
          <div className={styles.control}><TextInput
            value={draft.address}
            placeholder="https://tab.leokun.cn"
            aria-label={t("TAB 服务地址")}
            onChange={(event) => onDraftChange({ ...draft, address: event.target.value })}
            onKeyDown={(event) => { if (event.key === "Enter") onSave(); }}
          /></div>
        </div>}
      </> : <>
        <div className={styles.row}>
          <strong>{t("TAB 选择")}</strong>
          <span className={styles.value}>{settings ? modeLabel(settings.mode) : t("加载中…")}</span>
        </div>
        {settings?.mode === "custom" && <div className={styles.row}>
          <strong>{t("TAB 服务地址")}</strong>
          <span className={styles.value}>{settings.address}</span>
        </div>}
      </>}
    </div>
  </TitledCard>;
}
