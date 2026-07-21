package forwarder

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
	vm "cursor/internal/backend/virtualmodel"
)

func TestCompileAOSMemberTaskUsesExplicitFullAgentTask(t *testing.T) {
	req := MemberSpawnRequest{
		TaskID:      "task-42",
		MemberID:    "member-reviewer",
		Prompt:      "Review the proposed change.",
		ModelID:     "adapter-member",
		Description: "Review implementation",
	}

	compiled := compileAOSMemberTaskArgs(req)
	if got, want := compiled["model"], "adapter-member"; got != want {
		t.Fatalf("compiled model = %v, want %q", got, want)
	}
	if got, want := compiled["subagent_type"], DefaultAOSMemberSubagentType; got != want {
		t.Fatalf("compiled subagent_type = %v, want %q", got, want)
	}
	if got, ok := compiled["readonly"].(bool); !ok || got {
		t.Fatalf("compiled readonly = %v, want false for a full-agent Task", compiled["readonly"])
	}
	prompt, _ := compiled["prompt"].(string)
	if !strings.Contains(prompt, "AOS re-entry is strictly forbidden") {
		t.Fatalf("compiled prompt is missing anti-re-entry constraint: %q", prompt)
	}
	if strings.Contains(prompt, ".cursor/agents") {
		t.Fatalf("compiled prompt must not depend on .cursor/agents: %q", prompt)
	}

	taskArgs := BuildAOSMemberTaskArgs(req)
	if got, want := taskArgs.GetModel(), "adapter-member"; got != want {
		t.Fatalf("TaskArgs model = %q, want %q", got, want)
	}
	if got, want := taskArgs.GetMode(), agentv1.TaskMode_TASK_MODE_AGENT; got != want {
		t.Fatalf("TaskArgs mode = %s, want %s", got, want)
	}
}

func TestAOSMemberTaskExplicitModelWinsOverParentOverride(t *testing.T) {
	req := MemberSpawnRequest{
		TaskID:   "task-43",
		MemberID: "member-implementer",
		Prompt:   "Implement the selected task.",
		ModelID:  "adapter-member",
	}
	argsJSON, err := CompileAOSMemberTaskJSON(req)
	if err != nil {
		t.Fatalf("CompileAOSMemberTaskJSON() error = %v", err)
	}

	message, pending, err := execbridge.NewBridge().OpenExec(execbridge.OpenExecContext{
		ConversationID: "parent-conversation",
		ModelID:        "parent-model",
		SubagentModelOverrides: map[string]runtimecore.SubagentModelOverrideSelection{
			DefaultAOSMemberSubagentType: {
				SubagentType: DefaultAOSMemberSubagentType,
				Selection:    "model",
				ModelID:      "parent-override-model",
			},
		},
	}, runtimecore.ToolInvocation{
		CallID:   "call-43",
		ToolName: "Task",
		ArgsJSON: argsJSON,
	})
	if err != nil {
		t.Fatalf("OpenExec(Task) error = %v", err)
	}
	if got, want := pending.ExecKind, "subagent"; got != want {
		t.Fatalf("pending exec kind = %q, want %q", got, want)
	}
	subagent := message.GetExecServerMessage().GetSubagentArgs()
	if subagent == nil {
		t.Fatal("OpenExec(Task) did not emit SubagentArgs")
	}
	if got, want := subagent.GetModelId(), "adapter-member"; got != want {
		t.Fatalf("SubagentArgs.ModelId = %q, want explicit Task model %q", got, want)
	}
	if got, want := subagent.GetMode(), agentv1.TaskMode_TASK_MODE_AGENT; got != want {
		t.Fatalf("SubagentArgs.Mode = %s, want %s for normal Agent-mode Task dispatch", got, want)
	}
	if got, want := subagent.GetParentConversationId(), "parent-conversation"; got != want {
		t.Fatalf("SubagentArgs.ParentConversationId = %q, want %q", got, want)
	}
}

func TestAOSMemberTaskRewritePreservesExplicitModelBeforeOpenExec(t *testing.T) {
	req := MemberSpawnRequest{
		TaskID:   "task-44",
		MemberID: "member-reviewer",
		Prompt:   "Review the selected task.",
		ModelID:  "adapter-member",
	}
	argsJSON, err := CompileAOSMemberTaskJSON(req)
	if err != nil {
		t.Fatalf("CompileAOSMemberTaskJSON() error = %v", err)
	}
	overrides := map[string]runtimecore.SubagentModelOverrideSelection{
		DefaultAOSMemberSubagentType: {
			SubagentType: DefaultAOSMemberSubagentType,
			Selection:    "model",
			ModelID:      "parent-override-model",
		},
	}
	rewritten := rewriteTaskInvocationModelForDisplay(runtimecore.ToolInvocation{
		CallID:   "call-44",
		ToolName: "Task",
		ArgsJSON: argsJSON,
	}, "parent-model", overrides)
	if got := readStringMapValue(mustDecodeArgsForTest(t, rewritten.ArgsJSON), "model"); got != "adapter-member" {
		t.Fatalf("rewritten Task model = %q, want explicit member model", got)
	}

	message, _, err := execbridge.NewBridge().OpenExec(execbridge.OpenExecContext{
		ConversationID:         "parent-conversation",
		ModelID:                "parent-model",
		SubagentModelOverrides: overrides,
	}, rewritten)
	if err != nil {
		t.Fatalf("OpenExec(rewritten Task) error = %v", err)
	}
	if got := message.GetExecServerMessage().GetSubagentArgs().GetModelId(); got != "adapter-member" {
		t.Fatalf("SubagentArgs.ModelId = %q, want explicit member model", got)
	}
}

