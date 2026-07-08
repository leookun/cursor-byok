// reminders.go 负责按 mode 和上下文生成最小的 system reminder 集合。
package forwarder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	promptassets "cursor/prompt"
)

type DefaultReminderInjector struct {
}

const (
	promptContextSourcePlanTurnContract          = "plan_turn_contract"
	promptContextSourceActiveModeContract        = "active_mode_contract"
	promptContextSourceLatestUserIntent          = "latest_user_intent"
	promptContextSourceCurrentUserRequest        = "current_user_request"
	promptContextSourceSubagentContract          = "subagent_contract"
	promptContextSourceSubagentEmptyStopRecovery = "subagent_empty_stop_recovery"
	promptContextSourceDebugModeReminder         = "debug_mode_reminder"
)

// NewReminderInjector 创建默认 reminder 注入器。
func NewReminderInjector() *DefaultReminderInjector {
	return &DefaultReminderInjector{}
}

// Inject 根据 mode、最近用户输入和工具上下文生成本轮附加提醒。
func (injector *DefaultReminderInjector) Inject(mode agentv1.AgentMode, conversation *ConversationFile, replayMessages []modeladapter.Message, latestUserText string, toolNames []string) PromptReminders {
	_ = toolNames
	reminders := make([]string, 0, 6)
	normalizedMode, err := validateSupportedActiveMode(mode)
	if err != nil {
		normalizedMode = agentv1.AgentMode_AGENT_MODE_AGENT
	}
	if conversation != nil && isChildConversationSubagentTypeName(conversation.SubagentTypeName) {
		return appendCurrentTurnAttentionReminders(PromptReminders{
			SystemParts: reminders,
			PromptContexts: []PromptContextMessage{
				newPromptContextReminder(promptContextSourceSubagentContract, subagentContractText()),
				newPromptContextReminder(promptContextSourceActiveModeContract, currentModeContractText(normalizedMode, true)),
			},
		}, latestUserText)
	}
	if normalizedMode == agentv1.AgentMode_AGENT_MODE_DEBUG {
		return debugModePromptReminders(conversation)
	}
	reminders = append(reminders, "If multiple <current_plan> or <todo_list> blocks appear in the conversation, treat the last block of each type as the current source of truth.")
	switch normalizedMode {
	case agentv1.AgentMode_AGENT_MODE_ASK:
		reminders = append(reminders, "You are in ask mode. Prefer direct answers and only use tools when they are necessary to answer accurately.")
		reminders = append(reminders, "Lead with the conclusion, keep the response concise, and avoid unsolicited example code or long bullet lists.")
	case agentv1.AgentMode_AGENT_MODE_PLAN:
		reminders = append(reminders, "You are currently working in plan mode for the user. Prioritize investigation, decomposition, tradeoff analysis, and producing or refining a concrete plan.")
		reminders = append(reminders, "Do not directly modify files in plan mode. Avoid direct file-editing tools such as Write, Delete, and PatchEdit.")
		reminders = append(reminders, "For non-trivial plan-mode work, first do a quick reconnaissance yourself, then launch 2-4 parallel Task subagents with subagent_type=\"explore\" to investigate distinct angles before CreatePlan. Avoid using exactly one subagent for broad tasks: either handle narrow tasks directly, or split broad tasks into multiple independent investigations. Synthesize the subagent results yourself; do not delegate the final plan.")
		reminders = append(reminders, "For narrow, well-scoped tasks, you can investigate directly and then create a lean plan with only the essential stages, tradeoffs, and next steps.")
		if hasCurrentPlan(conversation) {
			reminders = append(reminders, "A current plan already exists. Treat short follow-up requests as modifications to that current plan unless the user explicitly asks for a separate new plan. When calling CreatePlan for an existing plan, send the complete revised plan, preserve relevant existing content, incorporate the user's requested changes, and omit the name field. The CreatePlan name field is only allowed on the first CreatePlan call; never use a later name to rename or create a separate plan.")
		}
	case agentv1.AgentMode_AGENT_MODE_MULTITASK:
		reminders = append(reminders, "You are in multitask mode. Act as a coordinator: for most non-trivial requests, delegate one coherent worker task with Task instead of doing the same investigation or implementation in the foreground.")
		reminders = append(reminders, "After delegating the only coherent worker task for a request, do not continue the same work in the foreground. Only do distinct coordination work, answer a new independent question, or synthesize after multiple workers return.")
		reminders = append(reminders, "Do not wait, sleep, or poll just for a running worker to complete. End the response unless there is separate useful coordination to do.")
		reminders = append(reminders, "Do not over-decompose small or medium tasks into many sibling workers. Use multiple sibling workers only for clearly independent top-level workstreams.")
	default:
		reminders = append(reminders, "You are in agent mode. Use the available tools when they materially improve correctness or efficiency.")
		reminders = append(reminders, "When reporting progress or completion, lead with the result, mention only key changes or verification, and avoid long recaps, exhaustive lists, or unsolicited example code.")
	}

	result := PromptReminders{
		SystemParts: reminders,
		PromptContexts: []PromptContextMessage{
			newPromptContextReminder(promptContextSourceActiveModeContract, currentModeContractText(normalizedMode, false)),
		},
	}
	if normalizedMode == agentv1.AgentMode_AGENT_MODE_PLAN {
		if reminder := strings.TrimSpace(promptassets.MustReadPlanSystemReminder()); reminder != "" {
			result.PromptContexts = append([]PromptContextMessage{
				newPromptContextReminder(promptContextSourcePlanTurnContract, reminder),
			}, result.PromptContexts...)
		}
		return appendCurrentTurnAttentionReminders(result, latestUserText)
	}
	if normalizedMode != agentv1.AgentMode_AGENT_MODE_AGENT {
		return appendCurrentTurnAttentionReminders(result, latestUserText)
	}

	candidate, ok := extractLatestSuccessfulEditReminder(replayMessages)
	if !ok {
		return appendCurrentTurnAttentionReminders(result, latestUserText)
	}
	result.PromptContexts = append(result.PromptContexts, newPromptContextMessage(
		"latest_edit_reminder",
		modeladapter.Message{
			Role: "user",
			Content: strings.TrimSpace(fmt.Sprintf(`<system_reminder>
You recently successfully edited %q.

For this file, the latest source of truth is the most recent successful %s, not earlier reads or memory.

When modifying this file:
- use PatchEdit with path, old_string, new_string, and optional replace_all
- copy old_string exactly from the latest file content; line endings are not normalized or treated equivalently during matching
- replace_all defaults to false, so old_string must match exactly one occurrence unless you intentionally set replace_all to true
- new_string may be empty to delete old_string
- preserve spaces, tabs, indentation, punctuation, and line endings exactly in old_string
- only read the file again yourself if you need exact current content or extra context to choose a unique old_string
</system_reminder>`, candidate.Path, candidate.SourceField)),
		},
		false,
	))
	return appendCurrentTurnAttentionReminders(result, latestUserText)
}

