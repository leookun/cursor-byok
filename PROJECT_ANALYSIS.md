# Cursor BYOK —— 项目架构与实现分析（面向 AI 的速读手册）

> 本文档目标是让另一个 AI（或新接手的人类工程师）在 **不读源码** 的情况下，快速、准确地理解这个项目「是什么、为什么、怎么实现的、在哪里改」。
> 所有结论均来自对当前仓库（分支 `fix/eng-audit-and-pet-engine`）实际代码的静态阅读，非推测。

---

## 0. 一句话定义

**Cursor BYOK 是一个用 Go + Wails v3 写的桌面应用（“Cursor 助手”），通过本地 MITM 代理拦截 Cursor IDE 发往 `*.cursor.sh` 的 HTTPS 请求，把它自己的本地后端（兼容 Cursor 私有 gRPC/Connect 协议）伪装成 Cursor 官方服务器，从而把任意第三方大模型（OpenAI / Anthropic 兼容 API）接入 Cursor，打破官方对“模型 + 订阅 + 计费”的绑定。**

附带能力：内置一个桌面宠物（PET）引擎、一个本地代理广告/资源服务、自动更新、系统托盘 UI。

---

## 1. 它解决什么问题（Why）

- 官方把 Agent 能力、模型、订阅、计费绑定在一起，用户只能在指定模型/订阅下使用工具。
- 本项目的核心目标：**模型选择权回到用户手里** —— 用户自己的 API Key 可以接到任何 IDE / Chat / Agent 工具里，也可自托管整套服务。
- 实现手段：让 Cursor 客户端以为自己还在连官方服务器，但实际请求被本地代理劫持并转给本地兼容后端，后端再调用用户配置的模型渠道。

---

## 2. 整体架构（How it fits together）

```
┌──────────────────┐     HTTPS (MITM)      ┌──────────────────────────────┐
│  Cursor IDE       │ ====================> │  MITM Proxy (internal/mitm)   │
│  (客户端，未改)    │  *.cursor.sh 流量      │  - 用内置 CA 动态签发证书        │
└──────────────────┘                       │  - 白名单域名 → 转发到 backend  │
                                           │  - 其他域名 → 直连回源           │
                                           └──────────────┬───────────────┘
                                                          │  HTTP (loopback)
                                                          ▼
                                           ┌──────────────────────────────┐
                                           │  Backend (internal/backend)    │
                                           │  - server/   路由+中间件+策略    │
                                           │  - forwarder/ 协议兼容+prompt编译│
                                           │  - agent/model 真正的 LLM 适配   │
                                           │  - 两种模式: local / upstream    │
                                           └──────────────┬───────────────┘
                                                          │  gRPC/Connect → 用户配置的
                                                          ▼  模型渠道 (OpenAI/Anthropic)
                                           ┌──────────────────────────────┐
                                           │  用户模型 API (任意第三方)       │
                                           └──────────────────────────────┘

桌面外壳 (internal/app + Wails v3):
   - 主窗口 (Vue3 前端, embed 进二进制)
   - 系统托盘 (启动/停止服务、显示窗口、显示桌宠、退出)
   - 桌宠引擎 (internal/pet + internal/bridge/pet)
   - 广告/资源服务 (internal/ads)
   - 自动更新 (internal/updater)
   - 证书管理 (internal/certs，内置 CA)
```

**关键数据流（一次对话）**
1. Cursor 发起 `https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend`（或 `agent.v1.AgentService/RunSSE`）的 Connect 流式请求。
2. MITM 代理解密后，识别 `*.cursor.sh` 白名单，把请求原样转发给本地 backend（`http://127.0.0.1:<port>`），并通过 `X-Server-Upstream-URL` 头带上真实上游地址。
3. backend `server` 层 `PolicyMiddleware` 根据 `routing.mode` 选择 **local**（本地处理）或 **upstream**（直连官方）。
4. local 分支进入 `forwarder`：`BidiAppend` / `RunSSE` → 写 history → `PromptCompiler` 把固定 prompt + 历史 + 工具目录编译成发给 LLM 的消息 → 通过 `model adapter router` 选 OpenAI 或 Anthropic 适配器 → 调用用户配置的模型渠道 → 把流式响应转回 Cursor 私有协议格式。
5. 其余非对话接口（如 `ServerTime`、`AvailableModels`、`Dashboard*`、`Auth*`）由 backend 直接 **mock** 返回，让 Cursor 以为已登录/有订阅。

