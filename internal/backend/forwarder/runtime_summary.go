package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

func newRuntimeConversation(conversationID string, mode agentv1.AgentMode) (*ConversationFile, error) {
	now := time.Now().UTC()
	normalizedID := strings.TrimSpace(conversationID)
	alias, err := modeAlias(mode)
	if err != nil {
		return nil, err
	}
	return &ConversationFile{
		ConversationID:     normalizedID,
		RootConversationID: normalizedID,
		Mode:               alias,
		CreatedAt:          now,
		UpdatedAt:          now,
		NextTurnSeq:        1,
		NextEntrySeq:       1,
		Entries:            make([]HistoryEntry, 0, 16),
	}, nil
}

func (service *Service) bootstrapRuntimeConversation(intent InboundIntent) (*ConversationFile, agentv1.AgentMode, int64, []HistoryEntry, error) {
	if service == nil {
		return nil, agentv1.AgentMode_AGENT_MODE_AGENT, 0, nil, fmt.Errorf("forwarder service is nil")
	}
	contextWindowTokens := service.resolveContextWindowTokens(intent.ModelID)
	var conversation *ConversationFile
	var err error
	if service.store != nil {
		conversation, err = service.store.LoadConversation(intent.ConversationID)
		if err != nil {
			return nil, agentv1.AgentMode_AGENT_MODE_AGENT, 0, nil, err
		}
	}
	if conversation == nil {
		conversation, err = newRuntimeConversation(intent.ConversationID, intent.Mode)
		if err != nil {
			return nil, agentv1.AgentMode_AGENT_MODE_AGENT, 0, nil, err
		}
	}
	importedEntries := []HistoryEntry(nil)
	if len(conversation.Entries) == 0 && intent.ConversationState != nil {
		importedEntries, err = service.importConversationState(conversation, intent.ConversationState)
		if err != nil {
			return nil, agentv1.AgentMode_AGENT_MODE_AGENT, 0, nil, err
		}
	}
	if strings.TrimSpace(conversation.ConversationID) == "" {
		conversation.ConversationID = strings.TrimSpace(intent.ConversationID)
	}
	if strings.TrimSpace(conversation.RootConversationID) == "" {
		conversation.RootConversationID = strings.TrimSpace(conversation.ConversationID)
	}
	if intent.HasExplicitMode {
		alias, err := modeAlias(intent.Mode)
		if err != nil {
			return nil, agentv1.AgentMode_AGENT_MODE_AGENT, 0, nil, err
		}
		conversation.Mode = alias
	} else if strings.TrimSpace(conversation.Mode) == "" {
		alias, err := modeAlias(intent.Mode)
		if err != nil {
			return nil, agentv1.AgentMode_AGENT_MODE_AGENT, 0, nil, err
		}
		conversation.Mode = alias
	}
	if strings.TrimSpace(intent.SubagentTypeName) != "" {
		conversation.SubagentTypeName = strings.TrimSpace(intent.SubagentTypeName)
	}
	if contextWindowTokens > 0 {
		conversation.TokenDetailsMaxTokens = contextWindowTokens
	} else if conversation.TokenDetailsMaxTokens == 0 {
		conversation.TokenDetailsMaxTokens = projectedConversationMaxTokens
	}
	turnSeq := conversation.NextTurnSeq
	if turnSeq <= 0 {
		turnSeq = 1
	}
	effectiveMode, err := parseModeAlias(conversation.Mode)
	if err != nil {
		return nil, agentv1.AgentMode_AGENT_MODE_AGENT, 0, nil, err
	}
	entries, err := buildRunEntries(intent, effectiveMode, turnSeq)
	if err != nil {
		return nil, agentv1.AgentMode_AGENT_MODE_AGENT, 0, nil, err
	}
	conversation.CurrentRequestID = strings.TrimSpace(intent.RequestID)
	conversation.CurrentTurnSeq = turnSeq
	conversation.CurrentLoopID = fmt.Sprintf("%d:%s", turnSeq, strings.TrimSpace(intent.RequestID))
	conversation.CurrentLoopStatus = "running"
	if conversation.NextTurnSeq <= turnSeq {
		conversation.NextTurnSeq = turnSeq + 1
	}
	return conversation, effectiveMode, turnSeq, append(importedEntries, entries...), nil
}

func (service *Service) syncConversationRecord(conversationID string, conversation *ConversationFile) error {
	if service == nil || service.store == nil || conversation == nil {
		return nil
	}
	mode, err := parseModeAlias(conversation.Mode)
	if err != nil {
		return err
	}
	if _, err := service.store.CreateConversation(
		conversationID,
		mode,
		conversation.ParentConversationID,
		conversation.ParentToolCallID,
		conversation.RootConversationID,
	); err != nil {
		return err
	}
	_, err = service.store.UpdateConversationMeta(conversationID, func(item *ConversationFile) error {
		if item == nil {
			return nil
		}
		item.ConversationID = conversation.ConversationID
		item.RootConversationID = conversation.RootConversationID
		item.ParentConversationID = conversation.ParentConversationID
		item.ParentToolCallID = conversation.ParentToolCallID
		item.SubagentTypeName = conversation.SubagentTypeName
		item.Mode = conversation.Mode
		item.TokenDetailsUsedTokens = conversation.TokenDetailsUsedTokens
		item.TokenDetailsMaxTokens = conversation.TokenDetailsMaxTokens
		item.AutoCompactionPending = conversation.AutoCompactionPending
		item.AutoCompactionPromptTokens = conversation.AutoCompactionPromptTokens
		item.AutoCompactionReserveTokens = conversation.AutoCompactionReserveTokens
		item.AutoCompactionTriggeredAt = conversation.AutoCompactionTriggeredAt
		item.AutoCompactionSourceModelCallID = conversation.AutoCompactionSourceModelCallID
		item.LatestRequestPrefix = cloneConversationRequestPrefix(conversation.LatestRequestPrefix)
		item.LastProviderCall = cloneConversationProviderCall(conversation.LastProviderCall)
		item.CreatedAt = conversation.CreatedAt
		item.UpdatedAt = conversation.UpdatedAt
		item.NextTurnSeq = conversation.NextTurnSeq
		item.NextEntrySeq = conversation.NextEntrySeq
		item.CurrentLoopID = conversation.CurrentLoopID
		item.CurrentLoopStatus = conversation.CurrentLoopStatus
		item.CurrentRequestID = conversation.CurrentRequestID
		item.CurrentTurnSeq = conversation.CurrentTurnSeq
		return nil
	})
	return err
}

