// executor.go executes allowlisted EvolutionTaskPlan actions (ADR-039).
//
// This is the first autonomous *implementation* stage that still respects the
// constitution: only deterministic, reverse-safe handbook/index repairs and
// curated package tests may run automatically. Code mutation is limited to closed recipes under path allowlists (ADR-041).
package evolver

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TaskExecutionStatus is the outcome of one planned task.
type TaskExecutionStatus string

const (
	TaskStatusApplied TaskExecutionStatus = "applied"
	TaskStatusSkipped TaskExecutionStatus = "skipped"
	TaskStatusFailed  TaskExecutionStatus = "failed"
)

// TaskExecution records one task attempt.
type TaskExecution struct {
	TaskID     string              `json:"taskID"`
	Action     string              `json:"action"`
	Status     TaskExecutionStatus `json:"status"`
	Detail     string              `json:"detail"`
	DurationMS int64               `json:"durationMS"`
}

// TaskPlanExecution is the full audited execution result for a plan.
type TaskPlanExecution struct {
	StartedAt  time.Time       `json:"startedAt"`
	DurationMS int64           `json:"durationMS"`
	Applied    int             `json:"applied"`
	Skipped    int             `json:"skipped"`
	Failed     int             `json:"failed"`
	Results    []TaskExecution `json:"results"`
	// Writeback is set when any auto-writeback action ran.
	Writeback *AutoWritebackResult `json:"writeback,omitempty"`
	// Tests is set when run-tests action ran.
	Tests *TestReport `json:"tests,omitempty"`
	// Scaffolds lists documentation drafts created by allowlisted scaffold actions.
	Scaffolds []ScaffoldResult `json:"scaffolds,omitempty"`
	// CodeFixes lists bounded implementation recipe outcomes (ADR-041).
	CodeFixes []CodeFixResult `json:"codeFixes,omitempty"`
}

