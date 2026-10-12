import type { JsonValue, PluginContext } from "./plugin.ts";
import type { ResourceSnapshot } from "./resource.ts";

export type ModelCapabilities = {
  images?: boolean;
  tools?: boolean;
};

export type ModelDefinition = {
  id: string;
  displayName: string;
  description?: string;
  maxOutputTokens?: number;
  /** Omitted when the upstream catalog does not report a context limit. */
  contextWindowTokens?: number;
  /** Ordinary catalog image flag. Confirmed support is capabilities.images;
   * omit that capability when upstream discovery does not report it. */
  images?: boolean;
  capabilities?: ModelCapabilities;
  /** 之后的调用原样传回;永远不会展示给用户。 */
  privateData?: JsonValue;
};

/** 宿主目录中持久化的一条模型。 */
export type ModelSnapshot = ModelDefinition;

/** Only copy explicit upstream metadata; model names are not capability evidence. */
export function modelMetadata(model: Record<string, unknown>): Pick<
  ModelDefinition, "contextWindowTokens" | "capabilities"
> {
  const rawContext = model.context_window ?? model.contextWindow ??
    model.context_window_tokens ?? model.contextWindowTokens ??
    model.context_length ?? model.contextLength ?? model.max_prompt_length;
  const context = typeof rawContext === "number" ? rawContext :
    typeof rawContext === "string" && rawContext.trim() ? Number(rawContext) : NaN;
  const rawCapabilities = model.capabilities;
  const capabilities = rawCapabilities && typeof rawCapabilities === "object" &&
      !Array.isArray(rawCapabilities)
    ? rawCapabilities as Record<string, unknown>
    : {};
  const tools = capabilities.tools ?? model.supports_tools ?? model.supportsTools ??
    model.supportsFunctionCalling;
  const images = capabilities.images ?? model.supports_images ?? model.supportsImages;
  const inputs = model.input_modalities ?? model.inputModalities;
  return {
    ...(Number.isSafeInteger(context) && context > 0 ? { contextWindowTokens: context } : {}),
    capabilities: {
      ...(typeof tools === "boolean" ? { tools } : {}),
      ...(typeof images === "boolean" ? { images } :
        Array.isArray(inputs) && inputs.every((input) => typeof input === "string")
          ? { images: inputs.some((input) => input.toLowerCase() === "image") }
          : {}),
    },
  };
}

export type ModelListInput = {
  /** 模型发现需要认证时为首个可用资源,否则为 null。 */
  resource: ResourceSnapshot | null;
};

export type ModelSupport = {
  /** 列举成功后,宿主用返回值整体替换该 Provider 的模型目录。 */
  list(input: ModelListInput, context: PluginContext): Promise<ModelDefinition[]>;
};
