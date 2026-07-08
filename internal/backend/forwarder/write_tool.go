package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

const (
	writeReadExecKind     = "write_read"
	writeWriteExecKind    = "write_write"
	writePostReadExecKind = "write_post_read"
)

type writeOperationArgs struct {
	Path     string `json:"path"`
	Contents string `json:"contents"`
}

type pendingWritePayload struct {
	VisibleArgs   writeOperationArgs `json:"visible_args"`
	ResolvedPath  string             `json:"resolved_path"`
	BeforeContent string             `json:"before_content,omitempty"`
	AfterContent  string             `json:"after_content,omitempty"`
}

func isHiddenWriteExecKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case writeReadExecKind, writeWriteExecKind, writePostReadExecKind:
		return true
	default:
		return false
	}
}

func (service *Service) handleWriteToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	if stream == nil {
		return fmt.Errorf("write stream is required")
	}

	writeArgs, err := decodeWriteOperationArgs(invocation.ArgsJSON)
	if err != nil {
		return newRecoverableToolInvocationError(err)
	}

	resolvedPath := strings.TrimSpace(writeArgs.Path)
	writeArgs.Path = resolvedPath

	visibleArgsJSON, err := writeArgs.MarshalJSON()
	if err != nil {
		return err
	}
	invocation.ArgsJSON = visibleArgsJSON

	startedToolCall := buildStartedToolCall(invocation)
	if startedToolCall != nil {
		historyStartedToolCall := buildStartedWriteHistoryToolCall(writeArgs.Path)
		toolCallPayload, err := protojson.Marshal(historyStartedToolCall)
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
	if err := service.broker.Publish(stream.RequestID, StreamEvent{
		Message: buildToolCallStartedMessage(invocation.CallID, invocation.ModelCallID, startedToolCall),
	}); err != nil {
		return err
	}

	return service.startHiddenWriteRead(stream, invocation.CallID, invocation.ModelCallID, currentProviderPass(stream), invocation.ReasoningContent, invocation.ReasoningSignature, invocation.ReasoningSignatureSource, pendingWritePayload{
		VisibleArgs:  writeArgs,
		ResolvedPath: resolvedPath,
	})
}

func (service *Service) startHiddenWriteRead(stream *ActiveStream, toolCallID string, modelCallID string, providerPass int, reasoningContent string, reasoningSignature string, reasoningSignatureSource string, payload pendingWritePayload) error {
	readArgsJSON, err := json.Marshal(map[string]any{
		"path": strings.TrimSpace(payload.ResolvedPath),
	})
	if err != nil {
		return err
	}
	serverMessage, pendingExec, err := service.execBridge.OpenExec(execbridge.OpenExecContext{
		ConversationID: stream.ConversationID,
	}, runtimecore.ToolInvocation{
		CallID:      strings.TrimSpace(toolCallID),
		ToolName:    "Read",
		ArgsJSON:    readArgsJSON,
		ModelCallID: strings.TrimSpace(modelCallID),
	})
	if err != nil {
		return err
	}
	pendingArgsJSON, err := payload.MarshalJSON()
	if err != nil {
		return err
	}
	pendingExec.ModelCallID = strings.TrimSpace(modelCallID)
	pendingExec.ProviderPass = providerPass
	pendingExec.ToolCallID = strings.TrimSpace(toolCallID)
	pendingExec.ReasoningContent = reasoningContent
	pendingExec.ReasoningSignature = strings.TrimSpace(reasoningSignature)
	pendingExec.ReasoningSignatureSource = strings.TrimSpace(reasoningSignatureSource)
	pendingExec.ExecKind = writeReadExecKind
	pendingExec.ArgsJSON = pendingArgsJSON
	stream.mu.Lock()
	stream.PendingExecs[pendingExec.ExecID] = pendingExec
	stream.mu.Unlock()
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.broker.Publish(stream.RequestID, StreamEvent{Message: serverMessage})
}

