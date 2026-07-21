// service.go 实现 forwarder 的主链路：Bidi 上行归一化、history 写入、provider 驱动和 RunSSE 下行。
package forwarder

import (
	"context"
	"cursor/internal/logger"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	"cursor/internal/appdata"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	interactionbridge "cursor/internal/backend/agent/bridge/interaction"
	modeladapter "cursor/internal/backend/agent/model"
	protocol "cursor/internal/backend/agent/protocol"
	cacheruntime "cursor/internal/backend/runtime/cache"
	contextruntime "cursor/internal/backend/runtime/context"
	optimize "cursor/internal/backend/runtime/optimize"
	toolruntime "cursor/internal/backend/runtime/tool"
	vm "cursor/internal/backend/virtualmodel"
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
	optimize           *optimize.Runtime
	toolRuntime        *toolruntime.Runtime
	contextRuntime     *contextruntime.Runtime

	// Phase 26c: per-stream AOS result registries for collecting spawned
	// AOS member Task tool results. Keyed by stream.RequestID.
	aosRegistries   map[string]*vm.AOSResultRegistry
	aosRegistriesMu sync.Mutex

	// R15 lifecycle unification: stopCh signals all background goroutines
	// (history maintenance, etc.) to exit; wg tracks them so Shutdown can
	// wait for clean drain. shutdownOnce guarantees Shutdown is idempotent.
	stopCh        chan struct{}
	wg            sync.WaitGroup
	shutdownOnce  sync.Once
	shutdownState atomic.Bool

	// onModelActivity 是模型活动状态回调（由 Host 注入，供 pet 桥接订阅）。
	// forwarder 在 stream 关键节点（思考/正文/完成/错误）调用它通知外部，
	// 不直接依赖 application/pet 包，保持低耦合。
	// state 取值：thinking | working | idle | error
	onModelActivityMu sync.RWMutex
	onModelActivity   func(state string)

	// lastEmittedActivity 记录上次 emit 的状态，避免状态未变化时重复回调
	// （每个 chunk 都 emit 会让 pet 抖动）。用 atomic 持有 string 指针实现
	// 无锁比较交换；pet 联动是全局状态，多 stream 并发取最近一次即可。
	lastEmittedActivity atomic.Pointer[string]
}

type agentModelMemory interface {
	LastAgentModelHash() string
	SaveLastAgentModelHash(context.Context, string) error
}

// NewServiceWithRuntimes 使用默认依赖创建 forwarder 服务，支持完整的 Runtime 注入。
func NewServiceWithRuntimes(historyRoot string, resolver modeladapter.ChannelResolver, vmManager *vm.Manager, optRuntime *optimize.Runtime, cacheRuntime *cacheruntime.Runtime, toolRT *toolruntime.Runtime, contextRT *contextruntime.Runtime) *Service {
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
	provider := NewProviderGatewayWithCache(resolver, vmManager, cacheRuntime)
	service := &Service{
		store:              store,
		usageStore:         NewUsageFileStore(historyRoot),
		codebaseIndexStore: NewCodebaseIndexStore(appdata.CodebaseIndexRootPath()),
		docsIndexStore:     NewDocsIndexStore(appdata.DocsIndexRootPath()),
		rules:              rules,
		projector:          projector,
		compiler:           NewPromptCompiler(projector, NewToolCatalog(), NewReminderInjector(), rules),
		provider:           provider,
		resolver:           resolver,
		modelMemory:        modelMemory,
		broker:             broker,
		recorder:           newArtifactRecorder(store, broker, debug),
		debug:              debug,
		execBridge:         execbridge.NewBridge(),
		interactionBridge:  interactionbridge.NewBridge(),
		appendSeq:          newAppendSequenceTracker(),
		optimize:           optRuntime,
		toolRuntime:        toolRT,
		contextRuntime:     contextRT,
		aosRegistries:      make(map[string]*vm.AOSResultRegistry),
		stopCh:             make(chan struct{}),
	}
	service.startHistoryMaintenance()
	service.loadMemoryFromConfig()
	service.wireToolRuntimeBridges()
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
		stopCh:             make(chan struct{}),
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
		logger.Infof("forwarder ignored stale bidi append request_id=%s append_seqno=%d", requestID, appendSeqno)
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


// handleToolInvocation 把模型产生的工具意图转成 exec/interaction 请求并下发给客户端。

// closeStreamWithProviderError 在真实 LLM/provider 出错时通过 RunSSE 传回错误，并正常结束流。
