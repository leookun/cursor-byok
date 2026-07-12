package pet

import (
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakePet 实现 PetInstance，用于测试 PetManager（不依赖真实 Win32 窗口）。
type fakePet struct {
	id      string
	started bool
	stopped bool
	mu      sync.Mutex
}

func (f *fakePet) Start() {
	f.mu.Lock()
	f.started = true
	f.mu.Unlock()
}
func (f *fakePet) Stop() {
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
}
func (f *fakePet) IsReady() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started && !f.stopped
}

// TestIntegration_DecisionToStatePipeline 串接 IntentDecider -> FSM -> Resolver -> Debugger，
// 验证"决策 -> 状态切换 -> 动画解析 -> 事件记录"全链路。
func TestIntegration_DecisionToStatePipeline(t *testing.T) {
	bus := NewEventBus()
	fsm := NewStateMachine()
	resolver := NewAnimationResolver()
	dbg := NewDebugger()
	dbg.Attach(bus)

	// EnterHook：状态切换时解析动画名（模拟引擎行为），并由 debugger 经事件记录。
	fsm.EnterHook = func(s *State) {
		_ = resolver.Resolve(s) // 解析动画名（真实引擎在此 Play/CrossFade）
	}
	fsm.OnChanged = func(from, to *State) {
		bus.Publish(Event{Type: EventStateChanged, Data: map[string]interface{}{"from": from.Name, "to": to.Name}})
	}

	decider := NewIntentDecider()
	rng := rand.New(rand.NewSource(42))

	// 模拟一次决策-应用循环：idle -> 决策为 walk -> 切到 walking。
	ctx := BehaviorContext{IdleSeconds: 1, RNG: rng}
	intent := decider.Decide(ctx)
	var target *State
	switch intent {
	case IntentWalk:
		target = StateWalking
	case IntentJump:
		target = StateJumping
	case IntentWave:
		target = StateWaving
	default:
		target = StateIdle
	}
	if !fsm.Transition(target) && target != StateIdle {
		t.Fatalf("transition to %s failed", target.Name)
	}

	// 给 debugger 一点时间（事件同步执行，无需等待）。
	if dbg.Snapshot()["last_state"] == nil {
		t.Fatal("debugger should have recorded last state")
	}
}

// TestIntegration_PetManagerLifecycle 验证多宠物管理器的完整生命周期。
func TestIntegration_PetManagerLifecycle(t *testing.T) {
	m := NewPetManager()
	p1 := &fakePet{id: "nezuko"}
	p2 := &fakePet{id: "codex"}

	if !m.Register("nezuko", p1) {
		t.Fatal("register nezuko should succeed")
	}
	if !m.Register("codex", p2) {
		t.Fatal("register codex should succeed")
	}
	// 重复注册应被拒绝。
	if m.Register("nezuko", &fakePet{id: "nezuko-dup"}) {
		t.Fatal("duplicate register should be rejected")
	}
	if m.Count() != 2 {
		t.Fatalf("expected 2 pets, got %d", m.Count())
	}

	// 获取与启动。
	got, ok := m.Get("nezuko")
	if !ok || !got.IsReady() {
		// 尚未 Start
	}
	m.Start("nezuko")
	if !p1.IsReady() {
		t.Fatal("nezuko should be ready after Start")
	}

	// 停止单个。
	if !m.Stop("nezuko") {
		t.Fatal("stop nezuko should return true")
	}
	if p1.IsReady() {
		t.Fatal("nezuko should not be ready after Stop")
	}
	if m.Count() != 1 {
		t.Fatalf("expected 1 pet remaining, got %d", m.Count())
	}

	// 停止全部。
	m.StopAll()
	if m.Count() != 0 {
		t.Fatalf("expected 0 pets after StopAll, got %d", m.Count())
	}
	if p2.IsReady() {
		t.Fatal("codex should be stopped by StopAll")
	}
}

// TestIntegration_PluginAndDebugger 验证插件订阅与 debugger 协作于同一总线。
func TestIntegration_PluginAndDebugger(t *testing.T) {
	bus := NewEventBus()
	dbg := NewDebugger()
	dbg.Attach(bus)
	api := newPluginAPI(nil, bus, NewStateMachine(), nil)
	mgr := NewPluginManager(api)


	p := &fakePlugin{name: "watcher"}
	if err := mgr.Register(p); err != nil {
		t.Fatal(err)
	}
	mgr.StartAll()

	// 发布状态变化：插件与 debugger 都应收到。
	bus.Publish(Event{Type: EventStateChanged, Data: map[string]interface{}{"from": "idle", "to": "walking"}})

	p.mu.Lock()
	pev := len(p.events)
	p.mu.Unlock()
	if pev != 1 {
		t.Fatalf("plugin should receive 1 event, got %d", pev)
	}
	if dbg.Snapshot()["last_state"] != "walking" {
		t.Fatal("debugger should record last_state=walking")
	}
	mgr.StopAll()
}

// 确保 time 导入被使用（部分测试平台）。
var _ = time.Now

// =========================================================================
// Phase 6 自动化回归测试（Engine 生命周期 / 关闭竞态 / 幂等 / 压力）
// =========================================================================

