// service.go 实现 forwarder 的主链路：Bidi 上行归一化、history 写入、provider 驱动和 RunSSE 下行。
package forwarder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	"cursor/internal/appdata"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	interactionbridge "cursor/internal/backend/agent/bridge/interaction"
	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
	protocol "cursor/internal/backend/agent/protocol"
)

const (
	providerResumeDebounce         = 200 * time.Millisecond
	completedExecRetention         = 15 * time.Second
	nonStreamingExecCloseGrace     = 1500 * time.Millisecond
	defaultSummaryCompletedThought = "Chat context summarized"
	providerDefaultMaxOutputTokens = 65536
	providerOutputSafetyTokens     = 1024

	runtimeThinkingEffortParameterID = "thinking_effort"
)

type parsedSubagentModelOverrides struct {
	Overrides map[string]runtimecore.SubagentModelOverrideSelection
	Ignored   []map[string]any
	RawCount  int
}

func parseSubagentModelOverrides(items []*agentv1.SubagentModelOverride) parsedSubagentModelOverrides {
	parsed := parsedSubagentModelOverrides{
		Overrides: make(map[string]runtimecore.SubagentModelOverrideSelection),
		RawCount:  len(items),
	}
	for index, item := range items {
		if item == nil {
			parsed.Ignored = append(parsed.Ignored, map[string]any{"index": index, "reason": "nil_override"})
			continue
		}
		subagentType := strings.TrimSpace(item.GetSubagentType())
		if subagentType == "" {
			parsed.Ignored = append(parsed.Ignored, map[string]any{"index": index, "reason": "empty_subagent_type"})
			continue
		}
		if _, exists := parsed.Overrides[subagentType]; exists {
			parsed.Ignored = append(parsed.Ignored, map[string]any{"index": index, "subagent_type": subagentType, "reason": "duplicate_overrides_previous"})
		}
		switch selection := item.GetSelection().(type) {
		case *agentv1.SubagentModelOverride_Model:
			model := selection.Model
			modelID := strings.TrimSpace(model.GetModelId())
			if modelID == "" {
				parsed.Ignored = append(parsed.Ignored, map[string]any{"index": index, "subagent_type": subagentType, "reason": "empty_model_id"})
				continue
			}
			parsed.Overrides[subagentType] = runtimecore.SubagentModelOverrideSelection{
				SubagentType:                  subagentType,
				Selection:                     "model",
				ModelID:                       modelID,
				MaxMode:                       model.GetMaxMode(),
				ParameterCount:                len(model.GetParameters()),
				BuiltInModel:                  model.GetBuiltInModel(),
				IsVariantStringRepresentation: model.GetIsVariantStringRepresentation(),
			}
		case *agentv1.SubagentModelOverride_Inherit:
			parsed.Overrides[subagentType] = runtimecore.SubagentModelOverrideSelection{
				SubagentType: subagentType,
				Selection:    "inherit",
			}
		case *agentv1.SubagentModelOverride_Disabled:
			parsed.Overrides[subagentType] = runtimecore.SubagentModelOverrideSelection{
				SubagentType: subagentType,
				Selection:    "disabled",
			}
		default:
			parsed.Ignored = append(parsed.Ignored, map[string]any{"index": index, "subagent_type": subagentType, "reason": "unknown_selection"})
		}
	}
	return parsed
}

func taskSubagentModelResolutionPayload(invocation runtimecore.ToolInvocation, parentModelID string, overrides map[string]runtimecore.SubagentModelOverrideSelection) map[string]any {
	if strings.TrimSpace(invocation.ToolName) != "Task" {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal(invocation.ArgsJSON, &args); err != nil {
		return map[string]any{
			"tool_call_id": strings.TrimSpace(invocation.CallID),
			"parse_error":  err.Error(),
		}
	}
	subagentType := readStringMapValue(args, "subagent_type", "subagentType")
	taskRequestedModelID := readStringMapValue(args, "model", "model_id", "modelId")
	effectiveModelID := taskRequestedModelID
	selection := "none"
	disabled := false
	overrideHit := false
	matchedSubagentType := ""
	if override, matched, ok := runtimecore.LookupSubagentModelOverride(overrides, subagentType); ok {
		overrideHit = true
		matchedSubagentType = matched
		selection = strings.TrimSpace(override.Selection)
		switch selection {
		case "model":
			effectiveModelID = strings.TrimSpace(override.ModelID)
		case "inherit":
			effectiveModelID = strings.TrimSpace(parentModelID)
		case "disabled":
			disabled = true
			effectiveModelID = ""
		}
	}
	if effectiveModelID == "" && !disabled {
		effectiveModelID = strings.TrimSpace(parentModelID)
	}
	payload := map[string]any{
		"tool_call_id":            strings.TrimSpace(invocation.CallID),
		"subagent_type":           subagentType,
		"override_hit":            overrideHit,
		"selection":               selection,
		"task_requested_model_id": taskRequestedModelID,
		"parent_model_id":         strings.TrimSpace(parentModelID),
		"effective_model_id":      strings.TrimSpace(effectiveModelID),
		"disabled":                disabled,
	}
	if matchedSubagentType != "" {
		payload["matched_subagent_type"] = matchedSubagentType
	}
	return payload
}

func rewriteTaskInvocationModelForDisplay(invocation runtimecore.ToolInvocation, parentModelID string, overrides map[string]runtimecore.SubagentModelOverrideSelection) runtimecore.ToolInvocation {
	if strings.TrimSpace(invocation.ToolName) != "Task" {
		return invocation
	}
	var args map[string]any
	if err := json.Unmarshal(invocation.ArgsJSON, &args); err != nil {
		return invocation
	}
	subagentType := readStringMapValue(args, "subagent_type", "subagentType")
	override, _, ok := runtimecore.LookupSubagentModelOverride(overrides, subagentType)
	if !ok {
		return invocation
	}
	effectiveModelID := ""
	switch strings.TrimSpace(override.Selection) {
	case "model":
		effectiveModelID = strings.TrimSpace(override.ModelID)
	case "inherit":
		effectiveModelID = strings.TrimSpace(parentModelID)
	default:
		return invocation
	}
	if effectiveModelID == "" {
		return invocation
	}
	args["model"] = effectiveModelID
	rewrittenArgs, err := json.Marshal(args)
	if err != nil {
		return invocation
	}
	invocation.ArgsJSON = rewrittenArgs
	return invocation
}

func readStringMapValue(args map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := args[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			return strings.TrimSpace(typed)
		case fmt.Stringer:
			return strings.TrimSpace(typed.String())
		}
	}
	return ""
}

func cloneSubagentModelOverrides(overrides map[string]runtimecore.SubagentModelOverrideSelection) map[string]runtimecore.SubagentModelOverrideSelection {
	if len(overrides) == 0 {
		return nil
	}
	cloned := make(map[string]runtimecore.SubagentModelOverrideSelection, len(overrides))
	for key, value := range overrides {
		cloned[strings.TrimSpace(key)] = value
	}
	return cloned
}

