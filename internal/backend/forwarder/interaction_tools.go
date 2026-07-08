package forwarder

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func (service *Service) handleInteractionToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	if service == nil || stream == nil {
		return fmt.Errorf("active stream is required")
	}
	serverMessage, pendingInteraction, err := service.interactionBridge.OpenQuery(invocation)
	if err != nil {
		return newRecoverableToolInvocationError(err)
	}
	pendingInteraction.ModelCallID = invocation.ModelCallID
	pendingInteraction.ReasoningContent = invocation.ReasoningContent
	pendingInteraction.ReasoningSignature = invocation.ReasoningSignature
	pendingInteraction.ReasoningSignatureSource = invocation.ReasoningSignatureSource
	if pendingInteraction.OpenedAt.IsZero() {
		pendingInteraction.OpenedAt = time.Now().UTC()
	}

	stream.mu.Lock()
	if stream.PendingInteractions == nil {
		stream.PendingInteractions = make(map[string]runtimecore.PendingInteraction)
	}
	pendingInteraction.ProviderPass = stream.ProviderPassCount
	stream.PendingInteractions[pendingInteraction.InteractionID] = pendingInteraction
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	removePending := func() {
		stream.mu.Lock()
		delete(stream.PendingInteractions, pendingInteraction.InteractionID)
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		removePending()
		return err
	}
	if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: serverMessage}); err != nil {
		removePending()
		return err
	}
	return nil
}

func (service *Service) handleInteractionResult(intent InboundIntent) error {
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", intent.RequestID)
	}
	if intent.InteractionResponse == nil {
		return fmt.Errorf("interaction response is required")
	}
	pending, found := selectPendingInteraction(intent.InteractionResponse, stream)
	if !found {
		return fmt.Errorf("pending interaction not found")
	}
	result, err := service.interactionBridge.ApplyInteractionResponse(intent.InteractionResponse, pending)
	if err != nil {
		return err
	}
	markInteractionCompleted(stream, pending)
	toolName := strings.TrimSpace(deriveToolNameFromPendingInteraction(pending))
	if result.ToolCall != nil {
		applySwitchModeMetadata(stream, result.ToolCall)
		if err := service.appendToolResult(stream, result.ToolCallID, toolName, pending.ArgsJSON, result.ToolResultPayload, pending.ReasoningContent, result.ToolCall); err != nil {
			return err
		}
	} else if strings.TrimSpace(result.ToolResultPayload) != "" {
		if err := service.appendToolResult(stream, pending.ToolCallID, toolName, pending.ArgsJSON, result.ToolResultPayload, pending.ReasoningContent, nil); err != nil {
			return err
		}
	}
	if err := service.publishToolCallCompleted(intent.RequestID, result.ToolCallID, pending.ModelCallID, result.ToolCall); err != nil {
		return err
	}
	if err := service.applyApprovedSwitchMode(stream, stream.ConversationID, result.ToolCall); err != nil {
		return err
	}
	if switchToolCall := result.ToolCall.GetSwitchModeToolCall(); switchToolCall != nil && switchToolCall.GetResult().GetSuccess() != nil {
		targetMode, err := switchModeTarget(switchToolCall.GetArgs())
		if err != nil {
			return err
		}
		modeEntry, err := newModeMetadataEntry(stream.TurnSeq, intent.RequestID, targetMode, true, ModeSourceSwitchModeTool)
		if err != nil {
			return err
		}
		if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
			modeEntry,
			newModeChangePromptContextEntry(stream.TurnSeq, intent.RequestID, targetMode),
		}); err != nil {
			return err
		}
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, intent.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishCheckpoint(intent.RequestID, stream.ConversationID); err != nil {
		return err
	}
	if !shouldAutoResumeAfterInteraction(pending) {
		rememberPendingProviderCompletion(stream, pendingTurnCompletion{
			ConversationID: stream.ConversationID,
			RequestID:      stream.RequestID,
			TurnSeq:        stream.TurnSeq,
			ModelCallID:    pending.ModelCallID,
			ProviderPass:   pending.ProviderPass,
			Disposition:    completionDispositionCompleteAfterExternal,
		})
	}
	return service.reconcileStream(stream)
}

