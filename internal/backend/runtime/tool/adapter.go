// adapter.go 将现有 ExecBridge / InteractionBridge 适配到 Tool Runtime 接口。
package tool

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	runtimecore "cursor/internal/backend/agent/core"
)

// ExecBridgeAdapter 将 execbridge.ExecBridge 适配为 Tool Runtime 的 ExecBridge 接口。
type ExecBridgeAdapter struct {
	// OpenExec 是原始的 execbridge.OpenExec 调用签名。
	// 参数：toolName, argsJSON → execID, serverPayload, error
	openFunc func(toolName string, argsJSON []byte) (execID string, serverPayload []byte, err error)
}

// NewExecBridgeAdapter 创建执行桥适配器。
// rawBridge 是 *execbridge.Bridge 实例（通过 interface{} 避免循环依赖）。
func NewExecBridgeAdapter(rawBridge interface{}) *ExecBridgeAdapter {
	adapter := &ExecBridgeAdapter{}
	adapter.openFunc = func(toolName string, argsJSON []byte) (string, []byte, error) {
		return adapter.openExecWithBridge(rawBridge, toolName, argsJSON)
	}
	return adapter
}

func (a *ExecBridgeAdapter) OpenExec(toolName string, argsJSON []byte) (string, []byte, error) {
	if a == nil || a.openFunc == nil {
		return "", nil, fmt.Errorf("exec bridge adapter is not initialized")
	}
	return a.openFunc(toolName, argsJSON)
}

// openExecWithBridge 使用反射风格调用真实 ExecBridge。
// 通过构建 ToolInvocation 并调用 OpenExec 方法。
func (a *ExecBridgeAdapter) openExecWithBridge(rawBridge interface{}, toolName string, argsJSON []byte) (string, []byte, error) {
	// 构建 ToolInvocation
	invocation := runtimecore.ToolInvocation{
		CallID:   fmt.Sprintf("tool-%s-%d", strings.ToLower(toolName), len(argsJSON)),
		ToolName: strings.TrimSpace(toolName),
		ArgsJSON: argsJSON,
	}

	// 尝试通过类型断言调用真实的 OpenExec
	type execOpener interface {
		OpenExec(openContext interface{}, toolCall runtimecore.ToolInvocation) (interface{}, interface{}, error)
	}

	if bridge, ok := rawBridge.(execOpener); ok {
		serverMsg, pending, err := bridge.OpenExec(nil, invocation)
		if err != nil {
			return "", nil, err
		}
		// 序列化 server message
		payload, err := json.Marshal(serverMsg)
		if err != nil {
			return "", nil, err
		}
		// 从 pending 中提取 execID
		type pendingWithID interface {
			GetExecID() string
		}
		execID := ""
		if p, ok := pending.(pendingWithID); ok {
			execID = p.GetExecID()
		} else {
			execID = fmt.Sprintf("exec-%s", strings.ToLower(toolName))
		}
		return execID, payload, nil
	}

	return "", nil, fmt.Errorf("unsupported bridge type for tool: %s", toolName)
}

// InteractionBridgeAdapter 将 interactionbridge.InteractionBridge 适配为 Tool Runtime 的 InteractionBridge 接口。
type InteractionBridgeAdapter struct {
	openFunc func(toolName string, argsJSON []byte) (queryID string, serverPayload []byte, err error)
}

// NewInteractionBridgeAdapter 创建交互桥适配器。
func NewInteractionBridgeAdapter(rawBridge interface{}) *InteractionBridgeAdapter {
	adapter := &InteractionBridgeAdapter{}
	adapter.openFunc = func(toolName string, argsJSON []byte) (string, []byte, error) {
		return adapter.openQueryWithBridge(rawBridge, toolName, argsJSON)
	}
	return adapter
}

func (a *InteractionBridgeAdapter) OpenQuery(toolName string, argsJSON []byte) (string, []byte, error) {
	if a == nil || a.openFunc == nil {
		return "", nil, fmt.Errorf("interaction bridge adapter is not initialized")
	}
	return a.openFunc(toolName, argsJSON)
}

func (a *InteractionBridgeAdapter) openQueryWithBridge(rawBridge interface{}, toolName string, argsJSON []byte) (string, []byte, error) {
	invocation := runtimecore.ToolInvocation{
		CallID:   fmt.Sprintf("query-%s-%d", strings.ToLower(toolName), len(argsJSON)),
		ToolName: strings.TrimSpace(toolName),
		ArgsJSON: argsJSON,
	}

	type queryOpener interface {
		OpenQuery(toolCall runtimecore.ToolInvocation) (interface{}, interface{}, error)
	}

	if bridge, ok := rawBridge.(queryOpener); ok {
		serverMsg, pending, err := bridge.OpenQuery(invocation)
		if err != nil {
			return "", nil, err
		}
		payload, err := json.Marshal(serverMsg)
		if err != nil {
			return "", nil, err
		}
		type pendingWithID interface {
			GetInteractionID() string
		}
		queryID := ""
		if p, ok := pending.(pendingWithID); ok {
			queryID = p.GetInteractionID()
		} else {
			queryID = fmt.Sprintf("query-%s", strings.ToLower(toolName))
		}
		return queryID, payload, nil
	}

	return "", nil, fmt.Errorf("unsupported bridge type for query: %s", toolName)
}

// 确保 protojson 被使用
var _ = protojson.Marshal
