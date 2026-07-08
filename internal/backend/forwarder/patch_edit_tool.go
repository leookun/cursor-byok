package forwarder

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

const (
	patchEditToolName = "PatchEdit"

	patchEditReadExecKindName     = "patch_edit_read"
	patchEditWriteExecKindName    = "patch_edit_write"
	patchEditPostReadExecKindName = "patch_edit_post_read"
)

type patchEditArgs struct {
	Path          string
	OldString     string
	NewString     string
	NewStringSet  bool
	ReplaceAll    bool
	ReplaceAllSet bool
}

type pendingPatchEditPayload struct {
	ToolName         string          `json:"tool_name"`
	RawArgsJSON      json.RawMessage `json:"raw_args_json,omitempty"`
	ArgsDecodeFailed bool            `json:"args_decode_failed,omitempty"`
	Args             patchEditArgs   `json:"args,omitempty"`
	ResolvedPath     string          `json:"resolved_path"`
	BeforeContent    string          `json:"before_content,omitempty"`
	AfterContent     string          `json:"after_content,omitempty"`
	DiffString       string          `json:"diff_string,omitempty"`
	LinesAdded       int32           `json:"lines_added,omitempty"`
	LinesRemoved     int32           `json:"lines_removed,omitempty"`
	Message          string          `json:"message,omitempty"`
}

type queuedPatchEditOperation struct {
	ToolCallID               string
	ModelCallID              string
	ProviderPass             int
	ReasoningContent         string
	ReasoningSignature       string
	ReasoningSignatureSource string
	Payload                  pendingPatchEditPayload
}

func isPatchEditToolName(name string) bool {
	return strings.TrimSpace(name) == patchEditToolName
}

func isHiddenPatchEditExecKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case patchEditReadExecKindName, patchEditWriteExecKindName, patchEditPostReadExecKindName:
		return true
	default:
		return false
	}
}

func (service *Service) handlePatchEditToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	if stream == nil {
		return fmt.Errorf("patch edit stream is required")
	}
	toolName := strings.TrimSpace(invocation.ToolName)
	if !isPatchEditToolName(toolName) {
		return fmt.Errorf("unsupported patch edit tool: %s", invocation.ToolName)
	}

	payload := pendingPatchEditPayload{
		ToolName:    patchEditToolName,
		RawArgsJSON: append(json.RawMessage(nil), invocation.ArgsJSON...),
	}
	args, decodeErr := decodePatchEditArgs(invocation.ArgsJSON)
	payload.Args = args
	payload.ArgsDecodeFailed = decodeErr != nil

	resolvedPath := strings.TrimSpace(args.Path)
	payload.ResolvedPath = resolvedPath
	if decodeErr == nil {
		payload.Args.Path = resolvedPath
		visibleArgsJSON, err := payload.Args.MarshalJSON()
		if err != nil {
			return err
		}
		invocation.ArgsJSON = visibleArgsJSON
	}

	startedToolCall := buildStartedToolCall(invocation)
	if startedToolCall != nil {
		toolCallPayload, err := protojson.Marshal(startedToolCall)
		if err != nil {
			return err
		}
		_, err = service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
			newToolCallEntryWithProviderMetadata(stream.TurnSeq, stream.RequestID, invocation.CallID, patchEditToolName, invocation.ReasoningContent, invocation.ReasoningSignature, invocation.ReasoningSignatureSource, invocation.ReasoningProviderItemID, invocation.ReasoningProviderStatus, invocation.ReasoningProviderSummary, invocation.ProviderItemID, invocation.ProviderCallID, invocation.ProviderStatus, toolCallPayload),
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
	providerPass := currentProviderPass(stream)
	if decodeErr != nil {
		return service.finishPatchEditOperation(stream, invocation.CallID, invocation.ModelCallID, providerPass, invocation.ReasoningContent, payload, buildEditErrorResult(resolvedPath, decodeErr.Error()))
	}
	return service.startOrQueuePatchEditOperation(stream, queuedPatchEditOperation{
		ToolCallID:               invocation.CallID,
		ModelCallID:              invocation.ModelCallID,
		ProviderPass:             providerPass,
		ReasoningContent:         invocation.ReasoningContent,
		ReasoningSignature:       invocation.ReasoningSignature,
		ReasoningSignatureSource: invocation.ReasoningSignatureSource,
		Payload:                  payload,
	})
}

