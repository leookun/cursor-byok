// persist.go implements the Evolution Report & Baseline Persistence layer
// (ADR-029). It writes every EvolutionReport to disk as both human-readable
// Markdown and machine-readable JSON, and maintains a benchmark baseline
// for regression detection.
//
// Design: ADR-029. Depends on: ADR-028 (Evolver), ADR-020 (benchmark).
// Read-only: writes only to docs/reports/ and docs/reports/.baselines/.
// Never modifies handbook chapters or source code.
package evolver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cursor/internal/benchmark"
)

// BenchmarkBaseline is the persisted slice of benchmark.Summary needed for
// regression comparison. Only the stable comparison fields are stored so
// schema changes to Report do not break historical baselines.
type BenchmarkBaseline struct {
	SuiteName      string `json:"suiteName"`
	Timestamp      string `json:"timestamp"` // RFC3339 of the Evolve run
	AvgLatencyMS   int64  `json:"avgLatencyMS"`
	TotalTokens    int    `json:"totalTokens"`
	TasksTotal     int    `json:"tasksTotal"`
	TasksSucceeded int    `json:"tasksSucceeded"`
	TasksFailed    int    `json:"tasksFailed"`
}

// RegressionReport holds the outcome of comparing a current benchmark run
// against the stored baseline.
type RegressionReport struct {
	HasBaseline  bool             `json:"hasBaseline"`
	Regressions  []RegressionItem `json:"regressions"`
	Improvements []RegressionItem `json:"improvements"`
}

// RegressionItem describes a single metric delta vs baseline.
type RegressionItem struct {
	Metric   string `json:"metric"` // "latency", "tokens", "failures"
	Baseline int64  `json:"baseline"`
	Current  int64  `json:"current"`
	Delta    int64  `json:"delta"`    // current - baseline
	PctDelta int    `json:"pctDelta"` // percentage change, 0 if baseline is 0
}

// WritebackItem extends the bare chapter-name writeback list with the specific
// action needed and a human-readable detail. This makes guidance actionable
// without auto-fix (ADR-029 design constraint).
type WritebackItem struct {
	Chapter string `json:"chapter"` // e.g., "28_ADR_Guide.md"
	Action  string `json:"action"`  // e.g., "add-adr-index"
	Detail  string `json:"detail"`  // human-readable description of the drift
}

// PersistResult is the output of Persist().
type PersistResult struct {
	MarkdownPath                  string          `json:"markdownPath"`
	JSONPath                      string          `json:"jsonPath"`
	BaselineUpdated               bool            `json:"baselineUpdated"`
	RuntimeMetricsBaselineUpdated bool            `json:"runtimeMetricsBaselineUpdated,omitempty"`
	RetentionDeleted              int             `json:"retentionDeleted,omitempty"`
	WritebackGuidance             []WritebackItem `json:"writebackGuidance"`
}

const baselinesDir = "docs/reports/.baselines"
const latestBaselineFile = "latest-benchmark.json"

// Persist writes the EvolutionReport to disk as Markdown + JSON, optionally
// updates the benchmark baseline, and computes actionable writeback guidance.
// It is idempotent: calling with the same report rewrites the same files.
func (e *Evolver) Persist(repoRoot string, report *EvolutionReport) (*PersistResult, error) {
	if report == nil {
		return nil, fmt.Errorf("cannot persist nil report")
	}

	result := &PersistResult{}
	dateStr := report.Timestamp.Format("2006-01-02")
	seq := e.nextSequence(repoRoot, dateStr)

	// 1. Write Markdown report.
	mdName := fmt.Sprintf("%s-evolution-%02d.md", dateStr, seq)
	mdPath := filepath.Join(repoRoot, "docs", "reports", mdName)
	mdContent := report.FormatEvolutionReport()
	mdContent += "\n## Reproduction\n\n```bash\ngo run ./cmd/evolver/\n```\n"
	if err := os.WriteFile(mdPath, []byte(mdContent), 0o644); err != nil {
		return nil, fmt.Errorf("write markdown report: %w", err)
	}
	result.MarkdownPath = mdPath

	// 2. Write JSON snapshot (full EvolutionReport).
	jsonName := fmt.Sprintf("evolution-%s-%02d.json", dateStr, seq)
	jsonPath := filepath.Join(repoRoot, baselinesDir, jsonName)
	if err := os.MkdirAll(filepath.Dir(jsonPath), 0o755); err != nil {
		return nil, fmt.Errorf("create baselines dir: %w", err)
	}
	jsonData, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal evolution report: %w", err)
	}
	if err := os.WriteFile(jsonPath, jsonData, 0o644); err != nil {
		return nil, fmt.Errorf("write json snapshot: %w", err)
	}
	result.JSONPath = jsonPath

	// 2b. Compact old evolution snapshots (ADR-036). Best-effort; never fails Persist.
	if rr, err := e.CompactBaselines(repoRoot, DefaultBaselineRetention); err == nil && rr != nil {
		result.RetentionDeleted = rr.Deleted
	}
	if rr, err := e.CompactEvolutionReports(repoRoot, DefaultReportRetention); err == nil && rr != nil {
		result.RetentionDeleted += rr.Deleted
	}

	// 3. Update benchmark baseline (if benchmark ran).
	if report.Benchmark != nil {
		bl := baselineFromReport(report)
		blPath := filepath.Join(repoRoot, baselinesDir, latestBaselineFile)
		blData, err := json.MarshalIndent(bl, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal baseline: %w", err)
		}
		if err := os.WriteFile(blPath, blData, 0o644); err != nil {
			return nil, fmt.Errorf("write baseline: %w", err)
		}
		result.BaselineUpdated = true
	}

	// 3b. Update runtime efficiency metric baseline (ADR-045).
	if report.RuntimeMetrics != nil && hasRuntimeMetricEvidence(report.RuntimeMetrics.Current) {
		if err := e.SaveRuntimeMetricBaseline(repoRoot, report.RuntimeMetrics.Current); err == nil {
			result.RuntimeMetricsBaselineUpdated = true
		}
	}

	// 4. Compute writeback guidance.
	result.WritebackGuidance = e.computeWritebackGuidance(report.Diagnosis, report.Sediment)

	return result, nil
}