// TestRegression_EngineStateMachine 验证状态机 Created→Running→Stopping→Stopped 全链路。
// 纯状态流转测试，不依赖真实窗口/渲染资源。
func TestRegression_EngineStateMachine(t *testing.T) {
	e := &Engine{
		stopCh: make(chan struct{}),
		cmdCh:  make(chan func(), 64),
	}
	e.state.Store(int32(Created))

	// Created 时 Post 拒绝命令。
	called := false
	e.Post(func() { called = true })
	if called {
		t.Fatal("Post should reject command when state=Created")
	}

	// 手动设 Running 状态。
	e.state.Store(int32(Running))

	// Running 时 Post 接受命令（但需要有人消费 cmdCh）。
	done := make(chan struct{})
	e.Post(func() { close(done) })
	// 手动消费 cmdCh 模拟引擎线程。
	select {
	case cmd := <-e.cmdCh:
		cmd()
	case <-time.After(time.Second):
		t.Fatal("Post should enqueue command when state=Running")
	}
	select {
	case <-done:
	default:
		t.Fatal("Post callback should have executed")
	}

	// 设 Stopping。
	e.state.Store(int32(Stopping))

	// Post 应拒绝。
	called = false
	e.Post(func() { called = true })
	if called {
		t.Fatal("Post should reject during Stopping")
	}

	// PostCritical 应接受。
	e.PostCritical(func() { called = true })
	// 消费 cmdCh。
	select {
	case cmd := <-e.cmdCh:
		cmd()
	case <-time.After(time.Second):
		t.Fatal("PostCritical should enqueue command")
	}
	if !called {
		t.Fatal("PostCritical command should have been executed")
	}

	// 设 Stopped。
	e.state.Store(int32(Stopped))

	if EngineState(e.state.Load()) != Stopped {
		t.Fatalf("expected Stopped, got %s", EngineState(e.state.Load()))
	}

	// Stopped 后 PostCritical 也应拒绝。
	if e.PostCritical(func() {}) {
		t.Fatal("PostCritical should reject when state=Stopped")
	}
}

// TestRegression_PetManagerStress 验证 PetManager 并发安全：100 次并发注册/停止。
func TestRegression_PetManagerStress(t *testing.T) {
	m := NewPetManager()
	const N = 100

	var wg sync.WaitGroup

	// 并发注册 N 个不同 petID。
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := fmt.Sprintf("pet-%d", idx)
			p := &fakePet{id: id}
			m.Register(id, p)
		}(i)
	}
	wg.Wait()

	if m.Count() != N {
		t.Fatalf("after concurrent register: expected %d, got %d", N, m.Count())
	}

	// 并发停止。
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := fmt.Sprintf("pet-%d", idx)
			m.Stop(id)
		}(i)
	}
	wg.Wait()

	if m.Count() != 0 {
		t.Fatalf("after concurrent stop: expected 0, got %d", m.Count())
	}
}

// TestRegression_StopIdempotent 验证 Engine.Stop 幂等：多次调用不 panic。
// 纯状态流转测试，不依赖窗口。
func TestRegression_StopIdempotent(t *testing.T) {
	e := &Engine{
		stopCh: make(chan struct{}),
		cmdCh:  make(chan func(), 64),
	}
	e.renderDone.Add(1)
	go e.run()
	// 手动设 Running（跳过大段初始化）。
	e.state.Store(int32(Running))

	// 第一次 Stop：应从 Running→Stopping→Stopped。
	e.Stop()

	state1 := EngineState(e.state.Load())
	if state1 != Stopped {
		t.Fatalf("1st Stop: expected Stopped, got %s", state1)
	}

	// 后续 Stop 幂等。
	for i := 0; i < 5; i++ {
		e.Stop() // 不应 panic
	}

	finalState := EngineState(e.state.Load())
	if finalState != Stopped {
		t.Fatalf("after 5x Stop: expected Stopped, got %s", finalState)
	}
}

// TestRegression_PostCriticalDuringShutdown 验证 PostCritical 在 Stopping 状态仍可入队。
func TestRegression_PostCriticalDuringShutdown(t *testing.T) {
	e := &Engine{
		stopCh: make(chan struct{}),
		cmdCh:  make(chan func(), 64),
	}
	e.renderDone.Add(1)
	go e.run()
	e.Start()

	// 模拟 Stopping 状态。
	e.state.Store(int32(Stopping))

	// Post 应拒绝。
	called := false
	e.Post(func() { called = true })
	if called {
		t.Fatal("Post should reject during Stopping")
	}

	// PostCritical 应接受。
	ok := e.PostCritical(func() { called = true })
	if !ok {
		t.Fatal("PostCritical should accept during Stopping")
	}
	// 等待引擎线程 drain 执行。
	time.Sleep(100 * time.Millisecond)
	if !called {
		t.Fatal("PostCritical command should have been executed in drain")
	}

	e.Stop()
}

