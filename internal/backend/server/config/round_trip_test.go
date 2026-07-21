package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cursor/internal/modelchannel"
)

// TestNormalizeConfig_PreservesModelAdapterEdits verifies that user edits to
// modelAdapters survive NormalizeConfig even when providers are also present.
// This covers the regression fix where modelAdapters became canonical (line 252-259).
func TestNormalizeConfig_PreservesModelAdapterEdits(t *testing.T) {
	// Given: a config with providers that has old modelID "gpt-4o"
	// AND modelAdapters where the user has edited modelID to "gpt-4o-mini"
	input := Config{
		ModelAdapters: []modelchannel.ModelAdapterConfig{
			{
				DisplayName: "GPT-4o Mini", Type: "openai",
				BaseURL: "https://api.openai.com/v1", APIKey: "sk-test",
				TooltipData: "openai", ModelID: "gpt-4o-mini",
				ReasoningEffort: "medium", OpenAIEndpoint: "/v1/chat/completions",
			},
		},
		// Providers with stale modelID — modelAdapters are canonical so this should
		// be ignored when modelAdapters are present.
		Providers: []ProviderConfig{
			{
				ID: "openai", Name: "api.openai.com", Type: "openai",
				BaseURL: "https://api.openai.com/v1", APIKey: "sk-test",
				Models: []ModelInProviderConfig{
					{
						DisplayName: "GPT-4o", ModelID: "gpt-4o",
						TooltipData: "openai",
					},
				},
			},
		},
	}

	// When: NormalizeConfig processes the input
	output, err := NormalizeConfig(input)
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}

	// Then: the output modelAdapters must reflect the user edit (gpt-4o-mini),
	// NOT the stale provider value (gpt-4o).
	found := false
	for _, a := range output.ModelAdapters {
		if a.ModelID == "gpt-4o-mini" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("modelAdapter edit not preserved: want modelID 'gpt-4o-mini', got adapters: %v",
			adapterModelIDs(output.ModelAdapters))
	}
}

// adapterModelIDs returns just the ModelID field from each adapter for diagnostics.
func adapterModelIDs(adapters []modelchannel.ModelAdapterConfig) []string {
	out := make([]string, len(adapters))
	for i, a := range adapters {
		out[i] = a.ModelID
	}
	return out
}

// TestNormalizeConfig_PreservesAOSConfig verifies that the AOS virtual model
// configuration survives NormalizeConfig without loss of any field.
func TestNormalizeConfig_PreservesAOSConfig(t *testing.T) {
	// Given: a config with AOS enabled, leader bound to an adapter, members,
	// and an explicit executionMode
	input := Config{
		ModelAdapters: []modelchannel.ModelAdapterConfig{
			{
				DisplayName: "leader-model", Type: "openai",
				BaseURL: "https://api.openai.com/v1", APIKey: "sk-test",
				TooltipData: "openai", ModelID: "gpt-4o",
				ReasoningEffort: "medium", OpenAIEndpoint: "/v1/chat/completions",
			},
			{
				DisplayName: "member-model", Type: "anthropic",
				BaseURL: "https://api.anthropic.com", APIKey: "sk-test",
				TooltipData: "anthropic", ModelID: "claude-sonnet-5",
				AnthropicThinkingEffort: "max",
			},
		},
		VirtualModels: VirtualModelsConfig{
			AOS: &AOSConfig{
				Enabled: true,
				Leader: AOSLeaderConfig{
					AdapterID: "leader-adapter-1",
				},
				Members: []AOSMemberConfig{
					{
						ID:           "reviewer",
						Name:         "Code Reviewer",
						AdapterID:    "member-adapter-1",
						SystemPrompt: "You are a careful code reviewer.",
					},
					{
						ID:           "architect",
						Name:         "System Architect",
						AdapterID:    "member-adapter-2",
						SystemPrompt: "You are a system architect.",
					},
				},
				ExecutionMode: AOSExecutionModeCursorTask,
			},
		},
	}

	// When: NormalizeConfig processes the input
	output, err := NormalizeConfig(input)
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}

	// Then: all AOS fields must be preserved
	if output.VirtualModels.AOS == nil {
		t.Fatal("AOS config should not be nil after NormalizeConfig")
	}
	aos := output.VirtualModels.AOS

	if !aos.Enabled {
		t.Error("AOS.Enabled: want true")
	}
	if aos.Leader.AdapterID != "leader-adapter-1" {
		t.Errorf("AOS.Leader.AdapterID: want 'leader-adapter-1', got %q", aos.Leader.AdapterID)
	}
	if len(aos.Members) != 2 {
		t.Fatalf("AOS.Members count: want 2, got %d", len(aos.Members))
	}
	if aos.ExecutionMode != AOSExecutionModeCursorTask {
		t.Errorf("AOS.ExecutionMode: want %q, got %q", AOSExecutionModeCursorTask, aos.ExecutionMode)
	}

	// verify member 0
	if aos.Members[0].ID != "reviewer" {
		t.Errorf("AOS.Members[0].ID: want 'reviewer', got %q", aos.Members[0].ID)
	}
	if aos.Members[0].Name != "Code Reviewer" {
		t.Errorf("AOS.Members[0].Name: want 'Code Reviewer', got %q", aos.Members[0].Name)
	}
	if aos.Members[0].AdapterID != "member-adapter-1" {
		t.Errorf("AOS.Members[0].AdapterID: want 'member-adapter-1', got %q", aos.Members[0].AdapterID)
	}
	if aos.Members[0].SystemPrompt != "You are a careful code reviewer." {
		t.Errorf("AOS.Members[0].SystemPrompt not preserved")
	}

	// verify member 1
	if aos.Members[1].ID != "architect" {
		t.Errorf("AOS.Members[1].ID: want 'architect', got %q", aos.Members[1].ID)
	}
	if aos.Members[1].Name != "System Architect" {
		t.Errorf("AOS.Members[1].Name: want 'System Architect', got %q", aos.Members[1].Name)
	}
	if aos.Members[1].AdapterID != "member-adapter-2" {
		t.Errorf("AOS.Members[1].AdapterID: want 'member-adapter-2', got %q", aos.Members[1].AdapterID)
	}
}

