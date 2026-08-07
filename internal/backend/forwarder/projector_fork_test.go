package forwarder

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	promptengine "cursor/internal/backend/agent/prompt"
)

func TestProjectCheckpointProjectionBuildsResolvableForkState(t *testing.T) {
	userPayload, err := protojson.Marshal(&agentv1.UserMessage{
		Text:      "parent question",
		MessageId: "message-1",
	})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	conversation := &ConversationFile{
		ConversationID:        "conversation-1",
		RootConversationID:    "conversation-1",
		Mode:                  "agent",
		NextTurnSeq:           2,
		NextEntrySeq:          3,
		TokenDetailsMaxTokens: projectedConversationMaxTokens,
		Entries: []HistoryEntry{
			{Seq: 1, TurnSeq: 1, RequestID: "request-1", Role: "user", Kind: "user_message", Payload: userPayload},
			newAssistantTextEntry(1, "request-1", "parent answer", "", ""),
		},
	}

	projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	state := projection.State
	if len(state.GetTurns()) != 1 {
		t.Fatalf("ProjectCheckpointProjection() turns = %d, want 1 Blob-backed turn", len(state.GetTurns()))
	}
	blobs := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		digest := sha256.Sum256(blob.Data)
		if len(blob.ID) != sha256.Size || string(blob.ID) != string(digest[:]) {
			t.Fatalf("invalid content-addressed Blob id=%x", blob.ID)
		}
		blobs[string(blob.ID)] = blob.Data
	}
	turnPayload, ok := blobs[string(state.GetTurns()[0])]
	if !ok {
		t.Fatal("turn references a missing Blob")
	}
	turn := &agentv1.ConversationTurnStructure{}
	if err := proto.Unmarshal(turnPayload, turn); err != nil {
		t.Fatalf("decode turn Blob: %v", err)
	}
	agentTurn := turn.GetAgentConversationTurn()
	if agentTurn == nil {
		t.Fatal("turn Blob does not contain an agent turn")
	}
	if _, ok := blobs[string(agentTurn.GetUserMessage())]; !ok {
		t.Fatal("turn references a missing user message Blob")
	}
	for _, stepID := range agentTurn.GetSteps() {
		if _, ok := blobs[string(stepID)]; !ok {
			t.Fatal("turn references a missing step Blob")
		}
	}

	messages, err := importedConversationStateModelMessages(state)
	if err != nil {
		t.Fatalf("importedConversationStateModelMessages() error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("imported messages = %d, want parent user and assistant context", len(messages))
	}
	if messages[0].Role != "user" || !strings.Contains(messages[0].Content, "parent question") {
		t.Fatalf("first imported message = %#v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Content != "parent answer" {
		t.Fatalf("second imported message = %#v", messages[1])
	}
}

func TestProjectCheckpointProjectionMergesResumeActivityIntoPreviousUserTurn(t *testing.T) {
	userPayload, err := protojson.Marshal(&agentv1.UserMessage{
		Text:      "original question",
		MessageId: "message-1",
	})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	firstAnswer := newAssistantTextEntry(1, "request-1", "before resume", "", "")
	firstAnswer.Seq = 2
	resumedAnswer := newAssistantTextEntry(2, "request-resume", "after resume", "", "")
	resumedAnswer.Seq = 3
	conversation := &ConversationFile{
		ConversationID:     "conversation-1",
		RootConversationID: "conversation-1",
		Mode:               "agent",
		NextTurnSeq:        3,
		NextEntrySeq:       4,
		Entries: []HistoryEntry{
			{Seq: 1, TurnSeq: 1, RequestID: "request-1", Role: "user", Kind: "user_message", Payload: userPayload},
			firstAnswer,
			resumedAnswer,
		},
	}

	projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	if len(projection.State.GetTurns()) != 1 {
		t.Fatalf("checkpoint turns = %d, want one logical user turn", len(projection.State.GetTurns()))
	}

	blobs := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		blobs[string(blob.ID)] = blob.Data
	}
	turn := &agentv1.ConversationTurnStructure{}
	if err := proto.Unmarshal(blobs[string(projection.State.GetTurns()[0])], turn); err != nil {
		t.Fatalf("decode checkpoint turn: %v", err)
	}
	agentTurn := turn.GetAgentConversationTurn()
	if agentTurn == nil {
		t.Fatal("checkpoint turn does not contain an agent turn")
	}
	if len(agentTurn.GetUserMessage()) == 0 {
		t.Fatal("checkpoint turn lost the original user message Blob id")
	}
	if _, ok := blobs[string(agentTurn.GetUserMessage())]; !ok {
		t.Fatal("checkpoint turn references a missing user message Blob")
	}
	if agentTurn.GetRequestId() != "request-1" {
		t.Fatalf("checkpoint request id = %q, want original request", agentTurn.GetRequestId())
	}

	steps := checkpointProjectionSteps(t, projection)
	if len(steps) != 2 || steps[0].GetAssistantMessage().GetText() != "before resume" || steps[1].GetAssistantMessage().GetText() != "after resume" {
		t.Fatalf("checkpoint steps did not preserve resumed activity: %#v", steps)
	}

	replay, err := promptengine.DecodeReplayMessages(projection.State.GetRootPromptMessagesJson())
	if err != nil {
		t.Fatalf("decode root prompt replay: %v", err)
	}
	if len(replay) != 3 || replay[0].Role != "user" || replay[1].Content != "before resume" || replay[2].Content != "after resume" {
		t.Fatalf("checkpoint turn merge changed model replay: %#v", replay)
	}
}

