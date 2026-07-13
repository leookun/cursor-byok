# Research Note: AutoGen — Multi-Agent Conversation Framework

**论文**：arxiv.org/abs/2308.08155
**源码**：github.com/microsoft/autogen
**日期**：2026-07-13

---

## 核心发现

AutoGen 提出了"可对话 Agent"的概念，核心架构：

```
ConversableAgent (基类)
    ├── AssistantAgent (LLM-backed)
    ├── UserProxyAgent (Human/Code executor)
    └── GroupChatManager (多 Agent 协调)
```

关键洞察：
1. **Agent 之间的对话是核心原语**：AutoGen 将 Agent 间的交互建模为对话（message exchange），而非函数调用。
2. **GroupChat**：多个 Agent 在一个共享对话中协作，Manager 决定下一个发言者。
3. **Code Executor**：UserProxyAgent 可以在沙箱中执行代码。
4. **Teachability**：Agent 可以从对话中学习并记住用户偏好。

## 对 Cursor BYOK 的启示

### 可直接借鉴的设计

1. **GroupChat 模式**：AutoGen 的 GroupChat 类似我们的 MOA，但 AutoGen 的 Manager 是"轮流发言"而非"并行执行+聚合"。我们的并行专家模式在延迟上更优，但 AutoGen 的轮流模式在复杂多步推理上可能更好。

2. **可教性 (Teachability)**：AutoGen 的 `teachability` 机制让 Agent 记住用户的纠正和偏好。这可以映射到我们的 User Memory 层。

3. **代码执行隔离**：AutoGen 的 Docker 沙箱执行。我们的 ExecBridge 目前直接执行 shell 命令，可以借鉴沙箱化思路。

### 可以改进的地方

1. **Speaker Selection**：AutoGen 的 GroupChatManager 使用 LLM 选择下一个发言者。我们的 Planner 类似但更结构化（输出 JSON plan）。

2. **Nested Chat**：AutoGen 支持嵌套对话（一个 Agent 在回复前先与其他 Agent 私下对话）。这对我们的 Critic → Expert 反馈循环很有启发。

3. **Tool Use 标准化**：AutoGen 使用统一的 tool schema。我们的 Tool Runtime 正在向这个方向演进。

### 不适用/暂不采用的设计

1. **Python 生态绑定**：AutoGen 深度依赖 Python，不能直接复用。
2. **对话式而非 DAG 式**：AutoGen 的工作流是对话驱动的（谁下一个发言），不如 DAG（我们的 Workflow Node）可控。

## 后续研究建议

1. 阅读 AutoGen 0.4 的 Magentic-One 架构（更接近我们的 MOA）
2. 研究 AutoGen 的 teachability 实现（记忆更新策略）
