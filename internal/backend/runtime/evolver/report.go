package evolver

import (
	"context"
	"fmt"
	"strings"
	"time"

	virtualmodel "cursor/internal/backend/virtualmodel"
	"cursor/internal/benchmark"
)

// Proposal is the output of Propose(). It synthesizes diagnosis findings,
// knowledge graph gaps, and benchmark results into actionable recommendations.
type Proposal struct {
	// Priorities are ordered by recommended execution sequence.
	Priorities []ProposalItem `json:"priorities"`
}

// ProposalItem is a single recommended action.
type ProposalItem struct {
	Priority  int    `json:"priority"`
	Category  string `json:"category"` // "fix", "optimize", "research", "implement"
	Title     string `json:"title"`
	Rationale string `json:"rationale"`
}

// EvolutionReport is the final output of Evolve(). It aggregates the full
// closed-loop result and is writable to docs/reports/.
type EvolutionReport struct {
	Timestamp  time.Time        `json:"timestamp"`
	DurationMS int64            `json:"durationMS"`
	Diagnosis  *DiagnosisReport `json:"diagnosis"`
	Sediment   *KnowledgeGraph  `json:"sediment"`
	// Tests is the constitutional Test stage result (ADR-031).
	// Nil or Ran=false when skipped (Host background / default Evolve).
	Tests     *TestReport       `json:"tests,omitempty"`
	Benchmark *benchmark.Report `json:"benchmark,omitempty"`
	Proposal  *Proposal         `json:"proposal"`
	// Regression is the benchmark-vs-baseline comparison result (ADR-029).
	// Populated only when a benchmark ran AND a prior baseline exists on disk.
	Regression *RegressionReport `json:"regression,omitempty"`
	// Memory is cross-report evolution memory / trend analysis (ADR-033).
	Memory *EvolutionMemory `json:"memory,omitempty"`
	// RuntimeMetrics is efficiency metric comparison (ADR-045).
	RuntimeMetrics *RuntimeMetricReport `json:"runtimeMetrics,omitempty"`
	// WritebackList lists handbook chapters that may need updates based
	// on diagnosed findings (e.g., new ADR not indexed in chapter 28).
	WritebackList []string `json:"writebackList"`
	// TaskPlan is the executable next-slice plan compiled from Proposal (ADR-038).
	TaskPlan *EvolutionTaskPlan `json:"taskPlan,omitempty"`
	// TaskPlanExecution is the audited allowlisted execution result (ADR-039).
	TaskPlanExecution *TaskPlanExecution `json:"taskPlanExecution,omitempty"`
}

// EvolveOptions controls optional stages of the closed loop.
// Host background path uses zero value (no tests) to stay non-blocking.
type EvolveOptions struct {
	// RunTests enables the curated package Test stage (ADR-031).
	RunTests bool
	// TestPackages overrides DefaultTestPackages when non-empty.
	TestPackages []string
}

// Evolve orchestrates the complete closed loop with default options
// (tests skipped). Prefer EvolveWithOptions for CLI/CI.
// Diagnose -> Sediment -> (Test?) -> Benchmark? -> CompareBaseline -> Propose.
// If model is nil, the Benchmark and Regression steps are skipped.
func (e *Evolver) Evolve(ctx context.Context, repoRoot string, model virtualmodel.VirtualModel) *EvolutionReport {
	return e.EvolveWithOptions(ctx, repoRoot, model, EvolveOptions{})
}

