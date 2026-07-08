package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	promptengine "cursor/internal/backend/agent/prompt"
	"google.golang.org/protobuf/proto"
)

type turnUsageSnapshot struct {
	Provider          string
	Model             string
	InputTokens       int64
	OutputTokens      int64
	CacheReadTokens   int64
	CacheWriteTokens  int64
	UsagePresent      bool
	CacheReadPresent  bool
	CacheWritePresent bool
}

func (snapshot turnUsageSnapshot) hasAny() bool {
	return snapshot.InputTokens > 0 ||
		snapshot.OutputTokens > 0 ||
		snapshot.CacheReadTokens > 0 ||
		snapshot.CacheWriteTokens > 0
}

func (snapshot turnUsageSnapshot) cacheUsageComplete() bool {
	return snapshot.CacheReadPresent && snapshot.CacheWritePresent
}

func (snapshot turnUsageSnapshot) promptTokensTotal() int64 {
	return nonNegativeInt64(snapshot.InputTokens) +
		nonNegativeInt64(snapshot.CacheReadTokens) +
		nonNegativeInt64(snapshot.CacheWriteTokens)
}

func (snapshot turnUsageSnapshot) requestTokensTotal() int64 {
	return snapshot.promptTokensTotal() + nonNegativeInt64(snapshot.OutputTokens)
}

func (service *Service) importConversationState(item *ConversationFile, state *agentv1.ConversationStateStructure) ([]HistoryEntry, error) {
	if item == nil || state == nil {
		return nil, nil
	}
	item.TokenDetailsUsedTokens = state.GetTokenDetails().GetUsedTokens()
	entries := make([]HistoryEntry, 0, 2)
	if messages, err := importedConversationStateModelMessages(state); err != nil {
		return nil, err
	} else {
		for _, message := range messages {
			entry, ok, err := newModelMessageEntry(0, "", message)
			if err != nil {
				return nil, err
			}
			if ok {
				entries = append(entries, entry)
			}
		}
	}
	if len(entries) == 0 {
		summary, ok, err := importedConversationStateSummary(state)
		if err != nil {
			return nil, err
		}
		if ok {
			payload, err := json.Marshal(compactionSummaryEntryPayload{
				Summary: strings.TrimSpace(summary),
				Trigger: "imported_conversation_state",
			})
			if err != nil {
				return nil, fmt.Errorf("encode imported summary context: %w", err)
			}
			entries = append(entries, HistoryEntry{
				TurnSeq: 0,
				Role:    "system",
				Kind:    "compacted_summary",
				Payload: payload,
			})
		}
	}
	runtimeState, ok, err := runtimeStatePayloadFromConversationState(state)
	if err != nil {
		return nil, err
	}
	if ok {
		payload, err := json.Marshal(runtimeState)
		if err != nil {
			return nil, fmt.Errorf("encode imported runtime state context: %w", err)
		}
		entries = append(entries, HistoryEntry{
			TurnSeq: 0,
			Role:    "system",
			Kind:    "runtime_state",
			Payload: payload,
		})
	}
	return entries, nil
}