func TestProjectCheckpointProjectionOmitsActivityWithoutAnyUserMessage(t *testing.T) {
	conversation := &ConversationFile{
		ConversationID: "conversation-1",
		Mode:           "agent",
		NextTurnSeq:    2,
		Entries: []HistoryEntry{
			newAssistantTextEntry(1, "request-resume", "orphaned resume output", "", ""),
		},
	}

	projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	if len(projection.State.GetTurns()) != 0 || len(projection.Blobs) != 0 {
		t.Fatalf("checkpoint emitted a turn without a user message: %#v", projection)
	}
}

func TestProjectCheckpointProjectionKeepsForkPointIsolatedFromLaterHistory(t *testing.T) {
	firstUser, err := protojson.Marshal(&agentv1.UserMessage{Text: "first question", MessageId: "message-1"})
	if err != nil {
		t.Fatalf("marshal first user message: %v", err)
	}
	conversation := &ConversationFile{
		ConversationID:        "conversation-1",
		RootConversationID:    "conversation-1",
		Mode:                  "agent",
		NextTurnSeq:           2,
		NextEntrySeq:          3,
		TokenDetailsMaxTokens: projectedConversationMaxTokens,
		Entries: []HistoryEntry{
			{Seq: 1, TurnSeq: 1, RequestID: "request-1", Role: "user", Kind: "user_message", Payload: firstUser},
			newAssistantTextEntry(1, "request-1", "first answer", "", ""),
		},
	}
	projector := NewHistoryProjector()
	midpoint, err := projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("midpoint projection: %v", err)
	}

	secondUser, err := protojson.Marshal(&agentv1.UserMessage{Text: "second question", MessageId: "message-2"})
	if err != nil {
		t.Fatalf("marshal second user message: %v", err)
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		{TurnSeq: 2, RequestID: "request-2", Role: "user", Kind: "user_message", Payload: secondUser},
		newAssistantTextEntry(2, "request-2", "second answer", "", ""),
	})
	latest, err := projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("latest projection: %v", err)
	}

	midpointMessages, err := importedConversationStateModelMessages(midpoint.State)
	if err != nil {
		t.Fatalf("import midpoint messages: %v", err)
	}
	latestMessages, err := importedConversationStateModelMessages(latest.State)
	if err != nil {
		t.Fatalf("import latest messages: %v", err)
	}
	if len(midpoint.State.GetTurns()) != 1 || len(midpointMessages) != 2 {
		t.Fatalf("midpoint turns=%d messages=%d, want 1 turn and 2 messages", len(midpoint.State.GetTurns()), len(midpointMessages))
	}
	if len(latest.State.GetTurns()) != 2 || len(latestMessages) != 4 {
		t.Fatalf("latest turns=%d messages=%d, want 2 turns and 4 messages", len(latest.State.GetTurns()), len(latestMessages))
	}
	if midpointMessages[1].Content != "first answer" || latestMessages[3].Content != "second answer" {
		t.Fatalf("fork snapshots are not isolated: midpoint=%#v latest=%#v", midpointMessages, latestMessages)
	}
}