---

## 3. 目录结构与职责（Where to change what）

| 路径 | 职责 | 改它来… |
|---|---|---|
| `main.go` | 入口，`//go:embed` 前端 dist、图标，调 `app.Run` | 改嵌入资源 |
| `internal/app` | Wails 应用装配：窗口、托盘、服务注册、自动启动代理、广告刷新循环 | 改 UI/外壳/生命周期 |
| `internal/mitm` | MITM 代理核心（基于 `elazarl/goproxy`），CA 证书、白名单、转发 | 改拦截/转发规则、TLS |
| `internal/certs` | 内置 CA 证书（`ca.crt`/`ca.key`），按 server name 动态签发叶子证书 | 改证书/信任策略 |
| `internal/backend/host.go` | 把 server + forwarder + 所有路由组装成 HTTP 服务；定义全部 endpoint | **改路由表/新增接口** |
| `internal/backend/server` | 自研轻量路由框架（基于 chi）、中间件、`PolicyMiddleware`、错误编码、配置管理 | 改路由框架/策略/配置 |
| `internal/backend/forwarder` | **协议兼容内核**：`BidiAppend`/`RunSSE`、history 写入、prompt 编译、provider 调用、repository/upload 服务 | 改对话链路/协议转换 |
| `internal/backend/agent/model` | 真正的 LLM 适配器：`router` 选 OpenAI/Anthropic，`openai.go`/`anthropic.go` 发流式请求 | 改模型调用/协议细节 |
| `internal/backend/agent/{bridge,core,prompt,protocol}` | Agent 执行桥接、核心类型、prompt 引擎、入站协议解析 | 改 agent 内部逻辑 |
| `internal/modelchannel` | 渠道身份（SHA-256 ID）、OpenAI endpoint 归一化、适配器解析 | 改渠道寻址/去重 |
| `internal/runtime` | 模型适配器配置结构 `ModelAdapterConfig`、解析/校验、渠道解析为 `ResolvedChannel` | 改用户配置 schema |
| `internal/pet` + `internal/bridge/pet` | 桌宠引擎（动画、状态机、行为、渲染循环、窗口）与前端 bridge | 改桌宠功能 |
| `internal/ads` | 本地代理广告/资源服务、拉取与缓存 | 改广告/资源 |
| `internal/updater` | 自动更新检查与下载 | 改更新逻辑 |
| `internal/appdata` | 所有本地路径常量（`~/.cursor-local-assistant-v2/...`） | 改数据落盘位置 |
| `internal/cursor` | 宿主（Cursor）相关：设备 ID、设置、state DB、证书注入（win/darwin） | 改宿主集成 |
| `internal/netproxy` | 全局 transport 安装、HTTP 客户端构造 | 改网络栈 |
| `internal/historymetrics` | 用量统计聚合（`usage.json`） | 改用量统计 |
| `prompt/` | 真正的 system prompt 资源（agent/ask/plan/debug/commit…），`embed.go` 嵌入 | **改发给模型的 prompt** |
| `proto/` | Cursor 私有 gRPC/Connect 协议 proto 文件（`agent_v1.proto`、`aiserver_v1.proto` 等） | 改协议定义 |
| `frontend/` | Vue3 + Vite 前端（Wails 窗口内容），`bindings/` 是 Go↔JS 桥 | 改 UI |
| `build/` | 打包资源（图标、plist、yml） | 改打包 |

---

## 4. 关键实现细节（How it actually works）

### 4.1 MITM 代理（`internal/mitm/service.go`）
- 基于 `github.com/elazarl/goproxy`，开启 `AllowHTTP2`。
- 内置 CA（`internal/certs` 的 `ca.crt`/`ca.key`）用于给 `*.cursor.sh` 动态签发叶子证书（`CertificateForServerName`）。**用户必须把这个 CA 安装到系统/宿主信任库**，否则 Cursor 会报证书错误。
- `isWhitelistedRelayHost`：只有 `api2.cursor.sh`/`api3.cursor.sh` 和 `*.cursor.sh` 才走本地 backend；其它域名（如普通 https 网站）**直连回源**，不影响宿主其它流量。
- 解密后（`DoFunc`）对白名单请求：删除自身头、记录 cursor 活动（接桌宠）、构造真实 `rawURL`、经由 `forwardToServer` 转发到 backend，并在 header 里注入 `X-Server-Upstream-URL`（让 backend 知道“真实上游是谁”，用于上游模式或 mock 参考）。
- 本地 CORS preflight 由代理直接应答，方便前端跨域。
- 日志有速率限制（`logLimiter`），避免 MITM 噪声刷屏。

