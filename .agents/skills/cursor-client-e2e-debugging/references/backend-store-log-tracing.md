# backend/store 日志反查

当用户只给一个 id，或明确让你从本地记录里反查一次请求、会话、模型调用、工具调用或 provider 错误时，优先读这份参考。

## 当前固定路径

当前用户机器上的固定助手根目录：

- `~/.cursor-local-assistant-v2`

当前最关键的是三类内容：

- `history/<conversationId>/state.json`
  - 会话元数据与当前状态。
  - 重点字段：`request_id` `conversation_id`、`root_conversation_id`、`parent_conversation_id`、`parent_tool_call_id`、`mode`、`current_loop_status`、`current_request_id`、`current_turn_seq`、`context_version`、`next_turn_seq`、`next_entry_seq`、`latest_request_prefix`、`last_provider_call`、`current_todos`、`current_plans`、token / compaction 字段。
- `history/<conversationId>/context.json`
  - append-only 语义历史。
  - 重点字段：`version`、`items[]`。
  - `items[]` 中每个 entry 通常有 `seq`、`turn_seq`、`request_id`、`role`、`kind`、`tool_call_id`、`parent_tool_call_id`、`payload`、`created_at`。
- `history/usage.json`
  - provider call 与 turn usage 聚合。
  - 重点字段：`totals`、`daily`、`recent_events`、`event_index`。

`logs/app.log` 是运行日志；它只用于补充运行时证据，不是会话事实源。

当前实现不再支持：

- DB-backed store / searchable conversation memory
- HTTP/protocol trace debug UI
- `data/data.sqlite` / `protocol_traces`
- `history/<conversationId>/conversation.json`
- `history/<conversationId>/turns/<n>/request.json|sse.jsonl|summary.json`
- 根目录或会话目录下的旧 `latest.json`、`summary.json`、`replay.json`、`runtime.json`、`request.json`、`recovery.json`、`entries.jsonl`、数字 turn 目录

这些旧产物会被 `internal/backend/forwarder/history_maintenance.go` 清理。不要把它们当成当前事实源。

## 用户发来一个 id 时的固定步骤

### 1. 先判断 id 类型

不要先假设它是 `requestId`。按这个顺序缩小范围：

1. 是否是 `conversationId`
   - 检查 `history/<id>/state.json` 和 `history/<id>/context.json` 是否存在。
2. 是否是 `requestId`
   - 在 `history/*/state.json` 中查 `current_request_id`、`latest_request_prefix.request_id`、`last_provider_call.request_id`。
   - 在 `history/*/context.json` 的 `items[].request_id` 中查。
   - 在 `logs/app.log` 中查。
3. 是否是 `modelCallId`
   - 在 `state.json` 中查 `latest_request_prefix.model_call_id`、`last_provider_call.model_call_id`。
   - 在 `context.json.items[].payload` 中查 `model_call_id`。
   - 在 `logs/app.log` 中查 `model_call_id=<id>`。
4. 是否是 `toolCallId` / `exec_id`
   - 在 `context.json.items[].tool_call_id`、`items[].payload` 中查。
   - 在协议/工具相关日志中查。

可以用本地脚本或 `rg` 做只读反查。不要再用 SQLite 查询模板。

### 2. 拿到 `conversationId` 后看两份事实源

```bash
HISTORY_ROOT="$HOME/.cursor-local-assistant-v2/history"
CONV_ID="<conversation-id>"

ls -la "$HISTORY_ROOT/$CONV_ID"
```

重点检查：

- `state.json`
  - `current_loop_status`：`idle`、`running`、`waiting_tool`、`completed`、`canceled`、`provider_error`、`failed`
  - `current_request_id`、`current_turn_seq`
  - `latest_request_prefix`：最近一次 provider 请求的 provider/model/openai_endpoint/model_call_id/prompt token 摘要
  - `last_provider_call`：最近 provider 状态与错误文本
  - `next_entry_seq`、`next_turn_seq`、`context_version`
  - `current_todos`、`current_plans`
- `context.json`
  - `version` 是否与 `state.context_version` 对齐
  - `items[]` 是否按 `seq` 稳定递增
  - 同一 `turn_seq` 下是否有预期的 user/request_context/prompt_context/assistant/tool_result/metadata entries
  - 是否有重复、缺失或顺序异常