func subagentModelOverrideSummaries(overrides map[string]runtimecore.SubagentModelOverrideSelection) []map[string]any {
	if len(overrides) == 0 {
		return nil
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	summaries := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		selection := overrides[key]
		summary := map[string]any{
			"subagent_type": strings.TrimSpace(selection.SubagentType),
			"selection":     strings.TrimSpace(selection.Selection),
		}
		if strings.TrimSpace(selection.ModelID) != "" {
			summary["model_id"] = strings.TrimSpace(selection.ModelID)
		}
		if selection.MaxMode {
			summary["max_mode"] = true
		}
		if selection.ParameterCount > 0 {
			summary["parameter_count"] = selection.ParameterCount
		}
		if selection.BuiltInModel {
			summary["built_in_model"] = true
		}
		if selection.IsVariantStringRepresentation {
			summary["is_variant_string_representation"] = true
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

type Service struct {
	store              *ConversationFileStore
	usageStore         *UsageFileStore
	codebaseIndexStore *CodebaseIndexStore
	docsIndexStore     *DocsIndexStore
	rules              *UserRuleStore
	projector          *HistoryProjector
	compiler           PromptCompiler
	provider           ProviderGateway
	resolver           modeladapter.ChannelResolver
	modelMemory        agentModelMemory
	broker             *StreamBroker
	recorder           *artifactRecorder
	debug              *debugRecorder
	execBridge         execbridge.ExecBridge
	interactionBridge  interactionbridge.InteractionBridge
	appendSeq          *appendSequenceTracker
}

type agentModelMemory interface {
	LastAgentModelHash() string
	SaveLastAgentModelHash(context.Context, string) error
}

// NewService 使用默认依赖创建 forwarder 服务。
func NewService(historyRoot string, resolver modeladapter.ChannelResolver) *Service {
	projector := NewHistoryProjector()
	store := NewConversationFileStore(historyRoot)
	broker := NewStreamBroker()
	rules := NewUserRuleStore(appdata.RulesRootPath())
	var modelMemory agentModelMemory
	if candidate, ok := resolver.(agentModelMemory); ok {
		modelMemory = candidate
	}
	var debugConfig debugLogConfig
	if candidate, ok := resolver.(debugLogConfig); ok {
		debugConfig = candidate
	}
	debug := newDebugRecorder(historyRoot, broker, debugConfig)
	service := &Service{
		store:              store,
		usageStore:         NewUsageFileStore(historyRoot),
		codebaseIndexStore: NewCodebaseIndexStore(appdata.CodebaseIndexRootPath()),
		docsIndexStore:     NewDocsIndexStore(appdata.DocsIndexRootPath()),
		rules:              rules,
		projector:          projector,
		compiler:           NewPromptCompiler(projector, NewToolCatalog(), NewReminderInjector(), rules),
		provider:           NewProviderGateway(resolver),
		resolver:           resolver,
		modelMemory:        modelMemory,
		broker:             broker,
		recorder:           newArtifactRecorder(store, broker, debug),
		debug:              debug,
		execBridge:         execbridge.NewBridge(),
		interactionBridge:  interactionbridge.NewBridge(),
		appendSeq:          newAppendSequenceTracker(),
	}
	service.startHistoryMaintenance()
	return service
}

// newServiceWithDependencies 主要用于测试场景，允许注入替身依赖。
func newServiceWithDependencies(store *ConversationFileStore, projector *HistoryProjector, compiler PromptCompiler, provider ProviderGateway, broker *StreamBroker) *Service {
	historyRoot := ""
	if store != nil {
		historyRoot = store.HistoryDir()
	}
	debug := newDebugRecorder(historyRoot, broker, nil)
	return &Service{
		store:              store,
		rules:              NewUserRuleStore(appdata.RulesRootPath()),
		projector:          projector,
		compiler:           compiler,
		provider:           provider,
		broker:             broker,
		usageStore:         NewUsageFileStore(store.HistoryDir()),
		codebaseIndexStore: NewCodebaseIndexStore(appdata.CodebaseIndexRootPath()),
		docsIndexStore:     NewDocsIndexStore(appdata.DocsIndexRootPath()),
		recorder:           newArtifactRecorder(store, broker, debug),
		debug:              debug,
		execBridge:         execbridge.NewBridge(),
		interactionBridge:  interactionbridge.NewBridge(),
		appendSeq:          newAppendSequenceTracker(),
	}
}

// BidiAppend 处理 legacy Bidi 上行，把用户输入和外部结果归一化后写入 history。
func (service *Service) BidiAppend(ctx context.Context, req *connect.Request[aiserverv1.BidiAppendRequest]) (*connect.Response[aiserverv1.BidiAppendResponse], error) {
	if service == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder service is nil"))
	}
	requestID := protocol.NormalizeRequestID(protocol.ReadAppendRequestID(req.Msg))
	if requestID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request_id is required"))
	}
	appendSeqno := req.Msg.GetAppendSeqno()
	dataHex := req.Msg.GetData()
	appendTicket, staleAppend, err := service.appendSeq.Acquire(ctx, requestID, appendSeqno)
	if err != nil {
		return nil, connect.NewError(connect.CodeCanceled, err)
	}
	if staleAppend {
		log.Printf("forwarder ignored stale bidi append request_id=%s append_seqno=%d", requestID, appendSeqno)
		service.debug.LogBidiRaw(ctx, requestID, "", appendSeqno, dataHex, "stale", nil)
		return connect.NewResponse(&aiserverv1.BidiAppendResponse{}), nil
	}
	defer appendTicket.Release()
	message, clientKind, err := protocol.DecodeAgentClientMessage(dataHex)
	if err != nil {
		service.debug.LogBidiRaw(ctx, requestID, "", appendSeqno, dataHex, "decode_error", map[string]any{
			"error": err.Error(),
		})
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	intent, err := service.decodeInboundIntent(requestID, message, clientKind)
	if err != nil {
		service.debug.LogBidiRaw(ctx, requestID, "", appendSeqno, dataHex, "intent_error", map[string]any{
			"client_kind": strings.TrimSpace(clientKind),
			"error":       err.Error(),
		})
		service.debug.LogBidiDecoded(ctx, requestID, "", appendSeqno, clientKind, message, InboundIntent{RequestID: requestID}, map[string]any{
			"error": err.Error(),
		})
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	service.debug.LogBidiRaw(ctx, requestID, intent.ConversationID, appendSeqno, dataHex, "accepted", map[string]any{
		"client_kind": strings.TrimSpace(clientKind),
	})
	service.debug.LogBidiDecoded(ctx, requestID, intent.ConversationID, appendSeqno, clientKind, message, intent, nil)
	if err := service.dispatchInboundIntent(intent); err != nil {
		if shouldAcknowledgeInterruptedInboundIntent(intent, err) {
			service.debug.LogRuntime(ctx, requestID, intent.ConversationID, "dispatch_interrupted_ignored", map[string]any{
				"kind":  strings.TrimSpace(intent.Kind),
				"error": err.Error(),
			})
			return connect.NewResponse(&aiserverv1.BidiAppendResponse{}), nil
		}
		service.debug.LogRuntime(ctx, requestID, intent.ConversationID, "dispatch_error", map[string]any{
			"kind":  strings.TrimSpace(intent.Kind),
			"error": err.Error(),
		})
		code := connect.CodeInvalidArgument
		if strings.TrimSpace(intent.Kind) == "run" {
			code = connect.CodeInternal
		}
		return nil, connect.NewError(code, err)
	}
	service.debug.LogRuntime(ctx, requestID, intent.ConversationID, "inbound_intent_dispatched", map[string]any{
		"kind":            strings.TrimSpace(intent.Kind),
		"thinking_effort": strings.TrimSpace(intent.ThinkingEffort),
		"prewarm":         intent.Prewarm,
		"ignored_reason":  strings.TrimSpace(intent.IgnoredReason),
	})

	return connect.NewResponse(&aiserverv1.BidiAppendResponse{}), nil
}

func shouldAcknowledgeInterruptedInboundIntent(intent InboundIntent, err error) bool {
	if !errors.Is(err, errProviderLoopInterrupted) {
		return false
	}
	switch strings.TrimSpace(intent.Kind) {
	case "metadata", "kv_result", "exec_result", "exec_control", "interaction_result", "cancel":
		return true
	default:
		return false
	}
}

// RunSSE 订阅指定 request 的活动流，优先回放 backlog，在 backlog 清空期间按 5 秒周期发送心跳。
func (service *Service) RunSSE(ctx context.Context, req *connect.Request[aiserverv1.BidiRequestId], stream *connect.ServerStream[agentv1.AgentServerMessage]) error {
	if service == nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder service is nil"))
	}
	requestID := protocol.NormalizeRequestID(protocol.ReadBidiRequestID(req.Msg))
	if requestID == "" {
		return buildRunSSECustomError(connect.CodeInvalidArgument, "请求参数无效", fmt.Errorf("request_id is required"))
	}
	subscriberID, signal, err := service.broker.Subscribe(requestID)
	if err != nil {
		return buildRunSSECustomError(connect.CodeInvalidArgument, "请求参数无效", err)
	}
	service.debug.LogRunSSE(ctx, requestID, "", "subscribe", map[string]any{
		"subscriber_id": subscriberID,
	})
	defer func() {
		remaining := service.broker.Unsubscribe(requestID, subscriberID)
		service.debug.LogRunSSE(context.Background(), requestID, "", "unsubscribe", map[string]any{
			"subscriber_id":         subscriberID,
			"remaining_subscribers": remaining,
		})
		if remaining == 0 {
			// RunSSE 连接短暂抖动时，给活跃 provider 一段重连宽限期，
			// 避免把本来还能正常收口的请求直接打成 context canceled。
			if !service.scheduleOrphanCancelActor(requestID, "[canceled] RunSSE client disconnected") {
				service.broker.RemoveIfIdle(requestID)
			}
		}
	}()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	cursor := 0
	for {
		backlog, err := service.broker.ReadFromCursor(requestID, cursor)
		if err != nil {
			service.debug.LogRunSSE(ctx, requestID, "", "read_error", map[string]any{
				"cursor": cursor,
				"error":  err.Error(),
			})
			return nil
		}
		if len(backlog) > 0 {
			for _, event := range backlog {
				if event.Message != nil {
					if err := stream.Send(event.Message); err != nil {
						service.debug.LogRunSSE(ctx, requestID, "", "send_error", map[string]any{
							"cursor":       cursor,
							"message_case": agentServerMessageCase(event.Message),
							"message":      protoJSONDebugPayload(event.Message),
							"error":        err.Error(),
						})
						return err
					}
					service.debug.LogRunSSE(ctx, requestID, "", "send_message", map[string]any{
						"cursor":       cursor,
						"message_case": agentServerMessageCase(event.Message),
						"message":      protoJSONDebugPayload(event.Message),
					})
				}
				cursor++
				if event.End {
					service.debug.LogRunSSE(ctx, requestID, "", "terminal", map[string]any{
						"cursor":                 cursor,
						"terminal_error_code":    strings.TrimSpace(event.TerminalErrorCode),
						"terminal_error_message": strings.TrimSpace(event.TerminalErrorMessage),
					})
					return buildTerminalStreamError(event)
				}
			}
			continue
		}
		select {
		case <-ctx.Done():
			service.debug.LogRunSSE(ctx, requestID, "", "client_context_done", map[string]any{
				"cursor": cursor,
				"error":  ctx.Err().Error(),
			})
			if backlog, err := service.broker.ReadFromCursor(requestID, cursor); err == nil {
				for _, event := range backlog {
					cursor++
					if event.End {
						service.debug.LogRunSSE(context.Background(), requestID, "", "terminal_after_context_done", map[string]any{
							"cursor":                 cursor,
							"terminal_error_code":    strings.TrimSpace(event.TerminalErrorCode),
							"terminal_error_message": strings.TrimSpace(event.TerminalErrorMessage),
						})
						return buildTerminalStreamError(event)
					}
				}
			}
			return nil
		case <-signal:
			continue
		case <-ticker.C:
		}
		if backlog, err := service.broker.ReadFromCursor(requestID, cursor); err != nil {
			service.debug.LogRunSSE(ctx, requestID, "", "read_error", map[string]any{
				"cursor": cursor,
				"error":  err.Error(),
			})
			return nil
		} else if len(backlog) > 0 {
			continue
		}
		heartbeat := buildHeartbeatMessage()
		if err := stream.Send(heartbeat); err != nil {
			service.debug.LogRunSSE(ctx, requestID, "", "heartbeat_error", map[string]any{
				"cursor":       cursor,
				"message_case": agentServerMessageCase(heartbeat),
				"message":      protoJSONDebugPayload(heartbeat),
				"error":        err.Error(),
			})
			return err
		}
		service.debug.LogRunSSE(ctx, requestID, "", "heartbeat", map[string]any{
			"cursor":       cursor,
			"message_case": agentServerMessageCase(heartbeat),
			"message":      protoJSONDebugPayload(heartbeat),
		})
	}
}

// decodeInboundIntent 把 legacy AgentClientMessage 映射为 forwarder 内部 intent。
func (service *Service) decodeInboundIntent(requestID string, message *agentv1.AgentClientMessage, clientKind string) (InboundIntent, error) {
	intent := InboundIntent{
		RequestID:     strings.TrimSpace(requestID),
		ClientMessage: message,
	}
	var err error
	switch strings.TrimSpace(clientKind) {
	case "run_request":
		runRequest := message.GetRunRequest()
		if runRequest == nil {
			return InboundIntent{}, fmt.Errorf("run_request payload is required")
		}
		conversationID := strings.TrimSpace(runRequest.GetConversationId())
		if conversationID == "" {
			return InboundIntent{}, fmt.Errorf("conversation_id is required in run_request")
		}
		intent.ConversationID = conversationID
		intent.ConversationState = runRequest.GetConversationState()
		intent.UserMessage = extractUserMessage(message)
		intent.RequestContext = extractRequestContext(message)
		if service.shouldIgnoreEmptyResumeRunRequest(requestID, runRequest, intent.UserMessage, intent.RequestContext) {
			intent.Kind = "metadata"
			intent.StartsRun = false
			intent.HasExplicitMode = false
			intent.ModeSource = ModeSourceUnknown
			intent.IgnoredReason = "empty_resume_without_pending_continuation"
			return intent, nil
		}
		intent.Kind = "run"
		intent.StartsRun = true
		intent.Mode, intent.ModeSource, intent.HasExplicitMode, err = extractRunMode(message)
		if err != nil {
			return InboundIntent{}, err
		}
		intent.ModelID = extractRequestedModelID(message)
		intent.ThinkingEffort = extractRuntimeThinkingEffort(message)
		intent.SubagentTypeName = strings.TrimSpace(runRequest.GetSubagentTypeName())
		parsedOverrides := parseSubagentModelOverrides(runRequest.GetSubagentModelOverrides())
		intent.SubagentModelOverrides = parsedOverrides.Overrides
		service.debug.LogRuntime(context.Background(), intent.RequestID, intent.ConversationID, "subagent_model_overrides_parsed", map[string]any{
			"override_count": parsedOverrides.RawCount,
			"valid_count":    len(parsedOverrides.Overrides),
			"ignored_count":  len(parsedOverrides.Ignored),
			"overrides":      subagentModelOverrideSummaries(parsedOverrides.Overrides),
			"ignored":        parsedOverrides.Ignored,
		})
		if intent.ModelID == "" {
			intent.ModelID = "default"
		}
		intent.ModelName = service.resolveRequestedModelName(message, intent.ModelID)
	case "prewarm_request":
		prewarmRequest := message.GetPrewarmRequest()
		if prewarmRequest == nil {
			return InboundIntent{}, fmt.Errorf("prewarm_request payload is required")
		}
		conversationID := strings.TrimSpace(prewarmRequest.GetConversationId())
		if conversationID == "" {
			return InboundIntent{}, fmt.Errorf("conversation_id is required in prewarm_request")
		}
		intent.Kind = "run"
		intent.Prewarm = true
		intent.StartsRun = true
		intent.ConversationID = conversationID
		intent.SubagentTypeName = strings.TrimSpace(prewarmRequest.GetSubagentTypeName())
		intent.ConversationState = prewarmRequest.GetConversationState()
		intent.Mode, intent.ModeSource, intent.HasExplicitMode, err = extractPrewarmMode(prewarmRequest)
		if err != nil {
			return InboundIntent{}, err
		}
		intent.ModelID = firstNonEmpty(extractRequestedModelID(message), "default")
		intent.ThinkingEffort = extractRuntimeThinkingEffort(message)
		intent.ModelName = service.resolveRequestedModelName(message, intent.ModelID)
	case "conversation_action":
		action := message.GetConversationAction()
		if action == nil {
			return InboundIntent{}, fmt.Errorf("conversation_action payload is required")
		}
		intent.UserMessage = extractConversationActionUserMessage(action)
		intent.RequestContext = extractConversationActionRequestContext(action)
		intent.StartsRun = conversationActionStartsRun(action)
		intent.Mode, intent.ModeSource, intent.HasExplicitMode, err = extractConversationActionMode(action)
		if err != nil {
			return InboundIntent{}, err
		}
		switch item := action.GetAction().(type) {
		case *agentv1.ConversationAction_CancelAction:
			intent.Kind = "cancel"
			intent.CancelReason = strings.TrimSpace(item.CancelAction.GetReason())
		default:
			if intent.StartsRun || intent.HasExplicitMode {
				if stream, ok := service.broker.Get(intent.RequestID); ok && stream != nil {
					stream.mu.Lock()
					intent.ConversationID = strings.TrimSpace(stream.ConversationID)
					intent.ModelID = strings.TrimSpace(stream.ModelID)
					intent.ModelName = strings.TrimSpace(stream.ModelName)
					intent.ThinkingEffort = strings.TrimSpace(stream.ThinkingEffort)
					if !intent.HasExplicitMode && stream.Mode != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
						intent.Mode = stream.Mode
					}
					if stream.CheckpointConversation != nil {
						intent.SubagentTypeName = strings.TrimSpace(stream.CheckpointConversation.SubagentTypeName)
					}
					stream.mu.Unlock()
				}
				if strings.TrimSpace(intent.ConversationID) == "" {
					return InboundIntent{}, fmt.Errorf("conversation_action requires active request context")
				}
			}
			if intent.StartsRun {
				intent.Kind = "run"
				intent.StartsRun = true
				if intent.ModelID == "" {
					intent.ModelID = "default"
				}
			} else {
				intent.Kind = "metadata"
			}
		}
	case "exec_client_message":
		intent.Kind = "exec_result"
		intent.ExecClientMessage = message.GetExecClientMessage()
	case "exec_client_control_message":
		intent.Kind = "exec_control"
		intent.ExecClientControlMessage = message.GetExecClientControlMessage()
	case "interaction_response":
		intent.Kind = "interaction_result"
		intent.InteractionResponse = message.GetInteractionResponse()
	case "kv_client_message":
		intent.Kind = "kv_result"
		intent.KVClientMessage = message.GetKvClientMessage()
	case "client_heartbeat":
		intent.Kind = "metadata"
	default:
		return InboundIntent{}, fmt.Errorf("unsupported client message kind: %s", clientKind)
	}
	return intent, nil
}

// handleRunIntent 处理 run/prewarm 类 intent，负责建会话、写 turn 和拉起 provider。
func (service *Service) handleRunIntent(intent InboundIntent) error {
	intent.UserMessage = normalizeUserMessageForStorage(intent.UserMessage)
	if !intent.Prewarm {
		service.cancelOtherConversationActors(
			intent.ConversationID,
			intent.RequestID,
			"[canceled] Superseded by newer request",
		)
	}
	conversation, effectiveMode, turnSeq, initialEntries, err := service.bootstrapRuntimeConversation(intent)
	if err != nil {
		return err
	}
	rewindDecision := service.decideRunRewind(intent, conversation)
	if rewindDecision.Evaluated && !rewindDecision.Apply {
		service.logRunRewindDecision(intent.RequestID, intent.ConversationID, "rewind_skipped", rewindDecision)
	}
	if rewindDecision.Apply {
		service.logRunRewindDecision(intent.RequestID, intent.ConversationID, "rewind_detected", rewindDecision)
		turnSeq = rewindDecision.TargetTurnSeq
		initialEntries, err = buildRunEntries(intent, effectiveMode, turnSeq)
		if err != nil {
			return err
		}
	}
	if service.store != nil {
		if rewindDecision.Apply {
			persisted, err := service.store.ReplaceEntries(
				intent.ConversationID,
				appendReplacementRunEntries(rewindDecision.PrefixEntries, initialEntries),
				func(item *ConversationFile) error {
					applyRunRewindMetadata(item, conversation, intent, turnSeq)
					return nil
				},
			)
			if err != nil {
				return err
			}
			if persisted != nil {
				conversation = persisted
			}
			service.logRunRewindDecision(intent.RequestID, intent.ConversationID, "rewind_applied", rewindDecision)
		} else {
			persisted, err := service.store.SaveConversationWithEntries(intent.ConversationID, conversation, initialEntries)
			if err != nil {
				return err
			}
			if persisted != nil {
				conversation = persisted
			}
		}
	} else if rewindDecision.Apply {
		service.applyRunRewindToConversation(conversation, rewindDecision, initialEntries, intent, turnSeq)
		service.logRunRewindDecision(intent.RequestID, intent.ConversationID, "rewind_applied", rewindDecision)
	} else if len(initialEntries) > 0 {
		appendEntriesInPlace(conversation, initialEntries)
		deriveConversationLoopState(conversation)
	}

	stream, err := service.broker.OpenStream(intent.RequestID, intent.ConversationID, turnSeq, intent.ModelID, intent.ModelName, effectiveMode, userMessageText(intent.UserMessage))
	if err != nil {
		return err
	}
	if stream == nil {
		return fmt.Errorf("open stream failed")
	}
	if err := service.replaceCheckpointConversation(stream, conversation); err != nil {
		return err
	}
	updateStreamRequestContextData(stream, intent.RequestContext)
	service.updateStreamMCPToolServers(stream, intent.RequestContext)
	clearPendingProviderCompletion(stream)
	stream.mu.Lock()
	stream.ThinkingEffort = strings.TrimSpace(intent.ThinkingEffort)
	stream.SubagentModelOverrides = cloneSubagentModelOverrides(intent.SubagentModelOverrides)
	stream.PendingProviderAction = providerActionNone
	stream.PendingCompaction = nil
	stream.PendingExecs = make(map[string]runtimecore.PendingExec)
	stream.PendingInteractions = make(map[string]runtimecore.PendingInteraction)
	stream.RecentCompletedExecs = make(map[uint32]time.Time)
	stream.BackgroundShells = make(map[string]*BackgroundShellState)
	stream.BackgroundShellsByMessageID = make(map[uint32]string)
	stream.BackgroundShellsByExecID = make(map[string]string)
	stream.TimerTokens = make(map[string]uint64)
	stream.CurrentProviderToken = 0
	stream.CurrentCompactionToken = 0
	stream.ProviderAccumulatedText = ""
	stream.ProviderAccumulatedReasoning = ""
	stream.ProviderAccumulatedReasoningSignature = ""
	stream.ProviderAccumulatedReasoningSignatureSource = ""
	stream.ProviderAccumulatedReasoningItemID = ""
	stream.ProviderAccumulatedReasoningStatus = ""
	stream.ProviderAccumulatedReasoningSummary = nil
	stream.ProviderSyntheticThinkingStartedAt = time.Time{}
	stream.ProviderSyntheticThinkingPublished = false
	stream.ProviderFinishReason = ""
	stream.ProviderUsage = turnUsageSnapshot{}
	stream.ToolInvocationCount = 0
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	service.setTurnPhase(stream, TurnPhaseIdle)
	service.debug.LogRuntime(context.Background(), intent.RequestID, intent.ConversationID, "stream_state_updated", map[string]any{
		"turn_seq":                      turnSeq,
		"model_id":                      strings.TrimSpace(intent.ModelID),
		"model_name":                    strings.TrimSpace(intent.ModelName),
		"thinking_effort":               strings.TrimSpace(intent.ThinkingEffort),
		"mode":                          effectiveMode.String(),
		"prewarm":                       intent.Prewarm,
		"subagent_type":                 strings.TrimSpace(intent.SubagentTypeName),
		"subagent_model_override_count": len(intent.SubagentModelOverrides),
		"subagent_model_overrides":      subagentModelOverrideSummaries(intent.SubagentModelOverrides),
		"latest_user_text":              userMessageText(intent.UserMessage),
	})
	if err := service.publishCheckpoint(intent.RequestID, intent.ConversationID); err != nil {
		return err
	}
	if intent.Prewarm {
		return nil
	}
	return service.requestProviderAction(stream, providerActionStart)
}

func (service *Service) loadPreviousSummaryReplay(conversationID string) ([][]byte, bool, error) {
	if service == nil || strings.TrimSpace(conversationID) == "" {
		return nil, false, nil
	}
	return service.loadLatestCarryForwardReplay(conversationID)
}

func (service *Service) snapshotVisibleTurns(conversation *ConversationFile) ([][]byte, error) {
	if service == nil || service.projector == nil || conversation == nil {
		return nil, nil
	}
	state, err := service.projector.ProjectLegacyCheckpoint(conversation)
	if err != nil {
		return nil, err
	}
	return cloneByteSlices(state.GetTurns()), nil
}

// handleCancelIntent 处理取消请求，并向客户端发送执行桥 abort。
func (service *Service) handleCancelIntent(intent InboundIntent) error {
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", intent.RequestID)
	}
	hasCheckpoint := checkpointConversationInitialized(stream)
	if hasCheckpoint {
		cancelReason := firstNonEmpty(intent.CancelReason, "user aborted")
		_, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
			newMetadataEntry(stream.TurnSeq, intent.RequestID, "control", map[string]any{
				"status":        "canceled",
				"reason":        cancelReason,
				"replay_policy": cancelReplayPolicyForReason(cancelReason),
			}),
		})
		if err != nil {
			return err
		}
	}
	stream.mu.Lock()
	pendingExecs := make([]runtimecore.PendingExec, 0, len(stream.PendingExecs))
	for _, pending := range stream.PendingExecs {
		pendingExecs = append(pendingExecs, pending)
	}
	stream.mu.Unlock()
	for _, pending := range pendingExecs {
		_ = service.broker.Publish(intent.RequestID, StreamEvent{
			Message: buildExecAbortMessage(pending),
		})
	}
	if hasCheckpoint {
		if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
			return err
		}
	}
	clearPendingProviderCompletion(stream)
	stream.mu.Lock()
	stream.PendingProviderAction = providerActionNone
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	service.setTurnPhase(stream, TurnPhaseCanceled)
	return service.broker.Cancel(intent.RequestID, firstNonEmpty(intent.CancelReason, "[canceled] User aborted request"))
}

