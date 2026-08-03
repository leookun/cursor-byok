package forwarder

import (
	"strings"
	"testing"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestAwaitShellSnapshotReturnsOnlyUnreadOutput(t *testing.T) {
	service, stream, _ := testShellRecoveryFixture(t, "opened")
	stream.mu.Lock()
	stream.BackgroundShells["42"] = &BackgroundShellState{
		ShellID:      "42",
		Status:       backgroundShellStatusRunning,
		StdoutBuffer: "first stdout\n",
		StderrBuffer: "first stderr\n",
		CreatedAt:    time.Now().UTC().Add(-time.Second),
	}
	stream.mu.Unlock()

	first := service.awaitShellSnapshot(stream, awaitShellArgs{ShellID: "42"})
	if first.Stdout != "first stdout\n" || first.Stderr != "first stderr\n" {
		t.Fatalf("first AwaitShell output = stdout %q stderr %q", first.Stdout, first.Stderr)
	}

	stream.mu.Lock()
	state := stream.BackgroundShells["42"]
	state.StdoutBuffer += "second stdout\n"
	state.StderrBuffer += "second stderr\n"
	stream.mu.Unlock()

	second := service.awaitShellSnapshot(stream, awaitShellArgs{ShellID: "42"})
	if second.Stdout != "second stdout\n" || second.Stderr != "second stderr\n" {
		t.Fatalf("second AwaitShell output = stdout %q stderr %q, want only new output", second.Stdout, second.Stderr)
	}
}

func TestAwaitShellSnapshotHandlesPatternsAndStates(t *testing.T) {
	testCases := []struct {
		name        string
		shellID     string
		status      string
		pattern     string
		stdout      string
		wantStatus  string
		wantMatched bool
		wantTimeout bool
		wantError   string
	}{
		{name: "regex match", shellID: "match", status: backgroundShellStatusRunning, pattern: `ready\s+now`, stdout: "ready now", wantStatus: backgroundShellStatusRunning, wantMatched: true},
		{name: "invalid regex", shellID: "invalid", status: backgroundShellStatusRunning, pattern: `[`, wantStatus: backgroundShellStatusRunning, wantTimeout: true, wantError: "invalid AwaitShell pattern"},
		{name: "unknown shell", shellID: "missing", wantStatus: backgroundShellStatusUnknown},
		{name: "backgrounded shell", shellID: "backgrounded", status: backgroundShellStatusBackgrounded, wantStatus: backgroundShellStatusBackgrounded, wantTimeout: true},
		{name: "completed shell", shellID: "completed", status: backgroundShellStatusCompleted, wantStatus: backgroundShellStatusCompleted},
		{name: "rejected shell", shellID: "rejected", status: backgroundShellStatusRejected, wantStatus: backgroundShellStatusRejected},
		{name: "permission denied shell", shellID: "permission", status: backgroundShellStatusPermissionDenied, wantStatus: backgroundShellStatusPermissionDenied},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service, stream, _ := testShellRecoveryFixture(t, "opened")
			if testCase.status != "" {
				stream.mu.Lock()
				stream.BackgroundShells[testCase.shellID] = &BackgroundShellState{
					ShellID:      testCase.shellID,
					Status:       testCase.status,
					StdoutBuffer: testCase.stdout,
					CreatedAt:    time.Now().UTC().Add(time.Second),
				}
				stream.mu.Unlock()
			}

			result := service.awaitShellSnapshot(stream, awaitShellArgs{ShellID: testCase.shellID, Pattern: testCase.pattern})
			if result.Status != testCase.wantStatus || result.Matched != testCase.wantMatched || result.TimedOut != testCase.wantTimeout {
				t.Fatalf("AwaitShell result = %#v, want status=%q matched=%t timed_out=%t", result, testCase.wantStatus, testCase.wantMatched, testCase.wantTimeout)
			}
			if testCase.wantError != "" && !strings.Contains(result.Message, testCase.wantError) {
				t.Fatalf("AwaitShell message = %q, want %q", result.Message, testCase.wantError)
			}
		})
	}
}

