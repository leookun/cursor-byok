# Local Changes

> Record local changes by date, newest first. Each change uses a `## YYYY-MM-DD` heading and a separate `###` entry.

## 2026-08-10

### Changed: Sync upstream and expand the conversation forwarder

### Changes

Merged `upstream/main` and integrated changes for checkpoint/projection, append-only compaction, imported history, the state database, provider adapters, upstream mocks, and UI/localization/build configuration.

The forwarder now provides improved conversation recovery, transcript replay, and terminal state management. It also adds handling for shell streaming, cancellation, terminal events, interrupted output, and continuation/checkpoint cases while the Agent is running.

The MCP tool registry is normalized by server identifier and tool name, and can resolve aliases from server names for compatibility with descriptors provided by Cursor.

### Regression coverage

- Added tests for checkpoint blobs, append-only compaction, imported history, and the state database.
- Added tests for shell stream deltas, interrupted output, terminal lifecycle, and provider thinking/reasoning carriers.
- Updated tests for the MCP registry, model streaming, and upstream mocks.

### Verification

The following commands completed successfully:

```powershell
go build ./...
go test ./...
yarn build
go test ./internal/backend/forwarder
```

`git diff --cached --check` also passed. `go vet ./...` continues to report a baseline error/warning at `internal/backend/agent/bridge/interaction/bridge.go:328` because `json.Marshal` copies a lock value in `SwitchModeArgs`; no new errors from the merge were found. E2E testing with the Cursor client and a real provider has not been run.

## 2026-08-05

### Changed: Sync upstream v0.0.45 and use inline checkpoints

### Changes

Merged `upstream/main` v0.0.45. The conversation checkpoint mechanism was changed from blob-backed turns to inline turns from upstream to apply the fix for disappearing conversations.

Blob synchronization, imported blobs, and the checkpoint blob timeout were removed under the new contract. Conversation state import and replay now decode inline turns/steps directly from the checkpoint.

### Preserved fork behavior

- Shared reasoning from a provider pass is persisted in only one tool call; `tool_result` retains reasoning as a fallback only when the corresponding `tool_call` is missing.
- Late partial/delta updates are ignored after a tool has completed.
- Hidden `PatchEdit` and `Write` operations continue to recover when the transport closes before the terminal result, while also completing the operation when the turn is canceled.
- The terminal stream does not emit duplicate terminal events or allow subscriptions after it has ended.

### Upstream updates

- Bumped the release version to `0.0.45`.
- Added the `CURSOR_BYOK_DISABLE_WEBVIEW_SANDBOX` environment variable to disable the WebView sandbox for affected VDI environments.

### Verification

```powershell
go test ./... -count=1 -timeout 180s
```

The command completed successfully. `go vet ./...` continues to report two existing warnings at `internal/backend/agent/bridge/interaction/bridge.go`; they are unrelated to this merge.

## 2026-08-03

### Fixed: Reasoning/progress repeated before multiple tools in one Agent turn

### Symptom

After syncing upstream, a reasoning/progress block could appear repeatedly before each tool in the same Agent turn. This was especially visible when the provider returned multiple consecutive tool calls.

### Cause

Provider reasoning was accumulated at the provider-pass level but was previously copied into every `ToolLikeCompleted`. As a result, the same reasoning data was persisted multiple times in `tool_call` and was also written to `tool_result` when the tool completed.

The new Cursor transcript sync feature from upstream renders persisted `tool_call` entries directly, making this latent issue visible as repeated progress/tool rows in the UI.

### Fix

The reasoning buffer now has explicit ownership: it is consumed exactly once by the first tool in the provider pass. If the provider also emits text, the reasoning is attached to `assistant_text` and is not copied to the tool.

`tool_result` retains `reasoning_content` only when the corresponding `tool_call` is missing, preserving replay support for older or incomplete history.

### Verification

Added regression tests covering shared reasoning across multiple tools, single-render transcript reasoning, and `tool_result` fallback only when the corresponding `tool_call` is missing:

```text
TestTakeProviderOutputForToolConsumesReasoningOnce
TestToolResultReasoningFallbackOnlyPersistsWhenToolCallIsMissing
TestProjectorKeepsSharedReasoningOnOnlyOneOfMultipleToolCalls
TestProjectCursorTranscriptJSONLKeepsSharedReasoningOnOnlyOneTool
```

Test command:

```powershell
go test ./internal/backend/forwarder -count=1 -timeout 60s
```

### Fixed: Cursor Agent transcript repeated or lost content while responding

### Symptom

While the Agent was streaming a response or calling a tool, the Cursor UI could display repeated progress/file rows. Some content that had already appeared could also disappear or be replaced by an older snapshot.

### Cause

Local conversation history synchronization to the Cursor transcript wrote the entire transcript after every history change. This happened concurrently with the Cursor client appending stream events for the same turn, causing two flows to update the transcript:

- The Cursor client rendered stream events directly.
- The backend re-projected history and overwrote the transcript file.

An intermediate history snapshot might not contain all of the latest events, causing content to repeat or disappear from the UI.

### Fix

Transcript sync is now deferred while the Agent turn is active (`running`, `waiting_tool`, or `checkpointing`). The backend writes the transcript only after the turn reaches a terminal state such as `turn_completed`, `failed`, `provider_error`, or `canceled`.

The terminal snapshot also includes `turn_ended`, so the completed transcript matches the final state of the chat turn.

### Verification

Added a regression test confirming that the transcript is not created during an active turn and is written only after `turn_completed`:

```text
TestConversationFileStoreDefersCursorTranscriptSyncUntilTurnEnds
```

Test command:

```powershell
go test ./internal/backend/forwarder -run "TestConversationFileStore(DefersCursorTranscriptSyncUntilTurnEnds|SyncsCursorTranscript|BackfillsTranscriptOnStartup)" -count=1 -timeout 30s
```
