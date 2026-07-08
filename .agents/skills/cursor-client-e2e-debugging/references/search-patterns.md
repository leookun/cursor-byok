# 搜索词与判断树

## 先判断层级

### `history + logs` / id 反查层

当现象是“用户只给了一个 id”“需要判断它是 `conversationId`、`requestId`、`modelCallId`、`toolCallId`”“要从本地 history 和日志追状态”：

优先搜索：

- `state.json`
- `context.json`
- `usage.json`
- `logs/app.log`
- `conversation_id`
- `current_request_id`
- `request_id`
- `model_call_id`
- `tool_call_id`
- `latest_request_prefix`
- `last_provider_call`
- `current_loop_status`
- `context_version`
- `next_entry_seq`
- `next_turn_seq`
- `LoadConversation`
- `CreateConversation`
- `SaveConversationWithEntries`
- `AppendEntries`
- `UpdateConversationMeta`
- `ReplaceEntries`
- `ProjectPromptReplay`
- `UsageFileStore`
- `UpsertEvent`
- `LookupEvent`

不要再优先搜索或依赖：

- `data.sqlite`
- `protocol_traces`
- `agent_request_runs`
- `conversation.json`
- `entries.jsonl`
- `turns/<n>`
- `request.json`
- `sse.jsonl`
- `summary.json`

这些是旧实现或 legacy artifact 相关线索，只在排查迁移/清理逻辑时作为历史背景。

### `cursor-agent-exec` / `cursor-agent-worker` 层

当现象涉及 agent 主循环、模型桥接、`InteractionUpdate` 映射、工具 started/completed、session/provider 状态：

优先在已安装客户端 split bundle 中搜索：

- `/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-agent-exec/dist/main.js`
- `/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-agent-exec/dist/*.js`
- `/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-agent-worker/dist/main.js`

优先搜索：

- `registerAgentProvider`
- `CursorAgentProvider`
- `CursorAgentProviderHandle`
- `ClaudeSDKClient`
- `streamInteractionUpdates`
- `handlePartialMessage`
- `AnthropicProxy`
- `getAnthropicProxyPort`
- `getAnthropicProxyAuthToken`
- `ANTHROPIC_BASE_URL`
- `ANTHROPIC_API_KEY`
- `InteractionUpdate`
- `checkpoint`

旧路径 `/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-agent/dist/main.js` 可能不存在。先确认实际 `extensions/` 结构，再按 `cursor-agent-exec` / `cursor-agent-worker` / `cursor-always-local` 分层排查。

### agent window / conversation metadata UI 层

当现象涉及 agent window 标题、窗口信息、titlebar 按钮、是否可点击修改、会话名/metadata 更新：

优先在已安装客户端主 UI 和 split bundle 中搜索：

- `/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js`
- `/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-agent-exec/dist/main.js`
- `/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-agent-exec/dist/*.js`
- `/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-always-local/dist/main.js`

优先搜索：

- `shouldShowAgentWindowTitleHelperText`
- `glass_open_agents_titlebar_button`
- `open_agent_window_top`
- `open_agent_window_bottom_convo`
- `glass.enable_open_agent_in_window`
- `NameAgentRequest`
- `NameAgentResponse`
- `UpdateConversationMetadataRequest`
- `UpdateConversationMetadataResponse`
- `CreateTranscriptOverviewRequest`
- `createTranscriptOverview`
- `updateConversationMetadata`
- `conversation_checkpoint_update`

判断规则：

- `ConversationStateStructure` / `conversation_checkpoint_update` 是 UI 同步快照，不应作为持久化修改入口。
- 如果客户端调用 `UpdateConversationMetadata` / `NameAgent`，要继续确认本地后端是否显式注册对应 `/agent.v1.AgentService/*` 路由；不能只看 proto message 存在。
- 本地模式修改会话名/metadata 时，应落到 `history/<conversationId>/state.json` 或等价持久化会话元数据，再 publish checkpoint 同步 UI。

### `cursor-always-local` / 协议层

当现象涉及本地模式、客户端没有回包、pending 不收口、同一 backend 进程内的 live checkpoint 重连错乱：

优先搜索：

