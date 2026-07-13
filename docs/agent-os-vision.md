# Agent OS — Cursor BYOK 六大 Runtime 体系设计文档

> 本文档定义 Cursor BYOK 从"模型代理"演进为 **Agent Operating System** 的完整架构规划。
> 不是一次性重构，而是渐进式演进，每个 Phase 独立可交付。

---

## 0. 总体愿景

```
                         Cursor
                            │
                            ▼
                   Virtual Model（MOA / Reflection / Best-of-N / Debate）
                            │
             ┌──────────────┼──────────────┐
             ▼              ▼              ▼
        MOA Runtime   Context Runtime   Tool Runtime
             │              │              │
             └──────┬───────┴───────┬──────┘
                    ▼               ▼
             Optimization Runtime  Cache Runtime
                    │               │
                    └──────┬────────┘
                           ▼
                   Existing Router
                           │
                        Provider
                           │
                          LLM
```

六大 Runtime 各司其职：

| Runtime | 职责 | 一句话 |
|---|---|---|
| **MOA Runtime** | 多模型协作 | "谁来思考" |
| **Context Runtime** | 上下文全生命周期 | "给模型看什么" |
| **Optimization Runtime** | Token 预算与成本优化 | "怎么省钱省 Token" |
| **Cache Runtime** | 语义缓存与命中 | "能不能不调模型" |
| **Tool Runtime** | 工具统一管理 | "能用什么工具" |
| **Telemetry Runtime** | 全链路可观测 | "发生了什么，花了多少钱" |

---

## 1. 现有基础设施评估

### 1.1 已有能力（可直接利用）

| 现有模块 | 对应 Runtime | 成熟度 |
|---|---|---|
| `forwarder/compiler.go` — PromptCompiler | Context Runtime | ✅ 已有 prompt 资产加载、历史投影、规则注入 |
| `forwarder/projector.go` — HistoryProjector | Context Runtime | ✅ 已有 conversation → messages 投影 |
| `forwarder/compaction.go` — 压缩引擎 | Context Runtime | ✅ 已有自动/手动 compaction、token 阈值 |
| `forwarder/tool_catalog.go` — ToolCatalog | Tool Runtime | ✅ 已有按 mode 过滤的工具白名单 |
| `forwarder/reminders.go` — ReminderInjector | Context Runtime | ✅ 已有 mode/subagent contract 注入 |
| `bridge/exec/` + `bridge/interaction/` | Tool Runtime | ✅ 已有工具执行桥接 |
| `historymetrics/` — UsageSummary | Telemetry Runtime | ✅ 已有 usage.json 统计 |
| `virtualmodel/` — VMR + MOA | MOA Runtime | ✅ Phase 1 已完成 |
| `modelchannel/` — 渠道解析 | Cache Runtime 基础 | ✅ 已有 SHA-256 channel ID |

### 1.2 待建设的缺口

| 缺失能力 | 目标 Runtime | 优先级 |
|---|---|---|
| 语义缓存（Semantic Cache） | Cache Runtime | 🔴 高 |
| Token Budget 动态分配 | Optimization Runtime | 🔴 高 |
| 分层 Memory（Working/Session/Long/Project/User） | Context Runtime | 🟡 中 |
| 成本跟踪与 Cost Optimizer | Optimization Runtime | 🟡 中 |
| 统一 Tool Registry（MCP/Browser/Shell/Git） | Tool Runtime | 🟡 中 |
| Prompt Rewrite / Dedup | Optimization Runtime | 🟢 低 |
| Context Ranking（相关性排序） | Context Runtime | 🟢 低 |
| 全链路 Telemetry Dashboard | Telemetry Runtime | 🟢 低 |

---

## 2. 演进路线图

```
Phase 1 (已完成)  →  MOA Runtime 基础框架
Phase 2 (本次)    →  Context Runtime + Cache Runtime
Phase 3           →  Optimization Runtime (Token Budget + Cost)
Phase 4           →  Tool Runtime 统一
Phase 5           →  Telemetry Runtime + Dashboard
Phase 6           →  高级能力（Reflection、Debate、Plugin SDK）
```

---

## 3. Phase 2 详细设计：Context Runtime + Cache Runtime

### 3.1 Context Runtime 架构

Context Runtime 是"所有 Prompt 的第一站"。任何发给模型的内容都先经过它处理。

