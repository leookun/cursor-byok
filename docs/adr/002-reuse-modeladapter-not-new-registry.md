# ADR-002: MOA 使用已有 ModelAdapter，不新建 Registry

**状态**：Accepted

**日期**：2026-07-13

**决策者**：AI Chief Architect

---

## 背景

MOA 的每个专家节点（Planner、Coding Expert、Research Expert 等）需要绑定到具体的物理模型。有两种方案：

- **方案 A**：新建 MOA 专用的 Model Registry
- **方案 B**：直接引用已有 ModelAdapter 的 ID

## 决策

**方案 B**：MOA 不维护自己的 Model Registry，所有专家节点的 adapter 绑定直接引用已有 `ModelAdapterConfig.ID`。

## 理由

1. **避免重复**：用户已在 `config.yaml` 的 `modelAdapters` 中配置了模型渠道（baseURL、apiKey、modelID）。新建 Registry 意味着用户需要重复配置。
2. **一致性**：MOA 使用的模型与用户直接使用的模型是同一份配置，修改 apiKey 或 baseURL 只需改一处。
3. **简化前端**：下拉框直接展示已有 adapter 列表，无需新建模型配置流程。
4. **遵循 DRY 原则**：ModelAdapter 已经包含完整的渠道信息（provider、baseURL、apiKey、modelID），MOA 不需要额外维护。

## 实现

```yaml
virtualModels:
  moa:
    enabled: true
    planner:
      adapterID: "a1b2c3d4e5f6a7b8"  # SHA-256 hash of Claude adapter
    nodes:
      coding:
        adapterID: "b2c3d4e5f6a7b8c9"
```

其中 `adapterID` = `modelchannel.BuildChannelID(baseURL, modelID, apiKey, name, endpoint)`。

## 影响

- MOA 的 `resolveAdapterForRole` 方法通过 adapterID 查找已有渠道
- ChannelService 接口抽象了渠道解析，MOA 不直接依赖 Runtime 层
- 前端 VirtualModels.vue 的 adapter 下拉框直接来自 `appState.modelAdapters`

## 参考

- `internal/modelchannel/identity.go` — BuildChannelID
- `internal/runtime/local_runtime.go` — ResolvedChannel
- `internal/backend/virtualmodel/resolver.go` — VMResolver
