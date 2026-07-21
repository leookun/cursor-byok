package execbridge

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

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
