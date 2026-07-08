---
name: coding-guidance
description: 本地模式实现指南
---
当用户在处理本地模式的时候，使用此指南

先遵守这个约束：

- 不要修改已安装的 Cursor 客户端代码、bundle 或 app 副本。
- 允许且推荐读取、搜索、比对和分析客户端 bundle、日志、协议与仓库代码。
- 如果用户提到“临时 patch 客户端做 e2e”，也要改成只读排查：核对实际运行副本、采集证据、对照仓库实现，然后把修复落在本仓库代码或输出明确结论。

如果问题已经涉及以下任一事项，请同时读取 `../cursor-client-e2e-debugging/SKILL.md`：

- 需要只读核对已安装的 Cursor 客户端 bundle、日志或运行副本
- 需要确认当前到底是哪一个 app 副本在运行
- 需要同时排查客户端 bundle 与本仓库 forwarder 的协同问题
- 需要对照已安装客户端行为与本仓库实现差异

本地模式协议需要优先核对这些文件：
- proto/agent_v1.proto
- proto/aiserver_v1.proto
客户端是：/Users/leokun/Library/Application\ Support/Cursor 
客户端 bundle 是：/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-always-local/dist/main.js

## Cursor 客户端格式化快照

- 如果用户要求提取、格式化、刷新或规范化 Cursor.app 快照流程，使用 `cursor-app-formatted` skill。
- 如果本仓库存在 `.cursor-app-formatted/`，排查客户端 bundle 时优先读取这里的格式化副本。
- `.cursor-app-formatted/` 是从 `/Applications/Cursor.app/Contents/Resources/app` 只读提取后格式化生成的本地快照；它不应写回、替换或影响已安装的 Cursor.app。
- 常用格式化路径：
  - `.cursor-app-formatted/extensions/cursor-always-local/dist/main.js`
  - `.cursor-app-formatted/extensions/cursor-agent-exec/dist/main.js`
  - `.cursor-app-formatted/extensions/cursor-agent-worker/dist/main.js`
  - `.cursor-app-formatted/out/vs/workbench/workbench.desktop.main.js`
  - `.cursor-app-formatted/out/vs/workbench/api/node/extensionHostProcess.js`
- 如果 `.cursor-app-formatted/` 不存在、明显过期，或需要核对真实安装包 hash，再只读读取 `/Applications/Cursor.app` 原始 bundle。

## 本仓库已固定的会话承接规则

- 下一轮真实请求给 LLM 的历史承接，以 `history/<conversationId>/state.json` + `history/<conversationId>/context.json` 为持久化事实源。
- `state.json` 保存会话元数据和当前状态，例如 `next_turn_seq`、`next_entry_seq`、`context_version`、`current_todos`、`current_plans`、`latest_request_prefix`、`last_provider_call`。
- `context.json.items` 保存 append-only 的语义历史 entries；provider messages 不是主存储事实，而是由 `ProjectPromptReplay()` 从 entries 投影出来。
- 模型渠道唯一性不再由 `modelID` 决定；当前规范化渠道 ID 是 `baseURL + modelID + apiKey + displayName + openAIEndpoint` 的短 `SHA-256` hash，resolver 仍兼容 legacy `baseURL + modelID + apiKey + displayName`。
- 可 replay 的历史应以 entry 顺序稳定追加，不能把已发送给模型且仍需保留的历史移动到新位置。
- 最新态、易变态，例如 active todo、current plan、最新编辑保护和动态 reminder，应优先作为 `state.json` 状态或本轮 latest-only suffix；不要无意持久化成会在后续轮次无限 replay 的历史。
- 新一轮 `run_request` 到来时，服务端应通过 `LoadConversation()` 读取 `state.json + context.json`，再由 projector 投影 prompt replay；客户端带回来的 checkpoint/replay 不参与历史承接真相判定。
- `summary.json`、`replay.json`、`runtime.json`、`request.json`、`conversation.json`、`entries.jsonl`、`turns/` 和数字 turn 目录都属于旧持久化产物，会被 history maintenance 当 legacy artifact 清理。
- 如果发现请求历史与本地状态不一致，优先检查 `context.json.items` 是否缺失、重复、顺序异常，以及 `state.json` 的 `next_entry_seq`、`next_turn_seq`、`context_version`、当前状态字段是否与 entries 派生结果一致；不要再按旧 `summary.json` 路径排查。

# 已确认结论

## 1. `AgentServerMessage` 不是统一都要“回复完成”

要按 `oneof message` 分类看：

- `exec_server_message`
  - 这是服务端发给客户端的“执行请求”。
  - 客户端需要显式回 `ExecClientMessage`。
  - 流式/异常场景下还会回 `ExecClientControlMessage`，常见是：
    - `stream_close`
    - `throw`
    - `heartbeat`
- `interaction_query`
  - 这是服务端发给客户端的“交互请求”。
  - 客户端需要显式回 `InteractionResponse`。
- `interaction_update`
  - 这是展示/状态更新消息，通常不需要客户端回包。
- `conversation_checkpoint_update`
  - 这是 checkpoint 同步消息，通常不需要客户端回包。
