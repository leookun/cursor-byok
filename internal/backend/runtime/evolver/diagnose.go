// Package evolver implements the Self-Evolution Runtime: an orchestration
// engine that runs the constitutionally-mandated closed loop
// (Research -> ADR -> Implementation -> Test -> Benchmark -> Documentation)
// and diagnoses contradictions between handbook, code, and artifacts.
//
// Design: ADR-028. Research: docs/research/self-evolution-runtime.md.
// Handbook: docs/handbook/00_Project_Constitution.md (Autonomous Development Loop).
package evolver

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"cursor/internal/docguard"
)

// Severity classifies a diagnosis finding.
type Severity string

const (
	SeverityError   Severity = "error"   // hard constraint violation or broken reference
	SeverityWarning Severity = "warning" // index drift or missing writeback
	SeverityInfo    Severity = "info"    // observation, not a defect
)

// Finding is a single diagnosed issue.
type Finding struct {
	Severity Severity `json:"severity"`
	Category string   `json:"category"` // "docguard", "code-path", "hard-constraint", "roadmap"
	Message  string   `json:"message"`
}

// DiagnosisReport is the output of Diagnose().
type DiagnosisReport struct {
	OK       bool      `json:"ok"`
	Findings []Finding `json:"findings"`
	// Summary counts by severity.
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Infos    int `json:"infos"`
}

// codePathPattern matches backtick-quoted relative paths like `internal/backend/`
// or `internal/backend/runtime/optimize/` in handbook markdown.
var codePathPattern = regexp.MustCompile("`internal/[a-zA-Z0-9_/.-]+`")

// Diagnose runs the full diagnosis pipeline against repoRoot.
// It delegates the core consistency check to docguard, then adds:
//   - code-path verification (do paths referenced in handbook chapters exist?)
//   - hard-constraint spot-check (ChannelService non-nil in host.go production path)
//   - roadmap status extraction (Phase statuses from chapter 29)
func (e *Evolver) Diagnose(repoRoot string) *DiagnosisReport {
	report := &DiagnosisReport{OK: true}

	// 1. Delegate to docguard for handbook/ADR/research index consistency.
	dgResult := docguard.CheckHandbookConsistency(repoRoot)
	for _, p := range dgResult.Problems {
		report.add(SeverityWarning, "docguard", p)
	}

	// 2. Verify code paths referenced in handbook chapters exist on disk.
	e.diagnoseCodePaths(repoRoot, report)

	// 3. Hard-constraint spot-check: ChannelService non-nil in host.go.
	e.diagnoseHardConstraints(repoRoot, report)

	// 4. Roadmap status extraction.
	e.diagnoseRoadmap(repoRoot, report)

	// 5. Report catalog drift vs chapter 27 (ADR-031).
	e.diagnoseReportIndex(repoRoot, report)

	// 6. Runtime Catalog maturity drift vs chapter 04 (ADR-032).
	e.diagnoseRuntimeCatalog(repoRoot, report)

	// 7. Per-chapter foundation tables (ADR-034).
	e.diagnoseFoundationTables(repoRoot, report)

	// 8. Bullet-style foundations (ADR-036).
	e.diagnoseBulletFoundations(repoRoot, report)

	// 9. Baseline retention pressure (ADR-036).
	e.diagnoseBaselineRetention(repoRoot, report)

	// 10. Semantic constitutional constraints (ADR-037).
	e.diagnoseSemanticConstraints(repoRoot, report)

	// 11. Markdown evolution report retention (ADR-040).
	e.diagnoseReportRetention(repoRoot, report)

	report.OK = report.Errors == 0
	return report
}

// diagnoseCodePaths scans handbook chapters for `internal/...` path references
// and checks whether those directories exist on disk.
func (e *Evolver) diagnoseCodePaths(repoRoot string, report *DiagnosisReport) {
	handbookDir := filepath.Join(repoRoot, "docs", "handbook")
	entries, err := os.ReadDir(handbookDir)
	if err != nil {
		report.add(SeverityError, "code-path", "cannot read handbook dir: "+err.Error())
		return
	}

	seen := map[string]bool{} // deduplicate paths across chapters
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(handbookDir, entry.Name()))
		if err != nil {
			continue
		}
		for _, m := range codePathPattern.FindAllString(string(content), -1) {
			relPath := strings.Trim(m, "`")
			if seen[relPath] {
				continue
			}
			seen[relPath] = true
			// Strip trailing slash for directory check.
			checkPath := strings.TrimSuffix(relPath, "/")
			fullPath := filepath.Join(repoRoot, filepath.FromSlash(checkPath))
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				report.add(SeverityWarning, "code-path",
					fmt.Sprintf("handbook references path %s but it does not exist on disk", relPath))
			}
		}
	}
}

