package modeladapter

import (
	"strings"
	"testing"
)

// TestNormalizeAnthropicProviderMessagesThinkingCarrier 验证 thinking 模式下，
// 缺少 reasoning 的 assistant 轮次（如 DeepSeek adaptive thinking 跳过思考的
// tool-call 轮次）会用请求内最近一个 carrier 的 thinking+signature 兜底，
// 保证每个 assistant 轮次都有 thinking 块，避免上游 "thinking must be passed
// back to the API" 400。
func TestNormalizeAnthropicProviderMessagesThinkingCarrier(t *testing.T) {
	carrierToolCall := []ToolCallDescriptor{{
		ID:   "call-2",
		Type: "function",
		Function: ToolCallFunctionShape{
			Name:      "read",
			Arguments: `{}`,
		},
	}}
	input := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "let me check", ReasoningContent: "R1", ReasoningSignature: "S1"},
		{Role: "user", Content: "tool result 1"},
		{Role: "assistant", ToolCalls: carrierToolCall}, // 无 reasoning → 用 carrier
		{Role: "user", Content: "tool result 2"},
		{Role: "assistant", Content: "done", ReasoningContent: "R2", ReasoningSignature: "S2"},
	}

	_, messages, err := normalizeAnthropicProviderMessages(input, true, false)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(messages) != 6 {
		t.Fatalf("expected 6 messages, got %d", len(messages))
	}

	// 第 2 条（有 reasoning）应保留自己的 thinking。
	assertAnthropicThinkingBlock(t, messages[1], "R1", "S1")
	// 第 4 条（无 reasoning 的 tool-call 轮次）应复用 carrier 的 thinking+signature。
	assertAnthropicThinkingBlock(t, messages[3], "R1", "S1")
	// 第 5 条应为 tool_result 消息（合并路径不适用时，tool-call 轮次独立成消息）。
	if role := messages[4].Role; role != "user" {
		t.Fatalf("expected messages[4] role=user, got %s", role)
	}
	// 第 6 条有自己的 thinking。
	assertAnthropicThinkingBlock(t, messages[5], "R2", "S2")

	// tool-call 轮次应包含 tool_use 块。
	hasToolUse := false
	for _, block := range messages[3].Content {
		if strings.TrimSpace(anthropicStringField(block, "type")) == "tool_use" {
			hasToolUse = true
		}
	}
	if !hasToolUse {
		t.Fatal("expected tool_use block on the carrier-fallback assistant message")
	}
}

// TestNormalizeAnthropicProviderMessagesThinkingCarrierFirstTurn 验证请求内第一条
// assistant 轮次就缺 reasoning 且无 carrier 时，兜底输出空 thinking 块。
func TestNormalizeAnthropicProviderMessagesThinkingCarrierFirstTurn(t *testing.T) {
	input := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "ok"},
	}

	_, messages, err := normalizeAnthropicProviderMessages(input, true, false)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if got := anthropicStringField(messages[1].Content[0], "type"); got != "thinking" {
		t.Fatalf("expected first block type=thinking, got %s", got)
	}
	if got := anthropicStringField(messages[1].Content[0], "thinking"); got != "" {
		t.Fatalf("expected empty fallback thinking, got %q", got)
	}
}

// TestNormalizeAnthropicProviderMessagesThinkingDisabled 验证 thinking 关闭时
// 不输出任何 thinking 块（回归保护）。
func TestNormalizeAnthropicProviderMessagesThinkingDisabled(t *testing.T) {
	input := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "ok", ReasoningContent: "R1", ReasoningSignature: "S1"},
		{Role: "assistant", ToolCalls: []ToolCallDescriptor{{
			ID:   "call-2",
			Type: "function",
			Function: ToolCallFunctionShape{
				Name:      "read",
				Arguments: `{}`,
			},
		}}},
	}

	_, messages, err := normalizeAnthropicProviderMessages(input, false, false)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	for index, message := range messages {
		for _, block := range message.Content {
			if blockType := anthropicStringField(block, "type"); blockType == "thinking" {
				t.Fatalf("unexpected thinking block at messages[%d]", index)
			}
		}
	}
}

// TestNormalizeAnthropicProviderMessagesThinkingMerge 验证有 reasoning 的纯
// tool-call 轮次仍按既有逻辑合并进上一条 assistant 消息（thinking 去重，无回归）。
func TestNormalizeAnthropicProviderMessagesThinkingMerge(t *testing.T) {
	input := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "let me check", ReasoningContent: "R1", ReasoningSignature: "S1"},
		{Role: "assistant", ToolCalls: []ToolCallDescriptor{{
			ID:   "call-2",
			Type: "function",
			Function: ToolCallFunctionShape{
				Name:      "read",
				Arguments: `{}`,
			},
		}}, ReasoningContent: "R1", ReasoningSignature: "S1"},
	}

	_, messages, err := normalizeAnthropicProviderMessages(input, true, false)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages (tool-call merged), got %d", len(messages))
	}
	assertAnthropicThinkingBlock(t, messages[1], "R1", "S1")
	hasToolUse := false
	for _, block := range messages[1].Content {
		if blockType := anthropicStringField(block, "type"); blockType == "tool_use" {
			hasToolUse = true
		}
	}
	if !hasToolUse {
		t.Fatal("expected merged tool_use block on messages[1]")
	}
}

func assertAnthropicThinkingBlock(t *testing.T, message anthropicMessage, wantThinking string, wantSignature string) {
	t.Helper()
	if len(message.Content) == 0 {
		t.Fatalf("expected non-empty content for %s message", message.Role)
	}
	first := message.Content[0]
	if blockType := anthropicStringField(first, "type"); blockType != "thinking" {
		t.Fatalf("expected first block type=thinking, got %s", blockType)
	}
	if got := anthropicStringField(first, "thinking"); got != wantThinking {
		t.Fatalf("expected thinking=%q, got %q", wantThinking, got)
	}
	if got := anthropicStringField(first, "signature"); got != wantSignature {
		t.Fatalf("expected signature=%q, got %q", wantSignature, got)
	}
}
