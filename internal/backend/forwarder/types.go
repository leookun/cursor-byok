// types.go 定义 forwarder 的核心数据结构与最小接口边界。
package forwarder

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
)

type ConversationFile struct {
	SchemaVersion                   int                                   `json:"schema_version,omitempty"`
	ConversationID                  string                                `json:"conversation_id"`
	RootConversationID              string                                `json:"root_conversation_id"`
	ParentConversationID            string                                `json:"parent_conversation_id"`
	ParentToolCallID                string                                `json:"parent_tool_call_id"`
	SubagentTypeName                string                                `json:"subagent_type_name,omitempty"`
	Mode                            string                                `json:"mode"`
	ContextVersion                  int64                                 `json:"context_version,omitempty"`
	CurrentLoopID                   string                                `json:"current_loop_id,omitempty"`
	CurrentLoopStatus               string                                `json:"current_loop_status,omitempty"`
	CurrentRequestID                string                                `json:"current_request_id,omitempty"`
	CurrentTurnSeq                  int64                                 `json:"current_turn_seq,omitempty"`
	TokenDetailsUsedTokens          uint32                                `json:"token_details_used_tokens,omitempty"`
	TokenDetailsMaxTokens           uint32                                `json:"token_details_max_tokens,omitempty"`
	AutoCompactionPending           bool                                  `json:"auto_compaction_pending,omitempty"`
	AutoCompactionPromptTokens      int64                                 `json:"auto_compaction_prompt_tokens,omitempty"`
	AutoCompactionReserveTokens     int64                                 `json:"auto_compaction_reserve_tokens,omitempty"`
	AutoCompactionTriggeredAt       string                                `json:"auto_compaction_triggered_at,omitempty"`
	AutoCompactionSourceModelCallID string                                `json:"auto_compaction_source_model_call_id,omitempty"`
	CurrentPlanText                 string                                `json:"current_plan_text,omitempty"`
	CurrentPlans                    map[string]*agentv1.PlanRegistryEntry `json:"current_plans,omitempty"`
	CurrentTodos                    []*agentv1.TodoItem                   `json:"current_todos,omitempty"`
	LatestRequestPrefix             *ConversationRequestPrefix            `json:"latest_request_prefix,omitempty"`
	LastProviderCall                *ConversationProviderCall             `json:"last_provider_call,omitempty"`
	CreatedAt                       time.Time                             `json:"created_at"`
	UpdatedAt                       time.Time                             `json:"updated_at"`
	NextTurnSeq                     int64                                 `json:"next_turn_seq"`
	NextEntrySeq                    int64                                 `json:"next_entry_seq"`
	Entries                         []HistoryEntry                        `json:"entries,omitempty"`
}

type ConversationRequestPrefix struct {
	RequestID               string    `json:"request_id,omitempty"`
	ModelCallID             string    `json:"model_call_id,omitempty"`
	Provider                string    `json:"provider,omitempty"`
	OpenAIEndpoint          string    `json:"openai_endpoint,omitempty"`
	Model                   string    `json:"model,omitempty"`
	PromptTokensTotal       int64     `json:"prompt_tokens_total,omitempty"`
	ReplayMessageCount      int       `json:"replay_message_count,omitempty"`
	CanonicalBodyHash       string    `json:"canonical_body_hash,omitempty"`
	FrontierHash            string    `json:"frontier_hash,omitempty"`
	FrontierPath            string    `json:"frontier_path,omitempty"`
	BreakpointCount         int       `json:"breakpoint_count,omitempty"`
	ExpectedCacheRead       bool      `json:"expected_cache_read,omitempty"`
	PreviousFrontierMatched bool      `json:"previous_frontier_matched,omitempty"`
	UpdatedAt               time.Time `json:"updated_at,omitempty"`
}

