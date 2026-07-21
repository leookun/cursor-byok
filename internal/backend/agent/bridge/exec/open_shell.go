package execbridge

import (
	"fmt"
	"strings"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

// shellResultArgs 表示 Shell 工具的归一化参数。
type shellResultArgs struct {
	Command          string                       `json:"command"`
	Description      string                       `json:"description,omitempty"`
	WorkingDirectory string                       `json:"working_directory,omitempty"`
	BlockUntilMS     float64                      `json:"block_until_ms,omitempty"`
	BlockUntilMSSet  bool                         `json:"-"`
	NotifyOnOutput   *shellOutputNotificationArgs `json:"notify_on_output,omitempty"`
}

// shellOutputNotificationArgs 表示 Shell 输出通知配置参数。
type shellOutputNotificationArgs struct {
	Pattern           string
	Reason            string
	DebounceMS        *float64
	NotificationLimit *int32
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
