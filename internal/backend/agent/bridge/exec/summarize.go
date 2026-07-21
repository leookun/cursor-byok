package execbridge

import (
	"fmt"
	"strings"

	"cursor/gen/agentv1"
)

// summarizeReadResult 生成 Read 结果摘要。
func summarizeReadResult(result *agentv1.ReadResult) string {
	if result == nil {
		return "read result missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.ReadResult_Success:
		if item.Success.GetContent() != "" {
			content := truncateReplayLines("Read", item.Success.GetContent(), readReplayLineLimit)
			return truncateReplayText("Read", content, readReplayContentLimit)
		}
		if item.Success.GetData() != nil {
			return fmt.Sprintf("read binary bytes=%d", len(item.Success.GetData()))
		}
		return fmt.Sprintf("read success path=%s", item.Success.GetPath())
	case *agentv1.ReadResult_Error:
		return item.Error.GetError()
	case *agentv1.ReadResult_Rejected:
		return item.Rejected.GetReason()
	case *agentv1.ReadResult_FileNotFound:
		return fmt.Sprintf("file not found: %s", item.FileNotFound.GetPath())
	case *agentv1.ReadResult_PermissionDenied:
		return fmt.Sprintf("permission denied: %s", item.PermissionDenied.GetPath())
	case *agentv1.ReadResult_InvalidFile:
		return item.InvalidFile.GetReason()
	default:
		return "unknown read result"
	}
}

// summarizeWriteResult 生成 Write 结果摘要。
func summarizeWriteResult(result *agentv1.WriteResult) string {
	if result == nil {
		return "write result missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.WriteResult_Success:
		if after := strings.TrimSpace(item.Success.GetFileContentAfterWrite()); after != "" {
			return after
		}
		return fmt.Sprintf("write success path=%s lines=%d", item.Success.GetPath(), item.Success.GetLinesCreated())
	case *agentv1.WriteResult_PermissionDenied:
		return item.PermissionDenied.GetError()
	case *agentv1.WriteResult_NoSpace:
		return fmt.Sprintf("no space left: %s", item.NoSpace.GetPath())
	case *agentv1.WriteResult_Error:
		return item.Error.GetError()
	case *agentv1.WriteResult_Rejected:
		return item.Rejected.GetReason()
	default:
		return "unknown write result"
	}
}

// summarizeDiagnosticsResult 生成 ReadLints 对应的执行结果摘要。
func summarizeDiagnosticsResult(result *agentv1.DiagnosticsResult) string {
	if result == nil {
		return "diagnostics result missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.DiagnosticsResult_Success:
		return fmt.Sprintf("diagnostics success path=%s count=%d", item.Success.GetPath(), item.Success.GetTotalDiagnostics())
	case *agentv1.DiagnosticsResult_Error:
		return item.Error.GetError()
	case *agentv1.DiagnosticsResult_Rejected:
		return item.Rejected.GetReason()
	case *agentv1.DiagnosticsResult_FileNotFound:
		return fmt.Sprintf("diagnostics file not found: %s", item.FileNotFound.GetPath())
	case *agentv1.DiagnosticsResult_PermissionDenied:
		return fmt.Sprintf("diagnostics permission denied: %s", item.PermissionDenied.GetPath())
	default:
		return "unknown diagnostics result"
	}
}

// summarizeSubagentResult 生成 Task 对应的执行结果摘要。
func summarizeSubagentResult(result *agentv1.SubagentResult) string {
	if result == nil {
		return "subagent result missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.SubagentResult_Success:
		if text := strings.TrimSpace(item.Success.GetFinalMessage()); text != "" {
			return text
		}
		if isBackgroundSubagentSuccess(item.Success) {
			return fmt.Sprintf("subagent running in background agent_id=%s reason=%s transcript_path=%s",
				strings.TrimSpace(item.Success.GetAgentId()),
				item.Success.GetBackgroundReason().String(),
				strings.TrimSpace(item.Success.GetTranscriptPath()),
			)
		}
		return "subagent returned empty response"
	case *agentv1.SubagentResult_Error:
		return item.Error.GetError()
	default:
		return "unknown subagent result"
	}
}

// summarizeDeleteResult 生成 Delete 结果摘要。
func summarizeDeleteResult(result *agentv1.DeleteResult) string {
	if result == nil {
		return "delete result missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.DeleteResult_Success:
		return fmt.Sprintf("delete success path=%s", item.Success.GetPath())
	case *agentv1.DeleteResult_FileNotFound:
		return fmt.Sprintf("file not found: %s", item.FileNotFound.GetPath())
	case *agentv1.DeleteResult_NotFile:
		return fmt.Sprintf("not file: %s", item.NotFile.GetPath())
	case *agentv1.DeleteResult_PermissionDenied:
		return item.PermissionDenied.GetClientVisibleError()
	case *agentv1.DeleteResult_FileBusy:
		return item.FileBusy.GetPath()
	case *agentv1.DeleteResult_Rejected:
		return item.Rejected.GetReason()
	case *agentv1.DeleteResult_Error:
		return item.Error.GetError()
	default:
		return "unknown delete result"
	}
}

