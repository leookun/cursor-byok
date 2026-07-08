package forwarder

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
)

type TurnPhase string

const (
	TurnPhaseIdle            TurnPhase = "idle"
	TurnPhaseProviderRunning TurnPhase = "provider_running"
	TurnPhaseWaitingExternal TurnPhase = "waiting_external"
	TurnPhaseAwaitingUser    TurnPhase = "awaiting_user"
	TurnPhaseCompacting      TurnPhase = "compacting"
	TurnPhaseCompleted       TurnPhase = "completed"
	TurnPhaseFailed          TurnPhase = "failed"
	TurnPhaseCanceled        TurnPhase = "canceled"
)

type providerAction string

const (
	providerActionNone   providerAction = ""
	providerActionStart  providerAction = "start"
	providerActionResume providerAction = "resume"
)

type pendingCompletionDisposition string

const (
	completionDispositionNone                  pendingCompletionDisposition = ""
	completionDispositionResumeAfterExternal   pendingCompletionDisposition = "resume_after_external"
	completionDispositionCompleteAfterExternal pendingCompletionDisposition = "complete_after_external"
)

type streamCommandKind string

const (
	streamCommandRun               streamCommandKind = "run"
	streamCommandCancel            streamCommandKind = "cancel"
	streamCommandMetadata          streamCommandKind = "metadata"
	streamCommandExecResult        streamCommandKind = "exec_result"
	streamCommandExecControl       streamCommandKind = "exec_control"
	streamCommandInteractionResult streamCommandKind = "interaction_result"
	streamCommandProviderEvent     streamCommandKind = "provider_event"
	streamCommandTimerFired        streamCommandKind = "timer_fired"
	streamCommandCompactionEvent   streamCommandKind = "compaction_event"
	streamCommandMaybeOrphaned     streamCommandKind = "maybe_orphaned"
)

type streamTimerKind string

const (
	streamTimerProviderResume       streamTimerKind = "provider_resume"
	streamTimerNonStreamingRecovery streamTimerKind = "non_streaming_recovery"
	streamTimerShellForeground      streamTimerKind = "shell_foreground"
	streamTimerShellTransportClose  streamTimerKind = "shell_transport_close"
	streamTimerOrphanCancel         streamTimerKind = "orphan_cancel"
)

type streamProviderEvent struct {
	Token uint64
	Event modeladapter.ModelEvent
	Done  bool
	Err   error
}

type streamTimerEvent struct {
	Key       string
	Kind      streamTimerKind
	Token     uint64
	ExecID    string
	MessageID uint32
	Reason    string
}

type streamCompactionEvent struct {
	Token       uint64
	Plan        *PendingCompaction
	SummaryText string
	Err         error
}

type streamCommand struct {
	Kind       streamCommandKind
	Intent     InboundIntent
	Provider   *streamProviderEvent
	Timer      *streamTimerEvent
	Compaction *streamCompactionEvent
	Reason     string
}

type streamCommandEnvelope struct {
	command streamCommand
	result  chan error
}

func commandKindForIntent(intent InboundIntent) (streamCommandKind, error) {
	switch strings.TrimSpace(intent.Kind) {
	case "run":
		return streamCommandRun, nil
	case "cancel":
		return streamCommandCancel, nil
	case "metadata", "kv_result":
		return streamCommandMetadata, nil
	case "exec_result":
		return streamCommandExecResult, nil
	case "exec_control":
		return streamCommandExecControl, nil
	case "interaction_result":
		return streamCommandInteractionResult, nil
	default:
		return "", fmt.Errorf("unsupported inbound intent: %s", intent.Kind)
	}
}

func (service *Service) dispatchInboundIntent(intent InboundIntent) error {
	if service == nil {
		return fmt.Errorf("forwarder service is nil")
	}
	stream, err := service.streamForIntent(intent)
	if err != nil {
		return err
	}
	if stream == nil {
		return nil
	}
	commandKind, err := commandKindForIntent(intent)
	if err != nil {
		return err
	}
	return service.postStreamCommandWait(stream, streamCommand{
		Kind:   commandKind,
		Intent: intent,
	})
}

func (service *Service) streamForIntent(intent InboundIntent) (*ActiveStream, error) {
	switch strings.TrimSpace(intent.Kind) {
	case "run":
		stream, err := service.broker.OpenStream(
			intent.RequestID,
			intent.ConversationID,
			0,
			intent.ModelID,
			intent.ModelName,
			intent.Mode,
			userMessageText(intent.UserMessage),
		)
		if err != nil {
			return nil, err
		}
		if stream == nil {
			return nil, fmt.Errorf("open stream failed")
		}
		return stream, nil
	case "metadata", "kv_result":
		stream, ok := service.broker.Get(intent.RequestID)
		if !ok || stream == nil {
			if intent.HasExplicitMode || intent.StartsRun {
				return nil, fmt.Errorf("metadata intent requires active request context: %s", intent.RequestID)
			}
			return nil, nil
		}
		if isTerminalIntentStream(stream) {
			return nil, nil
		}
		return stream, nil
	default:
		stream, ok := service.broker.Get(intent.RequestID)
		if !ok || stream == nil {
			return nil, fmt.Errorf("request is not active: %s", intent.RequestID)
		}
		if isTerminalIntentStream(stream) {
			return nil, nil
		}
		return stream, nil
	}
}