```
Conversation History
        │
        ▼
┌───────────────────────────────────────┐
│           Context Runtime              │
│                                        │
│  ┌─────────────┐  ┌────────────────┐  │
│  │ Context      │  │ Compression    │  │
│  │ Builder      │  │ Engine         │  │
│  │              │  │                │  │
│  │ - 历史投影    │  │ - Lossless     │  │
│  │ - 规则注入    │  │ - Lossy        │  │
│  │ - Memory 注入 │  │ - Semantic     │  │
│  │ - 工具注入    │  │ - Hierarchical │  │
│  └──────┬───────┘  └───────┬────────┘  │
│         │                  │           │
│  ┌──────┴──────────────────┴───────┐  │
│  │       Context Ranking            │  │
│  │       - 相关性排序（非时间）       │  │
│  │       - 重要性评分               │  │
│  └──────────────┬──────────────────┘  │
│                 │                      │
│  ┌──────────────┴──────────────────┐  │
│  │       Window Manager             │  │
│  │       - 按模型动态裁剪            │  │
│  │       - Claude 180K / GPT 100K   │  │
│  └──────────────┬──────────────────┘  │
│                 │                      │
│  ┌──────────────┴──────────────────┐  │
│  │       Memory Manager             │  │
│  │       - Working / Session        │  │
│  │       - Long / Project / User    │  │
│  └─────────────────────────────────┘  │
└───────────────────┬───────────────────┘
                    │
                    ▼
            Compiled Context
                    │
                    ▼
              MOA / Router
```

### 3.2 与现有代码的集成点

Context Runtime 替代现有的 `forwarder/compiler.go` 中的 `PromptCompiler.Compile()` 调用链。

**现有流程**：
```
service.driveProvider()
  → compiler.Compile(conversation, mode, latestUserText, modelName)
    → projector.ProjectPromptReplay(conversation)    // 历史投影
    → catalog.Load(mode)                              // 工具加载
    → rules.BuildSystemPrompt()                       // 规则注入
    → reminders.Inject()                              // 提醒注入
    → promptassets.ReadPrompt()                       // 固定 prompt
```

**新流程**：
```
service.driveProvider()
  → contextRuntime.BuildContext(ctx, BuildRequest{
        Conversation: conversation,
        Mode:         mode,
        UserText:     latestUserText,
        ModelID:      modelID,
        ModelName:    modelName,
    })
    → contextBuilder.Build()       // 整合历史/规则/Memory/工具
    → compressionEngine.Compress() // 智能压缩
    → contextRanker.Rank()         // 相关性排序
    → windowManager.Trim()         // 按模型窗口裁剪
    → memoryManager.Inject()       // Memory 注入
    → 返回 CompiledContext
```

### 3.3 目录结构

```
internal/backend/runtime/
├── context/
│   ├── doc.go
│   ├── runtime.go          // ContextRuntime 主入口
│   ├── builder.go           // ContextBuilder：组装 prompt
│   ├── compression.go       // CompressionEngine：多级压缩
│   ├── ranker.go            // ContextRanker：相关性排序
│   ├── window.go            // WindowManager：动态裁剪
│   └── memory.go            // MemoryManager：分层记忆
├── cache/
│   ├── doc.go
│   ├── runtime.go           // CacheRuntime 主入口
│   ├── exact.go             // 精确缓存（SHA-256 hash）
│   ├── semantic.go          // 语义缓存（embedding + similarity）
│   └── store.go             // 缓存存储（文件/SQLite）
├── optimize/
│   ├── doc.go
│   ├── runtime.go           // OptimizationRuntime 主入口
│   ├── budget.go            // Token Budget 管理器
│   ├── cost.go              // Cost Optimizer
│   ├── prompt_rewrite.go    // Prompt 重写/去重
│   └── dedup.go             // 上下文去重
├── tool/
│   ├── doc.go
│   ├── runtime.go           // ToolRuntime 主入口
│   ├── registry.go          // 统一 Tool Registry
│   ├── mcp.go               // MCP 工具桥接
│   └── catalog.go           // 增强版 ToolCatalog
└── telemetry/
    ├── doc.go
    ├── runtime.go           // TelemetryRuntime 主入口
    ├── metrics.go           // Token/成本/延迟/缓存命中
    ├── trace.go             // 执行链路追踪
    └── dashboard.go         // Dashboard 数据聚合
```

---

## 4. Cache Runtime 详细设计

### 4.1 核心概念

```
请求进入
    │
    ▼
┌──────────────┐
│ 计算 Prompt   │
│ SHA-256 Hash │
└──────┬───────┘
       │
       ▼
┌──────────────┐     Hit?     ┌──────────────┐
│ 精确缓存查询  │─────────────→│  直接返回结果  │
│ (Exact Match) │              └──────────────┘
└──────┬───────┘
       │ Miss
       ▼
┌──────────────┐
│ 语义缓存查询  │
│ (Embedding    │
│  + Similarity)│
└──────┬───────┘
       │
  ┌────┴────┐
  │         │
 >0.95?   <0.95?
  │         │
  ▼         ▼
返回缓存   调用 LLM
  │         │
  └────┬────┘
       ▼
  写入缓存
```

### 4.2 缓存键设计

