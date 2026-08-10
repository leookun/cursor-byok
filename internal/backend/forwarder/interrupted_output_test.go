package forwarder

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestAppendEntriesDeduplicatesIdempotencyKey(t *testing.T) {
	store := NewConversationFileStore(t.TempDir())
	entry := HistoryEntry{
		TurnSeq:        1,
		RequestID:      "request-1",
		IdempotencyKey: "provider-interrupted-output:test",
		Role:           "assistant",
		Kind:           "assistant_text",
		Payload:        json.RawMessage(`{"text":"partial"}`),
	}

	if _, assigned, err := store.AppendEntries("conversation-1", []HistoryEntry{entry}); err != nil {
		t.Fatalf("first AppendEntries() error = %v", err)
	} else if len(assigned) != 1 {
		t.Fatalf("first AppendEntries() assigned = %d, want 1", len(assigned))
	}
	if _, assigned, err := store.AppendEntries("conversation-1", []HistoryEntry{entry}); err != nil {
		t.Fatalf("duplicate AppendEntries() error = %v", err)
	} else if len(assigned) != 0 {
		t.Fatalf("duplicate AppendEntries() assigned = %d, want 0", len(assigned))
	}

	conversation, err := store.LoadConversation("conversation-1")
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if len(conversation.Entries) != 1 {
		t.Fatalf("persisted entries = %d, want 1", len(conversation.Entries))
	}
}

func TestCancelPersistsInterruptedProviderOutputIdempotently(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	if _, err := service.store.SaveConversationWithEntries(stream.ConversationID, conversation, conversation.Entries); err != nil {
		t.Fatalf("SaveConversationWithEntries() error = %v", err)
	}

	stream.mu.Lock()
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderAccumulatedText = "partial answer"
	stream.ProviderAccumulatedReasoning = "partial reasoning"
	stream.mu.Unlock()

	cancel := InboundIntent{
		Kind:         "cancel",
		RequestID:    stream.RequestID,
		CancelReason: "[canceled] Superseded by newer request",
	}
	if err := service.handleCancelIntent(cancel); err != nil {
		t.Fatalf("first handleCancelIntent() error = %v", err)
	}
	stream.mu.Lock()
	stream.ProviderAccumulatedText = "late duplicate fragment"
	stream.mu.Unlock()
	if err := service.handleCancelIntent(cancel); err != nil {
		t.Fatalf("duplicate handleCancelIntent() error = %v", err)
	}

	persisted, err := service.store.LoadConversation(stream.ConversationID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	assistantEntries := 0
	cancelEntries := 0
	for _, entry := range persisted.Entries {
		if entry.Kind == "metadata" {
			var payload metadataPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				t.Fatalf("decode metadata entry: %v", err)
			}
			if payload.Type == "control" && readStringValue(payload.Value["status"]) == "canceled" {
				cancelEntries++
			}
		}
		if entry.Kind != "assistant_text" {
			continue
		}
		var payload assistantTextPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatalf("decode assistant entry: %v", err)
		}
		if payload.Text == "partial answer" {
			assistantEntries++
		}
	}
	if assistantEntries != 1 {
		t.Fatalf("persisted interrupted assistant entries = %d, want 1", assistantEntries)
	}
	if cancelEntries != 1 {
		t.Fatalf("persisted cancel metadata entries = %d, want 1", cancelEntries)
	}

	replay, err := service.projector.ProjectPromptReplay(persisted)
	if err != nil {
		t.Fatalf("ProjectPromptReplay() error = %v", err)
	}
	found := false
	for _, message := range replay {
		if message.Role == "assistant" && strings.TrimSpace(message.Content) == "partial answer" && strings.TrimSpace(message.ReasoningContent) == "partial reasoning" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("replay = %#v, want interrupted assistant output", replay)
	}
	checkpoint, err := service.projector.ProjectCheckpointProjection(persisted)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	if checkpoint == nil || checkpoint.State == nil || len(checkpoint.State.GetTurns()) != 1 {
		t.Fatalf("checkpoint state = %#v, want interrupted turn", checkpoint)
	}
}

func TestGenericProviderFailurePersistsAccumulatedOutput(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	if _, err := service.store.SaveConversationWithEntries(stream.ConversationID, conversation, conversation.Entries); err != nil {
		t.Fatalf("SaveConversationWithEntries() error = %v", err)
	}

	stream.mu.Lock()
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderActive = true
	stream.ProviderAccumulatedText = "partial answer before transport failure"
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{
		Done: true,
		Err:  errors.New("transport failed"),
	}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}

	persisted, err := service.store.LoadConversation(stream.ConversationID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	foundPartialOutput := false
	for _, entry := range persisted.Entries {
		if entry.Kind != "assistant_text" {
			continue
		}
		var payload assistantTextPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatalf("decode assistant entry: %v", err)
		}
		if payload.Text == "partial answer before transport failure" {
			foundPartialOutput = true
			break
		}
	}
	if !foundPartialOutput {
		t.Fatal("generic provider failure discarded accumulated assistant output")
	}
}

func TestCancelPreservesPersistedTurnActivityWithoutLiveAccumulator(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	if _, err := service.store.SaveConversationWithEntries(stream.ConversationID, conversation, conversation.Entries); err != nil {
		t.Fatalf("SaveConversationWithEntries() error = %v", err)
	}

	if err := service.handleCancelIntent(InboundIntent{
		Kind:         "cancel",
		RequestID:    stream.RequestID,
		CancelReason: "new_message_submitted",
	}); err != nil {
		t.Fatalf("handleCancelIntent() error = %v", err)
	}

	persisted, err := service.store.LoadConversation(stream.ConversationID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	replay, err := service.projector.ProjectPromptReplay(persisted)
	if err != nil {
		t.Fatalf("ProjectPromptReplay() error = %v", err)
	}
	for _, message := range replay {
		if message.Role == "assistant" && strings.TrimSpace(message.Content) == "hi" {
			return
		}
	}
	t.Fatalf("replay = %#v, want persisted assistant activity", replay)
}

func TestProjectPromptReplayPreservesLegacyCanceledTurnActivity(t *testing.T) {
	cancelEntry := newMetadataEntry(1, "request-1", "control", map[string]any{
		"status":        "canceled",
		"reason":        "new_message_submitted",
		"replay_policy": cancelReplayPolicyKeepStableInput,
	})
	conversation := &ConversationFile{
		ConversationID: "conversation-1",
		NextTurnSeq:    2,
		Entries: []HistoryEntry{
			newAssistantTextEntry(1, "request-1", "persisted activity", "", ""),
			cancelEntry,
		},
	}

	replay, err := NewHistoryProjector().ProjectPromptReplay(conversation)
	if err != nil {
		t.Fatalf("ProjectPromptReplay() error = %v", err)
	}
	for _, message := range replay {
		if message.Role == "assistant" && strings.TrimSpace(message.Content) == "persisted activity" {
			return
		}
	}
	t.Fatalf("replay = %#v, want legacy canceled activity", replay)
}