// EvolveWithOptions is the full orchestration entrypoint (ADR-028/031).
func (e *Evolver) EvolveWithOptions(ctx context.Context, repoRoot string, model virtualmodel.VirtualModel, opts EvolveOptions) *EvolutionReport {
	start := time.Now()
	report := &EvolutionReport{Timestamp: start}

	// Step 1: Diagnose
	report.Diagnosis = e.Diagnose(repoRoot)

	// Step 2: Sediment
	report.Sediment = e.Sediment(repoRoot)

	// Step 3: Test (optional; CLI/CI enable via opts.RunTests)
	if opts.RunTests {
		report.Tests = e.RunTests(ctx, repoRoot, opts.TestPackages)
	}

	// Step 4: Benchmark (optional, requires a VirtualModel)
	if model != nil {
		report.Benchmark = e.runBenchmark(ctx, model)

		// Step 4b: Compare against stored baseline (ADR-029).
		baseline := e.LoadBaseline(repoRoot)
		report.Regression = e.CompareWithBaseline(&report.Benchmark.Summary, baseline)
	}

	// Step 5: Evolution Memory (ADR-033) — learn from prior snapshots.
	report.Memory = e.LoadEvolutionMemory(repoRoot, DefaultMemoryWindow)

	// Step 5b: Runtime efficiency metrics baseline/regression (ADR-045).
	curMetrics := e.CollectRuntimeMetrics(repoRoot)
	blMetrics := e.LoadRuntimeMetricBaseline(repoRoot)
	report.RuntimeMetrics = e.CompareRuntimeMetrics(curMetrics, blMetrics)

	// Step 6: Propose (includes test failures, regressions, recurring history)
	report.Proposal = e.Propose(report.Diagnosis, report.Sediment, report.Tests, report.Benchmark, report.Regression, report.Memory, report.RuntimeMetrics)

	// Step 6b: Compile executable next-slice task plan (ADR-038).
	report.TaskPlan = e.CompileTaskPlan(report.Proposal)
	e.addMetricRemediationTasks(report.TaskPlan, report.RuntimeMetrics)

	// Step 7: Writeback list
	report.WritebackList = e.computeWritebackList(report.Diagnosis, report.Sediment)

	report.DurationMS = time.Since(start).Milliseconds()
	return report
}

// runBenchmark executes the standard benchmark suite against the given model.
// When the model is AOS-capable (id "aos"), use the AOS workflow suite so
// planning/sprint/review/merge observations become reproducible evidence.
func (e *Evolver) runBenchmark(ctx context.Context, model virtualmodel.VirtualModel) *benchmark.Report {
	if model != nil && model.ID() == "aos" {
		report := benchmark.RunAOS(ctx, model, "Diagnose the project and propose the next safe evolution slice.")
		if report != nil {
			return report.Report
		}
	}
	suite := benchmark.NewSuite("evolver-baseline")
	suite.AddTask(benchmark.Task{
		Name: "ping",
		Messages: []virtualmodel.Message{
			{Role: "user", Content: "Hello, respond with a single word."},
		},
		Description: "Minimal round-trip to measure baseline latency and token cost.",
	})
	return suite.Run(ctx, model)
}

