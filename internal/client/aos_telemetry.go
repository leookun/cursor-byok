// Package client — AOS 遥测 / 执行树 / Replay IPC 封装。
package client

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	vm "cursor/internal/backend/virtualmodel"
	"cursor/internal/backend/virtualmodel/aos"
)

// AOSLastTraceSummary 最近一次 AOS 执行摘要 DTO。
type AOSLastTraceSummary struct {
	Summary  string            `json:"summary"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// AOSExecutionTreeNode 交互式执行树节点 DTO。
type AOSExecutionTreeNode struct {
	ID            string                 `json:"id"`
	Role          string                 `json:"role"`
	Action        string                 `json:"action"`
	Status        string                 `json:"status"`
	AdapterID     string                 `json:"adapterID,omitempty"`
	TaskID        string                 `json:"taskID,omitempty"`
	ExecID        string                 `json:"execID,omitempty"`
	ExecutionMode string                 `json:"executionMode,omitempty"`
	Spawned       bool                   `json:"spawned,omitempty"`
	DurationMS    int64                  `json:"durationMS,omitempty"`
	Tokens        int                    `json:"tokens,omitempty"`
	Prompt        string                 `json:"prompt,omitempty"`
	Response      string                 `json:"response,omitempty"`
	Error         string                 `json:"error,omitempty"`
	Children      []AOSExecutionTreeNode `json:"children,omitempty"`
	// NodeIndex is the original flat index into TraceFile.Nodes for single-node replay.
	NodeIndex int `json:"nodeIndex"`
}

// AOSExecutionTree 结构化执行树 DTO。
type AOSExecutionTree struct {
	SessionID  string               `json:"sessionID"`
	UserPrompt string               `json:"userPrompt,omitempty"`
	ModelID    string               `json:"modelID,omitempty"`
	Root       AOSExecutionTreeNode `json:"root"`
}

// GetAOSLastTraceSummary 返回进程内最近一次 AOS 执行轨迹摘要。
func (s *ProxyService) GetAOSLastTraceSummary() (AOSLastTraceSummary, error) {
	snap := aos.GetLastTraceSnapshot()
	return AOSLastTraceSummary{
		Summary:  snap.Summary,
		Metadata: snap.Metadata,
	}, nil
}

// GetAOSExecutionTree 按 session ID 返回结构化 AOS 执行树（Phase 9 切片）。
// 无落盘记录时返回零值 tree 且不报错（前端据此提示“暂无记录”）。
func (s *ProxyService) GetAOSExecutionTree(sessionID string) (AOSExecutionTree, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return AOSExecutionTree{}, nil
	}
	file, err := aos.LoadTrace(sessionID)
	if err != nil {
		return AOSExecutionTree{}, err
	}
	if file == nil {
		return AOSExecutionTree{}, nil
	}
	return buildExecutionTree(file), nil
}

// ReplayAOSTrace 以 trace 中保存的原始用户输入重新触发 AOS 执行。
func (s *ProxyService) ReplayAOSTrace(sessionID string) (string, error) {
	if s == nil || s.backendHost == nil {
		return "", fmt.Errorf("backend host is not available")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("sessionID is required")
	}
	file, err := aos.LoadTrace(sessionID)
	if err != nil {
		return "", err
	}
	if file == nil {
		return "", fmt.Errorf("trace not found for session %q", sessionID)
	}
	prompt := strings.TrimSpace(file.UserPrompt)
	if prompt == "" {
		return "", fmt.Errorf("trace %q has no userPrompt to replay", sessionID)
	}
	manager := s.backendHost.VirtualModelManager()
	if manager == nil {
		return "", fmt.Errorf("virtual model manager is not available")
	}
	modelID := strings.TrimSpace(file.ModelID)
	if modelID == "" {
		modelID = "aos"
	}
	model, ok := manager.Get(modelID)
	if !ok || model == nil {
		// fallback to registered aos id
		model, ok = manager.Get("aos")
		if !ok || model == nil {
			return "", fmt.Errorf("virtual model %q is not registered", modelID)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := model.Execute(ctx, &vm.ExecuteRequest{
		RequestID:      "replay-" + sessionID,
		ConversationID: sessionID,
		LatestUserText: prompt,
		Messages: []vm.Message{
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", fmt.Errorf("aos replay returned nil result")
	}
	return result.Text, nil
}

// ReplayAOSNode replays a single trace node by session ID and node index.
func (s *ProxyService) ReplayAOSNode(sessionID string, nodeIndex int) (string, error) {
	if s == nil || s.backendHost == nil {
		return "", fmt.Errorf("backend host is not available")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("sessionID is required")
	}
	file, err := aos.LoadTrace(sessionID)
	if err != nil {
		return "", err
	}
	if file == nil {
		return "", fmt.Errorf("trace not found for session %q", sessionID)
	}
	if nodeIndex < 0 || nodeIndex >= len(file.Nodes) {
		return "", fmt.Errorf("node index %d out of range (0..%d)", nodeIndex, len(file.Nodes)-1)
	}
	node := file.Nodes[nodeIndex]
	manager := s.backendHost.VirtualModelManager()
	if manager == nil {
		return "", fmt.Errorf("virtual model manager is not available")
	}
	modelID := strings.TrimSpace(file.ModelID)
	if modelID == "" {
		modelID = "aos"
	}
	model, ok := manager.Get(modelID)
	if !ok || model == nil {
		model, ok = manager.Get("aos")
		if !ok || model == nil {
			return "", fmt.Errorf("virtual model %q is not registered", modelID)
		}
	}
	aosModel, ok := model.(*aos.AOSModel)
	if !ok {
		return "", fmt.Errorf("model %q does not support node replay", modelID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return aosModel.ReplayNodeCall(ctx, node.AdapterID, node.Prompt)
}

func buildExecutionTree(file *aos.TraceFile) AOSExecutionTree {
	root := AOSExecutionTreeNode{
		ID:     "root",
		Role:   "aos",
		Action: "session",
		Status: "ok",
	}
	children := make([]AOSExecutionTreeNode, 0, len(file.Nodes))
	for i, n := range file.Nodes {
		children = append(children, AOSExecutionTreeNode{
			ID:            "n" + strconv.Itoa(i),
			Role:          n.Role,
			Action:        n.Action,
			Status:        n.Status,
			AdapterID:     n.AdapterID,
			TaskID:        n.TaskID,
			ExecID:        n.ExecID,
			ExecutionMode: n.ExecutionMode,
			Spawned:       n.Spawned,
			DurationMS:    n.Duration.Milliseconds(),
			Tokens:        n.Tokens,
			Prompt:        n.Prompt,
			Response:      n.Response,
			Error:         n.Error,
			NodeIndex:     i,
		})
	}
	root.Children = children
	return AOSExecutionTree{
		SessionID:  file.SessionID,
		UserPrompt: file.UserPrompt,
		ModelID:    file.ModelID,
		Root:       root,
	}
}