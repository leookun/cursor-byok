// provider_pass.go 提取自 service.go：provider pass 驱动、输出预算、流式收口与记忆落盘。
package forwarder

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"cursor/internal/logger"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	memruntime "cursor/internal/backend/runtime/memory"
	optimize "cursor/internal/backend/runtime/optimize"
	vm "cursor/internal/backend/virtualmodel"
)

func (service *Service) scheduleProviderResume(stream *ActiveStream, _ int) error {
	return service.requestProviderAction(stream, providerActionResume)
}

func shouldResumeAfterToolResults(finishReason string) bool {
	switch strings.TrimSpace(finishReason) {
	case "tool_use", "tool_calls", "function_call":
		return true
	default:
		return false
	}
}

func (service *Service) cancelScheduledProviderResume(stream *ActiveStream) {
	if stream == nil {
		return
	}
	clearStreamTimer(stream, providerTimerKey(streamTimerProviderResume, ""))
}

// driveProvider 由 actor 触发一次 provider pass，并把真实流包装成 provider_event 回投 mailbox。
func (service *Service) driveProvider(stream *ActiveStream) error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	if stream.ProviderActive || stream.Status == StreamStatusCanceled || stream.Status == StreamStatusCompleted || stream.Status == StreamStatusFailed {
		stream.mu.Unlock()
		return nil
	}
	stream.ProviderPassCount++
	currentPass := stream.ProviderPassCount
	stream.Status = StreamStatusStreaming
	stream.PendingProviderAction = providerActionNone
	stream.CurrentModelCallID = uuid.NewString()
	stream.CurrentProviderToken++
	currentToken := stream.CurrentProviderToken
	stream.ProviderAccumulatedText = ""
	stream.ProviderAccumulatedReasoning = ""
	stream.ProviderAccumulatedReasoningSignature = ""
	stream.ProviderAccumulatedReasoningSignatureSource = ""
	stream.ProviderAccumulatedReasoningItemID = ""
	stream.ProviderAccumulatedReasoningStatus = ""
	stream.ProviderAccumulatedReasoningSummary = nil
	if stream.ProviderSyntheticThinkingStartedAt.IsZero() {
		stream.ProviderSyntheticThinkingStartedAt = time.Now().UTC()
	}
	stream.ProviderFinishReason = ""
	stream.ProviderUsage = turnUsageSnapshot{}
	stream.ToolInvocationCount = 0
	modelCallID := stream.CurrentModelCallID
	conversationID := stream.ConversationID
	requestID := stream.RequestID
	modelID := stream.ModelID
	modelName := stream.ModelName
	thinkingEffort := stream.ThinkingEffort
	mode := stream.Mode
	latestUserText := stream.LatestUserText
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	logger.Infof("forwarder provider pass started request_id=%s model_call_id=%s provider_pass=%d", strings.TrimSpace(requestID), strings.TrimSpace(modelCallID), currentPass)

	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, TerminalErrorUnknown, err)
	}
	conversation, err = service.syncConversationContextWindowTokens(stream, conversationID, conversation)
	if err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, TerminalErrorUnknown, err)
	}
	conversation, err = service.persistDerivedPromptContexts(stream, conversationID, requestID, conversation, mode, latestUserText)
	if err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, TerminalErrorUnknown, err)
	}
	compiled, err := service.compiler.Compile(conversation, mode, latestUserText, modelName)
	if err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, TerminalErrorUnknown, err)
	}
	compiled = guardCompiledConversationForProvider(compiled)
	compiled = service.applyContextPostProcess(compiled, latestUserText, modelID)
	if compacted, compactErr := service.maybeCompactBeforeProvider(stream, conversation, compiled); compactErr != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, TerminalErrorUnknown, compactErr)
	} else if compacted {
		stream.mu.Lock()
		stream.ProviderActive = false
		stream.ProviderCancel = nil
		stream.UpdatedAt = time.Now().UTC()
		hasPendingCompaction := stream.PendingCompaction != nil
		status := stream.Status
		stream.mu.Unlock()
		switch {
		case isTerminalStreamStatus(status):
			switch status {
			case StreamStatusCompleted:
				service.setTurnPhase(stream, TurnPhaseCompleted)
			case StreamStatusCanceled:
				service.setTurnPhase(stream, TurnPhaseCanceled)
			default:
				service.setTurnPhase(stream, TurnPhaseFailed)
			}
		case hasPendingCompaction:
			service.setTurnPhase(stream, TurnPhaseCompacting)
		default:
			service.setTurnPhase(stream, TurnPhaseIdle)
		}
		return nil
	}
	if err := service.syncSummarySnapshot(stream, conversation, requestID, modelCallID); err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, TerminalErrorUnknown, err)
	}
	maxTokens, requestKnobs := service.resolveProviderOutputBudget(modelID, conversation, compiled)
	service.maybeSaveLastAgentModelHash(conversation, modelID, mode, currentPass)
	// 使用独立的可取消 context；provider 生命周期通过 stream.ProviderCancel 管理。
	// 注：此处有意不使用上游请求 ctx，因为 provider 流可能跨越多轮工具调用，
	// 需独立于单次 HTTP 请求的生命周期。
	ctx, cancel := context.WithCancel(context.Background())
	stream.mu.Lock()
	// [修复] goroutine泄漏: 覆盖前先终止旧provider goroutine, 防止cancel函数丢失
	if stream.ProviderCancel != nil {
		stream.ProviderCancel()
	}
	stream.ProviderActive = true
	stream.ProviderCancel = cancel
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	service.setTurnPhase(stream, TurnPhaseProviderRunning)

	providerRequest := ProviderRequest{
		RequestID:          requestID,
		ConversationID:     conversationID,
		RunID:              requestID,
		ModelCallID:        modelCallID,
		ModelID:            modelID,
		Mode:               compiled.Mode,
		ThinkingEffort:     compiled.Mode.String(),
		Messages:           compiled.Messages,
		StableMessageCount: compiled.StableMessageCount,
		Tools:              compiled.Tools,
		MaxTokens:          maxTokens,
		RequestKnobs:       requestKnobs,
		CompileSummary:     compiled.CompileSummary,
		Observer:           service.recorder,
		ArtifactPaths:      &modeladapter.LLMArtifactPaths{},
		LatestUserText:     latestUserText,
	}
	providerRequest.ThinkingEffort = thinkingEffort
	service.debug.LogProvider(ctx, requestID, conversationID, "provider_request_prepared", map[string]any{
		"model_call_id":          strings.TrimSpace(modelCallID),
		"provider_pass":          currentPass,
		"model_id":               strings.TrimSpace(modelID),
		"model_name":             strings.TrimSpace(modelName),
		"mode":                   compiled.Mode.String(),
		"thinking_effort":        strings.TrimSpace(thinkingEffort),
		"max_tokens":             maxTokens,
		"request_knobs":          requestKnobs,
		"message_count":          len(compiled.Messages),
		"tool_count":             len(compiled.Tools),
		"compile_summary_length": len(compiled.CompileSummary),
	})
	go service.runProviderStream(stream, currentToken, ctx, providerRequest)
	return nil
}

