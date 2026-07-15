# Task 3 Report

## Status

Complete. Runtime metric regressions now flow through the existing `BuildMetricRemediationTasks` mapping into `EvolutionReport.TaskPlan`.

## Implementation

- `EvolveWithOptions` compiles the existing proposal plan, then merges mapped runtime remediation tasks.
- Proposal task order is preserved.
- The existing five-task bound is preserved across proposal and remediation tasks.
- Remediation tasks are skipped when already represented by stable ID or matching action/title.
- Reports without runtime regressions, including stable reports, retain the existing task list and summary behavior.
- No second planner or remediation registry was introduced.

## Focused Evidence

The focused tests prove that a `cache_hit_rate` regression produces `Restore cache efficiency surface and hit-rate evidence` with action `bounded-code-fix`, stable reports do not append or rewrite the plan, and duplicate/max-task cases remain bounded.

## Test Output

Command:

```text
go test ./internal/backend/runtime/evolver -run "Test(AddMetricRemediationTasks|CompileTaskPlan|Propose_IncludesRuntimeMetricRegressions|CompareRuntimeMetrics)" -count=1
ok  	cursor/internal/backend/runtime/evolver	0.452s
```

Command:

```text
go test ./internal/backend/runtime/evolver -count=1
ok  	cursor/internal/backend/runtime/evolver	2.767s
```

## Concerns

Remediation tasks can only be appended when the proposal plan has fewer than five tasks; this is intentional and preserves `CompileTaskPlan`'s bounded next-slice contract. Unknown runtime metrics remain unmapped by the existing remediation policy.

## Scope

Baseline: `dfcf7d8`.

Owned files changed: `internal/backend/runtime/evolver/report.go`, `internal/backend/runtime/evolver/taskplan.go`, `internal/backend/runtime/evolver/taskplan_test.go`, and this report.
