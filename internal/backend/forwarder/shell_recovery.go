package forwarder

import (
	"fmt"
	"log"
	"strings"
	"time"

	runtimecore "cursor/internal/backend/agent/core"
)

const shellTerminalRecoveryGrace = 1500 * time.Millisecond

const (
	shellRecoveryReasonForegroundDeadline = "foreground_deadline_exceeded"
	shellRecoveryReasonTransportClosed    = "transport_closed_without_terminal"
)

func initializePendingExecForTracking(pending runtimecore.PendingExec) runtimecore.PendingExec {
	if strings.TrimSpace(pending.ExecKind) != "shell" {
		return pending
	}
	now := time.Now().UTC()
	if pending.OpenedAt.IsZero() {
		pending.OpenedAt = now
	}
	if pending.LastShellActivityAt.IsZero() {
		pending.LastShellActivityAt = pending.OpenedAt
	}
	if pending.ShellForegroundDeadline.IsZero() {
		pending.ShellForegroundDeadline = pending.OpenedAt.Add(shellForegroundTimeoutDuration(pending.ArgsJSON) + shellTerminalRecoveryGrace)
	}
	return pending
}

func shellForegroundTimeoutDuration(argsJSON []byte) time.Duration {
	timeoutMS := int64(30000)
	args, err := runtimecore.DecodeArgsMap(argsJSON)
	if err == nil {
		if blockUntilMS, found, err := runtimecore.ReadFloat64Arg(args, "block_until_ms", "blockUntilMS"); err == nil && found {
			if blockUntilMS <= 0 {
				return 0
			}
			timeoutMS = int64(blockUntilMS)
		}
	}
	return time.Duration(timeoutMS) * time.Millisecond
}

func shellForegroundTimeoutMS(argsJSON []byte) int64 {
	return shellForegroundTimeoutDuration(argsJSON).Milliseconds()
}

func (service *Service) scheduleShellForegroundRecovery(requestID string, pending runtimecore.PendingExec) {
	if service == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(pending.ExecKind) != "shell" || strings.TrimSpace(pending.ExecID) == "" {
		return
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return
	}
	deadline := pending.ShellForegroundDeadline
	if deadline.IsZero() {
		deadline = time.Now().UTC().Add(shellForegroundTimeoutDuration(pending.ArgsJSON) + shellTerminalRecoveryGrace)
	}
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerShellForeground, pending.ExecID),
		time.Until(deadline),
		streamTimerShellForeground,
		pending.ExecID,
		pending.MessageID,
		shellRecoveryReasonForegroundDeadline,
	)
}

func (service *Service) scheduleShellTransportCloseRecovery(requestID string, pending runtimecore.PendingExec) {
	if service == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(pending.ExecKind) != "shell" || strings.TrimSpace(pending.ExecID) == "" {
		return
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return
	}
	if _, scheduled := markShellRecoveryScheduled(stream, pending.ExecID); !scheduled {
		return
	}
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerShellTransportClose, pending.ExecID),
		shellTerminalRecoveryGrace,
		streamTimerShellTransportClose,
		pending.ExecID,
		pending.MessageID,
		shellRecoveryReasonTransportClosed,
	)
}