func (service *Service) resolveProviderOutputBudget(modelID string, conversation *ConversationFile, compiled CompiledConversation) (int, map[string]any) {
	configuredMaxTokens := service.resolveConfiguredProviderMaxOutputTokens(modelID)
	contextWindowTokens := compactionContextWindowSize(conversation)
	estimatedPromptTokens := estimateCompiledPromptTokens(compiled)
	if conversation != nil && int64(conversation.TokenDetailsUsedTokens) > estimatedPromptTokens {
		estimatedPromptTokens = int64(conversation.TokenDetailsUsedTokens)
	}

	// Optimization Runtime: 动态分配 Token Budget（带已编译 prompt 估算）
	var optBudget *optimize.TokenBudget
	if service.optimize != nil {
		modeStr := compiled.Mode.String()
		optBudget = service.optimize.AllocateBudgetWithEstimate(
			context.Background(),
			int(contextWindowTokens),
			modeStr,
			int(estimatedPromptTokens),
		)
		// 使用 Optimization Runtime 计算的 Output Budget（仅当策略启用且给出正值）
		if optBudget != nil && optBudget.OutputTokens > 0 {
			if configuredMaxTokens <= 0 || optBudget.OutputTokens < configuredMaxTokens {
				configuredMaxTokens = optBudget.OutputTokens
			}
		}
	}

	remainingTokens := int64(0)
	requestMaxTokens := int64(configuredMaxTokens)
	if requestMaxTokens <= 0 {
		requestMaxTokens = providerDefaultMaxOutputTokens
	}
	if contextWindowTokens > 0 && estimatedPromptTokens > 0 {
		remainingTokens = contextWindowTokens - estimatedPromptTokens
		allowedTokens := remainingTokens - providerOutputSafetyTokens
		if allowedTokens < 1 {
			allowedTokens = 1
		}
		if allowedTokens < requestMaxTokens {
			requestMaxTokens = allowedTokens
		}
	}
	maxTokens := int(requestMaxTokens)
	if maxTokens <= 0 {
		maxTokens = 1
	}
	requestKnobs := map[string]any{
		"configured_max_tokens":             configuredMaxTokens,
		"dynamic_max_tokens":                maxTokens,
		"compiled_prompt_tokens_estimate":   estimatedPromptTokens,
		"context_window_tokens":             contextWindowTokens,
		"remaining_context_tokens_estimate": remainingTokens,
		"provider_output_safety_tokens":     providerOutputSafetyTokens,
	}
	// 注入 Optimization Runtime 的 Token Budget 详情
	if optBudget != nil {
		requestKnobs["optimize_token_budget"] = map[string]any{
			"system_prompt_tokens": optBudget.SystemPromptTokens,
			"rules_tokens":         optBudget.RulesTokens,
			"memory_tokens":        optBudget.MemoryTokens,
			"history_tokens":       optBudget.HistoryTokens,
			"tools_tokens":         optBudget.ToolsTokens,
			"output_tokens":        optBudget.OutputTokens,
			"total_tokens":         optBudget.TotalTokens,
		}
	}
	return maxTokens, withPreviousCacheFrontierHint(requestKnobs, conversation)
}

