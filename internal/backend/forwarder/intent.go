// intent.go 提取自 service.go：inbound intent 解码与各 kind 的处理（run/cancel/exec/metadata）。
package forwarder

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cursor/internal/logger"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	vm "cursor/internal/backend/virtualmodel"
)

func shouldAcknowledgeInterruptedInboundIntent(intent InboundIntent, err error) bool {
	if !errors.Is(err, errProviderLoopInterrupted) {
		return false
	}
	switch strings.TrimSpace(intent.Kind) {
	case "metadata", "kv_result", "exec_result", "exec_control", "interaction_result", "cancel":
		return true
	default:
		return false
	}
}

// decodeInboundIntent 把 legacy AgentClientMessage 映射为 forwarder 内部 intent。
func (service *Service) decodeInboundIntent(requestID string, message *agentv1.AgentClientMessage, clientKind string) (InboundIntent, error) {
	intent := InboundIntent{
		RequestID:     strings.TrimSpace(requestID),
		ClientMessage: message,
	}
	var err error
	switch strings.TrimSpace(clientKind) {
	case "run_request":
		runRequest := message.GetRunRequest()
		if runRequest == nil {
			return InboundIntent{}, fmt.Errorf("run_request payload is required")
		}
		conversationID := strings.TrimSpace(runRequest.GetConversationId())
		if conversationID == "" {
			return InboundIntent{}, fmt.Errorf("conversation_id is required in run_request")
		}
		intent.ConversationID = conversationID
		intent.ConversationState = runRequest.GetConversationState()
		intent.UserMessage = extractUserMessage(message)
		intent.RequestContext = extractRequestContext(message)
		if service.shouldIgnoreEmptyResumeRunRequest(requestID, runRequest, intent.UserMessage, intent.RequestContext) {
			intent.Kind = "metadata"
			intent.StartsRun = false
			intent.HasExplicitMode = false
			intent.ModeSource = ModeSourceUnknown
			intent.IgnoredReason = "empty_resume_without_pending_continuation"
			return intent, nil
		}
		intent.Kind = "run"
		intent.StartsRun = true
		intent.Mode, intent.ModeSource, intent.HasExplicitMode, err = extractRunMode(message)
		if err != nil {
			return InboundIntent{}, err
		}
		intent.ModelID = extractRequestedModelID(message)
		intent.ThinkingEffort = extractRuntimeThinkingEffort(message)
		intent.SubagentTypeName = strings.TrimSpace(runRequest.GetSubagentTypeName())
		parsedOverrides := parseSubagentModelOverrides(runRequest.GetSubagentModelOverrides())
		intent.SubagentModelOverrides = parsedOverrides.Overrides
		service.debug.LogRuntime(context.Background(), intent.RequestID, intent.ConversationID, "subagent_model_overrides_parsed", map[string]any{
			"override_count": parsedOverrides.RawCount,
			"valid_count":    len(parsedOverrides.Overrides),
			"ignored_count":  len(parsedOverrides.Ignored),
			"overrides":      subagentModelOverrideSummaries(parsedOverrides.Overrides),
			"ignored":        parsedOverrides.Ignored,
		})
		if intent.ModelID == "" {
			intent.ModelID = "default"
		}
		intent.ModelName = service.resolveRequestedModelName(message, intent.ModelID)
	case "prewarm_request":
		prewarmRequest := message.GetPrewarmRequest()
		if prewarmRequest == nil {
			return InboundIntent{}, fmt.Errorf("prewarm_request payload is required")
		}
		conversationID := strings.TrimSpace(prewarmRequest.GetConversationId())
		if conversationID == "" {
			return InboundIntent{}, fmt.Errorf("conversation_id is required in prewarm_request")
		}
		intent.Kind = "run"
		intent.Prewarm = true
		intent.StartsRun = true
		intent.ConversationID = conversationID
		intent.SubagentTypeName = strings.TrimSpace(prewarmRequest.GetSubagentTypeName())
		intent.ConversationState = prewarmRequest.GetConversationState()
		intent.Mode, intent.ModeSource, intent.HasExplicitMode, err = extractPrewarmMode(prewarmRequest)
		if err != nil {
			return InboundIntent{}, err
		}
		intent.ModelID = firstNonEmpty(extractRequestedModelID(message), "default")
		intent.ThinkingEffort = extractRuntimeThinkingEffort(message)
		intent.ModelName = service.resolveRequestedModelName(message, intent.ModelID)
	case "conversation_action":
		action := message.GetConversationAction()
		if action == nil {
			return InboundIntent{}, fmt.Errorf("conversation_action payload is required")
		}
		intent.UserMessage = extractConversationActionUserMessage(action)
		intent.RequestContext = extractConversationActionRequestContext(action)
		intent.StartsRun = conversationActionStartsRun(action)
		intent.Mode, intent.ModeSource, intent.HasExplicitMode, err = extractConversationActionMode(action)
		if err != nil {
			return InboundIntent{}, err
		}
		switch item := action.GetAction().(type) {
		case *agentv1.ConversationAction_CancelAction:
			intent.Kind = "cancel"
			intent.CancelReason = strings.TrimSpace(item.CancelAction.GetReason())
		default:
			if intent.StartsRun || intent.HasExplicitMode {
				if stream, ok := service.broker.Get(intent.RequestID); ok && stream != nil {
					stream.mu.Lock()
					intent.ConversationID = strings.TrimSpace(stream.ConversationID)
					intent.ModelID = strings.TrimSpace(stream.ModelID)
					intent.ModelName = strings.TrimSpace(stream.ModelName)
					intent.ThinkingEffort = strings.TrimSpace(stream.ThinkingEffort)
					if !intent.HasExplicitMode && stream.Mode != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
						intent.Mode = stream.Mode
					}
					if stream.CheckpointConversation != nil {
						intent.SubagentTypeName = strings.TrimSpace(stream.CheckpointConversation.SubagentTypeName)
					}
					stream.mu.Unlock()
				}
				if strings.TrimSpace(intent.ConversationID) == "" {
					return InboundIntent{}, fmt.Errorf("conversation_action requires active request context")
				}
			}
			if intent.StartsRun {
				intent.Kind = "run"
				intent.StartsRun = true
				if intent.ModelID == "" {
					intent.ModelID = "default"
				}
			} else {
				intent.Kind = "metadata"
			}
		}
	case "exec_client_message":
		intent.Kind = "exec_result"
		intent.ExecClientMessage = message.GetExecClientMessage()
	case "exec_client_control_message":
		intent.Kind = "exec_control"
		intent.ExecClientControlMessage = message.GetExecClientControlMessage()
	case "interaction_response":
		intent.Kind = "interaction_result"
		intent.InteractionResponse = message.GetInteractionResponse()
	case "kv_client_message":
		intent.Kind = "kv_result"
		intent.KVClientMessage = message.GetKvClientMessage()
	case "client_heartbeat":
		intent.Kind = "metadata"
	default:
		return InboundIntent{}, fmt.Errorf("unsupported client message kind: %s", clientKind)
	}
	return intent, nil
}