func snapshotPendingExecWithStatus(stream *ActiveStream, execID string) (runtimecore.PendingExec, StreamStatus, bool) {
	if stream == nil || strings.TrimSpace(execID) == "" {
		return runtimecore.PendingExec{}, "", false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	item, ok := stream.PendingExecs[strings.TrimSpace(execID)]
	if !ok {
		return runtimecore.PendingExec{}, stream.Status, false
	}
	return item, stream.Status, true
}

func markShellRecoveryScheduled(stream *ActiveStream, execID string) (runtimecore.PendingExec, bool) {
	if stream == nil || strings.TrimSpace(execID) == "" {
		return runtimecore.PendingExec{}, false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	current, ok := stream.PendingExecs[strings.TrimSpace(execID)]
	if !ok || strings.TrimSpace(current.ExecKind) != "shell" || current.ShellRecoveryScheduled {
		return current, false
	}
	current.ShellRecoveryScheduled = true
	stream.PendingExecs[strings.TrimSpace(execID)] = current
	stream.UpdatedAt = time.Now().UTC()
	return current, true
}

func (service *Service) recoverShellWithoutTerminalIfNeeded(stream *ActiveStream, execID string, messageID uint32, reason string) error {
	if stream == nil || strings.TrimSpace(execID) == "" {
		return nil
	}
	current, status, found := snapshotPendingExecWithStatus(stream, execID)
	if !found || current.MessageID != messageID || strings.TrimSpace(current.ExecKind) != "shell" || isTerminalStreamStatus(status) {
		return nil
	}
	switch strings.TrimSpace(current.StreamState) {
	case "exited", "backgrounded", "rejected", "permission_denied":
		return nil
	}
	if reason == shellRecoveryReasonForegroundDeadline && !current.ShellForegroundDeadline.IsZero() && time.Now().UTC().Before(current.ShellForegroundDeadline) {
		return nil
	}
	return service.recoverShellWithoutTerminal(stream, current, reason)
}

func (service *Service) recoverShellWithoutTerminal(stream *ActiveStream, pending runtimecore.PendingExec, reason string) error {
	if stream == nil {
		return nil
	}
	pending.ShellRecoveryScheduled = true
	markExecCompleted(stream, pending)
	resultPayload := buildSyntheticShellResultPayload(pending, reason)
	log.Printf(
		"forwarder synthetic shell recovery request_id=%s tool_call_id=%s message_id=%d exec_id=%s reason=%s stream_state=%s",
		strings.TrimSpace(stream.RequestID),
		strings.TrimSpace(pending.ToolCallID),
		pending.MessageID,
		strings.TrimSpace(pending.ExecID),
		strings.TrimSpace(reason),
		strings.TrimSpace(pending.StreamState),
	)
	if err := service.appendToolResult(stream, pending.ToolCallID, deriveToolNameFromPendingExec(pending), pending.ArgsJSON, resultPayload, pending.ReasoningContent, nil); err != nil {
		return err
	}
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "shell_stream_stalled", map[string]any{
			"tool_call_id":             pending.ToolCallID,
			"message_id":               pending.MessageID,
			"exec_id":                  pending.ExecID,
			"exec_kind":                pending.ExecKind,
			"reason":                   strings.TrimSpace(reason),
			"recent_stream_state":      pending.StreamState,
			"chunk_count":              pending.ChunkCount,
			"first_chunk_at":           pending.FirstChunkAt,
			"last_activity_at":         pending.LastShellActivityAt,
			"last_heartbeat_at":        pending.LastShellHeartbeatAt,
			"foreground_deadline":      pending.ShellForegroundDeadline,
			"timeout_ms":               shellForegroundTimeoutMS(pending.ArgsJSON),
			"stdout_buffer_bytes":      len(pending.StdoutBuffer),
			"stderr_buffer_bytes":      len(pending.StderrBuffer),
			"shell_recovery_scheduled": pending.ShellRecoveryScheduled,
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

func buildSyntheticShellResultPayload(pending runtimecore.PendingExec, reason string) string {
	sections := make([]string, 0, 2)
	if captured := summarizeCapturedShellOutput(pending.StdoutBuffer, pending.StderrBuffer); captured != "" {
		sections = append(sections, captured)
	}
	noteLines := []string{
		"<shell-incomplete>",
		"Missing terminal shell stream event (expected exit or backgrounded).",
	}
	switch strings.TrimSpace(reason) {
	case shellRecoveryReasonTransportClosed:
		noteLines = append(noteLines, "The shell transport closed before a terminal event arrived.")
	case shellRecoveryReasonForegroundDeadline:
		noteLines = append(noteLines, fmt.Sprintf("The foreground wait window expired after %dms without a terminal event.", shellForegroundTimeoutMS(pending.ArgsJSON)))
	default:
		noteLines = append(noteLines, "The shell stream stopped progressing before a terminal event arrived.")
	}
	noteLines = append(noteLines,
		"The command may still be running in the Cursor app client.",
		"</shell-incomplete>",
	)
	sections = append(sections, strings.Join(noteLines, "\n"))
	return strings.Join(sections, "\n\n")
}

func summarizeCapturedShellOutput(stdout string, stderr string) string {
	trimmedStdout := strings.TrimSpace(stdout)
	trimmedStderr := strings.TrimSpace(stderr)
	sections := make([]string, 0, 2)
	if trimmedStdout != "" {
		sections = append(sections, trimmedStdout)
	}
	if trimmedStderr != "" {
		if trimmedStdout != "" {
			sections = append(sections, "<stderr>\n"+trimmedStderr+"\n</stderr>")
		} else {
			sections = append(sections, trimmedStderr)
		}
	}
	return strings.Join(sections, "\n\n")
}