func importedConversationStateModelMessages(state *agentv1.ConversationStateStructure) ([]modeladapter.Message, error) {
	if state == nil {
		return nil, nil
	}
	if len(state.GetRootPromptMessagesJson()) > 0 {
		decoded, err := promptengine.DecodeReplayMessages(state.GetRootPromptMessagesJson())
		if err != nil {
			return nil, fmt.Errorf("decode imported replay messages: %w", err)
		}
		decoded = restoreImportedReplayUserMessages(decoded, state.GetTurns())
		decoded = filterLegacyPlainWriteReplay(decoded)
		decoded = filterInternalPromptContextReplay(decoded)
		messages := make([]modeladapter.Message, 0, len(decoded))
		for _, item := range decoded {
			messages = append(messages, toModelMessage(item))
		}
		return normalizeReplayMessageSequence(messages), nil
	}
	if len(state.GetSummary()) > 0 {
		return nil, nil
	}
	if len(state.GetTurns()) == 0 {
		return nil, nil
	}
	messages := make([]modeladapter.Message, 0, len(state.GetTurns())*2)
	for _, rawTurn := range state.GetTurns() {
		if len(rawTurn) == 0 {
			continue
		}
		turn := &agentv1.ConversationTurnStructure{}
		if err := proto.Unmarshal(rawTurn, turn); err != nil {
			return nil, fmt.Errorf("decode imported turn: %w", err)
		}
		agentTurn := turn.GetAgentConversationTurn()
		if agentTurn == nil {
			continue
		}
		if rawUser := agentTurn.GetUserMessage(); len(rawUser) > 0 {
			userMessage := &agentv1.UserMessage{}
			if err := proto.Unmarshal(rawUser, userMessage); err != nil {
				return nil, fmt.Errorf("decode imported turn user_message: %w", err)
			}
			if replay, ok := promptengine.BuildUserMessageReplayMessage(userMessage); ok {
				messages = append(messages, toModelMessage(replay))
			}
		}
		for _, rawStep := range agentTurn.GetSteps() {
			if len(rawStep) == 0 {
				continue
			}
			step := &agentv1.ConversationStep{}
			if err := proto.Unmarshal(rawStep, step); err != nil {
				return nil, fmt.Errorf("decode imported turn step: %w", err)
			}
			for _, replay := range promptengine.BuildLegacyMessagesFromConversationStep(step) {
				messages = append(messages, toModelMessage(replay))
			}
		}
	}
	return normalizeReplayMessageSequence(messages), nil
}

func importedConversationStateSummary(state *agentv1.ConversationStateStructure) (string, bool, error) {
	if state == nil || len(state.GetSummary()) == 0 {
		return "", false, nil
	}
	item := &agentv1.ConversationSummary{}
	if err := proto.Unmarshal(state.GetSummary(), item); err != nil {
		return "", false, fmt.Errorf("decode imported summary: %w", err)
	}
	text := strings.TrimSpace(item.GetSummary())
	return text, text != "", nil
}

func newModelMessageEntry(turnSeq int64, requestID string, message modeladapter.Message) (HistoryEntry, bool, error) {
	message.Role = strings.TrimSpace(message.Role)
	if message.Role == "" {
		return HistoryEntry{}, false, nil
	}
	if strings.TrimSpace(message.Content) == "" &&
		len(message.ContentParts) == 0 &&
		len(message.ToolCalls) == 0 &&
		strings.TrimSpace(message.ToolCallID) == "" &&
		!hasReplayableReasoningPayload(message.ReasoningContent, message.ReasoningSignature, message.ReasoningSignatureSource) &&
		len(message.OpenAIResponsesReasoningSummary) == 0 {
		return HistoryEntry{}, false, nil
	}
	payload, err := json.Marshal(modelMessageEntryPayload{Message: message})
	if err != nil {
		return HistoryEntry{}, false, fmt.Errorf("encode imported model message context: %w", err)
	}
	return HistoryEntry{
		TurnSeq:   turnSeq,
		RequestID: strings.TrimSpace(requestID),
		Role:      message.Role,
		Kind:      "model_message",
		Payload:   payload,
	}, true, nil
}

func runtimeStatePayloadFromConversationState(state *agentv1.ConversationStateStructure) (runtimeStateEntryPayload, bool, error) {
	if state == nil {
		return runtimeStateEntryPayload{}, false, nil
	}
	payload := runtimeStateEntryPayload{
		Plans: clonePlanRegistryEntries(state.GetPlans()),
	}
	if len(state.GetPlan()) > 0 {
		plan := &agentv1.ConversationPlan{}
		if err := proto.Unmarshal(state.GetPlan(), plan); err != nil {
			return runtimeStateEntryPayload{}, false, fmt.Errorf("decode imported plan: %w", err)
		}
		payload.PlanText = strings.TrimSpace(plan.GetPlan())
	}
	if len(state.GetTodos()) > 0 {
		todos := make([]*agentv1.TodoItem, 0, len(state.GetTodos()))
		for _, raw := range state.GetTodos() {
			if len(raw) == 0 {
				continue
			}
			item := &agentv1.TodoItem{}
			if err := proto.Unmarshal(raw, item); err != nil {
				return runtimeStateEntryPayload{}, false, fmt.Errorf("decode imported todo: %w", err)
			}
			todos = append(todos, cloneTodoItem(item))
		}
		payload.Todos = todos
	}
	ok := strings.TrimSpace(payload.PlanText) != "" || len(payload.Plans) > 0 || len(payload.Todos) > 0
	return payload, ok, nil
}

