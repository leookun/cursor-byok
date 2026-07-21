# ADR-046: AOS Subagent Orchestration Architecture

## Status
Accepted

## Date
2026-07-18

## Context

AOS needs to execute member work using the existing Cursor-compatible request
path while preserving the established Forwarder, Exec Bridge, ModelAdapter, and
virtual-model-manager ownership boundaries. The protocol is private and the
repository can only assert behavior exercised by its source and focused tests.

The implementation must not rely on `.cursor/agents` files or a parent-session
mode that has not been validated by executable evidence.

## Decision

### 1. Keep the parent in normal Agent mode

The parent conversation remains in normal Agent mode. For `cursor_task`
execution, AOS internally emits the existing Cursor-native `Task` tool call;
the Exec Bridge creates the corresponding `SubagentArgs`. AOS does not require
or fabricate a parent multitask-mode transition.

`cursor_task` is the normalized default. The explicit `internal` execution mode
remains a compatibility path for existing adapter-backed AOS execution.

### 2. Bind each member to its configured physical adapter

The Forwarder compiles a member request into `TaskArgs` with its configured
adapter model in the explicit `model` field. That explicit member model wins
over any parent subagent override in display rewriting, telemetry, and
`OpenExec`. A dynamically registered virtual-model ID cannot be selected as a
member target, preventing recursive AOS dispatch.

### 3. Spawn first, then resolve by Task correlation key

The scheduler validates the complete Workspace task graph before scheduling.
For cursor-task batches it registers each deterministic AOS correlation key
(currently the generated Cursor Task `ToolCallId`), starts eligible Tasks, and
then resolves their results with bounded parallelism. `AOSResultRegistry`
correlates each returned result with that registered key; it is not the
`ExecServerMessage.exec_id`. Partial spawn
failure, timeout, and cancellation clean up pending registrations; dependent
levels do not advance after an unsuccessful prerequisite batch.

### 4. Replace the live AOS model after configuration saves

The Host registers AOS during virtual-model-manager setup only when AOS is
enabled and injects that manager before registration. When a running Host
accepts a config save, it serializes the save and replaces the AOS model in the
existing manager, or unregisters it when disabled. New turns receive the latest
leader/member bindings without rebuilding the HTTP Host.

## Evidence boundary and non-goals

Repository tests cover the normal Agent-mode Task compilation path, explicit
model precedence, anti-re-entry guard, workspace scheduling and result cleanup,
and live AOS replacement. They do not exercise a real Cursor client or network
session.

This ADR makes no claim that Cursor provides worktree isolation, process
sandboxing, or fork semantics for these Tasks. It uses no `.cursor/agents`
files. Those client-side properties remain unverified until a real Cursor E2E
run demonstrates them.

## Consequences

### Positive

- Reuses the existing Task protocol, Exec Bridge, and model adapter routing.
- Makes member model selection explicit and traceable across the dispatch path.
- Preserves the live Host and its existing virtual-model manager during config
  changes.

### Negative

- AOS completion still depends on the actual Cursor client's Task-result
  behavior.
- The private protocol can change with Cursor releases and requires continued
  regression coverage.

## Risks and controls

- **Recursive virtual-model dispatch**: reject registered virtual-model IDs as
  member targets.
- **Lost or late Task results**: correlate with the deterministic AOS key and remove pending
  registry entries on cancellation, timeout, and failed spawn.
- **Concurrent configuration saves**: serialize save/replacement so later
  completed saves determine the AOS bindings for future turns.
- **Unsupported client assumptions**: keep the parent in normal Agent mode and
  retain real Cursor E2E as a release gate.

## Verification and release gate

Focused repository tests cover configuration normalization/replacement, AOS
workflow scheduling, Task-model precedence, and the forwarder-to-Exec-Bridge
path. The remaining release gate is a real Cursor client E2E run that confirms
member dispatch, result return, and parent-session completion for
`cursor_task`.

## References

- `internal/backend/virtualmodel/aos/`
- `internal/backend/virtualmodel/result_registry.go`
- `internal/backend/forwarder/aos_bridge.go`
- `internal/backend/agent/bridge/exec/bridge.go`
- `.planning/aos-autonomous-evolution/task-1-report.md`
- `.planning/aos-autonomous-evolution/task-2-report.md`
- `.planning/aos-autonomous-evolution/task-3-report.md`
