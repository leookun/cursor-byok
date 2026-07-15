package evolver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cursor/internal/appdata"
)

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCompareRuntimeMetrics_DetectsRegressionsAndImprovements(t *testing.T) {
	e := NewEvolver()
	baseline := &RuntimeMetricSnapshot{
		HasCache: true, CacheHitRate: 0.80, CacheTokensSaved: 1000,
		HasToolCache: true, ToolCacheHitRate: 0.50,
		HasOptimize: true, OptimizeSpentUSD: 10,
	}
	current := &RuntimeMetricSnapshot{
		HasCache: true, CacheHitRate: 0.60, CacheTokensSaved: 700, // regressions
		HasToolCache: true, ToolCacheHitRate: 0.70, // improvement
		HasOptimize: true, OptimizeSpentUSD: 15, // regression >20%
	}
	rep := e.CompareRuntimeMetrics(current, baseline)
	if !rep.HasBaseline {
		t.Fatal("expected baseline")
	}
	if len(rep.Regressions) < 2 {
		t.Fatalf("expected regressions, got %+v", rep.Regressions)
	}
	foundHit, foundSpent, foundImprove := false, false, false
	for _, r := range rep.Regressions {
		if r.Metric == "cache_hit_rate" {
			foundHit = true
		}
		if r.Metric == "optimize_spent_usd" {
			foundSpent = true
		}
	}
	for _, r := range rep.Improvements {
		if r.Metric == "tool_cache_hit_rate" {
			foundImprove = true
		}
	}
	if !foundHit || !foundSpent || !foundImprove {
		t.Fatalf("missing expected deltas: hit=%v spent=%v improve=%v report=%+v", foundHit, foundSpent, foundImprove, rep)
	}
}

func TestCollectRuntimeMetrics_FromOverrideAndPersistBaseline(t *testing.T) {
	root := t.TempDir()
	baseDir := filepath.Join(root, "docs", "reports", ".baselines")
	writeJSON(t, filepath.Join(baseDir, "runtime-metrics-current.json"), map[string]any{
		"hasCache": true, "cacheHitRate": 0.9, "cacheTokensSaved": 123,
		"hasToolCache": true, "toolCacheHitRate": 0.4, "toolCacheHits": 4, "toolCacheMisses": 6,
		"hasOptimize": true, "optimizeSpentUSD": 2.5, "optimizeTurns": 5, "optimizeBudgetUSD": 50,
	})
	e := NewEvolver()
	cur := e.CollectRuntimeMetrics(root)
	if cur == nil || !cur.HasCache || !cur.HasToolCache || !cur.HasOptimize {
		t.Fatalf("unexpected snapshot: %+v", cur)
	}
	if err := e.SaveRuntimeMetricBaseline(root, cur); err != nil {
		t.Fatal(err)
	}
	bl := e.LoadRuntimeMetricBaseline(root)
	if bl == nil || bl.CacheHitRate != 0.9 {
		t.Fatalf("baseline not loaded: %+v", bl)
	}
	rep := e.CompareRuntimeMetrics(cur, bl)
	out := rep.FormatRuntimeMetrics()
	if !strings.Contains(out, "Runtime Metrics") || !strings.Contains(out, "cache hitRate") {
		t.Fatalf("format missing content: %s", out)
	}
}

func TestCollectRuntimeMetrics_ConsumesUnifiedRuntimeMetricExport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	root := t.TempDir()
	e := NewEvolver()
	exported := &RuntimeMetricSnapshot{
		HasOptimize:       true,
		OptimizeSpentUSD:  7.25,
		OptimizeTurns:     9,
		OptimizeBudgetUSD: 100,
	}
	if err := WriteRuntimeMetricExports(appdata.DataRootPath(), exported); err != nil {
		t.Fatal(err)
	}

	cur := e.CollectRuntimeMetrics(root)
	if cur == nil || !cur.HasOptimize {
		t.Fatalf("expected unified optimize metrics, got %+v", cur)
	}
	if cur.OptimizeSpentUSD != 7.25 || cur.OptimizeTurns != 9 || cur.OptimizeBudgetUSD != 100 {
		t.Fatalf("unexpected unified optimize metrics: %+v", cur)
	}
}

func TestCollectRuntimeMetrics_RepoOverridePrecedesUnifiedExport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	root := t.TempDir()
	override := &RuntimeMetricSnapshot{
		HasCache:          true,
		CacheHitRate:      0.91,
		CacheTokensSaved:  910,
		HasToolCache:      true,
		ToolCacheHitRate:  0.81,
		ToolCacheHits:     81,
		ToolCacheMisses:   19,
		HasOptimize:       true,
		OptimizeSpentUSD:  1.25,
		OptimizeTurns:     12,
		OptimizeBudgetUSD: 100,
	}
	unified := &RuntimeMetricSnapshot{
		HasCache:          true,
		CacheHitRate:      0.19,
		CacheTokensSaved:  190,
		HasToolCache:      true,
		ToolCacheHitRate:  0.29,
		ToolCacheHits:     29,
		ToolCacheMisses:   71,
		HasOptimize:       true,
		OptimizeSpentUSD:  9.75,
		OptimizeTurns:     97,
		OptimizeBudgetUSD: 200,
	}
	writeJSON(t, filepath.Join(appdata.DataRootPath(), "runtime-metrics", "current.json"), unified)
	writeJSON(t, filepath.Join(root, "docs", "reports", ".baselines", "runtime-metrics-current.json"), override)

	cur := NewEvolver().CollectRuntimeMetrics(root)
	if cur == nil || cur.CacheHitRate != override.CacheHitRate ||
		cur.CacheTokensSaved != override.CacheTokensSaved ||
		cur.ToolCacheHitRate != override.ToolCacheHitRate ||
		cur.ToolCacheHits != override.ToolCacheHits ||
		cur.OptimizeSpentUSD != override.OptimizeSpentUSD ||
		cur.OptimizeTurns != override.OptimizeTurns ||
		cur.OptimizeBudgetUSD != override.OptimizeBudgetUSD {
		t.Fatalf("repo-local override must win over unified export: got %+v, want %+v", cur, override)
	}
}

func TestCompareRuntimeMetrics_EmptySnapshotsDoNotClaimStableBaseline(t *testing.T) {
	e := NewEvolver()
	rep := e.CompareRuntimeMetrics(&RuntimeMetricSnapshot{}, &RuntimeMetricSnapshot{})
	if rep.HasBaseline {
		t.Fatalf("empty snapshots must not count as baseline evidence: %+v", rep)
	}
	out := rep.FormatRuntimeMetrics()
	if strings.Contains(out, "vs baseline: stable") {
		t.Fatalf("empty snapshots must not claim stable baseline: %s", out)
	}
}

func TestPropose_IncludesRuntimeMetricRegressions(t *testing.T) {
	e := NewEvolver()
	rm := &RuntimeMetricReport{
		HasBaseline: true,
		Regressions: []RuntimeMetricRegression{{
			Metric: "cache_hit_rate", Baseline: 0.8, Current: 0.5, Delta: -0.3, PctDelta: -37.5, IsRegression: true,
		}},
	}
	p := e.Propose(nil, &KnowledgeGraph{}, nil, nil, nil, nil, rm)
	found := false
	for _, item := range p.Priorities {
		if strings.Contains(item.Title, "cache_hit_rate") && item.Category == "optimize" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected runtime metric regression proposal, got %+v", p.Priorities)
	}
}
