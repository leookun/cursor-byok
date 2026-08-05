// projector.go 负责把 JSON history 投影成 prompt replay 和 legacy checkpoint 视图。
package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	promptengine "cursor/internal/backend/agent/prompt"
)

const projectedConversationMaxTokens = 130000

type HistoryProjector struct {
}

// NewHistoryProjector 创建 history 投影器。
func NewHistoryProjector() *HistoryProjector {
	return &HistoryProjector{}
}

// ProjectPromptReplay 把 conversation history 还原为 provider 可消费的消息列表。
func (projector *HistoryProjector) ProjectPromptReplay(conversation *ConversationFile) ([]modeladapter.Message, error) {
	if conversation == nil {
		return nil, nil
	}
	entries := replayablePromptProjectionEntries(conversation.Entries)
	messages := make([]modeladapter.Message, 0, len(entries)*2)
	seenToolCalls := make(map[string]struct{})
	openToolCalls := make(map[string]struct{})
	toolCallMessageIndexes := make(map[string]int)
	for _, entry := range entries {
		switch strings.TrimSpace(entry.Kind) {
		case "model_message":
			var payload modelMessageEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode model_message entry: %w", err)
			}
			message := cloneReplayModelMessage(payload.Message)
			if strings.TrimSpace(message.Role) != "" {
				messages = append(messages, message)
			}
		case "compaction_summary", "compacted_summary":
			summary, ok := decodeCompactionSummaryEntry(entry)
			if ok {
				messages = append(messages, modeladapter.Message{
					Role:    "user",
					Content: "<conversation_summary>\n" + summary + "\n</conversation_summary>",
				})
			}
		case "user_message":
			userMessage := &agentv1.UserMessage{}
			if err := protojson.Unmarshal(entry.Payload, userMessage); err != nil {
				return nil, fmt.Errorf("decode user_message entry: %w", err)
			}
			message, ok := promptengine.BuildUserMessageReplayMessage(userMessage)
			if ok {
				messages = append(messages, toModelMessage(message))
			}
		case "request_context":
			requestContext := &agentv1.RequestContext{}
			if err := protojson.Unmarshal(entry.Payload, requestContext); err != nil {
				return nil, fmt.Errorf("decode request_context entry: %w", err)
			}
			for _, replay := range promptengine.BuildRequestContextReplayMessages(requestContext) {
				messages = append(messages, toModelMessage(replay))
			}
		case "prompt_context":
			var payload promptContextEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode prompt_context entry: %w", err)
			}
			context := normalizePromptContextMessage(PromptContextMessage{
				Source:      payload.Source,
				ContentHash: payload.ContentHash,
				Message: modeladapter.Message{
					Role:    firstNonEmpty(strings.TrimSpace(payload.Role), "user"),
					Content: strings.TrimSpace(payload.Content),
				},
				Persist: true,
			})
			if isReplayablePromptContext(context) {
				messages = append(messages, context.Message)
			}
		case "assistant_text":
			var payload assistantTextPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode assistant_text entry: %w", err)
			}
			if strings.TrimSpace(payload.Text) == "" && strings.TrimSpace(payload.ReasoningContent) != "" && len(openToolCalls) > 0 {
				continue
			}
			if strings.TrimSpace(payload.Text) == "" && !hasReplayableReasoningPayload(payload.ReasoningContent, payload.ReasoningSignature, payload.ReasoningSignatureSource) {
				continue
			}
			messages = append(messages, modeladapter.Message{
				Role:                            "assistant",
				Content:                         strings.TrimSpace(payload.Text),
				ReasoningContent:                payload.ReasoningContent,
				ReasoningSignature:              payload.ReasoningSignature,
				ReasoningSignatureSource:        payload.ReasoningSignatureSource,
				OpenAIResponsesReasoningID:      payload.ReasoningItemID,
				OpenAIResponsesReasoningStatus:  payload.ReasoningStatus,
				OpenAIResponsesReasoningSummary: append(json.RawMessage(nil), payload.ReasoningSummary...),
			})
		case "tool_call":
			var payload toolCallEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode tool_call entry: %w", err)
			}
			toolCall := &agentv1.ToolCall{}
			if err := protojson.Unmarshal(payload.ToolCall, toolCall); err != nil {
				return nil, fmt.Errorf("decode tool_call payload: %w", err)
			}
			replayMessage, ok := promptengine.BuildAssistantToolCallReplayMessage(payload.ToolCallID, toolCall)
			if !ok {
				continue
			}
			replayMessage.ReasoningContent = payload.ReasoningContent
			replayMessage.ReasoningSignature = payload.ReasoningSignature
			replayMessage.ReasoningSignatureSource = payload.ReasoningSignatureSource
			replayMessage.OpenAIResponsesReasoningID = payload.ReasoningItemID
			replayMessage.OpenAIResponsesReasoningStatus = payload.ReasoningStatus
			replayMessage.OpenAIResponsesReasoningSummary = append(json.RawMessage(nil), payload.ReasoningSummary...)
			applyPromptProviderMetadataToFirstToolCall(&replayMessage, payload.ProviderItemID, payload.ProviderCallID, payload.ProviderStatus)
			messages = append(messages, toModelMessage(replayMessage))
			if toolCallID := strings.TrimSpace(payload.ToolCallID); toolCallID != "" {
				toolCallMessageIndexes[toolCallID] = len(messages) - 1
				seenToolCalls[toolCallID] = struct{}{}
				openToolCalls[toolCallID] = struct{}{}
			}
		case "tool_result":
			var payload toolResultEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode tool_result entry: %w", err)
			}
			historicalToolResult := isHistoricalReplayToolResult(conversation, entry)
			toolCallID := strings.TrimSpace(payload.ToolCallID)
			if _, ok := seenToolCalls[toolCallID]; ok {
				delete(openToolCalls, toolCallID)
				if index, found := toolCallMessageIndexes[toolCallID]; found && index >= 0 && index < len(messages) {
					overrideModelToolReplayFromEntry(&messages[index], payload.ToolName, payload.Arguments)
					delete(toolCallMessageIndexes, toolCallID)
				}
				var toolCall *agentv1.ToolCall
				if len(payload.ToolCall) > 0 {
					decoded := &agentv1.ToolCall{}
					if err := protojson.Unmarshal(payload.ToolCall, decoded); err == nil {
						toolCall = decoded
					}
				}
				toolName := strings.TrimSpace(payload.ToolName)
				if toolName == "" && toolCall != nil {
					toolName = inferToolName(toolCall)
				}
				if toolCallID == "" || toolName == "" {
					continue
				}
				if isLegacyPlainWriteReplay(toolName, len(payload.ToolCall) > 0) {
					continue
				}
				if toolCall != nil {
					replayMessage, ok := promptengine.BuildToolResultReplayMessage(toolCallID, toolCall)
					if ok {
						replayMessage.Name = toolName
						replayMessage.Content = limitProjectedToolResultReplay(toolName, replayMessage.Content, payload.ResultText, true, historicalToolResult)
						messages = append(messages, toModelMessage(replayMessage))
						continue
					}
				}
				messages = append(messages, modeladapter.Message{
					Role:       "tool",
					Name:       toolName,
					ToolCallID: toolCallID,
					Content:    limitProjectedToolResultReplay(toolName, payload.ResultText, "", false, historicalToolResult),
				})
				continue
			}
			if len(payload.ToolCall) > 0 {
				toolCall := &agentv1.ToolCall{}
				if err := protojson.Unmarshal(payload.ToolCall, toolCall); err != nil {
					return nil, fmt.Errorf("decode tool_result tool_call entry: %w", err)
				}
				replayMessages, ok := promptengine.BuildToolCallReplayMessages(payload.ToolCallID, toolCall)
				if !ok {
					continue
				}
				overrideToolReplayFromEntry(replayMessages, payload.ToolName, payload.Arguments)
				for index := range replayMessages {
					if strings.TrimSpace(replayMessages[index].Role) != "assistant" || len(replayMessages[index].ToolCalls) == 0 {
						continue
					}
					replayMessages[index].ReasoningContent = payload.ReasoningContent
					replayMessages[index].ReasoningSignature = payload.ReasoningSignature
					replayMessages[index].ReasoningSignatureSource = payload.ReasoningSignatureSource
					replayMessages[index].OpenAIResponsesReasoningID = payload.ReasoningItemID
					replayMessages[index].OpenAIResponsesReasoningStatus = payload.ReasoningStatus
					replayMessages[index].OpenAIResponsesReasoningSummary = append(json.RawMessage(nil), payload.ReasoningSummary...)
					applyPromptProviderMetadataToFirstToolCall(&replayMessages[index], payload.ProviderItemID, payload.ProviderCallID, payload.ProviderStatus)
				}
				for _, replay := range replayMessages {
					if strings.TrimSpace(replay.Role) == "tool" {
						toolName := firstNonEmpty(strings.TrimSpace(replay.Name), strings.TrimSpace(payload.ToolName))
						replay.Content = limitProjectedToolResultReplay(toolName, replay.Content, payload.ResultText, true, historicalToolResult)
					}
					messages = append(messages, toModelMessage(replay))
				}
				continue
			}
			if strings.TrimSpace(payload.ToolCallID) == "" || strings.TrimSpace(payload.ToolName) == "" {
				continue
			}
			if isLegacyPlainWriteReplay(strings.TrimSpace(payload.ToolName), len(payload.ToolCall) > 0) {
				continue
			}
			if !hasReplayableReasoningPayload(payload.ReasoningContent, payload.ReasoningSignature, payload.ReasoningSignatureSource) {
				continue
			}
			effectiveToolName := effectiveReplayToolName(strings.TrimSpace(payload.ToolName), strings.TrimSpace(payload.ToolName))
			effectiveArguments := firstNonEmpty(strings.TrimSpace(payload.Arguments), "{}")
			if isLegacyPatchEditToolName(payload.ToolName) {
				effectiveArguments = "{}"
			}
			messages = append(messages,
				modeladapter.Message{
					Role:                            "assistant",
					ReasoningContent:                payload.ReasoningContent,
					ReasoningSignature:              payload.ReasoningSignature,
					ReasoningSignatureSource:        payload.ReasoningSignatureSource,
					OpenAIResponsesReasoningID:      payload.ReasoningItemID,
					OpenAIResponsesReasoningStatus:  payload.ReasoningStatus,
					OpenAIResponsesReasoningSummary: append(json.RawMessage(nil), payload.ReasoningSummary...),
					ToolCalls: []modeladapter.ToolCallDescriptor{{
						ID:                    strings.TrimSpace(payload.ToolCallID),
						Type:                  "function",
						OpenAIResponsesID:     strings.TrimSpace(payload.ProviderItemID),
						OpenAIResponsesCallID: strings.TrimSpace(payload.ProviderCallID),
						OpenAIResponsesStatus: strings.TrimSpace(payload.ProviderStatus),
						Function: modeladapter.ToolCallFunctionShape{
							Name:      effectiveToolName,
							Arguments: effectiveArguments,
						},
					}},
				},
				modeladapter.Message{
					Role:       "tool",
					Name:       effectiveToolName,
					ToolCallID: strings.TrimSpace(payload.ToolCallID),
					Content:    limitProjectedToolResultReplay(payload.ToolName, payload.ResultText, "", false, historicalToolResult),
				},
			)
		}
	}
	return normalizeReplayMessageSequence(messages), nil
}

