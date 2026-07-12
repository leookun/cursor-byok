package pet

import (
	"context"
	"fmt"
	"image"
	"image/draw"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// toRGBA 把任意 image.Image 归一化为 *image.RGBA。
// spritesheet 经 webp/png 解码后可能是 *image.NRGBA / *image.YCbCr 等类型，
// 直接对其做 frame.(*image.RGBA) 类型断言会失败并导致该帧被静默丢弃
// （表现为"窗口已显示但桌面看不到桌宠"）。这里统一转成 *image.RGBA，
// 已是 *image.RGBA 的走零拷贝快路径。
func toRGBA(img image.Image) *image.RGBA {
	if img == nil {
		return nil
	}
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	b := img.Bounds()
	// 归一化到原点，避免 SubImage 的非零 Min 造成窗口渲染偏移。
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}

// dbgRender 在 PET_DEBUG=1 时打印每帧渲染信息（FrameIndex/尺寸），
// 用于确认 RenderLoop 确实在持续产出非空的当前帧。
func dbgRender(idx, w, h int) {
	dbg("RenderLoop: FrameIndex=%d CurrentFrame!=nil size=%dx%d", idx, w, h)
}

// EngineState 是引擎的显式生命周期状态（Phase 3 引入）。
// 所有 Post/Start/Stop/Run 全部依据 State 判断，以 atomic.Int32 实现单一状态源，
// 无锁读写且不会出现 running/state 双状态不同步的问题。
type EngineState int32

const (
	// Created 初始状态：引擎已构造但未启动，Post 拒绝所有命令。
	Created EngineState = iota
	// Running 运行中：引擎线程活跃，Post 接受命令。
	Running
	// Stopping 关闭中：引擎线程正在 drain 命令队列，Post 拒绝新业务命令但 PostCritical 仍可入队。
	Stopping
	// Stopped 已停止：引擎线程已退出，资源已释放，Post/PostCritical 均拒绝。
	Stopped
)

func (s EngineState) String() string {
	switch s {
	case Created:
		return "Created"
	case Running:
		return "Running"
	case Stopping:
		return "Stopping"
	case Stopped:
		return "Stopped"
	default:
		return fmt.Sprintf("EngineState(%d)", int(s))
	}
}

// Engine 是桌宠的核心引擎，管理窗口、动画、状态和行为。
//
// Phase 8 演进：Engine 从"所有模块的直接管理者"退化为 Composition Root。
// 它只负责：
//   1. 按正确顺序创建并装配各模块；
//   2. 提供引擎线程（命令队列 + 渲染循环）；
//   3. 通过 LifecycleManager 统一启动/停止各模块。
// 各模块通过构造函数注入依赖，不再直接依赖 *Engine。
//
// 线程模型（v2 Phase 1.1 引入）：
//   - 引擎线程：唯一允许访问 window/animCtrl/fsm/behavior 内部状态的线程。
//     由 Start() 启动的 run() goroutine 充当，串行性地执行命令队列（cmdCh）
//     与渲染循环（renderTicker），从根本上消除跨 goroutine 的 data race。
//   - 窗口线程：Win32 消息循环所在 OS 线程（NewNativeWindow 的 LockOSThread
//     goroutine），所有 WinAPI（UpdateLayeredWindow/SetWindowPos/ShowWindow）
//     均通过 PostMessage 投递到这里执行（见 Phase 1.2）。
//   - 其它线程（Behavior 定时器、bridge/Agent 回调）一律不直接改 Engine，
//     而是调用 Engine.Post(cmd) 把修改指令投递到引擎线程执行。
type Engine struct {
	window   *NativeWindow
	atlas    *FrameAtlas
	animCtrl *AnimationPlayer
	fsm      *StateMachine
	resolver *AnimationResolver
	behavior *BehaviorSystem
	motion   *MotionController
	plugins  *PluginManager
	debug    *Debugger
	sched    *Scheduler // 统一调度器（Phase 7）：所有模块的定时任务经此调度，取代各自散落的 Post

	// bus 是事件总线，用于模块间解耦通信（Phase 2 引入）。
	// FSM/Animation 的状态与动画变化会发布事件，Behavior/插件可订阅。
	bus *EventBus

	// lifecycle 按注册顺序管理所有核心模块的启动/停止/释放（Phase 8）。
	lifecycle *LifecycleManager

	// cmdCh 是引擎线程的命令队列：所有跨线程对 Engine 内部状态的修改
	// 都必须 Post 到这里，由引擎线程串行执行，消除 data race。
	cmdCh chan func()

	mu         sync.Mutex
	state      atomic.Int32 // 唯一生命周期状态源（int32）：Created→Running→Stopping→Stopped
	stopCh     chan struct{}
	renderDone sync.WaitGroup

	// dirty 渲染脏标记（v2 Phase 8）：为 true 时下一帧强制重绘。
	dirty bool
}

// transition 执行状态迁移，使用 CAS 保证原子性。
// 所有状态变更（Start/Stop/onDestroy）均经此入口，集中管控合法转移。
func (e *Engine) transition(from, to EngineState) bool {
	return e.state.CompareAndSwap(int32(from), int32(to))
}

// Post 把一条修改指令投递到引擎线程执行。
// 任何非引擎线程（Behavior 定时器、bridge/Agent 回调）需要改动
// window/animCtrl/fsm/behavior 时，都必须经此，禁止直接访问。
func (e *Engine) Post(cmd func()) {
	if cmd == nil {
		return
	}
	if EngineState(e.state.Load()) != Running {
		return
	}
	select {
	case e.cmdCh <- cmd:
	case <-e.stopCh:
		// 已停止，丢弃指令，避免向已退出的引擎线程发送命令导致永久阻塞。
	}
}

// PostCritical 关闭/清理专用投递通道：仅在 Running（正常）或 Stopping（drain 阶段）
// 允许命令进入 cmdCh。Created 时 run() 未启动、Stopped 时已销毁，均拒绝。
// 返回是否成功入队；拒绝或队列满时非阻塞失败，调用方需自行兜底。
func (e *Engine) PostCritical(cmd func()) bool {
	if cmd == nil {
		return false
	}
	switch EngineState(e.state.Load()) {
	case Running, Stopping:
		// 允许：Running 下正常执行，Stopping 下由 run() drain 执行。
	default:
		return false
	}
	select {
	case e.cmdCh <- cmd:
		return true
	default:
		// 队列满时非阻塞失败，避免永久阻塞调用方；调用方应回退到同步清理。
		log.Println("[Pet] PostCritical: engine queue full, drop (caller must fallback)")
		return false
	}
}

// NewEngine 创建桌宠引擎。
// petDir == EmbeddedDir 表示使用嵌入资源。
func NewEngine(petDir string) (*Engine, error) {
	petData, err := loadPetDataFor(petDir)
	if err != nil {
		return nil, err
	}
	sheetPath := petData.SpritesheetPath
	if petDir != EmbeddedDir {
		sheetPath = petDir + "/" + sheetPath
	}
	sheetImg, err := LoadSheet(sheetPath)
	if err != nil {
		return nil, err
	}
	return buildEngine(petData, sheetImg)
}

// NewEngineFromManifest 从 Manifest 加载并创建引擎。
// 自动调用 RepairDefaults 修复缺失字段。
func NewEngineFromManifest(m *PetManifest) (*Engine, error) {
	RepairDefaults(m, true)
	petData, err := m.ToPetData()
	if err != nil {
		return nil, err
	}
	sheetImg, err := LoadSheet(m.SheetPath)
	if err != nil {
		return nil, err
	}
	return buildEngine(petData, sheetImg)
}

// buildEngine 从 PetData + spritesheet image 构建引擎。
// Phase 8：Engine 作为 Composition Root，只负责创建和装配模块；
// 所有核心模块通过 LifecycleManager 统一管理。
func buildEngine(petData *PetData, sheetImg image.Image) (*Engine, error) {
	atlas, err := NewFrameAtlas(sheetImg, petData)
	if err != nil {
		return nil, err
	}
	if atlas.Len() == 0 {
		return nil, fmt.Errorf("frame atlas is empty: spritesheet produced 0 frames")
	}
	log.Printf("[Pet] buildEngine: atlas created, frames=%d", atlas.Len())

	firstFrame := atlas.GetFrame(0)
	winW := firstFrame.Bounds().Dx()
	winH := firstFrame.Bounds().Dy()

	win, err := NewNativeWindow(winW, winH)
	if err != nil {
		return nil, err
	}
	log.Println("[Pet] buildEngine: native window created")

	// 基础设施：先创建，先启动。
	bus := NewEventBus()
	sched := NewScheduler()

	// 功能模块：通过构造函数注入依赖，零 Engine 耦合。
	animGraph := NewAnimationGraph()
	resolver := NewAnimationResolver() // 保留旧 Resolver 供插件 API 兼容。
	animCtrl := NewAnimationPlayer(atlas, petData, bus)
	motion := NewMotionController(win, bus)
	fsm := NewStateMachine()
	debug := NewDebuggerWithBus(bus)
	intentResolver := NewIntentResolver()

	// Phase 7.5：注册内置任务所有者，行为系统持有该句柄创建/取消任务。
	behaviorOwner := sched.RegisterOwner(Owner{Name: "behavior", Priority: 10})
	behavior := NewBehaviorSystem(fsm, win, motion, debug, bus, sched, behaviorOwner, intentResolver)

	// 插件 API：仅暴露 Scheduler/Bus/FSM/Resolver，不暴露 *Engine。
	api := newPluginAPI(sched, bus, fsm, resolver)
	plugins := NewPluginManager(api)

	// 装配引擎：Engine 本身只保留运行渲染循环所必需的最小状态。
	e := &Engine{
		window:   win,
		atlas:    atlas,
		animCtrl: animCtrl,
		fsm:      fsm,
		resolver: resolver,
		motion:   motion,
		behavior: behavior,
		plugins:  plugins,
		debug:    debug,
		sched:    sched,
		bus:      bus,
		stopCh:   make(chan struct{}),
		cmdCh:    make(chan func(), 64),
	}

	// 注入调度器执行器：Scheduler 通过 Engine.Post 把任务派发到引擎线程。
	sched.SetExecute(e.Post)

	// 注入 FSM 钩子与转移表（v2 Phase 5 升级）。
	// EnterHook 统一负责"状态 -> 动画"的映射与播放，行为系统只提交意图，
	// 不再散落 playState + Transition 两步法（单一职责）。
	// Phase 9：使用 AnimationGraph 替代简单查表，支持上下文感知路由。
	fsm.EnterHook = func(s *State) {
		name := animGraph.Resolve(s, AnimationContext{})
		if animCtrl.CurrentAnimName() == "" {
			animCtrl.Play(name)
		} else {
			animCtrl.CrossFade(name, 200)
		}
	}
	fsm.ExitHook = func(s *State) {
		// 目前状态退出无需清理；预留扩展点（如取消状态专属定时器）。
	}
	// 显式转移表：登记所有合法的"主动转移"，使优先级不再是唯一判据。
	fsm.Allow(StateIdle, StateWalking)
	fsm.Allow(StateIdle, StateWaving)
	fsm.Allow(StateIdle, StateJumping)
	fsm.Allow(StateIdle, StateSitting)
	fsm.Allow(StateIdle, StateWaiting)
	fsm.Allow(StateIdle, StateReviewing)
	fsm.Allow(StateSitting, StateSleeping)
	fsm.Allow(StateSitting, StateIdle) // 坐->起
	fsm.Allow(StateWalking, StateIdle)
	fsm.Allow(StateWaving, StateIdle)
	fsm.Allow(StateJumping, StateIdle)
	fsm.Allow(StateWaiting, StateIdle)
	fsm.Allow(StateReviewing, StateIdle)
	fsm.Allow(StateSleeping, StateIdle) // 唤醒
	// 拖拽/失败态由 Interrupt 进入，无需在表中登记。

	// 注入事件钩子：FSM 状态变化发布到事件总线。
	// Animation/Motion 的完成/到达事件由组件自身通过注入的 EventPublisher 发布。
	fsm.OnChanged = func(from, to *State) {
		bus.Publish(Event{
			Type: EventStateChanged,
			Data: map[string]interface{}{"from": from.Name, "to": to.Name},
		})
	}

	// 注册生命周期（Phase 8）：按启动顺序注册，Stop 时按相反顺序停止。
	// 基础设施在前，业务模块在后；Plugin 最后启动、最先停止。
	lm := NewLifecycleManager()
	lm.Register(bus)
	lm.Register(sched)
	lm.Register(animGraph)
	lm.Register(win)
	lm.Register(animCtrl)
	lm.Register(motion)
	lm.Register(fsm)
	lm.Register(debug)
	lm.Register(behavior)
	lm.Register(plugins)
	e.lifecycle = lm

	log.Println("[Pet] buildEngine: engine created, atlas/anim/fsm/resolver/behavior/motion/plugins wired")

	// 窗口事件回调：只做最小转发，不再直接管理 behavior 生命周期。
	win.SetOnDestroy(func() {
		log.Println("[Pet] onDestroy triggered (abnormal shutdown)")
		if !e.transition(Running, Stopping) {
			log.Println("[Pet] onDestroy: not Running, skip")
			return
		}
		select {
		case <-e.stopCh:
		default:
			close(e.stopCh)
		}
		// 窗口线程回调中不直接操作引擎内部状态，改为 Post 到引擎线程执行。
		e.Post(func() {
			if e.lifecycle != nil {
				e.lifecycle.Stop()
			}
		})
	})

	// 拖拽交由 NativeWindow 自身维护 dragging 状态，Engine 层只转发位移。
	win.SetOnDrag(func(dx, dy int) {
		// 拖拽期间暂停 Motion 平滑移动，避免与用户拖拽打架。
		motion.Disable()
		win.MoveTo(win.x+dx, win.y+dy)
		// 记录交互时刻，触发 Behavior AI 交互冷却（避免刚拖完就乱跳）。
		if behavior != nil {
			behavior.NotifyInteraction()
		}
		// 发布拖拽事件（调试/插件可订阅）。
		bus.Publish(Event{
			Type: EventWindowDragged,
			Data: map[string]interface{}{"dx": dx, "dy": dy},
		})
	})
	// 拖拽结束恢复 Motion（并已在 onDrag 记录交互冷却）。
	win.SetOnDragEnd(func() {
		motion.Enable()
	})

	return e, nil
}

func loadPetDataFor(petDir string) (*PetData, error) {
	if petDir == EmbeddedDir {
		return loadEmbeddedPetData()
	}
	return LoadPetJSON(petDir)
}

func loadEmbeddedPetData() (*PetData, error) {
	data, err := embeddedPets.ReadFile(EmbeddedPetDir + "/pet.json")
	if err != nil {
		return nil, err
	}
	return parsePetData(data)
}

// Start 异步启动桌宠。启动后所有对引擎内部状态的操作都在引擎线程执行。
func (e *Engine) Start() {
	if !e.transition(Created, Running) {
		return
	}

	// 启动引擎线程（消费命令队列 + 渲染循环），统一串行化所有内部状态访问。
	go e.run()
	log.Println("[Pet] Start: engine thread launched")

	// 通过命令队列在引擎线程内完成模块启动，
	// 避免从 bridge goroutine 跨线程调用 WinAPI 与动画控制器。
	e.Post(func() {
		log.Println("[Pet] Start: showing window, starting animation & behavior")
		// Phase 8：通过 LifecycleManager 统一启动所有模块。
		if e.lifecycle != nil {
			e.lifecycle.Start()
		}
		// 播放初始 idle 动画。
		if e.animCtrl != nil {
			e.animCtrl.Play(e.resolver.Resolve(StateIdle))
		}
		log.Println("[Pet] Start: modules started")
		e.bus.Publish(Event{Type: EventPetLoaded})
	})
}

// Stop 有序停止桌宠。任何一步失败都不会死锁：
//  1. 置 running=false 并 close stopCh -> 引擎线程排空命令后退出
//  2. 通过 LifecycleManager 在引擎线程内停止所有模块
//  3. 等渲染/引擎线程退出
//  4. 释放核心资源
//
// 不依赖 3s 超时的"强行放弃清理"，避免句柄/线程泄漏。
func (e *Engine) Stop() {
	log.Println("[Pet] Stop: begin")
	if !e.transition(Running, Stopping) {
		log.Printf("[Pet] Stop: not Running (state=%s), skip", EngineState(e.state.Load()))
		return
	}

	select {
	case <-e.stopCh:
	default:
		close(e.stopCh)
	}
	log.Println("[Pet] Stop: stopCh closed")

	// Phase 8：通过 LifecycleManager 停止所有模块。
	// 必须在引擎线程内执行（这些模块是引擎内部对象）。
	// 注意：此时 running 已为 false，普通 Post 会直接丢弃命令，
	// 因此改用 PostCritical，让清理命令在 run() 的 drain 阶段执行。
	stoppedCh := make(chan struct{})
	cleanup := func() {
		// [验证] 该闭包若在引擎线程执行，说明 PostCritical 已把清理命令送入 cmdCh，
		// run() 的 drain 阶段会调用到这里；若在 Stop 调用方 goroutine 执行，说明走了兜底同步路径。
		log.Println("[Engine] cleanup executing via LifecycleManager")
		if e.lifecycle != nil {
			e.lifecycle.Stop()
		}
		close(stoppedCh)
	}
	if !e.PostCritical(cleanup) {
		// 兜底：入队失败（队列满）时，在当前 goroutine 同步清理。
		log.Println("[Engine] PostCritical enqueue failed, running cleanup synchronously (fallback)")
		cleanup()
	} else {
		log.Println("[Engine] cleanup enqueued via PostCritical (will run in run() drain)")
	}
	select {
	case <-stoppedCh:
		log.Println("[Pet] Stop: modules stopped")
	case <-time.After(2 * time.Second):
		log.Println("[Pet] Stop: modules stop TIMEOUT (continuing)")
	}

	log.Println("[Pet] Stop: waiting for engine thread...")
	renderDoneCtx, renderDoneCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer renderDoneCancel()
	go func() {
		e.renderDone.Wait()
		renderDoneCancel()
	}()
	<-renderDoneCtx.Done()
	if renderDoneCtx.Err() == context.DeadlineExceeded {
		log.Println("[Pet] Stop: engine thread TIMEOUT - forcing exit (avoid deadlock)")
	} else {
		log.Println("[Pet] Stop: engine thread exited")
	}

	// 发布卸载事件（供插件/调试系统响应），在资源释放前。
	if e.bus != nil {
		e.bus.Publish(Event{Type: EventPetUnloaded})
	}

	// Phase 8：释放所有模块资源。
	if e.lifecycle != nil {
		e.lifecycle.Dispose()
	}

	// 释放核心资源，避免频繁开关导致泄漏
	e.mu.Lock()
	e.atlas = nil
	e.animCtrl = nil
	e.fsm = nil
	e.behavior = nil
	e.mu.Unlock()
	e.state.Store(int32(Stopped))
	log.Printf("[Pet] Stop: resources released, state=%s, done", EngineState(e.state.Load()))
	e.Dump() // Phase 5：关闭后打印资源快照，快速定位未释放项。
}

// run 是引擎主线程：串行消费命令队列（cmdCh）并驱动渲染循环。
// 所有对 window/animCtrl/fsm/behavior 的访问都在此 goroutine 内发生，
// 天然无 data race，无需再用 Engine.mu 大锁包裹内部操作。
func (e *Engine) run() {
	e.renderDone.Add(1)
	defer e.renderDone.Done()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Pet][FATAL] engine thread panic recovered: %v", r)
		}
		log.Println("[Pet] engine thread: exited")
	}()

	log.Println("[Pet] engine thread: started")

	// 渲染主循环按固定高频率（≈60Hz）驱动，但动画切帧由各动画自己的 FPS
	// 决定（见 AnimationPlayer.Update 用 ap.current.FPS 计算 frameDuration）。
	// 这样 Render 最高 60Hz 刷新窗口，而 Sprite 按 8~12 FPS 自然切换，
	// 既接近 Codex 桌宠观感，也避免把动画硬压成单一低帧率（之前写死 4 FPS）。
	const renderHz = 60
	renderTicker := time.NewTicker(time.Second / time.Duration(renderHz))
	defer renderTicker.Stop()

	lastFrameIndex := -1
	lastTick := time.Now()

	for {
		select {
		case <-e.stopCh:
			log.Println("[Pet] engine thread: stopCh received, draining commands then returning")
			// 排空残留命令，确保已 Post 的清理/切换指令都执行完。
			drained := 0
			for {
				select {
				case cmd := <-e.cmdCh:
					cmd()
					drained++
				default:
					log.Printf("[Pet] engine thread: drained %d pending commands", drained)
					return
				}
			}
		case cmd := <-e.cmdCh:
			// 命令在引擎线程内执行，可直接访问所有内部组件。
			cmd()
		case <-renderTicker.C:
			now := time.Now()
			deltaMS := float64(now.Sub(lastTick).Milliseconds())
			lastTick = now

			// deltaTime 驱动：每个动画按自身 FPS 累积时间决定是否切到下一帧。
			if e.animCtrl != nil {
				e.animCtrl.Update(deltaMS)
			}
			// Motion 平滑移动（v2 Phase 7）：每帧推进窗口插值。
			if e.motion != nil {
				e.motion.Update(deltaMS)
			}

			// 没有窗口/动画控制器时跳过渲染（最小化 Engine 测试）。
			if e.animCtrl == nil || e.window == nil {
				continue
			}

			frame := e.animCtrl.CurrentFrame()

			// 脏标记去重（v2 Phase 8）：仅当帧索引变化或显式请求重绘时
			// 才调用 UpdateLayeredWindow，避免每帧无谓的 GDI 调用与 CPU 占用。
			currentIdx := e.animCtrl.CurrentFrameIndex()
			if currentIdx == lastFrameIndex && !e.dirty {
				continue
			}
			lastFrameIndex = currentIdx
			e.dirty = false

			if frame != nil {
				// 归一化为 *image.RGBA：atlas 帧可能是 NRGBA/YCbCr 等类型，
				// 之前直接类型断言 *image.RGBA 会失败并静默丢帧，导致窗口全透明。
				// 已是 RGBA 时 toRGBA 走零拷贝快路径，无额外开销。
				rgba := toRGBA(frame)
				if rgba != nil {
					if petDebug {
						// PET_DEBUG=1 时输出每帧渲染调试信息（DEBUG 级别）。
						dbgRender(currentIdx, rgba.Bounds().Dx(), rgba.Bounds().Dy())
					}
					e.window.Render(rgba)
					if e.debug != nil {
						e.debug.IncRender()
					}
				} else if petDebug {
					log.Printf("[Pet][DEBUG] RenderLoop: toRGBA returned nil at idx=%d", currentIdx)
				}
			} else {
				if petDebug {
					log.Printf("[Pet][DEBUG] RenderLoop: frame==nil at idx=%d (no pixels to draw)", currentIdx)
				}
			}
		}
	}
}

