package config

import "testing"

// TestNormalizeReasoningEffortFallback 锁定：未识别值必须回退到 "medium"，
// 而不是返回空字符串触发 validate 的 "reasoningEffort 仅支持 low、medium、high、xhigh" 报错。
// 回归：Cursor 新增 "minimal" / "none" 等值时不应阻塞配置保存。
func TestNormalizeReasoningEffortFallback(t *testing.T) {
	cases := map[string]string{
		"":         "medium",
		"medium":   "medium",
		"low":      "low",
		"high":     "high",
		"xhigh":    "xhigh",
		"  HIGH  ": "high",
		"MINIMAL":  "medium", // 未识别 → medium（关键回归点）
		"none":     "medium",
		"auto":     "medium",
		"garbage":  "medium",
	}
	for input, want := range cases {
		got := normalizeReasoningEffort(input)
		if got != want {
			t.Errorf("normalizeReasoningEffort(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestNormalizeAnthropicThinkingEffortFallback 同样锁定：未识别值必须回退到 "xhigh"。
// 当前实现已正确，此测试作为对称防护，防止后续回归到返回 ""。
func TestNormalizeAnthropicThinkingEffortFallback(t *testing.T) {
	cases := map[string]string{
		"":         "xhigh",
		"xhigh":    "xhigh",
		"low":      "low",
		"medium":   "medium",
		"high":     "high",
		"max":      "max",
		"  High  ": "high",
		"unknown":  "xhigh", // 未识别 → xhigh（对称防护）
	}
	for input, want := range cases {
		got := normalizeAnthropicThinkingEffort(input)
		if got != want {
			t.Errorf("normalizeAnthropicThinkingEffort(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestNormalizeModelAdaptersAcceptsUnknownReasoningEffort 端到端验证：
// openai 适配器带未识别的 reasoningEffort 应被静默回退，不应报错。
func TestNormalizeModelAdaptersAcceptsUnknownReasoningEffort(t *testing.T) {
	adapters := []ModelAdapterConfig{
		{
			DisplayName:     "test-model",
			Type:            "openai",
			BaseURL:         "https://api.example.com",
			APIKey:          "sk-test",
			TooltipData:     "tip",
			ModelID:         "test-model",
			ReasoningEffort: "minimal", // 未识别值
			OpenAIEndpoint:  "/v1/chat/completions",
		},
	}
	normalized, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("NormalizeModelAdapters rejected unknown reasoningEffort: %v", err)
	}
	if got := normalized[0].ReasoningEffort; got != "medium" {
		t.Errorf("expected reasoningEffort to fall back to \"medium\", got %q", got)
	}
}