type ConversationProviderCall struct {
	RequestID   string    `json:"request_id,omitempty"`
	ModelCallID string    `json:"model_call_id,omitempty"`
	Provider    string    `json:"provider,omitempty"`
	Model       string    `json:"model,omitempty"`
	Status      string    `json:"status,omitempty"`
	ErrorText   string    `json:"error_text,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

type HistoryEntry struct {
	Seq              int64           `json:"seq"`
	TurnSeq          int64           `json:"turn_seq"`
	RequestID        string          `json:"request_id,omitempty"`
	Role             string          `json:"role"`
	Kind             string          `json:"kind"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	ParentToolCallID string          `json:"parent_tool_call_id,omitempty"`
	Payload          json.RawMessage `json:"payload"`
	CreatedAt        time.Time       `json:"created_at"`
}

type ConversationSummary struct {
	ConversationID string    `json:"conversation_id"`
	Mode           string    `json:"mode"`
	EntriesCount   int       `json:"entries_count"`
	NextTurnSeq    int64     `json:"next_turn_seq"`
	NextEntrySeq   int64     `json:"next_entry_seq"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type StreamStatus string

const (
	StreamStatusCreated   StreamStatus = "created"
	StreamStatusStreaming StreamStatus = "streaming"
	StreamStatusCompleted StreamStatus = "completed"
	StreamStatusCanceled  StreamStatus = "canceled"
	StreamStatusFailed    StreamStatus = "failed"
)

type StreamEvent struct {
	Message              *agentv1.AgentServerMessage
	End                  bool
	TerminalErrorCode    string
	TerminalErrorMessage string
}

type StreamSubscriber struct {
	Signal chan struct{}
}

type ActiveStream struct {
	mu sync.Mutex

	RequestID              string
	ConversationID         string
	TurnSeq                int64
	ModelID                string
	ModelName              string
	Mode                   agentv1.AgentMode
	LatestUserText         string
	Status                 StreamStatus
	ThinkingEffort         string
	SubagentModelOverrides map[string]runtimecore.SubagentModelOverrideSelection

	CurrentModelCallID                          string
	ProviderActive                              bool
	ProviderCancel                              func()
	ProviderPassCount                           int
	ToolInvocationCount                         int
	ActorMailbox                                chan streamCommandEnvelope
	ActorDone                                   chan struct{}
	Phase                                       TurnPhase
	PendingProviderAction                       providerAction
	PendingProviderCompletion                   *pendingTurnCompletion
	CurrentProviderToken                        uint64
	CurrentCompactionToken                      uint64
	TimerTokens                                 map[string]uint64
	ProviderAccumulatedText                     string
	ProviderAccumulatedReasoning                string
	ProviderAccumulatedReasoningSignature       string
	ProviderAccumulatedReasoningSignatureSource string
	ProviderAccumulatedReasoningItemID          string
	ProviderAccumulatedReasoningStatus          string
	ProviderAccumulatedReasoningSummary         json.RawMessage
	ProviderSyntheticThinkingStartedAt          time.Time
	ProviderSyntheticThinkingPublished          bool
	ProviderFinishReason                        string
	ProviderUsage                               turnUsageSnapshot
	ProviderTerminalToolInvocation              bool
	PendingCompaction                           *PendingCompaction

	Backlog                     []StreamEvent
	Subscribers                 map[string]*StreamSubscriber
	CheckpointConversation      *ConversationFile
	PendingExecs                map[string]runtimecore.PendingExec
	PendingInteractions         map[string]runtimecore.PendingInteraction
	PartialToolCallIDs          map[string]struct{}
	PatchEditQueues             map[string][]queuedPatchEditOperation
	MCPToolServers              map[string]string
	WorkspacePaths              []string
	TerminalsFolder             string
	RequestFileContents         map[string]string
	RecentCompletedExecs        map[uint32]time.Time
	BackgroundShells            map[string]*BackgroundShellState
	BackgroundShellsByMessageID map[uint32]string
	BackgroundShellsByExecID    map[string]string
	BackgroundShellActions      map[string]time.Time
	TerminalCleanupTimer        *time.Timer
	TerminalCleanupSeq          atomic.Uint64

	CreatedAt time.Time
	UpdatedAt time.Time
}

type BackgroundShellState struct {
	ShellID            string
	Command            string
	WorkingDirectory   string
	PID                *uint32
	OriginalToolCallID string
	OriginalExecID     string
	OriginalMessageID  uint32
	ModelCallID        string
	ArgsJSON           []byte
	Status             string
	ExitCode           *int32
	StdoutBuffer       string
	StderrBuffer       string
	AwaitStdoutOffset  int
	AwaitStderrOffset  int
	CreatedAt          time.Time
	LastActivityAt     time.Time
	CompletedAt        time.Time
	StreamClosed       bool
}

type pendingTurnCompletion struct {
	ConversationID string
	RequestID      string
	TurnSeq        int64
	ModelCallID    string
	ProviderPass   int
	Usage          turnUsageSnapshot
	Disposition    pendingCompletionDisposition
}

type PendingCompaction struct {
	Trigger                   string
	ContextTokens             int64
	ContextWindowSize         int64
	ContextUsagePercent       float64
	ReserveTokens             int64
	MessageCount              int32
	MessagesToCompact         int32
	CompactTurnCount          int32
	IsFirstCompaction         bool
	ExistingSummary           string
	CompactedTurns            []compactedTurnSummary
	ManualInstruction         string
	RequestSource             string
	CurrentTurnSeq            int64
	CurrentRequestID          string
	CurrentUserText           string
	PreserveCurrentTurnInputs bool
	HookMessage               string
	SummaryModelCallID        string
	StartedAt                 time.Time
}

type ProviderRequest struct {
	RequestID           string
	ConversationID      string
	RunID               string
	ModelCallID         string
	ModelID             string
	Mode                agentv1.AgentMode
	ThinkingEffort      string
	Messages            []modeladapter.Message
	StableMessageCount  int
	Tools               []json.RawMessage
	MaxTokens           int
	RequestKnobs        map[string]any
	CompileSummary      string
	Observer            modeladapter.LLMArtifactObserver
	ArtifactPaths       *modeladapter.LLMArtifactPaths
	RequestBodyOverride map[string]any
}

type ProviderGateway interface {
	StartStream(ctx context.Context, req ProviderRequest, sink func(modeladapter.ModelEvent) error) error
}

type ToolCatalog interface {
	Load(mode agentv1.AgentMode, subagentTypeName string) ([]json.RawMessage, []string, error)
}

type PromptReminders struct {
	SystemParts    []string
	TailMessages   []modeladapter.Message
	PromptContexts []PromptContextMessage
}

type ReminderInjector interface {
	Inject(mode agentv1.AgentMode, conversation *ConversationFile, replayMessages []modeladapter.Message, latestUserText string, toolNames []string) PromptReminders
}

type PromptContextMessage struct {
	Source      string
	Message     modeladapter.Message
	ContentHash string
	Persist     bool
}

type CompiledConversation struct {
	Mode               agentv1.AgentMode
	Messages           []modeladapter.Message
	StableMessageCount int
	Tools              []json.RawMessage
	CompileSummary     string
}

type ToolRequestKind string

const (
	ToolRequestExec        ToolRequestKind = "exec"
	ToolRequestInteraction ToolRequestKind = "interaction"
)

// providerTerminalError 表示底层 LLM/provider 返回的真实错误。
type providerTerminalError struct {
	cause error
}

// Error 返回 provider 错误的字符串形式。
func (err providerTerminalError) Error() string {
	if err.cause == nil {
		return "provider error"
	}
	return err.cause.Error()
}

// Unwrap 允许调用方继续取到底层原始错误。
func (err providerTerminalError) Unwrap() error {
	return err.cause
}

type toolResultEntryPayload struct {
	ToolCallID               string          `json:"tool_call_id"`
	ToolName                 string          `json:"tool_name"`
	Arguments                string          `json:"arguments,omitempty"`
	ResultText               string          `json:"result_text,omitempty"`
	ReasoningContent         string          `json:"reasoning_content,omitempty"`
	ReasoningSignature       string          `json:"reasoning_signature,omitempty"`
	ReasoningSignatureSource string          `json:"reasoning_signature_source,omitempty"`
	ReasoningItemID          string          `json:"reasoning_item_id,omitempty"`
	ReasoningStatus          string          `json:"reasoning_status,omitempty"`
	ReasoningSummary         json.RawMessage `json:"reasoning_summary,omitempty"`
	ProviderItemID           string          `json:"provider_item_id,omitempty"`
	ProviderCallID           string          `json:"provider_call_id,omitempty"`
	ProviderStatus           string          `json:"provider_status,omitempty"`
	ToolCall                 json.RawMessage `json:"tool_call,omitempty"`
}

type toolCallEntryPayload struct {
	ToolCallID               string          `json:"tool_call_id"`
	ToolName                 string          `json:"tool_name"`
	ReasoningContent         string          `json:"reasoning_content,omitempty"`
	ReasoningSignature       string          `json:"reasoning_signature,omitempty"`
	ReasoningSignatureSource string          `json:"reasoning_signature_source,omitempty"`
	ReasoningItemID          string          `json:"reasoning_item_id,omitempty"`
	ReasoningStatus          string          `json:"reasoning_status,omitempty"`
	ReasoningSummary         json.RawMessage `json:"reasoning_summary,omitempty"`
	ProviderItemID           string          `json:"provider_item_id,omitempty"`
	ProviderCallID           string          `json:"provider_call_id,omitempty"`
	ProviderStatus           string          `json:"provider_status,omitempty"`
	ToolCall                 json.RawMessage `json:"tool_call"`
}

type assistantTextPayload struct {
	Text                     string          `json:"text"`
	ReasoningContent         string          `json:"reasoning_content,omitempty"`
	ReasoningSignature       string          `json:"reasoning_signature,omitempty"`
	ReasoningSignatureSource string          `json:"reasoning_signature_source,omitempty"`
	ReasoningItemID          string          `json:"reasoning_item_id,omitempty"`
	ReasoningStatus          string          `json:"reasoning_status,omitempty"`
	ReasoningSummary         json.RawMessage `json:"reasoning_summary,omitempty"`
}

type metadataPayload struct {
	Type  string         `json:"type"`
	Value map[string]any `json:"value,omitempty"`
}

type promptContextEntryPayload struct {
	Source      string `json:"source"`
	Role        string `json:"role"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash,omitempty"`
}