func (service *Service) applyApprovedSwitchMode(stream *ActiveStream, conversationID string, toolCall *agentv1.ToolCall) error {
	switchToolCall := toolCall.GetSwitchModeToolCall()
	if switchToolCall == nil || switchToolCall.GetResult().GetSuccess() == nil {
		return nil
	}
	targetMode, err := switchModeTarget(switchToolCall.GetArgs())
	if err != nil {
		return err
	}
	targetAlias, err := modeAlias(targetMode)
	if err != nil {
		return err
	}
	stream.mu.Lock()
	stream.Mode = targetMode
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	_, err = service.updateConversationMetaAndCheckpoint(stream, conversationID, func(item *ConversationFile) error {
		if item == nil {
			return nil
		}
		item.Mode = targetAlias
		return nil
	})
	return err
}

func applySwitchModeMetadata(stream *ActiveStream, toolCall *agentv1.ToolCall) {
	switchToolCall := toolCall.GetSwitchModeToolCall()
	if switchToolCall == nil {
		return
	}
	success := switchToolCall.GetResult().GetSuccess()
	if success == nil {
		return
	}
	if fromAlias, err := modeAlias(currentStreamMode(stream)); err == nil {
		success.FromModeId = fromAlias
	}
}

func switchModeTarget(args *agentv1.SwitchModeArgs) (agentv1.AgentMode, error) {
	if args == nil {
		return agentv1.AgentMode_AGENT_MODE_UNSPECIFIED, fmt.Errorf("switch mode args are required")
	}
	return parseTargetModeID(args.GetTargetModeId())
}

func markInteractionCompleted(stream *ActiveStream, pending runtimecore.PendingInteraction) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	delete(stream.PendingInteractions, pending.InteractionID)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}

func deriveToolNameFromPendingInteraction(pending runtimecore.PendingInteraction) string {
	switch strings.TrimSpace(pending.InteractionKind) {
	case "ask_question":
		return "AskQuestion"
	case "create_plan":
		return "CreatePlan"
	case "web_search":
		return "WebSearch"
	case "web_fetch":
		return "WebFetch"
	case "switch_mode":
		return "SwitchMode"
	default:
		return ""
	}
}

func isInteractionTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "AskQuestion", "CreatePlan", "WebSearch", "WebFetch", "SwitchMode":
		return true
	default:
		return false
	}
}

func shouldAutoResumeAfterInteraction(pending runtimecore.PendingInteraction) bool {
	switch strings.TrimSpace(pending.InteractionKind) {
	case "create_plan":
		return false
	default:
		return true
	}
}

type generateImageToolCarrier struct {
	Description         string   `json:"description,omitempty"`
	FilePath            string   `json:"file_path,omitempty"`
	ReferenceImagePaths []string `json:"reference_image_paths,omitempty"`
	ImageData           string   `json:"image_data,omitempty"`
}

func isImmediateNativeTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "GenerateImage", "AwaitShell":
		return true
	default:
		return false
	}
}

func (service *Service) handleImmediateNativeToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	switch strings.TrimSpace(invocation.ToolName) {
	case "GenerateImage":
		return service.handleGenerateImageToolInvocation(stream, invocation)
	case "AwaitShell":
		return service.handleAwaitShellToolInvocation(stream, invocation)
	default:
		return fmt.Errorf("unsupported immediate native tool: %s", invocation.ToolName)
	}
}

func (service *Service) handleGenerateImageToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	carrier, decodeErr := decodeGenerateImageToolCarrier(invocation.ArgsJSON)
	args := buildGenerateImageArgsFromCarrier(carrier)
	sanitizedInvocation := invocation
	sanitizedInvocation.ArgsJSON = encodeGenerateImageArgsForHistory(args)
	if decodeErr != nil {
		result, payload := buildGenerateImageErrorResult(decodeErr.Error())
		return service.completeImmediateToolResult(stream, sanitizedInvocation, payload, buildGenerateImageToolCall(args, result))
	}
	imageData, err := normalizeGenerateImageBase64(carrier.ImageData)
	if err != nil {
		result, payload := buildGenerateImageErrorResult(err.Error())
		return service.completeImmediateToolResult(stream, sanitizedInvocation, payload, buildGenerateImageToolCall(args, result))
	}
	result, payload := buildGenerateImageSuccessResult(args.GetFilePath(), imageData)
	if err := service.completeImmediateToolResult(stream, sanitizedInvocation, payload, buildGenerateImageToolCall(args, result)); err != nil {
		return err
	}
	markProviderTerminalToolInvocation(stream)
	return nil
}

func markProviderTerminalToolInvocation(stream *ActiveStream) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	stream.ProviderTerminalToolInvocation = true
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}