// handleRunIntent 处理 run/prewarm 类 intent，负责建会话、写 turn 和拉起 provider。
func (service *Service) handleRunIntent(intent InboundIntent) error {
	intent.UserMessage = normalizeUserMessageForStorage(intent.UserMessage)
	if !intent.Prewarm {
		service.cancelOtherConversationActors(
			intent.ConversationID,
			intent.RequestID,
			"[canceled] Superseded by newer request",
		)
	}
	conversation, effectiveMode, turnSeq, initialEntries, err := service.bootstrapRuntimeConversation(intent)
	if err != nil {
		return err
	}
	rewindDecision := service.decideRunRewind(intent, conversation)
	if rewindDecision.Evaluated && !rewindDecision.Apply {
		service.logRunRewindDecision(intent.RequestID, intent.ConversationID, "rewind_skipped", rewindDecision)
	}
	if rewindDecision.Apply {
		service.logRunRewindDecision(intent.RequestID, intent.ConversationID, "rewind_detected", rewindDecision)
		turnSeq = rewindDecision.TargetTurnSeq
		initialEntries, err = buildRunEntries(intent, effectiveMode, turnSeq)
		if err != nil {
			return err
		}
	}
	if service.store != nil {
		if rewindDecision.Apply {
			persisted, err := service.store.ReplaceEntries(
				intent.ConversationID,
				appendReplacementRunEntries(rewindDecision.PrefixEntries, initialEntries),
				func(item *ConversationFile) error {
					applyRunRewindMetadata(item, conversation, intent, turnSeq)
					return nil
				},
			)
			if err != nil {
				return err
			}
			if persisted != nil {
				conversation = persisted
			}
			service.logRunRewindDecision(intent.RequestID, intent.ConversationID, "rewind_applied", rewindDecision)
		} else {
			persisted, err := service.store.SaveConversationWithEntries(intent.ConversationID, conversation, initialEntries)
			if err != nil {
				return err
			}
			if persisted != nil {
				conversation = persisted
			}
		}
	} else if rewindDecision.Apply {
		service.applyRunRewindToConversation(conversation, rewindDecision, initialEntries, intent, turnSeq)
		service.logRunRewindDecision(intent.RequestID, intent.ConversationID, "rewind_applied", rewindDecision)
	} else if len(initialEntries) > 0 {
		appendEntriesInPlace(conversation, initialEntries)
		deriveConversationLoopState(conversation)
	}

	stream, err := service.broker.OpenStream(intent.RequestID, intent.ConversationID, turnSeq, intent.ModelID, intent.ModelName, effectiveMode, userMessageText(intent.UserMessage))
	if err != nil {
		return err
	}
	if stream == nil {
		return fmt.Errorf("open stream failed")
	}
	if err := service.replaceCheckpointConversation(stream, conversation); err != nil {
		return err
	}
	updateStreamRequestContextData(stream, intent.RequestContext)
	service.updateStreamMCPToolServers(stream, intent.RequestContext)
	clearPendingProviderCompletion(stream)
	stream.mu.Lock()
	stream.ThinkingEffort = strings.TrimSpace(intent.ThinkingEffort)
	stream.SubagentModelOverrides = cloneSubagentModelOverrides(intent.SubagentModelOverrides)
	stream.PendingProviderAction = providerActionNone
	stream.PendingCompaction = nil
	stream.PendingExecs = make(map[string]runtimecore.PendingExec)
	stream.PendingInteractions = make(map[string]runtimecore.PendingInteraction)
	stream.RecentCompletedExecs = make(map[uint32]time.Time)
	stream.BackgroundShells = make(map[string]*BackgroundShellState)
	stream.BackgroundShellsByMessageID = make(map[uint32]string)
	stream.BackgroundShellsByExecID = make(map[string]string)
	stream.TimerTokens = make(map[string]uint64)
	stream.CurrentProviderToken = 0
	stream.CurrentCompactionToken = 0
	stream.ProviderAccumulatedText = ""
	stream.ProviderAccumulatedReasoning = ""
	stream.ProviderAccumulatedReasoningSignature = ""
	stream.ProviderAccumulatedReasoningSignatureSource = ""
	stream.ProviderAccumulatedReasoningItemID = ""
	stream.ProviderAccumulatedReasoningStatus = ""
	stream.ProviderAccumulatedReasoningSummary = nil
	stream.ProviderSyntheticThinkingStartedAt = time.Time{}
	stream.ProviderSyntheticThinkingPublished = false
	stream.ProviderFinishReason = ""
	stream.ProviderUsage = turnUsageSnapshot{}
	stream.ToolInvocationCount = 0
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	service.setTurnPhase(stream, TurnPhaseIdle)
	service.debug.LogRuntime(context.Background(), intent.RequestID, intent.ConversationID, "stream_state_updated", map[string]any{
		"turn_seq":                      turnSeq,
		"model_id":                      strings.TrimSpace(intent.ModelID),
		"model_name":                    strings.TrimSpace(intent.ModelName),
		"thinking_effort":               strings.TrimSpace(intent.ThinkingEffort),
		"mode":                          effectiveMode.String(),
		"prewarm":                       intent.Prewarm,
		"subagent_type":                 strings.TrimSpace(intent.SubagentTypeName),
		"subagent_model_override_count": len(intent.SubagentModelOverrides),
		"subagent_model_overrides":      subagentModelOverrideSummaries(intent.SubagentModelOverrides),
		"latest_user_text":              userMessageText(intent.UserMessage),
	})
	if err := service.publishCheckpoint(intent.RequestID, intent.ConversationID); err != nil {
		return err
	}
	if intent.Prewarm {
		return nil
	}
	return service.requestProviderAction(stream, providerActionStart)
}

