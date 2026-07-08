// bridge.go 实现 MVP 阶段的执行桥协议映射。
package execbridge

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

// ExecApplyResult 表示一次执行桥结果归一化后的最小产物。
type ExecApplyResult struct {
	// ToolCallID 表示结果所属工具调用标识。
	ToolCallID string
	// ExecID 表示结果所属执行桥标识。
	ExecID string
	// IsTerminal 表示该执行桥是否已经收口。
	IsTerminal bool
	// ShellOutputDelta 保存 shell 流输出的增量事件。
	ShellOutputDelta *agentv1.ShellOutputDeltaUpdate
	// ToolResultPayload 保存可回写给模型的工具结果摘要。
	ToolResultPayload string
	// ToolCall 保存可用于发 ToolCallCompletedUpdate 的工具调用对象；当前仅对支持 ToolCall 的执行型工具可用。
	ToolCall *agentv1.ToolCall
	// ExecuteHookResponse 保存 execute hook 的结构化响应。
	ExecuteHookResponse *agentv1.ExecuteHookResponse
}

// OpenExecContext 表示执行桥打开请求时需要的最小上下文。
type OpenExecContext struct {
	ConversationID         string
	ModelID                string
	SubagentModelOverrides map[string]runtimecore.SubagentModelOverrideSelection
}

// ExecBridge 定义执行桥接口。
type ExecBridge interface {
	// OpenExec 打开一条执行桥请求。
	OpenExec(openContext OpenExecContext, toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error)
	// OpenExecuteHook 打开一条 execute hook 请求。
	OpenExecuteHook(request *agentv1.ExecuteHookRequest, execKind string) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error)
	// ApplyExecClientMessage 处理客户端执行结果。
	ApplyExecClientMessage(msg *agentv1.ExecClientMessage, pending runtimecore.PendingExec) (ExecApplyResult, error)
	// ApplyExecClientControl 处理客户端执行控制消息。
	ApplyExecClientControl(msg *agentv1.ExecClientControlMessage, pending runtimecore.PendingExec) (ExecApplyResult, error)
}

// Bridge 实现当前 MVP 阶段的执行桥。
type Bridge struct {
	// nextMessageID 生成 uint32 级别的桥消息编号。
	nextMessageID atomic.Uint32
}

// NewBridge 创建一个执行桥实例。
func NewBridge() *Bridge {
	return &Bridge{}
}

// OpenExec 打开一条执行型工具调用。
func (bridge *Bridge) OpenExec(openContext OpenExecContext, toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	switch strings.TrimSpace(toolCall.ToolName) {
	case "Read":
		return bridge.openRead(toolCall)
	case "Write":
		return bridge.openWrite(toolCall)
	case "Delete":
		return bridge.openDelete(toolCall)
	case "Glob":
		return bridge.openGlob(toolCall)
	case "Grep":
		return bridge.openGrep(toolCall)
	case "ReadLints":
		return bridge.openReadLints(toolCall)
	case "Ls":
		return bridge.openLs(toolCall)
	case "Shell":
		return bridge.openShell(toolCall)
	case "WriteShellStdin":
		return bridge.openWriteShellStdin(toolCall)
	case "ForceBackgroundShell":
		return bridge.openForceBackgroundShell(toolCall)
	case "Task":
		return bridge.openTask(openContext, toolCall)
	case "CallMcpTool":
		return bridge.openMcp(toolCall)
	case "ListMcpResources":
		return bridge.openListMcpResources(toolCall)
	case "FetchMcpResource":
		return bridge.openReadMcpResource(toolCall)
	default:
		return nil, runtimecore.PendingExec{}, fmt.Errorf("unsupported exec tool: %s", toolCall.ToolName)
	}
}

