// missing_funcs.go restores functions lost during service.go TD-002 extraction.
// These definitions were removed from service.go but not yet placed in any
// extracted file. They are collected here until each is moved to its
// appropriate extracted module.
//
// ponytail: TD-002 临时收容所。当前编译通过、功能正常。
// 迁移计划（按职责）：
//   - new*Entry 系列（newAssistantTextEntry*, newToolCallEntry*, newToolResultEntry, newMetadataEntry）→ history_entries.go
//   - extract* 系列（extractUserMessage, extractRequestContext, extractConversationAction*）→ intent_extract.go
//   - buildRun* / newMode* / newModeChangePromptContextEntry → 考虑新建 run_entry_builder.go 或并入 history_entries.go
//   - shouldIgnoreEmptyResumeRunRequest / loadConversationForResumeGuard / hasActiveConversationStream → service.go（resume guard 相关）
// 当前状态：517 行，保持不变以降低风险。逐步迁移时每次移动 1-2 个函数并验证编译。
package forwarder

import (
	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	"encoding/json"
	"fmt"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"strings"
)

func buildRunEntries(intent InboundIntent, effectiveMode agentv1.AgentMode, turnSeq int64) ([]HistoryEntry, error) {
	entries := make([]HistoryEntry, 0, 4)
	if intent.RequestContext != nil {
		normalized := normalizeRequestContextForStorageMode(intent.RequestContext, turnSeq == 1)
		if normalized != nil {
			payload, err := protojson.Marshal(normalized)
			if err != nil {
				return nil, err
			}
			entries = append(entries, HistoryEntry{
				TurnSeq:   turnSeq,
				RequestID: intent.RequestID,
				Role:      "user",
				Kind:      "request_context",
				Payload:   payload,
			})
		}
	}
	if intent.UserMessage != nil {
		payload, err := protojson.Marshal(normalizeUserMessageForStorage(intent.UserMessage))
		if err != nil {
			return nil, err
		}
		entries = append(entries, HistoryEntry{
			TurnSeq:   turnSeq,
			RequestID: intent.RequestID,
			Role:      "user",
			Kind:      EntryKindUserMessage,
			Payload:   payload,
		})
	}
	modeEntry, err := newModeMetadataEntry(turnSeq, intent.RequestID, effectiveMode, intent.HasExplicitMode, intent.ModeSource)
	if err != nil {
		return nil, err
	}
	entries = append(entries,
		modeEntry,
		newMetadataEntry(turnSeq, intent.RequestID, "run_request", buildRunRequestMetadata(intent)),
	)
	if intent.HasExplicitMode {
		entries = append(entries, newModeChangePromptContextEntry(turnSeq, intent.RequestID, effectiveMode))
	}
	return entries, nil
}

func buildRunRequestMetadata(intent InboundIntent) map[string]any {
	return map[string]any{
		"model_id":   intent.ModelID,
		"model_name": intent.ModelName,
		"prewarm":    intent.Prewarm,
	}
}

func newModeMetadataEntry(turnSeq int64, requestID string, mode agentv1.AgentMode, explicit bool, source ModeSource) (HistoryEntry, error) {
	modeAliasValue, err := modeAlias(mode)
	if err != nil {
		return HistoryEntry{}, err
	}
	payload := map[string]any{
		"mode": modeAliasValue,
	}
	if explicit {
		payload["explicit"] = true
	}
	if strings.TrimSpace(string(source)) != "" {
		payload["source"] = strings.TrimSpace(string(source))
	}
	return newMetadataEntry(turnSeq, requestID, "mode", payload), nil
}

func newModeChangePromptContextEntry(turnSeq int64, requestID string, mode agentv1.AgentMode) HistoryEntry {
	modeAliasValue, err := modeAlias(mode)
	if err != nil {
		modeAliasValue = "agent"
	}
	return newPromptContextEntry(turnSeq, requestID, newPromptContextMessage(
		"mode_change",
		modeladapter.Message{
			Role:    "user",
			Content: wrapSystemReminder(fmt.Sprintf("At this point, the active mode changed to %s; follow later mode reminders if present.", modeAliasValue)),
		},
		true,
	))
}