```go
type CacheKey struct {
    // 精确缓存键 = SHA-256(messages + tools + systemPrompt)
    ExactHash string
    // 语义缓存键 = embedding of user message
    SemanticEmbedding []float32
    // 元数据
    ModelID    string
    Mode       string
    Timestamp  time.Time
}
```

### 4.3 缓存策略

| 策略 | 适用场景 | TTL |
|---|---|---|
| Exact | 完全相同的 prompt（如重复编译错误） | 1 hour |
| Semantic (>0.95) | 语义高度相似的询问 | 30 min |
| Semantic (>0.85) | 相似但不完全相同 | 不缓存（仅提示） |
| Tool Result | 工具调用结果（如文件读取） | 5 min |

### 4.4 存储

```
~/.cursor-local-assistant-v2/cache/
├── exact/
│   └── <sha256>.json           // 精确缓存条目
├── semantic/
│   └── embeddings.db            // SQLite: embedding + metadata
└── stats.json                   // 缓存命中率统计
```

---

## 5. Optimization Runtime 详细设计

### 5.1 Token Budget 模型

```
每个请求的总 Token 预算 = Context Window - Safety Margin

按角色分配：
┌──────────────┬─────────────┐
│ 角色          │ Token 占比   │
├──────────────┼─────────────┤
│ System Prompt │ 固定 5-8K   │
│ Rules         │ 固定 2-4K   │
│ Memory        │ 动态 2-16K  │
│ History       │ 剩余 * 0.6  │
│ Tools         │ 固定 3-5K   │
│ Output Budget │ 剩余 * 0.3  │
└──────────────┴─────────────┘
```

### 5.2 Cost Optimizer

```
用户设置月度预算: $50
    │
    ▼
Planner 根据预算选择模型:
    │
    ├── Coding:   $0.001/K tokens → DeepSeek
    ├── Research: $0.002/K tokens → Gemini Flash
    ├── Judge:    $0.015/K tokens → Claude (贵但准)
    └── Agg:      $0.005/K tokens → GPT-4o-mini
    │
    ▼
实时跟踪花费，接近预算时自动降级
```

### 5.3 Quality Tiers

| Tier | Planner | Experts | Judge | Aggregator | 预估成本/turn |
|---|---|---|---|---|---|
| Fast | GPT-4o-mini | DeepSeek | Skip | GPT-4o-mini | ~$0.01 |
| Balanced | Claude Haiku | GPT-4o | Claude | GPT-4o | ~$0.05 |
| Quality | Claude Sonnet | Claude+GPT | Claude | Claude | ~$0.15 |
| Ultra | Claude Opus | 3× Experts | Claude | Claude | ~$0.50 |

---

## 6. Memory Runtime 设计

### 6.1 五层 Memory

```
┌─────────────────────────────────────────────┐
│              User Memory                     │
│  跨项目持久化：偏好、习惯、常用技术栈          │
│  生命周期：永久                               │
├─────────────────────────────────────────────┤
│              Project Memory                  │
│  项目级：架构决策、技术债务、关键文件索引       │
│  生命周期：项目周期                            │
├─────────────────────────────────────────────┤
│              Long Memory                     │
│  会话间持久化：重要结论、已解决问题             │
│  生命周期：跨会话（30天）                       │
├─────────────────────────────────────────────┤
│              Session Memory                  │
│  当前会话上下文：已讨论话题、已修改文件          │
│  生命周期：单会话                              │
├─────────────────────────────────────────────┤
│              Working Memory                  │
│  当前 turn：正在处理的任务、中间结果             │
│  生命周期：单 turn                             │
└─────────────────────────────────────────────┘
```

### 6.2 存储格式

```json
{
  "user": {
    "preferences": { "language": "go", "framework": "vue" },
    "habits": ["tdd", "small-commits"],
    "tech_stack": ["go", "vue3", "tailwindcss"]
  },
  "project": {
    "architecture": "wails desktop app, go backend, vue frontend",
    "key_files": ["internal/backend/forwarder/service.go"],
    "tech_debt": ["appState.js too large (1388 lines)"]
  },
  "long": [
    { "topic": "MITM proxy design", "conclusion": "use goproxy with embedded CA" }
  ],
  "session": {
    "current_task": "building virtual model runtime",
    "modified_files": ["internal/backend/virtualmodel/..."]
  }
}
```

---

## 7. Tool Runtime 设计

### 7.1 统一 Tool Registry