- `BidiTransport`
- `startYieldingInputsToTheServer`
- `BidiAppend`
- `RunSSE`
- `AgentServerMessage`
- `AgentClientMessage`
- `ExecServerMessage`
- `ExecClientMessage`
- `ExecClientControlMessage`
- `InteractionQuery`
- `InteractionResponse`
- `conversation_checkpoint_update`

### 本仓库 forwarder 层

当现象涉及本地后端收发、provider 继续/暂停、exec/interaction 桥接、history 投影：

优先搜索：

- `handleRunIntent`
- `driveProvider`
- `startStreamActor`
- `streamCommandEnvelope`
- `handleToolInvocation`
- `handleExecResult`
- `handleExecControl`
- `publishCheckpoint`
- `CheckpointConversation`
- `snapshotCheckpointConversation`
- `appendConversationEntries`
- `OpenExec`
- `OpenQuery`
- `StartStream`
- `deriveConversationLoopState`
- `historyEntryToolCallID`
- `recordProviderUsage`
- `recordTurnUsage`

### provider / 模型适配层

当现象是 provider 400/500、thinking/reasoning、tool_call_id、OpenAI/Anthropic 请求形状、usage/cache 不对：

优先搜索：

- `StartStream`
- `StreamRequest`
- `ResolvedChannelID`
- `ResolvedChannelName`
- `ProviderModelID`
- `ThinkingEnabled`
- `buildAnthropicThinkingConfig`
- `normalizeAnthropicProviderMessages`
- `normalizeOpenAIProviderMessages`
- `normalizeOpenAIResponsesInput`
- `reasoning_content`
- `ReasoningContent`
- `ReasoningSignature`
- `RecordLLMRequest`
- `RecordLLMSummary`
- `http_error`
- `namespaceToolCallID`

## 快速判断规则

- 如果问题是“给你一个 id，让你先判断是什么 ID，再找日志”，先看 `history/<id>/state.json` 是否存在，再扫 `history/*/state.json`、`history/*/context.json` 和 `logs/app.log`。
- 如果问题是“模型输出语义不对”，先看 `context.json.items` 到 `ProjectPromptReplay()` 的投影，再看 provider request normalization。
- 如果问题是“provider 报 400/参数错误”，先看模型适配层请求构造、`state.latest_request_prefix`、`state.last_provider_call`、`logs/app.log`。
- 如果问题是“客户端没回某个工具结果 / pending 不收口”，先看 `cursor-always-local` 与 forwarder，同时核对同一 `turn_seq` 是否有 `tool_result` 或控制面错误 entry。
- 如果问题是“backend 重启后为什么 checkpoint 没法继续恢复 pending”，不要找磁盘 checkpoint；checkpoint 是 live state，重启后的事实源是 `state.json + context.json`。
- 如果问题是“为什么同一个 `modelID` 还能出现多个渠道”，先检查渠道 ID：规范化后 `baseURL + modelID + apiKey + displayName + openAIEndpoint` 的短 SHA-256；resolver 仍兼容 legacy `baseURL + modelID + apiKey + displayName`。
- 如果问题是“只想桥接到其他 LLM”，优先看模型桥接层，不要默认深入整套 local runtime。
- 如果问题是“已安装 app 行为和仓库代码不一致”，先核对实际运行 bundle，再做只读比对；不要 patch 客户端。

## 协议关键词

上行：

- `run_request`
- `exec_client_message`
- `exec_client_control_message`
- `interaction_response`

下行：

- `interaction_update`
- `exec_server_message`
- `exec_server_control_message`
- `interaction_query`
- `conversation_checkpoint_update`

如果只看到下行请求，没有对应上行结果或控制消息，优先排查：

- `exec_id`
- `id`
- `tool_call_id`
- `request_id`
- `model_call_id`
- pending 收口逻辑

如果用户给的是一个裸 id，不要直接把它当成 `request_id`。先同时查：

- `history/<id>/state.json`
- `history/*/state.json` 的 `current_request_id`、`latest_request_prefix`、`last_provider_call`
- `history/*/context.json` 的 `items[].request_id`、`items[].tool_call_id`、`items[].payload`
- `history/usage.json` 的 `event_index` / `recent_events`
- `logs/app.log`
