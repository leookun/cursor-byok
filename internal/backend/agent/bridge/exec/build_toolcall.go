package execbridge

import (
	"encoding/json"
	"strings"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

// buildReadCompletedToolCall 构造 Read 对应的完成态 ToolCall。
func buildReadCompletedToolCall(toolCallID string, argsJSON []byte, result *agentv1.ReadResult) *agentv1.ToolCall {
	args, err := DecodeReadToolArgs(argsJSON)
	if err != nil || args == nil {
		args = &agentv1.ReadToolArgs{}
	}
	if strings.TrimSpace(args.GetPath()) == "" && result != nil && result.GetSuccess() != nil {
		args.Path = result.GetSuccess().GetPath()
	}
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ReadToolCall{
			ReadToolCall: &agentv1.ReadToolCall{
				Args:   args,
				Result: convertReadResultToReadToolResult(result),
			},
		},
	}
}

// buildDeleteCompletedToolCall 构造 Delete 对应的完成态 ToolCall。
func buildDeleteCompletedToolCall(toolCallID string, argsJSON []byte, result *agentv1.DeleteResult) *agentv1.ToolCall {
	var args agentv1.DeleteArgs
	_ = json.Unmarshal(argsJSON, &args)
	args.ToolCallId = toolCallID
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_DeleteToolCall{
			DeleteToolCall: &agentv1.DeleteToolCall{
				Args:   &args,
				Result: result,
			},
		},
	}
}

// buildGlobCompletedToolCall 构造 Glob 对应的完成态 ToolCall。
func buildGlobCompletedToolCall(toolCallID string, argsJSON []byte, result *agentv1.GrepResult) *agentv1.ToolCall {
	args, _ := decodeArgsMap(argsJSON)
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_GlobToolCall{
			GlobToolCall: &agentv1.GlobToolCall{
				Args:   buildGlobToolArgs(args),
				Result: convertGrepResultToGlobToolResult(result, args),
			},
		},
	}
}

// buildWriteCompletedToolCall 构造 Write 对应的完成态 ToolCall。
func buildWriteCompletedToolCall(toolCallID string, argsJSON []byte, result *agentv1.WriteResult) *agentv1.ToolCall {
	args, _ := decodeArgsMap(argsJSON)
	streamContent := stringPtr(readStringArg(args, "contents", "content", "stream_content", "streamContent"))
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_EditToolCall{
			EditToolCall: &agentv1.EditToolCall{
				Args: &agentv1.EditArgs{
					Path:          strings.TrimSpace(readStringArg(args, "path")),
					StreamContent: streamContent,
				},
				Result: convertWriteResultToEditResult(result),
			},
		},
	}
}

// buildReadLintsCompletedToolCall 构造 ReadLints 对应的完成态 ToolCall。
func buildReadLintsCompletedToolCall(argsJSON []byte, result *agentv1.DiagnosticsResult) *agentv1.ToolCall {
	args, _ := decodeArgsMap(argsJSON)
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ReadLintsToolCall{
			ReadLintsToolCall: &agentv1.ReadLintsToolCall{
				Args: &agentv1.ReadLintsToolArgs{
					Paths: readStringSliceArg(args, "paths"),
				},
				Result: convertDiagnosticsResultToReadLintsToolResult(result),
			},
		},
	}
}

// buildTaskCompletedToolCall 构造 Task 对应的完成态 ToolCall。
func buildTaskCompletedToolCall(argsJSON []byte, result *agentv1.SubagentResult) *agentv1.ToolCall {
	args, _ := decodeArgsMap(argsJSON)
	readonly := readBoolArg(args, "readonly", "readOnly")
	taskArgs := &agentv1.TaskArgs{
		Description:  strings.TrimSpace(readStringArg(args, "description")),
		Prompt:       strings.TrimSpace(readStringArg(args, "prompt")),
		SubagentType: subagentTypeProtoFromString(strings.TrimSpace(readStringArg(args, "subagent_type", "subagentType"))),
		Model:        stringPtr(strings.TrimSpace(readStringArg(args, "model"))),
		Resume:       stringPtr(strings.TrimSpace(readStringArg(args, "resume"))),
		Attachments:  readStringSliceArg(args, "attachments"),
		Mode:         taskModeFromReadonly(readonly),
	}
	if agentID := strings.TrimSpace(readStringArg(args, "agentId", "agent_id")); agentID != "" {
		taskArgs.AgentId = &agentID
	}
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_TaskToolCall{
			TaskToolCall: &agentv1.TaskToolCall{
				Args:   taskArgs,
				Result: convertSubagentResultToTaskResult(result),
			},
		},
	}
}