// ExecuteTaskPlan runs allowlisted autonomous actions from the plan.
// Non-allowlisted tasks are recorded as skipped with rationale.
// Mutates Go source only via closed bounded recipes under path allowlists (ADR-041).
func (e *Evolver) ExecuteTaskPlan(ctx context.Context, repoRoot string, plan *EvolutionTaskPlan) *TaskPlanExecution {
	start := time.Now().UTC()
	out := &TaskPlanExecution{StartedAt: start}
	if plan == nil || len(plan.Tasks) == 0 {
		out.DurationMS = time.Since(start).Milliseconds()
		return out
	}

	// Collapse duplicate allowlisted actions so writeback/tests/scaffolds run at most once.
	needWriteback := false
	needTests := false
	needScaffold := false
	needCodeFix := false
	for _, task := range plan.Tasks {
		switch task.Action {
		case "auto-writeback":
			needWriteback = true
		case "run-tests":
			needTests = true
		case "scaffold-adr", "scaffold-research", "scaffold-report":
			needScaffold = true
		case "bounded-code-fix":
			needCodeFix = true
		}
	}

	var writebackResult *AutoWritebackResult
	var testReport *TestReport
	var scaffolds []ScaffoldResult
	var codeFixes []CodeFixResult
	if needCodeFix {
		cfStart := time.Now()
		fixes, err := e.ApplyBoundedCodeRecipes(repoRoot, plan)
		if err != nil {
			out.Results = append(out.Results, TaskExecution{
				TaskID:     "action:bounded-code-fix",
				Action:     "bounded-code-fix",
				Status:     TaskStatusFailed,
				Detail:     err.Error(),
				DurationMS: time.Since(cfStart).Milliseconds(),
			})
			out.Failed++
		} else {
			codeFixes = fixes
			out.CodeFixes = fixes
			appliedFiles := 0
			for _, f := range fixes {
				appliedFiles += f.Applied
			}
			detail := "no code changes"
			if appliedFiles > 0 {
				detail = fmt.Sprintf("applied %d file change(s) via bounded recipes", appliedFiles)
				// Code changed: force curated tests.
				needTests = true
			}
			out.Results = append(out.Results, TaskExecution{
				TaskID:     "action:bounded-code-fix",
				Action:     "bounded-code-fix",
				Status:     TaskStatusApplied,
				Detail:     detail,
				DurationMS: time.Since(cfStart).Milliseconds(),
			})
			out.Applied++
		}
	}
	if needScaffold {
		scStart := time.Now()
		created, err := e.CreateAllowlistedScaffolds(repoRoot, plan)
		if err != nil {
			out.Results = append(out.Results, TaskExecution{
				TaskID:     "action:scaffold",
				Action:     "scaffold",
				Status:     TaskStatusFailed,
				Detail:     err.Error(),
				DurationMS: time.Since(scStart).Milliseconds(),
			})
			out.Failed++
		} else {
			scaffolds = created
			out.Scaffolds = created
			detail := "no new drafts"
			if len(created) > 0 {
				detail = fmt.Sprintf("created %d draft artifact(s)", len(created))
				// Newly created docs should be indexed.
				needWriteback = true
			}
			out.Results = append(out.Results, TaskExecution{
				TaskID:     "action:scaffold",
				Action:     "scaffold",
				Status:     TaskStatusApplied,
				Detail:     detail,
				DurationMS: time.Since(scStart).Milliseconds(),
			})
			out.Applied++
		}
	}
	if needWriteback {
		wbStart := time.Now()
		aw, err := e.AutoWriteback(repoRoot, nil)
		if err != nil {
			out.Results = append(out.Results, TaskExecution{
				TaskID:     "action:auto-writeback",
				Action:     "auto-writeback",
				Status:     TaskStatusFailed,
				Detail:     err.Error(),
				DurationMS: time.Since(wbStart).Milliseconds(),
			})
			out.Failed++
		} else {
			writebackResult = aw
			out.Writeback = aw
			detail := "no changes"
			if aw != nil && len(aw.Applied) > 0 {
				detail = fmt.Sprintf("applied %d writeback item(s)", len(aw.Applied))
			}
			out.Results = append(out.Results, TaskExecution{
				TaskID:     "action:auto-writeback",
				Action:     "auto-writeback",
				Status:     TaskStatusApplied,
				Detail:     detail,
				DurationMS: time.Since(wbStart).Milliseconds(),
			})
			out.Applied++
		}
	}
	if needTests {
		testStart := time.Now()
		if ctx == nil {
			ctx = context.Background()
		}
		// Prefer recipe-scoped packages when bounded code fixes ran; otherwise curated defaults.
		var pkgs []string
		seen := map[string]bool{}
		for _, fix := range codeFixes {
			for _, recipe := range CanonicalCodeRecipes {
				if recipe.ID != fix.RecipeID {
					continue
				}
				for _, pkg := range recipe.TestPackages {
					if !seen[pkg] {
						seen[pkg] = true
						pkgs = append(pkgs, pkg)
					}
				}
			}
		}
		testReport = e.RunTests(ctx, repoRoot, pkgs)
		out.Tests = testReport
		status := TaskStatusApplied
		detail := "tests passed"
		if testReport != nil && testReport.Failed > 0 {
			status = TaskStatusFailed
			detail = fmt.Sprintf("%d package test failure(s)", testReport.Failed)
			out.Failed++
		} else {
			out.Applied++
		}
		out.Results = append(out.Results, TaskExecution{
			TaskID:     "action:run-tests",
			Action:     "run-tests",
			Status:     status,
			Detail:     detail,
			DurationMS: time.Since(testStart).Milliseconds(),
		})
	}

	// Per-task audit trail.
	for _, task := range plan.Tasks {
		switch task.Action {
		case "auto-writeback":
			if writebackResult == nil && needWriteback {
				// failure already recorded at action level; mark task failed too
				out.Results = append(out.Results, TaskExecution{
					TaskID: task.ID,
					Action: task.Action,
					Status: TaskStatusFailed,
					Detail: "auto-writeback action failed",
				})
				out.Failed++
				continue
			}
			detail := "covered by allowlisted auto-writeback action"
			if writebackResult != nil {
				detail = fmt.Sprintf("covered by auto-writeback (%d applied)", len(writebackResult.Applied))
			}
			out.Results = append(out.Results, TaskExecution{
				TaskID: task.ID,
				Action: task.Action,
				Status: TaskStatusApplied,
				Detail: detail,
			})
			// do not double-count applied
		case "run-tests":
			status := TaskStatusApplied
			detail := "covered by allowlisted run-tests action"
			if testReport != nil && testReport.Failed > 0 {
				status = TaskStatusFailed
				detail = "covered by run-tests action (failures present)"
			}
			out.Results = append(out.Results, TaskExecution{
				TaskID: task.ID,
				Action: task.Action,
				Status: status,
				Detail: detail,
			})
		case "scaffold-adr", "scaffold-research", "scaffold-report":
			detail := "covered by allowlisted scaffold action"
			if len(scaffolds) > 0 {
				detail = fmt.Sprintf("covered by scaffold action (%d created)", len(scaffolds))
			}
			out.Results = append(out.Results, TaskExecution{
				TaskID: task.ID,
				Action: task.Action,
				Status: TaskStatusApplied,
				Detail: detail,
			})
		case "bounded-code-fix":
			detail := "covered by bounded-code-fix action"
			if len(codeFixes) > 0 {
				n := 0
				for _, f := range codeFixes {
					n += f.Applied
				}
				detail = fmt.Sprintf("covered by bounded-code-fix (%d file change(s))", n)
			}
			out.Results = append(out.Results, TaskExecution{
				TaskID: task.ID,
				Action: task.Action,
				Status: TaskStatusApplied,
				Detail: detail,
			})
		default:
			out.Results = append(out.Results, TaskExecution{
				TaskID: task.ID,
				Action: actionOrManual(task.Action),
				Status: TaskStatusSkipped,
				Detail: "non-allowlisted action requires agent/human implementation",
			})
			out.Skipped++
		}
	}

	out.DurationMS = time.Since(start).Milliseconds()
	return out
}

func actionOrManual(action string) string {
	if strings.TrimSpace(action) == "" {
		return "manual"
	}
	return action
}

// FormatTaskPlanExecution returns a human-readable execution audit.
func (r *TaskPlanExecution) FormatTaskPlanExecution() string {
	if r == nil {
		return "No task plan execution.\n"
	}
	var b strings.Builder
	b.WriteString("=== TaskPlan Execution ===\n")
	b.WriteString(fmt.Sprintf("Applied: %d  Skipped: %d  Failed: %d  Duration: %dms\n",
		r.Applied, r.Skipped, r.Failed, r.DurationMS))
	for _, item := range r.Results {
		b.WriteString(fmt.Sprintf("  - %s [%s] %s: %s\n", item.TaskID, item.Action, item.Status, item.Detail))
	}
	return b.String()
}