// summarizeGrepResult 生成 Grep 结果摘要。
func summarizeGrepResult(result *agentv1.GrepResult) string {
	if result == nil {
		return "grep result missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.GrepResult_Success:
		return fmt.Sprintf("grep success pattern=%s mode=%s", item.Success.GetPattern(), item.Success.GetOutputMode())
	case *agentv1.GrepResult_Error:
		return item.Error.GetError()
	default:
		return "unknown grep result"
	}
}

func summarizeGlobContinuationPayload(result *agentv1.GrepResult, argsJSON []byte) string {
	args, _ := decodeArgsMap(argsJSON)
	pattern := readGlobPatternArg(args)
	target := readGlobTargetDirectoryArg(args)
	if result == nil || result.GetSuccess() == nil {
		return formatGlobNoMatches(pattern, target)
	}
	filesResult := firstGrepFilesResult(result.GetSuccess())
	if filesResult == nil || len(filesResult.GetFiles()) == 0 {
		return formatGlobNoMatches(pattern, target)
	}
	files := filesResult.GetFiles()
	text := strings.Join(files, "\n")
	if total := int(filesResult.GetTotalFiles()); total > len(files) {
		text += fmt.Sprintf("\n...there are still %d files...", total-len(files))
	}
	return text
}

func formatGlobNoMatches(pattern string, target string) string {
	if pattern == "" && target == "" {
		return "no matches"
	}
	if target == "" {
		return fmt.Sprintf("no matches for %s", pattern)
	}
	if pattern == "" {
		return fmt.Sprintf("no matches in %s", target)
	}
	return fmt.Sprintf("no matches for %s in %s", pattern, target)
}

// summarizeLsResult 生成 Ls 结果摘要。
func summarizeLsResult(result *agentv1.LsResult) string {
	if result == nil {
		return "ls result missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.LsResult_Success:
		return fmt.Sprintf("ls success path=%s files=%d", item.Success.GetDirectoryTreeRoot().GetAbsPath(), item.Success.GetDirectoryTreeRoot().GetNumFiles())
	case *agentv1.LsResult_Error:
		return item.Error.GetError()
	case *agentv1.LsResult_Rejected:
		return item.Rejected.GetReason()
	case *agentv1.LsResult_Timeout:
		return fmt.Sprintf("ls timeout path=%s", item.Timeout.GetDirectoryTreeRoot().GetAbsPath())
	default:
		return "unknown ls result"
	}
}

// summarizeMcpResult 生成 MCP 执行结果摘要。
func summarizeMcpResult(result *agentv1.McpToolResult) string {
	if result == nil {
		return "mcp result missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.McpToolResult_Success:
		return fmt.Sprintf("mcp success content=%d", len(item.Success.GetContent()))
	case *agentv1.McpToolResult_Error:
		return item.Error.GetError()
	case *agentv1.McpToolResult_Rejected:
		return item.Rejected.GetReason()
	case *agentv1.McpToolResult_PermissionDenied:
		return item.PermissionDenied.GetError()
	default:
		return "unknown mcp result"
	}
}

// convertMcpResult 把 ExecClientMessage 中的 McpResult 映射为 ToolCall 使用的 McpToolResult。
func convertMcpResult(result *agentv1.McpResult) *agentv1.McpToolResult {
	if result == nil {
		return &agentv1.McpToolResult{
			Result: &agentv1.McpToolResult_Error{
				Error: &agentv1.McpToolError{Error: "mcp result missing"},
			},
		}
	}
	switch item := result.GetResult().(type) {
	case *agentv1.McpResult_Success:
		return &agentv1.McpToolResult{
			Result: &agentv1.McpToolResult_Success{
				Success: item.Success,
			},
		}
	case *agentv1.McpResult_Error:
		return &agentv1.McpToolResult{
			Result: &agentv1.McpToolResult_Error{
				Error: &agentv1.McpToolError{Error: item.Error.GetError()},
			},
		}
	case *agentv1.McpResult_Rejected:
		return &agentv1.McpToolResult{
			Result: &agentv1.McpToolResult_Rejected{
				Rejected: item.Rejected,
			},
		}
	case *agentv1.McpResult_PermissionDenied:
		return &agentv1.McpToolResult{
			Result: &agentv1.McpToolResult_PermissionDenied{
				PermissionDenied: item.PermissionDenied,
			},
		}
	case *agentv1.McpResult_ToolNotFound:
		return &agentv1.McpToolResult{
			Result: &agentv1.McpToolResult_Error{
				Error: &agentv1.McpToolError{
					Error: fmt.Sprintf("tool not found: %s", item.ToolNotFound.GetName()),
				},
			},
		}
	default:
		return &agentv1.McpToolResult{
			Result: &agentv1.McpToolResult_Error{
				Error: &agentv1.McpToolError{Error: "unknown mcp result"},
			},
		}
	}
}

