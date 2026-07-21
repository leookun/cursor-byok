// manager.go Virtual Model Runtime 管理器：注册、解析、执行虚拟模型。
package virtualmodel

import (
	"context"
	"fmt"
	"strings"
	"sync"

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
	// AdapterMetadata 返回虚拟模型暴露给 Cursor 的适配器元数据。
	//
	// 虚拟模型没有自己的物理模型参数；它应从其"主用 adapter"（例如
	// AOS 的 Leader adapter、MOA 的 Planner adapter）继承上下文窗口、
	// 最大输出 tokens、思考强度等参数，使 Cursor UI 与底层物理模型
	// 行为一致。实现可返回零值表示"无元数据可继承"。
	//
	// 接收 ctx 是因为解析 adapter 元数据通常需要走 ChannelResolver
	// （网络/快照读取）。调用方应使用短超时 ctx。
	AdapterMetadata(ctx context.Context) AdapterMetadata
}

// AdapterMetadata 是虚拟模型暴露给 Cursor 的适配器元数据。
// 由 VirtualModel.AdapterMetadata() 返回，VMResolver 在构建
// ModelAdapterConfig 时读取这些字段，使虚拟模型在 Cursor 模型选择器
// 中显示正确的上下文窗口、思考强度等信息。
//
// 字段语义与 legacyruntime.ModelAdapterConfig 同名字段一致。
type AdapterMetadata struct {
	// TooltipData 模型选择器中的描述文本。
	TooltipData string
	// ContextWindowTokens 上下文窗口大小（tokens）。
	ContextWindowTokens int
	// MaxCompletionTokens 单次最大输出 tokens。
	MaxCompletionTokens int
	// ReasoningEffort OpenAI 风格 reasoning effort（low/medium/high/xhigh）。
	ReasoningEffort string
	// AnthropicThinkingEffort Anthropic adaptive thinking effort。
	AnthropicThinkingEffort string
	// ThinkingBudgetTokens thinking budget tokens。
	ThinkingBudgetTokens int
	// AnthropicMaxTokens Anthropic 风格 max tokens。
	AnthropicMaxTokens int
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
	// ThinkingEffort 用户在 Cursor 模型选择器中选择的思考强度
	// （low/medium/high/xhigh）。虚拟模型应将其传递给内部 Leader /
	// member adapter 调用，使 Cursor 选择真正影响底层物理模型行为。
	ThinkingEffort string
	// MaxTokens 用户选择的最大输出 tokens。0 表示使用 adapter 默认值。
	MaxTokens int
}

// ExecuteResult 虚拟模型执行结果。
type ExecuteResult struct {
	// Text 最终输出文本。
	Text string
	// PhaseText 阶段性输出文本（如 AOS planning/sprint 状态），在最终结果前推送。
	PhaseText string
	// ReasoningContent 推理内容（如有）。
	ReasoningContent string
	// FinishReason 结束原因。
	FinishReason string
	// Usage 用量统计。
	Usage *UsageSummary
	// NodeResults 各节点的执行结果（用于调试）。
	NodeResults []NodeExecuteResult
	// Metadata 可选的运行时观测数据（例如 AOS 阶段与 trace 摘要）。
	Metadata map[string]string
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

// ctx keys for propagating Cursor request parameters (ThinkingEffort/MaxTokens)
// from ExecuteRequest into the internal adapter call chain so that AOS/MOA
// Leader and member calls respect the user's Cursor selection.
type ctxKeyThinkingEffort struct{}
type ctxKeyMaxTokens struct{}

// WithThinkingEffort returns a derived context carrying the thinking effort
// selected by the user in Cursor (low/medium/high/xhigh). The AOS/MOA internal
// callAdapter path reads this value and forwards it to the physical model.
func WithThinkingEffort(ctx context.Context, effort string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyThinkingEffort{}, effort)
}

// ThinkingEffortFromContext extracts the thinking effort injected by
// WithThinkingEffort. Returns empty string when absent.
func ThinkingEffortFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v := ctx.Value(ctxKeyThinkingEffort{})
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// WithMaxTokens returns a derived context carrying the max tokens selected by
// the user. Zero means "use adapter default".
func WithMaxTokens(ctx context.Context, maxTokens int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if maxTokens <= 0 {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyMaxTokens{}, maxTokens)
}

// MaxTokensFromContext extracts the max tokens injected by WithMaxTokens.
func MaxTokensFromContext(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	v := ctx.Value(ctxKeyMaxTokens{})
	if n, ok := v.(int); ok {
		return n
	}
	return 0
}

// [修复] 并发安全: 添加 sync.RWMutex 保护 map 并发读写
// Manager 管理所有虚拟模型的注册和查找。
type Manager struct {
	mu     sync.RWMutex
	models map[string]VirtualModel
}

// NewManager 创建虚拟模型管理器。
func NewManager() *Manager {
	return &Manager{
		models: make(map[string]VirtualModel),
	}
}

// [修复] 并发安全 + 空值防护: Register 加写锁，接收者 nil 检查
func (m *Manager) Register(model VirtualModel) error {
	if m == nil {
		return fmt.Errorf("virtualmodel: Manager is nil")
	}
	if model == nil {
		return fmt.Errorf("virtualmodel: model is nil")
	}
	id := strings.TrimSpace(model.ID())
	if id == "" {
		return fmt.Errorf("virtualmodel: model ID is empty")
	}
	m.mu.Lock()
	m.models[id] = model
	m.mu.Unlock()
	return nil
}

// Unregister removes a virtual model by ID. It is safe to call when the model
// is already absent, which makes config-driven enablement idempotent.
func (m *Manager) Unregister(id string) bool {
	if m == nil {
		return false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	m.mu.Lock()
	_, found := m.models[id]
	delete(m.models, id)
	m.mu.Unlock()
	return found
}

// Get 按 ID 查找虚拟模型。
// [修复] 并发安全 + 空值防护: 加读锁, 接收者 nil 检查
func (m *Manager) Get(id string) (VirtualModel, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.RLock()
	model, ok := m.models[strings.TrimSpace(id)]
	m.mu.RUnlock()
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
// [修复] 并发安全 + 空值防护: 加读锁, 接收者 nil 检查
func (m *Manager) List() []VirtualModel {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	result := make([]VirtualModel, 0, len(m.models))
	for _, model := range m.models {
		result = append(result, model)
	}
	m.mu.RUnlock()
	return result
}

// EnabledCount 返回已启用的虚拟模型数量。
// [修复] 并发安全 + 空值防护: 加读锁, 接收者 nil 检查
func (m *Manager) EnabledCount() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	count := 0
	for _, model := range m.models {
		if model.Enabled() {
			count++
		}
	}
	m.mu.RUnlock()
	return count
}