func (service *Service) startOrQueuePatchEditOperation(stream *ActiveStream, operation queuedPatchEditOperation) error {
	key := patchEditQueueKey(operation.Payload.ResolvedPath)
	if key != "" && patchEditPathHasInFlightOperation(stream, key) {
		enqueuePatchEditOperation(stream, key, operation)
		log.Printf(
			"forwarder patch edit queued behind in-flight edit conversation_id=%s request_id=%s tool_call_id=%s path=%s",
			strings.TrimSpace(stream.ConversationID),
			strings.TrimSpace(stream.RequestID),
			strings.TrimSpace(operation.ToolCallID),
			key,
		)
		return service.publishCheckpoint(stream.RequestID, stream.ConversationID)
	}
	return service.startQueuedPatchEditOperation(stream, operation)
}

func (service *Service) startQueuedPatchEditOperation(stream *ActiveStream, operation queuedPatchEditOperation) error {
	return service.startHiddenPatchEditRead(
		stream,
		operation.ToolCallID,
		operation.ModelCallID,
		operation.ProviderPass,
		operation.ReasoningContent,
		operation.ReasoningSignature,
		operation.ReasoningSignatureSource,
		operation.Payload,
	)
}

func (service *Service) startHiddenPatchEditRead(stream *ActiveStream, toolCallID string, modelCallID string, providerPass int, reasoningContent string, reasoningSignature string, reasoningSignatureSource string, payload pendingPatchEditPayload) error {
	readArgsJSON, err := json.Marshal(map[string]any{"path": strings.TrimSpace(payload.ResolvedPath)})
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
	pendingExec.ExecKind = patchEditReadExecKindName
	pendingExec.ArgsJSON = pendingArgsJSON
	stream.mu.Lock()
	stream.PendingExecs[pendingExec.ExecID] = pendingExec
	stream.mu.Unlock()
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.broker.Publish(stream.RequestID, StreamEvent{Message: serverMessage})
}