func compactedPromptProjectionEntries(entries []HistoryEntry) []HistoryEntry {
	if len(entries) == 0 {
		return nil
	}
	compactionIndex := -1
	for index := len(entries) - 1; index >= 0; index-- {
		if isCompactionSummaryKind(entries[index].Kind) {
			compactionIndex = index
			break
		}
	}
	if compactionIndex < 0 {
		return entries
	}
	var compactionPayload compactionSummaryEntryPayload
	_ = json.Unmarshal(entries[compactionIndex].Payload, &compactionPayload)
	preservedIndexes := map[int]struct{}{}
	if compactionPayload.PreserveCurrentTurnInputs {
		latestToolCallID := latestCompletedToolCallIDForTurn(entries, compactionPayload.CurrentTurnSeq, compactionPayload.CurrentRequestID)
		preservedIndexes = autoCompactionPreservedEntryIndexes(entries, compactionPayload.CurrentTurnSeq, compactionPayload.CurrentRequestID, latestToolCallID)
	}
	filtered := make([]HistoryEntry, 0, len(entries)-compactionIndex)
	for index, entry := range entries {
		if index < compactionIndex && isPromptReplayEntryKind(entry.Kind) {
			if _, ok := preservedIndexes[index]; !ok {
				continue
			}
		}
		if index < compactionIndex {
			if rewritten, ok := compactedProjectionPreservedEntry(entry); ok {
				entry = rewritten
			}
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func replayablePromptProjectionEntries(entries []HistoryEntry) []HistoryEntry {
	return sanitizeCanceledReplayEntries(compactedPromptProjectionEntries(entries))
}

func checkpointProjectionEntries(entries []HistoryEntry) []HistoryEntry {
	return sanitizeCanceledReplayEntries(entries)
}

const (
	cancelReplayPolicyDropTurn        = "drop_turn"
	cancelReplayPolicyDropUnstarted   = "drop_unstarted_turn"
	cancelReplayPolicyKeepStableInput = "keep_stable_input"
)

func sanitizeCanceledReplayEntries(entries []HistoryEntry) []HistoryEntry {
	if len(entries) == 0 {
		return nil
	}
	canceledTurns := canceledReplayPolicies(entries)
	if len(canceledTurns) == 0 {
		return entries
	}
	activeCanceledTurns := canceledReplayActivityTurns(entries)
	filtered := make([]HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.TurnSeq > 0 {
			if policy, canceled := canceledTurns[entry.TurnSeq]; canceled {
				if policy == cancelReplayPolicyDropUnstarted {
					if _, active := activeCanceledTurns[entry.TurnSeq]; active {
						policy = cancelReplayPolicyKeepStableInput
					} else {
						policy = cancelReplayPolicyDropTurn
					}
				}
				if policy == cancelReplayPolicyDropTurn || !isStableCanceledTurnInputEntry(entry) {
					continue
				}
			}
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func canceledReplayPolicies(entries []HistoryEntry) map[int64]string {
	canceledTurns := make(map[int64]string)
	for _, entry := range entries {
		if entry.TurnSeq <= 0 {
			continue
		}
		policy, ok := canceledReplayPolicyForEntry(entry)
		if !ok {
			continue
		}
		if policy == cancelReplayPolicyDropTurn {
			canceledTurns[entry.TurnSeq] = policy
			continue
		}
		if _, exists := canceledTurns[entry.TurnSeq]; !exists {
			canceledTurns[entry.TurnSeq] = policy
		}
	}
	return canceledTurns
}

func canceledReplayActivityTurns(entries []HistoryEntry) map[int64]struct{} {
	activeTurns := make(map[int64]struct{})
	for _, entry := range entries {
		if entry.TurnSeq <= 0 || !isCanceledTurnActivityEntry(entry) {
			continue
		}
		activeTurns[entry.TurnSeq] = struct{}{}
	}
	return activeTurns
}

func canceledReplayPolicyForEntry(entry HistoryEntry) (string, bool) {
	if strings.TrimSpace(entry.Kind) != "metadata" {
		return "", false
	}
	var payload metadataPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return "", false
	}
	if strings.TrimSpace(payload.Type) != "control" {
		return "", false
	}
	if strings.TrimSpace(readStringValue(payload.Value["status"])) != "canceled" {
		return "", false
	}
	return normalizeCancelReplayPolicy(
		readStringValue(payload.Value["replay_policy"]),
		readStringValue(payload.Value["reason"]),
	), true
}

func normalizeCancelReplayPolicy(policy string, reason string) string {
	switch strings.TrimSpace(policy) {
	case cancelReplayPolicyDropTurn:
		if strings.Contains(strings.ToLower(strings.TrimSpace(reason)), "superseded by newer request") {
			return cancelReplayPolicyDropUnstarted
		}
		return cancelReplayPolicyDropTurn
	case cancelReplayPolicyDropUnstarted:
		return cancelReplayPolicyDropUnstarted
	case cancelReplayPolicyKeepStableInput:
		return cancelReplayPolicyKeepStableInput
	default:
		return cancelReplayPolicyForReason(reason)
	}
}

func cancelReplayPolicyForReason(reason string) string {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case strings.Contains(normalized, "superseded by newer request"):
		return cancelReplayPolicyDropUnstarted
	default:
		return cancelReplayPolicyKeepStableInput
	}
}

func isStableCanceledTurnInputEntry(entry HistoryEntry) bool {
	switch strings.TrimSpace(entry.Kind) {
	case "request_context", "user_message", "prompt_context":
		return true
	default:
		return false
	}
}

func isCanceledTurnActivityEntry(entry HistoryEntry) bool {
	switch strings.TrimSpace(entry.Kind) {
	case "model_message", "assistant_text", "tool_call", "tool_result":
		return true
	default:
		return false
	}
}

func compactedProjectionPreservedEntry(entry HistoryEntry) (HistoryEntry, bool) {
	if strings.TrimSpace(entry.Kind) != "tool_result" {
		return entry, false
	}
	if rewritten, ok := rewriteAutoCompactionToolResultEntry(entry, autoCompactionPreservedToolResultLimitBytes, false); ok {
		return rewritten, true
	}
	return entry, true
}

func isPromptReplayEntryKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "model_message", "compaction_summary", "compacted_summary", "user_message", "request_context", "prompt_context", "assistant_text", "tool_call", "tool_result":
		return true
	default:
		return false
	}
}

func decodeCompactionSummaryEntry(entry HistoryEntry) (string, bool) {
	var payload compactionSummaryEntryPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return "", false
	}
	text := strings.TrimSpace(payload.Summary)
	return text, text != ""
}

func isHistoricalReplayToolResult(conversation *ConversationFile, entry HistoryEntry) bool {
	if conversation == nil || entry.TurnSeq <= 0 {
		return false
	}
	currentTurnSeq := conversation.NextTurnSeq - 1
	return currentTurnSeq > 0 && entry.TurnSeq < currentTurnSeq
}

// ProjectLegacyCheckpoint 按需从 JSON history 投影出兼容旧客户端的 checkpoint 结构。
func (projector *HistoryProjector) ProjectLegacyCheckpoint(conversation *ConversationFile) (*agentv1.ConversationStateStructure, error) {
	state := &agentv1.ConversationStateStructure{
		TokenDetails: &agentv1.ConversationTokenDetails{
			UsedTokens: conversationTokenDetailsUsedTokens(conversation),
			MaxTokens:  conversationTokenDetailsMaxTokens(conversation),
		},
		Summary:          latestCompactionSummaryBytes(conversation),
		SummaryArchive:   previousCompactionSummaryBytes(conversation),
		SummaryArchives:  compactionSummaryArchives(conversation),
		SelfSummaryCount: uint32(len(compactionSummaryTexts(conversation))),
	}
	if conversation == nil {
		mode := agentv1.AgentMode_AGENT_MODE_AGENT
		state.Mode = &mode
		return state, nil
	}
	mode, err := parseModeAlias(conversation.Mode)
	if err != nil {
		return nil, err
	}
	state.Mode = &mode
	structuredState, err := projectConversationStructuredState(conversation)
	if err != nil {
		return nil, err
	}
	if structuredState.HasPlan {
		state.Plan = encodeConversationPlanBytes(structuredState.PlanText)
	}
	state.Plans = clonePlanRegistryEntries(structuredState.Plans)
	if structuredState.HasTodos {
		state.Todos = encodeConversationTodoBytes(structuredState.Todos)
	}
	grouped := make(map[int64][]HistoryEntry)
	order := make([]int64, 0, conversation.NextTurnSeq)
	for _, entry := range checkpointProjectionEntries(conversation.Entries) {
		if entry.TurnSeq <= 0 {
			continue
		}
		if _, ok := grouped[entry.TurnSeq]; !ok {
			order = append(order, entry.TurnSeq)
		}
		grouped[entry.TurnSeq] = append(grouped[entry.TurnSeq], entry)
	}

	for _, turnSeq := range order {
		entries := grouped[turnSeq]
		var rawUserMessage []byte
		var turnRequestID string
		steps := make([][]byte, 0, len(entries))
		seenToolCalls := make(map[string]struct{})
		openToolCalls := make(map[string]struct{})
		for _, entry := range entries {
			if turnRequestID == "" {
				turnRequestID = strings.TrimSpace(entry.RequestID)
			}
			switch strings.TrimSpace(entry.Kind) {
			case "user_message":
				userMessage := &agentv1.UserMessage{}
				if err := protojson.Unmarshal(entry.Payload, userMessage); err != nil {
					return nil, fmt.Errorf("decode checkpoint user_message: %w", err)
				}
				payload, err := proto.Marshal(userMessage)
				if err != nil {
					return nil, err
				}
				rawUserMessage = payload
			case "assistant_text":
				var payload assistantTextPayload
				if err := json.Unmarshal(entry.Payload, &payload); err != nil {
					return nil, err
				}
				if strings.TrimSpace(payload.Text) == "" && strings.TrimSpace(payload.ReasoningContent) != "" && len(openToolCalls) > 0 {
					continue
				}
				if strings.TrimSpace(payload.ReasoningContent) != "" {
					stepPayload, err := marshalThinkingStep(payload.ReasoningContent)
					if err != nil {
						return nil, err
					}
					steps = append(steps, stepPayload)
				}
				if strings.TrimSpace(payload.Text) == "" {
					continue
				}
				stepPayload, err := proto.Marshal(&agentv1.ConversationStep{
					Message: &agentv1.ConversationStep_AssistantMessage{
						AssistantMessage: &agentv1.AssistantMessage{Text: strings.TrimSpace(payload.Text)},
					},
				})
				if err != nil {
					return nil, err
				}
				steps = append(steps, stepPayload)
			case "tool_call":
				var payload toolCallEntryPayload
				if err := json.Unmarshal(entry.Payload, &payload); err != nil {
					return nil, err
				}
				if strings.TrimSpace(payload.ReasoningContent) != "" {
					stepPayload, err := marshalThinkingStep(payload.ReasoningContent)
					if err != nil {
						return nil, err
					}
					steps = append(steps, stepPayload)
				}
				toolCall := &agentv1.ToolCall{}
				if err := protojson.Unmarshal(payload.ToolCall, toolCall); err != nil {
					return nil, err
				}
				if !shouldPersistToolResultName(firstNonEmpty(strings.TrimSpace(payload.ToolName), inferToolName(toolCall))) {
					continue
				}
				stepPayload, err := proto.Marshal(&agentv1.ConversationStep{
					Message: &agentv1.ConversationStep_ToolCall{
						ToolCall: toolCall,
					},
				})
				if err != nil {
					return nil, err
				}
				steps = append(steps, stepPayload)
				if toolCallID := strings.TrimSpace(payload.ToolCallID); toolCallID != "" {
					seenToolCalls[toolCallID] = struct{}{}
					openToolCalls[toolCallID] = struct{}{}
				}
			case "tool_result":
				var payload toolResultEntryPayload
				if err := json.Unmarshal(entry.Payload, &payload); err != nil {
					return nil, err
				}
				if toolCallID := strings.TrimSpace(payload.ToolCallID); toolCallID != "" {
					if _, ok := seenToolCalls[toolCallID]; ok {
						delete(openToolCalls, toolCallID)
						continue
					}
				}
				if strings.TrimSpace(payload.ReasoningContent) != "" {
					stepPayload, err := marshalThinkingStep(payload.ReasoningContent)
					if err != nil {
						return nil, err
					}
					steps = append(steps, stepPayload)
				}
				if len(payload.ToolCall) == 0 {
					continue
				}
				toolCall := &agentv1.ToolCall{}
				if err := protojson.Unmarshal(payload.ToolCall, toolCall); err != nil {
					return nil, err
				}
				if !shouldPersistToolResultName(firstNonEmpty(strings.TrimSpace(payload.ToolName), inferToolName(toolCall))) {
					continue
				}
				stepPayload, err := proto.Marshal(&agentv1.ConversationStep{
					Message: &agentv1.ConversationStep_ToolCall{
						ToolCall: toolCall,
					},
				})
				if err != nil {
					return nil, err
				}
				steps = append(steps, stepPayload)
			}
		}
		if len(rawUserMessage) == 0 && len(steps) == 0 {
			continue
		}
		agentTurn := &agentv1.AgentConversationTurnStructure{
			UserMessage: rawUserMessage,
			Steps:       steps,
		}
		if turnRequestID != "" {
			agentTurn.RequestId = &turnRequestID
		}
		turnPayload, err := proto.Marshal(&agentv1.ConversationTurnStructure{
			Turn: &agentv1.ConversationTurnStructure_AgentConversationTurn{
				AgentConversationTurn: agentTurn,
			},
		})
		if err != nil {
			return nil, err
		}
		state.Turns = append(state.Turns, turnPayload)
	}
	replayMessages, err := projector.ProjectPromptReplay(conversation)
	if err != nil {
		return nil, err
	}
	promptReplay := make([]promptengine.Message, 0, len(replayMessages))
	for _, message := range replayMessages {
		promptReplay = append(promptReplay, promptengine.Message{
			Role:                            message.Role,
			Content:                         message.Content,
			ContentParts:                    toPromptContentParts(message.ContentParts),
			ReasoningContent:                message.ReasoningContent,
			ReasoningSignature:              message.ReasoningSignature,
			ReasoningSignatureSource:        message.ReasoningSignatureSource,
			OpenAIResponsesReasoningID:      message.OpenAIResponsesReasoningID,
			OpenAIResponsesReasoningStatus:  message.OpenAIResponsesReasoningStatus,
			OpenAIResponsesReasoningSummary: append(json.RawMessage(nil), message.OpenAIResponsesReasoningSummary...),
			ToolCalls:                       toPromptToolCalls(message.ToolCalls),
			ToolCallID:                      message.ToolCallID,
			Name:                            message.Name,
		})
	}
	promptReplay = filterCheckpointPersistentToolReplay(promptReplay)
	rootPromptMessages, err := promptengine.EncodeReplayMessages(promptReplay)
	if err != nil {
		return nil, err
	}
	state.RootPromptMessagesJson = rootPromptMessages
	return state, nil
}

func marshalThinkingStep(text string) ([]byte, error) {
	return proto.Marshal(&agentv1.ConversationStep{
		Message: &agentv1.ConversationStep_ThinkingMessage{
			ThinkingMessage: &agentv1.ThinkingMessage{Text: text},
		},
	})
}

func conversationTokenDetailsUsedTokens(conversation *ConversationFile) uint32 {
	if conversation == nil {
		return 0
	}
	return conversation.TokenDetailsUsedTokens
}

func conversationTokenDetailsMaxTokens(conversation *ConversationFile) uint32 {
	if conversation == nil || conversation.TokenDetailsMaxTokens == 0 {
		return projectedConversationMaxTokens
	}
	return conversation.TokenDetailsMaxTokens
}

func latestCompactionSummaryBytes(conversation *ConversationFile) []byte {
	texts := compactionSummaryTexts(conversation)
	if len(texts) == 0 {
		return nil
	}
	return encodeConversationSummaryBytes(texts[len(texts)-1])
}

func previousCompactionSummaryBytes(conversation *ConversationFile) []byte {
	texts := compactionSummaryTexts(conversation)
	if len(texts) < 2 {
		return nil
	}
	return encodeConversationSummaryBytes(texts[len(texts)-2])
}

func compactionSummaryArchives(conversation *ConversationFile) [][]byte {
	texts := compactionSummaryTexts(conversation)
	if len(texts) == 0 {
		return nil
	}
	archives := make([][]byte, 0, len(texts))
	for _, text := range texts {
		if encoded := encodeConversationSummaryBytes(text); len(encoded) > 0 {
			archives = append(archives, encoded)
		}
	}
	return archives
}

func compactionSummaryTexts(conversation *ConversationFile) []string {
	if conversation == nil || len(conversation.Entries) == 0 {
		return nil
	}
	texts := make([]string, 0)
	for _, entry := range conversation.Entries {
		if !isCompactionSummaryKind(entry.Kind) {
			continue
		}
		if text, ok := decodeCompactionSummaryEntry(entry); ok {
			texts = append(texts, text)
		}
	}
	return texts
}

func isCompactionSummaryKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "compaction_summary", "compacted_summary":
		return true
	default:
		return false
	}
}