func isTerminalIntentStream(stream *ActiveStream) bool {
	if stream == nil {
		return true
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if isTerminalStreamStatus(stream.Status) {
		return true
	}
	switch stream.Phase {
	case TurnPhaseCanceled, TurnPhaseCompleted, TurnPhaseFailed:
		return true
	default:
		return false
	}
}

func (service *Service) ensureStreamActor(stream *ActiveStream) (chan streamCommandEnvelope, chan struct{}, error) {
	if stream == nil {
		return nil, nil, fmt.Errorf("active stream is required")
	}
	stream.mu.Lock()
	if stream.ActorMailbox != nil && stream.ActorDone != nil {
		mailbox := stream.ActorMailbox
		done := stream.ActorDone
		stream.mu.Unlock()
		return mailbox, done, nil
	}
	mailbox := make(chan streamCommandEnvelope, 128)
	done := make(chan struct{})
	stream.ActorMailbox = mailbox
	stream.ActorDone = done
	if stream.TimerTokens == nil {
		stream.TimerTokens = make(map[string]uint64)
	}
	if strings.TrimSpace(string(stream.Phase)) == "" {
		stream.Phase = TurnPhaseIdle
	}
	stream.mu.Unlock()
	go service.runStreamActor(stream, mailbox, done)
	return mailbox, done, nil
}

func (service *Service) postStreamCommandWait(stream *ActiveStream, command streamCommand) error {
	if stream == nil {
		return nil
	}
	mailbox, done, err := service.ensureStreamActor(stream)
	if err != nil {
		return err
	}
	result := make(chan error, 1)
	envelope := streamCommandEnvelope{
		command: command,
		result:  result,
	}
	select {
	case <-done:
		return errProviderLoopInterrupted
	case mailbox <- envelope:
	}
	select {
	case <-done:
		return errProviderLoopInterrupted
	case err := <-result:
		return err
	}
}

func (service *Service) postStreamCommandAsync(stream *ActiveStream, command streamCommand) error {
	if stream == nil {
		return nil
	}
	mailbox, done, err := service.ensureStreamActor(stream)
	if err != nil {
		return err
	}
	envelope := streamCommandEnvelope{command: command}
	select {
	case <-done:
		return errProviderLoopInterrupted
	case mailbox <- envelope:
		return nil
	}
}

func (service *Service) runStreamActor(stream *ActiveStream, mailbox <-chan streamCommandEnvelope, done chan struct{}) {
	defer close(done)
	for {
		envelope, ok := <-mailbox
		if !ok {
			return
		}
		err := service.handleStreamCommand(stream, envelope.command)
		if envelope.result != nil {
			envelope.result <- err
		} else if err != nil {
			_ = service.failStream(stream, "unknown", err)
		}
		if shouldStopStreamActor(stream) {
			return
		}
	}
}

func shouldStopStreamActor(stream *ActiveStream) bool {
	if stream == nil {
		return true
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if isTerminalStreamStatus(stream.Status) {
		return true
	}
	switch stream.Phase {
	case TurnPhaseCompleted, TurnPhaseFailed, TurnPhaseCanceled:
		return true
	default:
		return false
	}
}

func (service *Service) handleStreamCommand(stream *ActiveStream, command streamCommand) error {
	switch command.Kind {
	case streamCommandRun:
		return service.handleRunIntent(command.Intent)
	case streamCommandCancel:
		return service.handleCancelIntent(command.Intent)
	case streamCommandMetadata:
		return service.handleMetadataIntent(command.Intent)
	case streamCommandExecResult:
		return service.handleExecResult(command.Intent)
	case streamCommandExecControl:
		return service.handleExecControl(command.Intent)
	case streamCommandInteractionResult:
		return service.handleInteractionResult(command.Intent)
	case streamCommandProviderEvent:
		return service.handleProviderEvent(stream, command.Provider)
	case streamCommandTimerFired:
		return service.handleTimerEvent(stream, command.Timer)
	case streamCommandCompactionEvent:
		return service.handleCompactionEvent(stream, command.Compaction)
	case streamCommandMaybeOrphaned:
		if stream == nil {
			return nil
		}
		stream.mu.Lock()
		subscriberCount := len(stream.Subscribers)
		status := stream.Status
		stream.mu.Unlock()
		if subscriberCount > 0 || isTerminalStreamStatus(status) {
			return nil
		}
		service.scheduleStreamTimer(stream, providerTimerKey(streamTimerOrphanCancel, ""), orphanSubscriberGracePeriod, streamTimerOrphanCancel, "", 0, command.Reason)
		return nil
	default:
		return fmt.Errorf("unsupported stream command kind: %s", command.Kind)
	}
}

func (service *Service) requestProviderAction(stream *ActiveStream, action providerAction) error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	switch action {
	case providerActionStart:
		stream.PendingProviderAction = providerActionStart
	case providerActionResume:
		if stream.PendingProviderAction != providerActionStart {
			stream.PendingProviderAction = providerActionResume
		}
	default:
		stream.PendingProviderAction = providerActionNone
	}
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	return service.reconcileStream(stream)
}

func (service *Service) reconcileStream(stream *ActiveStream) error {
	if stream == nil {
		return nil
	}

	stream.mu.Lock()
	if isTerminalStreamStatus(stream.Status) {
		stream.mu.Unlock()
		return nil
	}
	providerActive := stream.ProviderActive
	pendingExecCount := len(stream.PendingExecs)
	pendingInteractionCount := len(stream.PendingInteractions)
	hasPendingCompaction := stream.PendingCompaction != nil
	action := stream.PendingProviderAction
	completion := stream.PendingProviderCompletion
	stream.mu.Unlock()

	if providerActive {
		return nil
	}
	if pendingExecCount+pendingInteractionCount > 0 {
		if hasPendingAwaitingUserInteraction(stream) {
			service.setTurnPhase(stream, TurnPhaseAwaitingUser)
		} else if hasPendingCompaction {
			service.setTurnPhase(stream, TurnPhaseCompacting)
		} else {
			service.setTurnPhase(stream, TurnPhaseWaitingExternal)
		}
		return nil
	}
	if hasPendingCompaction {
		service.setTurnPhase(stream, TurnPhaseCompacting)
		return nil
	}

	if completion != nil {
		if completion.Disposition == completionDispositionResumeAfterExternal {
			stream.mu.Lock()
			stream.PendingProviderCompletion = nil
			if stream.PendingProviderAction != providerActionStart {
				stream.PendingProviderAction = providerActionResume
			}
			stream.UpdatedAt = time.Now().UTC()
			stream.mu.Unlock()
			action = providerActionResume
		} else {
			clearPendingProviderCompletion(stream)
			if err := service.completeSuccessfulTurn(stream, *completion); err != nil {
				return service.failStreamIfNonTerminal(stream, "unknown", err)
			}
			return nil
		}
	}

	switch action {
	case providerActionStart:
		return service.driveProvider(stream)
	case providerActionResume:
		service.setTurnPhase(stream, TurnPhaseWaitingExternal)
		service.scheduleStreamTimer(stream, providerTimerKey(streamTimerProviderResume, ""), providerResumeDebounce, streamTimerProviderResume, "", 0, "")
		return nil
	default:
		service.setTurnPhase(stream, TurnPhaseIdle)
		return nil
	}
}

func (service *Service) handleProviderEvent(stream *ActiveStream, payload *streamProviderEvent) error {
	if stream == nil || payload == nil {
		return nil
	}
	if !providerTokenMatches(stream, payload.Token) {
		return nil
	}
	if payload.Done {
		return service.handleProviderDoneEvent(stream, payload)
	}
	return service.applyProviderModelEvent(stream, payload.Event)
}

func providerTokenMatches(stream *ActiveStream, token uint64) bool {
	if stream == nil || token == 0 {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.CurrentProviderToken == token
}

func (service *Service) applyProviderModelEvent(stream *ActiveStream, event modeladapter.ModelEvent) error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	requestID := stream.RequestID
	conversationID := stream.ConversationID
	turnSeq := stream.TurnSeq
	modelCallID := stream.CurrentModelCallID
	accumulatedText := stream.ProviderAccumulatedText
	accumulatedReasoning := stream.ProviderAccumulatedReasoning
	accumulatedReasoningSignature := stream.ProviderAccumulatedReasoningSignature
	accumulatedReasoningSignatureSource := stream.ProviderAccumulatedReasoningSignatureSource
	accumulatedReasoningItemID := stream.ProviderAccumulatedReasoningItemID
	accumulatedReasoningStatus := stream.ProviderAccumulatedReasoningStatus
	accumulatedReasoningSummary := append([]byte(nil), stream.ProviderAccumulatedReasoningSummary...)
	stream.mu.Unlock()

	switch event.Kind {
	case modeladapter.ModelEventKindTextDelta:
		stream.mu.Lock()
		stream.ProviderAccumulatedText += event.Text
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		return service.broker.Publish(requestID, StreamEvent{Message: buildTextDeltaMessage(event.Text)})
	case modeladapter.ModelEventKindThinkingDelta:
		stream.mu.Lock()
		stream.ProviderAccumulatedReasoning += event.Text
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		return service.broker.Publish(requestID, StreamEvent{Message: buildThinkingDeltaMessage(event.Text, event.ThinkingStyle)})
	case modeladapter.ModelEventKindThinkingCompleted:
		shouldEmitSyntheticThinking := false
		suppressThinkingCompleted := false
		completedDuration := event.ThinkingDurationMS
		if strings.TrimSpace(event.ThinkingSignature) != "" {
			stream.mu.Lock()
			stream.ProviderAccumulatedReasoningSignature = strings.TrimSpace(event.ThinkingSignature)
			stream.ProviderAccumulatedReasoningSignatureSource = strings.TrimSpace(event.ThinkingSignatureSource)
			stream.ProviderAccumulatedReasoningItemID = strings.TrimSpace(event.ProviderItemID)
			stream.ProviderAccumulatedReasoningStatus = strings.TrimSpace(event.ProviderStatus)
			stream.ProviderAccumulatedReasoningSummary = append([]byte(nil), event.ProviderSummary...)
			shouldEmitSyntheticThinking = strings.TrimSpace(stream.ProviderAccumulatedReasoning) == "" &&
				strings.TrimSpace(event.ThinkingSignatureSource) == modeladapter.ReasoningSignatureSourceOpenAIResponses
			if shouldEmitSyntheticThinking {
				if stream.ProviderSyntheticThinkingStartedAt.IsZero() {
					stream.ProviderSyntheticThinkingStartedAt = time.Now().UTC()
				}
				if completedDuration <= 0 {
					completedDuration = int32(time.Since(stream.ProviderSyntheticThinkingStartedAt).Milliseconds())
					if completedDuration <= 0 {
						completedDuration = 1
					}
				}
				if !stream.ProviderSyntheticThinkingPublished {
					stream.ProviderSyntheticThinkingPublished = true
				} else {
					shouldEmitSyntheticThinking = false
					suppressThinkingCompleted = true
				}
			}
			stream.UpdatedAt = time.Now().UTC()
			stream.mu.Unlock()
		}
		if shouldEmitSyntheticThinking {
			if err := service.broker.Publish(requestID, StreamEvent{Message: buildThinkingDeltaMessage("Thinking is encrypted. Please wait a moment.", event.ThinkingStyle)}); err != nil {
				return err
			}
		}
		if suppressThinkingCompleted {
			return nil
		}
		return service.broker.Publish(requestID, StreamEvent{Message: buildThinkingCompletedMessage(completedDuration)})
	case modeladapter.ModelEventKindPartialToolCall:
		toolCallID := strings.TrimSpace(event.ToolCallID)
		if toolCallID == "" || event.ToolCall == nil {
			return nil
		}
		displayToolCall := service.rewriteTaskToolCallModelForDisplay(stream, event.ToolCall)
		stream.mu.Lock()
		if stream.PartialToolCallIDs == nil {
			stream.PartialToolCallIDs = make(map[string]struct{})
		}
		stream.PartialToolCallIDs[toolCallID] = struct{}{}
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		if inferToolName(displayToolCall) == "GenerateImage" {
			return service.broker.Publish(requestID, StreamEvent{
				Message: buildToolCallStartedMessage(toolCallID, modelCallID, displayToolCall),
			})
		}
		return service.broker.Publish(requestID, StreamEvent{
			Message: buildPartialToolCallMessage(toolCallID, modelCallID, displayToolCall, event.ArgsTextDelta),
		})
	case modeladapter.ModelEventKindToolCallDelta:
		if strings.TrimSpace(event.ToolCallID) == "" || event.ToolCallDelta == nil {
			return nil
		}
		return service.broker.Publish(requestID, StreamEvent{
			Message: buildToolCallDeltaMessage(event.ToolCallID, modelCallID, event.ToolCallDelta),
		})
	case modeladapter.ModelEventKindToolLikeCompleted:
		reasoningForTool := accumulatedReasoning
		reasoningSignatureForTool := accumulatedReasoningSignature
		reasoningSignatureSourceForTool := accumulatedReasoningSignatureSource
		reasoningItemIDForTool := accumulatedReasoningItemID
		reasoningStatusForTool := accumulatedReasoningStatus
		reasoningSummaryForTool := append([]byte(nil), accumulatedReasoningSummary...)
		if strings.TrimSpace(accumulatedText) != "" {
			if err := service.flushAssistantText(stream, conversationID, turnSeq, requestID, accumulatedText, accumulatedReasoning, accumulatedReasoningSignature, accumulatedReasoningSignatureSource, accumulatedReasoningItemID, accumulatedReasoningStatus, accumulatedReasoningSummary, false); err != nil {
				return err
			}
		}
		if event.ToolInvocation == nil {
			return fmt.Errorf("tool invocation is required")
		}
		invocation := *event.ToolInvocation
		invocation.ReasoningContent = reasoningForTool
		invocation.ReasoningSignature = reasoningSignatureForTool
		invocation.ReasoningSignatureSource = reasoningSignatureSourceForTool
		invocation.ReasoningProviderItemID = reasoningItemIDForTool
		invocation.ReasoningProviderStatus = reasoningStatusForTool
		invocation.ReasoningProviderSummary = reasoningSummaryForTool
		invocation.ModelCallID = modelCallID
		stream.mu.Lock()
		stream.ProviderAccumulatedText = ""
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		return service.handleToolInvocation(stream, invocation)
	case modeladapter.ModelEventKindTurnFinished:
		stream.mu.Lock()
		stream.ProviderFinishReason = strings.TrimSpace(event.FinishReason)
		stream.ProviderUsage = turnUsageSnapshot{
			Provider:          event.Provider,
			Model:             event.Model,
			InputTokens:       event.InputTokens,
			OutputTokens:      event.OutputTokens,
			CacheReadTokens:   event.CacheReadTokens,
			CacheWriteTokens:  event.CacheWriteTokens,
			UsagePresent:      event.UsagePresent,
			CacheReadPresent:  event.CacheReadPresent,
			CacheWritePresent: event.CacheWritePresent,
		}
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		return nil
	case modeladapter.ModelEventKindProviderError:
		if event.Err != nil {
			return providerTerminalError{cause: event.Err}
		}
		return providerTerminalError{cause: fmt.Errorf("provider error")}
	default:
		return nil
	}
}

func (service *Service) rewriteTaskToolCallModelForDisplay(stream *ActiveStream, toolCall *agentv1.ToolCall) *agentv1.ToolCall {
	if service == nil || stream == nil || toolCall == nil {
		return toolCall
	}
	taskToolCall := toolCall.GetTaskToolCall()
	if taskToolCall == nil || taskToolCall.GetArgs() == nil {
		return toolCall
	}
	subagentType := taskSubagentTypeNameForDisplay(taskToolCall.GetArgs().GetSubagentType())
	stream.mu.Lock()
	parentModelID := strings.TrimSpace(stream.ModelID)
	overrides := cloneSubagentModelOverrides(stream.SubagentModelOverrides)
	stream.mu.Unlock()
	effectiveModelID := effectiveTaskDisplayModelID(subagentType, parentModelID, overrides)
	if effectiveModelID == "" {
		return toolCall
	}
	cloned, ok := proto.Clone(toolCall).(*agentv1.ToolCall)
	if !ok || cloned == nil {
		return toolCall
	}
	clonedTaskToolCall := cloned.GetTaskToolCall()
	if clonedTaskToolCall == nil || clonedTaskToolCall.GetArgs() == nil {
		return toolCall
	}
	clonedTaskToolCall.Args.Model = &effectiveModelID
	return cloned
}

func taskSubagentTypeNameForDisplay(subagentType *agentv1.SubagentType) string {
	if subagentType == nil || subagentType.GetType() == nil {
		return ""
	}
	switch item := subagentType.GetType().(type) {
	case *agentv1.SubagentType_Explore:
		return "explore"
	case *agentv1.SubagentType_BrowserUse:
		return "browser-use"
	case *agentv1.SubagentType_Shell:
		return "shell"
	case *agentv1.SubagentType_Custom:
		return strings.TrimSpace(item.Custom.GetName())
	default:
		return ""
	}
}

func effectiveTaskDisplayModelID(subagentType string, parentModelID string, overrides map[string]runtimecore.SubagentModelOverrideSelection) string {
	if override, _, ok := runtimecore.LookupSubagentModelOverride(overrides, subagentType); ok {
		switch strings.TrimSpace(override.Selection) {
		case "model":
			return strings.TrimSpace(override.ModelID)
		case "inherit":
			return strings.TrimSpace(parentModelID)
		case "disabled":
			return ""
		}
	}
	return ""
}

func (service *Service) handleProviderDoneEvent(stream *ActiveStream, payload *streamProviderEvent) error {
	if stream == nil || payload == nil {
		return nil
	}

	stream.mu.Lock()
	requestID := stream.RequestID
	conversationID := stream.ConversationID
	turnSeq := stream.TurnSeq
	modelCallID := stream.CurrentModelCallID
	accumulatedText := stream.ProviderAccumulatedText
	accumulatedReasoning := stream.ProviderAccumulatedReasoning
	accumulatedReasoningSignature := stream.ProviderAccumulatedReasoningSignature
	accumulatedReasoningSignatureSource := stream.ProviderAccumulatedReasoningSignatureSource
	accumulatedReasoningItemID := stream.ProviderAccumulatedReasoningItemID
	accumulatedReasoningStatus := stream.ProviderAccumulatedReasoningStatus
	accumulatedReasoningSummary := append([]byte(nil), stream.ProviderAccumulatedReasoningSummary...)
	finishReason := stream.ProviderFinishReason
	usage := stream.ProviderUsage
	hadToolInvocation := stream.ToolInvocationCount > 0
	terminalToolInvocation := stream.ProviderTerminalToolInvocation
	existingCompletion := stream.PendingProviderCompletion
	stream.ProviderActive = false
	stream.ProviderCancel = nil
	stream.PendingProviderAction = providerActionNone
	stream.ProviderAccumulatedText = ""
	stream.ProviderAccumulatedReasoning = ""
	stream.ProviderAccumulatedReasoningSignature = ""
	stream.ProviderAccumulatedReasoningSignatureSource = ""
	stream.ProviderAccumulatedReasoningItemID = ""
	stream.ProviderAccumulatedReasoningStatus = ""
	stream.ProviderAccumulatedReasoningSummary = nil
	stream.ProviderFinishReason = ""
	stream.ProviderUsage = turnUsageSnapshot{}
	stream.ProviderTerminalToolInvocation = false
	stream.ToolInvocationCount = 0
	status := stream.Status
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	if errors.Is(payload.Err, errProviderLoopInterrupted) || isTerminalStreamStatus(status) {
		return nil
	}
	if payload.Err != nil {
		var providerErr providerTerminalError
		if errors.As(payload.Err, &providerErr) {
			service.setTurnPhase(stream, TurnPhaseFailed)
			return service.closeStreamWithProviderError(stream, conversationID, turnSeq, requestID, accumulatedText, accumulatedReasoning, accumulatedReasoningSignature, accumulatedReasoningSignatureSource, accumulatedReasoningItemID, accumulatedReasoningStatus, accumulatedReasoningSummary, usage, providerErr, !hadToolInvocation)
		}
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", payload.Err)
	}
	if err := service.flushAssistantText(stream, conversationID, turnSeq, requestID, accumulatedText, accumulatedReasoning, accumulatedReasoningSignature, accumulatedReasoningSignatureSource, accumulatedReasoningItemID, accumulatedReasoningStatus, accumulatedReasoningSummary, !hadToolInvocation); err != nil {
		return service.failStreamIfNonTerminal(stream, "unknown", err)
	}
	if err := service.recordTurnUsageSnapshot(stream, conversationID, turnSeq, requestID, modelCallID, "completed", usage, "", false); err != nil {
		return service.failStreamIfNonTerminal(stream, "usage_persistence_error", err)
	}
	if err := service.updateConversationTokenState(stream, conversationID, usage, modelCallID, true); err != nil {
		return service.failStreamIfNonTerminal(stream, "unknown", err)
	}
	if err := service.syncSummaryCarryForward(conversationID, requestID, modelCallID); err != nil {
		return service.failStreamIfNonTerminal(stream, "unknown", err)
	}

	pendingCount := pendingBridgeCount(stream)
	if pendingCount > 0 {
		awaitingUser := hasPendingAwaitingUserInteraction(stream)
		forceComplete := awaitingUser
		rememberPendingProviderCompletion(stream, pendingTurnCompletion{
			ConversationID: conversationID,
			RequestID:      requestID,
			TurnSeq:        turnSeq,
			ModelCallID:    modelCallID,
			ProviderPass:   currentProviderPass(stream),
			Usage:          usage,
			Disposition:    completionDispositionForExternalResults(finishReason, forceComplete, hadToolInvocation),
		})
		if awaitingUser {
			service.setTurnPhase(stream, TurnPhaseAwaitingUser)
		} else {
			service.setTurnPhase(stream, TurnPhaseWaitingExternal)
		}
		if err := service.publishCheckpoint(requestID, conversationID); err != nil {
			return service.failStreamIfNonTerminal(stream, "unknown", err)
		}
		return nil
	}

	if existingCompletion == nil {
		handled, err := service.handleSubagentEmptyStopAfterToolResult(stream, conversationID, turnSeq, requestID, modelCallID, finishReason, accumulatedText)
		if err != nil {
			return service.failStreamIfNonTerminal(stream, "unknown", err)
		}
		if handled {
			return nil
		}
	}

	if existingCompletion != nil {
		completion := *existingCompletion
		if strings.TrimSpace(completion.ModelCallID) == "" {
			completion.ModelCallID = modelCallID
		}
		if completion.ProviderPass == 0 {
			completion.ProviderPass = currentProviderPass(stream)
		}
		completion.Usage = usage
		clearPendingProviderCompletion(stream)
		if completion.Disposition == completionDispositionResumeAfterExternal {
			if err := service.publishCheckpoint(requestID, conversationID); err != nil {
				return service.failStreamIfNonTerminal(stream, "unknown", err)
			}
			if err := service.requestProviderAction(stream, providerActionResume); err != nil {
				return service.failStreamIfNonTerminal(stream, "unknown", err)
			}
			return nil
		}
		if err := service.completeSuccessfulTurn(stream, completion); err != nil {
			return service.failStreamIfNonTerminal(stream, "unknown", err)
		}
		return nil
	}

	if (hadToolInvocation || shouldResumeAfterToolResults(finishReason)) && !terminalToolInvocation {
		if err := service.publishCheckpoint(requestID, conversationID); err != nil {
			return service.failStreamIfNonTerminal(stream, "unknown", err)
		}
		if err := service.requestProviderAction(stream, providerActionResume); err != nil {
			return service.failStreamIfNonTerminal(stream, "unknown", err)
		}
		return nil
	}

	clearPendingProviderCompletion(stream)
	if err := service.completeSuccessfulTurn(stream, pendingTurnCompletion{
		ConversationID: conversationID,
		RequestID:      requestID,
		TurnSeq:        turnSeq,
		ModelCallID:    modelCallID,
		ProviderPass:   currentProviderPass(stream),
		Usage:          usage,
	}); err != nil {
		return service.failStreamIfNonTerminal(stream, "unknown", err)
	}
	return nil
}

const subagentEmptyStopErrorText = "subagent returned empty response after tool result"

func (service *Service) handleSubagentEmptyStopAfterToolResult(stream *ActiveStream, conversationID string, turnSeq int64, requestID string, modelCallID string, finishReason string, accumulatedText string) (bool, error) {
	if stream == nil || strings.TrimSpace(finishReason) != "stop" || strings.TrimSpace(accumulatedText) != "" {
		return false, nil
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return true, err
	}
	if conversation == nil || !isChildConversationSubagentTypeName(conversation.SubagentTypeName) || !currentTurnHasToolResult(conversation, turnSeq) {
		return false, nil
	}
	if currentTurnHasPromptContextSource(conversation, turnSeq, promptContextSourceSubagentEmptyStopRecovery) {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return true, service.failStream(stream, "empty_response", errors.New(subagentEmptyStopErrorText))
	}
	context := newPromptContextReminder(promptContextSourceSubagentEmptyStopRecovery, subagentEmptyStopRecoveryText())
	if _, err := service.appendConversationEntries(stream, conversationID, []HistoryEntry{
		newPromptContextEntry(turnSeq, requestID, context),
	}); err != nil {
		return true, err
	}
	if err := service.syncSummaryCarryForward(conversationID, requestID, modelCallID); err != nil {
		return true, err
	}
	if err := service.publishCheckpoint(requestID, conversationID); err != nil {
		return true, err
	}
	if err := service.requestProviderAction(stream, providerActionResume); err != nil {
		return true, err
	}
	return true, nil
}

func subagentEmptyStopRecoveryText() string {
	return "During this subagent turn, a prior provider pass stopped after tool results without visible assistant output. Continue from the latest tool result and return a concise investigation result for the parent. Only call another allowed read-only tool if necessary."
}

func currentTurnHasToolResult(conversation *ConversationFile, turnSeq int64) bool {
	if conversation == nil || turnSeq <= 0 {
		return false
	}
	for _, entry := range conversation.Entries {
		if entry.TurnSeq == turnSeq && strings.TrimSpace(entry.Kind) == "tool_result" {
			return true
		}
	}
	return false
}

func currentTurnHasPromptContextSource(conversation *ConversationFile, turnSeq int64, source string) bool {
	if conversation == nil || turnSeq <= 0 || strings.TrimSpace(source) == "" {
		return false
	}
	for _, entry := range conversation.Entries {
		if entry.TurnSeq != turnSeq || strings.TrimSpace(entry.Kind) != "prompt_context" {
			continue
		}
		var payload promptContextEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		if strings.TrimSpace(payload.Source) == strings.TrimSpace(source) {
			return true
		}
	}
	return false
}

func hasPendingAwaitingUserInteraction(stream *ActiveStream) bool {
	if stream == nil {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	for _, pending := range stream.PendingInteractions {
		if !shouldAutoResumeAfterInteraction(pending) {
			return true
		}
	}
	return false
}

func providerTimerKey(kind streamTimerKind, execID string) string {
	if strings.TrimSpace(execID) == "" {
		return string(kind)
	}
	return string(kind) + ":" + strings.TrimSpace(execID)
}

func (service *Service) scheduleStreamTimer(stream *ActiveStream, key string, delay time.Duration, kind streamTimerKind, execID string, messageID uint32, reason string) {
	if stream == nil || strings.TrimSpace(key) == "" {
		return
	}
	stream.mu.Lock()
	if stream.TimerTokens == nil {
		stream.TimerTokens = make(map[string]uint64)
	}
	stream.TimerTokens[key]++
	token := stream.TimerTokens[key]
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	go func() {
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			<-timer.C
		}
		if err := service.postStreamCommandAsync(stream, streamCommand{
			Kind: streamCommandTimerFired,
			Timer: &streamTimerEvent{
				Key:       key,
				Kind:      kind,
				Token:     token,
				ExecID:    strings.TrimSpace(execID),
				MessageID: messageID,
				Reason:    strings.TrimSpace(reason),
			},
		}); err != nil && !errors.Is(err, errProviderLoopInterrupted) {
			log.Printf("forwarder timer post failed request_id=%s key=%s err=%v", strings.TrimSpace(stream.RequestID), strings.TrimSpace(key), err)
		}
	}()
}

func timerEventMatches(stream *ActiveStream, payload *streamTimerEvent) bool {
	if stream == nil || payload == nil || strings.TrimSpace(payload.Key) == "" {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.TimerTokens[payload.Key] == payload.Token
}

func clearStreamTimer(stream *ActiveStream, key string) {
	if stream == nil || strings.TrimSpace(key) == "" {
		return
	}
	stream.mu.Lock()
	delete(stream.TimerTokens, key)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}

func (service *Service) handleTimerEvent(stream *ActiveStream, payload *streamTimerEvent) error {
	if stream == nil || payload == nil {
		return nil
	}
	if !timerEventMatches(stream, payload) {
		return nil
	}
	clearStreamTimer(stream, payload.Key)

	switch payload.Kind {
	case streamTimerProviderResume:
		stream.mu.Lock()
		providerActive := stream.ProviderActive
		action := stream.PendingProviderAction
		status := stream.Status
		stream.mu.Unlock()
		if providerActive || isTerminalStreamStatus(status) || action != providerActionResume || pendingBridgeCount(stream) > 0 {
			return nil
		}
		return service.driveProvider(stream)
	case streamTimerNonStreamingRecovery:
		current, ok := snapshotPendingExec(stream, payload.ExecID)
		if !ok || current.MessageID != payload.MessageID || current.StreamState != "transport_closed" {
			return nil
		}
		return service.recoverNonStreamingExecAfterStreamClose(stream, current)
	case streamTimerShellForeground:
		return service.recoverShellWithoutTerminalIfNeeded(stream, payload.ExecID, payload.MessageID, shellRecoveryReasonForegroundDeadline)
	case streamTimerShellTransportClose:
		current, status, found := snapshotPendingExecWithStatus(stream, payload.ExecID)
		if !found || current.MessageID != payload.MessageID || current.StreamState != "transport_closed" || isTerminalStreamStatus(status) {
			return nil
		}
		return service.recoverShellWithoutTerminal(stream, current, shellRecoveryReasonTransportClosed)
	case streamTimerOrphanCancel:
		stream.mu.Lock()
		subscriberCount := len(stream.Subscribers)
		status := stream.Status
		stream.mu.Unlock()
		if subscriberCount > 0 || isTerminalStreamStatus(status) {
			return nil
		}
		return service.handleCancelIntent(InboundIntent{
			Kind:         "cancel",
			RequestID:    stream.RequestID,
			CancelReason: firstNonEmpty(payload.Reason, "[canceled] RunSSE client disconnected"),
		})
	default:
		return nil
	}
}

func (service *Service) scheduleOrphanCancelActor(requestID string, reason string) bool {
	if service == nil || service.broker == nil {
		return false
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return false
	}
	stream.mu.Lock()
	placeholder := strings.TrimSpace(stream.ConversationID) == "" &&
		!stream.ProviderActive &&
		len(stream.PendingExecs) == 0 &&
		len(stream.PendingInteractions) == 0 &&
		len(stream.Backlog) == 0
	terminal := isTerminalStreamStatus(stream.Status)
	stream.mu.Unlock()
	if placeholder || terminal {
		return false
	}
	if err := service.postStreamCommandAsync(stream, streamCommand{
		Kind:   streamCommandMaybeOrphaned,
		Reason: firstNonEmpty(strings.TrimSpace(reason), "[canceled] RunSSE client disconnected"),
	}); err != nil {
		return false
	}
	return true
}

func (service *Service) cancelOtherConversationActors(conversationID string, keepRequestID string, reason string) {
	if service == nil || service.broker == nil || strings.TrimSpace(conversationID) == "" {
		return
	}
	for _, requestID := range service.broker.OtherConversationRequestIDs(conversationID, keepRequestID) {
		stream, ok := service.broker.Get(requestID)
		if !ok || stream == nil {
			continue
		}
		if err := service.postStreamCommandWait(stream, streamCommand{
			Kind: streamCommandCancel,
			Intent: InboundIntent{
				Kind:         "cancel",
				RequestID:    requestID,
				CancelReason: reason,
			},
		}); err != nil && !errors.Is(err, errProviderLoopInterrupted) {
			log.Printf("forwarder cancel superseded stream failed request_id=%s err=%v", strings.TrimSpace(requestID), err)
		}
	}
}

func (service *Service) setTurnPhase(stream *ActiveStream, phase TurnPhase) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	stream.Phase = phase
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}

func rememberPendingProviderCompletion(stream *ActiveStream, completion pendingTurnCompletion) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	copy := mergePendingProviderCompletion(stream.PendingProviderCompletion, completion)
	stream.PendingProviderCompletion = &copy
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}

