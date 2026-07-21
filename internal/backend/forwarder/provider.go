// provider.go 把 forwarder 的 canonical 请求转交给现有的 provider adapter 层。
// 支持物理模型（通过 modeladapter.Router）、虚拟模型（通过 VirtualModelRuntime）和缓存（通过 Cache Runtime）。
package forwarder

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
	cacheruntime "cursor/internal/backend/runtime/cache"
	vm "cursor/internal/backend/virtualmodel"
)

const defaultCacheTTL = 30 * time.Minute

type DefaultProviderGateway struct {
	router modeladapter.ModelAdapterRouter
	vm     *vm.Manager
	cache  *cacheruntime.Runtime
}

// NewProviderGatewayWithCache 创建支持缓存 + 虚拟模型的 provider 网关。
func NewProviderGatewayWithCache(resolver modeladapter.ChannelResolver, vmManager *vm.Manager, cacheRuntime *cacheruntime.Runtime) *DefaultProviderGateway {
	return &DefaultProviderGateway{
		router: modeladapter.NewRouter(resolver),
		vm:     vmManager,
		cache:  cacheRuntime,
	}
}

// StartStream 把 forwarder 的 provider 请求翻译成 modeladapter.StreamRequest 并发起流式调用。
// 流程：Cache Lookup → Virtual Model? → Physical Router
func (gateway *DefaultProviderGateway) StartStream(ctx context.Context, req ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
	if ctx == nil {
		ctx = context.Background()
	}

	// Step 0: Cache Lookup（精确缓存 + 语义缓存）
	if gateway.cache != nil && !gateway.isVirtualModel(req.ModelID) {
		cacheMessages := gateway.toCacheMessages(req.Messages)
		if cached, hitType, hit := gateway.cache.Lookup(cacheMessages, "", req.ModelID, req.Mode.String()); hit {
			// 缓存命中：将缓存结果以流式事件返回
			return gateway.returnCached(cached, hitType, sink)
		}
	}

	// Step 1: 检查是否为虚拟模型
	if gateway.isVirtualModel(req.ModelID) {
		return gateway.startVirtualStream(ctx, req, sink)
	}

	requestKnobs := make(map[string]any, len(req.RequestKnobs)+2)
	for key, value := range req.RequestKnobs {
		requestKnobs[key] = value
	}
	requestKnobs["stream"] = true
	if req.MaxTokens > 0 {
		requestKnobs["max_tokens"] = req.MaxTokens
	}
	if strings.TrimSpace(req.ThinkingEffort) != "" {
		requestKnobs["runtime_thinking_effort"] = strings.TrimSpace(req.ThinkingEffort)
	}
	err := gateway.router.Stream(ctx, modeladapter.StreamRequest{
		RequestID:           req.RequestID,
		RunID:               req.RunID,
		ModelCallID:         req.ModelCallID,
		ConversationID:      req.ConversationID,
		Mode:                req.Mode,
		ModelID:             req.ModelID,
		ThinkingEffort:      req.ThinkingEffort,
		Messages:            req.Messages,
		StableMessageCount:  req.StableMessageCount,
		Tools:               append([]json.RawMessage(nil), req.Tools...),
		MaxTokens:           req.MaxTokens,
		Stream:              true,
		RequestKnobs:        requestKnobs,
		CompileSummary:      req.CompileSummary,
		Observer:            req.Observer,
		ArtifactPaths:       req.ArtifactPaths,
		RequestBodyOverride: req.RequestBodyOverride,
	}, sink)
	if err != nil {
		return providerTerminalError{cause: err}
	}

	// Step N: 非虚拟模型调用完成后，将结果写入缓存
	// 注：由于流式调用的特殊性，实际缓存写入在 runProviderStream 的 sink wrapper 中处理
	return nil
}

// isVirtualModel 检查 modelID 是否为虚拟模型。
func (gateway *DefaultProviderGateway) isVirtualModel(modelID string) bool {
	return gateway.vm != nil && gateway.vm.IsVirtualModel(modelID)
}

