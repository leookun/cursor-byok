package pet

import (
	"sync"
	"testing"
	"time"
)

// TestStateMachine_TransitionTable 验证显式转移表优先于优先级规则。
func TestStateMachine_TransitionTable(t *testing.T) {
	sm := NewStateMachine()
	sm.Allow(StateIdle, StateWalking)
	sm.Allow(StateWalking, StateIdle)

	if !sm.Transition(StateWalking) {
		t.Fatal("idle->walking should be allowed by table")
	}
	if sm.Current() != StateWalking {
		t.Fatalf("expected walking, got %s", sm.Current().Name)
	}
	// sitting (prio 2) 未登记，且不高于 walking (prio 3)，应被拒绝。
	if sm.Transition(StateSitting) {
		t.Fatal("walking->sitting not in table and not higher prio, should be rejected")
	}
	if !sm.Transition(StateIdle) {
		t.Fatal("walking->idle should be allowed by table")
	}
}

// TestStateMachine_InterruptReturnTo 验证 Interrupt 记录来源并在超时时恢复。
func TestStateMachine_InterruptReturnTo(t *testing.T) {
	sm := NewStateMachine()
	var mu sync.Mutex
	transitions := []string{}
	sm.OnChanged = func(from, to *State) {
		mu.Lock()
		transitions = append(transitions, to.Name)
		mu.Unlock()
	}

	// 先进入一个低优先级状态（通过强制转移建立起点）。
	if !sm.ForceTransition(StateSitting) {
		t.Fatal("force to sitting failed")
	}
	// 打断进入 dragging（高优先级），应记录 returnTo=sitting。
	if !sm.Interrupt(StateDragging) {
		t.Fatal("interrupt to dragging failed")
	}
	if sm.Current() != StateDragging {
		t.Fatalf("expected dragging, got %s", sm.Current().Name)
	}

	// 模拟超时回调：默认行为回 returnTo。
	sm.mu.Lock()
	ret := sm.returnTo
	sm.returnTo = nil
	sm.mu.Unlock()
	if ret != StateSitting {
		t.Fatalf("expected returnTo=sitting, got %v", ret)
	}
	if !sm.ForceTransition(ret) {
		t.Fatal("return to sitting failed")
	}
}

// TestStateMachine_TimeoutScheduled 验证带 Timeout 的状态触发 ScheduleTimeout。
func TestStateMachine_TimeoutScheduled(t *testing.T) {
	sm := NewStateMachine()
	var scheduled []time.Duration
	sm.ScheduleTimeout = func(d time.Duration, cb func()) {
		scheduled = append(scheduled, d)
	}
	// 给一个临时状态设超时并进入（用 ForceTransition 跳过表限制）。
	tmp := &State{Name: "tmp", Priority: 50, Timeout: 1234 * time.Millisecond}
	if !sm.ForceTransition(tmp) {
		t.Fatal("force to tmp failed")
	}
	if len(scheduled) != 1 || scheduled[0] != 1234*time.Millisecond {
		t.Fatalf("expected one timeout scheduled with 1234ms, got %v", scheduled)
	}
}

// TestStateMachine_TimeoutStaleIgnored 验证状态已切换后超时回调作废。
func TestStateMachine_TimeoutStaleIgnored(t *testing.T) {
	sm := NewStateMachine()
	var timeoutCb func()
	sm.ScheduleTimeout = func(d time.Duration, cb func()) {
		// 记录回调，模拟异步调度器：回调将在状态切走后才触发。
		timeoutCb = cb
	}
	var onChangedCount int
	sm.OnChanged = func(from, to *State) {
		onChangedCount++
	}

	tmp := &State{Name: "tmp", Priority: 50, Timeout: 10 * time.Millisecond}
	if !sm.ForceTransition(tmp) {
		t.Fatal("force to tmp failed")
	}
	if sm.Current() != tmp {
		t.Fatalf("expected tmp, got %s", sm.Current().Name)
	}
	if timeoutCb == nil {
		t.Fatal("timeout callback was not scheduled")
	}

	// 在回调触发前把状态切走，使后续 timeout 成为 stale。
	if !sm.ForceTransition(StateIdle) {
		t.Fatal("force back to idle failed")
	}
	if sm.Current() != StateIdle {
		t.Fatalf("expected idle, got %s", sm.Current().Name)
	}

	// 现在触发之前为 tmp 安排的 timeout 回调。
	timeoutCb()

	// stale 回调不应再触发转移，状态应保持 idle。
	if sm.Current() != StateIdle {
		t.Fatalf("stale timeout should be ignored, expected idle, got %s", sm.Current().Name)
	}
	// OnChanged 只应被触发两次：idle->tmp 和 tmp->idle。
	if onChangedCount != 2 {
		t.Fatalf("expected OnChanged fired 2 times, got %d", onChangedCount)
	}
}