- `kv_server_message`
  - 这是 KV 同步消息，通常不需要客户端回包。
- `exec_server_control_message`
  - 这是服务端对执行桥的控制消息（例如 abort），客户端要按控制语义处理，但不是通用“完成 ack”。

## 2. Cursor 客户端没有“收到任意 `ServerMessage` 自动回 ack”的通用层

在 `cursor-always-local/dist/main.js` 里，`BidiTransport.startYieldingInputsToTheServer` 只会把“客户端主动产出的消息”送到 `BidiAppend`：

- 它对输入 iterable 做 `p.value.toBinary()` 后 hex 编码，再发 `BidiAppendRequest.data`
- 说明只有客户端业务逻辑主动产出的 `AgentClientMessage` 才会上行
- 没有发现“收到一个 `AgentServerMessage` 就自动回 completed/ack”的统一机制

因此客户端是否回包，取决于上层业务逻辑有没有因为某个下行消息而主动构造新的 `AgentClientMessage`。

## 2.1 更具体的客户端侧结论

从 `cursor-always-local/dist/main.js` 里能直接确认：

- `AgentServerMessage` 的下行类型里有：
  - `interaction_update`
  - `exec_server_message`
  - `exec_server_control_message`
  - `conversation_checkpoint_update`
  - `interaction_query`
- `AgentClientMessage` 的上行类型里有：
  - `run_request`
  - `exec_client_message`
  - `exec_client_control_message`
  - `interaction_response`

这意味着本地模式不是“server message -> 通用 ack”模型，而是：

- `exec_server_message`
  -> 客户端执行本地工具
  -> 产出 `exec_client_message`
  -> 以及可选 `exec_client_control_message`
- `interaction_query`
  -> 客户端展示或处理交互
  -> 产出 `interaction_response`
- 其他下行消息
  -> 一般只更新 UI / checkpoint /流状态
  -> 不会自然地产生一个“完成 ack”

## 2.2 `exec_server_message` 常见的客户端回包形态

客户端协议模型里已确认这些回包类型：

- `ExecClientMessage`
  - 正常结果面
  - 包括 `read_result` / `write_result` / `grep_result` / `ls_result` / `diagnostics_result` / `mcp_result` / `shell_stream` 等
- `ExecClientControlMessage`
  - 控制面
  - 包括：
    - `stream_close`
    - `throw`
    - `heartbeat`

所以调查本地模式 exec 问题时，不要只盯 `ExecClientMessage`：

- 有些工具只回一次结果面消息
- shell 之类的流式工具会混合回：
  - 多次 `shell_stream`
  - 以及控制消息（例如 `stream_close` / `heartbeat`）

## 2.3 `exec_server_message` 的完整回包形态

事实依据：

- `proto/agent_v1.proto`
  - `ExecServerMessage.oneof message`
  - `ExecClientMessage.oneof message`
  - `ExecClientControlMessage.oneof message`
- `cursor-always-local/dist/main.js`
  - bundle 内含同名 proto 模型
  - `BidiTransport.startYieldingInputsToTheServer` 说明客户端上行消息来自业务逻辑主动构造，不存在通用自动 ack

### 结果面回包：`ExecServerMessage` -> `ExecClientMessage`

`ExecServerMessage` 的 `message` 分支与 `ExecClientMessage` 的 `message` 分支是一一对应的：

- `shell_args`
  -> `shell_result`
- `write_args`
  -> `write_result`
- `delete_args`
  -> `delete_result`
- `grep_args`
  -> `grep_result`
- `read_args`
  -> `read_result`
- `ls_args`
  -> `ls_result`
- `diagnostics_args`
  -> `diagnostics_result`
- `request_context_args`
  -> `request_context_result`
- `mcp_args`
  -> `mcp_result`
- `shell_stream_args`
  -> `shell_stream`
- `background_shell_spawn_args`
  -> `background_shell_spawn_result`
- `list_mcp_resources_exec_args`
  -> `list_mcp_resources_exec_result`
- `read_mcp_resource_exec_args`
  -> `read_mcp_resource_exec_result`
- `fetch_args`
  -> `fetch_result`
- `record_screen_args`
  -> `record_screen_result`
- `computer_use_args`
  -> `computer_use_result`
- `write_shell_stdin_args`
  -> `write_shell_stdin_result`
- `execute_hook_args`
  -> `execute_hook_result`
- `subagent_args`
  -> `subagent_result`

所有这些结果面回包都带：

- `id`
- `exec_id`

服务端匹配时通常优先用：

1. `exec_id`
2. `id`

### 控制面回包：`ExecServerMessage` -> `ExecClientControlMessage`

除了结果面回包外，客户端还可能回控制面消息：

- `stream_close`
  - 表示当前 exec 流已关闭
  - 只有 `id`
- `throw`
  - 表示执行异常
  - 只有 `id` + `error` + 可选 `stack_trace`
- `heartbeat`
  - 表示执行过程中的心跳
  - 只有 `id`

### 关键理解

