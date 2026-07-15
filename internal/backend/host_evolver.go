package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cursor/internal/appdata"
	"cursor/internal/backend/runtime/evolver"
	virtualmodel "cursor/internal/backend/virtualmodel"
	"cursor/internal/docguard"
	"cursor/internal/logger"
)

// runBackgroundEvolutionCheck performs a non-blocking self-evolution cycle after
// Host.Start succeeds:
//
//	Diagnose -> Sediment -> Propose -> Persist
//
// It never blocks serving and never mutates handbook/code (AutoWriteback remains
// CLI/Taskfile opt-in via `task evolver:writeback`). Packaged installs without a
// source checkout are skipped silently.
//
// Design: ADR-028 / ADR-029. Host startup is an automatic evidence producer so
// the closed loop does not require external push.
func (host *Host) runBackgroundEvolutionCheck() {
	if host == nil {
		return
	}
	root, err := resolveRepoRootForEvolution()
	if err != nil {
		logger.Infof("evolver: skip background evolution (%v)", err)
		return
	}

	// Require handbook presence so packaged installs do not spam warnings.
	handbookDir := filepath.Join(root, "docs", "handbook")
	if st, err := os.Stat(handbookDir); err != nil || !st.IsDir() {
		logger.Infof("evolver: skip background evolution (handbook not found under %s)", root)
		return
	}

	// Bound background work so a pathological FS does not hang the process.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := host.exportEvolutionRuntimeMetrics(); err != nil {
		logger.Warnf("evolver: runtime metric export skipped err=%v", err)
	}

	ev := evolver.NewEvolverWithRoot(root)
	report := ev.Evolve(ctx, root, host.preferredEvolutionModel())
	if report == nil || report.Diagnosis == nil {
		logger.Warnf("evolver: background evolution returned empty report root=%s", root)
		return
	}

	result, err := ev.Persist(root, report)
	if err != nil {
		logger.Warnf("evolver: persist failed root=%s err=%v", root, err)
		// Still surface diagnosis even if disk write fails.
		logEvolutionDiagnosis(report)
		return
	}

	logEvolutionDiagnosis(report)
	if result != nil {
		logger.Infof("evolver: persisted markdown=%s json=%s baseline=%v durationMS=%d",
			result.MarkdownPath, result.JSONPath, result.BaselineUpdated, report.DurationMS)
		if len(result.WritebackGuidance) > 0 {
			logger.Warnf("evolver: writeback guidance available (%d item(s)); run `task evolver:writeback` to apply safe index repairs",
				len(result.WritebackGuidance))
			shown := 0
			for _, item := range result.WritebackGuidance {
				logger.Warnf("evolver: writeback [%s] %s: %s", item.Action, item.Chapter, item.Detail)
				shown++
				if shown >= 5 {
					logger.Warnf("evolver: additional writeback items truncated")
					break
				}
			}
		}
	}
}

func (host *Host) exportEvolutionRuntimeMetrics() error {
	snap := host.evolutionRuntimeMetricSnapshot()
	if !hasEvolutionRuntimeMetricEvidence(snap) {
		return nil
	}
	return evolver.WriteRuntimeMetricExports(appdata.DataRootPath(), snap)
}

