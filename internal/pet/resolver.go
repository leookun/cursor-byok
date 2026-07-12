package pet

// AnimationResolver 负责把"状态机状态"映射为"应播放的动画名"。
// 这样 Behavior 不再直接调用 animCtrl.Play(具体动画名)，而是：
//
//	fsm.Transition(StateWalking)  ──►  resolver.Resolve(state)  ──►  animCtrl.Play("walk")
//
// 状态机成为动画选择的唯一中心，未来新增状态只需在这里登记映射。
type AnimationResolver struct {
	// stateToAnim 状态 -> 动画名
	stateToAnim map[*State]string
}

// NewAnimationResolver 创建默认映射（与现有行为系统一一对应）。
func NewAnimationResolver() *AnimationResolver {
	return &AnimationResolver{
		stateToAnim: map[*State]string{
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
}

// Resolve 返回给定状态应播放的动画名；未知状态回退到 idle。
func (r *AnimationResolver) Resolve(s *State) string {
	if s == nil {
		return AnimIdle
	}
	if anim, ok := r.stateToAnim[s]; ok {
		return anim
	}
	return AnimIdle
}
