# Task 2 Report: Host runtime state -> unified Evolver evidence export

## Status

Implemented and verified.

## Current research / code evidence

- `Host.rebuildLocked` already constructs the production `toolruntime.Runtime`, `cacheruntime.Runtime`, and `optimize.Runtime`.
- Existing runtime APIs are sufficient:
  - cache: `(*cache.Runtime).Stats() *CacheStats`
  - tool: `(*tool.Runtime).CacheStats() ToolCacheStats`
  - optimize: `(*optimize.Runtime).GetCostSummary() *CostTracker`
- Existing canonical export path is `evolver.WriteRuntimeMetricExports(appdata.DataRootPath(), *RuntimeMetricSnapshot)`.
- Existing collector consumes the unified export from `<dataRoot>/runtime-metrics/current.json`, so no registry, metrics provider, or alternate runtime instance is required.

## Reference papers and source

- Source: `internal/backend/host.go`
- Source: `internal/backend/host_evolver.go`
- Source: `internal/backend/runtime/evolver/metrics.go`
- Source: `internal/backend/runtime/cache/runtime.go`
- Source: `internal/backend/runtime/tool/runtime.go`
- Source: `internal/backend/runtime/optimize/runtime.go`
- Conceptual references retained from project charter: AIOS runtime observability, MemGPT-style memory/runtime surfaces, OpenAI Agents SDK tracing/session evidence patterns.

## Architecture design

- Host now keeps rebuilt cache/tool runtime references beside the existing optimization runtime.
- Cache/tool references use an independent `runtimeMu sync.RWMutex`, separate from `optMu`.
- `rebuildLocked` swaps the freshly created cache/tool runtimes under `runtimeMu` during rebuild.
- `host.evolutionRuntimeMetricSnapshot()` builds an `evolver.RuntimeMetricSnapshot` from the retained production runtimes.
- `host.exportEvolutionRuntimeMetrics()` writes the snapshot through `evolver.WriteRuntimeMetricExports`.
- `runBackgroundEvolutionCheck` calls export before `Evolve`, warning only on export errors.

## Technical choice

- Reused existing runtime APIs and the canonical Evolver writer.
- Kept the helper private to Host because this is production wiring, not a new public metrics subsystem.
- `HasOptimize` is set only when `OptimizationRuntime()` is non-nil; this avoids treating nil-host `GetCostSummary()` empty pointers as real evidence.
- Empty/nil snapshots skip writes instead of creating no-evidence export files.

## Integration with Cursor BYOK

- The production runtimes created for the actual forwarder path are the same instances used for evidence export.
- Evolver background diagnosis remains fail-open and non-blocking.
- `CollectRuntimeMetrics` continues to consume the unified file written by the Host export path.

## Implementation plan completed

1. Retain cache/tool runtimes on Host under `runtimeMu`.
2. Assign fresh runtime instances during `rebuildLocked`.
3. Add private snapshot/export helpers.
4. Call export before the background `Evolve` cycle.
5. Add deterministic tests for initialized runtimes, nil Host/runtime cases, skip-without-evidence, canonical export, and production call-site wiring.
6. Run focused Host tests and full backend relevant suite.

## Risk analysis

- Runtime rebuild race: mitigated by `runtimeMu` snapshotting references before reading individual runtime stats.
- Nil runtime panic: covered by nil Host and nil dependency tests.
- Background startup regression: export errors are warning-only and do not block diagnosis.
- Dirty worktree risk: only owned files were prepared for staging; unrelated dirty files remain untouched.

## Benchmark / verification

- Runtime export is lightweight: one in-memory snapshot plus small JSON writes before background diagnosis.
- No new network call, provider, registry, or runtime instance is introduced.
- Verification commands:
  - `go test ./internal/backend -run "Test(RuntimeMetricSnapshot|ExportEvolutionRuntimeMetrics|RunBackgroundEvolutionCheck_CallsRuntimeMetricExport)" -count=1`
  - `go test ./internal/backend -count=1`
  - `go test ./internal/backend/runtime/cache ./internal/backend/runtime/tool ./internal/backend/runtime/optimize ./internal/backend/runtime/evolver -count=1`
  - `go test ./internal/backend/... -count=1`

## Follow-up optimization route

- Keep `RuntimeMetricSnapshot` as the single exchange format.
- If future runtime surfaces are added, extend the existing snapshot/writer/collector path rather than adding a parallel metrics registry.
- Consider replacing source-string call-site assertions with a narrower injectable test seam only if the background Evolver path becomes too expensive to exercise.

## Next phase

- Use the unified runtime metric export as the evidence source for regression remediation and runtime optimization proposals.
- Continue routing metric-driven changes through existing Evolver task planning and bounded code-fix recipes.

---

## Important findings fix report — 2026-07-15

### Status

Implemented and verified in a follow-up fix commit.

### Fixes

1. Runtime pointer coherence:
   - Replaced separate `optMu` / `runtimeMu` ownership with one Host-local `hostRuntimeState` tuple protected by `runtimeMu`.
   - `rebuildLocked` now swaps cache, tool, and optimization runtime pointers together.
   - Host accessors/config updates (`GetCostSummary`, `OptimizationRuntime`, `applyOptimizationConfig`) read the optimization pointer through the same tuple snapshot.
   - `evolutionRuntimeMetricSnapshot` reads cache/tool/optimization pointers from one tuple snapshot before collecting stats, avoiding split-lock mixed-generation reads.

2. Behavior coverage:
   - Removed the source-string production-call-site assertion.
   - `TestRunBackgroundEvolutionCheck_CallsRuntimeMetricExport` now executes the real `runBackgroundEvolutionCheck` path with an isolated temporary user data root and temporary repo root, then asserts the canonical `<dataRoot>/runtime-metrics/current.json` file contains optimization evidence.
   - Nil/no-evidence fail-open behavior remains covered by `TestRuntimeMetricSnapshot_WithNilRuntimeDependencies` and `TestExportEvolutionRuntimeMetrics_SkipsWithoutEvidence`.

### Scope preservation

- Preserved current implementation commit `1f57f9b` and appended this fix report instead of rewriting the original report.
- The review mentioned unrelated context/embedder/AOS code in the diff. Those files were already modified or untracked before Task 2 began (`internal/backend/host.go` was modified and `internal/backend/host_evolver.go` / `internal/backend/host_evolver_test.go` were untracked at Task 2 start), so this fix intentionally does not remove or refactor that pre-existing user work.
- Staging scope is limited to owned Host files plus this report.

### Verification output

```text
> go test ./internal/backend -run 'Test(RuntimeMetricSnapshot|ExportEvolutionRuntimeMetrics|RunBackgroundEvolutionCheck_CallsRuntimeMetricExport)' -count=1
ok  	cursor/internal/backend	0.177s
```

```text
> go test ./internal/backend -count=1
ok  	cursor/internal/backend	0.242s
```

```text
> go test ./internal/backend/runtime/cache ./internal/backend/runtime/tool ./internal/backend/runtime/optimize ./internal/backend/runtime/evolver -count=1
ok  	cursor/internal/backend/runtime/cache	1.475s
ok  	cursor/internal/backend/runtime/tool	0.572s
ok  	cursor/internal/backend/runtime/optimize	0.550s
ok  	cursor/internal/backend/runtime/evolver	3.312s
```
