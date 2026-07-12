package pet

import (
	"sync"
	"time"
)

// EventLog 是一条事件日志（含时间戳与负载摘要）。
type EventLog struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Summary string    `json:"summary,omitempty"`
}

// Debugger 提供桌宠运行期可观测能力（v2 Phase 11）：
//   - 事件 ring buffer：缓存最近 N 条事件，供调试面板/日志查询。
//   - 指标计数：状态切换次数、渲染帧数、意图分布等。
//   - 状态快照：导出当前 FSM/动画/行为摘要，便于诊断"卡死/不响应"。
//
// Debugger 本身不持有 Engine 强引用，只通过事件订阅被动收集，
// 因此挂接/卸载都不会引入循环依赖或泄漏。
type Debugger struct {
	mu sync.Mutex

	// ring 事件环形缓冲。
	ring     []EventLog
	ringCap  int
	ringIdx  int
	ringFull bool

	// counters 各类指标计数。
	counters map[string]int64

	// intentCounts 意图分布（Behavior AI 决策统计）。
	intentCounts map[string]int64

	// lastState 最近一次状态名（用于快照）。
	lastState string

	// unsubscribes 保存事件订阅取消函数，用于 Stop/Dispose。
	unsubscribes []func()

	// bus 保存事件总线引用，Start 时自动 Attach。
	bus *EventBus
}

// NewDebugger 创建调试器，ring 容量默认 200。
func NewDebugger() *Debugger {
	return &Debugger{
		ring:         make([]EventLog, 200),
		ringCap:      200,
		counters:     make(map[string]int64),
		intentCounts: make(map[string]int64),
	}
}

// NewDebuggerWithBus 创建调试器并绑定事件总线。
func NewDebuggerWithBus(bus *EventBus) *Debugger {
	d := NewDebugger()
	d.bus = bus
	return d
}

// Attach 订阅事件总线，开始收集事件与指标。
func (d *Debugger) Attach(bus *EventBus) {
	if bus == nil {
		return
	}
	d.bus = bus
	d.Start()
}

// Start 实现 Lifecycle，订阅已绑定的事件总线。
func (d *Debugger) Start() {
	if d.bus == nil {
		return
	}
	d.Stop()
	// 订阅关心的事件类型（覆盖 EventBus 全部核心事件）。
	for _, t := range []EventType{
		EventStateChanged, EventAnimationFinished, EventAnimationStarted,
		EventWindowDragged, EventPetLoaded, EventPetUnloaded,
		EventAgentStarted, EventAgentFinished, EventAgentFailed,
		EventReviewStarted, EventReviewFinished, EventBehaviorFinished,
	} {
		cancel := d.bus.Subscribe(t, func(evt Event) {
			d.record(evt)
		})
		d.unsubscribes = append(d.unsubscribes, cancel)
	}
}

// Stop 实现 Lifecycle，取消所有事件订阅。
func (d *Debugger) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, cancel := range d.unsubscribes {
		if cancel != nil {
			cancel()
		}
	}
	d.unsubscribes = nil
}

// Dispose 实现 Lifecycle，清空所有数据。
func (d *Debugger) Dispose() {
	d.Stop()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ring = make([]EventLog, d.ringCap)
	d.ringIdx = 0
	d.ringFull = false
	d.counters = make(map[string]int64)
	d.intentCounts = make(map[string]int64)
	d.lastState = ""
}

// Ensure Debugger implements Lifecycle.
var _ Lifecycle = (*Debugger)(nil)

// record 记录一条事件（内部）。
func (d *Debugger) record(evt Event) {
	d.mu.Lock()
	defer d.mu.Unlock()

	summary := ""
	if m, ok := evt.Data.(map[string]interface{}); ok {
		if from, ok := m["from"]; ok {
			if to, ok := m["to"]; ok {
				d.lastState = to.(string)
				summary = from.(string) + "->" + to.(string)
				d.counters["state_changes"]++
			}
		}
		if anim, ok := m["anim"]; ok {
			summary = "anim=" + toString(anim)
		}
	}
	d.ring[d.ringIdx] = EventLog{Time: time.Now(), Type: string(evt.Type), Summary: summary}
	d.ringIdx++
	if d.ringIdx >= d.ringCap {
		d.ringIdx = 0
		d.ringFull = true
	}
	d.counters["events"]++
}

// RecordIntent 由 Behavior 在决策后调用，统计意图分布。
func (d *Debugger) RecordIntent(it Intent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.intentCounts[it.String()]++
}

// IncRender 递增渲染帧计数（由引擎渲染循环调用）。
func (d *Debugger) IncRender() {
	d.mu.Lock()
	d.counters["frames_rendered"]++
	d.mu.Unlock()
}

// RecentEvents 返回最近的事件日志（按时间从旧到新）。
func (d *Debugger) RecentEvents() []EventLog {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]EventLog, 0, d.ringCap)
	if d.ringFull {
		out = append(out, d.ring[d.ringIdx:]...)
	}
	out = append(out, d.ring[:d.ringIdx]...)
	return out
}

// Snapshot 返回当前状态快照（JSON 友好 map）。
func (d *Debugger) Snapshot() map[string]interface{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	counters := make(map[string]int64, len(d.counters))
	for k, v := range d.counters {
		counters[k] = v
	}
	intents := make(map[string]int64, len(d.intentCounts))
	for k, v := range d.intentCounts {
		intents[k] = v
	}
	return map[string]interface{}{
		"last_state":    d.lastState,
		"counters":      counters,
		"intent_counts": intents,
		"event_count":   len(d.ring),
	}
}

// toString 安全地把接口转为字符串。
func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
