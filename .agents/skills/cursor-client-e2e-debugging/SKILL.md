---
name: cursor-client-e2e-debugging
description: Use when debugging Cursor client agent/local-mode/tool/backend-store/provider-replay failures in this repo, especially after the state/context history-store refactor, when triaging installed app bundles read-only, correlating installed-client behavior with repo code, mapping a user-provided id to conversation/request/model-call evidence, replaying provider requests from debug logs, or locating the current client/backend/protocol/log files quickly.
---

当用户反馈 Cursor agent、本地模式、工具调用、协议桥接、客户端 bundle 行为异常，或需要只读核对已安装客户端与仓库实现/日志差异时，使用此技能。

当用户只给一个 UUID / id，希望反查它是 `conversationId`、`requestId`、`modelCallId`、`toolCallId` 还是其它运行期 id，并继续定位对应的会话 history、provider 调用状态或协议日志时，也使用此技能。

当用户遇到 provider 400/参数错误、SSE `event: error`、需要从 `debug/provider.jsonl` 抽取最终 provider body 并用 curl 独立复现时，也使用此技能。

## 首要约束

- 不要修改已安装的 Cursor 客户端代码、bundle、签名或 app 副本。
- 允许且推荐读取、搜索、比对和分析客户端 bundle、日志、协议事件与本仓库实现。
- 如果本仓库存在 `.cursor-app-formatted/`，优先读取这个格式化快照来搜索和引用客户端 bundle；只有在快照缺失、过期或需要 hash/真实性核对时，才只读读取 `/Applications/Cursor.app`。
- 如果用户要求提取、格式化、刷新或规范化 Cursor.app 快照流程，使用 `cursor-app-formatted` skill；调查时可以读格式化代码，但不要 patch 格式化快照里的 bundle 代码。
- 如果用户要求 patch 客户端做 e2e，要改成只读证据采集与差异定位，不执行客户端修改。
- 当前本仓库已经重构为 `state.json + context.json` history-store；不要沿用旧 `data.sqlite`、`conversation.json`、`turns/<n>/request.json|sse.jsonl|summary.json` 排查路径。

## 先做路由判断

- `history + logs` 反查层
  - 现象：用户发来一个 id，要判断它是 `conversationId`、`requestId`、`modelCallId`、`toolCallId`；需要从 `history/<conversationId>/state.json` 与 `history/<conversationId>/context.json` 追运行状态和语义历史。
  - 先读 [references/backend-store-log-tracing.md](references/backend-store-log-tracing.md)
- `provider replay / debug` 层
  - 现象：provider 返回 400/参数错误、SSE `event: error`、需要验证最终出站 provider body 是否能被独立 curl 复现。
  - 先读 [references/provider-replay-debugging.md](references/provider-replay-debugging.md)，必要时使用 [scripts/provider-replay.sh](scripts/provider-replay.sh)
- `cursor-agent` 层
  - 现象：`CursorAgentProvider`、`ClaudeSDKClient`、`AnthropicProxy`、`registerAgentProvider`、`InteractionUpdate` 映射、模型桥接异常。
  - 先读 [references/file-map.md](references/file-map.md) 和 [references/search-patterns.md](references/search-patterns.md)
- `cursor-always-local` / 本地模式协议层
  - 现象：`BidiAppend`、`RunSSE`、`AgentServerMessage`、`ExecClientMessage`、`InteractionResponse`、live checkpoint / pending 收口异常。
  - 先读 [references/file-map.md](references/file-map.md) 和 [references/search-patterns.md](references/search-patterns.md)
- 客户端 bundle 只读定位层
  - 现象：需要核对已安装 app bundle、确认实际运行副本、只读验证行为是否命中，并判断差异来自客户端还是本仓库。
  - 先读 [references/installed-client-readonly-validation.md](references/installed-client-readonly-validation.md)

如果问题同时涉及多层，优先从最靠近故障表象的一层开始，不要一开始就同时追所有链路。

## 当前工作流

1. 如果用户给了一个 id，先用 `history/` 目录、`context.json.items`、`state.json` 和 `logs/app.log` 判断它属于哪类 id；不要假设它一定是 `requestId`。
2. 一旦拿到 `conversationId`，同时看两份事实源：
   - `history/<conversationId>/state.json`：会话元数据和当前状态，例如 loop、token、current todo/plan、`latest_request_prefix`、`last_provider_call`。
   - `history/<conversationId>/context.json`：append-only 的语义历史 entries；prompt replay 由 `ProjectPromptReplay()` 从这里投影。
