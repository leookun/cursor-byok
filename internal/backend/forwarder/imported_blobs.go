package forwarder

import (
	"crypto/sha256"
	"fmt"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	promptengine "cursor/internal/backend/agent/prompt"
)

type importedBlobStore map[string][]byte

func newImportedBlobStore(items []*agentv1.PreFetchedBlob) (importedBlobStore, error) {
	if len(items) == 0 {
		return nil, nil
	}
	store := make(importedBlobStore, len(items))
	for _, item := range items {
		if item == nil || len(item.GetId()) == 0 {
			continue
		}
		if len(item.GetId()) != sha256.Size {
			return nil, fmt.Errorf("prefetched blob id length %d, want %d", len(item.GetId()), sha256.Size)
		}
		digest := sha256.Sum256(item.GetValue())
		if string(digest[:]) != string(item.GetId()) {
			return nil, fmt.Errorf("prefetched blob %x failed SHA-256 validation", item.GetId())
		}
		store[string(item.GetId())] = append([]byte(nil), item.GetValue()...)
	}
	return store, nil
}

func (store importedBlobStore) resolve(id []byte) ([]byte, bool) {
	if len(id) == 0 || len(store) == 0 {
		return nil, false
	}
	value, ok := store[string(id)]
	return append([]byte(nil), value...), ok
}

func decodeImportedTurn(raw []byte, blobs importedBlobStore) (*agentv1.ConversationTurnStructure, []byte, error) {
	if data, ok := blobs.resolve(raw); ok {
		turn := &agentv1.ConversationTurnStructure{}
		if err := proto.Unmarshal(data, turn); err != nil || turn.GetTurn() == nil {
			return nil, nil, fmt.Errorf("decode imported turn blob %x: %w", raw, firstNonNilError(err, fmt.Errorf("turn payload is empty")))
		}
		return turn, append([]byte(nil), raw...), nil
	}
	turn := &agentv1.ConversationTurnStructure{}
	if err := proto.Unmarshal(raw, turn); err == nil && turn.GetTurn() != nil {
		return turn, nil, nil
	}
	if len(raw) == sha256.Size {
		return nil, append([]byte(nil), raw...), nil
	}
	return nil, nil, fmt.Errorf("decode imported inline turn")
}

func decodeImportedUserMessage(raw []byte, blobs importedBlobStore) (*agentv1.UserMessage, error) {
	data := raw
	if resolved, ok := blobs.resolve(raw); ok {
		data = resolved
	} else if len(raw) == sha256.Size {
		candidate := &agentv1.UserMessage{}
		if err := proto.Unmarshal(raw, candidate); err != nil || !hasKnownUserMessageContent(candidate) {
			return nil, fmt.Errorf("missing prefetched user message blob %x", raw)
		}
		return candidate, nil
	}
	message := &agentv1.UserMessage{}
	if err := proto.Unmarshal(data, message); err != nil {
		return nil, fmt.Errorf("decode imported turn user_message: %w", err)
	}
	return message, nil
}

func decodeImportedStep(raw []byte, blobs importedBlobStore) (*agentv1.ConversationStep, error) {
	data := raw
	if resolved, ok := blobs.resolve(raw); ok {
		data = resolved
	} else if len(raw) == sha256.Size {
		candidate := &agentv1.ConversationStep{}
		if err := proto.Unmarshal(raw, candidate); err != nil || candidate.GetMessage() == nil {
			return nil, fmt.Errorf("missing prefetched conversation step blob %x", raw)
		}
		return candidate, nil
	}
	step := &agentv1.ConversationStep{}
	if err := proto.Unmarshal(data, step); err != nil {
		return nil, fmt.Errorf("decode imported turn step: %w", err)
	}
	if step.GetMessage() == nil {
		return nil, fmt.Errorf("decode imported turn step: payload is empty")
	}
	return step, nil
}

func importedBlobTurnMessages(turn *agentv1.ConversationTurnStructure, blobs importedBlobStore) ([]modeladapter.Message, error) {
	if turn == nil || turn.GetAgentConversationTurn() == nil {
		return nil, nil
	}
	agentTurn := turn.GetAgentConversationTurn()
	messages := make([]modeladapter.Message, 0, 1+len(agentTurn.GetSteps()))
	if len(agentTurn.GetUserMessage()) > 0 {
		userMessage, err := decodeImportedUserMessage(agentTurn.GetUserMessage(), blobs)
		if err != nil {
			return nil, err
		}
		if replay, ok := promptengine.BuildUserMessageReplayMessage(userMessage); ok {
			messages = append(messages, toModelMessage(replay))
		}
	}
	for _, rawStep := range agentTurn.GetSteps() {
		if len(rawStep) == 0 {
			continue
		}
		step, err := decodeImportedStep(rawStep, blobs)
		if err != nil {
			return nil, err
		}
		for _, replay := range promptengine.BuildLegacyMessagesFromConversationStep(step) {
			messages = append(messages, toModelMessage(replay))
		}
	}
	return messages, nil
}

func importedTurnIDs(turns [][]byte, blobs importedBlobStore) ([][]byte, error) {
	ids := make([][]byte, 0, len(turns))
	for _, raw := range turns {
		if len(raw) == 0 {
			continue
		}
		_, id, err := decodeImportedTurn(raw, blobs)
		if err != nil {
			return nil, err
		}
		if len(id) > 0 {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func hasKnownUserMessageContent(message *agentv1.UserMessage) bool {
	if message == nil {
		return false
	}
	return message.GetText() != "" ||
		message.GetMessageId() != "" ||
		message.GetSelectedContext() != nil ||
		message.GetRichText() != "" ||
		len(message.GetConversationStateBlobId()) > 0 ||
		len(message.GetTextBlobId()) > 0 ||
		len(message.GetRichTextBlobId()) > 0
}

func firstNonNilError(err error, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
