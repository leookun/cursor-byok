package forwarder

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
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

func TestCompletedEditIgnoresLatePartialAndDeltaEvents(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request-1", "conversation-1", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "edit file")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	service := &Service{broker: broker}
	completed := buildCompletedEditToolCall("file.txt", buildSuccessfulEditResult("file.txt", "before", "after", "@@ -1 +1 @@\n-before\n+after\n", 1, 1, ""))
	if err := service.publishToolCallCompleted(stream.RequestID, "edit-1", "model-call-1", completed); err != nil {
		t.Fatalf("publishToolCallCompleted() error = %v", err)
	}

	partial := modeladapter.ModelEvent{
		Kind:       modeladapter.ModelEventKindPartialToolCall,
		ToolCallID: "edit-1",
		ToolCall:   &agentv1.ToolCall{Tool: &agentv1.ToolCall_EditToolCall{EditToolCall: &agentv1.EditToolCall{Args: &agentv1.EditArgs{Path: "file.txt"}}}},
	}
	if err := service.applyProviderModelEvent(stream, partial); err != nil {
		t.Fatalf("applyProviderModelEvent(partial) error = %v", err)
	}
	delta := modeladapter.ModelEvent{
		Kind:       modeladapter.ModelEventKindToolCallDelta,
		ToolCallID: "edit-1",
		ToolCallDelta: &agentv1.ToolCallDelta{Delta: &agentv1.ToolCallDelta_EditToolCallDelta{
			EditToolCallDelta: &agentv1.EditToolCallDelta{StreamContentDelta: "late"},
		}},
	}
	if err := service.applyProviderModelEvent(stream, delta); err != nil {
		t.Fatalf("applyProviderModelEvent(delta) error = %v", err)
	}

	events, err := broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want only completion", len(events))
	}
	completion := events[0].Message.GetInteractionUpdate().GetToolCallCompleted()
	if completion == nil || completion.GetCallId() != "edit-1" || completion.GetToolCall().GetEditToolCall().GetResult().GetSuccess() == nil {
		t.Fatalf("event = %#v, want completed edit with success", events[0].Message)
	}
}

func TestUnfinishedEditForwardsPartialAndDeltaEvents(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request-1", "conversation-1", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "edit file")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	service := &Service{broker: broker}
	partial := modeladapter.ModelEvent{
		Kind:       modeladapter.ModelEventKindPartialToolCall,
		ToolCallID: "edit-1",
		ToolCall:   &agentv1.ToolCall{Tool: &agentv1.ToolCall_EditToolCall{EditToolCall: &agentv1.EditToolCall{Args: &agentv1.EditArgs{Path: "file.txt"}}}},
	}
	if err := service.applyProviderModelEvent(stream, partial); err != nil {
		t.Fatalf("applyProviderModelEvent(partial) error = %v", err)
	}
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{
		Kind:       modeladapter.ModelEventKindToolCallDelta,
		ToolCallID: "edit-1",
		ToolCallDelta: &agentv1.ToolCallDelta{Delta: &agentv1.ToolCallDelta_EditToolCallDelta{
			EditToolCallDelta: &agentv1.EditToolCallDelta{StreamContentDelta: "content"},
		}},
	}); err != nil {
		t.Fatalf("applyProviderModelEvent(delta) error = %v", err)
	}
	events, err := broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	if len(events) != 2 || events[0].Message.GetInteractionUpdate().GetPartialToolCall() == nil || events[1].Message.GetInteractionUpdate().GetToolCallDelta() == nil {
		t.Fatalf("events = %#v, want partial then delta", events)
	}
}

func TestCompletedEditHistoryRetainsBoundedVisibleDiff(t *testing.T) {
	before := strings.Repeat("before\n", projectedPatchEditReplayLimit)
	after := strings.Repeat("after\n", projectedPatchEditReplayLimit)
	diffString, linesAdded, linesRemoved := computeEditDiff(before, after)
	result := buildSuccessfulEditResult("file.txt", before, after, diffString, linesAdded, linesRemoved, "")

	for _, test := range []struct {
		name      string
		compactor func(string, *agentv1.EditResult) *agentv1.EditResult
		limit     int
	}{
		{name: "Write", compactor: compactWriteHistoryEditResult, limit: projectedEditReplayLimit},
		{name: "PatchEdit", compactor: compactPatchEditHistoryEditResult, limit: projectedPatchEditReplayLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			compacted := test.compactor("file.txt", result).GetSuccess()
			if compacted == nil {
				t.Fatal("compacted result is not success")
			}
			if compacted.GetPath() != "file.txt" || compacted.GetDiffString() == "" || compacted.GetLinesAdded() != linesAdded || compacted.GetLinesRemoved() != linesRemoved {
				t.Fatalf("compacted diff = %#v, want visible diff metadata", compacted)
			}
			if len(compacted.GetBeforeFullFileContent()) > test.limit || len(compacted.GetAfterFullFileContent()) > test.limit || len(compacted.GetDiffString()) > test.limit {
				t.Fatalf("compacted content exceeded %d bytes", test.limit)
			}
		})
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