// OpenExecuteHook 打开一条 execute hook 请求。
func (bridge *Bridge) OpenExecuteHook(request *agentv1.ExecuteHookRequest, execKind string) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	if request == nil {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("execute hook request is required")
	}
	messageID := bridge.nextID()
	execID := fmt.Sprintf("exec-hook-%d", time.Now().UnixNano())
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Id:     messageID,
				ExecId: execID,
				Message: &agentv1.ExecServerMessage_ExecuteHookArgs{
					ExecuteHookArgs: &agentv1.ExecuteHookArgs{
						Request: request,
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingExec{
		MessageID:   messageID,
		ExecID:      execID,
		ExecKind:    strings.TrimSpace(execKind),
		StreamState: "opened",
		OpenedAt:    time.Now().UTC(),
	}, nil
}

// ApplyExecClientMessage 处理客户端执行结果消息。
func (bridge *Bridge) ApplyExecClientMessage(msg *agentv1.ExecClientMessage, pending runtimecore.PendingExec) (ExecApplyResult, error) {
	if msg == nil {
		return ExecApplyResult{}, fmt.Errorf("exec client message is required")
	}

	result := ExecApplyResult{
		ToolCallID: pending.ToolCallID,
		ExecID:     pending.ExecID,
	}
	switch pending.ExecKind {
	case "read":
		readResult := normalizeReadResultForModel(msg.GetReadResult())
		result.ToolResultPayload = summarizeReadResult(readResult)
		result.ToolCall = buildReadCompletedToolCall(pending.ToolCallID, pending.ArgsJSON, readResult)
		result.IsTerminal = true
		return result, nil
	case "write":
		result.ToolResultPayload = summarizeWriteResult(msg.GetWriteResult())
		result.ToolCall = buildWriteCompletedToolCall(pending.ToolCallID, pending.ArgsJSON, msg.GetWriteResult())
		result.IsTerminal = true
		return result, nil
	case "delete":
		result.ToolResultPayload = summarizeDeleteResult(msg.GetDeleteResult())
		result.ToolCall = buildDeleteCompletedToolCall(pending.ToolCallID, pending.ArgsJSON, msg.GetDeleteResult())
		result.IsTerminal = true
		return result, nil
	case "glob":
		truncatedResult := truncateGlobResultForReplay(msg.GetGrepResult())
		result.ToolResultPayload = summarizeGlobContinuationPayload(truncatedResult, pending.ArgsJSON)
		result.ToolCall = buildGlobCompletedToolCall(pending.ToolCallID, pending.ArgsJSON, truncatedResult)
		result.IsTerminal = true
		return result, nil
	case "grep":
		truncatedResult := truncateGrepResultForReplay(msg.GetGrepResult())
		result.ToolResultPayload = summarizeGrepResult(truncatedResult)
		result.ToolCall = buildGrepCompletedToolCall(pending.ToolCallID, pending.ArgsJSON, truncatedResult)
		result.IsTerminal = true
		return result, nil
	case "diagnostics":
		result.ToolResultPayload = summarizeDiagnosticsResult(msg.GetDiagnosticsResult())
		result.ToolCall = buildReadLintsCompletedToolCall(pending.ArgsJSON, msg.GetDiagnosticsResult())
		result.IsTerminal = true
		return result, nil
	case "ls":
		result.ToolResultPayload = summarizeLsResult(msg.GetLsResult())
		result.ToolCall = buildLsCompletedToolCall(pending.ToolCallID, pending.ArgsJSON, msg.GetLsResult())
		result.IsTerminal = true
		return result, nil
	case "mcp":
		toolResult := truncateMcpToolResultForReplay(convertMcpResult(msg.GetMcpResult()))
		result.ToolResultPayload = summarizeMcpResult(toolResult)
		result.ToolCall = buildMcpCompletedToolCall(pending.ToolCallID, pending.ArgsJSON, toolResult)
		result.IsTerminal = true
		return result, nil
	case "list_mcp_resources":
		truncatedResult := truncateListMcpResourcesResultForReplay(msg.GetListMcpResourcesExecResult())
		result.ToolResultPayload = summarizeListMcpResourcesResult(truncatedResult)
		result.ToolCall = buildListMcpResourcesCompletedToolCall(pending.ArgsJSON, truncatedResult)
		result.IsTerminal = true
		return result, nil
	case "read_mcp_resource":
		truncatedResult := truncateReadMcpResourceResultForReplay(msg.GetReadMcpResourceExecResult())
		result.ToolResultPayload = summarizeReadMcpResourceResult(truncatedResult)
		result.ToolCall = buildReadMcpResourceCompletedToolCall(pending.ArgsJSON, truncatedResult)
		result.IsTerminal = true
		return result, nil
	case "subagent":
		result.ToolResultPayload = summarizeSubagentResult(msg.GetSubagentResult())
		result.ToolCall = buildTaskCompletedToolCall(pending.ArgsJSON, msg.GetSubagentResult())
		result.IsTerminal = true
		return result, nil
	case "write_shell_stdin":
		writeResult := msg.GetWriteShellStdinResult()
		if writeResult == nil {
			return ExecApplyResult{}, fmt.Errorf("write shell stdin result is required")
		}
		result.ToolResultPayload = summarizeWriteShellStdinResult(writeResult)
		result.ToolCall = buildWriteShellStdinCompletedToolCall(pending.ArgsJSON, writeResult)
		result.IsTerminal = true
		return result, nil
	case "force_background_shell":
		forceResult := msg.GetForceBackgroundShellResult()
		if forceResult == nil {
			return ExecApplyResult{}, fmt.Errorf("force background shell result is required")
		}
		result.ToolResultPayload = summarizeForceBackgroundShellResult(forceResult)
		result.IsTerminal = true
		return result, nil
	case "execute_hook_pre_compact":
		hookResult := msg.GetExecuteHookResult()
		if hookResult == nil {
			return ExecApplyResult{}, fmt.Errorf("execute hook result is required")
		}
		result.ExecuteHookResponse = hookResult.GetResponse()
		if preCompact := hookResult.GetResponse().GetPreCompact(); preCompact != nil {
			result.ToolResultPayload = strings.TrimSpace(preCompact.GetUserMessage())
		}
		result.IsTerminal = true
		return result, nil
	case "shell":
		shellResult := msg.GetShellStream()
		if shellResult == nil {
			return ExecApplyResult{}, fmt.Errorf("shell stream payload is required")
		}
		switch event := shellResult.GetEvent().(type) {
		case *agentv1.ShellStream_Stdout:
			stdoutText := DecodeShellStdout(event.Stdout)
			result.ShellOutputDelta = &agentv1.ShellOutputDeltaUpdate{
				Event: &agentv1.ShellOutputDeltaUpdate_Stdout{
					Stdout: event.Stdout,
				},
			}
			result.ToolResultPayload = stdoutText
			return result, nil
		case *agentv1.ShellStream_Stderr:
			result.ShellOutputDelta = &agentv1.ShellOutputDeltaUpdate{
				Event: &agentv1.ShellOutputDeltaUpdate_Stderr{
					Stderr: event.Stderr,
				},
			}
			result.ToolResultPayload = event.Stderr.GetData()
			return result, nil
		case *agentv1.ShellStream_Start:
			result.ShellOutputDelta = &agentv1.ShellOutputDeltaUpdate{
				Event: &agentv1.ShellOutputDeltaUpdate_Start{
					Start: event.Start,
				},
			}
			return result, nil
		case *agentv1.ShellStream_Exit:
			result.ShellOutputDelta = &agentv1.ShellOutputDeltaUpdate{
				Event: &agentv1.ShellOutputDeltaUpdate_Exit{
					Exit: event.Exit,
				},
			}
			stdout, stderr := truncateShellStreamsForReplay(pending.StdoutBuffer, pending.StderrBuffer)
			result.ToolResultPayload = summarizeShellTerminalPayload(stdout, stderr, event.Exit, false)
			result.ToolCall = buildShellCompletedToolCall(pending.ToolCallID, pending.ArgsJSON, stdout, stderr, event.Exit)
			result.IsTerminal = true
			return result, nil
		case *agentv1.ShellStream_Rejected:
			result.ToolResultPayload = fmt.Sprintf("shell rejected: %s", strings.TrimSpace(event.Rejected.GetReason()))
			result.ToolCall = buildShellRejectedToolCall(pending.ToolCallID, pending.ArgsJSON, event.Rejected)
			result.IsTerminal = true
			return result, nil
		case *agentv1.ShellStream_PermissionDenied:
			result.ToolResultPayload = fmt.Sprintf("shell permission denied: %s", strings.TrimSpace(event.PermissionDenied.GetError()))
			result.ToolCall = buildShellPermissionDeniedToolCall(pending.ToolCallID, pending.ArgsJSON, event.PermissionDenied)
			result.IsTerminal = true
			return result, nil
		case *agentv1.ShellStream_Backgrounded:
			result.ToolResultPayload = fmt.Sprintf("shell backgrounded: %d", event.Backgrounded.GetShellId())
			result.ToolCall = buildShellBackgroundedToolCall(pending.ToolCallID, pending.ArgsJSON, event.Backgrounded)
			result.IsTerminal = true
			return result, nil
		default:
			return ExecApplyResult{}, fmt.Errorf("unsupported shell stream event")
		}
	default:
		return ExecApplyResult{}, fmt.Errorf("unsupported pending exec kind: %s", pending.ExecKind)
	}
}

// ApplyExecClientControl 处理客户端执行控制消息。
func (bridge *Bridge) ApplyExecClientControl(msg *agentv1.ExecClientControlMessage, pending runtimecore.PendingExec) (ExecApplyResult, error) {
	if msg == nil {
		return ExecApplyResult{}, fmt.Errorf("exec client control message is required")
	}

	result := ExecApplyResult{
		ToolCallID: pending.ToolCallID,
		ExecID:     pending.ExecID,
	}
	switch message := msg.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_StreamClose:
		if isStreamingExecKind(pending.ExecKind) {
			result.IsTerminal = false
			result.ToolResultPayload = fmt.Sprintf("exec stream closed: id=%d", message.StreamClose.GetId())
			return result, nil
		}

		// Non-streaming exec kinds frequently emit streamClose as a transport-level
		// ack before the actual result arrives. Treating it as terminal corrupts
		// the pending tool result (for example Read -> "exec stream closed").
		result.IsTerminal = false
		result.ToolResultPayload = ""
		return result, nil
	case *agentv1.ExecClientControlMessage_Throw:
		result.IsTerminal = true
		result.ToolResultPayload = fmt.Sprintf("exec throw: %s", strings.TrimSpace(message.Throw.GetError()))
		return result, nil
	case *agentv1.ExecClientControlMessage_Heartbeat:
		result.ToolResultPayload = "exec heartbeat"
		return result, nil
	default:
		return ExecApplyResult{}, fmt.Errorf("unsupported exec client control payload")
	}
}

// isStreamingExecKind 判断当前 exec kind 是否属于依赖后续数据面终态的流式执行桥。
func isStreamingExecKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "shell":
		return true
	default:
		return false
	}
}

// nextID 返回下一个桥消息编号。
func (bridge *Bridge) nextID() uint32 {
	current := bridge.nextMessageID.Add(1)
	if current == 0 {
		current = bridge.nextMessageID.Add(1)
	}
	return current
}

type readExecArgs struct {
	Path   string
	Offset *int32
	Limit  *uint32
}

func decodeReadExecArgs(raw []byte) (readExecArgs, error) {
	args, err := decodeArgsMap(raw)
	if err != nil {
		return readExecArgs{}, err
	}
	result := readExecArgs{
		Path: strings.TrimSpace(readStringArg(args, "path")),
	}
	if result.Path == "" {
		return result, fmt.Errorf("Read path is required")
	}
	if offset, found, err := runtimecore.ReadInt32Arg(args, "offset"); err != nil {
		return result, err
	} else if found {
		result.Offset = int32Ptr(offset)
	}
	if limit, found, err := runtimecore.ReadUint32Arg(args, "limit"); err != nil {
		return result, err
	} else if found {
		result.Limit = uint32Ptr(limit)
	}
	return result, nil
}

