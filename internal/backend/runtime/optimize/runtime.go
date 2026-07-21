// Package optimize 实现 Optimization Runtime：Token Budget 管理 + Cost Optimizer。
// 动态分配 Token 预算，根据成本自动选择模型。
package optimize

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Runtime 是 Optimization Runtime 的主入口。
type Runtime struct {
	mu          sync.RWMutex
	enabled     bool
	budget      *TokenBudget
	costTracker *CostTracker
	qualityTier QualityTier
	// storePath 非空时，RecordCost 会 best-effort 落盘；NewRuntimeWithStore 会 hydrate。
	storePath string
	// spentMonth 当前累计所属 YYYY-MM（与落盘 yearMonth 对齐）。
	spentMonth string
	// closed 标记 Close 是否已调用（R14 lifecycle unification）。
	closed bool
}

// TokenBudget Token 预算分配。
type TokenBudget struct {
	// SystemPromptTokens 系统 prompt 固定占用。
	SystemPromptTokens int
	// RulesTokens 规则固定占用。
	RulesTokens int
	// MemoryTokens Memory 动态占用。
	MemoryTokens int
	// HistoryTokens 历史消息占用（剩余 * 0.6）。
	HistoryTokens int
	// ToolsTokens 工具定义占用。
	ToolsTokens int
	// OutputTokens 输出预算（剩余 * 0.3）。
	OutputTokens int
	// TotalTokens 总预算。
	TotalTokens int
	// RemainingTokens 剩余预算。
	RemainingTokens int
}

// CostTracker 成本跟踪器。
type CostTracker struct {
	// MonthlyBudgetUSD 月度预算（美元）。
	MonthlyBudgetUSD float64 `json:"monthlyBudgetUSD"`
	// SpentThisMonthUSD 本月已花费。
	SpentThisMonthUSD float64 `json:"spentThisMonthUSD"`
	// TurnsThisMonth 本月请求数。
	TurnsThisMonth int64 `json:"turnsThisMonth"`
	// EstimatedRemainingTurns 预估剩余可用请求数。
	EstimatedRemainingTurns int64 `json:"estimatedRemainingTurns"`
}

// QualityTier 质量等级。
type QualityTier string

const (
	TierFast     QualityTier = "fast"
	TierBalanced QualityTier = "balanced"
	TierQuality  QualityTier = "quality"
	TierUltra    QualityTier = "ultra"
)

// ProviderCost 各 provider 的 token 成本（每 1K tokens，美元）。
type ProviderCost struct {
	Provider string
	Input    float64 // 每 1K input tokens
	Output   float64 // 每 1K output tokens
}

// 默认成本表（近似值，实际因模型而异）
var defaultCosts = map[string]ProviderCost{
	"claude-opus":   {Provider: "claude-opus", Input: 0.015, Output: 0.075},
	"claude-sonnet": {Provider: "claude-sonnet", Input: 0.003, Output: 0.015},
	"claude-haiku":  {Provider: "claude-haiku", Input: 0.0008, Output: 0.004},
	"gpt-4o":        {Provider: "gpt-4o", Input: 0.005, Output: 0.015},
	"gpt-4o-mini":   {Provider: "gpt-4o-mini", Input: 0.00015, Output: 0.0006},
	"deepseek":      {Provider: "deepseek", Input: 0.00014, Output: 0.00028},
	"gemini-flash":  {Provider: "gemini-flash", Input: 0.000075, Output: 0.0003},
	"gemini-pro":    {Provider: "gemini-pro", Input: 0.00125, Output: 0.005},
}

// newRuntime 创建纯内存 Optimization Runtime（不落盘）。
func newRuntime(monthlyBudgetUSD float64, qualityTier QualityTier) *Runtime {
	if qualityTier == "" {
		qualityTier = TierBalanced
	}
	return &Runtime{
		enabled:     true,
		budget:      &TokenBudget{TotalTokens: 200000},
		costTracker: &CostTracker{MonthlyBudgetUSD: monthlyBudgetUSD},
		qualityTier: qualityTier,
		spentMonth:  CurrentYearMonth(),
	}
}

// NewRuntimeWithStore 创建 Runtime 并从 storePath hydrate 本月 spent/turns。
// storePath 为空时等价于 newRuntime。加载失败 fail-open（从零开始）。
func NewRuntimeWithStore(monthlyBudgetUSD float64, qualityTier QualityTier, storePath string) *Runtime {
	rt := newRuntime(monthlyBudgetUSD, qualityTier)
	rt.storePath = strings.TrimSpace(storePath)
	if rt.storePath == "" {
		return rt
	}
	rt.hydrateCostFromStore()
	return rt
}

