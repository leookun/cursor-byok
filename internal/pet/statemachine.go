package pet

import (
	"log"
	"sync"
	"time"
)

// 状态名称常量（与 pet.json 中的动画名对应）。
const (
	StateNameSleeping  = "sleeping"
	StateNameIdle      = "idle"
	StateNameSitting   = "sitting"
	StateNameWalking   = "walking"
	StateNameWaiting   = "waiting"
	StateNameReviewing = "reviewing"
	StateNameWaving    = "waving"
	StateNameJumping   = "jumping"
	StateNameDragging  = "dragging"
	StateNameFailed    = "failed"
)

// State 表示一个桌宠状态（纯数据，可安全跨 Engine 实例共享）。
//
// v2 Phase 5 升级：状态机从"仅优先级比较的简单切换"升级为完整状态机。
// 状态的副作用（播放动画、清理、超时）不再挂在 State 上（避免污染包级全局变量），
// 而是由 Engine 注入的引擎级钩子统一处理，见 StateMachine.EnterHook/ExitHook/
// TimeoutHook。
type State struct {
	Name     string
	Priority int
	// Timeout 进入该状态后自动触发的超时时长；<=0 表示无超时。
	Timeout time.Duration
}

var (
	// 短行为状态设置 Timeout 作为兜底：即便行为定时器异常未触发回 idle，
	// FSM 也会在超时后自动恢复，避免"卡死在某动作"的 Bug。
	StateSleeping  = &State{StateNameSleeping, 0, 0}
	StateIdle      = &State{StateNameIdle, 1, 0}
	StateSitting   = &State{StateNameSitting, 2, 0}
	StateWalking   = &State{StateNameWalking, 3, 3500 * time.Millisecond}
	StateWaiting   = &State{StateNameWaiting, 4, 0}
	StateReviewing = &State{StateNameReviewing, 5, 0}
	StateWaving    = &State{StateNameWaving, 6, 3000 * time.Millisecond}
	StateJumping   = &State{StateNameJumping, 7, 2500 * time.Millisecond}
	StateDragging  = &State{StateNameDragging, 8, 0}
	StateFailed    = &State{StateNameFailed, 9, 0}
)

// StateMachine 管理桌宠状态转换。
//
// 转移规则（优先级 + 显式转移表双重保障）：
//   - Allow(from, to) 注册"允许的直接转移"。
//   - Transition/Interrupt 会先查转移表；转移表未登记时退化为"严格更高优先级"规则
//     （保持向后兼容）。
//   - Interrupt 允许高优先级状态打断低优先级状态，并记录来源(returnTo)，
//     被 Interrupt 的状态在超时后自动回到 returnTo，实现"临时打断后恢复"。
//
// 副作用通过引擎级钩子注入（EnterHook/ExitHook/TimeoutHook），使 State 保持纯数据。
type StateMachine struct {
	current  *State
	mu       sync.RWMutex
	allowed  map[*State]map[*State]bool // 显式转移白名单
	returnTo *State                     // 被 Interrupt 打断前的来源状态

	// OnChanged 在状态成功切换时回调（由 Engine 注入，内部发布 EventStateChanged）。
	// from 为旧状态，to 为新状态。
	OnChanged func(from, to *State)

	// EnterHook 进入状态时执行（引擎线程）。典型用途：播放对应动画。
	EnterHook func(s *State)
	// ExitHook 离开状态时执行（引擎线程）。典型用途：清理定时器/资源。
	ExitHook func(s *State)
	// TimeoutHook 状态超时触发时执行（可选）；为 nil 时默认回到 Idle 或 returnTo。
	TimeoutHook func(s *State)

	// ScheduleTimeout 由 Engine 注入：安排一次超时回调（典型实现用 Scheduler）。
	// 当进入带 Timeout>0 的状态时，FSM 会调用它安排自动超时。
	// 若注入为 nil，则超时能力自动禁用（不调度）。
	ScheduleTimeout func(d time.Duration, cb func())
}

// NewStateMachine 创建状态机，默认 Idle。
func NewStateMachine() *StateMachine {
	return &StateMachine{
		current: StateIdle,
		allowed: make(map[*State]map[*State]bool),
	}
}

// Allow 注册一条允许的直接转移 from -> to。
func (sm *StateMachine) Allow(from, to *State) {
	if from == nil || to == nil {
		return
	}
	if sm.allowed[from] == nil {
		sm.allowed[from] = make(map[*State]bool)
	}
	sm.allowed[from][to] = true
}

// canTransitionToLocked 判断 target 是否可达。调用方需已持有 sm.mu。
func (sm *StateMachine) canTransitionToLocked(target *State) bool {
	if target == sm.current {
		return false
	}
	// 显式转移表优先。
	if m, ok := sm.allowed[sm.current]; ok {
		if allowed, ok := m[target]; ok {
			return allowed // 转移表中显式登记的 true/false 优先
		}
	}
	// 退化规则：严格更高优先级才允许（向后兼容旧行为）。
	return target.Priority > sm.current.Priority
}

