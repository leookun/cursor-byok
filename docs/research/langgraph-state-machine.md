# Research Note: LangGraph State Machine & Checkpoint

**源码**：github.com/langchain-ai/langgraph
**日期**：2026-07-13

---

## 核心发现

LangGraph 将 Agent 工作流建模为有向图（StateGraph），核心概念：

1. **State**：贯穿整个工作流的共享状态对象
2. **Node**：处理 State 的函数/Agent
3. **Edge**：连接 Node 的有向边（普通边或条件边）
4. **Checkpoint**：在每个 Superstep 后自动保存 State 快照
5. **Persistence**：基于 SQLite/Postgres 的 Checkpoint 存储

关键洞察：
- **Checkpoint 是核心**：允许暂停、恢复、重放、分支
- **条件边**：运行时决定下一步走哪个 Node
- **Human-in-the-loop**：通过 interrupt 机制暂停等待人工输入

## 对 Cursor BYOK 的启示

### 可直接借鉴的设计

1. **Checkpoint 机制**：
   - 我们的 `state.json` 和 `context.json` 已经实现了基础的状态持久化
   - 可以借鉴 LangGraph 的 Checkpoint 设计，在每个 Workflow Node 完成后自动保存快照

2. **条件边（Conditional Edge）**：
   - LangGraph 的条件边由函数返回路由键
   - 我们的 `NodeExecutionMode.Conditional` 可以增强为类似的条件路由

3. **Interrupt / Human-in-the-loop**：
   - LangGraph 的 `interrupt()` 可以在特定 Node 暂停
   - 对于 MOA 的 Critic/Judge 节点，可以支持"用户确认后再继续"

### 可以改进的地方

1. **Workflow State 统一**：当前 MOA 的 Workflow 节点之间通过 `nodeResult` 传递数据，可以改为统一的 `WorkflowState` 对象，类似 LangGraph 的 State。

2. **Replay 支持**：LangGraph 的 Checkpoint 天然支持从任意 Superstep 重放。我们可以：
   ```
   MOA Execution → Save Checkpoint at each Node
   → Debug UI: "Replay from Critic node"
   → Inject modified state and re-execute
   ```

3. **分支执行**：LangGraph 支持从同一个 Checkpoint fork 出多个分支并行探索。这对 Best-of-N 和 Debate 虚拟模型非常有用。

### 不适用/暂不采用的设计

1. **Python 生态**：LangGraph 是 Python 库，不能直接复用。但设计理念可以借鉴。
2. **过于通用的 State 模型**：LangGraph 的 State 是 `dict` + reducer 模式，过于灵活但类型不安全。我们更适合用 Go 的强类型 State。

## 实现建议

```go
// 类似 LangGraph 的 StateGraph
type MOAState struct {
    Messages        []Message
    CurrentNode     string
    NodeResults     map[string]*NodeResult
    CheckpointID    string
    CheckpointStack []Checkpoint
}

type Checkpoint struct {
    ID        string
    NodeID    string
    State     MOAState
    Timestamp time.Time
}
```

## 后续研究建议

1. 阅读 LangGraph 的 Checkpoint 实现（SQLite schema 设计）
2. 研究 Temporal.io 的 Workflow 引擎（Go 原生，更强的工作流能力）
3. 对比 Prefect 和 Dagster 的 DAG 执行模型
