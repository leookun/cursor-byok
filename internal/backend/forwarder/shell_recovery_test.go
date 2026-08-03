package forwarder

import (
	"strings"
	"testing"
	"time"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestShellForegroundTimeoutDuration(t *testing.T) {
	testCases := []struct {
		name string
		args string
		want time.Duration
	}{
		{name: "default", args: `{}`, want: 30 * time.Second},
		{name: "explicit foreground timeout", args: `{"block_until_ms":60000}`, want: time.Minute},
		{name: "background immediately", args: `{"block_until_ms":0}`, want: 0},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := shellForegroundTimeoutDuration([]byte(testCase.args)); got != testCase.want {
				t.Fatalf("shellForegroundTimeoutDuration(%s) = %s, want %s", testCase.args, got, testCase.want)
			}
		})
	}
}

func TestInitializePendingShellSetsDeadlineAfterForegroundTimeoutAndGrace(t *testing.T) {
	openedAt := time.Now().UTC().Add(-time.Second)
	pending := initializePendingExecForTracking(runtimecore.PendingExec{
		ExecID:      "exec-shell-1",
		ExecKind:    "shell",
		OpenedAt:    openedAt,
		ArgsJSON:    []byte(`{"block_until_ms":30000}`),
		StreamState: "opened",
	})
	wantDeadline := openedAt.Add(30*time.Second + shellTerminalRecoveryGrace)
	if !pending.ShellForegroundDeadline.Equal(wantDeadline) {
		t.Fatalf("foreground deadline = %s, want %s", pending.ShellForegroundDeadline, wantDeadline)
	}
	if !pending.LastShellActivityAt.Equal(openedAt) {
		t.Fatalf("last shell activity = %s, want %s", pending.LastShellActivityAt, openedAt)
	}
}

func TestShellStreamCloseMarksPendingTransportClosedOnce(t *testing.T) {
	service, stream, pending := testShellRecoveryFixture(t, "opened")
	streamClose := shellStreamClose(pending.MessageID)

	if err := service.handleExecControl(InboundIntent{RequestID: stream.RequestID, ExecClientControlMessage: streamClose}); err != nil {
		t.Fatalf("first stream close error = %v", err)
	}
	current, found := snapshotPendingExec(stream, pending.ExecID)
	if !found || current.StreamState != "transport_closed" || !current.ShellRecoveryScheduled {
		t.Fatalf("first stream close pending = %#v, want transport_closed recovery", current)
	}

	if err := service.handleExecControl(InboundIntent{RequestID: stream.RequestID, ExecClientControlMessage: streamClose}); err != nil {
		t.Fatalf("duplicate stream close error = %v", err)
	}
	current, found = snapshotPendingExec(stream, pending.ExecID)
	if !found || !current.ShellRecoveryScheduled {
		t.Fatalf("duplicate stream close changed pending state = %#v", current)
	}
}

func TestShellExitAfterStreamCloseWinsGracePeriodRecovery(t *testing.T) {
	service, stream, pending := testShellRecoveryFixture(t, "streaming")
	pending.StdoutBuffer = "completed stdout"
	stream.mu.Lock()
	stream.PendingExecs[pending.ExecID] = pending
	stream.mu.Unlock()

	if err := service.handleExecControl(InboundIntent{RequestID: stream.RequestID, ExecClientControlMessage: shellStreamClose(pending.MessageID)}); err != nil {
		t.Fatalf("stream close error = %v", err)
	}
	if err := service.handleExecResult(InboundIntent{RequestID: stream.RequestID, ExecClientMessage: shellExitMessage(pending, 0)}); err != nil {
		t.Fatalf("late exit error = %v", err)
	}
	if _, found := snapshotPendingExec(stream, pending.ExecID); found {
		t.Fatal("late exit left Shell pending")
	}
	if completions := shellCompletionCount(stream, pending.ToolCallID); completions != 1 {
		t.Fatalf("completion count = %d, want 1", completions)
	}
	if !shellHistoryContains(stream, "completed stdout") {
		t.Fatal("terminal Shell output was not persisted")
	}
	if shellHistoryContains(stream, "<shell-incomplete>") {
		t.Fatal("late exit incorrectly persisted synthetic Shell recovery")
	}

	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonTransportClosed); err != nil {
		t.Fatalf("recovery after terminal exit error = %v", err)
	}
	if completions := shellCompletionCount(stream, pending.ToolCallID); completions != 1 {
		t.Fatalf("completion count after recovery = %d, want 1", completions)
	}
}

