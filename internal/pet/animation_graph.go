package pet

// AnimationGraph 是状态→动画的上下文感知路由图（Phase 9）。
//
// 取代原来 AnimationResolver 的简单 map[*State]string 查表：
//   - 同一个 State 可根据上下文（前一个状态、空闲时长、Agent 状态等）路由到不同动画。
//   - 支持多级回退：特定规则未命中时退回通用映射。
//   - 插件/外部可注册自定义路由规则，不修改引擎核心。
//
// 典型用法：
//   StateWalking + 从 StateIdle 进入 → "walk"（带起步过渡）
//   StateWalking + 从 StateWaiting 恢复 → "walk_fast"（快走回来）
//   StateIdle + 空闲>30s → "idle_bored"（无聊变体）
//   StateIdle + 默认 → "idle"
//
// 线程：AnimationGraph 在构建后为只读数据结构（规则不变），
// 因此 Resolve 无需加锁，引擎线程可直接调用。

// AnimationContext 是动画解析的上下文快照（只读）。
type AnimationContext struct {
	// From 前一状态名（"" 表示初始/未知）。
	From string
	// To 目标状态名。
	To string
	// IdleSeconds 当前连续空闲秒数（<=0 表示不空闲或未知）。
	IdleSeconds float64
	// AgentBusy Agent 是否正在工作。
	AgentBusy bool
}

// AnimationRule 定义一条动画路由规则。
// 每条规则包含一组可选匹配条件；条件全部匹配（或为空）时命中。
// 多条规则按 Priority 降序排列，高优先级规则先匹配。
type AnimationRule struct {
	// Priority 规则优先级（越大越先匹配）。默认 0。
	Priority int

	// 匹配条件（均为可选，nil/零值表示不限制该条件）：
	// ToState 目标状态名（必填，否则规则无意义）。
	ToState string
	// FromState 前一状态名（"" 表示不限制）。
	FromState string
	// MinIdleSeconds 最小空闲秒数（0 表示不限制）。
	MinIdleSeconds float64
	// WhenAgentBusy 仅在 Agent 忙碌时匹配（nil 表示不限制）。
	WhenAgentBusy *bool

	// AnimName 命中此规则时应播放的动画名。
	AnimName string
}

// AnimationGraph 维护一组动画路由规则，按优先级匹配。
type AnimationGraph struct {
	rules    []AnimationRule // 按 Priority 降序排列
	fallback map[*State]string // 无规则命中时的兜底映射（兼容旧 Resolver）
}

// NewAnimationGraph 创建动画路由图，内置默认规则 + 兜底映射。
func NewAnimationGraph() *AnimationGraph {
	g := &AnimationGraph{
		fallback: map[*State]string{
			StateIdle:      AnimIdle,
			StateWalking:   AnimWalk,
			StateSitting:   AnimSit,
			StateSleeping:  AnimSleep,
			StateWaiting:   AnimThink,
			StateReviewing: AnimFocus,
			StateWaving:    AnimWave,
			StateJumping:   AnimHappy,
			StateDragging:  AnimIdle,
			StateFailed:    AnimHappy,
		},
	}
	return g
}

// RegisterRule 注册一条自定义路由规则。
// 规则按优先级自动排序（高优先在前）。
func (g *AnimationGraph) RegisterRule(r AnimationRule) {
	if r.ToState == "" {
		return
	}
	g.rules = append(g.rules, r)
	// 按 Priority 降序重排（简单插入排序即可，规则数量不会很大）。
	for i := len(g.rules) - 1; i > 0 && g.rules[i].Priority > g.rules[i-1].Priority; i-- {
		g.rules[i], g.rules[i-1] = g.rules[i-1], g.rules[i]
	}
}

// Resolve 根据状态和上下文返回应播放的动画名。
// 先遍历规则表（优先级高→低），命中则返回对应动画；
// 无规则命中时回退到旧 fallback 映射。
func (g *AnimationGraph) Resolve(s *State, ctx AnimationContext) string {
	if s == nil {
		return AnimIdle
	}
	// 填充 ctx.To（兼容仅传 State 的调用方）。
	if ctx.To == "" {
		ctx.To = s.Name
	}
	// 1) 尝试匹配自定义规则。
	for _, r := range g.rules {
		if g.match(r, s, ctx) {
			return r.AnimName
		}
	}
	// 2) 回退到旧映射（保持向后兼容）。
	if anim, ok := g.fallback[s]; ok {
		return anim
	}
	return AnimIdle
}

// match 判断规则是否匹配当前状态与上下文。
func (g *AnimationGraph) match(r AnimationRule, s *State, ctx AnimationContext) bool {
	// 目标状态必须匹配。
	if r.ToState != "" && r.ToState != s.Name {
		return false
	}
	// 前一状态匹配（可选）。
	if r.FromState != "" && r.FromState != ctx.From {
		return false
	}
	// 空闲秒数阈值（可选）。
	if r.MinIdleSeconds > 0 && ctx.IdleSeconds < r.MinIdleSeconds {
		return false
	}
	// Agent 忙碌状态匹配（可选）。
	if r.WhenAgentBusy != nil && ctx.AgentBusy != *r.WhenAgentBusy {
		return false
	}
	return true
}

// ResolveSimple 便捷方法：仅传 State，上下文为空（向后兼容旧 Resolve 调用）。
func (g *AnimationGraph) ResolveSimple(s *State) string {
	return g.Resolve(s, AnimationContext{})
}

// Ensure AnimationGraph implements Lifecycle (no-op).
func (g *AnimationGraph) Start() {}
func (g *AnimationGraph) Stop() {}
func (g *AnimationGraph) Dispose() {
	g.rules = nil
	g.fallback = nil
}

var _ Lifecycle = (*AnimationGraph)(nil)