// newAssistantTextEntry 构造 assistant 文本 entry。
func newAssistantTextEntry(turnSeq int64, requestID string, text string, reasoningContent string, reasoningSignature string) HistoryEntry {
	return newAssistantTextEntryWithProviderMetadata(turnSeq, requestID, text, reasoningContent, reasoningSignature, "", "", "", nil)
}

func newAssistantTextEntryWithProviderMetadata(turnSeq int64, requestID string, text string, reasoningContent string, reasoningSignature string, reasoningSignatureSource string, reasoningItemID string, reasoningStatus string, reasoningSummary json.RawMessage) HistoryEntry {
	payload, _ := json.Marshal(assistantTextPayload{
		Text:                     text,
		ReasoningContent:         reasoningContent,
		ReasoningSignature:       strings.TrimSpace(reasoningSignature),
		ReasoningSignatureSource: strings.TrimSpace(reasoningSignatureSource),
		ReasoningItemID:          strings.TrimSpace(reasoningItemID),
		ReasoningStatus:          strings.TrimSpace(reasoningStatus),
		ReasoningSummary:         append(json.RawMessage(nil), reasoningSummary...),
	})
	return HistoryEntry{
		TurnSeq:   turnSeq,
		RequestID: strings.TrimSpace(requestID),
		Role:      "assistant",
		Kind:      EntryKindAssistantText,
		Payload:   payload,
	}
}

// newToolCallEntry 构造 tool_call entry。
func newToolCallEntry(turnSeq int64, requestID string, toolCallID string, toolName string, reasoningContent string, reasoningSignature string, toolCall json.RawMessage) HistoryEntry {
	return newToolCallEntryWithProviderMetadata(turnSeq, requestID, toolCallID, toolName, reasoningContent, reasoningSignature, "", "", "", nil, "", "", "", toolCall)
}

func newToolCallEntryWithProviderMetadata(turnSeq int64, requestID string, toolCallID string, toolName string, reasoningContent string, reasoningSignature string, reasoningSignatureSource string, reasoningItemID string, reasoningStatus string, reasoningSummary json.RawMessage, providerItemID string, providerCallID string, providerStatus string, toolCall json.RawMessage) HistoryEntry {
	payload, _ := json.Marshal(toolCallEntryPayload{
		ToolCallID:               strings.TrimSpace(toolCallID),
		ToolName:                 strings.TrimSpace(toolName),
		ReasoningContent:         reasoningContent,
		ReasoningSignature:       strings.TrimSpace(reasoningSignature),
		ReasoningSignatureSource: strings.TrimSpace(reasoningSignatureSource),
		ReasoningItemID:          strings.TrimSpace(reasoningItemID),
		ReasoningStatus:          strings.TrimSpace(reasoningStatus),
		ReasoningSummary:         append(json.RawMessage(nil), reasoningSummary...),
		ProviderItemID:           strings.TrimSpace(providerItemID),
		ProviderCallID:           strings.TrimSpace(providerCallID),
		ProviderStatus:           strings.TrimSpace(providerStatus),
		ToolCall:                 append(json.RawMessage(nil), toolCall...),
	})
	return HistoryEntry{
		TurnSeq:    turnSeq,
		RequestID:  strings.TrimSpace(requestID),
		Role:       "assistant",
		Kind:       EntryKindToolCall,
		ToolCallID: strings.TrimSpace(toolCallID),
		Payload:    payload,
	}
}

// newToolResultEntry 构造 tool_result entry。
func newToolResultEntry(turnSeq int64, requestID string, toolCallID string, toolName string, arguments string, resultText string, reasoningContent string, toolCall json.RawMessage) HistoryEntry {
	payload, _ := json.Marshal(toolResultEntryPayload{
		ToolCallID:       strings.TrimSpace(toolCallID),
		ToolName:         strings.TrimSpace(toolName),
		Arguments:        strings.TrimSpace(arguments),
		ResultText:       strings.TrimSpace(resultText),
		ReasoningContent: strings.TrimSpace(reasoningContent),
		ToolCall:         append(json.RawMessage(nil), toolCall...),
	})
	return HistoryEntry{
		TurnSeq:    turnSeq,
		RequestID:  strings.TrimSpace(requestID),
		Role:       "tool",
		Kind:       EntryKindToolResult,
		ToolCallID: strings.TrimSpace(toolCallID),
		Payload:    payload,
	}
}

