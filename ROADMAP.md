# Cursor BYOK — Development Roadmap

> 本文档维护长期开发路线和阶段目标。每个 Phase 完成后更新状态。

---

## 当前版本：v0.2.0-alpha（Agent OS 基础建设阶段）

---

## Phase 1: MOA Runtime ✅ 已完成

**目标**：建立虚拟模型框架，MOA 作为第一个 Virtual Model 出现在 AvailableModels 中。

**交付物**：
- ✅ `internal/backend/virtualmodel/` — VMR 管理器 + 配置 + 解析器
- ✅ `internal/backend/virtualmodel/moa/` — MOA 完整实现（Planner → Experts → Judge → Aggregator）
- ✅ `frontend/src/views/VirtualModels.vue` — 前端虚拟模型配置页
- ✅ 集成到 Backend/Forwarder 路由

**关键决策 (ADR)**：
- ADR-001: MOA 作为 VirtualModel 接口实现，而非独立模块
- ADR-002: 使用已有 ModelAdapter 而非新建 Registry

---

## Phase 2: Runtime 基础框架 ✅ 已完成

**目标**：建立 Context、Cache、Optimization、Tool、Telemetry 五大 Runtime 的基础框架。

**交付物**：
- ✅ `internal/backend/runtime/context/` — ContextRuntime (Builder/Compressor/Ranker/Window/Memory)
- ✅ `internal/backend/runtime/cache/` — CacheRuntime (精确缓存 + 语义缓存框架)
- ✅ `internal/backend/runtime/optimize/` — OptimizationRuntime (Token Budget + Cost Optimizer)
- ✅ `internal/backend/runtime/tool/` — ToolRuntime (统一 Registry)
- ✅ `internal/backend/runtime/telemetry/` — TelemetryRuntime (Turn 级遥测)

**待完成**：
- ✅ Memory 五层实现
- ✅ Embedding 基础设施
- 🟡 Optimization/Cache/Tool 已部分接入主链路；Context/Memory/Telemetry 仍待完整集成

---

## Phase 3: Optimization Runtime 集成 🟡 近完成（3.4 adapter 池 + budget estimate 已完成）

**目标**：将 Optimization Runtime 接入 Forwarder 链路，实现 Token Budget 动态分配和 Cost Optimizer。

**已完成**：
1. ✅ `host.go` 创建 Optimization Runtime 并注入 Forwarder + MOA
2. ✅ `resolveProviderOutputBudget` → `AllocateBudgetWithEstimate`（mode + prompt 估算）
3. ✅ `runProviderStream` 成功后 `RecordCost`（优先 provider 真实 usage）
4. ✅ MOA：`selectAdapterIDForRole` 在配置绑定池内 `SelectOptimalCandidate`（ADR-002）
5. ✅ unit tests：`runtime/optimize` + `moa` + `config`
6. ✅ ADR-005 / ADR-008 / **ADR-009**
7. ✅ 配置落盘 + 前端 Quality Tier / Cost 摘要

**待完成**：
1. 🔴 Cost spent 跨进程持久化
2. 🔴 关/开 Optimization 的端到端 benchmark 记录（非阻塞单元已覆盖）

**预估剩余工作量**：1 天

---

## Phase 4: Context Runtime 集成 + Memory 五层 🔴 计划中

**目标**：Context Runtime 替代现有 PromptCompiler，Memory 实现五层记忆。

**任务**：
1. ContextRuntime.BuildContext() 包装现有 PromptCompiler.Compile()
2. CompressionEngine 对接现有 compaction.go
3. MemoryManager 实现五层 Memory：
   - Working Memory（当前 turn，内存）
   - Session Memory（当前会话，文件）
   - Long Memory（跨会话，SQLite + embedding）
   - Project Memory（项目级，.cursor/rules/）
   - User Memory（用户偏好，config.yaml）

**预估工作量**：3-4 周

---

## Phase 5: Cache Runtime 语义缓存 🔴 计划中

**目标**：实现语义缓存（embedding + vector similarity search）。

**任务**：
1. 集成 embedding 模型（复用用户已配置的 adapter 或内置轻量模型）
2. 实现 vector store（SQLite + 余弦相似度）
3. SemanticCache.Lookup() — TopK similarity > 0.95 命中
4. 缓存预热策略
5. 前端 Dashboard 显示缓存命中率

**预估工作量**：2-3 周

---

## Phase 6: Tool Runtime 与 ExecBridge 集成 🟡 进行中（元数据 + Execute 已就绪）

**目标**：Tool Runtime 作为统一入口，对接现有 ExecBridge 和 InteractionBridge。

**已完成**：
1. ✅ Category / InternalName / Cacheable / SyncFromCatalog / RegisterBuiltinTools
2. ✅ `Execute()` 按 Category 分派到 Exec/Interaction 接口
3. ✅ Forwarder `handleToolInvocation` 查询 Enabled 元数据
4. ✅ adapter.go + unit tests；ADR-007

**待完成**：
1. 🔴 Forwarder 主路径完整改走 `Execute`（需保留 pending 状态机，不可简单替换）
2. 🔴 工具结果缓存对接 Cache Runtime
3. 🔴 MCP 动态工具注册
4. 🔴 前端工具管理页

**预估剩余工作量**：1–2 周

---

## Phase 7: 高级 Virtual Models 🟢 远期

**目标**：实现 Reflection、Best-of-N、Tree-of-Thought、Debate 等虚拟模型。

**任务**：
1. Reflection Model：Self-critique → Improve → Repeat
2. Best-of-N Model：N 次并行推理 → Judge 选最优
3. Debate Model：两个 Agent 辩论 → Judge 裁决
4. 前端可视化工作流编辑器（拖拽节点）

**预估工作量**：4-6 周

---

## Phase 8: Plugin SDK 🟢 远期

**目标**：允许第三方注册新的 Role、Node、Workflow 和 Virtual Model。

**任务**：
1. Plugin 接口定义
2. Plugin 加载器
3. Plugin Marketplace 前端
4. 沙箱执行

**预估工作量**：4-6 周

---

## Phase 9: Benchmark & Telemetry Dashboard 🟢 远期

**目标**：完整的性能基准测试和可观测性面板。

**任务**：
1. Benchmark 框架（延迟/Token/成本/质量）
2. A/B 对比工具
3. Execution Tree 可视化
4. 前端 Telemetry Dashboard

**预估工作量**：3-4 周

---

## 里程碑时间线

```
2026 Q2: Phase 1 (MOA) ✅ + Phase 2 (Runtime 框架) ✅
2026 Q3: Phase 3 (Optimization 集成) + Phase 4 (Context + Memory)
2026 Q4: Phase 5 (Cache) + Phase 6 (Tool)
2027 Q1: Phase 7 (高级 Virtual Models)
2027 Q2: Phase 8 (Plugin SDK) + Phase 9 (Benchmark)
```

---

## 技术债务追踪

| ID | 描述 | 严重度 | 状态 |
|---|---|---|---|
| TD-001 | `frontend/src/state/appState.js` 1388 行，需拆分 | 中 | 未开始 |
| TD-002 | `forwarder/service.go` 3500+ 行，需拆分 | 高 | 未开始 |
| TD-003 | Runtime 未集成到 Forwarder 主链路 | 高 | Phase 3-6 |
| TD-004 | MOA ChannelService 为 nil，无法实际调用模型 | 高 | Phase 4 |
| TD-005 | 缺少单元测试覆盖 | 高 | 持续 |

---

*此路线图随项目演进持续更新。每个 Phase 完成后更新状态。*
