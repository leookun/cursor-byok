// tools_exec.go 提取自 service.go：工具调用派发、exec 进度记账与 tool 结果写回。
package forwarder

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cursor/internal/logger"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
	"google.golang.org/protobuf/encoding/protojson"
)

// handleToolInvocation 把模型产生的工具意图转成 exec/interaction 请求并下发给客户端。
func (service *Service) handleToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	if err := providerLoopInterruptErr(nil, stream, invocation.ModelCallID); err != nil {
		return err
	}
	invocation = service.rewriteDirectMCPToolInvocation(stream, invocation)
	invocation = service.normalizeCallMCPToolInvocation(stream, invocation)
	trimmedToolName := strings.TrimSpace(invocation.ToolName)
	stream.mu.Lock()
	mode := stream.Mode
	subagentTypeName := ""
	if stream.CheckpointConversation != nil {
		subagentTypeName = strings.TrimSpace(stream.CheckpointConversation.SubagentTypeName)
	}
	stream.ToolInvocationCount++
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	// Tool Runtime: 查询工具元数据（category, cacheable）
	if service.toolRuntime != nil {
		if entry, ok := service.toolRuntime.GetByInternalName(trimmedToolName); ok {
			// 同步启用状态
			if !entry.Enabled {
				return service.completePreDispatchToolError(stream, invocation, nil, false, false,
					fmt.Errorf("tool %q is disabled by Tool Runtime", trimmedToolName))
			}
			// ADR-016/024: Check tool result cache before dispatch.
			// If cached, return the result immediately without going
			// through ExecBridge, using the immediate-tool-result path.
			if entry.Cacheable && len(invocation.ArgsJSON) > 0 {
				if cached, err := service.toolRuntime.Execute(context.Background(), trimmedToolName, invocation.ArgsJSON); err == nil && cached != nil && cached.Success && cached.Cached {
					return service.completeImmediateToolResult(stream, invocation, cached.Output, nil)
				}
			}
		}
	}

	if !isToolAllowedInMode(mode, subagentTypeName, trimmedToolName) {
		return service.completePreDispatchToolError(stream, invocation, nil, false, false, fmt.Errorf("tool invocation is not enabled in mode %s: %s", mode.String(), invocation.ToolName))
	}
	var err error
	invocation, err = service.sanitizeCreatePlanInvocationForCurrentPlan(stream, invocation)
	if err != nil {
		if cause, ok := recoverableToolInvocationCause(err); ok {
			return service.completePreDispatchToolError(stream, invocation, nil, false, false, cause)
		}
		return err
	}
	if isPatchEditToolName(trimmedToolName) {
		if err := service.handlePatchEditToolInvocation(stream, invocation); err != nil {
			if cause, ok := recoverableToolInvocationCause(err); ok {
				return service.completePreDispatchToolError(stream, invocation, nil, false, false, cause)
			}
			return err
		}
		return nil
	}
	if trimmedToolName == "Write" {
		if err := service.handleWriteToolInvocation(stream, invocation); err != nil {
			if cause, ok := recoverableToolInvocationCause(err); ok {
				return service.completePreDispatchToolError(stream, invocation, nil, false, false, cause)
			}
			return err
		}
		return nil
	}
	isExecInvocation := isExecTool(trimmedToolName)
	isInteractionInvocation := isInteractionTool(trimmedToolName)
	isLocalStateInvocation := isLocalStateTool(trimmedToolName)
	isImmediateNativeInvocation := isImmediateNativeTool(trimmedToolName)
	if !isExecInvocation && !isInteractionInvocation && !isLocalStateInvocation && !isImmediateNativeInvocation {
		return service.completePreDispatchToolError(stream, invocation, nil, false, false, fmt.Errorf("unsupported tool invocation: %s", invocation.ToolName))
	}
	var subagentOverrides map[string]runtimecore.SubagentModelOverrideSelection
	if isExecInvocation {
		subagentOverrides = cloneSubagentModelOverrides(stream.SubagentModelOverrides)
		if resolutionPayload := taskSubagentModelResolutionPayload(invocation, stream.ModelID, subagentOverrides); resolutionPayload != nil {
			service.debug.LogRuntime(context.Background(), stream.RequestID, stream.ConversationID, "subagent_model_override_resolved", resolutionPayload)
		}
		invocation = rewriteTaskInvocationModelForDisplay(invocation, stream.ModelID, subagentOverrides)
	}
	bufferExecDispatch := isExecInvocation && shouldBufferExecDispatch(invocation.ToolName)
	suppressStartedToolCall := shouldSuppressStartedToolCallAfterPartial(stream, trimmedToolName, invocation.CallID)
	startedToolCall := buildStartedToolCall(invocation)
	startedEmitted := suppressStartedToolCall
	ensureLoopActive := func() error {
		return providerLoopInterruptErr(nil, stream, invocation.ModelCallID)
	}
	if startedToolCall != nil {
		if err := ensureLoopActive(); err != nil {
			return err
		}
		toolCallPayload, err := protojson.Marshal(startedToolCall)
		if err != nil {
			return err
		}
		_, err = service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
			newToolCallEntryWithProviderMetadata(stream.TurnSeq, stream.RequestID, invocation.CallID, invocation.ToolName, invocation.ReasoningContent, invocation.ReasoningSignature, invocation.ReasoningSignatureSource, invocation.ReasoningProviderItemID, invocation.ReasoningProviderStatus, invocation.ReasoningProviderSummary, invocation.ProviderItemID, invocation.ProviderCallID, invocation.ProviderStatus, toolCallPayload),
		})
		if err != nil {
			return err
		}
	}
	if !bufferExecDispatch && !suppressStartedToolCall {
		if err := ensureLoopActive(); err != nil {
			return err
		}
		if err := service.broker.Publish(stream.RequestID, StreamEvent{
			Message: buildToolCallStartedMessage(invocation.CallID, invocation.ModelCallID, startedToolCall),
		}); err != nil {
			return err
		}
		startedEmitted = true
	}
	if isImmediateNativeInvocation {
		return service.handleImmediateNativeToolInvocation(stream, invocation)
	}
	if isLocalStateInvocation {
		return service.handleLocalStateToolInvocation(stream, invocation)
	}
	if isInteractionInvocation {
		if err := service.handleInteractionToolInvocation(stream, invocation); err != nil {
			if cause, ok := recoverableToolInvocationCause(err); ok {
				return service.completePreDispatchToolError(stream, invocation, startedToolCall, startedToolCall != nil, startedEmitted, cause)
			}
			return err
		}
		return nil
	}
	if isExecInvocation {
		serverMessage, pendingExec, err := service.execBridge.OpenExec(execbridge.OpenExecContext{
			ConversationID:         stream.ConversationID,
			ModelID:                stream.ModelID,
			SubagentModelOverrides: subagentOverrides,
		}, invocation)
		if err != nil {
			return service.completePreDispatchToolError(stream, invocation, startedToolCall, startedToolCall != nil, startedEmitted, err)
		}
		pendingExec.ModelCallID = invocation.ModelCallID
		pendingExec.ReasoningContent = invocation.ReasoningContent
		pendingExec.ReasoningSignature = invocation.ReasoningSignature
		pendingExec.ReasoningSignatureSource = invocation.ReasoningSignatureSource
		pendingExec = initializePendingExecForTracking(pendingExec)
		stream.mu.Lock()
		pendingExec.ProviderPass = stream.ProviderPassCount
		stream.PendingExecs[pendingExec.ExecID] = pendingExec
		stream.mu.Unlock()
		service.scheduleShellForegroundRecovery(stream.RequestID, pendingExec)
		removePendingExec := func() {
			stream.mu.Lock()
			delete(stream.PendingExecs, pendingExec.ExecID)
			stream.mu.Unlock()
		}
		if err := ensureLoopActive(); err != nil {
			removePendingExec()
			return err
		}
		if bufferExecDispatch {
			if err := ensureLoopActive(); err != nil {
				removePendingExec()
				return err
			}
			if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: serverMessage}); err != nil {
				removePendingExec()
				return err
			}
			if err := ensureLoopActive(); err != nil {
				removePendingExec()
				return err
			}
			if err := service.broker.Publish(stream.RequestID, StreamEvent{
				Message: buildToolCallStartedMessage(invocation.CallID, invocation.ModelCallID, startedToolCall),
			}); err != nil {
				removePendingExec()
				return err
			}
			startedEmitted = true
			service.recordExecDispatchMetadata(stream, pendingExec, true, startedEmitted, "exec_then_started_then_checkpoint")
			if err := ensureLoopActive(); err != nil {
				removePendingExec()
				return err
			}
			if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
				removePendingExec()
				return err
			}
			return nil
		}
		if err := ensureLoopActive(); err != nil {
			removePendingExec()
			return err
		}
		if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
			removePendingExec()
			return err
		}
		if err := ensureLoopActive(); err != nil {
			removePendingExec()
			return err
		}
		if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: serverMessage}); err != nil {
			removePendingExec()
			return err
		}
		service.recordExecDispatchMetadata(stream, pendingExec, false, startedEmitted, "started_then_checkpoint_then_exec")
		return nil
	}
	return nil
}

