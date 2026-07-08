// types.go 定义运行时、公用命令、事件、状态与 pending 结构。
package runtimecore

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

// SubagentModelOverrideSelection 表示父 run 对某类 subagent 的模型选择覆盖。
type SubagentModelOverrideSelection struct {
	SubagentType                  string `json:"subagent_type"`
	Selection                     string `json:"selection"`
	ModelID                       string `json:"model_id,omitempty"`
	MaxMode                       bool   `json:"max_mode,omitempty"`
	ParameterCount                int    `json:"parameter_count,omitempty"`
	BuiltInModel                  bool   `json:"built_in_model,omitempty"`
	IsVariantStringRepresentation bool   `json:"is_variant_string_representation,omitempty"`
}

// LookupSubagentModelOverride 按 Task subagent_type 查找运行期模型覆盖。
func LookupSubagentModelOverride(overrides map[string]SubagentModelOverrideSelection, subagentType string) (SubagentModelOverrideSelection, string, bool) {
	if len(overrides) == 0 {
		return SubagentModelOverrideSelection{}, "", false
	}
	for _, key := range subagentModelOverrideLookupKeys(subagentType) {
		if selection, ok := overrides[key]; ok {
			return selection, key, true
		}
	}
	return SubagentModelOverrideSelection{}, "", false
}

func subagentModelOverrideLookupKeys(subagentType string) []string {
	trimmed := strings.TrimSpace(subagentType)
	if trimmed == "" {
		return nil
	}
	keys := []string{trimmed}
	switch trimmed {
	case "generalPurpose":
		keys = append(keys, "explore")
	case "explore":
		keys = append(keys, "generalPurpose")
	case "browserUse":
		keys = append(keys, "browser-use")
	case "browser-use":
		keys = append(keys, "browserUse")
	}
	return keys
}

type RunState string

const (
	// RunStateIdle 表示空闲态，此时 session 已存在但没有活跃 run。
	RunStateIdle RunState = "IDLE"
	// RunStateRestoring 表示恢复态，此时正在装载会话状态与最小恢复信息。
	RunStateRestoring RunState = "RESTORING"
	// RunStatePreparingModelInput 表示模型输入准备态。
	RunStatePreparingModelInput RunState = "PREPARING_MODEL_INPUT"
	// RunStateStreamingModel 表示模型流消费态。
	RunStateStreamingModel RunState = "STREAMING_MODEL"
	// RunStateWaitingExec 表示执行桥等待态。
	RunStateWaitingExec RunState = "WAITING_EXEC"
	// RunStateWaitingInteraction 表示交互桥等待态。
	RunStateWaitingInteraction RunState = "WAITING_INTERACTION"
	// RunStateApplyingExternalResult 表示外部结果回写态。
	RunStateApplyingExternalResult RunState = "APPLYING_EXTERNAL_RESULT"
	// RunStateCheckpointing 表示检查点写入态。
	RunStateCheckpointing RunState = "CHECKPOINTING"
	// RunStateCompleted 表示正常完成态。
	RunStateCompleted RunState = "COMPLETED"
	// RunStateCanceled 表示取消终态。
	RunStateCanceled RunState = "CANCELED"
	// RunStateFailed 表示失败终态。
	RunStateFailed RunState = "FAILED"
)

// CommandKind 表示运行时接收的上行命令类型。
type CommandKind string

const (
	// CommandKindRunRequested 表示收到 `run_request`。
	CommandKindRunRequested CommandKind = "run_requested"
	// CommandKindPrewarmRequested 表示收到 `prewarm_request`。
	CommandKindPrewarmRequested CommandKind = "prewarm_requested"
	// CommandKindCancelRequested 表示收到 `conversation_action.cancel_action`。
	CommandKindCancelRequested CommandKind = "cancel_requested"
	// CommandKindConversationActionRecordOnly 表示收到非取消型的 `conversation_action`，当前阶段只记录不推进状态。
	CommandKindConversationActionRecordOnly CommandKind = "conversation_action_record_only"
	// CommandKindExecClientMessage 表示收到 `exec_client_message`。
	CommandKindExecClientMessage CommandKind = "exec_client_message"
	// CommandKindInteractionResponse 表示收到 `interaction_response`。
	CommandKindInteractionResponse CommandKind = "interaction_response"
	// CommandKindExecClientControlMessage 表示收到 `exec_client_control_message`，当前阶段只记录不推进状态。
	CommandKindExecClientControlMessage CommandKind = "exec_client_control_message"
	// CommandKindClientHeartbeat 表示收到客户端心跳，当前阶段只记录不推进状态。
	CommandKindClientHeartbeat CommandKind = "client_heartbeat"
	// CommandKindKVClientMessage 表示收到 `kv_client_message`，当前阶段只记录不推进状态。
	CommandKindKVClientMessage CommandKind = "kv_client_message"
)

