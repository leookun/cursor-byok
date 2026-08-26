import type { ModelInput, ProviderType } from "../api";
import styles from "./PresetModelEditor.module.scss";

interface Props {
  models: ModelInput[];
  endpointType: ProviderType;
  onChange: (models: ModelInput[]) => void;
}

export function PresetModelEditor({ models, endpointType, onChange }: Props) {
  const update = (index: number, patch: Partial<ModelInput>) => {
    onChange(models.map((entry, i) => (i === index ? { ...entry, ...patch } : entry)));
  };
  const remove = (index: number) => {
    onChange(models.filter((_, i) => i !== index));
  };
  const add = () => {
    const template = models[models.length - 1];
    onChange([
      ...models,
      {
        ...(template ?? {
          display_name: "",
          request_url: "",
          sort_order: 0,
          context_window_tokens: 128000,
          max_output_tokens: 32768,
          reasoning_effort: "high",
        }),
        model_id: "",
        display_name: "",
        endpoint_type: endpointType,
        reasoning_enabled: template?.reasoning_enabled ?? true,
        supports_image_generation: false,
      },
    ]);
  };

  return (
    <div className={styles.wrap}>
      <div className={styles.header}>
        <span>{t("模型列表")}</span>
        <span className={styles.hint}>{t("model_id 需与厂商一致；保存后会自动向厂商接口发现其余可用模型")}</span>
      </div>
      <div className={styles.rows}>
        {models.map((entry, index) => (
          <div key={index} className={styles.row}>
            <input
              className={styles.input}
              value={entry.model_id}
              placeholder="model_id，如 kimi-k3"
              spellCheck={false}
              onChange={(event) => update(index, { model_id: event.target.value })}
            />
            <input
              className={styles.input}
              value={entry.display_name}
              placeholder={t("显示名")}
              onChange={(event) => update(index, { display_name: event.target.value })}
            />
            <button type="button" className={styles.remove} onClick={() => remove(index)} aria-label={t("移除")}>
              ×
            </button>
          </div>
        ))}
      </div>
      <button type="button" className={styles.add} onClick={add}>+ {t("添加模型")}</button>
    </div>
  );
}
