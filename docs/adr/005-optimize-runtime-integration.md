# ADR-005: Optimization Runtime 集成到 Forwarder 主链路

**状态**：Accepted（实施中 — 核心接线完成，配置/前端/adapter 池选择待补）

**日期**：2026-07-13

**决策者**：AI Chief Architect

**修订**：2026-07-13 — `recordProviderCost` 改为优先使用 provider 真实 usage；成本表支持 model id 子串匹配；补充 unit tests。

---

## 背景

Optimization Runtime（Token Budget + Cost Optimizer）框架已就绪，需要集成到 Forwarder 主链路中，使每个请求都经过动态 Token 预算分配和成本跟踪。

## 决策

在以下 5 个集成点注入 Optimization Runtime：

### 集成点 1: `Service.optimize` 字段
`forwarder.Service` 结构体增加 `optimize *optimize.Runtime` 字段，在 `NewServiceWithVM` 中通过参数注入。

### 集成点 2: `resolveProviderOutputBudget`
在 Token 预算计算中调用 `optimize.AllocateBudget()`：
- 获取动态分配的 Output Budget
- 将 Token Budget 详情注入 `requestKnobs` 供调试

### 集成点 3: `runProviderStream`
每次 provider 流式调用成功后，调用 `service.recordProviderCost()` 记录成本。

### 集成点 4: `executeSingleExpert` (MOA)
MOA 的每个专家节点在调用 LLM 前后：
- 调用前：使用 `SelectOptimalProvider` 检查是否需要切换 provider
- 调用后：使用 `RecordCost` 记录节点成本

### 集成点 5: `host.rebuildLocked`
在 Backend 启动时创建 Optimization Runtime 实例（默认 Balanced tier，$50/月），注入到 Forwarder 和 MOA 中。

## 理由

1. **Token Budget 动态分配**：不同模式/模型有不同的上下文窗口，统一由 Optimization Runtime 管理分配策略
2. **成本可见性**：每个 turn 和每个 MOA 节点的成本都被记录，支持后续 Dashboard 展示
3. **渐进式集成**：`optimize == nil` 时完全回退到原有行为，零风险
4. **未来可扩展**：Quality Tier 和 Cost Optimizer 的 provider 选择逻辑已预留

## 影响

- `forwarder/service.go` — 新增 `optimize` 字段、`recordProviderCost`、`GetCostSummary` 方法
- `forwarder/module.go` — 新增 `NewModuleWithRuntimes` 函数
- `backend/host.go` — `rebuildLocked` 创建 Optimization Runtime
- `virtualmodel/moa/provider.go` — `executeSingleExpert` 集成成本记录

## 参考

- ADR-003: Runtime 采用装饰器模式
- `internal/backend/runtime/optimize/runtime.go`
