#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/cursor-proto-sync.XXXXXX")"

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

for PROTO_NAME in agent_v1.proto aiserver_v1.proto; do
  ROOT_PROTO="$SCRIPT_DIR/$PROTO_NAME"
  EXTRACTED_PROTO="$SCRIPT_DIR/from_extensions/$PROTO_NAME"
  if [[ ! -f "$ROOT_PROTO" || ! -f "$EXTRACTED_PROTO" ]]; then
    echo "Missing proto pair for $PROTO_NAME" >&2
    exit 1
  fi

  sed -E 's|^option go_package = ".*";$|option go_package = "__NORMALIZED__";|' "$ROOT_PROTO" > "$TEMP_DIR/root-$PROTO_NAME"
  sed -E 's|^option go_package = ".*";$|option go_package = "__NORMALIZED__";|' "$EXTRACTED_PROTO" > "$TEMP_DIR/extracted-$PROTO_NAME"
  if ! cmp -s "$TEMP_DIR/root-$PROTO_NAME" "$TEMP_DIR/extracted-$PROTO_NAME"; then
    echo "Proto snapshot is out of sync: $PROTO_NAME" >&2
    diff -u "$TEMP_DIR/root-$PROTO_NAME" "$TEMP_DIR/extracted-$PROTO_NAME" >&2 || true
    exit 1
  fi
done