func taskModeFromReadonly(readonly bool) agentv1.TaskMode {
	if readonly {
		return agentv1.TaskMode_TASK_MODE_PLAN
	}
	return agentv1.TaskMode_TASK_MODE_AGENT
}

func subagentTypeProtoFromString(raw string) *agentv1.SubagentType {
	switch strings.TrimSpace(raw) {
	case "explore":
		return &agentv1.SubagentType{Type: &agentv1.SubagentType_Explore{Explore: &agentv1.SubagentTypeExplore{}}}
	case "browser-use", "browserUse":
		return &agentv1.SubagentType{Type: &agentv1.SubagentType_BrowserUse{BrowserUse: &agentv1.SubagentTypeBrowserUse{}}}
	case "shell":
		return &agentv1.SubagentType{Type: &agentv1.SubagentType_Shell{Shell: &agentv1.SubagentTypeShell{}}}
	case "":
		return &agentv1.SubagentType{Type: &agentv1.SubagentType_Unspecified{Unspecified: &agentv1.SubagentTypeUnspecified{}}}
	default:
		return &agentv1.SubagentType{
			Type: &agentv1.SubagentType_Custom{
				Custom: &agentv1.SubagentTypeCustom{Name: strings.TrimSpace(raw)},
			},
		}
	}
}

// buildWriteShellStdinCompletedToolCall 构造 WriteShellStdin 对应的完成态 ToolCall。
func buildWriteShellStdinCompletedToolCall(argsJSON []byte, result *agentv1.WriteShellStdinResult) *agentv1.ToolCall {
	args, err := decodeWriteShellStdinArgs(argsJSON)
	if err != nil {
		args = writeShellStdinArgs{ShellID: 0, Chars: ""}
	}
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_WriteShellStdinToolCall{
			WriteShellStdinToolCall: &agentv1.WriteShellStdinToolCall{
				Args: &agentv1.WriteShellStdinArgs{
					ShellId: args.ShellID,
					Chars:   args.Chars,
				},
				Result: result,
			},
		},
	}
}

// buildGrepCompletedToolCall 构造 Grep 对应的完成态 ToolCall。
func buildGrepCompletedToolCall(toolCallID string, argsJSON []byte, result *agentv1.GrepResult) *agentv1.ToolCall {
	args, err := DecodeGrepToolArgs(argsJSON, toolCallID)
	if err != nil && args == nil {
		args = &agentv1.GrepArgs{ToolCallId: toolCallID}
	}
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_GrepToolCall{
			GrepToolCall: &agentv1.GrepToolCall{
				Args:   args,
				Result: result,
			},
		},
	}
}

// buildLsCompletedToolCall 构造 Ls 对应的完成态 ToolCall。
func buildLsCompletedToolCall(toolCallID string, argsJSON []byte, result *agentv1.LsResult) *agentv1.ToolCall {
	var input struct {
		Path   string   `json:"path"`
		Ignore []string `json:"ignore,omitempty"`
	}
	_ = json.Unmarshal(argsJSON, &input)
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_LsToolCall{
			LsToolCall: &agentv1.LsToolCall{
				Args: &agentv1.LsArgs{
					Path:       strings.TrimSpace(input.Path),
					Ignore:     append([]string(nil), input.Ignore...),
					ToolCallId: toolCallID,
				},
				Result: result,
			},
		},
	}
}

