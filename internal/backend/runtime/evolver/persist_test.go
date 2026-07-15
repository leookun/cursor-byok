package evolver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cursor/internal/benchmark"
)

func TestPersist_WritesMarkdownAndJSON(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "reports"), 0o755); err != nil {
		t.Fatal(err)
	}

	e := NewEvolver()
	report := &EvolutionReport{
		Timestamp:  time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
		DurationMS: 42,
		Diagnosis: &DiagnosisReport{
			OK: true,
			Findings: []Finding{
				{Severity: SeverityInfo, Category: "roadmap", Message: "phases marked done: 3"},
			},
			Infos: 1,
		},
		Sediment: &KnowledgeGraph{
			TotalArtifacts: 2,
			ADRs:           []Artifact{{Type: "adr", Filename: "028-self-evolution-runtime.md", ADRID: "028"}},
			ResearchNotes:  []Artifact{{Type: "research", Filename: "self-evolution-runtime.md"}},
		},
		Proposal: &Proposal{
			Priorities: []ProposalItem{
				{Priority: 1, Category: "implement", Title: "Plan next evolution cycle", Rationale: "constitution"},
			},
		},
	}

	result, err := e.Persist(root, report)
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if result.MarkdownPath == "" || result.JSONPath == "" {
		t.Fatalf("expected markdown and json paths, got %+v", result)
	}
	if _, err := os.Stat(result.MarkdownPath); err != nil {
		t.Fatalf("markdown missing: %v", err)
	}
	if _, err := os.Stat(result.JSONPath); err != nil {
		t.Fatalf("json missing: %v", err)
	}
	if result.BaselineUpdated {
		t.Fatal("baseline should not update when no benchmark ran")
	}

	md, err := os.ReadFile(result.MarkdownPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "Evolution Report") {
		t.Error("markdown missing Evolution Report title")
	}
	if !strings.Contains(string(md), "Reproduction") {
		t.Error("markdown missing Reproduction footer")
	}
	if !strings.Contains(string(md), "go run ./cmd/evolver/") {
		t.Error("markdown missing reproduction command")
	}

	raw, err := os.ReadFile(result.JSONPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded EvolutionReport
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if decoded.DurationMS != 42 {
		t.Fatalf("decoded duration = %d, want 42", decoded.DurationMS)
	}
}

func TestPersist_UpdatesBaselineWhenBenchmarkPresent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "reports"), 0o755); err != nil {
		t.Fatal(err)
	}

	e := NewEvolver()
	report := &EvolutionReport{
		Timestamp: time.Date(2026, 7, 14, 13, 0, 0, 0, time.UTC),
		Diagnosis: &DiagnosisReport{OK: true},
		Sediment:  &KnowledgeGraph{},
		Proposal:  &Proposal{},
		Benchmark: &benchmark.Report{
			SuiteName: "evolver-baseline",
			Summary: benchmark.Summary{
				AvgLatencyMS:   100,
				TotalTokens:    50,
				TasksTotal:     1,
				TasksSucceeded: 1,
				TasksFailed:    0,
			},
		},
	}

	result, err := e.Persist(root, report)
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if !result.BaselineUpdated {
		t.Fatal("expected baseline update when benchmark present")
	}

	bl := e.LoadBaseline(root)
	if bl == nil {
		t.Fatal("expected baseline on disk")
	}
	if bl.AvgLatencyMS != 100 || bl.TotalTokens != 50 {
		t.Fatalf("unexpected baseline: %+v", bl)
	}
}

func TestPersist_DoesNotUpdateRuntimeMetricBaselineWithoutEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "reports"), 0o755); err != nil {
		t.Fatal(err)
	}

	e := NewEvolver()
	report := &EvolutionReport{
		Timestamp:      time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC),
		Diagnosis:      &DiagnosisReport{OK: true},
		Sediment:       &KnowledgeGraph{},
		Proposal:       &Proposal{},
		RuntimeMetrics: &RuntimeMetricReport{Current: &RuntimeMetricSnapshot{}},
	}

	result, err := e.Persist(root, report)
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if result.RuntimeMetricsBaselineUpdated {
		t.Fatal("empty runtime metric snapshot must not update baseline")
	}
	if bl := e.LoadRuntimeMetricBaseline(root); bl != nil {
		t.Fatalf("empty runtime metric snapshot must not load as baseline: %+v", bl)
	}
	if _, err := os.Stat(filepath.Join(root, baselinesDir, latestRuntimeMetricsFile)); !os.IsNotExist(err) {
		t.Fatalf("runtime metric baseline should not be written, stat err=%v", err)
	}
}

func TestLoadBaseline_Missing(t *testing.T) {
	e := NewEvolver()
	if bl := e.LoadBaseline(t.TempDir()); bl != nil {
		t.Fatalf("expected nil baseline, got %+v", bl)
	}
}

