package forwarder

import (
	"testing"
	"time"

	"cursor/gen/agentv1"
	vm "cursor/internal/backend/virtualmodel"
)

func TestResolveAOSMemberTaskResultPropagatesSubagentOutcome(t *testing.T) {
	tests := []struct {
		name       string
		message    *agentv1.ExecClientMessage
		wantErr    string
		wantOutput string
	}{
		{
			name: "error",
			message: &agentv1.ExecClientMessage{Message: &agentv1.ExecClientMessage_SubagentResult{
				SubagentResult: &agentv1.SubagentResult{Result: &agentv1.SubagentResult_Error{
					Error: &agentv1.SubagentError{Error: "member execution failed"},
				}},
			}},
			wantErr:    "member execution failed",
			wantOutput: "member execution failed",
		},
		{
			name: "success",
			message: &agentv1.ExecClientMessage{Message: &agentv1.ExecClientMessage_SubagentResult{
				SubagentResult: &agentv1.SubagentResult{Result: &agentv1.SubagentResult_Success{
					Success: &agentv1.SubagentSuccess{FinalMessage: stringPtr("member completed")},
				}},
			}},
			wantOutput: "member completed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := vm.NewAOSResultRegistry()
			const execID = "aos-member-task-member"
			registry.Expect(execID)

			resolveAOSMemberTaskResult(registry, execID, tt.message, tt.wantOutput)

			result, err := registry.Wait(execID, time.Second)
			if err != nil {
				t.Fatalf("registry.Wait() error = %v", err)
			}
			if result.Text != tt.wantOutput {
				t.Fatalf("registry output = %q, want %q", result.Text, tt.wantOutput)
			}
			if tt.wantErr == "" {
				if result.Error != nil {
					t.Fatalf("registry error = %v, want nil", result.Error)
				}
				return
			}
			if result.Error == nil || result.Error.Error() != tt.wantErr {
				t.Fatalf("registry error = %v, want %q", result.Error, tt.wantErr)
			}
		})
	}
}
