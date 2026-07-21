// lifecycle.go 提取自 service.go：Service 生命周期、回调注入、内存初始化与上下文后处理。
package forwarder

import (
	"context"
	"strings"

	"cursor/internal/logger"

	contextruntime "cursor/internal/backend/runtime/context"
	memruntime "cursor/internal/backend/runtime/memory"
	toolruntime "cursor/internal/backend/runtime/tool"
	"cursor/gen/agentv1"
)

// wireToolRuntimeBridges connects the Tool Runtime to the service's ExecBridge
// and InteractionBridge via adapter wrappers (ADR-024). This enables
// toolRuntime.Execute to dispatch to real bridges and makes the tool result
// cache (ADR-016) effective on the main path. Also syncs the static tool
// catalog so Tool Runtime knows about all Cursor internal tools.
func (service *Service) wireToolRuntimeBridges() {
	if service == nil || service.toolRuntime == nil {
		return
	}
	service.toolRuntime.SetBridges(
		toolruntime.NewExecBridgeAdapter(service.execBridge),
		toolruntime.NewInteractionBridgeAdapter(service.interactionBridge),
	)
	// Sync static tool catalog: register all known Cursor tool names so
	// the Tool Runtime has metadata (category, cacheable, TTL) for every
	// tool, not just the 9 builtins.
	catalog := NewToolCatalog()
	if tools, names, err := catalog.Load(agentv1.AgentMode_AGENT_MODE_AGENT, ""); err == nil {
		service.toolRuntime.SyncFromCatalog(tools, names)
	}
}

// ensureStopCh lazily initializes the Service's stop channel. Constructors
// that don't call startHistoryMaintenance (e.g. newServiceWithDependencies in
// tests) still need stopCh for Shutdown to be safe.
func (service *Service) ensureStopCh() {
	if service == nil {
		return
	}
	if service.stopCh == nil {
		service.stopCh = make(chan struct{})
	}
}

// SetModelActivityCallback 注册模型活动状态回调（由 Host 注入，供 pet 桥接订阅）。
// forwarder 在 stream 关键节点调用它通知外部，state 取值：thinking|working|idle|error。
// 设为 nil 可解绑。线程安全。
func (service *Service) SetModelActivityCallback(fn func(state string)) {
	service.onModelActivityMu.Lock()
	defer service.onModelActivityMu.Unlock()
	service.onModelActivity = fn
}

// emitModelActivity 通知外部模型活动状态。状态未变化时静默跳过，避免抖动。
// 调用方在 stream 关键节点（thinking/working/idle/error）调用此方法。
func (service *Service) emitModelActivity(state string) {
	if service == nil || state == "" {
		return
	}
	// 去重：只在状态变化时回调，避免每个 chunk 都触发 pet 状态切换。
	for {
		old := service.lastEmittedActivity.Load()
		if old != nil && *old == state {
			return // 状态未变化
		}
		newState := state
		if service.lastEmittedActivity.CompareAndSwap(old, &newState) {
			break
		}
		// CAS 失败，重试
	}
	service.onModelActivityMu.RLock()
	fn := service.onModelActivity
	service.onModelActivityMu.RUnlock()
	if fn != nil {
		fn(state)
	}
}

// Shutdown signals all background goroutines (history maintenance, stream
// broker timers, etc.) to exit, waits for them to drain (bounded by ctx),
// and tears down the StreamBroker. Idempotent via sync.Once.
// R15: lifecycle unification.
func (service *Service) Shutdown(ctx context.Context) error {
	if service == nil {
		return nil
	}
	service.ensureStopCh()
	service.shutdownOnce.Do(func() {
		service.shutdownState.Store(true)
		close(service.stopCh)
	})
	// Wait for background goroutines to drain. Bounded by ctx so a stuck
	// goroutine cannot block Host.Stop indefinitely.
	waitDone := make(chan struct{})
	go func() {
		service.wg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-ctx.Done():
		logger.Warnf("forwarder Service.Shutdown timed out waiting for goroutines: %v", ctx.Err())
	}
	// R17: tear down the stream broker (cancel inflight streams, stop timers).
	if service.broker != nil {
		if err := service.broker.Shutdown(ctx); err != nil {
			logger.Errorf("forwarder Service.Shutdown broker error: %v", err)
		}
	}
	return nil
}

// IsShutdown reports whether Shutdown has been invoked on this Service.
func (service *Service) IsShutdown() bool {
	if service == nil {
		return false
	}
	return service.shutdownState.Load()
}

// loadMemoryFromConfig populates Project and User Memory layers from existing
// configuration sources (UserRuleStore + config preferences). This makes those
// layers visible to PostProcess memory injection without requiring a separate
// scanner or new public interface (ADR-023).
func (service *Service) loadMemoryFromConfig() {
	if service == nil || service.contextRuntime == nil {
		return
	}
	mm := service.contextRuntime.MemoryManager()
	if mm == nil {
		return
	}

	// Project Memory: load user rules as project-level context
	if service.rules != nil {
		if rules, err := service.rules.List(); err == nil {
			for _, rule := range rules {
				text := strings.TrimSpace(rule.Knowledge)
				if text == "" {
					continue
				}
				_ = mm.Remember(context.Background(), &memruntime.Entry{
					Layer:   memruntime.LayerProject,
					Content: text,
					Source:  "user_rules",
					Tags:    []string{"rule", rule.ID},
				})
			}
		}
	}

	// User Memory: load user-level preferences (stable across projects)
	_ = mm.Remember(context.Background(), &memruntime.Entry{
		Layer:   memruntime.LayerUser,
		Content: "Memory Runtime enabled with five-layer hierarchy (Working/Session/Long/Project/User).",
		Source:  "system",
		Tags:    []string{"memory_config"},
	})
}

// applyContextPostProcess 对已编译的上下文应用 Context Runtime 后处理（压缩/排序/窗口/记忆注入）。
// nil 安全：contextRuntime 为 nil 时直接返回原始 compiled。
func (service *Service) applyContextPostProcess(compiled CompiledConversation, latestUserText string, modelID string) CompiledConversation {
	if service == nil || service.contextRuntime == nil {
		return compiled
	}
	contextWindow := int(service.resolveContextWindowTokens(modelID))
	result := service.contextRuntime.PostProcess(context.Background(), contextruntime.PostProcessRequest{
		Messages:            compiled.Messages,
		StableMessageCount:  compiled.StableMessageCount,
		UserText:            latestUserText,
		ModelID:             modelID,
		ContextWindowTokens: contextWindow,
	})
	if result.Messages != nil {
		compiled.Messages = result.Messages
	}
	if result.StableMessageCount > 0 {
		compiled.StableMessageCount = result.StableMessageCount
	}
	return compiled
}