func applyPromptProviderMetadataToFirstToolCall(message *promptengine.Message, providerItemID string, providerCallID string, providerStatus string) {
	if message == nil || len(message.ToolCalls) == 0 {
		return
	}
	message.ToolCalls[0].OpenAIResponsesID = strings.TrimSpace(providerItemID)
	message.ToolCalls[0].OpenAIResponsesCallID = strings.TrimSpace(providerCallID)
	message.ToolCalls[0].OpenAIResponsesStatus = strings.TrimSpace(providerStatus)
}

func normalizeReplayMessageSequence(messages []modeladapter.Message) []modeladapter.Message {
	if len(messages) == 0 {
		return nil
	}
	normalized := make([]modeladapter.Message, 0, len(messages))
	for _, item := range messages {
		message := cloneReplayModelMessage(item)
		if mergeReplayAssistantToolCalls(&normalized, message) {
			continue
		}
		normalized = append(normalized, message)
	}
	normalized = filterProviderSuppressedToolReplayMessages(normalized)
	normalized = coalesceInterleavedReplayToolBatches(normalized)
	return trimReplayDanglingAssistantToolCalls(normalized)
}

func filterProviderSuppressedToolReplayMessages(messages []modeladapter.Message) []modeladapter.Message {
	if len(messages) == 0 {
		return nil
	}
	filtered := make([]modeladapter.Message, 0, len(messages))
	skippedToolCallIDs := make(map[string]struct{})
	for _, item := range messages {
		message := cloneReplayModelMessage(item)
		if strings.TrimSpace(message.Role) == "assistant" && len(message.ToolCalls) > 0 {
			nextToolCalls := make([]modeladapter.ToolCallDescriptor, 0, len(message.ToolCalls))
			for _, toolCall := range message.ToolCalls {
				if isProviderPromptReplaySuppressedToolName(toolCall.Function.Name) {
					if toolCallID := strings.TrimSpace(toolCall.ID); toolCallID != "" {
						skippedToolCallIDs[toolCallID] = struct{}{}
					}
					continue
				}
				toolCall.Index = len(nextToolCalls)
				nextToolCalls = append(nextToolCalls, toolCall)
			}
			if len(nextToolCalls) == 0 && strings.TrimSpace(message.Content) == "" && len(message.ContentParts) == 0 && !hasReplayableReasoningPayload(message.ReasoningContent, message.ReasoningSignature, message.ReasoningSignatureSource) {
				continue
			}
			message.ToolCalls = nextToolCalls
			filtered = append(filtered, message)
			continue
		}
		if strings.TrimSpace(message.Role) == "tool" {
			if _, ok := skippedToolCallIDs[strings.TrimSpace(message.ToolCallID)]; ok {
				continue
			}
			if isProviderPromptReplaySuppressedToolName(message.Name) {
				continue
			}
		}
		filtered = append(filtered, message)
	}
	return filtered
}