func (service *Service) startHiddenWriteExec(stream *ActiveStream, toolCallID string, modelCallID string, providerPass int, reasoningContent string, reasoningSignature string, reasoningSignatureSource string, payload pendingWritePayload, beforeContent string) error {
	transportContents := prepareWriteContentsForClient(payload.ResolvedPath, payload.VisibleArgs.Contents)
	writeArgsJSON, err := json.Marshal(map[string]any{
		"path":     strings.TrimSpace(payload.ResolvedPath),
		"contents": transportContents,
	})
	if err != nil {
		return err
	}
	serverMessage, pendingExec, err := service.execBridge.OpenExec(execbridge.OpenExecContext{
		ConversationID: stream.ConversationID,
	}, runtimecore.ToolInvocation{
		CallID:      strings.TrimSpace(toolCallID),
		ToolName:    "Write",
		ArgsJSON:    writeArgsJSON,
		ModelCallID: strings.TrimSpace(modelCallID),
	})
	if err != nil {
		return err
	}
	payload.BeforeContent = beforeContent
	payload.AfterContent = payload.VisibleArgs.Contents
	pendingArgsJSON, err := payload.MarshalJSON()
	if err != nil {
		return err
	}
	pendingExec.ModelCallID = strings.TrimSpace(modelCallID)
	pendingExec.ProviderPass = providerPass
	pendingExec.ToolCallID = strings.TrimSpace(toolCallID)
	pendingExec.ReasoningContent = reasoningContent
	pendingExec.ReasoningSignature = strings.TrimSpace(reasoningSignature)
	pendingExec.ReasoningSignatureSource = strings.TrimSpace(reasoningSignatureSource)
	pendingExec.ExecKind = writeWriteExecKind
	pendingExec.ArgsJSON = pendingArgsJSON
	stream.mu.Lock()
	stream.PendingExecs[pendingExec.ExecID] = pendingExec
	stream.mu.Unlock()
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.broker.Publish(stream.RequestID, StreamEvent{Message: serverMessage})
}

func (service *Service) startHiddenWritePostRead(stream *ActiveStream, toolCallID string, modelCallID string, providerPass int, reasoningContent string, reasoningSignature string, reasoningSignatureSource string, payload pendingWritePayload) error {
	readArgsJSON, err := json.Marshal(map[string]any{
		"path": strings.TrimSpace(payload.ResolvedPath),
	})
	if err != nil {
		return err
	}
	serverMessage, pendingExec, err := service.execBridge.OpenExec(execbridge.OpenExecContext{
		ConversationID: stream.ConversationID,
	}, runtimecore.ToolInvocation{
		CallID:      strings.TrimSpace(toolCallID),
		ToolName:    "Read",
		ArgsJSON:    readArgsJSON,
		ModelCallID: strings.TrimSpace(modelCallID),
	})
	if err != nil {
		return err
	}
	pendingArgsJSON, err := payload.MarshalJSON()
	if err != nil {
		return err
	}
	pendingExec.ModelCallID = strings.TrimSpace(modelCallID)
	pendingExec.ProviderPass = providerPass
	pendingExec.ToolCallID = strings.TrimSpace(toolCallID)
	pendingExec.ReasoningContent = reasoningContent
	pendingExec.ReasoningSignature = strings.TrimSpace(reasoningSignature)
	pendingExec.ReasoningSignatureSource = strings.TrimSpace(reasoningSignatureSource)
	pendingExec.ExecKind = writePostReadExecKind
	pendingExec.ArgsJSON = pendingArgsJSON
	stream.mu.Lock()
	stream.PendingExecs[pendingExec.ExecID] = pendingExec
	stream.mu.Unlock()
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.broker.Publish(stream.RequestID, StreamEvent{Message: serverMessage})
}