func (host *Host) evolutionRuntimeMetricSnapshot() *evolver.RuntimeMetricSnapshot {
	snap := &evolver.RuntimeMetricSnapshot{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if host == nil {
		return snap
	}

	host.runtimeMu.RLock()
	cacheRT := host.cacheRuntime
	toolRT := host.toolRuntime
	host.runtimeMu.RUnlock()

	if cacheRT != nil {
		stats := cacheRT.Stats()
		snap.HasCache = true
		snap.CacheHitRate = stats.HitRate
		snap.CacheTokensSaved = stats.TokensSaved
		snap.CacheExactHits = stats.ExactHits
		snap.CacheSemanticHits = stats.SemanticHits
	}
	if toolRT != nil {
		stats := toolRT.CacheStats()
		snap.HasToolCache = true
		snap.ToolCacheHitRate = stats.HitRate
		snap.ToolCacheHits = stats.Hits
		snap.ToolCacheMisses = stats.Misses
	}
	if optRT := host.OptimizationRuntime(); optRT != nil {
		cost := optRT.GetCostSummary()
		snap.HasOptimize = true
		snap.OptimizeSpentUSD = cost.SpentThisMonthUSD
		snap.OptimizeTurns = cost.TurnsThisMonth
		snap.OptimizeBudgetUSD = cost.MonthlyBudgetUSD
	}
	return snap
}

func hasEvolutionRuntimeMetricEvidence(snap *evolver.RuntimeMetricSnapshot) bool {
	return snap != nil && (snap.HasCache || snap.HasToolCache || snap.HasOptimize)
}

func logEvolutionDiagnosis(report *evolver.EvolutionReport) {
	if report == nil || report.Diagnosis == nil {
		return
	}
	diag := report.Diagnosis
	if diag.OK && diag.Warnings == 0 {
		logger.Infof("evolver: HEALTHY errors=%d warnings=%d infos=%d artifacts=%d durationMS=%d",
			diag.Errors, diag.Warnings, diag.Infos, sedimentArtifactCount(report), report.DurationMS)
		return
	}
	logger.Warnf("evolver: ISSUES FOUND errors=%d warnings=%d infos=%d durationMS=%d",
		diag.Errors, diag.Warnings, diag.Infos, report.DurationMS)
	shown := 0
	for _, f := range diag.Findings {
		if f.Severity == evolver.SeverityInfo {
			continue
		}
		logger.Warnf("evolver: [%s] %s: %s", f.Severity, f.Category, f.Message)
		shown++
		if shown >= 8 {
			logger.Warnf("evolver: additional findings truncated; run `task evolver` for full report")
			break
		}
	}
}

func sedimentArtifactCount(report *evolver.EvolutionReport) int {
	if report == nil || report.Sediment == nil {
		return 0
	}
	return report.Sediment.TotalArtifacts
}

func resolveRepoRootForEvolution() (string, error) {
	// Prefer process working directory (dev / task / go run).
	if wd, err := os.Getwd(); err == nil {
		if root, err := docguard.RepoRoot(wd); err == nil {
			return root, nil
		}
	}
	// Fall back to executable directory (useful when launched from IDE tooling).
	if exe, err := os.Executable(); err == nil {
		if root, err := docguard.RepoRoot(filepath.Dir(exe)); err == nil {
			return root, nil
		}
	}
	return "", fmt.Errorf("repository root with go.mod not found")
}

// preferredEvolutionModel returns an enabled AOS model when available, otherwise
// the first enabled virtual model. Background evolution stays non-blocking and
// may still pass nil when no model is configured.
func (host *Host) preferredEvolutionModel() virtualmodel.VirtualModel {
	if host == nil {
		return nil
	}
	host.vmMu.RLock()
	manager := host.vmManager
	host.vmMu.RUnlock()
	if manager == nil {
		return nil
	}
	if model, ok := manager.Get("aos"); ok && model != nil && model.Enabled() {
		return model
	}
	for _, model := range manager.List() {
		if model != nil && model.Enabled() {
			return model
		}
	}
	return nil
}

// evolverPlanningAdvisor injects diagnose-only project health advice into AOS
// Leader planning. It never mutates handbook/code and soft-fails on errors.
type evolverPlanningAdvisor struct{}

func (evolverPlanningAdvisor) AdvisePlanning(ctx context.Context, requirement string) (string, error) {
	_ = requirement
	root, err := resolveRepoRootForEvolution()
	if err != nil {
		return "", nil
	}
	ev := evolver.NewEvolverWithRoot(root)
	// Diagnose + Propose + CompileTaskPlan (no tests/benchmark) for non-blocking advisory.
	report := ev.EvolveWithOptions(ctx, root, nil, evolver.EvolveOptions{})
	if report == nil {
		return "", nil
	}
	var parts []string
	if report.Diagnosis != nil && (report.Diagnosis.Errors > 0 || report.Diagnosis.Warnings > 0) {
		parts = append(parts, fmt.Sprintf("diagnosis errors=%d warnings=%d", report.Diagnosis.Errors, report.Diagnosis.Warnings))
	}
	shown := 0
	if report.Diagnosis != nil {
		for _, finding := range report.Diagnosis.Findings {
			if finding.Severity == evolver.SeverityInfo {
				continue
			}
			parts = append(parts, fmt.Sprintf("[%s] %s: %s", finding.Severity, finding.Category, finding.Message))
			shown++
			if shown >= 3 {
				break
			}
		}
	}
	if report.TaskPlan != nil {
		if advice := strings.TrimSpace(report.TaskPlan.AdvisoryText()); advice != "" {
			parts = append(parts, advice)
		}
	}
	return strings.Join(parts, "\n"), nil
}