func isProviderPromptReplaySuppressedToolName(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "GenerateImage":
		return true
	default:
		return false
	}
}

func cloneReplayModelMessage(message modeladapter.Message) modeladapter.Message {
	cloned := message
	if len(message.ContentParts) > 0 {
		cloned.ContentParts = append([]modeladapter.ContentPart(nil), message.ContentParts...)
	}
	if len(message.ToolCalls) > 0 {
		cloned.ToolCalls = append([]modeladapter.ToolCallDescriptor(nil), message.ToolCalls...)
	}
	if len(message.OpenAIResponsesReasoningSummary) > 0 {
		cloned.OpenAIResponsesReasoningSummary = append(json.RawMessage(nil), message.OpenAIResponsesReasoningSummary...)
	}
	return cloned
}

func mergeReplayAssistantToolCalls(messages *[]modeladapter.Message, message modeladapter.Message) bool {
	if len(*messages) == 0 {
		return false
	}
	last := &(*messages)[len(*messages)-1]
	if !canMergeReplayAssistantToolCalls(*last, message) {
		return false
	}
	startIndex := len(last.ToolCalls)
	for index, toolCall := range message.ToolCalls {
		item := toolCall
		item.Index = startIndex + index
		last.ToolCalls = append(last.ToolCalls, item)
	}
	last.ReasoningContent = mergeReplayReasoning(last.ReasoningContent, message.ReasoningContent)
	mergeReplayReasoningMetadata(last, message)
	return true
}

