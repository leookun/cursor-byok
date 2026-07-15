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
