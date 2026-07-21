package config

import (
	"strings"
	"testing"
)

func TestGroupAndFlattenRoundTrip(t *testing.T) {
	flat := []ModelAdapterConfig{
		{
			DisplayName: "deepseek-v4-pro", Type: "openai",
			BaseURL: "https://xlapis.com", APIKey: "sk-a",
			TooltipData: "XL", ModelID: "deepseek-v4-pro",
			ReasoningEffort: "high", OpenAIEndpoint: "/v1/chat/completions",
		},
		{
			DisplayName: "glm-5.2", Type: "openai",
			BaseURL: "https://xlapis.com", APIKey: "sk-a",
			TooltipData: "XL", ModelID: "glm-5.2",
			ReasoningEffort: "high", OpenAIEndpoint: "/v1/chat/completions",
		},
		{
			DisplayName: "claude", Type: "anthropic",
			BaseURL: "https://cn.vanyospace.com", APIKey: "sk-anth",
			TooltipData: "myth", ModelID: "claude-fable-5",
			AnthropicThinkingEffort: "max",
		},
		{
			DisplayName: "grok-4.5", Type: "openai",
			BaseURL: "https://cn.vanyospace.com", APIKey: "sk-b",
			TooltipData: "vanyo", ModelID: "grok-4.5",
			ReasoningEffort: "high", OpenAIEndpoint: "/v1/responses",
		},
		{
			DisplayName: "grok-4.5", Type: "openai",
			BaseURL: "https://cn.caiaiu.com", APIKey: "sk-c",
			TooltipData: "caiai", ModelID: "grok-4.5",
			ReasoningEffort: "high", OpenAIEndpoint: "/v1/responses",
		},
	}

	providers := GroupAdaptersToProviders(flat)
	// xlapis(openai) + vanyo(anthropic) + vanyo(openai) + caiai(openai) = 4
	if len(providers) != 4 {
		t.Fatalf("want 4 providers, got %d", len(providers))
	}

	out, err := FlattenProvidersToAdapters(providers)
	if err != nil {
		t.Fatalf("flatten: %v", err)
	}
	if len(out) != 5 {
		t.Fatalf("want 5 adapters, got %d", len(out))
	}

	refs := map[string]struct{}{}
	for _, a := range out {
		if a.Ref == "" {
			t.Fatalf("missing ref for %s", a.DisplayName)
		}
		if _, ok := refs[a.Ref]; ok {
			t.Fatalf("duplicate ref %q", a.Ref)
		}
		refs[a.Ref] = struct{}{}
	}

	var grokRefs []string
	for _, a := range out {
		if a.ModelID == "grok-4.5" {
			grokRefs = append(grokRefs, a.Ref)
		}
	}
	if len(grokRefs) != 2 || grokRefs[0] == grokRefs[1] {
		t.Fatalf("grok refs should be 2 unique, got %v", grokRefs)
	}
}

func TestResolveByRef(t *testing.T) {
	providers := []ProviderConfig{
		{
			ID: "vanyo", Name: "vanyo", Type: "openai",
			BaseURL: "https://cn.vanyospace.com", APIKey: "sk-b",
			Models: []ModelInProviderConfig{
				{DisplayName: "grok", ModelID: "grok-4.5", TooltipData: "vanyo",
					ReasoningEffort: "high", OpenAIEndpoint: "/v1/responses"},
			},
		},
		{
			ID: "caiai", Name: "caiai", Type: "openai",
			BaseURL: "https://cn.caiaiu.com", APIKey: "sk-c",
			Models: []ModelInProviderConfig{
				{DisplayName: "grok", ModelID: "grok-4.5", TooltipData: "caiai",
					ReasoningEffort: "high", OpenAIEndpoint: "/v1/responses"},
			},
		},
	}
	adapters, err := FlattenProvidersToAdapters(providers)
	if err != nil {
		t.Fatal(err)
	}

	// modelID alone is ambiguous → must fail
	if _, err := resolveModelAdapterChannel(adapters, "grok-4.5"); err == nil {
		t.Fatal("expected ambiguous modelID to fail")
	}

	// Collect actual provider IDs post-normalization.
	providerIDs := make(map[string]string) // baseURL prefix → stable ID
	for _, a := range adapters {
		ref := a.Ref // format: providerID:modelID
		if idx := strings.IndexByte(ref, ':'); idx >= 0 {
			pid := ref[:idx]
			if strings.Contains(a.BaseURL, "caiaiu") {
				providerIDs["caiai"] = pid
			} else if strings.Contains(a.BaseURL, "vanyospace") {
				providerIDs["vanyo"] = pid
			}
		}
	}

	// Ref is unique — resolve using the actual stable provider ID.
	ch, err := resolveModelAdapterChannel(adapters, providerIDs["caiai"]+":grok-4.5")
	if err != nil {
		t.Fatal(err)
	}
	if ch.Model != "grok-4.5" {
		t.Fatalf("API model want grok-4.5, got %s", ch.Model)
	}
	if ch.BaseURL != "https://cn.caiaiu.com" {
		t.Fatalf("baseURL: %s", ch.BaseURL)
	}
}