func TestTaskWithoutExplicitModelRetainsParentOverride(t *testing.T) {
	argsJSON := []byte(`{"prompt":"Inspect the task","subagent_type":"generalPurpose"}`)
	overrides := map[string]runtimecore.SubagentModelOverrideSelection{
		DefaultAOSMemberSubagentType: {
			SubagentType: DefaultAOSMemberSubagentType,
			Selection:    "model",
			ModelID:      "parent-override-model",
		},
	}
	invocation := rewriteTaskInvocationModelForDisplay(runtimecore.ToolInvocation{
		CallID:   "call-45",
		ToolName: "Task",
		ArgsJSON: argsJSON,
	}, "parent-model", overrides)
	if got := readStringMapValue(mustDecodeArgsForTest(t, invocation.ArgsJSON), "model"); got != "parent-override-model" {
		t.Fatalf("rewritten fallback Task model = %q, want parent override", got)
	}
	message, _, err := execbridge.NewBridge().OpenExec(execbridge.OpenExecContext{
		ConversationID:         "parent-conversation",
		ModelID:                "parent-model",
		SubagentModelOverrides: overrides,
	}, runtimecore.ToolInvocation{CallID: "call-45", ToolName: "Task", ArgsJSON: argsJSON})
	if err != nil {
		t.Fatalf("OpenExec(fallback Task) error = %v", err)
	}
	if got := message.GetExecServerMessage().GetSubagentArgs().GetModelId(); got != "parent-override-model" {
		t.Fatalf("fallback SubagentArgs.ModelId = %q, want parent override", got)
	}
}

func TestTaskModelResolutionPayloadUsesExplicitModelPrecedence(t *testing.T) {
	overrides := map[string]runtimecore.SubagentModelOverrideSelection{
		DefaultAOSMemberSubagentType: {
			SubagentType: DefaultAOSMemberSubagentType,
			Selection:    "model",
			ModelID:      "parent-override-model",
		},
	}
	payload := taskSubagentModelResolutionPayload(runtimecore.ToolInvocation{
		CallID:   "call-46",
		ToolName: "Task",
		ArgsJSON: []byte(`{"subagent_type":"generalPurpose","model":"adapter-member"}`),
	}, "parent-model", overrides)
	if got, want := payload["effective_model_id"], "adapter-member"; got != want {
		t.Fatalf("effective_model_id = %v, want %q", got, want)
	}
	if got, want := payload["selection"], "explicit"; got != want {
		t.Fatalf("selection = %v, want %q", got, want)
	}
	if got, want := payload["override_hit"], false; got != want {
		t.Fatalf("override_hit = %v, want %t when explicit model bypasses parent override", got, want)
	}
}

func TestRewriteTaskToolCallDisplayPreservesExplicitModel(t *testing.T) {
	modelID := "adapter-member"
	toolCall := &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_TaskToolCall{TaskToolCall: &agentv1.TaskToolCall{Args: &agentv1.TaskArgs{
			Model:        &modelID,
			SubagentType: subagentTypeFromString(DefaultAOSMemberSubagentType),
		}}},
	}
	stream := &ActiveStream{ModelID: "parent-model", SubagentModelOverrides: map[string]runtimecore.SubagentModelOverrideSelection{
		DefaultAOSMemberSubagentType: {SubagentType: DefaultAOSMemberSubagentType, Selection: "model", ModelID: "parent-override-model"},
	}}
	rewritten := (&Service{}).rewriteTaskToolCallModelForDisplay(stream, toolCall)
	if got := rewritten.GetTaskToolCall().GetArgs().GetModel(); got != modelID {
		t.Fatalf("display Task model = %q, want explicit member model", got)
	}
}

func mustDecodeArgsForTest(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("decode Task args: %v", err)
	}
	return args
}

func TestValidateNoAOSReEntryBlocksRegisteredVirtualModels(t *testing.T) {
	manager := vm.NewManager()
	if err := manager.Register(testVirtualModel{id: "organization-model"}); err != nil {
		t.Fatalf("register virtual model: %v", err)
	}
	if err := ValidateNoAOSReEntry("organization-model", manager); err == nil {
		t.Fatal("ValidateNoAOSReEntry() allowed a registered virtual model")
	}
	if err := ValidateNoAOSReEntry("adapter-physical", manager); err != nil {
		t.Fatalf("ValidateNoAOSReEntry() rejected a physical adapter ID: %v", err)
	}
}

type testVirtualModel struct {
	id string
}

func (m testVirtualModel) ID() string {
	return m.id
}

func (m testVirtualModel) DisplayName() string {
	return m.id
}

func (m testVirtualModel) Enabled() bool {
	return true
}

func (m testVirtualModel) Execute(context.Context, *vm.ExecuteRequest) (*vm.ExecuteResult, error) {
	return nil, nil
}

func (m testVirtualModel) AdapterMetadata(context.Context) vm.AdapterMetadata {
	return vm.AdapterMetadata{}
}