// newMetadataEntry 构造 metadata entry。
func newMetadataEntry(turnSeq int64, requestID string, eventType string, values map[string]any) HistoryEntry {
	payload, _ := json.Marshal(metadataPayload{
		Type:  strings.TrimSpace(eventType),
		Value: values,
	})
	return HistoryEntry{
		TurnSeq:   turnSeq,
		RequestID: strings.TrimSpace(requestID),
		Role:      "system",
		Kind:      "metadata",
		Payload:   payload,
	}
}

// extractUserMessage 从 legacy run_request 中提取用户消息。
func extractUserMessage(message *agentv1.AgentClientMessage) *agentv1.UserMessage {
	if message == nil || message.GetRunRequest() == nil || message.GetRunRequest().GetAction() == nil {
		return nil
	}
	switch item := message.GetRunRequest().GetAction().GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction:
		return item.UserMessageAction.GetUserMessage()
	case *agentv1.ConversationAction_StartPlanAction:
		return item.StartPlanAction.GetUserMessage()
	default:
		return nil
	}
}

// extractRequestContext 从 legacy 请求中提取 request_context。
func extractRequestContext(message *agentv1.AgentClientMessage) *agentv1.RequestContext {
	if message == nil || message.GetRunRequest() == nil || message.GetRunRequest().GetAction() == nil {
		return nil
	}
	switch item := message.GetRunRequest().GetAction().GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction:
		return item.UserMessageAction.GetRequestContext()
	case *agentv1.ConversationAction_ResumeAction:
		return item.ResumeAction.GetRequestContext()
	case *agentv1.ConversationAction_StartPlanAction:
		return item.StartPlanAction.GetRequestContext()
	case *agentv1.ConversationAction_ExecutePlanAction:
		return item.ExecutePlanAction.GetRequestContext()
	default:
		return nil
	}
}

func requestContextHasPayload(requestContext *agentv1.RequestContext) bool {
	return requestContext != nil && proto.Size(requestContext) > 0
}

func emptyResumeCanBeIgnoredForConversation(conversation *ConversationFile) bool {
	if conversation == nil {
		return false
	}
	status := strings.TrimSpace(conversation.CurrentLoopStatus)
	currentRequestID := strings.TrimSpace(conversation.CurrentRequestID)
	if status == "" {
		return currentRequestID == ""
	}
	switch status {
	case "completed", "idle":
		return true
	default:
		return false
	}
}

func extractConversationActionUserMessage(action *agentv1.ConversationAction) *agentv1.UserMessage {
	if action == nil {
		return nil
	}
	switch item := action.GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction:
		return item.UserMessageAction.GetUserMessage()
	case *agentv1.ConversationAction_StartPlanAction:
		return item.StartPlanAction.GetUserMessage()
	default:
		return nil
	}
}

func extractConversationActionRequestContext(action *agentv1.ConversationAction) *agentv1.RequestContext {
	if action == nil {
		return nil
	}
	switch item := action.GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction:
		return item.UserMessageAction.GetRequestContext()
	case *agentv1.ConversationAction_ResumeAction:
		return item.ResumeAction.GetRequestContext()
	case *agentv1.ConversationAction_StartPlanAction:
		return item.StartPlanAction.GetRequestContext()
	case *agentv1.ConversationAction_ExecutePlanAction:
		return item.ExecutePlanAction.GetRequestContext()
	default:
		return nil
	}
}

func conversationActionIsResume(action *agentv1.ConversationAction) bool {
	if action == nil {
		return false
	}
	_, ok := action.GetAction().(*agentv1.ConversationAction_ResumeAction)
	return ok
}

