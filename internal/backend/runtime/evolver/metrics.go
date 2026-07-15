// metrics.go implements runtime efficiency metric baselines and regressions (ADR-045).
//
// Benchmark baselines already track latency/tokens. This layer tracks operational
// efficiency surfaces created by prior recipes:
//
//	cache hit-rate / tokens saved
//	tool cache hit-rate
//	optimize spent / turns
//
// Collection is best-effort and never fails Evolve. Missing sources simply omit
// metrics; first evidence-bearing snapshot becomes the baseline.
package evolver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cursor/internal/appdata"
	"cursor/internal/backend/runtime/optimize"
)

const latestRuntimeMetricsFile = "latest-runtime-metrics.json"

// RuntimeMetricSnapshot is a compact efficiency snapshot for evolution evidence.
type RuntimeMetricSnapshot struct {
	Timestamp string `json:"timestamp"`

	// Cache metrics (from cache stats.json when available).
	CacheHitRate      float64 `json:"cacheHitRate,omitempty"`
	CacheTokensSaved  int64   `json:"cacheTokensSaved,omitempty"`
	CacheExactHits    int64   `json:"cacheExactHits,omitempty"`
	CacheSemanticHits int64   `json:"cacheSemanticHits,omitempty"`
	HasCache          bool    `json:"hasCache"`

	// Tool cache metrics (optional in-memory export file when present).
	ToolCacheHitRate float64 `json:"toolCacheHitRate,omitempty"`
	ToolCacheHits    int64   `json:"toolCacheHits,omitempty"`
	ToolCacheMisses  int64   `json:"toolCacheMisses,omitempty"`
	HasToolCache     bool    `json:"hasToolCache"`

	// Optimize cost metrics (from cost_tracker.json when available).
	OptimizeSpentUSD  float64 `json:"optimizeSpentUSD,omitempty"`
	OptimizeTurns     int64   `json:"optimizeTurns,omitempty"`
	OptimizeBudgetUSD float64 `json:"optimizeBudgetUSD,omitempty"`
	HasOptimize       bool    `json:"hasOptimize"`
}

// RuntimeMetricRegression is one efficiency metric delta vs baseline.
type RuntimeMetricRegression struct {
	Metric       string  `json:"metric"`
	Baseline     float64 `json:"baseline"`
	Current      float64 `json:"current"`
	Delta        float64 `json:"delta"`
	PctDelta     float64 `json:"pctDelta"`
	IsRegression bool    `json:"isRegression"`
}

// RuntimeMetricReport is the comparison outcome for runtime efficiency metrics.
type RuntimeMetricReport struct {
	HasBaseline  bool                      `json:"hasBaseline"`
	Current      *RuntimeMetricSnapshot    `json:"current,omitempty"`
	Baseline     *RuntimeMetricSnapshot    `json:"baseline,omitempty"`
	Regressions  []RuntimeMetricRegression `json:"regressions,omitempty"`
	Improvements []RuntimeMetricRegression `json:"improvements,omitempty"`
}

// CollectRuntimeMetrics best-effort loads live efficiency metrics from known stores.
// It also accepts optional repo-local override:
//
//	docs/reports/.baselines/runtime-metrics-current.json
func (e *Evolver) CollectRuntimeMetrics(repoRoot string) *RuntimeMetricSnapshot {
	snap := &RuntimeMetricSnapshot{Timestamp: time.Now().UTC().Format(time.RFC3339)}

	// 1) Optional explicit current snapshot for CI/fixtures.
	if override := loadRuntimeMetricSnapshot(filepath.Join(repoRoot, baselinesDir, "runtime-metrics-current.json")); override != nil {
		override.Timestamp = snap.Timestamp
		return override
	}

	// 2) Production unified current snapshot exported by host/runtime wiring.
	dataRoot := strings.TrimSpace(appdata.DataRootPath())
	if unified := loadRuntimeMetricSnapshot(filepath.Join(dataRoot, "runtime-metrics", "current.json")); unified != nil {
		unified.Timestamp = snap.Timestamp
		return unified
	}

	// 3) Cache stats.json under appdata data root conventions.
	// Common layout: <dataRoot>/cache/stats.json (best-effort; path may vary by host wiring).
	for _, candidate := range []string{
		filepath.Join(dataRoot, "stats.json"), // host currently constructs cache.Runtime with DataRootPath()
		filepath.Join(dataRoot, "cache", "stats.json"),
		filepath.Join(dataRoot, "runtime", "cache", "stats.json"),
		filepath.Join(repoRoot, "testdata", "runtime-metrics", "cache-stats.json"),
	} {
		if loadCacheStatsInto(candidate, snap) {
			break
		}
	}

	// 4) Tool cache optional export.
	for _, candidate := range []string{
		filepath.Join(dataRoot, "tool", "cache_stats.json"),
		filepath.Join(repoRoot, "testdata", "runtime-metrics", "tool-cache-stats.json"),
	} {
		if loadToolCacheStatsInto(candidate, snap) {
			break
		}
	}

	// 5) Optimize cost tracker.
	costPath := optimize.DefaultCostStorePath()
	if loadOptimizeCostInto(costPath, snap) {
		// ok
	} else {
		// fixture fallback
		_ = loadOptimizeCostInto(filepath.Join(repoRoot, "testdata", "runtime-metrics", "cost_tracker.json"), snap)
	}

	return snap
}

