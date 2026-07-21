package execbridge

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

// readExecArgs 表示 Read 工具的归一化参数。
type readExecArgs struct {
	Path   string
	Offset *int32
	Limit  *uint32
}

// writeShellStdinArgs 表示 WriteShellStdin 工具的归一化参数。
type writeShellStdinArgs struct {
	ShellID uint32
	Chars   string
}

// forceBackgroundShellArgs 表示 ForceBackgroundShell 工具的归一化参数。
type forceBackgroundShellArgs struct {
	ToolCallID string
}

// grepReplayBudget 跟踪 grep 结果回放时的剩余配额。
type grepReplayBudget struct {
	remainingContentBytes int
	remainingMatches      int
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
