# Cursor BYOK — Architecture Document

> 本文档描述 Cursor BYOK 的完整系统架构。配合 `PROJECT_ANALYSIS.md`（速读手册）、`AGENTS.md`（AI 研发协议）与 **`docs/handbook/`**（AOS Engineering Handbook：按需加载，宪法 00–02 + Runtime/标准/路线图）使用。
>
> 长期定位：**AI Organization System (AOS)**。Virtual Model 对 Cursor 透明；专家绑定已有 ModelAdapter；ChannelService 生产路径必须非 nil；不改 MITM / `proto/` / Forwarder 公共接口。索引一致性守卫：`internal/docguard`。

---

## 1. 系统分层

```
┌──────────────────────────────────────────────────────────────┐
│                      桌面外壳 (Wails v3)                       │
│  ┌─────────────┐  ┌──────────────┐  ┌────────────────────┐  │
│  │ 主窗口 (Vue3) │  │ 系统托盘      │  │ PET 桌宠窗口        │  │
│  └─────────────┘  └──────────────┘  └────────────────────┘  │
├──────────────────────────────────────────────────────────────┤
│                      服务层 (Wails Services)                   │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌───────────────┐  │
│  │ ProxySvc  │ │ AdSvc    │ │ PetSvc   │ │ UpdateManager │  │
│  └──────────┘ └──────────┘ └──────────┘ └───────────────┘  │
├──────────────────────────────────────────────────────────────┤
│                      MITM 代理层                               │
│  ┌──────────────────────────────────────────────────────────┐│
│  │ goproxy + 内置 CA → 白名单 *.cursor.sh → 转发到 Backend   ││
│  └──────────────────────────────────────────────────────────┘│
├──────────────────────────────────────────────────────────────┤
│                      Backend 层                               │
│  ┌──────────────┐  ┌────────────────────────────────────────┐│
│  │ Server        │  │ Forwarder                              ││
│  │ (路由+策略)    │  │ (协议兼容+Prompt编译+Provider驱动)      ││
│  └──────────────┘  └───────────────┬────────────────────────┘│
│                                    │                          │
│  ┌─────────────────────────────────┴────────────────────────┐│
│  │                    Agent Model 层                         ││
│  │  Router → OpenAI Adapter / Anthropic Adapter              ││
│  └─────────────────────────────────┬────────────────────────┘│
│                                    │                          │
│  ┌─────────────────────────────────┴────────────────────────┐│
│  │                    Runtime 层 (NEW)                       ││
│  │  AOS / MOA / Context / Cache / Optimize / Tool / Telemetry / Evolver ││
│  └──────────────────────────────────────────────────────────┘│
├──────────────────────────────────────────────────────────────┤
│                      基础设施层                                │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌───────────────┐  │
│  │ certs    │ │ netproxy │ │ modelchannel│ │ appdata     │  │
│  └──────────┘ └──────────┘ └──────────┘ └───────────────┘  │
└──────────────────────────────────────────────────────────────┘
```

---

## 2. 模块职责矩阵