// buildMcpCompletedToolCall 构造 CallMcpTool 对应的完成态 ToolCall。
func buildMcpCompletedToolCall(toolCallID string, argsJSON []byte, result *agentv1.McpToolResult) *agentv1.ToolCall {
	input, _ := runtimecore.DecodeMCPToolPayload(argsJSON)
	serverIdentifier := strings.TrimSpace(input.Server)
	if serverIdentifier == "" {
		serverIdentifier = strings.TrimSpace(input.ProviderIdentifier)
	}
	toolName := strings.TrimSpace(input.ToolName)
	if toolName == "" {
		toolName = runtimecore.InferMCPToolName(serverIdentifier, input.Name)
	}
	if serverIdentifier == "" && strings.TrimSpace(input.Name) != "" {
		serverIdentifier = runtimecore.InferMCPServerIdentifier(input.Name)
	}
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_McpToolCall{
			McpToolCall: &agentv1.McpToolCall{
				Args: &agentv1.McpArgs{
					Name:               canonicalMCPToolLookupName(serverIdentifier, toolName),
					Args:               buildStructValueMap(input.Arguments),
					ToolCallId:         toolCallID,
					ProviderIdentifier: serverIdentifier,
					ToolName:           toolName,
				},
				Result: result,
			},
		},
	}
}

func canonicalMCPToolLookupName(server string, toolName string) string {
	trimmedServer := strings.TrimSpace(server)
	trimmedToolName := strings.TrimSpace(toolName)
	if trimmedToolName == "" {
		return ""
	}
	if trimmedServer == "" {
		return trimmedToolName
	}
	return trimmedServer + "-" + trimmedToolName
}

// buildListMcpResourcesCompletedToolCall 构造 ListMcpResources 对应的完成态 ToolCall。
func buildListMcpResourcesCompletedToolCall(argsJSON []byte, result *agentv1.ListMcpResourcesExecResult) *agentv1.ToolCall {
	var input struct {
		Server string `json:"server,omitempty"`
	}
	_ = json.Unmarshal(argsJSON, &input)
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ListMcpResourcesToolCall{
			ListMcpResourcesToolCall: &agentv1.ListMcpResourcesToolCall{
				Args: &agentv1.ListMcpResourcesExecArgs{
					Server: stringPtr(strings.TrimSpace(input.Server)),
				},
				Result: result,
			},
		},
	}
}

// buildReadMcpResourceCompletedToolCall 构造 FetchMcpResource 对应的完成态 ToolCall。
func buildReadMcpResourceCompletedToolCall(argsJSON []byte, result *agentv1.ReadMcpResourceExecResult) *agentv1.ToolCall {
	var input struct {
		Server       string `json:"server"`
		URI          string `json:"uri"`
		DownloadPath string `json:"downloadPath,omitempty"`
	}
	_ = json.Unmarshal(argsJSON, &input)
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ReadMcpResourceToolCall{
			ReadMcpResourceToolCall: &agentv1.ReadMcpResourceToolCall{
				Args: &agentv1.ReadMcpResourceExecArgs{
					Server:       strings.TrimSpace(input.Server),
					Uri:          strings.TrimSpace(input.URI),
					DownloadPath: stringPtr(strings.TrimSpace(input.DownloadPath)),
				},
				Result: result,
			},
		},
	}
}

