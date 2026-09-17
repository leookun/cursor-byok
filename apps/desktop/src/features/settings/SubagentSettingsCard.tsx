import { useCallback, useEffect, useMemo, useState } from "react";
import { api, pluginText, type SubagentRoutingSettings } from "../../shared/api";
import { useI18n } from "../../i18n/store";
import { useAppStore } from "../../shared/store/appStore";
import { Button } from "../../shared/ui/Button";
import { Checkbox } from "../../shared/ui/Checkbox";
import { TextInput } from "../../shared/ui/FormControls";
import { ModelSelect, type ModelSelectOption } from "../../shared/ui/ModelSelect";
import { TitledCard } from "../../shared/ui/TitledCard";
import { claudeIcon, flatColorOrganizationIcon, openAiIcon } from "../../shared/ui/icons";
import { useMessage } from "../../shared/ui/message";
import { modelProviderName } from "../../shared/utils/modelProvider";
import styles from "./SubagentSettingsCard.module.scss";

function errorText(cause: unknown) {
  return cause instanceof Error ? cause.message : String(cause);
}

export function SubagentSettingsCard() {
  const { models, plugins } = useAppStore();
  const { locale } = useI18n();
  const message = useMessage();
  const [settings, setSettings] = useState<SubagentRoutingSettings | null>(null);
  const [draft, setDraft] = useState<SubagentRoutingSettings | null>(null);
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [newAliasKey, setNewAliasKey] = useState("");

  useEffect(() => {
    let active = true;
    void (async () => {
      try {
        const loaded = await api.subagentRoutingSettings();
        if (active) {
          setSettings(loaded);
          setDraft(loaded);
        }
      } catch (cause) {
        if (active) message(errorText(cause));
      }
    })();
    return () => {
      active = false;
    };
  }, [message]);

  const modelOptions = useMemo(() => {
    const options: ModelSelectOption[] = [
      { value: "", label: t("默认（首个可用模型）"), group: "Cursor" },
    ];
    const seen = new Set<string>();
    for (const model of models) {
      seen.add(model.model_hash);
      options.push({
        value: model.model_hash,
        label:
          model.display_name && model.display_name !== model.model_id
            ? `${model.display_name}（${model.model_id}）`
            : model.display_name || model.model_id,
        group: modelProviderName(model),
        icon: model.type === "anthropic" ? claudeIcon : openAiIcon,
      });
    }
    for (const plugin of plugins) {
      for (const provider of plugin.providers) {
        if (!provider.configured) continue;
        const group = pluginText(provider.displayName, locale) || plugin.name;
        for (const model of provider.models.filter((model) => model.enabled)) {
          seen.add(model.id);
          options.push({
            value: model.id,
            label: model.displayName,
            group,
            iconSrc: model.icon || undefined,
            icon: model.icon ? undefined : flatColorOrganizationIcon,
          });
        }
      }
    }
    if (settings?.target_model_id && !seen.has(settings.target_model_id)) {
      seen.add(settings.target_model_id);
      options.push({ value: settings.target_model_id, label: settings.target_model_id, group: "Cursor" });
    }
    if (settings?.model_aliases) {
      for (const target of Object.values(settings.model_aliases)) {
        if (target && !seen.has(target)) {
          seen.add(target);
          options.push({ value: target, label: target, group: "Cursor" });
        }
      }
    }
    return options;
  }, [locale, models, plugins, settings]);

  const resolveModelLabel = useCallback(
    (modelHash: string) => {
      if (!modelHash) return t("默认（首个可用模型）");
      const found = modelOptions.find((o) => o.value === modelHash);
      return found?.label ?? modelHash;
    },
    [modelOptions],
  );

  const startEdit = useCallback(() => {
    if (!settings) return;
    setDraft({ ...settings, model_aliases: { ...settings.model_aliases } });
    setNewAliasKey("");
    setEditing(true);
  }, [settings]);

  const cancelEdit = useCallback(() => {
    setDraft(settings ? { ...settings, model_aliases: { ...settings.model_aliases } } : null);
    setNewAliasKey("");
    setEditing(false);
  }, [settings]);

  const saveSettings = useCallback(async () => {
    if (!draft) return;
    setSaving(true);
    try {
      const saved = await api.setSubagentRoutingSettings(draft);
      setSettings(saved);
      setDraft(saved);
      setEditing(false);
      message(t("子 Agent 路由设置已保存"));
    } catch (cause) {
      message(errorText(cause));
    } finally {
      setSaving(false);
    }
  }, [draft, message]);

  const updateAlias = useCallback(
    (key: string, targetModel: string) => {
      if (!draft) return;
      setDraft({
        ...draft,
        model_aliases: {
          ...draft.model_aliases,
          [key]: targetModel,
        },
      });
    },
    [draft],
  );

  const removeAlias = useCallback(
    (key: string) => {
      if (!draft) return;
      const nextAliases = { ...draft.model_aliases };
      delete nextAliases[key];
      setDraft({
        ...draft,
        model_aliases: nextAliases,
      });
    },
    [draft],
  );

  const addAlias = useCallback(() => {
    const trimmed = newAliasKey.trim();
    if (!trimmed || !draft) return;
    if (draft.model_aliases[trimmed] !== undefined) {
      message(t("模型别名已存在"));
      return;
    }
    setDraft({
      ...draft,
      model_aliases: {
        ...draft.model_aliases,
        [trimmed]: "",
      },
    });
    setNewAliasKey("");
  }, [draft, newAliasKey, message]);

  const action = editing ? (
    <div className={styles.actionGroup}>
      <Button size="small" disabled={saving} onClick={cancelEdit}>
        {t("取消")}
      </Button>
      <Button
        variant="primary"
        size="small"
        disabled={saving}
        onClick={() => void saveSettings()}
      >
        {saving ? t("保存中…") : t("保存")}
      </Button>
    </div>
  ) : (
    <button
      type="button"
      className={styles.headerAction}
      disabled={!settings}
      onClick={startEdit}
    >
      {t("编辑")}
    </button>
  );

  if (!settings && !editing) {
    return (
      <TitledCard title={t("子 Agent 路由与模型别名")}>
        <div className={styles.content}>
          <div className={styles.row}>
            <span className={styles.value}>{t("正在加载…")}</span>
          </div>
        </div>
      </TitledCard>
    );
  }

  const displayData = editing ? draft : settings;
  const isEnabled = displayData?.enabled ?? true;
  const targetModel = displayData?.target_model_id ?? "";
  const aliases = displayData?.model_aliases ?? {};

  return (
    <TitledCard title={t("子 Agent 路由与模型别名")} action={action}>
      <div className={styles.content}>
        <div className={styles.row}>
          <div className={styles.details}>
            <strong>{t("启用子 Agent 拦截与模型别名")}</strong>
            <small>
              {t(
                "开启后，自动拦截子 Agent 及 Composer 2.5 调用并重定向至自定义模型；关闭后直连官方上游。",
              )}
            </small>
          </div>
          {editing && draft ? (
            <div className={styles.control}>
              <Checkbox
                checked={draft.enabled}
                label={t("已启用")}
                onChange={(enabled) => setDraft({ ...draft, enabled })}
              />
            </div>
          ) : (
            <span className={styles.value}>
              {isEnabled ? t("已启用") : t("已禁用")}
            </span>
          )}
        </div>

        <div className={styles.row}>
          <div className={styles.details}>
            <strong>{t("子 Agent 默认目标模型")}</strong>
            <small>
              {t("拦截未指定模型的子 Agent（如 generalPurpose、explore）时使用的自定义模型")}
            </small>
          </div>
          {editing && draft ? (
            <div className={styles.control}>
              <ModelSelect
                mode="single"
                value={draft.target_model_id}
                options={modelOptions}
                disabled={saving}
                label={t("子 Agent 默认目标模型")}
                onChange={(val) => setDraft({ ...draft, target_model_id: val })}
              />
            </div>
          ) : (
            <span className={styles.value}>{resolveModelLabel(targetModel)}</span>
          )}
        </div>

        <div className={styles.aliasesSection}>
          <div className={styles.details}>
            <strong>{t("托管模型别名重写")}</strong>
            <small>
              {t(
                "当请求指定 Cursor 官方模型时，自动重定向至本地自定义模型。留空时使用上方默认目标模型。",
              )}
            </small>
          </div>

          <div className={styles.aliasesList}>
            {Object.entries(aliases).map(([key, value]) => (
              <div key={key} className={styles.aliasRow}>
                <span className={styles.aliasKey}>{key}</span>
                {editing && draft ? (
                  <div className={styles.aliasSelect}>
                    <ModelSelect
                      mode="single"
                      value={value}
                      options={modelOptions}
                      disabled={saving}
                      label={key}
                      onChange={(targetHash) => updateAlias(key, targetHash)}
                    />
                    <button
                      type="button"
                      className={styles.deleteButton}
                      title={t("删除别名")}
                      aria-label={t("删除别名")}
                      onClick={() => removeAlias(key)}
                    >
                      ×
                    </button>
                  </div>
                ) : (
                  <span className={styles.value}>{resolveModelLabel(value)}</span>
                )}
              </div>
            ))}
          </div>

          {editing && (
            <div className={styles.newAliasRow}>
              <div className={styles.newAliasInput}>
                <TextInput
                  value={newAliasKey}
                  placeholder={t("添加模型别名，如 gemini-3.8-flash")}
                  onChange={(event) => setNewAliasKey(event.target.value)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") {
                      event.preventDefault();
                      addAlias();
                    }
                  }}
                />
              </div>
              <Button size="small" onClick={addAlias}>
                {t("添加别名")}
              </Button>
            </div>
          )}
        </div>
      </div>
    </TitledCard>
  );
}
