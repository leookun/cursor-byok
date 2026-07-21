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

**已完成**：
- ✅ Memory 五层实现（Working/Session/Long/Project/User，ADR-011/012/023）
- ✅ Embedding 基础设施（SimpleEmbedder + APIEmbedder + FallbackEmbedder，ADR-025）
- ✅ 五大 Runtime 基础框架已就位，并在后续 Phase 中全部接入主链路（Context/Optimize/Cache/Tool/Telemetry）

---

## Phase 3: Optimization Runtime 集成 ✅ 已完成

**目标**：将 Optimization Runtime 接入 Forwarder 链路，实现 Token Budget 动态分配和 Cost Optimizer。

**已完成**：
1. ✅ `host.go` 创建 Optimization Runtime 并注入 Forwarder + MOA
2. ✅ `resolveProviderOutputBudget` → `AllocateBudgetWithEstimate`（mode + prompt 估算）
3. ✅ `runProviderStream` 成功后 `RecordCost`（优先 provider 真实 usage）
4. ✅ MOA：`selectAdapterIDForRole` 在配置绑定池内 `SelectOptimalCandidate`（ADR-002）
5. ✅ unit tests：`runtime/optimize` + `moa` + `config`
6. ✅ ADR-005 / ADR-008 / ADR-009 / **ADR-010**
7. ✅ 配置落盘 + 前端 Quality Tier / Cost 摘要
8. ✅ **Cost spent 跨进程持久化**（`NewRuntimeWithStore` + `data/optimize/cost_tracker.json`）
9. ✅ Benchmark：`docs/reports/2026-07-13-phase3-cost-persistence.md`

**关键决策**：ADR-005, ADR-008, ADR-009, ADR-010

---

## Phase 4: Context Runtime 集成 + Memory 五层 ✅ 已完成（BuildContext + 五层 Memory + Embedding 升级 + Compression 对齐）

**目标**：Context Runtime 包装/扩展 PromptCompiler 路径能力，Memory 实现五层记忆。

**已完成**：
1. ✅ `context.Runtime.BuildContext` 真实管线（Builder→Compress→Rank→Window→Memory）
2. ✅ Working（内存）+ Session（文件跨实例 reload）
3. ✅ Forwarder 主链路 PostProcess 集成（`service.go:1377` + `:2371`，ADR-014）
4. ✅ Session Memory 写入集成（`service.go:1767` `recordSessionMemory`）
5. ✅ ContextWindowTokens 从 ModelAdapter 解析
6. ✅ Long Memory embedding 语义搜索（ADR-012，SimpleEmbedder + InMemoryStore）
7. ✅ 研究 + ADR-011/012/014 + benchmark 报告
8. ✅ 单元测试 `runtime/context` + `runtime/memory`
9. ✅ Long Memory SQLite 持久化升级（`sqliteLongMemoryStore` 替代 JSON 文件方案，ADR-023）
10. ✅ Embedding 升级为 `FallbackEmbedder`（APIEmbedder + SimpleEmbedder fallback，ADR-025）
11. ✅ CompressionEngine 与 `forwarder/compaction.go` 深度对齐（共享 `internal/backend/runtime/compress` 包）

**Phase 4 核心切片已全部完成**：Working / Session / Long / Project / User 五层均有生产写入源。

**关键决策**：ADR-011, ADR-012, ADR-014, ADR-023, ADR-025

**预估剩余工作量**：已完成

---

## Phase 5: Cache Runtime 语义缓存 ✅ 已完成（精确 + 语义缓存 + 前端 Dashboard + Prompt Cache 对齐 + LRU 失效）

**目标**：实现语义缓存（embedding + vector similarity search）。

**任务**：
  1. ✅ 集成 embedding 模型（SimpleEmbedder TF-IDF 内置轻量模型；生产路径已走 API embedder）
  2. ✅ 实现 vector store（InMemoryStore + 余弦相似度）
  3. ✅ SemanticCache.Lookup() — TopK similarity > 0.85 命中（比原计划 0.95 更宽松，实践效果更好）
  4. ✅ 缓存预热策略（loadSemanticStore 在 NewRuntime 时加载已有条目）
  5. ✅ 前端 Dashboard 显示缓存命中率（`frontend/src/views/CacheDashboard.vue`，复用 `CacheRuntime.Stats()`）
  6. ✅ 真 LRU 上限淘汰 + Prompt Cache 对齐