// convertReadResultToReadToolResult 把 `ReadResult` 映射为 `ReadToolResult`。
func convertReadResultToReadToolResult(result *agentv1.ReadResult) *agentv1.ReadToolResult {
	if result == nil {
		return &agentv1.ReadToolResult{
			Result: &agentv1.ReadToolResult_Error{
				Error: &agentv1.ReadToolError{ErrorMessage: "read result missing"},
			},
		}
	}

	switch item := result.GetResult().(type) {
	case *agentv1.ReadResult_Success:
		content := item.Success.GetContent()
		data := item.Success.GetData()
		exceededLimit := item.Success.GetTruncated()
		if content != "" {
			original := content
			content = truncateReplayLines("Read", content, readReplayLineLimit)
			content = truncateReplayText("Read", content, readReplayContentLimit)
			if content != original {
				exceededLimit = true
			}
		}
		toolSuccess := &agentv1.ReadToolSuccess{
			IsEmpty:       strings.TrimSpace(item.Success.GetContent()) == "" && len(item.Success.GetData()) == 0,
			ExceededLimit: exceededLimit,
			TotalLines:    uint32(item.Success.GetTotalLines()),
			FileSize:      uint32(item.Success.GetFileSize()),
			Path:          item.Success.GetPath(),
		}
		if content != "" {
			toolSuccess.Output = &agentv1.ReadToolSuccess_Content{Content: content}
		} else if len(data) > 0 {
			if len(data) > readReplayBinaryLimit {
				toolSuccess.ExceededLimit = true
				toolSuccess.Output = &agentv1.ReadToolSuccess_Content{
					Content: replayTruncationNotice("Read binary data", readReplayBinaryLimit, 0, len(data)),
				}
			} else {
				toolSuccess.Output = &agentv1.ReadToolSuccess_Data{Data: append([]byte(nil), data...)}
			}
		}
		return &agentv1.ReadToolResult{
			Result: &agentv1.ReadToolResult_Success{
				Success: toolSuccess,
			},
		}
	case *agentv1.ReadResult_Error:
		return &agentv1.ReadToolResult{
			Result: &agentv1.ReadToolResult_Error{
				Error: &agentv1.ReadToolError{ErrorMessage: item.Error.GetError()},
			},
		}
	case *agentv1.ReadResult_Rejected:
		return &agentv1.ReadToolResult{
			Result: &agentv1.ReadToolResult_Error{
				Error: &agentv1.ReadToolError{ErrorMessage: item.Rejected.GetReason()},
			},
		}
	case *agentv1.ReadResult_FileNotFound:
		return &agentv1.ReadToolResult{
			Result: &agentv1.ReadToolResult_Error{
				Error: &agentv1.ReadToolError{ErrorMessage: summarizeReadResult(result)},
			},
		}
	case *agentv1.ReadResult_PermissionDenied:
		return &agentv1.ReadToolResult{
			Result: &agentv1.ReadToolResult_Error{
				Error: &agentv1.ReadToolError{ErrorMessage: summarizeReadResult(result)},
			},
		}
	case *agentv1.ReadResult_InvalidFile:
		return &agentv1.ReadToolResult{
			Result: &agentv1.ReadToolResult_Error{
				Error: &agentv1.ReadToolError{ErrorMessage: summarizeReadResult(result)},
			},
		}
	default:
		return &agentv1.ReadToolResult{
			Result: &agentv1.ReadToolResult_Error{
				Error: &agentv1.ReadToolError{ErrorMessage: "unknown read result"},
			},
		}
	}
}

// convertGrepResultToGlobToolResult 把 grep files mode 结果映射为 GlobToolResult。
func convertGrepResultToGlobToolResult(result *agentv1.GrepResult, args map[string]any) *agentv1.GlobToolResult {
	if result == nil {
		return &agentv1.GlobToolResult{
			Result: &agentv1.GlobToolResult_Error{
				Error: &agentv1.GlobToolError{Error: "glob result missing"},
			},
		}
	}
	switch item := result.GetResult().(type) {
	case *agentv1.GrepResult_Success:
		filesResult := firstGrepFilesResult(item.Success)
		if filesResult == nil {
			return &agentv1.GlobToolResult{
				Result: &agentv1.GlobToolResult_Error{
					Error: &agentv1.GlobToolError{Error: "glob files result missing"},
				},
			}
		}
		return &agentv1.GlobToolResult{
			Result: &agentv1.GlobToolResult_Success{
				Success: &agentv1.GlobToolSuccess{
					Pattern:          readGlobPatternArg(args),
					Path:             readGlobTargetDirectoryArg(args),
					Files:            append([]string(nil), filesResult.GetFiles()...),
					TotalFiles:       filesResult.GetTotalFiles(),
					ClientTruncated:  filesResult.GetClientTruncated(),
					RipgrepTruncated: filesResult.GetRipgrepTruncated(),
				},
			},
		}
	case *agentv1.GrepResult_Error:
		return &agentv1.GlobToolResult{
			Result: &agentv1.GlobToolResult_Error{
				Error: &agentv1.GlobToolError{Error: item.Error.GetError()},
			},
		}
	default:
		return &agentv1.GlobToolResult{
			Result: &agentv1.GlobToolResult_Error{
				Error: &agentv1.GlobToolError{Error: "unknown glob result"},
			},
		}
	}
}