func shouldSuppressStartedToolCallAfterPartial(stream *ActiveStream, toolName string, callID string) bool {
	if stream == nil {
		return false
	}
	switch strings.TrimSpace(toolName) {
	case "CreatePlan", "GenerateImage":
	default:
		return false
	}
	trimmedCallID := strings.TrimSpace(callID)
	if trimmedCallID == "" {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.PartialToolCallIDs == nil {
		return false
	}
	_, ok := stream.PartialToolCallIDs[trimmedCallID]
	return ok
}

func (service *Service) recordExecDispatchMetadata(stream *ActiveStream, pending runtimecore.PendingExec, buffered bool, startedEmitted bool, dispatchOrder string) {
	if service == nil || stream == nil {
		return
	}
	toolName := strings.TrimSpace(deriveToolNameFromPendingExec(pending))
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "exec_dispatch", map[string]any{
			"tool_call_id":    pending.ToolCallID,
			"message_id":      pending.MessageID,
			"exec_id":         pending.ExecID,
			"exec_kind":       pending.ExecKind,
			"provider_pass":   pending.ProviderPass,
			"tool_name":       toolName,
			"model_call_id":   pending.ModelCallID,
			"buffered":        buffered,
			"started_emitted": startedEmitted,
			"dispatch_order":  strings.TrimSpace(dispatchOrder),
			"opened_at":       pending.OpenedAt,
		}),
	}); err != nil {
		logger.Infof("forwarder exec dispatch metadata failed request_id=%s tool_call_id=%s message_id=%d err=%v", strings.TrimSpace(stream.RequestID), strings.TrimSpace(pending.ToolCallID), pending.MessageID, err)
	}
}

