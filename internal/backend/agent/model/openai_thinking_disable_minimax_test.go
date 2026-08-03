package modeladapter

import "testing"

// TestOpenAIThinkingDisableKindMiniMax verifies that the MiniMax OpenAI-compatible
// endpoint is routed to the thinking_type disable branch, so that disabling
// thinking writes thinking:{type:"disabled"} and drops reasoning_effort. This
// covers the global endpoint, the China endpoint and a custom proxy that only
// exposes the MiniMax model id, plus regression cases for the other branches.
func TestOpenAIThinkingDisableKindMiniMax(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		modelID  string
		endpoint string
		want     string
	}{
		// MiniMax global endpoint
		{name: "minimax global base + M3", baseURL: "https://api.minimax.io/v1", modelID: "MiniMax-M3", endpoint: "/chat/completions", want: "thinking_type"},
		{name: "minimax global base + M2.7", baseURL: "https://api.minimax.io/v1", modelID: "MiniMax-M2.7", endpoint: "/chat/completions", want: "thinking_type"},
		// MiniMax China endpoint
		{name: "minimax cn base + M3", baseURL: "https://api.minimaxi.com/v1", modelID: "MiniMax-M3", endpoint: "/chat/completions", want: "thinking_type"},
		{name: "minimax cn base + M2.7", baseURL: "https://api.minimaxi.com/v1", modelID: "MiniMax-M2.7", endpoint: "/chat/completions", want: "thinking_type"},
		// MiniMax model id only (custom base)
		{name: "minimax model only (custom base)", baseURL: "https://custom.proxy.example.com/v1", modelID: "MiniMax-M3", endpoint: "/chat/completions", want: "thinking_type"},
		// Regression: enable_thinking branch (qwen) is unaffected
		{name: "qwen via dashscope", baseURL: "https://dashscope.aliyuncs.com/v1", modelID: "qwen-max", endpoint: "/chat/completions", want: "enable_thinking"},
		// Regression: reasoning_none branch (gpt-5.1+/gpt-6) is unaffected
		{name: "gpt-6", baseURL: "https://api.openai.com/v1", modelID: "gpt-6", endpoint: "/chat/completions", want: "reasoning_none"},
		// Regression: unknown provider does not disable
		{name: "unknown provider", baseURL: "https://api.unknown-llm.com/v1", modelID: "some-model", endpoint: "/chat/completions", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := openAIThinkingDisableKind(tc.baseURL, tc.modelID, tc.endpoint)
			if got != tc.want {
				t.Fatalf("openAIThinkingDisableKind(%q, %q, %q) = %q, want %q", tc.baseURL, tc.modelID, tc.endpoint, got, tc.want)
			}
		})
	}
}

// TestApplyOpenAIThinkingDisableMiniMax verifies that when ThinkingEffort=disabled
// and the provider is MiniMax, applyOpenAIThinkingDisable writes
// thinking:{type:"disabled"} and deletes reasoning_effort on both the global and
// the China endpoint.
func TestApplyOpenAIThinkingDisableMiniMax(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		modelID string
	}{
		{name: "global endpoint", baseURL: "https://api.minimax.io/v1", modelID: "MiniMax-M3"},
		{name: "china endpoint", baseURL: "https://api.minimaxi.com/v1", modelID: "MiniMax-M3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := StreamRequest{ThinkingEffort: "disabled", RequestKnobs: map[string]any{}}
			body := map[string]any{
				"model":            tc.modelID,
				"messages":         []map[string]any{{"role": "user", "content": "hi"}},
				"reasoning_effort": "high",
			}
			applyOpenAIThinkingDisable(body, req, tc.baseURL, tc.modelID, "/chat/completions")

			thinking, ok := body["thinking"].(map[string]any)
			if !ok {
				t.Fatalf("expected body[thinking] to be map[string]any, got %T (%v)", body["thinking"], body["thinking"])
			}
			if thinking["type"] != "disabled" {
				t.Fatalf("expected thinking.type=disabled, got %v", thinking["type"])
			}
			if _, stillPresent := body["reasoning_effort"]; stillPresent {
				t.Fatalf("reasoning_effort should be deleted when thinking disabled, got %v", body["reasoning_effort"])
			}
			if got := req.RequestKnobs["thinking_disabled_provider_param"]; got != "thinking.type" {
				t.Fatalf("expected request knob thinking_disabled_provider_param=thinking.type, got %v", got)
			}
		})
	}
}

// TestApplyOpenAIThinkingDisableMiniMaxNotTriggered verifies that a non-disabled
// thinking effort does not inject the disable field for MiniMax.
func TestApplyOpenAIThinkingDisableMiniMaxNotTriggered(t *testing.T) {
	req := StreamRequest{ThinkingEffort: "high", RequestKnobs: map[string]any{}}
	body := map[string]any{"model": "MiniMax-M3", "reasoning_effort": "high"}
	applyOpenAIThinkingDisable(body, req, "https://api.minimax.io/v1", "MiniMax-M3", "/chat/completions")
	if _, present := body["thinking"]; present {
		t.Fatalf("thinking should not be injected when ThinkingEffort != disabled, got %v", body["thinking"])
	}
	if body["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort should be preserved when not disabled, got %v", body["reasoning_effort"])
	}
}
