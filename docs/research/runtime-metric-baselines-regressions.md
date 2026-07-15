# Runtime Metric Baselines & Regressions Research

## Basic Info
- Date: 2026-07-15
- Module: Evolver runtime efficiency baselines (ADR-045)

## Decision
Persist and compare cache/tool/optimize efficiency metrics across evolution cycles, feeding regressions into Propose priorities while keeping collection best-effort.

## Verified gap

Two independent audits verified that WriteRuntimeMetricExports wrote <dataRoot>/runtime-metrics/current.json, while CollectRuntimeMetrics did not read that file. The collector therefore fell through to granular stores and could return a no-source snapshot. Because an empty snapshot could be persisted and compared, the loop could emit a false stable baseline.

## Implemented fix

- Preserve docs/reports/.baselines/runtime-metrics-current.json as the highest-priority repo-local CI/fixture override.
- Read <dataRoot>/runtime-metrics/current.json immediately after the override and before granular cache, tool-cache, and optimize fallbacks.
- Treat a snapshot as evidence-bearing only when HasCache, HasToolCache, or HasOptimize is true.
- Reject no-source snapshots for baseline save/load/update and require evidence on both sides of a runtime comparison.
- Keep fail-open collection, existing thresholds, granular fallback behavior, and the no-registry design unchanged.

The unified writer is now consumable by the Evolver. The remaining production gap is the Host background cycle call site that must export the live runtime snapshot.

## Reproduction

go test ./internal/backend/runtime/evolver -run 'RuntimeMetric|CollectRuntime|CompareRuntime|Propose_IncludesRuntime' -count=1

The precedence regression test creates distinct repo-local override and unified snapshots and verifies the override wins. The empty-snapshot tests verify that no-source data cannot become a baseline or claim "vs baseline: stable".

## Risks and next phase

The override intentionally masks live data when present, which is appropriate for deterministic CI and is now covered directly. Missing or stale Host exports remain fail-open and therefore visible as absent evidence rather than durable baseline data. The next phase wires Host's existing cache, tool-cache, and optimize instances into WriteRuntimeMetricExports on the background Evolver path.
