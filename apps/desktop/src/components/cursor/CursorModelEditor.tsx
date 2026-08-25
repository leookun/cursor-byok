import type { ModelInput, Provider, ProviderInput, ProviderType } from "../../api";
import { FormField, SecretTextInput, TextInput } from "../ui/FormControls";
import { Checkbox } from "../ui/Checkbox";
import { JsonEditor } from "../ui/JsonEditor";
import { Combobox, MultiCombobox, Select } from "../ui/Select";
import { Switch } from "../ui/Switch";
import controls from "../ui/Controls.module.scss";
import { TooltipTrigger } from "../ui/TooltipTrigger";
import { claudeIcon, openAiIcon } from "../ui/icons";
import { defaultCustomHeaders, defaultCustomHeadersText } from "../../utils/providerDefaults";
import styles from "./CursorSettings.module.scss";

export type CursorModelDraft = {
  providerMode: string;
  provider: ProviderInput;
  model: ModelInput;
  modelIds: string[];
  headersText: string;
  extraText: string;
  customRequestUrl: boolean;
};

export const emptyCursorModelDraft = (): CursorModelDraft => ({
  providerMode: "new",
  provider: { name: "", provider_type: "openai-responses", base_url: "", api_key: "", custom_headers: { ...defaultCustomHeaders }, extra_params: {} },
  model: { model_id: "", display_name: "", endpoint_type: "openai-responses", request_url: "", enabled: true, sort_order: 0, context_window_tokens: null, max_output_tokens: null, reasoning_enabled: true, reasoning_effort: null, supports_image_generation: false },
  modelIds: [],
  headersText: defaultCustomHeadersText,
  extraText: "{}",
  customRequestUrl: false,
});