func (service *Service) loadPreviousSummaryReplay(conversationID string) ([][]byte, bool, error) {
	if service == nil || strings.TrimSpace(conversationID) == "" {
		return nil, false, nil
	}
	return service.loadLatestCarryForwardReplay(conversationID)
}

func (service *Service) snapshotVisibleTurns(conversation *ConversationFile) ([][]byte, error) {
	if service == nil || service.projector == nil || conversation == nil {
		return nil, nil
	}
	state, err := service.projector.ProjectLegacyCheckpoint(conversation)
	if err != nil {
		return nil, err
	}
	return cloneByteSlices(state.GetTurns()), nil
}

// handleCancelIntent 处理取消请求，并向客户端发送执行桥 abort。
func (service *Service) handleCancelIntent(intent InboundIntent) error {
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", intent.RequestID)
	}
	hasCheckpoint := checkpointConversationInitialized(stream)
	if hasCheckpoint {
		cancelReason := firstNonEmpty(intent.CancelReason, "user aborted")
		_, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
			newMetadataEntry(stream.TurnSeq, intent.RequestID, "control", map[string]any{
				"status":        "canceled",
				"reason":        cancelReason,
				"replay_policy": cancelReplayPolicyForReason(cancelReason),
			}),
		})
		if err != nil {
			return err
		}
	}
	stream.mu.Lock()
	pendingExecs := make([]runtimecore.PendingExec, 0, len(stream.PendingExecs))
	for _, pending := range stream.PendingExecs {
		pendingExecs = append(pendingExecs, pending)
	}
	stream.mu.Unlock()
	for _, pending := range pendingExecs {
		if err := service.broker.Publish(intent.RequestID, StreamEvent{
			Message: buildExecAbortMessage(pending),
		}); err != nil {
			logger.Warnf("service: broker publish failed for request %s: %v", intent.RequestID, err)
		}
	}
	if hasCheckpoint {
		if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
			return err
		}
	}
	clearPendingProviderCompletion(stream)
	stream.mu.Lock()
	stream.PendingProviderAction = providerActionNone
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	service.setTurnPhase(stream, TurnPhaseCanceled)
	return service.broker.Cancel(intent.RequestID, firstNonEmpty(intent.CancelReason, "[canceled] User aborted request"))
}

