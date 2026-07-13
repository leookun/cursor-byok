# ADR-001: MOA 作为 VirtualModel 接口实现

**状态**：Accepted

**日期**：2026-07-13

**决策者**：AI Chief Architect

---

## 背景

需要将 MOA（Multi-model Orchestration Architecture）集成到 Cursor BYOK 中。MOA 需要对 Cursor 表现为一个普通模型，但实际上通过工作流编排多个物理模型。

## 决策

MOA 作为 `VirtualModel` 接口的实现，而非独立模块。

```go
type VirtualModel interface {
    ID() string
    DisplayName() string
    Enabled() bool
    Execute(ctx context.Context, req *ExecuteRequest) (*ExecuteResult, error)
}
```

## 理由

1. **对 Cursor 透明**：MOA 作为 channel ID "moa" 出现在 AvailableModels 中，Cursor 不需要任何特殊处理。
2. **可扩展**：未来 Reflection、Debate、Best-of-N 等虚拟模型只需实现同一接口。
3. **低耦合**：VMR Manager 通过接口管理所有虚拟模型，Forwarder 通过 VMManager 路由请求。
4. **复用现有基础设施**：通过 `forwarder/provider.go` 的 `startVirtualStream` 方法将虚拟模型结果转成 `modeladapter.ModelEvent`，无缝对接现有流式输出管线。

## 替代方案

### 方案 A：MOA 作为独立 HTTP 服务
- **拒绝理由**：增加运维复杂度，需要额外进程管理。

### 方案 B：MOA 作为 Router 的一个分支
- **拒绝理由**：Router 的职责是选择 OpenAI/Anthropic 适配器，不应承担工作流编排职责。违反单一职责原则。

## 影响

- 新增 `internal/backend/virtualmodel/` 包
- Forwarder 的 `ProviderGateway.StartStream()` 增加虚拟模型判断分支
- Config 结构体增加 `VirtualModelsConfig` 字段

## 参考

- Together AI MoA 论文：arxiv.org/abs/2406.04692
- 项目架构文档：ARCHITECTURE.md