// Command 描述一次投递到运行时协调层的上行命令。
type Command struct {
	// Kind 指定该命令的运行时语义。
	Kind CommandKind
	// IsResume 标记当前命令是否为恢复型启动。
	IsResume bool
	// ClientKind 保留协议层顶级消息种类，便于观测与调试。
	ClientKind string
	// HistoryEntry 保存协议摘要文本，供当前 MVP 的合成回复使用。
	HistoryEntry string
	// ClientMessage 保存解码后的完整上行协议消息。
	ClientMessage *agentv1.AgentClientMessage
}

// EventKind 表示一次可回放下行事件的业务类型。
type EventKind string

const (
	// EventKindRunStarted 表示新 run 已创建并开始进入恢复路径。
	EventKindRunStarted EventKind = "run_started"
	// EventKindStepStarted 表示步骤开始事件。
	EventKindStepStarted EventKind = "step_started"
	// EventKindTextDelta 表示文本增量事件。
	EventKindTextDelta EventKind = "text_delta"
	// EventKindStepCompleted 表示步骤完成事件。
	EventKindStepCompleted EventKind = "step_completed"
	// EventKindTurnEnded 表示回合结束事件。
	EventKindTurnEnded EventKind = "turn_ended"
	// EventKindCheckpoint 表示会话检查点事件。
	EventKindCheckpoint EventKind = "checkpoint"
	// EventKindCanceled 表示取消事件。
	EventKindCanceled EventKind = "canceled"
	// EventKindHeartbeat 表示服务端心跳事件。
	EventKindHeartbeat EventKind = "heartbeat"
)

// Event 表示一条可广播、可回放的下行事件记录。
type Event struct {
	// Seq 是请求维度内递增的事件序号。
	Seq int64
	// RequestID 是事件所属请求标识。
	RequestID string
	// RunID 是事件所属运行标识。
	RunID string
	// Kind 标识该事件的业务类型。
	Kind EventKind
	// Message 是要透传到 RunSSE 的协议消息体。
	Message *agentv1.AgentServerMessage
	// End 表示该事件会结束当前 SSE 读取。
	End bool
	// TerminalErrorCode 表示当前终态 SSE 需要返回的 connect error code，例如 canceled。
	TerminalErrorCode string
	// TerminalErrorMessage 表示当前终态 SSE 需要返回的错误消息。
	TerminalErrorMessage string
	// CreatedAt 是事件入库时间。
	CreatedAt time.Time
}

// RunSnapshot 表示一次 run 的最小快照信息。
type RunSnapshot struct {
	// RunID 是运行唯一标识。
	RunID string
	// RequestID 是当前 run 绑定的请求标识。
	RequestID string
	// ConversationID 是当前 run 绑定的会话标识。
	ConversationID string
	// ModelID 表示当前运行使用的模型标识。
	ModelID string
	// State 表示该 run 当前所处状态。
	State RunState
	// Mode 表示该 run 当前使用的会话模式。
	Mode agentv1.AgentMode
	// Version 是运行时版本号，便于后续扩展乐观更新。
	Version int64
	// StartedAt 记录 run 启动时间。
	StartedAt time.Time
	// UpdatedAt 记录 run 最近一次状态更新时间。
	UpdatedAt time.Time
	// CurrentUserMessageText 保存当前 turn 的用户输入文本，直到本 turn 提交进 `turns`。
	CurrentUserMessageText string
	// CustomSystemPrompt 保存当前 run 附带的自定义系统提示词。
	CustomSystemPrompt string
	// RequestContextPayload 保存当前 run 的 request_context proto 序列化结果。
	RequestContextPayload []byte
	// IsPrewarm 标记当前 run 是否由 `prewarm_request` 触发。
	IsPrewarm bool
}

// PendingAssistantOutput 表示尚未收口的一条 assistant 输出记录。
type PendingAssistantOutput struct {
	// RawMessage 保存原始序列化 assistant message。
	RawMessage string
	// Role 表示该记录的 role，当前常见值为 assistant。
	Role string
	// ContentKinds 记录内容块类型顺序，例如 text 或 tool-call。
	ContentKinds []string
	// ToolCallIDs 记录该输出中出现的全部 tool_call_id。
	ToolCallIDs []string
	// ToolNames 记录该输出中出现的全部工具名称。
	ToolNames []string
	// TextPreview 保存文本块的简要摘要。
	TextPreview string
}