func withPreviousCacheFrontierHint(requestKnobs map[string]any, conversation *ConversationFile) map[string]any {
	if len(requestKnobs) == 0 {
		requestKnobs = map[string]any{}
	}
	if conversation == nil || conversation.LatestRequestPrefix == nil {
		return requestKnobs
	}
	prefix := conversation.LatestRequestPrefix
	frontierHash := strings.TrimSpace(prefix.FrontierHash)
	if frontierHash == "" {
		return requestKnobs
	}
	requestKnobs["previous_cache_frontier_hash"] = frontierHash
	requestKnobs["previous_cache_frontier"] = map[string]any{
		"canonical_body_hash": prefix.CanonicalBodyHash,
		"frontier_hash":       frontierHash,
		"frontier_path":       prefix.FrontierPath,
		"breakpoint_count":    prefix.BreakpointCount,
		"request_id":          strings.TrimSpace(prefix.RequestID),
		"model_call_id":       strings.TrimSpace(prefix.ModelCallID),
	}
	return requestKnobs
}

func (service *Service) resolveConfiguredProviderMaxOutputTokens(modelID string) int {
	if service == nil || service.resolver == nil {
		return providerDefaultMaxOutputTokens
	}
	channel, err := service.resolver.SelectChannelForModel(context.Background(), strings.TrimSpace(modelID))
	if err != nil || channel == nil {
		return providerDefaultMaxOutputTokens
	}
	maxTokens := configuredProviderMaxOutputTokens(channel.Provider, channel.MaxTokens, channel.AnthropicMaxTokens)
	if maxTokens <= 0 {
		return providerDefaultMaxOutputTokens
	}
	return maxTokens
}

