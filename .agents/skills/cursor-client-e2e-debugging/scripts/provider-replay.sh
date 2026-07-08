#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  REQUEST_LOG=<path/to/provider.jsonl> REQUEST_ID=<request-id> MODEL_CALL_ID=<model-call-id> \
    [BASE_URL=<provider-base-url>] [API_KEY=<provider-api-key>] [CONFIG_FILE=<config.yaml>] \
    [OUT_DIR=<output-dir>] [MAX_TIME=240] provider-replay.sh

Required:
  REQUEST_LOG     Path to history/<conversationId>/debug/provider.jsonl
  REQUEST_ID      Provider request_id to replay
  MODEL_CALL_ID   Provider model_call_id to replay

Optional:
  BASE_URL        Provider base URL. Falls back to GLM_BASE_URL or ANTHROPIC_BASE_URL.
  API_KEY         Provider API key. Falls back to ANTHROPIC_API_KEY or GLM_API_KEY.
  CONFIG_FILE     Defaults to ~/.cursor-local-assistant-v2/config.yaml.
  CHANNEL_NAME    Display name to read from config.yaml when BASE_URL/API_KEY is missing. Defaults to GLM.
  OUT_DIR         Output directory. Defaults to /tmp/cursor-provider-replay-<request-id>.
  MAX_TIME        curl max-time seconds. Defaults to 240.
  ENDPOINT_PATH   Provider path. Defaults to /v1/messages.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

REQUEST_LOG="${REQUEST_LOG:-}"
REQUEST_ID="${REQUEST_ID:-}"
MODEL_CALL_ID="${MODEL_CALL_ID:-}"
CONFIG_FILE="${CONFIG_FILE:-$HOME/.cursor-local-assistant-v2/config.yaml}"
CHANNEL_NAME="${CHANNEL_NAME:-GLM}"
BASE_URL="${BASE_URL:-${GLM_BASE_URL:-${ANTHROPIC_BASE_URL:-}}}"
API_KEY="${API_KEY:-${ANTHROPIC_API_KEY:-${GLM_API_KEY:-}}}"
ENDPOINT_PATH="${ENDPOINT_PATH:-/v1/messages}"
MAX_TIME="${MAX_TIME:-240}"
OUT_DIR="${OUT_DIR:-/tmp/cursor-provider-replay-${REQUEST_ID:-unknown}}"
BODY_FILE="$OUT_DIR/request.body.json"
RESP_FILE="$OUT_DIR/response.sse"
HEADER_FILE="$OUT_DIR/response.headers"
META_FILE="$OUT_DIR/replay.meta.json"

require_value() {
  local name="$1"
  local value="$2"
  if [[ -z "$value" ]]; then
    echo "缺少 $name。运行 --help 查看用法。" >&2
    exit 2
  fi
}

read_channel_config() {
  local field="$1"
  python3 - "$CONFIG_FILE" "$CHANNEL_NAME" "$field" <<'PY'
import sys
from pathlib import Path

config_path = Path(sys.argv[1]).expanduser()
channel_name = sys.argv[2]
field = sys.argv[3]
if not config_path.exists():
    raise SystemExit

lines = config_path.read_text(encoding="utf-8").splitlines()
in_channel = False
for line in lines:
    stripped = line.strip()
    if stripped.startswith("- displayName:"):
        in_channel = stripped.split(":", 1)[1].strip().strip('"') == channel_name
        continue
    if in_channel and stripped.startswith(field + ":"):
        print(stripped.split(":", 1)[1].strip().strip('"'))
        raise SystemExit
PY
}

require_value "REQUEST_LOG" "$REQUEST_LOG"
require_value "REQUEST_ID" "$REQUEST_ID"
require_value "MODEL_CALL_ID" "$MODEL_CALL_ID"

if [[ ! -f "$REQUEST_LOG" ]]; then
  echo "REQUEST_LOG 不存在: $REQUEST_LOG" >&2
  exit 2
fi

if [[ -z "$BASE_URL" ]]; then
  BASE_URL="$(read_channel_config baseURL || true)"
fi

if [[ -z "$API_KEY" ]]; then
  API_KEY="$(read_channel_config apiKey || true)"
fi

require_value "BASE_URL/GLM_BASE_URL/ANTHROPIC_BASE_URL 或 config[$CHANNEL_NAME].baseURL" "$BASE_URL"
require_value "API_KEY/ANTHROPIC_API_KEY/GLM_API_KEY 或 config[$CHANNEL_NAME].apiKey" "$API_KEY"

mkdir -p "$OUT_DIR"
: > "$HEADER_FILE"
: > "$RESP_FILE"

python3 - "$REQUEST_LOG" "$REQUEST_ID" "$MODEL_CALL_ID" "$BODY_FILE" "$META_FILE" <<'PY'
import json
import sys
from pathlib import Path

log_path = Path(sys.argv[1]).expanduser()
request_id = sys.argv[2]
model_call_id = sys.argv[3]
body_path = Path(sys.argv[4])
meta_path = Path(sys.argv[5])

body = None
meta = None
with log_path.open(encoding="utf-8") as f:
    for raw in f:
        if not raw.strip():
            continue
        row = json.loads(raw)
        if row.get("event") != "llm_request":
            continue
        if row.get("request_id") != request_id:
            continue
        if row.get("model_call_id") != model_call_id:
            continue
        payload = row.get("payload") or {}
        body = payload.get("body")
        meta = {
            "at": row.get("at"),
            "event": row.get("event"),
            "conversation_id": row.get("conversation_id"),
            "request_id": row.get("request_id"),
            "model_call_id": row.get("model_call_id"),
            "provider_log": str(log_path),
        }
        break

if body is None:
    raise SystemExit(f"未找到 llm_request: request_id={request_id} model_call_id={model_call_id}")

body_path.write_text(json.dumps(body, ensure_ascii=False, separators=(",", ":")), encoding="utf-8")
meta_path.write_text(json.dumps(meta, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
PY

url="${BASE_URL%/}${ENDPOINT_PATH}"

set +e
curl --no-buffer --silent --show-error \
  --connect-timeout 30 \
  --max-time "$MAX_TIME" \
  --request POST "$url" \
  --header "content-type: application/json" \
  --header "anthropic-version: 2023-06-01" \
  --header "User-Agent: claude-cli/1.0.25" \
  --header "x-api-key: $API_KEY" \
  --header "Authorization: Bearer $API_KEY" \
  --data-binary "@$BODY_FILE" \
  --dump-header "$HEADER_FILE" \
  --output "$RESP_FILE"
code=$?
set -e

echo "curl_exit_code=$code"
echo "body=$BODY_FILE"
echo "headers=$HEADER_FILE"
echo "response=$RESP_FILE"
echo "meta=$META_FILE"

echo "--- response headers ---"
if [[ -f "$HEADER_FILE" ]]; then
  python3 - "$HEADER_FILE" <<'PY'
from pathlib import Path
import sys
for line in Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace").splitlines()[:40]:
    print(line)
PY
fi

echo "--- response first 120 lines ---"
if [[ -f "$RESP_FILE" ]]; then
  python3 - "$RESP_FILE" <<'PY'
from pathlib import Path
import sys
for line in Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace").splitlines()[:120]:
    print(line)
PY
else
  echo "响应文件不存在。"
fi

exit "$code"