// handleExecResult 处理客户端返回的执行桥结果，并在终态时把 tool_result 写回 history。
func (service *Service) handleExecResult(intent InboundIntent) error {
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", intent.RequestID)
	}
	if intent.ExecClientMessage == nil {
		return fmt.Errorf("exec client message is required")
	}
	pending, found := selectPendingExec(intent.ExecClientMessage.GetExecId(), intent.ExecClientMessage.GetId(), stream)
	if !found {
		if service.observeMissingBackgroundShellExecClientMessage(stream, intent.ExecClientMessage) {
			return nil
		}
		if service.observeMissingShellExecClientMessage(stream, intent.ExecClientMessage) {
			return nil
		}
		if shouldIgnoreMissingExecResult(intent.ExecClientMessage, stream) {
			return nil
		}
		return fmt.Errorf("pending exec not found")
	}
	service.observeBackgroundShellExecClientMessage(stream, pending, intent.ExecClientMessage)
	service.observeShellExecClientMessage(stream, pending, intent.ExecClientMessage)
	pending = service.applyExecProgress(stream, pending, intent.ExecClientMessage)
	if isHiddenPatchEditExecKind(pending.ExecKind) {
		return service.handleHiddenPatchEditExecResult(stream, pending, intent.ExecClientMessage)
	}
	if isHiddenWriteExecKind(pending.ExecKind) {
		return service.handleHiddenWriteExecResult(stream, pending, intent.ExecClientMessage)
	}
	result, err := service.execBridge.ApplyExecClientMessage(intent.ExecClientMessage, pending)
	if err != nil {
		return err
	}
	if result.ShellOutputDelta != nil {
		if err := service.broker.Publish(intent.RequestID, StreamEvent{
			Message: buildShellOutputDeltaMessage(result.ShellOutputDelta),
		}); err != nil {
			return err
		}
	}
	if !result.IsTerminal {
		return nil
	}
	markExecCompleted(stream, pending)
	backgroundShellToolCallID := ""
	if strings.TrimSpace(pending.ExecKind) == "shell" && shellToolCallIsBackgrounded(result.ToolCall) {
		backgroundShellToolCallID = firstNonEmpty(strings.TrimSpace(result.ToolCallID), strings.TrimSpace(pending.ToolCallID))
	}
	if strings.TrimSpace(pending.ExecKind) == "execute_hook_pre_compact" {
		return service.handlePreCompactTerminal(stream, pending.ProviderPass, strings.TrimSpace(result.ToolResultPayload))
	}
	if result.ToolCall != nil {
		if err := service.appendToolResult(stream, result.ToolCallID, deriveToolNameFromPendingExec(pending), pending.ArgsJSON, result.ToolResultPayload, pending.ReasoningContent, result.ToolCall); err != nil {
			return err
		}
	} else if strings.TrimSpace(result.ToolResultPayload) != "" {
		if err := service.appendToolResult(stream, pending.ToolCallID, deriveToolNameFromPendingExec(pending), pending.ArgsJSON, result.ToolResultPayload, pending.ReasoningContent, nil); err != nil {
			return err
		}
	}
	if backgroundShellToolCallID != "" {
		if recordedToolCallID, recorded := recordBackgroundShellActionMemory(stream, backgroundShellToolCallID, time.Now().UTC()); recorded {
			if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
				newBackgroundShellActionMetadataEntry(stream.TurnSeq, stream.RequestID, recordedToolCallID, backgroundShellActionSourceLocalBackgrounded),
			}); err != nil {
				return err
			}
		}
	}
	if err := service.publishToolCallCompleted(intent.RequestID, result.ToolCallID, pending.ModelCallID, result.ToolCall); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, intent.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishCheckpoint(intent.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}

// handleExecControl 处理执行桥控制面结果，例如 stream_close 或 throw。
func (service *Service) handleExecControl(intent InboundIntent) error {
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok || stream == nil {
		if shouldIgnoreStaleExecControl(intent.ExecClientControlMessage) {
			return nil
		}
		return fmt.Errorf("request is not active: %s", intent.RequestID)
	}
	if intent.ExecClientControlMessage == nil {
		return fmt.Errorf("exec client control message is required")
	}
	pending, found := selectPendingExecByControl(intent.ExecClientControlMessage, stream)
	if !found {
		if shouldIgnoreMissingExecControl(intent.ExecClientControlMessage, stream) {
			return nil
		}
		return fmt.Errorf("pending exec not found for control message")
	}
	pending = service.applyExecControlProgress(stream, pending, intent.ExecClientControlMessage)
	if isHiddenPatchEditExecKind(pending.ExecKind) {
		return service.handleHiddenPatchEditExecControl(stream, pending, intent.ExecClientControlMessage)
	}
	if isHiddenWriteExecKind(pending.ExecKind) {
		return service.handleHiddenWriteExecControl(stream, pending, intent.ExecClientControlMessage)
	}
	result, err := service.execBridge.ApplyExecClientControl(intent.ExecClientControlMessage, pending)
	if err != nil {
		return err
	}
	if !result.IsTerminal {
		if shouldRecoverNonStreamingExecOnStreamClose(intent.ExecClientControlMessage, pending) {
			markExecTransportClosed(stream, pending)
			service.scheduleNonStreamingExecRecovery(intent.RequestID, pending)
			return nil
		}
		if shouldObserveShellStreamClose(intent.ExecClientControlMessage, pending) {
			service.observeShellStreamClose(stream, pending)
		}
		return nil
	}
	markExecCompleted(stream, pending)
	if strings.TrimSpace(pending.ExecKind) == "execute_hook_pre_compact" {
		return service.handlePreCompactTerminal(stream, pending.ProviderPass, "")
	}
	if strings.TrimSpace(result.ToolResultPayload) != "" {
		if err := service.appendToolResult(stream, pending.ToolCallID, deriveToolNameFromPendingExec(pending), pending.ArgsJSON, result.ToolResultPayload, pending.ReasoningContent, nil); err != nil {
			return err
		}
		_, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
			newMetadataEntry(stream.TurnSeq, stream.RequestID, "tool_control", map[string]any{
				"tool_call_id": result.ToolCallID,
				"payload":      result.ToolResultPayload,
			}),
		})
		if err != nil {
			return err
		}
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, intent.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishToolCallCompleted(intent.RequestID, result.ToolCallID, pending.ModelCallID, nil); err != nil {
		return err
	}
	if err := service.publishCheckpoint(intent.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}

func shouldRecoverNonStreamingExecOnStreamClose(message *agentv1.ExecClientControlMessage, pending runtimecore.PendingExec) bool {
	if message == nil || isStreamingPendingExecKind(pending.ExecKind) {
		return false
	}
	switch message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_StreamClose:
		return true
	default:
		return false
	}
}

func shouldObserveShellStreamClose(message *agentv1.ExecClientControlMessage, pending runtimecore.PendingExec) bool {
	if message == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return false
	}
	switch message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_StreamClose:
		return true
	default:
		return false
	}
}

func isStreamingPendingExecKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "shell":
		return true
	default:
		return false
	}
}

func markExecTransportClosed(stream *ActiveStream, pending runtimecore.PendingExec) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	current, ok := stream.PendingExecs[pending.ExecID]
	if ok {
		now := time.Now().UTC()
		current.StreamState = "transport_closed"
		current.LastShellActivityAt = now
		stream.PendingExecs[pending.ExecID] = current
		stream.UpdatedAt = now
	}
	stream.mu.Unlock()
}

func snapshotPendingExec(stream *ActiveStream, execID string) (runtimecore.PendingExec, bool) {
	if stream == nil || strings.TrimSpace(execID) == "" {
		return runtimecore.PendingExec{}, false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	item, ok := stream.PendingExecs[strings.TrimSpace(execID)]
	return item, ok
}

func (service *Service) scheduleNonStreamingExecRecovery(requestID string, pending runtimecore.PendingExec) {
	if service == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(pending.ExecID) == "" {
		return
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return
	}
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerNonStreamingRecovery, pending.ExecID),
		nonStreamingExecCloseGrace,
		streamTimerNonStreamingRecovery,
		pending.ExecID,
		pending.MessageID,
		"",
	)
}

func (service *Service) recoverNonStreamingExecAfterStreamClose(stream *ActiveStream, pending runtimecore.PendingExec) error {
	if stream == nil {
		return nil
	}
	markExecCompleted(stream, pending)
	toolName := strings.TrimSpace(deriveToolNameFromPendingExec(pending))
	resultPayload := fmt.Sprintf("%s transport closed before terminal result arrived", firstNonEmpty(toolName, pending.ExecKind, "tool"))
	log.Printf("forwarder synthetic exec recovery request_id=%s tool_call_id=%s message_id=%d exec_id=%s exec_kind=%s", strings.TrimSpace(stream.RequestID), strings.TrimSpace(pending.ToolCallID), pending.MessageID, strings.TrimSpace(pending.ExecID), strings.TrimSpace(pending.ExecKind))
	if toolName != "" {
		if err := service.appendToolResult(stream, pending.ToolCallID, toolName, pending.ArgsJSON, resultPayload, pending.ReasoningContent, nil); err != nil {
			return err
		}
	}
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "tool_transport_closed", map[string]any{
			"tool_call_id": pending.ToolCallID,
			"message_id":   pending.MessageID,
			"exec_id":      pending.ExecID,
			"exec_kind":    pending.ExecKind,
			"payload":      resultPayload,
		}),
	}); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, stream.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishToolCallCompleted(stream.RequestID, pending.ToolCallID, pending.ModelCallID, nil); err != nil {
		return err
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}

func (service *Service) observeShellStreamClose(stream *ActiveStream, pending runtimecore.PendingExec) {
	if service == nil || stream == nil {
		return
	}
	current, ok := snapshotPendingExec(stream, pending.ExecID)
	if !ok {
		return
	}
	recentState := strings.TrimSpace(current.StreamState)
	if recentState == "transport_closed" || recentState == "exited" || recentState == "backgrounded" || recentState == "rejected" || recentState == "permission_denied" {
		return
	}
	log.Printf(
		"forwarder shell stream closed without terminal event request_id=%s tool_call_id=%s message_id=%d exec_id=%s stream_state=%s chunk_count=%d",
		strings.TrimSpace(stream.RequestID),
		strings.TrimSpace(current.ToolCallID),
		current.MessageID,
		strings.TrimSpace(current.ExecID),
		recentState,
		current.ChunkCount,
	)
	markExecTransportClosed(stream, current)
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "shell_stream_transport_closed", map[string]any{
			"tool_call_id":        current.ToolCallID,
			"message_id":          current.MessageID,
			"exec_id":             current.ExecID,
			"exec_kind":           current.ExecKind,
			"recent_stream_state": recentState,
			"chunk_count":         current.ChunkCount,
			"first_chunk_at":      current.FirstChunkAt,
			"reasoning_present":   strings.TrimSpace(current.ReasoningContent) != "",
			"stdout_buffer_bytes": len(current.StdoutBuffer),
			"stderr_buffer_bytes": len(current.StderrBuffer),
		}),
	}); err != nil {
		log.Printf("forwarder shell stream close metadata failed request_id=%s tool_call_id=%s err=%v", strings.TrimSpace(stream.RequestID), strings.TrimSpace(current.ToolCallID), err)
	}
	service.scheduleShellTransportCloseRecovery(stream.RequestID, current)
}

// handleMetadataIntent 处理当前不驱动 provider 的轻量元数据上行。
func (service *Service) handleMetadataIntent(intent InboundIntent) error {
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok || stream == nil {
		if intent.HasExplicitMode || intent.StartsRun {
			return fmt.Errorf("metadata intent requires active request context: %s", intent.RequestID)
		}
		return nil
	}
	backgroundShellToolCallID, backgroundShellActionWasNew := observeBackgroundShellAction(stream, intent.ClientMessage)
	observeBackgroundTaskCompletionAction(stream, intent.ClientMessage)
	if !checkpointConversationInitialized(stream) {
		if intent.HasExplicitMode {
			stream.mu.Lock()
			stream.Mode = intent.Mode
			stream.UpdatedAt = time.Now().UTC()
			stream.mu.Unlock()
		}
		return nil
	}
	entries := []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "metadata", map[string]any{
			"kind":       intent.Kind,
			"starts_run": intent.StartsRun,
		}),
	}
	if backgroundShellToolCallID != "" && backgroundShellActionWasNew {
		entries = append(entries, newBackgroundShellActionMetadataEntry(stream.TurnSeq, stream.RequestID, backgroundShellToolCallID, backgroundShellActionSourceClient))
	}
	entries = append(entries, backgroundTaskCompletionMetadataEntries(stream.TurnSeq, stream.RequestID, intent.ClientMessage)...)
	if intent.HasExplicitMode {
		modeEntry, err := newModeMetadataEntry(stream.TurnSeq, stream.RequestID, intent.Mode, true, intent.ModeSource)
		if err != nil {
			return err
		}
		modeAliasValue, err := modeAlias(intent.Mode)
		if err != nil {
			return err
		}
		entries = append(entries, modeEntry, newModeChangePromptContextEntry(stream.TurnSeq, stream.RequestID, intent.Mode))
		stream.mu.Lock()
		stream.Mode = intent.Mode
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		if _, err := service.updateConversationMetaAndCheckpoint(stream, stream.ConversationID, func(item *ConversationFile) error {
			if item == nil {
				return nil
			}
			item.Mode = modeAliasValue
			return nil
		}); err != nil {
			return err
		}
	}
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, entries); err != nil {
		return err
	}
	if intent.HasExplicitMode {
		stream.mu.Lock()
		modelCallID := strings.TrimSpace(stream.CurrentModelCallID)
		stream.mu.Unlock()
		if modelCallID != "" {
			if err := service.syncSummaryCarryForward(stream.ConversationID, intent.RequestID, modelCallID); err != nil {
				return err
			}
		}
		if err := service.publishCheckpoint(intent.RequestID, stream.ConversationID); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) scheduleProviderResume(stream *ActiveStream, _ int) error {
	return service.requestProviderAction(stream, providerActionResume)
}

func shouldResumeAfterToolResults(finishReason string) bool {
	switch strings.TrimSpace(finishReason) {
	case "tool_use", "tool_calls", "function_call":
		return true
	default:
		return false
	}
}

func (service *Service) cancelScheduledProviderResume(stream *ActiveStream) {
	if stream == nil {
		return
	}
	clearStreamTimer(stream, providerTimerKey(streamTimerProviderResume, ""))
}

