// manager.go Virtual Model Runtime 管理器：注册、解析、执行虚拟模型。
package virtualmodel

import (
	"context"
	"fmt"
	"strings"

	vmconfig "cursor/internal/backend/virtualmodel/config"
)

// VirtualModel 表示一个虚拟模型实例。
type VirtualModel interface {
	// ID 返回虚拟模型标识（如 "moa"）。
	ID() string
	// DisplayName 返回显示名称。
	DisplayName() string
	// Enabled 是否已启用。
	Enabled() bool
	// Execute 执行虚拟模型的工作流，返回 provider 兼容的事件流。
	Execute(ctx context.Context, req *ExecuteRequest) (*ExecuteResult, error)
}

// ExecuteRequest 虚拟模型执行请求。
type ExecuteRequest struct {
	// RequestID 请求 ID。
	RequestID string
	// ConversationID 会话 ID。
	ConversationID string
	// ModelCallID 模型调用 ID。
	ModelCallID string
	// Messages 输入消息。
	Messages []Message
	// Tools 可用工具定义。
	Tools []any
	// LatestUserText 最新用户文本。
	LatestUserText string
}

// ExecuteResult 虚拟模型执行结果。
type ExecuteResult struct {
	// Text 最终输出文本。
	Text string
	// ReasoningContent 推理内容（如有）。
	ReasoningContent string
	// FinishReason 结束原因。
	FinishReason string
	// Usage 用量统计。
	Usage *UsageSummary
	// NodeResults 各节点的执行结果（用于调试）。
	NodeResults []NodeExecuteResult
}

// Message 通用消息类型。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
	Name    string `json:"name,omitempty"`
}

// UsageSummary 用量汇总。
type UsageSummary struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// NodeExecuteResult 单个节点的执行结果。
type NodeExecuteResult struct {
	// NodeID 节点 ID。
	NodeID string `json:"nodeID"`
	// Role 节点角色。
	Role vmconfig.NodeRole `json:"role"`
	// AdapterID 使用的 adapter ID。
	AdapterID string `json:"adapterID"`
	// DurationMS 执行耗时（毫秒）。
	DurationMS int64 `json:"durationMS"`
	// Success 是否成功。
	Success bool `json:"success"`
	// Error 错误信息（如有）。
	Error string `json:"error,omitempty"`
	// OutputText 输出文本（截断）。
	OutputText string `json:"outputText,omitempty"`
}

// Manager 管理所有虚拟模型的注册和查找。
type Manager struct {
	models map[string]VirtualModel
}

// NewManager 创建虚拟模型管理器。
func NewManager() *Manager {
	return &Manager{
		models: make(map[string]VirtualModel),
	}
}

// Register 注册一个虚拟模型。
func (m *Manager) Register(model VirtualModel) error {
	if model == nil {
		return fmt.Errorf("virtual model is nil")
	}
	id := strings.TrimSpace(model.ID())
	if id == "" {
		return fmt.Errorf("virtual model ID is empty")
	}
	m.models[id] = model
	return nil
}

// Get 按 ID 查找虚拟模型。
func (m *Manager) Get(id string) (VirtualModel, bool) {
	model, ok := m.models[strings.TrimSpace(id)]
	return model, ok
}

// IsVirtualModel 判断指定 ID 是否为已注册的虚拟模型。
func (m *Manager) IsVirtualModel(id string) bool {
	if m == nil {
		return false
	}
	model, ok := m.Get(id)
	return ok && model != nil && model.Enabled()
}

// List 返回所有虚拟模型。
func (m *Manager) List() []VirtualModel {
	result := make([]VirtualModel, 0, len(m.models))
	for _, model := range m.models {
		result = append(result, model)
	}
	return result
}

// EnabledCount 返回已启用的虚拟模型数量。
func (m *Manager) EnabledCount() int {
	count := 0
	for _, model := range m.models {
		if model.Enabled() {
			count++
		}
	}
	return count
}