func appendCurrentTurnAttentionReminders(result PromptReminders, latestUserText string) PromptReminders {
	if reminder := latestUserIntentReminderText(latestUserText); reminder != "" {
		result.PromptContexts = append(result.PromptContexts, newPromptContextReminder(promptContextSourceLatestUserIntent, reminder))
	}
	return appendCurrentUserRequestReminder(result, latestUserText)
}

func appendCurrentUserRequestReminder(result PromptReminders, latestUserText string) PromptReminders {
	if strings.TrimSpace(latestUserText) == "" {
		return result
	}
	result.PromptContexts = append(result.PromptContexts, newCurrentUserRequestReminder(latestUserText))
	return result
}

func latestUserIntentReminderText(latestUserText string) string {
	normalizedText := strings.ToLower(strings.TrimSpace(latestUserText))
	switch {
	case strings.Contains(normalizedText, "review"), strings.Contains(latestUserText, "评审"), strings.Contains(latestUserText, "审查"):
		return "When reviewing code, focus on bugs, regressions, behavioral risks, and missing tests."
	case strings.Contains(normalizedText, "plan"), strings.Contains(latestUserText, "计划"):
		return "Prefer clear staged plans with concrete checkpoints."
	default:
		return ""
	}
}

func newCurrentUserRequestReminder(latestUserText string) PromptContextMessage {
	return newPromptContextMessage(
		promptContextSourceCurrentUserRequest,
		modeladapter.Message{
			Role: "user",
			Content: strings.TrimSpace(fmt.Sprintf(`<current_user_request>
%s
</current_user_request>

Handle this request. Use any latest tool results as evidence when present, and provide the requested answer once you have enough information. Treat surrounding system reminders as constraints, not as the user's task.`, strings.TrimSpace(latestUserText))),
		},
		false,
	)
}

func newPromptContextReminder(source string, content string) PromptContextMessage {
	return newPromptContextMessage(
		source,
		modeladapter.Message{
			Role:    "user",
			Content: wrapSystemReminder(content),
		},
		false,
	)
}