func conversationActionStartsRun(action *agentv1.ConversationAction) bool {
	if action == nil {
		return false
	}
	switch action.GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction,
		*agentv1.ConversationAction_ResumeAction,
		*agentv1.ConversationAction_StartPlanAction,
		*agentv1.ConversationAction_ExecutePlanAction:
		return true
	default:
		return false
	}
}

// extractRunMode 推导本轮应使用的 mode。
func extractRunMode(message *agentv1.AgentClientMessage) (agentv1.AgentMode, ModeSource, bool, error) {
	if userMessage := extractUserMessage(message); userMessage != nil && userMessage.GetMode() != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
		return resolveExplicitMode(userMessage.GetMode(), ModeSourceUserMessage)
	}
	if message != nil && message.GetRunRequest() != nil && message.GetRunRequest().GetAction() != nil {
		if item, ok := message.GetRunRequest().GetAction().GetAction().(*agentv1.ConversationAction_ExecutePlanAction); ok && item.ExecutePlanAction != nil {
			if mode := item.ExecutePlanAction.GetExecutionMode(); mode != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
				return resolveExplicitMode(mode, ModeSourceExecutePlanAction)
			}
		}
	}
	if message != nil && message.GetRunRequest() != nil && message.GetRunRequest().GetConversationState() != nil {
		if mode := message.GetRunRequest().GetConversationState().GetMode(); mode != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
			return resolveExplicitMode(mode, ModeSourceConversationState)
		}
	}
	return agentv1.AgentMode_AGENT_MODE_AGENT, ModeSourceUnknown, false, nil
}

func extractPrewarmMode(request *agentv1.PrewarmRequest) (agentv1.AgentMode, ModeSource, bool, error) {
	if request == nil || request.GetConversationState() == nil {
		return agentv1.AgentMode_AGENT_MODE_AGENT, ModeSourceUnknown, false, nil
	}
	mode := request.GetConversationState().GetMode()
	if mode == agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
		return agentv1.AgentMode_AGENT_MODE_AGENT, ModeSourceUnknown, false, nil
	}
	return resolveExplicitMode(mode, ModeSourceConversationState)
}

func extractConversationActionMode(action *agentv1.ConversationAction) (agentv1.AgentMode, ModeSource, bool, error) {
	if userMessage := extractConversationActionUserMessage(action); userMessage != nil && userMessage.GetMode() != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
		return resolveExplicitMode(userMessage.GetMode(), ModeSourceUserMessage)
	}
	if action == nil {
		return agentv1.AgentMode_AGENT_MODE_AGENT, ModeSourceUnknown, false, nil
	}
	switch item := action.GetAction().(type) {
	case *agentv1.ConversationAction_ExecutePlanAction:
		if item.ExecutePlanAction != nil && item.ExecutePlanAction.GetExecutionMode() != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
			return resolveExplicitMode(item.ExecutePlanAction.GetExecutionMode(), ModeSourceExecutePlanAction)
		}
	}
	return agentv1.AgentMode_AGENT_MODE_AGENT, ModeSourceUnknown, false, nil
}

// extractRequestedModelID 提取本轮显式请求的模型 ID。
func extractRequestedModelID(message *agentv1.AgentClientMessage) string {
	if message == nil {
		return ""
	}
	if runRequest := message.GetRunRequest(); runRequest != nil {
		return firstNonEmpty(extractRequestedModelIDFromRequestedModel(runRequest.GetRequestedModel()), runRequest.GetModelDetails().GetModelId())
	}
	if prewarm := message.GetPrewarmRequest(); prewarm != nil {
		return firstNonEmpty(extractRequestedModelIDFromRequestedModel(prewarm.GetRequestedModel()), prewarm.GetModelDetails().GetModelId())
	}
	return ""
}

func extractRequestedModelIDFromRequestedModel(model *agentv1.RequestedModel) string {
	if model == nil {
		return ""
	}
	if model.GetIsVariantStringRepresentation() {
		modelID, _ := splitRuntimeThinkingEffortVariantString(model.GetModelId())
		return modelID
	}
	return strings.TrimSpace(model.GetModelId())
}