| 模块 | 包路径 | 单一职责 | 依赖 |
|---|---|---|---|
| MITM Proxy | `internal/mitm/` | HTTPS 劫持 + 白名单转发 | certs |
| CA Manager | `internal/certs/` | 动态签发 TLS 证书 | — |
| Backend Server | `internal/backend/server/` | HTTP/Connect 路由 + 策略 + 中间件 | chi |
| Config Manager | `internal/backend/server/config/` | 用户配置加载/保存/校验 | yaml |
| Upstream Mocks | `internal/backend/server/upstream/` | 伪装 Cursor 官方响应 | protobuf |
| Forwarder | `internal/backend/forwarder/` | 协议兼容内核 + Prompt 编译 + Provider 驱动 | agent/model, agent/bridge |
| Prompt Compiler | `internal/backend/forwarder/compiler.go` | 历史投影 + 工具加载 + Prompt 资产编译 | prompt/ |
| Compaction | `internal/backend/forwarder/compaction.go` | 自动/手动上下文压缩 | — |
| Model Router | `internal/backend/agent/model/router.go` | 按 provider 选择适配器 | modelchannel, runtime |
| OpenAI Adapter | `internal/backend/agent/model/openai.go` | OpenAI 兼容流式请求 | netproxy |
| Anthropic Adapter | `internal/backend/agent/model/anthropic.go` | Anthropic 兼容流式请求 | netproxy |
| Exec Bridge | `internal/backend/agent/bridge/exec/` | 执行型工具归一化 | — |
| Interaction Bridge | `internal/backend/agent/bridge/interaction/` | 交互型工具归一化 | netproxy |
| Model Channel | `internal/modelchannel/` | 渠道 ID 计算 + endpoint 归一化 | — |
| Runtime Config | `internal/runtime/` | 模型适配器配置解析 | modelchannel |
| **VMR** | `internal/backend/virtualmodel/` | 虚拟模型注册/解析 | runtime |
| **MOA** | `internal/backend/virtualmodel/moa/` | Planner → Experts → Critic → Judge → Aggregator | VMR |
| **AOS** | internal/backend/virtualmodel/aos/ | Leader -> Members -> Sprint -> Review -> Merge (ADR-013) | VMR |
| **Context Runtime** | `internal/backend/runtime/context/` | `BuildContext` + `PostProcess` 已接入 Forwarder 主链路 (ADR-014) | Phase 4 进行中 |
| **Memory Runtime** | `internal/backend/runtime/memory/` | 五层全部生产写入 (ADR-023)；APIEmbedder + FallbackEmbedder (ADR-025) | Phase 4 进行中 |
| **Cache Runtime** | `internal/backend/runtime/cache/` | 精确 + 语义缓存 + FallbackEmbedder (ADR-025)；P0 延迟修复 (23x) | Phase 5 进行中 |
| **Optimize Runtime** | `internal/backend/runtime/optimize/` | Token Budget + Cost + **spent 持久化** (`data/optimize/cost_tracker.json`, ADR-010) | Phase 3 完成 |
| **Tool Runtime** | `internal/backend/runtime/tool/` | Bridge 接线 + Catalog 同步 + 缓存 + MCP 动态注册 (ADR-024/026) | Phase 6 进行中 |
| **Telemetry Runtime** | `internal/backend/runtime/telemetry/` | 框架就绪 (Turn 级遥测) | Phase 9 进行中 |
| **Evolver Runtime** | `internal/backend/runtime/evolver/` | Diagnose/Sediment/Test/Memory/Propose/Persist/AutoWriteback + Runtime Catalog + Foundation Tables；Host 启动后台 Evolve+Persist；`task evolver` / `task evolver:writeback` / `task evolver:ci` (ADR-028..034) | Phase 14 完成 |
| PET Engine | `internal/pet/` | 桌面宠物动画/状态机 | Wails |
| App Runner | `internal/app/runner.go` | Wails 装配 + 生命周期 | bridge, mitm |

---

## 3. 数据流（一次对话）

```
Cursor IDE
    │ HTTPS (MITM 解密)
    ▼
MITM Proxy ──→ Backend Server (PolicyMiddleware: local/upstream)
    │               │
    │               ▼ (local)
    │          Forwarder.BidiAppend / RunSSE
    │               │
    │               ├── 解析 Connect protobuf
    │               ├── 写入 state.json / context.json
    │               ├── PromptCompiler.Compile()
    │               │     ├── HistoryProjector (历史投影)
    │               │     ├── ToolCatalog (工具加载)
    │               │     ├── ReminderInjector (提醒注入)
    │               │     └── prompt assets (固定 prompt)
    │               │
    │               ├── [NEW] ContextRuntime.BuildContext()
    │               │     ├── ContextBuilder
    │               │     ├── CompressionEngine (3级)
    │               │     ├── ContextRanker
    │               │     ├── WindowManager
    │               │     └── MemoryManager
    │               │
    │               ├── [NEW] CacheRuntime.Lookup()
    │               │     └── 命中 → 直接返回
    │               │
    │               ├── ProviderGateway.StartStream()
    │               │     ├── [NEW] VirtualModel? → VMR.Execute()
    │               │     │     └── MOA: Planner → Experts → Judge → Aggregator
    │               │     │     └── AOS: Leader → Members (parallel) → Review → Merge (ADR-013)
    │               │     └── Physical? → Router → Adapter
    │               │
    │               └── [NEW] TelemetryRuntime.RecordTurn()
    │
    └── Cursor 收到响应（以为来自官方）
```

---

### 3.1 AOS Cursor-native Task dispatch contract (ADR-046)

