# Benchmark Report: 2026-07-15 Phase 25 Runtime Metric Baselines & Regressions

## Results
| Check | Result |
|---|---|
| CompareRuntimeMetrics regressions/improvements | PASS |
| Collect override + baseline persist/load | PASS |
| Propose includes runtime metric regressions | PASS |
| evolver package tests | PASS |

## Review Fix

The verified gap was that WriteRuntimeMetricExports wrote <dataRoot>/runtime-metrics/current.json but CollectRuntimeMetrics never consumed it. A no-source snapshot could also be persisted as a baseline and later formatted as stable. The implementation now reads the unified export immediately after the repo-local CI override, preserves granular fallbacks and fail-open collection, and requires evidence from HasCache, HasToolCache, or HasOptimize before baseline persistence or stable comparison.

The precedence regression test writes distinct values to both docs/reports/.baselines/runtime-metrics-current.json and the unified data-root file and verifies that the repo-local override remains highest priority. Regression thresholds were unchanged.

### Verification

| Check | Result |
|---|---|
| repo-local override beats unified export | PASS |
| no-source baseline protection | PASS |
| Evolver package suite | PASS |
| docguard index consistency | PASS |

## Next phase

Wire the Host background Evolver cycle to export the existing cache, tool-cache, and optimize runtime state through WriteRuntimeMetricExports. Keep the export best-effort and do not introduce a registry/provider abstraction.

## Reproduction
```bash
go test ./internal/backend/runtime/evolver -run 'RuntimeMetric|CollectRuntime|CompareRuntime|Propose_IncludesRuntime' -count=1
go test ./internal/backend/runtime/evolver
go test ./internal/docguard -run 'TestCheckHandbookConsistency_RepoIsConsistent|TestADRIndex_MatchesDisk|TestResearchNotes_OnDiskListedInCharter' -count=1
go run ./cmd/evolver/ -ci
```
