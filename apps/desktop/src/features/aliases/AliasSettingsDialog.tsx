import { useEffect, useState } from "react";
import { api, type AliasSettings } from "../../shared/api";
import { FormField, TextInput } from "../../shared/ui/FormControls";
import { Modal } from "../../shared/ui/Modal";
import { errorText } from "./aliasPresentation";
import styles from "./Aliases.module.scss";

export function AliasSettingsDialog({ onClose }: { onClose: () => void }) {
  const [draft, setDraft] = useState<AliasSettings | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => { let disposed = false; void api.aliasSettings().then((value) => { if (!disposed) setDraft(value); }).catch((cause) => { if (!disposed) setError(errorText(cause)); }); return () => { disposed = true; }; }, []);
  const fields: { key: keyof AliasSettings; label: string }[] = [
    { key: "rate_limit_seconds", label: t("限流冷却（秒）") },
    { key: "transient_seconds", label: t("临时错误冷却（秒）") },
    { key: "authorization_seconds", label: t("授权错误冷却（秒）") },
    { key: "connect_timeout_seconds", label: t("连接超时（秒）") },
    { key: "first_token_timeout_seconds", label: t("首个 Token 超时（秒）") },
  ];
  const range = (key: keyof AliasSettings) => ({ min: key === "authorization_seconds" ? 600 : 1, max: key.includes("timeout") ? 86400 : 2592000 });
  const valid = (key: keyof AliasSettings, value: number) => Number.isSafeInteger(value) && value >= range(key).min && value <= range(key).max;
  const invalid = Boolean(draft && fields.some(({ key }) => !valid(key, draft[key])));
  const save = async () => {
    if (!draft || invalid) return;
    setBusy(true); setError(null);
    try { await api.setAliasSettings(draft); onClose(); }
    catch (cause) { setError(errorText(cause)); }
    finally { setBusy(false); }
  };
  return <Modal open title={t("别名冷却与超时")} busy={busy} onClose={onClose} onSubmit={() => void save()} submitDisabled={!draft || invalid}>
    <div className={styles.stack}>
      {!draft && !error && <span role="status">{t("正在加载…")}</span>}
      {error && <p role="alert" className={styles.error}>{error}</p>}
      {draft && fields.map(({ key, label }) => <FormField key={key} label={label}><TextInput type="number" min={range(key).min} max={range(key).max} step={1} value={Number.isNaN(draft[key]) ? "" : draft[key]} aria-invalid={!valid(key, draft[key])} onChange={(event) => setDraft({ ...draft, [key]: event.target.value === "" ? NaN : Number(event.target.value) })} />{!valid(key, draft[key]) && <span className={styles.error}>{t("请输入 {min} 到 {max} 之间的整数。", { min: range(key).min, max: range(key).max })}</span>}</FormField>)}
    </div>
  </Modal>;
}