// summarizeListMcpResourcesResult 生成 MCP 资源列表结果摘要。
func summarizeListMcpResourcesResult(result *agentv1.ListMcpResourcesExecResult) string {
	if result == nil {
		return "list mcp resources result missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.ListMcpResourcesExecResult_Success:
		return fmt.Sprintf("list mcp resources success count=%d", len(item.Success.GetResources()))
	case *agentv1.ListMcpResourcesExecResult_Error:
		return item.Error.GetError()
	case *agentv1.ListMcpResourcesExecResult_Rejected:
		return item.Rejected.GetReason()
	default:
		return "unknown list mcp resources result"
	}
}

// summarizeReadMcpResourceResult 生成读取 MCP 资源结果摘要。
func summarizeReadMcpResourceResult(result *agentv1.ReadMcpResourceExecResult) string {
	if result == nil {
		return "read mcp resource result missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.ReadMcpResourceExecResult_Success:
		if text := strings.TrimSpace(item.Success.GetText()); text != "" {
			return text
		}
		if blob := item.Success.GetBlob(); len(blob) > 0 {
			return fmt.Sprintf("read mcp resource blob=%d", len(blob))
		}
		return fmt.Sprintf("read mcp resource success uri=%s", item.Success.GetUri())
	case *agentv1.ReadMcpResourceExecResult_Error:
		return item.Error.GetError()
	case *agentv1.ReadMcpResourceExecResult_Rejected:
		return item.Rejected.GetReason()
	case *agentv1.ReadMcpResourceExecResult_NotFound:
		return fmt.Sprintf("mcp resource not found: %s", item.NotFound.GetUri())
	default:
		return "unknown read mcp resource result"
	}
}

func summarizeWriteShellStdinResult(result *agentv1.WriteShellStdinResult) string {
	if result == nil {
		return ""
	}
	switch item := result.GetResult().(type) {
	case *agentv1.WriteShellStdinResult_Success:
		if item.Success == nil {
			return "write shell stdin succeeded"
		}
		return fmt.Sprintf(
			"wrote input to shell %d (terminal file length before input: %d)",
			item.Success.GetShellId(),
			item.Success.GetTerminalFileLengthBeforeInputWritten(),
		)
	case *agentv1.WriteShellStdinResult_Error:
		if item.Error == nil {
			return "write shell stdin failed"
		}
		return fmt.Sprintf("write shell stdin failed: %s", strings.TrimSpace(item.Error.GetError()))
	default:
		return "write shell stdin completed"
	}
}

func summarizeForceBackgroundShellResult(result *agentv1.ForceBackgroundShellResult) string {
	if result == nil {
		return ""
	}
	switch result.GetStatus() {
	case agentv1.ForceBackgroundShellStatus_FORCE_BACKGROUND_SHELL_STATUS_ACCEPTED:
		return "force background shell accepted"
	case agentv1.ForceBackgroundShellStatus_FORCE_BACKGROUND_SHELL_STATUS_NOT_FOUND:
		return "force background shell target not found"
	default:
		return "force background shell completed"
	}
}

// convertDiagnostics 把 Diagnostic 转成 DiagnosticItem。
func convertDiagnostics(items []*agentv1.Diagnostic) []*agentv1.DiagnosticItem {
	if len(items) == 0 {
		return nil
	}
	result := make([]*agentv1.DiagnosticItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		result = append(result, &agentv1.DiagnosticItem{
			Severity: item.GetSeverity(),
			Range: &agentv1.DiagnosticRange{
				Start: item.GetRange().GetStart(),
				End:   item.GetRange().GetEnd(),
			},
			Message: item.GetMessage(),
			Source:  item.GetSource(),
			Code:    item.GetCode(),
			IsStale: item.GetIsStale(),
		})
	}
	return result
}

func isBackgroundSubagentSuccess(success *agentv1.SubagentSuccess) bool {
	if success == nil {
		return false
	}
	return success.GetBackgroundReason() != agentv1.SubagentBackgroundReason_SUBAGENT_BACKGROUND_REASON_UNSPECIFIED ||
		strings.TrimSpace(success.GetTranscriptPath()) != ""
}

// buildEmptyGlobResult 构造 Glob 工具在没有匹配文件时的占位 GrepResult。
func buildEmptyGlobResult(argsJSON []byte) *agentv1.GrepResult {
	args, _ := decodeArgsMap(argsJSON)
	path := readGlobTargetDirectoryArg(args)
	pattern := readGlobPatternArg(args)
	filesResult := &agentv1.GrepFilesResult{}
	success := &agentv1.GrepSuccess{
		Pattern:    pattern,
		Path:       path,
		OutputMode: "files_with_matches",
		WorkspaceResults: map[string]*agentv1.GrepUnionResult{
			path: {
				Result: &agentv1.GrepUnionResult_Files{
					Files: filesResult,
				},
			},
		},
	}
	return &agentv1.GrepResult{
		Result: &agentv1.GrepResult_Success{
			Success: success,
		},
	}
}
