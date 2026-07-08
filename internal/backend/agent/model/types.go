// types.go 定义模型适配层的统一请求、事件与路由接口。
package modeladapter

import (
	"context"
	"encoding/json"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

const (
	// ReasoningSignatureSourceAnthropic 表示 signature 来自 Anthropic thinking signature。
	ReasoningSignatureSourceAnthropic = "anthropic"
	// ReasoningSignatureSourceOpenAIResponses 表示 signature 来自 OpenAI Responses encrypted reasoning content。
	ReasoningSignatureSourceOpenAIResponses = "openai_responses"
)

// Message 表示模型适配层统一使用的消息结构。
type Message struct {
	// Role 表示消息角色。
	Role string `json:"role"`
	// Content 表示消息文本内容。
	Content string `json:"content"`
	// ContentParts 表示消息中的结构化内容块，例如文本或图片。
	ContentParts []ContentPart `json:"content_parts,omitempty"`
	// ReasoningContent 表示推理内容（用于支持 reasoning 的模型）。
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// ReasoningSignature 表示 provider 对推理内容签发的签名（如 Anthropic thinking signature）。
	ReasoningSignature string `json:"reasoning_signature,omitempty"`
	// ReasoningSignatureSource 表示 reasoning signature 的 provider 语义来源。
	ReasoningSignatureSource string `json:"reasoning_signature_source,omitempty"`
	// OpenAIResponsesReasoningID 保存 Responses reasoning output item 的原始 id。
	OpenAIResponsesReasoningID string `json:"openai_responses_reasoning_id,omitempty"`
	// OpenAIResponsesReasoningStatus 保存 Responses reasoning output item 的原始 status。
	OpenAIResponsesReasoningStatus string `json:"openai_responses_reasoning_status,omitempty"`
	// OpenAIResponsesReasoningSummary 保存 Responses reasoning output item 的原始 summary。
	OpenAIResponsesReasoningSummary json.RawMessage `json:"openai_responses_reasoning_summary,omitempty"`
	// ToolCalls 表示 assistant 发起的函数调用。
	ToolCalls []ToolCallDescriptor `json:"tool_calls,omitempty"`
	// ToolCallID 表示 tool role 关联的调用 id。
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Name 表示 tool role 的工具名。
	Name string `json:"name,omitempty"`
}

type ToolCallDescriptor struct {
	ID                    string                `json:"id"`
	Index                 int                   `json:"index,omitempty"`
	Type                  string                `json:"type"`
	Function              ToolCallFunctionShape `json:"function"`
	OpenAIResponsesID     string                `json:"openai_responses_id,omitempty"`
	OpenAIResponsesCallID string                `json:"openai_responses_call_id,omitempty"`
	OpenAIResponsesStatus string                `json:"openai_responses_status,omitempty"`
}

type ToolCallFunctionShape struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// StreamRequest 表示一次统一的模型流请求。
type StreamRequest struct {
	// RequestID 表示当前模型调用所属 request。
	RequestID string
	// RunID 表示当前模型调用所属 run。
	RunID string
	// ModelCallID 表示当前模型调用标识。
	ModelCallID string
	// ConversationID 表示当前模型调用所属会话，用于稳定 provider 侧 prompt cache 路由。
	ConversationID string
	// Mode 表示当前运行模式。
	Mode agentv1.AgentMode
	// ModelID 表示当前模型标识。
	ModelID string
	// ThinkingEffort 表示客户端在本轮运行时选择的思考强度覆盖。
	ThinkingEffort string
	// Provider 表示目标 provider 类型，例如 openai 或 anthropic。
	Provider string
	// BaseURL 表示请求应发送到的 provider 基础地址。
	BaseURL string
	// APIKey 表示 provider 鉴权凭据。
	APIKey string
	// ProviderModelID 表示 provider 侧真实模型标识。
	ProviderModelID string
	// ResolvedChannelID 表示本次请求实际命中的 adapter 渠道 ID。
	ResolvedChannelID string
	// ResolvedChannelName 表示本次请求实际命中的 adapter 展示名。
	ResolvedChannelName string
	// ResolvedContextWindowTokens 表示本次请求实际命中的 adapter 上下文窗口。
	ResolvedContextWindowTokens int
	// ReasoningEffort 表示 OpenAI 兼容 provider 的推理强度。
	ReasoningEffort string
	// OpenAIEndpoint 表示 OpenAI 兼容 provider 使用的 API 端点。
	OpenAIEndpoint string
	// OpenAIExtraParamsEnabled 表示是否启用 OpenAI 额外请求参数。
	OpenAIExtraParamsEnabled bool
	// OpenAIExtraParamsJSON 表示 OpenAI 额外请求参数 JSON 对象。
	OpenAIExtraParamsJSON string
	// CustomHeadersEnabled 表示是否启用自定义请求头。
	CustomHeadersEnabled bool
	// CustomHeadersJSON 表示自定义请求头 JSON 对象。
	CustomHeadersJSON string
	// AnthropicExtraParamsEnabled 表示是否启用 Anthropic 额外请求参数。
	AnthropicExtraParamsEnabled bool
	// AnthropicExtraParamsJSON 表示 Anthropic 额外请求参数 JSON 对象。
	AnthropicExtraParamsJSON string
	// AnthropicMaxTokens 表示 Anthropic 兼容 provider 的 max_tokens。
	AnthropicMaxTokens int
	// AnthropicThinkingEffort 表示 Anthropic adaptive thinking 的 output_config.effort。
	AnthropicThinkingEffort string
	// ThinkingBudgetTokens 表示 Anthropic thinking 预算。
	ThinkingBudgetTokens int
	// Messages 表示按顺序排列的消息列表。
	Messages []Message
	// StableMessageCount 表示 messages 中可作为稳定缓存前缀的 provider-visible 消息数量。
	StableMessageCount int
	// Tools 表示原始工具描述 JSON 列表。
	Tools []json.RawMessage
	// MaxTokens 表示本轮最大输出 token 数。
	MaxTokens int
	// Stream 表示当前请求必须使用流式。
	Stream bool
	// RequestKnobs 保存本轮请求的附加参数摘要。
	RequestKnobs map[string]any
	// CompileSummary 保存当前 prompt 编译摘要。
	CompileSummary string
	// Observer 负责写入 request-scoped LLM 工件。
	Observer LLMArtifactObserver
	// ArtifactPaths 用于由 adapter 回填工件路径。
	ArtifactPaths *LLMArtifactPaths
	// RequestBodyOverride 表示直接复用的 provider 原始请求体；设置后由 adapter 原样发送。
	RequestBodyOverride map[string]any
	// ProviderStreamIdleTimeout 表示 provider 流式响应无有效内容时的空闲超时。
	ProviderStreamIdleTimeout time.Duration
}

// LLMArtifactPaths 表示一次模型调用相关工件路径。
type LLMArtifactPaths struct {
	RequestPath  string
	ResponsePath string
	SummaryPath  string
}

// LLMArtifactObserver 定义模型调用原始工件写入接口。
type LLMArtifactObserver interface {
	RecordLLMRequest(requestID string, runID string, modelCallID string, payload map[string]any) (string, error)
	AppendLLMResponseChunk(requestID string, runID string, modelCallID string, chunk string) (string, error)
	RecordLLMSummary(requestID string, runID string, modelCallID string, payload map[string]any) (string, error)
}

// ModelEventKind 表示统一模型事件类型。
type ModelEventKind string

const (
	// ModelEventKindTextDelta 表示文本增量事件。
	ModelEventKindTextDelta ModelEventKind = "text_delta"
	// ModelEventKindThinkingDelta 表示思考增量事件。
	ModelEventKindThinkingDelta ModelEventKind = "thinking_delta"
	// ModelEventKindThinkingCompleted 表示思考结束事件。
	ModelEventKindThinkingCompleted ModelEventKind = "thinking_completed"
	// ModelEventKindPartialToolCall 表示工具调用已开始，但参数仍在流式生成中。
	ModelEventKindPartialToolCall ModelEventKind = "partial_tool_call"
	// ModelEventKindToolCallDelta 表示工具调用参数或输出的流式增量。
	ModelEventKindToolCallDelta ModelEventKind = "tool_call_delta"
	// ModelEventKindToolLikeCompleted 表示工具意图已完整收口。
	ModelEventKindToolLikeCompleted ModelEventKind = "tool_like_completed"
	// ModelEventKindTurnFinished 表示当前模型回合结束。
	ModelEventKindTurnFinished ModelEventKind = "turn_finished"
	// ModelEventKindProviderError 表示 provider 侧返回错误。
	ModelEventKindProviderError ModelEventKind = "provider_error"
)

// ModelEvent 表示一条统一模型事件。
type ModelEvent struct {
	// Kind 表示事件类型。
	Kind ModelEventKind
	// OccurredAt 表示当前 provider 事件发生时间。
	OccurredAt time.Time
	// Provider 表示当前事件所属 provider。
	Provider string
	// Model 表示当前事件所属模型标识。
	Model string
	// Text 表示文本增量。
	Text string
	// ThinkingStyle 表示思考样式。
	ThinkingStyle agentv1.ThinkingStyle
	// ThinkingDurationMS 表示思考持续时长。
	ThinkingDurationMS int32
	// ThinkingSignature 表示 provider 返回的思考签名（如 Anthropic signature_delta）。
	ThinkingSignature string
	// ThinkingSignatureSource 表示思考签名的 provider 语义来源。
	ThinkingSignatureSource string
	// ProviderItemID 保存 provider 原始 output item id，用于 stateless Responses replay。
	ProviderItemID string
	// ProviderStatus 保存 provider 原始 output item status，用于 stateless Responses replay。
	ProviderStatus string
	// ProviderSummary 保存 provider 原始 output item summary，用于 stateless Responses replay。
	ProviderSummary json.RawMessage
	// ProviderCallID 保存 provider 原始 tool/function call id，用于 stateless Responses replay。
	ProviderCallID string
	// ToolCallID 表示当前 partial/delta 对应的工具调用标识。
	ToolCallID string
	// ToolCall 保存 partial tool call 当前可公开的结构化快照。
	ToolCall *agentv1.ToolCall
	// ToolCallDelta 保存与当前工具调用相关的流式增量。
	ToolCallDelta *agentv1.ToolCallDelta
	// ArgsTextDelta 保存原始工具参数文本增量，供兼容层透传。
	ArgsTextDelta string
	// InputTokens 表示当前已知的输入 token 数。
	InputTokens int64
	// OutputTokens 表示当前已知的输出 token 数。
	OutputTokens int64
	// CacheReadTokens 表示当前已知的 cache read token 数。
	CacheReadTokens int64
	// CacheWriteTokens 表示当前已知的 cache write token 数。
	CacheWriteTokens int64
	// UsagePresent 表示 provider 本次流里实际返回过 usage 对象。
	UsagePresent bool
	// CacheReadPresent 表示 provider 明确返回了 cache read token 字段。
	CacheReadPresent bool
	// CacheWritePresent 表示 provider 明确返回了 cache write token 字段。
	CacheWritePresent bool
	// ToolInvocation 表示完成收口的工具调用意图。
	ToolInvocation *runtimecore.ToolInvocation
	// FinishReason 表示回合结束原因。
	FinishReason string
	// Err 表示 provider 错误。
	Err error
}

// ModelAdapter 定义具体 provider 适配器接口。
type ModelAdapter interface {
	// Stream 按流式方式发送请求，并持续产出统一模型事件。
	Stream(ctx context.Context, req StreamRequest, sink func(ModelEvent) error) error
}

// ModelAdapterRouter 定义 provider 路由接口。
type ModelAdapterRouter interface {
	// Stream 根据模型标识选择底层 provider 适配器。
	Stream(ctx context.Context, req StreamRequest, sink func(ModelEvent) error) error
}
