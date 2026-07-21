package execbridge

import (
	"fmt"
	"strings"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

// DecodeShellStdout 直接返回 shell stream stdout 的文本内容。
func DecodeShellStdout(stdout *agentv1.ShellStreamStdout) string {
	if stdout == nil {
		return ""
	}
	return stdout.GetData()
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