// openRead 构造 Read 对应的执行桥请求。
func (bridge *Bridge) openRead(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	args, err := decodeReadExecArgs(toolCall.ArgsJSON)
	if err != nil {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("decode Read args failed: %w", err)
	}
	messageID := bridge.nextID()
	execID := fmt.Sprintf("exec-read-%d", time.Now().UnixNano())
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Id:     messageID,
				ExecId: execID,
				Message: &agentv1.ExecServerMessage_ReadArgs{
					ReadArgs: &agentv1.ReadArgs{
						Path:       args.Path,
						ToolCallId: toolCall.CallID,
						Offset:     args.Offset,
						Limit:      args.Limit,
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingExec{
		MessageID:   messageID,
		ExecID:      execID,
		ArgsJSON:    append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:  toolCall.CallID,
		ExecKind:    "read",
		StreamState: "opened",
		OpenedAt:    time.Now().UTC(),
	}, nil
}

// openWrite 构造 Write 对应的执行桥请求。
func (bridge *Bridge) openWrite(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	var args struct {
		Path     string `json:"path"`
		Contents string `json:"contents"`
	}
	if err := json.Unmarshal(toolCall.ArgsJSON, &args); err != nil {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("decode Write args failed: %w", err)
	}
	messageID := bridge.nextID()
	execID := fmt.Sprintf("exec-write-%d", time.Now().UnixNano())
	encodingHint := "utf-8"
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Id:     messageID,
				ExecId: execID,
				Message: &agentv1.ExecServerMessage_WriteArgs{
					WriteArgs: &agentv1.WriteArgs{
						Path:                        strings.TrimSpace(args.Path),
						FileText:                    args.Contents,
						EncodingHint:                &encodingHint,
						ToolCallId:                  toolCall.CallID,
						ReturnFileContentAfterWrite: true,
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingExec{
		MessageID:   messageID,
		ExecID:      execID,
		ArgsJSON:    append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:  toolCall.CallID,
		ExecKind:    "write",
		StreamState: "opened",
	}, nil
}

// openDelete 构造 Delete 对应的执行桥请求。
func (bridge *Bridge) openDelete(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(toolCall.ArgsJSON, &args); err != nil {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("decode Delete args failed: %w", err)
	}
	messageID := bridge.nextID()
	execID := fmt.Sprintf("exec-delete-%d", time.Now().UnixNano())
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Id:     messageID,
				ExecId: execID,
				Message: &agentv1.ExecServerMessage_DeleteArgs{
					DeleteArgs: &agentv1.DeleteArgs{
						Path:       strings.TrimSpace(args.Path),
						ToolCallId: toolCall.CallID,
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingExec{
		MessageID:   messageID,
		ExecID:      execID,
		ArgsJSON:    append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:  toolCall.CallID,
		ExecKind:    "delete",
		StreamState: "opened",
	}, nil
}

// openGlob 构造 Glob 对应的执行桥请求；当前通过 grep files mode 交给本地宿主处理。
func (bridge *Bridge) openGlob(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	globArgs, err := DecodeGlobToolArgs(toolCall.ArgsJSON)
	if err != nil {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("decode Glob args failed: %w", err)
	}
	globPattern := strings.TrimSpace(globArgs.GetGlobPattern())
	if globPattern == "" {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("glob pattern is required")
	}
	messageID := bridge.nextID()
	execID := fmt.Sprintf("exec-glob-%d", time.Now().UnixNano())
	outputMode := "files_with_matches"
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Id:     messageID,
				ExecId: execID,
				Message: &agentv1.ExecServerMessage_GrepArgs{
					GrepArgs: &agentv1.GrepArgs{
						Glob:       stringPtr(globPattern),
						Path:       globArgs.TargetDirectory,
						OutputMode: &outputMode,
						ToolCallId: toolCall.CallID,
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingExec{
		MessageID:   messageID,
		ExecID:      execID,
		ArgsJSON:    append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:  toolCall.CallID,
		ExecKind:    "glob",
		StreamState: "opened",
		OpenedAt:    time.Now().UTC(),
	}, nil
}

func decodeShellArgs(raw []byte) (shellResultArgs, error) {
	args, err := decodeArgsMap(raw)
	if err != nil {
		return shellResultArgs{}, err
	}
	result := shellResultArgs{
		Command:          strings.TrimSpace(readStringArg(args, "command")),
		Description:      strings.TrimSpace(readStringArg(args, "description")),
		WorkingDirectory: strings.TrimSpace(readStringArg(args, "working_directory", "workingDirectory")),
	}
	if result.Command == "" {
		return result, fmt.Errorf("Shell command is required")
	}
	if blockUntilMS, found, err := runtimecore.ReadFloat64Arg(args, "block_until_ms", "blockUntilMS"); err != nil {
		return result, err
	} else if found {
		result.BlockUntilMS = blockUntilMS
		result.BlockUntilMSSet = true
	}
	notifyOnOutput, err := decodeShellOutputNotificationArgs(args)
	if err != nil {
		return result, err
	}
	result.NotifyOnOutput = notifyOnOutput
	return result, nil
}

func decodeShellOutputNotificationArgs(args map[string]any) (*shellOutputNotificationArgs, error) {
	raw, ok := args["notify_on_output"]
	if !ok || raw == nil {
		raw, ok = args["notifyOnOutput"]
	}
	if !ok || raw == nil {
		return nil, nil
	}
	items, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("notify_on_output must be an object")
	}
	pattern := strings.TrimSpace(readStringArg(items, "pattern"))
	reason := strings.TrimSpace(readStringArg(items, "reason"))
	if pattern == "" || reason == "" {
		return nil, nil
	}
	result := &shellOutputNotificationArgs{Pattern: pattern, Reason: reason}
	if debounceMS, found, err := runtimecore.ReadFloat64Arg(items, "debounce_ms", "debounceMs"); err != nil {
		return nil, err
	} else if found {
		result.DebounceMS = &debounceMS
	}
	if limit, found, err := runtimecore.ReadInt32Arg(items, "notification_limit", "notificationLimit"); err != nil {
		return nil, err
	} else if found {
		result.NotificationLimit = &limit
	}
	return result, nil
}

func buildShellOutputNotificationConfig(input *shellOutputNotificationArgs) *agentv1.ShellOutputNotificationConfig {
	if input == nil {
		return nil
	}
	pattern := strings.TrimSpace(input.Pattern)
	reason := strings.TrimSpace(input.Reason)
	if pattern == "" || reason == "" {
		return nil
	}
	var debounce *float64
	if input.DebounceMS != nil {
		value := *input.DebounceMS / 1000
		if value < 5 {
			value = 5
		}
		debounce = &value
	}
	return &agentv1.ShellOutputNotificationConfig{
		Pattern:           pattern,
		Reason:            reason,
		Debounce:          debounce,
		NotificationLimit: input.NotificationLimit,
	}
}

// openShell 构造 Shell 对应的流式执行桥请求。
func (bridge *Bridge) openShell(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	args, err := decodeShellArgs(toolCall.ArgsJSON)
	if err != nil {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("decode Shell args failed: %w", err)
	}
	timeout := shellTimeoutFromArgs(args)
	messageID := bridge.nextID()
	execID := fmt.Sprintf("exec-shell-%d", time.Now().UnixNano())
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Id:     messageID,
				ExecId: execID,
				Message: &agentv1.ExecServerMessage_ShellStreamArgs{
					ShellStreamArgs: &agentv1.ShellArgs{
						Command:                  args.Command,
						WorkingDirectory:         args.WorkingDirectory,
						Timeout:                  timeout,
						ToolCallId:               toolCall.CallID,
						SimpleCommands:           buildSimpleShellCommands(args.Command),
						ParsingResult:            buildShellParsingResultProto(args.Command),
						FileOutputThresholdBytes: uint64Ptr(40000),
						TimeoutBehavior:          agentv1.TimeoutBehavior_TIMEOUT_BEHAVIOR_BACKGROUND,
						HardTimeout:              int32Ptr(86400000),
						Description:              stringPtr(args.Description),
						OutputNotification:       buildShellOutputNotificationConfig(args.NotifyOnOutput),
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingExec{
		MessageID:   messageID,
		ExecID:      execID,
		ArgsJSON:    append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:  toolCall.CallID,
		ExecKind:    "shell",
		StreamState: "opened",
		OpenedAt:    time.Now().UTC(),
	}, nil
}

type writeShellStdinArgs struct {
	ShellID uint32
	Chars   string
}

func decodeWriteShellStdinArgs(raw []byte) (writeShellStdinArgs, error) {
	args, err := decodeArgsMap(raw)
	if err != nil {
		return writeShellStdinArgs{}, err
	}
	shellID, found, err := runtimecore.ReadUint32Arg(args, "shell_id", "shellId")
	if err != nil {
		return writeShellStdinArgs{}, err
	}
	if !found || shellID == 0 {
		return writeShellStdinArgs{}, fmt.Errorf("WriteShellStdin shell_id is required")
	}
	rawChars, charsFound := args["chars"]
	if !charsFound || rawChars == nil {
		return writeShellStdinArgs{}, fmt.Errorf("WriteShellStdin chars is required")
	}
	chars, ok := rawChars.(string)
	if !ok {
		return writeShellStdinArgs{}, fmt.Errorf("WriteShellStdin chars must be a string")
	}
	return writeShellStdinArgs{ShellID: shellID, Chars: chars}, nil
}

func (bridge *Bridge) openWriteShellStdin(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	args, err := decodeWriteShellStdinArgs(toolCall.ArgsJSON)
	if err != nil {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("decode WriteShellStdin args failed: %w", err)
	}
	messageID := bridge.nextID()
	execID := fmt.Sprintf("exec-write-shell-stdin-%d", time.Now().UnixNano())
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Id:     messageID,
				ExecId: execID,
				Message: &agentv1.ExecServerMessage_WriteShellStdinArgs{
					WriteShellStdinArgs: &agentv1.WriteShellStdinArgs{
						ShellId: args.ShellID,
						Chars:   args.Chars,
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingExec{
		MessageID:   messageID,
		ExecID:      execID,
		ArgsJSON:    append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:  toolCall.CallID,
		ExecKind:    "write_shell_stdin",
		StreamState: "opened",
		OpenedAt:    time.Now().UTC(),
	}, nil
}

type forceBackgroundShellArgs struct {
	ToolCallID string
}

func decodeForceBackgroundShellArgs(raw []byte) (forceBackgroundShellArgs, error) {
	args, err := decodeArgsMap(raw)
	if err != nil {
		return forceBackgroundShellArgs{}, err
	}
	toolCallID := strings.TrimSpace(readStringArg(args, "tool_call_id", "toolCallId"))
	if toolCallID == "" {
		return forceBackgroundShellArgs{}, fmt.Errorf("ForceBackgroundShell tool_call_id is required")
	}
	return forceBackgroundShellArgs{ToolCallID: toolCallID}, nil
}

func (bridge *Bridge) openForceBackgroundShell(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	args, err := decodeForceBackgroundShellArgs(toolCall.ArgsJSON)
	if err != nil {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("decode ForceBackgroundShell args failed: %w", err)
	}
	messageID := bridge.nextID()
	execID := fmt.Sprintf("exec-force-background-shell-%d", time.Now().UnixNano())
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Id:     messageID,
				ExecId: execID,
				Message: &agentv1.ExecServerMessage_ForceBackgroundShellArgs{
					ForceBackgroundShellArgs: &agentv1.ForceBackgroundShellArgs{
						ToolCallId: args.ToolCallID,
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingExec{
		MessageID:   messageID,
		ExecID:      execID,
		ArgsJSON:    append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:  toolCall.CallID,
		ExecKind:    "force_background_shell",
		StreamState: "opened",
		OpenedAt:    time.Now().UTC(),
	}, nil
}

// openTask 构造 Task 对应的执行桥请求。
func (bridge *Bridge) openTask(openContext OpenExecContext, toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	args, err := decodeArgsMap(toolCall.ArgsJSON)
	if err != nil {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("decode Task args failed: %w", err)
	}
	subagentType := strings.TrimSpace(readStringArg(args, "subagent_type", "subagentType"))
	if subagentType == "" {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("task subagent_type is required")
	}
	messageID := bridge.nextID()
	now := time.Now().UTC()
	execID := fmt.Sprintf("exec-subagent-%d", now.UnixNano())
	readonly := readBoolArg(args, "readonly", "readOnly")
	parentConversationID := strings.TrimSpace(openContext.ConversationID)
	taskRequestedModelID := strings.TrimSpace(readStringArg(args, "model", "model_id", "modelId"))
	modelID := taskRequestedModelID
	if override, _, ok := runtimecore.LookupSubagentModelOverride(openContext.SubagentModelOverrides, subagentType); ok {
		switch strings.TrimSpace(override.Selection) {
		case "disabled":
			return nil, runtimecore.PendingExec{}, fmt.Errorf("subagent type %q is disabled by model override", subagentType)
		case "model":
			modelID = strings.TrimSpace(override.ModelID)
		case "inherit":
			modelID = strings.TrimSpace(openContext.ModelID)
		}
	}
	if modelID == "" {
		modelID = strings.TrimSpace(openContext.ModelID)
	}
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Id:     messageID,
				ExecId: execID,
				Message: &agentv1.ExecServerMessage_SubagentArgs{
					SubagentArgs: &agentv1.SubagentArgs{
						ToolCallId:           toolCall.CallID,
						SubagentType:         subagentType,
						ModelId:              modelID,
						Prompt:               strings.TrimSpace(readStringArg(args, "prompt")),
						Readonly:             readonly,
						ResumeAgentId:        stringPtr(strings.TrimSpace(readStringArg(args, "resume"))),
						ParentConversationId: stringPtrIfNonEmpty(parentConversationID),
						Mode:                 taskModeFromReadonly(readonly),
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingExec{
		MessageID:   messageID,
		ExecID:      execID,
		ArgsJSON:    append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:  toolCall.CallID,
		ExecKind:    "subagent",
		StreamState: "opened",
		OpenedAt:    now,
	}, nil
}

// openGrep 构造 Grep 对应的执行桥请求。
func (bridge *Bridge) openGrep(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	input, err := DecodeGrepToolArgs(toolCall.ArgsJSON, toolCall.CallID)
	if err != nil {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("decode Grep args failed: %w", err)
	}
	messageID := bridge.nextID()
	execID := fmt.Sprintf("exec-grep-%d", time.Now().UnixNano())
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Id:     messageID,
				ExecId: execID,
				Message: &agentv1.ExecServerMessage_GrepArgs{
					GrepArgs: input,
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingExec{
		MessageID:   messageID,
		ExecID:      execID,
		ArgsJSON:    append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:  toolCall.CallID,
		ExecKind:    "grep",
		StreamState: "opened",
		OpenedAt:    time.Now().UTC(),
	}, nil
}

// openReadLints 构造 ReadLints 对应的执行桥请求。
func (bridge *Bridge) openReadLints(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	args, err := decodeArgsMap(toolCall.ArgsJSON)
	if err != nil {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("decode ReadLints args failed: %w", err)
	}
	paths := readStringSliceArg(args, "paths")
	path := ""
	if len(paths) > 0 {
		path = strings.TrimSpace(paths[0])
	}
	messageID := bridge.nextID()
	execID := fmt.Sprintf("exec-diagnostics-%d", time.Now().UnixNano())
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Id:     messageID,
				ExecId: execID,
				Message: &agentv1.ExecServerMessage_DiagnosticsArgs{
					DiagnosticsArgs: &agentv1.DiagnosticsArgs{
						Path:       path,
						ToolCallId: toolCall.CallID,
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingExec{
		MessageID:   messageID,
		ExecID:      execID,
		ArgsJSON:    append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:  toolCall.CallID,
		ExecKind:    "diagnostics",
		StreamState: "opened",
	}, nil
}

// openLs 构造 Ls 对应的执行桥请求。
func (bridge *Bridge) openLs(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	var input struct {
		Path   string   `json:"path"`
		Ignore []string `json:"ignore,omitempty"`
	}
	if err := json.Unmarshal(toolCall.ArgsJSON, &input); err != nil {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("decode Ls args failed: %w", err)
	}
	messageID := bridge.nextID()
	execID := fmt.Sprintf("exec-ls-%d", time.Now().UnixNano())
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Id:     messageID,
				ExecId: execID,
				Message: &agentv1.ExecServerMessage_LsArgs{
					LsArgs: &agentv1.LsArgs{
						Path:       strings.TrimSpace(input.Path),
						Ignore:     append([]string(nil), input.Ignore...),
						ToolCallId: toolCall.CallID,
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingExec{
		MessageID:   messageID,
		ExecID:      execID,
		ArgsJSON:    append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:  toolCall.CallID,
		ExecKind:    "ls",
		StreamState: "opened",
		OpenedAt:    time.Now().UTC(),
	}, nil
}

// openMcp 构造 CallMcpTool 对应的执行桥请求。
func (bridge *Bridge) openMcp(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	input, err := runtimecore.DecodeMCPToolPayload(toolCall.ArgsJSON)
	if err != nil {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("decode CallMcpTool args failed: %w", err)
	}
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
	messageID := bridge.nextID()
	execID := fmt.Sprintf("exec-mcp-%d", time.Now().UnixNano())
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Id:     messageID,
				ExecId: execID,
				Message: &agentv1.ExecServerMessage_McpArgs{
					McpArgs: &agentv1.McpArgs{
						Name:               canonicalMCPToolLookupName(serverIdentifier, toolName),
						Args:               buildStructValueMap(input.Arguments),
						ToolCallId:         toolCall.CallID,
						ProviderIdentifier: serverIdentifier,
						ToolName:           toolName,
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingExec{
		MessageID:   messageID,
		ExecID:      execID,
		ArgsJSON:    append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:  toolCall.CallID,
		ExecKind:    "mcp",
		StreamState: "opened",
	}, nil
}

// openListMcpResources 构造 ListMcpResources 对应的执行桥请求。
func (bridge *Bridge) openListMcpResources(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	var input struct {
		Server string `json:"server,omitempty"`
	}
	if err := json.Unmarshal(toolCall.ArgsJSON, &input); err != nil {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("decode ListMcpResources args failed: %w", err)
	}
	messageID := bridge.nextID()
	execID := fmt.Sprintf("exec-list-mcp-resources-%d", time.Now().UnixNano())
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Id:     messageID,
				ExecId: execID,
				Message: &agentv1.ExecServerMessage_ListMcpResourcesExecArgs{
					ListMcpResourcesExecArgs: &agentv1.ListMcpResourcesExecArgs{
						Server: stringPtr(strings.TrimSpace(input.Server)),
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingExec{
		MessageID:   messageID,
		ExecID:      execID,
		ArgsJSON:    append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:  toolCall.CallID,
		ExecKind:    "list_mcp_resources",
		StreamState: "opened",
	}, nil
}

// openReadMcpResource 构造 FetchMcpResource 对应的执行桥请求。
func (bridge *Bridge) openReadMcpResource(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingExec, error) {
	var input struct {
		Server       string `json:"server"`
		URI          string `json:"uri"`
		DownloadPath string `json:"downloadPath,omitempty"`
	}
	if err := json.Unmarshal(toolCall.ArgsJSON, &input); err != nil {
		return nil, runtimecore.PendingExec{}, fmt.Errorf("decode FetchMcpResource args failed: %w", err)
	}
	messageID := bridge.nextID()
	execID := fmt.Sprintf("exec-read-mcp-resource-%d", time.Now().UnixNano())
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Id:     messageID,
				ExecId: execID,
				Message: &agentv1.ExecServerMessage_ReadMcpResourceExecArgs{
					ReadMcpResourceExecArgs: &agentv1.ReadMcpResourceExecArgs{
						Server:       strings.TrimSpace(input.Server),
						Uri:          strings.TrimSpace(input.URI),
						DownloadPath: stringPtr(strings.TrimSpace(input.DownloadPath)),
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingExec{
		MessageID:   messageID,
		ExecID:      execID,
		ArgsJSON:    append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:  toolCall.CallID,
		ExecKind:    "read_mcp_resource",
		StreamState: "opened",
	}, nil
}

func normalizeReadResultForModel(result *agentv1.ReadResult) *agentv1.ReadResult {
	if result == nil {
		return nil
	}
	cloned, ok := proto.Clone(result).(*agentv1.ReadResult)
	if !ok {
		return result
	}
	success := cloned.GetSuccess()
	if success == nil {
		return cloned
	}
	if output, ok := success.GetOutput().(*agentv1.ReadSuccess_Content); ok {
		normalized := normalizeReadContentLineEndingsToLF(output.Content)
		if normalized != output.Content {
			output.Content = normalized
			success.TotalLines = countLFReadLines(normalized)
		}
	}
	return cloned
}

func normalizeReadContentLineEndingsToLF(content string) string {
	if !strings.ContainsAny(content, "\r\n") {
		return content
	}
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	return strings.ReplaceAll(normalized, "\r", "\n")
}

func countLFReadLines(content string) int32 {
	if content == "" {
		return 0
	}
	count := int32(strings.Count(content, "\n"))
	if !strings.HasSuffix(content, "\n") {
		count++
	}
	return count
}

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

func truncateMcpToolResultForReplay(result *agentv1.McpToolResult) *agentv1.McpToolResult {
	if result == nil {
		return nil
	}
	cloned, ok := proto.Clone(result).(*agentv1.McpToolResult)
	if !ok || cloned == nil || cloned.GetSuccess() == nil {
		return result
	}
	success := cloned.GetSuccess()
	notices := make([]string, 0, 3)
	if structured := success.GetStructuredContent(); structured != nil {
		if encoded, err := protojson.Marshal(structured); err == nil && len(encoded) > mcpReplayStructuredLimit {
			replacement, _ := structpb.NewStruct(map[string]any{
				"_truncated":          true,
				"original_json_bytes": float64(len(encoded)),
				"limit_bytes":         float64(mcpReplayStructuredLimit),
			})
			success.StructuredContent = replacement
			notices = append(notices, replayTruncationNotice("MCP structured_content", mcpReplayStructuredLimit, 0, len(encoded)))
		}
	}
	content := success.GetContent()
	if len(content) > mcpReplayContentItemLimit {
		notices = append(notices, fmt.Sprintf("[truncated: MCP content items exceeded %d items; showing %d of %d items]", mcpReplayContentItemLimit, mcpReplayContentItemLimit, len(content)))
		content = content[:mcpReplayContentItemLimit]
	}
	totalText := 0
	truncatedContent := make([]*agentv1.McpToolResultContentItem, 0, len(content)+len(notices))
	for _, item := range content {
		if item == nil {
			continue
		}
		next := proto.Clone(item).(*agentv1.McpToolResultContentItem)
		if text := next.GetText(); text != nil {
			original := text.GetText()
			nextText := truncateReplayText("MCP content item", original, mcpReplayTextItemLimit)
			remaining := mcpReplayTextTotalLimit - totalText
			if remaining <= 0 {
				notices = append(notices, replayTruncationNotice("MCP text", mcpReplayTextTotalLimit, totalText, totalText+len(original)))
				continue
			}
			nextText = truncateReplayText("MCP text", nextText, remaining)
			text.Text = nextText
			totalText += len(nextText)
			truncatedContent = append(truncatedContent, next)
			continue
		}
		if image := next.GetImage(); image != nil && len(image.GetData()) > mcpReplayBinaryLimit {
			original := len(image.GetData())
			image.Data, _ = truncateByteSlice(image.GetData(), mcpReplayBinaryLimit)
			notices = append(notices, replayTruncationNotice("MCP image data", mcpReplayBinaryLimit, len(image.GetData()), original))
		}
		truncatedContent = append(truncatedContent, next)
	}
	for _, notice := range notices {
		truncatedContent = append(truncatedContent, &agentv1.McpToolResultContentItem{
			Content: &agentv1.McpToolResultContentItem_Text{
				Text: &agentv1.McpTextContent{Text: notice},
			},
		})
	}
	success.Content = truncatedContent
	return cloned
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

func truncateListMcpResourcesResultForReplay(result *agentv1.ListMcpResourcesExecResult) *agentv1.ListMcpResourcesExecResult {
	if result == nil {
		return nil
	}
	cloned, ok := proto.Clone(result).(*agentv1.ListMcpResourcesExecResult)
	if !ok || cloned == nil || cloned.GetSuccess() == nil {
		return result
	}
	resources := cloned.GetSuccess().GetResources()
	if len(resources) > mcpResourcesReplayCount {
		resources = resources[:mcpResourcesReplayCount]
	}
	trimmed := make([]*agentv1.ListMcpResourcesExecResult_McpResource, 0, len(resources))
	for _, resource := range resources {
		if resource == nil {
			continue
		}
		next := proto.Clone(resource).(*agentv1.ListMcpResourcesExecResult_McpResource)
		if next.Description != nil {
			description := truncateReplayText("MCP resource description", next.GetDescription(), mcpResourceDescriptionSize)
			next.Description = stringPtr(description)
		}
		trimmed = append(trimmed, next)
	}
	cloned.GetSuccess().Resources = trimmed
	for len(cloned.GetSuccess().Resources) > 0 {
		encoded, err := protojson.Marshal(cloned)
		if err != nil || len(encoded) <= mcpResourcesReplayLimit {
			break
		}
		cloned.GetSuccess().Resources = cloned.GetSuccess().Resources[:len(cloned.GetSuccess().Resources)-1]
	}
	if len(cloned.GetSuccess().Resources) < len(result.GetSuccess().GetResources()) {
		notice := replayTruncationNotice("ListMcpResources", mcpResourcesReplayLimit, len(cloned.GetSuccess().Resources), len(result.GetSuccess().GetResources()))
		cloned.GetSuccess().Resources = append(cloned.GetSuccess().Resources, &agentv1.ListMcpResourcesExecResult_McpResource{
			Uri:         "truncated:list-mcp-resources",
			Name:        stringPtr("truncated"),
			Description: stringPtr(notice),
		})
	}
	return cloned
}

func truncateReadMcpResourceResultForReplay(result *agentv1.ReadMcpResourceExecResult) *agentv1.ReadMcpResourceExecResult {
	if result == nil {
		return nil
	}
	cloned, ok := proto.Clone(result).(*agentv1.ReadMcpResourceExecResult)
	if !ok || cloned == nil || cloned.GetSuccess() == nil {
		return result
	}
	success := cloned.GetSuccess()
	if text := success.GetText(); text != "" {
		success.Content = &agentv1.ReadMcpResourceSuccess_Text{
			Text: truncateReplayText("FetchMcpResource", text, mcpReplayTextTotalLimit),
		}
		return cloned
	}
	if blob := success.GetBlob(); len(blob) > mcpReplayBinaryLimit {
		success.Content = &agentv1.ReadMcpResourceSuccess_Text{
			Text: replayTruncationNotice("FetchMcpResource blob", mcpReplayBinaryLimit, 0, len(blob)),
		}
	}
	return cloned
}

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

const maxGlobReplayFiles = 200

const (
	replayKiB = 1024

	readReplayContentLimit     = 64 * replayKiB
	readReplayLineLimit        = 0
	readReplayBinaryLimit      = 32 * replayKiB
	shellReplayStreamLimit     = 16 * replayKiB
	grepReplayContentLimit     = 32 * replayKiB
	grepReplayMatchLimit       = 2 * replayKiB
	grepReplayMatchesPerFile   = 100
	grepReplayTotalMatches     = 300
	grepReplayListLimit        = 300
	mcpReplayTextTotalLimit    = 32 * replayKiB
	mcpReplayTextItemLimit     = 32 * replayKiB
	mcpReplayContentItemLimit  = 20
	mcpReplayStructuredLimit   = 32 * replayKiB
	mcpReplayBinaryLimit       = 32 * replayKiB
	mcpResourcesReplayLimit    = 32 * replayKiB
	mcpResourcesReplayCount    = 200
	mcpResourceDescriptionSize = replayKiB
)

func truncateReplayText(toolName string, text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	original := len(text)
	notice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; showing %d of %d bytes]", toolName, limit, limit, original)
	for {
		keep := limit - len(notice)
		if keep <= 0 {
			return truncateUTF8Bytes(text, limit)
		}
		kept := truncateUTF8Bytes(text, keep)
		nextNotice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; showing %d of %d bytes]", toolName, limit, len(kept), original)
		output := strings.TrimRight(kept, "\n") + nextNotice
		if len(output) <= limit || nextNotice == notice {
			return output
		}
		notice = nextNotice
	}
}

func truncateReplayTextMiddle(toolName string, text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	original := len(text)
	notice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; omitted middle; showing %d of %d bytes]\n\n", toolName, limit, limit, original)
	for {
		keep := limit - len(notice)
		if keep <= 0 {
			return truncateUTF8Bytes(text, limit)
		}
		headLimit := keep / 2
		tailLimit := keep - headLimit
		head := truncateUTF8Bytes(text, headLimit)
		tail := truncateUTF8Suffix(text, tailLimit)
		kept := len(head) + len(tail)
		nextNotice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; omitted middle; showing %d of %d bytes]\n\n", toolName, limit, kept, original)
		output := head + nextNotice + tail
		if len(output) <= limit || nextNotice == notice {
			return output
		}
		notice = nextNotice
	}
}

func truncateReplayLine(toolName string, text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	original := len(text)
	notice := fmt.Sprintf(" [truncated: %s line exceeded %d bytes; showing %d of %d bytes]", toolName, limit, limit, original)
	for {
		keep := limit - len(notice)
		if keep <= 0 {
			return truncateUTF8Bytes(text, limit)
		}
		kept := truncateUTF8Bytes(text, keep)
		nextNotice := fmt.Sprintf(" [truncated: %s line exceeded %d bytes; showing %d of %d bytes]", toolName, limit, len(kept), original)
		output := kept + nextNotice
		if len(output) <= limit || nextNotice == notice {
			return output
		}
		notice = nextNotice
	}
}

func truncateReplayLines(toolName string, text string, lineLimit int) string {
	if lineLimit <= 0 || text == "" {
		return text
	}
	parts := strings.SplitAfter(text, "\n")
	for index, part := range parts {
		newline := ""
		body := part
		if strings.HasSuffix(part, "\n") {
			body = strings.TrimSuffix(part, "\n")
			newline = "\n"
		}
		parts[index] = truncateReplayLine(toolName, body, lineLimit) + newline
	}
	return strings.Join(parts, "")
}

func truncateUTF8Bytes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	if limit > len(text) {
		limit = len(text)
	}
	truncated := text[:limit]
	for !utf8.ValidString(truncated) && len(truncated) > 0 {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

func truncateUTF8Suffix(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	start := len(text) - limit
	if start < 0 {
		start = 0
	}
	suffix := text[start:]
	for !utf8.ValidString(suffix) && start < len(text) {
		start++
		suffix = text[start:]
	}
	return suffix
}

func truncateByteSlice(value []byte, limit int) ([]byte, bool) {
	if limit <= 0 || len(value) <= limit {
		return value, false
	}
	return append([]byte(nil), value[:limit]...), true
}

func replayTruncationNotice(toolName string, limit int, kept int, original int) string {
	return fmt.Sprintf("[truncated: %s result exceeded %d bytes; showing %d of %d bytes]", toolName, limit, kept, original)
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

// buildShellCompletedToolCall 构造 Shell 对应的完成态 ToolCall。
func buildShellCompletedToolCall(toolCallID string, argsJSON []byte, stdout string, stderr string, exit *agentv1.ShellStreamExit) *agentv1.ToolCall {
	args := decodeShellArgsForResult(argsJSON)
	shellArgs := &agentv1.ShellArgs{
		Command:          args.Command,
		WorkingDirectory: args.WorkingDirectory,
		Timeout:          shellTimeoutFromArgs(args),
		ToolCallId:       toolCallID,
		Description:      stringPtr(strings.TrimSpace(args.Description)),
	}
	successPayload := buildShellSuccessPayload(args, stdout, stderr, exit)
	isBackground := false
	result := &agentv1.ShellResult{
		IsBackground: &isBackground,
		Result: &agentv1.ShellResult_Success{
			Success: successPayload,
		},
	}
	if exit != nil && exit.GetCode() != 0 {
		failure := &agentv1.ShellFailure{
			Command:           args.Command,
			WorkingDirectory:  args.WorkingDirectory,
			ExitCode:          int32(exit.GetCode()),
			Stdout:            stdout,
			Stderr:            stderr,
			InterleavedOutput: buildShellInterleavedOutput(stdout, stderr),
			Aborted:           exit.GetAborted(),
		}
		if exit.LocalExecutionTimeMs != nil {
			failure.LocalExecutionTimeMs = int32Ptr(exit.GetLocalExecutionTimeMs())
		}
		if exit.AbortReason != nil {
			failure.AbortReason = shellAbortReasonPtr(exit.GetAbortReason())
		}
		if exit.GetOutputLocation() != nil {
			failure.OutputLocation = exit.GetOutputLocation()
		}
		result.Result = &agentv1.ShellResult_Failure{
			Failure: failure,
		}
	}
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ShellToolCall{
			ShellToolCall: &agentv1.ShellToolCall{
				Args:        shellArgs,
				Result:      result,
				Description: stringPtr(strings.TrimSpace(args.Description)),
			},
		},
	}
}

// buildShellBackgroundedToolCall 构造 Shell 被转入后台时的完成态 ToolCall。
func buildShellBackgroundedToolCall(toolCallID string, argsJSON []byte, backgrounded *agentv1.ShellStreamBackgrounded) *agentv1.ToolCall {
	args := decodeShellArgsForResult(argsJSON)
	shellArgs := &agentv1.ShellArgs{
		Command:          args.Command,
		WorkingDirectory: args.WorkingDirectory,
		Timeout:          shellTimeoutFromArgs(args),
		ToolCallId:       toolCallID,
		Description:      stringPtr(strings.TrimSpace(args.Description)),
	}
	successPayload := &agentv1.ShellSuccess{
		Command:           strings.TrimSpace(args.Command),
		WorkingDirectory:  strings.TrimSpace(args.WorkingDirectory),
		ExitCode:          0,
		ShellId:           uint32Ptr(backgrounded.GetShellId()),
		InterleavedOutput: stringPtr(""),
	}
	if workingDirectory := strings.TrimSpace(backgrounded.GetWorkingDirectory()); workingDirectory != "" {
		successPayload.WorkingDirectory = workingDirectory
	}
	if backgrounded.GetPid() != 0 {
		successPayload.Pid = uint32Ptr(backgrounded.GetPid())
	}
	if backgrounded.MsToWait != nil {
		successPayload.MsToWait = int32Ptr(backgrounded.GetMsToWait())
	}
	isBackground := true
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ShellToolCall{
			ShellToolCall: &agentv1.ShellToolCall{
				Args: shellArgs,
				Result: &agentv1.ShellResult{
					IsBackground: &isBackground,
					Pid:          uint32Ptr(backgrounded.GetPid()),
					Result: &agentv1.ShellResult_Success{
						Success: successPayload,
					},
				},
				Description: stringPtr(strings.TrimSpace(args.Description)),
			},
		},
	}
}

// buildShellSuccessPayload 构造 shell 终态结果。
func buildShellSuccessPayload(args shellResultArgs, stdout string, stderr string, exit *agentv1.ShellStreamExit) *agentv1.ShellSuccess {
	stdout, stderr = truncateShellStreamsForReplay(stdout, stderr)
	payload := &agentv1.ShellSuccess{
		Command:           strings.TrimSpace(args.Command),
		WorkingDirectory:  strings.TrimSpace(args.WorkingDirectory),
		Stdout:            stdout,
		Stderr:            stderr,
		InterleavedOutput: buildShellInterleavedOutput(stdout, stderr),
	}
	if exit != nil {
		payload.ExitCode = int32(exit.GetCode())
		if cwd := strings.TrimSpace(exit.GetCwd()); cwd != "" {
			payload.WorkingDirectory = cwd
		}
		if exit.GetOutputLocation() != nil {
			payload.OutputLocation = exit.GetOutputLocation()
		}
		if exit.LocalExecutionTimeMs != nil {
			duration := int32(exit.GetLocalExecutionTimeMs())
			payload.ExecutionTime = duration
			payload.LocalExecutionTimeMs = &duration
		}
	}
	return payload
}

func buildShellInterleavedOutput(stdout string, stderr string) *string {
	combinedLimit := shellReplayStreamLimit * 2
	switch {
	case stdout == "" && stderr == "":
		return nil
	case stdout == "":
		return stringPtr(truncateReplayTextMiddle("Shell interleaved output", stderr, combinedLimit))
	case stderr == "":
		return stringPtr(truncateReplayTextMiddle("Shell interleaved output", stdout, combinedLimit))
	default:
		combined := stdout
		if !strings.HasSuffix(combined, "\n") {
			combined += "\n"
		}
		combined += stderr
		combined = truncateReplayTextMiddle("Shell interleaved output", combined, combinedLimit)
		return &combined
	}
}

func truncateShellStreamsForReplay(stdout string, stderr string) (string, string) {
	return truncateReplayTextMiddle("Shell stdout", stdout, shellReplayStreamLimit),
		truncateReplayTextMiddle("Shell stderr", stderr, shellReplayStreamLimit)
}

// buildShellRejectedToolCall 构造 Shell 被拒绝时的完成态 ToolCall。
func buildShellRejectedToolCall(toolCallID string, argsJSON []byte, rejected *agentv1.ShellRejected) *agentv1.ToolCall {
	args := decodeShellArgsForResult(argsJSON)
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ShellToolCall{
			ShellToolCall: &agentv1.ShellToolCall{
				Args: &agentv1.ShellArgs{
					Command:          args.Command,
					WorkingDirectory: args.WorkingDirectory,
					Timeout:          shellTimeoutFromArgs(args),
					ToolCallId:       toolCallID,
					Description:      stringPtr(strings.TrimSpace(args.Description)),
				},
				Result: &agentv1.ShellResult{
					Result: &agentv1.ShellResult_Rejected{
						Rejected: rejected,
					},
				},
				Description: stringPtr(strings.TrimSpace(args.Description)),
			},
		},
	}
}

// buildShellPermissionDeniedToolCall 构造 Shell 权限拒绝时的完成态 ToolCall。
func buildShellPermissionDeniedToolCall(toolCallID string, argsJSON []byte, denied *agentv1.ShellPermissionDenied) *agentv1.ToolCall {
	args := decodeShellArgsForResult(argsJSON)
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ShellToolCall{
			ShellToolCall: &agentv1.ShellToolCall{
				Args: &agentv1.ShellArgs{
					Command:          args.Command,
					WorkingDirectory: args.WorkingDirectory,
					Timeout:          shellTimeoutFromArgs(args),
					ToolCallId:       toolCallID,
					Description:      stringPtr(strings.TrimSpace(args.Description)),
				},
				Result: &agentv1.ShellResult{
					Result: &agentv1.ShellResult_PermissionDenied{
						PermissionDenied: denied,
					},
				},
				Description: stringPtr(strings.TrimSpace(args.Description)),
			},
		},
	}
}

// buildSimpleShellCommands 生成最小 simple_commands 列表。
func buildSimpleShellCommands(command string) []string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return nil
	}
	return []string{trimmed}
}

// buildShellParsingResultProto 生成最小 shell parsing_result。
func buildShellParsingResultProto(command string) *agentv1.ShellCommandParsingResult {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return nil
	}
	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		return nil
	}
	args := make([]*agentv1.ShellCommandParsingResult_ExecutableCommandArg, 0, len(parts)-1)
	for _, part := range parts[1:] {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		args = append(args, &agentv1.ShellCommandParsingResult_ExecutableCommandArg{
			Type:  "word",
			Value: value,
		})
	}
	return &agentv1.ShellCommandParsingResult{
		ExecutableCommands: []*agentv1.ShellCommandParsingResult_ExecutableCommand{
			{
				Name:     strings.TrimSpace(parts[0]),
				Args:     args,
				FullText: trimmed,
			},
		},
	}
}

// summarizeShellTerminalPayload 返回 shell 对模型可消费的终态结果文本。
func summarizeShellTerminalPayload(stdout string, stderr string, exit *agentv1.ShellStreamExit, closedWithoutExit bool) string {
	trimmedStdout := strings.TrimSpace(stdout)
	trimmedStderr := strings.TrimSpace(stderr)
	sections := make([]string, 0, 3)
	if trimmedStdout != "" {
		sections = append(sections, trimmedStdout)
	}
	if trimmedStderr != "" {
		if trimmedStdout != "" {
			sections = append(sections, "<stderr>\n"+trimmedStderr+"\n</stderr>")
		} else {
			sections = append(sections, trimmedStderr)
		}
	}
	if len(sections) > 0 {
		return strings.Join(sections, "\n\n")
	}
	if exit != nil {
		return fmt.Sprintf("shell exited with code=%d cwd=%s", exit.GetCode(), strings.TrimSpace(exit.GetCwd()))
	}
	if closedWithoutExit {
		return "shell stream closed without captured output"
	}
	return "shell completed without captured output"
}

type shellResultArgs struct {
	Command          string                       `json:"command"`
	Description      string                       `json:"description,omitempty"`
	WorkingDirectory string                       `json:"working_directory,omitempty"`
	BlockUntilMS     float64                      `json:"block_until_ms,omitempty"`
	BlockUntilMSSet  bool                         `json:"-"`
	NotifyOnOutput   *shellOutputNotificationArgs `json:"notify_on_output,omitempty"`
}

type shellOutputNotificationArgs struct {
	Pattern           string
	Reason            string
	DebounceMS        *float64
	NotificationLimit *int32
}

// decodeShellArgsForResult 解码 shell 参数，供完成态 ToolCall 复用。
func decodeShellArgsForResult(argsJSON []byte) shellResultArgs {
	args, err := decodeShellArgs(argsJSON)
	if err != nil {
		argsMap, _ := decodeArgsMap(argsJSON)
		args.Command = strings.TrimSpace(readStringArg(argsMap, "command"))
		args.Description = strings.TrimSpace(readStringArg(argsMap, "description"))
		args.WorkingDirectory = strings.TrimSpace(readStringArg(argsMap, "working_directory", "workingDirectory"))
		if blockUntilMS, found, err := runtimecore.ReadFloat64Arg(argsMap, "block_until_ms", "blockUntilMS"); err == nil && found {
			args.BlockUntilMS = blockUntilMS
			args.BlockUntilMSSet = true
		}
	}
	return args
}

// shellTimeoutFromArgs 把工具 JSON 中的 block_until_ms 映射回 proto timeout。
func shellTimeoutFromArgs(args shellResultArgs) int32 {
	if !args.BlockUntilMSSet {
		return 30000
	}
	if args.BlockUntilMS <= 0 {
		return 0
	}
	return int32(args.BlockUntilMS)
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

func truncateGlobResultForReplay(result *agentv1.GrepResult) *agentv1.GrepResult {
	if result == nil {
		return nil
	}
	cloned, ok := proto.Clone(result).(*agentv1.GrepResult)
	if !ok || cloned == nil || cloned.GetSuccess() == nil {
		return result
	}
	filesResult := firstGrepFilesResult(cloned.GetSuccess())
	if filesResult == nil {
		return cloned
	}
	files := append([]string(nil), filesResult.GetFiles()...)
	totalFiles := int(filesResult.GetTotalFiles())
	if totalFiles <= 0 {
		totalFiles = len(files)
	}
	if len(files) <= maxGlobReplayFiles {
		if filesResult.GetTotalFiles() <= 0 {
			filesResult.TotalFiles = int32(totalFiles)
		}
		return cloned
	}
	filesResult.Files = append([]string(nil), files[:maxGlobReplayFiles]...)
	filesResult.TotalFiles = int32(totalFiles)
	filesResult.ClientTruncated = true
	return cloned
}

func truncateGrepResultForReplay(result *agentv1.GrepResult) *agentv1.GrepResult {
	if result == nil {
		return nil
	}
	cloned, ok := proto.Clone(result).(*agentv1.GrepResult)
	if !ok || cloned == nil || cloned.GetSuccess() == nil {
		return result
	}
	budget := &grepReplayBudget{
		remainingContentBytes: grepReplayContentLimit,
		remainingMatches:      grepReplayTotalMatches,
	}
	success := cloned.GetSuccess()
	for _, union := range success.GetWorkspaceResults() {
		truncateGrepUnionResultForReplay(union, budget)
	}
	truncateGrepUnionResultForReplay(success.GetActiveEditorResult(), budget)
	return cloned
}

type grepReplayBudget struct {
	remainingContentBytes int
	remainingMatches      int
}

func truncateGrepUnionResultForReplay(union *agentv1.GrepUnionResult, budget *grepReplayBudget) {
	if union == nil || budget == nil {
		return
	}
	if content := union.GetContent(); content != nil {
		truncateGrepContentResultForReplay(content, budget)
		return
	}
	if files := union.GetFiles(); files != nil {
		total := int(files.GetTotalFiles())
		if total <= 0 {
			total = len(files.GetFiles())
		}
		if len(files.Files) > grepReplayListLimit {
			files.Files = append([]string(nil), files.Files[:grepReplayListLimit]...)
			files.ClientTruncated = true
		}
		if files.GetTotalFiles() <= 0 {
			files.TotalFiles = int32(total)
		}
		return
	}
	if counts := union.GetCount(); counts != nil {
		totalFiles := int(counts.GetTotalFiles())
		if totalFiles <= 0 {
			totalFiles = len(counts.GetCounts())
		}
		if len(counts.Counts) > grepReplayListLimit {
			counts.Counts = append([]*agentv1.GrepFileCount(nil), counts.Counts[:grepReplayListLimit]...)
			counts.ClientTruncated = true
		}
		if counts.GetTotalFiles() <= 0 {
			counts.TotalFiles = int32(totalFiles)
		}
	}
}

func truncateGrepContentResultForReplay(content *agentv1.GrepContentResult, budget *grepReplayBudget) {
	if content == nil || budget == nil {
		return
	}
	originalBytes := grepContentBytes(content.GetMatches())
	truncated := false
	newFiles := make([]*agentv1.GrepFileMatch, 0, len(content.GetMatches()))
	for _, fileMatch := range content.GetMatches() {
		if fileMatch == nil {
			continue
		}
		if budget.remainingMatches <= 0 || budget.remainingContentBytes <= 0 {
			truncated = true
			break
		}
		nextFile := &agentv1.GrepFileMatch{File: fileMatch.GetFile()}
		perFile := 0
		for _, match := range fileMatch.GetMatches() {
			if match == nil {
				continue
			}
			if perFile >= grepReplayMatchesPerFile || budget.remainingMatches <= 0 || budget.remainingContentBytes <= 0 {
				truncated = true
				break
			}
			nextMatch := proto.Clone(match).(*agentv1.GrepContentMatch)
			originalContent := nextMatch.GetContent()
			nextMatch.Content = truncateReplayText("Grep match", originalContent, grepReplayMatchLimit)
			if nextMatch.Content != originalContent {
				nextMatch.ContentTruncated = true
				truncated = true
			}
			if len(nextMatch.Content) > budget.remainingContentBytes {
				nextMatch.Content = truncateReplayText("Grep", nextMatch.Content, budget.remainingContentBytes)
				nextMatch.ContentTruncated = true
				truncated = true
			}
			if strings.TrimSpace(nextMatch.Content) == "" {
				truncated = true
				break
			}
			budget.remainingContentBytes -= len(nextMatch.Content)
			budget.remainingMatches--
			perFile++
			nextFile.Matches = append(nextFile.Matches, nextMatch)
		}
		if len(nextFile.Matches) > 0 {
			newFiles = append(newFiles, nextFile)
		}
		if len(fileMatch.GetMatches()) > perFile {
			truncated = true
		}
	}
	if len(newFiles) < len(content.GetMatches()) {
		truncated = true
	}
	if truncated {
		content.ClientTruncated = true
		newFiles = addGrepContentTruncationNotice(newFiles, originalBytes)
	}
	content.Matches = newFiles
}

func addGrepContentTruncationNotice(files []*agentv1.GrepFileMatch, originalBytes int) []*agentv1.GrepFileMatch {
	used := grepContentBytes(files)
	notice := replayTruncationNotice("Grep", grepReplayContentLimit, used, originalBytes)
	match := &agentv1.GrepContentMatch{
		LineNumber:       0,
		Content:          notice,
		ContentTruncated: true,
		IsContextLine:    true,
	}
	if len(files) == 0 {
		return []*agentv1.GrepFileMatch{{File: "[truncated]", Matches: []*agentv1.GrepContentMatch{match}}}
	}
	files[len(files)-1].Matches = append(files[len(files)-1].Matches, match)
	return files
}

func grepContentBytes(files []*agentv1.GrepFileMatch) int {
	used := 0
	for _, file := range files {
		for _, match := range file.GetMatches() {
			used += len(match.GetContent())
		}
	}
	return used
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

func isBackgroundSubagentSuccess(success *agentv1.SubagentSuccess) bool {
	if success == nil {
		return false
	}
	return success.GetBackgroundReason() != agentv1.SubagentBackgroundReason_SUBAGENT_BACKGROUND_REASON_UNSPECIFIED ||
		strings.TrimSpace(success.GetTranscriptPath()) != ""
}

// DecodeGlobToolArgs 解析并归一化 Glob 参数，兼容历史与模型常见别名。
func DecodeGlobToolArgs(raw []byte) (*agentv1.GlobToolArgs, error) {
	args, err := decodeArgsMap(raw)
	if err != nil {
		return nil, err
	}
	return buildGlobToolArgs(args), nil
}

// DecodeReadToolArgs decodes Read args for ToolCall replay/update payloads.
func DecodeReadToolArgs(raw []byte) (*agentv1.ReadToolArgs, error) {
	args, err := decodeArgsMap(raw)
	if err != nil {
		return nil, err
	}
	result := &agentv1.ReadToolArgs{
		Path: strings.TrimSpace(readStringArg(args, "path")),
	}
	if result.Path == "" {
		return result, fmt.Errorf("Read path is required")
	}
	if offset, found, err := runtimecore.ReadInt32Arg(args, "offset"); err != nil {
		return result, err
	} else if found {
		result.Offset = int32Ptr(offset)
	}
	if limit, found, err := runtimecore.ReadUint32Arg(args, "limit"); err != nil {
		return result, err
	} else if found {
		if limit <= 1<<31-1 {
			result.Limit = int32Ptr(int32(limit))
		}
	}
	return result, nil
}

// DecodeGrepToolArgs decodes Grep args for client exec and ToolCall payloads.
func DecodeGrepToolArgs(raw []byte, toolCallID string) (*agentv1.GrepArgs, error) {
	args, err := decodeArgsMap(raw)
	if err != nil {
		return nil, err
	}
	result := &agentv1.GrepArgs{
		Pattern:    strings.TrimSpace(readStringArg(args, "pattern")),
		Path:       stringPtr(strings.TrimSpace(readStringArg(args, "path"))),
		Glob:       stringPtr(strings.TrimSpace(readStringArg(args, "glob"))),
		OutputMode: stringPtr(strings.TrimSpace(readStringArg(args, "output_mode", "outputMode"))),
		Type:       stringPtr(strings.TrimSpace(readStringArg(args, "type"))),
		ToolCallId: strings.TrimSpace(toolCallID),
	}
	if result.Pattern == "" {
		return result, fmt.Errorf("Grep pattern is required")
	}
	if contextBefore, found, err := runtimecore.ReadInt32Arg(args, "-B"); err != nil {
		return result, err
	} else if found {
		result.ContextBefore = int32Ptr(contextBefore)
	}
	if contextAfter, found, err := runtimecore.ReadInt32Arg(args, "-A"); err != nil {
		return result, err
	} else if found {
		result.ContextAfter = int32Ptr(contextAfter)
	}
	if context, found, err := runtimecore.ReadInt32Arg(args, "-C"); err != nil {
		return result, err
	} else if found {
		result.Context = int32Ptr(context)
	}
	caseInsensitive, err := readBoolPtrArg(args, "-i")
	if err != nil {
		return result, err
	}
	result.CaseInsensitive = caseInsensitive
	if headLimit, found, err := runtimecore.ReadInt32Arg(args, "head_limit", "headLimit"); err != nil {
		return result, err
	} else if found {
		result.HeadLimit = int32Ptr(headLimit)
	}
	multiline, err := readBoolPtrArg(args, "multiline")
	if err != nil {
		return result, err
	}
	result.Multiline = multiline
	if offset, found, err := runtimecore.ReadInt32Arg(args, "offset"); err != nil {
		return result, err
	} else if found {
		result.Offset = int32Ptr(offset)
	}
	return result, nil
}

// stringPtr 在需要 optional string 时构造指针值。
func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringPtrIfNonEmpty(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

// DecodeShellStdout 直接返回 shell stream stdout 的文本内容。
func DecodeShellStdout(stdout *agentv1.ShellStreamStdout) string {
	if stdout == nil {
		return ""
	}
	return stdout.GetData()
}

// int32Ptr 在需要 optional int32 时构造指针值。
func int32Ptr(value int32) *int32 {
	return &value
}

// uint32Ptr 在需要 optional uint32 时构造指针值。
func uint32Ptr(value uint32) *uint32 {
	return &value
}

// uint64Ptr 在需要 optional uint64 时构造指针值。
func uint64Ptr(value uint64) *uint64 {
	return &value
}

// shellAbortReasonPtr 在需要 optional ShellAbortReason 时构造指针值。
func shellAbortReasonPtr(value agentv1.ShellAbortReason) *agentv1.ShellAbortReason {
	return &value
}

// buildStructValueMap 把普通 JSON 对象映射为 protobuf Struct value 映射。
func buildStructValueMap(items map[string]any) map[string]*structpb.Value {
	if len(items) == 0 {
		return make(map[string]*structpb.Value)
	}
	result := make(map[string]*structpb.Value, len(items))
	for key, value := range items {
		item, err := structpb.NewValue(value)
		if err != nil {
			continue
		}
		result[key] = item
	}
	return result
}

// decodeArgsMap 把工具 JSON 参数解析为通用 map。
func decodeArgsMap(raw []byte) (map[string]any, error) {
	return runtimecore.DecodeArgsMap(raw)
}

func buildGlobToolArgs(args map[string]any) *agentv1.GlobToolArgs {
	return &agentv1.GlobToolArgs{
		TargetDirectory: stringPtr(readGlobTargetDirectoryArg(args)),
		GlobPattern:     readGlobPatternArg(args),
	}
}

func readGlobPatternArg(args map[string]any) string {
	return strings.TrimSpace(readStringArg(args, "glob_pattern", "globPattern", "pattern"))
}

func readGlobTargetDirectoryArg(args map[string]any) string {
	return strings.TrimSpace(readStringArg(args, "target_directory", "targetDirectory", "path"))
}

// readStringArg 从参数映射中按多个候选键读取字符串。
func readStringArg(args map[string]any, keys ...string) string {
	return runtimecore.ReadStringArg(args, keys...)
}

// readBoolArg 从参数映射中按多个候选键读取布尔值。
func readBoolArg(args map[string]any, keys ...string) bool {
	return runtimecore.ReadBoolArg(args, keys...)
}

// hasArgKey 判断参数映射中是否存在任一候选键。
func hasArgKey(args map[string]any, keys ...string) bool {
	return runtimecore.HasArgKey(args, keys...)
}

// readStringSliceArg 读取字符串数组参数。
func readStringSliceArg(args map[string]any, keys ...string) []string {
	return runtimecore.ReadStringSliceArg(args, keys...)
}

func readBoolPtrArg(args map[string]any, keys ...string) (*bool, error) {
	for _, key := range keys {
		value, ok := args[key]
		if !ok || value == nil {
			continue
		}
		typed, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("%s must be a boolean", key)
		}
		return &typed, nil
	}
	return nil, nil
}