func canMergeReplayAssistantToolCalls(last modeladapter.Message, current modeladapter.Message) bool {
	if strings.TrimSpace(last.Role) != "assistant" || strings.TrimSpace(current.Role) != "assistant" {
		return false
	}
	if len(last.ToolCalls) == 0 || len(current.ToolCalls) == 0 {
		return false
	}
	if strings.TrimSpace(last.ToolCallID) != "" || strings.TrimSpace(last.Name) != "" {
		return false
	}
	if strings.TrimSpace(current.ToolCallID) != "" || strings.TrimSpace(current.Name) != "" {
		return false
	}
	if strings.TrimSpace(current.Content) != "" || len(current.ContentParts) > 0 {
		return false
	}
	return true
}

func coalesceInterleavedReplayToolBatches(messages []modeladapter.Message) []modeladapter.Message {
	if len(messages) == 0 {
		return nil
	}
	normalized := make([]modeladapter.Message, 0, len(messages))
	for index := 0; index < len(messages); index++ {
		message := cloneReplayModelMessage(messages[index])
		groupID := replayAssistantToolGroupID(message)
		if groupID == "" {
			normalized = append(normalized, message)
			continue
		}

		batch := message
		toolResults := make(map[string]modeladapter.Message)
		toolResultOrder := make([]string, 0)
		changed := false
		nextIndex := index + 1
		for nextIndex < len(messages) {
			next := cloneReplayModelMessage(messages[nextIndex])
			if strings.TrimSpace(next.Role) == "tool" && replayToolCallGroupID(next.ToolCallID) == groupID {
				toolCallID := strings.TrimSpace(next.ToolCallID)
				if _, ok := toolResults[toolCallID]; !ok {
					toolResultOrder = append(toolResultOrder, toolCallID)
				}
				toolResults[toolCallID] = next
				changed = true
				nextIndex++
				continue
			}
			if replayAssistantToolGroupID(next) == groupID && canMergeReplayAssistantToolCalls(batch, next) {
				startIndex := len(batch.ToolCalls)
				for toolIndex, toolCall := range next.ToolCalls {
					item := toolCall
					item.Index = startIndex + toolIndex
					batch.ToolCalls = append(batch.ToolCalls, item)
				}
				batch.ReasoningContent = mergeReplayReasoning(batch.ReasoningContent, next.ReasoningContent)
				mergeReplayReasoningMetadata(&batch, next)
				changed = true
				nextIndex++
				continue
			}
			break
		}

		if !changed {
			normalized = append(normalized, message)
			continue
		}
		normalized = append(normalized, batch)
		emittedResults := make(map[string]struct{}, len(toolResults))
		for _, toolCall := range batch.ToolCalls {
			toolCallID := strings.TrimSpace(toolCall.ID)
			result, ok := toolResults[toolCallID]
			if !ok {
				continue
			}
			normalized = append(normalized, result)
			emittedResults[toolCallID] = struct{}{}
		}
		for _, toolCallID := range toolResultOrder {
			if _, ok := emittedResults[toolCallID]; ok {
				continue
			}
			normalized = append(normalized, toolResults[toolCallID])
		}
		index = nextIndex - 1
	}
	return normalized
}

