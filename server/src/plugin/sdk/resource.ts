import type { JsonValue, LocalizedText, PluginContext } from "./plugin.ts";

/**
 * 资源是插件定义的私有记录(通常是上游账号),由 Provider 消费。
 * 宿主负责持久化、列表和每次调用的资源选择;插件只负责创建、投影和解释资源。
 */
export type ResourceState =
  | { status: "ready" }
  | { status: "cooling"; retryAtMs?: number; message?: string }
  | { status: "invalid"; message?: string };

/** 由添加流程或导入产生的新资源。 */
export type ResourceDraft = {
  /** 去重键:宿主按 (资源类型, key) 执行 upsert。 */
  key: string;
  /** 凭证与插件私有字段;永远不会展示给用户。 */
  privateData: JsonValue;
  /** 缺省为 ready。 */
  state?: ResourceState;
};

/** 宿主已持久化的一条资源。 */
export type ResourceSnapshot = {
  /** 宿主分配的标识,区别于插件的去重键。 */
  id: string;
  type: string;
  key: string;
  privateData: JsonValue;
  state: ResourceState;
};

/** 宿主原子应用到单条资源上的部分更新。 */
export type ResourcePatch = {
  privateData?: JsonValue;
  state?: ResourceState;
};

export type ResourceMetric = {
  id: string;
  label: LocalizedText;
  unit: "percent" | "count";
  /** percent 指标表示剩余占比,0..100。 */
  value: number;
  resetAtMs?: number;
};

export type ResourceActionTarget = "resource" | "card";

export type ResourceAction = {
  id: string;
  displayName: LocalizedText;
  description?: LocalizedText;
  target?: ResourceActionTarget;
  destructive?: boolean;
  run(
    resource: ResourceSnapshot,
    input: JsonValue,
    context: PluginContext,
  ): Promise<ResourceActionResult>;
};

export type ResourceActionField = {
  id: string;
  label: LocalizedText;
  value: string;
};

/** 资源操作返回的通用详情卡片;不得包含凭证。 */
export type ResourceActionCard = {
  id: string;
  title: LocalizedText;
  status?: LocalizedText;
  grantedAtMs?: number;
  expiresAtMs?: number;
  fields?: ResourceActionField[];
};

export type ResourceActionResult = {
  title: LocalizedText;
  description?: LocalizedText;
  cards?: ResourceActionCard[];
  /** 消费类操作可用它更新宿主保存的资源状态。 */
  patch?: ResourcePatch;
};

/** 单条资源的用户可见投影;不得泄露凭证。displayName 是数据(如邮箱),保持纯字符串。 */
export type ResourceView = {
  displayName: string;
  description?: LocalizedText;
  metrics?: ResourceMetric[];
};

/**
 * OAuth 2.0 设备码式添加流程。宿主负责绘制 UI、驱动轮询循环
 * (间隔、slow-down 退避、超时判定),并在流程存续期内在内存中持有
 * `session`;插件只实现两次 HTTP 状态转移。
 */
export type OAuth2AddMethod = {
  type: "oauth2.0";
  id: string;
  displayName: LocalizedText;
  description?: LocalizedText;
  begin(context: PluginContext): Promise<OAuth2Begin>;
  poll(session: JsonValue, context: PluginContext): Promise<OAuth2Poll>;
};

export type OAuth2Begin = {
  /** 不透明流程状态(如设备码);永远不会持久化。 */
  session: JsonValue;
  userCode: string;
  verificationUrl: string;
  verificationUrlComplete?: string;
  expiresAtMs: number;
  pollIntervalMs: number;
};

export type OAuth2Poll =
  | { status: "pending"; session?: JsonValue }
  | { status: "slow-down"; session?: JsonValue }
  | { status: "completed"; resources: ResourceDraft[] }
  | { status: "denied"; message?: string }
  | { status: "failed"; message: string };

/** Core 托管浏览器回调、state 与 PKCE 的 OAuth 2.0 授权码流程。 */
export type OAuth2AuthorizationCodeAddMethod = {
  type: "oauth2.authorization-code";
  id: string;
  displayName: LocalizedText;
  description?: LocalizedText;
  /** 仅在上游 OAuth 客户端要求固定 loopback 地址时指定。 */
  callback?: { port?: number; path?: string };
  begin(
    input: {
      redirectUri: string;
      state: string;
      codeChallenge: string;
    },
    context: PluginContext,
  ): Promise<OAuth2AuthorizationCodeBegin>;
  complete(
    session: JsonValue,
    input: {
      code: string;
      redirectUri: string;
      codeVerifier: string;
    },
    context: PluginContext,
  ): Promise<ResourceDraft[]>;
};

export type OAuth2AuthorizationCodeBegin = {
  session: JsonValue;
  authorizationUrl: string;
  expiresAtMs: number;
  pollIntervalMs?: number;
};

export type ResourceAddMethod = OAuth2AddMethod | OAuth2AuthorizationCodeAddMethod;

export type ResourceImportFile = {
  name: string;
  /** 文件原文;解析和校验由插件负责。 */
  content: string;
};

export type ResourceImportSupport = {
  displayName: LocalizedText;
  description?: LocalizedText;
  /** 宿主文件选择器接受的扩展名,如 [".json"]。 */
  accept: string[];
  multiple?: boolean;
  parse(files: ResourceImportFile[], context: PluginContext): Promise<ResourceImportResult>;
};

export type ResourceImportResult = {
  resources: ResourceDraft[];
  /** 单个文件的问题,值得提示但不必使整次导入失败。 */
  warnings?: string[];
};

export type ResourceSupport = {
  type: string;
  displayName: LocalizedText;
  add?: ResourceAddMethod[];
  import?: ResourceImportSupport;
  present(resource: ResourceSnapshot): ResourceView;
  actions?: ResourceAction[];
  /** 用户主动触发时重新读取上游状态(额度、凭证有效性)。 */
  refresh?(resource: ResourceSnapshot, context: PluginContext): Promise<ResourcePatch>;
  /** 可选的后台刷新间隔;未声明时宿主绝不自动刷新。 */
  refreshIntervalMs?: number;
  /** 可选的上游撤销;宿主随后删除本地记录。 */
  remove?(resource: ResourceSnapshot, context: PluginContext): Promise<void>;
};
