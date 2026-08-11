package forwarder

import (
	"testing"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestHandleExecResultPublishesShellToolCallDelta(t *testing.T) {
	tests := []struct {
		name        string
		shellStream func() *agentv1.ShellStream
		wantStdout  string
		wantStderr  string
	}{
		{
			name: "stdout",
			shellStream: func() *agentv1.ShellStream {
				return &agentv1.ShellStream{Event: &agentv1.ShellStream_Stdout{
					Stdout: &agentv1.ShellStreamStdout{Data: "stdout chunk\n"},
				}}
			},
			wantStdout: "stdout chunk\n",
		},
		{
			name: "stderr",
			shellStream: func() *agentv1.ShellStream {
				return &agentv1.ShellStream{Event: &agentv1.ShellStream_Stderr{
					Stderr: &agentv1.ShellStreamStderr{Data: "stderr chunk\n"},
				}}
			},
			wantStderr: "stderr chunk\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broker := NewStreamBroker()
			service := &Service{
				broker:     broker,
				execBridge: execbridge.NewBridge(),
			}
			stream, err := broker.OpenStream(
				"request-1", "conversation-1", 1, "default", "default",
				agentv1.AgentMode_AGENT_MODE_AGENT, "run command",
			)
			if err != nil {
				t.Fatalf("OpenStream() error = %v", err)
			}
			pending := runtimecore.PendingExec{
				MessageID:   42,
				ExecID:      "exec-shell-1",
				ModelCallID: "model-call-1",
				ToolCallID:  "tool-call-1",
				ExecKind:    "shell",
			}
			stream.mu.Lock()
			stream.PendingExecs[pending.ExecID] = pending
			stream.mu.Unlock()

			if err := service.handleExecResult(InboundIntent{
				Kind:      "exec_result",
				RequestID: "request-1",
				ExecClientMessage: &agentv1.ExecClientMessage{
					Id:     pending.MessageID,
					ExecId: pending.ExecID,
					Message: &agentv1.ExecClientMessage_ShellStream{
						ShellStream: test.shellStream(),
					},
				},
			}); err != nil {
				t.Fatalf("handleExecResult() error = %v", err)
			}

			events, err := broker.ReadFromCursor("request-1", 0)
			if err != nil {
				t.Fatalf("ReadFromCursor() error = %v", err)
			}
			if len(events) != 2 {
				t.Fatalf("published events = %d, want compatibility and tool-call deltas", len(events))
			}

			var compatibilityCount, toolCallDeltaCount int
			for _, event := range events {
				update := event.Message.GetInteractionUpdate()
				if update.GetShellOutputDelta() != nil {
					compatibilityCount++
				}
				deltaUpdate := update.GetToolCallDelta()
				if deltaUpdate == nil {
					continue
				}
				toolCallDeltaCount++
				if deltaUpdate.GetCallId() != pending.ToolCallID || deltaUpdate.GetModelCallId() != pending.ModelCallID {
					t.Fatalf("tool-call delta ids = call %q model %q", deltaUpdate.GetCallId(), deltaUpdate.GetModelCallId())
				}
				shellDelta := deltaUpdate.GetToolCallDelta().GetShellToolCallDelta()
				if shellDelta == nil || shellDelta.GetStdout().GetContent() != test.wantStdout || shellDelta.GetStderr().GetContent() != test.wantStderr {
					t.Fatalf("shell tool-call delta = %#v", shellDelta)
				}
			}
			if compatibilityCount != 1 || toolCallDeltaCount != 1 {
				t.Fatalf("published compatibility=%d tool_call_delta=%d, want one each", compatibilityCount, toolCallDeltaCount)
			}
		})
	}
}

func TestBuildShellToolCallDeltaMessageIgnoresNonOutputEvents(t *testing.T) {
	tests := []struct {
		name   string
		output *agentv1.ShellOutputDeltaUpdate
	}{
		{name: "nil"},
		{
			name: "start",
			output: &agentv1.ShellOutputDeltaUpdate{Event: &agentv1.ShellOutputDeltaUpdate_Start{
				Start: &agentv1.ShellStreamStart{},
			}},
		},
		{
			name: "exit",
			output: &agentv1.ShellOutputDeltaUpdate{Event: &agentv1.ShellOutputDeltaUpdate_Exit{
				Exit: &agentv1.ShellStreamExit{},
			}},
		},
		{
			name: "empty stdout",
			output: &agentv1.ShellOutputDeltaUpdate{Event: &agentv1.ShellOutputDeltaUpdate_Stdout{
				Stdout: &agentv1.ShellStreamStdout{},
			}},
		},
		{
			name: "empty stderr",
			output: &agentv1.ShellOutputDeltaUpdate{Event: &agentv1.ShellOutputDeltaUpdate_Stderr{
				Stderr: &agentv1.ShellStreamStderr{},
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if message := buildShellToolCallDeltaMessage("tool-call-1", "model-call-1", test.output); message != nil {
				t.Fatalf("buildShellToolCallDeltaMessage() = %#v, want nil", message)
			}
		})
	}
}