func (service *Service) handleHiddenWriteExecResult(stream *ActiveStream, pending runtimecore.PendingExec, message *agentv1.ExecClientMessage) error {
	if stream == nil || message == nil {
		return fmt.Errorf("write exec result requires stream and message")
	}
	payload, err := decodePendingWritePayload(pending.ArgsJSON)
	if err != nil {
		return err
	}

	switch strings.TrimSpace(pending.ExecKind) {
	case writeReadExecKind:
		markExecCompleted(stream, pending)
		readResult := message.GetReadResult()
		if readResult == nil {
			return service.finishWriteOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload.VisibleArgs, buildEditErrorResult(payload.ResolvedPath, "write read result missing"))
		}
		switch readResult.GetResult().(type) {
		case *agentv1.ReadResult_FileNotFound:
			return service.startHiddenWriteExec(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, pending.ReasoningSignature, pending.ReasoningSignatureSource, payload, "")
		default:
			content, ok := extractReadContentForEdit(readResult)
			if !ok {
				return service.finishWriteOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload.VisibleArgs, buildEditResultFromReadResult(payload.ResolvedPath, readResult))
			}
			return service.startHiddenWriteExec(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, pending.ReasoningSignature, pending.ReasoningSignatureSource, payload, content)
		}
	case writeWriteExecKind:
		markExecCompleted(stream, pending)
		writeResult := message.GetWriteResult()
		if writeResult == nil {
			return service.finishWriteOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload.VisibleArgs, buildEditErrorResult(payload.ResolvedPath, "write result missing"))
		}
		switch item := writeResult.GetResult().(type) {
		case *agentv1.WriteResult_Success:
			payload.ResolvedPath = firstNonEmpty(strings.TrimSpace(item.Success.GetPath()), strings.TrimSpace(payload.ResolvedPath), strings.TrimSpace(payload.VisibleArgs.Path))
			if item.Success.GetFileContentAfterWrite() != "" {
				payload.AfterContent, _ = reconcilePostWriteObservedContent(payload.AfterContent, item.Success.GetFileContentAfterWrite())
			}
			payload.VisibleArgs.Path = payload.ResolvedPath
			return service.startHiddenWritePostRead(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, pending.ReasoningSignature, pending.ReasoningSignatureSource, payload)
		default:
			return service.finishWriteOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload.VisibleArgs, buildEditResultFromWriteResult(payload.ResolvedPath, writeResult))
		}
	case writePostReadExecKind:
		markExecCompleted(stream, pending)
		writeArgs := payload.VisibleArgs
		writeArgs.Path = firstNonEmpty(strings.TrimSpace(payload.ResolvedPath), strings.TrimSpace(writeArgs.Path))
		readResult := message.GetReadResult()
		if readResult == nil {
			return service.finishWriteOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, writeArgs, buildSuccessfulWriteResult(writeArgs.Path, payload.BeforeContent, payload.AfterContent))
		}
		if content, ok := extractReadContentForEdit(readResult); ok {
			if success := readResult.GetSuccess(); success != nil {
				writeArgs.Path = firstNonEmpty(strings.TrimSpace(success.GetPath()), writeArgs.Path)
			}
			finalAfterContent, _ := reconcilePostWriteObservedContent(payload.AfterContent, content)
			return service.finishWriteOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, writeArgs, buildSuccessfulWriteResult(writeArgs.Path, payload.BeforeContent, finalAfterContent))
		}
		return service.finishWriteOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, writeArgs, buildSuccessfulWriteResult(writeArgs.Path, payload.BeforeContent, payload.AfterContent))
	default:
		return fmt.Errorf("unsupported hidden write exec kind: %s", pending.ExecKind)
	}
}

func (service *Service) handleHiddenWriteExecControl(stream *ActiveStream, pending runtimecore.PendingExec, message *agentv1.ExecClientControlMessage) error {
	if stream == nil || message == nil {
		return fmt.Errorf("write exec control requires stream and message")
	}
	if _, ok := message.GetMessage().(*agentv1.ExecClientControlMessage_Heartbeat); ok {
		return nil
	}
	if _, ok := message.GetMessage().(*agentv1.ExecClientControlMessage_StreamClose); ok {
		return nil
	}
	payload, err := decodePendingWritePayload(pending.ArgsJSON)
	if err != nil {
		return err
	}
	markExecCompleted(stream, pending)
	if strings.TrimSpace(pending.ExecKind) == writePostReadExecKind {
		return service.finishWriteOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload.VisibleArgs, buildSuccessfulWriteResult(payload.ResolvedPath, payload.BeforeContent, payload.AfterContent))
	}
	return service.finishWriteOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload.VisibleArgs, buildEditErrorResult(payload.ResolvedPath, hiddenWriteControlError(message)))
}

func (service *Service) finishWriteOperation(stream *ActiveStream, toolCallID string, modelCallID string, providerPass int, reasoningContent string, writeArgs writeOperationArgs, result *agentv1.EditResult) error {
	if stream == nil {
		return nil
	}
	if result == nil {
		result = buildEditErrorResult(writeArgs.Path, "write result missing")
	}
	writeArgs.Path = firstNonEmpty(resultPath(result), writeArgs.Path)
	historyArgsJSON, err := writeArgs.MarshalJSON()
	if err != nil {
		return err
	}
	toolCall := buildCompletedWriteToolCall(writeArgs.Path, writeArgs.Contents, result)
	historyToolCall := buildCompletedWriteHistoryToolCall(writeArgs.Path, result)
	if err := service.appendToolResult(stream, strings.TrimSpace(toolCallID), "Write", historyArgsJSON, summarizeWriteHistoryResult(writeArgs.Path, result), reasoningContent, historyToolCall); err != nil {
		return err
	}
	if err := service.publishToolCallCompleted(stream.RequestID, strings.TrimSpace(toolCallID), strings.TrimSpace(modelCallID), toolCall); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, stream.RequestID, strings.TrimSpace(modelCallID)); err != nil {
		return err
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}