### 4.2 后端路由与双模式（`internal/backend/host.go` + `server/policy.go`）
- `ExecutionMode`：`local`（本地处理）与 `upstream`（直连官方 `api2.cursor.sh:443`）。
- `PolicyMiddleware` 根据配置 `routing.mode` 与 `X-Server-Upstream-URL` 决定走哪条分支；每条路由都注册了 `Local` 和 `Upstream` 两个 handler。
- 路由表（host.go 中 `rebuildLocked`）覆盖：
  - 对话主链路：`/aiserver.v1.BidiService/BidiAppend`、`/agent.v1.AgentService/RunSSE`。
  - mock 接口（让 Cursor 以为已登录/有订阅）：`ServerTime`、`GetServerConfig`、`AvailableModels`、`GetEmail`、`/oauth/token`、`Dashboard*`、`Auth*`、`BootstrapStatsig` 等。
  - 转发到自托管 tab server（`https://tab.leokun.cn`）的接口：`StreamCpp`、`FileSync*`、`Cpp*` 等。
  - 本地处理的 repository/upload 服务（`RepositoryService*`、`UploadService*`）。
- 默认上游地址在 `directUpstreamProcedure` 里写死为 `https://api2.cursor.sh:443`。

### 4.3 协议兼容内核（`internal/backend/forwarder`）
- `Service.BidiAppend` / `Service.RunSSE` 是核心入口（service.go 有 3500+ 行，是项目最复杂的文件）。
- 处理流程（来自 `backend/README.md` + service.go）：
  1. 解析 Cursor 私有 Connect 请求（用 `cursor/gen/agentv1`、`cursor/gen/aiserverv1` 生成的 pb）。
  2. 把当前 loop 状态写入 `state.json`，把语义事件追加到 `context.json`（history 目录）。
  3. `PromptCompiler.Compile`（`compiler.go`）把 `common_prefix.md` + 自然历史（`HistoryProjector`）+ `ToolCatalog` + reminders + 用户规则，编译成发给 provider 的 messages/tools。
  4. 通过 `provider.StartStream`（provider.go）→ `modeladapter.NewRouter` → 具体适配器，发起流式调用；用 `sink func(ModelEvent)` 把 provider 事件转成 Cursor 期望的 SSE/Connect 流。
  5. provider usage/cache 统计写入 `history/usage.json`（不从会话文件现场扫描）。
- `loop status` 语义：`idle`/`running`/`waiting_tool`/`completed`/`canceled`/`provider_error`/`failed`（见 README）。

### 4.4 模型适配器与渠道（`internal/backend/agent/model` + `internal/runtime` + `internal/modelchannel`）
- 用户在 `~/.cursor-local-assistant-v2/config.yaml` 配置 `modelAdapters`：`displayName`、`type`(openai|anthropic)、`baseURL`、`apiKey`、`modelID`、`reasoningEffort`、`openAIEndpoint`（`/v1/responses` 或 `/v1/chat/completions`）、自定义头/额外参数等。
- `runtime.NormalizeModelAdapterConfigs` 校验并归一化；渠道唯一 ID = `SHA-256(url|modelID|apiKey|name|endpoint)` 前 16 位（见 `modelchannel.BuildChannelID`，避免重复）。
- `modelchannel.ResolveAdapterIndex` 按 `id` → legacy id → `modelID` 顺序匹配，支持 `fast`/`default`/`auto` 元别名（取第一个）。
- `router.Stream`：按 `provider` 选 OpenAI / Anthropic 适配器；处理 thinking effort 映射（openai `reasoning_effort` ↔ anthropic `adaptive thinking`）、max tokens、自定义头、额外参数；对 messages 做 sanitize（合并相邻 assistant tool call、裁剪悬空 tool call、去掉占位 prefill）以适配不同 provider 的校验要求。
- `openai.go` 支持 `chat/completions` 与 `responses` 两种协议形态（`OpenAIEndpointShape` 按路径末段判断），并累积 tool call 与 reasoning（`<think>`/`</think>` 标签）。