**预估工作量**：已完成

---

## Phase 6: Tool Runtime 与 ExecBridge 集成 ✅ 已完成（Bridge 接线 + 缓存主路径 + MCP 动态注册 + 前端工具管理页）

**目标**：Tool Runtime 作为统一入口，对接现有 ExecBridge 和 InteractionBridge。

**已完成**：
1. ✅ Category / InternalName / Cacheable / SyncFromCatalog / RegisterBuiltinTools
2. ✅ `Execute()` 按 Category 分派到 Exec/Interaction 接口
3. ✅ Forwarder `handleToolInvocation` 查询 Enabled 元数据
4. ✅ adapter.go + unit tests；ADR-007
5. ✅ Bridge 接线：`SetBridges` 在 `NewServiceWithRuntimes` 中调用 (ADR-024)
6. ✅ Catalog 同步：`SyncFromCatalog` 在 Service 初始化时同步 Cursor 完整工具目录 (ADR-024)
7. ✅ 缓存接入主路径：`handleToolInvocation` 在 dispatch 前检查工具结果缓存 (ADR-024)
8. ✅ MCP 动态工具注册 (ADR-026：`SyncMCPTools` 在 `updateStreamMCPToolServers` 中调用)
9. ✅ 前端工具管理页（`frontend/src/views/ToolManagement.vue`）

**预估剩余工作量**：已完成

---

## Phase 7: 高级 Virtual Models ✅ 已完成

**目标**：实现 Reflection、Best-of-N、Tree-of-Thought、Debate 等虚拟模型。

**任务**：
1. ✅ Reflection Model (ADR-015)：Self-critique -> Improve -> Repeat
2. ✅ Best-of-N Model (ADR-017)：N parallel + Judge select
3. ✅ Debate Model (ADR-018)：2 Agent debate + Judge
4. ✅ Tree-of-Thought Model (ADR-019)：K branches * D depth + Evaluate
5. ✅ 前端可视化工作流编辑器（`frontend/src/views/WorkflowEditor.vue`，拖拽节点 + 连线 + 执行）

**预估工作量**：已完成

---

## Phase 8: Plugin SDK ✅ 已完成（SDK + Marketplace 后端 + 动态加载沙箱 + 前端 Marketplace）

**目标**：允许第三方注册新的 Role、Node、Workflow 和 Virtual Model，并提供 Marketplace 管理 + 动态加载沙箱。

**已完成**：
1. ✅ Plugin 接口定义（`Plugin` interface: Name/Version/Init）
2. ✅ Registry 加载器（`RegisterPlugin` / `RegisterVirtualModel` / `CreateVirtualModels` / `Unregister`）
3. ✅ 单元测试覆盖（8 tests，含死锁修复回归）
4. ✅ ADR-021 + research note
5. ✅ Plugin Registry 接入 host 运行时（`rebuildLocked` 挂载路由，`Stop` 卸载全部插件）
6. ✅ Plugin Marketplace 后端（REST：`GET /api/plugins`、`:name/install|uninstall|toggle|call`）
7. ✅ 动态加载沙箱（受限 goroutine + timeout/cancel + panic recover；内置 catalog）
8. ✅ 前端 Marketplace（`frontend/src/views/Plugins.vue`：安装/卸载/启用/调用）
9. ✅ Plugin 配置块（`server/config` `PluginConfig{Enabled, DataDir}`）

**后续优化方向**：
- 远程 registry URL / `.so` / WASM 加载

**关键决策**：ADR-021, ADR-047

---

## Phase 9: Benchmark & Telemetry Dashboard ✅ 已完成（Benchmark 框架 + A/B + Execution Tree + Telemetry Dashboard + Replay）

**目标**：完整的性能基准测试和可观测性面板。

**任务**：
1. ✅ Benchmark 框架（ADR-020：Suite + Task + Result + Report）
2. ✅ A/B 对比工具（ADR-027：Compare + ComparisonReport + FormatComparison）
3. ✅ Execution Tree 可视化（`GetAOSExecutionTree` + 前端可折叠/点击状态树）
4. ✅ 前端 Telemetry Dashboard（`frontend/src/views/TelemetryDashboard.vue`，复用 `GetAOSLastTraceSummary` + `GetHomeMetricsSummary`）
5. ✅ Trace 落盘（`aos.SaveTrace` -> `<dataRoot>/telemetry/traces/{sessionID}.json`）
6. ✅ Replay（`ReplayAOSTrace` 复用生产 `aos` 模型与 `ChannelService`）
7. ✅ Benchmark 自动接入 CI（`.github/workflows/benchmark.yml`）