type modelMessageEntryPayload struct {
	Message modeladapter.Message `json:"message"`
}

type runtimeStateEntryPayload struct {
	PlanText string                                `json:"plan_text,omitempty"`
	Plans    map[string]*agentv1.PlanRegistryEntry `json:"plans,omitempty"`
	Todos    []*agentv1.TodoItem                   `json:"todos,omitempty"`
}

type compactionSummaryEntryPayload struct {
	Summary                   string `json:"summary"`
	Trigger                   string `json:"trigger,omitempty"`
	CurrentTurnSeq            int64  `json:"current_turn_seq,omitempty"`
	CurrentRequestID          string `json:"current_request_id,omitempty"`
	CompactTurnCount          int32  `json:"compact_turn_count,omitempty"`
	MessagesToCompact         int32  `json:"messages_to_compact,omitempty"`
	PreserveCurrentTurnInputs bool   `json:"preserve_current_turn_inputs,omitempty"`
}

type ModeSource string

const (
	ModeSourceUnknown           ModeSource = ""
	ModeSourceUserMessage       ModeSource = "user_message"
	ModeSourceExecutePlanAction ModeSource = "execute_plan_action"
	ModeSourceConversationState ModeSource = "conversation_state"
	ModeSourceSwitchModeTool    ModeSource = "switch_mode_tool"
)

