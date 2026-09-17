import { useEffect, useState } from "react";
import type { TokenPricingSettings } from "../../shared/api";
import { appStore, DEFAULT_TOKEN_PRICING, useAppStore } from "../../shared/store/appStore";
import { Button } from "../../shared/ui/Button";
import { FormField, TextInput } from "../../shared/ui/FormControls";
import { TitledCard } from "../../shared/ui/TitledCard";
import { useMessage } from "../../shared/ui/message";
import styles from "./ProxySettingsCard.module.scss";

type PricingDraft = {
  input_per_million: string;
  output_per_million: string;
  cache_read_per_million: string;
  cache_write_per_million: string;
};

function toDraft(pricing: TokenPricingSettings): PricingDraft {
  return {
    input_per_million: String(pricing.input_per_million),
    output_per_million: String(pricing.output_per_million),
    cache_read_per_million: String(pricing.cache_read_per_million),
    cache_write_per_million: String(pricing.cache_write_per_million),
  };
}

function formatPrice(value: number) {
  return `$${value}`;
}

function parsePrice(value: string, label: string) {
  const price = Number(value);
  if (!Number.isFinite(price) || price < 0) {
    throw new Error(t("{label}必须是非负数", { label }));
  }
  return price;
}

function toSettings(draft: PricingDraft): TokenPricingSettings {
  return {
    input_per_million: parsePrice(draft.input_per_million, t("输入价格")),
    output_per_million: parsePrice(draft.output_per_million, t("输出价格")),
    cache_read_per_million: parsePrice(draft.cache_read_per_million, t("缓存读取价格")),
    cache_write_per_million: parsePrice(draft.cache_write_per_million, t("缓存写入价格")),
  };
}

export function PricingSettingsCard() {
  const { pricing } = useAppStore();
  const message = useMessage();
  const [draft, setDraft] = useState<PricingDraft>(() => toDraft(pricing));
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!editing) setDraft(toDraft(pricing));
  }, [pricing, editing]);

  const edit = () => {
    setDraft(toDraft(pricing));
    setEditing(true);
  };

  const cancel = () => {
    setDraft(toDraft(pricing));
    setEditing(false);
  };

  const save = async () => {
    try {
      setSaving(true);
      const next = toSettings(draft);
      if (await appStore.updatePricingSettings(next)) {
        setEditing(false);
        message(t("定价设置已保存"));
      }
    } catch (cause) {
      message(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  };

  const restoreDefault = () => {
    setDraft(toDraft(DEFAULT_TOKEN_PRICING));
  };

  const action = editing ? (
    <div className={styles.actionGroup}>
      <Button size="small" disabled={saving} onClick={restoreDefault}>{t("恢复默认")}</Button>
      <Button size="small" disabled={saving} onClick={cancel}>{t("取消")}</Button>
      <Button variant="primary" size="small" disabled={saving} onClick={() => void save()}>
        {saving ? t("保存中…") : t("保存")}
      </Button>
    </div>
  ) : (
    <button type="button" className={styles.headerAction} onClick={edit}>{t("编辑")}</button>
  );

  return (
    <TitledCard title={t("Token 定价")} action={action}>
      <div className={styles.content}>
        <small>{t("用于首页价值估算的 Token 单价，单位：美元 / 百万 Token。")}</small>
        {editing ? (
          <div className={styles.customFields}>
            <FormField label={t("输入价格（$/1M）")}>
              <TextInput
                type="number"
                min={0}
                step="any"
                value={draft.input_per_million}
                onChange={(event) => setDraft({ ...draft, input_per_million: event.target.value })}
              />
            </FormField>
            <FormField label={t("输出价格（$/1M）")}>
              <TextInput
                type="number"
                min={0}
                step="any"
                value={draft.output_per_million}
                onChange={(event) => setDraft({ ...draft, output_per_million: event.target.value })}
              />
            </FormField>
            <FormField label={t("缓存读取价格（$/1M）")}>
              <TextInput
                type="number"
                min={0}
                step="any"
                value={draft.cache_read_per_million}
                onChange={(event) => setDraft({ ...draft, cache_read_per_million: event.target.value })}
              />
            </FormField>
            <FormField label={t("缓存写入价格（$/1M）")}>
              <TextInput
                type="number"
                min={0}
                step="any"
                value={draft.cache_write_per_million}
                onChange={(event) => setDraft({ ...draft, cache_write_per_million: event.target.value })}
              />
            </FormField>
          </div>
        ) : (
          <>
            <div className={styles.row}>
              <strong>{t("输入价格（$/1M）")}</strong>
              <span className={styles.value}>{formatPrice(pricing.input_per_million)}</span>
            </div>
            <div className={styles.row}>
              <strong>{t("输出价格（$/1M）")}</strong>
              <span className={styles.value}>{formatPrice(pricing.output_per_million)}</span>
            </div>
            <div className={styles.row}>
              <strong>{t("缓存读取价格（$/1M）")}</strong>
              <span className={styles.value}>{formatPrice(pricing.cache_read_per_million)}</span>
            </div>
            <div className={styles.row}>
              <strong>{t("缓存写入价格（$/1M）")}</strong>
              <span className={styles.value}>{formatPrice(pricing.cache_write_per_million)}</span>
            </div>
          </>
        )}
      </div>
    </TitledCard>
  );
}