func extractRuntimeThinkingEffort(message *agentv1.AgentClientMessage) string {
	if message == nil {
		return ""
	}
	if runRequest := message.GetRunRequest(); runRequest != nil {
		return extractRuntimeThinkingEffortFromRequestedModel(runRequest.GetRequestedModel())
	}
	if prewarm := message.GetPrewarmRequest(); prewarm != nil {
		return extractRuntimeThinkingEffortFromRequestedModel(prewarm.GetRequestedModel())
	}
	return ""
}

func extractRuntimeThinkingEffortFromRequestedModel(model *agentv1.RequestedModel) string {
	if model == nil {
		return ""
	}
	for _, parameter := range model.GetParameters() {
		if parameter == nil || !isRuntimeThinkingEffortParameterID(parameter.GetId()) {
			continue
		}
		if effort := normalizeRuntimeThinkingEffort(parameter.GetValue()); effort != "" {
			return effort
		}
	}
	if model.GetIsVariantStringRepresentation() {
		if _, effort := splitRuntimeThinkingEffortVariantString(model.GetModelId()); effort != "" {
			return effort
		}
		return normalizeRuntimeThinkingEffort(model.GetModelId())
	}
	return ""
}

func isRuntimeThinkingEffortParameterID(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case runtimeThinkingEffortParameterID,
		"reasoning",
		"reasoning_effort",
		"thinking_intensity",
		"anthropic_thinking_effort",
		"openai_reasoning_effort":
		return true
	default:
		return false
	}
}

func normalizeRuntimeThinkingEffort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "disabled", "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(raw))
	case "disable", "off", "none", "false", "no", "0":
		return "disabled"
	case "very_high", "very-high", "veryhigh", "x-high", "extra_high", "extra-high", "extrahigh":
		return "xhigh"
	case "maximum":
		return "max"
	default:
		return ""
	}
}

func splitRuntimeThinkingEffortVariantString(raw string) (string, string) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", ""
	}
	if effort := normalizeRuntimeThinkingEffort(text); effort != "" {
		return "", effort
	}
	index := strings.LastIndex(text, ":")
	if index <= 0 || index >= len(text)-1 {
		return "", ""
	}
	modelID := strings.TrimSpace(text[:index])
	effort := normalizeRuntimeThinkingEffort(text[index+1:])
	if modelID == "" || effort == "" {
		return "", ""
	}
	return modelID, effort
}

func (service *Service) shouldIgnoreEmptyResumeRunRequest(requestID string, runRequest *agentv1.AgentRunRequest, userMessage *agentv1.UserMessage, requestContext *agentv1.RequestContext) bool {
	if runRequest == nil || !conversationActionIsResume(runRequest.GetAction()) {
		return false
	}
	if userMessage != nil || requestContextHasPayload(requestContext) {
		return false
	}
	state := runRequest.GetConversationState()
	if state != nil && len(state.GetPendingToolCalls()) > 0 {
		return false
	}
	conversationID := strings.TrimSpace(runRequest.GetConversationId())
	if conversationID == "" || service.hasActiveConversationStream(conversationID, requestID) {
		return false
	}
	conversation, err := service.loadConversationForResumeGuard(conversationID)
	if err != nil || conversation == nil {
		return false
	}
	return emptyResumeCanBeIgnoredForConversation(conversation)
}

func (service *Service) loadConversationForResumeGuard(conversationID string) (*ConversationFile, error) {
	if service == nil || service.store == nil {
		return nil, nil
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, nil
	}
	return service.store.LoadConversation(conversationID)
}

func (service *Service) hasActiveConversationStream(conversationID string, requestID string) bool {
	conversationID = strings.TrimSpace(conversationID)
	if service == nil || service.broker == nil || conversationID == "" {
		return false
	}
	if len(service.broker.OtherConversationRequestIDs(conversationID, requestID)) > 0 {
		return true
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if strings.TrimSpace(stream.ConversationID) != conversationID {
		return false
	}
	if isTerminalStreamStatus(stream.Status) {
		return false
	}
	switch stream.Phase {
	case TurnPhaseCanceled, TurnPhaseCompleted, TurnPhaseFailed:
		return false
	default:
		return true
	}
}