type InboundIntent struct {
	Kind                     string
	RequestID                string
	ConversationID           string
	ModelID                  string
	ModelName                string
	ThinkingEffort           string
	Mode                     agentv1.AgentMode
	HasExplicitMode          bool
	ModeSource               ModeSource
	StartsRun                bool
	SubagentTypeName         string
	SubagentModelOverrides   map[string]runtimecore.SubagentModelOverrideSelection
	ConversationState        *agentv1.ConversationStateStructure
	UserMessage              *agentv1.UserMessage
	RequestContext           *agentv1.RequestContext
	ClientMessage            *agentv1.AgentClientMessage
	ExecClientMessage        *agentv1.ExecClientMessage
	ExecClientControlMessage *agentv1.ExecClientControlMessage
	InteractionResponse      *agentv1.InteractionResponse
	KVClientMessage          *agentv1.KvClientMessage
	CancelReason             string
	IgnoredReason            string
	Prewarm                  bool
}

// normalizeMode 对外部传入的 mode 做最小归一化，但不再静默降级。
func normalizeMode(mode agentv1.AgentMode) agentv1.AgentMode {
	return mode
}

func isSupportedActiveMode(mode agentv1.AgentMode) bool {
	switch normalizeMode(mode) {
	case agentv1.AgentMode_AGENT_MODE_AGENT,
		agentv1.AgentMode_AGENT_MODE_ASK,
		agentv1.AgentMode_AGENT_MODE_PLAN,
		agentv1.AgentMode_AGENT_MODE_DEBUG,
		agentv1.AgentMode_AGENT_MODE_MULTITASK:
		return true
	default:
		return false
	}
}