func TestProjectCheckpointProjectionMergesToolCallWithCompletedResult(t *testing.T) {
	userPayload, err := protojson.Marshal(&agentv1.UserMessage{Text: "inspect file", MessageId: "message-1"})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	startedAt := uint64(100)
	toolCallID := "call-1"
	startedToolCall := checkpointTestToolCallPayload(t, &agentv1.ToolCall{
		ToolCallId:  &toolCallID,
		StartedAtMs: &startedAt,
		Tool: &agentv1.ToolCall_ReadToolCall{
			ReadToolCall: &agentv1.ReadToolCall{
				Args: &agentv1.ReadToolArgs{Path: "/tmp/example.txt"},
			},
		},
	})
	completedAt := uint64(200)
	completedToolCall := checkpointTestToolCallPayload(t, &agentv1.ToolCall{
		CompletedAtMs: &completedAt,
		Tool: &agentv1.ToolCall_ReadToolCall{
			ReadToolCall: &agentv1.ReadToolCall{
				Result: &agentv1.ReadToolResult{
					Result: &agentv1.ReadToolResult_Success{
						Success: &agentv1.ReadToolSuccess{
							Path:       "/tmp/example.txt",
							TotalLines: 1,
							Output:     &agentv1.ReadToolSuccess_Content{Content: "file contents"},
						},
					},
				},
			},
		},
	})
	conversation := &ConversationFile{
		ConversationID: "conversation-1",
		Mode:           "agent",
		NextTurnSeq:    2,
		Entries: []HistoryEntry{
			{Seq: 1, TurnSeq: 1, RequestID: "request-1", Role: "user", Kind: "user_message", Payload: userPayload},
			newAssistantTextEntry(1, "request-1", "before", "", ""),
			newToolCallEntry(1, "request-1", "call-1", "Read", "", "", startedToolCall),
			newToolResultEntry(1, "request-1", "call-1", "Read", `{"path":"/tmp/example.txt"}`, "file contents", "", completedToolCall),
			newAssistantTextEntry(1, "request-1", "after", "", ""),
		},
	}

	projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	if len(projection.Blobs) != 5 {
		t.Fatalf("checkpoint blobs = %d, want user, three final steps, and turn", len(projection.Blobs))
	}
	steps := checkpointProjectionSteps(t, projection)
	if len(steps) != 3 {
		t.Fatalf("checkpoint steps = %d, want assistant, completed Read, assistant", len(steps))
	}
	if steps[0].GetAssistantMessage().GetText() != "before" || steps[2].GetAssistantMessage().GetText() != "after" {
		t.Fatalf("checkpoint step ordering changed: %#v", steps)
	}
	mergedToolCall := steps[1].GetToolCall()
	readCall := mergedToolCall.GetReadToolCall()
	if readCall == nil || readCall.GetResult().GetSuccess().GetContent() != "file contents" {
		t.Fatalf("checkpoint Read step does not contain completed result: %#v", steps[1].GetToolCall())
	}
	if readCall.GetArgs().GetPath() != "/tmp/example.txt" || mergedToolCall.GetToolCallId() != toolCallID || mergedToolCall.GetStartedAtMs() != startedAt || mergedToolCall.GetCompletedAtMs() != completedAt {
		t.Fatalf("checkpoint Read step lost started-call fields: %#v", mergedToolCall)
	}

	replay, err := promptengine.DecodeReplayMessages(projection.State.GetRootPromptMessagesJson())
	if err != nil {
		t.Fatalf("decode root prompt replay: %v", err)
	}
	for _, message := range replay {
		if message.Name == "Read" || len(message.ToolCalls) > 0 {
			t.Fatalf("UI-only Read result leaked into root prompt replay: %#v", replay)
		}
	}
}

