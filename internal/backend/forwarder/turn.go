// turn.go 提取自 service.go：turn 收口、checkpoint 投影、provider 错误收尾与活动流失败。
package forwarder

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cursor/internal/logger"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func (service *Service) closeStreamWithProviderError(
	stream *ActiveStream,
	conversationID string,
	turnSeq int64,
	requestID string,
	accumulatedText string,
	accumulatedReasoning string,
	accumulatedReasoningSignature string,
	accumulatedReasoningSignatureSource string,
	accumulatedReasoningItemID string,
	accumulatedReasoningStatus string,
	accumulatedReasoningSummary json.RawMessage,
	usage turnUsageSnapshot,
	providerErr providerTerminalError,
	allowReasoningOnly bool,
) error {
	if stream == nil {
		return nil
	}
	errorText := strings.TrimSpace(providerErr.Error())
	if errorText == "" {
		errorText = "provider error"
	}
	modelCallID := strings.TrimSpace(stream.CurrentModelCallID)
	if err := service.flushAssistantText(stream, conversationID, turnSeq, requestID, accumulatedText, accumulatedReasoning, accumulatedReasoningSignature, accumulatedReasoningSignatureSource, accumulatedReasoningItemID, accumulatedReasoningStatus, accumulatedReasoningSummary, allowReasoningOnly); err != nil {
		return fmt.Errorf("flush provider error assistant output: %w", err)
	}
	if err := service.recordTurnUsageSnapshot(stream, conversationID, turnSeq, requestID, modelCallID, "provider_error", usage, errorText, false); err != nil {
		return fmt.Errorf("record provider error usage: %w", err)
	}
	if _, err := service.appendConversationEntries(stream, conversationID, []HistoryEntry{
		newMetadataEntry(turnSeq, requestID, "provider_error", map[string]any{
			"model_call_id": modelCallID,
			"error":         errorText,
		}),
	}); err != nil {
		return err
	}
	if err := service.recordTurnFinalizedSnapshot(stream, conversationID, turnSeq, requestID, "provider_error", errorText); err != nil {
		return fmt.Errorf("record provider error turn finalized: %w", err)
	}
	if err := service.updateConversationTokenState(stream, conversationID, usage, modelCallID, false); err != nil {
		return err
	}
	return service.failActiveStream(stream, conversationID, requestID, modelCallID, "provider_error", errorText)
}

