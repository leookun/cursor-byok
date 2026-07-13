# Research Note: Awesome Multi-Agent & Efficient Agents

**日期**：2026-07-13  
**状态**：Living document  
**用途**：为 Cursor BYOK Agent OS 提供持续研究入口，约束选型与演进优先级。

---

## 1. 资料源

| List | URL | 对本项目重点 |
|---|---|---|
| Awesome Multi-Agent Papers | https://github.com/kyegomez/awesome-multi-agent-papers | Debate、Collaboration、MAS、Communication、AgentScope、LongAgent |
| Awesome Efficient Agents | https://github.com/yxf203/Awesome-Efficient-Agents | Memory、Cache、Planning、Hierarchical Memory、Compression、Latency |
| Awesome Context Engineering | https://github.com/Meirtz/Awesome-Context-Engineering | Prompt optimization、Ranking、Compaction、Prompt/Semantic Cache |

---

## 2. 对 Cursor BYOK 的映射

| 研究方向 | 对应 Runtime | 当前落地 | 下一动作 |
|---|---|---|---|
| Multi-agent collaboration / Debate | Virtual Model Runtime | MOA（Planner→Experts→Judge→Aggregator） | Phase 7：Debate / Best-of-N / Nested Critic |
| Hierarchical Memory | Memory + Context Runtime | `runtime/memory` 五层骨架 | Phase 4：注入主链路 |
| Compression | Context Runtime | `forwarder/compaction.go` 生产级 | ContextRuntime 包装 Compiler |
| Cache / Semantic Cache | Cache Runtime | 精确 + 语义框架已接 Provider | 校准阈值、命中率 Dashboard |
| Latency / Cost | Optimization Runtime | Budget + RecordCost 已接 Forwarder/MOA | 配置化 Tier、真实 usage 记账 ✅ |
| Communication / MAS protocols | Workflow / Plugin（远期） | 无 | 先稳定单会话 Runtime，再抽象消息总线 |

---

## 3. 选型原则（Evidence-Based）

1. **优先复用主链路能力**：Compiler、Compaction、ModelAdapter、HistoryMetrics — 不平行造第二套。
2. **装饰器集成（ADR-003）**：Runtime 可 nil 回退，渐进接入。
3. **Virtual Model 对 Cursor 透明**：协作图只在 BYOK 内部展开。
4. **效率优先于炫技**：Efficient Agents 列表中的 Cache/Memory/Compression 优先于复杂 Debate（成本倍增）。

---

## 4. 主动发现的架构问题（持续清单）

| 问题 | 证据 | 建议 |
|---|---|---|
| Runtime 框架与主链路不同步 | ROADMAP 标 Phase 3 计划中，但 host/forwarder 已部分接线 | 以代码为准更新 Roadmap；补测试与配置化 |
| Cost 记账曾用 MaxTokens/2 估算 | `recordProviderCost` 旧实现 | 改用 provider usage / 字符估算 |
| SelectOptimalProvider 只有单 provider | MOA expert 绑定固定 AdapterID | 在用户配置的 adapter 池内选，禁止新建 registry |
| AllocateBudget 未消费 mode / 实际 prompt size | `optimize.AllocateBudget` | Phase 3.1：按 mode 与 estimated prompt 动态切分 |

---

## 5. 后续研究任务

- [ ] 从 Awesome Efficient Agents 摘 3 篇 Compression/Latency 论文做精读笔记
- [ ] 从 Awesome Multi-Agent 摘 Debate / AgentScope 对比 MOA workflow
- [ ] 对照 Anthropic Prompt Cache 与本项目 cache frontier 实现差异