// PendingExec 表示一条尚未收口的执行桥记录。
type PendingExec struct {
	// MessageID 是打开该执行桥时下发给客户端的桥消息编号。
	MessageID uint32
	// ExecID 是执行桥唯一标识。
	ExecID string
	// ProviderPass 表示创建该执行桥时所属的 provider pass。
	ProviderPass int
	// ModelCallID 是触发该执行桥的模型调用标识。
	ModelCallID string
	// ToolCallID 是与该执行桥关联的工具调用标识。
	ToolCallID string
	// ArgsJSON 保存打开该执行桥时的原始参数 JSON，便于恢复 completed ToolCall。
	ArgsJSON []byte
	// ReasoningContent 保存触发该工具调用时的 thinking 文本，供 checkpoint/replay 续跑复用。
	ReasoningContent string
	// ReasoningSignature 保存 provider 对当前 thinking 文本签发的签名。
	ReasoningSignature string
	// ReasoningSignatureSource 保存 reasoning signature 的 provider 语义来源。
	ReasoningSignatureSource string
	// ExecKind 描述执行桥类型，例如 read、write、shellStream。
	ExecKind string
	// StreamState 描述当前流式执行桥的阶段。
	StreamState string
	// OpenedAt 表示执行桥请求发出的时间。
	OpenedAt time.Time
	// FirstChunkAt 表示 shellStream 首个输出块时间。
	FirstChunkAt time.Time
	// ChunkCount 表示 shellStream 已接收的输出块数量。
	ChunkCount int64
	// LastShellActivityAt 记录最近一次 shell 相关上行事件时间，包括输出、start、heartbeat 和 close。
	LastShellActivityAt time.Time
	// LastShellHeartbeatAt 记录最近一次 shell heartbeat 到达时间。
	LastShellHeartbeatAt time.Time
	// ShellForegroundDeadline 表示前台 shell 预计最晚应收到终态的时间点。
	ShellForegroundDeadline time.Time
	// ShellRecoveryScheduled 标记是否已经为该 shell 安排了异常收口协程。
	ShellRecoveryScheduled bool
	// StdoutBuffer 保存当前 shell 已累计的 stdout 文本。
	StdoutBuffer string
	// StderrBuffer 保存当前 shell 已累计的 stderr 文本。
	StderrBuffer string
	// ArtifactPath 保存该 exec 对应的原始桥接工件路径。
	ArtifactPath string
}

// PendingInteraction 表示一条尚未收口的交互桥记录。
type PendingInteraction struct {
	// InteractionID 是交互桥唯一标识。
	InteractionID string
	// ProviderPass 表示创建该交互桥时所属的 provider pass。
	ProviderPass int
	// ModelCallID 是触发该交互桥的模型调用标识。
	ModelCallID string
	// ToolCallID 是与该交互桥关联的工具调用标识。
	ToolCallID string
	// ArgsJSON 保存打开该交互桥时的原始参数 JSON，便于结果回写时恢复结构化状态。
	ArgsJSON []byte
	// ReasoningContent 保存触发该工具调用时的 thinking 文本，供 checkpoint/replay 续跑复用。
	ReasoningContent string
	// ReasoningSignature 保存 provider 对当前 thinking 文本签发的签名。
	ReasoningSignature string
	// ReasoningSignatureSource 保存 reasoning signature 的 provider 语义来源。
	ReasoningSignatureSource string
	// InteractionKind 描述交互类型，例如 ask_question、create_plan。
	InteractionKind string
	// OpenedAt 表示交互请求发出的时间。
	OpenedAt time.Time
	// ArtifactPath 保存该 interaction 对应的原始桥接工件路径。
	ArtifactPath string
}

// ActiveStep 表示当前正在推进、尚未收口的 step 元数据。
type ActiveStep struct {
	// StepID 是当前 step 唯一标识。
	StepID uint64
	// ModelCallID 是当前 step 绑定的模型调用标识。
	ModelCallID string
	// StartedAt 是当前 step 的开始时间。
	StartedAt time.Time
	// InputTokens 保存当前 step 已知的输入 token 数。
	InputTokens int64
	// OutputTokens 保存当前 step 已知的输出 token 数。
	OutputTokens int64
}

// ExternalResultSummary 表示 APPLYING_EXTERNAL_RESULT 后继续下一轮编译所需的最小上下文。
type ExternalResultSummary struct {
	// Source 表示结果来源，例如 exec 或 interaction。
	Source string
	// ToolName 表示对应工具名或交互名。
	ToolName string
	// Payload 表示可直接注入 prompt 的结果摘要。
	Payload string
}