When an AOS team uses `cursor_task` execution, the parent conversation remains
in the normal Agent mode. AOS does not rewrite the parent into an undocumented
multitask mode. Instead, it compiles each member request into the existing
Cursor-native `Task` protocol and sends its `TaskArgs` through the normal
Forwarder stream; the Exec Bridge then opens the internal `SubagentArgs`
execution.

1. **Current model binding**: each member's configured physical adapter ID is
   written explicitly to `TaskArgs.Model`. That explicit value is preserved by
   Forwarder display/rewrite paths and takes precedence over a parent subagent
   model override. A registered virtual-model ID is rejected as a member target
   to prevent AOS re-entry.
2. **Batch lifecycle**: the scheduler validates the complete workspace graph,
   registers the generated `Task.ToolCallId` as its deterministic result key,
   starts eligible member Tasks, and resolves results with bounded parallelism.
   `AOSResultRegistry` correlates returned Task results by that key, not by
   `ExecServerMessage.exec_id`; timeout, cancellation, and spawn-error cleanup
   remove unresolved expectations before dependent work advances.
3. **Live configuration**: a successful config save normalizes the execution
   mode and, while the Host is running, replaces the registered AOS model or
   unregisters it when disabled. Future turns therefore use the latest
   leader/member adapter bindings without rebuilding the HTTP Host.

This contract is intentionally limited to repository-backed protocol and
runtime behavior. It does not use `.cursor/agents` files. It also does not
claim worktree isolation, process sandboxing, or fork semantics because this
repository has no executable evidence for those Cursor-client behaviors. A real
Cursor client E2E run remains the release gate for client-side dispatch and
completion behavior.

---

## 4. 配置模型

```yaml
# ~/.cursor-byok/config.yaml
routing:
  mode: local  # local | upstream

modelAdapters:
  - displayName: "Claude"
    type: anthropic
    baseURL: "https://api.anthropic.com"
    apiKey: "sk-..."
    modelID: "claude-sonnet-4-20250514"
    anthropicThinkingEffort: "xhigh"

virtualModels:
  moa:
    enabled: true
  aos:
    enabled: true
    executionMode: cursor_task
    leader:
      adapterID: "a1b2c3d4e5f6a7b8"
    members:
      - id: frontend
        name: "Frontend Engineer"
        adapterID: "b2c3d4e5f6a7b8c9"
        systemPrompt: "You are a senior React developer..."
```

AOS member tags are runtime-derived after the Leader recognizes the team and are not persisted in user configuration.

---

## 5. 持久化布局

```
~/.cursor-byok/
├── config.yaml              # 用户配置
├── data/
│   ├── ca.crt               # CA 证书（注入给宿主）
│   └── ads/                 # 广告资源缓存
├── history/
│   ├── usage.json           # 用量统计
│   └── <conversation_id>/
│       ├── state.json       # loop 状态
│       ├── context.json     # 可投影历史
│       └── conversation.lock
├── logs/                    # 运行日志
├── cache/                   # [NEW] Cache Runtime
│   ├── exact/               # 精确缓存条目
│   └── stats.json
├── telemetry/               # [NEW] Telemetry Runtime
│   ├── turns/               # Turn 级遥测
│   └── daily_summary.json
└── memory/                  # [NEW] Memory Runtime (Phase 4)
```

---

## 6. 扩展点（Plugin Points）

| 扩展点 | 接口 | 位置 |
|---|---|---|
| Virtual Model | `VirtualModel` interface | `internal/backend/virtualmodel/manager.go` |
| AOS Team | `TeamProfile` / `MemberConfig` | `internal/backend/virtualmodel/aos/types.go` |
| AOS Workflow | `WorkflowScheduler` | `internal/backend/virtualmodel/aos/workflow.go` |
| Workflow | `WorkflowConfig` | `internal/backend/virtualmodel/config/types.go` |
| Compression Strategy | `CompressionEngine` | `internal/backend/runtime/context/runtime.go` |
| Cache Strategy | `Runtime.Lookup/Store` | `internal/backend/runtime/cache/runtime.go` |
| Tool | `ToolRegistry.Register` | `internal/backend/runtime/tool/runtime.go` |
| Memory Layer | `MemoryManager` | `internal/backend/runtime/context/runtime.go` |
| Provider Adapter | `ModelAdapter` interface | `internal/backend/agent/model/types.go` |

---

*本文档随项目演进持续更新。每次架构变更必须同步更新。*
