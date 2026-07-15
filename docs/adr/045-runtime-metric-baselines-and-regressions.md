# ADR-045: Runtime Metric Baselines & Regressions

## Status
Accepted

## Date
2026-07-15

## Context

Prior phases established:

- efficiency *surfaces* (cache/tool/memory/optimize/forwarder summaries)
- benchmark latency/token baselines

But evolution still could not compare **live operational efficiency metrics** over time. Hit-rate drops or cost spikes could exist while handbook/code remained "structurally healthy".

## Decision

### 1. RuntimeMetricSnapshot

Collect best-effort metrics:

| Source | Metrics |
|---|---|
| cache `stats.json` | hitRate, tokensSaved, exact/semantic hits |
| tool `cache_stats.json` | hitRate, hits, misses |
| optimize `cost_tracker.json` | spent, turns, budget |

Also supports fixture/override:

`docs/reports/.baselines/runtime-metrics-current.json`

#### Source ordering and unified live export

CollectRuntimeMetrics resolves sources in this order:

1. docs/reports/.baselines/runtime-metrics-current.json is the repo-local CI/fixture override and has highest priority.
2. <dataRoot>/runtime-metrics/current.json is the unified live snapshot exported by runtime/Host wiring.
3. Granular fallback stores are read only when neither complete snapshot exists: cache stats, tool-cache stats, and the optimize cost tracker, followed by their fixture paths.

The unified export is the canonical live snapshot path and is consumed immediately after the CI override. Collection remains fail-open: missing, unreadable, or malformed sources are skipped and do not fail evolution. WriteRuntimeMetricExports already writes the unified file; wiring the production Host cycle to call that writer remains the next phase.

### 2. Baseline persistence

Persist latest snapshot to:

`docs/reports/.baselines/latest-runtime-metrics.json`

#### Evidence-bearing baseline protection

A snapshot is evidence-bearing only when at least one of HasCache, HasToolCache, or HasOptimize is true. Only evidence-bearing snapshots may be saved or loaded as runtime baselines, or update the baseline during Persist. Runtime comparison requires evidence on both current and baseline snapshots, so no-source snapshots cannot produce a false "vs baseline: stable" result.

### 3. Regression thresholds

- cache/tool hit-rate: regression if absolute drop > 0.05 or relative drop > 10%
- cache tokensSaved: higher-is-better with same drop thresholds
- optimize spent: regression if current > baseline * 1.20

### 4. Loop integration

- `EvolveWithOptions` always collects + compares metrics
- `Persist` updates runtime metric baseline
- `Propose` emits optimize priorities for metric regressions
- report formatting includes `=== Runtime Metrics ===`

### 5. Non-goals

- Auto-tuning cache TTL / model routing from regressions
- Requiring production metric stores in unit tests (fixtures allowed)
- Replacing benchmark latency/token baselines

## Rationale

1. Turns efficiency surfaces into longitudinal evidence.
2. Lets the living loop prioritize real cost/hit-rate regressions.
3. Keeps collection fail-open so offline/dev environments still evolve.

## Consequences

### Positive
- Operational regressions become first-class evolution findings.
- Baseline evidence is reproducible under `.baselines/`.

### Negative
- Metric availability depends on host data-root wiring.
- Thresholds are heuristic and may need calibration.

## Risks and controls

- A missing Host export still produces a no-source snapshot, but the evidence predicate prevents it from becoming durable baseline evidence.
- The repo-local override can intentionally mask live data in CI; its highest-priority position is explicit and tested.
- Unified-file staleness remains a production integration concern until the Host cycle writes it on every collection interval.

## Next phase

Connect the Host background Evolver cycle to WriteRuntimeMetricExports(appdata.DataRootPath(), snapshot) using the existing cache, tool-cache, and optimize runtime instances. Keep the call best-effort and preserve nil-runtime handling; do not add a registry/provider abstraction.

## References

- ADR-029 Benchmark baselines
- ADR-044 Optimize/Forwarder recipes & metric proposals
- docs/research/runtime-metric-baselines-regressions.md
