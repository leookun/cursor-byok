# Research Note: Mixture of Agents (Together AI)

**论文**：arxiv.org/abs/2406.04692
**源码**：github.com/togethercomputer/MoA
**博客**：together.ai/blog/together-moa
**日期**：2026-07-13

---

## 核心发现

MoA 提出了一种分层多模型协作架构：

```
Layer 1: Proposers (多个 LLM 独立生成候选回答)
    ↓
Layer 2: Aggregator (一个高质量 LLM 融合多个候选回答)
    ↓
Final Output
```

关键洞察：
1. **模型多样性** > 单个最强模型。多个不同模型生成的回答互补性很强。
2. **Aggregator 是瓶颈**。Aggregator 的质量决定了最终输出质量。
3. **分层可以叠加**。可以有多层 Proposer → Aggregator 链。

## 对 Cursor BYOK 的启示

### 可直接借鉴的设计

1. **Planner → Experts → Aggregator 链路**：我们在 MOA 中已经实现了这个模式，但 Together 的论文提供了更多的实验证据。

2. **模型多样性策略**：
   - Together 建议 Proposer 应该使用不同类型/厂商的模型
   - 我们的 MOA 允许为每个 Expert 绑定不同 adapter，天然支持多样性

3. **Aggregator 的 prompt 设计**：
   - Together 的 aggregator prompt 不引用专家名称
   - 我们的 `buildAggregatorPrompt()` 已经采用了相同的策略

### 可以改进的地方

1. **Layered MoA**：当前 MOA 是单层 Expert → Aggregator。可以支持多层：
   ```
   Planner → Experts Layer 1 → Intermediate Aggregator → Experts Layer 2 → Final Aggregator
   ```
   对应 Workflow 配置中增加 Layer 概念。

2. **Proposer 多样性评分**：在选择 Expert 时，可以考虑模型之间的多样性（如 provider 不同、架构不同），而不仅仅是功能匹配。

3. **Temperature 多样性**：即使同一个模型，不同 temperature 也能产生多样化的输出。可以在 Expert 配置中增加 temperature 参数。

### 不适用/暂不采用的设计

1. **固定 Proposer 集合**：Together 的 MoA 使用固定的 Proposer 集合（6 个模型）。我们的 Planner 动态选择 Expert，更灵活。
2. **仅文本融合**：Together 的 Aggregator 只做文本融合。我们增加了 Critic 和 Judge 节点，提供更强的质量保证。

## 参考实现细节

Together 的 MoA 实现（Python）：
```python
# 核心循环
for layer in range(layers):
    responses = []
    for model in models[layer]:
        response = model.generate(references + prompt)
        responses.append(response)
    references = aggregate(responses, aggregator_model)
```

## 后续研究建议

1. 阅读 Together MoA 的 AlpacaEval 2.0 benchmark 数据，了解不同模型组合的质量提升
2. 研究 Anthropic 的 Constitutional AI 对 Aggregator 的 prompt 设计启示
3. 关注 OpenAI 的 "Many-Shot In-Context Learning" 对多层 MoA 的启示
