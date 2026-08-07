package forwarder

import (
	"fmt"
	"strings"
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

func TestHiddenEditPostReadPublishesVisibleDiff(t *testing.T) {
	for _, tool := range hiddenEditLifecycleTools() {
		t.Run(tool.name, func(t *testing.T) {
			service, stream, pending := testHiddenEditControlFixture(t, tool.execKinds[2], tool.marshalState)
			if err := service.handleExecResult(InboundIntent{
				RequestID:         stream.RequestID,
				ExecClientMessage: hiddenEditReadSuccessMessage(pending, `C:\\workspace\\file.go`, "after"),
			}); err != nil {
				t.Fatalf("handleExecResult(post-read success) error = %v", err)
			}

			assertHiddenEditPending(t, stream, pending.ExecID, false)
			assertHiddenEditSuccessDiff(t, stream, pending.ToolCallID, `C:\\workspace\\file.go`, "before", "after")
		})
	}
}

func TestHiddenEditTerminalResultWinsAfterStreamClose(t *testing.T) {
	for _, tool := range hiddenEditLifecycleTools() {
		t.Run(tool.name, func(t *testing.T) {
			service, stream, pending := testHiddenEditControlFixture(t, tool.execKinds[2], tool.marshalState)
			if err := service.handleExecControl(InboundIntent{
				RequestID:                stream.RequestID,
				ExecClientControlMessage: hiddenEditStreamCloseMessage(pending),
			}); err != nil {
				t.Fatalf("handleExecControl(stream_close) error = %v", err)
			}
			if err := service.handleExecResult(InboundIntent{
				RequestID:         stream.RequestID,
				ExecClientMessage: hiddenEditReadSuccessMessage(pending, `C:\\workspace\\file.go`, "after"),
			}); err != nil {
				t.Fatalf("handleExecResult(post-read after stream_close) error = %v", err)
			}

			assertHiddenEditPending(t, stream, pending.ExecID, false)
			assertHiddenEditSuccessDiff(t, stream, pending.ToolCallID, `C:\\workspace\\file.go`, "before", "after")
			if completions := hiddenEditCompletionCount(stream, pending.ToolCallID); completions != 1 {
				t.Fatalf("completion count = %d, want 1", completions)
			}
		})
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

func hiddenEditStreamCloseMessage(pending runtimecore.PendingExec) *agentv1.ExecClientControlMessage {
	return &agentv1.ExecClientControlMessage{
		Message: &agentv1.ExecClientControlMessage_StreamClose{
			StreamClose: &agentv1.ExecClientStreamClose{Id: pending.MessageID},
		},
	}
}

func hiddenEditReadSuccessMessage(pending runtimecore.PendingExec, path string, content string) *agentv1.ExecClientMessage {
	return &agentv1.ExecClientMessage{
		Id:     pending.MessageID,
		ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_ReadResult{
			ReadResult: &agentv1.ReadResult{
				Result: &agentv1.ReadResult_Success{
					Success: &agentv1.ReadSuccess{
						Path:   path,
						Output: &agentv1.ReadSuccess_Content{Content: content},
					},
				},
			},
		},
	}
}

func assertHiddenEditSuccessDiff(t *testing.T, stream *ActiveStream, toolCallID string, path string, before string, after string) {
	t.Helper()
	completed := hiddenEditCompletedToolCall(stream, toolCallID)
	if completed == nil {
		t.Fatalf("tool call %q did not publish a completed tool call", toolCallID)
	}
	success := completed.GetEditToolCall().GetResult().GetSuccess()
	if success == nil {
		t.Fatalf("tool call %q result = %#v, want edit success", toolCallID, completed.GetEditToolCall().GetResult())
	}
	if success.GetPath() != path {
		t.Fatalf("success path = %q, want %q", success.GetPath(), path)
	}
	if success.GetBeforeFullFileContent() != before || success.GetAfterFullFileContent() != after {
		t.Fatalf("success content = (%q, %q), want (%q, %q)", success.GetBeforeFullFileContent(), success.GetAfterFullFileContent(), before, after)
	}
	if success.GetDiffString() == "" || success.GetLinesAdded() == 0 || success.GetLinesRemoved() == 0 {
		t.Fatalf("success diff = (%q, +%d, -%d), want non-empty visible diff", success.GetDiffString(), success.GetLinesAdded(), success.GetLinesRemoved())
	}
}

func hiddenEditCompletedToolCall(stream *ActiveStream, toolCallID string) *agentv1.ToolCall {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	var latest *agentv1.ToolCall
	for _, event := range stream.Backlog {
		if event.Message == nil || event.Message.GetInteractionUpdate() == nil {
			continue
		}
		completed := event.Message.GetInteractionUpdate().GetToolCallCompleted()
		if completed != nil && completed.GetCallId() == toolCallID {
			latest = completed.GetToolCall()
		}
	}
	return latest
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
				diffString, linesAdded, linesRemoved := computeEditDiff("before", "after")
				return (pendingPatchEditPayload{
					ToolName:      patchEditToolName,
					ResolvedPath:  `C:\\workspace\\file.go`,
					BeforeContent: "before",
					AfterContent:  "after",
					DiffString:    diffString,
					LinesAdded:    linesAdded,
					LinesRemoved:  linesRemoved,
					Message:       "PatchEdit applied",
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

func TestComputeEditDiffCountsNewFileLines(t *testing.T) {
	lines := make([]string, 102)
	for index := range lines {
		lines[index] = fmt.Sprintf("line-%03d", index+1)
	}
	content := strings.Join(lines, "\n") + "\n"

	diffString, linesAdded, linesRemoved := computeEditDiff("", content)
	if linesAdded != 102 || linesRemoved != 0 {
		t.Fatalf("new file diff lines = (+%d, -%d), want (+102, -0)", linesAdded, linesRemoved)
	}
	if diffString == "" {
		t.Fatal("new file diff string is empty")
	}

	result := buildSuccessfulWriteResult(`C:\\workspace\\new.go`, "", content)
	success := result.GetSuccess()
	if success == nil {
		t.Fatal("new file result is not an edit success")
	}
	if success.GetLinesAdded() != linesAdded || success.GetLinesRemoved() != linesRemoved || success.GetDiffString() != diffString {
		t.Fatalf("new file result diff = (%q, +%d, -%d), want (%q, +%d, -%d)", success.GetDiffString(), success.GetLinesAdded(), success.GetLinesRemoved(), diffString, linesAdded, linesRemoved)
	}
}

func TestHiddenEditCancelCompletesVisiblePostReadEdit(t *testing.T) {
	for _, tool := range hiddenEditLifecycleTools() {
		t.Run(tool.name, func(t *testing.T) {
			service, stream, pending := testHiddenEditControlFixture(t, tool.execKinds[2], tool.marshalState)
			if err := service.handleCancelIntent(InboundIntent{Kind: "cancel", RequestID: stream.RequestID}); err != nil {
				t.Fatalf("handleCancelIntent() error = %v", err)
			}
			assertHiddenEditPending(t, stream, pending.ExecID, false)
			assertHiddenEditSuccessDiff(t, stream, pending.ToolCallID, `C:\\workspace\\file.go`, "before", "after")
		})
	}
}

func TestHiddenEditCheckpointReplayDoesNotReopenEditingUI(t *testing.T) {
	hiddenWritePayload, err := (pendingWritePayload{
		VisibleArgs:  writeOperationArgs{Path: `C:\\workspace\\file.go`, Contents: "after"},
		ResolvedPath: `C:\\workspace\\file.go`,
	}).MarshalJSON()
	if err != nil {
		t.Fatalf("marshal hidden Write payload: %v", err)
	}
	hiddenPatchPayload, err := (pendingPatchEditPayload{
		ToolName:     patchEditToolName,
		ResolvedPath: `C:\\workspace\\file.go`,
	}).MarshalJSON()
	if err != nil {
		t.Fatalf("marshal hidden PatchEdit payload: %v", err)
	}

	pending := buildPendingToolCalls([]runtimecore.PendingExec{
		{
			ExecID:     "write-post-read",
			MessageID:  1,
			ToolCallID: "edit-write-1",
			ExecKind:   writePostReadExecKind,
			ArgsJSON:   hiddenWritePayload,
		},
		{
			ExecID:     "patch-post-read",
			MessageID:  2,
			ToolCallID: "edit-patch-1",
			ExecKind:   patchEditPostReadExecKindName,
			ArgsJSON:   hiddenPatchPayload,
		},
		{
			ExecID:     "shell",
			MessageID:  3,
			ToolCallID: "shell-1",
			ExecKind:   "shell",
			ArgsJSON:   []byte(`{"command":"git status"}`),
		},
	}, nil)

	if len(pending) != 1 || !strings.Contains(pending[0], "shell-1") {
		t.Fatalf("checkpoint pending tool replay = %#v, want only non-hidden shell", pending)
	}
	if strings.Contains(strings.Join(pending, "\n"), "edit-write-1") || strings.Contains(strings.Join(pending, "\n"), "edit-patch-1") {
		t.Fatalf("checkpoint replay still contains hidden Edit implementation execs: %#v", pending)
	}
}