func validateSupportedActiveMode(mode agentv1.AgentMode) (agentv1.AgentMode, error) {
	normalized := normalizeMode(mode)
	if !isSupportedActiveMode(normalized) {
		return agentv1.AgentMode_AGENT_MODE_UNSPECIFIED, fmt.Errorf("unsupported active mode: %s", normalized.String())
	}
	return normalized, nil
}

func resolveExplicitMode(mode agentv1.AgentMode, source ModeSource) (agentv1.AgentMode, ModeSource, bool, error) {
	normalized, err := validateSupportedActiveMode(mode)
	if err != nil {
		return agentv1.AgentMode_AGENT_MODE_UNSPECIFIED, source, true, fmt.Errorf("%w (source=%s)", err, strings.TrimSpace(string(source)))
	}
	return normalized, source, true, nil
}

// modeAlias 把协议枚举转换为写入 JSON history 的简短模式名。
func modeAlias(mode agentv1.AgentMode) (string, error) {
	switch normalizeMode(mode) {
	case agentv1.AgentMode_AGENT_MODE_AGENT:
		return "agent", nil
	case agentv1.AgentMode_AGENT_MODE_ASK:
		return "ask", nil
	case agentv1.AgentMode_AGENT_MODE_PLAN:
		return "plan", nil
	case agentv1.AgentMode_AGENT_MODE_DEBUG:
		return "debug", nil
	case agentv1.AgentMode_AGENT_MODE_MULTITASK:
		return "multitask", nil
	default:
		return "", fmt.Errorf("unsupported mode alias: %s", normalizeMode(mode).String())
	}
}

// parseModeAlias 把写入 JSON history 的模式名恢复为协议枚举。
func parseModeAlias(raw string) (agentv1.AgentMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "agent":
		return agentv1.AgentMode_AGENT_MODE_AGENT, nil
	case "ask":
		return agentv1.AgentMode_AGENT_MODE_ASK, nil
	case "plan":
		return agentv1.AgentMode_AGENT_MODE_PLAN, nil
	case "debug":
		return agentv1.AgentMode_AGENT_MODE_DEBUG, nil
	case "multitask":
		return agentv1.AgentMode_AGENT_MODE_MULTITASK, nil
	default:
		return agentv1.AgentMode_AGENT_MODE_UNSPECIFIED, fmt.Errorf("unsupported mode alias: %q", strings.TrimSpace(raw))
	}
}

func parseTargetModeID(raw string) (agentv1.AgentMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "agent":
		return agentv1.AgentMode_AGENT_MODE_AGENT, nil
	case "ask":
		return agentv1.AgentMode_AGENT_MODE_ASK, nil
	case "plan":
		return agentv1.AgentMode_AGENT_MODE_PLAN, nil
	case "debug":
		return agentv1.AgentMode_AGENT_MODE_DEBUG, nil
	case "multitask":
		return agentv1.AgentMode_AGENT_MODE_MULTITASK, nil
	default:
		return agentv1.AgentMode_AGENT_MODE_UNSPECIFIED, fmt.Errorf("unsupported target mode id: %q", strings.TrimSpace(raw))
	}
}