// driveProvider 由 actor 触发一次 provider pass，并把真实流包装成 provider_event 回投 mailbox。
func (service *Service) driveProvider(stream *ActiveStream) error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	if stream.ProviderActive || stream.Status == StreamStatusCanceled || stream.Status == StreamStatusCompleted || stream.Status == StreamStatusFailed {
		stream.mu.Unlock()
		return nil
	}
	stream.ProviderPassCount++
	currentPass := stream.ProviderPassCount
	stream.Status = StreamStatusStreaming
	stream.PendingProviderAction = providerActionNone
	stream.CurrentModelCallID = uuid.NewString()
	stream.CurrentProviderToken++
	currentToken := stream.CurrentProviderToken
	stream.ProviderAccumulatedText = ""
	stream.ProviderAccumulatedReasoning = ""
	stream.ProviderAccumulatedReasoningSignature = ""
	stream.ProviderAccumulatedReasoningSignatureSource = ""
	stream.ProviderAccumulatedReasoningItemID = ""
	stream.ProviderAccumulatedReasoningStatus = ""
	stream.ProviderAccumulatedReasoningSummary = nil
	if stream.ProviderSyntheticThinkingStartedAt.IsZero() {
		stream.ProviderSyntheticThinkingStartedAt = time.Now().UTC()
	}
	stream.ProviderFinishReason = ""
	stream.ProviderUsage = turnUsageSnapshot{}
	stream.ToolInvocationCount = 0
	modelCallID := stream.CurrentModelCallID
	conversationID := stream.ConversationID
	requestID := stream.RequestID
	modelID := stream.ModelID
	modelName := stream.ModelName
	thinkingEffort := stream.ThinkingEffort
	mode := stream.Mode
	latestUserText := stream.LatestUserText
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	log.Printf("forwarder provider pass started request_id=%s model_call_id=%s provider_pass=%d", strings.TrimSpace(requestID), strings.TrimSpace(modelCallID), currentPass)

	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", err)
	}
	conversation, err = service.syncConversationContextWindowTokens(stream, conversationID, conversation)
	if err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", err)
	}
	conversation, err = service.persistDerivedPromptContexts(stream, conversationID, requestID, conversation, mode, latestUserText)
	if err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", err)
	}
	compiled, err := service.compiler.Compile(conversation, mode, latestUserText, modelName)
	if err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", err)
	}
	compiled = guardCompiledConversationForProvider(compiled)
	if compacted, compactErr := service.maybeCompactBeforeProvider(stream, conversation, compiled); compactErr != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", compactErr)
	} else if compacted {
		stream.mu.Lock()
		stream.ProviderActive = false
		stream.ProviderCancel = nil
		stream.UpdatedAt = time.Now().UTC()
		hasPendingCompaction := stream.PendingCompaction != nil
		status := stream.Status
		stream.mu.Unlock()
		switch {
		case isTerminalStreamStatus(status):
			switch status {
			case StreamStatusCompleted:
				service.setTurnPhase(stream, TurnPhaseCompleted)
			case StreamStatusCanceled:
				service.setTurnPhase(stream, TurnPhaseCanceled)
			default:
				service.setTurnPhase(stream, TurnPhaseFailed)
			}
		case hasPendingCompaction:
			service.setTurnPhase(stream, TurnPhaseCompacting)
		default:
			service.setTurnPhase(stream, TurnPhaseIdle)
		}
		return nil
	}
	if err := service.syncSummarySnapshot(stream, conversation, requestID, modelCallID); err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", err)
	}
	maxTokens, requestKnobs := service.resolveProviderOutputBudget(modelID, conversation, compiled)
	service.maybeSaveLastAgentModelHash(conversation, modelID, mode, currentPass)
	ctx, cancel := context.WithCancel(context.Background())
	stream.mu.Lock()
	stream.ProviderActive = true
	stream.ProviderCancel = cancel
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	service.setTurnPhase(stream, TurnPhaseProviderRunning)

	providerRequest := ProviderRequest{
		RequestID:          requestID,
		ConversationID:     conversationID,
		RunID:              requestID,
		ModelCallID:        modelCallID,
		ModelID:            modelID,
		Mode:               compiled.Mode,
		ThinkingEffort:     compiled.Mode.String(),
		Messages:           compiled.Messages,
		StableMessageCount: compiled.StableMessageCount,
		Tools:              compiled.Tools,
		MaxTokens:          maxTokens,
		RequestKnobs:       requestKnobs,
		CompileSummary:     compiled.CompileSummary,
		Observer:           service.recorder,
		ArtifactPaths:      &modeladapter.LLMArtifactPaths{},
	}
	providerRequest.ThinkingEffort = thinkingEffort
	service.debug.LogProvider(context.Background(), requestID, conversationID, "provider_request_prepared", map[string]any{
		"model_call_id":          strings.TrimSpace(modelCallID),
		"provider_pass":          currentPass,
		"model_id":               strings.TrimSpace(modelID),
		"model_name":             strings.TrimSpace(modelName),
		"mode":                   compiled.Mode.String(),
		"thinking_effort":        strings.TrimSpace(thinkingEffort),
		"max_tokens":             maxTokens,
		"request_knobs":          requestKnobs,
		"message_count":          len(compiled.Messages),
		"tool_count":             len(compiled.Tools),
		"compile_summary_length": len(compiled.CompileSummary),
	})
	go service.runProviderStream(stream, currentToken, ctx, providerRequest)
	return nil
}

func (service *Service) resolveProviderOutputBudget(modelID string, conversation *ConversationFile, compiled CompiledConversation) (int, map[string]any) {
	configuredMaxTokens := service.resolveConfiguredProviderMaxOutputTokens(modelID)
	contextWindowTokens := compactionContextWindowSize(conversation)
	estimatedPromptTokens := estimateCompiledPromptTokens(compiled)
	if conversation != nil && int64(conversation.TokenDetailsUsedTokens) > estimatedPromptTokens {
		estimatedPromptTokens = int64(conversation.TokenDetailsUsedTokens)
	}
	remainingTokens := int64(0)
	requestMaxTokens := int64(configuredMaxTokens)
	if requestMaxTokens <= 0 {
		requestMaxTokens = providerDefaultMaxOutputTokens
	}
	if contextWindowTokens > 0 && estimatedPromptTokens > 0 {
		remainingTokens = contextWindowTokens - estimatedPromptTokens
		allowedTokens := remainingTokens - providerOutputSafetyTokens
		if allowedTokens < 1 {
			allowedTokens = 1
		}
		if allowedTokens < requestMaxTokens {
			requestMaxTokens = allowedTokens
		}
	}
	maxTokens := int(requestMaxTokens)
	if maxTokens <= 0 {
		maxTokens = 1
	}
	requestKnobs := map[string]any{
		"configured_max_tokens":             configuredMaxTokens,
		"dynamic_max_tokens":                maxTokens,
		"compiled_prompt_tokens_estimate":   estimatedPromptTokens,
		"context_window_tokens":             contextWindowTokens,
		"remaining_context_tokens_estimate": remainingTokens,
		"provider_output_safety_tokens":     providerOutputSafetyTokens,
	}
	return maxTokens, withPreviousCacheFrontierHint(requestKnobs, conversation)
}

func withPreviousCacheFrontierHint(requestKnobs map[string]any, conversation *ConversationFile) map[string]any {
	if len(requestKnobs) == 0 {
		requestKnobs = map[string]any{}
	}
	if conversation == nil || conversation.LatestRequestPrefix == nil {
		return requestKnobs
	}
	prefix := conversation.LatestRequestPrefix
	frontierHash := strings.TrimSpace(prefix.FrontierHash)
	if frontierHash == "" {
		return requestKnobs
	}
	requestKnobs["previous_cache_frontier_hash"] = frontierHash
	requestKnobs["previous_cache_frontier"] = map[string]any{
		"canonical_body_hash": prefix.CanonicalBodyHash,
		"frontier_hash":       frontierHash,
		"frontier_path":       prefix.FrontierPath,
		"breakpoint_count":    prefix.BreakpointCount,
		"request_id":          strings.TrimSpace(prefix.RequestID),
		"model_call_id":       strings.TrimSpace(prefix.ModelCallID),
	}
	return requestKnobs
}

func (service *Service) resolveConfiguredProviderMaxOutputTokens(modelID string) int {
	if service == nil || service.resolver == nil {
		return providerDefaultMaxOutputTokens
	}
	channel, err := service.resolver.SelectChannelForModel(context.Background(), strings.TrimSpace(modelID))
	if err != nil || channel == nil {
		return providerDefaultMaxOutputTokens
	}
	maxTokens := configuredProviderMaxOutputTokens(channel.Provider, channel.MaxTokens, channel.AnthropicMaxTokens)
	if maxTokens <= 0 {
		return providerDefaultMaxOutputTokens
	}
	return maxTokens
}

func configuredProviderMaxOutputTokens(provider string, maxTokens int, anthropicMaxTokens int) int {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		if anthropicMaxTokens > 0 {
			return anthropicMaxTokens
		}
		if maxTokens > 0 {
			return maxTokens
		}
	case "openai":
		if maxTokens > 0 {
			return maxTokens
		}
		if anthropicMaxTokens > 0 {
			return anthropicMaxTokens
		}
	default:
		if maxTokens > 0 && anthropicMaxTokens > 0 {
			if anthropicMaxTokens > maxTokens {
				return anthropicMaxTokens
			}
			return maxTokens
		}
		if maxTokens > 0 {
			return maxTokens
		}
		if anthropicMaxTokens > 0 {
			return anthropicMaxTokens
		}
	}
	return providerDefaultMaxOutputTokens
}

func (service *Service) maybeSaveLastAgentModelHash(conversation *ConversationFile, modelID string, mode agentv1.AgentMode, providerPass int) {
	if service == nil || service.modelMemory == nil || service.resolver == nil {
		return
	}
	if providerPass != 1 || !isSupportedActiveMode(mode) {
		return
	}
	if conversation != nil && strings.TrimSpace(conversation.SubagentTypeName) != "" {
		return
	}
	channel, err := service.resolver.SelectChannelForModel(context.Background(), strings.TrimSpace(modelID))
	if err != nil || channel == nil || strings.TrimSpace(channel.ID) == "" {
		if err != nil {
			log.Printf("forwarder skipped last agent model hash update model_id=%s error=%v", strings.TrimSpace(modelID), err)
		}
		return
	}
	if err := service.modelMemory.SaveLastAgentModelHash(context.Background(), strings.TrimSpace(channel.ID)); err != nil {
		log.Printf("forwarder failed to save last agent model hash channel_id=%s error=%v", strings.TrimSpace(channel.ID), err)
	}
}

func (service *Service) persistDerivedPromptContexts(stream *ActiveStream, conversationID string, requestID string, conversation *ConversationFile, mode agentv1.AgentMode, latestUserText string) (*ConversationFile, error) {
	if stream == nil {
		return nil, fmt.Errorf("active stream is required")
	}
	if service == nil || service.compiler == nil {
		return conversation, nil
	}
	contexts, err := service.compiler.DerivePromptContexts(conversation, mode, latestUserText)
	if err != nil {
		return nil, err
	}
	if len(contexts) == 0 {
		return conversation, nil
	}
	stream.mu.Lock()
	turnSeq := stream.TurnSeq
	stream.mu.Unlock()
	if turnSeq <= 0 {
		return conversation, nil
	}
	entries := make([]HistoryEntry, 0, len(contexts))
	for _, context := range contexts {
		context = normalizePromptContextMessage(context)
		if !isReplayablePromptContext(context) {
			continue
		}
		entries = append(entries, newPromptContextEntry(turnSeq, requestID, context))
	}
	if len(entries) == 0 {
		return conversation, nil
	}
	if _, err := service.appendConversationEntries(stream, conversationID, entries); err != nil {
		return nil, err
	}
	conversation, _, _, err = service.snapshotCheckpointConversation(stream)
	return conversation, err
}

func (service *Service) runProviderStream(stream *ActiveStream, token uint64, ctx context.Context, request ProviderRequest) {
	err := service.provider.StartStream(ctx, request, func(event modeladapter.ModelEvent) error {
		return service.postStreamCommandWait(stream, streamCommand{
			Kind: streamCommandProviderEvent,
			Provider: &streamProviderEvent{
				Token: token,
				Event: event,
			},
		})
	})
	if postErr := service.postStreamCommandWait(stream, streamCommand{
		Kind: streamCommandProviderEvent,
		Provider: &streamProviderEvent{
			Token: token,
			Done:  true,
			Err:   err,
		},
	}); postErr != nil && !errors.Is(postErr, errProviderLoopInterrupted) {
		service.debug.LogProvider(context.Background(), request.RequestID, request.ConversationID, "provider_completion_post_error", map[string]any{
			"model_call_id":  strings.TrimSpace(request.ModelCallID),
			"provider_token": token,
			"error":          postErr.Error(),
		})
		log.Printf(
			"forwarder provider completion post failed request_id=%s model_call_id=%s provider_token=%d err=%v",
			strings.TrimSpace(request.RequestID),
			strings.TrimSpace(request.ModelCallID),
			token,
			postErr,
		)
		_ = service.failStreamIfNonTerminal(stream, "unknown", postErr)
	}
	if err != nil {
		service.debug.LogProvider(context.Background(), request.RequestID, request.ConversationID, "provider_stream_finished", map[string]any{
			"model_call_id":  strings.TrimSpace(request.ModelCallID),
			"provider_token": token,
			"error":          err.Error(),
		})
		return
	}
	service.debug.LogProvider(context.Background(), request.RequestID, request.ConversationID, "provider_stream_finished", map[string]any{
		"model_call_id":  strings.TrimSpace(request.ModelCallID),
		"provider_token": token,
	})
}

// handleToolInvocation 把模型产生的工具意图转成 exec/interaction 请求并下发给客户端。
func (service *Service) handleToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	if err := providerLoopInterruptErr(nil, stream, invocation.ModelCallID); err != nil {
		return err
	}
	invocation = service.rewriteDirectMCPToolInvocation(stream, invocation)
	invocation = service.normalizeCallMCPToolInvocation(stream, invocation)
	trimmedToolName := strings.TrimSpace(invocation.ToolName)
	stream.mu.Lock()
	mode := stream.Mode
	subagentTypeName := ""
	if stream.CheckpointConversation != nil {
		subagentTypeName = strings.TrimSpace(stream.CheckpointConversation.SubagentTypeName)
	}
	stream.ToolInvocationCount++
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if !isToolAllowedInMode(mode, subagentTypeName, trimmedToolName) {
		return service.completePreDispatchToolError(stream, invocation, nil, false, false, fmt.Errorf("tool invocation is not enabled in mode %s: %s", mode.String(), invocation.ToolName))
	}
	var err error
	invocation, err = service.sanitizeCreatePlanInvocationForCurrentPlan(stream, invocation)
	if err != nil {
		if cause, ok := recoverableToolInvocationCause(err); ok {
			return service.completePreDispatchToolError(stream, invocation, nil, false, false, cause)
		}
		return err
	}
	if isPatchEditToolName(trimmedToolName) {
		if err := service.handlePatchEditToolInvocation(stream, invocation); err != nil {
			if cause, ok := recoverableToolInvocationCause(err); ok {
				return service.completePreDispatchToolError(stream, invocation, nil, false, false, cause)
			}
			return err
		}
		return nil
	}
	if trimmedToolName == "Write" {
		if err := service.handleWriteToolInvocation(stream, invocation); err != nil {
			if cause, ok := recoverableToolInvocationCause(err); ok {
				return service.completePreDispatchToolError(stream, invocation, nil, false, false, cause)
			}
			return err
		}
		return nil
	}
	isExecInvocation := isExecTool(trimmedToolName)
	isInteractionInvocation := isInteractionTool(trimmedToolName)
	isLocalStateInvocation := isLocalStateTool(trimmedToolName)
	isImmediateNativeInvocation := isImmediateNativeTool(trimmedToolName)
	if !isExecInvocation && !isInteractionInvocation && !isLocalStateInvocation && !isImmediateNativeInvocation {
		return service.completePreDispatchToolError(stream, invocation, nil, false, false, fmt.Errorf("unsupported tool invocation: %s", invocation.ToolName))
	}
	var subagentOverrides map[string]runtimecore.SubagentModelOverrideSelection
	if isExecInvocation {
		subagentOverrides = cloneSubagentModelOverrides(stream.SubagentModelOverrides)
		if resolutionPayload := taskSubagentModelResolutionPayload(invocation, stream.ModelID, subagentOverrides); resolutionPayload != nil {
			service.debug.LogRuntime(context.Background(), stream.RequestID, stream.ConversationID, "subagent_model_override_resolved", resolutionPayload)
		}
		invocation = rewriteTaskInvocationModelForDisplay(invocation, stream.ModelID, subagentOverrides)
	}
	bufferExecDispatch := isExecInvocation && shouldBufferExecDispatch(invocation.ToolName)
	suppressStartedToolCall := shouldSuppressStartedToolCallAfterPartial(stream, trimmedToolName, invocation.CallID)
	startedToolCall := buildStartedToolCall(invocation)
	startedEmitted := suppressStartedToolCall
	ensureLoopActive := func() error {
		return providerLoopInterruptErr(nil, stream, invocation.ModelCallID)
	}
	if startedToolCall != nil {
		if err := ensureLoopActive(); err != nil {
			return err
		}
		toolCallPayload, err := protojson.Marshal(startedToolCall)
		if err != nil {
			return err
		}
		_, err = service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
			newToolCallEntryWithProviderMetadata(stream.TurnSeq, stream.RequestID, invocation.CallID, invocation.ToolName, invocation.ReasoningContent, invocation.ReasoningSignature, invocation.ReasoningSignatureSource, invocation.ReasoningProviderItemID, invocation.ReasoningProviderStatus, invocation.ReasoningProviderSummary, invocation.ProviderItemID, invocation.ProviderCallID, invocation.ProviderStatus, toolCallPayload),
		})
		if err != nil {
			return err
		}
	}
	if !bufferExecDispatch && !suppressStartedToolCall {
		if err := ensureLoopActive(); err != nil {
			return err
		}
		if err := service.broker.Publish(stream.RequestID, StreamEvent{
			Message: buildToolCallStartedMessage(invocation.CallID, invocation.ModelCallID, startedToolCall),
		}); err != nil {
			return err
		}
		startedEmitted = true
	}
	if isImmediateNativeInvocation {
		return service.handleImmediateNativeToolInvocation(stream, invocation)
	}
	if isLocalStateInvocation {
		return service.handleLocalStateToolInvocation(stream, invocation)
	}
	if isInteractionInvocation {
		if err := service.handleInteractionToolInvocation(stream, invocation); err != nil {
			if cause, ok := recoverableToolInvocationCause(err); ok {
				return service.completePreDispatchToolError(stream, invocation, startedToolCall, startedToolCall != nil, startedEmitted, cause)
			}
			return err
		}
		return nil
	}
	if isExecInvocation {
		serverMessage, pendingExec, err := service.execBridge.OpenExec(execbridge.OpenExecContext{
			ConversationID:         stream.ConversationID,
			ModelID:                stream.ModelID,
			SubagentModelOverrides: subagentOverrides,
		}, invocation)
		if err != nil {
			return service.completePreDispatchToolError(stream, invocation, startedToolCall, startedToolCall != nil, startedEmitted, err)
		}
		pendingExec.ModelCallID = invocation.ModelCallID
		pendingExec.ReasoningContent = invocation.ReasoningContent
		pendingExec.ReasoningSignature = invocation.ReasoningSignature
		pendingExec.ReasoningSignatureSource = invocation.ReasoningSignatureSource
		pendingExec = initializePendingExecForTracking(pendingExec)
		stream.mu.Lock()
		pendingExec.ProviderPass = stream.ProviderPassCount
		stream.PendingExecs[pendingExec.ExecID] = pendingExec
		stream.mu.Unlock()
		service.scheduleShellForegroundRecovery(stream.RequestID, pendingExec)
		removePendingExec := func() {
			stream.mu.Lock()
			delete(stream.PendingExecs, pendingExec.ExecID)
			stream.mu.Unlock()
		}
		if err := ensureLoopActive(); err != nil {
			removePendingExec()
			return err
		}
		if bufferExecDispatch {
			if err := ensureLoopActive(); err != nil {
				removePendingExec()
				return err
			}
			if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: serverMessage}); err != nil {
				removePendingExec()
				return err
			}
			if err := ensureLoopActive(); err != nil {
				removePendingExec()
				return err
			}
			if err := service.broker.Publish(stream.RequestID, StreamEvent{
				Message: buildToolCallStartedMessage(invocation.CallID, invocation.ModelCallID, startedToolCall),
			}); err != nil {
				removePendingExec()
				return err
			}
			startedEmitted = true
			service.recordExecDispatchMetadata(stream, pendingExec, true, startedEmitted, "exec_then_started_then_checkpoint")
			if err := ensureLoopActive(); err != nil {
				removePendingExec()
				return err
			}
			if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
				removePendingExec()
				return err
			}
			return nil
		}
		if err := ensureLoopActive(); err != nil {
			removePendingExec()
			return err
		}
		if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
			removePendingExec()
			return err
		}
		if err := ensureLoopActive(); err != nil {
			removePendingExec()
			return err
		}
		if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: serverMessage}); err != nil {
			removePendingExec()
			return err
		}
		service.recordExecDispatchMetadata(stream, pendingExec, false, startedEmitted, "started_then_checkpoint_then_exec")
		return nil
	}
	return nil
}