// LoadBaseline reads the latest benchmark baseline from disk.
// Returns nil if no baseline exists (first run).
func (e *Evolver) LoadBaseline(repoRoot string) *BenchmarkBaseline {
	path := filepath.Join(repoRoot, baselinesDir, latestBaselineFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var bl BenchmarkBaseline
	if err := json.Unmarshal(data, &bl); err != nil {
		return nil
	}
	return &bl
}

// CompareWithBaseline compares the current benchmark summary against the
// stored baseline and flags regressions and improvements.
func (e *Evolver) CompareWithBaseline(current *benchmark.Summary, baseline *BenchmarkBaseline) *RegressionReport {
	r := &RegressionReport{HasBaseline: baseline != nil}
	if baseline == nil || current == nil {
		return r
	}

	// Latency: regression if current > baseline * 1.20 (20% slower).
	blLat := baseline.AvgLatencyMS
	curLat := current.AvgLatencyMS
	if blLat > 0 {
		delta := curLat - blLat
		pct := int(float64(delta) / float64(blLat) * 100)
		if delta > 0 {
			item := RegressionItem{Metric: "latency", Baseline: blLat, Current: curLat, Delta: delta, PctDelta: pct}
			if float64(curLat) > float64(blLat)*1.20 {
				r.Regressions = append(r.Regressions, item)
			} else {
				r.Improvements = append(r.Improvements, item)
			}
		} else if delta < 0 {
			r.Improvements = append(r.Improvements, RegressionItem{
				Metric: "latency", Baseline: blLat, Current: curLat, Delta: delta, PctDelta: pct,
			})
		}
	}

	// Tokens: regression if current > baseline * 1.15 (15% more).
	blTok := int64(baseline.TotalTokens)
	curTok := int64(current.TotalTokens)
	if blTok > 0 {
		delta := curTok - blTok
		pct := int(float64(delta) / float64(blTok) * 100)
		if delta > 0 {
			item := RegressionItem{Metric: "tokens", Baseline: blTok, Current: curTok, Delta: delta, PctDelta: pct}
			if float64(curTok) > float64(blTok)*1.15 {
				r.Regressions = append(r.Regressions, item)
			} else {
				r.Improvements = append(r.Improvements, item)
			}
		} else if delta < 0 {
			r.Improvements = append(r.Improvements, RegressionItem{
				Metric: "tokens", Baseline: blTok, Current: curTok, Delta: delta, PctDelta: pct,
			})
		}
	}

	// Failures: regression if new failures appear or failures increase.
	blFail := int64(baseline.TasksFailed)
	curFail := int64(current.TasksFailed)
	if curFail > blFail {
		r.Regressions = append(r.Regressions, RegressionItem{
			Metric: "failures", Baseline: blFail, Current: curFail, Delta: curFail - blFail,
		})
	} else if curFail < blFail {
		r.Improvements = append(r.Improvements, RegressionItem{
			Metric: "failures", Baseline: blFail, Current: curFail, Delta: curFail - blFail,
		})
	}

	return r
}

// computeWritebackGuidance produces specific, actionable writeback items from
// diagnosis findings and sediment gaps. This extends computeWritebackList()
// with per-item action and detail.
func (e *Evolver) computeWritebackGuidance(diag *DiagnosisReport, kg *KnowledgeGraph) []WritebackItem {
	var items []WritebackItem
	seen := map[string]bool{} // dedupe by chapter+action

	add := func(chapter, action, detail string) {
		key := chapter + "|" + action
		if seen[key] {
			return
		}
		seen[key] = true
		items = append(items, WritebackItem{Chapter: chapter, Action: action, Detail: detail})
	}

	for _, f := range diag.Findings {
		switch {
		case f.Category == "docguard" && (strings.Contains(f.Message, "ADR") || strings.Contains(f.Message, "28")):
			add("28_ADR_Guide.md", "add-adr-index", f.Message)
		case f.Category == "docguard" && (strings.Contains(f.Message, "research") || strings.Contains(f.Message, "24")):
			add("24_Research_Charter.md", "add-research-index", f.Message)
		case f.Category == "report-index":
			add("27_Benchmark.md", "add-report-index", f.Message)
		case f.Category == "runtime-catalog" && f.Severity != SeverityInfo:
			add("04_Runtime_Architecture.md", "sync-runtime-catalog", f.Message)
		case f.Category == "foundation-table" && f.Severity != SeverityInfo:
			// Chapter is embedded in the message; action heals all foundation tables.
			add("foundation-tables", "sync-foundation-tables", f.Message)
		case f.Category == "foundation-bullet" && f.Severity != SeverityInfo:
			add("bullet-foundations", "sync-bullet-foundations", f.Message)
		case f.Category == "code-path":
			add("04_Runtime_Architecture.md", "fix-code-path", f.Message)
		case f.Category == "hard-constraint" && f.Severity == SeverityError:
			add("00_Project_Constitution.md", "fix-hard-constraint", f.Message)
		case f.Category == "semantic-constraint" && f.Severity == SeverityError:
			add("00_Project_Constitution.md", "fix-semantic-constraint", f.Message)
		}
	}
	// Orphan research/ADR gaps are knowledge-graph proposals, not deterministic
	// handbook writebacks. Keep them out of guidance so AutoWriteback only sees
	// mechanical index/path/constraint drift.
	_ = kg
	return items
}

// nextSequence finds the next sequence number for a given date to avoid
// overwriting existing reports.
func (e *Evolver) nextSequence(repoRoot, dateStr string) int {
	reportsDir := filepath.Join(repoRoot, "docs", "reports")
	entries, err := os.ReadDir(reportsDir)
	if err != nil {
		return 1
	}
	maxSeq := 0
	prefix := dateStr + "-evolution-"
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".md") {
			continue
		}
		// Extract the NN from "<date>-evolution-NN.md".
		seqStr := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".md")
		var seq int
		if _, err := fmt.Sscanf(seqStr, "%d", &seq); err == nil && seq > maxSeq {
			maxSeq = seq
		}
	}
	return maxSeq + 1
}