**后续优化方向**：
- Replay 增量单节点重放 ✅ 已完成 / 对话界面自动 re-trigger

**预估工作量**：已完成

---

## 里程碑时间线

```
2026 Q2: Phase 1 (MOA) ✅ + Phase 2 (Runtime 框架) ✅
2026 Q3: Phase 3 (Optimization 集成) ✅ + Phase 4 (Context + Memory) ✅
2026 Q4: Phase 5 (Cache) ✅ + Phase 6 (Tool) ✅
2027 Q1: Phase 7 (高级 Virtual Models) ✅
2027 Q2: Phase 8 (Plugin SDK) ✅ + Phase 9 (Benchmark) ✅
```

---

## 技术债务追踪

| ID | 描述 | 严重度 | 状态 |
|---|---|---|---|
| TD-001 | `frontend/src/state/appState.js` 1388 行，需拆分 | 中 | 未开始 |
| TD-002 | `forwarder/service.go` 3439 行，需继续拆分 | 高 | 🟡 部分完成：MCP + 工具推断已拆出（mcp_tools.go 126 行 + tool_inference.go 180 行） |
| TD-003 | Runtime 未集成到 Forwarder 主链路 | 高 | ✅ 已解决：Context/Optimize/Cache/Tool 全部接入主链路（ADR-014/024/026） |
| TD-004 | MOA ChannelService 为 nil，无法实际调用模型 | 高 | ✅ 已修复：`buildVirtualModelManager` 注入 `AdapterChannelService`（生产 non-nil；测试可用 stub） |
| TD-005 | 缺少单元测试覆盖 | 高 | 持续 |

---

## 下一阶段（与 handbook 29 对齐）

### 已完成 ADR 索引（ADR-011 到 ADR-047）

| ADR | 内容 | Phase |
|---|---|---|
| ADR-011 | Phase 4 切片：BuildContext + Working/Session Memory | Phase 4 |
| ADR-012 | Long Memory Embedding 语义搜索 | Phase 4 |
| ADR-013 | AOS Organization Runtime 架构设计 | Phase 5/7 |
| ADR-014 | Context Runtime PostProcess 集成 | Phase 4 |
| ADR-015 | Reflection Virtual Model | Phase 7 |
| ADR-016 | Tool Runtime 结果缓存 | Phase 6 |
| ADR-017 | Best-of-N Virtual Model | Phase 7 |
| ADR-018 | Debate Virtual Model | Phase 7 |
| ADR-019 | Tree-of-Thought Virtual Model | Phase 7 |
| ADR-020 | Benchmark 框架 | Phase 9 |
| ADR-021 | Plugin SDK | Phase 8 |
| ADR-022 | Streaming 单流输出策略 | Phase 9 |
| ADR-023 | Project/User Memory Production Writeback | Phase 4 |
| ADR-024 | Tool Runtime Bridge Wiring + Catalog Sync | Phase 6 |
| ADR-025 | Embedder Interface + API Embedder | Phase 4/5 |
| ADR-026 | MCP Dynamic Tool Registration | Phase 6 |
| ADR-027 | A/B Comparison Tool | Phase 9 |
| ADR-028 | Self-Evolution Runtime (Evolver) | Phase 10 |
| ADR-029 | Evolution Report & Baseline Persistence | Phase 10 |
| ADR-030 | Safe Auto-Writeback for Deterministic Index Drift | Phase 10 |
| ADR-031 | Evolver Test Stage, CI Gate, and Report Catalog Writeback | Phase 11 |
| ADR-032 | Runtime Catalog Co-Evolution | Phase 12 |
| ADR-033 | Evolution Memory & Trend Analysis | Phase 13 |
| ADR-034 | Per-Chapter Foundation Table Co-Evolution | Phase 14 |
| ADR-035 | AOS Benchmark Injection & Planning Advisor | Phase 15 |
| ADR-036 | Bullet Foundations & Baseline Retention | Phase 16 |
| ADR-037 | Semantic Handbook↔Code Constraint Diagnosis | Phase 17 |
| ADR-038 | Evolution TaskPlan & Semantic Rule Expansion | Phase 18 |
| ADR-039 | TaskPlan Allowlisted Executor | Phase 19 |
| ADR-040 | Scaffold Actions & Markdown Report Retention | Phase 20 |
| ADR-041 | Bounded Code Implementation Executor | Phase 21 |
| ADR-042 | AST Semantic Scan & Recipe Expansion | Phase 22 |
| ADR-043 | Runtime Recipe Expansion & Risk Gates | Phase 23 |
| ADR-044 | Optimize/Forwarder Recipes & Metric Proposals | Phase 24 |
| ADR-045 | Runtime Metric Baselines & Regressions | Phase 25 |
| ADR-046 | AOS Subagent Orchestration Architecture | Phase 26 |
| ADR-047 | Plugin 沙箱执行 / 安全隔离 | Phase 8 |