// TestRegression_PetManagerStopNonexistent 验证停止不存在的 petID 返回 false 且不 panic。
func TestRegression_PetManagerStopNonexistent(t *testing.T) {
	m := NewPetManager()
	if m.Stop("nonexistent") {
		t.Fatal("Stop on nonexistent pet should return false")
	}
	if m.Count() != 0 {
		t.Fatal("count should still be 0 after stopping nonexistent")
	}
}

// TestRegression_PetManagerDuplicateRegister 验证重复注册被拒绝。
func TestRegression_PetManagerDuplicateRegister(t *testing.T) {
	m := NewPetManager()
	p := &fakePet{id: "test"}
	if !m.Register("test", p) {
		t.Fatal("first register should succeed")
	}
	if m.Register("test", &fakePet{id: "test-dup"}) {
		t.Fatal("duplicate register should be rejected")
	}
}

// TestRegression_PostNilSafety 验证 Post/PostCritical 对 nil 命令不 panic。
func TestRegression_PostNilSafety(t *testing.T) {
	e := &Engine{
		stopCh: make(chan struct{}),
		cmdCh:  make(chan func(), 64),
	}
	e.Post(nil) // 不 panic
	if e.PostCritical(nil) {
		t.Fatal("PostCritical(nil) should return false")
	}
}

// TestRegression_RapidStartStop 验证 20 次完整 Start→Post→Stop 流程不 panic。
// 每次启动真实 run() 消费 cmdCh，Post 命令后 Stop，覆盖完整生命周期。
func TestRegression_RapidStartStop(t *testing.T) {
	const N = 20

	for i := 0; i < N; i++ {
		e := &Engine{
			stopCh: make(chan struct{}),
			cmdCh:  make(chan func(), 64),
		}
		e.state.Store(int32(Created))

		// 启动 run（window 为 nil，Post 回调中 window.Show 会 panic 但被 recover 捕获，不影响流程）。
		e.transition(Created, Running)
		go e.run()

		// Post 几条命令。
		for j := 0; j < 5; j++ {
			e.Post(func() { /* no-op */ })
		}

		// Stop（会走到 window=nil 保护路径）。
		e.Stop()

		if EngineState(e.state.Load()) != Stopped {
			t.Fatalf("iteration %d: expected Stopped after Stop, got %s", i, EngineState(e.state.Load()))
		}
	}
}

// TestRegression_MultiInstanceStop 验证关闭一个实例不影响其他。
func TestRegression_MultiInstanceStop(t *testing.T) {
	m := NewPetManager()

	pA := &fakePet{id: "petA"}
	pB := &fakePet{id: "petB"}
	pC := &fakePet{id: "petC"}

	m.Register("A", pA)
	m.Register("B", pB)
	m.Register("C", pC)

	m.Start("A")
	m.Start("B")
	m.Start("C")

	if m.Count() != 3 {
		t.Fatalf("expected 3 pets, got %d", m.Count())
	}

	// 关闭 B。
	if !m.Stop("B") {
		t.Fatal("Stop B should succeed")
	}

	if m.Count() != 2 {
		t.Fatalf("expected 2 pets after stopping B, got %d", m.Count())
	}

	// A 和 C 应仍在运行且功能完好。
	a, ok := m.Get("A")
	if !ok || !a.IsReady() {
		t.Fatal("A should still be ready after stopping B")
	}
	if _, ok := m.Get("B"); ok {
		t.Fatal("B should be removed after Stop")
	}
	c, ok := m.Get("C")
	if !ok || !c.IsReady() {
		t.Fatal("C should still be ready after stopping B")
	}

	// 重新 Start A（幂等，fakePet.Start 设 started=true，不会有副作用）。
	m.Start("A")
	if !a.IsReady() {
		t.Fatal("A should still be ready after re-Start")
	}

	// 清理剩余。
	m.StopAll()
	if m.Count() != 0 {
		t.Fatalf("expected 0 pets after StopAll, got %d", m.Count())
	}
}

// TestRegression_HighConcurrencyShutdown 验证 100 goroutine 并发 Post/RequestRender 期间
// 调用 Stop 不会 panic/死锁/数据竞态。这是关闭链路最关键的稳定性测试。
func TestRegression_HighConcurrencyShutdown(t *testing.T) {
	e := &Engine{
		stopCh: make(chan struct{}),
		cmdCh:  make(chan func(), 64),
	}
	e.state.Store(int32(Created))
	e.transition(Created, Running)
	go e.run()

	// 启动 100 个 goroutine 持续 Post 命令。
	var wg sync.WaitGroup
	stopFlag := &atomic.Bool{}
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stopFlag.Load() {
				e.Post(func() { /* no-op */ })
				// 偶尔也调 RequestRender。
				e.RequestRender()
			}
		}()
	}

	// 给一些时间让 Post 堆积。
	time.Sleep(100 * time.Millisecond)

	// Stop。
	e.Stop()

	// 通知所有 goroutine 停止投递。
	stopFlag.Store(true)
	wg.Wait()

	// 验证最终状态。
	if EngineState(e.state.Load()) != Stopped {
		t.Fatalf("expected Stopped after concurrent Stop, got %s", EngineState(e.state.Load()))
	}
}
