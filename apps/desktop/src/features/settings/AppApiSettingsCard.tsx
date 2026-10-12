import { useEffect, useState } from "react";
import { api, type AppApiSettings } from "../../shared/api";
import { Button } from "../../shared/ui/Button";
import { FormField, SecretTextInput } from "../../shared/ui/FormControls";
import { Select } from "../../shared/ui/Select";
import { Switch } from "../../shared/ui/Switch";
import { TitledCard } from "../../shared/ui/TitledCard";
import { useMessage } from "../../shared/ui/message";
import styles from "./AppApiSettingsCard.module.scss";

const EMPTY: AppApiSettings = {
  enabled: false,
  auth_required: false,
  auth_method: "bearer",
  api_key: "",
};

export function AppApiSettingsCard({ servicePort }: { servicePort: number }) {
  const message = useMessage();
  const [saved, setSaved] = useState<AppApiSettings | null>(null);
  const [draft, setDraft] = useState<AppApiSettings>(EMPTY);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    void api.appApiSettings().then((settings) => {
      setSaved(settings);
      setDraft(settings);
    }).catch((cause: unknown) => message(cause instanceof Error ? cause.message : String(cause)));
  }, [message]);

  const save = async () => {
    try {
      setSaving(true);
      const settings = await api.setAppApiSettings(draft);
      setSaved(settings);
      setDraft(settings);
      message(t("应用控制 API 设置已保存"));
    } catch (cause) {
      message(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  };

  const generateKey = () => {
    const bytes = new Uint8Array(32);
    crypto.getRandomValues(bytes);
    const key = btoa(String.fromCharCode(...bytes)).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
    setDraft((current) => ({ ...current, api_key: key }));
  };

  const address = `http://127.0.0.1:${servicePort}/byok/app/v1`;
  const changed = saved !== null && (
    saved.enabled !== draft.enabled
    || saved.auth_required !== draft.auth_required
    || saved.auth_method !== draft.auth_method
    || saved.api_key !== draft.api_key
  );
  const authHint = draft.auth_method === "bearer"
    ? t("调用时设置请求头 Authorization: Bearer，值为上方密钥。")
    : t("调用时设置请求头 X-Api-Key，值为上方密钥。");

  return <TitledCard title={t("应用控制")} action={<Button size="small" variant="primary" disabled={!changed || saving} onClick={() => void save()}>
    {saving ? t("保存中…") : t("保存")}
  </Button>}>
    <div className={styles.content}>
      <div className={styles.row}>
        <div className={styles.description}>
          <strong>{t("开启应用控制 API")}</strong>
          <small>{t("允许本机代理配置应用、接入模型，并调用桌面端使用的管理接口。")}</small>
        </div>
        <Switch label={t("开启应用控制 API")} checked={draft.enabled} disabled={!saved || saving}
          onChange={(enabled) => setDraft((current) => ({ ...current, enabled }))} />
      </div>
      <div className={styles.row}>
        <div className={styles.description}>
          <strong>{t("需要授权")}</strong>
          <small>{t("关闭时，能访问本机端口的程序可以直接调用。开启后，请求必须使用所选方式携带密钥。")}</small>
        </div>
        <Switch label={t("需要授权")} checked={draft.auth_required} disabled={!saved || saving}
          onChange={(auth_required) => setDraft((current) => ({ ...current, auth_required }))} />
      </div>
      {draft.auth_required && <>
        <FormField label={t("授权方式")}>
          <Select ariaLabel={t("授权方式")} value={draft.auth_method} disabled={!saved || saving}
            options={[
              { value: "bearer", label: "Authorization: Bearer" },
              { value: "api_key", label: "X-Api-Key" },
            ]}
            onChange={(auth_method) => setDraft((current) => ({ ...current, auth_method: auth_method === "api_key" ? "api_key" : "bearer" }))} />
        </FormField>
        <div className={styles.keyRow}>
          <FormField label={t("API 密钥")} hint={t("可生成新密钥，也可粘贴已有密钥。保存后生效。")}>
            <SecretTextInput value={draft.api_key} autoComplete="off" disabled={!saved || saving}
              onChange={(event) => setDraft((current) => ({ ...current, api_key: event.target.value }))} />
          </FormField>
          <Button size="small" disabled={!saved || saving} onClick={generateKey}>{t("生成密钥")}</Button>
        </div>
        <small className={styles.hint}>{authHint}</small>
      </>}
      <div className={styles.address}>
        <strong>{t("基础地址")}</strong>
        <code>{address}</code>
      </div>
      <small className={styles.hint}>{t("路径与应用内部管理接口一致，例如 /models、/plugins、/settings 和 /harness/cursor。关闭时，这个地址拒绝访问。应用窗口仍使用内部管理接口。")}</small>
    </div>
  </TitledCard>;
}
