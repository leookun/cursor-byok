# Research Note: AIOS — Agent Operating System

**论文**：arxiv.org/abs/2403.16971
**日期**：2026-07-13

---

## 核心发现

AIOS 将 LLM Agent 的运行时建模为操作系统，提出了：

```
AIOS Kernel
    ├── Agent Scheduler (FIFO / Priority / Round-Robin)
    ├── Context Manager (Snapshot / Restore)
    ├── Memory Manager (Short-term / Long-term)
    ├── Storage Manager (File system for agent data)
    ├── Tool Manager (API wrapper for tools)
    └── Access Controller (Permission / Quota)
```

关键洞察：
1. **Agent 是进程**：每个 Agent 有独立的上下文、内存、存储
2. **LLM 是 CPU**：LLM 调用是计算资源，需要调度和分配
3. **Context Window 是 RAM**：有限且昂贵，需要管理
4. **Scheduler 是关键**：多个 Agent 并发时需要调度策略

## 对 Cursor BYOK 的启示

### 可直接借鉴的设计

1. **Agent Scheduler**：AIOS 的调度器可以映射到我们的 MOA Scheduler（Sequential/Parallel/Conditional）。但 AIOS 更关注资源调度（CPU/内存），而我们更关注执行顺序。

2. **Context Manager (Snapshot/Restore)**：类似我们的 Checkpoint 机制。AIOS 的 snapshot 设计可以增强我们的 Workflow State 持久化。

3. **Access Controller**：AIOS 的权限控制对 Tool Runtime 很有参考价值——不同 Expert 可能有不同的工具权限。

### 可以改进的地方

1. **LLM 调用抽象为"计算资源"**：AIOS 将 LLM 调用视为 CPU 时间，这启发了我们的 Cost Optimizer 和 Token Budget。可以进一步抽象：
   ```go
   type LLMResource struct {
       TokenBudget  int
       CostBudget   float64
       LatencyBudget time.Duration
       Priority     int
   }
   ```

2. **存储管理器**：AIOS 的 Storage Manager 为每个 Agent 分配独立存储。我们可以为每个 Workflow Node 分配独立的临时存储。

3. **调度策略**：AIOS 支持 FIFO/Priority/Round-Robin。我们的 MOA Scheduler 可以增加优先级调度。

### 不适用/暂不采用的设计

1. **内核级实现**：AIOS 是一个研究原型，很多设计过于理想化。我们不追求 OS 级别的抽象。
2. **多 Agent 并发**：当前 Cursor BYOK 主要处理单个用户请求，不需要复杂的并发 Agent 调度。

## 实现建议

AIOS 验证了"Agent OS"概念的可行性。我们的六大 Runtime 体系与 AIOS 的六大模块高度对应：

| AIOS Module | Cursor BYOK Runtime | 成熟度 |
|---|---|---|
| Agent Scheduler | MOA Runtime (Workflow Scheduler) | ✅ Phase 1 |
| Context Manager | Context Runtime | 🟡 Phase 2 |
| Memory Manager | Memory Manager (in Context Runtime) | 🔴 Phase 4 |
| Storage Manager | History (state.json/context.json) | ✅ 生产级 |
| Tool Manager | Tool Runtime | 🟡 Phase 2 |
| Access Controller | 待建设 | 🔴 远期 |

## 后续研究建议

1. 阅读 AIOS 的 Scheduler 实现（源码在 GitHub）
2. 对比 AIOS 的 Context Manager 与 LangGraph 的 Checkpoint
3. 研究 Temporal.io 的工作流调度器设计
