package pet

import (
	"math/rand"
	"time"
)

// Intent 是 Behavior AI 决策出的"意图"（与 FSM 状态解耦的高层语义）。
// Behavior 不再直接 roll 随机数切换状态，而是由 IntentDecider 产出意图，
// 再映射到 FSM 状态（见 BehaviorSystem.applyIntent）。
type Intent int

const (
	IntentIdle Intent = iota
	IntentWalk
	IntentJump
	IntentWave
	IntentSit
	IntentSleep
)

// String 调试用名称。
func (i Intent) String() string {
	switch i {
	case IntentWalk:
		return "walk"
	case IntentJump:
		return "jump"
	case IntentWave:
		return "wave"
	case IntentSit:
		return "sit"
	case IntentSleep:
		return "sleep"
	default:
		return "idle"
	}
}

// BehaviorContext 是意图决策的上下文快照（在引擎线程采集，只读）。
type BehaviorContext struct {
	// IdleSeconds 连续空闲（无 Agent/Review/交互）的秒数。
	IdleSeconds float64
	// AgentBusy Agent 正在工作（waiting/thinking）。
	AgentBusy bool
	// Reviewing 正在 Review。
	Reviewing bool
	// LastInteractionAt 距上次用户交互（拖拽/点击）的秒数；<0 表示无记录。
	LastInteractionSec float64
	// RNG 随机源（可注入以便测试确定性）。
	RNG *rand.Rand
}

// IntentDecider 是 Behavior AI 的核心：根据上下文产出下一个意图。
//
// v2 Phase 6：把原来散落在 doWalkOrRandom 里的"硬编码 roll 概率"升级为
// 带上下文感知的决策器，使行为更自然、可配置、可测试：
//   - Agent 忙碌 / Review 中不发起随机动作（避免打断思考）。
//   - 刚交互完（拖拽）的短时间内降低动作概率，让宠安静一会儿。
//   - 空闲越久，越倾向于 sit/sleep（拟人化"待久了就歇着"）。
//   - 其余情况按权重在 walk/jump/wave 间选择。
type IntentDecider struct {
	// 基础动作权重（归一化前）。
	weightWalk  float64
	weightJump  float64
	weightWave  float64
	weightSit   float64
	weightSleep float64

	// 交互后冷却秒数：在此期间大幅降低随机动作概率。
	interactionCooldown float64
}

// NewIntentDecider 创建默认决策器。
func NewIntentDecider() *IntentDecider {
	return &IntentDecider{
		weightWalk:          6,
		weightJump:          1,
		weightWave:          3,
		weightSit:           2,
		weightSleep:         1,
		interactionCooldown: 8,
	}
}

// Decide 根据上下文产出下一个意图。
func (d *IntentDecider) Decide(ctx BehaviorContext) Intent {
	rng := ctx.RNG
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	// 1) Agent 忙碌或 Review 中：不发起自主动作，保持当前状态（idle/think/focus）。
	if ctx.AgentBusy || ctx.Reviewing {
		return IntentIdle
	}

	// 2) 刚交互完的冷却期：大概率什么都不做（安静一会儿）。
	if ctx.LastInteractionSec >= 0 && ctx.LastInteractionSec < d.interactionCooldown {
		// 冷却期内 80% 概率保持 idle，仅 20% 允许轻微动作。
		if rng.Float64() < 0.8 {
			return IntentIdle
		}
	}

	// 3) 空闲越久，越倾向于 sit/sleep。
	// idleFactor 随空闲秒数在 0->1 增长（约 60s 趋于饱和）。
	idleFactor := clamp01(ctx.IdleSeconds / 60.0)

	// 动态权重：walk 随空闲略降，sit/sleep 随空闲升高。
	wWalk := d.weightWalk * (1 - 0.5*idleFactor)
	wSit := d.weightSit + 4*idleFactor
	wSleep := d.weightSleep + 3*idleFactor
	wJump := d.weightJump
	wWave := d.weightWave

	// 4) 加权随机挑选动作意图。
	total := wWalk + wJump + wWave + wSit + wSleep
	r := rng.Float64() * total
	switch {
	case r < wWalk:
		return IntentWalk
	case r < wWalk+wJump:
		return IntentJump
	case r < wWalk+wJump+wWave:
		return IntentWave
	case r < wWalk+wJump+wWave+wSit:
		return IntentSit
	default:
		return IntentSleep
	}
}

// clamp01 限制在 [0,1]。
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
