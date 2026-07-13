package moa

import (
	"context"
	"testing"

	optimize "cursor/internal/backend/runtime/optimize"
	vmconfig "cursor/internal/backend/virtualmodel/config"
)

type stubChannelService struct {
	byID map[string]*ChannelInfo
}

func (s *stubChannelService) ResolveChannel(_ context.Context, adapterID string) (*ChannelInfo, error) {
	if s == nil || s.byID == nil {
		return &ChannelInfo{ID: adapterID, ModelID: adapterID, Provider: adapterID}, nil
	}
	if info, ok := s.byID[adapterID]; ok {
		return info, nil
	}
	return &ChannelInfo{ID: adapterID, ModelID: adapterID, Provider: adapterID}, nil
}

func (s *stubChannelService) CallAdapter(_ context.Context, _ *ChannelInfo, _ []Message, _ string) (*AdapterResult, error) {
	return &AdapterResult{Text: "ok", PromptTokens: 10, CompletionTokens: 5}, nil
}

func TestSelectAdapterIDForRole_UsesPoolAndTier(t *testing.T) {
	cfg := &vmconfig.VirtualModelConfig{
		Enabled: true,
		Planner: &vmconfig.NodeBindingConfig{AdapterID: "ad-opus", Enabled: true},
		Nodes: map[string]*vmconfig.NodeBindingConfig{
			"coding":   {AdapterID: "ad-mini", Enabled: true},
			"research": {AdapterID: "ad-haiku", Enabled: true},
		},
	}
	svc := &stubChannelService{byID: map[string]*ChannelInfo{
		"ad-opus":  {ID: "ad-opus", ModelID: "claude-opus-4", Provider: "anthropic", DisplayName: "Opus"},
		"ad-mini":  {ID: "ad-mini", ModelID: "gpt-4o-mini", Provider: "openai", DisplayName: "Mini"},
		"ad-haiku": {ID: "ad-haiku", ModelID: "claude-haiku", Provider: "anthropic", DisplayName: "Haiku"},
	}}
	opt := optimize.NewRuntime(50, optimize.TierFast)
	m := NewMOAModelWithOptimize(cfg, vmconfig.DefaultMOAWorkflow(), svc, opt)

	// Fast tier should prefer cheapest model among configured adapters
	got := m.selectAdapterIDForRole(context.Background(), vmconfig.RoleCoding)
	if got != "ad-mini" {
		t.Fatalf("coding under fast tier got %q want ad-mini", got)
	}

	// Preferred role binding still participates in pool; ultra prefers opus
	opt.SetQualityTier(optimize.TierUltra)
	got = m.selectAdapterIDForRole(context.Background(), vmconfig.RoleResearch)
	if got != "ad-opus" {
		t.Fatalf("research under ultra got %q want ad-opus", got)
	}

	// Disabled optimize → role preferred binding
	opt.SetEnabled(false)
	got = m.selectAdapterIDForRole(context.Background(), vmconfig.RoleCoding)
	if got != "ad-mini" {
		t.Fatalf("disabled want preferred coding binding ad-mini, got %q", got)
	}
}

func TestCollectAdapterCandidates_OnlyFromConfigBindings(t *testing.T) {
	cfg := &vmconfig.VirtualModelConfig{
		Enabled: true,
		Planner: &vmconfig.NodeBindingConfig{AdapterID: "a1", Enabled: true},
		Nodes: map[string]*vmconfig.NodeBindingConfig{
			"coding": {AdapterID: "a2", Enabled: true},
			"skip":   {AdapterID: "a3", Enabled: false},
		},
	}
	m := NewMOAModelWithOptimize(cfg, nil, &stubChannelService{}, optimize.NewRuntime(50, optimize.TierBalanced))
	cands := m.collectAdapterCandidates(context.Background())
	keys := map[string]bool{}
	for _, c := range cands {
		keys[c.Key] = true
	}
	if !keys["a1"] || !keys["a2"] {
		t.Fatalf("expected a1,a2 in pool: %+v", cands)
	}
	if keys["a3"] {
		t.Fatal("disabled binding a3 should not be in pool when other enabled exist")
	}
}

func TestSelectAdapterIDForRole_NoNewRegistry(t *testing.T) {
	// Empty config → no candidates; falls back to preferred empty string
	m := NewMOAModelWithOptimize(&vmconfig.VirtualModelConfig{Enabled: true}, nil, &stubChannelService{}, optimize.NewRuntime(50, optimize.TierFast))
	got := m.selectAdapterIDForRole(context.Background(), vmconfig.RoleCoding)
	if got != "" {
		t.Fatalf("empty bindings should yield empty preferred, got %q", got)
	}
}