func TestMultiKeyProviderFlatten(t *testing.T) {
	providers := []ProviderConfig{
		{
			ID: "multi", Name: "multi", Type: "openai",
			BaseURL: "https://api.example.com", APIKeys: []string{"sk-primary", "sk-secondary"},
			Models: []ModelInProviderConfig{
				{DisplayName: "gpt-4", ModelID: "gpt-4", TooltipData: "multi"},
				{DisplayName: "gpt-3.5", ModelID: "gpt-3.5", TooltipData: "multi"},
			},
		},
	}
	adapters, err := FlattenProvidersToAdapters(providers)
	if err != nil {
		t.Fatal(err)
	}
	if len(adapters) != 2 {
		t.Fatalf("want 2 adapters, got %d", len(adapters))
	}
	// Both adapters should use the primary key.
	for _, a := range adapters {
		if a.APIKey != "sk-primary" {
			t.Fatalf("adapter %s: want apiKey sk-primary, got %s", a.DisplayName, a.APIKey)
		}
	}
}

func TestMultiKeyMigrationFromLegacy(t *testing.T) {
	providers := []ProviderConfig{
		{
			ID: "legacy", Name: "legacy", Type: "openai",
			BaseURL: "https://api.example.com", APIKey: "sk-old",
			Models: []ModelInProviderConfig{
				{DisplayName: "m1", ModelID: "m1", TooltipData: "t"},
			},
		},
	}
	normalized := NormalizeProviders(providers)
	if len(normalized) != 1 {
		t.Fatalf("want 1 provider, got %d", len(normalized))
	}
	p := normalized[0]
	if len(p.APIKeys) != 1 || p.APIKeys[0] != "sk-old" {
		t.Fatalf("APIKeys migration: want [sk-old], got %v", p.APIKeys)
	}
	if p.APIKey != "sk-old" {
		t.Fatalf("legacy APIKey: want sk-old, got %s", p.APIKey)
	}
}

func TestMultiKeyDedup(t *testing.T) {
	providers := []ProviderConfig{
		{
			ID: "dedup", Name: "dedup", Type: "openai",
			BaseURL: "https://api.example.com", APIKeys: []string{"sk-a", "sk-a", "sk-b"},
			Models: []ModelInProviderConfig{
				{DisplayName: "m1", ModelID: "m1", TooltipData: "t"},
			},
		},
	}
	normalized := NormalizeProviders(providers)
	if len(normalized[0].APIKeys) != 2 {
		t.Fatalf("want 2 unique keys, got %d: %v", len(normalized[0].APIKeys), normalized[0].APIKeys)
	}
}

func TestGroupAdaptersMergeSameBaseURLType(t *testing.T) {
	flat := []ModelAdapterConfig{
		{
			DisplayName: "gpt-4", Type: "openai",
			BaseURL: "https://api.example.com", APIKey: "sk-primary",
			TooltipData: "t", ModelID: "gpt-4",
			ReasoningEffort: "high", OpenAIEndpoint: "/v1/responses",
		},
		{
			DisplayName: "gpt-3.5", Type: "openai",
			BaseURL: "https://api.example.com", APIKey: "sk-secondary",
			TooltipData: "t", ModelID: "gpt-3.5",
			ReasoningEffort: "high", OpenAIEndpoint: "/v1/responses",
		},
	}
	providers := GroupAdaptersToProviders(flat)
	// Same baseURL+type → 1 provider with 2 keys
	if len(providers) != 1 {
		t.Fatalf("want 1 provider, got %d", len(providers))
	}
	p := providers[0]
	if len(p.APIKeys) != 2 {
		t.Fatalf("want 2 keys, got %d: %v", len(p.APIKeys), p.APIKeys)
	}
	if len(p.Models) != 2 {
		t.Fatalf("want 2 models, got %d", len(p.Models))
	}
	// Flatten should use first key.
	adapters, err := FlattenProvidersToAdapters(providers)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range adapters {
		if a.APIKey != "sk-primary" {
			t.Fatalf("adapter %s: want sk-primary, got %s", a.DisplayName, a.APIKey)
		}
	}
}