### 4.5 Prompt 资源（`prompt/` + `internal/prompt` 包）
- `prompt/embed.go` 把 `prompt/` 下所有 `.md`（agent/ask/plan/debug/commit/compaction/multitask/subagent/common_prefix）嵌入二进制。
- forwarder 的 `PromptCompiler` 引用 `cursor/prompt` 包按需取用，按 mode/subagent 选择对应资产（`promptAssetModeForConversation`）。

### 4.6 桌宠（PET）引擎（`internal/pet` + `internal/bridge/pet`）
- `internal/pet` 是一个相对独立的桌面宠物框架：动画图集（`atlas`）、动画/状态机（`animation`/`statemachine`）、行为（`behavior`）、意图解析（`intent`/`intent_resolver`）、事件总线（`eventbus`）、窗口（`window_windows.go`）、生命周期（`lifecycle`/`engine`）。`Engine` 是 Composition Root，单线程引擎 + 命令队列 + 渲染循环。
- `internal/bridge/pet` 通过 Wails 事件把宠物列表/状态暴露给前端，并监听 `cursor:activity`（来自 MITM）让宠物对 Cursor 活动作出反应。

### 4.7 外壳与生命周期（`internal/app/runner.go`）
- Wails v3 `application.New`，注册服务：`proxyService`、`metricsService`、`windowService`、`adService`、`petService`。
- 启动即自动开启代理（`ApplicationStarted` → `proxyService.StartProxy`），并开启广告 3 分钟刷新循环。
- 系统托盘菜单：启动/停止服务、检查更新、显示/隐藏窗口、显示桌宠、退出。
- 所有资产通过 `application.AssetOptions.Handler` 提供（embed 的 `frontend/dist`），并加中间件服务用户桌宠资源。
- `OnShutdown` 优雅停止广告刷新、桌宠、窗口、更新、代理。

### 4.8 配置与数据落盘（`internal/appdata/paths.go`）
固定根目录 `~/.cursor-local-assistant-v2/`：
- `config.yaml`：用户配置（含 modelAdapters、routing.mode）。
- `data/ca.crt`：注入给宿主的 CA（让 Cursor 信任 MITM）。
- `data/ads/`：广告包与资源缓存。
- `history/`：`usage.json` + `<conversation_id>/`（state.json/context.json/conversation.lock）。
- `logs/`：文本运行日志。

---

## 5. 技术栈与依赖（What it's built with）

- **语言**：Go 1.25（`go.mod` 声明 `go 1.25.0`）。
- **桌面框架**：`github.com/wailsapp/wails/v3`（alpha.74），前端 Vue3 + Vite7。
- **RPC**：`connectrpc.com/connect` + `google.golang.org/protobuf`（Cursor 私有协议，proto 在 `proto/`）。
- **MITM**：`github.com/elazarl/goproxy`（HTTP/2 MITM）。
- **路由**：`github.com/go-chi/chi/v5`（backend/server 自研封装其上）。
- **DB/存储**：`modernc.org/sqlite`（纯 Go sqlite，用于历史/索引类存储）。
- **网页抓取/转换**：`go-readability`、`html-to-markdown`、`firecrawl`（用于网页/文档类上下文）。
- **其它**：`machineid`（设备 ID）、`uuid`、`yaml.v3`、`tint`（日志）、`samber/lo`（间接）等。
- **前端**：Vue3、vue-router、chart.js（用量图表）、tailwindcss、@wailsio/runtime（Go↔JS 桥）、@iconify。

---

## 6. 构建与运行（Build / Run）

- 前端：`frontend/` 下 `yarn`/`npm` + `vite`（`build` 脚本产出 `frontend/dist`，由 `main.go` embed 进二进制）。
- Go 构建：`go build ./...`（模块名 `cursor`）。生成的二进制即桌面应用。
- 协议代码：由 `proto/` 的 `.proto` 生成到 `cursor/gen/`（被 forwarder/agent 引用；仓库可能用 `extract_extensions_proto.sh` 与 `proto/*.sh` 同步 Cursor 扩展协议）。
- 任务编排：`Taskfile.yml`。
- 运行后：应用自动启动本地代理 + backend；用户需在 Cursor 里把代理设为 `http://127.0.0.1:<proxyPort>`，并把 `data/ca.crt` 安装为受信任根证书，然后在配置里填入自己的模型渠道。