func replayAssistantToolGroupID(message modeladapter.Message) string {
	if strings.TrimSpace(message.Role) != "assistant" || len(message.ToolCalls) == 0 {
		return ""
	}
	groupID := ""
	for _, toolCall := range message.ToolCalls {
		nextGroupID := replayToolCallGroupID(toolCall.ID)
		if nextGroupID == "" {
			return ""
		}
		if groupID == "" {
			groupID = nextGroupID
			continue
		}
		if groupID != nextGroupID {
			return ""
		}
	}
	return groupID
}

func replayToolCallGroupID(toolCallID string) string {
	trimmed := strings.TrimSpace(toolCallID)
	if trimmed == "" {
		return ""
	}
	if namespace, _, ok := strings.Cut(trimmed, "::"); ok {
		return strings.TrimSpace(namespace)
	}
	if strings.HasPrefix(trimmed, "tc_") {
		parts := strings.SplitN(trimmed, "_", 3)
		if len(parts) >= 2 && strings.TrimSpace(parts[1]) != "" {
			return "tc_" + strings.TrimSpace(parts[1])
		}
	}
	return ""
}

func mergeReplayReasoning(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "", right == left:
		return left
	default:
		return left + "\n\n" + right
	}
}

func mergeReplayReasoningSignature(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "", right == left:
		return left
	default:
		return ""
	}
}

func mergeReplayReasoningSignatureSource(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "", right == left:
		return left
	default:
		return ""
	}
}

func mergeReplayReasoningMetadata(last *modeladapter.Message, current modeladapter.Message) {
	if last == nil {
		return
	}
	leftSignature := strings.TrimSpace(last.ReasoningSignature)
	rightSignature := strings.TrimSpace(current.ReasoningSignature)
	mergedSignature := mergeReplayReasoningSignature(leftSignature, rightSignature)
	last.ReasoningSignature = mergedSignature
	if mergedSignature == "" {
		last.ReasoningSignatureSource = ""
		last.OpenAIResponsesReasoningID = ""
		last.OpenAIResponsesReasoningStatus = ""
		last.OpenAIResponsesReasoningSummary = nil
		return
	}
	if leftSignature == "" && rightSignature != "" {
		last.ReasoningSignatureSource = strings.TrimSpace(current.ReasoningSignatureSource)
		last.OpenAIResponsesReasoningID = current.OpenAIResponsesReasoningID
		last.OpenAIResponsesReasoningStatus = current.OpenAIResponsesReasoningStatus
		last.OpenAIResponsesReasoningSummary = append(json.RawMessage(nil), current.OpenAIResponsesReasoningSummary...)
		return
	}
	if leftSignature == rightSignature {
		last.ReasoningSignatureSource = mergeReplayReasoningSignatureSource(last.ReasoningSignatureSource, current.ReasoningSignatureSource)
		if strings.TrimSpace(last.OpenAIResponsesReasoningID) == "" {
			last.OpenAIResponsesReasoningID = current.OpenAIResponsesReasoningID
		}
		if strings.TrimSpace(last.OpenAIResponsesReasoningStatus) == "" {
			last.OpenAIResponsesReasoningStatus = current.OpenAIResponsesReasoningStatus
		}
		if len(last.OpenAIResponsesReasoningSummary) == 0 {
			last.OpenAIResponsesReasoningSummary = append(json.RawMessage(nil), current.OpenAIResponsesReasoningSummary...)
		}
	}
}

func trimReplayDanglingAssistantToolCalls(messages []modeladapter.Message) []modeladapter.Message {
	if len(messages) == 0 {
		return nil
	}
	trimmed := make([]modeladapter.Message, 0, len(messages))
	for index := 0; index < len(messages); index++ {
		message := cloneReplayModelMessage(messages[index])
		if strings.TrimSpace(message.Role) != "assistant" || len(message.ToolCalls) == 0 {
			trimmed = append(trimmed, message)
			continue
		}

		end := index + 1
		responded := make(map[string]struct{}, len(message.ToolCalls))
		for end < len(messages) && strings.TrimSpace(messages[end].Role) == "tool" {
			toolCallID := strings.TrimSpace(messages[end].ToolCallID)
			if toolCallID != "" {
				responded[toolCallID] = struct{}{}
			}
			end++
		}

		nextToolCalls := make([]modeladapter.ToolCallDescriptor, 0, len(message.ToolCalls))
		allowedToolCallIDs := make(map[string]struct{}, len(message.ToolCalls))
		for _, toolCall := range message.ToolCalls {
			toolCallID := strings.TrimSpace(toolCall.ID)
			if _, ok := responded[toolCallID]; !ok {
				continue
			}
			item := toolCall
			item.Index = len(nextToolCalls)
			nextToolCalls = append(nextToolCalls, item)
			allowedToolCallIDs[toolCallID] = struct{}{}
		}

		if len(nextToolCalls) > 0 {
			message.ToolCalls = nextToolCalls
			trimmed = append(trimmed, message)
			for toolIndex := index + 1; toolIndex < end; toolIndex++ {
				toolMessage := cloneReplayModelMessage(messages[toolIndex])
				if _, ok := allowedToolCallIDs[strings.TrimSpace(toolMessage.ToolCallID)]; !ok {
					continue
				}
				trimmed = append(trimmed, toolMessage)
			}
		} else if strings.TrimSpace(message.Content) != "" || len(message.ContentParts) > 0 || hasReplayableReasoningPayload(message.ReasoningContent, message.ReasoningSignature, message.ReasoningSignatureSource) {
			message.ToolCalls = nil
			trimmed = append(trimmed, message)
		}

		index = end - 1
	}
	return trimmed
}

func shouldPersistToolResultName(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "PatchEdit", "PatchEditLines", "PatchEditSpan", "Edit", "Write", "GenerateImage":
		return true
	default:
		return false
	}
}

func filterCheckpointTurns(rawTurns [][]byte) [][]byte {
	if len(rawTurns) == 0 {
		return nil
	}
	filtered := make([][]byte, 0, len(rawTurns))
	for _, rawTurn := range rawTurns {
		if len(rawTurn) == 0 {
			continue
		}
		turn := &agentv1.ConversationTurnStructure{}
		if err := proto.Unmarshal(rawTurn, turn); err != nil {
			filtered = append(filtered, append([]byte(nil), rawTurn...))
			continue
		}
		agentTurn := turn.GetAgentConversationTurn()
		if agentTurn == nil {
			filtered = append(filtered, append([]byte(nil), rawTurn...))
			continue
		}

		nextSteps := make([][]byte, 0, len(agentTurn.GetSteps()))
		for _, rawStep := range agentTurn.GetSteps() {
			if len(rawStep) == 0 {
				continue
			}
			step := &agentv1.ConversationStep{}
			if err := proto.Unmarshal(rawStep, step); err != nil {
				continue
			}
			if toolCall := step.GetToolCall(); toolCall != nil && !shouldPersistToolResultName(inferToolName(toolCall)) {
				continue
			}
			nextSteps = append(nextSteps, append([]byte(nil), rawStep...))
		}
		if len(agentTurn.GetUserMessage()) == 0 && len(nextSteps) == 0 {
			continue
		}
		encoded, err := proto.Marshal(&agentv1.ConversationTurnStructure{
			Turn: &agentv1.ConversationTurnStructure_AgentConversationTurn{
				AgentConversationTurn: &agentv1.AgentConversationTurnStructure{
					UserMessage: append([]byte(nil), agentTurn.GetUserMessage()...),
					Steps:       nextSteps,
				},
			},
		})
		if err != nil {
			filtered = append(filtered, append([]byte(nil), rawTurn...))
			continue
		}
		filtered = append(filtered, encoded)
	}
	return filtered
}

