package forwarder

import (
	"encoding/json"
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

type hiddenEditLifecycleTool struct {
	name         string
	execKinds    []string
	control      func(*Service, *ActiveStream, runtimecore.PendingExec, *agentv1.ExecClientControlMessage) error
	marshalState func() ([]byte, error)
}

func TestHiddenEditControlBehavior(t *testing.T) {
	for _, tool := range hiddenEditLifecycleTools() {
		for _, kind := range tool.execKinds {
			t.Run(tool.name+"/heartbeat/"+kind, func(t *testing.T) {
				service, stream, pending := testHiddenEditControlFixture(t, kind, tool.marshalState)
				heartbeat := &agentv1.ExecClientControlMessage{
					Message: &agentv1.ExecClientControlMessage_Heartbeat{
						Heartbeat: &agentv1.ExecClientHeartbeat{Id: pending.MessageID},
					},
				}
				if err := tool.control(service, stream, pending, heartbeat); err != nil {
					t.Fatalf("heartbeat error = %v", err)
				}
				assertHiddenEditPending(t, stream, pending.ExecID, true)
			})

			t.Run(tool.name+"/throw/"+kind, func(t *testing.T) {
				service, stream, pending := testHiddenEditControlFixture(t, kind, tool.marshalState)
				throw := &agentv1.ExecClientControlMessage{
					Message: &agentv1.ExecClientControlMessage_Throw{
						Throw: &agentv1.ExecClientThrow{Id: pending.MessageID, Error: "transport failed"},
					},
				}
				if err := tool.control(service, stream, pending, throw); err != nil {
					t.Fatalf("throw error = %v", err)
				}
				assertHiddenEditPending(t, stream, pending.ExecID, false)
				assertHiddenEditCompletion(t, stream, pending.ToolCallID)
			})
		}
	}
}

func TestHiddenEditStreamCloseSchedulesGracefulRecovery(t *testing.T) {
	for _, tool := range hiddenEditLifecycleTools() {
		for _, kind := range tool.execKinds {
			t.Run(tool.name+"/"+kind, func(t *testing.T) {
				service, stream, pending := testHiddenEditControlFixture(t, kind, tool.marshalState)
				streamClose := &agentv1.ExecClientControlMessage{
					Message: &agentv1.ExecClientControlMessage_StreamClose{
						StreamClose: &agentv1.ExecClientStreamClose{Id: pending.MessageID},
					},
				}
				if err := service.handleExecControl(InboundIntent{RequestID: stream.RequestID, ExecClientControlMessage: streamClose}); err != nil {
					t.Fatalf("handleExecControl(stream_close) error = %v", err)
				}
				current, found := snapshotPendingExec(stream, pending.ExecID)
				if !found || current.StreamState != "transport_closed" {
					t.Fatalf("stream close state = %#v, want pending transport_closed exec", current)
				}
			})
		}
	}
}

func TestHiddenEditStreamCloseRecoveryCompletesVisibleTool(t *testing.T) {
	for _, tool := range hiddenEditLifecycleTools() {
		for _, kind := range tool.execKinds {
			t.Run(tool.name+"/"+kind, func(t *testing.T) {
				service, stream, pending := testHiddenEditControlFixture(t, kind, tool.marshalState)
				markExecTransportClosed(stream, pending)
				if err := service.recoverNonStreamingExecAfterStreamClose(stream, pending); err != nil {
					t.Fatalf("recoverNonStreamingExecAfterStreamClose() error = %v", err)
				}
				assertHiddenEditPending(t, stream, pending.ExecID, false)
				assertHiddenEditCompletion(t, stream, pending.ToolCallID)
			})
		}
	}
}

func TestHiddenEditLateResultAfterRecoveryIsIgnored(t *testing.T) {
	tools := hiddenEditLifecycleTools()
	for _, tool := range tools {
		t.Run(tool.name, func(t *testing.T) {
			service, stream, pending := testHiddenEditControlFixture(t, tool.execKinds[1], tool.marshalState)
			markExecTransportClosed(stream, pending)
			if err := service.recoverNonStreamingExecAfterStreamClose(stream, pending); err != nil {
				t.Fatalf("recover hidden edit: %v", err)
			}
			lateResult := &agentv1.ExecClientMessage{Id: pending.MessageID, ExecId: pending.ExecID}
			if err := service.handleExecResult(InboundIntent{RequestID: stream.RequestID, ExecClientMessage: lateResult}); err != nil {
				t.Fatalf("late result error = %v, want ignored tombstone", err)
			}
			if completions := hiddenEditCompletionCount(stream, pending.ToolCallID); completions != 1 {
				t.Fatalf("completion count after late result = %d, want 1", completions)
			}
		})
	}
}

func hiddenEditLifecycleTools() []hiddenEditLifecycleTool {
	return []hiddenEditLifecycleTool{
		{
			name:      "Write",
			execKinds: []string{writeReadExecKind, writeWriteExecKind, writePostReadExecKind},
			control:   (*Service).handleHiddenWriteExecControl,
			marshalState: func() ([]byte, error) {
				return (pendingWritePayload{
					VisibleArgs:   writeOperationArgs{Path: `C:\\workspace\\file.go`, Contents: "after"},
					ResolvedPath:  `C:\\workspace\\file.go`,
					BeforeContent: "before",
					AfterContent:  "after",
				}).MarshalJSON()
			},
		},
		{
			name:      "PatchEdit",
			execKinds: []string{patchEditReadExecKindName, patchEditWriteExecKindName, patchEditPostReadExecKindName},
			control:   (*Service).handleHiddenPatchEditExecControl,
			marshalState: func() ([]byte, error) {
				return (pendingPatchEditPayload{
					ToolName:      patchEditToolName,
					ResolvedPath:  `C:\\workspace\\file.go`,
					BeforeContent: "before",
					AfterContent:  "after",
				}).MarshalJSON()
			},
		},
	}
}

func testHiddenEditControlFixture(t *testing.T, execKind string, marshalState func() ([]byte, error)) (*Service, *ActiveStream, runtimecore.PendingExec) {
	t.Helper()
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request-1", "conversation-1", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "test")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	service := &Service{broker: broker, projector: NewHistoryProjector()}
	if err := service.replaceCheckpointConversation(stream, testConversation(nil)); err != nil {
		t.Fatalf("replaceCheckpointConversation() error = %v", err)
	}
	argsJSON, err := marshalState()
	if err != nil {
		t.Fatalf("marshal pending state: %v", err)
	}
	pending := runtimecore.PendingExec{
		MessageID:   41,
		ExecID:      "exec-" + execKind,
		ExecKind:    execKind,
		ToolCallID:  "tool-" + execKind,
		ModelCallID: "model-call-1",
		ArgsJSON:    argsJSON,
		StreamState: "opened",
	}
	stream.mu.Lock()
	stream.PendingExecs[pending.ExecID] = pending
	stream.mu.Unlock()
	return service, stream, pending
}

func assertHiddenEditPending(t *testing.T, stream *ActiveStream, execID string, want bool) {
	t.Helper()
	_, found := snapshotPendingExec(stream, execID)
	if found != want {
		t.Fatalf("pending exec %q found=%v, want %v", execID, found, want)
	}
}

func assertHiddenEditCompletion(t *testing.T, stream *ActiveStream, toolCallID string) {
	t.Helper()
	if !hasHiddenEditCompletion(stream, toolCallID) {
		t.Fatalf("tool call %q did not publish completion", toolCallID)
	}
}

func hasHiddenEditCompletion(stream *ActiveStream, toolCallID string) bool {
	return hiddenEditCompletionCount(stream, toolCallID) > 0
}

func hiddenEditCompletionCount(stream *ActiveStream, toolCallID string) int {
	if stream == nil {
		return 0
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	count := 0
	for _, event := range stream.Backlog {
		if event.Message == nil || event.Message.GetInteractionUpdate() == nil {
			continue
		}
		completed := event.Message.GetInteractionUpdate().GetToolCallCompleted()
		if completed != nil && completed.GetCallId() == toolCallID {
			count++
		}
	}
	return count
}

func TestHiddenEditFixturePayloadsAreValidJSON(t *testing.T) {
	payloads := []any{
		pendingWritePayload{VisibleArgs: writeOperationArgs{Path: `C:\\workspace\\file.go`}},
		pendingPatchEditPayload{ToolName: patchEditToolName, ResolvedPath: `C:\\workspace\\file.go`},
	}
	for _, payload := range payloads {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal %T: %v", payload, err)
		}
		if len(encoded) == 0 {
			t.Fatalf("empty JSON for %T", payload)
		}
	}
}
