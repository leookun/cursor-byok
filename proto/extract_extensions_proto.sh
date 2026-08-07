#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALLED_CURSOR_DEFAULT="/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-always-local/dist/main.js"
INPUT_DEFAULT="$INSTALLED_CURSOR_DEFAULT"
OUTPUT_DEFAULT="$SCRIPT_DIR/from_extensions"

INPUT_PATH="${1:-$INPUT_DEFAULT}"
OUTPUT_DIR="${2:-$OUTPUT_DEFAULT}"

canonicalize_path() {
  local path="$1"
  local parent
  local base
  if [[ -d "$path" ]]; then
    (cd "$path" && pwd -P)
    return
  fi
  parent="$(dirname "$path")"
  base="$(basename "$path")"
  if [[ ! -d "$parent" ]]; then
    echo "Parent directory does not exist: $parent" >&2
    return 1
  fi
  printf '%s/%s\n' "$(cd "$parent" && pwd -P)" "$base"
}

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
    JS_FILES=()
    while IFS= read -r JS_FILE; do
      JS_FILES+=("$JS_FILE")
    done < <(find "$INPUT_PATH" -type f -path "*/dist/main.js" | sort)
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
  echo "Install/update Cursor, or pass an explicit input bundle:" >&2
  echo "  $0 /path/to/cursor-always-local/dist/main.js [output-dir]" >&2
  exit 1
fi

INPUT_PATH="$(canonicalize_path "$INPUT_PATH")"
OUTPUT_DIR="$(canonicalize_path "$OUTPUT_DIR")"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd -P)"
CURRENT_DIR="$(pwd -P)"

case "$OUTPUT_DIR" in
  "/"|"$HOME"|"$REPO_ROOT"|"$SCRIPT_DIR"|"$CURRENT_DIR")
    echo "Refusing unsafe output directory: $OUTPUT_DIR" >&2
    exit 1
    ;;
esac
case "$INPUT_PATH" in
  "$OUTPUT_DIR"|"$OUTPUT_DIR"/*)
    echo "Refusing output directory that contains the input bundle: $OUTPUT_DIR" >&2
    exit 1
    ;;
esac

OUTPUT_PARENT="$(dirname "$OUTPUT_DIR")"
OUTPUT_BASENAME="$(basename "$OUTPUT_DIR")"
TEMP_DIR="$(mktemp -d "$OUTPUT_PARENT/.${OUTPUT_BASENAME}.tmp.XXXXXX")"
BACKUP_DIR=""

cleanup() {
  if [[ -n "$TEMP_DIR" && -d "$TEMP_DIR" ]]; then
    rm -rf "$TEMP_DIR"
  fi
  if [[ -n "$BACKUP_DIR" && -e "$BACKUP_DIR" ]]; then
    if [[ ! -e "$OUTPUT_DIR" ]]; then
      mv "$BACKUP_DIR" "$OUTPUT_DIR"
    else
      rm -rf "$BACKUP_DIR"
    fi
  fi
}
trap cleanup EXIT

go run "$SCRIPT_DIR/ext_tool" \
  -input "$INPUT_PATH" \
  -output "$TEMP_DIR" \
  -skip-format \
  -strict

if [[ -e "$OUTPUT_DIR" ]]; then
  BACKUP_DIR="$(mktemp -d "$OUTPUT_PARENT/.${OUTPUT_BASENAME}.backup.XXXXXX")"
  rmdir "$BACKUP_DIR"
  mv "$OUTPUT_DIR" "$BACKUP_DIR"
fi
mv "$TEMP_DIR" "$OUTPUT_DIR"
TEMP_DIR=""
if [[ -n "$BACKUP_DIR" ]]; then
  rm -rf "$BACKUP_DIR"
  BACKUP_DIR=""
fi