func (service *Service) syncSummaryCarryForward(_ string, requestID string, modelCallID string) error {
	if service == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(modelCallID) == "" {
		return nil
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return nil
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return err
	}
	return service.syncSummarySnapshot(stream, conversation, requestID, modelCallID)
}

func (service *Service) syncSummarySnapshot(stream *ActiveStream, conversation *ConversationFile, requestID string, modelCallID string) error {
	if service == nil || conversation == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(modelCallID) == "" {
		return nil
	}
	return service.syncConversationRecord(conversation.ConversationID, conversation)
}

func (service *Service) projectSummaryInputHistoryMessages(conversation *ConversationFile) ([]modeladapter.Message, error) {
	if service == nil || service.projector == nil || conversation == nil {
		return nil, nil
	}
	inputConversation := cloneConversationFile(conversation)
	inputConversation.Entries = filterConversationEntriesByKind(conversation.Entries, "user_message", "request_context", "prompt_context")
	return service.projector.ProjectPromptReplay(inputConversation)
}

func (service *Service) projectSummaryOutputMessages(conversation *ConversationFile) ([]modeladapter.Message, error) {
	if service == nil || service.projector == nil || conversation == nil {
		return nil, nil
	}
	outputConversation := cloneConversationFile(conversation)
	outputConversation.Entries = filterConversationEntriesByKind(conversation.Entries, "assistant_text", "tool_call", "tool_result")
	return service.projector.ProjectPromptReplay(outputConversation)
}

func filterConversationEntriesByKind(entries []HistoryEntry, kinds ...string) []HistoryEntry {
	if len(entries) == 0 || len(kinds) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(kinds))
	for _, kind := range kinds {
		if trimmed := strings.TrimSpace(kind); trimmed != "" {
			allowed[trimmed] = struct{}{}
		}
	}
	filtered := make([]HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		if _, ok := allowed[strings.TrimSpace(entry.Kind)]; !ok {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func buildSummaryTurnOutcome(stream *ActiveStream, conversation *ConversationFile, requestID string, modelCallID string) map[string]any {
	outcome := map[string]any{
		"status":        "in_progress",
		"request_id":    strings.TrimSpace(requestID),
		"run_id":        strings.TrimSpace(requestID),
		"model_call_id": strings.TrimSpace(modelCallID),
	}
	if stream != nil {
		outcome["model_id"] = strings.TrimSpace(stream.ModelID)
		outcome["is_prewarm"] = false
		outcome["turn_seq"] = stream.TurnSeq
		if !stream.CreatedAt.IsZero() {
			outcome["started_at"] = stream.CreatedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	if conversation != nil && !conversation.UpdatedAt.IsZero() {
		outcome["last_event_at"] = conversation.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	for index := len(conversation.Entries) - 1; index >= 0; index-- {
		entry := conversation.Entries[index]
		if strings.TrimSpace(entry.RequestID) != strings.TrimSpace(requestID) || strings.TrimSpace(entry.Kind) != "metadata" {
			continue
		}
		var payload metadataPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		switch strings.TrimSpace(payload.Type) {
		case "turn_completed":
			outcome["status"] = "completed"
		case "provider_error":
			outcome["status"] = "provider_error"
			if errorText := strings.TrimSpace(readStringValue(payload.Value["error"])); errorText != "" {
				outcome["error"] = errorText
			}
		case "failed":
			outcome["status"] = "failed"
			if errorText := strings.TrimSpace(readStringValue(payload.Value["error"])); errorText != "" {
				outcome["error"] = errorText
			}
		default:
			continue
		}
		if modelCall := strings.TrimSpace(readStringValue(payload.Value["model_call_id"])); modelCall != "" {
			outcome["model_call_id"] = modelCall
		}
		if !entry.CreatedAt.IsZero() {
			outcome["last_event_at"] = entry.CreatedAt.UTC().Format(time.RFC3339Nano)
		}
		break
	}
	return outcome
}

func buildSummaryAnnotations(conversation *ConversationFile, requestID string) map[string]any {
	if conversation == nil {
		return nil
	}
	for index := len(conversation.Entries) - 1; index >= 0; index-- {
		entry := conversation.Entries[index]
		if strings.TrimSpace(entry.RequestID) != strings.TrimSpace(requestID) || strings.TrimSpace(entry.Kind) != "metadata" {
			continue
		}
		var payload metadataPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		if strings.TrimSpace(payload.Type) != "thought_annotation" {
			continue
		}
		if strings.TrimSpace(readStringValue(payload.Value["kind"])) != "summary_completed" {
			continue
		}
		thought := strings.TrimSpace(readStringValue(payload.Value["thought"]))
		if thought == "" {
			continue
		}
		return map[string]any{
			"summary_completed_thought": thought,
		}
	}
	return nil
}
