# Research Note: MemGPT — Hierarchical Memory & Context Paging

**论文**：arxiv.org/abs/2310.08560
**源码**：github.com/cpacker/MemGPT（后更名为 Letta）
**日期**：2026-07-13

---

## 核心发现

MemGPT 将 LLM 的上下文窗口视为"虚拟内存"，实现了类操作系统的分层存储：

```
Working Memory (Context Window)
    ↕ Context Paging (LLM 自主决定读写)
Long-term Memory (External Storage)
    ├── Core Memory (persona, human)
    ├── Archival Memory (向量搜索)
    └── Recall Memory (会话历史)
```

关键洞察：
1. **LLM 作为 Memory Controller**：LLM 通过 function calling 自主决定何时从 Long-term Memory 读取/写入
2. **Context Paging**：当 Working Memory 满了，LLM 主动将不重要的内容"换出"到 Long-term Memory
3. **Archival Memory 使用 Embedding**：通过向量相似度搜索找到最相关的记忆

## 对 Cursor BYOK 的启示

### 可直接借鉴的设计

1. **五层 Memory 模型**（MemGPT 验证了分层 Memory 的有效性）：
   ```
   Working Memory (当前 turn, 上下文窗口内)
   Session Memory (当前会话, 上下文窗口外但会话内)
   Long Memory (跨会话, SQLite + embedding)
   Project Memory (项目级, .cursor/rules/)
   User Memory (用户偏好, config.yaml)
   ```

2. **LLM 自主管理 Memory**：
   - MemGPT 让 LLM 通过 `conversation_search` 和 `archival_memory_search` 工具自主检索记忆
   - 我们可以为 MOA 的 Memory Expert 提供类似的工具

3. **Context Paging 启发**：
   - MemGPT 的 paging 机制类似我们的 Compaction Engine
   - 但 MemGPT 更智能：不是简单摘要，而是选择性保留关键信息

### 可以改进的地方

1. **Memory Expert**：在 MOA 中增加一个 Memory Expert 角色，负责在每次对话开始时检索相关记忆，在结束时更新记忆。

2. **自动记忆提取**：类似 MemGPT 的 `conversation_search`，在对话结束后自动提取关键信息存入 Long Memory。

3. **Embedding-based Retrieval**：使用 embedding 做语义记忆检索（Phase 5 的 Semantic Cache 可以共享同一套 embedding 基础设施）。

### 不适用/暂不采用的设计

1. **LLM 完全自主管理**：MemGPT 让 LLM 完全自主决定何时换页。对于 Agent 场景，由 Planner 决定何时检索 Memory 更可控。
2. **Python 实现**：MemGPT/Letta 是 Python 项目，不能直接复用。但 Memory 存储格式（JSON + SQLite + embedding）可以借鉴。

## 实现建议

```go
type MemoryLayer int

const (
    MemoryWorking  MemoryLayer = iota  // 当前 turn
    MemorySession                      // 当前会话
    MemoryLong                         // 跨会话
    MemoryProject                      // 项目级
    MemoryUser                         // 用户级
)

type MemoryEntry struct {
    Layer      MemoryLayer
    Content    string
    Embedding  []float32  // 语义向量（可选）
    Timestamp  time.Time
    TTL        time.Duration
    Relevance  float64    // 动态计算
}
```

## 后续研究建议

1. 阅读 MemGPT 的 archival memory 实现（SQLite + Chroma 的向量存储方案）
2. 研究 Anthropic 的 Context Engineering Guide 中的 Memory 设计
3. 对比 Google ADK 的 Session/Memory 实现
