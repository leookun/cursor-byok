# 文件地图

## 已安装客户端 bundle

优先核对这些实际运行中的客户端文件：

- `/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js`
- `/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/api/node/extensionHostProcess.js`
- `/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-always-local/dist/main.js`
- `/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-always-local/dist/gitWorker.js`
- `/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-agent-exec/dist/main.js`
- `/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-agent-exec/dist/*.js`
- `/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-agent-worker/dist/main.js`

当前安装包里 `cursor-agent` 已拆成 split bundle；旧路径 `/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-agent/dist/main.js` 通常不存在。不要按旧路径下结论；需要先列出 `extensions/`，再确认实际存在的 `cursor-agent-exec`、`cursor-agent-worker`、`cursor-always-local` 及其 `dist/` 文件。

大致归属：

- `out/vs/workbench/workbench.desktop.main.js`：主 UI、agent window / titlebar、feature flag、用户可点击入口和只读/禁用态。
- `cursor-always-local/dist/main.js`：本地模式协议、`BidiAppend`、`RunSSE`、`AgentServerMessage` / `AgentClientMessage` 桥接。
- `cursor-agent-exec/dist/main.js` 与同目录数字 chunk：agent 执行侧、SDK/canvas runtime、工具执行、proto 消息定义与拆分 chunk。当前构建中 `411.js` 可能包含 agent 执行链路关键片段，但 chunk 编号不是稳定接口，先用 `dist/*.js` 搜索。
- `cursor-agent-worker/dist/main.js`：agent worker 侧后台逻辑。

用户机器上可能还存在其它 app 副本，例如：

- `~/Applications/Cursor Hooked.app`
- `/Applications/Cursor Patched.app`

不要假设哪一个在跑，先看进程路径。

## 当前 backend/store 与 history

当前用户机器上的固定助手目录：

- `~/.cursor-local-assistant-v2/`

重点看：

- `~/.cursor-local-assistant-v2/config.yaml`
- `~/.cursor-local-assistant-v2/data/ca.crt`
- `~/.cursor-local-assistant-v2/data/ads/`
- `~/.cursor-local-assistant-v2/history/usage.json`
- `~/.cursor-local-assistant-v2/history/<conversationId>/state.json`
- `~/.cursor-local-assistant-v2/history/<conversationId>/context.json`
- `~/.cursor-local-assistant-v2/history/<conversationId>/conversation.lock`
- `~/.cursor-local-assistant-v2/logs/app.log`

其中：

- `state.json` 是会话元数据、loop 状态、latest provider/request prefix、当前 todos/plans、token/compaction 状态。
- `context.json.items` 是 append-only 语义历史，也是 prompt replay 的事实源。
- `usage.json` 是全局 provider call / turn usage 聚合。
- `conversation.lock` 是会话级文件锁。
- checkpoint 只表示同一 backend 进程内的 live state，不是持久化恢复事实源。
- legacy artifacts：`conversation.json`、`entries.jsonl`、`turns/`、`request.json`、`summary.json`、`sse.jsonl`、`replay.json`、`runtime.json`、`latest.json`、数字 turn 目录，当前会被 history maintenance 清理。

这些内容的生成入口主要在：

- `internal/appdata/paths.go`
- `internal/backend/host.go`
- `internal/backend/README.md`
- `internal/backend/forwarder/file_store.go`
- `internal/backend/forwarder/history_maintenance.go`
- `internal/backend/forwarder/usage_store.go`
- `internal/backend/forwarder/token_usage.go`
- `internal/backend/forwarder/artifacts.go`

## 本仓库协议与本地模式实现

协议定义：

- `proto/agent_v1.proto`
- `proto/aiserver_v1.proto`
- `proto/from_extensions/agent_v1.proto`
- `proto/from_extensions/aiserver_v1.proto`

扩展快照与提取：

- `proto/extensions-cursor-app/cursor-always-local/package.json`
- `proto/extract_extensions_proto.sh`
- `proto/ext_tool/main.go`

本地后端入口：

- `internal/backend/host.go`
- `internal/backend/server/route.go`
- `internal/backend/server/policy.go`
- `internal/backend/server/local.go`
- `internal/backend/server/config/types.go`
- `internal/backend/server/config/manager.go`
- `internal/backend/server/config/resolver.go`

forwarder 主链路：

- `internal/backend/forwarder/module.go`
- `internal/backend/forwarder/service.go`
- `internal/backend/forwarder/actor.go`
- `internal/backend/forwarder/broker.go`
- `internal/backend/forwarder/events.go`
- `internal/backend/forwarder/compiler.go`
- `internal/backend/forwarder/projector.go`
- `internal/backend/forwarder/provider.go`
- `internal/backend/forwarder/checkpoint_memory.go`
- `internal/backend/forwarder/runtime_summary.go`

协议解码：

- `internal/backend/agent/protocol/inbound.go`

执行桥 / 交互桥：

- `internal/backend/agent/bridge/exec/bridge.go`
- `internal/backend/agent/bridge/interaction/bridge.go`

模型适配：

- `internal/backend/agent/model/router.go`
- `internal/backend/agent/model/openai.go`
- `internal/backend/agent/model/anthropic.go`
- `internal/backend/agent/model/artifacts.go`
- `internal/backend/agent/model/http_error.go`
- `internal/backend/agent/model/tool_call_id.go`
- `internal/modelchannel/identity.go`
- `internal/runtime/local_runtime.go`

Prompt / replay：

- `internal/backend/agent/prompt/engine.go`
- `internal/backend/agent/prompt/replay.go`
- `internal/backend/agent/prompt/content_parts.go`
- `internal/backend/forwarder/prompt_context.go`
- `internal/backend/forwarder/request_context.go`
- `internal/backend/forwarder/reminders.go`
- `internal/backend/forwarder/prompt_guard.go`

## 构建相关参考（只读）

仓库内已有 macOS 构建与签名相关文件，可用于理解产物结构或历史处理方式，但不要把它们当成修改已安装 Cursor 客户端的操作指南：

- `Taskfile.yml`
- `build/darwin/Taskfile.yml`
- `build/dmg-extras/提示损坏？点我.command`

重点看：

- `build/darwin/Taskfile.yml` 中的 `codesign:adhoc`
- `build/dmg-extras/提示损坏？点我.command` 中的 `xattr -cr`