3. 不要去找旧 provider 调用工件：当前 `RecordLLMRequest` 不再落 `request.json`，`AppendLLMResponseChunk` 是 no-op，`RecordLLMSummary` 只补齐内存态并更新 `state.latest_request_prefix` / usage。
4. 再确认故障主要落在 `cursor-agent`、`cursor-always-local`，还是本仓库 `internal/backend` 的协议兼容层。
5. 用 references 里的固定搜索词快速找到入口函数、协议消息和桥接点。
6. 如果 provider 返回 400/参数错误、SSE `event: error`，或需要验证最终出站 provider body：
   - 先读 [references/provider-replay-debugging.md](references/provider-replay-debugging.md)。
   - 通过 id 反查拿到 `conversationId`、`requestId`、`modelCallId`，再定位 `history/<conversationId>/debug/provider.jsonl`。
   - 必要时运行 [scripts/provider-replay.sh](scripts/provider-replay.sh)，只保存 replay 产物，不把 API key 或完整 request body 写进技能/回复。
7. 如果用户要核对 prefix cache / cache hit：
   - 优先运行 `go run ./scripts/historymetrics [conversationId|path]`
   - 它读取当前 `history/<conversationId>/state.json` 与 `history/<conversationId>/context.json`，并结合 `history/usage.json` 统计。
   - 关注 `cache_read_tokens / prompt_tokens_total`，并检查 `context.json.items` 是否缺失、重复或顺序异常。
8. 如果需要对照已安装 app 与仓库行为：
   - 只做只读核对与证据采集，不修改客户端 bundle / app 副本 / 签名。
   - 优先用 `.cursor-app-formatted/` 中的格式化副本定位符号、行号和控制流；再按需只读核对 `/Applications/Cursor.app` 原始文件 hash 或运行副本。
   - 先确认实际运行的 app 副本和目标 bundle 路径。
   - 再读取 bundle 内容、日志、端口与 history 状态，并与本仓库实现对照。
9. 如果证据显示问题更像是客户端 bundle 行为差异：
   - 记录具体文件、符号、日志和协议证据链。
   - 继续判断本仓库是否可以兼容、绕过，或直接输出分析结论。
   - 不要对已安装 Cursor 客户端做 patch、重签名、替换文件或写入式验证。

## 约束

- 不要修改已安装的 Cursor 客户端代码、bundle、签名或 app 副本。
- 不要默认复刻整套 Cursor backend；先确认是不是只需要改模型桥接层。
- 不要先假设用户给的是 `requestId`；必须同时考虑 `conversationId`、`requestId`、`modelCallId`、`toolCallId`。
- 不要把 `history/<conversationId>/state.json` 和 `history/<conversationId>/context.json` 混为一谈：前者是元数据与当前状态，后者是 replayable 语义历史。
- 不要再依赖 `agent_request_runs`、`agent_conversations`、`agent_history_entries`、`protocol_traces`、`data.sqlite`；当前实现已经不支持 DB-backed store / trace debug UI。
- 不要把当前排查进度、临时结论、一次性的 request_id / 端口 / token 写进技能。
- 技能里只保留稳定流程、固定入口、可复用搜索词和只读验证规则。

## 模型渠道规则

- 模型渠道唯一性不再由 `modelID` 决定。
- 当前规范化渠道 ID 是 `baseURL + modelID + apiKey + displayName + openAIEndpoint` 的短 `SHA-256` hash（前 16 个十六进制字符）。
- resolver 仍兼容 legacy 渠道 ID：`baseURL + modelID + apiKey + displayName`。
- `modelID` 只表示 provider model；排查选择器、默认模型和命中渠道时，要优先看渠道 ID 和 `openAIEndpoint`。

## 参考加载规则

- `history + logs` 路径、id 反查、state/context 生成链路：读 [references/backend-store-log-tracing.md](references/backend-store-log-tracing.md)
- provider 400/参数错误、SSE `event: error`、最终出站 provider body curl 重放：读 [references/provider-replay-debugging.md](references/provider-replay-debugging.md)
- 文件地图：读 [references/file-map.md](references/file-map.md)
- 搜索词与判断树：读 [references/search-patterns.md](references/search-patterns.md)
- 已安装客户端的只读核对、进程确认、行为验证：读 [references/installed-client-readonly-validation.md](references/installed-client-readonly-validation.md)

## 自带脚本

- 统计 prefix cache / cache hit：运行 `go run ./scripts/historymetrics [conversationId|path]`
- 兼容壳脚本：运行 [scripts/cache-hit-rate.mjs](scripts/cache-hit-rate.mjs)
- provider curl 重放：运行 [scripts/provider-replay.sh](scripts/provider-replay.sh)，必填 `REQUEST_LOG`、`REQUEST_ID`、`MODEL_CALL_ID`
