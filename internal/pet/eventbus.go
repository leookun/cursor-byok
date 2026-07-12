package pet

import (
	"log"
	"sync"
)

// EventType 标识事件种类。
type EventType string

// 核心事件类型定义。
const (
	// AnimationFinished 某个动画播放完毕（loop=false 时触发）。
	EventAnimationFinished EventType = "animation.finished"
	// AnimationStarted 某个动画开始播放。
	EventAnimationStarted EventType = "animation.started"
	// StateChanged 状态机发生状态切换。
	EventStateChanged EventType = "state.changed"
	// WindowDragged 用户拖拽窗口（dx,dy 为相对位移）。
	EventWindowDragged EventType = "window.dragged"
	// WindowMoved 窗口被移动到新坐标（x,y）。
	EventWindowMoved EventType = "window.moved"
	// PetLoaded 桌宠资源加载完成并创建引擎。
	EventPetLoaded EventType = "pet.loaded"
	// PetUnloaded 桌宠资源卸载/引擎停止。
	EventPetUnloaded EventType = "pet.unloaded"
	// BehaviorFinished 一次自主行为（walk/jump/wave 等）完成。
	EventBehaviorFinished EventType = "behavior.finished"
	// AgentStarted Agent 开始工作。
	EventAgentStarted EventType = "agent.started"
	// AgentFinished Agent 完成工作。
	EventAgentFinished EventType = "agent.finished"
	// AgentFailed Agent 工作失败。
	EventAgentFailed EventType = "agent.failed"
	// ReviewStarted 进入 Review 状态。
	EventReviewStarted EventType = "review.started"
	// ReviewFinished Review 结束。
	EventReviewFinished EventType = "review.finished"
	// MotionArrived Motion 平滑移动到达目标。
	EventMotionArrived EventType = "motion.arrived"
	// RequestIntent 外部/插件请求应用一个行为意图。
	EventRequestIntent EventType = "intent.request"
)


// Event 是事件总线传递的事件。
// Data 为可选负载，由具体事件自行约定类型。
type Event struct {
	Type EventType
	Data interface{}
}

// EventHandler 处理事件的函数。
// 注意：所有 handler 都在发布方线程同步执行；发布到引擎内部状态的事件
// 应由发布方保证已在引擎线程（如经 Scheduler 派发），handler 内可安全访问状态。
type EventHandler func(evt Event)

// EventPublisher 是事件发布能力的最小接口。
// 组件（如 MotionController/AnimationPlayer）通过该接口发布事件，无需持有 *Engine。
type EventPublisher interface {
	Publish(Event)
}

// 确保 *EventBus 实现 EventPublisher。
var _ EventPublisher = (*EventBus)(nil)


// subscription 是单次订阅的标识，用于精确取消。
type subscription struct {
	id int
	h  EventHandler
}

// EventBus 是进程内事件总线，用于模块间解耦通信。
//
// 设计原则（v2 Phase 2）：
//   - 模块间不再直接调用彼此的方法（如 Behavior 直接调 FSM/Animation），
//     而是发布/订阅事件，降低耦合、便于后续插件与调试系统接入。
//   - Publish 是同步的：handler 在当前（引擎）线程立即执行，
//     避免异步带来的状态时序不确定性。
//   - 若 handler panic，不会杀死发布者，仅记录日志（与全局 panic 恢复互补）。
type EventBus struct {
	mu           sync.RWMutex
	handlers     map[EventType][]subscription
	nextID       int
}

// NewEventBus 创建事件总线。
func NewEventBus() *EventBus {
	return &EventBus{
		handlers: make(map[EventType][]subscription),
	}
}

// Start 实现 Lifecycle。事件总线无需特殊启动逻辑。
func (b *EventBus) Start() {}

// Stop 实现 Lifecycle。事件总线无需特殊停止逻辑。
func (b *EventBus) Stop() {}

// Dispose 实现 Lifecycle，清空所有订阅者。
func (b *EventBus) Dispose() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers = make(map[EventType][]subscription)
	b.nextID = 0
}

// Ensure EventBus implements Lifecycle.
var _ Lifecycle = (*EventBus)(nil)

// Subscribe 订阅某类事件。返回取消订阅的函数。
func (b *EventBus) Subscribe(t EventType, h EventHandler) (cancel func()) {
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.handlers[t] = append(b.handlers[t], subscription{id: id, h: h})
	b.mu.Unlock()
	return func() {
		b.UnsubscribeByID(t, id)
	}
}

// UnsubscribeByID 按订阅 ID 取消订阅。
func (b *EventBus) UnsubscribeByID(t EventType, id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	hs := b.handlers[t]
	for i, sub := range hs {
		if sub.id == id {
			b.handlers[t] = append(hs[:i], hs[i+1:]...)
			return
		}
	}
}

// Publish 同步发布事件，立即在当前线程执行所有订阅者的 handler。
// handler 中的 panic 被捕获并记日志，不影响其他订阅者与发布者。
func (b *EventBus) Publish(evt Event) {
	b.mu.RLock()
	hs := make([]EventHandler, 0, len(b.handlers[evt.Type]))
	for _, sub := range b.handlers[evt.Type] {
		hs = append(hs, sub.h)
	}
	b.mu.RUnlock()

	for _, h := range hs {
		if h == nil {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[Pet][EventBus] handler panic for %s recovered: %v", evt.Type, r)
				}
			}()
			h(evt)
		}()
	}
}

// HasSubscriber 检查某事件是否已有订阅者（调试/诊断用）。
func (b *EventBus) HasSubscriber(t EventType) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.handlers[t]) > 0
}