func (service *Service) updateConversationTokenState(stream *ActiveStream, conversationID string, usage turnUsageSnapshot, modelCallID string, finalizeAutoCompaction bool) error {
	if service == nil || !usage.hasAny() {
		return nil
	}
	now := time.Now().UTC()
	autoCompactionReserveTokens := int64(compactionAutoReserveTokens)
	if finalizeAutoCompaction {
		autoCompactionReserveTokens = service.resolveCompactionReserveTokens(activeStreamModelID(stream))
		if autoCompactionReserveTokens <= 0 {
			autoCompactionReserveTokens = compactionAutoReserveTokens
		}
	}
	_, err := service.updateConversationMetaAndCheckpoint(stream, conversationID, func(item *ConversationFile) error {
		if item == nil {
			return nil
		}
		promptTokensTotal := usage.promptTokensTotal()
		if promptTokensTotal > 0 {
			item.TokenDetailsUsedTokens = clampInt64ToUint32(promptTokensTotal)
		}
		if item.TokenDetailsMaxTokens == 0 {
			item.TokenDetailsMaxTokens = projectedConversationMaxTokens
		}
		if finalizeAutoCompaction {
			updateConversationAutoCompactionState(item, usage.requestTokensTotal(), autoCompactionReserveTokens, modelCallID, now)
		}
		return nil
	})
	return err
}