// Close persists the cost snapshot (best-effort) and marks the runtime closed.
// Subsequent Close calls are no-ops. R14: lifecycle unification.
func (rt *Runtime) Close(ctx context.Context) error {
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return nil
	}
	rt.closed = true
	storePath := rt.storePath
	tracker := rt.costTracker
	ym := rt.spentMonth
	rt.mu.Unlock()
	if storePath != "" && tracker != nil {
		_ = SaveCostSnapshot(storePath, SnapshotFromTracker(tracker, ym))
	}
	return nil
}

// IsClosed reports whether Close has been invoked on this runtime.
func (rt *Runtime) IsClosed() bool {
	if rt == nil {
		return false
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.closed
}

func (rt *Runtime) hydrateCostFromStore() {
	if rt == nil || rt.storePath == "" {
		return
	}
	snap, err := LoadCostSnapshot(rt.storePath)
	if err != nil {
		return
	}
	ym := CurrentYearMonth()
	rt.mu.Lock()
	ApplySnapshotToTracker(rt.costTracker, snap, ym)
	rt.spentMonth = ym
	rt.mu.Unlock()
}

// SetEnabled 启用/禁用 Optimization 策略（成本仍可记录，便于 Dashboard）。
func (rt *Runtime) SetEnabled(enabled bool) {
	if rt == nil {
		return
	}
	rt.mu.Lock()
	rt.enabled = enabled
	rt.mu.Unlock()
}

// Enabled 返回是否启用预算/选模策略。
func (rt *Runtime) Enabled() bool {
	if rt == nil {
		return false
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.enabled
}

// AllocateBudget 为一次请求分配 Token 预算。
// estimatedPromptTokens>0 时：用实际 prompt 估算钳制 HistoryTokens，并把更多剩余留给 Output。
// mode 影响 output 比例（agent 工具回合偏输出，ask 偏均衡）。
func (rt *Runtime) AllocateBudget(ctx context.Context, contextWindowTokens int, mode string) *TokenBudget {
	return rt.AllocateBudgetWithEstimate(ctx, contextWindowTokens, mode, 0)
}

// AllocateBudgetWithEstimate 带 prompt 估算的预算分配（主链路应优先调用）。
func (rt *Runtime) AllocateBudgetWithEstimate(ctx context.Context, contextWindowTokens int, mode string, estimatedPromptTokens int) *TokenBudget {
	_ = ctx
	if rt == nil {
		return &TokenBudget{TotalTokens: contextWindowTokens}
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !rt.enabled {
		return &TokenBudget{TotalTokens: contextWindowTokens, OutputTokens: 0}
	}
	if contextWindowTokens <= 0 {
		contextWindowTokens = 200000
	}

	budget := &TokenBudget{
		TotalTokens:        contextWindowTokens,
		SystemPromptTokens: min(8192, contextWindowTokens/10),
		RulesTokens:        min(4096, contextWindowTokens/20),
		MemoryTokens:       min(16384, contextWindowTokens/8),
		ToolsTokens:        min(5120, contextWindowTokens/20),
	}

	fixed := budget.SystemPromptTokens + budget.RulesTokens + budget.MemoryTokens + budget.ToolsTokens
	remaining := contextWindowTokens - fixed
	if remaining <= 0 {
		remaining = contextWindowTokens / 2
	}

	// mode 调整 history/output 比例
	historyRatio := 6
	outputRatio := 3
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "ask", "chat":
		historyRatio, outputRatio = 5, 4
	case "plan":
		historyRatio, outputRatio = 4, 5
	case "agent", "edit", "debug":
		historyRatio, outputRatio = 5, 4
	}

	budget.HistoryTokens = remaining * historyRatio / 10
	budget.OutputTokens = remaining * outputRatio / 10
	budget.RemainingTokens = remaining - budget.HistoryTokens - budget.OutputTokens

	// 用实际 prompt 估算钳制：若已编译 prompt 明显小于 history 槽位，把差额转给 output
	if estimatedPromptTokens > 0 {
		// prompt 中已含 system/rules/tools 的粗估；history 槽不应超过 estimated 与 history 分配的较小者
		// 将 fixed 近似从 estimated 中剥离，得到 history 占用粗值
		histUsed := estimatedPromptTokens - fixed
		if histUsed < 0 {
			histUsed = estimatedPromptTokens
		}
		if histUsed < budget.HistoryTokens {
			freed := budget.HistoryTokens - histUsed
			budget.HistoryTokens = histUsed
			// 优先增大 output，保留一点 remaining
			boost := freed * 8 / 10
			budget.OutputTokens += boost
			budget.RemainingTokens += freed - boost
		}
		// 确保 output 不超过 window - estimatedPrompt - safety
		maxOut := contextWindowTokens - estimatedPromptTokens - 1024
		if maxOut < 1 {
			maxOut = 1
		}
		if budget.OutputTokens > maxOut {
			budget.OutputTokens = maxOut
		}
	}

	if budget.OutputTokens < 1 && rt.enabled {
		budget.OutputTokens = 1
	}

	rt.budget = budget
	return budget
}

// ProviderCandidate 表示从已有 ModelAdapter 池中选出的候选（禁止新建 registry）。
type ProviderCandidate struct {
	// Key 稳定标识，通常为 adapter channel ID。
	Key string
	// CostHint 用于成本表匹配的字符串（modelID 或 provider 名）。
	CostHint string
	// DisplayName 可选展示名。
	DisplayName string
}

// SelectOptimalCandidate 在用户已配置的 adapter 候选中按 QualityTier/预算选最优。
// 返回选中的 Key；池为空返回 ""。不创建任何新 Model Registry。
func (rt *Runtime) SelectOptimalCandidate(ctx context.Context, role string, candidates []ProviderCandidate) string {
	_ = ctx
	if rt == nil || len(candidates) == 0 {
		return ""
	}
	// 去重并保持顺序
	type item struct {
		key  string
		hint string
	}
	seen := make(map[string]struct{}, len(candidates))
	items := make([]item, 0, len(candidates))
	for _, c := range candidates {
		k := strings.TrimSpace(c.Key)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		hint := strings.TrimSpace(c.CostHint)
		if hint == "" {
			hint = k
		}
		items = append(items, item{key: k, hint: hint})
	}
	if len(items) == 0 {
		return ""
	}

	rt.mu.RLock()
	enabled := rt.enabled
	tier := rt.qualityTier
	budgetRemaining := 0.0
	if rt.costTracker != nil {
		budgetRemaining = rt.costTracker.MonthlyBudgetUSD - rt.costTracker.SpentThisMonthUSD
	}
	rt.mu.RUnlock()
	if !enabled {
		return items[0].key
	}

	// 按成本表在 items 上直接选（hint 用于 MatchProviderCostKey）
	pickCheapest := func() string {
		bestKey := items[0].key
		bestCost := 1e9
		found := false
		for _, it := range items {
			key := MatchProviderCostKey(it.hint)
			cost, ok := defaultCosts[key]
			if !ok {
				continue
			}
			if !found || cost.Input < bestCost {
				found = true
				bestCost = cost.Input
				bestKey = it.key
			}
		}
		return bestKey
	}
	pickBest := func() string {
		bestKey := items[0].key
		bestCost := -1.0
		found := false
		for _, it := range items {
			key := MatchProviderCostKey(it.hint)
			cost, ok := defaultCosts[key]
			if !ok {
				continue
			}
			if !found || cost.Output > bestCost {
				found = true
				bestCost = cost.Output
				bestKey = it.key
			}
		}
		return bestKey
	}
	pickForRole := func() string {
		switch strings.ToLower(role) {
		case "planner", "judge", "critic", "coding", "reasoning", "aggregator":
			return pickBest()
		case "research":
			return pickCheapest()
		default:
			return items[0].key
		}
	}

	switch tier {
	case TierFast:
		return pickCheapest()
	case TierUltra:
		return pickBest()
	case TierBalanced:
		if budgetRemaining < 5.0 {
			return pickCheapest()
		}
		return pickForRole()
	case TierQuality:
		return pickForRole()
	default:
		return items[0].key
	}
}

// EstimateCost 估算一次请求的成本。
// providerName 支持成本表键、model id 子串（如 "claude-sonnet-4"、"gpt-4o-mini"）。
func (rt *Runtime) EstimateCost(providerName string, promptTokens int, outputTokens int) float64 {
	key := MatchProviderCostKey(providerName)
	cost, ok := defaultCosts[key]
	if !ok {
		cost = ProviderCost{Input: 0.003, Output: 0.015} // 默认按 Claude Sonnet
	}
	return float64(promptTokens)*cost.Input/1000 + float64(outputTokens)*cost.Output/1000
}

// RecordCost 记录一次请求的成本，并在配置了 storePath 时 best-effort 落盘。
// 内存更新与写盘必须在同一把 mu 临界区内完成，否则并发 RecordCost 会出现
// last-writer-wins 用旧 snapshot 覆盖新 spent（内存 turns=N，磁盘 turns≪N）。
func (rt *Runtime) RecordCost(providerName string, promptTokens int, outputTokens int) {
	if rt == nil {
		return
	}

	// EstimateCost 只读 defaultCosts，不碰 Runtime 可变状态，可在锁外调用。
	cost := rt.EstimateCost(providerName, promptTokens, outputTokens)
	rt.mu.Lock()
	defer rt.mu.Unlock()

	ym := CurrentYearMonth()
	if rt.spentMonth != "" && rt.spentMonth != ym {
		rt.costTracker.SpentThisMonthUSD = 0
		rt.costTracker.TurnsThisMonth = 0
	}
	rt.spentMonth = ym
	rt.costTracker.SpentThisMonthUSD += cost
	rt.costTracker.TurnsThisMonth++

	if rt.storePath == "" {
		return
	}
	// fail-open：磁盘错误不影响主链路内存状态；持锁写保证并发安全。
	_ = SaveCostSnapshot(rt.storePath, SnapshotFromTracker(rt.costTracker, ym))
}

// GetCostSummary 获取成本摘要。
func (rt *Runtime) GetCostSummary() *CostTracker {
	if rt == nil {
		return &CostTracker{}
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	tracker := *rt.costTracker
	if tracker.TurnsThisMonth > 0 && tracker.SpentThisMonthUSD > 0 {
		avgCost := tracker.SpentThisMonthUSD / float64(tracker.TurnsThisMonth)
		remaining := tracker.MonthlyBudgetUSD - tracker.SpentThisMonthUSD
		if remaining > 0 {
			tracker.EstimatedRemainingTurns = int64(remaining / avgCost)
		}
	}
	return &tracker
}

// QualityTier 返回当前质量等级。
func (rt *Runtime) QualityTier() QualityTier {
	if rt == nil {
		return TierBalanced
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.qualityTier
}

// SetQualityTier 更新质量等级（供配置热更新使用）。
func (rt *Runtime) SetQualityTier(tier QualityTier) {
	if rt == nil {
		return
	}
	if tier == "" {
		tier = TierBalanced
	}
	rt.mu.Lock()
	rt.qualityTier = tier
	rt.mu.Unlock()
}

// SetMonthlyBudgetUSD 更新月度预算。
func (rt *Runtime) SetMonthlyBudgetUSD(usd float64) {
	if rt == nil {
		return
	}
	if usd < 0 {
		usd = 0
	}
	rt.mu.Lock()
	rt.costTracker.MonthlyBudgetUSD = usd
	rt.mu.Unlock()
}

// MatchProviderCostKey 将 model/provider 字符串映射到成本表键（导出供测试与适配）。
func MatchProviderCostKey(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return ""
	}
	// 精确命中
	if _, ok := defaultCosts[n]; ok {
		return n
	}
	// 子串匹配：优先更具体的键
	candidates := []string{
		"claude-opus", "claude-sonnet", "claude-haiku",
		"gpt-4o-mini", "gpt-4o",
		"gemini-flash", "gemini-pro",
		"deepseek",
	}
	for _, key := range candidates {
		if strings.Contains(n, key) {
			return key
		}
	}
	// 宽松别名
	switch {
	case strings.Contains(n, "opus"):
		return "claude-opus"
	case strings.Contains(n, "sonnet"):
		return "claude-sonnet"
	case strings.Contains(n, "haiku"):
		return "claude-haiku"
	case strings.Contains(n, "gpt-4o-mini") || strings.Contains(n, "4o-mini"):
		return "gpt-4o-mini"
	case strings.Contains(n, "gpt-4o") || strings.Contains(n, "gpt4o"):
		return "gpt-4o"
	case strings.Contains(n, "flash"):
		return "gemini-flash"
	case strings.Contains(n, "gemini"):
		return "gemini-pro"
	case strings.Contains(n, "deepseek"):
		return "deepseek"
	default:
		return ""
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// 确保 time 包被使用
var _ = time.Now

// Summary returns a compact cost-efficiency line for evolution/benchmark evidence.
func (t *CostTracker) Summary() string {
	if t == nil {
		return "optimize cost: n/a"
	}
	return fmt.Sprintf("optimize spent=%.4f budget=%.4f turns=%d remainingTurns=%d",
		t.SpentThisMonthUSD, t.MonthlyBudgetUSD, t.TurnsThisMonth, t.EstimatedRemainingTurns)
}
