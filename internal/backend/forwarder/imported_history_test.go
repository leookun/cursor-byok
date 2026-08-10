package forwarder

import (
	"crypto/sha256"
	"testing"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

func TestImportedConversationStateRestoresBlobOnlyForkAndCheckpointPrefix(t *testing.T) {
	parent := compactionAppendOnlyConversation(t)
	parent.Entries = parent.Entries[:2]
	parent.NextEntrySeq = 3
	parent.NextTurnSeq = 2
	projection, err := NewHistoryProjector().ProjectCheckpointProjection(parent)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	prefetched := make([]*agentv1.PreFetchedBlob, 0, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		prefetched = append(prefetched, &agentv1.PreFetchedBlob{Id: blob.ID, Value: blob.Data})
	}
	state := proto.Clone(projection.State).(*agentv1.ConversationStateStructure)
	state.RootPromptMessagesJson = nil
	conversation, err := newRuntimeConversation("fork-conversation", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatalf("newRuntimeConversation() error = %v", err)
	}
	entries, err := (&Service{}).importConversationState(conversation, state, prefetched)
	if err != nil {
		t.Fatalf("importConversationState() error = %v", err)
	}
	if len(conversation.ImportedTurnIDs) != 1 || conversation.NextTurnSeq != 2 {
		t.Fatalf("imported prefix turns=%d next_turn_seq=%d, want 1 and 2", len(conversation.ImportedTurnIDs), conversation.NextTurnSeq)
	}
	if len(entries) != 2 {
		t.Fatalf("imported model entries = %d, want parent user and assistant", len(entries))
	}
	appendEntriesInPlace(conversation, append(entries,
		compactionTestUserEntry(t, 2, "request-2", "fork question", "message-2"),
	))
	forkProjection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("fork ProjectCheckpointProjection() error = %v", err)
	}
	if len(forkProjection.State.GetTurns()) != 2 {
		t.Fatalf("fork checkpoint turns = %d, want imported parent plus local fork turn", len(forkProjection.State.GetTurns()))
	}
	if string(forkProjection.State.GetTurns()[0]) != string(projection.State.GetTurns()[0]) {
		t.Fatal("fork checkpoint did not preserve the imported parent turn ID as its prefix")
	}
}

func TestImportedConversationStateRejectsUnresolvedBlobTurn(t *testing.T) {
	turnID := sha256.Sum256([]byte("missing imported turn"))
	conversation, err := newRuntimeConversation("fork-conversation", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatalf("newRuntimeConversation() error = %v", err)
	}
	if _, err := (&Service{}).importConversationState(conversation, &agentv1.ConversationStateStructure{
		Turns: [][]byte{turnID[:]},
	}, nil); err == nil {
		t.Fatal("importConversationState() accepted an unresolved Blob turn")
	}
}

func TestImportedTurnIDsPersistThroughConversationStore(t *testing.T) {
	store := NewConversationFileStore(t.TempDir())
	turnID := sha256.Sum256([]byte("parent turn"))
	conversation, err := newRuntimeConversation("fork-conversation", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatalf("newRuntimeConversation() error = %v", err)
	}
	conversation.ImportedTurnIDs = [][]byte{turnID[:]}
	persisted, err := store.SaveConversationWithEntries(conversation.ConversationID, conversation, []HistoryEntry{
		compactionTestUserEntry(t, 2, "request-2", "fork question", "message-2"),
	})
	if err != nil {
		t.Fatalf("SaveConversationWithEntries() error = %v", err)
	}
	if len(persisted.ImportedTurnIDs) != 1 || string(persisted.ImportedTurnIDs[0]) != string(turnID[:]) {
		t.Fatalf("persisted ImportedTurnIDs = %x, want %x", persisted.ImportedTurnIDs, turnID)
	}
	loaded, err := store.LoadConversation(conversation.ConversationID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if len(loaded.ImportedTurnIDs) != 1 || string(loaded.ImportedTurnIDs[0]) != string(turnID[:]) {
		t.Fatalf("loaded ImportedTurnIDs = %x, want %x", loaded.ImportedTurnIDs, turnID)
	}
}

func TestRewindImportedTurnPrefixUsesClientForkPoint(t *testing.T) {
	ids := make([][]byte, 3)
	for index := range ids {
		digest := sha256.Sum256([]byte{byte(index + 1)})
		ids[index] = digest[:]
	}
	trimmed := rewindImportedTurnPrefix(ids, runRewindDecision{
		TargetTurnSeq:      4,
		HasClientTurnCount: true,
		ClientTurnCount:    1,
	})
	if len(trimmed) != 1 || string(trimmed[0]) != string(ids[0]) {
		t.Fatalf("rewindImportedTurnPrefix() = %x, want first imported turn only", trimmed)
	}
}