// Propose synthesizes diagnosis, sediment, tests, benchmark, and regression into actionable priorities.
func (e *Evolver) Propose(diag *DiagnosisReport, kg *KnowledgeGraph, tests *TestReport, bench *benchmark.Report, reg *RegressionReport, mem *EvolutionMemory, runtimeMetrics ...*RuntimeMetricReport) *Proposal {
	p := &Proposal{}
	priority := 1

	// Error-severity findings are top priority fixes.
	if diag != nil {
		for _, f := range diag.Findings {
			if f.Severity == SeverityError {
				p.Priorities = append(p.Priorities, ProposalItem{
					Priority:  priority,
					Category:  "fix",
					Title:     fmt.Sprintf("Fix %s error: %s", f.Category, truncate(f.Message, 80)),
					Rationale: "Hard constraint violation or broken reference must be resolved before next phase.",
				})
				priority++
			}
		}

		// Warning-severity findings (index drift, missing writeback).
		for _, f := range diag.Findings {
			if f.Severity == SeverityWarning {
				p.Priorities = append(p.Priorities, ProposalItem{
					Priority:  priority,
					Category:  "fix",
					Title:     fmt.Sprintf("Resolve %s drift: %s", f.Category, truncate(f.Message, 80)),
					Rationale: "Handbook-code consistency is the foundation of the living document.",
				})
				priority++
			}
		}
	}

	// Test stage failures (ADR-031) — package health is a hard loop gate.
	if tests != nil && tests.Ran && tests.Failed > 0 {
		for _, pkg := range tests.Packages {
			if pkg.Passed {
				continue
			}
			p.Priorities = append(p.Priorities, ProposalItem{
				Priority:  priority,
				Category:  "fix",
				Title:     fmt.Sprintf("Fix failing package test: %s", pkg.Package),
				Rationale: "Constitutional Test stage failed; package health must be restored before further evolution.",
			})
			priority++
		}
	}

	// Orphan research notes suggest unconverted knowledge.
	if kg != nil && len(kg.OrphanResearch) > 0 {
		p.Priorities = append(p.Priorities, ProposalItem{
			Priority:  priority,
			Category:  "research",
			Title:     fmt.Sprintf("Convert %d orphaned research note(s) into ADRs", len(kg.OrphanResearch)),
			Rationale: "Research without architectural decision is knowledge that hasn't sedimented into the project's decision record.",
		})
		priority++
	}

	// Benchmark regressions (if benchmark was run).
	if bench != nil && bench.Summary.TasksFailed > 0 {
		p.Priorities = append(p.Priorities, ProposalItem{
			Priority:  priority,
			Category:  "optimize",
			Title:     fmt.Sprintf("Investigate %d failed benchmark task(s)", bench.Summary.TasksFailed),
			Rationale: "Benchmark failures indicate a runtime regression that needs root-cause analysis.",
		})
		priority++
	}

	// Baseline regressions (ADR-029): latency/token cost increases vs history.
	if reg != nil && reg.HasBaseline {
		for _, item := range reg.Regressions {
			p.Priorities = append(p.Priorities, ProposalItem{
				Priority:  priority,
				Category:  "optimize",
				Title:     fmt.Sprintf("Fix %s regression: %d -> %d (%d%%)", item.Metric, item.Baseline, item.Current, item.PctDelta),
				Rationale: "Benchmark metric regressed beyond threshold vs stored baseline.",
			})
			priority++
		}
	}

	// Evolution Memory (ADR-033): chronic findings and health trends.
	if mem != nil && mem.Window > 0 {
		current := map[string]bool{}
		if diag != nil {
			for _, f := range diag.Findings {
				if f.Severity == SeverityInfo {
					continue
				}
				current[f.Category+"|"+normalizeFindingMessage(f.Message)] = true
			}
		}
		recurringShown := 0
		for _, r := range mem.Recurring {
			key := r.Category + "|" + r.Message
			stillPresent := current[key]
			if !stillPresent && recurringShown >= 3 {
				continue
			}
			title := fmt.Sprintf("Chronic %s finding (%d/%d): %s", r.Category, r.Count, r.WindowSize, truncate(r.Message, 60))
			rationale := "Evolution memory: this finding recurred across recent reports."
			if stillPresent {
				rationale = "Evolution memory: still present now and recurrent — prioritize permanent fix/writeback automation."
			}
			p.Priorities = append(p.Priorities, ProposalItem{
				Priority:  priority,
				Category:  "fix",
				Title:     title,
				Rationale: rationale,
			})
			priority++
			recurringShown++
			if recurringShown >= 5 {
				break
			}
		}
		if mem.Worsening {
			p.Priorities = append(p.Priorities, ProposalItem{
				Priority:  priority,
				Category:  "optimize",
				Title:     "Health trend worsening across evolution window",
				Rationale: "Newest error/warning totals exceed the oldest snapshot in the memory window.",
			})
			priority++
		}
	}

	// Runtime efficiency surfaces (ADR-044): missing stats/summary helpers become optimize tasks.
	if diag != nil {
		for _, f := range diag.Findings {
			if f.Category == "semantic-constraint" && strings.Contains(f.Message, "runtime-efficiency-surfaces") {
				p.Priorities = append(p.Priorities, ProposalItem{
					Priority:  priority,
					Category:  "optimize",
					Title:     "Restore runtime efficiency surfaces (cache/tool/memory/optimize/forwarder)",
					Rationale: "Evolution proposals prioritize measurable runtime efficiency evidence surfaces.",
				})
				priority++
				break
			}
		}
	}

	// Runtime metric regressions (ADR-045).
	var rm *RuntimeMetricReport
	if len(runtimeMetrics) > 0 {
		rm = runtimeMetrics[0]
	}
	if rm != nil && rm.HasBaseline {
		for _, item := range rm.Regressions {
			p.Priorities = append(p.Priorities, ProposalItem{
				Priority:  priority,
				Category:  "optimize",
				Title:     fmt.Sprintf("Fix runtime metric regression %s: %.4f -> %.4f (%.1f%%)", item.Metric, item.Baseline, item.Current, item.PctDelta),
				Rationale: "Runtime efficiency metric regressed beyond threshold vs stored baseline.",
			})
			priority++
		}
	}

	// Always propose next-phase planning as the last item.
	p.Priorities = append(p.Priorities, ProposalItem{
		Priority:  priority,
		Category:  "implement",
		Title:     "Plan next evolution cycle based on Roadmap priorities",
		Rationale: "The constitution mandates: never stop after coding — proactively plan the next phase.",
	})

	return p
}