func activeStreamModelID(stream *ActiveStream) string {
	if stream == nil {
		return ""
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.ModelID
}

func updateConversationAutoCompactionState(conversation *ConversationFile, promptTokensTotal int64, reserveTokens int64, modelCallID string, triggeredAt time.Time) {
	if conversation == nil {
		return
	}
	if promptTokensTotal <= 0 {
		clearConversationAutoCompactionState(conversation)
		return
	}
	contextWindowTokens := int64(conversation.TokenDetailsMaxTokens)
	if contextWindowTokens <= 0 {
		contextWindowTokens = projectedConversationMaxTokens
	}
	if reserveTokens <= 0 {
		reserveTokens = compactionAutoReserveTokens
	}
	remainingTokens := contextWindowTokens - promptTokensTotal
	if remainingTokens > reserveTokens {
		clearConversationAutoCompactionState(conversation)
		return
	}
	conversation.AutoCompactionPending = true
	conversation.AutoCompactionPromptTokens = promptTokensTotal
	conversation.AutoCompactionReserveTokens = reserveTokens
	conversation.AutoCompactionTriggeredAt = triggeredAt.UTC().Format(time.RFC3339Nano)
	conversation.AutoCompactionSourceModelCallID = strings.TrimSpace(modelCallID)
}

func clearConversationAutoCompactionState(conversation *ConversationFile) {
	if conversation == nil {
		return
	}
	conversation.AutoCompactionPending = false
	conversation.AutoCompactionPromptTokens = 0
	conversation.AutoCompactionReserveTokens = 0
	conversation.AutoCompactionTriggeredAt = ""
	conversation.AutoCompactionSourceModelCallID = ""
}

func (service *Service) recordTurnUsageSnapshot(stream *ActiveStream, conversationID string, turnSeq int64, requestID string, modelCallID string, status string, usage turnUsageSnapshot, errorText string, turnFinalized bool) error {
	_ = turnFinalized
	if service == nil || strings.TrimSpace(requestID) == "" {
		return nil
	}
	modelID := ""
	modelName := ""
	startedAt := time.Time{}
	lastEventAt := time.Now().UTC()
	if stream != nil {
		stream.mu.Lock()
		modelID = strings.TrimSpace(stream.ModelID)
		modelName = strings.TrimSpace(stream.ModelName)
		startedAt = stream.CreatedAt
		if !stream.UpdatedAt.IsZero() {
			lastEventAt = stream.UpdatedAt
		}
		stream.mu.Unlock()
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = modelID
	}
	provider := strings.TrimSpace(usage.Provider)
	if strings.TrimSpace(usage.Model) != "" {
		modelName = strings.TrimSpace(usage.Model)
	}
	effectiveModelCallID := firstNonEmpty(strings.TrimSpace(modelCallID), strings.TrimSpace(requestID))
	if service.usageStore != nil {
		if err := service.usageStore.UpsertEvent(usageFileEvent{
			EventID:          usageEventID(requestID, effectiveModelCallID),
			Kind:             usageEventKindProvider,
			At:               lastEventAt,
			InputTokens:      usage.InputTokens,
			OutputTokens:     usage.OutputTokens,
			CacheReadTokens:  usage.CacheReadTokens,
			CacheWriteTokens: usage.CacheWriteTokens,
			UsagePresent:     usage.UsagePresent,
		}); err != nil {
			return err
		}
	}
	if strings.TrimSpace(conversationID) != "" {
		_, err := service.updateConversationMetaAndCheckpoint(stream, conversationID, func(item *ConversationFile) error {
			if item == nil {
				return nil
			}
			item.LastProviderCall = &ConversationProviderCall{
				RequestID:   strings.TrimSpace(requestID),
				ModelCallID: effectiveModelCallID,
				Provider:    provider,
				Model:       modelName,
				Status:      strings.TrimSpace(status),
				ErrorText:   strings.TrimSpace(errorText),
				UpdatedAt:   lastEventAt,
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	_ = turnSeq
	_ = startedAt
	return nil
}

func (service *Service) recordTurnFinalizedSnapshot(stream *ActiveStream, conversationID string, turnSeq int64, requestID string, status string, errorText string) error {
	_ = stream
	_ = errorText
	if service == nil || service.usageStore == nil || strings.TrimSpace(requestID) == "" {
		return nil
	}
	eventID := turnUsageEventID(conversationID, turnSeq, requestID)
	usage := usageFileEvent{}
	if aggregate, ok, err := service.usageStore.LookupEvent(strings.TrimSpace(requestID)); err != nil {
		return err
	} else if ok {
		usage = aggregate
	}
	return service.usageStore.UpsertEvent(usageFileEvent{
		EventID:          eventID,
		Kind:             usageEventKindTurn,
		Status:           normalizeUsageTurnStatus(status),
		At:               time.Now().UTC(),
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		UsagePresent:     usage.UsagePresent,
	})
}

func normalizeUsageTurnStatus(status string) string {
	if strings.TrimSpace(status) == usageTurnStatusDone {
		return usageTurnStatusDone
	}
	return strings.TrimSpace(status)
}

func turnUsageEventID(conversationID string, turnSeq int64, requestID string) string {
	conversationID = strings.TrimSpace(conversationID)
	requestID = strings.TrimSpace(requestID)
	if conversationID != "" && turnSeq > 0 {
		return fmt.Sprintf("turn::%s::%d", conversationID, turnSeq)
	}
	return "turn::" + requestID
}

func usageEventID(requestID string, modelCallID string) string {
	requestID = strings.TrimSpace(requestID)
	modelCallID = strings.TrimSpace(modelCallID)
	if modelCallID == "" || modelCallID == requestID {
		return requestID
	}
	return requestID + "::" + modelCallID
}

func clampInt64ToUint32(value int64) uint32 {
	if value <= 0 {
		return 0
	}
	if value > int64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(value)
}

func nonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func maxPositiveInt64(values ...int64) int64 {
	maxValue := int64(0)
	for _, value := range values {
		if value > maxValue {
			maxValue = value
		}
	}
	return maxValue
}
