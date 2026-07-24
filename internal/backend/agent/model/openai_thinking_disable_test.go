package modeladapter

import (
	"testing"
)

// TestOpenAIThinkingDisableKindMiMo 验证小米 MiMo（base_url 含 xiaomimimo/mimo，
// model 含 mimo）被识别为 thinking_type 分支，从而在用户禁用思考时正确写入
// thinking:{type:"disabled"}。回归覆盖 deepseek/glm/qwen/gpt-5 等既有分支。
func TestOpenAIThinkingDisableKindMiMo(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		modelID  string
		endpoint string
		want     string
	}{
		// MiMo —— 修复目标
		{name: "mimo official base + pro model", baseURL: "https://api.xiaomimimo.com/v1", modelID: "mimo-v2.5-pro", endpoint: "/chat/completions", want: "thinking_type"},
		{name: "mimo base by host keyword", baseURL: "https://api.xiaomimimo.com/v1", modelID: "mimo-v2.5", endpoint: "/chat/completions", want: "thinking_type"},
		{name: "mimo model only (custom base)", baseURL: "https://custom.proxy.example.com/v1", modelID: "mimo-v2.5-pro", endpoint: "/chat/completions", want: "thinking_type"},
		{name: "mimo ultraspeed variant", baseURL: "https://api.xiaomimimo.com/v1", modelID: "mimo-v2.5-pro-ultraspeed", endpoint: "/chat/completions", want: "thinking_type"},
		// 回归：既有 thinking_type 分支不受影响
		{name: "deepseek base", baseURL: "https://api.deepseek.com/v1", modelID: "deepseek-chat", endpoint: "/chat/completions", want: "thinking_type"},
		{name: "glm model via zhipu base", baseURL: "https://open.bigmodel.cn/api/paas/v4", modelID: "glm-4.6", endpoint: "/chat/completions", want: "thinking_type"},
		{name: "z.ai base", baseURL: "https://api.z.ai/api/paas/v4", modelID: "glm-4.5", endpoint: "/chat/completions", want: "thinking_type"},
		// 回归：enable_thinking 分支（qwen 系）
		{name: "qwen via dashscope", baseURL: "https://dashscope.aliyuncs.com/v1", modelID: "qwen-max", endpoint: "/chat/completions", want: "enable_thinking"},
		{name: "qwen model keyword", baseURL: "https://custom.example.com/v1", modelID: "qwen3-coder", endpoint: "/chat/completions", want: "enable_thinking"},
		// 回归：reasoning_none 分支（gpt-5.1+/gpt-6）
		{name: "gpt-5.1", baseURL: "https://api.openai.com/v1", modelID: "gpt-5.1", endpoint: "/chat/completions", want: "reasoning_none"},
		{name: "gpt-6", baseURL: "https://api.openai.com/v1", modelID: "gpt-6", endpoint: "/chat/completions", want: "reasoning_none"},
		// 回归：未知 provider 不做 disable
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

// TestApplyOpenAIThinkingDisableMiMo 验证当 ThinkingEffort=disabled 且 provider
// 为 MiMo 时，applyOpenAIThinkingDisable 会写入 thinking:{type:"disabled"} 并删除
// reasoning_effort，与 deepseek/glm 行为一致。
func TestApplyOpenAIThinkingDisableMiMo(t *testing.T) {
	req := StreamRequest{ThinkingEffort: "disabled", RequestKnobs: map[string]any{}}
	body := map[string]any{
		"model":           "mimo-v2.5-pro",
		"messages":        []map[string]any{{"role": "user", "content": "hi"}},
		"reasoning_effort": "high",
	}
	applyOpenAIThinkingDisable(body, req, "https://api.xiaomimimo.com/v1", "mimo-v2.5-pro", "/chat/completions")

	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected body[thinking] to be map[string]any, got %T (%v)", body["thinking"], body["thinking"])
	}
	if thinking["type"] != "disabled" {
		t.Fatalf("expected thinking.type=disabled, got %v", thinking["type"])
	}
	if _, stillPresent := body["reasoning_effort"]; stillPresent {
		t.Fatalf("reasoning_effort should be deleted when thinking disabled for mimo, got %v", body["reasoning_effort"])
	}
	if got := req.RequestKnobs["thinking_disabled_provider_param"]; got != "thinking.type" {
		t.Fatalf("expected request knob thinking_disabled_provider_param=thinking.type, got %v", got)
	}
}

// TestApplyOpenAIThinkingDisableMiMoNotTriggered 验证非 disabled 时不会误写 disable 字段。
func TestApplyOpenAIThinkingDisableMiMoNotTriggered(t *testing.T) {
	req := StreamRequest{ThinkingEffort: "high", RequestKnobs: map[string]any{}}
	body := map[string]any{"model": "mimo-v2.5-pro", "reasoning_effort": "high"}
	applyOpenAIThinkingDisable(body, req, "https://api.xiaomimimo.com/v1", "mimo-v2.5-pro", "/chat/completions")
	if _, present := body["thinking"]; present {
		t.Fatalf("thinking should not be injected when ThinkingEffort != disabled, got %v", body["thinking"])
	}
	if body["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort should be preserved when not disabled, got %v", body["reasoning_effort"])
	}
}
