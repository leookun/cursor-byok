package forwarder

import (
	"fmt"
	"strings"
	"time"

	runtimecore "cursor/internal/backend/agent/core"
)

func (service *Service) replaceCheckpointConversation(stream *ActiveStream, conversation *ConversationFile) error {
	if stream == nil {
		return fmt.Errorf("active stream is required")
	}
	if conversation == nil {
		return fmt.Errorf("checkpoint conversation is required")
	}
	stream.mu.Lock()
	stream.CheckpointConversation = cloneConversationFile(conversation)
	stream.mu.Unlock()
	return nil
}

func checkpointConversationInitialized(stream *ActiveStream) bool {
	if stream == nil {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.CheckpointConversation != nil
}

func (service *Service) appendCheckpointEntries(stream *ActiveStream, entries []HistoryEntry) error {
	if len(entries) == 0 {
		return nil
	}
	if stream == nil {
		return fmt.Errorf("active stream is required")
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.CheckpointConversation == nil {
		return fmt.Errorf("checkpoint conversation is not initialized")
	}
	appendEntriesInPlace(stream.CheckpointConversation, entries)
	return nil
}

func (service *Service) snapshotCheckpointConversation(stream *ActiveStream) (*ConversationFile, []runtimecore.PendingExec, []runtimecore.PendingInteraction, error) {
	if stream == nil {
		return nil, nil, nil, fmt.Errorf("active stream is required")
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.CheckpointConversation == nil {
		return nil, nil, nil, fmt.Errorf("checkpoint conversation is not initialized")
	}
	conversation := cloneConversationFile(stream.CheckpointConversation)
	pendingExecs := make([]runtimecore.PendingExec, 0, len(stream.PendingExecs))
	for _, pending := range stream.PendingExecs {
		pendingExecs = append(pendingExecs, pending)
	}
	pendingInteractions := make([]runtimecore.PendingInteraction, 0, len(stream.PendingInteractions))
	for _, pending := range stream.PendingInteractions {
		pendingInteractions = append(pendingInteractions, pending)
	}
	return conversation, pendingExecs, pendingInteractions, nil
}

func (service *Service) appendConversationEntries(stream *ActiveStream, conversationID string, entries []HistoryEntry) ([]HistoryEntry, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	if stream == nil {
		return nil, fmt.Errorf("active stream is required")
	}
	stream.mu.Lock()
	if stream.CheckpointConversation == nil {
		stream.mu.Unlock()
		return nil, fmt.Errorf("checkpoint conversation is not initialized")
	}
	working := cloneConversationFile(stream.CheckpointConversation)
	if service.store != nil {
		persisted, assigned, err := service.store.AppendEntries(conversationID, resetEntrySequences(entries))
		if err != nil {
			stream.mu.Unlock()
			return nil, err
		}
		if persisted != nil {
			working = persisted
		} else {
			appendEntriesInPlace(working, assigned)
		}
		stream.CheckpointConversation = working
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		return assigned, nil
	}
	assigned := appendEntriesInPlace(working, entries)
	stream.CheckpointConversation = working
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	return assigned, nil
}

func resetEntrySequences(entries []HistoryEntry) []HistoryEntry {
	if len(entries) == 0 {
		return nil
	}
	reset := make([]HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		next := entry
		next.Seq = 0
		reset = append(reset, next)
	}
	return reset
}

func firstWorkspacePath(paths []string) string {
	for _, path := range paths {
		if strings.TrimSpace(path) != "" {
			return strings.TrimSpace(path)
		}
	}
	return ""
}

func (service *Service) updateConversationMetaAndCheckpoint(stream *ActiveStream, conversationID string, update func(*ConversationFile) error) (*ConversationFile, error) {
	if stream == nil {
		return nil, fmt.Errorf("active stream is required")
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.CheckpointConversation == nil {
		return nil, fmt.Errorf("checkpoint conversation is not initialized")
	}
	conversation := cloneConversationFile(stream.CheckpointConversation)
	if err := update(conversation); err != nil {
		return nil, err
	}
	if err := service.syncConversationRecord(conversationID, conversation); err != nil {
		return nil, err
	}
	stream.CheckpointConversation = conversation
	stream.UpdatedAt = time.Now().UTC()
	return cloneConversationFile(conversation), nil
}
