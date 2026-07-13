package optimize

import (
	"context"
	"testing"
)

func TestAllocateBudget_SplitsWindow(t *testing.T) {
	rt := NewRuntime(50, TierBalanced)
	budget := rt.AllocateBudget(context.Background(), 200_000, "agent")
	if budget == nil {
		t.Fatal("budget is nil")
	}
	if budget.TotalTokens != 200_000 {
		t.Fatalf("TotalTokens=%d", budget.TotalTokens)
	}
	if budget.OutputTokens <= 0 || budget.HistoryTokens <= 0 {
		t.Fatalf("expected positive history/output: %+v", budget)
	}
	used := budget.SystemPromptTokens + budget.RulesTokens + budget.MemoryTokens + budget.ToolsTokens +
		budget.HistoryTokens + budget.OutputTokens + budget.RemainingTokens
	// remaining can absorb rounding; total allocated pieces should not exceed window by much
	if used > budget.TotalTokens+10 {
		t.Fatalf("allocated %d > total %d", used, budget.TotalTokens)
	}
}

func TestAllocateBudget_NilRuntime(t *testing.T) {
	var rt *Runtime
	budget := rt.AllocateBudget(context.Background(), 1000, "ask")
	if budget.TotalTokens != 1000 {
		t.Fatalf("TotalTokens=%d", budget.TotalTokens)
	}
}

func TestEstimateCost_ModelIDSubstring(t *testing.T) {
	rt := NewRuntime(50, TierBalanced)
	// known table key via model id substring
	sonnet := rt.EstimateCost("anthropic/claude-sonnet-4-20250514", 1000, 1000)
	defaultCost := rt.EstimateCost("unknown-model-xyz", 1000, 1000)
	// sonnet table: 0.003 in + 0.015 out per 1k = 0.018
	if sonnet < 0.017 || sonnet > 0.019 {
		t.Fatalf("sonnet cost=%v want ~0.018", sonnet)
	}
	// unknown falls back to sonnet rates currently
	if defaultCost != sonnet {
		t.Fatalf("default=%v sonnet=%v", defaultCost, sonnet)
	}
	mini := rt.EstimateCost("gpt-4o-mini-2024-07-18", 1000, 1000)
	if mini >= sonnet {
		t.Fatalf("mini=%v should be cheaper than sonnet=%v", mini, sonnet)
	}
}

func TestMatchProviderCostKey(t *testing.T) {
	cases := map[string]string{
		"claude-sonnet":              "claude-sonnet",
		"Claude-Sonnet-4":            "claude-sonnet",
		"gpt-4o-mini":                "gpt-4o-mini",
		"openai/gpt-4o":              "gpt-4o",
		"deepseek-chat":              "deepseek",
		"gemini-2.0-flash":           "gemini-flash",
		"":                           "",
		"completely-unknown-model":   "",
	}
	for in, want := range cases {
		got := MatchProviderCostKey(in)
		if got != want {
			t.Errorf("MatchProviderCostKey(%q)=%q want %q", in, got, want)
		}
	}
}

func TestSelectOptimalProvider_Tiers(t *testing.T) {
	providers := []string{"claude-opus", "gpt-4o-mini", "claude-haiku"}

	fast := NewRuntime(50, TierFast)
	got := fast.SelectOptimalProvider(context.Background(), "coding", providers)
	if MatchProviderCostKey(got) != "gpt-4o-mini" && MatchProviderCostKey(got) != "claude-haiku" {
		// cheapest among table should be haiku or mini depending on rates
		// mini input 0.00015, haiku 0.0008 → mini wins
		if MatchProviderCostKey(got) != "gpt-4o-mini" {
			t.Fatalf("fast tier got %q", got)
		}
	}

	ultra := NewRuntime(50, TierUltra)
	got = ultra.SelectOptimalProvider(context.Background(), "coding", providers)
	if MatchProviderCostKey(got) != "claude-opus" {
		t.Fatalf("ultra tier got %q want claude-opus", got)
	}

	balanced := NewRuntime(50, TierBalanced)
	got = balanced.SelectOptimalProvider(context.Background(), "research", providers)
	if MatchProviderCostKey(got) != "gpt-4o-mini" {
		t.Fatalf("balanced research got %q want cheapest", got)
	}
}