- `ExecClientControlMessage` 不是某个单独 `ExecServerMessage` 分支的“专属结果类型”
- 它是跨 exec 通用的控制面回包
- 因此调查时必须同时看两类上行：
  - `ExecClientMessage`
  - `ExecClientControlMessage`

### 调查规则

对于任意 `exec_server_message`，至少要确认以下之一是否发生：

- 收到对应的 `ExecClientMessage`
- 或收到 `ExecClientControlMessage.throw`
- 对流式 exec，还要看：
  - 是否有多次增量 `ExecClientMessage`
  - 是否最终有 `stream_close`

如果只看到 started / pending，没有任何结果面或控制面回包，服务端 pending 大概率不会收口。

排查时优先搜索这些关键字：

## 3. forwarder 状态机实现规则

在本仓库修本地模式 forwarder 时，默认遵守下面这些稳定约束，避免再次引入“工具晚到污染当前轮”或“同一 request 在 `[DONE]` 后又续跑一轮”的问题。

### 3.1 resume 必须按 provider pass 隔离

- `request_id` 不是 provider 调用代次；同一个 request 可以合法包含多次 provider pass。
- `scheduleProviderResume` 不能只依赖 request 级布尔态（例如单个 `ResumePending`）。
- resume 请求必须带来源 pass，至少要能区分：
  - 当前 pass 的结果触发的合法续跑
  - 上一轮工具终态晚到造成的陈旧 resume
- `driveProvider` 开始与结束时都要显式清理上一轮的 resume 状态，不能让旧状态跨 pass 残留。

### 3.2 工具晚到是常态，只能影响所属 pass

- `ExecClientMessage` / `ExecClientControlMessage` 晚于 provider `[DONE]` 到达是正常现象。
- 晚到结果只能驱动其所属 pass 的 checkpoint / history / resume 判定，不能影响后续 pass。
- 非流式 exec 的 `stream_close` synthetic recovery 也必须沿用原工具的来源 pass，不能按 request 级全局状态续跑。

### 3.3 pending exec 必须严格按 id 匹配

- `selectPendingExec` / `selectPendingExecByControl` 只允许按：
  - `exec_id`
  - `message_id`
 进行匹配。
- 不允许再用“当前只有一个 pending，就直接返回它”的兜底逻辑。
- 迟到的 result / `stream_close` / `throw` 如果 pending 已不存在：
  - 优先看 `RecentCompletedExecs` 做幂等忽略
  - 不要把它重新落到当前轮的 pending 上

### 3.4 看到这些现象时，优先怀疑 stale resume / stale exec

如果出现下面任一现象，先查 forwarder 状态机，不要先怪客户端：

- 同一个 `request_id` 在 `[DONE]` 后又出现新的 `model_call_id`
- `turns/<n+1>/request.json` 与 `turns/<n>/request.json` messages 几乎完全相同
- 上一轮工具 `grepResult/readResult/...` 晚于上一轮 `[DONE]`
- 晚到的 `stream_close` 恰好跨到下一轮 provider 已经启动之后

优先核对：

- `ProviderPassCount`
- resume 请求的来源 pass
- `PendingExec.ProviderPass`
- `selectPendingExec` 是否存在跨轮误匹配

- `startYieldingInputsToTheServer`
- `bidiAppend({requestId:A,appendSeqno`
- `ExecServerMessage`
- `ExecClientMessage`
- `ExecClientControlMessage`
- `InteractionQuery`
- `InteractionResponse`

## 3. 对本地模式最重要的协议理解

- `exec_server_message` / `interaction_query` 属于“请求型下行消息”
  - 如果客户端不回对应结果，服务端 pending 不会收口
  - 后续重连后可能出现 “No tool output found for function call ...” 这类 provider 400
- `interaction_update` / `conversation_checkpoint_update` 属于“通知型下行消息”
  - 它们用于 UI 展示、同一 backend 进程内的 live checkpoint 同步、状态同步
  - 一般不要求客户端再回一个“完成”消息

## 4. 调查本地模式时的优先顺序

1. 先确认收到的 `AgentServerMessage` 是哪一类
2. 如果是 `exec_server_message`
   - 查客户端是否回了 `ExecClientMessage`
   - 查是否只回了 `stream_close` 但没有真正结果
   - 查 `exec_id` / `id` 是否匹配
3. 如果是 `interaction_query`
   - 查客户端是否回了 `InteractionResponse`
4. 如果是 `conversation_checkpoint_update`
   - 重点查里面的 `pending_tool_calls` / `root_prompt_messages_json` / `turns`
   - 不要误以为它本身需要回 ack

## 5. 对服务端实现的直接要求

- 服务端必须区分“请求型下行”和“通知型下行”
- 服务端不能把 `ServerMessage` 统一建模成“发出去就等一个完成 ack”
- 对请求型消息，必须在本地状态机里维护 pending：
  - `PendingExec`
  - `PendingInteraction`
- 同一 backend 进程内的 `RunSSE` 重连，要优先看 checkpoint / `pending_tool_calls` 里的 live pending
- backend 重启后，不要把 checkpoint 当持久恢复点；跨轮承接与持久恢复只看 `history/<conversationId>/state.json` + `history/<conversationId>/context.json`