func takePendingProviderCompletion(stream *ActiveStream) (pendingTurnCompletion, bool) {
	if stream == nil {
		return pendingTurnCompletion{}, false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.PendingProviderCompletion == nil {
		return pendingTurnCompletion{}, false
	}
	completion := *stream.PendingProviderCompletion
	stream.PendingProviderCompletion = nil
	stream.UpdatedAt = time.Now().UTC()
	return completion, true
}

func pendingBridgeCount(stream *ActiveStream) int {
	if stream == nil {
		return 0
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return len(stream.PendingExecs) + len(stream.PendingInteractions)
}

func (service *Service) finishDeferredTurnAfterInteraction(stream *ActiveStream, pending runtimecore.PendingInteraction) error {
	completion, ok := takePendingProviderCompletion(stream)
	if !ok {
		stream.mu.Lock()
		completion = pendingTurnCompletion{
			ConversationID: stream.ConversationID,
			RequestID:      stream.RequestID,
			TurnSeq:        stream.TurnSeq,
			ModelCallID:    firstNonEmpty(strings.TrimSpace(pending.ModelCallID), strings.TrimSpace(stream.CurrentModelCallID)),
			ProviderPass:   pending.ProviderPass,
		}
		stream.mu.Unlock()
		logger.Infof(
			"forwarder missing deferred turn completion snapshot request_id=%s tool_call_id=%s interaction_kind=%s provider_pass=%d",
			strings.TrimSpace(completion.RequestID),
			strings.TrimSpace(pending.ToolCallID),
			strings.TrimSpace(pending.InteractionKind),
			pending.ProviderPass,
		)
	}
	if strings.TrimSpace(completion.ModelCallID) == "" {
		completion.ModelCallID = strings.TrimSpace(pending.ModelCallID)
	}
	if completion.ProviderPass == 0 {
		completion.ProviderPass = pending.ProviderPass
	}
	return service.completeSuccessfulTurn(stream, completion)
}

func (service *Service) completeSuccessfulTurn(stream *ActiveStream, completion pendingTurnCompletion) error {
	if stream == nil {
		return nil
	}
	requestID := firstNonEmpty(strings.TrimSpace(completion.RequestID), strings.TrimSpace(stream.RequestID))
	conversationID := firstNonEmpty(strings.TrimSpace(completion.ConversationID), strings.TrimSpace(stream.ConversationID))
	modelCallID := firstNonEmpty(strings.TrimSpace(completion.ModelCallID), strings.TrimSpace(stream.CurrentModelCallID))
	turnSeq := completion.TurnSeq
	if turnSeq <= 0 {
		turnSeq = stream.TurnSeq
	}
	usage := completion.Usage
	if err := service.recordTurnUsageSnapshot(stream, conversationID, turnSeq, requestID, modelCallID, "completed", usage, "", false); err != nil {
		return fmt.Errorf("record completed turn usage: %w", err)
	}
	if _, err := service.appendConversationEntries(stream, conversationID, []HistoryEntry{
		newMetadataEntry(turnSeq, requestID, "turn_completed", map[string]any{
			"model_call_id": modelCallID,
		}),
	}); err != nil {
		return err
	}
	if err := service.recordTurnFinalizedSnapshot(stream, conversationID, turnSeq, requestID, "completed", ""); err != nil {
		return fmt.Errorf("record completed turn finalized: %w", err)
	}
	if err := service.syncSummaryCarryForward(conversationID, requestID, modelCallID); err != nil {
		logger.Infof(
			"forwarder summary sync after turn completion failed request_id=%s model_call_id=%s err=%v",
			strings.TrimSpace(requestID),
			strings.TrimSpace(modelCallID),
			err,
		)
	}
	if err := service.publishCheckpoint(requestID, conversationID); err != nil {
		return err
	}
	if err := service.broker.Publish(requestID, StreamEvent{
		Message: buildTurnEndedMessage(usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens),
	}); err != nil {
		return err
	}
	if err := service.broker.Complete(requestID, "", ""); err != nil {
		return err
	}
	service.setTurnPhase(stream, TurnPhaseCompleted)
	return nil
}

func (service *Service) failStreamIfNonTerminal(stream *ActiveStream, terminalCode string, cause error) error {
	if stream == nil || cause == nil {
		return nil
	}
	stream.mu.Lock()
	terminal := isTerminalStreamStatus(stream.Status)
	stream.mu.Unlock()
	if terminal {
		return nil
	}
	return service.failStream(stream, terminalCode, cause)
}

// publishCheckpoint 按当前内存会话镜像投影出 checkpoint，并广播给所有 RunSSE 订阅者。
func (service *Service) publishCheckpoint(requestID string, _ string) error {
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", requestID)
	}
	conversation, pendingExecs, pendingInteractions, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return err
	}
	state, err := service.projector.ProjectLegacyCheckpoint(conversation)
	if err != nil {
		return err
	}
	state.PendingToolCalls = buildPendingToolCalls(pendingExecs, pendingInteractions)
	service.rewriteCheckpointTokenDetailsForClient(stream, conversation, state)
	return service.broker.Publish(requestID, StreamEvent{
		Message: buildCheckpointMessage(state),
	})
}

func (service *Service) rewriteCheckpointTokenDetailsForClient(stream *ActiveStream, conversation *ConversationFile, state *agentv1.ConversationStateStructure) {
	if state == nil {
		return
	}
	if state.TokenDetails == nil {
		state.TokenDetails = &agentv1.ConversationTokenDetails{}
	}
	state.TokenDetails.MaxTokens = clampInt64ToUint32(service.checkpointDisplayMaxTokens(stream, conversation))
	compiled, hasCompiled := service.checkpointCompiledConversation(stream, conversation)
	state.TokenDetails.UsedTokens = clampInt64ToUint32(service.checkpointDisplayUsedTokens(conversation, state, compiled, hasCompiled))
	state.TokenDetails.Breakdown = estimateCheckpointPromptTokenBreakdown(compiled, hasCompiled, state.TokenDetails.UsedTokens, state.TokenDetails.MaxTokens)
}

func (service *Service) checkpointCompiledConversation(stream *ActiveStream, conversation *ConversationFile) (CompiledConversation, bool) {
	if service == nil || service.compiler == nil || conversation == nil {
		return CompiledConversation{}, false
	}
	modelID, _, latestUserText, mode := checkpointPromptContext(stream)
	compiled, err := service.compiler.Compile(conversation, mode, latestUserText, modelID)
	if err != nil {
		logger.Infof("forwarder checkpoint token estimate failed request_id=%s conversation_id=%s err=%v", strings.TrimSpace(activeStreamRequestID(stream)), strings.TrimSpace(conversation.ConversationID), err)
		return CompiledConversation{}, false
	}
	guarded := guardCompiledConversationForProvider(compiled)
	guarded = service.applyContextPostProcess(guarded, latestUserText, modelID)
	return guarded, true
}

