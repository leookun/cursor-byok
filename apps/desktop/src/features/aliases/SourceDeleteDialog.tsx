import { useEffect, useState } from "react";
import { api, type SourceType } from "../../shared/api";
import { ConfirmDialog } from "../../shared/ui/ConfirmDialog";
import { errorText } from "./aliasPresentation";
import styles from "./Aliases.module.scss";

export function SourceDeleteDialog({ title, sourceType, sourceIds, busy, onCancel, onConfirm }: {
  title: string; sourceType: SourceType; sourceIds: string[]; busy?: boolean; onCancel: () => void; onConfirm: () => void;
}) {
  const [names, setNames] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const ids = JSON.stringify(sourceIds);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setError(null);
    void api.aliases(controller.signal).then((aliases) => {
      const selected = new Set<string>(JSON.parse(ids));
      setNames(aliases.filter((alias) => alias.targets.some((target) => target.source_type === sourceType && selected.has(target.source_id))).map((alias) => alias.name));
    }).catch((cause) => { if (!controller.signal.aborted) setError(errorText(cause)); }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [ids, sourceType]);
  return <ConfirmDialog destructive open title={title} confirmLabel={title} busy={busy} confirmDisabled={loading || Boolean(error)} onCancel={onCancel} onConfirm={() => { if (!error) onConfirm(); }}>
    <p>{t("确定删除此来源吗？")}</p>
    {loading && <p role="status">{t("正在检查别名引用…")}</p>}
    {error && <p role="alert" className={styles.error}>{t("无法检查别名引用，请关闭后重试。")}{" "}{error}</p>}
    {names.length > 0 && <><p className={styles.warning}>{t("以下别名引用了此来源。删除后这些目标可能不可用，别名将尝试其他来源。")}</p><ul>{names.map((name) => <li key={name}>{name}</li>)}</ul></>}
  </ConfirmDialog>;
}
