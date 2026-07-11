package upstream

import (
	"encoding/json"
	"testing"
)

func TestBuildBootstrapStatsigConfigJSONDisablesAlwaysLocalDecompositionGate(t *testing.T) {
	payload, err := buildBootstrapStatsigConfigJSON(12345, "test-auth-id")
	if err != nil {
		t.Fatalf("build bootstrap statsig config: %v", err)
	}

	var decoded statsigBootstrapTemplate
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode bootstrap statsig config: %v", err)
	}

	gate, ok := decoded.FeatureGates[bootstrapStatsigDecomposeAlwaysLocalExtHostGate]
	if !ok {
		t.Fatalf("missing feature gate %q", bootstrapStatsigDecomposeAlwaysLocalExtHostGate)
	}
	if value, _ := gate["value"].(bool); value {
		t.Fatalf("expected %q to be disabled", bootstrapStatsigDecomposeAlwaysLocalExtHostGate)
	}
	if ruleID, _ := gate["rule_id"].(string); ruleID != "local_disabled" {
		t.Fatalf("unexpected rule_id: %q", ruleID)
	}
}

func TestBuildBootstrapStatsigConfigJSONDisablesGlassGates(t *testing.T) {
	payload, err := buildBootstrapStatsigConfigJSON(12345, "test-auth-id")
	if err != nil {
		t.Fatalf("build bootstrap statsig config: %v", err)
	}

	var decoded statsigBootstrapTemplate
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode bootstrap statsig config: %v", err)
	}

	for _, name := range []string{
		bootstrapStatsigGlassModeAvailableGate,
		bootstrapStatsigGlassOpenAgentInWindowGate,
		bootstrapStatsigOpenAgentsTitlebarGate,
		bootstrapStatsigOpenAgentWindowTopGate,
		bootstrapStatsigOpenAgentWindowBottomGate,
		bootstrapStatsigGlassCustomThemeSupport,
		bootstrapStatsigGlassAutomationsUI,
	} {
		gate, ok := decoded.FeatureGates[name]
		if !ok {
			t.Fatalf("missing feature gate %q", name)
		}
		if value, _ := gate["value"].(bool); value {
			t.Fatalf("expected %q to be disabled", name)
		}
	}
}
