# Task 1 Report: Runtime metric snapshots evidence-bearing

## Status

Done.

## Files changed

- `internal/backend/runtime/evolver/metrics.go`
- `internal/backend/runtime/evolver/metrics_test.go`
- `internal/backend/runtime/evolver/persist.go`
- `internal/backend/runtime/evolver/persist_test.go`
- `.superpowers/sdd/task-1-report.md`

## Implementation summary

- Preserved repo-local CI override precedence at `docs/reports/.baselines/runtime-metrics-current.json`.
- Added the production unified live source `<dataRoot>/runtime-metrics/current.json` immediately after the CI override and before granular fallback stores.
- Added a runtime metric evidence predicate: a snapshot is evidence-bearing only when at least one of `HasCache`, `HasToolCache`, or `HasOptimize` is true.
- Prevented no-source snapshots from:
  - loading as a valid runtime metric baseline,
  - saving as the latest runtime metric baseline,
  - updating the runtime metric baseline during `Persist`,
  - producing a false `vs baseline: stable` comparison.
- Kept fail-open collection behavior and existing granular fallback behavior.
- Did not change regression thresholds.
- Did not add a registry/provider abstraction.

## Tests and commands

### Focused regression tests

Command:

```bash
go test ./internal/backend/runtime/evolver -run "TestCollectRuntimeMetrics_ConsumesUnifiedRuntimeMetricExport|TestCompareRuntimeMetrics_EmptySnapshotsDoNotClaimStableBaseline|TestPersist_DoesNotUpdateRuntimeMetricBaselineWithoutEvidence"
```

Output:

```text
ok  	cursor/internal/backend/runtime/evolver	0.475s
```

### Full Evolver package suite

Command:

```bash
go test ./internal/backend/runtime/evolver
```

Output:

```text
ok  	cursor/internal/backend/runtime/evolver	2.552s
```

## Self-review

- Scope check: touched only the four owned Evolver files plus this report.
- Ordering check: CI override remains highest priority; unified live source is before granular fallbacks.
- Evidence check: empty snapshots cannot establish, update, load as, or compare as a stable runtime metric baseline.
- Compatibility check: existing granular fallback loaders are unchanged.
- Simplicity check: reused existing file/path patterns; added no abstractions or threshold changes.

## Concerns

- `internal/backend/runtime/evolver/` is untracked in this worktree, so staging the owned files will add those files rather than record a normal tracked-file delta. I staged only the task-owned files.

## Task 1 Review Fix Report

### Status

The review fixes are implemented and verified. This append records the second deterministic precedence test and the durable ADR/research/benchmark writeback. The prior report above is preserved unchanged.

### Files changed in this fix

- `internal/backend/runtime/evolver/metrics_test.go`
- `docs/adr/045-runtime-metric-baselines-and-regressions.md`
- `docs/research/runtime-metric-baselines-regressions.md`
- `docs/reports/2026-07-15-phase25-runtime-metric-baselines.md`
- `.superpowers/sdd/task-1-report.md`

### Evidence and behavior

- Added `TestCollectRuntimeMetrics_RepoOverridePrecedesUnifiedExport`, which writes distinct evidence-bearing snapshots to both `docs/reports/.baselines/runtime-metrics-current.json` and `<dataRoot>/runtime-metrics/current.json` under isolated temporary roots. The collected cache, tool-cache, and optimize values must match the repo-local override, proving it remains highest priority.
- ADR-045 now records the exact source ordering: repo-local CI/fixture override, unified live export, then granular fallbacks.
- ADR-045, the research note, and the Phase 25 report record the verified writer/collector gap, the evidence-bearing predicate, no-source baseline protection, fail-open behavior, unchanged thresholds, and the remaining Host production export integration as the next phase.
- No provider/model registry abstraction or threshold change was introduced.

### Tests and commands

Focused Evolver coverage:

    go test ./internal/backend/runtime/evolver -run 'RuntimeMetric|CollectRuntime|CompareRuntime|Propose_IncludesRuntime' -count=1

Output:

    ok  	cursor/internal/backend/runtime/evolver	0.485s

Full Evolver package suite:

    go test ./internal/backend/runtime/evolver

Output:

    ok  	cursor/internal/backend/runtime/evolver	2.891s

Focused documentation consistency coverage:

    go test ./internal/docguard -run 'Handbook|ADR|Research' -count=1

Output:

    ok  	cursor/internal/docguard	0.277s

### Self-review

- Test scope is deterministic and isolated with temporary repository and user-data roots.
- Override precedence is asserted across all three runtime metric surfaces, not only one field.
- Documentation matches the implemented order and evidence predicate; handbook indexes were already consistent and were not modified.
- Existing granular fallbacks, fail-open collection, regression thresholds, and implementation ownership remain unchanged.
- The fix commit will stage only the four review-fix files above plus this report; unrelated dirty files remain unstaged.

### Concerns

- Host production wiring still needs to call `WriteRuntimeMetricExports` from the background Evolver cycle; that is explicitly documented as the next phase and is outside this fix scope.
- The repository has extensive unrelated dirty and untracked files. They are preserved and excluded from the fix commit.

### Report path

`.superpowers/sdd/task-1-report.md`
