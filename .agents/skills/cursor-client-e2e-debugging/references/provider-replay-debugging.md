# Provider replay / debug

当现象是 provider 返回错误、SSE 里只有 `event: error`、需要确认最终出站 provider body 是否能被独立复现时，优先读这份参考。

这套流程只用于还原“后端最终发给 provider 的请求形状”和“provider 对该请求的真实响应”。它不是语义 history，也不是客户端输入事实源。

## 证据边界

- `history/<conversationId>/debug/provider.jsonl`
  - 最接近 provider 出站边界。
  - `event=llm_request` 的 `payload.body` 是最终 provider request body。
  - 用它做 curl replay，判断问题是否已经出现在出站请求形状。
- `history/<conversationId>/state.json`
  - 当前状态和最近 provider 摘要。
  - 重点看 `latest_request_prefix`、`last_provider_call`。
- `history/<conversationId>/context.json`
  - replayable 语义历史。
  - 用来解释为什么会形成这次 prompt，不用来直接重放 provider HTTP 请求。
- `history/<conversationId>/debug/bidi.raw.jsonl`
  - 客户端原始上行字节证据。
- `history/<conversationId>/debug/bidi.decoded.jsonl`
  - 当前 known-schema 解码后的客户端上行证据。
- `history/<conversationId>/debug/runtime.jsonl`
  - 后端把哪些字段挂到 active request / stream 上。
- `history/<conversationId>/debug/runsse.jsonl`
  - 后端尝试发回客户端的消息。

不要把这些证据混用：provider replay 只能证明最终 provider HTTP 请求与响应，不能单独证明客户端原始上传了什么，也不能替代 `context.json` 的语义历史。

## 前置条件

- 请求发生时 `config.yaml` 中 `log: true`，或已有 `history/<conversationId>/debug/provider.jsonl`。
- 已经通过 id 反查拿到：
  - `conversationId`
  - `requestId`
  - `modelCallId`
- 已经确认要测的是 Anthropic-compatible `/v1/messages` 请求。

如果没有 debug 文件，先回到 `state.json`、`context.json`、`usage.json`、`logs/app.log` 做推断，并明确“没有直接 provider body 证据”。

## 最小流程

1. 定位 provider debug 文件：

```bash
ROOT="$HOME/.cursor-local-assistant-v2"
CONV="<conversationId>"
REQ="<requestId>"
MODEL_CALL="<modelCallId>"
REQUEST_LOG="$ROOT/history/$CONV/debug/provider.jsonl"
```

2. 确认 `llm_request` 存在：

```bash
jq -c --arg req "$REQ" --arg mc "$MODEL_CALL" '
  select(.event == "llm_request" and .request_id == $req and .model_call_id == $mc)
  | {at, conversation_id, request_id, model_call_id, provider: .payload.provider, model: .payload.body.model}
' "$REQUEST_LOG"
```

3. 执行通用重放脚本：

```bash
REQUEST_LOG="$REQUEST_LOG" \
REQUEST_ID="$REQ" \
MODEL_CALL_ID="$MODEL_CALL" \
CHANNEL_NAME="GLM" \
OUT_DIR="/tmp/cursor-provider-replay-$REQ" \
.agents/skills/cursor-client-e2e-debugging/scripts/provider-replay.sh
```

也可以直接传入 provider 配置，避免读取 `config.yaml`：

```bash
REQUEST_LOG="$REQUEST_LOG" \
REQUEST_ID="$REQ" \
MODEL_CALL_ID="$MODEL_CALL" \
BASE_URL="<provider-base-url>" \
API_KEY="<provider-api-key>" \
OUT_DIR="/tmp/cursor-provider-replay-$REQ" \
.agents/skills/cursor-client-e2e-debugging/scripts/provider-replay.sh
```

4. 查看产物：

- `request.body.json`：抽取出的最终 provider body。
- `response.headers`：HTTP 响应头。
- `response.sse`：SSE 响应体。
- `replay.meta.json`：本次重放引用的 id 和 provider log 路径。

## 结果判断

- `curl_exit_code != 0`
  - 网络、TLS、超时、连接或本机 curl 问题。
  - 先看 stderr、`response.headers` 是否存在，再判断是否真的到达 provider。
- HTTP 非 2xx
  - provider 网关或鉴权层拒绝。
  - 优先看 `response.headers` 和 provider 错误体。
- HTTP 2xx 但 SSE 中有 `event: error`
  - provider 已接受连接，但认为请求参数不合法或模型侧拒绝。
  - 这种情况下重点比对 `request.body.json` 的消息结构、tool schema、thinking/reasoning 参数、model 名称和 endpoint 兼容性。
- SSE 正常流式输出
  - 原始 provider 请求形状基本可用。
  - 如果客户端仍失败，回到 `runsse.jsonl`、forwarder 状态机或客户端协议层继续查。

## 常见收敛方向

provider 参数错误时，优先检查：

- `model` 是否是目标 endpoint 支持的名称。
- `messages` 是否符合 Anthropic-compatible 形态。
- `system` 是否被目标 provider 支持，或需要改成 message。
- `tools` / `tool_choice` 是否符合目标 provider 方言。
- `thinking` / `reasoning` 字段是否被目标 provider 支持。
- 图片、文件、cache_control、metadata 等扩展字段是否超出 provider 兼容范围。
- `max_tokens`、`temperature`、`top_p`、`stop_sequences` 是否落在 provider 允许范围内。

## 敏感信息规则

- 不把 API key 写进 skill、reference、脚本默认值或提交内容。
- 回复用户时不要粘贴完整 API key；最多说明“已使用用户提供的 key / config 中的 key”。
- 不把完整 `request.body.json` 大段贴给用户；只摘和结论相关的字段形状。
- 不把一次性 `conversationId/requestId/modelCallId` 写进 skill 文档。
- 临时 replay 产物默认放 `/tmp`；如果需要保留，明确说明路径和原因。

## 脚本参数

`scripts/provider-replay.sh` 使用环境变量控制：

- 必填：
  - `REQUEST_LOG`
  - `REQUEST_ID`
  - `MODEL_CALL_ID`
- 可选：
  - `BASE_URL`
  - `API_KEY`
  - `GLM_BASE_URL`
  - `GLM_API_KEY`
  - `ANTHROPIC_BASE_URL`
  - `ANTHROPIC_API_KEY`
  - `CONFIG_FILE`，默认 `~/.cursor-local-assistant-v2/config.yaml`
  - `CHANNEL_NAME`，默认 `GLM`
  - `OUT_DIR`，默认 `/tmp/cursor-provider-replay-<requestId>`
  - `MAX_TIME`，默认 `240`
  - `ENDPOINT_PATH`，默认 `/v1/messages`

脚本输出四个稳定产物：

- `request.body.json`
- `response.headers`
- `response.sse`
- `replay.meta.json`