// diagnoseHardConstraints spot-checks that production construction paths
// do not inject nil ChannelService. It scans host.go for the guard pattern
// `if channelResolver != nil` before constructing AdapterChannelService.
func (e *Evolver) diagnoseHardConstraints(repoRoot string, report *DiagnosisReport) {
	hostPath := filepath.Join(repoRoot, "internal", "backend", "host.go")
	content, err := os.ReadFile(hostPath)
	if err != nil {
		report.add(SeverityInfo, "hard-constraint", "cannot read host.go for spot-check: "+err.Error())
		return
	}
	text := string(content)

	// Production path must:
	// 1) construct AdapterChannelService via NewAdapterChannelService
	// 2) nil-guard channelResolver for both MOA and AOS assembly
	// 3) call buildVirtualModelManager with host.configs (non-nil resolver in production)
	if !strings.Contains(text, "NewAdapterChannelService") {
		report.add(SeverityInfo, "hard-constraint", "host.go does not reference NewAdapterChannelService (may be refactored)")
		return
	}
	guardCount := strings.Count(text, "if channelResolver != nil")
	if guardCount == 0 {
		report.add(SeverityError, "hard-constraint",
			"host.go constructs AdapterChannelService without nil-guard on channelResolver — violates ChannelService non-nil rule")
	} else if guardCount < 2 {
		// MOA and AOS each need a production guard.
		report.add(SeverityWarning, "hard-constraint",
			fmt.Sprintf("host.go has only %d channelResolver nil-guard(s); expected >=2 for MOA+AOS production paths", guardCount))
	}
	if !strings.Contains(text, "buildVirtualModelManager(&cfg, optRuntime, host.configs)") {
		report.add(SeverityError, "hard-constraint",
			"rebuildLocked does not pass host.configs as channelResolver to buildVirtualModelManager — production ChannelService path may be nil")
	}
}

// diagnoseRoadmap extracts Phase statuses from chapter 29 and reports them
// as info-level findings for the evolution report.
func (e *Evolver) diagnoseRoadmap(repoRoot string, report *DiagnosisReport) {
	roadmapPath := filepath.Join(repoRoot, "docs", "handbook", "29_Roadmap.md")
	content, err := os.ReadFile(roadmapPath)
	if err != nil {
		report.add(SeverityInfo, "roadmap", "cannot read 29_Roadmap.md: "+err.Error())
		return
	}
	text := string(content)

	// Count phase statuses. The roadmap uses markers like:
	// Phase N (已完成 / 进行中 / 计划中)
	// or English equivalents.
	for _, marker := range []string{"已完成", "进行中", "计划中"} {
		count := strings.Count(text, marker)
		if count > 0 {
			report.add(SeverityInfo, "roadmap",
				fmt.Sprintf("roadmap phases marked %q: %d", marker, count))
		}
	}
}

// diagnoseReportIndex checks whether evolution/baseline reports under
// docs/reports/ are mentioned in chapter 27 (ADR-031).
func (e *Evolver) diagnoseReportIndex(repoRoot string, report *DiagnosisReport) {
	benchPath := filepath.Join(repoRoot, "docs", "handbook", "27_Benchmark.md")
	content, err := os.ReadFile(benchPath)
	if err != nil {
		report.add(SeverityInfo, "report-index", "cannot read 27_Benchmark.md: "+err.Error())
		return
	}
	text := string(content)

	reportsDir := filepath.Join(repoRoot, "docs", "reports")
	entries, err := os.ReadDir(reportsDir)
	if err != nil {
		report.add(SeverityInfo, "report-index", "cannot list docs/reports: "+err.Error())
		return
	}

	missing := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		name := entry.Name()
		if !strings.Contains(name, "evolution") && !strings.Contains(name, "evolver") && !strings.Contains(name, "baseline") {
			continue
		}
		if !strings.Contains(text, name) {
			missing++
			if missing <= 3 {
				report.add(SeverityWarning, "report-index",
					fmt.Sprintf("report %s not indexed in 27_Benchmark.md §27.8", name))
			}
		}
	}
	if missing > 3 {
		report.add(SeverityWarning, "report-index",
			fmt.Sprintf("%d additional report catalog entries missing from chapter 27", missing-3))
	}
}

// add appends a finding and updates summary counters.
func (r *DiagnosisReport) add(sev Severity, category, msg string) {
	r.Findings = append(r.Findings, Finding{
		Severity: sev,
		Category: category,
		Message:  msg,
	})
	switch sev {
	case SeverityError:
		r.Errors++
	case SeverityWarning:
		r.Warnings++
	case SeverityInfo:
		r.Infos++
	}
}

// FormatDiagnosis returns a human-readable diagnosis report.
func (r *DiagnosisReport) FormatDiagnosis() string {
	var sb strings.Builder
	sb.WriteString("=== Diagnosis Report ===\n")
	sb.WriteString(fmt.Sprintf("Status: %s\n", statusWord(r.OK)))
	sb.WriteString(fmt.Sprintf("Errors: %d  Warnings: %d  Info: %d\n\n", r.Errors, r.Warnings, r.Infos))

	// Sort findings by severity (errors first).
	sorted := make([]Finding, len(r.Findings))
	copy(sorted, r.Findings)
	sort.Slice(sorted, func(i, j int) bool {
		return sevRank(sorted[i].Severity) < sevRank(sorted[j].Severity)
	})

	for _, f := range sorted {
		sb.WriteString(fmt.Sprintf("  [%s] %s: %s\n", f.Severity, f.Category, f.Message))
	}
	return sb.String()
}

func statusWord(ok bool) string {
	if ok {
		return "HEALTHY"
	}
	return "ISSUES FOUND"
}

func sevRank(s Severity) int {
	switch s {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	case SeverityInfo:
		return 2
	default:
		return 3
	}
}
