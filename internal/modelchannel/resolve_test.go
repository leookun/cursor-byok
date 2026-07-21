package modelchannel

import "testing"

// testAdapter is a minimal struct for testing ResolveAdapterIndex generic function.
type testAdapter struct {
	ID      string
	ModelID string
	Name    string
	BaseURL string
	APIKey  string
}

func TestResolveAdapterIndex_EmptyAdapters(t *testing.T) {
	idx, ok := ResolveAdapterIndex([]testAdapter{}, "foo",
		func(a testAdapter) string { return a.ID },
		func(a testAdapter) string { return a.ModelID },
	)
	if ok {
		t.Error("expected ok=false for empty adapters")
	}
	if idx != -1 {
		t.Errorf("expected index -1, got %d", idx)
	}
}

func TestResolveAdapterIndex_ExactIDMatch(t *testing.T) {
	adapters := []testAdapter{
		{ID: "alpha", ModelID: "gpt-4"},
		{ID: "beta", ModelID: "claude-3"},
	}
	idx, ok := ResolveAdapterIndex(adapters, "beta",
		func(a testAdapter) string { return a.ID },
		func(a testAdapter) string { return a.ModelID },
	)
	if !ok {
		t.Error("expected ok=true for exact ID match")
	}
	if idx != 1 {
		t.Errorf("expected index 1, got %d", idx)
	}
}

func TestResolveAdapterIndex_ProviderModelIDFallback(t *testing.T) {
	adapters := []testAdapter{
		{ID: "alpha", ModelID: "gpt-4"},
		{ID: "beta", ModelID: "claude-3"},
	}
	idx, ok := ResolveAdapterIndex(adapters, "claude-3",
		func(a testAdapter) string { return a.ID },
		func(a testAdapter) string { return a.ModelID },
	)
	if !ok {
		t.Error("expected ok=true for providerModelID fallback")
	}
	if idx != 1 {
		t.Errorf("expected index 1, got %d", idx)
	}
}

func TestResolveAdapterIndex_NoMatch(t *testing.T) {
	adapters := []testAdapter{
		{ID: "alpha", ModelID: "gpt-4"},
		{ID: "beta", ModelID: "claude-3"},
	}
	idx, ok := ResolveAdapterIndex(adapters, "nonexistent",
		func(a testAdapter) string { return a.ID },
		func(a testAdapter) string { return a.ModelID },
	)
	if ok {
		t.Error("expected ok=false for no match")
	}
	if idx != -1 {
		t.Errorf("expected index -1, got %d", idx)
	}
}

func TestResolveAdapterIndex_MetaAliasFallsBackToFirst(t *testing.T) {
	adapters := []testAdapter{
		{ID: "first", ModelID: "gpt-4"},
		{ID: "second", ModelID: "claude-3"},
	}
	for _, alias := range []string{"fast", "default", "auto"} {
		idx, ok := ResolveAdapterIndex(adapters, alias,
			func(a testAdapter) string { return a.ID },
			func(a testAdapter) string { return a.ModelID },
		)
		if !ok {
			t.Errorf("expected ok=true for meta alias %q", alias)
		}
		if idx != 0 {
			t.Errorf("expected index 0 for meta alias %q, got %d", alias, idx)
		}
	}
}

func TestResolveAdapterIndex_EmptyRequestFallsBackToFirst(t *testing.T) {
	adapters := []testAdapter{
		{ID: "first", ModelID: "gpt-4"},
	}
	idx, ok := ResolveAdapterIndex(adapters, "",
		func(a testAdapter) string { return a.ID },
		func(a testAdapter) string { return a.ModelID },
	)
	if !ok {
		t.Error("expected ok=true for empty request")
	}
	if idx != 0 {
		t.Errorf("expected index 0, got %d", idx)
	}
}

func TestResolveAdapterIndex_LegacyIDMatch(t *testing.T) {
	adapters := []testAdapter{
		{ID: "alpha", ModelID: "gpt-4", BaseURL: "https://api.openai.com", APIKey: "key1", Name: "GPT-4"},
		{ID: "beta", ModelID: "claude-3", BaseURL: "https://api.anthropic.com", APIKey: "key2", Name: "Claude"},
	}
	legacyID := BuildLegacyChannelID(adapters[0].BaseURL, adapters[0].ModelID, adapters[0].APIKey, adapters[0].Name)
	idx, ok := ResolveAdapterIndex(adapters, legacyID,
		func(a testAdapter) string { return a.ID },
		func(a testAdapter) string { return a.ModelID },
		func(a testAdapter) string { return BuildLegacyChannelID(a.BaseURL, a.ModelID, a.APIKey, a.Name) },
	)
	if !ok {
		t.Error("expected ok=true for legacy ID match")
	}
	if idx != 0 {
		t.Errorf("expected index 0, got %d", idx)
	}
}

func TestResolveAdapterIndex_AmbiguousLegacyIDMatch(t *testing.T) {
	// Two adapters with identical legacy IDs should cause ambiguity → (-1, false)
	adapters := []testAdapter{
		{ID: "alpha", ModelID: "gpt-4", BaseURL: "https://api.openai.com", APIKey: "key1", Name: "Same"},
		{ID: "beta", ModelID: "gpt-4", BaseURL: "https://api.openai.com", APIKey: "key1", Name: "Same"},
	}
	legacyID := BuildLegacyChannelID(adapters[0].BaseURL, adapters[0].ModelID, adapters[0].APIKey, adapters[0].Name)
	idx, ok := ResolveAdapterIndex(adapters, legacyID,
		func(a testAdapter) string { return a.ID },
		func(a testAdapter) string { return a.ModelID },
		func(a testAdapter) string { return BuildLegacyChannelID(a.BaseURL, a.ModelID, a.APIKey, a.Name) },
	)
	if ok {
		t.Error("expected ok=false for ambiguous legacy ID match")
	}
	if idx != -1 {
		t.Errorf("expected index -1, got %d", idx)
	}
}

func TestResolveAdapterIndex_AmbiguousProviderModelIDMatch(t *testing.T) {
	// Two adapters with same ModelID but different IDs should cause ambiguity → (-1, false)
	adapters := []testAdapter{
		{ID: "alpha", ModelID: "gpt-4"},
		{ID: "beta", ModelID: "gpt-4"},
	}
	idx, ok := ResolveAdapterIndex(adapters, "gpt-4",
		func(a testAdapter) string { return a.ID },
		func(a testAdapter) string { return a.ModelID },
	)
	if ok {
		t.Error("expected ok=false for ambiguous providerModelID match")
	}
	if idx != -1 {
		t.Errorf("expected index -1, got %d", idx)
	}
}

func TestIsMetaModelAlias(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"fast", true},
		{"default", true},
		{"auto", true},
		{"FAST", true},
		{"Default", true},
		{"Auto", true},
		{" fast ", true},
		{"gpt-4", false},
		{"", false},
		{"custom", false},
	}
	for _, tt := range tests {
		got := IsMetaModelAlias(tt.input)
		if got != tt.want {
			t.Errorf("IsMetaModelAlias(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
