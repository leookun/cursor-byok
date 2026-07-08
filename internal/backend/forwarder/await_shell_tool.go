package forwarder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

const awaitShellOutputLimit = 16 * 1024

const (
	backgroundShellStatusBackgrounded     = "backgrounded"
	backgroundShellStatusRunning          = "running"
	backgroundShellStatusCompleted        = "completed"
	backgroundShellStatusRejected         = "rejected"
	backgroundShellStatusPermissionDenied = "permission_denied"
	backgroundShellStatusTransportClosed  = "transport_closed"
	backgroundShellStatusUnknown          = "unknown"

	backgroundShellActionSourceClient            = "client"
	backgroundShellActionSourceLocalBackgrounded = "local_shell_backgrounded"
)

type awaitShellArgs struct {
	ShellID      string `json:"shell_id,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	BlockUntilMS *int64 `json:"block_until_ms,omitempty"`
	Pattern      string `json:"pattern,omitempty"`
}

type awaitShellResult struct {
	ShellID        string  `json:"shell_id,omitempty"`
	Status         string  `json:"status"`
	Matched        bool    `json:"matched"`
	TimedOut       bool    `json:"timed_out"`
	ExitCode       *int64  `json:"exit_code,omitempty"`
	Stdout         string  `json:"stdout,omitempty"`
	Stderr         string  `json:"stderr,omitempty"`
	StdoutOffset   int     `json:"stdout_offset,omitempty"`
	StderrOffset   int     `json:"stderr_offset,omitempty"`
	RuntimeMS      uint64  `json:"runtime_ms,omitempty"`
	OutputLength   uint64  `json:"output_length,omitempty"`
	RegexRequested bool    `json:"regex_requested,omitempty"`
	RegexMatch     *string `json:"regex_match,omitempty"`
	Message        string  `json:"message,omitempty"`
}

func (service *Service) handleAwaitShellToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	args, err := decodeAwaitShellArgs(invocation.ArgsJSON)
	if err != nil {
		result := awaitShellResult{Status: "error", Message: err.Error()}
		payload, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			return encodeErr
		}
		return service.completeImmediateToolResult(stream, invocation, string(payload), buildAwaitShellToolCall(buildAwaitArgsFromAwaitShellArgs(args), buildAwaitShellProtoResult(result)))
	}
	result := service.awaitShellSnapshot(stream, args)
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return service.completeImmediateToolResult(stream, invocation, string(payload), buildAwaitShellToolCall(buildAwaitArgsFromAwaitShellArgs(args), buildAwaitShellProtoResult(result)))
}

func decodeAwaitShellArgs(raw []byte) (awaitShellArgs, error) {
	argsMap, err := runtimecore.DecodeArgsMap(raw)
	if err != nil {
		return awaitShellArgs{}, fmt.Errorf("decode AwaitShell args failed: %w", err)
	}
	result := awaitShellArgs{
		ShellID: strings.TrimSpace(runtimecore.ReadStringArg(argsMap, "shell_id")),
		TaskID:  strings.TrimSpace(runtimecore.ReadStringArg(argsMap, "task_id")),
		Pattern: strings.TrimSpace(runtimecore.ReadStringArg(argsMap, "pattern")),
	}
	if result.ShellID == "" {
		result.ShellID = result.TaskID
	}
	if value, found, err := runtimecore.ReadInt64Arg(argsMap, "block_until_ms"); err != nil {
		return result, err
	} else if found {
		if value < 0 {
			value = 0
		}
		result.BlockUntilMS = &value
	}
	if result.BlockUntilMS != nil && *result.BlockUntilMS == 0 && strings.TrimSpace(result.ShellID) == "" {
		return result, fmt.Errorf("AwaitShell shell_id is required when block_until_ms is 0")
	}
	return result, nil
}

func (service *Service) awaitShellSnapshot(stream *ActiveStream, args awaitShellArgs) awaitShellResult {
	blockUntilMS := int64(30000)
	if args.BlockUntilMS != nil {
		blockUntilMS = *args.BlockUntilMS
	}
	shellID := strings.TrimSpace(args.ShellID)
	if shellID == "" {
		return awaitShellResult{
			Status:   "waited",
			TimedOut: false,
			Message:  fmt.Sprintf("waited %dms", blockUntilMS),
		}
	}

	service.refreshBackgroundShellFromTerminalFile(stream, shellID)

	stream.mu.Lock()
	state, ok := stream.BackgroundShells[shellID]
	if !ok || state == nil {
		stream.mu.Unlock()
		return awaitShellResult{
			ShellID:  shellID,
			Status:   backgroundShellStatusUnknown,
			TimedOut: false,
			Message:  "unknown or expired shell_id",
		}
	}
	stdoutStart := clampOffset(state.AwaitStdoutOffset, len(state.StdoutBuffer))
	stderrStart := clampOffset(state.AwaitStderrOffset, len(state.StderrBuffer))
	stdout := state.StdoutBuffer[stdoutStart:]
	stderr := state.StderrBuffer[stderrStart:]
	stdoutEnd := len(state.StdoutBuffer)
	stderrEnd := len(state.StderrBuffer)
	state.AwaitStdoutOffset = stdoutEnd
	state.AwaitStderrOffset = stderrEnd
	status := strings.TrimSpace(state.Status)
	if status == "" {
		status = backgroundShellStatusUnknown
	}
	var exitCode *int64
	if state.ExitCode != nil {
		value := int64(*state.ExitCode)
		exitCode = &value
	}
	createdAt := state.CreatedAt
	completedAt := state.CompletedAt
	combinedOutput := state.StdoutBuffer + "\n" + state.StderrBuffer
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	matched, matchText, patternErr := awaitShellPatternMatched(args.Pattern, combinedOutput)
	message := ""
	if patternErr != nil {
		message = patternErr.Error()
	}
	timedOut := blockUntilMS > 0 && !matched && !isBackgroundShellTerminalStatus(status)
	stdout = truncateAwaitShellOutput(stdout)
	stderr = truncateAwaitShellOutput(stderr)
	return awaitShellResult{
		ShellID:        shellID,
		Status:         status,
		Matched:        matched,
		TimedOut:       timedOut,
		ExitCode:       exitCode,
		Stdout:         stdout,
		Stderr:         stderr,
		StdoutOffset:   stdoutEnd,
		StderrOffset:   stderrEnd,
		RuntimeMS:      backgroundShellRuntimeMS(createdAt, completedAt),
		OutputLength:   uint64(len(combinedOutput)),
		RegexRequested: strings.TrimSpace(args.Pattern) != "",
		RegexMatch:     matchText,
		Message:        message,
	}
}

func buildAwaitArgsFromAwaitShellArgs(args awaitShellArgs) *agentv1.AwaitArgs {
	awaitArgs := &agentv1.AwaitArgs{TaskId: strings.TrimSpace(firstNonEmpty(args.ShellID, args.TaskID))}
	if args.BlockUntilMS != nil {
		value := uint32(0)
		if *args.BlockUntilMS > 0 {
			value = uint32(*args.BlockUntilMS)
		}
		awaitArgs.BlockUntilMs = &value
	}
	if pattern := strings.TrimSpace(args.Pattern); pattern != "" {
		awaitArgs.Regex = &pattern
	}
	return awaitArgs
}

func buildAwaitShellToolCall(args *agentv1.AwaitArgs, result *agentv1.AwaitResult) *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_AwaitToolCall{
			AwaitToolCall: &agentv1.AwaitToolCall{
				Args:   args,
				Result: result,
			},
		},
	}
}

func buildAwaitShellProtoResult(result awaitShellResult) *agentv1.AwaitResult {
	switch strings.TrimSpace(result.Status) {
	case "error", backgroundShellStatusUnknown:
		message := firstNonEmpty(strings.TrimSpace(result.Message), "unknown or expired shell_id")
		return &agentv1.AwaitResult{Result: &agentv1.AwaitResult_Error{Error: &agentv1.AwaitError{Error: message}}}
	}
	if isBackgroundShellTerminalStatus(result.Status) || result.Matched {
		task := &agentv1.AwaitTaskComplete{
			TaskId:         strings.TrimSpace(result.ShellID),
			RuntimeMs:      result.RuntimeMS,
			OutputLength:   result.OutputLength,
			RegexRequested: result.RegexRequested,
			RegexMatch:     result.RegexMatch,
		}
		if result.ExitCode != nil {
			exitCode := int32(*result.ExitCode)
			task.ExitCode = &exitCode
		}
		return &agentv1.AwaitResult{Result: &agentv1.AwaitResult_Complete{Complete: task}}
	}
	task := &agentv1.AwaitTaskStillRunning{
		TaskId:         strings.TrimSpace(result.ShellID),
		RuntimeMs:      result.RuntimeMS,
		OutputLength:   result.OutputLength,
		RegexRequested: result.RegexRequested,
		RegexMatch:     result.RegexMatch,
	}
	return &agentv1.AwaitResult{Result: &agentv1.AwaitResult_StillRunning{StillRunning: task}}
}

func backgroundShellRuntimeMS(createdAt time.Time, completedAt time.Time) uint64 {
	if createdAt.IsZero() {
		return 0
	}
	end := completedAt
	if end.IsZero() {
		end = time.Now().UTC()
	}
	if end.Before(createdAt) {
		return 0
	}
	return uint64(end.Sub(createdAt).Milliseconds())
}

func awaitShellPatternMatched(pattern string, output string) (bool, *string, error) {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" {
		return false, nil, nil
	}
	expr, err := regexp.Compile("(?m)" + trimmed)
	if err != nil {
		return false, nil, fmt.Errorf("invalid AwaitShell pattern: %w", err)
	}
	match := expr.FindString(output)
	if match == "" {
		return false, nil, nil
	}
	return true, &match, nil
}

func truncateAwaitShellOutput(value string) string {
	if len(value) <= awaitShellOutputLimit {
		return value
	}
	return value[len(value)-awaitShellOutputLimit:]
}

func clampOffset(offset int, length int) int {
	if offset < 0 {
		return 0
	}
	if offset > length {
		return length
	}
	return offset
}

type terminalShellFileSnapshot struct {
	PID       *uint32
	Command   string
	CWD       string
	StartedAt time.Time
	Output    string
	ExitCode  *int32
	EndedAt   time.Time
}

func (service *Service) refreshBackgroundShellFromTerminalFile(stream *ActiveStream, shellID string) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	terminalsFolder := strings.TrimSpace(stream.TerminalsFolder)
	stream.mu.Unlock()
	terminalSnapshot, ok := readTerminalShellFileSnapshot(terminalsFolder, shellID)
	if !ok {
		return
	}
	now := time.Now().UTC()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	state := ensureBackgroundShellStateLocked(stream, shellID, runtimecore.PendingExec{}, firstNonZeroTime(terminalSnapshot.StartedAt, now))
	if state == nil {
		return
	}
	if state.Command == "" {
		state.Command = terminalSnapshot.Command
	}
	if state.WorkingDirectory == "" {
		state.WorkingDirectory = terminalSnapshot.CWD
	}
	if state.PID == nil && terminalSnapshot.PID != nil {
		pid := *terminalSnapshot.PID
		state.PID = &pid
	}
	if state.CreatedAt.IsZero() {
		state.CreatedAt = firstNonZeroTime(terminalSnapshot.StartedAt, now)
	}
	if state.StdoutBuffer == "" && state.StderrBuffer == "" && terminalSnapshot.Output != "" {
		state.StdoutBuffer = terminalSnapshot.Output
		if !isBackgroundShellTerminalStatus(state.Status) {
			state.Status = backgroundShellStatusRunning
		}
		state.LastActivityAt = now
	}
	if !isBackgroundShellTerminalStatus(state.Status) && terminalSnapshot.ExitCode != nil {
		exitCode := *terminalSnapshot.ExitCode
		state.ExitCode = &exitCode
		state.Status = backgroundShellStatusCompleted
		state.CompletedAt = firstNonZeroTime(terminalSnapshot.EndedAt, now)
		state.LastActivityAt = state.CompletedAt
	}
	if strings.TrimSpace(state.Status) == "" {
		state.Status = backgroundShellStatusBackgrounded
	}
	if state.LastActivityAt.IsZero() {
		state.LastActivityAt = now
	}
	stream.UpdatedAt = now
}

func readTerminalShellFileSnapshot(terminalsFolder string, shellID string) (terminalShellFileSnapshot, bool) {
	path, ok := terminalShellFilePath(terminalsFolder, shellID)
	if !ok {
		return terminalShellFileSnapshot{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return terminalShellFileSnapshot{}, false
	}
	return parseTerminalShellFileSnapshot(string(data)), true
}

func terminalShellFilePath(terminalsFolder string, shellID string) (string, bool) {
	folder := filepath.Clean(strings.TrimSpace(terminalsFolder))
	if folder == "." || !filepath.IsAbs(folder) {
		return "", false
	}
	id, err := strconv.ParseUint(strings.TrimSpace(shellID), 10, 32)
	if err != nil {
		return "", false
	}
	filename := strconv.FormatUint(id, 10) + ".txt"
	return filepath.Join(folder, filename), true
}

func parseTerminalShellFileSnapshot(raw string) terminalShellFileSnapshot {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	separators := terminalShellSeparatorIndexes(lines)
	snapshot := terminalShellFileSnapshot{}
	if len(separators) >= 2 {
		parseTerminalShellMetadata(lines[separators[0]+1:separators[1]], &snapshot)
	}
	outputStart := 0
	if len(separators) >= 2 {
		outputStart = separators[1] + 1
	}
	outputEnd := len(lines)
	for index := len(separators) - 2; index >= 1; index-- {
		block := lines[separators[index]+1 : separators[index+1]]
		if terminalMetadataBlockHasKey(block, "exit_code") || terminalMetadataBlockHasKey(block, "ended_at") {
			parseTerminalShellMetadata(block, &snapshot)
			outputEnd = separators[index]
			break
		}
	}
	if outputStart < outputEnd {
		snapshot.Output = strings.Join(lines[outputStart:outputEnd], "\n")
		snapshot.Output = strings.TrimSuffix(snapshot.Output, "\n")
	}
	return snapshot
}

func terminalShellSeparatorIndexes(lines []string) []int {
	indexes := make([]int, 0, 4)
	for index, line := range lines {
		if strings.TrimSpace(line) == "---" {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func parseTerminalShellMetadata(lines []string, snapshot *terminalShellFileSnapshot) {
	if snapshot == nil {
		return
	}
	for _, line := range lines {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = parseTerminalShellMetadataValue(value)
		switch key {
		case "pid":
			if parsed, err := strconv.ParseUint(value, 10, 32); err == nil {
				pid := uint32(parsed)
				snapshot.PID = &pid
			}
		case "cwd":
			snapshot.CWD = value
		case "command":
			snapshot.Command = value
		case "started_at":
			snapshot.StartedAt = parseTerminalShellTime(value)
		case "exit_code":
			if parsed, err := strconv.ParseInt(value, 10, 32); err == nil {
				exitCode := int32(parsed)
				snapshot.ExitCode = &exitCode
			}
		case "ended_at":
			snapshot.EndedAt = parseTerminalShellTime(value)
		}
	}
}

func parseTerminalShellMetadataValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if unquoted, err := strconv.Unquote(trimmed); err == nil {
		return unquoted
	}
	return trimmed
}

func parseTerminalShellTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func terminalMetadataBlockHasKey(lines []string, key string) bool {
	for _, line := range lines {
		current, _, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(current) == key {
			return true
		}
	}
	return false
}

func isBackgroundShellTerminalStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case backgroundShellStatusCompleted, backgroundShellStatusRejected, backgroundShellStatusPermissionDenied, backgroundShellStatusTransportClosed:
		return true
	default:
		return false
	}
}

func (service *Service) observeBackgroundShellExecClientMessage(stream *ActiveStream, pending runtimecore.PendingExec, message *agentv1.ExecClientMessage) {
	if stream == nil || message == nil {
		return
	}
	now := time.Now().UTC()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	service.observeBackgroundShellSpawnResultLocked(stream, pending, message.GetBackgroundShellSpawnResult(), now)
	service.observeForceBackgroundShellResultLocked(stream, pending, message.GetForceBackgroundShellResult(), now)
}

func (service *Service) observeMissingBackgroundShellExecClientMessage(stream *ActiveStream, message *agentv1.ExecClientMessage) bool {
	if stream == nil || message == nil {
		return false
	}
	if message.GetBackgroundShellSpawnResult() == nil && message.GetForceBackgroundShellResult() == nil {
		return false
	}
	now := time.Now().UTC()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	pending := runtimecore.PendingExec{
		MessageID: message.GetId(),
		ExecID:    strings.TrimSpace(message.GetExecId()),
		ExecKind:  "shell",
	}
	return service.observeBackgroundShellSpawnResultLocked(stream, pending, message.GetBackgroundShellSpawnResult(), now) || service.observeForceBackgroundShellResultLocked(stream, pending, message.GetForceBackgroundShellResult(), now)
}

func (service *Service) observeBackgroundShellSpawnResultLocked(stream *ActiveStream, pending runtimecore.PendingExec, result *agentv1.BackgroundShellSpawnResult, now time.Time) bool {
	if stream == nil || result == nil {
		return false
	}
	if stream.BackgroundShells == nil {
		stream.BackgroundShells = make(map[string]*BackgroundShellState)
	}
	if stream.BackgroundShellsByMessageID == nil {
		stream.BackgroundShellsByMessageID = make(map[uint32]string)
	}
	if stream.BackgroundShellsByExecID == nil {
		stream.BackgroundShellsByExecID = make(map[string]string)
	}
	success := result.GetSuccess()
	if success != nil {
		shellID := strconv.FormatUint(uint64(success.GetShellId()), 10)
		state := stream.BackgroundShells[shellID]
		if state == nil {
			state = &BackgroundShellState{ShellID: shellID, CreatedAt: now}
			stream.BackgroundShells[shellID] = state
		}
		pid := success.Pid
		state.Command = firstNonEmpty(strings.TrimSpace(success.GetCommand()), state.Command)
		state.WorkingDirectory = firstNonEmpty(strings.TrimSpace(success.GetWorkingDirectory()), state.WorkingDirectory)
		state.PID = pid
		state.Status = backgroundShellStatusBackgrounded
		state.OriginalToolCallID = firstNonEmpty(strings.TrimSpace(pending.ToolCallID), state.OriginalToolCallID)
		state.OriginalExecID = firstNonEmpty(strings.TrimSpace(pending.ExecID), state.OriginalExecID)
		if state.OriginalMessageID == 0 {
			state.OriginalMessageID = pending.MessageID
		}
		if len(state.ArgsJSON) == 0 && len(pending.ArgsJSON) > 0 {
			state.ArgsJSON = append([]byte(nil), pending.ArgsJSON...)
		}
		state.ModelCallID = firstNonEmpty(strings.TrimSpace(pending.ModelCallID), state.ModelCallID)
		state.LastActivityAt = now
		if pending.MessageID != 0 {
			stream.BackgroundShellsByMessageID[pending.MessageID] = shellID
		}
		if strings.TrimSpace(pending.ExecID) != "" {
			stream.BackgroundShellsByExecID[strings.TrimSpace(pending.ExecID)] = shellID
		}
		stream.UpdatedAt = now
		return true
	}
	shellID := backgroundShellIDForMessageLocked(stream, pending.MessageID, pending.ExecID)
	if shellID == "" {
		return false
	}
	state := ensureBackgroundShellStateLocked(stream, shellID, pending, now)
	if state == nil {
		return false
	}
	switch {
	case result.GetRejected() != nil:
		state.Status = backgroundShellStatusRejected
		state.StderrBuffer += strings.TrimSpace(result.GetRejected().GetReason())
	case result.GetPermissionDenied() != nil:
		state.Status = backgroundShellStatusPermissionDenied
		state.StderrBuffer += strings.TrimSpace(result.GetPermissionDenied().GetError())
	case result.GetError() != nil:
		state.Status = backgroundShellStatusTransportClosed
		state.StderrBuffer += strings.TrimSpace(result.GetError().GetError())
	default:
		return false
	}
	state.LastActivityAt = now
	state.CompletedAt = now
	stream.UpdatedAt = now
	return true
}

func (service *Service) observeForceBackgroundShellResultLocked(stream *ActiveStream, pending runtimecore.PendingExec, result *agentv1.ForceBackgroundShellResult, now time.Time) bool {
	if stream == nil || result == nil || result.GetShellResult() == nil {
		return false
	}
	shellResult := result.GetShellResult()
	shellIDValue := uint32(0)
	if success := shellResult.GetSuccess(); success != nil {
		shellIDValue = success.GetShellId()
	}
	if shellIDValue == 0 {
		return false
	}
	shellID := strconv.FormatUint(uint64(shellIDValue), 10)
	state := ensureBackgroundShellStateLocked(stream, shellID, pending, now)
	if state == nil {
		return false
	}
	state.Status = backgroundShellStatusBackgrounded
	state.LastActivityAt = now
	stream.UpdatedAt = now
	return true
}

func (service *Service) observeShellExecClientMessage(stream *ActiveStream, pending runtimecore.PendingExec, message *agentv1.ExecClientMessage) {
	if stream == nil || message == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return
	}
	shellStream := message.GetShellStream()
	if shellStream == nil {
		return
	}
	now := time.Now().UTC()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	service.observeShellStreamLocked(stream, pending, shellStream, now)
}

func (service *Service) observeMissingShellExecClientMessage(stream *ActiveStream, message *agentv1.ExecClientMessage) bool {
	if stream == nil || message == nil || message.GetShellStream() == nil {
		return false
	}
	now := time.Now().UTC()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	shellID := backgroundShellIDForMessageLocked(stream, message.GetId(), message.GetExecId())
	if shellID == "" {
		return false
	}
	pending := runtimecore.PendingExec{
		MessageID: message.GetId(),
		ExecID:    strings.TrimSpace(message.GetExecId()),
		ExecKind:  "shell",
	}
	if state := stream.BackgroundShells[shellID]; state != nil {
		pending.ToolCallID = state.OriginalToolCallID
		pending.ArgsJSON = append([]byte(nil), state.ArgsJSON...)
		pending.ModelCallID = state.ModelCallID
	}
	service.observeShellStreamLocked(stream, pending, message.GetShellStream(), now)
	return true
}

func (service *Service) observeShellStreamLocked(stream *ActiveStream, pending runtimecore.PendingExec, shellStream *agentv1.ShellStream, now time.Time) {
	if stream.BackgroundShells == nil {
		stream.BackgroundShells = make(map[string]*BackgroundShellState)
	}
	if stream.BackgroundShellsByMessageID == nil {
		stream.BackgroundShellsByMessageID = make(map[uint32]string)
	}
	if stream.BackgroundShellsByExecID == nil {
		stream.BackgroundShellsByExecID = make(map[string]string)
	}

	shellID := backgroundShellIDForMessageLocked(stream, pending.MessageID, pending.ExecID)
	switch event := shellStream.GetEvent().(type) {
	case *agentv1.ShellStream_Backgrounded:
		shellID = strconv.FormatUint(uint64(event.Backgrounded.GetShellId()), 10)
		state := stream.BackgroundShells[shellID]
		if state == nil {
			state = &BackgroundShellState{ShellID: shellID, CreatedAt: now}
			stream.BackgroundShells[shellID] = state
		}
		state.Command = firstNonEmpty(strings.TrimSpace(event.Backgrounded.GetCommand()), state.Command)
		state.WorkingDirectory = firstNonEmpty(strings.TrimSpace(event.Backgrounded.GetWorkingDirectory()), state.WorkingDirectory)
		state.PID = event.Backgrounded.Pid
		state.Status = backgroundShellStatusBackgrounded
		state.OriginalToolCallID = strings.TrimSpace(pending.ToolCallID)
		state.OriginalExecID = strings.TrimSpace(pending.ExecID)
		state.OriginalMessageID = pending.MessageID
		state.ArgsJSON = append([]byte(nil), pending.ArgsJSON...)
		state.ModelCallID = strings.TrimSpace(pending.ModelCallID)
		state.LastActivityAt = now
		if pending.MessageID != 0 {
			stream.BackgroundShellsByMessageID[pending.MessageID] = shellID
		}
		if strings.TrimSpace(pending.ExecID) != "" {
			stream.BackgroundShellsByExecID[strings.TrimSpace(pending.ExecID)] = shellID
		}
	case *agentv1.ShellStream_Stdout:
		state := ensureBackgroundShellStateLocked(stream, shellID, pending, now)
		if state == nil {
			return
		}
		state.StdoutBuffer += execbridge.DecodeShellStdout(event.Stdout)
		state.Status = backgroundShellStatusRunning
		state.LastActivityAt = now
	case *agentv1.ShellStream_Stderr:
		state := ensureBackgroundShellStateLocked(stream, shellID, pending, now)
		if state == nil {
			return
		}
		state.StderrBuffer += event.Stderr.GetData()
		state.Status = backgroundShellStatusRunning
		state.LastActivityAt = now
	case *agentv1.ShellStream_Exit:
		state := ensureBackgroundShellStateLocked(stream, shellID, pending, now)
		if state == nil {
			return
		}
		exitCode := int32(event.Exit.GetCode())
		state.ExitCode = &exitCode
		state.WorkingDirectory = firstNonEmpty(strings.TrimSpace(event.Exit.GetCwd()), state.WorkingDirectory)
		state.Status = backgroundShellStatusCompleted
		state.LastActivityAt = now
		state.CompletedAt = now
	case *agentv1.ShellStream_Rejected:
		state := ensureBackgroundShellStateLocked(stream, shellID, pending, now)
		if state == nil {
			return
		}
		state.Status = backgroundShellStatusRejected
		state.StderrBuffer += strings.TrimSpace(event.Rejected.GetReason())
		state.LastActivityAt = now
		state.CompletedAt = now
	case *agentv1.ShellStream_PermissionDenied:
		state := ensureBackgroundShellStateLocked(stream, shellID, pending, now)
		if state == nil {
			return
		}
		state.Status = backgroundShellStatusPermissionDenied
		state.StderrBuffer += strings.TrimSpace(event.PermissionDenied.GetError())
		state.LastActivityAt = now
		state.CompletedAt = now
	}
	stream.UpdatedAt = now
}

func observeBackgroundTaskCompletionAction(stream *ActiveStream, message *agentv1.AgentClientMessage) {
	if stream == nil || message == nil {
		return
	}
	action := message.GetConversationAction()
	if action == nil {
		return
	}
	item, ok := action.GetAction().(*agentv1.ConversationAction_BackgroundTaskCompletionAction)
	if !ok || item.BackgroundTaskCompletionAction == nil {
		return
	}
	now := time.Now().UTC()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	for _, completion := range item.BackgroundTaskCompletionAction.GetCompletions() {
		observeBackgroundTaskCompletionLocked(stream, completion, now)
	}
}

func observeBackgroundTaskCompletionLocked(stream *ActiveStream, completion *agentv1.BackgroundTaskCompletion, now time.Time) bool {
	if stream == nil || completion == nil || completion.GetKind() != agentv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL {
		return false
	}
	shellID := strings.TrimSpace(completion.GetTaskId())
	if shellID == "" {
		return false
	}
	state := ensureBackgroundShellStateLocked(stream, shellID, runtimecore.PendingExec{}, now)
	if state == nil {
		return false
	}
	detail := strings.TrimSpace(completion.GetDetail())
	switch completion.GetStatus() {
	case agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCESS:
		state.Status = backgroundShellStatusCompleted
		if detail != "" {
			state.StdoutBuffer = appendBackgroundShellBuffer(state.StdoutBuffer, detail)
		}
	case agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_ERROR:
		state.Status = backgroundShellStatusTransportClosed
		if detail != "" {
			state.StderrBuffer = appendBackgroundShellBuffer(state.StderrBuffer, detail)
		}
	case agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_ABORTED:
		state.Status = backgroundShellStatusTransportClosed
		if detail != "" {
			state.StderrBuffer = appendBackgroundShellBuffer(state.StderrBuffer, detail)
		}
	default:
		if completion.GetReason() == agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_PROGRESS {
			state.Status = backgroundShellStatusRunning
		} else {
			return false
		}
	}
	state.LastActivityAt = now
	if completion.GetReason() == agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_FINISHED || isBackgroundShellTerminalStatus(state.Status) {
		state.CompletedAt = now
	}
	stream.UpdatedAt = now
	return true
}

func observeBackgroundShellAction(stream *ActiveStream, message *agentv1.AgentClientMessage) (string, bool) {
	if stream == nil || message == nil {
		return "", false
	}
	action := message.GetConversationAction()
	if action == nil {
		return "", false
	}
	item, ok := action.GetAction().(*agentv1.ConversationAction_BackgroundShellAction)
	if !ok || item.BackgroundShellAction == nil {
		return "", false
	}
	return recordBackgroundShellActionMemory(stream, item.BackgroundShellAction.GetToolCallId(), time.Now().UTC())
}

func recordBackgroundShellActionMemory(stream *ActiveStream, toolCallID string, now time.Time) (string, bool) {
	trimmedToolCallID := strings.TrimSpace(toolCallID)
	if stream == nil || trimmedToolCallID == "" {
		return "", false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return recordBackgroundShellActionLocked(stream, trimmedToolCallID, now)
}

func recordBackgroundShellActionLocked(stream *ActiveStream, toolCallID string, now time.Time) (string, bool) {
	trimmedToolCallID := strings.TrimSpace(toolCallID)
	if stream == nil || trimmedToolCallID == "" {
		return "", false
	}
	if stream.BackgroundShellActions == nil {
		stream.BackgroundShellActions = make(map[string]time.Time)
	}
	if _, exists := stream.BackgroundShellActions[trimmedToolCallID]; exists {
		return trimmedToolCallID, false
	}
	stream.BackgroundShellActions[trimmedToolCallID] = now
	stream.UpdatedAt = now
	return trimmedToolCallID, true
}

func newBackgroundShellActionMetadataEntry(turnSeq int64, requestID string, toolCallID string, source string) HistoryEntry {
	return newMetadataEntry(turnSeq, requestID, "background_shell_action", map[string]any{
		"tool_call_id": strings.TrimSpace(toolCallID),
		"source":       strings.TrimSpace(source),
	})
}

func backgroundTaskCompletionMetadataEntries(turnSeq int64, requestID string, message *agentv1.AgentClientMessage) []HistoryEntry {
	if message == nil || message.GetConversationAction() == nil {
		return nil
	}
	item, ok := message.GetConversationAction().GetAction().(*agentv1.ConversationAction_BackgroundTaskCompletionAction)
	if !ok || item.BackgroundTaskCompletionAction == nil {
		return nil
	}
	completions := item.BackgroundTaskCompletionAction.GetCompletions()
	if len(completions) == 0 {
		return nil
	}
	entries := make([]HistoryEntry, 0, len(completions))
	for _, completion := range completions {
		if completion == nil {
			continue
		}
		values := map[string]any{
			"task_id": strings.TrimSpace(completion.GetTaskId()),
			"kind":    completion.GetKind().String(),
			"status":  completion.GetStatus().String(),
			"reason":  completion.GetReason().String(),
		}
		if title := strings.TrimSpace(completion.GetTitle()); title != "" {
			values["title"] = title
		}
		if detail := strings.TrimSpace(completion.GetDetail()); detail != "" {
			values["detail"] = detail
		}
		if outputPath := strings.TrimSpace(completion.GetOutputPath()); outputPath != "" {
			values["output_path"] = outputPath
		}
		if threadID := strings.TrimSpace(completion.GetThreadId()); threadID != "" {
			values["thread_id"] = threadID
		}
		entries = append(entries, newMetadataEntry(turnSeq, requestID, "background_task_completion_action", values))
	}
	return entries
}

func shellToolCallIsBackgrounded(toolCall *agentv1.ToolCall) bool {
	if toolCall == nil {
		return false
	}
	shellToolCall := toolCall.GetShellToolCall()
	if shellToolCall == nil || shellToolCall.GetResult() == nil {
		return false
	}
	return shellToolCall.GetResult().GetIsBackground()
}

func appendBackgroundShellBuffer(current string, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return current
	}
	if current == "" || strings.HasSuffix(current, "\n") {
		return current + trimmed
	}
	return current + "\n" + trimmed
}

func ensureBackgroundShellStateLocked(stream *ActiveStream, shellID string, pending runtimecore.PendingExec, now time.Time) *BackgroundShellState {
	trimmedShellID := strings.TrimSpace(shellID)
	if trimmedShellID == "" {
		return nil
	}
	if stream.BackgroundShells == nil {
		stream.BackgroundShells = make(map[string]*BackgroundShellState)
	}
	if stream.BackgroundShellsByMessageID == nil {
		stream.BackgroundShellsByMessageID = make(map[uint32]string)
	}
	if stream.BackgroundShellsByExecID == nil {
		stream.BackgroundShellsByExecID = make(map[string]string)
	}
	state := stream.BackgroundShells[trimmedShellID]
	if state == nil {
		state = &BackgroundShellState{ShellID: trimmedShellID, Status: backgroundShellStatusRunning, CreatedAt: now}
		stream.BackgroundShells[trimmedShellID] = state
	}
	if pending.MessageID != 0 {
		stream.BackgroundShellsByMessageID[pending.MessageID] = trimmedShellID
	}
	if strings.TrimSpace(pending.ExecID) != "" {
		stream.BackgroundShellsByExecID[strings.TrimSpace(pending.ExecID)] = trimmedShellID
	}
	if state.OriginalToolCallID == "" {
		state.OriginalToolCallID = strings.TrimSpace(pending.ToolCallID)
	}
	if state.OriginalExecID == "" {
		state.OriginalExecID = strings.TrimSpace(pending.ExecID)
	}
	if state.OriginalMessageID == 0 {
		state.OriginalMessageID = pending.MessageID
	}
	if len(state.ArgsJSON) == 0 && len(pending.ArgsJSON) > 0 {
		state.ArgsJSON = append([]byte(nil), pending.ArgsJSON...)
	}
	if state.ModelCallID == "" {
		state.ModelCallID = strings.TrimSpace(pending.ModelCallID)
	}
	return state
}

func backgroundShellIDForMessageLocked(stream *ActiveStream, messageID uint32, execID string) string {
	if stream == nil {
		return ""
	}
	if strings.TrimSpace(execID) != "" {
		if shellID := strings.TrimSpace(stream.BackgroundShellsByExecID[strings.TrimSpace(execID)]); shellID != "" {
			return shellID
		}
	}
	if messageID != 0 {
		return strings.TrimSpace(stream.BackgroundShellsByMessageID[messageID])
	}
	return ""
}
