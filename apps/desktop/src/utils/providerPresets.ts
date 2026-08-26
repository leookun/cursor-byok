import deepseekIcon from "../assets/provider-icons/deepseek.svg";
import huoshanIcon from "../assets/provider-icons/huoshan.png";
import kimiIcon from "../assets/provider-icons/kimi.svg";
import minimaxIcon from "../assets/provider-icons/minimax.svg";
import zhipuIcon from "../assets/provider-icons/zhipu.svg";
import type { ModelInput, ProviderInput, ProviderType } from "../api";
import { defaultCustomHeaders } from "./providerDefaults";

export interface ProviderPreset {
  key: string;
  name: string;
  icon: string;
  providerType: ProviderType;
  baseUrl: string;
  description: string;
  keyHint: string;
  /** 已配置过（名称匹配即视为同一接入点） */
  matchName?: string;
  draft: Omit<ProviderInput, "api_key">;
  models: ModelInput[];
}

const model = (
  modelId: string,
  displayName: string,
  endpointType: ProviderType,
  contextWindowTokens: number,
  maxOutputTokens: number,
  reasoning = true,
): ModelInput => ({
  model_id: modelId,
  display_name: displayName,
  endpoint_type: endpointType,
  request_url: "",
  enabled: true,
  sort_order: 0,
  context_window_tokens: contextWindowTokens,
  max_output_tokens: maxOutputTokens,
  reasoning_enabled: reasoning,
  reasoning_effort: reasoning ? "high" : null,
  supports_image_generation: false,
});

export const providerPresets: ProviderPreset[] = [
  {
    key: "zhipu",
    name: "智谱 GLM",
    icon: zhipuIcon,
    providerType: "anthropic",
    baseUrl: "https://open.bigmodel.cn/api/anthropic",
    description: "GLM Coding Plan 套餐（Anthropic 协议），glm-5.3 / glm-4.7",
    keyHint: "bigmodel.cn → 个人编程套餐 → 新建 API Key（套餐 Key 与普通 Key 不通用）",
    matchName: "glm5.3",
    draft: {
      name: "zhipu-glm",
      provider_type: "anthropic",
      base_url: "https://open.bigmodel.cn/api/anthropic",
      custom_headers: { ...defaultCustomHeaders },
      extra_params: {},
    },
    models: [
      model("glm-5.3", "GLM 5.3", "anthropic", 1000000, 65536),
      model("glm-4.7", "GLM 4.7", "anthropic", 200000, 32768),
    ],
  },
  {
    key: "kimi",
    name: "Kimi (Moonshot)",
    icon: kimiIcon,
    providerType: "anthropic",
    baseUrl: "https://api.kimi.com/coding",
    description: "Kimi 编程套餐（Anthropic 协议），K3 / K2.7 Coding",
    keyHint: "Kimi 编程套餐页获取 API Key（api.kimi.com/coding 端点）",
    matchName: "k3",
    draft: {
      name: "kimi",
      provider_type: "anthropic",
      base_url: "https://api.kimi.com/coding",
      custom_headers: { ...defaultCustomHeaders },
      extra_params: {},
    },
    models: [
      model("k3", "Kimi K3", "anthropic", 1048576, 65536),
      model("kimi-for-coding", "K2.7 Coding", "anthropic", 262144, 32768),
    ],
  },
  {
    key: "deepseek",
    name: "DeepSeek",
    icon: deepseekIcon,
    providerType: "openai-chat",
    baseUrl: "https://api.deepseek.com/v1",
    description: "DeepSeek 开放平台（OpenAI 协议），deepseek-chat / deepseek-reasoner",
    keyHint: "platform.deepseek.com → API Keys",
    draft: {
      name: "deepseek",
      provider_type: "openai-chat",
      base_url: "https://api.deepseek.com/v1",
      custom_headers: {},
      extra_params: {},
    },
    models: [
      model("deepseek-chat", "DeepSeek Chat (V3)", "openai-chat", 128000, 8192, false),
      model("deepseek-reasoner", "DeepSeek Reasoner (R1)", "openai-chat", 128000, 65536),
    ],
  },
  {
    key: "volcengine",
    name: "火山引擎方舟",
    icon: huoshanIcon,
    providerType: "openai-chat",
    baseUrl: "https://ark.cn-beijing.volces.com/api/v3",
    description: "豆包 Seed 系列（OpenAI 协议）；模型 ID 以方舟控制台为准",
    keyHint: "console.volcengine.com → 方舟 → API Key 管理",
    draft: {
      name: "volcengine-ark",
      provider_type: "openai-chat",
      base_url: "https://ark.cn-beijing.volces.com/api/v3",
      custom_headers: {},
      extra_params: {},
    },
    models: [
      model("doubao-seed-code-1-6-250915", "Doubao Seed Code 1.6", "openai-chat", 256000, 32768),
      model("doubao-seed-1-6-250615", "Doubao Seed 1.6", "openai-chat", 256000, 32768),
    ],
  },
  {
    key: "minimax",
    name: "MiniMax",
    icon: minimaxIcon,
    providerType: "openai-chat",
    baseUrl: "https://api.minimaxi.com/v1",
    description: "MiniMax-M2（OpenAI 协议）",
    keyHint: "platform.minimaxi.com → API Keys",
    draft: {
      name: "minimax",
      provider_type: "openai-chat",
      base_url: "https://api.minimaxi.com/v1",
      custom_headers: {},
      extra_params: {},
    },
    models: [model("MiniMax-M2", "MiniMax M2", "openai-chat", 245000, 65536)],
  },
];