func TestShellTransportCloseRecoveryCompletesToolExactlyOnce(t *testing.T) {
	service, stream, pending := testShellRecoveryFixture(t, "transport_closed")
	pending.StdoutBuffer = "partial stdout"
	pending.StderrBuffer = "partial stderr"
	stream.mu.Lock()
	stream.PendingExecs[pending.ExecID] = pending
	stream.mu.Unlock()

	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonTransportClosed); err != nil {
		t.Fatalf("transport-close recovery error = %v", err)
	}
	if _, found := snapshotPendingExec(stream, pending.ExecID); found {
		t.Fatal("transport-close recovery left the shell pending")
	}
	if completions := shellCompletionCount(stream, pending.ToolCallID); completions != 1 {
		t.Fatalf("completion count = %d, want 1", completions)
	}
	if !shellHistoryContains(stream, "The shell transport closed before a terminal event arrived.") {
		t.Fatal("synthetic shell result was not persisted")
	}

	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonTransportClosed); err != nil {
		t.Fatalf("duplicate recovery error = %v", err)
	}
	if completions := shellCompletionCount(stream, pending.ToolCallID); completions != 1 {
		t.Fatalf("duplicate recovery completion count = %d, want 1", completions)
	}
}

func TestShellForegroundRecoveryCompletesAfterDeadline(t *testing.T) {
	service, stream, pending := testShellRecoveryFixture(t, "streaming")
	pending.ShellForegroundDeadline = time.Now().UTC().Add(-time.Millisecond)
	stream.mu.Lock()
	stream.PendingExecs[pending.ExecID] = pending
	stream.mu.Unlock()

	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonForegroundDeadline); err != nil {
		t.Fatalf("foreground recovery error = %v", err)
	}
	if _, found := snapshotPendingExec(stream, pending.ExecID); found {
		t.Fatal("foreground recovery left the shell pending")
	}
	if completions := shellCompletionCount(stream, pending.ToolCallID); completions != 1 {
		t.Fatalf("completion count = %d, want 1", completions)
	}
	if !shellHistoryContains(stream, "The foreground wait window expired after 30000ms") {
		t.Fatal("foreground recovery result did not retain timeout reason")
	}
}

func TestShellForegroundRecoveryDoesNotCloseBeforeDeadline(t *testing.T) {
	service, stream, pending := testShellRecoveryFixture(t, "streaming")
	pending.ShellForegroundDeadline = time.Now().UTC().Add(time.Minute)
	stream.mu.Lock()
	stream.PendingExecs[pending.ExecID] = pending
	stream.mu.Unlock()

	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonForegroundDeadline); err != nil {
		t.Fatalf("recovery error = %v", err)
	}
	if _, found := snapshotPendingExec(stream, pending.ExecID); !found {
		t.Fatal("recovery completed shell before its foreground deadline")
	}
}

func TestForegroundRecoveryNeverClosesShellAfterTerminalEvent(t *testing.T) {
	terminalStates := []string{"exited", "backgrounded", "rejected", "permission_denied"}
	for _, state := range terminalStates {
		t.Run(state, func(t *testing.T) {
			service, stream, pending := testShellRecoveryFixture(t, state)
			pending.ShellForegroundDeadline = time.Now().UTC().Add(-time.Second)
			stream.mu.Lock()
			stream.PendingExecs[pending.ExecID] = pending
			stream.mu.Unlock()

			if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonForegroundDeadline); err != nil {
				t.Fatalf("recovery error = %v", err)
			}
			if _, found := snapshotPendingExec(stream, pending.ExecID); !found {
				t.Fatalf("recovery incorrectly completed %q terminal state", state)
			}
		})
	}
}

