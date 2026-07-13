# ADR-009: Optimization adapter 池选模 + Budget 使用 prompt 估算

**状态**：Accepted

**日期**：2026-07-13

**决策者**：AI Chief Architect

**关联**：ADR-002（禁止 MOA 自建 Registry）、ADR-005、ADR-008

---

## 背景

Optimization Runtime 的 `SelectOptimalProvider` 在 MOA 路径上仅传入单一 provider，无法在用户已配置的多个 ModelAdapter 之间切换。`AllocateBudget` 未消费已编译 prompt 估算，output 预算与真实上下文脱节。

## 决策

1. **`SelectOptimalCandidate(role, []ProviderCandidate)`**  
   - Candidate.Key = 已有 adapter channel ID  
   - Candidate.CostHint = modelID（成本表匹配）  
   - 候选仅来自 `VirtualModelConfig` 的 Planner/Nodes 绑定

2. **MOA `selectAdapterIDForRole`**  
   - 收集配置中 enabled 绑定 → 解析 ChannelInfo → 调用 SelectOptimalCandidate  
   - 关闭 Optimization 或池为空时回退 `resolveAdapterForRole`  
   - **不**维护任何 MOA 私有 Model Registry

3. **`AllocateBudgetWithEstimate(window, mode, estimatedPrompt)`**  
   - mode 调整 history/output 比例  
   - estimatedPrompt 钳制 history 槽，释放额度给 output，且 `output ≤ window - prompt - safety`

4. **Forwarder** `resolveProviderOutputBudget` 调用 `AllocateBudgetWithEstimate` 并传入 `estimateCompiledPromptTokens`

## 否决方案

| 方案 | 理由 |
|---|---|
| MOA 内建模型目录 | 违反 ADR-002 / Charter |
| 仅按 provider 类型字符串切换 | 与 channel ID 解析脱节，无法 CallAdapter |
| 用 max_tokens/2 估 output | 失真（已在 ADR-005 修订中否决） |

## 兼容性

- Cursor 仍只见 `moa` channel  
- MITM / Forwarder / Compiler / Config 仍是集成面  
- optimize == nil 或 Enabled=false 行为与原先回退一致

## 测试

- `optimize.TestSelectOptimalCandidate_*`  
- `optimize.TestAllocateBudgetWithEstimate_*`  
- `moa.TestSelectAdapterIDForRole_*` / `TestCollectAdapterCandidates_*`

## 生产 ChannelService 接线（同日修订）

`host.buildVirtualModelManager` 必须注入 `moa.NewAdapterChannelService(host.configs)`：

- `ResolveChannel` → `config.Manager.SelectChannelForModel`（用户 modelAdapters）
- `CallAdapter` → `modeladapter.Router.Stream`（已有 OpenAI/Anthropic 适配器）

禁止 `NewMOAModelWithOptimize(..., nil, opt)` 作为生产路径。回归测试：`TestBuildVirtualModelManager_WiresNonNilChannelService`。

## 后续

- spent 跨进程持久化  
- 单 turn 多专家并行时的预算共享  
- Context Runtime 与 HistoryTokens 槽位真正对接 Compiler 裁剪  
