// subagent_overrides.go 提取自 service.go：SubagentModelOverride 解析、克隆、汇总与 Task 工具显示改写。
package forwarder

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

type parsedSubagentModelOverrides struct {
	Overrides map[string]runtimecore.SubagentModelOverrideSelection
	Ignored   []map[string]any
	RawCount  int
}

func parseSubagentModelOverrides(items []*agentv1.SubagentModelOverride) parsedSubagentModelOverrides {
	parsed := parsedSubagentModelOverrides{
		Overrides: make(map[string]runtimecore.SubagentModelOverrideSelection),
		RawCount:  len(items),
	}
	for index, item := range items {
		if item == nil {
			parsed.Ignored = append(parsed.Ignored, map[string]any{"index": index, "reason": "nil_override"})
			continue
		}
		subagentType := strings.TrimSpace(item.GetSubagentType())
		if subagentType == "" {
			parsed.Ignored = append(parsed.Ignored, map[string]any{"index": index, "reason": "empty_subagent_type"})
			continue
		}
		if _, exists := parsed.Overrides[subagentType]; exists {
			parsed.Ignored = append(parsed.Ignored, map[string]any{"index": index, "subagent_type": subagentType, "reason": "duplicate_overrides_previous"})
		}
		switch selection := item.GetSelection().(type) {
		case *agentv1.SubagentModelOverride_Model:
			model := selection.Model
			modelID := strings.TrimSpace(model.GetModelId())
			if modelID == "" {
				parsed.Ignored = append(parsed.Ignored, map[string]any{"index": index, "subagent_type": subagentType, "reason": "empty_model_id"})
				continue
			}
			parsed.Overrides[subagentType] = runtimecore.SubagentModelOverrideSelection{
				SubagentType:                  subagentType,
				Selection:                     "model",
				ModelID:                       modelID,
				MaxMode:                       model.GetMaxMode(),
				ParameterCount:                len(model.GetParameters()),
				BuiltInModel:                  model.GetBuiltInModel(),
				IsVariantStringRepresentation: model.GetIsVariantStringRepresentation(),
			}
		case *agentv1.SubagentModelOverride_Inherit:
			parsed.Overrides[subagentType] = runtimecore.SubagentModelOverrideSelection{
				SubagentType: subagentType,
				Selection:    "inherit",
			}
		case *agentv1.SubagentModelOverride_Disabled:
			parsed.Overrides[subagentType] = runtimecore.SubagentModelOverrideSelection{
				SubagentType: subagentType,
				Selection:    "disabled",
			}
		default:
			parsed.Ignored = append(parsed.Ignored, map[string]any{"index": index, "subagent_type": subagentType, "reason": "unknown_selection"})
		}
	}
	return parsed
}

func taskSubagentModelResolutionPayload(invocation runtimecore.ToolInvocation, parentModelID string, overrides map[string]runtimecore.SubagentModelOverrideSelection) map[string]any {
	if strings.TrimSpace(invocation.ToolName) != "Task" {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal(invocation.ArgsJSON, &args); err != nil {
		return map[string]any{
			"tool_call_id": strings.TrimSpace(invocation.CallID),
			"parse_error":  err.Error(),
		}
	}
	subagentType := readStringMapValue(args, "subagent_type", "subagentType")
	taskRequestedModelID := readStringMapValue(args, "model", "model_id", "modelId")
	effectiveModelID := taskRequestedModelID
	selection := "none"
	disabled := false
	overrideHit := false
	matchedSubagentType := ""
	if taskRequestedModelID != "" {
		selection = "explicit"
	} else if override, matched, ok := runtimecore.LookupSubagentModelOverride(overrides, subagentType); ok {
		overrideHit = true
		matchedSubagentType = matched
		selection = strings.TrimSpace(override.Selection)
		switch selection {
		case "model":
			effectiveModelID = strings.TrimSpace(override.ModelID)
		case "inherit":
			effectiveModelID = strings.TrimSpace(parentModelID)
		case "disabled":
			disabled = true
			effectiveModelID = ""
		}
	}
	if effectiveModelID == "" && !disabled {
		effectiveModelID = strings.TrimSpace(parentModelID)
	}
	payload := map[string]any{
		"tool_call_id":            strings.TrimSpace(invocation.CallID),
		"subagent_type":           subagentType,
		"override_hit":            overrideHit,
		"selection":               selection,
		"task_requested_model_id": taskRequestedModelID,
		"parent_model_id":         strings.TrimSpace(parentModelID),
		"effective_model_id":      strings.TrimSpace(effectiveModelID),
		"disabled":                disabled,
	}
	if matchedSubagentType != "" {
		payload["matched_subagent_type"] = matchedSubagentType
	}
	return payload
}

func rewriteTaskInvocationModelForDisplay(invocation runtimecore.ToolInvocation, parentModelID string, overrides map[string]runtimecore.SubagentModelOverrideSelection) runtimecore.ToolInvocation {
	if strings.TrimSpace(invocation.ToolName) != "Task" {
		return invocation
	}
	var args map[string]any
	if err := json.Unmarshal(invocation.ArgsJSON, &args); err != nil {
		return invocation
	}
	subagentType := readStringMapValue(args, "subagent_type", "subagentType")
	if readStringMapValue(args, "model", "model_id", "modelId") != "" {
		return invocation
	}
	override, _, ok := runtimecore.LookupSubagentModelOverride(overrides, subagentType)
	if !ok {
		return invocation
	}
	effectiveModelID := ""
	switch strings.TrimSpace(override.Selection) {
	case "model":
		effectiveModelID = strings.TrimSpace(override.ModelID)
	case "inherit":
		effectiveModelID = strings.TrimSpace(parentModelID)
	default:
		return invocation
	}
	if effectiveModelID == "" {
		return invocation
	}
	args["model"] = effectiveModelID
	rewrittenArgs, err := json.Marshal(args)
	if err != nil {
		return invocation
	}
	invocation.ArgsJSON = rewrittenArgs
	return invocation
}

func cloneSubagentModelOverrides(overrides map[string]runtimecore.SubagentModelOverrideSelection) map[string]runtimecore.SubagentModelOverrideSelection {
	if len(overrides) == 0 {
		return nil
	}
	cloned := make(map[string]runtimecore.SubagentModelOverrideSelection, len(overrides))
	for key, value := range overrides {
		cloned[strings.TrimSpace(key)] = value
	}
	return cloned
}

func subagentModelOverrideSummaries(overrides map[string]runtimecore.SubagentModelOverrideSelection) []map[string]any {
	if len(overrides) == 0 {
		return nil
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	summaries := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		selection := overrides[key]
		summary := map[string]any{
			"subagent_type": strings.TrimSpace(selection.SubagentType),
			"selection":     strings.TrimSpace(selection.Selection),
		}
		if strings.TrimSpace(selection.ModelID) != "" {
			summary["model_id"] = strings.TrimSpace(selection.ModelID)
		}
		if selection.MaxMode {
			summary["max_mode"] = true
		}
		if selection.ParameterCount > 0 {
			summary["parameter_count"] = selection.ParameterCount
		}
		if selection.BuiltInModel {
			summary["built_in_model"] = true
		}
		if selection.IsVariantStringRepresentation {
			summary["is_variant_string_representation"] = true
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

// readStringMapValue looks up the first present string-valued key in args.
// Kept here because it is only used by the subagent override helpers above.
func readStringMapValue(args map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := args[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			return strings.TrimSpace(typed)
		case fmt.Stringer:
			return strings.TrimSpace(typed.String())
		}
	}
	return ""
}
