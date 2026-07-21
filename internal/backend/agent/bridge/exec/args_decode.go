package execbridge

import (
	"fmt"
	"strings"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

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
