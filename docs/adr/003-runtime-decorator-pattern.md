# ADR-003: Runtime 采用装饰器模式集成，不破坏现有架构

**状态**：Accepted

**日期**：2026-07-13

**决策者**：AI Chief Architect

---

## 背景

需要将五大 Runtime（Context、Cache、Optimization、Tool、Telemetry）集成到 Forwarder 主链路。现有链路已经稳定运行（`forwarder/service.go` 3500+ 行），直接修改风险很高。

## 决策

所有 Runtime 通过 **装饰器模式（Decorator Pattern）** 包装现有逻辑，不直接修改现有代码路径。

```go
// 现有流程
provider := NewProviderGateway(resolver)

// 新流程（可选启用）
provider := NewProviderGatewayWithRuntimes(resolver, ProviderRuntimes{
    Cache:      cacheRuntime,
    Context:    contextRuntime,
    Optimize:   optimizeRuntime,
    Telemetry:  telemetryRuntime,
})

// 当 Runtime 为 nil 时，回退到现有行为
func (gateway *DefaultProviderGateway) StartStream(ctx context.Context, req ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
    // Step 1: Cache lookup (skip if nil)
    if gateway.cache != nil {
        if result, hit := gateway.cache.Lookup(...); hit {
            return gateway.returnCached(result, sink)
        }
    }
    
    // Step 2: Virtual model routing (skip if nil)
    if gateway.vm != nil && gateway.vm.IsVirtualModel(req.ModelID) {
        return gateway.startVirtualStream(ctx, req, sink)
    }
    
    // Step 3: Original physical model path
    return gateway.router.Stream(ctx, req, sink)
}
```

## 理由

1. **向后兼容**：所有 Runtime 为 nil 时，行为与修改前完全一致。
2. **渐进式演进**：每个 Runtime 可以独立启用/禁用，降低集成风险。
3. **可测试**：每个 Runtime 可以独立单元测试，不依赖完整链路。
4. **低侵入**：只需在入口函数增加装饰器调用，不修改现有 3500+ 行代码。

## 影响

- Forwarder 的 `Service` 结构体增加 Runtime 字段
- `host.go` 的 `rebuildLocked` 中创建 Runtime 实例
- 每个 Runtime 独立包，零循环依赖

## 参考

- GoF Design Patterns: Decorator Pattern
- `internal/backend/forwarder/provider.go` — 已实现 VirtualModel 装饰器
- `internal/backend/forwarder/service.go` — 已实现 `NewServiceWithVM`