func configuredProviderMaxOutputTokens(provider string, maxTokens int, anthropicMaxTokens int) int {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		if anthropicMaxTokens > 0 {
			return anthropicMaxTokens
		}
		if maxTokens > 0 {
			return maxTokens
		}
	case "openai":
		if maxTokens > 0 {
			return maxTokens
		}
		if anthropicMaxTokens > 0 {
			return anthropicMaxTokens
		}
	default:
		if maxTokens > 0 && anthropicMaxTokens > 0 {
			if anthropicMaxTokens > maxTokens {
				return anthropicMaxTokens
			}
			return maxTokens
		}
		if maxTokens > 0 {
			return maxTokens
		}
		if anthropicMaxTokens > 0 {
			return anthropicMaxTokens
		}
	}
	return providerDefaultMaxOutputTokens
}

func (service *Service) maybeSaveLastAgentModelHash(conversation *ConversationFile, modelID string, mode agentv1.AgentMode, providerPass int) {
	if service == nil || service.modelMemory == nil || service.resolver == nil {
		return
	}
	if providerPass != 1 || !isSupportedActiveMode(mode) {
		return
	}
	if conversation != nil && strings.TrimSpace(conversation.SubagentTypeName) != "" {
		return
	}
	channel, err := service.resolver.SelectChannelForModel(context.Background(), strings.TrimSpace(modelID))
	if err != nil || channel == nil || strings.TrimSpace(channel.ID) == "" {
		if err != nil {
			logger.Infof("forwarder skipped last agent model hash update model_id=%s error=%v", strings.TrimSpace(modelID), err)
		}
		return
	}
	if err := service.modelMemory.SaveLastAgentModelHash(context.Background(), strings.TrimSpace(channel.ID)); err != nil {
		logger.Infof("forwarder failed to save last agent model hash channel_id=%s error=%v", strings.TrimSpace(channel.ID), err)
	}
}

func (service *Service) persistDerivedPromptContexts(stream *ActiveStream, conversationID string, requestID string, conversation *ConversationFile, mode agentv1.AgentMode, latestUserText string) (*ConversationFile, error) {
	if stream == nil {
		return nil, fmt.Errorf("active stream is required")
	}
	if service == nil || service.compiler == nil {
		return conversation, nil
	}
	contexts, err := service.compiler.DerivePromptContexts(conversation, mode, latestUserText)
	if err != nil {
		return nil, err
	}
	if len(contexts) == 0 {
		return conversation, nil
	}
	stream.mu.Lock()
	turnSeq := stream.TurnSeq
	stream.mu.Unlock()
	if turnSeq <= 0 {
		return conversation, nil
	}
	entries := make([]HistoryEntry, 0, len(contexts))
	for _, context := range contexts {
		context = normalizePromptContextMessage(context)
		if !isReplayablePromptContext(context) {
			continue
		}
		entries = append(entries, newPromptContextEntry(turnSeq, requestID, context))
	}
	if len(entries) == 0 {
		return conversation, nil
	}
	if _, err := service.appendConversationEntries(stream, conversationID, entries); err != nil {
		return nil, err
	}
	conversation, _, _, err = service.snapshotCheckpointConversation(stream)
	return conversation, err
}

