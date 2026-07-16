package upstream

import (
	"encoding/json"
	"testing"

	legacyruntime "cursor/internal/runtime"
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

func TestBuildAvailableModelEntriesKeepsAllModelsFromSameChannel(t *testing.T) {
	adapters := []legacyruntime.ModelAdapterConfig{
		{
			ID:          "channel-gpt",
			DisplayName: "GPT",
			Type:        "openai",
			BaseURL:     "https://provider.example/v1",
			APIKey:      "shared-key",
			TooltipData: "GPT model",
			ModelID:     "gpt-5",
		},
		{
			ID:          "channel-qwen",
			DisplayName: "Qwen",
			Type:        "openai",
			BaseURL:     "https://provider.example/v1",
			APIKey:      "shared-key",
			TooltipData: "Qwen model",
			ModelID:     "qwen3-max",
		},
	}

	entries := buildAvailableModelEntries(adapters)
	if len(entries) != len(adapters) {
		t.Fatalf("expected %d Cursor model entries, got %d", len(adapters), len(entries))
	}
	for index, expectedID := range []string{"channel-gpt", "channel-qwen"} {
		if actualID, _ := entries[index]["serverModelName"].(string); actualID != expectedID {
			t.Fatalf("entry %d serverModelName: expected %q, got %q", index, expectedID, actualID)
		}
	}

	refs := collectModelAdapterRefs(adapters)
	if len(refs) != 2 || refs[0] != "channel-gpt" || refs[1] != "channel-qwen" {
		t.Fatalf("unexpected Cursor model refs: %#v", refs)
	}
}