func decodeGenerateImageToolCarrier(raw []byte) (generateImageToolCarrier, error) {
	var carrier generateImageToolCarrier
	if len(raw) == 0 {
		return carrier, nil
	}
	if err := json.Unmarshal(raw, &carrier); err != nil {
		return carrier, fmt.Errorf("decode GenerateImage args failed: %w", err)
	}
	carrier.Description = strings.TrimSpace(carrier.Description)
	carrier.FilePath = strings.TrimSpace(carrier.FilePath)
	carrier.ImageData = strings.TrimSpace(carrier.ImageData)
	carrier.ReferenceImagePaths = compactTrimmedStrings(carrier.ReferenceImagePaths)
	return carrier, nil
}

func compactTrimmedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	items := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

func buildGenerateImageArgsFromCarrier(carrier generateImageToolCarrier) *agentv1.GenerateImageArgs {
	args := &agentv1.GenerateImageArgs{
		Description:         strings.TrimSpace(carrier.Description),
		ReferenceImagePaths: append([]string(nil), carrier.ReferenceImagePaths...),
	}
	if filePath := strings.TrimSpace(carrier.FilePath); filePath != "" {
		args.FilePath = &filePath
	}
	return args
}

func encodeGenerateImageArgsForHistory(args *agentv1.GenerateImageArgs) []byte {
	payload := map[string]any{}
	if args != nil {
		if description := strings.TrimSpace(args.GetDescription()); description != "" {
			payload["description"] = description
		}
		if filePath := strings.TrimSpace(args.GetFilePath()); filePath != "" {
			payload["file_path"] = filePath
		}
		if referenceImagePaths := compactTrimmedStrings(args.GetReferenceImagePaths()); len(referenceImagePaths) > 0 {
			payload["reference_image_paths"] = referenceImagePaths
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

func normalizeGenerateImageBase64(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("GenerateImage image_data is required")
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "data:image/") {
		return "", fmt.Errorf("GenerateImage image_data must be raw base64 without a data URL prefix")
	}
	compact := strings.Join(strings.Fields(trimmed), "")
	if compact == "" {
		return "", fmt.Errorf("GenerateImage image_data is required")
	}
	if _, err := base64.StdEncoding.DecodeString(compact); err != nil {
		return "", fmt.Errorf("GenerateImage image_data must be valid base64: %w", err)
	}
	return compact, nil
}

func buildGenerateImageToolCall(args *agentv1.GenerateImageArgs, result *agentv1.GenerateImageResult) *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_GenerateImageToolCall{
			GenerateImageToolCall: &agentv1.GenerateImageToolCall{
				Args:   args,
				Result: result,
			},
		},
	}
}

func buildGenerateImageSuccessResult(filePath string, imageData string) (*agentv1.GenerateImageResult, string) {
	trimmedFilePath := strings.TrimSpace(filePath)
	payload := fmt.Sprintf("generate image success image_data_bytes=%d", len(imageData))
	if trimmedFilePath != "" {
		payload = fmt.Sprintf("generate image success file_path=%s image_data_bytes=%d", trimmedFilePath, len(imageData))
	}
	return &agentv1.GenerateImageResult{
		Result: &agentv1.GenerateImageResult_Success{
			Success: &agentv1.GenerateImageSuccess{
				FilePath:  trimmedFilePath,
				ImageData: imageData,
			},
		},
	}, payload
}

func buildGenerateImageErrorResult(message string) (*agentv1.GenerateImageResult, string) {
	errorText := strings.TrimSpace(message)
	if errorText == "" {
		errorText = "image generation failed"
	}
	return &agentv1.GenerateImageResult{
		Result: &agentv1.GenerateImageResult_Error{
			Error: &agentv1.GenerateImageError{Error: errorText},
		},
	}, errorText
}

func isLocalStateTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "TodoWrite":
		return true
	default:
		return false
	}
}

func (service *Service) handleLocalStateToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	switch strings.TrimSpace(invocation.ToolName) {
	case "TodoWrite":
		return service.handleTodoWriteToolInvocation(stream, invocation)
	default:
		return fmt.Errorf("unsupported local state tool: %s", invocation.ToolName)
	}
}