export function CursorModelEditor({ draft, providers, editing, modelOptions, discovering, onChange, onDiscover }: {
  draft: CursorModelDraft;
  providers: Provider[];
  editing: boolean;
  modelOptions: string[];
  discovering: boolean;
  onChange: (draft: CursorModelDraft) => void;
  onDiscover: () => void;
}) {
  const setProvider = (patch: Partial<ProviderInput>) => onChange({ ...draft, provider: { ...draft.provider, ...patch } });
  const setModel = (patch: Partial<ModelInput>) => onChange({ ...draft, model: { ...draft.model, ...patch } });
  const canDiscover = draft.providerMode !== "new"
    || Boolean(draft.provider.base_url.trim() && draft.provider.api_key?.trim());
  const selectProvider = (providerMode: string) => {
    const endpointType = providerMode === "new"
      ? draft.provider.provider_type
      : providers.find((provider) => String(provider.provider_id) === providerMode)?.provider_type;
    onChange({ ...draft, providerMode, model: endpointType ? { ...draft.model, endpoint_type: endpointType } : draft.model });
  };
  const setEndpointType = (endpoint_type: ProviderType) => onChange({
    ...draft,
    provider: draft.providerMode === "new" ? { ...draft.provider, provider_type: endpoint_type } : draft.provider,
    model: { ...draft.model, endpoint_type },
  });
  const setModelIds = (modelIds: string[]) => onChange({
    ...draft,
    modelIds,
    model: {
      ...draft.model,
      model_id: modelIds[0] ?? "",
      display_name: modelIds.length === 1 && draft.modelIds.length !== 1 ? modelIds[0] : draft.model.display_name,
    },
  });
  return <div className={styles.editor}>
    {!editing && <FormField label={t("上游")} hint={t("选择已有上游，或创建一个新的上游。")}><Select ariaLabel={t("选择上游")} value={draft.providerMode} options={[
      { value: "new", label: t("新建上游") },
      ...providers.map((provider) => ({ value: String(provider.provider_id), label: provider.name })),
    ]} onChange={selectProvider} /></FormField>}

    <div className={styles.grid}>
      {!editing && draft.providerMode === "new" && <>
        <FormField label="Base URL" hint={t("模型服务的 API 根地址，例如 https://api.openai.com/v1。")}><TextInput placeholder="例如：https://api.openai.com/v1" value={draft.provider.base_url} onChange={(event) => setProvider({ base_url: event.target.value })} /></FormField>
        <FormField label="API Key" hint={t("访问模型服务所需的密钥。")}><SecretTextInput placeholder="例如：sk-xxxxxx" autoComplete="off" value={draft.provider.api_key ?? ""} onChange={(event) => setProvider({ api_key: event.target.value })} /></FormField>
      </>}
      <FormField label={t("端点类型")} hint={t("默认继承上游，可为当前模型单独修改。")}><Select ariaLabel={t("端点类型")} value={draft.model.endpoint_type} options={[
        { value: "openai-responses", label: "OpenAI Responses", icon: openAiIcon }, { value: "openai-chat", label: "OpenAI Chat", icon: openAiIcon }, { value: "anthropic", label: "Anthropic", icon: claudeIcon },
      ]} onChange={(endpointType) => setEndpointType(endpointType as ProviderType)} /></FormField>
      {(editing || draft.modelIds.length <= 1) && <FormField label={t("显示名称")} hint={t("仅用于界面展示，不会改变发送给上游的模型名称。")}><TextInput placeholder="例如：GPT-4.1" value={draft.model.display_name} onChange={(event) => setModel({ display_name: event.target.value })} /></FormField>}
      <FormField label={t("模型名称")} hint={editing ? t("可以直接输入模型标识，也可以从当前上游返回的模型列表中选择。") : t("支持选择或输入多个模型；批量添加时显示名称默认使用对应模型名称。")}>{editing
        ? <Combobox value={draft.model.model_id} options={modelOptions} placeholder="例如：gpt-4.1" append={<button type="button" className={controls.secondary} disabled={discovering || !canDiscover} onClick={onDiscover}>{discovering ? t("获取中…") : t("获取模型")}</button>} onChange={(model_id) => setModel({ model_id, display_name: draft.model.display_name || model_id })} />
        : <MultiCombobox value={draft.modelIds} options={modelOptions} placeholder="例如：gpt-4.1" append={<button type="button" className={controls.secondary} disabled={discovering || !canDiscover} onClick={onDiscover}>{discovering ? t("获取中…") : t("获取模型")}</button>} onChange={setModelIds} />
      }</FormField>
      <FormField label={t("自定义上下文")} hint={t("自定义模型上下文长度，配置后优先使用自定义项")}>
        <TextInput type="number" min={1} step={1} aria-label={t("自定义上下文 tokens")} placeholder={t("例如：272000")} value={draft.model.context_window_tokens ?? ""} onChange={(event) => setModel({ context_window_tokens: event.target.value === "" ? null : Math.trunc(Number(event.target.value)) })} />
      </FormField>
      <div className={styles.fullWidth}><Checkbox label={t("自定义请求完整地址")} checked={draft.customRequestUrl} onChange={(customRequestUrl) => onChange({ ...draft, customRequestUrl, model: { ...draft.model, request_url: customRequestUrl ? draft.model.request_url : "" } })} /></div>
      {draft.customRequestUrl && <FormField className={styles.fullWidth} label={t("请求完整地址")} hint={t("支持完整 HTTP(S) 地址或以 / 开头、与上游地址组合的相对路径。")}><TextInput placeholder="例如：https://api.example.com/v1/chat/completions" value={draft.model.request_url} onChange={(event) => setModel({ request_url: event.target.value })} /></FormField>}
      {!editing && draft.providerMode === "new" && <>
        <FormField className={styles.fullWidth} label={t("额外参数 JSON")} hint={t("合并到该上游所有模型的请求体。")}><JsonEditor ariaLabel={t("额外参数 JSON")} value={draft.extraText} onChange={(extraText) => onChange({ ...draft, extraText })} /></FormField>
        <FormField className={styles.fullWidth} label={t("自定义 Headers JSON")} hint={t("附加到该上游所有请求的自定义请求头，值必须是字符串。")}><JsonEditor ariaLabel={t("自定义 Headers JSON")} value={draft.headersText} onChange={(headersText) => onChange({ ...draft, headersText })} /></FormField>
      </>}
      <div className={`${styles.switches} ${styles.fullWidth}`}>
        <label><TooltipTrigger label={t("模型是否允许被 Cursor 选择使用。")}><span>{t("启用模型")}</span></TooltipTrigger><Switch label={t("启用模型")} checked={draft.model.enabled} onChange={(enabled) => setModel({ enabled })} /></label>
        <label><TooltipTrigger label={t("是否声明模型支持推理能力。")}><span>{t("启用推理")}</span></TooltipTrigger><Switch label={t("启用推理")} checked={draft.model.reasoning_enabled} onChange={(reasoning_enabled) => setModel({ reasoning_enabled })} /></label>
        <label><TooltipTrigger label={t("是否声明模型支持图片生成。")}><span>{t("图片生成")}</span></TooltipTrigger><Switch label={t("图片生成")} checked={draft.model.supports_image_generation} onChange={(supports_image_generation) => setModel({ supports_image_generation })} /></label>
      </div>
    </div>
  </div>;
}