// handleExecResult 处理客户端返回的执行桥结果，并在终态时把 tool_result 写回 history。
func (service *Service) handleExecResult(intent InboundIntent) error {
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", intent.RequestID)
	}
	if intent.ExecClientMessage == nil {
		return fmt.Errorf("exec client message is required")
	}
	pending, found := selectPendingExec(intent.ExecClientMessage.GetExecId(), intent.ExecClientMessage.GetId(), stream)
	if !found {
		if service.observeMissingBackgroundShellExecClientMessage(stream, intent.ExecClientMessage) {
			return nil
		}
		if service.observeMissingShellExecClientMessage(stream, intent.ExecClientMessage) {
			return nil
		}
		if shouldIgnoreMissingExecResult(intent.ExecClientMessage, stream) {
			return nil
		}
		return fmt.Errorf("pending exec not found")
	}
	service.observeBackgroundShellExecClientMessage(stream, pending, intent.ExecClientMessage)
	service.observeShellExecClientMessage(stream, pending, intent.ExecClientMessage)
	pending = service.applyExecProgress(stream, pending, intent.ExecClientMessage)
	if isHiddenPatchEditExecKind(pending.ExecKind) {
		return service.handleHiddenPatchEditExecResult(stream, pending, intent.ExecClientMessage)
	}
	if isHiddenWriteExecKind(pending.ExecKind) {
		return service.handleHiddenWriteExecResult(stream, pending, intent.ExecClientMessage)
	}
	result, err := service.execBridge.ApplyExecClientMessage(intent.ExecClientMessage, pending)
	if err != nil {
		return err
	}
	if result.ShellOutputDelta != nil {
		if err := service.broker.Publish(intent.RequestID, StreamEvent{
			Message: buildShellOutputDeltaMessage(result.ShellOutputDelta),
		}); err != nil {
			return err
		}
	}
	if !result.IsTerminal {
		return nil
	}
	markExecCompleted(stream, pending)
	backgroundShellToolCallID := ""
	if strings.TrimSpace(pending.ExecKind) == "shell" && shellToolCallIsBackgrounded(result.ToolCall) {
		backgroundShellToolCallID = firstNonEmpty(strings.TrimSpace(result.ToolCallID), strings.TrimSpace(pending.ToolCallID))
	}
	if strings.TrimSpace(pending.ExecKind) == "execute_hook_pre_compact" {
		return service.handlePreCompactTerminal(stream, pending.ProviderPass, strings.TrimSpace(result.ToolResultPayload))
	}
	if result.ToolCall != nil {
		if err := service.appendToolResult(stream, result.ToolCallID, deriveToolNameFromPendingExec(pending), pending.ArgsJSON, result.ToolResultPayload, pending.ReasoningContent, result.ToolCall); err != nil {
			return err
		}
	} else if strings.TrimSpace(result.ToolResultPayload) != "" {
		if err := service.appendToolResult(stream, pending.ToolCallID, deriveToolNameFromPendingExec(pending), pending.ArgsJSON, result.ToolResultPayload, pending.ReasoningContent, nil); err != nil {
			return err
		}
	}
	if backgroundShellToolCallID != "" {
		if recordedToolCallID, recorded := recordBackgroundShellActionMemory(stream, backgroundShellToolCallID, time.Now().UTC()); recorded {
			if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
				newBackgroundShellActionMetadataEntry(stream.TurnSeq, stream.RequestID, recordedToolCallID, backgroundShellActionSourceLocalBackgrounded),
			}); err != nil {
				return err
			}
		}
	}

	// Phase 26c: resolve pending AOS member Task results when the spawned
	// tool call completes. We check the pending exec's ToolCallID for the
	// "aos-member-" prefix set by EmitMemberSpawn, then Resolve the result
	// through the per-stream registry so executeMemberTask's waiting select
	// picks it up.
	toolCallIDForResult := firstNonEmpty(strings.TrimSpace(result.ToolCallID), strings.TrimSpace(pending.ToolCallID))
	if strings.HasPrefix(toolCallIDForResult, "aos-member-") {
		service.aosRegistriesMu.Lock()
		aosReg := service.aosRegistries[stream.RequestID]
		service.aosRegistriesMu.Unlock()
		resolveAOSMemberTaskResult(aosReg, toolCallIDForResult, intent.ExecClientMessage, result.ToolResultPayload)
	}

	if err := service.publishToolCallCompleted(intent.RequestID, result.ToolCallID, pending.ModelCallID, result.ToolCall); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, intent.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishCheckpoint(intent.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}

