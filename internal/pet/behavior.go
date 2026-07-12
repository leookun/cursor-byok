package pet

import (
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// BehaviorSystem 管理桌宠的自主行为。
//
// Phase 7.5：Behavior 不再持有 *Engine，只依赖注入的 FSM/Window/Motion/Debug/Bus/Scheduler。
// 所有跨线程指令通过 Scheduler 派发到引擎线程执行；Motion/Animation 通过 EventBus 事件通信。
type BehaviorSystem struct {
	fsm    *StateMachine
	win    *NativeWindow
	motion *MotionController
	debug  *Debugger
	bus    *EventBus
	sched  *Scheduler
	owner  OwnerHandle

	mu     sync.Mutex
	active bool
	moving bool
	// gen 是 generation counter：每次 Start 递增，用于防御性过滤过期回调。
	gen atomic.Int64

	// decider 是 Behavior AI 意图决策器（v2 Phase 6）。
	decider *IntentDecider
	// intentResolver 把意图解析为状态+副作用（Phase 9）。
	intentResolver *IntentResolver
	// idleSince 进入 idle 的时刻，用于决策"空闲多久"。
	idleSince time.Time
	// lastInteract 上次用户交互（拖拽/点击）时刻，用于交互冷却。
	lastInteract time.Time

	// subscriptions 在 Stop 时取消。
	motionSub  func()
	intentSub  func()
}

// 行为定时常量
const (
	walkDelayMin    = 4              // 秒
	walkDelayExtra  = 6              // 秒（rand 范围）
	walkDuration    = 2 * time.Second
	jumpDuration    = 1500 * time.Millisecond
	waveDuration    = 2000 * time.Millisecond
	failRecovery    = 2 * time.Second
	sitDelay        = 15 * time.Second
	sleepDelay      = 60 * time.Second
	jumpProbability = 0.05
	waveProbability = 0.20
	screenDefaultW  = 1920
	screenDefaultH  = 1080
)

// NewBehaviorSystem 创建行为系统。
// Phase 7.5：所有依赖由 Engine 注入，不再持有 *Engine。
// Phase 9：增加 IntentResolver 参数，使意图→状态映射可替换。
func NewBehaviorSystem(fsm *StateMachine, win *NativeWindow, motion *MotionController, debug *Debugger, bus *EventBus, sched *Scheduler, owner OwnerHandle, resolver *IntentResolver) *BehaviorSystem {
	if resolver == nil {
		resolver = NewIntentResolver()
	}
	return &BehaviorSystem{
		fsm:            fsm,
		win:            win,
		motion:         motion,
		debug:          debug,
		bus:            bus,
		sched:          sched,
		owner:          owner,
		decider:        NewIntentDecider(),
		intentResolver: resolver,
	}
}

// Start 启动行为系统。
func (b *BehaviorSystem) Start() {
	b.gen.Add(1) // 递增 generation
	b.mu.Lock()
	b.active = true
	b.mu.Unlock()

	// 注入 FSM 超时调度：FSM 进入带 Timeout 的状态时，经统一 Scheduler 安排超时回调。
	if b.fsm != nil && b.sched != nil {
		b.fsm.ScheduleTimeout = func(d time.Duration, cb func()) {
			b.sched.Schedule(TaskSpec{Owner: b.owner, Delay: d, Fn: cb})
		}
	}

	// 订阅 Motion 到达事件与外部意图请求。
	if b.bus != nil {
		b.motionSub = b.bus.Subscribe(EventMotionArrived, func(_ Event) {
			b.onMotionArrive()
		})
		b.intentSub = b.bus.Subscribe(EventRequestIntent, func(evt Event) {
			it, ok := evt.Data.(Intent)
			if !ok {
				return
			}
			b.dispatch(func() {
				b.applyIntent(it)
			})
		})
	}

	b.resetIdleTimers()
	b.scheduleWalk()
	log.Println("[Pet] Behavior: started")
}

// Stop 停止行为系统。
// Phase 7.5：仅取消自己的 owner 任务与事件订阅，不停止 Scheduler（由 Engine 统一管理）。
func (b *BehaviorSystem) Stop() {
	b.mu.Lock()
	b.active = false
	b.mu.Unlock()

	if b.motionSub != nil {
		b.motionSub()
		b.motionSub = nil
	}
	if b.intentSub != nil {
		b.intentSub()
		b.intentSub = nil
	}
	if b.fsm != nil {
		b.fsm.ScheduleTimeout = nil
	}
	if b.sched != nil {
		b.sched.CancelByOwner(b.owner)
	}
	log.Println("[Pet] Behavior: stopped")
}

// Dispose 释放行为系统资源（实现 Lifecycle）。
func (b *BehaviorSystem) Dispose() {
	b.Stop()
}

// Ensure BehaviorSystem implements Lifecycle.
var _ Lifecycle = (*BehaviorSystem)(nil)

// Update 每帧调用。行为由 Scheduler 驱动，update 中无需处理。
func (b *BehaviorSystem) Update() {}

// isActiveGen 检查当前 generation 是否匹配且 active（防御性过滤）。
func (b *BehaviorSystem) isActiveGen(gen int64) bool {
	if b.gen.Load() != gen {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active
}

// dispatch 把指令派发到引擎线程执行（通过 Scheduler 立即任务）。
// 这是 Phase 7.5 的核心：模块不再直接调用 Engine.Post。
func (b *BehaviorSystem) dispatch(cmd func()) {
	if cmd == nil || b.sched == nil {
		return
	}
	b.sched.Schedule(TaskSpec{Owner: b.owner, Delay: 0, Fn: cmd})
}

// publish 发布事件到总线。
func (b *BehaviorSystem) publish(t EventType) {
	if b.bus != nil {
		b.bus.Publish(Event{Type: t})
	}
}

// OnAgentStarted Agent 开始工作。
func (b *BehaviorSystem) OnAgentStarted() {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()

	b.publish(EventAgentStarted)
	b.dispatch(func() {
		if b.fsm != nil {
			b.fsm.Transition(StateWaiting)
		}
	})
}

// OnAgentFinished Agent 完成工作。
func (b *BehaviorSystem) OnAgentFinished() {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()

	b.publish(EventAgentFinished)
	b.dispatch(func() {
		b.markIdle()
	})
}

// OnAgentFailed Agent 失败。
func (b *BehaviorSystem) OnAgentFailed() {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()

	b.publish(EventAgentFailed)
	gen := b.gen.Load()
	b.sched.Schedule(TaskSpec{Owner: b.owner, Delay: failRecovery, Fn: func() {
		if !b.isActiveGen(gen) {
			return
		}
		b.dispatch(func() {
			b.markIdle()
		})
	}})
}

// OnReviewStarted 进入 Review。
func (b *BehaviorSystem) OnReviewStarted() {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()

	b.publish(EventReviewStarted)
	b.dispatch(func() {
		if b.fsm != nil {
			b.fsm.Transition(StateReviewing)
		}
	})
}

// OnReviewFinished Review 结束。
func (b *BehaviorSystem) OnReviewFinished() {
	b.publish(EventReviewFinished)
	b.dispatch(func() {
		if b.fsm != nil && b.fsm.Is(StateReviewing) {
			b.markIdle()
		}
	})
}

func (b *BehaviorSystem) scheduleWalk() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.active {
		return
	}
	delay := walkDelayMin + rand.Intn(walkDelayExtra)

	gen := b.gen.Load()
	b.sched.Schedule(TaskSpec{Owner: b.owner, Delay: time.Duration(delay) * time.Second, Fn: func() {
		if !b.isActiveGen(gen) {
			return
		}
		b.doWalkOrRandom()
	}})
}

// doWalkOrRandom 必须在引擎线程内执行（它直接访问 fsm/window，并切换状态）。
// v2 Phase 6：改为由 IntentDecider 根据上下文产出意图，再映射到 FSM 状态，
// 取代原先硬编码的 rand 概率分支，使行为更自然、可配置、可测试。
func (b *BehaviorSystem) doWalkOrRandom() {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return
	}
	if b.fsm == nil || b.win == nil {
		b.mu.Unlock()
		return
	}
	cur := b.fsm.Current()
	if cur == StateDragging || cur == StateSleeping || cur == StateFailed {
		b.mu.Unlock()
		b.scheduleWalk()
		return
	}
	b.mu.Unlock()

	now := time.Now()
	// 计算上下文：空闲时长、交互冷却、Agent 状态。
	var idleSec, interactSec float64
	if cur == StateIdle {
		idleSec = now.Sub(b.idleSince).Seconds()
	} else {
		b.idleSince = now
	}
	interactSec = -1
	if !b.lastInteract.IsZero() {
		interactSec = now.Sub(b.lastInteract).Seconds()
	}

	ctx := BehaviorContext{
		IdleSeconds:        idleSec,
		AgentBusy:          cur == StateWaiting,
		Reviewing:          cur == StateReviewing,
		LastInteractionSec: interactSec,
	}
	intent := b.decider.Decide(ctx)
	b.applyIntent(intent)
}

