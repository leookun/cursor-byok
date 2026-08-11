package forwarder

import (
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
)

func TestApplyCompactionToConversationPreservesCanonicalHistory(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	originalEntries := append([]HistoryEntry(nil), conversation.Entries...)
	plan := &PendingCompaction{
		Trigger:          "manual",
		CurrentTurnSeq:   2,
		CurrentRequestID: "request-2",
	}

	if err := applyCompactionToConversation(conversation, plan, "earlier context summary"); err != nil {
		t.Fatalf("applyCompactionToConversation() error = %v", err)
	}
	if len(conversation.Entries) <= len(originalEntries) {
		t.Fatalf("entries after compaction = %d, want the %d original entries plus a summary marker", len(conversation.Entries), len(originalEntries))
	}
	if !reflect.DeepEqual(conversation.Entries[:len(originalEntries)], originalEntries) {
		t.Fatal("compaction changed the canonical history prefix")
	}

	projector := NewHistoryProjector()
	projection, err := projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	if len(projection.State.GetTurns()) != 2 {
		t.Fatalf("checkpoint turns after compaction = %d, want 2 visible turns", len(projection.State.GetTurns()))
	}
	replay, err := projector.ProjectPromptReplay(conversation)
	if err != nil {
		t.Fatalf("ProjectPromptReplay() error = %v", err)
	}
	if len(replay) != 1 || replay[0].Role != "user" || !strings.Contains(replay[0].Content, "earlier context summary") {
		t.Fatalf("prompt replay after compaction = %#v, want only the compacted summary", replay)
	}
}

func TestCompactedPromptProjectionPlacesSummaryBeforePreservedCurrentTurn(t *testing.T) {
	conversation := &ConversationFile{
		ConversationID:     "conversation-1",
		RootConversationID: "conversation-1",
		Mode:               "agent",
		NextTurnSeq:        1,
		NextEntrySeq:       1,
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-1", "current question", "message-1"),
		newToolCallEntry(1, "request-1", "call-1", "Read", "", "", checkpointTestReadToolCall(t, nil)),
		newToolResultEntry(1, "request-1", "call-1", "Read", `{"path":"/tmp/example.txt"}`, "file contents", "", checkpointTestReadToolCall(t, nil)),
	})
	plan := &PendingCompaction{
		Trigger:                   "auto",
		CurrentTurnSeq:            1,
		CurrentRequestID:          "request-1",
		PreserveCurrentTurnInputs: true,
	}
	if err := applyCompactionToConversation(conversation, plan, "current progress summary"); err != nil {
		t.Fatalf("applyCompactionToConversation() error = %v", err)
	}

	projected := compactedPromptProjectionEntries(conversation.Entries)
	promptKinds := make([]string, 0, len(projected))
	for _, entry := range projected {
		if isPromptReplayEntryKind(entry.Kind) {
			promptKinds = append(promptKinds, entry.Kind)
		}
	}
	want := []string{"compacted_summary", "user_message", "tool_call", "tool_result"}
	if !reflect.DeepEqual(promptKinds, want) {
		t.Fatalf("compacted prompt entry order = %#v, want %#v", promptKinds, want)
	}
}

func TestCompactionPlanningDoesNotRecompactArchivedHistory(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	if err := applyCompactionToConversation(conversation, &PendingCompaction{
		Trigger:          "manual",
		CurrentTurnSeq:   2,
		CurrentRequestID: "request-2",
	}, "archived history summary"); err != nil {
		t.Fatalf("applyCompactionToConversation() error = %v", err)
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 3, "request-3", "new question", "message-3"),
	})

	plan, err := (&Service{}).buildLegacyCompactionPlan(&compactionPlan{
		CurrentTurnSeq:   3,
		CurrentRequestID: "request-3",
	}, conversation, false, 0)
	if err != nil {
		t.Fatalf("buildLegacyCompactionPlan() error = %v", err)
	}
	if plan != nil {
		t.Fatalf("buildLegacyCompactionPlan() = %#v, want no already summarized candidates", plan)
	}
}