func (service *Service) handleTodoWriteToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	decodedArgs, err := decodeUpdateTodosArgsJSONWithPresence(invocation.ArgsJSON)
	if err != nil {
		return service.completeImmediateToolResult(stream, invocation, summarizeUpdateTodosResult(&agentv1.UpdateTodosResult{
			Result: &agentv1.UpdateTodosResult_Error{
				Error: &agentv1.UpdateTodosError{Error: err.Error()},
			},
		}), buildUpdateTodosToolCall(&agentv1.UpdateTodosArgs{}, &agentv1.UpdateTodosResult{
			Result: &agentv1.UpdateTodosResult_Error{
				Error: &agentv1.UpdateTodosError{Error: err.Error()},
			},
		}))
	}
	args := decodedArgs.Args
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return err
	}
	structuredState, err := projectConversationStructuredState(conversation)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	applyMerge := shouldMergeTodoUpdate(args, decodedArgs.MergeSet, structuredState.Todos)
	args.Merge = applyMerge
	var nextTodos []*agentv1.TodoItem
	if applyMerge {
		nextTodos, err = mergeTodoItems(structuredState.Todos, args.GetTodos(), now)
	} else {
		nextTodos, err = normalizeTodoItems(args.GetTodos(), now, true)
	}
	if err != nil {
		result := buildUpdateTodosErrorResult(args, err.Error())
		return service.completeImmediateToolResult(stream, invocation, summarizeUpdateTodosResult(result), buildUpdateTodosToolCall(args, result))
	}
	if !applyMerge {
		if missingIDs := missingActiveTodoReplacementIDs(structuredState.Todos, nextTodos); len(missingIDs) > 0 {
			result := buildUpdateTodosErrorResult(args, unsafeTodoReplaceError(missingIDs))
			return service.completeImmediateToolResult(stream, invocation, summarizeUpdateTodosResult(result), buildUpdateTodosToolCall(args, result))
		}
	}
	result := &agentv1.UpdateTodosResult{
		Result: &agentv1.UpdateTodosResult_Success{
			Success: &agentv1.UpdateTodosSuccess{
				Todos:      cloneTodoItems(nextTodos),
				TotalCount: int32(len(nextTodos)),
				WasMerge:   applyMerge,
			},
		},
	}
	return service.completeImmediateToolResult(stream, invocation, summarizeUpdateTodosResult(result), buildUpdateTodosToolCall(args, result))
}

func shouldMergeTodoUpdate(args *agentv1.UpdateTodosArgs, mergeSet bool, existing []*agentv1.TodoItem) bool {
	if args.GetMerge() {
		return true
	}
	if mergeSet || len(existing) == 0 {
		return false
	}
	if len(missingActiveTodoReplacementIDs(existing, args.GetTodos())) > 0 {
		return true
	}
	for _, item := range args.GetTodos() {
		if item == nil {
			continue
		}
		if strings.TrimSpace(item.GetContent()) == "" {
			return true
		}
	}
	return false
}

func (service *Service) handleReadTodosToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	args, err := decodeReadTodosArgsJSON(invocation.ArgsJSON)
	if err != nil {
		return service.completeImmediateToolResult(stream, invocation, summarizeReadTodosResult(&agentv1.ReadTodosResult{
			Result: &agentv1.ReadTodosResult_Error{
				Error: &agentv1.ReadTodosError{Error: err.Error()},
			},
		}), buildReadTodosToolCall(&agentv1.ReadTodosArgs{}, &agentv1.ReadTodosResult{
			Result: &agentv1.ReadTodosResult_Error{
				Error: &agentv1.ReadTodosError{Error: err.Error()},
			},
		}))
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return err
	}
	structuredState, err := projectConversationStructuredState(conversation)
	if err != nil {
		return err
	}
	filtered := filterTodoItems(structuredState.Todos, args.GetStatusFilter(), args.GetIdFilter())
	result := &agentv1.ReadTodosResult{
		Result: &agentv1.ReadTodosResult_Success{
			Success: &agentv1.ReadTodosSuccess{
				Todos:      filtered,
				TotalCount: int32(len(filtered)),
			},
		},
	}
	return service.completeImmediateToolResult(stream, invocation, summarizeReadTodosResult(result), buildReadTodosToolCall(args, result))
}

func (service *Service) completeImmediateToolResult(stream *ActiveStream, invocation runtimecore.ToolInvocation, resultText string, toolCall *agentv1.ToolCall) error {
	if err := service.appendToolResult(stream, invocation.CallID, strings.TrimSpace(invocation.ToolName), invocation.ArgsJSON, resultText, invocation.ReasoningContent, toolCall); err != nil {
		return err
	}
	if err := service.publishToolCallCompleted(stream.RequestID, invocation.CallID, invocation.ModelCallID, toolCall); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, stream.RequestID, invocation.ModelCallID); err != nil {
		return err
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}
