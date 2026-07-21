// exec_control.go 提取自 service.go：exec 控制面 stream_close/throw 处理与非流式 exec 恢复。
package forwarder

import (
	"fmt"
	"strings"
	"time"

	"cursor/internal/logger"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func shouldRecoverNonStreamingExecOnStreamClose(message *agentv1.ExecClientControlMessage, pending runtimecore.PendingExec) bool {
	if message == nil || isStreamingPendingExecKind(pending.ExecKind) {
		return false
	}
	switch message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_StreamClose:
		return true
	default:
		return false
	}
}

func shouldObserveShellStreamClose(message *agentv1.ExecClientControlMessage, pending runtimecore.PendingExec) bool {
	if message == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return false
	}
	switch message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_StreamClose:
		return true
	default:
		return false
	}
}

func isStreamingPendingExecKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "shell":
		return true
	default:
		return false
	}
}

func markExecTransportClosed(stream *ActiveStream, pending runtimecore.PendingExec) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	current, ok := stream.PendingExecs[pending.ExecID]
	if ok {
		now := time.Now().UTC()
		current.StreamState = "transport_closed"
		current.LastShellActivityAt = now
		stream.PendingExecs[pending.ExecID] = current
		stream.UpdatedAt = now
	}
	stream.mu.Unlock()
}

func snapshotPendingExec(stream *ActiveStream, execID string) (runtimecore.PendingExec, bool) {
	if stream == nil || strings.TrimSpace(execID) == "" {
		return runtimecore.PendingExec{}, false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	item, ok := stream.PendingExecs[strings.TrimSpace(execID)]
	return item, ok
}

func (service *Service) scheduleNonStreamingExecRecovery(requestID string, pending runtimecore.PendingExec) {
	if service == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(pending.ExecID) == "" {
		return
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return
	}
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerNonStreamingRecovery, pending.ExecID),
		nonStreamingExecCloseGrace,
		streamTimerNonStreamingRecovery,
		pending.ExecID,
		pending.MessageID,
		"",
	)
}

func (service *Service) recoverNonStreamingExecAfterStreamClose(stream *ActiveStream, pending runtimecore.PendingExec) error {
	if stream == nil {
		return nil
	}
	markExecCompleted(stream, pending)
	toolName := strings.TrimSpace(deriveToolNameFromPendingExec(pending))
	resultPayload := fmt.Sprintf("%s transport closed before terminal result arrived", firstNonEmpty(toolName, pending.ExecKind, "tool"))
	logger.Infof("forwarder synthetic exec recovery request_id=%s tool_call_id=%s message_id=%d exec_id=%s exec_kind=%s", strings.TrimSpace(stream.RequestID), strings.TrimSpace(pending.ToolCallID), pending.MessageID, strings.TrimSpace(pending.ExecID), strings.TrimSpace(pending.ExecKind))
	if toolName != "" {
		if err := service.appendToolResult(stream, pending.ToolCallID, toolName, pending.ArgsJSON, resultPayload, pending.ReasoningContent, nil); err != nil {
			return err
		}
	}
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "tool_transport_closed", map[string]any{
			"tool_call_id": pending.ToolCallID,
			"message_id":   pending.MessageID,
			"exec_id":      pending.ExecID,
			"exec_kind":    pending.ExecKind,
			"payload":      resultPayload,
		}),
	}); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, stream.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishToolCallCompleted(stream.RequestID, pending.ToolCallID, pending.ModelCallID, nil); err != nil {
		return err
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}

func (service *Service) observeShellStreamClose(stream *ActiveStream, pending runtimecore.PendingExec) {
	if service == nil || stream == nil {
		return
	}
	current, ok := snapshotPendingExec(stream, pending.ExecID)
	if !ok {
		return
	}
	recentState := strings.TrimSpace(current.StreamState)
	if recentState == "transport_closed" || recentState == "exited" || recentState == "backgrounded" || recentState == "rejected" || recentState == "permission_denied" {
		return
	}
	logger.Infof(
		"forwarder shell stream closed without terminal event request_id=%s tool_call_id=%s message_id=%d exec_id=%s stream_state=%s chunk_count=%d",
		strings.TrimSpace(stream.RequestID),
		strings.TrimSpace(current.ToolCallID),
		current.MessageID,
		strings.TrimSpace(current.ExecID),
		recentState,
		current.ChunkCount,
	)
	markExecTransportClosed(stream, current)
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "shell_stream_transport_closed", map[string]any{
			"tool_call_id":        current.ToolCallID,
			"message_id":          current.MessageID,
			"exec_id":             current.ExecID,
			"exec_kind":           current.ExecKind,
			"recent_stream_state": recentState,
			"chunk_count":         current.ChunkCount,
			"first_chunk_at":      current.FirstChunkAt,
			"reasoning_present":   strings.TrimSpace(current.ReasoningContent) != "",
			"stdout_buffer_bytes": len(current.StdoutBuffer),
			"stderr_buffer_bytes": len(current.StderrBuffer),
		}),
	}); err != nil {
		logger.Infof("forwarder shell stream close metadata failed request_id=%s tool_call_id=%s err=%v", strings.TrimSpace(stream.RequestID), strings.TrimSpace(current.ToolCallID), err)
	}
	service.scheduleShellTransportCloseRecovery(stream.RequestID, current)
}