// toCacheMessages 将 modeladapter.Message 转换为 cache.Message。
func (gateway *DefaultProviderGateway) toCacheMessages(messages []modeladapter.Message) []cacheruntime.Message {
	result := make([]cacheruntime.Message, 0, len(messages))
	for _, msg := range messages {
		result = append(result, cacheruntime.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	return result
}

// returnCached 将缓存结果以流式事件返回。
func (gateway *DefaultProviderGateway) returnCached(cached string, hitType string, sink func(modeladapter.ModelEvent) error) error {
	// 发送文本内容
	if err := sink(modeladapter.ModelEvent{
		Kind: modeladapter.ModelEventKindTextDelta,
		Text: cached,
	}); err != nil {
		return providerTerminalError{cause: err}
	}

	// 发送完成事件
	if err := sink(modeladapter.ModelEvent{
		Kind:         modeladapter.ModelEventKindTurnFinished,
		FinishReason: "stop",
	}); err != nil {
		return providerTerminalError{cause: err}
	}

	return nil
}

// CacheStore 将 provider 响应写入缓存（由 runProviderStream 在流式调用完成后调用）。
func (gateway *DefaultProviderGateway) CacheStore(req ProviderRequest, resultText string, promptTokens int, outputTokens int) {
	if gateway.cache == nil || gateway.isVirtualModel(req.ModelID) {
		return
	}
	cacheMessages := gateway.toCacheMessages(req.Messages)
	_ = gateway.cache.Store(cacheMessages, "", req.ModelID, req.Mode.String(), resultText, promptTokens, outputTokens, defaultCacheTTL)
}

// startVirtualStream 将请求路由到虚拟模型运行时，并将结果以流式事件发送给 forwarder。
func (gateway *DefaultProviderGateway) startVirtualStream(ctx context.Context, req ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
	// Phase 26g: AOS re-entry hard guard. If the context indicates we are
	// already inside an AOS execution (depth >= 1), reject any attempt to
	// route to another virtual model. This prevents infinite AOS nesting.
	if vm.GetAOSDepth(ctx) >= 1 {
		return providerTerminalError{cause: &virtualModelError{
			modelID: req.ModelID,
			reason:  fmt.Sprintf("AOS re-entry blocked: already inside AOS execution (depth=%d)", vm.GetAOSDepth(ctx)),
		}}
	}

	model, ok := gateway.vm.Get(req.ModelID)
	if !ok || model == nil {
		return providerTerminalError{cause: &virtualModelError{modelID: req.ModelID, reason: "virtual model not found or not enabled"}}
	}

	// 将 Messages 转换为虚拟模型消息格式
	vmMessages := make([]vm.Message, 0, len(req.Messages))
	for _, msg := range req.Messages {
		vmMessages = append(vmMessages, vm.Message{
			Role:    msg.Role,
			Content: msg.Content,
			Name:    msg.Name,
		})
	}

	// 提取最新的用户消息
	latestUserText := req.LatestUserText
	if latestUserText == "" {
		for i := len(vmMessages) - 1; i >= 0; i-- {
			if vmMessages[i].Role == "user" && vmMessages[i].Content != "" {
				latestUserText = vmMessages[i].Content
				break
			}
		}
	}

	// 执行虚拟模型
	// [修复] 思考强度/最大 tokens 传递：Cursor 请求中的 ThinkingEffort/MaxTokens
	// 必须透传到虚拟模型执行上下文，使 AOS/MOA 的内部 Leader/member 调用真正
	// 受到用户在 Cursor 模型选择器中选择的参数影响。
	result, err := model.Execute(ctx, &vm.ExecuteRequest{
		RequestID:      req.RequestID,
		ConversationID: req.ConversationID,
		ModelCallID:    req.ModelCallID,
		Messages:       vmMessages,
		LatestUserText: latestUserText,
		ThinkingEffort: req.ThinkingEffort,
		MaxTokens:      req.MaxTokens,
	})
	if err != nil {
		return providerTerminalError{cause: &virtualModelError{modelID: req.ModelID, reason: err.Error()}}
	}

	// 将结果转为流式事件
	// 默认使用 Text 字段一次性推送完整结果（Phase 1 非流式模式）。
	// Phase 15 切片：如果虚拟模型返回了阶段性文本，先推送阶段状态再推送最终结果。
	if result.PhaseText != "" {
		if err := sink(modeladapter.ModelEvent{
			Kind: modeladapter.ModelEventKindTextDelta,
			Text: result.PhaseText,
		}); err != nil {
			return providerTerminalError{cause: err}
		}
	}
	if result.Text != "" {
		if err := sink(modeladapter.ModelEvent{
			Kind: modeladapter.ModelEventKindTextDelta,
			Text: result.Text,
		}); err != nil {
			return providerTerminalError{cause: err}
		}
	}

	// 发送完成事件
	finishReason := result.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	if err := sink(modeladapter.ModelEvent{
		Kind:         modeladapter.ModelEventKindTurnFinished,
		FinishReason: finishReason,
	}); err != nil {
		return providerTerminalError{cause: err}
	}

	return nil
}

type virtualModelError struct {
	modelID string
	reason  string
}

func (e *virtualModelError) Error() string {
	return "virtual model " + e.modelID + ": " + e.reason
}