// shouldBufferExecDispatch 把只需要完整参数的快工具改成“先发 exec 请求，再发 started，再发 checkpoint”，
// 避免客户端在参数仍未稳定前过早起计时，同时保留显式的工具开始信号。
func shouldBufferExecDispatch(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "Read", "Grep", "Glob":
		return true
	default:
		return false
	}
}

// appendToolResult 把已完成的工具结果追加到 history，供后续 prompt replay 使用。
//
// reasoning 在已提交 history 中应挂在 assistant_text / tool_call 上。
// tool_result 保存一份 reasoning_content 兜底，replay 只会在缺失 tool_call entry
// 且 reasoning 可回放时用它重建 assistant tool_use，不会把 thinking 复制到工具消息上。
func (service *Service) appendToolResult(stream *ActiveStream, toolCallID string, toolName string, argsJSON []byte, resultText string, reasoningContent string, toolCall *agentv1.ToolCall) error {
	if stream == nil {
		return nil
	}
	var payload json.RawMessage
	if toolCall != nil {
		encoded, err := protojson.Marshal(toolCall)
		if err != nil {
			return err
		}
		payload = encoded
	}
	_, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newToolResultEntry(stream.TurnSeq, stream.RequestID, toolCallID, toolName, string(argsJSON), resultText, reasoningContent, payload),
	})
	return err
}

