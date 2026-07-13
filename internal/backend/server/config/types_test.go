package config

import "testing"

func TestNormalizeOptimizationConfig_Defaults(t *testing.T) {
	got := NormalizeOptimizationConfig(OptimizationConfig{})
	if !got.Enabled {
		t.Fatal("expected enabled by default")
	}
	if got.QualityTier != DefaultOptimizationQualityTier {
		t.Fatalf("tier=%s", got.QualityTier)
	}
	if got.MonthlyBudgetUSD != DefaultOptimizationMonthlyBudget {
		t.Fatalf("budget=%v", got.MonthlyBudgetUSD)
	}
}

func TestNormalizeOptimizationConfig_Explicit(t *testing.T) {
	got := NormalizeOptimizationConfig(OptimizationConfig{
		Enabled:          false,
		QualityTier:      "fast",
		MonthlyBudgetUSD: 12.5,
	})
	if got.Enabled {
		t.Fatal("expected disabled")
	}
	if got.QualityTier != "fast" {
		t.Fatalf("tier=%s", got.QualityTier)
	}
	if got.MonthlyBudgetUSD != 12.5 {
		t.Fatalf("budget=%v", got.MonthlyBudgetUSD)
	}
}

func TestNormalizeOptimizationConfig_InvalidTierFallsBack(t *testing.T) {
	got := NormalizeOptimizationConfig(OptimizationConfig{
		Enabled:     true,
		QualityTier: "super",
	})
	if got.QualityTier != DefaultOptimizationQualityTier {
		t.Fatalf("tier=%s", got.QualityTier)
	}
}

func TestNormalizeConfig_PreservesOptimizationAndVirtualModels(t *testing.T) {
	input := DefaultConfig()
	input.Optimization = OptimizationConfig{Enabled: true, QualityTier: "ultra", MonthlyBudgetUSD: 99}
	input.VirtualModels = VirtualModelsConfig{
		MOA: &VirtualModelConfig{Enabled: true, WorkflowID: "moa-default"},
	}
	out, err := NormalizeConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Optimization.QualityTier != "ultra" || out.Optimization.MonthlyBudgetUSD != 99 {
		t.Fatalf("opt=%+v", out.Optimization)
	}
	if out.VirtualModels.MOA == nil || !out.VirtualModels.MOA.Enabled || out.VirtualModels.MOA.WorkflowID != "moa-default" {
		t.Fatalf("vm=%+v", out.VirtualModels)
	}
}