func hasRuntimeMetricEvidence(snap *RuntimeMetricSnapshot) bool {
	return snap != nil && (snap.HasCache || snap.HasToolCache || snap.HasOptimize)
}

func loadRuntimeMetricSnapshot(path string) *RuntimeMetricSnapshot {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var snap RuntimeMetricSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil
	}
	return &snap
}

func loadCacheStatsInto(path string, snap *RuntimeMetricSnapshot) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var raw struct {
		ExactHits    int64   `json:"exactHits"`
		SemanticHits int64   `json:"semanticHits"`
		TotalHits    int64   `json:"totalHits"`
		TotalMisses  int64   `json:"totalMisses"`
		HitRate      float64 `json:"hitRate"`
		TokensSaved  int64   `json:"tokensSaved"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	snap.HasCache = true
	snap.CacheExactHits = raw.ExactHits
	snap.CacheSemanticHits = raw.SemanticHits
	snap.CacheTokensSaved = raw.TokensSaved
	if raw.HitRate > 0 {
		snap.CacheHitRate = raw.HitRate
	} else if raw.TotalHits+raw.TotalMisses > 0 {
		snap.CacheHitRate = float64(raw.TotalHits) / float64(raw.TotalHits+raw.TotalMisses)
	}
	return true
}

func loadToolCacheStatsInto(path string, snap *RuntimeMetricSnapshot) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var raw struct {
		Hits    int64   `json:"hits"`
		Misses  int64   `json:"misses"`
		HitRate float64 `json:"hitRate"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	snap.HasToolCache = true
	snap.ToolCacheHits = raw.Hits
	snap.ToolCacheMisses = raw.Misses
	if raw.HitRate > 0 {
		snap.ToolCacheHitRate = raw.HitRate
	} else if raw.Hits+raw.Misses > 0 {
		snap.ToolCacheHitRate = float64(raw.Hits) / float64(raw.Hits+raw.Misses)
	}
	return true
}

