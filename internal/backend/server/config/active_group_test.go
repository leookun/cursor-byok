package config

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	legacyruntime "cursor/internal/runtime"
)

func TestNormalizeConfigKeepsOnlyValidActiveGroup(t *testing.T) {
	input := activeGroupTestConfig()
	normalizedAdapters, err := NormalizeModelAdapterConfigs(input.ModelAdapters)
	if err != nil {
		t.Fatalf("normalize adapters: %v", err)
	}
	input.ActiveModelGroupID = normalizedAdapters[0].GroupID

	normalized, err := NormalizeConfig(input)
	if err != nil {
		t.Fatalf("normalize config: %v", err)
	}
	if normalized.ActiveModelGroupID != normalizedAdapters[0].GroupID {
		t.Fatalf("active group not preserved: %q", normalized.ActiveModelGroupID)
	}
	active := ActiveModelAdapterConfigs(normalized)
	if len(active) != 2 || active[0].ModelID != "model-a" || active[1].ModelID != "model-b" {
		t.Fatalf("unexpected active adapters: %#v", active)
	}
	if active[0].GroupID != active[1].GroupID || active[0].GroupID == normalized.ModelAdapters[2].GroupID {
		t.Fatalf("unexpected group IDs: %#v", normalized.ModelAdapters)
	}

	input.ActiveModelGroupID = "grp_missing"
	normalized, err = NormalizeConfig(input)
	if err != nil {
		t.Fatalf("normalize unknown group: %v", err)
	}
	if normalized.ActiveModelGroupID != "" || len(ActiveModelAdapterConfigs(normalized)) != 0 {
		t.Fatalf("unknown group must fail closed: %#v", normalized)
	}
}

func TestActiveGroupFiltersRuntimeSnapshotAndResolver(t *testing.T) {
	ctx := context.Background()
	input := activeGroupTestConfig()
	normalizedAdapters, err := NormalizeModelAdapterConfigs(input.ModelAdapters)
	if err != nil {
		t.Fatalf("normalize adapters: %v", err)
	}
	input.ActiveModelGroupID = normalizedAdapters[0].GroupID

	root := t.TempDir()
	store := NewStore(filepath.Join(root, "config.yaml"), filepath.Join(root, "logs"))
	if _, err := store.Save(ctx, input); err != nil {
		t.Fatalf("save config: %v", err)
	}
	storeSnapshot, err := store.LegacyRuntimeSnapshot(ctx)
	if err != nil {
		t.Fatalf("store snapshot: %v", err)
	}
	assertActiveRuntimeAdapters(t, storeSnapshot.ModelAdapters)

	manager, err := NewManager(ctx, store)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	managerSnapshot, err := manager.LegacyRuntimeSnapshot(ctx)
	if err != nil {
		t.Fatalf("manager snapshot: %v", err)
	}
	assertActiveRuntimeAdapters(t, managerSnapshot.ModelAdapters)

	activeChannel, err := manager.SelectChannelForModel(ctx, normalizedAdapters[0].ID)
	if err != nil || activeChannel.Model != "model-a" {
		t.Fatalf("resolve active channel: channel=%#v err=%v", activeChannel, err)
	}
	if _, err := manager.SelectChannelForModel(ctx, normalizedAdapters[2].ID); !errors.Is(err, legacyruntime.ErrChannelNotAvailable) {
		t.Fatalf("inactive channel ID must be rejected: %v", err)
	}
	if _, err := manager.SelectChannelForModel(ctx, "shared-model"); !errors.Is(err, legacyruntime.ErrChannelNotAvailable) {
		t.Fatalf("inactive provider model ID must be rejected: %v", err)
	}
}

func TestExplicitEmptyModelGroupCanBeActivated(t *testing.T) {
	input := DefaultConfig()
	input.ModelGroups = []ModelGroupConfig{{
		Name:    "Production",
		Type:    "openai",
		BaseURL: "https://provider.example/v1",
		APIKey:  "key-a",
	}}
	groups, err := NormalizeModelGroupConfigs(input.ModelGroups)
	if err != nil {
		t.Fatalf("normalize groups: %v", err)
	}
	input.ActiveModelGroupID = groups[0].ID
	normalized, err := NormalizeConfig(input)
	if err != nil {
		t.Fatalf("normalize config: %v", err)
	}
	if normalized.ActiveModelGroupID != groups[0].ID || len(normalized.ModelGroups) != 1 {
		t.Fatalf("explicit group was not preserved: %#v", normalized)
	}
	if normalized.ModelGroups[0].OpenAIEndpoint != "/v1/responses" {
		t.Fatalf("unexpected default endpoint: %#v", normalized.ModelGroups[0])
	}
	if active := ActiveModelAdapterConfigs(normalized); len(active) != 0 {
		t.Fatalf("empty active group must publish no models: %#v", active)
	}
}