func shouldSuppressStartedToolCallAfterPartial(stream *ActiveStream, toolName string, callID string) bool {
	if stream == nil {
		return false
	}
	switch strings.TrimSpace(toolName) {
	case "CreatePlan", "GenerateImage":
	default:
		return false
	}
	trimmedCallID := strings.TrimSpace(callID)
	if trimmedCallID == "" {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.PartialToolCallIDs == nil {
		return false
	}
	_, ok := stream.PartialToolCallIDs[trimmedCallID]
	return ok
}

func (service *Service) recordExecDispatchMetadata(stream *ActiveStream, pending runtimecore.PendingExec, buffered bool, startedEmitted bool, dispatchOrder string) {
	if service == nil || stream == nil {
		return
	}
	toolName := strings.TrimSpace(deriveToolNameFromPendingExec(pending))
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "exec_dispatch", map[string]any{
			"tool_call_id":    pending.ToolCallID,
			"message_id":      pending.MessageID,
			"exec_id":         pending.ExecID,
			"exec_kind":       pending.ExecKind,
			"provider_pass":   pending.ProviderPass,
			"tool_name":       toolName,
			"model_call_id":   pending.ModelCallID,
			"buffered":        buffered,
			"started_emitted": startedEmitted,
			"dispatch_order":  strings.TrimSpace(dispatchOrder),
			"opened_at":       pending.OpenedAt,
		}),
	}); err != nil {
		log.Printf("forwarder exec dispatch metadata failed request_id=%s tool_call_id=%s message_id=%d err=%v", strings.TrimSpace(stream.RequestID), strings.TrimSpace(pending.ToolCallID), pending.MessageID, err)
	}
}

// shouldBufferExecDispatch 把只需要完整参数的快工具改成“先发 exec 请求，再发 started，再发 checkpoint”，
// 避免客户端在参数仍未稳定前过早起计时，同时保留显式的工具开始信号。
func shouldBufferExecDispatch(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "Read", "Grep", "Glob":
		return true
	default:
		return false
	}
}

// appendToolResult 把已完成的工具结果追加到 history，供后续 prompt replay 使用。
//
// reasoning 在已提交 history 中应挂在 assistant_text / tool_call 上。
// tool_result 保存一份 reasoning_content 兜底，replay 只会在缺失 tool_call entry
// 且 reasoning 可回放时用它重建 assistant tool_use，不会把 thinking 复制到工具消息上。
func (service *Service) appendToolResult(stream *ActiveStream, toolCallID string, toolName string, argsJSON []byte, resultText string, reasoningContent string, toolCall *agentv1.ToolCall) error {
	if stream == nil {
		return nil
	}
	var payload json.RawMessage
	if toolCall != nil {
		encoded, err := protojson.Marshal(toolCall)
		if err != nil {
			return err
		}
		payload = encoded
	}
	_, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newToolResultEntry(stream.TurnSeq, stream.RequestID, toolCallID, toolName, string(argsJSON), resultText, reasoningContent, payload),
	})
	return err
}

func (service *Service) publishToolCallCompleted(requestID string, toolCallID string, modelCallID string, toolCall *agentv1.ToolCall) error {
	if strings.TrimSpace(requestID) == "" || strings.TrimSpace(toolCallID) == "" {
		return nil
	}
	return service.broker.Publish(requestID, StreamEvent{
		Message: buildToolCallCompletedMessage(toolCallID, modelCallID, toolCall),
	})
}

func (service *Service) applyExecProgress(stream *ActiveStream, pending runtimecore.PendingExec, message *agentv1.ExecClientMessage) runtimecore.PendingExec {
	if stream == nil || message == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return pending
	}
	shellStream := message.GetShellStream()
	if shellStream == nil {
		return pending
	}

	stream.mu.Lock()
	defer stream.mu.Unlock()
	current, ok := stream.PendingExecs[pending.ExecID]
	if !ok {
		return pending
	}
	now := time.Now().UTC()
	switch event := shellStream.GetEvent().(type) {
	case *agentv1.ShellStream_Stdout:
		if current.FirstChunkAt.IsZero() {
			current.FirstChunkAt = now
		}
		current.ChunkCount++
		current.StreamState = "streaming"
		current.LastShellActivityAt = now
		current.StdoutBuffer += execbridge.DecodeShellStdout(event.Stdout)
	case *agentv1.ShellStream_Stderr:
		if current.FirstChunkAt.IsZero() {
			current.FirstChunkAt = now
		}
		current.ChunkCount++
		current.StreamState = "streaming"
		current.LastShellActivityAt = now
		current.StderrBuffer += event.Stderr.GetData()
	case *agentv1.ShellStream_Start:
		if current.FirstChunkAt.IsZero() {
			current.FirstChunkAt = now
		}
		current.StreamState = "started"
		current.LastShellActivityAt = now
	case *agentv1.ShellStream_Backgrounded:
		current.StreamState = "backgrounded"
		current.LastShellActivityAt = now
	case *agentv1.ShellStream_Exit:
		current.StreamState = "exited"
		current.LastShellActivityAt = now
	case *agentv1.ShellStream_Rejected:
		current.StreamState = "rejected"
		current.LastShellActivityAt = now
	case *agentv1.ShellStream_PermissionDenied:
		current.StreamState = "permission_denied"
		current.LastShellActivityAt = now
	}
	stream.PendingExecs[pending.ExecID] = current
	return current
}

func (service *Service) applyExecControlProgress(stream *ActiveStream, pending runtimecore.PendingExec, message *agentv1.ExecClientControlMessage) runtimecore.PendingExec {
	if stream == nil || message == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return pending
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	current, ok := stream.PendingExecs[pending.ExecID]
	if !ok {
		return pending
	}
	now := time.Now().UTC()
	switch message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_Heartbeat:
		current.LastShellActivityAt = now
		current.LastShellHeartbeatAt = now
	case *agentv1.ExecClientControlMessage_StreamClose:
		current.LastShellActivityAt = now
	case *agentv1.ExecClientControlMessage_Throw:
		current.LastShellActivityAt = now
		current.StreamState = "throw"
	}
	stream.PendingExecs[pending.ExecID] = current
	return current
}

// closeStreamWithProviderError 在真实 LLM/provider 出错时通过 RunSSE 传回错误，并正常结束流。
func (service *Service) closeStreamWithProviderError(
	stream *ActiveStream,
	conversationID string,
	turnSeq int64,
	requestID string,
	accumulatedText string,
	accumulatedReasoning string,
	accumulatedReasoningSignature string,
	accumulatedReasoningSignatureSource string,
	accumulatedReasoningItemID string,
	accumulatedReasoningStatus string,
	accumulatedReasoningSummary json.RawMessage,
	usage turnUsageSnapshot,
	providerErr providerTerminalError,
	allowReasoningOnly bool,
) error {
	if stream == nil {
		return nil
	}
	errorText := strings.TrimSpace(providerErr.Error())
	if errorText == "" {
		errorText = "provider error"
	}
	modelCallID := strings.TrimSpace(stream.CurrentModelCallID)
	if err := service.flushAssistantText(stream, conversationID, turnSeq, requestID, accumulatedText, accumulatedReasoning, accumulatedReasoningSignature, accumulatedReasoningSignatureSource, accumulatedReasoningItemID, accumulatedReasoningStatus, accumulatedReasoningSummary, allowReasoningOnly); err != nil {
		return fmt.Errorf("flush provider error assistant output: %w", err)
	}
	if err := service.recordTurnUsageSnapshot(stream, conversationID, turnSeq, requestID, modelCallID, "provider_error", usage, errorText, false); err != nil {
		return fmt.Errorf("record provider error usage: %w", err)
	}
	if _, err := service.appendConversationEntries(stream, conversationID, []HistoryEntry{
		newMetadataEntry(turnSeq, requestID, "provider_error", map[string]any{
			"model_call_id": modelCallID,
			"error":         errorText,
		}),
	}); err != nil {
		return err
	}
	if err := service.recordTurnFinalizedSnapshot(stream, conversationID, turnSeq, requestID, "provider_error", errorText); err != nil {
		return fmt.Errorf("record provider error turn finalized: %w", err)
	}
	if err := service.updateConversationTokenState(stream, conversationID, usage, modelCallID, false); err != nil {
		return err
	}
	return service.failActiveStream(stream, conversationID, requestID, modelCallID, "provider_error", errorText)
}

func takePendingProviderCompletion(stream *ActiveStream) (pendingTurnCompletion, bool) {
	if stream == nil {
		return pendingTurnCompletion{}, false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.PendingProviderCompletion == nil {
		return pendingTurnCompletion{}, false
	}
	completion := *stream.PendingProviderCompletion
	stream.PendingProviderCompletion = nil
	stream.UpdatedAt = time.Now().UTC()
	return completion, true
}

func pendingBridgeCount(stream *ActiveStream) int {
	if stream == nil {
		return 0
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return len(stream.PendingExecs) + len(stream.PendingInteractions)
}

func (service *Service) finishDeferredTurnAfterInteraction(stream *ActiveStream, pending runtimecore.PendingInteraction) error {
	completion, ok := takePendingProviderCompletion(stream)
	if !ok {
		stream.mu.Lock()
		completion = pendingTurnCompletion{
			ConversationID: stream.ConversationID,
			RequestID:      stream.RequestID,
			TurnSeq:        stream.TurnSeq,
			ModelCallID:    firstNonEmpty(strings.TrimSpace(pending.ModelCallID), strings.TrimSpace(stream.CurrentModelCallID)),
			ProviderPass:   pending.ProviderPass,
		}
		stream.mu.Unlock()
		log.Printf(
			"forwarder missing deferred turn completion snapshot request_id=%s tool_call_id=%s interaction_kind=%s provider_pass=%d",
			strings.TrimSpace(completion.RequestID),
			strings.TrimSpace(pending.ToolCallID),
			strings.TrimSpace(pending.InteractionKind),
			pending.ProviderPass,
		)
	}
	if strings.TrimSpace(completion.ModelCallID) == "" {
		completion.ModelCallID = strings.TrimSpace(pending.ModelCallID)
	}
	if completion.ProviderPass == 0 {
		completion.ProviderPass = pending.ProviderPass
	}
	return service.completeSuccessfulTurn(stream, completion)
}

func (service *Service) completeSuccessfulTurn(stream *ActiveStream, completion pendingTurnCompletion) error {
	if stream == nil {
		return nil
	}
	requestID := firstNonEmpty(strings.TrimSpace(completion.RequestID), strings.TrimSpace(stream.RequestID))
	conversationID := firstNonEmpty(strings.TrimSpace(completion.ConversationID), strings.TrimSpace(stream.ConversationID))
	modelCallID := firstNonEmpty(strings.TrimSpace(completion.ModelCallID), strings.TrimSpace(stream.CurrentModelCallID))
	turnSeq := completion.TurnSeq
	if turnSeq <= 0 {
		turnSeq = stream.TurnSeq
	}
	usage := completion.Usage
	if err := service.recordTurnUsageSnapshot(stream, conversationID, turnSeq, requestID, modelCallID, "completed", usage, "", false); err != nil {
		return fmt.Errorf("record completed turn usage: %w", err)
	}
	if _, err := service.appendConversationEntries(stream, conversationID, []HistoryEntry{
		newMetadataEntry(turnSeq, requestID, "turn_completed", map[string]any{
			"model_call_id": modelCallID,
		}),
	}); err != nil {
		return err
	}
	if err := service.recordTurnFinalizedSnapshot(stream, conversationID, turnSeq, requestID, "completed", ""); err != nil {
		return fmt.Errorf("record completed turn finalized: %w", err)
	}
	if err := service.syncSummaryCarryForward(conversationID, requestID, modelCallID); err != nil {
		log.Printf(
			"forwarder summary sync after turn completion failed request_id=%s model_call_id=%s err=%v",
			strings.TrimSpace(requestID),
			strings.TrimSpace(modelCallID),
			err,
		)
	}
	if err := service.publishCheckpoint(requestID, conversationID); err != nil {
		return err
	}
	if err := service.broker.Publish(requestID, StreamEvent{
		Message: buildTurnEndedMessage(usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens),
	}); err != nil {
		return err
	}
	if err := service.broker.Complete(requestID, "", ""); err != nil {
		return err
	}
	service.setTurnPhase(stream, TurnPhaseCompleted)
	return nil
}

func (service *Service) failStreamIfNonTerminal(stream *ActiveStream, terminalCode string, cause error) error {
	if stream == nil || cause == nil {
		return nil
	}
	stream.mu.Lock()
	terminal := isTerminalStreamStatus(stream.Status)
	stream.mu.Unlock()
	if terminal {
		return nil
	}
	return service.failStream(stream, terminalCode, cause)
}

// publishCheckpoint 按当前内存会话镜像投影出 checkpoint，并广播给所有 RunSSE 订阅者。
func (service *Service) publishCheckpoint(requestID string, _ string) error {
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", requestID)
	}
	conversation, pendingExecs, pendingInteractions, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return err
	}
	state, err := service.projector.ProjectLegacyCheckpoint(conversation)
	if err != nil {
		return err
	}
	state.PendingToolCalls = buildPendingToolCalls(pendingExecs, pendingInteractions)
	service.rewriteCheckpointTokenDetailsForClient(stream, conversation, state)
	return service.broker.Publish(requestID, StreamEvent{
		Message: buildCheckpointMessage(state),
	})
}