func TestProjectCheckpointProjectionIsIdempotentAndDoesNotMutateHistory(t *testing.T) {
	userPayload, err := protojson.Marshal(&agentv1.UserMessage{Text: "inspect file", MessageId: "message-1"})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	completedToolCall := checkpointTestReadToolCall(t, &agentv1.ReadToolResult{
		Result: &agentv1.ReadToolResult_Success{
			Success: &agentv1.ReadToolSuccess{
				Path:       "/tmp/example.txt",
				TotalLines: 1,
				Output:     &agentv1.ReadToolSuccess_Content{Content: "file contents"},
			},
		},
	})
	conversation := &ConversationFile{
		ConversationID: "conversation-1",
		Mode:           "agent",
		NextTurnSeq:    2,
		Entries: []HistoryEntry{
			{Seq: 1, TurnSeq: 1, RequestID: "request-1", Role: "user", Kind: "user_message", Payload: userPayload},
			newToolCallEntry(1, "request-1", "call-1", "Read", "", "", checkpointTestReadToolCall(t, nil)),
			newToolResultEntry(1, "request-1", "call-1", "Read", `{"path":"/tmp/example.txt"}`, "file contents", "", completedToolCall),
		},
	}
	before, err := json.Marshal(conversation)
	if err != nil {
		t.Fatalf("marshal conversation before projection: %v", err)
	}

	projector := NewHistoryProjector()
	first, err := projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("first projection: %v", err)
	}
	second, err := projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("second projection: %v", err)
	}

	if !proto.Equal(first.State, second.State) {
		t.Fatalf("repeated projection changed checkpoint state: first=%#v second=%#v", first.State, second.State)
	}
	if len(first.Blobs) != len(second.Blobs) {
		t.Fatalf("repeated projection changed blob count: first=%d second=%d", len(first.Blobs), len(second.Blobs))
	}
	for index := range first.Blobs {
		if !bytes.Equal(first.Blobs[index].ID, second.Blobs[index].ID) || !bytes.Equal(first.Blobs[index].Data, second.Blobs[index].Data) {
			t.Fatalf("repeated projection changed blob %d", index)
		}
	}
	after, err := json.Marshal(conversation)
	if err != nil {
		t.Fatalf("marshal conversation after projection: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("checkpoint projection mutated semantic history:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestProjectCheckpointProjectionKeepsStartedToolCallWhenResultPayloadIsMissing(t *testing.T) {
	startedToolCall := checkpointTestReadToolCall(t, nil)
	conversation := &ConversationFile{
		ConversationID: "conversation-1",
		Mode:           "agent",
		NextTurnSeq:    2,
		Entries: []HistoryEntry{
			testCheckpointUserEntry(t),
			newToolCallEntry(1, "request-1", "call-1", "Read", "", "", startedToolCall),
			newToolResultEntry(1, "request-1", "call-1", "Read", `{"path":"/tmp/example.txt"}`, "read failed", "", nil),
		},
	}

	projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	steps := checkpointProjectionSteps(t, projection)
	if len(steps) != 1 {
		t.Fatalf("checkpoint steps = %d, want the original Read step", len(steps))
	}
	readCall := steps[0].GetToolCall().GetReadToolCall()
	if readCall == nil || readCall.GetArgs().GetPath() != "/tmp/example.txt" || readCall.GetResult() != nil {
		t.Fatalf("checkpoint did not preserve the original Read call: %#v", steps[0].GetToolCall())
	}
}

func TestProjectCheckpointProjectionAppendsLegacyResultWithoutToolCallEntry(t *testing.T) {
	completedToolCall := checkpointTestReadToolCall(t, &agentv1.ReadToolResult{
		Result: &agentv1.ReadToolResult_Error{Error: &agentv1.ReadToolError{ErrorMessage: "not readable"}},
	})
	conversation := &ConversationFile{
		ConversationID: "conversation-1",
		Mode:           "agent",
		NextTurnSeq:    2,
		Entries: []HistoryEntry{
			testCheckpointUserEntry(t),
			newToolResultEntry(1, "request-1", "call-1", "Read", `{"path":"/tmp/example.txt"}`, "not readable", "", completedToolCall),
		},
	}

	projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	steps := checkpointProjectionSteps(t, projection)
	if len(steps) != 1 || steps[0].GetToolCall().GetReadToolCall().GetResult().GetError().GetErrorMessage() != "not readable" {
		t.Fatalf("legacy result-only Read step was not preserved: %#v", steps)
	}
}

func checkpointTestReadToolCall(t *testing.T, result *agentv1.ReadToolResult) []byte {
	t.Helper()
	return checkpointTestToolCallPayload(t, &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ReadToolCall{
			ReadToolCall: &agentv1.ReadToolCall{
				Args:   &agentv1.ReadToolArgs{Path: "/tmp/example.txt"},
				Result: result,
			},
		},
	})
}

func checkpointTestToolCallPayload(t *testing.T, toolCall *agentv1.ToolCall) []byte {
	t.Helper()
	payload, err := protojson.Marshal(toolCall)
	if err != nil {
		t.Fatalf("marshal Read tool call: %v", err)
	}
	return payload
}

func checkpointProjectionSteps(t *testing.T, projection *CheckpointProjection) []*agentv1.ConversationStep {
	t.Helper()
	if projection == nil || projection.State == nil || len(projection.State.GetTurns()) != 1 {
		t.Fatalf("checkpoint turns = %#v, want exactly one turn", projection)
	}
	blobs := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		blobs[string(blob.ID)] = blob.Data
	}
	turn := &agentv1.ConversationTurnStructure{}
	if err := proto.Unmarshal(blobs[string(projection.State.GetTurns()[0])], turn); err != nil {
		t.Fatalf("decode checkpoint turn: %v", err)
	}
	agentTurn := turn.GetAgentConversationTurn()
	if agentTurn == nil {
		t.Fatal("checkpoint turn does not contain an agent turn")
	}
	steps := make([]*agentv1.ConversationStep, 0, len(agentTurn.GetSteps()))
	for _, stepID := range agentTurn.GetSteps() {
		step := &agentv1.ConversationStep{}
		if err := proto.Unmarshal(blobs[string(stepID)], step); err != nil {
			t.Fatalf("decode checkpoint step: %v", err)
		}
		steps = append(steps, step)
	}
	return steps
}