// computeWritebackList determines which handbook chapters need updates
// based on diagnosed findings.
func (e *Evolver) computeWritebackList(diag *DiagnosisReport, kg *KnowledgeGraph) []string {
	var list []string

	// If docguard found ADR index problems, chapter 28 needs update.
	for _, f := range diag.Findings {
		if f.Category == "docguard" && (strings.Contains(f.Message, "ADR") || strings.Contains(f.Message, "28")) {
			list = append(list, "28_ADR_Guide.md")
			break
		}
	}
	// If docguard found research index problems, chapter 24 needs update.
	for _, f := range diag.Findings {
		if f.Category == "docguard" && (strings.Contains(f.Message, "research") || strings.Contains(f.Message, "24")) {
			list = append(list, "24_Research_Charter.md")
			break
		}
	}
	// Report catalog drift (ADR-031) → chapter 27.
	for _, f := range diag.Findings {
		if f.Category == "report-index" {
			list = append(list, "27_Benchmark.md")
			break
		}
	}
	// Runtime Catalog drift (ADR-032) → chapter 04 (warnings/errors only).
	for _, f := range diag.Findings {
		if f.Category == "runtime-catalog" && f.Severity != SeverityInfo {
			list = append(list, "04_Runtime_Architecture.md")
			break
		}
	}
	// Foundation table drift (ADR-034) → list unique chapters from messages.
	seenFound := map[string]bool{}
	for _, f := range diag.Findings {
		if f.Category != "foundation-table" || f.Severity == SeverityInfo {
			continue
		}
		// message starts with "<chapter> ["
		ch := f.Message
		if i := strings.Index(ch, " "); i > 0 {
			ch = ch[:i]
		}
		if strings.HasSuffix(ch, ".md") && !seenFound[ch] {
			seenFound[ch] = true
			list = append(list, ch)
		}
	}
	// If code-path warnings found, chapter 04 (Runtime Architecture) may need update.
	for _, f := range diag.Findings {
		if f.Category == "code-path" {
			list = append(list, "04_Runtime_Architecture.md")
			break
		}
	}
	// Knowledge orphans (research without ADR / ADR without research citation)
	// are Proposal items, not handbook writeback. They require judgment and
	// must not pollute the writeback list when indexes are already consistent.
	_ = kg

	// Deduplicate while preserving order.
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// FormatEvolutionReport returns a human-readable evolution report suitable
// for writing to docs/reports/.
func (r *EvolutionReport) FormatEvolutionReport() string {
	var sb strings.Builder
	sb.WriteString("# Evolution Report\n\n")
	sb.WriteString(fmt.Sprintf("Timestamp: %s\n", r.Timestamp.Format("2006-01-02 15:04:05")))
	sb.WriteString(fmt.Sprintf("Duration: %dms\n\n", r.DurationMS))

	sb.WriteString(r.Diagnosis.FormatDiagnosis())
	sb.WriteString("\n")
	sb.WriteString(r.Sediment.FormatSediment())
	sb.WriteString("\n")

	if r.Tests != nil {
		sb.WriteString(r.Tests.FormatTestReport())
		sb.WriteString("\n")
	}

	if r.Memory != nil {
		sb.WriteString(r.Memory.FormatEvolutionMemory())
		sb.WriteString("\n")
	}

	if r.Benchmark != nil {
		sb.WriteString("=== Benchmark ===\n")
		sb.WriteString(r.Benchmark.FormatReport())
		sb.WriteString("\n")
	}

	if r.Regression != nil {
		sb.WriteString("=== Baseline Comparison ===\n")
		sb.WriteString(r.Regression.FormatRegression())
		sb.WriteString("\n")
	}

	sb.WriteString("=== Proposal ===\n")
	for _, item := range r.Proposal.Priorities {
		sb.WriteString(fmt.Sprintf("  %d. [%s] %s\n", item.Priority, item.Category, item.Title))
		sb.WriteString(fmt.Sprintf("     Rationale: %s\n", item.Rationale))
	}
	sb.WriteString("\n")

	if len(r.WritebackList) > 0 {
		sb.WriteString("=== Writeback Required ===\n")
		sb.WriteString("The following handbook chapters need manual updates:\n")
		for _, ch := range r.WritebackList {
			sb.WriteString(fmt.Sprintf("  - %s\n", ch))
		}
	} else {
		sb.WriteString("=== Writeback ===\nNo handbook updates required.\n")
	}

	if r.TaskPlan != nil {
		sb.WriteString("\n")
		sb.WriteString(r.TaskPlan.FormatTaskPlan())
	}
	if r.TaskPlanExecution != nil {
		sb.WriteString("\n")
		sb.WriteString(r.TaskPlanExecution.FormatTaskPlanExecution())
	}
	if r.RuntimeMetrics != nil {
		sb.WriteString("\n")
		sb.WriteString(r.RuntimeMetrics.FormatRuntimeMetrics())
	}
	return sb.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