func (service *Service) runProviderStream(stream *ActiveStream, token uint64, ctx context.Context, request ProviderRequest) {
	// Phase 26b: inject AOS member spawner into context for cursor_task mode.
	// [修复] 健壮性: panic recover 防止 provider goroutine 崩溃导致进程退出
	defer func() {
		if r := recover(); r != nil {
			logger.Errorf("forwarder provider stream panic recovered request_id=%s err=%v",
				strings.TrimSpace(request.RequestID), r)
			_ = service.failStreamIfNonTerminal(stream, TerminalErrorUnknown,
				fmt.Errorf("provider panic: %v", r))
		}
	}()
	// This allows AOSModel.executeMemberTask to spawn Cursor-native Task tool
	// calls instead of calling callAdapter directly. The spawner wraps
	// Service.EmitMemberSpawn with the current ActiveStream.
	ctx = vm.WithAOSMemberSpawner(ctx, func(taskID, memberID, prompt, modelID, description string) (string, error) {
		req := MemberSpawnRequest{
			TaskID:      taskID,
			MemberID:    memberID,
			Prompt:      prompt,
			ModelID:     modelID,
			Description: description,
		}
		if err := service.EmitMemberSpawn(stream, req); err != nil {
			return "", err
		}
		// Return the deterministic ToolCallId used by EmitMemberSpawn as the AOS
		// result correlation key. This is not the ExecServerMessage.exec_id.
		return fmt.Sprintf("aos-member-%s-%s", taskID, memberID), nil
	})

	// Phase 26c: create per-stream AOS result registry so executeMemberTask
	// can block on spawned Task tool results. The registry is stored on Service
	// (keyed by stream.RequestID) so handleExecResult can Resolve pending
	// results when the client returns the Task tool output.
	aosReg := vm.NewAOSResultRegistry()
	service.aosRegistriesMu.Lock()
	service.aosRegistries[stream.RequestID] = aosReg
	service.aosRegistriesMu.Unlock()
	ctx = vm.WithAOSResultRegistry(ctx, aosReg)
	defer func() {
		service.aosRegistriesMu.Lock()
		delete(service.aosRegistries, stream.RequestID)
		service.aosRegistriesMu.Unlock()
	}()

	// 累积流式文本用于缓存写入；同步收集 provider 上报的 usage 供 Optimization Runtime 记账。
	var accumulatedText strings.Builder
	var usageInputTokens, usageOutputTokens int
	var usagePresent bool
	var usageProvider string
	err := service.provider.StartStream(ctx, request, func(event modeladapter.ModelEvent) error {
		if event.Text != "" {
			accumulatedText.WriteString(event.Text)
		}
		if event.UsagePresent {
			usagePresent = true
			if event.InputTokens > 0 {
				usageInputTokens = int(event.InputTokens)
			}
			if event.OutputTokens > 0 {
				usageOutputTokens = int(event.OutputTokens)
			}
		}
		if p := strings.TrimSpace(event.Provider); p != "" {
			usageProvider = p
		}
		return service.postStreamCommandWait(stream, streamCommand{
			Kind: streamCommandProviderEvent,
			Provider: &streamProviderEvent{
				Token: token,
				Event: event,
			},
		})
	})
	if postErr := service.postStreamCommandWait(stream, streamCommand{
		Kind: streamCommandProviderEvent,
		Provider: &streamProviderEvent{
			Token: token,
			Done:  true,
			Err:   err,
		},
	}); postErr != nil && !errors.Is(postErr, errProviderLoopInterrupted) {
		service.debug.LogProvider(ctx, request.RequestID, request.ConversationID, "provider_completion_post_error", map[string]any{
			"model_call_id":  strings.TrimSpace(request.ModelCallID),
			"provider_token": token,
			"error":          postErr.Error(),
		})
		logger.Infof(
			"forwarder provider completion post failed request_id=%s model_call_id=%s provider_token=%d err=%v",
			strings.TrimSpace(request.RequestID),
			strings.TrimSpace(request.ModelCallID),
			token,
			postErr,
		)
		_ = service.failStreamIfNonTerminal(stream, TerminalErrorUnknown, postErr)
	}
	if err != nil {
		service.debug.LogProvider(ctx, request.RequestID, request.ConversationID, "provider_stream_finished", map[string]any{
			"model_call_id":  strings.TrimSpace(request.ModelCallID),
			"provider_token": token,
			"error":          err.Error(),
		})
		return
	}

	// Cache Runtime: 将结果写入缓存
	resultText := accumulatedText.String()
	promptTokensEst := 0
	for _, msg := range request.Messages {
		promptTokensEst += len(msg.Content) / 4
	}
	outputTokensEst := len(resultText) / 4
	if resultText != "" {
		if gw, ok := service.provider.(*DefaultProviderGateway); ok {
			storeIn := promptTokensEst
			storeOut := outputTokensEst
			if usagePresent {
				if usageInputTokens > 0 {
					storeIn = usageInputTokens
				}
				if usageOutputTokens > 0 {
					storeOut = usageOutputTokens
				}
			}
			gw.CacheStore(request, resultText, storeIn, storeOut)
		}
	}

	// Optimization Runtime: 优先使用 provider 真实 usage，否则回退字符估算
	costPrompt := promptTokensEst
	costOutput := outputTokensEst
	if usagePresent {
		if usageInputTokens > 0 {
			costPrompt = usageInputTokens
		}
		if usageOutputTokens > 0 {
			costOutput = usageOutputTokens
		}
	}
	service.recordProviderCost(request, usageProvider, costPrompt, costOutput)
	service.recordSessionMemory(request, accumulatedText.String())
	service.recordLongMemory(request, accumulatedText.String())
	service.debug.LogProvider(ctx, request.RequestID, request.ConversationID, "provider_stream_finished", map[string]any{
		"model_call_id":  strings.TrimSpace(request.ModelCallID),
		"provider_token": token,
		"usage_present":  usagePresent,
		"cost_prompt":    costPrompt,
		"cost_output":    costOutput,
	})
}