func TestApplyCompactionPlanPersistsHistoryAppendOnly(t *testing.T) {
	store := NewConversationFileStore(t.TempDir())
	conversation := compactionAppendOnlyConversation(t)
	if _, _, err := store.AppendEntries(conversation.ConversationID, resetEntrySequences(conversation.Entries)); err != nil {
		t.Fatalf("AppendEntries() error = %v", err)
	}
	persisted, err := store.LoadConversation(conversation.ConversationID)
	if err != nil {
		t.Fatalf("initial LoadConversation() error = %v", err)
	}
	originalEntries := append([]HistoryEntry(nil), persisted.Entries...)
	projector := NewHistoryProjector()
	service := &Service{
		store:     store,
		projector: projector,
		compiler:  compactionProjectionCompiler{projector: projector},
	}
	stream := &ActiveStream{
		RequestID:              "request-2",
		ConversationID:         conversation.ConversationID,
		TurnSeq:                2,
		Mode:                   agentv1.AgentMode_AGENT_MODE_AGENT,
		CheckpointConversation: persisted,
	}
	plan := &PendingCompaction{
		Trigger:           "manual",
		CurrentTurnSeq:    2,
		CurrentRequestID:  "request-2",
		ContextWindowSize: 1_000_000,
	}
	if err := service.applyCompactionPlan(stream, conversation.ConversationID, plan, "persisted summary"); err != nil {
		t.Fatalf("applyCompactionPlan() error = %v", err)
	}

	loaded, err := store.LoadConversation(conversation.ConversationID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if len(loaded.Entries) <= len(originalEntries) {
		t.Fatalf("persisted entries after compaction = %d, want more than %d", len(loaded.Entries), len(originalEntries))
	}
	for index := range originalEntries {
		if !reflect.DeepEqual(loaded.Entries[index], originalEntries[index]) {
			t.Fatalf("persisted history entry %d changed after compaction:\ngot  %#v\nwant %#v", index, loaded.Entries[index], originalEntries[index])
		}
	}
}

type compactionProjectionCompiler struct {
	projector *HistoryProjector
}

func (compiler compactionProjectionCompiler) Compile(conversation *ConversationFile, _ agentv1.AgentMode, _ string, _ string) (CompiledConversation, error) {
	messages, err := compiler.projector.ProjectPromptReplay(conversation)
	return CompiledConversation{Messages: messages}, err
}

func (compactionProjectionCompiler) DerivePromptContexts(*ConversationFile, agentv1.AgentMode, string) ([]PromptContextMessage, error) {
	return nil, nil
}

func compactionAppendOnlyConversation(t *testing.T) *ConversationFile {
	t.Helper()
	conversation := &ConversationFile{
		ConversationID:         "conversation-1",
		RootConversationID:     "conversation-1",
		Mode:                   "agent",
		NextTurnSeq:            1,
		NextEntrySeq:           1,
		TokenDetailsUsedTokens: 42_000,
		TokenDetailsMaxTokens:  50_000,
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-1", "first question", "message-1"),
		newAssistantTextEntry(1, "request-1", "first answer", "", ""),
		compactionTestUserEntry(t, 2, "request-2", "second question", "message-2"),
		newAssistantTextEntry(2, "request-2", "second answer", "", ""),
	})
	return conversation
}

func compactionTestUserEntry(t *testing.T, turnSeq int64, requestID string, text string, messageID string) HistoryEntry {
	t.Helper()
	payload, err := protojson.Marshal(&agentv1.UserMessage{Text: text, MessageId: messageID})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	return HistoryEntry{
		TurnSeq:   turnSeq,
		RequestID: requestID,
		Role:      "user",
		Kind:      "user_message",
		Payload:   payload,
	}
}

var _ PromptCompiler = compactionProjectionCompiler{}