func TestNormalizeConfigMigratesFlatAdapters(t *testing.T) {
	cfg, err := NormalizeConfig(Config{
		ModelAdapters: []ModelAdapterConfig{
			{
				DisplayName: "m1", Type: "openai",
				BaseURL: "https://example.com", APIKey: "sk",
				TooltipData: "t", ModelID: "m1",
				ReasoningEffort: "medium", OpenAIEndpoint: "/v1/chat/completions",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("providers: %d", len(cfg.Providers))
	}
	if len(cfg.ModelAdapters) != 1 {
		t.Fatalf("adapters: %d", len(cfg.ModelAdapters))
	}
	if cfg.ModelAdapters[0].Ref == "" {
		t.Fatal("expected ref on flattened adapter")
	}
}

func TestStableProviderIDDeterminism(t *testing.T) {
	// Same baseURL+type always produces the same ID.
	id1 := stableProviderID("https://api.openai.com/v1", "openai")
	id2 := stableProviderID("https://api.openai.com/v1", "openai")
	if id1 != id2 {
		t.Fatalf("same inputs produced different IDs: %q vs %q", id1, id2)
	}

	// Different baseURLs produce different IDs.
	idA := stableProviderID("https://api.openai.com/v1", "openai")
	idB := stableProviderID("https://api.anthropic.com", "openai")
	if idA == idB {
		t.Fatalf("different baseURLs produced same ID: %q", idA)
	}

	// Same baseURL, different type produces different ID.
	idOpenAI := stableProviderID("https://cn.vanyospace.com", "openai")
	idAnth := stableProviderID("https://cn.vanyospace.com", "anthropic")
	if idOpenAI == idAnth {
		t.Fatalf("same baseURL different types produced same ID: %q", idOpenAI)
	}

	// Variations in URL format (trailing slash, protocol case) that map
	// to the same host still produce deterministic results per exact input.
	// Note: the hash is over the trimmed lowercased baseURL, so minor
	// normalization differences affect the hash — that's by design:
	// config.yaml saves always use the normalized baseURL from the UI.
}

func TestStableProviderIDHostPrefix(t *testing.T) {
	// Verify IDs have the readable host prefix + hash suffix format.
	id := stableProviderID("https://api.openai.com/v1", "openai")
	if !strings.HasPrefix(id, "api-openai-com-") {
		t.Fatalf("expected host prefix 'api-openai-com-', got %q", id)
	}
	// After the prefix, there should be exactly 8 hex chars at the end.
	suffix := id[len(id)-8:]
	if len(suffix) != 8 {
		t.Fatalf("expected 8-char hex suffix, got %q (len=%d)", suffix, len(suffix))
	}
	// Verify suffix is all hex characters.
	for _, c := range suffix {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("suffix %q contains non-hex char %q", suffix, c)
		}
	}

	// Non-openai type includes type in slug prefix.
	idAnth := stableProviderID("https://api.anthropic.com", "anthropic")
	if !strings.HasPrefix(idAnth, "api-anthropic-com-anthropic-") {
		t.Fatalf("expected 'api-anthropic-com-anthropic-' prefix for anthropic type, got %q", idAnth)
	}
}

func TestProviderIDStableAcrossRegeneration(t *testing.T) {
	// Simulate the bug scenario: user has adapters, they get grouped into
	// providers, then later a re-save happens. Provider IDs must stay the same.
	adapters := []ModelAdapterConfig{
		{
			DisplayName: "gpt-4o", Type: "openai",
			BaseURL: "https://api.openai.com/v1", APIKey: "sk-a",
			TooltipData: "OpenAI", ModelID: "gpt-4o",
			ReasoningEffort: "high", OpenAIEndpoint: "/v1/chat/completions",
		},
	}

	// First grouping.
	providers1 := GroupAdaptersToProviders(adapters)
	// Second grouping (simulates re-save).
	providers2 := GroupAdaptersToProviders(adapters)

	if len(providers1) != 1 || len(providers2) != 1 {
		t.Fatalf("expected 1 provider each, got %d and %d", len(providers1), len(providers2))
	}
	if providers1[0].ID != providers2[0].ID {
		t.Fatalf("provider IDs changed across regenerations: %q → %q",
			providers1[0].ID, providers2[0].ID)
	}

	// Flatten→Ref must also be stable.
	adapters1, _ := FlattenProvidersToAdapters(providers1)
	adapters2, _ := FlattenProvidersToAdapters(providers2)
	if adapters1[0].Ref != adapters2[0].Ref {
		t.Fatalf("Ref changed across regenerations: %q → %q",
			adapters1[0].Ref, adapters2[0].Ref)
	}
}

func TestNormalizeProvidersReplacesOldSlugIDs(t *testing.T) {
	// Old config with slug-based IDs gets new hash-based IDs on normalization.
	oldProviders := []ProviderConfig{
		{
			ID: "openai", Name: "OpenAI", Type: "openai",
			BaseURL: "https://api.openai.com/v1", APIKey: "sk-a",
			Models: []ModelInProviderConfig{
				{DisplayName: "gpt-4o", ModelID: "gpt-4o", TooltipData: "OpenAI"},
			},
		},
	}
	normalized := NormalizeProviders(oldProviders)
	if len(normalized) != 1 {
		t.Fatalf("want 1 provider, got %d", len(normalized))
	}
	// The old slug "openai" should be replaced with the hash-based ID.
	if normalized[0].ID == "openai" {
		t.Fatal("expected slug-based ID to be replaced with hash-based ID")
	}
	// The new ID should match what stableProviderID produces.
	expected := stableProviderID("https://api.openai.com/v1", "openai")
	if normalized[0].ID != expected {
		t.Fatalf("want %q, got %q", expected, normalized[0].ID)
	}
}