// recordSessionMemory writes a session-level memory entry from the completed provider turn.
// This enables cross-turn memory recall in subsequent PostProcess calls.
func (service *Service) recordSessionMemory(request ProviderRequest, accumulatedText string) {
	if service == nil || service.contextRuntime == nil {
		return
	}
	text := strings.TrimSpace(accumulatedText)
	if text == "" {
		return
	}
	// Truncate to avoid storing overly long responses
	maxLen := 2000
	if len(text) > maxLen {
		text = text[:maxLen] + "..."
	}
	mm := service.contextRuntime.MemoryManager()
	if mm == nil {
		return
	}
	_ = mm.Remember(context.Background(), &memruntime.Entry{
		Layer:   memruntime.LayerSession,
		Content: text,
		Source:  strings.TrimSpace(request.ConversationID),
		Tags:    []string{"assistant_response", strings.TrimSpace(request.ModelID)},
	})
}

// recordLongMemory writes a long-term memory entry from the completed provider turn.
// Unlike Session Memory (which stores the full response), Long Memory stores a summary
// suitable for cross-session semantic retrieval via embedding search (ADR-012/023).
// Only responses above a minimum length are stored to avoid polluting the index with
// trivial acknowledgements.
func (service *Service) recordLongMemory(request ProviderRequest, accumulatedText string) {
	if service == nil || service.contextRuntime == nil {
		return
	}
	text := strings.TrimSpace(accumulatedText)
	if len(text) < 500 {
		return // skip short responses that don't carry meaningful knowledge
	}
	mm := service.contextRuntime.MemoryManager()
	if mm == nil {
		return
	}
	// Summary: first 500 chars of the response as a searchable snippet
	summary := text
	if len(summary) > 500 {
		summary = summary[:500] + "..."
	}
	_ = mm.Remember(context.Background(), &memruntime.Entry{
		Layer:   memruntime.LayerLong,
		Content: text,
		Summary: summary,
		Source:  strings.TrimSpace(request.ConversationID),
		Tags:    []string{"assistant_response", strings.TrimSpace(request.ModelID)},
	})
}
