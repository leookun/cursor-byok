#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SNAPSHOT_DEFAULT="$SCRIPT_DIR/extensions-cursor-app/cursor-always-local"
INSTALLED_CURSOR_DEFAULT="/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-always-local/dist/main.js"
LATEST_EXT_DIR="$(
  find "$SCRIPT_DIR" -maxdepth 1 -type d -name 'extensions-*' 2>/dev/null \
    | sort -V \
    | tail -n 1
)"
if [[ -f "$SNAPSHOT_DEFAULT/dist/main.js" ]]; then
  INPUT_DEFAULT="$SNAPSHOT_DEFAULT"
elif [[ -f "$INSTALLED_CURSOR_DEFAULT" ]]; then
  INPUT_DEFAULT="$INSTALLED_CURSOR_DEFAULT"
elif [[ -n "$LATEST_EXT_DIR" ]]; then
  INPUT_DEFAULT="$LATEST_EXT_DIR"
else
  INPUT_DEFAULT="$SCRIPT_DIR/extensions-2.6.19"
fi
OUTPUT_DEFAULT="$SCRIPT_DIR/from_extensions"

INPUT_PATH="${1:-$INPUT_DEFAULT}"
OUTPUT_DIR="${2:-$OUTPUT_DEFAULT}"

# Resolve input: accept either a single JS file or an extensions root directory.
if [[ -d "$INPUT_PATH" ]]; then
  CANDIDATES=(
    "$INPUT_PATH/cursor-always-local/dist/main.js"
    "$INPUT_PATH/cursor-retrieval/dist/main.js"
    "$INPUT_PATH/dist/main.js"
  )
  FOUND_CANDIDATE=""
  for CANDIDATE in "${CANDIDATES[@]}"; do
    if [[ -f "$CANDIDATE" ]]; then
      FOUND_CANDIDATE="$CANDIDATE"
      break
    fi
  done
  if [[ -n "$FOUND_CANDIDATE" ]]; then
    INPUT_PATH="$FOUND_CANDIDATE"
  else
    mapfile -t JS_FILES < <(find "$INPUT_PATH" -type f -path "*/dist/main.js" | sort)
    if [[ ${#JS_FILES[@]} -eq 1 ]]; then
      INPUT_PATH="${JS_FILES[0]}"
    elif [[ ${#JS_FILES[@]} -eq 0 ]]; then
      echo "No dist/main.js found under: $INPUT_PATH" >&2
      exit 1
    else
      echo "Multiple dist/main.js files found under: $INPUT_PATH" >&2
      printf ' - %s\n' "${JS_FILES[@]}" >&2
      echo "Please pass a concrete input JS file as the first argument." >&2
      exit 1
    fi
  fi
fi

if [[ ! -f "$INPUT_PATH" ]]; then
  echo "Input JS not found: $INPUT_PATH" >&2
  echo "Usage: $0 [input-js-file-or-extensions-dir] [output-dir]" >&2
  exit 1
fi

rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

go run "$SCRIPT_DIR/ext_tool" \
  -input "$INPUT_PATH" \
  -output "$OUTPUT_DIR" \
  -skip-format \
  -strict