// convertWriteResultToEditResult 把 WriteResult 映射为 EditResult。
func convertWriteResultToEditResult(result *agentv1.WriteResult) *agentv1.EditResult {
	if result == nil {
		return &agentv1.EditResult{
			Result: &agentv1.EditResult_Error{
				Error: &agentv1.EditError{Error: "write result missing"},
			},
		}
	}
	switch item := result.GetResult().(type) {
	case *agentv1.WriteResult_Success:
		success := &agentv1.EditSuccess{
			Path:                 item.Success.GetPath(),
			AfterFullFileContent: item.Success.GetFileContentAfterWrite(),
		}
		return &agentv1.EditResult{
			Result: &agentv1.EditResult_Success{Success: success},
		}
	case *agentv1.WriteResult_PermissionDenied:
		return &agentv1.EditResult{
			Result: &agentv1.EditResult_WritePermissionDenied{
				WritePermissionDenied: &agentv1.EditWritePermissionDenied{
					Path:  item.PermissionDenied.GetPath(),
					Error: item.PermissionDenied.GetError(),
				},
			},
		}
	case *agentv1.WriteResult_Rejected:
		return &agentv1.EditResult{
			Result: &agentv1.EditResult_Rejected{
				Rejected: &agentv1.EditRejected{
					Path:   item.Rejected.GetPath(),
					Reason: item.Rejected.GetReason(),
				},
			},
		}
	case *agentv1.WriteResult_Error:
		return &agentv1.EditResult{
			Result: &agentv1.EditResult_Error{
				Error: &agentv1.EditError{
					Path:              item.Error.GetPath(),
					Error:             item.Error.GetError(),
					ModelVisibleError: stringPtr(item.Error.GetError()),
				},
			},
		}
	case *agentv1.WriteResult_NoSpace:
		return &agentv1.EditResult{
			Result: &agentv1.EditResult_Error{
				Error: &agentv1.EditError{
					Path:              item.NoSpace.GetPath(),
					Error:             "no space left",
					ModelVisibleError: stringPtr("no space left"),
				},
			},
		}
	default:
		return &agentv1.EditResult{
			Result: &agentv1.EditResult_Error{
				Error: &agentv1.EditError{Error: "unknown write result"},
			},
		}
	}
}