- `usage.json`
  - 通过 `event_index` 或 `recent_events` 查 request/model-call 相关 usage
  - `totals.cache_read_tokens / (totals.cache_read_tokens + totals.input_tokens)` 可粗略看 cache hit

### 3. Provider 调用证据现在在哪里

当前 provider artifact recorder 的行为：

- `RecordLLMRequest(...)`
  - 只缓存当前 provider call 的请求摘要。
  - 如果 payload 可解析 provider/model/openai_endpoint，会更新 `state.latest_request_prefix`。
  - 不再写 `request.json`。
- `AppendLLMResponseChunk(...)`
  - 当前是 no-op。
  - 不再写 `sse.jsonl`。
- `RecordLLMSummary(...)`
  - 只补齐当前 provider call summary，并更新 `state.latest_request_prefix.prompt_tokens_total`。
  - usage 聚合写入 `history/usage.json`。
  - 不再写 `summary.json`。

所以 provider 错误排查应优先看：

- `state.last_provider_call`
- `state.latest_request_prefix`
- `context.json.items` 里的 `metadata/provider_error/turn_completed` 等 payload
- `history/usage.json`
- `logs/app.log`
- `internal/backend/agent/model/openai.go` / `anthropic.go` 的请求构造和错误解析

## 这些文件是怎么生成的

### 根路径

- `internal/appdata/paths.go`
  - `RootDir()` 固定为 `~/.cursor-local-assistant-v2`
  - `HistoryRootPath()` 为 `~/.cursor-local-assistant-v2/history`
  - `UsageFilePath()` 为 `~/.cursor-local-assistant-v2/history/usage.json`
  - `LogsRootPath()` 为 `~/.cursor-local-assistant-v2/logs`

### `state.json + context.json`

来源链路：

- `internal/backend/forwarder/file_store.go`
  - `CreateConversation`
  - `LoadConversation`
  - `AppendEntries`
  - `SaveConversationWithEntries`
  - `UpdateConversationMeta`
  - `ReplaceEntries`
- `internal/backend/forwarder/service.go`
  - `handleRunIntent` 开始新 loop / turn
  - `appendConversationEntries` 追加语义事件
- `internal/backend/forwarder/projector.go`
  - `ProjectPromptReplay()` 把 `context.json.items` 投影为 provider messages

稳定结论：

- `state.json` 是当前状态和可变元数据。
- `context.json.items` 是 replayable 语义历史。
- 发给 LLM 的历史由 projector 从 `context.json.items` 投影，不是从 provider artifacts 重放。
- `state.json.entries` 只是内存结构 `ConversationFile` 的字段；落盘时可投影历史在 `context.json.items`。

### `usage.json`

来源链路：

- `internal/backend/forwarder/usage_store.go`
  - `UsageFileStore.UpsertEvent`
  - `UsageFileStore.LookupEvent`
- `internal/backend/forwarder/token_usage.go`
- `internal/historymetrics/`

稳定结论：

- `usage.json` 是全局 usage 聚合，不属于单个 conversation 的语义历史。
- `recent_events` 只保留最近有限数量事件；长期总量看 `totals` / `daily`。

### legacy 清理

来源链路：

- `internal/backend/forwarder/history_maintenance.go`

稳定结论：

- `turns/`、`conversation.json`、`entries.jsonl`、`request.json`、`summary.json` 等都是 legacy artifact。
- 启动后的 history maintenance 会清理这些旧产物。

## 快速判断规则

- 用户只发一个 id 时，先查 `history/<id>/state.json` 是否存在；不存在再扫 `state.json/context.json/logs`。
- 请求失败时，先看 `state.last_provider_call`、`context.json.items` 的错误 metadata、`logs/app.log`；不要找 `turns/<n>/summary.json`。
- pending / 工具不收口时，先看同一 `turn_seq` 的 tool call 和 tool result entries，再对照协议上行 `exec_client_message` / `exec_client_control_message` / `interaction_response`。
- prefix cache 异常时，先看 `context.json.items` 的稳定追加顺序和 `usage.json` 的 cache token 字段。
- 如果 history 与日志冲突，优先相信当前仍在更新的 `state.json/context.json`，再用日志解释运行时经过了哪条路径。