func TestNormalizeModelGroupKeepsCustomOpenAIEndpoint(t *testing.T) {
	groups, err := NormalizeModelGroupConfigs([]ModelGroupConfig{{
		Name:           "Custom",
		Type:           "openai",
		BaseURL:        "https://provider.example/v2/responses",
		APIKey:         "key-a",
		OpenAIEndpoint: "/custom",
	}})
	if err != nil {
		t.Fatalf("normalize groups: %v", err)
	}
	if len(groups) != 1 || groups[0].OpenAIEndpoint != "/custom" {
		t.Fatalf("custom endpoint was not preserved: %#v", groups)
	}
}

func TestEditedGroupIdentityControlsActiveState(t *testing.T) {
	input := activeGroupTestConfig()
	normalized, err := NormalizeConfig(input)
	if err != nil {
		t.Fatalf("normalize initial config: %v", err)
	}
	input.ModelGroups = normalized.ModelGroups
	input.ModelAdapters = normalized.ModelAdapters
	input.ActiveModelGroupID = normalized.ModelGroups[0].ID

	input.ModelGroups[0].Name = "Renamed"
	input.ModelGroups[0].OpenAIEndpoint = "/v1/chat/completions"
	for index := range input.ModelAdapters {
		if input.ModelAdapters[index].GroupID == input.ActiveModelGroupID {
			input.ModelAdapters[index].OpenAIEndpoint = "/v1/chat/completions"
		}
	}
	metadataOnly, err := NormalizeConfig(input)
	if err != nil {
		t.Fatalf("normalize metadata edit: %v", err)
	}
	if metadataOnly.ActiveModelGroupID == "" {
		t.Fatal("name and endpoint edits must preserve active group identity")
	}

	oldGroupID := metadataOnly.ActiveModelGroupID
	input = metadataOnly
	input.ModelGroups[0].APIKey = "changed-key"
	for index := range input.ModelAdapters {
		if input.ModelAdapters[index].GroupID == oldGroupID {
			input.ModelAdapters[index].APIKey = "changed-key"
		}
	}
	identityEdit, err := NormalizeConfig(input)
	if err != nil {
		t.Fatalf("normalize identity edit: %v", err)
	}
	if identityEdit.ActiveModelGroupID != "" {
		t.Fatalf("identity edit must fail closed: %q", identityEdit.ActiveModelGroupID)
	}
}

func assertActiveRuntimeAdapters(t *testing.T, adapters []legacyruntime.ModelAdapterConfig) {
	t.Helper()
	if len(adapters) != 2 || adapters[0].ModelID != "model-a" || adapters[1].ModelID != "model-b" {
		t.Fatalf("unexpected runtime adapters: %#v", adapters)
	}
	if !adapters[0].CustomHeadersEnabled || adapters[0].CustomHeadersJSON == "" {
		t.Fatalf("custom headers were lost: %#v", adapters[0])
	}
}

func activeGroupTestConfig() Config {
	config := DefaultConfig()
	config.ModelAdapters = []ModelAdapterConfig{
		activeGroupTestAdapter("Model A", "model-a", "key-a", `{"Authorization":"Bearer token","X-Tenant":"a"}`),
		activeGroupTestAdapter("Model B", "model-b", "key-a", `{"x-tenant":"a","authorization":"Bearer token"}`),
		activeGroupTestAdapter("Other", "shared-model", "key-b", `{}`),
	}
	return config
}

func activeGroupTestAdapter(displayName string, modelID string, apiKey string, headers string) ModelAdapterConfig {
	return ModelAdapterConfig{
		DisplayName:          displayName,
		Type:                 "openai",
		BaseURL:              "https://provider.example/v1/",
		APIKey:               apiKey,
		TooltipData:          displayName,
		ModelID:              modelID,
		ReasoningEffort:      "medium",
		OpenAIEndpoint:       "/v1/responses",
		CustomHeadersEnabled: headers != `{}`,
		CustomHeadersJSON:    headers,
	}
}