func loadOptimizeCostInto(path string, snap *RuntimeMetricSnapshot) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	// Support both CostSnapshot and richer tracker-like JSON.
	var raw struct {
		SpentThisMonthUSD float64 `json:"spentThisMonthUSD"`
		TurnsThisMonth    int64   `json:"turnsThisMonth"`
		MonthlyBudgetUSD  float64 `json:"monthlyBudgetUSD"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	// Empty file / zero object still counts as "has source" only if any field present.
	// Treat successful decode as hasOptimize so first baseline can form.
	snap.HasOptimize = true
	snap.OptimizeSpentUSD = raw.SpentThisMonthUSD
	snap.OptimizeTurns = raw.TurnsThisMonth
	snap.OptimizeBudgetUSD = raw.MonthlyBudgetUSD
	return true
}

// LoadRuntimeMetricBaseline reads the latest runtime efficiency baseline.
func (e *Evolver) LoadRuntimeMetricBaseline(repoRoot string) *RuntimeMetricSnapshot {
	snap := loadRuntimeMetricSnapshot(filepath.Join(repoRoot, baselinesDir, latestRuntimeMetricsFile))
	if !hasRuntimeMetricEvidence(snap) {
		return nil
	}
	return snap
}

// SaveRuntimeMetricBaseline persists the current snapshot as latest baseline.
func (e *Evolver) SaveRuntimeMetricBaseline(repoRoot string, snap *RuntimeMetricSnapshot) error {
	if snap == nil {
		return fmt.Errorf("nil runtime metric snapshot")
	}
	if !hasRuntimeMetricEvidence(snap) {
		return fmt.Errorf("runtime metric snapshot has no evidence source")
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, baselinesDir), 0o755); err != nil {
		return err
	}
	if strings.TrimSpace(snap.Timestamp) == "" {
		snap.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(repoRoot, baselinesDir, latestRuntimeMetricsFile), data, 0o644)
}

// CompareRuntimeMetrics compares current vs baseline efficiency metrics.
//
// Regressions:
//   - cache hit-rate drop > 10% relative (or > 0.05 absolute)
//   - tool cache hit-rate drop > 10% relative (or > 0.05 absolute)
//   - optimize spent increase > 20% relative (when baseline spent > 0)
//   - cache tokensSaved drop > 15% relative
func (e *Evolver) CompareRuntimeMetrics(current, baseline *RuntimeMetricSnapshot) *RuntimeMetricReport {
	hasCurrentEvidence := hasRuntimeMetricEvidence(current)
	hasBaselineEvidence := hasRuntimeMetricEvidence(baseline)
	report := &RuntimeMetricReport{
		HasBaseline: hasCurrentEvidence && hasBaselineEvidence,
		Current:     current,
		Baseline:    baseline,
	}
	if !hasCurrentEvidence || !hasBaselineEvidence {
		return report
	}

	// Higher is better.
	compareHigher := func(metric string, cur, bl float64, has bool) {
		if !has || bl == 0 && cur == 0 {
			return
		}
		delta := cur - bl
		pct := 0.0
		if bl != 0 {
			pct = delta / bl * 100
		}
		item := RuntimeMetricRegression{Metric: metric, Baseline: bl, Current: cur, Delta: delta, PctDelta: pct}
		// regression if drop exceeds thresholds
		absDrop := bl - cur
		relDrop := 0.0
		if bl > 0 {
			relDrop = absDrop / bl
		}
		if absDrop > 0.05 || relDrop > 0.10 {
			item.IsRegression = true
			report.Regressions = append(report.Regressions, item)
			return
		}
		if delta > 0 {
			report.Improvements = append(report.Improvements, item)
		}
	}

	// Lower is better for spent.
	compareLower := func(metric string, cur, bl float64, has bool) {
		if !has {
			return
		}
		delta := cur - bl
		pct := 0.0
		if bl != 0 {
			pct = delta / bl * 100
		}
		item := RuntimeMetricRegression{Metric: metric, Baseline: bl, Current: cur, Delta: delta, PctDelta: pct}
		if bl > 0 && cur > bl*1.20 {
			item.IsRegression = true
			report.Regressions = append(report.Regressions, item)
			return
		}
		if delta < 0 {
			report.Improvements = append(report.Improvements, item)
		}
	}

	if current.HasCache && baseline.HasCache {
		compareHigher("cache_hit_rate", current.CacheHitRate, baseline.CacheHitRate, true)
		// tokens saved: higher better
		compareHigher("cache_tokens_saved", float64(current.CacheTokensSaved), float64(baseline.CacheTokensSaved), true)
	}
	if current.HasToolCache && baseline.HasToolCache {
		compareHigher("tool_cache_hit_rate", current.ToolCacheHitRate, baseline.ToolCacheHitRate, true)
	}
	if current.HasOptimize && baseline.HasOptimize {
		compareLower("optimize_spent_usd", current.OptimizeSpentUSD, baseline.OptimizeSpentUSD, true)
	}
	return report
}

// FormatRuntimeMetrics returns a human-readable efficiency section.
func (r *RuntimeMetricReport) FormatRuntimeMetrics() string {
	if r == nil || r.Current == nil {
		return "=== Runtime Metrics ===\nNo runtime metrics collected.\n"
	}
	var b strings.Builder
	b.WriteString("=== Runtime Metrics ===\n")
	c := r.Current
	if c.HasCache {
		b.WriteString(fmt.Sprintf("  cache hitRate=%.4f exactHits=%d semanticHits=%d tokensSaved=%d\n",
			c.CacheHitRate, c.CacheExactHits, c.CacheSemanticHits, c.CacheTokensSaved))
	} else {
		b.WriteString("  cache: n/a\n")
	}
	if c.HasToolCache {
		b.WriteString(fmt.Sprintf("  tool-cache hitRate=%.4f hits=%d misses=%d\n",
			c.ToolCacheHitRate, c.ToolCacheHits, c.ToolCacheMisses))
	} else {
		b.WriteString("  tool-cache: n/a\n")
	}
	if c.HasOptimize {
		b.WriteString(fmt.Sprintf("  optimize spent=%.4f turns=%d budget=%.4f\n",
			c.OptimizeSpentUSD, c.OptimizeTurns, c.OptimizeBudgetUSD))
	} else {
		b.WriteString("  optimize: n/a\n")
	}
	if r.HasBaseline {
		if len(r.Regressions) == 0 && len(r.Improvements) == 0 {
			b.WriteString("  vs baseline: stable\n")
		}
		for _, item := range r.Regressions {
			b.WriteString(fmt.Sprintf("  regression %s: %.4f -> %.4f (%+.1f%%)\n",
				item.Metric, item.Baseline, item.Current, item.PctDelta))
		}
		for _, item := range r.Improvements {
			b.WriteString(fmt.Sprintf("  improvement %s: %.4f -> %.4f (%+.1f%%)\n",
				item.Metric, item.Baseline, item.Current, item.PctDelta))
		}
	} else {
		b.WriteString("  vs baseline: none (first snapshot establishes baseline)\n")
	}
	return b.String()
}

// WriteRuntimeMetricExports writes host-exported metric files into the standard
// data-root locations consumed by CollectRuntimeMetrics (ADR-046).
// All writes are best-effort and never return hard failures to callers.
func WriteRuntimeMetricExports(dataRoot string, snap *RuntimeMetricSnapshot) error {
	if snap == nil {
		return fmt.Errorf("nil snapshot")
	}
	dataRoot = strings.TrimSpace(dataRoot)
	if dataRoot == "" {
		return fmt.Errorf("empty data root")
	}
	// Always write the unified current snapshot used by evolver collection.
	unified := filepath.Join(dataRoot, "runtime-metrics", "current.json")
	if err := os.MkdirAll(filepath.Dir(unified), 0o755); err == nil {
		if data, err := json.MarshalIndent(snap, "", "  "); err == nil {
			_ = os.WriteFile(unified, data, 0o644)
		}
	}
	// Tool cache export path expected by CollectRuntimeMetrics.
	if snap.HasToolCache {
		toolPath := filepath.Join(dataRoot, "tool", "cache_stats.json")
		if err := os.MkdirAll(filepath.Dir(toolPath), 0o755); err == nil {
			payload := map[string]any{
				"hits":    snap.ToolCacheHits,
				"misses":  snap.ToolCacheMisses,
				"hitRate": snap.ToolCacheHitRate,
			}
			if data, err := json.MarshalIndent(payload, "", "  "); err == nil {
				_ = os.WriteFile(toolPath, data, 0o644)
			}
		}
	}
	// Cache stats path variants for collectors.
	if snap.HasCache {
		for _, p := range []string{
			filepath.Join(dataRoot, "stats.json"),
			filepath.Join(dataRoot, "cache", "stats.json"),
		} {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				continue
			}
			payload := map[string]any{
				"exactHits":    snap.CacheExactHits,
				"semanticHits": snap.CacheSemanticHits,
				"totalHits":    snap.CacheExactHits + snap.CacheSemanticHits,
				"hitRate":      snap.CacheHitRate,
				"tokensSaved":  snap.CacheTokensSaved,
			}
			if data, err := json.MarshalIndent(payload, "", "  "); err == nil {
				_ = os.WriteFile(p, data, 0o644)
			}
		}
	}
	return nil
}

// BuildMetricRemediationTasks maps runtime metric regressions to executable TaskPlan items.
func (e *Evolver) BuildMetricRemediationTasks(report *RuntimeMetricReport) []EvolutionTask {
	if report == nil || !report.HasBaseline || len(report.Regressions) == 0 {
		return nil
	}
	var tasks []EvolutionTask
	seen := map[string]bool{}
	for i, item := range report.Regressions {
		title, action := remediationForMetric(item.Metric)
		if title == "" {
			continue
		}
		if seen[action+"|"+title] {
			continue
		}
		seen[action+"|"+title] = true
		role := "implementation"
		if action == "auto-writeback" {
			role = "docs"
		}
		tasks = append(tasks, EvolutionTask{
			ID:         fmt.Sprintf("remediate-%d", i+1),
			Role:       role,
			Title:      title,
			Category:   "optimize",
			Priority:   i + 1,
			Rationale:  fmt.Sprintf("Runtime metric %s regressed %.4f -> %.4f (%.1f%%)", item.Metric, item.Baseline, item.Current, item.PctDelta),
			Acceptance: "Metric regression addressed by allowlisted remediation recipe and package tests pass.",
			Action:     action,
		})
	}
	return tasks
}

func remediationForMetric(metric string) (title, action string) {
	switch strings.ToLower(strings.TrimSpace(metric)) {
	case "cache_hit_rate", "cache_tokens_saved":
		return "Restore cache efficiency surface and hit-rate evidence", "bounded-code-fix"
	case "tool_cache_hit_rate":
		return "Restore tool cache stats runtime surface", "bounded-code-fix"
	case "optimize_spent_usd":
		return "Restore optimize cost budget surface", "bounded-code-fix"
	default:
		return "", ""
	}
}