func (service *Service) publishToolCallCompleted(requestID string, toolCallID string, modelCallID string, toolCall *agentv1.ToolCall) error {
	if strings.TrimSpace(requestID) == "" || strings.TrimSpace(toolCallID) == "" {
		return nil
	}
	return service.broker.Publish(requestID, StreamEvent{
		Message: buildToolCallCompletedMessage(toolCallID, modelCallID, toolCall),
	})
}

func (service *Service) applyExecProgress(stream *ActiveStream, pending runtimecore.PendingExec, message *agentv1.ExecClientMessage) runtimecore.PendingExec {
	if stream == nil || message == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return pending
	}
	shellStream := message.GetShellStream()
	if shellStream == nil {
		return pending
	}

	stream.mu.Lock()
	defer stream.mu.Unlock()
	current, ok := stream.PendingExecs[pending.ExecID]
	if !ok {
		return pending
	}
	now := time.Now().UTC()
	switch event := shellStream.GetEvent().(type) {
	case *agentv1.ShellStream_Stdout:
		if current.FirstChunkAt.IsZero() {
			current.FirstChunkAt = now
		}
		current.ChunkCount++
		current.StreamState = "streaming"
		current.LastShellActivityAt = now
		current.StdoutBuffer += execbridge.DecodeShellStdout(event.Stdout)
	case *agentv1.ShellStream_Stderr:
		if current.FirstChunkAt.IsZero() {
			current.FirstChunkAt = now
		}
		current.ChunkCount++
		current.StreamState = "streaming"
		current.LastShellActivityAt = now
		current.StderrBuffer += event.Stderr.GetData()
	case *agentv1.ShellStream_Start:
		if current.FirstChunkAt.IsZero() {
			current.FirstChunkAt = now
		}
		current.StreamState = "started"
		current.LastShellActivityAt = now
	case *agentv1.ShellStream_Backgrounded:
		current.StreamState = "backgrounded"
		current.LastShellActivityAt = now
	case *agentv1.ShellStream_Exit:
		current.StreamState = "exited"
		current.LastShellActivityAt = now
	case *agentv1.ShellStream_Rejected:
		current.StreamState = "rejected"
		current.LastShellActivityAt = now
	case *agentv1.ShellStream_PermissionDenied:
		current.StreamState = "permission_denied"
		current.LastShellActivityAt = now
	}
	stream.PendingExecs[pending.ExecID] = current
	return current
}

func (service *Service) applyExecControlProgress(stream *ActiveStream, pending runtimecore.PendingExec, message *agentv1.ExecClientControlMessage) runtimecore.PendingExec {
	if stream == nil || message == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return pending
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	current, ok := stream.PendingExecs[pending.ExecID]
	if !ok {
		return pending
	}
	now := time.Now().UTC()
	switch message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_Heartbeat:
		current.LastShellActivityAt = now
		current.LastShellHeartbeatAt = now
	case *agentv1.ExecClientControlMessage_StreamClose:
		current.LastShellActivityAt = now
	case *agentv1.ExecClientControlMessage_Throw:
		current.LastShellActivityAt = now
		current.StreamState = "throw"
	}
	stream.PendingExecs[pending.ExecID] = current
	return current
}