func (service *Service) rewriteCheckpointTokenDetailsForClient(stream *ActiveStream, conversation *ConversationFile, state *agentv1.ConversationStateStructure) {
	if state == nil {
		return
	}
	if state.TokenDetails == nil {
		state.TokenDetails = &agentv1.ConversationTokenDetails{}
	}
	state.TokenDetails.MaxTokens = clampInt64ToUint32(service.checkpointDisplayMaxTokens(stream, conversation))
	compiled, hasCompiled := service.checkpointCompiledConversation(stream, conversation)
	state.TokenDetails.UsedTokens = clampInt64ToUint32(service.checkpointDisplayUsedTokens(conversation, state, compiled, hasCompiled))
	state.TokenDetails.Breakdown = estimateCheckpointPromptTokenBreakdown(compiled, hasCompiled, state.TokenDetails.UsedTokens, state.TokenDetails.MaxTokens)
}

func (service *Service) checkpointCompiledConversation(stream *ActiveStream, conversation *ConversationFile) (CompiledConversation, bool) {
	if service == nil || service.compiler == nil || conversation == nil {
		return CompiledConversation{}, false
	}
	_, modelName, latestUserText, mode := checkpointPromptContext(stream)
	compiled, err := service.compiler.Compile(conversation, mode, latestUserText, modelName)
	if err != nil {
		log.Printf("forwarder checkpoint token estimate failed request_id=%s conversation_id=%s err=%v", strings.TrimSpace(activeStreamRequestID(stream)), strings.TrimSpace(conversation.ConversationID), err)
		return CompiledConversation{}, false
	}
	return guardCompiledConversationForProvider(compiled), true
}

func (service *Service) checkpointDisplayMaxTokens(stream *ActiveStream, conversation *ConversationFile) int64 {
	_ = stream
	maxTokens := int64(conversationTokenDetailsMaxTokens(conversation))
	if maxTokens < 1 {
		return 1
	}
	return maxTokens
}

func (service *Service) checkpointDisplayUsedTokens(conversation *ConversationFile, state *agentv1.ConversationStateStructure, compiled CompiledConversation, hasCompiled bool) int64 {
	usedTokens := int64(0)
	if state != nil && state.TokenDetails != nil {
		usedTokens = int64(state.TokenDetails.GetUsedTokens())
	}
	if conversation != nil && int64(conversation.TokenDetailsUsedTokens) > usedTokens {
		usedTokens = int64(conversation.TokenDetailsUsedTokens)
	}
	if hasCompiled {
		if estimatedTokens := estimateCompiledPromptTokens(compiled); estimatedTokens > usedTokens {
			usedTokens = estimatedTokens
		}
	}
	return usedTokens
}

func checkpointPromptContext(stream *ActiveStream) (string, string, string, agentv1.AgentMode) {
	if stream == nil {
		return "", "", "", agentv1.AgentMode_AGENT_MODE_AGENT
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.ModelID, stream.ModelName, stream.LatestUserText, stream.Mode
}

func activeStreamRequestID(stream *ActiveStream) string {
	if stream == nil {
		return ""
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.RequestID
}

// flushAssistantText 把本轮累计的 assistant 文本一次性写回 history。
func (service *Service) flushAssistantText(stream *ActiveStream, conversationID string, turnSeq int64, requestID string, text string, reasoningContent string, reasoningSignature string, reasoningSignatureSource string, reasoningItemID string, reasoningStatus string, reasoningSummary json.RawMessage, allowReasoningOnly bool) error {
	if strings.TrimSpace(text) == "" && (!allowReasoningOnly || !hasReplayableReasoningPayload(reasoningContent, reasoningSignature, reasoningSignatureSource)) {
		return nil
	}
	_, err := service.appendConversationEntries(stream, conversationID, []HistoryEntry{
		newAssistantTextEntryWithProviderMetadata(turnSeq, requestID, text, reasoningContent, reasoningSignature, reasoningSignatureSource, reasoningItemID, reasoningStatus, reasoningSummary),
	})
	return err
}

// failStream 在 provider 或投影失败时把错误写入 history 并收口活动流。
func (service *Service) failStream(stream *ActiveStream, terminalCode string, cause error) error {
	if stream == nil {
		return nil
	}
	errorText := "unknown error"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		errorText = strings.TrimSpace(cause.Error())
	}
	resolvedTerminalCode := resolveTerminalCode(terminalCode, cause)
	metadataType := "failed"
	var providerErr providerTerminalError
	if errors.As(cause, &providerErr) || resolvedTerminalCode == "provider_error" {
		metadataType = "provider_error"
	}
	_, _ = service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, metadataType, map[string]any{
			"error": errorText,
		}),
	})
	return service.failActiveStream(
		stream,
		stream.ConversationID,
		stream.RequestID,
		stream.CurrentModelCallID,
		resolvedTerminalCode,
		errorText,
	)
}

func resolveTerminalCode(fallback string, cause error) string {
	terminalCode := firstNonEmpty(strings.TrimSpace(fallback), "unknown")
	if cause == nil || terminalCode != "unknown" {
		return terminalCode
	}
	var coded interface{ TerminalCode() string }
	if errors.As(cause, &coded) && strings.TrimSpace(coded.TerminalCode()) != "" {
		return strings.TrimSpace(coded.TerminalCode())
	}
	return terminalCode
}

func (service *Service) failActiveStream(stream *ActiveStream, conversationID string, requestID string, modelCallID string, terminalCode string, terminalMessage string) error {
	if stream == nil {
		return nil
	}
	clearPendingProviderCompletion(stream)
	stream.mu.Lock()
	cancel := stream.ProviderCancel
	stream.ProviderActive = false
	stream.ProviderCancel = nil
	stream.PendingProviderAction = providerActionNone
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	service.setTurnPhase(stream, TurnPhaseFailed)
	var firstErr error
	if err := service.syncSummaryCarryForward(conversationID, requestID, modelCallID); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := service.publishCheckpoint(requestID, conversationID); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := service.broker.Fail(requestID, terminalCode, terminalMessage); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// buildRunEntries 构造一次 run intent 需要写入 history 的首批 entry。
func buildRunEntries(intent InboundIntent, effectiveMode agentv1.AgentMode, turnSeq int64) ([]HistoryEntry, error) {
	entries := make([]HistoryEntry, 0, 4)
	if intent.RequestContext != nil {
		normalized := normalizeRequestContextForStorageMode(intent.RequestContext, turnSeq == 1)
		if normalized != nil {
			payload, err := protojson.Marshal(normalized)
			if err != nil {
				return nil, err
			}
			entries = append(entries, HistoryEntry{
				TurnSeq:   turnSeq,
				RequestID: intent.RequestID,
				Role:      "user",
				Kind:      "request_context",
				Payload:   payload,
			})
		}
	}
	if intent.UserMessage != nil {
		payload, err := protojson.Marshal(normalizeUserMessageForStorage(intent.UserMessage))
		if err != nil {
			return nil, err
		}
		entries = append(entries, HistoryEntry{
			TurnSeq:   turnSeq,
			RequestID: intent.RequestID,
			Role:      "user",
			Kind:      "user_message",
			Payload:   payload,
		})
	}
	modeEntry, err := newModeMetadataEntry(turnSeq, intent.RequestID, effectiveMode, intent.HasExplicitMode, intent.ModeSource)
	if err != nil {
		return nil, err
	}
	entries = append(entries,
		modeEntry,
		newMetadataEntry(turnSeq, intent.RequestID, "run_request", buildRunRequestMetadata(intent)),
	)
	if intent.HasExplicitMode {
		entries = append(entries, newModeChangePromptContextEntry(turnSeq, intent.RequestID, effectiveMode))
	}
	return entries, nil
}

func buildRunRequestMetadata(intent InboundIntent) map[string]any {
	return map[string]any{
		"model_id":   intent.ModelID,
		"model_name": intent.ModelName,
		"prewarm":    intent.Prewarm,
	}
}

func newModeMetadataEntry(turnSeq int64, requestID string, mode agentv1.AgentMode, explicit bool, source ModeSource) (HistoryEntry, error) {
	modeAliasValue, err := modeAlias(mode)
	if err != nil {
		return HistoryEntry{}, err
	}
	payload := map[string]any{
		"mode": modeAliasValue,
	}
	if explicit {
		payload["explicit"] = true
	}
	if strings.TrimSpace(string(source)) != "" {
		payload["source"] = strings.TrimSpace(string(source))
	}
	return newMetadataEntry(turnSeq, requestID, "mode", payload), nil
}

func newModeChangePromptContextEntry(turnSeq int64, requestID string, mode agentv1.AgentMode) HistoryEntry {
	modeAliasValue, err := modeAlias(mode)
	if err != nil {
		modeAliasValue = "agent"
	}
	return newPromptContextEntry(turnSeq, requestID, newPromptContextMessage(
		"mode_change",
		modeladapter.Message{
			Role:    "user",
			Content: wrapSystemReminder(fmt.Sprintf("At this point, the active mode changed to %s; follow later mode reminders if present.", modeAliasValue)),
		},
		true,
	))
}

// newAssistantTextEntry 构造 assistant 文本 entry。
func newAssistantTextEntry(turnSeq int64, requestID string, text string, reasoningContent string, reasoningSignature string) HistoryEntry {
	return newAssistantTextEntryWithProviderMetadata(turnSeq, requestID, text, reasoningContent, reasoningSignature, "", "", "", nil)
}

func newAssistantTextEntryWithProviderMetadata(turnSeq int64, requestID string, text string, reasoningContent string, reasoningSignature string, reasoningSignatureSource string, reasoningItemID string, reasoningStatus string, reasoningSummary json.RawMessage) HistoryEntry {
	payload, _ := json.Marshal(assistantTextPayload{
		Text:                     text,
		ReasoningContent:         reasoningContent,
		ReasoningSignature:       strings.TrimSpace(reasoningSignature),
		ReasoningSignatureSource: strings.TrimSpace(reasoningSignatureSource),
		ReasoningItemID:          strings.TrimSpace(reasoningItemID),
		ReasoningStatus:          strings.TrimSpace(reasoningStatus),
		ReasoningSummary:         append(json.RawMessage(nil), reasoningSummary...),
	})
	return HistoryEntry{
		TurnSeq:   turnSeq,
		RequestID: strings.TrimSpace(requestID),
		Role:      "assistant",
		Kind:      "assistant_text",
		Payload:   payload,
	}
}

// newToolCallEntry 构造 tool_call entry。
func newToolCallEntry(turnSeq int64, requestID string, toolCallID string, toolName string, reasoningContent string, reasoningSignature string, toolCall json.RawMessage) HistoryEntry {
	return newToolCallEntryWithProviderMetadata(turnSeq, requestID, toolCallID, toolName, reasoningContent, reasoningSignature, "", "", "", nil, "", "", "", toolCall)
}

func newToolCallEntryWithProviderMetadata(turnSeq int64, requestID string, toolCallID string, toolName string, reasoningContent string, reasoningSignature string, reasoningSignatureSource string, reasoningItemID string, reasoningStatus string, reasoningSummary json.RawMessage, providerItemID string, providerCallID string, providerStatus string, toolCall json.RawMessage) HistoryEntry {
	payload, _ := json.Marshal(toolCallEntryPayload{
		ToolCallID:               strings.TrimSpace(toolCallID),
		ToolName:                 strings.TrimSpace(toolName),
		ReasoningContent:         reasoningContent,
		ReasoningSignature:       strings.TrimSpace(reasoningSignature),
		ReasoningSignatureSource: strings.TrimSpace(reasoningSignatureSource),
		ReasoningItemID:          strings.TrimSpace(reasoningItemID),
		ReasoningStatus:          strings.TrimSpace(reasoningStatus),
		ReasoningSummary:         append(json.RawMessage(nil), reasoningSummary...),
		ProviderItemID:           strings.TrimSpace(providerItemID),
		ProviderCallID:           strings.TrimSpace(providerCallID),
		ProviderStatus:           strings.TrimSpace(providerStatus),
		ToolCall:                 append(json.RawMessage(nil), toolCall...),
	})
	return HistoryEntry{
		TurnSeq:    turnSeq,
		RequestID:  strings.TrimSpace(requestID),
		Role:       "assistant",
		Kind:       "tool_call",
		ToolCallID: strings.TrimSpace(toolCallID),
		Payload:    payload,
	}
}

// newToolResultEntry 构造 tool_result entry。
func newToolResultEntry(turnSeq int64, requestID string, toolCallID string, toolName string, arguments string, resultText string, reasoningContent string, toolCall json.RawMessage) HistoryEntry {
	payload, _ := json.Marshal(toolResultEntryPayload{
		ToolCallID:       strings.TrimSpace(toolCallID),
		ToolName:         strings.TrimSpace(toolName),
		Arguments:        strings.TrimSpace(arguments),
		ResultText:       strings.TrimSpace(resultText),
		ReasoningContent: strings.TrimSpace(reasoningContent),
		ToolCall:         append(json.RawMessage(nil), toolCall...),
	})
	return HistoryEntry{
		TurnSeq:    turnSeq,
		RequestID:  strings.TrimSpace(requestID),
		Role:       "tool",
		Kind:       "tool_result",
		ToolCallID: strings.TrimSpace(toolCallID),
		Payload:    payload,
	}
}

// newMetadataEntry 构造 metadata entry。
func newMetadataEntry(turnSeq int64, requestID string, eventType string, values map[string]any) HistoryEntry {
	payload, _ := json.Marshal(metadataPayload{
		Type:  strings.TrimSpace(eventType),
		Value: values,
	})
	return HistoryEntry{
		TurnSeq:   turnSeq,
		RequestID: strings.TrimSpace(requestID),
		Role:      "system",
		Kind:      "metadata",
		Payload:   payload,
	}
}

// extractUserMessage 从 legacy run_request 中提取用户消息。
func extractUserMessage(message *agentv1.AgentClientMessage) *agentv1.UserMessage {
	if message == nil || message.GetRunRequest() == nil || message.GetRunRequest().GetAction() == nil {
		return nil
	}
	switch item := message.GetRunRequest().GetAction().GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction:
		return item.UserMessageAction.GetUserMessage()
	case *agentv1.ConversationAction_StartPlanAction:
		return item.StartPlanAction.GetUserMessage()
	default:
		return nil
	}
}

// extractRequestContext 从 legacy 请求中提取 request_context。
func extractRequestContext(message *agentv1.AgentClientMessage) *agentv1.RequestContext {
	if message == nil || message.GetRunRequest() == nil || message.GetRunRequest().GetAction() == nil {
		return nil
	}
	switch item := message.GetRunRequest().GetAction().GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction:
		return item.UserMessageAction.GetRequestContext()
	case *agentv1.ConversationAction_ResumeAction:
		return item.ResumeAction.GetRequestContext()
	case *agentv1.ConversationAction_StartPlanAction:
		return item.StartPlanAction.GetRequestContext()
	case *agentv1.ConversationAction_ExecutePlanAction:
		return item.ExecutePlanAction.GetRequestContext()
	default:
		return nil
	}
}

func (service *Service) shouldIgnoreEmptyResumeRunRequest(requestID string, runRequest *agentv1.AgentRunRequest, userMessage *agentv1.UserMessage, requestContext *agentv1.RequestContext) bool {
	if runRequest == nil || !conversationActionIsResume(runRequest.GetAction()) {
		return false
	}
	if userMessage != nil || requestContextHasPayload(requestContext) {
		return false
	}
	state := runRequest.GetConversationState()
	if state != nil && len(state.GetPendingToolCalls()) > 0 {
		return false
	}
	conversationID := strings.TrimSpace(runRequest.GetConversationId())
	if conversationID == "" || service.hasActiveConversationStream(conversationID, requestID) {
		return false
	}
	conversation, err := service.loadConversationForResumeGuard(conversationID)
	if err != nil || conversation == nil {
		return false
	}
	return emptyResumeCanBeIgnoredForConversation(conversation)
}

