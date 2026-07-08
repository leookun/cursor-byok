---
name: cursor-debug-log
description: 当需要调查 Cursor 本地模式 debug/log 证据时使用：config.yaml 的 log 热加载、history/<conversationId>/debug JSONL 文件、Bidi 原始/解码记录、RunSSE 记录、runtime/provider debug 记录、debug 文件缺失原因，或解释这些 debug 文件如何生成与如何查询。
---

# Cursor Debug Log

使用这个技能来解释和检查本地 debug log 体系。目标是在不修改已安装 Cursor 客户端、不依赖旧版 legacy artifact 的前提下，还原一次请求附近发生了什么。

## 作用定位

debug log 是本地模式请求链路的可选证据层。它和模型可见历史是分开的：

- 用来回答“客户端到底发了什么”。
- 用来回答“后端解码后认为这是什么请求”。
- 用来回答“哪些字段被挂到了当前 active request 上”。
- 用来回答“最终 provider request body 是什么样”。
- 用来回答“RunSSE 实际给客户端发送了什么”。
- 不要把它当成 replay history、prompt 输入或状态事实源。

稳定事实源仍然是：

- `history/<conversationId>/state.json`
- `history/<conversationId>/context.json`
- `history/usage.json`
- `logs/app.log`

debug 文件是在这些事实源之外，补充原始或近原始链路证据。

## 固定路径

- 助手根目录：`~/.cursor-local-assistant-v2`
- 配置文件：`~/.cursor-local-assistant-v2/config.yaml`
- history 根目录：`~/.cursor-local-assistant-v2/history`
- app 日志：`~/.cursor-local-assistant-v2/logs/app.log`
- 会话 debug 目录：`history/<conversationId>/debug/`
- 孤儿 debug 目录：`history/_debug/orphan/<requestId>/`

通过配置开启 debug logging：

```yaml
log: true
```

当前实现会用轻量文件快照检查热加载 `config.yaml`。改完 `log` 后，预留大约 500ms，再期待下一次请求事件使用新值。旧二进制可能仍然需要重启。

## 文件如何生成

debug 层随着请求穿过后端边界逐步落盘：

1. `BidiAppend` 收到客户端上行数据。
   - 原始 hex 写入 `bidi.raw.jsonl`。
   - 解码后的 known-schema protobuf 与后端提取出的 intent 写入 `bidi.decoded.jsonl`。
2. forwarder 把解码结果转成 active runtime state。
   - stream/request 状态决策写入 `runtime.jsonl`。
3. provider pass 被准备并执行。
   - adapter 前的请求摘要、`model_call_id`、`provider_pass` 等写入 `provider.jsonl`。
   - provider artifact callback 追加最终 request/summary payload 到 `provider.jsonl`。
4. `RunSSE` 把后端输出流式发送给客户端。
   - 已发送消息、终态事件、发送错误、断连和 heartbeat 写入 `runsse.jsonl`。

如果某条消息到达时后端还不知道 `conversationId`，早期事件可能写到 `_debug/orphan/<requestId>/`。后续一旦知道 `conversationId`，新事件应进入 `history/<conversationId>/debug/`。还原早期或乱序请求时，两处都要查。

## Debug 文件含义

`bidi.raw.jsonl`

- 方向：客户端到后端。
- 包含 `request_id`、可选 `conversation_id`、`append_seqno`、`status`、原始 `data_hex`。
- 当需要精确确认客户端上传字节时先看它。

`bidi.decoded.jsonl`

- 方向：客户端到后端，protobuf 解码后。
- 当前 schema v2 包含完整的 known-schema `AgentClientMessage` protojson：`message`。
- 同时包含后端从上行包提取出的 intent：`intent`，其中会展开相关 proto 子对象，例如 `client_message`、`user_message`、`request_context`、`conversation_state`、exec/interaction/kv 回包等。
- 还包含 `message_case`、`requested_model`、`conversation_action` 等检索索引；这些索引只方便搜索，不是完整证据本体。
- 当需要确认后端如何理解客户端请求时看它。若要证明客户端原始上传字节，仍以 `bidi.raw.jsonl` 为准。
- 旧二进制或旧日志可能只有 schema v1 摘要，未必展开 `message` 和 `intent` 里的完整字段。

`runtime.jsonl`

- 方向：后端内部 runtime。
- 包含状态流转，以及挂到 active stream/request 上的字段。
- 当需要把 decoded input 和后续 provider 行为串起来时看它。

`provider.jsonl`

- 方向：后端到 provider adapter/provider。
- 包含 provider pass 元数据、`model_call_id`、request knobs、最终 provider request artifact、provider summary artifact。
- 当最终出站 provider body 或 provider summary 是关键证据时看它。

`runsse.jsonl`