```go
type ToolRegistry struct {
    tools map[string]ToolDescriptor
}

type ToolDescriptor struct {
    Name        string
    Description string
    Schema      json.RawMessage   // JSON Schema
    Handler     ToolHandler       // 执行函数
    Category    ToolCategory      // filesystem / mcp / browser / shell / git / search
    CachePolicy CachePolicy       // 是否缓存工具结果
    CostEstimate CostEstimate     // 预估 Token 消耗
}

type ToolCategory string
const (
    CategoryFilesystem ToolCategory = "filesystem"
    CategoryMCP        ToolCategory = "mcp"
    CategoryBrowser    ToolCategory = "browser"
    CategoryShell      ToolCategory = "shell"
    CategoryGit        ToolCategory = "git"
    CategorySearch     ToolCategory = "search"
)
```

### 7.2 与现有 ExecBridge/InteractionBridge 的关系

Tool Runtime 是 ExecBridge 和 InteractionBridge 的上层抽象。现有的桥接代码保持不变，Tool Runtime 作为统一入口分发请求：

```
ToolRuntime.Execute(toolName, args)
    │
    ├── CategoryFilesystem → ExecBridge
    ├── CategoryMCP        → MCPClient
    ├── CategoryBrowser    → InteractionBridge (WebFetch)
    ├── CategoryShell      → ExecBridge (Shell)
    ├── CategoryGit        → ExecBridge (Shell via git)
    └── CategorySearch     → InteractionBridge (WebSearch)
```

---

## 8. Telemetry Runtime 设计

### 8.1 核心指标

```go
type TurnTelemetry struct {
    TurnID        string
    RequestID     string
    ConversationID string
    ModelID       string
    VirtualModel  string  // "moa" or "" (physical)

    // 各阶段耗时
    PlannerDurationMS    int64
    ExpertDurationsMS    map[string]int64  // role → ms
    JudgeDurationMS      int64
    AggregatorDurationMS int64
    TotalDurationMS      int64

    // Token 统计
    PromptTokens     int
    CompletionTokens int
    TotalTokens      int
    CacheHitTokens   int

    // 成本统计
    EstimatedCostUSD float64
    ProviderBreakdown map[string]float64  // provider → cost

    // 缓存统计
    CacheHit       bool
    CacheHitType   string  // "exact" / "semantic" / "miss"

    // 压缩统计
    CompactionTriggered bool
    TokensCompacted     int
    CompactionRatio     float64
}
```

### 8.2 存储与 Dashboard

```
~/.cursor-local-assistant-v2/telemetry/
├── turns/
│   └── <date>/
│       └── <turnID>.json
├── daily_summary.json
├── monthly_summary.json
└── cost_tracker.json
```

---

## 9. 实施计划（Phase 2 → Phase 6）

### Phase 2A: Context Runtime 基础（2-3 周）

1. 创建 `internal/backend/runtime/context/` 包
2. 实现 `ContextRuntime` 主入口，封装现有的 `PromptCompiler`
3. 实现 `WindowManager`（按模型动态裁剪）
4. 实现 `ContextRanker`（基于简单启发式的相关性排序）
5. 将 `forwarder/service.go` 中的 `driveProvider` 改为调用 `ContextRuntime.BuildContext()`

### Phase 2B: Cache Runtime 基础（1-2 周）

1. 创建 `internal/backend/runtime/cache/` 包
2. 实现精确缓存（SHA-256 hash + JSON 文件存储）
3. 在 `forwarder/provider.go` 的 `StartStream` 中注入缓存查询
4. 统计缓存命中率并接入 Telemetry

### Phase 3: Optimization Runtime（2-3 周）

1. Token Budget 动态分配
2. Cost Optimizer（按 budget 自动选择模型）
3. Quality Tiers（Fast/Balanced/Quality/Ultra）

### Phase 4: Memory + Tool Runtime（2-3 周）

1. 分层 Memory Manager
2. 统一 Tool Registry
3. MCP 协议支持

### Phase 5: Telemetry Dashboard（1-2 周）

1. 全链路 Telemetry 收集
2. 前端 Dashboard（成本/延迟/缓存命中率图表）

### Phase 6: 高级能力（持续）

1. Reflection、Debate、Best-of-N 虚拟模型
2. 语义缓存（Embedding + Vector Search）
3. Plugin SDK

---

## 10. 与现有代码的向后兼容

所有新 Runtime 通过 **装饰器模式** 包装现有逻辑，不破坏已有功能：

```go
// 现有流程
provider := NewProviderGateway(resolver)

// 新流程（可选启用）
cacheRuntime := cache.NewRuntime(cacheDir)
contextRuntime := context.NewRuntime(memoryDir)
optimizeRuntime := optimize.NewRuntime()

provider := NewProviderGatewayWithRuntimes(resolver, ProviderRuntimes{
    Cache:      cacheRuntime,
    Context:    contextRuntime,
    Optimize:   optimizeRuntime,
})
```

当 Runtime 为 nil 时，回退到现有行为。

---

*本文档定义了 Agent OS 的完整架构规划。每个 Phase 可独立交付，渐进式演进。*