// resolveAOSMemberTaskResult forwards a Cursor Task result to an AOS member
// waiter. A subagent error is terminal for the member task rather than output
// the AOS scheduler can safely treat as a successful response.
func resolveAOSMemberTaskResult(registry *vm.AOSResultRegistry, execID string, message *agentv1.ExecClientMessage, payload string) {
	if registry == nil {
		return
	}

	var resultErr error
	if subagentErr := message.GetSubagentResult().GetError(); subagentErr != nil {
		errText := strings.TrimSpace(subagentErr.GetError())
		if errText == "" {
			errText = "subagent reported an error"
		}
		resultErr = errors.New(errText)
	}

	registry.Resolve(execID, strings.TrimSpace(payload), resultErr)
}

// handleExecControl 处理执行桥控制面结果，例如 stream_close 或 throw。
func (service *Service) handleExecControl(intent InboundIntent) error {
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok || stream == nil {
		if shouldIgnoreStaleExecControl(intent.ExecClientControlMessage) {
			return nil
		}
		return fmt.Errorf("request is not active: %s", intent.RequestID)
	}
	if intent.ExecClientControlMessage == nil {
		return fmt.Errorf("exec client control message is required")
	}
	pending, found := selectPendingExecByControl(intent.ExecClientControlMessage, stream)
	if !found {
		if shouldIgnoreMissingExecControl(intent.ExecClientControlMessage, stream) {
			return nil
		}
		return fmt.Errorf("pending exec not found for control message")
	}
	pending = service.applyExecControlProgress(stream, pending, intent.ExecClientControlMessage)
	if isHiddenPatchEditExecKind(pending.ExecKind) {
		return service.handleHiddenPatchEditExecControl(stream, pending, intent.ExecClientControlMessage)
	}
	if isHiddenWriteExecKind(pending.ExecKind) {
		return service.handleHiddenWriteExecControl(stream, pending, intent.ExecClientControlMessage)
	}
	result, err := service.execBridge.ApplyExecClientControl(intent.ExecClientControlMessage, pending)
	if err != nil {
		return err
	}
	if !result.IsTerminal {
		if shouldRecoverNonStreamingExecOnStreamClose(intent.ExecClientControlMessage, pending) {
			markExecTransportClosed(stream, pending)
			service.scheduleNonStreamingExecRecovery(intent.RequestID, pending)
			return nil
		}
		if shouldObserveShellStreamClose(intent.ExecClientControlMessage, pending) {
			service.observeShellStreamClose(stream, pending)
		}
		return nil
	}
	markExecCompleted(stream, pending)
	if strings.TrimSpace(pending.ExecKind) == "execute_hook_pre_compact" {
		return service.handlePreCompactTerminal(stream, pending.ProviderPass, "")
	}
	if strings.TrimSpace(result.ToolResultPayload) != "" {
		if err := service.appendToolResult(stream, pending.ToolCallID, deriveToolNameFromPendingExec(pending), pending.ArgsJSON, result.ToolResultPayload, pending.ReasoningContent, nil); err != nil {
			return err
		}
		_, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
			newMetadataEntry(stream.TurnSeq, stream.RequestID, "tool_control", map[string]any{
				"tool_call_id": result.ToolCallID,
				"payload":      result.ToolResultPayload,
			}),
		})
		if err != nil {
			return err
		}
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, intent.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishToolCallCompleted(intent.RequestID, result.ToolCallID, pending.ModelCallID, nil); err != nil {
		return err
	}
	if err := service.publishCheckpoint(intent.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}