// baselineFromReport extracts a BenchmarkBaseline from an EvolutionReport.
func baselineFromReport(report *EvolutionReport) *BenchmarkBaseline {
	if report.Benchmark == nil {
		return nil
	}
	s := report.Benchmark.Summary
	return &BenchmarkBaseline{
		SuiteName:      report.Benchmark.SuiteName,
		Timestamp:      report.Timestamp.Format(time.RFC3339),
		AvgLatencyMS:   s.AvgLatencyMS,
		TotalTokens:    s.TotalTokens,
		TasksTotal:     s.TasksTotal,
		TasksSucceeded: s.TasksSucceeded,
		TasksFailed:    s.TasksFailed,
	}
}

// FormatRegression returns a human-readable regression report.
func (r *RegressionReport) FormatRegression() string {
	if !r.HasBaseline {
		return "No prior baseline — first run establishes the baseline.\n"
	}
	var sb strings.Builder
	if len(r.Regressions) == 0 && len(r.Improvements) == 0 {
		sb.WriteString("No regressions or improvements vs baseline.\n")
		return sb.String()
	}
	if len(r.Regressions) > 0 {
		sb.WriteString("Regressions vs baseline:\n")
		for _, item := range r.Regressions {
			sb.WriteString(fmt.Sprintf("  - %s: %d -> %d (%d, %d%%)\n",
				item.Metric, item.Baseline, item.Current, item.Delta, item.PctDelta))
		}
	}
	if len(r.Improvements) > 0 {
		sb.WriteString("Improvements vs baseline:\n")
		for _, item := range r.Improvements {
			sb.WriteString(fmt.Sprintf("  - %s: %d -> %d (%d, %d%%)\n",
				item.Metric, item.Baseline, item.Current, item.Delta, item.PctDelta))
		}
	}
	return sb.String()
}

// FormatWritebackGuidance returns a human-readable writeback guidance list.
func (pr *PersistResult) FormatWritebackGuidance() string {
	if len(pr.WritebackGuidance) == 0 {
		return "No writeback required.\n"
	}
	var sb strings.Builder
	sb.WriteString("Writeback guidance (advisory, not auto-applied):\n")
	for _, item := range pr.WritebackGuidance {
		sb.WriteString(fmt.Sprintf("  - %s [%s]: %s\n", item.Chapter, item.Action, item.Detail))
	}
	return sb.String()
}