- 方向：后端到客户端。
- 包含解码后的 `AgentServerMessage` 发送、终态事件、发送错误、断连和 heartbeat。
- 用来检查后端尝试返回给客户端的内容。它是解码后的消息证据，不是原始 HTTP/SSE framing。

## 查询流程

1. 先判断 id 类型。
   - 先查 `history/<id>/state.json`，确认它是不是 `conversationId`。
   - 再在 `history/*/{state.json,context.json}`、`history/usage.json`、`logs/app.log` 里搜索 request/model-call/tool id。
2. 拿到 `conversationId` 后，列出 debug 目录。
   - `ls -la "$HOME/.cursor-local-assistant-v2/history/<conversationId>/debug"`
3. 如果 debug 目录不存在，确认请求发生时 debug 是否已开启。
   - 读取 `config.yaml`。
   - 对比 `config.yaml`、`state.json`、`context.json` 的 mtime。
   - 搜索 `logs/app.log` 里的 config hot reload 或 provider start 记录。
4. 按时间顺序读 JSONL，并用这些字段串联：
   - `request_id`
   - `conversation_id`
   - `model_call_id`
   - `provider_pass`
   - `append_seqno`
   - event timestamp
5. 最终回复只总结结论所需字段。不要粘贴 secret、API key、完整 provider body 或大段原始 payload。

常用命令：

```bash
ROOT="$HOME/.cursor-local-assistant-v2"
REQ="<requestId>"
CONV="<conversationId>"

rg -n "$REQ" "$ROOT/history" "$ROOT/logs/app.log"
find "$ROOT/history" -path "*/debug/*" -type f | sort
rg -n "$REQ|model_call_id|provider_request_prepared|llm_request" "$ROOT/history/$CONV/debug"
```

紧凑查看 JSONL：

```bash
jq -c 'select(.request_id == "<requestId>")' "$ROOT/history/$CONV/debug/provider.jsonl"
jq -c 'select(.request_id == "<requestId>") | {append_seqno, message_case, conversation_action, message, intent}' "$ROOT/history/$CONV/debug/bidi.decoded.jsonl"
```

## 证据怎么用

根据问题选择对应文件：

- 客户端原始上行问题：先看 `bidi.raw.jsonl`。这是精确原始包证据。
- 客户端 known-schema 字段问题：看 `bidi.decoded.jsonl` 的 `message`。例如 `user_message.message_id`、selected image、conversation state bytes 等字段是否在解码结果里。
- 后端如何理解请求：看 `bidi.decoded.jsonl` 的 `intent`，再接 `runtime.jsonl`。
- provider request 问题：看 `provider.jsonl`，尤其是 `llm_request`。
- UI/流式输出问题：看 `runsse.jsonl`。
- 请求状态问题：先看 `state.json`、`context.json`、`usage.json`，再用 debug 文件补证。
- debug 缺失问题：看 `config.yaml`、mtime、app log、orphan debug 目录。

runtime model parameters，例如 thinking strength，只是 provider request 证据的一类例子：

- `bidi.raw.jsonl` 说明客户端原始上传了什么。
- `bidi.decoded.jsonl.message` 说明上行包按当前 known schema 解码出了什么。
- `bidi.decoded.jsonl.intent` 说明后端从 decoded input 里提取并准备使用了什么。
- `runtime.jsonl` 说明后端把什么挂到了请求状态上。
- `provider.jsonl` 说明最终为 provider 准备了什么。

只有普通 history 时不要过度断言。例如 `context.json` 里的 `reasoning_content` 能说明产生过 reasoning 文本，但不能单独证明是哪一个 runtime parameter value 导致的。

注意证据边界：

- `bidi.decoded.jsonl` 使用当前已知 proto schema 做解码。未知字段或原始 framing 差异不能靠 decoded 证明，必须回到 `bidi.raw.jsonl`。
- `context.json` 仍是持久化历史事实源；debug 文件只能证明某次请求链路附近发生过什么。
- `provider.jsonl` 的 provider body 和 `bidi.raw.jsonl` / `bidi.decoded.jsonl` 都可能很大，回复用户时只摘必要字段，不粘贴完整图片、完整 body 或 secret。

## Debug 文件缺失

如果某个 request 没有 debug 文件，要明确说明“没有直接 debug 证据”。常见原因：

- 请求发生时 `log: false`。
- 正在运行的二进制版本早于 debug logging 或 hot reload 实现。
- 事件发生时还没有解析到 conversation id，记录在 `_debug/orphan/<requestId>/`。
- 请求在开启 `log` 前已经完成。
- 写文件失败；如果该版本有相关记录，app log 里可能有 warning。

debug 证据缺失时，回退到 `state.json`、`context.json`、`usage.json`、`logs/app.log`，并把结论标成推断，而不是直接证明。
