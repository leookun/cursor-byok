// tool_inference.go extracts tool name inference helpers from service.go (TD-002).
// Contains: readStringAny, readMapAny, inferToolName, deriveToolNameFromPendingExec,
// execKindFromToolName, isExecTool, inferEditToolName, inferEditToolNameFromToolCall,
// editResultLooksLikeStructuredEdit.
package forwarder

import (
	"strings"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func readStringAny(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func readMapAny(value any) map[string]any {
	switch item := value.(type) {
	case map[string]any:
		return item
	case nil:
		return map[string]any{}
	default:
		return map[string]any{}
	}
}

// inferToolName 从完整 ToolCall proto 中反推出 canonical 工具名。
func inferToolName(toolCall *agentv1.ToolCall) string {
	if toolCall == nil || toolCall.GetTool() == nil {
		return ""
	}
	switch toolCall.GetTool().(type) {
	case *agentv1.ToolCall_ReadToolCall:
		return "Read"
	case *agentv1.ToolCall_UpdateTodosToolCall:
		return "TodoWrite"
	case *agentv1.ToolCall_ReadTodosToolCall:
		return "ReadTodos"
	case *agentv1.ToolCall_DeleteToolCall:
		return "Delete"
	case *agentv1.ToolCall_GrepToolCall:
		return "Grep"
	case *agentv1.ToolCall_GlobToolCall:
		return "Glob"
	case *agentv1.ToolCall_ShellToolCall:
		return "Shell"
	case *agentv1.ToolCall_AwaitToolCall:
		return "AwaitShell"
	case *agentv1.ToolCall_WriteShellStdinToolCall:
		return "WriteShellStdin"
	case *agentv1.ToolCall_EditToolCall:
		return inferEditToolNameFromToolCall(toolCall.GetEditToolCall())
	case *agentv1.ToolCall_LsToolCall:
		return "Ls"
	case *agentv1.ToolCall_McpToolCall:
		return "CallMcpTool"
	case *agentv1.ToolCall_ListMcpResourcesToolCall:
		return "ListMcpResources"
	case *agentv1.ToolCall_ReadMcpResourceToolCall:
		return "FetchMcpResource"
	case *agentv1.ToolCall_CreatePlanToolCall:
		return "CreatePlan"
	case *agentv1.ToolCall_AskQuestionToolCall:
		return "AskQuestion"
	case *agentv1.ToolCall_WebSearchToolCall:
		return "WebSearch"
	case *agentv1.ToolCall_WebFetchToolCall:
		return "WebFetch"
	case *agentv1.ToolCall_SwitchModeToolCall:
		return "SwitchMode"
	case *agentv1.ToolCall_GenerateImageToolCall:
		return "GenerateImage"
	case *agentv1.ToolCall_TaskToolCall:
		return "Task"
	default:
		return ""
	}
}

// deriveToolNameFromPendingExec 根据执行桥种类反推出 canonical 工具名。
func deriveToolNameFromPendingExec(pending runtimecore.PendingExec) string {
	switch strings.TrimSpace(pending.ExecKind) {
	case "read":
		return "Read"
	case "write":
		return "Write"
	case "delete":
		return "Delete"
	case "glob":
		return "Glob"
	case "grep":
		return "Grep"
	case "diagnostics":
		return "ReadLints"
	case "ls":
		return "Ls"
	case "mcp":
		return "CallMcpTool"
	case "list_mcp_resources":
		return "ListMcpResources"
	case "read_mcp_resource":
		return "FetchMcpResource"
	case "shell":
		return "Shell"
	case "await_shell":
		return "AwaitShell"
	case "write_shell_stdin":
		return "WriteShellStdin"
	case "force_background_shell":
		return "ForceBackgroundShell"
	case "subagent":
		return "Task"
	default:
		return ""
	}
}

func execKindFromToolName(name string) (string, bool) {
	switch strings.TrimSpace(name) {
	case "Read":
		return "read", true
	case "Write":
		return "write", true
	case "PatchEdit":
		return "patch_edit", true
	case "Delete":
		return "delete", true
	case "Glob":
		return "glob", true
	case "Grep":
		return "grep", true
	case "Ls":
		return "ls", true
	case "ReadLints":
		return "diagnostics", true
	case "CallMcpTool":
		return "mcp", true
	case "FetchMcpResource":
		return "read_mcp_resource", true
	case "Shell":
		return "shell", true
	case "AwaitShell":
		return "await_shell", true
	case "WriteShellStdin":
		return "write_shell_stdin", true
	case "ForceBackgroundShell":
		return "force_background_shell", true
	case "Task":
		return "subagent", true
	default:
		return "", false
	}
}

func isExecTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "Read", "Write", "PatchEdit", "Delete", "Shell", "WriteShellStdin", "ForceBackgroundShell", "Grep", "Glob", "Ls", "ReadLints", "CallMcpTool", "FetchMcpResource", "Task":
		return true
	default:
		return false
	}
}

func inferEditToolName(args *agentv1.EditArgs) string {
	if args != nil && args.StreamContent != nil {
		return "Write"
	}
	return "Edit"
}

func inferEditToolNameFromToolCall(toolCall *agentv1.EditToolCall) string {
	if toolCall == nil {
		return ""
	}
	if editResultLooksLikeStructuredEdit(toolCall.GetResult()) {
		return "Edit"
	}
	return inferEditToolName(toolCall.GetArgs())
}

func editResultLooksLikeStructuredEdit(result *agentv1.EditResult) bool {
	success := result.GetSuccess()
	if success == nil {
		return false
	}
	return success.BeforeFullFileContent != nil || success.DiffString != nil
}
