# ADR-004: Phase 2 架构评审 — 基于研究发现的改进

**状态**：Proposed

**日期**：2026-07-13

**决策者**：AI Chief Architect

**依据**：5 篇论文/项目研究（MoA、LangGraph、MemGPT、AutoGen、AIOS）

---

## 评审发现

### 发现 1：Workflow 缺少 Checkpoint 机制

**来源**：LangGraph 的 StateGraph + Checkpoint 设计

**问题**：当前 MOA Workflow 是一次性执行（Planner → Experts → Aggregator），失败后无法从中间节点恢复。用户无法查看中间结果或从指定节点重放。

**建议**：在每个 Workflow Node 完成后自动保存 Checkpoint：
```go
type WorkflowCheckpoint struct {
    ID          string
    NodeID      string
    State       MOAState
    Input       ExecuteRequest
    Output      NodeExecuteResult
    Timestamp   time.Time
}
```
- 支持 `ReplayFrom(checkpointID)` 从任意节点重放
- 支持 `Fork(checkpointID)` 从任意节点分支执行（用于 Best-of-N）
- Debug UI 展示 Execution Tree

**影响**：Workflow 执行器需要重构以支持 Checkpoint。优先级：Phase 7（高级 Virtual Models）前完成。

---

### 发现 2：Memory 需要分层设计

**来源**：MemGPT 的五层 Memory + AIOS 的 Memory Manager

**问题**：当前 Memory Manager 是空实现（stub），只是预留了接口。实际的 Memory 注入逻辑需要尽快实现。

**建议**：采用 MemGPT 验证过的分层模型：
```
Working Memory → Session Memory → Long Memory → Project Memory → User Memory
```
- Working Memory：当前 turn 的上下文（已在 Context Window 内）
- Session Memory：当前会话的历史摘要（compaction 产物）
- Long Memory：跨会话的关键信息（SQLite + embedding）
- Project Memory：项目级规则和架构知识（`~/.cursor/rules/`）
- User Memory：用户偏好和习惯（config.yaml 扩展）

**影响**：Memory Manager 需要实现具体的存储和检索。优先级：Phase 4。

---

### 发现 3：MOA 的 Expert 可以支持 Nested Chat

**来源**：AutoGen 的 Nested Chat 机制

**问题**：当前 MOA 的 Critic 只能对 Expert 输出做一次评论，无法触发 Expert 的修正。

**建议**：增加 Critic → Expert 的反馈循环：
```
Expert → Critic (发现问题) → Expert (修正) → Critic (再检查) → Aggregator
```
配置为可选的 `maxCriticRounds` 参数。

**影响**：增加一轮 LLM 调用，但显著提升输出质量。优先级：Phase 7。

---

### 发现 4：Cache Runtime 缺少 Embedding 基础设施

**来源**：MemGPT 的 Archival Memory（embedding search）

**问题**：当前 Cache Runtime 只有精确缓存（SHA-256），缺少语义缓存。语义缓存和 Memory 的 embedding search 可以共享同一套基础设施。

**建议**：Phase 5 的 Semantic Cache 和 Phase 4 的 Long Memory 共用 embedding 基础设施：
- 复用用户已配置的 adapter 做 embedding（或内置轻量模型）
- SQLite 存储向量 + 余弦相似度搜索
- 统一 `EmbeddingStore` 接口

**影响**：Phase 4 和 Phase 5 可以并行建设 embedding 基础设施。优先级：Phase 4-5。

---

### 发现 5：需要统一 Workflow State 对象

**来源**：LangGraph 的 State + AIOS 的 Context Manager

**问题**：当前 MOA 各节点通过 `nodeResult` 和函数参数传递数据，没有统一的 State 对象。

**建议**：引入 `WorkflowState` 作为跨节点的共享状态：
```go
type WorkflowState struct {
    RequestID      string
    ConversationID string
    UserRequest    string
    Plan           *PlanResult
    ExpertResults  map[string]*NodeResult
    CriticFeedback string
    JudgeVerdict   string
    FinalOutput    string
    CheckpointID   string
    Metadata       map[string]any
}
```

**影响**：Workflow 执行器需要重构。优先级：Phase 7。

---

## 决策

基于以上发现，更新路线图优先级：

| 优先级 | 任务 | 原计划 | 调整后 |
|---|---|---|---|
| 🔴 高 | Memory 五层实现 | Phase 4 | Phase 4（不变） |
| 🔴 高 | Embedding 基础设施 | Phase 5 | Phase 4-5（提前共享） |
| 🟡 中 | Checkpoint 机制 | Phase 7 | Phase 4（提前，依赖 Memory） |
| 🟡 中 | Workflow State 统一 | Phase 7 | Phase 7（不变） |
| 🟢 低 | Nested Chat (Critic→Expert) | Phase 7 | Phase 7（不变） |

**关键调整**：Embedding 基础设施从 Phase 5 提前到 Phase 4（与 Memory 共享），Checkpoint 机制从 Phase 7 提前到 Phase 4（与 Memory 存储共享 SQLite 基础设施）。

---

## 参考

- LangGraph Checkpoint: github.com/langchain-ai/langgraph
- MemGPT Memory: arxiv.org/abs/2310.08560
- AIOS Kernel: arxiv.org/abs/2403.16971
- AutoGen Nested Chat: github.com/microsoft/autogen
