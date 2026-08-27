import type { ModelInput, ModelType } from "../../api";
import { GPT56_DEFAULT_CONTEXT, GPT56_LONG_CONTEXT, isGpt56LongContextModel } from "../../utils/modelContext";
import { defaultCustomHeadersText } from "../../utils/modelDefaults";
import { modelPresets, presetEndpoint, trimTrailingSlash, type ModelPreset } from "../../utils/modelPresets";
import { Button } from "../ui/Button";
import { Checkbox } from "../ui/Checkbox";
import { FormField, SecretTextInput, TextInput } from "../ui/FormControls";
import { JsonEditor } from "../ui/JsonEditor";
import { Combobox, Select } from "../ui/Select";
import { Switch } from "../ui/Switch";
import { Tabs } from "../ui/Tabs";
import { claudeIcon, openAiIcon } from "../ui/icons";
import { CursorPresetChips } from "./CursorPresetChips";
import { SubscriptionAuthTab } from "./SubscriptionAuthTab";
import styles from "./CursorSettings.module.scss";

export type CursorModelDraft = {
  model: ModelInput;
  openAIExtraParamsText: string;
  customHeadersText: string;
  anthropicExtraParamsText: string;
};

export const emptyCursorModelDraft = (): CursorModelDraft => ({
  model: {
    sort_order: 0,
    display_name: "",
    type: "openai",
    base_url: "",
    use_full_url: false,
    api_key: "",
    tooltip_data: "",
    model_id: "",
    reasoning_effort: null,
    openai_endpoint: "/v1/responses",
    openai_extra_params_enabled: false,
    openai_extra_params: {},
    custom_headers_enabled: false,
    custom_headers: {},
    anthropic_extra_params_enabled: false,
    anthropic_extra_params: {},
    context_window_tokens: null,
    max_completion_tokens: null,
    anthropic_max_tokens: null,
    anthropic_thinking_effort: "xhigh",
    thinking_budget_tokens: null,
  },
  openAIExtraParamsText: "{}",
  customHeadersText: defaultCustomHeadersText,
  anthropicExtraParamsText: "{}",
});