// RequestRender 请求一次重绘（即使帧未变）。供插件/外部在修改视觉后触发。
func (e *Engine) RequestRender() {
	if EngineState(e.state.Load()) != Running {
		return
	}
	e.Post(func() {
		e.dirty = true
	})
}

// Window returns the native window.
func (e *Engine) Window() *NativeWindow {
	return e.window
}

// Bus 返回事件总线，供外部（bridge/插件/调试系统）订阅事件。
func (e *Engine) Bus() *EventBus {
	return e.bus
}

// RegisterPlugin 注册一个插件。若引擎已启动，立即初始化。
func (e *Engine) RegisterPlugin(p Plugin) error {
	if e.plugins == nil {
		return nil
	}
	return e.plugins.Register(p)
}

// Debug 返回调试器，供外部查询事件日志/指标快照。
func (e *Engine) Debug() *Debugger {
	return e.debug
}

// IsReady 查询桌宠是否已完全初始化并显示。
// 只做轻量非阻塞检查：窗口已显示 + 引擎在运行 + 核心组件非空。
func (e *Engine) IsReady() bool {
	if e == nil || e.window == nil || !e.window.IsShown() {
		return false
	}
	if EngineState(e.state.Load()) != Running {
		return false
	}
	e.mu.Lock()
	atlasReady := e.atlas != nil && e.animCtrl != nil && e.behavior != nil
	e.mu.Unlock()
	return atlasReady
}

// Dump 打印引擎当前资源快照（Phase 5 引入）。
// 用于关闭失败后快速定位是哪一步未释放，无需逐个排查。
func (e *Engine) Dump() {
	state := EngineState(e.state.Load())
	e.mu.Lock()
	pluginCount := 0
	if e.plugins != nil {
		pluginCount = e.plugins.Count()
	}
	hwnd := uintptr(0)
	windowAlive := false
	if e.window != nil {
		hwnd = e.window.hwnd.Load()
		windowAlive = hwnd != 0
	}
	animOK := e.animCtrl != nil
	behaviorOK := e.behavior != nil
	fsmOK := e.fsm != nil
	schedOK := e.sched != nil
	e.mu.Unlock()

	log.Printf("[Pet][Dump] Engine state=%s | Scheduler=%v | Behavior=%v | Plugins=%d | Animations=%v | FSM=%v | Window Alive=%v | HWND=%x",
		state, schedOK, behaviorOK, pluginCount, animOK, fsmOK, windowAlive, hwnd)
}