// handleMetadataIntent 处理当前不驱动 provider 的轻量元数据上行。
func (service *Service) handleMetadataIntent(intent InboundIntent) error {
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok || stream == nil {
		if intent.HasExplicitMode || intent.StartsRun {
			return fmt.Errorf("metadata intent requires active request context: %s", intent.RequestID)
		}
		return nil
	}
	backgroundShellToolCallID, backgroundShellActionWasNew := observeBackgroundShellAction(stream, intent.ClientMessage)
	observeBackgroundTaskCompletionAction(stream, intent.ClientMessage)
	if !checkpointConversationInitialized(stream) {
		if intent.HasExplicitMode {
			stream.mu.Lock()
			stream.Mode = intent.Mode
			stream.UpdatedAt = time.Now().UTC()
			stream.mu.Unlock()
		}
		return nil
	}
	entries := []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "metadata", map[string]any{
			"kind":       intent.Kind,
			"starts_run": intent.StartsRun,
		}),
	}
	if backgroundShellToolCallID != "" && backgroundShellActionWasNew {
		entries = append(entries, newBackgroundShellActionMetadataEntry(stream.TurnSeq, stream.RequestID, backgroundShellToolCallID, backgroundShellActionSourceClient))
	}
	entries = append(entries, backgroundTaskCompletionMetadataEntries(stream.TurnSeq, stream.RequestID, intent.ClientMessage)...)
	if intent.HasExplicitMode {
		modeEntry, err := newModeMetadataEntry(stream.TurnSeq, stream.RequestID, intent.Mode, true, intent.ModeSource)
		if err != nil {
			return err
		}
		modeAliasValue, err := modeAlias(intent.Mode)
		if err != nil {
			return err
		}
		entries = append(entries, modeEntry, newModeChangePromptContextEntry(stream.TurnSeq, stream.RequestID, intent.Mode))
		stream.mu.Lock()
		stream.Mode = intent.Mode
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		if _, err := service.updateConversationMetaAndCheckpoint(stream, stream.ConversationID, func(item *ConversationFile) error {
			if item == nil {
				return nil
			}
			item.Mode = modeAliasValue
			return nil
		}); err != nil {
			return err
		}
	}
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, entries); err != nil {
		return err
	}
	if intent.HasExplicitMode {
		stream.mu.Lock()
		modelCallID := strings.TrimSpace(stream.CurrentModelCallID)
		stream.mu.Unlock()
		if modelCallID != "" {
			if err := service.syncSummaryCarryForward(stream.ConversationID, intent.RequestID, modelCallID); err != nil {
				return err
			}
		}
		if err := service.publishCheckpoint(intent.RequestID, stream.ConversationID); err != nil {
			return err
		}
	}
	return nil
}
