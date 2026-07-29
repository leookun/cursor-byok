package promptengine

import (
	"reflect"
	"testing"
)

func TestBuildReplayMessagesFromPendingAssistantOutputsKeepsTextAndToolCallInOneAssistantTurn(t *testing.T) {
	raw := `{
		"id":"1",
		"role":"assistant",
		"content":[
			{"type":"reasoning","text":"I need to update service.go.","signature":"reasoning-signature"},
			{"type":"text","text":"Now let me pass stream.Mode in service.go."},
			{"type":"tool-call","toolCallId":"call_1","toolName":"PatchEdit","args":{"path":"/workspace/service.go","old_string":"old","new_string":"new"},"result":{"success":{"path":"/workspace/service.go"}}}
		]
	}`

	first := BuildReplayMessagesFromPendingAssistantOutputs([]string{raw})
	if len(first) != 2 {
		t.Fatalf("message count = %d, want 2 (assistant tool-call turn plus tool result): %#v", len(first), first)
	}

	assistant := first[0]
	if assistant.Role != "assistant" {
		t.Fatalf("assistant role = %q, want assistant", assistant.Role)
	}
	if assistant.Content != "Now let me pass stream.Mode in service.go." {
		t.Fatalf("assistant content = %q", assistant.Content)
	}
	if assistant.ReasoningContent != "I need to update service.go." {
		t.Fatalf("assistant reasoning = %q", assistant.ReasoningContent)
	}
	if assistant.ReasoningSignature != "reasoning-signature" {
		t.Fatalf("assistant reasoning signature = %q", assistant.ReasoningSignature)
	}
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant tool calls = %d, want 1", len(assistant.ToolCalls))
	}
	if assistant.ToolCalls[0].ID != "call_1" || assistant.ToolCalls[0].Function.Name != "PatchEdit" {
		t.Fatalf("assistant tool call = %#v", assistant.ToolCalls[0])
	}

	toolResult := first[1]
	if toolResult.Role != "tool" || toolResult.ToolCallID != "call_1" || toolResult.Name != "PatchEdit" {
		t.Fatalf("tool result = %#v", toolResult)
	}

	second := BuildReplayMessagesFromPendingAssistantOutputs([]string{raw})
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("rebuilding the same pending output changed messages:\nfirst: %#v\nsecond: %#v", first, second)
	}
}