// convertDiagnosticsResultToReadLintsToolResult 把 DiagnosticsResult 映射为 ReadLintsToolResult。
func convertDiagnosticsResultToReadLintsToolResult(result *agentv1.DiagnosticsResult) *agentv1.ReadLintsToolResult {
	if result == nil {
		return &agentv1.ReadLintsToolResult{
			Result: &agentv1.ReadLintsToolResult_Error{
				Error: &agentv1.ReadLintsToolError{ErrorMessage: "diagnostics result missing"},
			},
		}
	}
	switch item := result.GetResult().(type) {
	case *agentv1.DiagnosticsResult_Success:
		fileDiagnostics := &agentv1.FileDiagnostics{
			Path:             item.Success.GetPath(),
			Diagnostics:      convertDiagnostics(item.Success.GetDiagnostics()),
			DiagnosticsCount: item.Success.GetTotalDiagnostics(),
		}
		return &agentv1.ReadLintsToolResult{
			Result: &agentv1.ReadLintsToolResult_Success{
				Success: &agentv1.ReadLintsToolSuccess{
					FileDiagnostics:  []*agentv1.FileDiagnostics{fileDiagnostics},
					TotalFiles:       1,
					TotalDiagnostics: int32(len(fileDiagnostics.GetDiagnostics())),
				},
			},
		}
	case *agentv1.DiagnosticsResult_Error:
		return &agentv1.ReadLintsToolResult{
			Result: &agentv1.ReadLintsToolResult_Error{
				Error: &agentv1.ReadLintsToolError{ErrorMessage: item.Error.GetError()},
			},
		}
	case *agentv1.DiagnosticsResult_Rejected:
		return &agentv1.ReadLintsToolResult{
			Result: &agentv1.ReadLintsToolResult_Error{
				Error: &agentv1.ReadLintsToolError{ErrorMessage: item.Rejected.GetReason()},
			},
		}
	default:
		return &agentv1.ReadLintsToolResult{
			Result: &agentv1.ReadLintsToolResult_Error{
				Error: &agentv1.ReadLintsToolError{ErrorMessage: "unknown diagnostics result"},
			},
		}
	}
}

// convertSubagentResultToTaskResult 把 SubagentResult 映射为 TaskResult。
func convertSubagentResultToTaskResult(result *agentv1.SubagentResult) *agentv1.TaskResult {
	if result == nil {
		return &agentv1.TaskResult{
			Result: &agentv1.TaskResult_Error{
				Error: &agentv1.TaskError{Error: "subagent result missing"},
			},
		}
	}
	switch item := result.GetResult().(type) {
	case *agentv1.SubagentResult_Success:
		steps := make([]*agentv1.ConversationStep, 0, 1)
		if text := strings.TrimSpace(item.Success.GetFinalMessage()); text != "" {
			steps = append(steps, &agentv1.ConversationStep{
				Message: &agentv1.ConversationStep_AssistantMessage{
					AssistantMessage: &agentv1.AssistantMessage{Text: text},
				},
			})
		}
		if len(steps) == 0 {
			if isBackgroundSubagentSuccess(item.Success) {
				return &agentv1.TaskResult{
					Result: &agentv1.TaskResult_Success{
						Success: &agentv1.TaskSuccess{
							AgentId:          stringPtr(strings.TrimSpace(item.Success.GetAgentId())),
							IsBackground:     true,
							BackgroundReason: item.Success.GetBackgroundReason(),
							TranscriptPath:   stringPtr(strings.TrimSpace(item.Success.GetTranscriptPath())),
						},
					},
				}
			}
			return &agentv1.TaskResult{
				Result: &agentv1.TaskResult_Error{
					Error: &agentv1.TaskError{Error: "subagent returned empty response"},
				},
			}
		}
		return &agentv1.TaskResult{
			Result: &agentv1.TaskResult_Success{
				Success: &agentv1.TaskSuccess{
					ConversationSteps: steps,
					AgentId:           stringPtr(strings.TrimSpace(item.Success.GetAgentId())),
				},
			},
		}
	case *agentv1.SubagentResult_Error:
		return &agentv1.TaskResult{
			Result: &agentv1.TaskResult_Error{
				Error: &agentv1.TaskError{Error: item.Error.GetError()},
			},
		}
	default:
		return &agentv1.TaskResult{
			Result: &agentv1.TaskResult_Error{
				Error: &agentv1.TaskError{Error: "unknown subagent result"},
			},
		}
	}
}

// firstGrepFilesResult 取 workspaceResults 中首个 files 结果。
func firstGrepFilesResult(success *agentv1.GrepSuccess) *agentv1.GrepFilesResult {
	if success == nil {
		return nil
	}
	for _, item := range success.GetWorkspaceResults() {
		if item == nil {
			continue
		}
		if files := item.GetFiles(); files != nil {
			return files
		}
	}
	if active := success.GetActiveEditorResult(); active != nil {
		if files := active.GetFiles(); files != nil {
			return files
		}
	}
	return nil
}