func filterCheckpointPersistentToolReplay(messages []promptengine.Message) []promptengine.Message {
	if len(messages) == 0 {
		return nil
	}
	filtered := make([]promptengine.Message, 0, len(messages))
	skippedToolCallIDs := make(map[string]struct{})
	for _, message := range messages {
		if strings.TrimSpace(message.Role) == "assistant" && len(message.ToolCalls) > 0 {
			nextToolCalls := make([]promptengine.ToolCallDescriptor, 0, len(message.ToolCalls))
			for _, toolCall := range message.ToolCalls {
				if !shouldPersistToolResultName(toolCall.Function.Name) {
					skippedToolCallIDs[strings.TrimSpace(toolCall.ID)] = struct{}{}
					continue
				}
				nextToolCalls = append(nextToolCalls, toolCall)
			}
			if len(nextToolCalls) == 0 && strings.TrimSpace(message.Content) == "" && !hasReplayableReasoningPayload(message.ReasoningContent, message.ReasoningSignature, message.ReasoningSignatureSource) {
				continue
			}
			message.ToolCalls = nextToolCalls
			filtered = append(filtered, message)
			continue
		}
		if strings.TrimSpace(message.Role) == "tool" {
			if _, ok := skippedToolCallIDs[strings.TrimSpace(message.ToolCallID)]; ok {
				continue
			}
			if !shouldPersistToolResultName(message.Name) {
				continue
			}
		}
		filtered = append(filtered, message)
	}
	return filtered
}

func restoreImportedReplayUserMessages(messages []promptengine.Message, importedTurns [][]byte) []promptengine.Message {
	if len(messages) == 0 || len(importedTurns) == 0 {
		return messages
	}
	cursor := 0
	for _, rawTurn := range importedTurns {
		if len(rawTurn) == 0 {
			continue
		}
		turn := &agentv1.ConversationTurnStructure{}
		if err := proto.Unmarshal(rawTurn, turn); err != nil {
			continue
		}
		agentTurn := turn.GetAgentConversationTurn()
		if agentTurn == nil || len(agentTurn.GetUserMessage()) == 0 {
			continue
		}
		userMessage := &agentv1.UserMessage{}
		if err := proto.Unmarshal(agentTurn.GetUserMessage(), userMessage); err != nil {
			continue
		}
		replay, ok := promptengine.BuildUserMessageReplayMessage(userMessage)
		if !ok || len(replay.ContentParts) == 0 {
			continue
		}
		for cursor < len(messages) {
			if strings.TrimSpace(messages[cursor].Role) == "user" &&
				strings.TrimSpace(messages[cursor].Content) == strings.TrimSpace(replay.Content) {
				if len(messages[cursor].ContentParts) == 0 {
					messages[cursor].ContentParts = replay.ContentParts
				}
				if strings.TrimSpace(messages[cursor].Content) == "" {
					messages[cursor].Content = replay.Content
				}
				cursor++
				break
			}
			cursor++
		}
	}
	return messages
}

func isLegacyPlainWriteReplay(toolName string, hasStructuredToolCall bool) bool {
	return !hasStructuredToolCall && strings.TrimSpace(toolName) == "Write"
}

func overrideToolReplayFromEntry(messages []promptengine.Message, toolName string, arguments string) {
	overrideName := strings.TrimSpace(toolName)
	overrideArgs := strings.TrimSpace(arguments)
	if overrideName == "" || len(messages) == 0 {
		return
	}
	for index := range messages {
		switch strings.TrimSpace(messages[index].Role) {
		case "assistant":
			if len(messages[index].ToolCalls) == 0 {
				continue
			}
			for toolIndex := range messages[index].ToolCalls {
				currentName := strings.TrimSpace(messages[index].ToolCalls[toolIndex].Function.Name)
				effectiveName := effectiveReplayToolName(currentName, overrideName)
				effectiveArgs := firstNonEmpty(
					overrideArgs,
					strings.TrimSpace(messages[index].ToolCalls[toolIndex].Function.Arguments),
					"{}",
				)
				if isLegacyPatchEditToolName(overrideName) {
					effectiveArgs = firstNonEmpty(strings.TrimSpace(messages[index].ToolCalls[toolIndex].Function.Arguments), "{}")
				}
				messages[index].ToolCalls[toolIndex].Function.Name = effectiveName
				messages[index].ToolCalls[toolIndex].Function.Arguments = firstNonEmpty(effectiveArgs, "{}")
			}
		case "tool":
			messages[index].Name = effectiveReplayToolName(strings.TrimSpace(messages[index].Name), overrideName)
		}
	}
}

func overrideModelToolReplayFromEntry(message *modeladapter.Message, toolName string, arguments string) {
	if message == nil {
		return
	}
	overrideName := strings.TrimSpace(toolName)
	overrideArgs := strings.TrimSpace(arguments)
	if overrideName == "" {
		return
	}
	switch strings.TrimSpace(message.Role) {
	case "assistant":
		if len(message.ToolCalls) == 0 {
			return
		}
		for index := range message.ToolCalls {
			currentName := strings.TrimSpace(message.ToolCalls[index].Function.Name)
			effectiveName := effectiveReplayToolName(currentName, overrideName)
			effectiveArgs := firstNonEmpty(
				overrideArgs,
				strings.TrimSpace(message.ToolCalls[index].Function.Arguments),
				"{}",
			)
			if isLegacyPatchEditToolName(overrideName) {
				effectiveArgs = firstNonEmpty(strings.TrimSpace(message.ToolCalls[index].Function.Arguments), "{}")
			}
			message.ToolCalls[index].Function.Name = effectiveName
			message.ToolCalls[index].Function.Arguments = firstNonEmpty(effectiveArgs, "{}")
		}
	case "tool":
		message.Name = effectiveReplayToolName(strings.TrimSpace(message.Name), overrideName)
	}
}

func effectiveReplayToolName(currentName string, overrideName string) string {
	if isLegacyPatchEditToolName(currentName) || isLegacyPatchEditToolName(overrideName) {
		return "Edit"
	}
	switch strings.TrimSpace(currentName) {
	case "PatchEdit", "Edit", "Write":
		switch strings.TrimSpace(overrideName) {
		case "PatchEdit":
			return strings.TrimSpace(overrideName)
		case "Edit":
			return "Edit"
		case "Write":
			return "Write"
		}
		return strings.TrimSpace(currentName)
	default:
		return strings.TrimSpace(overrideName)
	}
}

func isLegacyPatchEditToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "PatchEditLines", "PatchEditSpan":
		return true
	default:
		return false
	}
}

func isLegacyPlainWriteToolCall(toolCall promptengine.ToolCallDescriptor) bool {
	if strings.TrimSpace(toolCall.Function.Name) != "Write" {
		return false
	}
	args := strings.TrimSpace(toolCall.Function.Arguments)
	return args == "" || args == "{}" || args == "null"
}

func filterLegacyPlainWriteReplay(messages []promptengine.Message) []promptengine.Message {
	if len(messages) == 0 {
		return nil
	}
	filtered := make([]promptengine.Message, 0, len(messages))
	skippedToolCallIDs := make(map[string]struct{})
	for _, message := range messages {
		if strings.TrimSpace(message.Role) == "assistant" && len(message.ToolCalls) > 0 {
			nextToolCalls := make([]promptengine.ToolCallDescriptor, 0, len(message.ToolCalls))
			for _, toolCall := range message.ToolCalls {
				if isLegacyPlainWriteToolCall(toolCall) {
					skippedToolCallIDs[strings.TrimSpace(toolCall.ID)] = struct{}{}
					continue
				}
				nextToolCalls = append(nextToolCalls, toolCall)
			}
			if len(nextToolCalls) == 0 && strings.TrimSpace(message.Content) == "" && !hasReplayableReasoningPayload(message.ReasoningContent, message.ReasoningSignature, message.ReasoningSignatureSource) {
				continue
			}
			message.ToolCalls = nextToolCalls
			filtered = append(filtered, message)
			continue
		}
		if strings.TrimSpace(message.Role) == "tool" {
			if _, ok := skippedToolCallIDs[strings.TrimSpace(message.ToolCallID)]; ok {
				continue
			}
		}
		filtered = append(filtered, message)
	}
	return filtered
}

func filterInternalPromptContextReplay(messages []promptengine.Message) []promptengine.Message {
	if len(messages) == 0 {
		return nil
	}
	filtered := make([]promptengine.Message, 0, len(messages))
	for _, message := range messages {
		if isInternalPromptContextReplayMessage(message) {
			continue
		}
		filtered = append(filtered, message)
	}
	return filtered
}

func isInternalPromptContextReplayMessage(message promptengine.Message) bool {
	if strings.TrimSpace(message.Role) != "user" {
		return false
	}
	if strings.TrimSpace(message.Name) != "" || strings.TrimSpace(message.ToolCallID) != "" || len(message.ToolCalls) > 0 || len(message.ContentParts) > 0 {
		return false
	}
	if strings.TrimSpace(message.ReasoningContent) != "" || strings.TrimSpace(message.ReasoningSignature) != "" {
		return false
	}
	return isInternalPromptContextContent(message.Content)
}

func isInternalPromptContextContent(content string) bool {
	trimmed := strings.TrimSpace(content)
	switch {
	case trimmed == strings.TrimSpace(todoSectionReminderMessage):
		return true
	case strings.HasPrefix(trimmed, "<system_reminder>") &&
		strings.HasSuffix(trimmed, "</system_reminder>") &&
		strings.Contains(trimmed, "You recently successfully edited ") &&
		strings.Contains(trimmed, "latest source of truth is the most recent successful"):
		return true
	default:
		return false
	}
}

// toModelMessage 把 promptengine 的消息结构转换为 modeladapter 消息结构。
func toModelMessage(message promptengine.Message) modeladapter.Message {
	return modeladapter.Message{
		Role:                            message.Role,
		Content:                         message.Content,
		ContentParts:                    toModelContentParts(message.ContentParts),
		ReasoningContent:                message.ReasoningContent,
		ReasoningSignature:              message.ReasoningSignature,
		ReasoningSignatureSource:        message.ReasoningSignatureSource,
		OpenAIResponsesReasoningID:      message.OpenAIResponsesReasoningID,
		OpenAIResponsesReasoningStatus:  message.OpenAIResponsesReasoningStatus,
		OpenAIResponsesReasoningSummary: append(json.RawMessage(nil), message.OpenAIResponsesReasoningSummary...),
		ToolCalls:                       toModelToolCalls(message.ToolCalls),
		ToolCallID:                      message.ToolCallID,
		Name:                            message.Name,
	}
}

func toModelContentParts(items []promptengine.ContentPart) []modeladapter.ContentPart {
	if len(items) == 0 {
		return nil
	}
	output := make([]modeladapter.ContentPart, 0, len(items))
	for _, item := range items {
		part := modeladapter.ContentPart{
			Type: item.Type,
			Text: item.Text,
		}
		if item.Image != nil {
			part.Image = &modeladapter.ImageContent{
				MIMEType: item.Image.MIMEType,
				Path:     item.Image.Path,
				Data:     item.Image.Data,
			}
		}
		output = append(output, part)
	}
	return output
}

func toPromptContentParts(items []modeladapter.ContentPart) []promptengine.ContentPart {
	if len(items) == 0 {
		return nil
	}
	output := make([]promptengine.ContentPart, 0, len(items))
	for _, item := range items {
		part := promptengine.ContentPart{
			Type: item.Type,
			Text: item.Text,
		}
		if item.Image != nil {
			part.Image = &promptengine.ImageContent{
				MIMEType: item.Image.MIMEType,
				Path:     item.Image.Path,
				Data:     item.Image.Data,
			}
		}
		output = append(output, part)
	}
	return output
}

// toModelToolCalls 把 promptengine 的 tool call 描述转换为 modeladapter 版本。
func toModelToolCalls(items []promptengine.ToolCallDescriptor) []modeladapter.ToolCallDescriptor {
	output := make([]modeladapter.ToolCallDescriptor, 0, len(items))
	for _, item := range items {
		output = append(output, modeladapter.ToolCallDescriptor{
			ID:                    item.ID,
			Index:                 item.Index,
			Type:                  item.Type,
			OpenAIResponsesID:     item.OpenAIResponsesID,
			OpenAIResponsesCallID: item.OpenAIResponsesCallID,
			OpenAIResponsesStatus: item.OpenAIResponsesStatus,
			Function: modeladapter.ToolCallFunctionShape{
				Name:      item.Function.Name,
				Arguments: item.Function.Arguments,
			},
		})
	}
	return output
}

// toPromptToolCalls 把 modeladapter 的 tool call 描述转换回 promptengine 版本。
func toPromptToolCalls(items []modeladapter.ToolCallDescriptor) []promptengine.ToolCallDescriptor {
	output := make([]promptengine.ToolCallDescriptor, 0, len(items))
	for _, item := range items {
		output = append(output, promptengine.ToolCallDescriptor{
			ID:                    item.ID,
			Index:                 item.Index,
			Type:                  item.Type,
			OpenAIResponsesID:     item.OpenAIResponsesID,
			OpenAIResponsesCallID: item.OpenAIResponsesCallID,
			OpenAIResponsesStatus: item.OpenAIResponsesStatus,
			Function: promptengine.ToolCallFunctionShape{
				Name:      item.Function.Name,
				Arguments: item.Function.Arguments,
			},
		})
	}
	return output
}