func decodeWriteOperationArgs(raw []byte) (writeOperationArgs, error) {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return writeOperationArgs{}, fmt.Errorf("decode write args failed: %w", err)
	}
	result := writeOperationArgs{
		Path:     firstNonEmpty(readStringAny(args["path"]), readStringAny(args["file_path"])),
		Contents: firstNonEmpty(readStringAny(args["contents"]), readStringAny(args["content"]), readStringAny(args["stream_content"]), readStringAny(args["streamContent"])),
	}
	if strings.TrimSpace(result.Path) == "" {
		return writeOperationArgs{}, fmt.Errorf("write path is required")
	}
	if !isAbsoluteToolPath(result.Path) {
		return writeOperationArgs{}, fmt.Errorf("write path must be absolute")
	}
	return result, nil
}

func (args writeOperationArgs) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"path":     strings.TrimSpace(args.Path),
		"contents": args.Contents,
	})
}

func (payload pendingWritePayload) MarshalJSON() ([]byte, error) {
	type alias pendingWritePayload
	return json.Marshal(alias(payload))
}

func decodePendingWritePayload(raw []byte) (pendingWritePayload, error) {
	var payload pendingWritePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return pendingWritePayload{}, fmt.Errorf("decode write pending payload failed: %w", err)
	}
	return payload, nil
}

func buildSuccessfulWriteResult(path string, beforeContent string, afterContent string) *agentv1.EditResult {
	diffString, linesAdded, linesRemoved := computeEditDiff(beforeContent, afterContent)
	return buildSuccessfulEditResult(path, beforeContent, afterContent, diffString, linesAdded, linesRemoved, "")
}

func buildCompletedWriteToolCall(path string, streamContent string, result *agentv1.EditResult) *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_EditToolCall{
			EditToolCall: &agentv1.EditToolCall{
				Args: &agentv1.EditArgs{
					Path:          strings.TrimSpace(path),
					StreamContent: literalStringPtr(streamContent),
				},
				Result: result,
			},
		},
	}
}

func buildStartedWriteHistoryToolCall(path string) *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_EditToolCall{
			EditToolCall: &agentv1.EditToolCall{
				Args: &agentv1.EditArgs{
					Path: strings.TrimSpace(path),
				},
			},
		},
	}
}

func buildCompletedWriteHistoryToolCall(path string, result *agentv1.EditResult) *agentv1.ToolCall {
	return buildCompletedEditToolCall(path, compactWriteHistoryEditResult(path, result))
}

func summarizeWriteHistoryResult(path string, result *agentv1.EditResult) string {
	if result == nil {
		return "write result missing"
	}
	success := result.GetSuccess()
	if success == nil {
		return summarizeEditResultWithoutPath(result)
	}
	encoded, err := json.Marshal(map[string]any{
		"success": map[string]any{
			"path": firstNonEmpty(success.GetPath(), path),
		},
	})
	if err != nil {
		return fmt.Sprintf(`{"success":{"path":%q}}`, firstNonEmpty(success.GetPath(), path))
	}
	return string(encoded)
}

func compactWriteHistoryEditResult(path string, result *agentv1.EditResult) *agentv1.EditResult {
	if result == nil {
		return buildEditErrorResult("", "write result missing")
	}
	success := result.GetSuccess()
	if success == nil {
		return editResultWithoutPath(result)
	}
	return &agentv1.EditResult{
		Result: &agentv1.EditResult_Success{
			Success: &agentv1.EditSuccess{
				Path: firstNonEmpty(success.GetPath(), path),
			},
		},
	}
}

func boundedWriteDiffString(diffString string) string {
	return truncateProjectedReplayText("Write", diffString, projectedEditReplayLimit)
}

func resultPath(result *agentv1.EditResult) string {
	if result == nil {
		return ""
	}
	switch item := result.GetResult().(type) {
	case *agentv1.EditResult_Success:
		return strings.TrimSpace(item.Success.GetPath())
	case *agentv1.EditResult_FileNotFound:
		return strings.TrimSpace(item.FileNotFound.GetPath())
	case *agentv1.EditResult_ReadPermissionDenied:
		return strings.TrimSpace(item.ReadPermissionDenied.GetPath())
	case *agentv1.EditResult_WritePermissionDenied:
		return strings.TrimSpace(item.WritePermissionDenied.GetPath())
	case *agentv1.EditResult_Rejected:
		return strings.TrimSpace(item.Rejected.GetPath())
	case *agentv1.EditResult_Error:
		return strings.TrimSpace(item.Error.GetPath())
	default:
		return ""
	}
}