export function CursorModelEditor({ draft, isEditing = false, modelOptions, contextByModelId, discovering, onChange, onDiscover, onGrokAuthorized }: {
  draft: CursorModelDraft;
  isEditing?: boolean;
  modelOptions: string[];
  contextByModelId?: Record<string, number>;
  discovering: boolean;
  onChange: (draft: CursorModelDraft) => void;
  onDiscover: () => void;
  onGrokAuthorized?: () => void;
}) {
  const setModel = (patch: Partial<ModelInput>) => onChange({ ...draft, model: { ...draft.model, ...patch } });
  const setType = (type: ModelType) => {
    // 切换协议类型时，若当前地址命中某预设的另一协议端点，自动换到该预设对应协议的端点，
    // 避免出现「类型是 Anthropic、URL 却是 OpenAI chat/completions」的错配
    const other: ModelType = type === "anthropic" ? "openai" : "anthropic";
    const preset = modelPresets.find((candidate) => trimTrailingSlash(presetEndpoint(candidate, other).baseUrl) === trimTrailingSlash(draft.model.base_url.trim()));
    const endpoint = preset ? presetEndpoint(preset, type) : null;
    onChange({
      ...draft,
      model: {
        ...draft.model,
        type,
        ...(endpoint ? {
          base_url: endpoint.baseUrl,
          use_full_url: endpoint.useFullUrl,
          custom_headers_enabled: endpoint.customHeaders !== null,
          custom_headers: endpoint.customHeaders ? { ...endpoint.customHeaders } : {},
        } : {}),
        openai_endpoint: type === "openai" ? (endpoint?.openaiEndpoint || draft.model.openai_endpoint || "/v1/responses") : "",
        anthropic_thinking_effort: type === "anthropic" ? draft.model.anthropic_thinking_effort || "xhigh" : null,
      },
      customHeadersText: endpoint?.customHeaders ? JSON.stringify(endpoint.customHeaders, null, 2) : draft.customHeadersText,
    });
  };
  const numberValue = (value: string) => value === "" ? null : Math.trunc(Number(value));
  const canDiscover = Boolean(draft.model.base_url.trim() && draft.model.api_key.trim());
  // 选中预设后，把该服务商已知的模型 id 并入下拉，方便直接选（仍可用「获取模型」发现）
  const presetModelOptions = modelPresets
    .filter((preset) => trimTrailingSlash(presetEndpoint(preset, draft.model.type).baseUrl) === trimTrailingSlash(draft.model.base_url.trim()))
    .flatMap((preset) => preset.models.map((item) => item.model_id));
  const combinedOptions = [...new Set([...modelOptions, ...presetModelOptions])];
  const applyPreset = (preset: ModelPreset) => {
    const endpoint = presetEndpoint(preset, draft.model.type);
    const first = preset.models[0];
    // 切到别家服务商时清空 API Key（不同家的 Key 不能串用）；同一家内切换协议则保留
    const currentBase = trimTrailingSlash(draft.model.base_url.trim());
    const sameProvider = [preset.endpoints.anthropic, preset.endpoints.openai]
      .some((candidate) => trimTrailingSlash(candidate.baseUrl) === currentBase);
    onChange({
      ...draft,
      model: {
        ...draft.model,
        base_url: endpoint.baseUrl,
        use_full_url: endpoint.useFullUrl,
        openai_endpoint: draft.model.type === "openai" ? endpoint.openaiEndpoint : draft.model.openai_endpoint,
        custom_headers_enabled: endpoint.customHeaders !== null,
        custom_headers: endpoint.customHeaders ? { ...endpoint.customHeaders } : {},
        api_key: sameProvider ? draft.model.api_key : "",
        model_id: first?.model_id ?? draft.model.model_id,
        display_name: first?.display_name ?? draft.model.display_name,
        tooltip_data: !draft.model.tooltip_data.trim() || draft.model.tooltip_data === t("备注") ? preset.name : draft.model.tooltip_data,
        context_window_tokens: first?.context_window_tokens ?? draft.model.context_window_tokens,
        ...(draft.model.type === "openai"
          ? { max_completion_tokens: first?.max_output_tokens ?? draft.model.max_completion_tokens }
          : { anthropic_max_tokens: first?.max_output_tokens ?? draft.model.anthropic_max_tokens }),
      },
      customHeadersText: endpoint.customHeaders ? JSON.stringify(endpoint.customHeaders, null, 2) : draft.customHeadersText,
    });
  };
  const requestUrlPlaceholder = draft.model.use_full_url
    ? draft.model.type === "anthropic"
      ? "https://api.anthropic.com/v1/messages"
      : draft.model.openai_endpoint === "/v1/chat/completions"
        ? "https://api.openai.com/v1/chat/completions"
        : "https://api.openai.com/v1/responses"
    : draft.model.type === "anthropic"
      ? "https://api.anthropic.com"
      : "https://api.openai.com";

  const customForm = (
    <div className={styles.editor}>
      <CursorPresetChips type={draft.model.type} baseUrl={draft.model.base_url} onPick={applyPreset} />
      <div className={styles.grid}>
        <FormField label={t("模型类型")}><Select ariaLabel={t("模型类型")} value={draft.model.type} options={[
          { value: "openai", label: "OpenAI", icon: openAiIcon },
          { value: "anthropic", label: "Anthropic", icon: claudeIcon },
        ]} onChange={(value) => setType(value as ModelType)} /></FormField>
        {draft.model.type === "openai" && <FormField label={t("请求协议")} hint={t("只决定请求与响应的格式，不会改变请求地址。")}> <Select ariaLabel={t("请求协议")} value={draft.model.openai_endpoint} options={[
          { value: "/v1/responses", label: "Responses API" },
          { value: "/v1/chat/completions", label: "Chat Completions API" },
        ]} onChange={(openai_endpoint) => setModel({ openai_endpoint })} /></FormField>}

        <div className={styles.urlField}>
          <FormField label={draft.model.use_full_url ? t("完整请求 URL") : t("服务器地址")} hint={draft.model.use_full_url ? t("系统会原样使用此地址，不追加或修改请求路径。") : t("系统会根据请求协议自动追加标准端点路径。")}> <TextInput placeholder={requestUrlPlaceholder} value={draft.model.base_url} onChange={(event) => setModel({ base_url: event.target.value })} /></FormField>
          <Checkbox checked={draft.model.use_full_url} label={t("使用完整请求地址")} onChange={(use_full_url) => setModel({ use_full_url })} />
        </div>
        <div className={styles.apiKeyField}>
          <FormField label="API Key / Token" hint={t("访问模型服务所需的密钥。")}>
            <SecretTextInput placeholder="sk-xxxxxx" autoComplete="off" value={draft.model.api_key} onChange={(event) => setModel({ api_key: event.target.value })} />
          </FormField>
        </div>

        <FormField label={t("模型名称")} hint={t("可以直接输入模型标识，也可以读取接口返回的模型列表。")}> <Combobox value={draft.model.model_id} options={combinedOptions} placeholder="gpt-5" append={<Button className={styles.discoverButton} disabled={discovering || !canDiscover} onClick={onDiscover}>{discovering ? t("获取中…") : t("获取模型")}</Button>} onChange={(model_id) => {
          const next: Partial<ModelInput> = { model_id, display_name: draft.model.display_name || model_id };
          if (isGpt56LongContextModel(model_id)) {
            next.context_window_tokens = draft.model.context_window_tokens === GPT56_LONG_CONTEXT ? GPT56_LONG_CONTEXT : GPT56_DEFAULT_CONTEXT;
          } else if (contextByModelId?.[model_id]) {
            next.context_window_tokens = contextByModelId[model_id];
          }
          setModel(next);
        }} /></FormField>
        <FormField label={t("显示名称")} hint={t("仅用于界面展示，不会改变发送给模型服务的模型名称。")}> <TextInput placeholder={t("例如：主力模型")} value={draft.model.display_name} onChange={(event) => setModel({ display_name: event.target.value })} /></FormField>
        <FormField className={styles.fullWidth} label={t("备注")} hint={t("显示在 Cursor 模型说明中。")}> <TextInput placeholder={t("请输入模型备注")} value={draft.model.tooltip_data} onChange={(event) => setModel({ tooltip_data: event.target.value })} /></FormField>

        {isGpt56LongContextModel(draft.model.model_id) ? (
          <FormField label={t("1M 上下文")} hint={t("默认 272K。开启后使用 1M 上下文窗口。")}>
            <Switch
              label={t("1M 上下文")}
              checked={(draft.model.context_window_tokens ?? GPT56_DEFAULT_CONTEXT) >= GPT56_LONG_CONTEXT}
              onChange={(enabled) => setModel({ context_window_tokens: enabled ? GPT56_LONG_CONTEXT : GPT56_DEFAULT_CONTEXT })}
            />
          </FormField>
        ) : (
          <FormField label={t("上下文窗口 Token")} hint={t("留空时使用默认值。Grok 可从模型接口读取该值。")}>
            <TextInput type="number" min={1} step={1} placeholder={t("留空使用默认值")} value={draft.model.context_window_tokens ?? ""} onChange={(event) => setModel({ context_window_tokens: numberValue(event.target.value) })} />
          </FormField>
        )}
        {draft.model.type === "openai" ? <>
          <FormField label={t("最大输出 Token")} hint={t("留空时使用默认值。")}> <TextInput type="number" min={1} step={1} placeholder={t("留空使用默认值")} value={draft.model.max_completion_tokens ?? ""} onChange={(event) => setModel({ max_completion_tokens: numberValue(event.target.value) })} /></FormField>
          <FormField label={t("推理强度")}> <Select ariaLabel={t("推理强度")} value={draft.model.reasoning_effort ?? ""} options={effortOptions(true)} onChange={(value) => setModel({ reasoning_effort: value || null })} /></FormField>
        </> : <>
          <FormField label={t("最大输出 Token")} hint={t("留空时使用默认值。")}> <TextInput type="number" min={1} step={1} placeholder={t("留空使用默认值")} value={draft.model.anthropic_max_tokens ?? ""} onChange={(event) => setModel({ anthropic_max_tokens: numberValue(event.target.value) })} /></FormField>
          <FormField label={t("思考强度")}> <Select ariaLabel={t("思考强度")} value={draft.model.anthropic_thinking_effort ?? "xhigh"} options={effortOptions(false)} onChange={(anthropic_thinking_effort) => setModel({ anthropic_thinking_effort })} /></FormField>
          <FormField label={t("思考预算 Token")} hint={t("留空时使用 adaptive thinking。")}> <TextInput type="number" min={1} step={1} placeholder={t("留空使用 adaptive thinking")} value={draft.model.thinking_budget_tokens ?? ""} onChange={(event) => setModel({ thinking_budget_tokens: numberValue(event.target.value) })} /></FormField>
        </>}

        <ToggleJsonField
          label={t("自定义 Headers")}
          enabled={draft.model.custom_headers_enabled}
          text={draft.customHeadersText}
          onEnabledChange={(custom_headers_enabled) => setModel({ custom_headers_enabled })}
          onTextChange={(customHeadersText) => onChange({ ...draft, customHeadersText })}
        />
        {draft.model.type === "openai" ? <ToggleJsonField
          label={t("OpenAI 额外参数")}
          enabled={draft.model.openai_extra_params_enabled}
          text={draft.openAIExtraParamsText}
          onEnabledChange={(openai_extra_params_enabled) => setModel({ openai_extra_params_enabled })}
          onTextChange={(openAIExtraParamsText) => onChange({ ...draft, openAIExtraParamsText })}
        /> : <ToggleJsonField
          label={t("Anthropic 额外参数")}
          enabled={draft.model.anthropic_extra_params_enabled}
          text={draft.anthropicExtraParamsText}
          onEnabledChange={(anthropic_extra_params_enabled) => setModel({ anthropic_extra_params_enabled })}
          onTextChange={(anthropicExtraParamsText) => onChange({ ...draft, anthropicExtraParamsText })}
        />}
      </div>
    </div>
  );

  if (isEditing) {
    return customForm;
  }

  return (
    <Tabs
      defaultValue="custom"
      items={[
        {
          value: "custom",
          label: t("连接到模型"),
          content: customForm,
        },
        {
          value: "subscriptions",
          label: t("订阅与认证"),
          content: <SubscriptionAuthTab onSwitchToModels={() => onGrokAuthorized?.()} />,
        },
      ]}
    />
  );
}

function ToggleJsonField({ label, enabled, text, onEnabledChange, onTextChange }: {
  label: string;
  enabled: boolean;
  text: string;
  onEnabledChange: (enabled: boolean) => void;
  onTextChange: (text: string) => void;
}) {
  return <div className={`${styles.fullWidth} ${styles.jsonOption}`}>
    <label><span>{label}</span><Switch label={label} checked={enabled} onChange={onEnabledChange} /></label>
    {enabled && <JsonEditor ariaLabel={label} value={text} onChange={onTextChange} />}
  </div>;
}

function effortOptions(optional: boolean) {
  return [
    ...(optional ? [{ value: "", label: t("不设置") }] : []),
    { value: "low", label: "Low" },
    { value: "medium", label: "Medium" },
    { value: "high", label: "High" },
    { value: "xhigh", label: "Extra High" },
    { value: "max", label: "Max" },
  ];
}