func (service *Service) checkpointDisplayMaxTokens(stream *ActiveStream, conversation *ConversationFile) int64 {
	_ = stream
	maxTokens := int64(conversationTokenDetailsMaxTokens(conversation))
	if maxTokens < 1 {
		return 1
	}
	return maxTokens
}

func (service *Service) checkpointDisplayUsedTokens(conversation *ConversationFile, state *agentv1.ConversationStateStructure, compiled CompiledConversation, hasCompiled bool) int64 {
	usedTokens := int64(0)
	if state != nil && state.TokenDetails != nil {
		usedTokens = int64(state.TokenDetails.GetUsedTokens())
	}
	if conversation != nil && int64(conversation.TokenDetailsUsedTokens) > usedTokens {
		usedTokens = int64(conversation.TokenDetailsUsedTokens)
	}
	if hasCompiled {
		if estimatedTokens := estimateCompiledPromptTokens(compiled); estimatedTokens > usedTokens {
			usedTokens = estimatedTokens
		}
	}
	return usedTokens
}

func checkpointPromptContext(stream *ActiveStream) (string, string, string, agentv1.AgentMode) {
	if stream == nil {
		return "", "", "", agentv1.AgentMode_AGENT_MODE_AGENT
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.ModelID, stream.ModelName, stream.LatestUserText, stream.Mode
}

func activeStreamRequestID(stream *ActiveStream) string {
	if stream == nil {
		return ""
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.RequestID
}

// flushAssistantText 把本轮累计的 assistant 文本一次性写回 history。
func (service *Service) flushAssistantText(stream *ActiveStream, conversationID string, turnSeq int64, requestID string, text string, reasoningContent string, reasoningSignature string, reasoningSignatureSource string, reasoningItemID string, reasoningStatus string, reasoningSummary json.RawMessage, allowReasoningOnly bool) error {
	if strings.TrimSpace(text) == "" && (!allowReasoningOnly || !hasReplayableReasoningPayload(reasoningContent, reasoningSignature, reasoningSignatureSource)) {
		return nil
	}
	_, err := service.appendConversationEntries(stream, conversationID, []HistoryEntry{
		newAssistantTextEntryWithProviderMetadata(turnSeq, requestID, text, reasoningContent, reasoningSignature, reasoningSignatureSource, reasoningItemID, reasoningStatus, reasoningSummary),
	})
	return err
}

// failStream 在 provider 或投影失败时把错误写入 history 并收口活动流。
func (service *Service) failStream(stream *ActiveStream, terminalCode string, cause error) error {
	if stream == nil {
		return nil
	}
	errorText := "unknown error"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		errorText = strings.TrimSpace(cause.Error())
	}
	resolvedTerminalCode := resolveTerminalCode(terminalCode, cause)
	metadataType := "failed"
	var providerErr providerTerminalError
	if errors.As(cause, &providerErr) || resolvedTerminalCode == "provider_error" {
		metadataType = "provider_error"
	}
	_, _ = service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, metadataType, map[string]any{
			"error": errorText,
		}),
	})
	return service.failActiveStream(
		stream,
		stream.ConversationID,
		stream.RequestID,
		stream.CurrentModelCallID,
		resolvedTerminalCode,
		errorText,
	)
}

func resolveTerminalCode(fallback string, cause error) string {
	terminalCode := firstNonEmpty(strings.TrimSpace(fallback), TerminalErrorUnknown)
	if cause == nil || terminalCode != TerminalErrorUnknown {
		return terminalCode
	}
	var coded interface{ TerminalCode() string }
	if errors.As(cause, &coded) && strings.TrimSpace(coded.TerminalCode()) != "" {
		return strings.TrimSpace(coded.TerminalCode())
	}
	return terminalCode
}

func (service *Service) failActiveStream(stream *ActiveStream, conversationID string, requestID string, modelCallID string, terminalCode string, terminalMessage string) error {
	if stream == nil {
		return nil
	}
	clearPendingProviderCompletion(stream)
	stream.mu.Lock()
	cancel := stream.ProviderCancel
	stream.ProviderActive = false
	stream.ProviderCancel = nil
	stream.PendingProviderAction = providerActionNone
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	service.setTurnPhase(stream, TurnPhaseFailed)
	var firstErr error
	if err := service.syncSummaryCarryForward(conversationID, requestID, modelCallID); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := service.publishCheckpoint(requestID, conversationID); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := service.broker.Fail(requestID, terminalCode, terminalMessage); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