func TestCompareWithBaseline_RegressionsAndImprovements(t *testing.T) {
	e := NewEvolver()
	baseline := &BenchmarkBaseline{
		AvgLatencyMS: 100,
		TotalTokens:  100,
		TasksFailed:  0,
	}

	current := &benchmark.Summary{
		AvgLatencyMS: 130,
		TotalTokens:  120,
		TasksFailed:  1,
	}
	reg := e.CompareWithBaseline(current, baseline)
	if !reg.HasBaseline {
		t.Fatal("expected HasBaseline")
	}
	if len(reg.Regressions) != 3 {
		t.Fatalf("expected 3 regressions, got %d: %+v", len(reg.Regressions), reg.Regressions)
	}

	improved := &benchmark.Summary{
		AvgLatencyMS: 80,
		TotalTokens:  90,
		TasksFailed:  0,
	}
	reg2 := e.CompareWithBaseline(improved, &BenchmarkBaseline{
		AvgLatencyMS: 100,
		TotalTokens:  100,
		TasksFailed:  1,
	})
	if len(reg2.Regressions) != 0 {
		t.Fatalf("expected no regressions, got %+v", reg2.Regressions)
	}
	if len(reg2.Improvements) < 2 {
		t.Fatalf("expected improvements, got %+v", reg2.Improvements)
	}

	reg3 := e.CompareWithBaseline(current, nil)
	if reg3.HasBaseline {
		t.Fatal("expected HasBaseline=false")
	}
}

func TestComputeWritebackGuidance_SpecificActions(t *testing.T) {
	e := NewEvolver()
	diag := &DiagnosisReport{
		Findings: []Finding{
			{Severity: SeverityWarning, Category: "docguard", Message: "ADR-029 on disk but missing from 28 index paths"},
			{Severity: SeverityWarning, Category: "docguard", Message: "research note foo.md on disk but not listed in 24"},
			{Severity: SeverityWarning, Category: "code-path", Message: "handbook references internal/foo/ but it does not exist"},
			{Severity: SeverityError, Category: "hard-constraint", Message: "ChannelService is nil"},
		},
	}
	kg := &KnowledgeGraph{
		OrphanADR:      []string{"001"},
		OrphanResearch: []string{"orphan.md"},
	}

	items := e.computeWritebackGuidance(diag, kg)
	if len(items) < 3 {
		t.Fatalf("expected actionable writeback items, got %d: %+v", len(items), items)
	}

	found := map[string]bool{}
	for _, item := range items {
		found[item.Chapter+"|"+item.Action] = true
		if item.Detail == "" {
			t.Errorf("empty detail for %+v", item)
		}
	}
	for _, key := range []string{
		"28_ADR_Guide.md|add-adr-index",
		"24_Research_Charter.md|add-research-index",
		"04_Runtime_Architecture.md|fix-code-path",
		"00_Project_Constitution.md|fix-hard-constraint",
	} {
		if !found[key] {
			t.Errorf("missing writeback guidance key %s", key)
		}
	}
	// Orphans must not appear as writeback guidance.
	if found["28_ADR_Guide.md|add-research-citation"] || found["24_Research_Charter.md|convert-or-link-research"] {
		t.Fatalf("orphan knowledge gaps must stay out of writeback guidance: %+v", items)
	}
}

func TestPersist_NilReport(t *testing.T) {
	e := NewEvolver()
	if _, err := e.Persist(t.TempDir(), nil); err == nil {
		t.Fatal("expected error for nil report")
	}
}

func TestNextSequence_Increments(t *testing.T) {
	root := t.TempDir()
	reportsDir := filepath.Join(root, "docs", "reports")
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(reportsDir, "2026-07-14-evolution-01.md"), []byte("a"), 0o644)
	_ = os.WriteFile(filepath.Join(reportsDir, "2026-07-14-evolution-02.md"), []byte("b"), 0o644)

	e := NewEvolver()
	seq := e.nextSequence(root, "2026-07-14")
	if seq != 3 {
		t.Fatalf("nextSequence = %d, want 3", seq)
	}
}

func TestEvolve_ThenPersist_RealRepo(t *testing.T) {
	root := testRepoRoot(t)
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "docs", "reports"), 0o755); err != nil {
		t.Fatal(err)
	}

	e := NewEvolver()
	report := e.Evolve(context.Background(), root, nil)
	if report == nil || report.Diagnosis == nil || report.Sediment == nil {
		t.Fatal("incomplete evolve report")
	}

	result, err := e.Persist(tmp, report)
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if result.MarkdownPath == "" {
		t.Fatal("missing markdown path")
	}
	if _, err := os.Stat(result.MarkdownPath); err != nil {
		t.Fatalf("markdown not written: %v", err)
	}
	_ = result.FormatWritebackGuidance()
}

func TestFormatRegression_AndWritebackGuidance(t *testing.T) {
	r := &RegressionReport{
		HasBaseline: true,
		Regressions: []RegressionItem{
			{Metric: "latency", Baseline: 100, Current: 150, Delta: 50, PctDelta: 50},
		},
		Improvements: []RegressionItem{
			{Metric: "tokens", Baseline: 100, Current: 80, Delta: -20, PctDelta: -20},
		},
	}
	out := r.FormatRegression()
	if !strings.Contains(out, "Regressions") || !strings.Contains(out, "Improvements") {
		t.Fatalf("unexpected format: %s", out)
	}

	pr := &PersistResult{
		WritebackGuidance: []WritebackItem{
			{Chapter: "28_ADR_Guide.md", Action: "add-adr-index", Detail: "ADR-029 missing"},
		},
	}
	g := pr.FormatWritebackGuidance()
	if !strings.Contains(g, "28_ADR_Guide.md") || !strings.Contains(g, "add-adr-index") {
		t.Fatalf("unexpected guidance: %s", g)
	}
}
