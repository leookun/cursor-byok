package pet

import (
	"math/rand"
	"time"
)

// IntentResolver 负责把 IntentDecider 产出的意图映射为 FSM 状态 + 副作用规格（Phase 9）。
//
// 原来 BehaviorSystem.applyIntent 中硬编码的 switch 分支被提取到这里，
// 使意图→状态→副作用这条链路可独立测试、可扩展、可被插件覆盖。
//
// 核心设计：
//   - Resolve 返回 IntentResolution：包含目标 State、转移方式、可选副作用。
//   - BehaviorSystem 只负责执行 Resolution，不再自行决策副作用细节。
//   - 插件可通过 SetResolver 替换默认映射（如自定义状态机）。
//
// 线程：IntentResolver 为纯函数（无内部可变状态），并发安全。

// TransitionMode 表示状态转移的方式。
type TransitionMode int

const (
	// TransitionNormal 常规转移（走转移表+优先级检查）。
	TransitionNormal TransitionMode = iota
	// TransitionForce 强制转移（无视优先级与转移表）。
	TransitionForce
	// TransitionInterrupt 打断转移（记录 returnTo，超时后恢复）。
	TransitionInterrupt
	// TransitionNone 不转移（保持当前状态）。
	TransitionNone
)

// IntentResolution 是意图解析的结果。
// BehaviorSystem 根据此结果执行状态转移与副作用调度。
type IntentResolution struct {
	// Target 目标状态（nil 表示保持当前）。
	Target *State
	// Mode 转移方式。
	Mode TransitionMode
	// SideEffects 附加副作用规格（可选）。
	SideEffects *IntentSideEffects
}

// IntentSideEffects 描述意图执行时附带的副作用。
type IntentSideEffects struct {
	// MotionTarget 移动目标坐标（仅 IntentWalk 时非 nil）。
	MotionTarget *MotionTarget
	// TimeoutBack 超时后自动回到 Idle 的延迟（<=0 表示无超时兜底）。
	TimeoutBack time.Duration
	// OnApplied 在转移成功后调用的回调（可选，如记录统计）。
	OnApplied func()
}

// MotionTarget 移动目标坐标。
type MotionTarget struct {
	X, Y int
}

// IntentResolver 将意图映射为状态+副作用。
// 默认实现与现有 applyIntent 行为完全一致。
type IntentResolver struct {
	// rng 随机源（用于 Walk 随机目标）。
	rng *rand.Rand
}

// NewIntentResolver 创建默认意图解析器。
func NewIntentResolver() *IntentResolver {
	return &IntentResolver{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Resolve 根据意图和当前状态返回解析结果。
// current 为当前 FSM 状态（用于条件判断，如 Sit 需 Idle）。
func (r *IntentResolver) Resolve(intent Intent, current *State) IntentResolution {
	switch intent {
	case IntentWalk:
		return IntentResolution{
			Target: StateWalking,
			Mode:   TransitionNormal,
			SideEffects: &IntentSideEffects{
				MotionTarget: r.randomWalkTarget(),
				TimeoutBack:  walkDuration,
			},
		}

	case IntentJump:
		return IntentResolution{
			Target: StateJumping,
			Mode:   TransitionNormal,
			SideEffects: &IntentSideEffects{
				TimeoutBack: jumpDuration,
			},
		}

	case IntentWave:
		return IntentResolution{
			Target: StateWaving,
			Mode:   TransitionNormal,
			SideEffects: &IntentSideEffects{
				TimeoutBack: waveDuration,
			},
		}

	case IntentSit:
		// 仅当当前为 Idle 时才能坐下。
		if current == StateIdle {
			return IntentResolution{
				Target: StateSitting,
				Mode:   TransitionNormal,
			}
		}
		return IntentResolution{Mode: TransitionNone}

	case IntentSleep:
		// Sitting → Sleeping 自然递进，或 Idle → Sleeping 跳过坐下。
		if current == StateSitting || current == StateIdle {
			return IntentResolution{
				Target: StateSleeping,
				Mode:   TransitionForce,
			}
		}
		return IntentResolution{Mode: TransitionNone}

	default: // IntentIdle
		return IntentResolution{Mode: TransitionNone}
	}
}

// randomWalkTarget 生成随机步行目标（屏幕内安全区域）。
func (r *IntentResolver) randomWalkTarget() *MotionTarget {
	x := 50 + r.rng.Intn(screenDefaultW-100-200) // 默认窗口宽度约 200
	y := 50 + r.rng.Intn(screenDefaultH-100-200)
	return &MotionTarget{X: x, Y: y}
}

// SetSeed 设置随机种子（用于测试确定性）。
func (r *IntentResolver) SetSeed(seed int64) {
	r.rng = rand.New(rand.NewSource(seed))
}

// Ensure IntentResolver implements Lifecycle (no-op).
func (r *IntentResolver) Start() {}
func (r *IntentResolver) Stop() {}
func (r *IntentResolver) Dispose() {}

var _ Lifecycle = (*IntentResolver)(nil)
