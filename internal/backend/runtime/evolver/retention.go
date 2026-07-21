// retention.go implements baseline JSON retention/compaction (ADR-036).
//
// Evolution snapshots under docs/reports/.baselines/evolution-*.json grow on
// every Persist. Memory analysis only needs a recent window (DefaultMemoryWindow),
// so older snapshots can be compacted after Persist without losing the living loop.
package evolver

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultBaselineRetention is how many newest evolution-*.json snapshots to keep.
// Must be >= DefaultMemoryWindow so trend analysis remains fully populated.
const DefaultBaselineRetention = 24

// DefaultReportRetention is how many newest evolution-*.md reports to keep under docs/reports.
// Phase/manual reports are never deleted; only dated evolution reports are compacted.
const DefaultReportRetention = 40

// RetentionResult records compaction outcomes.
type RetentionResult struct {
	Kept    int      `json:"kept"`
	Deleted int      `json:"deleted"`
	Files   []string `json:"deletedFiles,omitempty"`
}

// CompactBaselines deletes oldest evolution-*.json snapshots beyond keepN.
// keepN <= 0 uses DefaultBaselineRetention. latest-benchmark.json is never touched.
// Deletion is best-effort and deterministic (lexical oldest first for date-seq names).
func (e *Evolver) CompactBaselines(repoRoot string, keepN int) (*RetentionResult, error) {
	if keepN <= 0 {
		keepN = DefaultBaselineRetention
	}
	// Never compact below the memory window; otherwise trend analysis starves.
	if keepN < DefaultMemoryWindow {
		keepN = DefaultMemoryWindow
	}
	result := &RetentionResult{}
	dir := filepath.Join(repoRoot, baselinesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, fmt.Errorf("read baselines dir: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "evolution-") && strings.HasSuffix(name, ".json") {
			names = append(names, name)
		}
	}
	sort.Strings(names) // oldest first for date-seq naming
	result.Kept = len(names)
	if len(names) <= keepN {
		return result, nil
	}
	obsolete := names[:len(names)-keepN]
	keptNames := names[len(names)-keepN:]
	result.Kept = len(keptNames)
	for _, name := range obsolete {
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil {
			// Soft-fail individual deletes; continue compacting the rest.
			continue
		}
		result.Deleted++
		result.Files = append(result.Files, name)
	}
	return result, nil
}

// diagnoseBaselineRetention emits an info/warning about snapshot growth.
func (e *Evolver) diagnoseBaselineRetention(repoRoot string, report *DiagnosisReport) {
	dir := filepath.Join(repoRoot, baselinesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "evolution-") && strings.HasSuffix(name, ".json") {
			count++
		}
	}
	if count > DefaultBaselineRetention {
		report.add(SeverityWarning, "baseline-retention",
			fmt.Sprintf("%d evolution snapshots exceed retention %d; compact on Persist", count, DefaultBaselineRetention))
		return
	}
	report.add(SeverityInfo, "baseline-retention",
		fmt.Sprintf("evolution snapshots within retention: %d/%d", count, DefaultBaselineRetention))
}

// CompactEvolutionReports deletes oldest dated evolution markdown reports beyond keepN.
// Keeps files matching YYYY-MM-DD-evolution-NN.md only. Phase reports and baselines remain.
func (e *Evolver) CompactEvolutionReports(repoRoot string, keepN int) (*RetentionResult, error) {
	if keepN <= 0 {
		keepN = DefaultReportRetention
	}
	if keepN < DefaultMemoryWindow {
		keepN = DefaultMemoryWindow
	}
	result := &RetentionResult{}
	dir := filepath.Join(repoRoot, "docs", "reports")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, fmt.Errorf("read reports dir: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Strict: 2026-07-15-evolution-01.md
		if len(name) >= len("2006-01-02-evolution-01.md") &&
			strings.Contains(name, "-evolution-") &&
			strings.HasSuffix(name, ".md") &&
			!strings.Contains(name, "phase") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	result.Kept = len(names)
	if len(names) <= keepN {
		return result, nil
	}
	obsolete := names[:len(names)-keepN]
	result.Kept = keepN
	for _, name := range obsolete {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			continue
		}
		result.Deleted++
		result.Files = append(result.Files, name)
	}
	return result, nil
}

// diagnoseReportRetention emits retention pressure for markdown evolution reports.
func (e *Evolver) diagnoseReportRetention(repoRoot string, report *DiagnosisReport) {
	dir := filepath.Join(repoRoot, "docs", "reports")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.Contains(name, "-evolution-") && strings.HasSuffix(name, ".md") && !strings.Contains(name, "phase") {
			count++
		}
	}
	if count > DefaultReportRetention {
		report.add(SeverityWarning, "report-retention",
			fmt.Sprintf("%d evolution markdown reports exceed retention %d; compact on Persist", count, DefaultReportRetention))
		return
	}
	report.add(SeverityInfo, "report-retention",
		fmt.Sprintf("evolution markdown reports within retention: %d/%d", count, DefaultReportRetention))
}
