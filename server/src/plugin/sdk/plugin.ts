import type { ProviderSupport } from "./provider.ts";
import type { ResourceSupport } from "./resource.ts";

export type JsonPrimitive = string | number | boolean | null;
export type JsonValue = JsonPrimitive | JsonValue[] | { [key: string]: JsonValue };

/**
 * 可本地化文本:纯字符串,或 locale → 文本 的映射
 * (如 { "zh-CN": "账号", "en-US": "Accounts" })。
 * 宿主原样透传,由界面按当前语言解析;模型名等来自上游的数据保持纯字符串。
 */
export type LocalizedText = string | { [locale: string]: string };

export type NetworkRequestInit = {
  method?: string;
  headers?: Record<string, string>;
  body?: string;
};

export type NetworkResponse = {
  status: number;
  headers: Record<string, string>;
  body: string;
};

/** 流式响应体,按行随到随交付(用于 SSE)。 */
export type NetworkEventStream = {
  status: number;
  headers: Record<string, string>;
  lines: AsyncIterable<string>;
};

/**
 * 每次能力调用收到的宿主服务。网络请求仅限 plugin.json 声明的 HTTPS 主机;
 * 宿主取消本次调用时通过 `signal` 中止。
 */
export type PluginContext = {
  network: {
    fetch(url: string, init?: NetworkRequestInit): Promise<NetworkResponse>;
    stream(url: string, init?: NetworkRequestInit): Promise<NetworkEventStream>;
  };
  signal: AbortSignal;
};

/**
 * Provider 插件定义:一组能力实现的集合。插件不持有任何持久状态——
 * 资源与模型目录由宿主存储,每次调用所需的数据都通过参数传入。
 */
export type ProviderPluginDefinition = {
  providers: ProviderSupport[];
  resources?: ResourceSupport[];
};

let registered: ProviderPluginDefinition | undefined;

/** 注册 Provider 插件;每个插件入口只能调用一次。 */
export function defineProviderPlugin(definition: ProviderPluginDefinition): ProviderPluginDefinition {
  if (registered) throw new Error("defineProviderPlugin can only be called once");
  registered = definition;
  return definition;
}

export function __getRegisteredPlugin(): ProviderPluginDefinition {
  if (!registered) throw new Error("plugin entry must call defineProviderPlugin");
  return registered;
}

/** 可序列化的能力摘要,宿主收集它时不调用任何能力方法。 */
export function __descriptor(definition: ProviderPluginDefinition) {
  return {
    providers: definition.providers.map((provider) => ({
      id: provider.id,
      displayName: provider.displayName,
      description: provider.description ?? null,
      providerType: provider.providerType,
      resourceType: provider.resourceType ?? null,
      hasModels: provider.models !== undefined,
    })),
    resources: (definition.resources ?? []).map((resource) => ({
      type: resource.type,
      displayName: resource.displayName,
      add: (resource.add ?? []).map((method) => ({
        type: method.type,
        id: method.id,
        displayName: method.displayName,
        description: method.description ?? null,
        callback: method.type === "oauth2.authorization-code"
          ? {
            port: method.callback?.port ?? null,
            path: method.callback?.path ?? "/oauth-callback",
          }
          : null,
      })),
      import: resource.import
        ? {
          displayName: resource.import.displayName,
          description: resource.import.description ?? null,
          accept: resource.import.accept,
          multiple: resource.import.multiple ?? false,
        }
        : null,
      actions: (resource.actions ?? []).map((action) => ({
        id: action.id,
        displayName: action.displayName,
        description: action.description ?? null,
        target: action.target ?? "resource",
        destructive: action.destructive ?? false,
      })),
      canRefresh: resource.refresh !== undefined,
      canPrepare: resource.prepare !== undefined,
      canRemove: resource.remove !== undefined,
    })),
  };
}
