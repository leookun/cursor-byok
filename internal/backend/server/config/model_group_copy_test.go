package config

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeConfigKeepsExplicitCopiedGroupIdentity(t *testing.T) {
	input := DefaultConfig()
	input.ModelGroups = []ModelGroupConfig{
		{
			Name:           "原始渠道",
			Type:           "openai",
			BaseURL:        "https://provider.example/v1",
			APIKey:         "fixture-key",
			OpenAIEndpoint: "/v1/responses",
		},
		{
			ID:             "grp_copy_test",
			Name:           "原始渠道 - 副本",
			Type:           "openai",
			BaseURL:        "https://provider.example/v1",
			APIKey:         "fixture-key",
			OpenAIEndpoint: "/v1/responses",
		},
	}
	input.ModelAdapters = []ModelAdapterConfig{
		copyTestAdapter("原始模型", "grp_original", "model-a"),
		copyTestAdapter("原始模型 - 副本", "grp_copy_test", "model-a"),
	}

	normalized, err := NormalizeConfig(input)
	if err != nil {
		t.Fatalf("normalize copied config: %v", err)
	}
	if len(normalized.ModelGroups) != 2 {
		t.Fatalf("expected two groups, got %#v", normalized.ModelGroups)
	}
	if normalized.ModelGroups[1].ID != "grp_copy_test" {
		t.Fatalf("copied group ID was not preserved: %#v", normalized.ModelGroups)
	}
	if normalized.ModelAdapters[1].GroupID != "grp_copy_test" {
		t.Fatalf("copied adapter group ID was not preserved: %#v", normalized.ModelAdapters)
	}
}

func TestCopiedGroupIdentitySurvivesStoreRoundTrip(t *testing.T) {
	input := DefaultConfig()
	input.ModelGroups = []ModelGroupConfig{
		{
			Name:           "原始渠道",
			Type:           "openai",
			BaseURL:        "https://provider.example/v1",
			APIKey:         "fixture-key",
			OpenAIEndpoint: "/v1/responses",
		},
		{
			ID:             "grp_copy_roundtrip",
			Name:           "原始渠道 - 副本",
			Type:           "openai",
			BaseURL:        "https://provider.example/v1",
			APIKey:         "fixture-key",
			OpenAIEndpoint: "/v1/responses",
		},
	}
	input.ModelAdapters = []ModelAdapterConfig{
		copyTestAdapter("原始模型", "", "model-a"),
		copyTestAdapter("原始模型 - 副本", "grp_copy_roundtrip", "model-a"),
	}

	store := NewStore(filepath.Join(t.TempDir(), "config.yaml"), t.TempDir())
	if _, err := store.Save(context.Background(), input); err != nil {
		t.Fatalf("save copied config: %v", err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("load copied config: %v", err)
	}
	if len(loaded.ModelGroups) != 2 || loaded.ModelGroups[1].ID != "grp_copy_roundtrip" {
		t.Fatalf("copied group identity was lost after reload: %#v", loaded.ModelGroups)
	}
	if len(loaded.ModelAdapters) != 2 || loaded.ModelAdapters[1].GroupID != "grp_copy_roundtrip" {
		t.Fatalf("copied adapter identity was lost after reload: %#v", loaded.ModelAdapters)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	if !bytes.Contains(data, []byte("grp_copy_roundtrip")) {
		t.Fatalf("persisted config does not contain copied identity: %s", data)
	}
}

func copyTestAdapter(displayName string, groupID string, modelID string) ModelAdapterConfig {
	return ModelAdapterConfig{
		GroupID:         groupID,
		DisplayName:     displayName,
		Type:            "openai",
		BaseURL:         "https://provider.example/v1",
		APIKey:          "fixture-key",
		TooltipData:     displayName,
		ModelID:         modelID,
		ReasoningEffort: "medium",
		OpenAIEndpoint:  "/v1/responses",
	}
}
