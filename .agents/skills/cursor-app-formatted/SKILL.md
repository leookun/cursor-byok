---
name: cursor-app-formatted
description: Use when extracting, formatting, refreshing, or investigating a read-only formatted snapshot of the installed Cursor.app bundle under .cursor-app-formatted; includes git-ignore rules, snapshot generation workflow, and the rule to inspect formatted code without patching either the snapshot code or the installed app.
---

# Cursor App Formatted Snapshot

Use this skill whenever a task involves reading, searching, formatting, refreshing, or relying on a formatted copy of the installed Cursor client bundle.

## Invariants

- Never modify `/Applications/Cursor.app`, any installed app bundle, signatures, or app copies.
- Never patch bundled code under `.cursor-app-formatted/` as a fix target. It is an ignored investigation snapshot only.
- If `.cursor-app-formatted/` is stale or wrong, regenerate it from the installed app instead of hand-editing its code.
- Fixes should land in this repository's real source code, scripts, or docs, not in formatted snapshot code.
- Keep `.cursor-app-formatted/` git-ignored. Do not stage or commit generated snapshot contents.

## Preferred Investigation Flow

1. If `.cursor-app-formatted/` exists, search and read that formatted snapshot first.
2. Use `/Applications/Cursor.app` only for read-only authenticity checks, hash comparison, or when the snapshot is missing or stale.
3. Prefer stable formatted paths for line references and control-flow reading:
   - `.cursor-app-formatted/extensions/cursor-always-local/dist/main.js`
   - `.cursor-app-formatted/extensions/cursor-agent-exec/dist/main.js`
   - `.cursor-app-formatted/extensions/cursor-agent-worker/dist/main.js`
   - `.cursor-app-formatted/out/vs/workbench/workbench.desktop.main.js`
   - `.cursor-app-formatted/out/vs/workbench/api/node/extensionHostProcess.js`
4. When investigating installed-client behavior, compare formatted findings back to original source hashes or original bundle content only as needed.

## Git Ignore Rule

Ensure `.gitignore` contains:

```gitignore
.cursor-app-formatted/
```

If the entry is missing and the user asked to create or refresh the snapshot, add it before generating the snapshot.

## Snapshot Generation Workflow

Run from the repository root. This workflow copies only from the installed app into the ignored snapshot, then formats the copy.

```bash
set -euo pipefail

SNAPSHOT=.cursor-app-formatted
SOURCE=/Applications/Cursor.app/Contents/Resources/app

rm -rf "$SNAPSHOT"
mkdir -p "$SNAPSHOT"

/usr/bin/ditto "$SOURCE/extensions" "$SNAPSHOT/extensions"
mkdir -p "$SNAPSHOT/out/vs/workbench/api/node"
/usr/bin/ditto "$SOURCE/out/vs/workbench/workbench.desktop.main.js" "$SNAPSHOT/out/vs/workbench/workbench.desktop.main.js"
/usr/bin/ditto "$SOURCE/out/vs/workbench/api/node/extensionHostProcess.js" "$SNAPSHOT/out/vs/workbench/api/node/extensionHostProcess.js"

/usr/bin/shasum -a 256 \
  "$SOURCE/out/vs/workbench/workbench.desktop.main.js" \
  "$SOURCE/out/vs/workbench/api/node/extensionHostProcess.js" \
  > "$SNAPSHOT/source-sha256.txt"

/usr/bin/find "$SOURCE/extensions" -type f \( -name '*.js' -o -name '*.json' -o -name '*.css' \) -print \
  | /usr/bin/sed "s#^$SOURCE/##" \
  | while IFS= read -r rel; do
      /usr/bin/shasum -a 256 "$SOURCE/$rel"
    done >> "$SNAPSHOT/source-sha256.txt"
```

Format large JS bundles with `js-beautify`; Prettier can OOM on very large Cursor bundles and also skips ignored paths unless forced.

```bash
find .cursor-app-formatted -type f \( -name '*.js' -o -name '*.mjs' -o -name '*.cjs' \) -size +1M -print \
  | while IFS= read -r file; do
      npx --yes js-beautify --type js --indent-size 2 --end-with-newline --replace --quiet "$file"
    done

find .cursor-app-formatted -type f \( -name '*.js' -o -name '*.mjs' -o -name '*.cjs' \) ! -size +1M -print \
  | while IFS= read -r file; do
      npx --yes js-beautify --type js --indent-size 2 --end-with-newline --replace --quiet "$file"
    done

EMPTY_IGNORE="$(mktemp)"
trap 'rm -f "$EMPTY_IGNORE"' EXIT
find .cursor-app-formatted -type f \( -name '*.json' -o -name '*.css' \) -print0 \
  | xargs -0 -n 25 npx --yes prettier --ignore-path "$EMPTY_IGNORE" --with-node-modules --write --log-level warn
```

Optionally add a small `.cursor-app-formatted/README.md` describing the source path, observed Cursor version, and that the snapshot is read-only.

## Validation

After generation, verify the snapshot is ignored and key files are readable:

```bash
git status --short --ignored | rg '\.cursor-app-formatted'
wc -l \
  .cursor-app-formatted/out/vs/workbench/workbench.desktop.main.js \
  .cursor-app-formatted/extensions/cursor-always-local/dist/main.js \
  .cursor-app-formatted/extensions/cursor-agent-exec/dist/main.js
```

Useful investigation check:

```bash
rg -n 'localMode|runLocalAgent|localProvider|BidiTransport|startYieldingInputsToTheServer' .cursor-app-formatted
```