func requestContextHasPayload(requestContext *agentv1.RequestContext) bool {
	return requestContext != nil && proto.Size(requestContext) > 0
}

func (service *Service) loadConversationForResumeGuard(conversationID string) (*ConversationFile, error) {
	if service == nil || service.store == nil {
		return nil, nil
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, nil
	}
	return service.store.LoadConversation(conversationID)
}

func (service *Service) hasActiveConversationStream(conversationID string, requestID string) bool {
	conversationID = strings.TrimSpace(conversationID)
	if service == nil || service.broker == nil || conversationID == "" {
		return false
	}
	if len(service.broker.OtherConversationRequestIDs(conversationID, requestID)) > 0 {
		return true
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if strings.TrimSpace(stream.ConversationID) != conversationID {
		return false
	}
	if isTerminalStreamStatus(stream.Status) {
		return false
	}
	switch stream.Phase {
	case TurnPhaseCanceled, TurnPhaseCompleted, TurnPhaseFailed:
		return false
	default:
		return true
	}
}

func emptyResumeCanBeIgnoredForConversation(conversation *ConversationFile) bool {
	if conversation == nil {
		return false
	}
	status := strings.TrimSpace(conversation.CurrentLoopStatus)
	currentRequestID := strings.TrimSpace(conversation.CurrentRequestID)
	if status == "" {
		return currentRequestID == ""
	}
	switch status {
	case "completed", "idle":
		return true
	default:
		return false
	}
}

func extractConversationActionUserMessage(action *agentv1.ConversationAction) *agentv1.UserMessage {
	if action == nil {
		return nil
	}
	switch item := action.GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction:
		return item.UserMessageAction.GetUserMessage()
	case *agentv1.ConversationAction_StartPlanAction:
		return item.StartPlanAction.GetUserMessage()
	default:
		return nil
	}
}

func extractConversationActionRequestContext(action *agentv1.ConversationAction) *agentv1.RequestContext {
	if action == nil {
		return nil
	}
	switch item := action.GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction:
		return item.UserMessageAction.GetRequestContext()
	case *agentv1.ConversationAction_ResumeAction:
		return item.ResumeAction.GetRequestContext()
	case *agentv1.ConversationAction_StartPlanAction:
		return item.StartPlanAction.GetRequestContext()
	case *agentv1.ConversationAction_ExecutePlanAction:
		return item.ExecutePlanAction.GetRequestContext()
	default:
		return nil
	}
}

func conversationActionIsResume(action *agentv1.ConversationAction) bool {
	if action == nil {
		return false
	}
	_, ok := action.GetAction().(*agentv1.ConversationAction_ResumeAction)
	return ok
}

func conversationActionStartsRun(action *agentv1.ConversationAction) bool {
	if action == nil {
		return false
	}
	switch action.GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction,
		*agentv1.ConversationAction_ResumeAction,
		*agentv1.ConversationAction_StartPlanAction,
		*agentv1.ConversationAction_ExecutePlanAction:
		return true
	default:
		return false
	}
}

// extractRunMode 推导本轮应使用的 mode。
func extractRunMode(message *agentv1.AgentClientMessage) (agentv1.AgentMode, ModeSource, bool, error) {
	if userMessage := extractUserMessage(message); userMessage != nil && userMessage.GetMode() != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
		return resolveExplicitMode(userMessage.GetMode(), ModeSourceUserMessage)
	}
	if message != nil && message.GetRunRequest() != nil && message.GetRunRequest().GetAction() != nil {
		if item, ok := message.GetRunRequest().GetAction().GetAction().(*agentv1.ConversationAction_ExecutePlanAction); ok && item.ExecutePlanAction != nil {
			if mode := item.ExecutePlanAction.GetExecutionMode(); mode != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
				return resolveExplicitMode(mode, ModeSourceExecutePlanAction)
			}
		}
	}
	if message != nil && message.GetRunRequest() != nil && message.GetRunRequest().GetConversationState() != nil {
		if mode := message.GetRunRequest().GetConversationState().GetMode(); mode != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
			return resolveExplicitMode(mode, ModeSourceConversationState)
		}
	}
	return agentv1.AgentMode_AGENT_MODE_AGENT, ModeSourceUnknown, false, nil
}

func extractPrewarmMode(request *agentv1.PrewarmRequest) (agentv1.AgentMode, ModeSource, bool, error) {
	if request == nil || request.GetConversationState() == nil {
		return agentv1.AgentMode_AGENT_MODE_AGENT, ModeSourceUnknown, false, nil
	}
	mode := request.GetConversationState().GetMode()
	if mode == agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
		return agentv1.AgentMode_AGENT_MODE_AGENT, ModeSourceUnknown, false, nil
	}
	return resolveExplicitMode(mode, ModeSourceConversationState)
}

func extractConversationActionMode(action *agentv1.ConversationAction) (agentv1.AgentMode, ModeSource, bool, error) {
	if userMessage := extractConversationActionUserMessage(action); userMessage != nil && userMessage.GetMode() != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
		return resolveExplicitMode(userMessage.GetMode(), ModeSourceUserMessage)
	}
	if action == nil {
		return agentv1.AgentMode_AGENT_MODE_AGENT, ModeSourceUnknown, false, nil
	}
	switch item := action.GetAction().(type) {
	case *agentv1.ConversationAction_ExecutePlanAction:
		if item.ExecutePlanAction != nil && item.ExecutePlanAction.GetExecutionMode() != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
			return resolveExplicitMode(item.ExecutePlanAction.GetExecutionMode(), ModeSourceExecutePlanAction)
		}
	}
	return agentv1.AgentMode_AGENT_MODE_AGENT, ModeSourceUnknown, false, nil
}

// extractRequestedModelID 提取本轮显式请求的模型 ID。
func extractRequestedModelID(message *agentv1.AgentClientMessage) string {
	if message == nil {
		return ""
	}
	if runRequest := message.GetRunRequest(); runRequest != nil {
		return firstNonEmpty(extractRequestedModelIDFromRequestedModel(runRequest.GetRequestedModel()), runRequest.GetModelDetails().GetModelId())
	}
	if prewarm := message.GetPrewarmRequest(); prewarm != nil {
		return firstNonEmpty(extractRequestedModelIDFromRequestedModel(prewarm.GetRequestedModel()), prewarm.GetModelDetails().GetModelId())
	}
	return ""
}

func extractRequestedModelIDFromRequestedModel(model *agentv1.RequestedModel) string {
	if model == nil {
		return ""
	}
	if model.GetIsVariantStringRepresentation() {
		modelID, _ := splitRuntimeThinkingEffortVariantString(model.GetModelId())
		return modelID
	}
	return strings.TrimSpace(model.GetModelId())
}

func extractRuntimeThinkingEffort(message *agentv1.AgentClientMessage) string {
	if message == nil {
		return ""
	}
	if runRequest := message.GetRunRequest(); runRequest != nil {
		return extractRuntimeThinkingEffortFromRequestedModel(runRequest.GetRequestedModel())
	}
	if prewarm := message.GetPrewarmRequest(); prewarm != nil {
		return extractRuntimeThinkingEffortFromRequestedModel(prewarm.GetRequestedModel())
	}
	return ""
}

func extractRuntimeThinkingEffortFromRequestedModel(model *agentv1.RequestedModel) string {
	if model == nil {
		return ""
	}
	for _, parameter := range model.GetParameters() {
		if parameter == nil || !isRuntimeThinkingEffortParameterID(parameter.GetId()) {
			continue
		}
		if effort := normalizeRuntimeThinkingEffort(parameter.GetValue()); effort != "" {
			return effort
		}
	}
	if model.GetIsVariantStringRepresentation() {
		if _, effort := splitRuntimeThinkingEffortVariantString(model.GetModelId()); effort != "" {
			return effort
		}
		return normalizeRuntimeThinkingEffort(model.GetModelId())
	}
	return ""
}

func isRuntimeThinkingEffortParameterID(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case runtimeThinkingEffortParameterID,
		"reasoning",
		"reasoning_effort",
		"thinking_intensity",
		"anthropic_thinking_effort",
		"openai_reasoning_effort":
		return true
	default:
		return false
	}
}

func normalizeRuntimeThinkingEffort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "disabled", "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(raw))
	case "disable", "off", "none", "false", "no", "0":
		return "disabled"
	case "very_high", "very-high", "veryhigh", "x-high", "extra_high", "extra-high", "extrahigh":
		return "xhigh"
	case "maximum":
		return "max"
	default:
		return ""
	}
}

func splitRuntimeThinkingEffortVariantString(raw string) (string, string) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", ""
	}
	if effort := normalizeRuntimeThinkingEffort(text); effort != "" {
		return "", effort
	}
	index := strings.LastIndex(text, ":")
	if index <= 0 || index >= len(text)-1 {
		return "", ""
	}
	modelID := strings.TrimSpace(text[:index])
	effort := normalizeRuntimeThinkingEffort(text[index+1:])
	if modelID == "" || effort == "" {
		return "", ""
	}
	return modelID, effort
}

func (service *Service) resolveRequestedModelName(message *agentv1.AgentClientMessage, modelID string) string {
	if message != nil {
		if runRequest := message.GetRunRequest(); runRequest != nil {
			if name := firstNonEmpty(
				runRequest.GetModelDetails().GetDisplayName(),
				runRequest.GetModelDetails().GetDisplayModelId(),
			); name != "" {
				return name
			}
		}
		if prewarm := message.GetPrewarmRequest(); prewarm != nil {
			if name := firstNonEmpty(
				prewarm.GetModelDetails().GetDisplayName(),
				prewarm.GetModelDetails().GetDisplayModelId(),
			); name != "" {
				return name
			}
		}
	}
	if service != nil && service.resolver != nil {
		channel, err := service.resolver.SelectChannelForModel(context.Background(), strings.TrimSpace(modelID))
		if err == nil && channel != nil {
			if name := firstNonEmpty(channel.Name, channel.Model); name != "" {
				return name
			}
		}
	}
	return strings.TrimSpace(modelID)
}

func (service *Service) resolveContextWindowTokens(modelID string) uint32 {
	if service == nil || service.resolver == nil {
		return projectedConversationMaxTokens
	}
	channel, err := service.resolver.SelectChannelForModel(context.Background(), strings.TrimSpace(modelID))
	if err != nil || channel == nil || channel.ContextWindowTokens <= 0 {
		return projectedConversationMaxTokens
	}
	return clampInt64ToUint32(int64(channel.ContextWindowTokens))
}

func (service *Service) syncConversationContextWindowTokens(stream *ActiveStream, conversationID string, conversation *ConversationFile) (*ConversationFile, error) {
	if stream == nil || conversation == nil {
		return conversation, nil
	}
	stream.mu.Lock()
	modelID := stream.ModelID
	stream.mu.Unlock()
	target := service.resolveContextWindowTokens(modelID)
	if target == 0 || conversation.TokenDetailsMaxTokens == target {
		return conversation, nil
	}
	return service.updateConversationMetaAndCheckpoint(stream, conversationID, func(item *ConversationFile) error {
		if item == nil {
			return nil
		}
		item.TokenDetailsMaxTokens = target
		return nil
	})
}

// userMessageText 返回用户消息中的纯文本。
func userMessageText(message *agentv1.UserMessage) string {
	if message == nil {
		return ""
	}
	return strings.TrimSpace(message.GetText())
}

func currentProviderPass(stream *ActiveStream) int {
	if stream == nil {
		return 0
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.ProviderPassCount
}

func currentStreamMode(stream *ActiveStream) agentv1.AgentMode {
	if stream == nil {
		return agentv1.AgentMode_AGENT_MODE_AGENT
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if normalized, err := validateSupportedActiveMode(stream.Mode); err == nil {
		return normalized
	}
	return stream.Mode
}

// selectPendingExec 按 exec_id 或 message_id 在当前流里查找挂起执行桥。
func selectPendingExec(execID string, messageID uint32, stream *ActiveStream) (runtimecore.PendingExec, bool) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if item, ok := stream.PendingExecs[strings.TrimSpace(execID)]; ok {
		return item, true
	}
	if messageID != 0 {
		for _, item := range stream.PendingExecs {
			if item.MessageID == messageID {
				return item, true
			}
		}
	}
	return runtimecore.PendingExec{}, false
}

func selectPendingInteraction(message *agentv1.InteractionResponse, stream *ActiveStream) (runtimecore.PendingInteraction, bool) {
	if stream == nil || message == nil {
		return runtimecore.PendingInteraction{}, false
	}
	interactionID := fmt.Sprintf("%d", message.GetId())
	stream.mu.Lock()
	defer stream.mu.Unlock()
	item, ok := stream.PendingInteractions[interactionID]
	return item, ok
}

// selectPendingExecByControl 根据控制消息的桥消息 ID 查找挂起执行桥。
func selectPendingExecByControl(message *agentv1.ExecClientControlMessage, stream *ActiveStream) (runtimecore.PendingExec, bool) {
	messageID, ok := execControlMessageID(message)
	if !ok {
		return runtimecore.PendingExec{}, false
	}
	return selectPendingExec("", messageID, stream)
}

func execControlMessageID(message *agentv1.ExecClientControlMessage) (uint32, bool) {
	if message == nil {
		return 0, false
	}
	switch item := message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_StreamClose:
		return item.StreamClose.GetId(), true
	case *agentv1.ExecClientControlMessage_Throw:
		return item.Throw.GetId(), true
	case *agentv1.ExecClientControlMessage_Heartbeat:
		return item.Heartbeat.GetId(), true
	default:
		return 0, false
	}
}

func shouldIgnoreMissingExecResult(message *agentv1.ExecClientMessage, stream *ActiveStream) bool {
	if message == nil {
		return false
	}
	return recentlyCompletedExecExists(stream, message.GetId())
}

func shouldIgnoreMissingExecControl(message *agentv1.ExecClientControlMessage, stream *ActiveStream) bool {
	if shouldIgnoreStaleExecControl(message) {
		return true
	}
	messageID, ok := execControlMessageID(message)
	if !ok {
		return false
	}
	return recentlyCompletedExecExists(stream, messageID)
}

func shouldIgnoreStaleExecControl(message *agentv1.ExecClientControlMessage) bool {
	if message == nil {
		return false
	}
	switch message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_Heartbeat, *agentv1.ExecClientControlMessage_StreamClose:
		// Reconnecting Cursor clients may keep sending transport-level exec
		// heartbeats / close acks after the original in-memory pending state is gone.
		// Treat these as idempotent noise instead of surfacing protocol 400s.
		return true
	default:
		return false
	}
}

type pendingAssistantMessage struct {
	ID      string                    `json:"id,omitempty"`
	Role    string                    `json:"role,omitempty"`
	Content []pendingAssistantContent `json:"content,omitempty"`
}

