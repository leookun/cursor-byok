# Stage Report — Phase 3.4 Optimization Adapter Pool + Budget Estimate

**日期**：2026-07-13  
**目标对齐**：Engineering Charter Autonomous Loop + Agent OS Optimization Runtime

---

## 1. 当前研究成果

- MoA（Together AI）证明多专家协作可提升质量，但成本随专家数上升 → 必须在 **已配置 adapter 池** 内做 cost/quality 调度。
- Efficient Agents 列表强调 Latency/Cost/Compression 优先于复杂 Debate。
- 现有缺口：SelectOptimal 单点 no-op；AllocateBudget 忽略真实 prompt。

## 2. 参考论文与源码

| 来源 | 用途 |
|---|---|
| MoA arxiv 2406.04692 / togethercomputer/MoA | 分层专家与聚合 |
| MemGPT / AIOS | Runtime 分层与资源调度思路 |
| Awesome Efficient Agents | 成本与延迟优先 |
| 本仓 ADR-002/005/008、ModelAdapter、modelchannel | 禁止第二 Registry |

## 3. 架构设计

```
VirtualModelConfig (Planner/Nodes → AdapterID)
        │
        ▼
MOA.collectAdapterCandidates → ResolveChannel (existing)
        │
        ▼
optimize.SelectOptimalCandidate(role, pool) → adapter Key
        │
        ▼
ChannelService.CallAdapter (unchanged)

Forwarder.resolveProviderOutputBudget
        │
        ▼
AllocateBudgetWithEstimate(window, mode, estimatedPrompt)
```

## 4. 技术选型理由

| 采用 | 否决 |
|---|---|
| Candidate{Key, CostHint} 映射已有 channel | MOA 私有模型表 |
| 成本表 MatchProviderCostKey(modelID) | 仅 provider 字符串 |
| Budget 用 compiled prompt 估算 | MaxTokens/2 或固定比例无视 prompt |

兼容：Cursor 仍只见 `moa`；MITM/Forwarder/Config 为主集成面。

## 5. 与 Cursor BYOK 现有架构集成方式

- 装饰 `virtualmodel/moa` 与 `forwarder.resolveProviderOutputBudget`
- 复用 `ChannelService` / ModelAdapter 解析
- 配置仍在 `config.yaml` optimization + virtualModels

## 6. 实施计划

1. optimize：AllocateBudgetWithEstimate + SelectOptimalCandidate — ✅  
2. MOA：selectAdapterIDForRole + collectAdapterCandidates — ✅  
3. Forwarder 接线 — ✅  
4. 单测 — ✅  
5. ADR-009 + ROADMAP + 本报告 — ✅  

## 7. 风险分析

| 风险 | 缓解 |
|---|---|
| 池内选模覆盖角色显式绑定 | 文档说明 Tier 策略优先；关闭 Optimization 回退绑定 |
| 成本表不全 | 未知 model 不参与 min/max，回退第一候选 |
| 并行专家各自选同一最贵模型 | 后续可加 per-turn budget share |

## 8. Benchmark 方案

- 单元：Fast→mini、Ultra→opus；estimate 增大 output  
- 关/开 Optimization 对比 knobs 中 output_tokens（进程内）  
- 不强制付费 API E2E（goal non-goal）

## 9. 后续优化路线

1. spent 持久化到 usage 扩展  
2. ContextRuntime 真正消费 HistoryTokens 槽  
3. Tool/Cache 主路径深化  
4. Phase 4 Memory 注入  

## 10. 下一阶段开发计划

- Phase 3 收尾：spent 持久化  
- Phase 4：Context 包装 Compiler + Memory 五层注入  
- 保持 Charter 文档与代码同步  