// applyIntent 把决策出的意图映射到 FSM 状态与对应动作。
// Phase 9：意图→状态映射由 IntentResolver 解析，BehaviorSystem 只负责执行结果。
// 必须在引擎线程内调用。
func (b *BehaviorSystem) applyIntent(intent Intent) {
	// 记录意图分布（v2 Phase 11 可观测）。
	if b.debug != nil {
		b.debug.RecordIntent(intent)
	}
	gen := b.gen.Load()

	// Phase 9：通过 IntentResolver 解析意图为状态+副作用。
	cur := b.fsm.Current()
	resolution := b.intentResolver.Resolve(intent, cur)

	switch resolution.Mode {
	case TransitionNone:
		// 不转移：保持当前状态，仅重新排程。
		b.scheduleWalk()

	case TransitionNormal:
		// 常规转移（走转移表+优先级检查）。
		if b.fsm != nil && resolution.Target != nil {
			b.fsm.Transition(resolution.Target)
		}
		b.executeSideEffects(resolution.SideEffects, gen)

	case TransitionForce:
		// 强制转移（无视优先级与转移表）。
		if b.fsm != nil && resolution.Target != nil {
			b.fsm.ForceTransition(resolution.Target)
		}
		b.executeSideEffects(resolution.SideEffects, gen)

	case TransitionInterrupt:
		// 打断转移（记录 returnTo，超时后恢复）。
		if b.fsm != nil && resolution.Target != nil {
			b.fsm.Interrupt(resolution.Target)
		}
		b.executeSideEffects(resolution.SideEffects, gen)
	}
}