type pendingAssistantContent struct {
	Type       string          `json:"type,omitempty"`
	Text       string          `json:"text,omitempty"`
	Signature  string          `json:"signature,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
}

type pendingToolCallReplay struct {
	OpenedAt time.Time
	SortKey  string
	Raw      string
}

func buildPendingToolCalls(pendingExecs []runtimecore.PendingExec, pendingInteractions []runtimecore.PendingInteraction) []string {
	if len(pendingExecs) == 0 && len(pendingInteractions) == 0 {
		return nil
	}

	items := make([]pendingToolCallReplay, 0, len(pendingExecs)+len(pendingInteractions))
	for _, pending := range pendingExecs {
		raw, ok := encodePendingExecAsAssistantOutput(pending)
		if !ok {
			continue
		}
		items = append(items, pendingToolCallReplay{
			OpenedAt: pending.OpenedAt,
			SortKey:  fmt.Sprintf("exec-%020d", pending.MessageID),
			Raw:      raw,
		})
	}
	for _, pending := range pendingInteractions {
		raw, ok := encodePendingInteractionAsAssistantOutput(pending)
		if !ok {
			continue
		}
		items = append(items, pendingToolCallReplay{
			OpenedAt: pending.OpenedAt,
			SortKey:  "interaction-" + strings.TrimSpace(pending.InteractionID),
			Raw:      raw,
		})
	}
	if len(items) == 0 {
		return nil
	}

	sort.SliceStable(items, func(i, j int) bool {
		left := items[i]
		right := items[j]
		switch {
		case left.OpenedAt.Equal(right.OpenedAt):
			return left.SortKey < right.SortKey
		case left.OpenedAt.IsZero():
			return false
		case right.OpenedAt.IsZero():
			return true
		default:
			return left.OpenedAt.Before(right.OpenedAt)
		}
	})

	encoded := make([]string, 0, len(items))
	for _, item := range items {
		encoded = append(encoded, item.Raw)
	}
	return encoded
}

func encodePendingExecAsAssistantOutput(pending runtimecore.PendingExec) (string, bool) {
	toolCallID := strings.TrimSpace(pending.ToolCallID)
	toolName, argsJSON, ok := pendingAssistantToolShape(pending)
	if toolCallID == "" || !ok || strings.TrimSpace(toolName) == "" {
		return "", false
	}

	payload, err := json.Marshal(pendingAssistantMessage{
		ID:      "1",
		Role:    "assistant",
		Content: buildPendingAssistantContents(pending.ReasoningContent, pending.ReasoningSignature, toolCallID, toolName, argsJSON),
	})
	if err != nil {
		return "", false
	}
	return string(payload), true
}

func encodePendingInteractionAsAssistantOutput(pending runtimecore.PendingInteraction) (string, bool) {
	toolCallID := strings.TrimSpace(pending.ToolCallID)
	toolName := strings.TrimSpace(deriveToolNameFromPendingInteraction(pending))
	if toolCallID == "" || toolName == "" {
		return "", false
	}
	payload, err := json.Marshal(pendingAssistantMessage{
		ID:      "1",
		Role:    "assistant",
		Content: buildPendingAssistantContents(pending.ReasoningContent, pending.ReasoningSignature, toolCallID, toolName, pending.ArgsJSON),
	})
	if err != nil {
		return "", false
	}
	return string(payload), true
}

func buildPendingAssistantContents(reasoningContent string, reasoningSignature string, toolCallID string, toolName string, argsJSON []byte) []pendingAssistantContent {
	items := make([]pendingAssistantContent, 0, 2)
	if strings.TrimSpace(reasoningContent) != "" {
		items = append(items, pendingAssistantContent{
			Type:      "reasoning",
			Text:      reasoningContent,
			Signature: strings.TrimSpace(reasoningSignature),
		})
	}
	items = append(items, pendingAssistantContent{
		Type:       "tool-call",
		ToolCallID: toolCallID,
		ToolName:   strings.TrimSpace(toolName),
		Args:       append(json.RawMessage(nil), argsJSON...),
	})
	return items
}

func pendingAssistantToolShape(pending runtimecore.PendingExec) (string, []byte, bool) {
	switch strings.TrimSpace(pending.ExecKind) {
	case patchEditReadExecKindName, patchEditWriteExecKindName, patchEditPostReadExecKindName:
		payload, err := decodePendingPatchEditPayload(pending.ArgsJSON)
		if err != nil {
			return "", nil, false
		}
		argsJSON, err := patchEditPayloadArgsJSON(payload)
		if err != nil {
			return "", nil, false
		}
		return firstNonEmpty(strings.TrimSpace(payload.ToolName), patchEditToolName), argsJSON, true
	case writeReadExecKind, writeWriteExecKind, writePostReadExecKind:
		payload, err := decodePendingWritePayload(pending.ArgsJSON)
		if err != nil {
			return "", nil, false
		}
		argsJSON, err := payload.VisibleArgs.MarshalJSON()
		if err != nil {
			return "", nil, false
		}
		return "Write", argsJSON, true
	default:
		toolName := strings.TrimSpace(deriveToolNameFromPendingExec(pending))
		if toolName == "" {
			return "", nil, false
		}
		return toolName, append([]byte(nil), pending.ArgsJSON...), true
	}
}

// markExecCompleted 保留一个短时 tombstone，避免迟到的 transport-level control 被误判为协议错误。
func markExecCompleted(stream *ActiveStream, pending runtimecore.PendingExec) {
	if stream == nil {
		return
	}
	now := time.Now().UTC()
	cutoff := now.Add(-completedExecRetention)

	stream.mu.Lock()
	delete(stream.PendingExecs, pending.ExecID)
	if pending.MessageID != 0 {
		if stream.RecentCompletedExecs == nil {
			stream.RecentCompletedExecs = make(map[uint32]time.Time)
		}
		for messageID, completedAt := range stream.RecentCompletedExecs {
			if completedAt.Before(cutoff) {
				delete(stream.RecentCompletedExecs, messageID)
			}
		}
		stream.RecentCompletedExecs[pending.MessageID] = now
	}
	stream.UpdatedAt = now
	stream.mu.Unlock()
}

func recentlyCompletedExecExists(stream *ActiveStream, messageID uint32) bool {
	if stream == nil || messageID == 0 {
		return false
	}
	now := time.Now().UTC()
	cutoff := now.Add(-completedExecRetention)

	stream.mu.Lock()
	defer stream.mu.Unlock()
	if len(stream.RecentCompletedExecs) == 0 {
		return false
	}
	completedAt, ok := stream.RecentCompletedExecs[messageID]
	for id, ts := range stream.RecentCompletedExecs {
		if ts.Before(cutoff) {
			delete(stream.RecentCompletedExecs, id)
		}
	}
	if !ok {
		return false
	}
	if completedAt.Before(cutoff) {
		delete(stream.RecentCompletedExecs, messageID)
		return false
	}
	return true
}

func (service *Service) updateStreamMCPToolServers(stream *ActiveStream, requestContext *agentv1.RequestContext) {
	if stream == nil {
		return
	}
	servers := collectMCPToolServers(requestContext)
	if len(servers) == 0 {
		return
	}
	stream.mu.Lock()
	if stream.MCPToolServers == nil {
		stream.MCPToolServers = make(map[string]string, len(servers))
	}
	for toolName, serverIdentifier := range servers {
		trimmedToolName := strings.TrimSpace(toolName)
		trimmedServerIdentifier := strings.TrimSpace(serverIdentifier)
		if trimmedToolName == "" || trimmedServerIdentifier == "" {
			continue
		}
		stream.MCPToolServers[trimmedToolName] = trimmedServerIdentifier
	}
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}

func (service *Service) rewriteDirectMCPToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) runtimecore.ToolInvocation {
	toolName := strings.TrimSpace(invocation.ToolName)
	if toolName == "" || isExecTool(toolName) {
		return invocation
	}
	serverIdentifier := lookupMCPToolServer(stream, toolName)
	if serverIdentifier == "" {
		return invocation
	}

	arguments := make(map[string]any)
	if len(invocation.ArgsJSON) > 0 {
		_ = json.Unmarshal(invocation.ArgsJSON, &arguments)
	}
	payload := struct {
		Server    string         `json:"server"`
		ToolName  string         `json:"toolName"`
		Arguments map[string]any `json:"arguments,omitempty"`
	}{
		Server:    serverIdentifier,
		ToolName:  toolName,
		Arguments: arguments,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return invocation
	}
	invocation.ToolName = "CallMcpTool"
	invocation.ArgsJSON = encoded
	return invocation
}

func (service *Service) normalizeCallMCPToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) runtimecore.ToolInvocation {
	if strings.TrimSpace(invocation.ToolName) != "CallMcpTool" {
		return invocation
	}

	payload, err := runtimecore.DecodeMCPToolPayload(invocation.ArgsJSON)
	if err != nil {
		return invocation
	}

	serverIdentifier := firstNonEmpty(payload.Server, payload.ProviderIdentifier)
	toolName := strings.TrimSpace(payload.ToolName)
	name := strings.TrimSpace(payload.Name)
	if toolName == "" {
		toolName = runtimecore.InferMCPToolName(serverIdentifier, name)
	}
	if serverIdentifier == "" {
		serverIdentifier = lookupMCPToolServer(stream, toolName)
		if serverIdentifier == "" && name != "" {
			serverIdentifier = runtimecore.InferMCPServerIdentifier(name)
		}
	}

	if toolName == "" {
		return invocation
	}

	normalized := struct {
		Server    string         `json:"server"`
		ToolName  string         `json:"toolName"`
		Arguments map[string]any `json:"arguments,omitempty"`
	}{
		Server:    serverIdentifier,
		ToolName:  toolName,
		Arguments: payload.Arguments,
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return invocation
	}
	invocation.ArgsJSON = encoded
	return invocation
}

func lookupMCPToolServer(stream *ActiveStream, toolName string) string {
	trimmedToolName := strings.TrimSpace(toolName)
	if trimmedToolName == "" {
		return ""
	}
	if stream != nil {
		stream.mu.Lock()
		serverIdentifier := strings.TrimSpace(stream.MCPToolServers[trimmedToolName])
		stream.mu.Unlock()
		if serverIdentifier != "" {
			return serverIdentifier
		}
	}
	return ""
}

func readStringAny(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func readMapAny(value any) map[string]any {
	switch item := value.(type) {
	case map[string]any:
		return item
	case nil:
		return map[string]any{}
	default:
		return map[string]any{}
	}
}

// inferToolName 从完整 ToolCall proto 中反推出 canonical 工具名。
func inferToolName(toolCall *agentv1.ToolCall) string {
	if toolCall == nil || toolCall.GetTool() == nil {
		return ""
	}
	switch toolCall.GetTool().(type) {
	case *agentv1.ToolCall_ReadToolCall:
		return "Read"
	case *agentv1.ToolCall_UpdateTodosToolCall:
		return "TodoWrite"
	case *agentv1.ToolCall_ReadTodosToolCall:
		return "ReadTodos"
	case *agentv1.ToolCall_DeleteToolCall:
		return "Delete"
	case *agentv1.ToolCall_GrepToolCall:
		return "Grep"
	case *agentv1.ToolCall_GlobToolCall:
		return "Glob"
	case *agentv1.ToolCall_ShellToolCall:
		return "Shell"
	case *agentv1.ToolCall_AwaitToolCall:
		return "AwaitShell"
	case *agentv1.ToolCall_WriteShellStdinToolCall:
		return "WriteShellStdin"
	case *agentv1.ToolCall_EditToolCall:
		return inferEditToolNameFromToolCall(toolCall.GetEditToolCall())
	case *agentv1.ToolCall_LsToolCall:
		return "Ls"
	case *agentv1.ToolCall_McpToolCall:
		return "CallMcpTool"
	case *agentv1.ToolCall_ListMcpResourcesToolCall:
		return "ListMcpResources"
	case *agentv1.ToolCall_ReadMcpResourceToolCall:
		return "FetchMcpResource"
	case *agentv1.ToolCall_CreatePlanToolCall:
		return "CreatePlan"
	case *agentv1.ToolCall_AskQuestionToolCall:
		return "AskQuestion"
	case *agentv1.ToolCall_WebSearchToolCall:
		return "WebSearch"
	case *agentv1.ToolCall_WebFetchToolCall:
		return "WebFetch"
	case *agentv1.ToolCall_SwitchModeToolCall:
		return "SwitchMode"
	case *agentv1.ToolCall_GenerateImageToolCall:
		return "GenerateImage"
	case *agentv1.ToolCall_TaskToolCall:
		return "Task"
	default:
		return ""
	}
}

// deriveToolNameFromPendingExec 根据执行桥种类反推出 canonical 工具名。
func deriveToolNameFromPendingExec(pending runtimecore.PendingExec) string {
	switch strings.TrimSpace(pending.ExecKind) {
	case "read":
		return "Read"
	case "write":
		return "Write"
	case "delete":
		return "Delete"
	case "glob":
		return "Glob"
	case "grep":
		return "Grep"
	case "diagnostics":
		return "ReadLints"
	case "ls":
		return "Ls"
	case "mcp":
		return "CallMcpTool"
	case "list_mcp_resources":
		return "ListMcpResources"
	case "read_mcp_resource":
		return "FetchMcpResource"
	case "shell":
		return "Shell"
	case "await_shell":
		return "AwaitShell"
	case "write_shell_stdin":
		return "WriteShellStdin"
	case "force_background_shell":
		return "ForceBackgroundShell"
	case "subagent":
		return "Task"
	default:
		return ""
	}
}

func execKindFromToolName(name string) (string, bool) {
	switch strings.TrimSpace(name) {
	case "Read":
		return "read", true
	case "Write":
		return "write", true
	case "PatchEdit":
		return "patch_edit", true
	case "Delete":
		return "delete", true
	case "Glob":
		return "glob", true
	case "Grep":
		return "grep", true
	case "Ls":
		return "ls", true
	case "ReadLints":
		return "diagnostics", true
	case "CallMcpTool":
		return "mcp", true
	case "FetchMcpResource":
		return "read_mcp_resource", true
	case "Shell":
		return "shell", true
	case "AwaitShell":
		return "await_shell", true
	case "WriteShellStdin":
		return "write_shell_stdin", true
	case "ForceBackgroundShell":
		return "force_background_shell", true
	case "Task":
		return "subagent", true
	default:
		return "", false
	}
}

func isExecTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "Read", "Write", "PatchEdit", "Delete", "Shell", "WriteShellStdin", "ForceBackgroundShell", "Grep", "Glob", "Ls", "ReadLints", "CallMcpTool", "FetchMcpResource", "Task":
		return true
	default:
		return false
	}
}

func inferEditToolName(args *agentv1.EditArgs) string {
	if args != nil && args.StreamContent != nil {
		return "Write"
	}
	return "Edit"
}

func inferEditToolNameFromToolCall(toolCall *agentv1.EditToolCall) string {
	if toolCall == nil {
		return ""
	}
	if editResultLooksLikeStructuredEdit(toolCall.GetResult()) {
		return "Edit"
	}
	return inferEditToolName(toolCall.GetArgs())
}

func editResultLooksLikeStructuredEdit(result *agentv1.EditResult) bool {
	success := result.GetSuccess()
	if success == nil {
		return false
	}
	return success.BeforeFullFileContent != nil || success.DiffString != nil
}

// buildTerminalStreamError 把 broker 终态事件转换成 Connect endstream 错误。
func buildTerminalStreamError(event StreamEvent) error {
	if !event.End {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(event.TerminalErrorCode)) {
	case "":
		return nil
	case "canceled":
		return connect.NewError(connect.CodeCanceled, errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	case "invalid_argument":
		return connect.NewError(connect.CodeInvalidArgument, errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	case "failed_precondition":
		return connect.NewError(connect.CodeFailedPrecondition, errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	case compactionOverflowTerminalCode:
		return buildRunSSECustomError(connect.CodeInvalidArgument, "Context Too Large After Compaction", errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	case "provider_error":
		return buildRunSSEProviderError(errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	default:
		return connect.NewError(connect.CodeUnknown, errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	}
}

// buildRunSSEProviderError 构造 provider 专用的 RunSSE 错误包。
func buildRunSSEProviderError(cause error) error {
	return buildRunSSEStructuredErrorWithDetail(
		connect.CodeUnavailable,
		"Server Error",
		"",
		cause,
		aiserverv1.ErrorDetails_ERROR_PROVIDER_ERROR,
		false,
	)
}

// buildRunSSECustomError 构造带有 CustomErrorDetails 的 RunSSE 结构化错误。
func buildRunSSECustomError(code connect.Code, title string, cause error) error {
	return buildRunSSEStructuredErrorWithDetail(code, title, "", cause, aiserverv1.ErrorDetails_ERROR_CUSTOM_MESSAGE, false)
}

// buildRunSSEStructuredError 统一构造带有 ErrorDetails 的 Connect endstream 错误。
func buildRunSSEStructuredErrorWithDetail(code connect.Code, title string, detailText string, cause error, errorKind aiserverv1.ErrorDetails_Error, expected bool) error {
	if cause == nil {
		cause = fmt.Errorf("unknown RunSSE error")
	}
	trimmedDetail := strings.TrimSpace(detailText)
	if trimmedDetail == "" {
		trimmedDetail = cause.Error()
	}
	isRetryable := true
	allowUnsafeCommandLinks := true
	showRequestID := true
	shouldShowImmediateError := false
	isExpected := expected
	payload := &aiserverv1.ErrorDetails{
		Error: errorKind,
		Details: &aiserverv1.CustomErrorDetails{
			Title:       strings.TrimSpace(title),
			Detail:      trimmedDetail,
			IsRetryable: &isRetryable,
			AllowCommandLinksPotentiallyUnsafePleaseOnlyUseForHandwrittenTrustedMarkdown: &allowUnsafeCommandLinks,
			ShowRequestId:            &showRequestID,
			ShouldShowImmediateError: &shouldShowImmediateError,
		},
		IsExpected: &isExpected,
	}
	result := connect.NewError(code, cause)
	detail, detailErr := connect.NewErrorDetail(payload)
	if detailErr == nil {
		result.AddDetail(detail)
	}
	return result
}
