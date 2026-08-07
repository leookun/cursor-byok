package config

import "testing"

func testModelAdapter(displayName string, sortValue int) ModelAdapterConfig {
	return ModelAdapterConfig{
		Sort:            sortValue,
		DisplayName:     displayName,
		Type:            "openai",
		BaseURL:         "https://api.example.com/v1",
		APIKey:          "test-key",
		TooltipData:     displayName,
		ModelID:         displayName,
		ReasoningEffort: "medium",
		OpenAIEndpoint:  "/v1/responses",
	}
}

func TestNormalizeModelAdapterConfigsPreservesLegacyArrayOrder(t *testing.T) {
	adapters, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{
		testModelAdapter("first", 0),
		testModelAdapter("second", 0),
		testModelAdapter("third", 0),
	})
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs returned error: %v", err)
	}

	for index, expectedName := range []string{"first", "second", "third"} {
		if adapters[index].DisplayName != expectedName {
			t.Fatalf("adapter %d = %q, want %q", index, adapters[index].DisplayName, expectedName)
		}
		if adapters[index].Sort != index+1 {
			t.Fatalf("adapter %d sort = %d, want %d", index, adapters[index].Sort, index+1)
		}
	}
}

func TestNormalizeModelAdapterConfigsUsesStableExplicitSort(t *testing.T) {
	adapters, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{
		testModelAdapter("legacy", 0),
		testModelAdapter("third", 30),
		testModelAdapter("first", 10),
		testModelAdapter("second-a", 20),
		testModelAdapter("second-b", 20),
	})
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs returned error: %v", err)
	}

	expectedNames := []string{"first", "second-a", "second-b", "third", "legacy"}
	for index, expectedName := range expectedNames {
		if adapters[index].DisplayName != expectedName {
			t.Fatalf("adapter %d = %q, want %q", index, adapters[index].DisplayName, expectedName)
		}
		if adapters[index].Sort != index+1 {
			t.Fatalf("adapter %d sort = %d, want %d", index, adapters[index].Sort, index+1)
		}
	}
}