func (service *Service) startHiddenPatchEditWrite(stream *ActiveStream, toolCallID string, modelCallID string, providerPass int, reasoningContent string, reasoningSignature string, reasoningSignatureSource string, payload pendingPatchEditPayload, beforeContent string) error {
	computation, err := computePatchEdit(payload, beforeContent)
	if err != nil {
		return service.finishPatchEditOperation(stream, strings.TrimSpace(toolCallID), strings.TrimSpace(modelCallID), providerPass, reasoningContent, payload, buildEditErrorResult(payload.ResolvedPath, err.Error()))
	}

	transportContent := preparePatchEditWriteContentsForClient(computation.AfterContent)
	writeArgsJSON, err := json.Marshal(map[string]any{
		"path":     strings.TrimSpace(payload.ResolvedPath),
		"contents": transportContent,
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

	payload.BeforeContent = computation.BeforeContent
	payload.AfterContent = computation.AfterContent
	payload.DiffString = computation.DiffString
	payload.LinesAdded = computation.LinesAdded
	payload.LinesRemoved = computation.LinesRemoved
	payload.Message = computation.Message
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
	pendingExec.ExecKind = patchEditWriteExecKindName
	pendingExec.ArgsJSON = pendingArgsJSON
	stream.mu.Lock()
	stream.PendingExecs[pendingExec.ExecID] = pendingExec
	stream.mu.Unlock()
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.broker.Publish(stream.RequestID, StreamEvent{Message: serverMessage})
}

func (service *Service) startHiddenPatchEditPostRead(stream *ActiveStream, toolCallID string, modelCallID string, providerPass int, reasoningContent string, reasoningSignature string, reasoningSignatureSource string, payload pendingPatchEditPayload) error {
	readArgsJSON, err := json.Marshal(map[string]any{"path": strings.TrimSpace(payload.ResolvedPath)})
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
	pendingExec.ExecKind = patchEditPostReadExecKindName
	pendingExec.ArgsJSON = pendingArgsJSON
	stream.mu.Lock()
	stream.PendingExecs[pendingExec.ExecID] = pendingExec
	stream.mu.Unlock()
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.broker.Publish(stream.RequestID, StreamEvent{Message: serverMessage})
}

func (service *Service) handleHiddenPatchEditExecResult(stream *ActiveStream, pending runtimecore.PendingExec, message *agentv1.ExecClientMessage) error {
	if stream == nil || message == nil {
		return fmt.Errorf("patch edit exec result requires stream and message")
	}
	payload, err := decodePendingPatchEditPayload(pending.ArgsJSON)
	if err != nil {
		return err
	}

	switch strings.TrimSpace(pending.ExecKind) {
	case patchEditReadExecKindName:
		markExecCompleted(stream, pending)
		readResult := message.GetReadResult()
		if readResult == nil {
			return service.finishPatchEditOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload, buildEditErrorResult(payload.ResolvedPath, "patch edit read result missing"))
		}
		content, ok, readErr := extractCompleteReadContentForPatchEdit(readResult)
		if !ok {
			if readErr != "" {
				return service.finishPatchEditOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload, buildEditErrorResult(payload.ResolvedPath, readErr))
			}
			return service.finishPatchEditOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload, buildEditResultFromReadResult(payload.ResolvedPath, readResult))
		}
		return service.startHiddenPatchEditWrite(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, pending.ReasoningSignature, pending.ReasoningSignatureSource, payload, content)
	case patchEditWriteExecKindName:
		markExecCompleted(stream, pending)
		writeResult := message.GetWriteResult()
		if writeResult == nil {
			return service.finishPatchEditOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload, buildEditErrorResult(payload.ResolvedPath, "patch edit write result missing"))
		}
		switch item := writeResult.GetResult().(type) {
		case *agentv1.WriteResult_Success:
			payload.ResolvedPath = firstNonEmpty(strings.TrimSpace(item.Success.GetPath()), strings.TrimSpace(payload.ResolvedPath), strings.TrimSpace(patchEditPayloadPath(payload)))
			if item.Success.GetFileContentAfterWrite() != "" {
				finalAfterContent, reconciled, ok := service.reconcilePatchEditObservedContent(stream, pending.ToolCallID, payload.ResolvedPath, payload.AfterContent, item.Success.GetFileContentAfterWrite())
				if !ok {
					return service.finishPatchEditOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload, buildEditErrorResult(payload.ResolvedPath, "patch edit write verification failed: observed file content differs from expected content"))
				}
				if finalAfterContent != payload.AfterContent {
					payload.DiffString, payload.LinesAdded, payload.LinesRemoved = computeEditDiff(payload.BeforeContent, finalAfterContent)
				}
				payload.AfterContent = finalAfterContent
				if reconciled {
					payload.Message = appendPatchEditMessage(payload.Message, "write result matched after client line-ending normalization")
				}
			}
			setPatchEditPayloadPath(&payload, payload.ResolvedPath)
			return service.startHiddenPatchEditPostRead(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, pending.ReasoningSignature, pending.ReasoningSignatureSource, payload)
		default:
			return service.finishPatchEditOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload, buildEditResultFromWriteResult(payload.ResolvedPath, writeResult))
		}
	case patchEditPostReadExecKindName:
		markExecCompleted(stream, pending)
		readResult := message.GetReadResult()
		if readResult == nil {
			return service.finishPatchEditOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload, buildFinalEditSuccessResult(payload.ResolvedPath, payload.AfterContent, patchEditPayloadAsEditPayload(payload)))
		}
		if content, ok := extractReadContentForEdit(readResult); ok {
			if success := readResult.GetSuccess(); success != nil {
				payload.ResolvedPath = firstNonEmpty(strings.TrimSpace(success.GetPath()), payload.ResolvedPath)
				setPatchEditPayloadPath(&payload, payload.ResolvedPath)
			}
			finalAfterContent, reconciled, ok := service.reconcilePatchEditObservedContent(stream, pending.ToolCallID, payload.ResolvedPath, payload.AfterContent, content)
			if !ok {
				return service.finishPatchEditOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload, buildEditErrorResult(payload.ResolvedPath, "patch edit post-read verification failed: observed file content differs from expected content"))
			}
			if reconciled {
				payload.Message = appendPatchEditMessage(payload.Message, "post-read matched after client line-ending normalization")
			}
			return service.finishPatchEditOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload, buildFinalEditSuccessResult(payload.ResolvedPath, finalAfterContent, patchEditPayloadAsEditPayload(payload)))
		}
		return service.finishPatchEditOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload, buildFinalEditSuccessResult(payload.ResolvedPath, payload.AfterContent, patchEditPayloadAsEditPayload(payload)))
	default:
		return fmt.Errorf("unsupported hidden patch edit exec kind: %s", pending.ExecKind)
	}
}