// ToolInvocation 表示一次模型产出的工具调用意图。
type ToolInvocation struct {
	// CallID 是模型层工具调用标识。
	CallID string
	// ToolName 表示工具名称，例如 Read、Write、AskQuestion。
	ToolName string
	// ArgsJSON 保存工具参数原始 JSON。
	ArgsJSON []byte
	// ReasoningContent 保存当前工具调用前伴随的 thinking 文本。
	ReasoningContent string
	// ReasoningSignature 保存 provider 对当前 thinking 文本签发的签名。
	ReasoningSignature string
	// ReasoningSignatureSource 保存 reasoning signature 的 provider 语义来源。
	ReasoningSignatureSource string
	// ReasoningProviderItemID 保存 provider 原始 reasoning output item id。
	ReasoningProviderItemID string
	// ReasoningProviderStatus 保存 provider 原始 reasoning output item status。
	ReasoningProviderStatus string
	// ReasoningProviderSummary 保存 provider 原始 reasoning output item summary。
	ReasoningProviderSummary json.RawMessage
	// ProviderItemID 保存 provider 原始 tool/function output item id。
	ProviderItemID string
	// ProviderCallID 保存 provider 原始 tool/function call id。
	ProviderCallID string
	// ProviderStatus 保存 provider 原始 tool/function output item status。
	ProviderStatus string
	// ModelCallID 表示本轮模型调用标识。
	ModelCallID string
}

// NormalizeSupportedMode 规范化并校验当前支持的会话 mode。
//
// 当前默认口径：
// 1. 未显式携带 mode 或值为 `AGENT_MODE_UNSPECIFIED` 时，按 `AGENT_MODE_AGENT` 处理；
// 2. 仅允许 `AGENT_MODE_AGENT`、`AGENT_MODE_ASK`、`AGENT_MODE_PLAN`、`AGENT_MODE_DEBUG`、`AGENT_MODE_MULTITASK`；
// 3. 其他 mode 一律报错，不允许静默回退。
func NormalizeSupportedMode(mode agentv1.AgentMode) (agentv1.AgentMode, error) {
	switch mode {
	case agentv1.AgentMode_AGENT_MODE_UNSPECIFIED:
		return agentv1.AgentMode_AGENT_MODE_AGENT, nil
	case agentv1.AgentMode_AGENT_MODE_AGENT,
		agentv1.AgentMode_AGENT_MODE_ASK,
		agentv1.AgentMode_AGENT_MODE_PLAN,
		agentv1.AgentMode_AGENT_MODE_DEBUG,
		agentv1.AgentMode_AGENT_MODE_MULTITASK:
		return mode, nil
	default:
		return agentv1.AgentMode_AGENT_MODE_UNSPECIFIED, fmt.Errorf("unsupported mode: %s", mode.String())
	}
}

// CloneToolCallMap 深拷贝 tool_call 结果映射，避免共享 proto 指针。
func CloneToolCallMap(items map[string]*agentv1.ToolCall) map[string]*agentv1.ToolCall {
	if len(items) == 0 {
		return make(map[string]*agentv1.ToolCall)
	}

	cloned := make(map[string]*agentv1.ToolCall, len(items))
	for key, value := range items {
		if value == nil {
			cloned[key] = nil
			continue
		}
		typed, ok := proto.Clone(value).(*agentv1.ToolCall)
		if !ok {
			cloned[key] = nil
			continue
		}
		cloned[key] = typed
	}
	return cloned
}

// IsCurrentlySupportedTool 判断当前 Phase 5 稳定化版本是否真正支持该工具。
//
// 当前规则：
// 1. 只返回 runtime/loop 当前已经具备完整推进链路的能力；
// 2. 结果用于限制实际对模型暴露的工具集合，避免模型调用未实现能力后把整轮 run 直接打失败；
// 3. 必须保持最小闭环优先，而不是优先暴露抓包里存在但服务端尚未支持的能力。
func IsCurrentlySupportedTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "Read", "Write", "PatchEdit", "Delete", "Shell", "AwaitShell", "WriteShellStdin", "ForceBackgroundShell",
		"Glob", "Grep", "ReadLints",
		"AskQuestion", "CreatePlan", "SwitchMode", "WebSearch", "WebFetch",
		"TodoWrite", "Task",
		"CallMcpTool", "FetchMcpResource":
		return true
	default:
		return false
	}
}