func subagentContractText() string {
	return strings.Join([]string{
		"The turn that contains this reminder runs inside a subagent child conversation. Work as an investigator for the parent agent, not as the final user-facing assistant.",
		"Return a short textual result: lead with the conclusion, keep only the key evidence, and do not produce a long response.",
		"Use the available agent tools when they materially improve correctness or efficiency. Do not ask the user questions. If required information is missing, report the gap to the parent agent instead of asking the user directly.",
	}, "\n\n")
}

func currentModeContractText(mode agentv1.AgentMode, childSubagent bool) string {
	if childSubagent {
		return "For the turn that contains this reminder, the active mode is a subagent child conversation. Use the available agent tools, but do not call AskQuestion. Return only a concise investigation result for the parent agent."
	}
	switch normalizeMode(mode) {
	case agentv1.AgentMode_AGENT_MODE_PLAN:
		return "For the turn that contains this reminder, the active mode is plan. Do not modify files or system state. Use CreatePlan when the plan is ready or needs updating."
	case agentv1.AgentMode_AGENT_MODE_ASK:
		return "For the turn that contains this reminder, the active mode is ask. Prefer a direct answer. Use tools only when they materially improve accuracy, and do not call CreatePlan."
	case agentv1.AgentMode_AGENT_MODE_DEBUG:
		return "For the turn that contains this reminder, the active mode is debug. Follow the Debug Mode workflow from the static debug prompt: inspect or reproduce before editing, keep 3-5 concrete hypotheses, use the injected debug session log path when temporary instrumentation is useful, and verify with runtime evidence. Do not call CreatePlan or SwitchMode."
	case agentv1.AgentMode_AGENT_MODE_MULTITASK:
		return "For the turn that contains this reminder, the active mode is multitask. Act as the foreground coordinator: delegate most non-trivial work to a coherent worker with Task, avoid duplicating delegated work in the foreground, and do not wait just for a worker to finish."
	default:
		return "For the turn that contains this reminder, the active mode is agent. CreatePlan is not available in this mode; do not call CreatePlan. If the user explicitly asks to create or revise a plan, call SwitchMode to return to plan mode first. If there is an accepted or current plan, execute or continue the implementation using the available agent-mode tools."
	}
}

func debugModePromptReminders(conversation *ConversationFile) PromptReminders {
	initial := !hasPreviousPromptContextSource(conversation, promptContextSourceDebugModeReminder)
	content := renderDebugSystemReminder(promptassets.MustReadDebugSystemReminder(initial), conversation)
	if strings.TrimSpace(content) == "" {
		return PromptReminders{}
	}
	return PromptReminders{
		PromptContexts: []PromptContextMessage{
			newPromptContextMessage(promptContextSourceDebugModeReminder, modeladapter.Message{
				Role:    "user",
				Content: strings.TrimSpace(content),
			}, false),
		},
	}
}

func hasPreviousPromptContextSource(conversation *ConversationFile, source string) bool {
	if conversation == nil {
		return false
	}
	needle := strings.TrimSpace(source)
	if needle == "" {
		return false
	}
	for _, entry := range conversation.Entries {
		if strings.TrimSpace(entry.Kind) != "prompt_context" {
			continue
		}
		var payload promptContextEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		if strings.TrimSpace(payload.Source) == needle {
			return true
		}
	}
	return false
}

func renderDebugSystemReminder(template string, conversation *ConversationFile) string {
	sessionID := firstNonEmpty(debugSessionID(conversation), "debug")
	logPath := debugLogPath(sessionID)
	serverEndpoint := debugServerEndpoint(sessionID)
	result := strings.TrimSpace(template)
	for _, replacement := range []struct {
		placeholder string
		value       string
	}{
		{placeholder: "{{DEBUG_SERVER_ENDPOINT}}", value: serverEndpoint},
		{placeholder: "{{DEBUG_LOG_PATH}}", value: logPath},
		{placeholder: "{{DEBUG_SESSION_ID}}", value: sessionID},
	} {
		result = strings.ReplaceAll(result, replacement.placeholder, replacement.value)
	}
	return result
}

func debugLogPath(sessionID string) string {
	filename := fmt.Sprintf("debug-%s.log", firstNonEmpty(strings.TrimSpace(sessionID), "debug"))
	cwd, err := os.Getwd()
	if err != nil || strings.TrimSpace(cwd) == "" {
		return filepath.Join(".cursor", filename)
	}
	return filepath.Join(cwd, ".cursor", filename)
}

func debugServerEndpoint(sessionID string) string {
	return "http://127.0.0.1:7337/ingest/" + firstNonEmpty(strings.TrimSpace(sessionID), "debug")
}