// executeSideEffects 执行 IntentResolution 中的副作用。
func (b *BehaviorSystem) executeSideEffects(se *IntentSideEffects, gen int64) {
	if se == nil {
		return
	}

	// Motion 移动目标。
	if se.MotionTarget != nil && b.motion != nil {
		tx, ty := se.MotionTarget.X, se.MotionTarget.Y
		// 根据窗口实际尺寸 clamp。
		if b.win != nil {
			ww, wh := b.win.Size()
			if tx < 0 {
				tx = 50
			}
			if tx > screenDefaultW-ww {
				tx = screenDefaultW - ww - 50
			}
			if ty < 0 {
				ty = 50
			}
			if ty > screenDefaultH-wh {
				ty = screenDefaultH - wh - 50
			}
		}
		log.Printf("[Pet] Behavior: moving to (%d,%d)", tx, ty)
		b.motion.MoveTo(tx, ty)
	}

	// 超时兜底回 Idle。
	if se.TimeoutBack > 0 {
		b.sched.Schedule(TaskSpec{Owner: b.owner, Delay: se.TimeoutBack, Fn: func() {
			if !b.isActiveGen(gen) {
				return
			}
			b.dispatch(b.markIdle)
		}})
	}

	// OnApplied 回调。
	if se.OnApplied != nil {
		se.OnApplied()
	}
}

// markIdle 标记回到 idle 态：递增 generation 取消所有旧定时器（避免叠加），
// 重置空闲计时，再重启 sit/sleep 兜底与 walk 排程。
func (b *BehaviorSystem) markIdle() {
	b.gen.Add(1) // 作废所有此前注册的定时器（walk/sit/sleep/jump...）
	if b.fsm != nil {
		b.fsm.ForceTransition(StateIdle)
	}
	b.idleSince = time.Now()
	b.resetIdleTimers()
	b.scheduleWalk()
}

// NotifyInteraction 由外部（拖拽/点击）调用，记录交互时刻以触发冷却。
func (b *BehaviorSystem) NotifyInteraction() {
	b.mu.Lock()
	b.lastInteract = time.Now()
	b.mu.Unlock()
}

// onMotionArrive 由 Motion 控制器在窗口到达目标时通过 EventBus 触发。
// 仅当当前处于 Walking 态才回 idle，避免干扰其它移动（如拖拽复位）。
func (b *BehaviorSystem) onMotionArrive() {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	if b.fsm != nil && b.fsm.Is(StateWalking) {
		b.markIdle()
	}
}

// RequestIntent 由插件/外部请求应用一个意图（v2 Phase 10）。
// 经 Scheduler 派发到引擎线程执行，线程安全；会打断当前自主动作并应用新意图。
func (b *BehaviorSystem) RequestIntent(it Intent) {
	b.dispatch(func() {
		b.applyIntent(it)
	})
}

func (b *BehaviorSystem) resetIdleTimers() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.active {
		return
	}

	gen := b.gen.Load()
	b.sched.Schedule(TaskSpec{Owner: b.owner, Delay: sitDelay, Fn: func() {
		if !b.isActiveGen(gen) {
			return
		}
		b.dispatch(func() {
			if b.fsm != nil && b.fsm.Is(StateIdle) {
				b.fsm.Transition(StateSitting)
			}
		})
	}})
	b.sched.Schedule(TaskSpec{Owner: b.owner, Delay: sleepDelay, Fn: func() {
		if !b.isActiveGen(gen) {
			return
		}
		b.dispatch(func() {
			if b.fsm != nil && b.fsm.Is(StateSitting) {
				b.fsm.ForceTransition(StateSleeping)
			}
		})
	}})
}