func TestShellTransportRecoverySkipsTerminalStateAfterStreamClose(t *testing.T) {
	service, stream, pending := testShellRecoveryFixture(t, "transport_closed")
	stream.mu.Lock()
	current := stream.PendingExecs[pending.ExecID]
	current.StreamState = "exited"
	stream.PendingExecs[pending.ExecID] = current
	stream.mu.Unlock()

	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonTransportClosed); err != nil {
		t.Fatalf("transport recovery error = %v", err)
	}
	if _, found := snapshotPendingExec(stream, pending.ExecID); !found {
		t.Fatal("transport recovery incorrectly completed a late terminal state")
	}
	if completions := shellCompletionCount(stream, pending.ToolCallID); completions != 0 {
		t.Fatalf("completion count = %d, want 0 before terminal result handling", completions)
	}
}

func TestShellLateControlAndResultAfterRecoveryAreIgnored(t *testing.T) {
	service, stream, pending := testShellRecoveryFixture(t, "transport_closed")
	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonTransportClosed); err != nil {
		t.Fatalf("recovery error = %v", err)
	}

	if err := service.handleExecControl(InboundIntent{RequestID: stream.RequestID, ExecClientControlMessage: shellStreamClose(pending.MessageID)}); err != nil {
		t.Fatalf("late stream close error = %v, want ignored", err)
	}
	if err := service.handleExecResult(InboundIntent{RequestID: stream.RequestID, ExecClientMessage: &agentv1.ExecClientMessage{Id: pending.MessageID, ExecId: pending.ExecID}}); err != nil {
		t.Fatalf("late shell result error = %v, want ignored", err)
	}
	if completions := shellCompletionCount(stream, pending.ToolCallID); completions != 1 {
		t.Fatalf("completion count after late events = %d, want 1", completions)
	}
}

func testShellRecoveryFixture(t *testing.T, state string) (*Service, *ActiveStream, runtimecore.PendingExec) {
	t.Helper()
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request-shell-1", "conversation-shell-1", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "shell test")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	service := &Service{
		broker:     broker,
		projector:  NewHistoryProjector(),
		execBridge: execbridge.NewBridge(),
	}
	if err := service.replaceCheckpointConversation(stream, testConversation(nil)); err != nil {
		t.Fatalf("replaceCheckpointConversation() error = %v", err)
	}
	pending := initializePendingExecForTracking(runtimecore.PendingExec{
		MessageID:   71,
		ExecID:      "exec-shell-71",
		ExecKind:    "shell",
		ToolCallID:  "tool-shell-71",
		ModelCallID: "model-call-71",
		ArgsJSON:    []byte(`{"command":"go test ./...","block_until_ms":30000}`),
		OpenedAt:    time.Now().UTC().Add(-time.Second),
		StreamState: state,
	})
	stream.mu.Lock()
	stream.PendingExecs[pending.ExecID] = pending
	stream.mu.Unlock()
	return service, stream, pending
}

func shellStreamClose(messageID uint32) *agentv1.ExecClientControlMessage {
	return &agentv1.ExecClientControlMessage{
		Message: &agentv1.ExecClientControlMessage_StreamClose{
			StreamClose: &agentv1.ExecClientStreamClose{Id: messageID},
		},
	}
}

func shellExitMessage(pending runtimecore.PendingExec, exitCode uint32) *agentv1.ExecClientMessage {
	return &agentv1.ExecClientMessage{
		Id:     pending.MessageID,
		ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_ShellStream{
			ShellStream: &agentv1.ShellStream{
				Event: &agentv1.ShellStream_Exit{
					Exit: &agentv1.ShellStreamExit{Code: exitCode},
				},
			},
		},
	}
}

func shellCompletionCount(stream *ActiveStream, toolCallID string) int {
	if stream == nil {
		return 0
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	count := 0
	for _, event := range stream.Backlog {
		completed := event.Message.GetInteractionUpdate().GetToolCallCompleted()
		if completed != nil && completed.GetCallId() == toolCallID {
			count++
		}
	}
	return count
}

func shellHistoryContains(stream *ActiveStream, needle string) bool {
	if stream == nil {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.CheckpointConversation == nil {
		return false
	}
	for _, entry := range stream.CheckpointConversation.Entries {
		if strings.Contains(string(entry.Payload), needle) {
			return true
		}
	}
	return false
}