func debugSessionID(conversation *ConversationFile) string {
	if conversation == nil {
		return ""
	}
	for _, candidate := range []string{
		conversation.CurrentRequestID,
		conversation.CurrentLoopID,
		conversation.ConversationID,
	} {
		if normalized := normalizeDebugSessionID(candidate); normalized != "" {
			return normalized
		}
	}
	return ""
}

func normalizeDebugSessionID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	for _, value := range trimmed {
		switch {
		case value >= 'a' && value <= 'z':
			builder.WriteRune(value)
		case value >= 'A' && value <= 'Z':
			builder.WriteRune(value)
		case value >= '0' && value <= '9':
			builder.WriteRune(value)
		case value == '-', value == '_':
			builder.WriteRune(value)
		default:
			builder.WriteRune('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}

func wrapSystemReminder(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "<system_reminder>") && strings.HasSuffix(trimmed, "</system_reminder>") {
		return trimmed
	}
	return "<system_reminder>\n" + trimmed + "\n</system_reminder>"
}

func hasCurrentPlan(conversation *ConversationFile) bool {
	if conversation == nil {
		return false
	}
	state, err := projectConversationStructuredState(conversation)
	if err != nil {
		return false
	}
	return state.HasPlan || len(state.Plans) > 0
}

type editReminderCandidate struct {
	ToolCallID  string
	Path        string
	SourceField string
}

type replayEditSuccess struct {
	Success *struct {
		Path                      string `json:"path"`
		AfterFullFileContent      string `json:"afterFullFileContent"`
		AfterFullFileContentSnake string `json:"after_full_file_content"`
		DiffString                string `json:"diffString"`
		DiffStringSnake           string `json:"diff_string"`
	} `json:"success"`
}

func extractLatestSuccessfulEditReminder(messages []modeladapter.Message) (editReminderCandidate, bool) {
	if len(messages) == 0 {
		return editReminderCandidate{}, false
	}
	toolPaths := collectReplayEditToolPaths(messages)
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if strings.TrimSpace(message.Role) != "tool" {
			continue
		}
		toolName := strings.TrimSpace(message.Name)
		if !isEditReminderToolName(toolName) {
			continue
		}
		toolCallID := strings.TrimSpace(message.ToolCallID)
		if toolCallID == "" {
			continue
		}
		var result replayEditSuccess
		if err := json.Unmarshal([]byte(message.Content), &result); err != nil || result.Success == nil {
			continue
		}
		path := strings.TrimSpace(result.Success.Path)
		if path == "" {
			path = toolPaths[toolCallID]
		}
		if path == "" {
			continue
		}
		sourceField := editReminderSourceField(result)
		if sourceField == "" {
			continue
		}
		return editReminderCandidate{
			ToolCallID:  toolCallID,
			Path:        path,
			SourceField: sourceField,
		}, true
	}
	return editReminderCandidate{}, false
}

func editReminderSourceField(result replayEditSuccess) string {
	if result.Success == nil {
		return ""
	}
	diffString := strings.TrimSpace(firstNonEmpty(result.Success.DiffStringSnake, result.Success.DiffString))
	if diffString != "" && !looksLikeProjectedReplayTruncation(diffString) {
		return "`success.diff_string`"
	}
	afterContent := strings.TrimSpace(firstNonEmpty(result.Success.AfterFullFileContentSnake, result.Success.AfterFullFileContent))
	if afterContent != "" && !looksLikeProjectedReplayTruncation(afterContent) {
		return "`success.after_full_file_content`"
	}
	return ""
}

func looksLikeProjectedReplayTruncation(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.Contains(trimmed, "[truncated:") || strings.Contains(trimmed, "[tool result replay truncated:")
}

func collectReplayEditToolPaths(messages []modeladapter.Message) map[string]string {
	paths := make(map[string]string)
	for _, message := range messages {
		if strings.TrimSpace(message.Role) != "assistant" || len(message.ToolCalls) == 0 {
			continue
		}
		for _, toolCall := range message.ToolCalls {
			toolName := strings.TrimSpace(toolCall.Function.Name)
			if !isEditReminderToolName(toolName) {
				continue
			}
			toolCallID := strings.TrimSpace(toolCall.ID)
			if toolCallID == "" {
				continue
			}
			if path := extractPathFromToolArguments(toolCall.Function.Arguments); path != "" {
				paths[toolCallID] = path
			}
		}
	}
	return paths
}

func isEditReminderToolName(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "Write", "PatchEdit", "PatchEditLines", "PatchEditSpan":
		return true
	default:
		return false
	}
}

func extractPathFromToolArguments(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return ""
	}
	for _, key := range []string{"path", "file_path"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
