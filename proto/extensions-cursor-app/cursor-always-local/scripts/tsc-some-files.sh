#!/bin/bash
include_files=()
for f in "$@"; do
  include_files+=("${f#$PWD/}")
done
node_modules/.bin/tsc --noEmit -p . | (
  status=0
  show_continuation=false
  while IFS='' read -r line; do
    case "$line" in
    (' '*)
      if $show_continuation; then
        echo "$line" >&2
      fi
      ;;
    (*)
      file="${line%%(*}"
      if [[ " ${include_files[@]} " =~ " ${file} " ]]; then
        show_continuation=true
        echo "$line" >&2
        status=1
      else
        show_continuation=false
      fi
      ;;
    esac
  done
  exit $status
)