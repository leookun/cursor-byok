package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"

	runtimecore "cursor/internal/backend/agent/core"
)

func (service *Service) sanitizeCreatePlanInvocationForCurrentPlan(stream *ActiveStream, invocation runtimecore.ToolInvocation) (runtimecore.ToolInvocation, error) {
	if strings.TrimSpace(invocation.ToolName) != "CreatePlan" {
		return invocation, nil
	}
	args, err := runtimecore.DecodeCreatePlanArgsJSON(invocation.ArgsJSON)
	if err != nil {
		return invocation, newRecoverableToolInvocationError(fmt.Errorf("decode CreatePlan args failed: %w", err))
	}
	if strings.TrimSpace(args.GetName()) == "" {
		return invocation, nil
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return invocation, err
	}
	if !hasCurrentPlan(conversation) {
		return invocation, nil
	}
	args.Name = ""
	sanitized, err := json.Marshal(args)
	if err != nil {
		return invocation, newRecoverableToolInvocationError(fmt.Errorf("encode sanitized CreatePlan args failed: %w", err))
	}
	invocation.ArgsJSON = sanitized
	return invocation, nil
}
