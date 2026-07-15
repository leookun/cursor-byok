package evolver

import (
	"strings"
	"testing"
)

func TestCompileTaskPlan_FromProposal(t *testing.T) {
	e := NewEvolver()
	p := &Proposal{Priorities: []ProposalItem{
		{Priority: 1, Category: "fix", Title: "Resolve report-index drift", Rationale: "index"},
		{Priority: 2, Category: "research", Title: "Study semantic scanners", Rationale: "research"},
		{Priority: 3, Category: "implement", Title: "Land next ADR", Rationale: "code"},
	}}
	plan := e.CompileTaskPlan(p)
	if plan == nil || len(plan.Tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %+v", plan)
	}
	if plan.Tasks[0].Role != "docs" {
		t.Fatalf("expected docs role for index fix, got %s", plan.Tasks[0].Role)
	}
	if plan.Tasks[1].Role != "research" {
		t.Fatalf("expected research role, got %s", plan.Tasks[1].Role)
	}
	if !strings.Contains(plan.FormatTaskPlan(), "Evolution Task Plan") {
		t.Fatal("format missing header")
	}
	if !strings.Contains(plan.AdvisoryText(), "task-1") {
		t.Fatal("advisory missing tasks")
	}
}

func TestCompileTaskPlan_EmptyProposalFallback(t *testing.T) {
	e := NewEvolver()
	plan := e.CompileTaskPlan(nil)
	if len(plan.Tasks) != 1 {
		t.Fatalf("expected fallback task, got %+v", plan)
	}
}

func TestAddMetricRemediationTasks_CacheRegressionReachesPlan(t *testing.T) {
	e := NewEvolver()
	plan := e.CompileTaskPlan(&Proposal{Priorities: []ProposalItem{
		{Priority: 1, Category: "implement", Title: "Keep the existing proposal first"},
	}})
	runtimeMetrics := &RuntimeMetricReport{
		HasBaseline: true,
		Regressions: []RuntimeMetricRegression{{
			Metric: "cache_hit_rate", Baseline: 0.8, Current: 0.5, PctDelta: -37.5, IsRegression: true,
		}},
	}

	e.addMetricRemediationTasks(plan, runtimeMetrics)

	if len(plan.Tasks) != 2 {
		t.Fatalf("expected proposal plus remediation task, got %+v", plan.Tasks)
	}
	if plan.Tasks[0].Title != "Keep the existing proposal first" {
		t.Fatalf("proposal ordering changed: %+v", plan.Tasks)
	}
	remediation := plan.Tasks[1]
	if remediation.Title != "Restore cache efficiency surface and hit-rate evidence" || remediation.Action != "bounded-code-fix" {
		t.Fatalf("unexpected cache remediation task: %+v", remediation)
	}
}

func TestAddMetricRemediationTasks_StableReportDoesNotAppend(t *testing.T) {
	e := NewEvolver()
	plan := e.CompileTaskPlan(&Proposal{Priorities: []ProposalItem{
		{Priority: 1, Category: "implement", Title: "Keep the existing proposal"},
	}})
	before := append([]EvolutionTask(nil), plan.Tasks...)
	summaryBefore := plan.Summary

	e.addMetricRemediationTasks(plan, &RuntimeMetricReport{HasBaseline: true})

	if len(plan.Tasks) != len(before) {
		t.Fatalf("stable report changed task count: before=%d after=%d", len(before), len(plan.Tasks))
	}
	for i := range before {
		if plan.Tasks[i] != before[i] {
			t.Fatalf("stable report changed task %d: before=%+v after=%+v", i, before[i], plan.Tasks[i])
		}
	}
	if plan.Summary != summaryBefore {
		t.Fatalf("stable report changed summary: before=%q after=%q", summaryBefore, plan.Summary)
	}
}

func TestAddMetricRemediationTasks_DeduplicatesAndCapsPlan(t *testing.T) {
	e := NewEvolver()
	plan := &EvolutionTaskPlan{Tasks: []EvolutionTask{
		{ID: "remediate-1", Action: "bounded-code-fix", Title: "Restore cache efficiency surface and hit-rate evidence"},
		{ID: "task-2"}, {ID: "task-3"}, {ID: "task-4"}, {ID: "task-5"},
	}}
	runtimeMetrics := &RuntimeMetricReport{
		HasBaseline: true,
		Regressions: []RuntimeMetricRegression{{
			Metric: "cache_hit_rate", Baseline: 0.8, Current: 0.5, PctDelta: -37.5, IsRegression: true,
		}},
	}

	e.addMetricRemediationTasks(plan, runtimeMetrics)

	if len(plan.Tasks) != 5 {
		t.Fatalf("expected maxTasks bound and no duplicate, got %d tasks: %+v", len(plan.Tasks), plan.Tasks)
	}
}

func TestCheckDualModeRouting_RealRepo(t *testing.T) {
	root := testRepoRoot(t)
	ok, detail := checkDualModeRouting(root)
	if !ok {
		t.Fatalf("dual-mode routing failed: %s", detail)
	}
}

func TestSemanticRules_CountExpanded(t *testing.T) {
	if len(CanonicalSemanticRules) < 8 {
		t.Fatalf("expected expanded semantic rules >=8, got %d", len(CanonicalSemanticRules))
	}
}
