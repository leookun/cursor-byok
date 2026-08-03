package forwarder

import (
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
)

func TestTakeProviderOutputForToolConsumesReasoningOnce(t *testing.T) {
	stream := &ActiveStream{
		ProviderAccumulatedText:                     "I will inspect both files.",
		ProviderAccumulatedReasoning:                "Planning two reads.",
		ProviderAccumulatedReasoningSignature:       "signature-1",
		ProviderAccumulatedReasoningSignatureSource: "openai_responses",
		ProviderAccumulatedReasoningItemID:          "reasoning-1",
		ProviderAccumulatedReasoningStatus:          "completed",
		ProviderAccumulatedReasoningSummary:         []byte(`[{"type":"summary_text","text":"Planning two reads."}]`),
	}

	text, first := takeProviderOutputForTool(stream)
	if text != "I will inspect both files." || first.Content != "Planning two reads." || first.Signature != "signature-1" {
		t.Fatalf("first tool output = (%q, %#v)", text, first)
	}
	if first.ItemID != "reasoning-1" || first.Status != "completed" || string(first.Summary) == "" {
		t.Fatalf("first tool reasoning metadata = %#v", first)
	}

	text, second := takeProviderOutputForTool(stream)
	if text != "" || second.Content != "" || second.Signature != "" || second.ItemID != "" || len(second.Summary) != 0 {
		t.Fatalf("second tool received duplicate provider output = (%q, %#v)", text, second)
	}
}

func TestToolResultReasoningFallbackOnlyPersistsWhenToolCallIsMissing(t *testing.T) {
	toolCall := actorTestEditToolCall(t, "file.txt")
	withToolCall := &ActiveStream{CheckpointConversation: testConversation([]HistoryEntry{
		newToolCallEntry(1, "request-1", "call-1", "Edit", "reasoning", "signature-1", toolCall),
	})}
	if got := toolResultReasoningFallback(withToolCall, "call-1", "reasoning"); got != "" {
		t.Fatalf("tool result duplicated persisted reasoning = %q", got)
	}

	withoutToolCall := &ActiveStream{CheckpointConversation: testConversation(nil)}
	if got := toolResultReasoningFallback(withoutToolCall, "call-1", "reasoning"); got != "reasoning" {
		t.Fatalf("orphan tool result lost replay fallback = %q", got)
	}
}

func TestProjectorKeepsSharedReasoningOnOnlyOneOfMultipleToolCalls(t *testing.T) {
	firstTool := actorTestEditToolCall(t, "first.txt")
	secondTool := actorTestEditToolCall(t, "second.txt")
	conversation := testConversation([]HistoryEntry{
		testUserMessageEntry(t, 1, "request-1", "inspect both files"),
		newToolCallEntry(1, "request-1", "call-1", "Edit", "Planning two reads.", "signature-1", firstTool),
		newToolResultEntry(1, "request-1", "call-1", "Edit", `{"path":"first.txt"}`, "first read", "", firstTool),
		newToolCallEntry(1, "request-1", "call-2", "Edit", "", "", secondTool),
		newToolResultEntry(1, "request-1", "call-2", "Edit", `{"path":"second.txt"}`, "second read", "", secondTool),
	})

	messages, err := NewHistoryProjector().ProjectPromptReplay(conversation)
	if err != nil {
		t.Fatalf("ProjectPromptReplay() error = %v", err)
	}
	if len(messages) != 5 {
		t.Fatalf("replay messages = %d, want user, two tool calls, and two results", len(messages))
	}
	if messages[1].ReasoningContent != "Planning two reads." || messages[3].ReasoningContent != "" {
		t.Fatalf("replay reasoning = (%q, %q), want one shared prefix", messages[1].ReasoningContent, messages[3].ReasoningContent)
	}
	if len(messages[1].ToolCalls) != 1 || len(messages[3].ToolCalls) != 1 {
		t.Fatalf("tool calls were not retained: %#v", messages)
	}
}

func actorTestEditToolCall(t *testing.T, path string) []byte {
	t.Helper()
	payload, err := protojson.Marshal(&agentv1.ToolCall{
		Tool: &agentv1.ToolCall_EditToolCall{
			EditToolCall: &agentv1.EditToolCall{Args: &agentv1.EditArgs{Path: path}},
		},
	})
	if err != nil {
		t.Fatalf("marshal edit tool call: %v", err)
	}
	return payload
}