// TestProviderID_StableAcrossReSaves verifies that provider IDs are deterministic
// and stable when the same baseURL+type is used across NormalizeConfig calls,
// even after the modelID changes (the user edits the model).
//
// ID stability is critical for AOS adapterID bindings: changing IDs would break
// the AOS Leader/Member adapter references.
func TestProviderID_StableAcrossReSaves(t *testing.T) {
	baseURL := "https://api.openai.com/v1"
	adapterType := "openai"

	// Given: first NormalizeConfig with modelID "gpt-4o"
	cfg1, err := NormalizeConfig(Config{
		ModelAdapters: []modelchannel.ModelAdapterConfig{
			{
				DisplayName: "GPT-4o", Type: adapterType,
				BaseURL: baseURL, APIKey: "sk-test",
				TooltipData: "openai", ModelID: "gpt-4o",
				ReasoningEffort: "medium", OpenAIEndpoint: "/v1/chat/completions",
			},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeConfig 1: %v", err)
	}
	if len(cfg1.Providers) == 0 {
		t.Fatal("first normalize: expected at least 1 provider")
	}
	id1 := cfg1.Providers[0].ID

	// When: second NormalizeConfig with modelID changed to "gpt-4o-mini"
	// (same baseURL+type — user edited the model in the frontend)
	cfg2, err := NormalizeConfig(Config{
		ModelAdapters: []modelchannel.ModelAdapterConfig{
			{
				DisplayName: "GPT-4o Mini", Type: adapterType,
				BaseURL: baseURL, APIKey: "sk-test",
				TooltipData: "openai", ModelID: "gpt-4o-mini",
				ReasoningEffort: "low", OpenAIEndpoint: "/v1/chat/completions",
			},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeConfig 2: %v", err)
	}
	if len(cfg2.Providers) == 0 {
		t.Fatal("second normalize: must have at least 1 provider")
	}
	id2 := cfg2.Providers[0].ID

	// Then: provider ID must be the same (baseURL+type unchanged)
	if id1 != id2 {
		t.Errorf("provider ID changed across re-saves: %q → %q (baseURL+type unchanged)", id1, id2)
	}
	t.Logf("provider ID stable: %s (consistent across modelID changes)", id1)
}

// TestSaveLoad_PreservesAOSConfig verifies the full config persistence round-trip:
// Save → Load preserves all AOS fields including Leader, Members, ExecutionMode,
// and Enabled state.
func TestSaveLoad_PreservesAOSConfig(t *testing.T) {
	// Given: a temp directory with config store
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	store := NewStore(cfgPath, filepath.Join(dir, "logs"))

	aosCfg := &AOSConfig{
		Enabled: true,
		Leader: AOSLeaderConfig{
			AdapterID: "test-leader-adapter",
		},
		Members: []AOSMemberConfig{
			{
				ID:           "member-1",
				Name:         "Alice the Reviewer",
				AdapterID:    "member-adapter-1",
				SystemPrompt: "Review code carefully.",
			},
			{
				ID:           "member-2",
				Name:         "Bob the Architect",
				AdapterID:    "member-adapter-2",
				SystemPrompt: "Architect systems well.",
			},
		},
		ExecutionMode: AOSExecutionModeCursorTask,
	}

	config := Config{
		ModelAdapters: []modelchannel.ModelAdapterConfig{
			{
				DisplayName: "gpt-4o", Type: "openai",
				BaseURL: "https://api.openai.com/v1", APIKey: "sk-test",
				TooltipData: "openai", ModelID: "gpt-4o",
				ReasoningEffort: "medium", OpenAIEndpoint: "/v1/chat/completions",
			},
		},
		VirtualModels: VirtualModelsConfig{
			AOS: aosCfg,
		},
	}

	// When: Save
	saved, err := store.Save(context.Background(), config)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Then: saved config has AOS
	if saved.VirtualModels.AOS == nil {
		t.Fatal("Save: AOSConfig should not be nil")
	}

	// Verify file was created
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	// When: Load the config back from disk
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Then: loaded config must preserve all AOS fields
	if loaded.VirtualModels.AOS == nil {
		t.Fatal("Load: AOSConfig should not be nil")
	}
	aos := loaded.VirtualModels.AOS

	if !aos.Enabled {
		t.Error("AOS.Enabled: want true")
	}
	if aos.Leader.AdapterID != "test-leader-adapter" {
		t.Errorf("AOS.Leader.AdapterID: want 'test-leader-adapter', got %q", aos.Leader.AdapterID)
	}
	if aos.ExecutionMode != AOSExecutionModeCursorTask {
		t.Errorf("AOS.ExecutionMode: want %q, got %q", AOSExecutionModeCursorTask, aos.ExecutionMode)
	}
	if len(aos.Members) != 2 {
		t.Fatalf("AOS.Members count: want 2, got %d", len(aos.Members))
	}
	if aos.Members[0].ID != "member-1" || aos.Members[0].Name != "Alice the Reviewer" {
		t.Errorf("AOS.Members[0]: got id=%q name=%q", aos.Members[0].ID, aos.Members[0].Name)
	}
	if aos.Members[1].ID != "member-2" || aos.Members[1].Name != "Bob the Architect" {
		t.Errorf("AOS.Members[1]: got id=%q name=%q", aos.Members[1].ID, aos.Members[1].Name)
	}
}