### Virtual Model 族系

| VM | 模式 | 状态 |
|---|---|---|
| MOA | Planner -> Experts -> Aggregator | Phase 1 完成 |
| AOS | Leader -> Members -> Sprint -> Review -> Merge | Phase 5 完成；Phase 26 仓库级验证完成，Cursor 客户端 E2E 待验收 |
| Reflection | Generate -> Critique -> Refine | Phase 7 完成 |
| Best-of-N | N parallel + Judge select | Phase 7 完成 |
| Debate | 2 Agent debate + Judge | Phase 7 完成 |
| Tree-of-Thought | K branches * D depth + Evaluate | Phase 7 完成 |

### 当前状态

1. **Phase 4 已完成**：PostProcess 已集成 (ADR-014)；五层 Memory 生产写入 (ADR-023)；Long Memory 已升级为 SQLite 持久化 + APIEmbedder + FallbackEmbedder (ADR-025)；CompressionEngine 与 `forwarder/compaction.go` 深度对齐。
2. **Phase 5 已完成**：精确缓存 + 语义缓存 + LRU 上限淘汰 + Prompt Cache 对齐，已接入 Forwarder (ADR-006/025)；前端 Cache Dashboard 已上线。
3. **Phase 6 已完成**：Bridge 已接线 + Catalog 已同步 + 缓存已接入主路径 (ADR-024)；MCP 动态工具注册已实现 (ADR-026)；前端工具管理页已上线。
4. **Phase 7 已完成**：Reflection (ADR-015) + Best-of-N (ADR-017) + Debate (ADR-018) + Tree-of-Thought (ADR-019) + 前端可视化工作流编辑器全部实现。
5. **Phase 8 已完成**：Plugin SDK 核心可用 (ADR-021)；Marketplace 后端 + 动态加载沙箱 + 前端 Marketplace 已上线 (ADR-047)；单元测试 PASS。
6. **Phase 9 已完成**：Benchmark 框架 (ADR-020) + A/B 对比 (ADR-027) + Trace 落盘 + Execution Tree 可视化 + Replay + 前端 Telemetry Dashboard + CI Benchmark 已上线。
7. **Phase 10-25 已完成**：Self-Evolution Runtime 及后续 Evolver 增强、Test Gate、Runtime Catalog、Evolution Memory、Foundation Tables、Bullet Baselines、Semantic Constraints、TaskPlan Executor、Scaffold Actions、Bounded Code Executor、AST Scan、Runtime Recipes、Optimize/Forwarder Recipes、Runtime Metric Baselines 全部闭环。
8. **Phase 26 仓库级验证完成（26a-26g）**：AOS 在正常 Agent-mode 父会话中经内部 Cursor-native `Task` / `SubagentArgs` 派发成员；协议桥接、显式成员 adapter 模型绑定、批量 spawn/resolve、`execID` 结果关联、防 AOS 重入，以及配置保存后的运行中 AOS 替换均有仓库级实现与聚焦测试（ADR-046）。这不等同于真实 Cursor 客户端 E2E 已通过。
9. **Phase 26 剩余发布门禁**：使用真实 Cursor 客户端验证 `cursor_task` 的派发、结果回传和父会话完成路径。当前不使用 `.cursor/agents` 文件；在没有可执行证据前，不声明 worktree/process sandbox 或 fork 行为。后续优化：Plugin 远程 registry / `.so` / WASM 加载。
10. 手册 / ADR / research 一致性由 internal/docguard + evolver 守卫。

---

*此路线图随项目演进持续更新。每个 Phase 完成后更新状态。与 docs/handbook/29_Roadmap.md 保持事实同步。*
