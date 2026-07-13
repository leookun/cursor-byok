# ADR-008: Optimization 配置落盘与 Cost Summary 暴露

**状态**：Accepted

**日期**：2026-07-13

**决策者**：AI Chief Architect

**关联**：ADR-005

---

## 背景

Phase 3 核心接线完成后，Optimization Runtime 仍使用硬编码 Balanced / $50，且成本摘要无法从 UI 观测。需要配置化与 Dashboard 集成。

## 决策

1. **配置 schema**（`config.yaml` → `optimization`）：
   - `enabled` bool
   - `qualityTier`：`fast|balanced|quality|ultra`
   - `monthlyBudgetUSD` float64
2. **热更新**：`config.Manager.Subscribe` → `Host.applyOptimizationConfig` 原地 `SetEnabled/SetQualityTier/SetMonthlyBudgetUSD`，不重建 mux。
3. **关闭语义**：Runtime 始终创建；`Enabled=false` 时 `AllocateBudget` 返回 `OutputTokens=0`（不覆盖用户 max tokens），`SelectOptimalProvider` 返回列表首项。
4. **Cost Summary**：`Host.GetCostSummary` + `ProxyService.GetOptimizationCostSummary` 暴露给 Wails；Home 卡片展示本月 spent/budget。
5. **顺带修复**：`NormalizeConfig` 保留 `virtualModels`（此前保存配置会丢失 MOA 设置）。

## 为何不用其它方案

| 方案 | 否决理由 |
|---|---|
| 关闭时 `optimize=nil` | 热开启需 rebuild 整条路由 |
| 成本写入 usage.json 立即持久化 | 与 historymetrics 模型不同；留作下一阶段 |
| 前端独立 localStorage 存 Tier | 与 backend 行为脱节 |

## 影响

- `server/config/types.go`、`host.go`、`client/config.go`、`bridge/proxy.go`
- 前端 `Config.vue`、`appState.js`、`HomeMetricsCard.vue`
- 进程重启后 spent 清零（tech debt）

## 后续

- adapter 池内真实 SelectOptimalProvider
- spent 持久化到 `usage.json` 扩展字段