---

## 7. 给「下一个 AI」的实操指引（Where do I start）

| 我想… | 先看 | 再改 |
|---|---|---|
| 新增/修改一个 Cursor 接口 | `internal/backend/host.go` 的 `rebuildLocked` | 在 `server/` 加 handler，或在 `forwarder/` 加 service 方法 |
| 改对话协议转换/流式格式 | `internal/backend/forwarder/service.go` | `provider.go`、`compiler.go`、`projector.go` |
| 支持新模型厂商/新 API 形态 | `internal/backend/agent/model/` | 加 adapter 文件，更新 `router.Stream` 分支 |
| 改用户配置项 | `internal/runtime/local_runtime.go` (`ModelAdapterConfig`) | `internal/backend/server/config/`（`types.go`/`manager.go`/`store.go`）；`modelchannel` 的 ID/校验 |
| 改发给模型的 system prompt | `prompt/` 下对应 `.md` | `internal/backend/forwarder/compiler.go`（`PromptCompiler`）、`internal/prompt` 引用方式 |
| 改拦截/转发规则或证书 | `internal/mitm/service.go`、`internal/certs/ca.go` | 白名单 `isWhitelistedRelayHost`、CA 注入逻辑 `internal/cursor/*_cert*.go` |
| 改 UI | `frontend/src/*`、`frontend/bindings/*` | 通过 Wails 事件/绑定调用 `internal/bridge/*` 服务 |
| 改桌宠 | `internal/pet/*`、`internal/bridge/pet.go` | 动画/状态机/引擎 |
| 改数据落盘位置 | `internal/appdata/paths.go` | — |

---

## 8. 重要约束与已知边界（Gotchas）

- **必须安装内置 CA**：MITM 依赖 `internal/certs` 的 CA，用户需把 `~/.cursor-local-assistant-v2/data/ca.crt` 加入系统/宿主信任库，否则 Cursor HTTPS 握手失败。
- **白名单收窄**：MITM 只劫持 `*.cursor.sh`，不会动其它流量（设计如此，避免变成全局抓包）。
- **最复杂文件**：`internal/backend/forwarder/service.go` 单文件 3500+ 行，是协议兼容主链路，改动风险最高，需谨慎。
- **协议是私有/逆向的**：`proto/` 下是 Cursor 私有协议近似定义（注释 `Copied from: local:...`），随 Cursor 版本可能变动，需持续同步。
- **两种模式**：local 模式完全用本地模型；upstream 模式把请求直连官方（用于需要官方能力时）。切换在 `routing.mode` 配置。
- **当前已不再支持**（按 README）：Pro/`cursor-byok`、HTTP trace debug UI、DB-backed 会话索引/searchable memory。
- **Go 版本要求 1.25**，且用了 Wails v3 alpha（API 相对不稳定，注意升级风险）。

---

## 9. 术语速查

| 术语 | 含义 |
|---|---|
| BYOK | Bring Your Own Key（用自己的模型 API Key） |
| MITM | 中间人代理，本项目中用于解密 Cursor 的 HTTPS 流量 |
| backend | 本地后端 HTTP 服务，伪装成 Cursor 服务器 |
| forwarder | backend 内负责把 Cursor 私有协议转成标准 LLM 调用的内核 |
| model adapter | 把一次对话请求翻译成 OpenAI/Anthropic 兼容请求的适配器 |
| model channel | 用户配置的一个具体模型接入点（baseURL + key + modelID + endpoint） |
| ResolvedChannel | 运行时解析出的实际渠道对象 |
| PromptCompiler | 把历史/工具/规则编译成 provider 请求的编译器 |
| loop status | 一次对话推进的状态机（idle/running/waiting_tool/...） |
| PET | 桌宠引擎，独立于核心代理的娱乐/状态可视化模块 |
| routing.mode | `local`（本地模型）或 `upstream`（直连官方） |

---

*本文档基于仓库静态代码分析生成，如代码更新请以实际源码为准。*
