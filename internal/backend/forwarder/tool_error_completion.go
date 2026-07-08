package forwarder

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

type recoverableToolInvocationError struct {
	cause error
}

func (err recoverableToolInvocationError) Error() string {
	if err.cause == nil {
		return "recoverable tool invocation error"
	}
	return err.cause.Error()
}

func (err recoverableToolInvocationError) Unwrap() error {
	return err.cause
}

func newRecoverableToolInvocationError(cause error) error {
	if cause == nil {
		return nil
	}
	return recoverableToolInvocationError{cause: cause}
}

func recoverableToolInvocationCause(err error) (error, bool) {
	if err == nil {
		return nil, false
	}
	var recoverable recoverableToolInvocationError
	if !errors.As(err, &recoverable) {
		return nil, false
	}
	if recoverable.cause == nil {
		return err, true
	}
	return recoverable.cause, true
}

func (service *Service) completePreDispatchToolError(
	stream *ActiveStream,
	invocation runtimecore.ToolInvocation,
	startedToolCall *agentv1.ToolCall,
	startedHistoryAppended bool,
	startedEmitted bool,
	cause error,
) error {
	if stream == nil || cause == nil {
		return cause
	}
	if strings.TrimSpace(invocation.CallID) == "" {
		return cause
	}
	if startedToolCall == nil {
		startedToolCall = buildStartedToolCall(invocation)
	}
	if !startedHistoryAppended && startedToolCall != nil {
		toolCallPayload, err := protojson.Marshal(startedToolCall)
		if err != nil {
			return err
		}
		if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
			newToolCallEntryWithProviderMetadata(stream.TurnSeq, stream.RequestID, invocation.CallID, invocation.ToolName, invocation.ReasoningContent, invocation.ReasoningSignature, invocation.ReasoningSignatureSource, invocation.ReasoningProviderItemID, invocation.ReasoningProviderStatus, invocation.ReasoningProviderSummary, invocation.ProviderItemID, invocation.ProviderCallID, invocation.ProviderStatus, toolCallPayload),
		}); err != nil {
			return err
		}
	}
	if !startedEmitted {
		if err := service.broker.Publish(stream.RequestID, StreamEvent{
			Message: buildToolCallStartedMessage(invocation.CallID, invocation.ModelCallID, startedToolCall),
		}); err != nil {
			return err
		}
	}
	resultText := formatPreDispatchToolError(invocation, cause)
	if err := service.appendToolResult(stream, invocation.CallID, strings.TrimSpace(invocation.ToolName), invocation.ArgsJSON, resultText, invocation.ReasoningContent, nil); err != nil {
		return err
	}
	if err := service.publishToolCallCompleted(stream.RequestID, invocation.CallID, invocation.ModelCallID, nil); err != nil {
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

func formatPreDispatchToolError(invocation runtimecore.ToolInvocation, cause error) string {
	toolName := strings.TrimSpace(invocation.ToolName)
	if toolName == "" {
		toolName = "Tool"
	}
	message := "unknown error"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		message = strings.TrimSpace(cause.Error())
	}
	return fmt.Sprintf("%s error: %s", toolName, message)
}
