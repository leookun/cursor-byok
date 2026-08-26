import { useState } from "react";
import { api, type ModelInput, type Provider, type ProviderInput } from "../api";
import { PresetModelEditor } from "../components/PresetModelEditor";
import { ProviderEditor } from "../components/ProviderEditor";
import { ProviderPresetGallery } from "../components/ProviderPresetGallery";
import { ProviderTable } from "../components/ProviderTable";
import { PageContent } from "../components/layout/PageContent";
import controls from "../components/ui/Controls.module.scss";
import { Icon } from "../components/ui/Icon";
import { Modal } from "../components/ui/Modal";
import { TooltipTrigger } from "../components/ui/TooltipTrigger";
import { addIcon } from "../components/ui/icons";
import { useMessage } from "../components/ui/message";
import { PageActions } from "../layouts/PageActions";
import { appStore, useAppStore } from "../store/appStore";
import { defaultCustomHeaders, defaultCustomHeadersText } from "../utils/providerDefaults";
import { providerPresets, type ProviderPreset } from "../utils/providerPresets";
import styles from "./ProvidersPage.module.scss";

const emptyProvider = (): ProviderInput => ({
  name: "",
  provider_type: "openai-chat",
  base_url: "",
  api_key: "",
  custom_headers: { ...defaultCustomHeaders },
  extra_params: {},
});

export function ProvidersPage() {
  const { providers, busy } = useAppStore();
  const message = useMessage();
  const [draft, setDraft] = useState<ProviderInput | null>(null);
  const [editing, setEditing] = useState<Provider | null>(null);
  const [headersText, setHeadersText] = useState(defaultCustomHeadersText);
  const [extraText, setExtraText] = useState("{}");
  const [saving, setSaving] = useState(false);
  const [presetModels, setPresetModels] = useState<ModelInput[] | null>(null);

  const openNew = () => {
    setEditing(null);
    setDraft(emptyProvider());
    setHeadersText(defaultCustomHeadersText);
    setExtraText("{}");
    setPresetModels(null);
  };
  const openPreset = (preset: ProviderPreset) => {
    // 已有同端点接入点时进入编辑模式（保存走更新，避免哈希冲突/重复空壳）
    const existing = providers.find((provider) => provider.base_url.replace(/\/+$/, "") === preset.baseUrl.replace(/\/+$/, "") && provider.provider_type === preset.providerType)
      ?? (preset.matchName ? providers.find((provider) => provider.name === preset.matchName) : undefined);
    if (existing) {
      openEdit(existing);
      return;
    }
    setEditing(null);
    setDraft({ ...preset.draft, api_key: "" });
    setHeadersText(JSON.stringify(preset.draft.custom_headers, null, 2));
    setExtraText("{}");
    setPresetModels(preset.models);
  };
  const openEdit = (provider: Provider) => {
    setEditing(provider);
    setDraft({ name: provider.name, provider_type: provider.provider_type, base_url: provider.base_url, api_key: provider.api_key ?? "", custom_headers: provider.custom_headers, extra_params: provider.extra_params });
    setHeadersText(JSON.stringify(provider.custom_headers, null, 2));
    setExtraText(JSON.stringify(provider.extra_params, null, 2));
  };
  const closeEditor = () => {
    if (saving) return;
    setDraft(null);
    setEditing(null);
    setPresetModels(null);
  };
  const save = async () => {
    if (!draft) return;
    try {
      const name = draft.name.trim();
      const baseUrl = draft.base_url.trim();
      if (!name || !baseUrl) throw new Error(t("名称和 Base URL 不能为空"));
      const input: ProviderInput = {
        ...draft,
        name,
        base_url: baseUrl,
        api_key: editing && !draft.api_key?.trim() ? undefined : draft.api_key,
        custom_headers: parseHeaders(headersText),
        extra_params: parseObject(extraText, t("额外参数")),
      };
      setSaving(true);
      if (editing) {
        const ok = await appStore.updateProvider(editing.provider_id, input);
        if (!ok) return;
      } else {
        // 新建：预设流程会一并挂载模型，普通流程只建接入点
        const provider = await api.createProvider(input);
        if (presetModels) {
          const seen = new Set<string>();
          const manual = presetModels
            .filter((entry) => entry.model_id.trim() !== "")
            .map((entry) => ({ ...entry, model_id: entry.model_id.trim() }))
            .filter((entry) => (seen.has(entry.model_id) ? false : (seen.add(entry.model_id), true)));
          let models = manual;
          try {
            // 向厂商接口发现该 Key 实际可用的模型（如 Kimi K3），失败不阻塞
            const found = await api.discoverModels(provider.provider_id);
            const template = manual[0];
            const extra: ModelInput[] = [];
            for (const id of found.models) {
              const trimmed = id.trim();
              if (!trimmed || seen.has(trimmed)) continue;
              seen.add(trimmed);
              extra.push({
                ...(template ?? { request_url: "", sort_order: 0, context_window_tokens: 128000, max_output_tokens: 32768, supports_image_generation: false, reasoning_effort: null }),
                model_id: trimmed,
                display_name: trimmed,
                endpoint_type: input.provider_type,
                enabled: true,
              } as ModelInput);
            }
            models = [...manual, ...extra];
          } catch {
            // 发现失败（协议不支持或网络问题），按手动列表保存
          }
          if (models.length > 0) {
            await api.saveModels(provider.provider_id, models);
          }
        }
        await appStore.refresh();
      }
      setDraft(null);
      setEditing(null);
      setPresetModels(null);
    } catch (cause) {
      message(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  };

  const content = <ProviderTable providers={providers} onEdit={openEdit} onDelete={(provider) => void appStore.deleteProvider(provider.provider_id)} />;
  const gallery = <ProviderPresetGallery presets={providerPresets} providers={providers} onPick={openPreset} />;

  return <>
    <PageActions><TooltipTrigger label={t("添加上游")}><button className={controls.iconButton} aria-label={t("添加上游")} disabled={busy} onClick={openNew}><Icon icon={addIcon} size="1.1em" /></button></TooltipTrigger></PageActions>
    <PageContent fixed title={t("上游")} contentClassName={styles.pageContent} sections={[
      { key: "presets", estimatedHeight: 120, content: gallery },
      { key: "providers", estimatedHeight: 720, content },
    ]} />
    <Modal open={draft !== null} title={editing ? t("编辑上游") : presetModels ? t("从预设添加上游") : t("添加上游")} busy={saving} onClose={closeEditor} onSubmit={() => void save()}>
      {draft && <>
        <ProviderEditor value={draft} headersText={headersText} extraText={extraText} editing={editing !== null} onChange={setDraft} onHeadersChange={setHeadersText} onExtraChange={setExtraText} />
        {presetModels && <PresetModelEditor models={presetModels} endpointType={draft.provider_type} onChange={setPresetModels} />}
      </>}
    </Modal>
  </>;
}

function parseHeaders(text: string): Record<string, string | null> {
  const parsed = parseObject(text, t("自定义 Headers"));
  if (Object.values(parsed).some((value) => typeof value !== "string" && value !== null)) throw new Error(t("自定义 Headers 的值必须是字符串或 null"));
  return parsed as Record<string, string | null>;
}

function parseObject(text: string, label: string): Record<string, unknown> {
  let parsed: unknown;
  try { parsed = JSON.parse(text || "{}"); } catch { throw new Error(t("{label} 必须是有效 JSON", { label })); }
  if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") throw new Error(t("{label} 必须是 JSON 对象", { label }));
  return parsed as Record<string, unknown>;
}
