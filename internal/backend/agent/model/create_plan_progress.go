package modeladapter

import (
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func emitCreatePlanToolProgress(
	sink func(ModelEvent) error,
	provider string,
	model string,
	callID string,
	rawArgs string,
	argsTextDelta string,
	lastSnapshot *string,
) error {
	if sink == nil || lastSnapshot == nil {
		return nil
	}
	trimmedCallID := strings.TrimSpace(callID)
	if trimmedCallID == "" {
		return nil
	}
	args, ok := createPlanArgsProgressSnapshot(rawArgs)
	if !ok {
		return nil
	}
	signatureBytes, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(args)
	if err != nil {
		return err
	}
	signature := string(signatureBytes)
	if signature == "" || signature == *lastSnapshot {
		return nil
	}
	*lastSnapshot = signature
	if err := sink(ModelEvent{
		Kind:          ModelEventKindPartialToolCall,
		OccurredAt:    time.Now().UTC(),
		Provider:      provider,
		Model:         model,
		ToolCallID:    trimmedCallID,
		ArgsTextDelta: argsTextDelta,
		ToolCall: &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_CreatePlanToolCall{
				CreatePlanToolCall: &agentv1.CreatePlanToolCall{
					Args: args,
				},
			},
		},
	}); err != nil {
		return err
	}
	return nil
}

func createPlanArgsProgressSnapshot(rawArgs string) (*agentv1.CreatePlanArgs, bool) {
	trimmed := strings.TrimSpace(rawArgs)
	if trimmed == "" {
		return nil, false
	}
	if args, err := runtimecore.DecodeCreatePlanArgsJSON([]byte(trimmed)); err == nil && hasCreatePlanArgsProgress(args) {
		return args, true
	}

	args := &agentv1.CreatePlanArgs{}
	if value, found, _ := extractJSONStringFieldPrefix(trimmed, "plan"); found {
		args.Plan = value
	}
	if value, found, _ := extractJSONStringFieldPrefix(trimmed, "overview"); found {
		args.Overview = value
	}
	if value, found, complete := extractJSONStringFieldPrefix(trimmed, "name"); found && complete {
		args.Name = strings.TrimSpace(value)
	}
	if !hasCreatePlanArgsProgress(args) {
		return nil, false
	}
	return args, true
}

func hasCreatePlanArgsProgress(args *agentv1.CreatePlanArgs) bool {
	if args == nil {
		return false
	}
	return args.GetPlan() != "" ||
		args.GetOverview() != "" ||
		args.GetName() != "" ||
		args.GetIsProject() ||
		len(args.GetTodos()) > 0 ||
		len(args.GetPhases()) > 0
}