func TestWriteShellStdinTerminalErrorCompletesToolOnce(t *testing.T) {
	service, stream, _ := testShellRecoveryFixture(t, "opened")
	pending := initializePendingExecForTracking(runtimecore.PendingExec{
		MessageID:   91,
		ExecID:      "exec-write-stdin-91",
		ExecKind:    "write_shell_stdin",
		ToolCallID:  "tool-write-stdin-91",
		ModelCallID: "model-call-91",
		ArgsJSON:    []byte(`{"shell_id":42,"chars":"exit\\n"}`),
		OpenedAt:    time.Now().UTC(),
		StreamState: "opened",
	})
	stream.mu.Lock()
	stream.BackgroundShells["42"] = &BackgroundShellState{ShellID: "42", Status: backgroundShellStatusCompleted, CreatedAt: time.Now().UTC().Add(-time.Second)}
	stream.PendingExecs = map[string]runtimecore.PendingExec{pending.ExecID: pending}
	stream.mu.Unlock()
	terminalError := &agentv1.ExecClientMessage{
		Id:     pending.MessageID,
		ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_WriteShellStdinResult{
			WriteShellStdinResult: &agentv1.WriteShellStdinResult{
				Result: &agentv1.WriteShellStdinResult_Error{Error: &agentv1.WriteShellStdinError{Error: "shell 42 has already exited"}},
			},
		},
	}

	if err := service.handleExecResult(InboundIntent{RequestID: stream.RequestID, ExecClientMessage: terminalError}); err != nil {
		t.Fatalf("terminal stdin error = %v", err)
	}
	if _, found := snapshotPendingExec(stream, pending.ExecID); found {
		t.Fatal("terminal stdin error left the tool pending")
	}
	if completions := shellCompletionCount(stream, pending.ToolCallID); completions != 1 {
		t.Fatalf("completion count = %d, want 1", completions)
	}
	if !shellHistoryContains(stream, "shell 42 has already exited") {
		t.Fatal("terminal stdin error was not persisted")
	}

	if err := service.handleExecResult(InboundIntent{RequestID: stream.RequestID, ExecClientMessage: terminalError}); err != nil {
		t.Fatalf("duplicate terminal stdin error = %v, want ignored", err)
	}
	if completions := shellCompletionCount(stream, pending.ToolCallID); completions != 1 {
		t.Fatalf("duplicate terminal stdin error emitted %d completions, want 1", completions)
	}
}

func TestForceBackgroundShellDuplicateResultCompletesOnce(t *testing.T) {
	service, stream, _ := testShellRecoveryFixture(t, "opened")
	pending := initializePendingExecForTracking(runtimecore.PendingExec{
		MessageID:   92,
		ExecID:      "exec-force-background-92",
		ExecKind:    "force_background_shell",
		ToolCallID:  "tool-force-background-92",
		ModelCallID: "model-call-92",
		ArgsJSON:    []byte(`{"tool_call_id":"tool-shell-42"}`),
		OpenedAt:    time.Now().UTC(),
		StreamState: "opened",
	})
	stream.mu.Lock()
	stream.PendingExecs = map[string]runtimecore.PendingExec{pending.ExecID: pending}
	stream.mu.Unlock()
	result := &agentv1.ExecClientMessage{
		Id:     pending.MessageID,
		ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_ForceBackgroundShellResult{
			ForceBackgroundShellResult: &agentv1.ForceBackgroundShellResult{
				ShellResult: &agentv1.ShellResult{
					Result: &agentv1.ShellResult_Success{Success: &agentv1.ShellSuccess{}},
				},
			},
		},
	}

	if err := service.handleExecResult(InboundIntent{RequestID: stream.RequestID, ExecClientMessage: result}); err != nil {
		t.Fatalf("force background result = %v", err)
	}
	if _, found := snapshotPendingExec(stream, pending.ExecID); found {
		t.Fatal("force background result left the tool pending")
	}
	if completions := shellCompletionCount(stream, pending.ToolCallID); completions != 1 {
		t.Fatalf("completion count = %d, want 1", completions)
	}

	if err := service.handleExecResult(InboundIntent{RequestID: stream.RequestID, ExecClientMessage: result}); err != nil {
		t.Fatalf("duplicate force background result = %v, want ignored", err)
	}
	if completions := shellCompletionCount(stream, pending.ToolCallID); completions != 1 {
		t.Fatalf("duplicate force background result emitted %d completions, want 1", completions)
	}
}
