package config

import (
	"strings"
	"testing"
)

func validSystemPromptAdapter() ModelAdapterConfig {
	return ModelAdapterConfig{
		DisplayName:          "Kimi K3",
		Type:                 "openai",
		BaseURL:              "https://example.com/v1",
		APIKey:               "test-key",
		TooltipData:          "test adapter",
		ModelID:              "kimi-k3",
		ReasoningEffort:      "medium",
		OpenAIEndpoint:       "/v1/chat/completions",
		SystemPromptEnabled:  true,
		SystemPrompt:         "Keep working until the task is complete.",
		SystemPromptPosition: "after",
	}
}

func TestNormalizeModelAdapterConfigSystemPrompt(t *testing.T) {
	adapter := validSystemPromptAdapter()
	adapter.SystemPrompt = "  channel instructions  "
	adapter.SystemPromptPosition = ""

	result, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{adapter})
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs returned error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("adapter count mismatch: got %d want 1", len(result))
	}
	if result[0].SystemPrompt != "channel instructions" {
		t.Fatalf("prompt mismatch: %q", result[0].SystemPrompt)
	}
	if result[0].SystemPromptPosition != DefaultSystemPromptPosition {
		t.Fatalf("position mismatch: got %q want %q", result[0].SystemPromptPosition, DefaultSystemPromptPosition)
	}
}

func TestNormalizeModelAdapterConfigRejectsInvalidSystemPrompt(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ModelAdapterConfig)
		message string
	}{
		{
			name: "empty enabled prompt",
			mutate: func(adapter *ModelAdapterConfig) {
				adapter.SystemPrompt = ""
			},
			message: "systemPrompt 启用时不能为空",
		},
		{
			name: "oversized prompt",
			mutate: func(adapter *ModelAdapterConfig) {
				adapter.SystemPrompt = strings.Repeat("a", MaxSystemPromptBytes+1)
			},
			message: "systemPrompt 不能超过",
		},
		{
			name: "invalid position",
			mutate: func(adapter *ModelAdapterConfig) {
				adapter.SystemPromptPosition = "replace"
			},
			message: "systemPromptPosition 仅支持 before 或 after",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := validSystemPromptAdapter()
			test.mutate(&adapter)
			_, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{adapter})
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("expected error containing %q, got %v", test.message, err)
			}
		})
	}
}

func TestResolveModelAdapterChannelCarriesSystemPrompt(t *testing.T) {
	adapter := validSystemPromptAdapter()
	adapters, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{adapter})
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs returned error: %v", err)
	}

	channel, err := resolveModelAdapterChannel(adapters, adapters[0].ID)
	if err != nil {
		t.Fatalf("resolveModelAdapterChannel returned error: %v", err)
	}
	if !channel.SystemPromptEnabled || channel.SystemPrompt != adapter.SystemPrompt || channel.SystemPromptPosition != "after" {
		t.Fatalf("system prompt settings not propagated: %#v", channel)
	}
}
