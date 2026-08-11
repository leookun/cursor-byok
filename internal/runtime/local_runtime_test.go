package runtime

import "testing"

func testRuntimeModelAdapter(reasoningEffort string) ModelAdapterConfig {
	return ModelAdapterConfig{
		DisplayName:     "non-reasoning-model",
		Type:            "openai",
		BaseURL:         "https://api.example.com/v1",
		APIKey:          "test-key",
		TooltipData:     "non-reasoning-model",
		ModelID:         "non-reasoning-model",
		ReasoningEffort: reasoningEffort,
		OpenAIEndpoint:  "/v1/responses",
	}
}

func TestNormalizeModelAdapterConfigsAllowsBlankReasoningEffort(t *testing.T) {
	adapters, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{testRuntimeModelAdapter("")})
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs returned error: %v", err)
	}
	if got := adapters[0].ReasoningEffort; got != "" {
		t.Fatalf("ReasoningEffort = %q, want blank", got)
	}
}

func TestNormalizeModelAdapterConfigsRejectsUnknownReasoningEffort(t *testing.T) {
	if _, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{testRuntimeModelAdapter("unsupported")}); err == nil {
		t.Fatal("NormalizeModelAdapterConfigs should reject an unknown reasoning effort")
	}
}