func mergePendingProviderCompletion(existing *pendingTurnCompletion, incoming pendingTurnCompletion) pendingTurnCompletion {
	if existing == nil {
		if incoming.Disposition == completionDispositionNone {
			incoming.Disposition = completionDispositionCompleteAfterExternal
		}
		return incoming
	}
	merged := *existing
	if merged.ConversationID == "" && incoming.ConversationID != "" {
		merged.ConversationID = incoming.ConversationID
	}
	if merged.RequestID == "" && incoming.RequestID != "" {
		merged.RequestID = incoming.RequestID
	}
	if merged.TurnSeq <= 0 && incoming.TurnSeq > 0 {
		merged.TurnSeq = incoming.TurnSeq
	}
	if strings.TrimSpace(merged.ModelCallID) == "" && strings.TrimSpace(incoming.ModelCallID) != "" {
		merged.ModelCallID = incoming.ModelCallID
	}
	if merged.ProviderPass == 0 && incoming.ProviderPass != 0 {
		merged.ProviderPass = incoming.ProviderPass
	}
	if incoming.Usage.hasAny() {
		merged.Usage = incoming.Usage
	}
	merged.Disposition = mergeCompletionDisposition(merged.Disposition, incoming.Disposition)
	return merged
}

func mergeCompletionDisposition(existing pendingCompletionDisposition, incoming pendingCompletionDisposition) pendingCompletionDisposition {
	if existing == completionDispositionCompleteAfterExternal || incoming == completionDispositionCompleteAfterExternal {
		return completionDispositionCompleteAfterExternal
	}
	if existing == completionDispositionResumeAfterExternal || incoming == completionDispositionResumeAfterExternal {
		return completionDispositionResumeAfterExternal
	}
	return completionDispositionCompleteAfterExternal
}

func completionDispositionForExternalResults(finishReason string, forceComplete bool, hadToolInvocation bool) pendingCompletionDisposition {
	if forceComplete {
		return completionDispositionCompleteAfterExternal
	}
	// Some providers may report end_turn even after emitting a valid tool_use block.
	if hadToolInvocation || shouldResumeAfterToolResults(finishReason) {
		return completionDispositionResumeAfterExternal
	}
	return completionDispositionCompleteAfterExternal
}

func clearPendingProviderCompletion(stream *ActiveStream) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	stream.PendingProviderCompletion = nil
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}