func TestRecordCost_AndSummary(t *testing.T) {
	rt := NewRuntime(10, TierBalanced)
	rt.RecordCost("gpt-4o-mini", 1000, 1000)
	sum := rt.GetCostSummary()
	if sum.TurnsThisMonth != 1 {
		t.Fatalf("turns=%d", sum.TurnsThisMonth)
	}
	if sum.SpentThisMonthUSD <= 0 {
		t.Fatalf("spent=%v", sum.SpentThisMonthUSD)
	}
	if sum.EstimatedRemainingTurns <= 0 {
		t.Fatalf("remaining turns=%d", sum.EstimatedRemainingTurns)
	}
}

func TestSetQualityTierAndBudget(t *testing.T) {
	rt := NewRuntime(50, TierBalanced)
	rt.SetQualityTier(TierUltra)
	if rt.QualityTier() != TierUltra {
		t.Fatalf("tier=%s", rt.QualityTier())
	}
	rt.SetMonthlyBudgetUSD(100)
	if rt.GetCostSummary().MonthlyBudgetUSD != 100 {
		t.Fatalf("budget=%v", rt.GetCostSummary().MonthlyBudgetUSD)
	}
}

func TestSetEnabled_DisablesBudgetOverride(t *testing.T) {
	rt := NewRuntime(50, TierBalanced)
	rt.SetEnabled(false)
	if rt.Enabled() {
		t.Fatal("expected disabled")
	}
	budget := rt.AllocateBudget(context.Background(), 100_000, "agent")
	if budget.OutputTokens != 0 {
		t.Fatalf("disabled OutputTokens=%d want 0", budget.OutputTokens)
	}
	// Select returns first available
	got := rt.SelectOptimalProvider(context.Background(), "coding", []string{"claude-opus", "gpt-4o-mini"})
	if got != "claude-opus" {
		t.Fatalf("got %q", got)
	}
}

func TestAllocateBudgetWithEstimate_BoostsOutputWhenPromptSmall(t *testing.T) {
	rt := NewRuntime(50, TierBalanced)
	// Small prompt vs large window → history slot shrinks, output grows vs no-estimate
	base := rt.AllocateBudget(context.Background(), 100_000, "agent")
	withEst := rt.AllocateBudgetWithEstimate(context.Background(), 100_000, "agent", 8_000)
	if withEst.OutputTokens <= base.OutputTokens {
		t.Fatalf("expected estimate to boost output: base=%d withEst=%d hist=%d", base.OutputTokens, withEst.OutputTokens, withEst.HistoryTokens)
	}
	if withEst.HistoryTokens > base.HistoryTokens {
		t.Fatalf("history should not grow: base=%d with=%d", base.HistoryTokens, withEst.HistoryTokens)
	}
	// Output must not exceed window - prompt - safety
	if withEst.OutputTokens > 100_000-8_000-1024 {
		t.Fatalf("output exceeds remaining window: %d", withEst.OutputTokens)
	}
}

func TestSelectOptimalCandidate_FromAdapterPool(t *testing.T) {
	candidates := []ProviderCandidate{
		{Key: "adapter-opus", CostHint: "claude-opus-4"},
		{Key: "adapter-mini", CostHint: "gpt-4o-mini"},
		{Key: "adapter-haiku", CostHint: "claude-haiku"},
	}

	fast := NewRuntime(50, TierFast)
	got := fast.SelectOptimalCandidate(context.Background(), "coding", candidates)
	if got != "adapter-mini" {
		t.Fatalf("fast tier got %q want adapter-mini (cheapest)", got)
	}

	ultra := NewRuntime(50, TierUltra)
	got = ultra.SelectOptimalCandidate(context.Background(), "coding", candidates)
	if got != "adapter-opus" {
		t.Fatalf("ultra tier got %q want adapter-opus", got)
	}

	// Disabled → first candidate key
	fast.SetEnabled(false)
	got = fast.SelectOptimalCandidate(context.Background(), "coding", candidates)
	if got != "adapter-opus" {
		t.Fatalf("disabled got %q want first key", got)
	}
}

func TestSelectOptimalCandidate_EmptyPool(t *testing.T) {
	rt := NewRuntime(50, TierFast)
	if got := rt.SelectOptimalCandidate(context.Background(), "coding", nil); got != "" {
		t.Fatalf("got %q", got)
	}
}