func (service *Service) handleHiddenPatchEditExecControl(stream *ActiveStream, pending runtimecore.PendingExec, message *agentv1.ExecClientControlMessage) error {
	if stream == nil || message == nil {
		return fmt.Errorf("patch edit exec control requires stream and message")
	}
	if _, ok := message.GetMessage().(*agentv1.ExecClientControlMessage_Heartbeat); ok {
		return nil
	}
	if _, ok := message.GetMessage().(*agentv1.ExecClientControlMessage_StreamClose); ok {
		return nil
	}
	payload, err := decodePendingPatchEditPayload(pending.ArgsJSON)
	if err != nil {
		return err
	}
	markExecCompleted(stream, pending)
	if strings.TrimSpace(pending.ExecKind) == patchEditPostReadExecKindName {
		return service.finishPatchEditOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload, buildFinalEditSuccessResult(payload.ResolvedPath, payload.AfterContent, patchEditPayloadAsEditPayload(payload)))
	}
	return service.finishPatchEditOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload, buildEditErrorResult(payload.ResolvedPath, hiddenPatchEditControlError(message)))
}

func (service *Service) finishPatchEditOperation(stream *ActiveStream, toolCallID string, modelCallID string, providerPass int, reasoningContent string, payload pendingPatchEditPayload, result *agentv1.EditResult) error {
	if stream == nil {
		return nil
	}
	if result == nil {
		result = buildEditErrorResult(payload.ResolvedPath, "patch edit result missing")
	}
	path := firstNonEmpty(resultPath(result), payload.ResolvedPath, patchEditPayloadPath(payload))
	setPatchEditPayloadPath(&payload, path)
	argsJSON, err := patchEditPayloadArgsJSON(payload)
	if err != nil {
		return err
	}
	toolCall := buildCompletedEditToolCall(path, result)
	historyToolCall := buildCompletedEditToolCall(path, compactPatchEditHistoryEditResult(path, result))
	if err := service.appendToolResult(stream, strings.TrimSpace(toolCallID), patchEditToolName, argsJSON, summarizePatchEditResult(path, result), reasoningContent, historyToolCall); err != nil {
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
	if next, ok := takeNextQueuedPatchEditOperation(stream, firstNonEmpty(payload.ResolvedPath, path)); ok {
		if err := service.startQueuedPatchEditOperation(stream, next); err != nil {
			return err
		}
	}
	return service.reconcileStream(stream)
}

func patchEditQueueKey(path string) string {
	return strings.TrimSpace(path)
}

func patchEditPathHasInFlightOperation(stream *ActiveStream, key string) bool {
	if stream == nil || strings.TrimSpace(key) == "" {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	for _, pending := range stream.PendingExecs {
		if !isHiddenPatchEditExecKind(pending.ExecKind) {
			continue
		}
		payload, err := decodePendingPatchEditPayload(pending.ArgsJSON)
		if err != nil {
			continue
		}
		if patchEditQueueKey(firstNonEmpty(payload.ResolvedPath, patchEditPayloadPath(payload))) == key {
			return true
		}
	}
	return false
}

func enqueuePatchEditOperation(stream *ActiveStream, key string, operation queuedPatchEditOperation) {
	if stream == nil || strings.TrimSpace(key) == "" {
		return
	}
	if len(operation.Payload.RawArgsJSON) > 0 {
		operation.Payload.RawArgsJSON = append(json.RawMessage(nil), operation.Payload.RawArgsJSON...)
	}
	stream.mu.Lock()
	if stream.PatchEditQueues == nil {
		stream.PatchEditQueues = make(map[string][]queuedPatchEditOperation)
	}
	stream.PatchEditQueues[key] = append(stream.PatchEditQueues[key], operation)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}

func takeNextQueuedPatchEditOperation(stream *ActiveStream, path string) (queuedPatchEditOperation, bool) {
	key := patchEditQueueKey(path)
	if stream == nil || key == "" {
		return queuedPatchEditOperation{}, false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.PatchEditQueues == nil {
		return queuedPatchEditOperation{}, false
	}
	queue := stream.PatchEditQueues[key]
	if len(queue) == 0 {
		delete(stream.PatchEditQueues, key)
		return queuedPatchEditOperation{}, false
	}
	next := queue[0]
	if len(queue) == 1 {
		delete(stream.PatchEditQueues, key)
	} else {
		remaining := make([]queuedPatchEditOperation, len(queue)-1)
		copy(remaining, queue[1:])
		stream.PatchEditQueues[key] = remaining
	}
	stream.UpdatedAt = time.Now().UTC()
	return next, true
}

func decodePatchEditArgs(raw []byte) (patchEditArgs, error) {
	var rawArgs map[string]any
	if err := json.Unmarshal(raw, &rawArgs); err != nil {
		return patchEditArgs{}, fmt.Errorf("decode PatchEdit args failed: %w", err)
	}
	path, pathFound, pathValid := readJSONStringAny(rawArgs, "path", "file_path", "Path", "FilePath")
	oldString, oldFound, oldValid := readJSONStringAny(rawArgs, "old_string", "oldString", "OldString")
	newString, newFound, newValid := readJSONStringAny(rawArgs, "new_string", "newString", "NewString")
	replaceAll, replaceAllFound, replaceAllValid := readJSONBoolAny(rawArgs, "replace_all", "replaceAll", "ReplaceAll")
	result := patchEditArgs{
		Path:          path,
		OldString:     oldString,
		NewString:     newString,
		NewStringSet:  newFound,
		ReplaceAll:    replaceAll,
		ReplaceAllSet: replaceAllFound,
	}
	switch {
	case pathFound && !pathValid:
		return result, fmt.Errorf("PatchEdit path must be a string")
	case strings.TrimSpace(result.Path) == "":
		return result, fmt.Errorf("PatchEdit path is required")
	case !isAbsoluteToolPath(result.Path):
		return result, fmt.Errorf("PatchEdit path must be absolute")
	case oldFound && !oldValid:
		return result, fmt.Errorf("PatchEdit old_string must be a string")
	case !oldFound || oldString == "":
		return result, fmt.Errorf("PatchEdit old_string is required")
	case newFound && !newValid:
		return result, fmt.Errorf("PatchEdit new_string must be a string")
	case !newFound:
		return result, fmt.Errorf("PatchEdit new_string is required")
	case replaceAllFound && !replaceAllValid:
		return result, fmt.Errorf("PatchEdit replace_all must be a boolean")
	}
	return result, nil
}

func computePatchEdit(payload pendingPatchEditPayload, beforeContent string) (editComputation, error) {
	args := payload.Args
	occurrences := strings.Count(beforeContent, args.OldString)
	switch {
	case occurrences == 0:
		return editComputation{}, fmt.Errorf("PatchEdit old_string not found")
	case occurrences > 1 && !args.ReplaceAll:
		return editComputation{}, fmt.Errorf("PatchEdit old_string is not unique; found %d occurrences", occurrences)
	}
	replaceCount := 1
	if args.ReplaceAll {
		replaceCount = -1
	}
	afterContent := strings.Replace(beforeContent, args.OldString, args.NewString, replaceCount)
	diffString, linesAdded, linesRemoved := computeEditDiff(beforeContent, afterContent)
	return editComputation{
		BeforeContent: beforeContent,
		AfterContent:  afterContent,
		DiffString:    diffString,
		LinesAdded:    linesAdded,
		LinesRemoved:  linesRemoved,
		Message:       "PatchEdit applied",
	}, nil
}

func extractCompleteReadContentForPatchEdit(result *agentv1.ReadResult) (string, bool, string) {
	success := result.GetSuccess()
	if success == nil {
		return "", false, ""
	}
	if success.GetTruncated() {
		return "", false, "patch edit requires a complete Read result, but the file content was truncated"
	}
	switch output := success.GetOutput().(type) {
	case *agentv1.ReadSuccess_Content:
		return normalizeLineEndingsToLF(output.Content), true, ""
	case *agentv1.ReadSuccess_Data:
		text, ok, readErr := decodeReadDataAsEditableText(output.Data)
		if !ok {
			return "", false, "patch edit requires editable text content, but " + readErr
		}
		return normalizeLineEndingsToLF(text), true, ""
	}
	if success.GetOutputBlobId() != nil {
		return "", false, "patch edit requires inline file content, but Read returned only an output blob"
	}
	return "", false, "patch edit requires inline file content, but Read did not include content"
}

func (service *Service) reconcilePatchEditObservedContent(stream *ActiveStream, toolCallID string, path string, expected string, observed string) (string, bool, bool) {
	if observed == expected {
		return observed, false, true
	}
	expectedNormalized := normalizeLineEndingsToLF(expected)
	observedNormalized := normalizeLineEndingsToLF(observed)
	lineEndingEquivalent := expectedNormalized == observedNormalized
	log.Printf(
		"forwarder patch edit observed content mismatch conversation_id=%s request_id=%s tool_call_id=%s path=%s expected_bytes=%d observed_bytes=%d expected_sha256=%s observed_sha256=%s line_ending_equivalent=%t",
		strings.TrimSpace(stream.ConversationID),
		strings.TrimSpace(stream.RequestID),
		strings.TrimSpace(toolCallID),
		strings.TrimSpace(path),
		len(expected),
		len(observed),
		shortSHA256(expected),
		shortSHA256(observed),
		lineEndingEquivalent,
	)
	if lineEndingEquivalent {
		return observed, true, true
	}
	return observed, false, false
}

func appendPatchEditMessage(existing string, addition string) string {
	existing = strings.TrimSpace(existing)
	addition = strings.TrimSpace(addition)
	if existing == "" {
		return addition
	}
	if addition == "" {
		return existing
	}
	return existing + "; " + addition
}

func patchEditPayloadPath(payload pendingPatchEditPayload) string {
	return strings.TrimSpace(payload.Args.Path)
}

func setPatchEditPayloadPath(payload *pendingPatchEditPayload, path string) {
	if payload == nil {
		return
	}
	payload.Args.Path = strings.TrimSpace(path)
}

func patchEditPayloadArgsJSON(payload pendingPatchEditPayload) ([]byte, error) {
	if payload.ArgsDecodeFailed && len(payload.RawArgsJSON) > 0 {
		return append([]byte(nil), payload.RawArgsJSON...), nil
	}
	return payload.Args.MarshalJSON()
}

func patchEditPayloadAsEditPayload(payload pendingPatchEditPayload) editResultPayload {
	return editResultPayload{
		BeforeContent: payload.BeforeContent,
		AfterContent:  payload.AfterContent,
		DiffString:    payload.DiffString,
		LinesAdded:    payload.LinesAdded,
		LinesRemoved:  payload.LinesRemoved,
		Message:       payload.Message,
	}
}

func summarizePatchEditResult(path string, result *agentv1.EditResult) string {
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

func compactPatchEditHistoryEditResult(path string, result *agentv1.EditResult) *agentv1.EditResult {
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

func boundedPatchEditDiffString(diffString string) string {
	return truncateProjectedReplayText(patchEditToolName, diffString, projectedPatchEditReplayLimit)
}

func (args patchEditArgs) MarshalJSON() ([]byte, error) {
	payload := map[string]any{
		"path":       strings.TrimSpace(args.Path),
		"old_string": args.OldString,
		"new_string": args.NewString,
	}
	if args.ReplaceAllSet || args.ReplaceAll {
		payload["replace_all"] = args.ReplaceAll
	}
	return json.Marshal(payload)
}

func (args *patchEditArgs) UnmarshalJSON(raw []byte) error {
	var rawArgs map[string]any
	if err := json.Unmarshal(raw, &rawArgs); err != nil {
		return err
	}
	path, pathFound, pathValid := readJSONStringAny(rawArgs, "path", "file_path", "Path", "FilePath")
	if pathFound && !pathValid {
		return fmt.Errorf("PatchEdit path must be a string")
	}
	oldString, oldFound, oldValid := readJSONStringAny(rawArgs, "old_string", "oldString", "OldString")
	if oldFound && !oldValid {
		return fmt.Errorf("PatchEdit old_string must be a string")
	}
	newString, newFound, newValid := readJSONStringAny(rawArgs, "new_string", "newString", "NewString")
	if newFound && !newValid {
		return fmt.Errorf("PatchEdit new_string must be a string")
	}
	replaceAll, replaceAllFound, replaceAllValid := readJSONBoolAny(rawArgs, "replace_all", "replaceAll", "ReplaceAll")
	if replaceAllFound && !replaceAllValid {
		return fmt.Errorf("PatchEdit replace_all must be a boolean")
	}
	*args = patchEditArgs{
		Path:          path,
		OldString:     oldString,
		NewString:     newString,
		NewStringSet:  newFound,
		ReplaceAll:    replaceAll,
		ReplaceAllSet: replaceAllFound,
	}
	return nil
}

func (payload pendingPatchEditPayload) MarshalJSON() ([]byte, error) {
	type alias pendingPatchEditPayload
	return json.Marshal(alias(payload))
}

func decodePendingPatchEditPayload(raw []byte) (pendingPatchEditPayload, error) {
	var payload pendingPatchEditPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return pendingPatchEditPayload{}, fmt.Errorf("decode patch edit pending payload failed: %w", err)
	}
	return payload, nil
}

func hiddenPatchEditControlError(message *agentv1.ExecClientControlMessage) string {
	if message == nil {
		return "patch edit operation failed"
	}
	switch item := message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_Throw:
		return firstNonEmpty(strings.TrimSpace(item.Throw.GetError()), "patch edit operation failed")
	case *agentv1.ExecClientControlMessage_StreamClose:
		return "patch edit operation closed unexpectedly"
	default:
		return "patch edit operation failed"
	}
}