// Transition 尝试转换状态。
// 规则：查转移表；若未登记则要求严格更高优先级（保持旧行为）。
// 成功时：在锁外按顺序执行旧状态 ExitHook -> 新状态 EnterHook -> 安排超时 -> 触发 OnChanged。
func (sm *StateMachine) Transition(target *State) bool {
	if target == nil {
		return false
	}
	sm.mu.Lock()
	if target == sm.current {
		sm.mu.Unlock()
		return false
	}
	if !sm.canTransitionToLocked(target) {
		sm.mu.Unlock()
		return false
	}
	from := sm.current
	sm.current = target
	sm.mu.Unlock()

	sm.runTransitionEffects(from, target, "interrupt=false")
	return true
}

// Interrupt 高优先级状态打断当前状态。
// 与 Transition 不同：打断无视优先级与转移表（强制进入），并记录当前状态为
// returnTo，使被中断状态超时后自动恢复。典型场景：拖拽/失败打断正常行为。
func (sm *StateMachine) Interrupt(target *State) bool {
	if target == nil {
		return false
	}
	sm.mu.Lock()
	if target == sm.current {
		sm.mu.Unlock()
		return false
	}
	// 记录 returnTo（仅当并非已经处于打断态，避免覆盖更早的来源）。
	if sm.returnTo == nil {
		sm.returnTo = sm.current
	}
	from := sm.current
	sm.current = target
	sm.mu.Unlock()

	sm.runTransitionEffects(from, target, "interrupt=true")
	return true
}

// runTransitionEffects 在锁外执行转移副作用（按原顺序）。
// 这是 StateMachine 的核心安全契约：绝不在持锁期间执行外部代码。
func (sm *StateMachine) runTransitionEffects(from, to *State, label string) {
	// 离开旧状态。
	if sm.ExitHook != nil {
		sm.ExitHook(from)
	}
	// 进入新状态。
	if sm.EnterHook != nil {
		sm.EnterHook(to)
	}
	// 安排超时（若有）。
	sm.scheduleTimeout(to)

	log.Printf("[Pet] FSM: %s -> %s (%s)", from.Name, to.Name, label)

	// 触发变更通知。
	if sm.OnChanged != nil {
		sm.OnChanged(from, to)
	}
}

// scheduleTimeout 为带 Timeout>0 的状态安排一次自动超时回调。
func (sm *StateMachine) scheduleTimeout(s *State) {
	if s == nil || s.Timeout <= 0 || sm.ScheduleTimeout == nil {
		return
	}
	d := s.Timeout
	sm.ScheduleTimeout(d, func() {
		// 超时回调在调度线程执行；回到 FSM 安全上下文确认状态未变。
		sm.mu.Lock()
		still := sm.current == s
		var ret *State
		if still {
			ret = sm.returnTo
			sm.returnTo = nil
		}
		sm.mu.Unlock()
		if !still {
			return // 状态已切换，超时作废
		}
		if sm.TimeoutHook != nil {
			sm.TimeoutHook(s)
			return
		}
		// 默认行为：若由 Interrupt 进入，则回到 returnTo；否则回到 Idle。
		if ret != nil {
			sm.ForceTransition(ret)
		} else {
			sm.ForceTransition(StateIdle)
		}
	})
}

// ForceTransition 强制转换状态（无视优先级与转移表）。
// 用于"回到 Idle""打断恢复"等确定性回退场景。
func (sm *StateMachine) ForceTransition(target *State) bool {
	if target == nil {
		return false
	}
	sm.mu.Lock()
	if target == sm.current {
		sm.mu.Unlock()
		return false
	}
	from := sm.current
	sm.current = target
	sm.mu.Unlock()

	if sm.ExitHook != nil {
		sm.ExitHook(from)
	}
	if sm.EnterHook != nil {
		sm.EnterHook(target)
	}
	sm.scheduleTimeout(target)
	log.Printf("[Pet] FSM: %s -> %s (force)", from.Name, target.Name)
	if sm.OnChanged != nil {
		sm.OnChanged(from, target)
	}
	return true
}

// Current 返回当前状态。
func (sm *StateMachine) Current() *State {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.current
}

// Is 检查当前是否处于指定状态。
func (sm *StateMachine) Is(s *State) bool {
	if s == nil {
		return false
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.current == s
}

// Start 实现 Lifecycle。状态机无需特殊启动逻辑。
func (sm *StateMachine) Start() {}

// Stop 实现 Lifecycle，重置为初始状态并清理回调。
func (sm *StateMachine) Stop() {
	sm.mu.Lock()
	sm.current = StateIdle
	sm.returnTo = nil
	sm.mu.Unlock()
}

// Dispose 实现 Lifecycle，清理所有外部回调引用。
func (sm *StateMachine) Dispose() {
	sm.Stop()
	sm.mu.Lock()
	sm.OnChanged = nil
	sm.EnterHook = nil
	sm.ExitHook = nil
	sm.TimeoutHook = nil
	sm.ScheduleTimeout = nil
	sm.mu.Unlock()
}

// Ensure StateMachine implements Lifecycle.
var _ Lifecycle = (*StateMachine)(nil)
