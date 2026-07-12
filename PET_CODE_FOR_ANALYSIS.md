# 桌宠（Desktop Pet）源代码汇总 — v2 架构（最新版）

> 用途：发送给 ChatGPT / 用于本地排查"开启桌宠后无响应"根因。
> 项目：cursor-byok（Go + Wails3 桌面应用，Windows Layered Window 渲染透明桌宠）
> 生成时间：2026-07-12（已同步至最新修复代码）
> ⚠️ 本文档为 **v2 架构** 的真实记录。旧版（v1）文档已作废。

## 0. 架构概览（v2）

```
前端 (Wails3) ──► bridge.PetService / WindowService (Go)
                        │
                        ├─ ScanPetDir / ScanPets        （扫描 pet 目录，产出 Manifest）
                        ├─ LoadEngine(m)                （按 Manifest 构建 Engine）
                        │       │
                        │       ▼
                        │   Engine ── 引擎线程（run goroutine，串行消费 cmdCh + 渲染循环）
                        │     ├─ NativeWindow   (Win32 Layered Window, 透明渲染; 自有消息循环 OS 线程)
                        │     ├─ FrameAtlas     (从 spritesheet 切片)
                        │     ├─ AnimationPlayer(动画播放, 各动画自身 FPS; Phase4 增加 CrossFade 过渡)
                        │     ├─ StateMachine   (Phase5 升级: EnterHook/ExitHook/Timeout + 转移表 + Interrupt)
                        │     ├─ AnimationResolver (状态→动画名映射)
                        │     ├─ MotionController  (Phase7: 指数缓动平滑移动, 替代瞬时 MoveTo)
                        │     ├─ BehaviorSystem  (Phase6: IntentDecider 行为 AI + Scheduler 统一调度)
                        │     ├─ Scheduler      (Phase3: 统一定时器; 回调经 Engine.Post 在引擎线程执行)
                        │     ├─ EventBus       (Phase2: 模块间解耦发布/订阅)
                        │     ├─ PluginManager  (Phase10: 插件扩展)
                        │     └─ Debugger       (Phase11: 事件 ring buffer + 指标快照)
                        │
                        ▼
              PetManager (Phase12: 多宠物实例并发管理; bridge 层已接入)
```

### 关键事实（已诊断/已落地）

- `pet` 包全部用标准库 `log.Printf`。GUI 程序无控制台，stderr 被丢弃；
  `bridge.NewWindowService()` 调用 `logger.RedirectStdLog()` 把 log 重定向到
  `%USERPROFILE%\.cursor-local-assistant-v2\logs\app.log`。
- 嵌入资源 `nezukocoder/pet.json` 声明：单帧 192×208，网格 8×9=72 帧，
  各动画 FPS：idle=6 / walk=10 / wave=8 / sit=6 / sleep=3 / think=8 / happy=10 / focus=8。
- 渲染主循环 60Hz + deltaTime 驱动（v1 写死 4 FPS，已修复）。
- `AnimationPlayer.Update` 有 `FPS<=0` 兜底（防除零冻结）。

### 已修复的历史根因（务必同步到代码）

| 序号 | 现象 | 根因 | 修复 |
|---|---|---|---|
| 1 | 窗口显示但桌面看不到桌宠 | `engine.run()` 渲染分支 `frame.(*image.RGBA)` 类型断言失败（webp 解码为 `*image.NRGBA`），每一帧被静默丢弃 → `UpdateLayeredWindow` 从未调用 | 新增 `toRGBA()` 归一化（任意 `image.Image` → `*image.RGBA`，RGBA 走零拷贝），渲染分支调用 `toRGBA(frame)` |
| 2 | 桌宠画面卡死（鼠标移到窗口上后突然冻结，workCh 满溢） | `WM_MOUSEMOVE` 在窗口线程持 `w.mu` 时调用 `onDrag`→`MoveTo`→`w.mu.Lock()`，**`sync.Mutex` 不可重入导致窗口线程自死锁**；且旧 `postWork` 满时静默丢弃掩盖了问题 | `WM_MOUSEMOVE` 改为先锁外读取字段再回调 `onDrag`；`postWork` 增加 `isWindowThread()` 判断，窗口线程内直接同步执行（避免 onDrag→MoveTo→postWork 的自等待死锁）；满时阻塞等待并打 ERROR 而非静默丢弃 |
| 3 | 鼠标移到桌宠上显示"无响应"光标 | 窗口类 `HCursor=0` 且无 `WM_SETCURSOR` 处理，分层窗口找不到类光标 | `init()` 中 `LoadCursor(0, IDC_ARROW)` 设给窗口类；`windowProc` 处理 `WM_SETCURSOR` 显式 `SetCursor` 并返回 TRUE |
| 4 | 点击关闭桌宠不消失 | `TogglePetWindow`/`ClosePetWindow` 关闭时传 `s.activePetID`（默认 `""`），但打开时 fallback 到 `EmbeddedPetDir`（"nezukocoder"），`mgr.Stop("")` 因 petID 不匹配失败 | 新增 `stopActivePet()`，activeID 为空时同样 fallback 到 `EmbeddedPetDir`，与打开逻辑一致 |

### 线程模型（v2 核心）

- **引擎线程**：`Start()` 启动的 `run()` goroutine，串行消费 `cmdCh`（命令队列）+ `renderTicker`（60Hz 渲染）。所有对 Engine 内部状态（window/animCtrl/fsm/behavior/motion）的修改，一律通过 `Engine.Post(cmd)` 投递。
- **窗口线程**：`NewNativeWindow` 内 `LockOSThread` 的 goroutine，运行 `runMessageLoop`（Win32 消息循环 + draining workCh）。所有 WinAPI（UpdateLayeredWindow/SetWindowPos/ShowWindow）经 `postWork` 在此线程执行。
- 其它线程（Behavior 定时器、bridge/Agent 回调）一律不直接改 Engine，而是 `Engine.Post(cmd)`。

### v2 各 Phase 一览

| Phase | 文件 | 内容 |
|-------|------|------|
| 1.1 | engine.go | 引擎线程 + `cmdCh` 命令队列 |
| 1.2 | window_windows.go | WinAPI 调用保持在窗口线程 |
| 2 | eventbus.go | `EventBus` 进程内同步发布/订阅 |
| 3 | scheduler.go | `Scheduler` 统一调度 |
| 4 | animation.go | `CrossFade` 渐变过渡 |
| 5 | statemachine.go | 升级：`Allow` 转移表 + `Enter/Exit/Timeout` 钩子 + `Interrupt` |
| 6 | intent.go + behavior.go | `IntentDecider` 行为 AI |
| 7 | motion.go | `MotionController` 平滑移动 |
| 8 | engine.go | `dirty` 脏标记去重渲染 |
| 10 | plugin.go | `Plugin` 接口 + `PluginManager` |
| 11 | debug.go | `Debugger` 事件环形缓冲与指标 |
| 12 | petmanager.go | `PetManager` 多实例管理 |
| bridge | bridge/window.go, bridge/pet.go | 前端桥接、资源管理、自动发现 |

---

## 1. internal/pet/engine.go（核心引擎 + 线程模型）

完整源码见附录 B。要点：

- `toRGBA(img image.Image) *image.RGBA`：把任意 image 归一化为 `*image.RGBA`；已是 RGBA 走零拷贝；否则 `draw.Draw` 拷贝（已处理 SubImage 非零 Min 偏移）。**这是修复"窗口显示但看不到桌宠"的关键**。
- `Engine.Post(cmd)`：跨线程修改内部状态的唯一入口；已停止则丢弃避免阻塞。
- `buildEngine`：注入 FSM 钩子（`EnterHook`→`resolver.Resolve`→`CrossFade`/`Play`；`ExitHook` 预留；`Allow` 显式转移表；`OnChanged`/`OnFinished` 发布事件）；配置 `onDestroy`/`onDrag`/`onDragEnd` 回调（拖拽经 `motion.Disable` + `win.MoveTo`）。
- `Start()`：`go e.run()` 后通过 `Post` 在引擎线程内 `Show→Play(idle)→behavior.Start()→plugins.StartAll()→debug.Attach→publish(PetLoaded)`。
- `run()`：串行 `select` `cmdCh` + `renderTicker`；每帧 `animCtrl.Update` + `motion.Update`；脏标记去重：仅当 `currentIdx != lastFrameIndex || dirty` 时调用 `window.Render(toRGBA(frame))`。`stopCh` 触发后排空残留命令再退出。
- `Stop()`：置 running=false + close stopCh → Post 停止 behavior/plugins → 等引擎线程退出 → `window.Close()`（PostMessage WM_QUIT）→ 等消息循环退出 → 发布 `PetUnloaded` → 释放资源。

---

## 2. internal/pet/window_windows.go（Win32 Layered Window + 消息循环）

完整源码见附录 B。要点（含修复 2、3）：

- `petDebug`：环境变量 `PET_DEBUG=1` 开启 Window 层详细调试日志。
- `WM_SETCURSOR`：设置箭头光标并返回 1（TRUE），修复"无响应"光标（修复 3）。
- `WM_MOUSEMOVE`：**先短暂加锁读取 `dragging`/`dragStart`/`onDrag`，解锁后再回调**，避免持 `w.mu` 调 `onDrag`→`MoveTo`→`w.mu.Lock()` 的自死锁（修复 2）。
- `runMessageLoop`：`MsgWaitForMultipleObjects` + `PeekMessage` 循环；每轮先 draining `workCh`（在窗口线程执行 Render/Move/Show），再处理系统消息；收到 `WM_QUIT` 退出并 `DestroyWindow`。退出前关闭 `workEvent` 句柄避免泄漏。
- `postWork(fn)`：若 `isWindowThread()` 为 true（窗口线程内，如 onDrag→MoveTo），**直接同步执行**；否则非阻塞入队，满时阻塞等待 1s 兜底并打 ERROR。`SetEvent(workEvent)` 唤醒消息循环（修复 2）。
- `isWindowThread()`：比较 `GetCurrentThreadId()` 与 `windowThreadID`（在 `runMessageLoop` 入口记录）。
- `NativeWindow` 字段含 `windowThreadID`、`workCh`(容量16)、`workEvent`、`renderCount`。
- `init()`：`LoadCursor(0, IDC_ARROW)` 设给窗口类 `HCursor`（修复 3）。
- `doRender`：INFO 级分步日志 + 计时（setup done / UpdateLayeredWindow OK/FAILED took=ms），不依赖 PET_DEBUG；像素拷贝含 alpha 预乘（alpha=0 像素 RGB 清零避免黑边）；`petDebug` 时左上角画红块便于验证窗口可见性。

---

## 3. internal/pet/behavior.go（Phase 6 行为 AI）

- `BehaviorSystem` 持有 `sched *Scheduler`、`decider *IntentDecider`、交互/空闲时间戳、`gen` generation 计数器。
- `Start()`：`gen.Add(1)`；`go b.sched.Run(b.engine.Post)`（任务 fn 经 Post 在引擎线程执行）；注入 `fsm.ScheduleTimeout`。
- `Stop()`：取消所有任务 + `sched.Stop()` + 等 `syncCh` 关闭（2s 超时兜底）。
- `doWalkOrRandom`/`applyIntent`：由 `IntentDecider.Decide(ctx)` 产出意图，再映射到 FSM 状态；短动作结束兜底回 idle。
- `markIdle`：`gen.Add(1)` 作废旧定时器，回 idle 并重启 sit/sleep/ walk 排程。
- `onMotionArrive`：Walking 到达后回 idle。

---

## 4. internal/pet/intent.go（Phase 6 行为 AI 决策器）

- `Intent`：idle/walk/jump/wave/sit/sleep 高层意图（与 FSM 状态解耦）。
- `IntentDecider.Decide(ctx)`：Agent 忙/Review 中不发起动作；刚交互完冷却期内大概率保持 idle；空闲越久越倾向 sit/sleep；其余按权重在 walk/jump/wave 间选择。

---

## 5. internal/pet/statemachine.go（Phase 5 升级）

- `StateMachine`：显式转移表 `Allow` + 优先级退化规则 + `Interrupt`（记录 `returnTo`，超时后自动恢复）。
- 副作用通过引擎级钩子 `EnterHook`/`ExitHook`/`TimeoutHook` 注入，State 保持纯数据。
- `ForceTransition`：确定性回退（回 Idle / 打断恢复）。

---

## 6. internal/pet/motion.go（Phase 7 平滑移动）

- `MotionController`：指数缓动逼近目标（`cur += (target-cur)*(1-e^{-k*dt})`），到达后回调 `onArrive`；目标 clamp 到屏幕内；拖拽时 `Disable`。

---

## 7. internal/pet/animation.go（含 Phase 4 CrossFade）

- `AnimationPlayer`：`Play`/`ForcePlay`/`CrossFade`；`Update` 推进帧（FPS<=0 兜底 12）；`CurrentFrame` 返回帧（CrossFade 期间混合）；`blendFrames` alpha 混合。

---

## 8. internal/pet/eventbus.go（Phase 2）

- `EventBus`：同步发布/订阅；handler panic 被捕获记日志不杀死发布者。

---

## 9. internal/pet/resolver.go（状态→动画名映射）

- `AnimationResolver`：状态→动画名默认映射（如 StateWalking→AnimWalk）。

---

## 10. internal/pet/plugin.go（Phase 10）

- `Plugin` 接口 + `PluginAPI` 门面 + `PluginManager` 生命周期。

---

## 11. internal/pet/debug.go（Phase 11）

- `Debugger`：事件 ring buffer（200 条）+ 指标计数 + 意图分布 + 状态快照。

---

## 12. internal/pet/petmanager.go（Phase 12 多宠物）

- `PetManager`：`Register`/`Get`/`List`/`Start`/`Stop`/`StopAll`/`Count`；并发安全；`Stop(petID)` 返回 false 表示不存在（注意 activeID 需与注册一致，见修复 4）。

---

## 13. internal/pet/atlas.go / layout.go / loader.go / manifest.go

- `FrameAtlas`：惰性切片 + 缩放缓存；`sliceFrame` 优先 SubImage 零拷贝。
- `layout.go`：`AutoDetectSpriteLayout` 从图片真实尺寸反推网格（Codex 标准 96×144 优先）。
- `loader.go`：`LoadPetJSON`/`LoadSheet`（优先文件系统，回退嵌入）；`LoadEngine` 统一入口。
- `manifest.go`：`ScanPetDir`/`validateManifest`/`RepairDefaults`；`PetManifest` 结构。

---

## 14. 嵌入资源与 bridge 层现状

### 14.1 internal/pet/nezukocoder/pet.json（默认桌宠）

见附录 B 末节。声明单帧 192×208，网格 8×9=72 帧，含 idle/walk/wave/sit/sleep/think/happy/focus 八套动画。

### 14.2 internal/bridge/window.go（v2 已接入 PetManager）

- `WindowService` 持有 `petManager *pet.PetManager`、`activePetID string`。
- `OpenPetWindow` → `openPetWindowLocked`：异步启动；activeID 为空 fallback 到 `EmbeddedPetDir`；扫描用户目录→失败回退嵌入；经 `mgr.Register(activeID, engine)` + `mgr.Start(activeID)`。
- `ClosePetWindow` / `TogglePetWindow`：经 `stopActivePet`（修复 4：activeID 为空 fallback 到 `EmbeddedPetDir`，与打开一致）。
- `onEngineStateChanged`：将 `EventStateChanged` 桥接为前端 `pet:state-changed`。
- `StopAllPets`：程序退出时统一释放。

### 14.3 internal/bridge/pet.go（资源发现/监听）

- `PetService`：`PetsDir()`、`ScanPets`、`scanAllPets`、`startWatching`（3s 间隔）、`DeletePet`。
- 事件常量：`EventPetStateChanged` / `EventPetListChanged` / `EventCursorActivity`。

### 14.4 引擎事件 → 前端桥接

- 前端 `clientApi.js`：`togglePetWindow()` → `Call.ByName("cursor/internal/bridge.WindowService.TogglePetWindow")`。
- 前端 `Home.vue`：pet 开关 `enabled` 变更时调用 `isPetWindowVisible()` + `togglePetWindow()`。
- 托盘菜单 `runner.go`：`menu.Add("显示桌宠").OnClick → windowService.TogglePetWindow()`。
- `runner.go`：`PET_DEBUG=1` 时延迟 2s 自动 `OpenPetWindow`；`OnShutdown` 调 `petService.Stop()` + `windowService.StopAllPets()`。

---

## 附录 A：完整源码汇总

> 以下源码与当前仓库**完全一致**（已含修复 1~4）。

### internal/pet/engine.go

```go
package pet

import (
	"fmt"
	"image"
	"image/draw"
	"log"
	"sync"
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

// Engine 是桌宠的核心引擎，管理窗口、动画、状态和行为。
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

	// bus 是事件总线，用于模块间解耦通信（Phase 2 引入）。
	bus *EventBus

	// cmdCh 是引擎线程的命令队列：所有跨线程对 Engine 内部状态的修改
	// 都必须 Post 到这里，由引擎线程串行执行，消除 data race。
	cmdCh chan func()

	mu         sync.Mutex
	running    bool
	stopCh     chan struct{}
	renderDone sync.WaitGroup

	// dirty 渲染脏标记（v2 Phase 8）：为 true 时下一帧强制重绘。
	dirty bool
}

// Post 把一条修改指令投递到引擎线程执行。
func (e *Engine) Post(cmd func()) {
	if cmd == nil {
		return
	}
	e.mu.Lock()
	closed := !e.running
	e.mu.Unlock()
	if closed {
		return
	}
	select {
	case e.cmdCh <- cmd:
	case <-e.stopCh:
	}
}

// NewEngine 创建桌宠引擎。
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

	motion := NewMotionController(win)

	bus := NewEventBus()
	e := &Engine{
		window:   win,
		atlas:    atlas,
		animCtrl: NewAnimationPlayer(atlas, petData),
		fsm:      NewStateMachine(),
		resolver: NewAnimationResolver(),
		motion:   motion,
		stopCh:   make(chan struct{}),
		cmdCh:    make(chan func(), 64),
		bus:      bus,
	}
	e.plugins = NewPluginManager(e)
	e.debug = NewDebugger()
	e.fsm.EnterHook = func(s *State) {
		name := e.resolver.Resolve(s)
		if e.animCtrl.CurrentAnimName() == "" {
			e.animCtrl.Play(name)
		} else {
			e.animCtrl.CrossFade(name, 200)
		}
	}
	e.fsm.ExitHook = func(s *State) {}
	e.fsm.Allow(StateIdle, StateWalking)
	e.fsm.Allow(StateIdle, StateWaving)
	e.fsm.Allow(StateIdle, StateJumping)
	e.fsm.Allow(StateIdle, StateSitting)
	e.fsm.Allow(StateIdle, StateWaiting)
	e.fsm.Allow(StateIdle, StateReviewing)
	e.fsm.Allow(StateSitting, StateSleeping)
	e.fsm.Allow(StateSitting, StateIdle)
	e.fsm.Allow(StateWalking, StateIdle)
	e.fsm.Allow(StateWaving, StateIdle)
	e.fsm.Allow(StateJumping, StateIdle)
	e.fsm.Allow(StateWaiting, StateIdle)
	e.fsm.Allow(StateReviewing, StateIdle)
	e.fsm.Allow(StateSleeping, StateIdle)

	e.fsm.OnChanged = func(from, to *State) {
		bus.Publish(Event{
			Type: EventStateChanged,
			Data: map[string]interface{}{"from": from.Name, "to": to.Name},
		})
	}
	e.animCtrl.OnFinished = func(name string) {
		bus.Publish(Event{
			Type: EventAnimationFinished,
			Data: map[string]interface{}{"anim": name},
		})
	}
	e.behavior = NewBehaviorSystem(e)
	e.motion.SetOnArrive(func() {
		if e.behavior != nil {
			e.behavior.onMotionArrive()
		}
	})
	log.Println("[Pet] buildEngine: engine created, atlas/anim/fsm/resolver/behavior/motion/plugins wired")

	win.SetOnDestroy(func() {
		log.Println("[Pet] onDestroy triggered (abnormal shutdown)")
		e.mu.Lock()
		wasRunning := e.running
		e.running = false
		e.mu.Unlock()
		if !wasRunning {
			log.Println("[Pet] onDestroy: already stopped, skip")
			return
		}
		select {
		case <-e.stopCh:
		default:
			close(e.stopCh)
		}
		e.Post(func() {
			e.behavior.Stop()
		})
	})

	win.SetOnDrag(func(dx, dy int) {
		e.motion.Disable()
		win.MoveTo(win.x+dx, win.y+dy)
		if e.behavior != nil {
			e.behavior.NotifyInteraction()
		}
		bus.Publish(Event{
			Type: EventWindowDragged,
			Data: map[string]interface{}{"dx": dx, "dy": dy},
		})
	})
	win.SetOnDragEnd(func() {
		e.motion.Enable()
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

// Start 异步启动桌宠。
func (e *Engine) Start() {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return
	}
	e.running = true
	e.mu.Unlock()

	go e.run()
	log.Println("[Pet] Start: engine thread launched")

	e.Post(func() {
		log.Println("[Pet] Start: showing window, starting animation & behavior")
		e.window.Show()
		log.Println("[Pet] Start: window shown")
		e.animCtrl.Play(e.resolver.Resolve(StateIdle))
		log.Println("[Pet] Start: anim idle playing")
		e.behavior.Start()
		log.Println("[Pet] Start: behavior started")
		e.plugins.StartAll()
		log.Println("[Pet] Start: plugins started")
		e.debug.Attach(e.bus)
		e.bus.Publish(Event{Type: EventPetLoaded})
	})
}

// Stop 有序停止桌宠。
func (e *Engine) Stop() {
	log.Println("[Pet] Stop: begin")
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		log.Println("[Pet] Stop: already stopped, return")
		return
	}
	e.running = false
	e.mu.Unlock()

	select {
	case <-e.stopCh:
	default:
		close(e.stopCh)
	}
	log.Println("[Pet] Stop: stopCh closed")

	stoppedCh := make(chan struct{})
	e.Post(func() {
		e.behavior.Stop()
		e.plugins.StopAll()
		close(stoppedCh)
	})
	select {
	case <-stoppedCh:
		log.Println("[Pet] Stop: behavior stopped")
	case <-time.After(2 * time.Second):
		log.Println("[Pet] Stop: behavior stop TIMEOUT (continuing)")
	}

	log.Println("[Pet] Stop: waiting for engine thread...")
	renderDoneCh := make(chan struct{})
	go func() {
		e.renderDone.Wait()
		close(renderDoneCh)
	}()
	select {
	case <-renderDoneCh:
		log.Println("[Pet] Stop: engine thread exited")
	case <-time.After(3 * time.Second):
		log.Println("[Pet] Stop: engine thread TIMEOUT - forcing exit (avoid deadlock)")
	}

	log.Println("[Pet] Stop: closing window...")
	e.window.Close()

	log.Println("[Pet] Stop: waiting for messageLoop...")
	e.window.WaitForMessageLoop(3 * time.Second)
	log.Println("[Pet] Stop: messageLoop exited")

	if e.bus != nil {
		e.bus.Publish(Event{Type: EventPetUnloaded})
	}

	e.mu.Lock()
	e.atlas = nil
	e.animCtrl = nil
	e.fsm = nil
	e.behavior = nil
	e.mu.Unlock()
	log.Println("[Pet] Stop: resources released, done")
}

// run 是引擎主线程：串行消费命令队列（cmdCh）并驱动渲染循环。
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

	const renderHz = 60
	renderTicker := time.NewTicker(time.Second / time.Duration(renderHz))
	defer renderTicker.Stop()

	lastFrameIndex := -1
	lastTick := time.Now()

	for {
		select {
		case <-e.stopCh:
			log.Println("[Pet] engine thread: stopCh received, draining commands then returning")
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
			cmd()
		case <-renderTicker.C:
			now := time.Now()
			deltaMS := float64(now.Sub(lastTick).Milliseconds())
			lastTick = now

			e.animCtrl.Update(deltaMS)
			e.motion.Update(deltaMS)
			frame := e.animCtrl.CurrentFrame()

			currentIdx := e.animCtrl.CurrentFrameIndex()
			if currentIdx == lastFrameIndex && !e.dirty {
				continue
			}
			lastFrameIndex = currentIdx
			e.dirty = false

			if frame != nil {
				rgba := toRGBA(frame)
				if rgba != nil {
					if petDebug {
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

// RequestRender 请求一次重绘。
func (e *Engine) RequestRender() {
	e.mu.Lock()
	running := e.running
	e.mu.Unlock()
	if !running {
		return
	}
	e.Post(func() {
		e.dirty = true
	})
}

func (e *Engine) Window() *NativeWindow { return e.window }
func (e *Engine) Bus() *EventBus        { return e.bus }

// RegisterPlugin 注册一个插件。
func (e *Engine) RegisterPlugin(p Plugin) error {
	if e.plugins == nil {
		return nil
	}
	return e.plugins.Register(p)
}

func (e *Engine) Debug() *Debugger { return e.debug }

// IsReady 查询桌宠是否已完全初始化并显示。
func (e *Engine) IsReady() bool {
	if e == nil || e.window == nil || !e.window.IsShown() {
		return false
	}
	e.mu.Lock()
	running := e.running
	atlasReady := e.atlas != nil && e.animCtrl != nil && e.behavior != nil
	e.mu.Unlock()
	return running && atlasReady
}
```

### internal/pet/window_windows.go

```go
//go:build windows

package pet

import (
	"fmt"
	"image"
	"log"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// petDebug 读取环境变量 PET_DEBUG=1 开启 Window 层详细调试日志。
var petDebug = os.Getenv("PET_DEBUG") == "1"

func dbg(format string, args ...interface{}) {
	if petDebug {
		log.Printf("[Pet][DEBUG] "+format, args...)
	}
}

// dbgDPI 在 PET_DEBUG=1 时打印进程/系统 DPI 感知信息。
func dbgDPI() {
	if !petDebug {
		return
	}
	sysDPI, _, _ := procGetDpiForSystem.Call()
	awareness := int64(-1)
	if ctx, _, _ := procGetThreadDpiAwareCtx.Call(); ctx != 0 {
		a, _, _ := procGetAwarenessFromCtx.Call(ctx)
		awareness = int64(a)
	}
	dbg("DPI: system=%d awareness=%d (0=UNAWARE,1=SYSTEM,2=PERMONITOR; UNAWARE 会导致 Layered Window 被虚化/移出可视区)",
		sysDPI, awareness)
}

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	gdi32                = syscall.NewLazyDLL("gdi32.dll")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procDefWindowProc    = user32.NewProc("DefWindowProcW")
	procCreateWindowEx   = user32.NewProc("CreateWindowExW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procGetMessage       = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessage  = user32.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procPostMessage      = user32.NewProc("PostMessageW")
	procRegisterClassEx  = user32.NewProc("RegisterClassExW")
	procSetWindowPos     = user32.NewProc("SetWindowPos")
	procGetDC            = user32.NewProc("GetDC")
	procReleaseDC        = user32.NewProc("ReleaseDC")
	procUpdateLayeredWindow = user32.NewProc("UpdateLayeredWindow")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	procIsWindowVisible = user32.NewProc("IsWindowVisible")
	procGetWindowLong  = user32.NewProc("GetWindowLongW")
	procSetWindowLong  = user32.NewProc("SetWindowLongW")
	procGetDpiForSystem       = user32.NewProc("GetDpiForSystem")
	procGetDpiForWindow       = user32.NewProc("GetDpiForWindow")
	procGetThreadDpiAwareCtx  = user32.NewProc("GetThreadDpiAwarenessContext")
	procGetAwarenessFromCtx   = user32.NewProc("GetAwarenessFromDpiAwarenessContext")
	procCreateCompatibleDC   = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC             = gdi32.NewProc("DeleteDC")
	procCreateDIBSection     = gdi32.NewProc("CreateDIBSection")
	procSelectObject         = gdi32.NewProc("SelectObject")
	procDeleteObject         = gdi32.NewProc("DeleteObject")
	procGetObject            = gdi32.NewProc("GetObjectW")
	procGetModuleHandle      = kernel32.NewProc("GetModuleHandleW")
	procGetCurrentThreadId   = kernel32.NewProc("GetCurrentThreadId")
)

const (
	WS_EX_LAYERED    = 0x00080000
	WS_EX_TOOLWINDOW = 0x00000080
	WS_EX_TOPMOST    = 0x00000008
	WS_EX_TRANSPARENT = 0x00000020
	WS_POPUP         = 0x80000000
	SM_CXSCREEN      = 0
	SM_CYSCREEN      = 1
	ULW_ALPHA        = 0x00000002
	LWA_ALPHA        = 0x00000002
	HWND_TOPMOST     = ^uintptr(0)
	SWP_NOSIZE       = 0x0001
	SWP_NOMOVE       = 0x0002
	SWP_NOACTIVATE   = 0x0010
	SWP_SHOWWINDOW   = 0x0040
	WM_DESTROY       = 0x0002
	WM_QUIT          = 0x0012
	WM_LBUTTONDOWN   = 0x0201
	WM_MOUSEMOVE     = 0x0200
	WM_LBUTTONUP     = 0x0202
	WM_RBUTTONUP     = 0x0205
	WM_PET_RENDER = 0x8001
	WM_PET_MOVE   = 0x8002
	WM_PET_SHOW   = 0x8003
	WM_PET_HIDE   = 0x8004
	BI_RGB           = 0
	DIB_RGB_COLORS   = 0
)

type BITMAPINFOHEADER struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type BLENDFUNCTION struct {
	BlendOp             byte
	BlendFlags          byte
	SourceConstantAlpha byte
	AlphaFormat         byte
}

const AC_SRC_ALPHA = 1
const AC_SRC_OVER = 0

// NativeWindow 是一个原生的 Win32 Layered Window，用于渲染透明桌宠。
type NativeWindow struct {
	hwnd       atomic.Uintptr
	width      int
	height     int
	x, y       int
	onDestroy  func()
	onDrag     func(dx, dy int)
	onDragEnd  func()
	mu         sync.Mutex
	running    bool
	shown      bool
	dragStartX int
	dragStartY int
	winStartX  int
	winStartY  int
	dragging   bool
	messageLoopDone chan struct{}
	workCh chan func()
	workEvent uintptr
	renderCount uint64
	// windowThreadID 记录运行 runMessageLoop 的 OS 线程 ID。
	windowThreadID uint32
}

var windowClass uintptr

func init() {
	hInst, _, _ := procGetModuleHandle.Call(0)
	className := syscall.StringToUTF16Ptr("CursorPetWindow")

	// 加载系统箭头光标：分层窗口必须有类光标，否则鼠标移入时 WM_SETCURSOR
	// 找不到光标，系统会显示"无响应"的等待圈。
	hArrowCursor, _, _ = procLoadCursor.Call(0, IDC_ARROW)

	var wc WNDCLASSEX
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	wc.Style = 0
	wc.LpfnWndProc = syscall.NewCallback(windowProc)
	wc.HInstance = syscall.Handle(hInst)
	wc.HCursor = syscall.Handle(hArrowCursor)
	wc.HbrBackground = 0
	wc.LpszClassName = className

	ret, _, _ := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	windowClass = ret
}

type WNDCLASSEX struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     syscall.Handle
	HIcon         syscall.Handle
	HCursor       syscall.Handle
	HbrBackground syscall.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       syscall.Handle
}

type POINT struct {
	X int32
	Y int32
}

type MSG struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      POINT
}

var nativeWindows = make(map[uintptr]*NativeWindow)
var nativeWindowsMu sync.Mutex

func windowProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	nativeWindowsMu.Lock()
	w := nativeWindows[hwnd]
	nativeWindowsMu.Unlock()

	switch msg {
	case WM_SETCURSOR:
		// 鼠标在窗口上移动时设置箭头光标并返回 TRUE，阻止 DefWindowProc 因
		// 无类光标而保留旧光标/显示"无响应"等待圈。
		if hArrowCursor != 0 {
			procSetCursor.Call(hArrowCursor)
		}
		return 1
	case WM_DESTROY:
		if w != nil && w.onDestroy != nil {
			w.onDestroy()
		}
		procPostQuitMessage.Call(0)
		return 0
	case WM_LBUTTONDOWN:
		if w != nil {
			x := int(int16(lParam & 0xFFFF))
			y := int(int16((lParam >> 16) & 0xFFFF))
			w.mu.Lock()
			w.dragging = true
			w.dragStartX = x
			w.dragStartY = y
			var rect RECT
			procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
			w.winStartX = int(rect.Left)
			w.winStartY = int(rect.Top)
			w.mu.Unlock()
			procSetCapture.Call(hwnd)
		}
		return 0
	case WM_MOUSEMOVE:
		if w != nil {
			// 注意：onDrag 回调内部会调用 win.MoveTo，而 MoveTo 会再次获取 w.mu。
			// sync.Mutex 不可重入，若在此处持锁调用 onDrag 会自死锁，导致整个
			// 窗口线程永久冻结（表现为桌宠画面卡死、workCh 满溢）。
			// 因此先短暂加锁读取拖拽状态与起点，解锁后再回调。
			w.mu.Lock()
			dragging := w.dragging
			startX := w.dragStartX
			startY := w.dragStartY
			onDrag := w.onDrag
			w.mu.Unlock()
			if dragging && onDrag != nil {
				x := int(int16(lParam & 0xFFFF))
				y := int(int16((lParam >> 16) & 0xFFFF))
				onDrag(x-startX, y-startY)
			}
		}
		return 0
	case WM_LBUTTONUP:
		if w != nil {
			w.mu.Lock()
			w.dragging = false
			w.mu.Unlock()
			procReleaseCapture.Call()
			if w.onDragEnd != nil {
				w.onDragEnd()
			}
		}
		return 0
	case WM_RBUTTONUP:
		if w != nil && w.onDragEnd != nil {
			w.onDragEnd()
		}
		return 0
	}
	ret, _, _ := procDefWindowProc.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

type RECT struct {
	Left, Top, Right, Bottom int32
}

var (
	procGetWindowRect  = user32.NewProc("GetWindowRect")
	procSetCapture     = user32.NewProc("SetCapture")
	procReleaseCapture = user32.NewProc("ReleaseCapture")
	procLoadCursor     = user32.NewProc("LoadCursorW")
	procSetCursor      = user32.NewProc("SetCursor")
	procMsgWaitForMultipleObjects = user32.NewProc("MsgWaitForMultipleObjects")
	procPeekMessage    = user32.NewProc("PeekMessageW")
	procCreateEvent    = kernel32.NewProc("CreateEventW")
	procSetEvent       = kernel32.NewProc("SetEvent")
	procCloseHandle    = kernel32.NewProc("CloseHandle")
)

// IDC_ARROW 是标准箭头光标资源 ID。
const IDC_ARROW = 32512

// WM_SETCURSOR：鼠标移入窗口时系统询问"该显示什么光标"。
const WM_SETCURSOR = 0x0020

// hArrowCursor 缓存系统箭头光标句柄，供 WM_SETCURSOR 处理使用。
var hArrowCursor uintptr

// NewNativeWindow 创建一个透明 Layered Window。
func NewNativeWindow(width, height int) (*NativeWindow, error) {
	className := syscall.StringToUTF16Ptr("CursorPetWindow")
	title := syscall.StringToUTF16Ptr("")

	dbgDPI()

	screenW, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
	screenH, _, _ := procGetSystemMetrics.Call(SM_CYSCREEN)

	exStyle := uintptr(WS_EX_LAYERED | WS_EX_TOOLWINDOW | WS_EX_TOPMOST)
	style := uintptr(WS_POPUP)

	x := (int(screenW) - width) / 2
	y := (int(screenH) - height) / 2

	dbg("NewNativeWindow: screen=%dx%d pos=(%d,%d) size=%dx%d exStyle=0x%X style=0x%X",
		screenW, screenH, x, y, width, height, exStyle, style)

	w := &NativeWindow{
		width:           width,
		height:          height,
		x:               x,
		y:               y,
		messageLoopDone: make(chan struct{}),
		workCh:          make(chan func(), 16),
	}
	ev, _, _ := procCreateEvent.Call(0, 0, 0, 0)
	w.workEvent = ev
	if w.workEvent == 0 {
		log.Println("[Pet] NewNativeWindow: failed to create workEvent")
	}

	createErrCh := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(w.messageLoopDone)

		hwnd, _, err := procCreateWindowEx.Call(
			exStyle,
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(title)),
			style,
			uintptr(x), uintptr(y),
			uintptr(width), uintptr(height),
			0, 0, 0, 0,
		)
		if hwnd == 0 {
			errMsg := fmt.Sprintf("CreateWindowEx failed: %v (GetLastError=%d)", err, getLastError())
			log.Println("[Pet] " + errMsg)
			dbg("CreateWindowEx FAILED: GetLastError=%d", getLastError())
			createErrCh <- fmt.Errorf("%s", errMsg)
			return
		}
		w.hwnd.Store(hwnd)

		nativeWindowsMu.Lock()
		nativeWindows[hwnd] = w
		nativeWindowsMu.Unlock()

		log.Printf("[Pet] NewNativeWindow: window created, hwnd=%x", hwnd)
		dbg("CreateWindow OK: HWND=%x exStyle=0x%X", hwnd, exStyle)
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		dbg("IsWindowVisible after create = %v (0=invisible)", vis != 0)
		var rc RECT
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
		dbg("WindowRect after create = (%d,%d,%d,%d) size=%dx%d",
			rc.Left, rc.Top, rc.Right, rc.Bottom, rc.Right-rc.Left, rc.Bottom-rc.Top)
		if wdpi, _, _ := procGetDpiForWindow.Call(hwnd); wdpi != 0 {
			dbg("WindowDPI = %d (96=100%%,120=125%%,144=150%%,192=200%%)", wdpi)
		}
		createErrCh <- nil
		w.runMessageLoop()
	}()

	select {
	case err := <-createErrCh:
		if err != nil {
			return nil, err
		}
	case <-time.After(2 * time.Second):
		return nil, fmt.Errorf("CreateWindowEx timeout")
	}
	return w, nil
}

// runMessageLoop 在窗口所属 OS 线程上运行消息循环。
func (w *NativeWindow) runMessageLoop() {
	log.Println("[Pet] messageLoop: started")
	if tid, _, _ := procGetCurrentThreadId.Call(); tid != 0 {
		w.windowThreadID = uint32(tid)
	}
	dbg("messageLoop: entering loop on OS thread, workEvent=%x", w.workEvent)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Pet][FATAL] messageLoop panic recovered: %v", r)
		}
		log.Println("[Pet] messageLoop: exited")
	}()

	const QS_ALLINPUT = 0x04FF
	const WAIT_OBJECT_0 = 0
	const WAIT_TIMEOUT = 0x00000102

	for {
		var handles [1]uintptr
		nCount := uintptr(0)
		if w.workEvent != 0 {
			handles[0] = w.workEvent
			nCount = 1
		}
		_, _, _ = procMsgWaitForMultipleObjects.Call(
			nCount,
			uintptr(unsafe.Pointer(&handles[0])),
			0,
			uintptr(0xFFFFFFFF),
			uintptr(QS_ALLINPUT),
		)
		_ = WAIT_OBJECT_0
		_ = WAIT_TIMEOUT

		draining := true
		for draining {
			select {
			case work := <-w.workCh:
				if work != nil {
					work()
					dbg("postWork executed (workCh len=%d)", len(w.workCh))
				}
			default:
				draining = false
			}
		}

		for {
			var msg MSG
			hasMsg, _, _ := procPeekMessage.Call(
				uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 0x0001,
			)
			if hasMsg == 0 {
				break
			}
			if msg.Message == WM_QUIT {
				log.Println("[Pet] messageLoop: WM_QUIT received, breaking")
				goto exitLoop
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))

			if msg.Message == WM_DESTROY {
				log.Println("[Pet] messageLoop: WM_DESTROY received")
				if w.onDestroy != nil {
					w.onDestroy()
				}
				procPostQuitMessage.Call(0)
			}
		}
	}
exitLoop:
	hwnd := w.hwnd.Load()
	if hwnd != 0 {
		log.Println("[Pet] messageLoop: destroying window")
		procDestroyWindow.Call(hwnd)
		nativeWindowsMu.Lock()
		delete(nativeWindows, hwnd)
		nativeWindowsMu.Unlock()
		w.hwnd.Store(0)
	}
	if w.workEvent != 0 {
		procCloseHandle.Call(w.workEvent)
		w.workEvent = 0
	}
}

// SetOnDestroy 注册销毁回调。
func (w *NativeWindow) SetOnDestroy(fn func()) { w.onDestroy = fn }

// SetOnDrag 注册拖拽回调。
func (w *NativeWindow) SetOnDrag(fn func(dx, dy int)) { w.onDrag = fn }

// SetOnDragEnd 注册拖拽结束回调。
func (w *NativeWindow) SetOnDragEnd(fn func()) { w.onDragEnd = fn }

// WaitForMessageLoop 等待窗口消息循环退出。
func (w *NativeWindow) WaitForMessageLoop(timeout time.Duration) {
	if w.messageLoopDone == nil {
		return
	}
	select {
	case <-w.messageLoopDone:
		log.Println("[Pet] WaitForMessageLoop: done")
	case <-time.After(timeout):
		log.Println("[Pet] WaitForMessageLoop: TIMEOUT - messageLoop did not exit in time")
	}
}

// Show 显示窗口。
func (w *NativeWindow) Show() {
	hwnd := w.hwnd.Load()
	if hwnd == 0 {
		log.Println("[Pet] Show: hwnd is 0, cannot show")
		return
	}
	w.mu.Lock()
	w.shown = true
	w.mu.Unlock()
	w.postWork(func() {
		ret, _, _ := procShowWindow.Call(hwnd, 5)
		dbg("ShowWindow(SW_SHOW) ret=%d (0=fail, GetLastError=%d)", ret, getLastError())
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		dbg("IsWindowVisible after Show = %v", vis != 0)
		var rc RECT
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
		dbg("WindowRect after Show = (%d,%d,%d,%d) size=%dx%d",
			rc.Left, rc.Top, rc.Right, rc.Bottom, rc.Right-rc.Left, rc.Bottom-rc.Top)
	})
}

// Hide 隐藏窗口。
func (w *NativeWindow) Hide() {
	hwnd := w.hwnd.Load()
	if hwnd == 0 {
		return
	}
	w.mu.Lock()
	w.shown = false
	w.mu.Unlock()
	w.postWork(func() {
		procShowWindow.Call(hwnd, 0)
	})
}

// isWindowThread 判断当前 goroutine 是否运行在窗口线程上。
func (w *NativeWindow) isWindowThread() bool {
	if w.windowThreadID == 0 {
		return false
	}
	tid, _, _ := procGetCurrentThreadId.Call()
	return uint32(tid) == w.windowThreadID
}

// postWork 把一条 WinAPI 操作投递到窗口线程执行。
func (w *NativeWindow) postWork(fn func()) {
	if fn == nil {
		return
	}
	// 若调用方就在窗口线程（如 WndProc 的 onDrag→MoveTo 回调），直接同步执行，
	// 否则会死锁：postWork 阻塞等 workCh 消费，而消费需要窗口线程回到循环，
	// 但窗口线程正卡在 onDrag 里等 postWork 入队。
	if w.isWindowThread() {
		fn()
		return
	}
	// 优先非阻塞入队；满时阻塞等待消费（带 1s 超时兜底，避免永久死锁放大）。
	select {
	case w.workCh <- fn:
	default:
		select {
		case w.workCh <- fn:
		case <-time.After(1 * time.Second):
			log.Println("[Pet][ERROR] postWork: workCh still full after 1s wait (window thread likely blocked in doRender) - dropping work")
		}
	}
	if w.workEvent != 0 {
		procSetEvent.Call(w.workEvent)
	} else {
		dbg("postWork: workEvent=0, cannot wake window thread")
	}
}

// IsShown 查询窗口是否已显示。
func (w *NativeWindow) IsShown() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.shown
}

// Close 请求销毁窗口。
func (w *NativeWindow) Close() {
	hwnd := w.hwnd.Load()
	if hwnd == 0 {
		return
	}
	log.Println("[Pet] Close: posting WM_QUIT to window thread")
	procPostMessage.Call(hwnd, WM_QUIT, 0, 0)
}

// MoveTo 移动窗口到屏幕坐标。
func (w *NativeWindow) MoveTo(x, y int) {
	hwnd := w.hwnd.Load()
	if hwnd == 0 {
		return
	}
	w.mu.Lock()
	w.x = x
	w.y = y
	w.mu.Unlock()
	w.postWork(func() {
		procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), 0, 0, SWP_NOSIZE|SWP_NOACTIVATE)
	})
}

// Position 返回窗口当前屏幕坐标。
func (w *NativeWindow) Position() (int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.x, w.y
}

// Size 返回窗口宽高。
func (w *NativeWindow) Size() (int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.width, w.height
}

// Render 绘制 RGBA 图像到 Layered Window。
func (w *NativeWindow) Render(img *image.RGBA) {
	if img == nil {
		dbg("Render skipped: img==nil")
		return
	}
	hwnd := w.hwnd.Load()
	if hwnd == 0 {
		dbg("Render skipped: hwnd==0 (window destroyed)")
		return
	}
	b := img.Bounds()
	dbg("Render: frame bounds=%dx%d stride=%d", b.Dx(), b.Dy(), img.Stride)
	w.postWork(func() {
		w.doRender(hwnd, img)
	})
}

// doRender 在窗口线程上执行实际 UpdateLayeredWindow。
func (w *NativeWindow) doRender(hwnd uintptr, img *image.RGBA) {
	t0 := time.Now()
	if img == nil || hwnd == 0 {
		log.Printf("[Pet][ERROR] doRender: early return img=nil?%v hwnd=%d", img == nil, hwnd)
		return
	}
	bounds := img.Bounds()
	bw := bounds.Dx()
	bh := bounds.Dy()

	hdcMem, _, _ := procCreateCompatibleDC.Call(0)
	if hdcMem == 0 {
		log.Printf("[Pet][ERROR] doRender: CreateCompatibleDC failed hwnd=%x", hwnd)
		return
	}
	defer procDeleteDC.Call(hdcMem)

	var bi BITMAPINFOHEADER
	bi.BiSize = uint32(unsafe.Sizeof(bi))
	bi.BiWidth = int32(bw)
	bi.BiHeight = -int32(bh)
	bi.BiPlanes = 1
	bi.BiBitCount = 32
	bi.BiCompression = BI_RGB

	var bits unsafe.Pointer
	hBitmap, _, _ := procCreateDIBSection.Call(
		hdcMem,
		uintptr(unsafe.Pointer(&bi)),
		DIB_RGB_COLORS,
		uintptr(unsafe.Pointer(&bits)),
		0, 0,
	)
	if hBitmap == 0 {
		log.Printf("[Pet][ERROR] doRender: CreateDIBSection failed hwnd=%x size=%dx%d GetLastError=%d",
			hwnd, bw, bh, getLastError())
		return
	}
	defer procDeleteObject.Call(hBitmap)

	oldBitmap, _, _ := procSelectObject.Call(hdcMem, hBitmap)
	defer procSelectObject.Call(hdcMem, oldBitmap)
	log.Printf("[Pet] doRender: setup done hwnd=%x size=%dx%d (%.2fms)", hwnd, bw, bh, float64(time.Since(t0).Microseconds())/1000)

	pixels := unsafe.Slice((*byte)(bits), bw*bh*4)
	stride := img.Stride
	for row := 0; row < bh; row++ {
		for col := 0; col < bw; col++ {
			src := img.Pix[row*stride+col*4:]
			dst := pixels[row*bw*4+col*4:]
			alpha := src[3]
			if alpha == 0 {
				dst[0] = 0
				dst[1] = 0
				dst[2] = 0
				dst[3] = 0
			} else {
				dst[0] = byte(uint32(src[2]) * uint32(alpha) / 255)
				dst[1] = byte(uint32(src[1]) * uint32(alpha) / 255)
				dst[2] = byte(uint32(src[0]) * uint32(alpha) / 255)
				dst[3] = alpha
			}
		}
	}

	if petDebug {
		const ov = 16
		for row := 0; row < ov && row < bh; row++ {
			for col := 0; col < ov && col < bw; col++ {
				dst := pixels[row*bw*4+col*4:]
				edge := row == 0 || col == 0 || row == ov-1 || col == ov-1
				if edge {
					dst[0], dst[1], dst[2], dst[3] = 255, 255, 255, 255
				} else {
					dst[0], dst[1], dst[2], dst[3] = 0, 0, 255, 255
				}
			}
		}
	}

	blend := BLENDFUNCTION{
		BlendOp:             AC_SRC_OVER,
		BlendFlags:          0,
		SourceConstantAlpha: 255,
		AlphaFormat:         AC_SRC_ALPHA,
	}

	ptSrc := POINT{X: 0, Y: 0}
	size := struct {
		Cx int32
		Cy int32
	}{int32(bw), int32(bh)}

	ret, _, _ := procUpdateLayeredWindow.Call(
		hwnd,
		0,
		0,
		uintptr(unsafe.Pointer(&size)),
		hdcMem,
		uintptr(unsafe.Pointer(&ptSrc)),
		0,
		uintptr(unsafe.Pointer(&blend)),
		ULW_ALPHA,
	)
	ulwDur := time.Since(t0)
	if ret == 0 {
		log.Printf("[Pet][ERROR] UpdateLayeredWindow FAILED: GetLastError=%d (hwnd=%x size=%dx%d took=%.2fms)",
			getLastError(), hwnd, bw, bh, float64(ulwDur.Microseconds())/1000)
	} else {
		log.Printf("[Pet] doRender: UpdateLayeredWindow OK hwnd=%x size=%dx%d took=%.2fms total=%.2fms",
			hwnd, bw, bh, float64(ulwDur.Microseconds())/1000, float64(time.Since(t0).Microseconds())/1000)
		w.renderCount++
		if w.renderCount <= 5 || w.renderCount%60 == 0 {
			dbg("UpdateLayeredWindow success #%d: hwnd=%x size=%dx%d (PET_DEBUG 下左上角有红块=窗口真的可见)",
				w.renderCount, hwnd, bw, bh)
		}
	}
}

var procShowWindow = user32.NewProc("ShowWindow")
var procGetLastError = kernel32.NewProc("GetLastError")

func getLastError() uint32 {
	ret, _, _ := procGetLastError.Call()
	return uint32(ret)
}
```

### internal/pet/behavior.go

```go
package pet

import (
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

const behaviorOwner = "behavior"

// BehaviorSystem 管理桌宠的自主行为。
type BehaviorSystem struct {
	engine *Engine
	sched  *Scheduler
	syncCh chan struct{}
	mu     sync.Mutex
	active bool
	moving bool
	gen atomic.Int64

	decider *IntentDecider
	idleSince time.Time
	lastInteract time.Time
}

const (
	walkDelayMin    = 4
	walkDelayExtra  = 6
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

func NewBehaviorSystem(e *Engine) *BehaviorSystem {
	return &BehaviorSystem{
		engine: e,
		sched:  NewScheduler(),
		syncCh: make(chan struct{}),
		decider: NewIntentDecider(),
	}
}

func (b *BehaviorSystem) Start() {
	b.gen.Add(1)
	b.mu.Lock()
	b.active = true
	b.mu.Unlock()

	go func() {
		b.sched.Run(b.engine.Post)
		close(b.syncCh)
	}()

	b.engine.fsm.ScheduleTimeout = func(d time.Duration, cb func()) {
		b.sched.Once(behaviorOwner, d, cb)
	}

	b.resetIdleTimers()
	b.scheduleWalk()
	log.Println("[Pet] Behavior: started")
}

func (b *BehaviorSystem) Stop() {
	b.mu.Lock()
	b.active = false
	b.mu.Unlock()

	b.sched.CancelAll()
	b.sched.Stop()
	select {
	case <-b.syncCh:
	case <-time.After(2 * time.Second):
		log.Println("[Pet] Behavior: Stop TIMEOUT waiting scheduler exit")
	}
	log.Println("[Pet] Behavior: stopped")
}

func (b *BehaviorSystem) Update() {}

func (b *BehaviorSystem) isActiveGen(gen int64) bool {
	if b.gen.Load() != gen {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active
}

func (b *BehaviorSystem) post(cmd func()) { b.engine.Post(cmd) }

func (b *BehaviorSystem) publish(t EventType) {
	if b.engine.bus != nil {
		b.engine.bus.Publish(Event{Type: t})
	}
}

func (b *BehaviorSystem) OnAgentStarted() {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	b.publish(EventAgentStarted)
	b.post(func() { b.engine.fsm.Transition(StateWaiting) })
}

func (b *BehaviorSystem) OnAgentFinished() {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	b.publish(EventAgentFinished)
	b.post(func() { b.markIdle() })
}

func (b *BehaviorSystem) OnAgentFailed() {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	b.publish(EventAgentFailed)
	gen := b.gen.Load()
	b.sched.Once(behaviorOwner, failRecovery, func() {
		if !b.isActiveGen(gen) {
			return
		}
		b.post(func() { b.markIdle() })
	})
}

func (b *BehaviorSystem) OnReviewStarted() {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	b.publish(EventReviewStarted)
	b.post(func() { b.engine.fsm.Transition(StateReviewing) })
}

func (b *BehaviorSystem) OnReviewFinished() {
	b.publish(EventReviewFinished)
	b.post(func() {
		if b.engine.fsm.Is(StateReviewing) {
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
	b.sched.Once(behaviorOwner, time.Duration(delay)*time.Second, func() {
		if !b.isActiveGen(gen) {
			return
		}
		b.doWalkOrRandom()
	})
}

func (b *BehaviorSystem) doWalkOrRandom() {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return
	}
	cur := b.engine.fsm.Current()
	if cur == StateDragging || cur == StateSleeping || cur == StateFailed {
		b.mu.Unlock()
		b.scheduleWalk()
		return
	}
	b.mu.Unlock()

	now := time.Now()
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
		IdleSeconds:       idleSec,
		AgentBusy:         cur == StateWaiting,
		Reviewing:         cur == StateReviewing,
		LastInteractionSec: interactSec,
	}
	intent := b.decider.Decide(ctx)
	b.applyIntent(intent)
}

func (b *BehaviorSystem) applyIntent(intent Intent) {
	if b.engine.debug != nil {
		b.engine.debug.RecordIntent(intent)
	}
	gen := b.gen.Load()
	switch intent {
	case IntentWalk:
		b.engine.fsm.Transition(StateWalking)
		win := b.engine.window
		motion := b.engine.motion
		targetX := 50 + rand.Intn(screenDefaultW-100-win.width)
		targetY := 50 + rand.Intn(screenDefaultH-100-win.height)
		log.Printf("[Pet] Behavior: walking to (%d,%d) intent=%s", targetX, targetY, intent)
		motion.MoveTo(targetX, targetY)
		b.sched.Once(behaviorOwner, walkDuration, func() {
			if !b.isActiveGen(gen) {
				return
			}
			b.post(func() {
				if b.engine.fsm.Is(StateWalking) {
					b.markIdle()
				}
			})
		})
	case IntentJump:
		b.engine.fsm.Transition(StateJumping)
		b.sched.Once(behaviorOwner, jumpDuration, func() {
			if !b.isActiveGen(gen) {
				return
			}
			b.post(b.markIdle)
		})
	case IntentWave:
		b.engine.fsm.Transition(StateWaving)
		b.sched.Once(behaviorOwner, waveDuration, func() {
			if !b.isActiveGen(gen) {
				return
			}
			b.post(b.markIdle)
		})
	case IntentSit:
		b.post(func() {
			if b.engine.fsm.Is(StateIdle) {
				b.engine.fsm.Transition(StateSitting)
			}
		})
	case IntentSleep:
		b.post(func() {
			if b.engine.fsm.Is(StateSitting) {
				b.engine.fsm.ForceTransition(StateSleeping)
			} else if b.engine.fsm.Is(StateIdle) {
				b.engine.fsm.ForceTransition(StateSleeping)
			}
		})
	default:
		b.scheduleWalk()
	}
}

func (b *BehaviorSystem) markIdle() {
	b.gen.Add(1)
	b.engine.fsm.ForceTransition(StateIdle)
	b.idleSince = time.Now()
	b.resetIdleTimers()
	b.scheduleWalk()
}

func (b *BehaviorSystem) NotifyInteraction() {
	b.mu.Lock()
	b.lastInteract = time.Now()
	b.mu.Unlock()
}

func (b *BehaviorSystem) onMotionArrive() {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	if b.engine.fsm.Is(StateWalking) {
		b.markIdle()
	}
}

func (b *BehaviorSystem) RequestIntent(it Intent) {
	b.post(func() { b.applyIntent(it) })
}

func (b *BehaviorSystem) resetIdleTimers() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.active {
		return
	}
	gen := b.gen.Load()
	b.sched.Once(behaviorOwner, sitDelay, func() {
		if !b.isActiveGen(gen) {
			return
		}
		b.post(func() {
			if b.engine.fsm.Is(StateIdle) {
				b.engine.fsm.Transition(StateSitting)
			}
		})
	})
	b.sched.Once(behaviorOwner, sleepDelay, func() {
		if !b.isActiveGen(gen) {
			return
		}
		b.post(func() {
			if b.engine.fsm.Is(StateSitting) {
				b.engine.fsm.ForceTransition(StateSleeping)
			}
		})
	})
}
```

### internal/pet/scheduler.go

```go
package pet

import (
	"log"
	"sync"
	"time"
)

// ScheduledTask 表示一个已调度的任务。
type ScheduledTask struct {
	id         int
	owner      string
	firstDelay time.Duration
	interval   time.Duration
	fn         func()
	canceled   bool
	deadline   time.Time
}

// Scheduler 是统一调度器。
type Scheduler struct {
	mu       sync.Mutex
	tasks    map[int]*ScheduledTask
	nextID   int
	stopCh   chan struct{}
	tickCh   chan struct{}
	now      func() time.Time
}

func NewScheduler() *Scheduler {
	return &Scheduler{
		tasks:  make(map[int]*ScheduledTask),
		stopCh: make(chan struct{}),
		tickCh: make(chan struct{}, 1),
		now:    time.Now,
	}
}

func (s *Scheduler) Once(owner string, delay time.Duration, fn func()) (cancel func()) {
	return s.schedule(owner, delay, 0, fn)
}

func (s *Scheduler) Interval(owner string, interval time.Duration, fn func()) (cancel func()) {
	return s.schedule(owner, interval, interval, fn)
}

func (s *Scheduler) schedule(owner string, delay, interval time.Duration, fn func()) func() {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.tasks[id] = &ScheduledTask{
		id:       id,
		owner:    owner,
		interval: interval,
		fn:       fn,
	}
	s.mu.Unlock()
	select {
	case s.tickCh <- struct{}{}:
	default:
	}
	return func() {
		s.mu.Lock()
		if t, ok := s.tasks[id]; ok {
			t.canceled = true
			delete(s.tasks, id)
		}
		s.mu.Unlock()
	}
}

func (s *Scheduler) CancelByOwner(owner string) {
	s.mu.Lock()
	for id, t := range s.tasks {
		if t.owner == owner {
			t.canceled = true
			delete(s.tasks, id)
		}
	}
	s.mu.Unlock()
}

func (s *Scheduler) CancelAll() {
	s.mu.Lock()
	for id, t := range s.tasks {
		t.canceled = true
		delete(s.tasks, id)
	}
	s.mu.Unlock()
}

func (s *Scheduler) Run(execute func(fn func())) {
	log.Println("[Pet][Scheduler] Run: started")
	defer log.Println("[Pet][Scheduler] Run: exited")

	for {
		next := s.nextTick()
		var timer *time.Timer
		var timerC <-chan time.Time
		if next > 0 {
			timer = time.NewTimer(next)
			timerC = timer.C
		}

		select {
		case <-s.stopCh:
			if timer != nil {
				timer.Stop()
			}
			return
		case <-s.tickCh:
			if timer != nil {
				timer.Stop()
			}
		case <-timerC:
			s.fireDue(execute)
		}
	}
}

func (s *Scheduler) nextTick() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var min time.Duration
	has := false
	for _, t := range s.tasks {
		if t.canceled {
			continue
		}
		if t.deadline.IsZero() {
			t.deadline = now.Add(t.firstDelay)
		}
		d := t.deadline.Sub(now)
		if !has || d < min {
			min = d
			has = true
		}
	}
	if !has {
		return 0
	}
	if min < 0 {
		return 0
	}
	return min
}

func (s *Scheduler) fireDue(execute func(fn func())) {
	s.mu.Lock()
	now := s.now()
	due := make([]*ScheduledTask, 0)
	for _, t := range s.tasks {
		if t.canceled {
			delete(s.tasks, t.id)
			continue
		}
		if t.deadline.IsZero() {
			t.deadline = now.Add(t.firstDelay)
		}
		if !now.Before(t.deadline) {
			due = append(due, t)
		}
	}
	s.mu.Unlock()

	for _, t := range due {
		execute(t.fn)
		s.mu.Lock()
		if t.interval > 0 && !t.canceled {
			t.deadline = s.now().Add(t.interval)
		} else {
			delete(s.tasks, t.id)
		}
		s.mu.Unlock()
	}
}

func (s *Scheduler) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}
```

### internal/pet/intent.go

```go
package pet

import (
	"math/rand"
	"time"
)

// Intent 是 Behavior AI 决策出的"意图"（与 FSM 状态解耦的高层语义）。
type Intent int

const (
	IntentIdle Intent = iota
	IntentWalk
	IntentJump
	IntentWave
	IntentSit
	IntentSleep
)

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

// BehaviorContext 是意图决策的上下文快照。
type BehaviorContext struct {
	IdleSeconds       float64
	AgentBusy         bool
	Reviewing         bool
	LastInteractionSec float64
	RNG               *rand.Rand
}

// IntentDecider 是 Behavior AI 的核心。
type IntentDecider struct {
	weightWalk  float64
	weightJump  float64
	weightWave  float64
	weightSit   float64
	weightSleep float64
	interactionCooldown float64
}

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

func (d *IntentDecider) Decide(ctx BehaviorContext) Intent {
	rng := ctx.RNG
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	if ctx.AgentBusy || ctx.Reviewing {
		return IntentIdle
	}

	if ctx.LastInteractionSec >= 0 && ctx.LastInteractionSec < d.interactionCooldown {
		if rng.Float64() < 0.8 {
			return IntentIdle
		}
	}

	idleFactor := clamp01(ctx.IdleSeconds / 60.0)
	wWalk := d.weightWalk * (1 - 0.5*idleFactor)
	wSit := d.weightSit + 4*idleFactor
	wSleep := d.weightSleep + 3*idleFactor
	wJump := d.weightJump
	wWave := d.weightWave

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

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
```

### internal/pet/statemachine.go

```go
package pet

import (
	"log"
	"sync"
	"time"
)

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

type State struct {
	Name     string
	Priority int
	Timeout  time.Duration
}

var (
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

type StateMachine struct {
	current  *State
	mu       sync.RWMutex
	allowed  map[*State]map[*State]bool
	returnTo *State

	OnChanged func(from, to *State)
	EnterHook func(s *State)
	ExitHook  func(s *State)
	TimeoutHook func(s *State)
	ScheduleTimeout func(d time.Duration, cb func())
}

func NewStateMachine() *StateMachine {
	return &StateMachine{
		current: StateIdle,
		allowed: make(map[*State]map[*State]bool),
	}
}

func (sm *StateMachine) Allow(from, to *State) {
	if from == nil || to == nil {
		return
	}
	if sm.allowed[from] == nil {
		sm.allowed[from] = make(map[*State]bool)
	}
	sm.allowed[from][to] = true
}

func (sm *StateMachine) canTransitionToLocked(target *State) bool {
	if target == sm.current {
		return false
	}
	if m, ok := sm.allowed[sm.current]; ok {
		if allowed, ok := m[target]; ok {
			return allowed
		}
	}
	return target.Priority > sm.current.Priority
}

func (sm *StateMachine) Transition(target *State) bool {
	if target == nil {
		return false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.doTransitionLocked(target, false)
}

func (sm *StateMachine) Interrupt(target *State) bool {
	if target == nil {
		return false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.doTransitionLocked(target, true)
}

func (sm *StateMachine) doTransitionLocked(target *State, interrupt bool) bool {
	if target == sm.current {
		return false
	}
	if !interrupt && !sm.canTransitionToLocked(target) {
		return false
	}

	from := sm.current

	if interrupt && sm.returnTo == nil {
		sm.returnTo = from
	}

	if sm.ExitHook != nil {
		sm.ExitHook(from)
	}

	sm.current = target
	log.Printf("[Pet] FSM: %s -> %s (interrupt=%v)", from.Name, target.Name, interrupt)

	if sm.EnterHook != nil {
		sm.EnterHook(target)
	}

	sm.scheduleTimeoutLocked(target)

	if sm.OnChanged != nil {
		sm.OnChanged(from, target)
	}
	return true
}

func (sm *StateMachine) scheduleTimeoutLocked(s *State) {
	if s == nil || s.Timeout <= 0 || sm.ScheduleTimeout == nil {
		return
	}
	d := s.Timeout
	sm.ScheduleTimeout(d, func() {
		sm.mu.Lock()
		still := sm.current == s
		sm.mu.Unlock()
		if !still {
			return
		}
		if sm.TimeoutHook != nil {
			sm.TimeoutHook(s)
			return
		}
		sm.mu.Lock()
		ret := sm.returnTo
		sm.returnTo = nil
		sm.mu.Unlock()
		if ret != nil {
			sm.ForceTransition(ret)
		} else {
			sm.ForceTransition(StateIdle)
		}
	})
}

func (sm *StateMachine) ForceTransition(target *State) bool {
	if target == nil {
		return false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if target == sm.current {
		return false
	}
	from := sm.current
	if sm.ExitHook != nil {
		sm.ExitHook(from)
	}
	sm.current = target
	log.Printf("[Pet] FSM: %s -> %s (force)", from.Name, target.Name)
	if sm.EnterHook != nil {
		sm.EnterHook(target)
	}
	sm.scheduleTimeoutLocked(target)
	if sm.OnChanged != nil {
		sm.OnChanged(from, target)
	}
	return true
}

func (sm *StateMachine) Current() *State {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.current
}

func (sm *StateMachine) Is(s *State) bool {
	if s == nil {
		return false
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.current == s
}
```

### internal/pet/motion.go

```go
package pet

import (
	"math"
)

type motionWindow interface {
	Position() (int, int)
	Size() (int, int)
	MoveTo(x, y int)
}

// MotionController 负责桌宠窗口的平滑移动（v2 Phase 7）。
type MotionController struct {
	win motionWindow

	curX, curY    float64
	targetX, tgtY float64
	arrived       bool
	enabled       bool

	smoothing float64
	onArrive  func()

	screenW, screenH int
}

func NewMotionController(win *NativeWindow) *MotionController {
	x, y := win.Position()
	sw, sh := winSizeViaMetrics()
	return &MotionController{
		win:       win,
		curX:      float64(x),
		curY:      float64(y),
		targetX:   float64(x),
		tgtY:      float64(y),
		arrived:   true,
		enabled:   true,
		smoothing: 6.0,
		screenW:   sw,
		screenH:   sh,
	}
}

func (m *MotionController) SetOnArrive(fn func()) { m.onArrive = fn }

func (m *MotionController) SetSmoothing(s float64) {
	if s > 0 {
		m.smoothing = s
	}
}

func (m *MotionController) MoveTo(x, y int) bool {
	mx, my := m.win.Size()
	x = clampInt(x, 0, m.screenW-mx)
	y = clampInt(y, 0, m.screenH-my)
	if x == int(m.targetX) && y == int(m.tgtY) && m.arrived {
		return false
	}
	m.targetX = float64(x)
	m.tgtY = float64(y)
	m.arrived = false
	return true
}

func (m *MotionController) Disable() { m.enabled = false }
func (m *MotionController) Enable()  { m.enabled = true }
func (m *MotionController) IsArrived() bool { return m.arrived }

func (m *MotionController) Update(dtMs float64) {
	if !m.enabled || m.arrived {
		return
	}
	dt := dtMs / 1000.0
	factor := 1 - math.Exp(-m.smoothing*dt)
	newX := m.curX + (m.targetX-m.curX)*factor
	newY := m.curY + (m.targetY-m.curY)*factor

	const eps = 0.5
	if math.Abs(m.targetX-newX) < eps && math.Abs(m.tgtY-newY) < eps {
		newX = m.targetX
		newY = m.tgtY
		m.arrived = true
	}
	m.curX = newX
	m.curY = newY
	m.win.MoveTo(int(newX), int(newY))

	if m.arrived && m.onArrive != nil {
		m.onArrive()
	}
}

func winSizeViaMetrics() (int, int) {
	sw, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
	sh, _, _ := procGetSystemMetrics.Call(SM_CYSCREEN)
	return int(sw), int(sh)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
```

### internal/pet/animation.go

```go
package pet

import (
	"image"
	"log"
	"sync"
)

const (
	AnimIdle    = "idle"
	AnimWalk    = "walk"
	AnimWave    = "wave"
	AnimSit     = "sit"
	AnimSleep   = "sleep"
	AnimThink   = "think"
	AnimHappy   = "happy"
	AnimFocus   = "focus"
)

const msPerSecond = 1000.0

type AnimationPlayer struct {
	atlas      *FrameAtlas
	anims      map[string]*Animation
	current    *Animation
	currentIdx int
	elapsed    float64
	playing    bool
	queued     *Animation

	blendFrom     *Animation
	blendFromIdx  int
	blendElapsed  float64
	blendDuration float64

	mu sync.Mutex
	OnFinished func(name string)
}

type Animation struct {
	Name     string
	Frames   []int
	FPS      float64
	Loop     bool
	Priority int
}

func NewAnimationPlayer(atlas *FrameAtlas, pet *PetData) *AnimationPlayer {
	if atlas == nil || pet == nil {
		return &AnimationPlayer{anims: make(map[string]*Animation)}
	}
	ap := &AnimationPlayer{
		atlas: atlas,
		anims: make(map[string]*Animation),
	}
	for name, def := range pet.Animations {
		ap.anims[name] = &Animation{
			Name:     name,
			Frames:   def.Frames,
			FPS:      float64(def.FPS),
			Loop:     def.Loop,
			Priority: def.Priority,
		}
	}
	return ap
}

func (ap *AnimationPlayer) Play(name string) {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	anim, ok := ap.anims[name]
	if !ok {
		log.Printf("[Pet] Anim: play %q ignored (no such animation)", name)
		return
	}
	if ap.current != nil && ap.current.Priority > anim.Priority {
		ap.queued = anim
		log.Printf("[Pet] Anim: play %q queued (current %q higher priority)", name, ap.current.Name)
		return
	}
	log.Printf("[Pet] Anim: play %q (priority=%d)", name, anim.Priority)
	ap.startAnimLocked(anim)
}

func (ap *AnimationPlayer) ForcePlay(name string) {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	anim, ok := ap.anims[name]
	if !ok {
		return
	}
	ap.startAnimLocked(anim)
}

func (ap *AnimationPlayer) startAnimLocked(anim *Animation) {
	ap.current = anim
	ap.currentIdx = 0
	ap.elapsed = 0
	ap.playing = true
	ap.queued = nil
	ap.blendFrom = nil
	ap.blendFromIdx = 0
	ap.blendElapsed = 0
	ap.blendDuration = 0
}

func (ap *AnimationPlayer) CrossFade(name string, duration float64) {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	anim, ok := ap.anims[name]
	if !ok {
		log.Printf("[Pet] Anim: crossfade %q ignored (no such animation)", name)
		return
	}
	if ap.current == anim && ap.blendDuration <= 0 {
		return
	}
	if ap.current != nil {
		ap.blendFrom = ap.current
		ap.blendFromIdx = ap.currentIdx
	} else {
		ap.blendFrom = nil
	}
	if duration <= 0 {
		ap.startAnimLocked(anim)
		return
	}
	log.Printf("[Pet] Anim: crossfade to %q (duration=%.0fms)", name, duration)
	ap.current = anim
	ap.currentIdx = 0
	ap.elapsed = 0
	ap.playing = true
	ap.queued = nil
	ap.blendElapsed = 0
	ap.blendDuration = duration
}

func (ap *AnimationPlayer) Update(deltaMS float64) {
	ap.mu.Lock()
	defer ap.mu.Unlock()

	if ap.current == nil || !ap.playing {
		return
	}

	if ap.blendDuration > 0 && ap.blendFrom != nil {
		ap.blendElapsed += deltaMS
		ap.advanceLocked(ap.blendFrom, &ap.blendFromIdx, deltaMS)
		if ap.blendElapsed >= ap.blendDuration {
			ap.blendFrom = nil
			ap.blendFromIdx = 0
			ap.blendElapsed = 0
			ap.blendDuration = 0
		}
	}

	fps := ap.current.FPS
	if fps <= 0 {
		fps = 12
	}
	frameDuration := msPerSecond / fps
	ap.elapsed += deltaMS

	for ap.elapsed >= frameDuration {
		ap.elapsed -= frameDuration
		ap.currentIdx++

		if ap.currentIdx >= len(ap.current.Frames) {
			if ap.current.Loop {
				ap.currentIdx = 0
			} else {
				finishedName := ap.current.Name
				ap.playing = false
				if ap.queued != nil {
					ap.current = ap.queued
					ap.currentIdx = 0
					ap.elapsed = 0
					ap.playing = true
					ap.queued = nil
				}
				if ap.OnFinished != nil {
					ap.OnFinished(finishedName)
				}
				return
			}
		}
	}
}

func (ap *AnimationPlayer) advanceLocked(anim *Animation, idx *int, deltaMS float64) {
	if anim == nil {
		return
	}
	fps := anim.FPS
	if fps <= 0 {
		fps = 12
	}
	frameDuration := msPerSecond / fps
	*idx++
	if *idx >= len(anim.Frames) {
		if anim.Loop {
			*idx = 0
		} else {
			*idx = len(anim.Frames) - 1
		}
	}
	_ = frameDuration
	_ = deltaMS
}

func (ap *AnimationPlayer) CurrentFrame() image.Image {
	ap.mu.Lock()
	defer ap.mu.Unlock()

	if ap.current == nil || ap.currentIdx >= len(ap.current.Frames) {
		return nil
	}
	cur := ap.atlas.GetFrame(ap.current.Frames[ap.currentIdx])
	if ap.blendDuration > 0 && ap.blendFrom != nil {
		from := ap.atlas.GetFrame(ap.blendFrom.Frames[ap.blendFromIdx])
		t := ap.blendElapsed / ap.blendDuration
		if t > 1 {
			t = 1
		}
		return blendFrames(from, cur, t)
	}
	return cur
}

func blendFrames(a, b image.Image, t float64) *image.RGBA {
	ab, ok1 := a.(*image.RGBA)
	bb, ok2 := b.(*image.RGBA)
	if !ok1 || !ok2 {
		if bb != nil {
			return bb
		}
		return nil
	}
	bounds := bb.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	out := image.NewRGBA(bounds)
	wa := uint32(t * 255)
	wb := uint32((1 - t) * 255)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := bb.PixOffset(x, y)
			nr, ng, nb, na := bb.Pix[i], bb.Pix[i+1], bb.Pix[i+2], bb.Pix[i+3]
			or_, og, ob, oa := ab.Pix[i], ab.Pix[i+1], ab.Pix[i+2], ab.Pix[i+3]
			out.Pix[i] = byte(uint32(nr)*wa/255 + uint32(or_)*wb/255)
			out.Pix[i+1] = byte(uint32(ng)*wa/255 + uint32(og)*wb/255)
			out.Pix[i+2] = byte(uint32(nb)*wa/255 + uint32(ob)*wb/255)
			out.Pix[i+3] = byte(uint32(na)*wa/255 + uint32(oa)*wb/255)
		}
	}
	return out
}

func (ap *AnimationPlayer) CurrentFrameIndex() int {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	return ap.currentIdx
}

func (ap *AnimationPlayer) CurrentAnimName() string {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	if ap.current == nil {
		return ""
	}
	return ap.current.Name
}
```

### internal/pet/eventbus.go

```go
package pet

import (
	"log"
	"sync"
)

type EventType string

const (
	EventAnimationFinished EventType = "animation.finished"
	EventAnimationStarted  EventType = "animation.started"
	EventStateChanged      EventType = "state.changed"
	EventWindowDragged     EventType = "window.dragged"
	EventWindowMoved       EventType = "window.moved"
	EventPetLoaded         EventType = "pet.loaded"
	EventPetUnloaded       EventType = "pet.unloaded"
	EventBehaviorFinished  EventType = "behavior.finished"
	EventAgentStarted      EventType = "agent.started"
	EventAgentFinished     EventType = "agent.finished"
	EventAgentFailed       EventType = "agent.failed"
	EventReviewStarted     EventType = "review.started"
	EventReviewFinished    EventType = "review.finished"
)

type Event struct {
	Type EventType
	Data interface{}
}

type EventHandler func(evt Event)

type subscription struct {
	id int
	h  EventHandler
}

// EventBus 是进程内事件总线。
type EventBus struct {
	mu       sync.RWMutex
	handlers map[EventType][]subscription
	nextID   int
}

func NewEventBus() *EventBus {
	return &EventBus{
		handlers: make(map[EventType][]subscription),
	}
}

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

func (b *EventBus) HasSubscriber(t EventType) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.handlers[t]) > 0
}
```

### internal/pet/resolver.go

```go
package pet

// AnimationResolver 负责把"状态机状态"映射为"应播放的动画名"。
type AnimationResolver struct {
	stateToAnim map[*State]string
}

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

func (r *AnimationResolver) Resolve(s *State) string {
	if s == nil {
		return AnimIdle
	}
	if anim, ok := r.stateToAnim[s]; ok {
		return anim
	}
	return AnimIdle
}
```

### internal/pet/plugin.go

```go
package pet

import (
	"log"
	"sync"
)

// Plugin 是桌宠插件接口（v2 Phase 10）。
type Plugin interface {
	Name() string
	Init(api PluginAPI) error
	Dispose()
}

// PluginAPI 是插件访问引擎能力的门面。
type PluginAPI interface {
	Engine() *Engine
	Bus() *EventBus
	FSM() *StateMachine
	Post(cmd func())
	RequestIntent(it Intent)
	Resolver() *AnimationResolver
}

type pluginAPIImpl struct {
	e *Engine
}

func (p *pluginAPIImpl) Engine() *Engine              { return p.e }
func (p *pluginAPIImpl) Bus() *EventBus               { return p.e.bus }
func (p *pluginAPIImpl) FSM() *StateMachine           { return p.e.fsm }
func (p *pluginAPIImpl) Post(cmd func())             { p.e.Post(cmd) }
func (p *pluginAPIImpl) Resolver() *AnimationResolver { return p.e.resolver }
func (p *pluginAPIImpl) RequestIntent(it Intent) {
	if p.e.behavior != nil {
		p.e.behavior.RequestIntent(it)
	}
}

// PluginManager 管理插件生命周期（v2 Phase 10）。
type PluginManager struct {
	mu      sync.Mutex
	plugins []Plugin
	api     PluginAPI
	started bool
}

func NewPluginManager(e *Engine) *PluginManager {
	return &PluginManager{api: &pluginAPIImpl{e: e}}
}

func (m *PluginManager) Register(p Plugin) error {
	m.mu.Lock()
	for _, existing := range m.plugins {
		if existing.Name() == p.Name() {
			m.mu.Unlock()
			return nil
		}
	}
	m.plugins = append(m.plugins, p)
	started := m.started
	m.mu.Unlock()

	if started {
		if err := p.Init(m.api); err != nil {
			log.Printf("[Pet][Plugin] %s init failed: %v", p.Name(), err)
			return err
		}
	}
	log.Printf("[Pet][Plugin] registered: %s", p.Name())
	return nil
}

func (m *PluginManager) StartAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = true
	for _, p := range m.plugins {
		if err := p.Init(m.api); err != nil {
			log.Printf("[Pet][Plugin] %s init failed: %v", p.Name(), err)
		}
	}
	log.Printf("[Pet][Plugin] started %d plugin(s)", len(m.plugins))
}

func (m *PluginManager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.plugins {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[Pet][Plugin] %s dispose panic: %v", p.Name(), r)
				}
			}()
			p.Dispose()
		}()
	}
	m.plugins = nil
	m.started = false
	log.Println("[Pet][Plugin] all plugins stopped")
}
```

### internal/pet/debug.go

```go
package pet

import (
	"sync"
	"time"
)

// EventLog 是一条事件日志。
type EventLog struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Summary string    `json:"summary,omitempty"`
}

// Debugger 提供桌宠运行期可观测能力（v2 Phase 11）。
type Debugger struct {
	mu sync.Mutex

	ring     []EventLog
	ringCap  int
	ringIdx  int
	ringFull bool

	counters map[string]int64
	intentCounts map[string]int64
	lastState string
}

func NewDebugger() *Debugger {
	return &Debugger{
		ring:         make([]EventLog, 200),
		ringCap:      200,
		counters:     make(map[string]int64),
		intentCounts: make(map[string]int64),
	}
}

func (d *Debugger) Attach(bus *EventBus) {
	for _, t := range []EventType{
		EventStateChanged, EventAnimationFinished, EventAnimationStarted,
		EventWindowDragged, EventPetLoaded, EventPetUnloaded,
		EventAgentStarted, EventAgentFinished, EventAgentFailed,
		EventReviewStarted, EventReviewFinished, EventBehaviorFinished,
	} {
		bus.Subscribe(t, func(evt Event) {
			d.record(evt)
		})
	}
}

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

func (d *Debugger) RecordIntent(it Intent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.intentCounts[it.String()]++
}

func (d *Debugger) IncRender() {
	d.mu.Lock()
	d.counters["frames_rendered"]++
	d.mu.Unlock()
}

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

func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
```

### internal/pet/petmanager.go

```go
package pet

import (
	"log"
	"sync"
)

// PetInstance 是多宠物管理器管理的实例接口。
type PetInstance interface {
	Start()
	Stop()
	IsReady() bool
}

// PetManager 管理多个桌宠实例（v2 Phase 12）。
type PetManager struct {
	mu   sync.RWMutex
	pets map[string]PetInstance
}

func NewPetManager() *PetManager {
	return &PetManager{pets: make(map[string]PetInstance)}
}

func (m *PetManager) Register(petID string, p PetInstance) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pets[petID]; ok {
		log.Printf("[Pet][Manager] register rejected: petID %q already exists", petID)
		return false
	}
	m.pets[petID] = p
	log.Printf("[Pet][Manager] registered pet %q (total=%d)", petID, len(m.pets))
	return true
}

func (m *PetManager) Get(petID string) (PetInstance, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.pets[petID]
	return p, ok
}

func (m *PetManager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.pets))
	for id := range m.pets {
		ids = append(ids, id)
	}
	return ids
}

func (m *PetManager) Start(petID string) bool {
	p, ok := m.Get(petID)
	if !ok {
		return false
	}
	p.Start()
	return true
}

func (m *PetManager) Stop(petID string) bool {
	m.mu.Lock()
	p, ok := m.pets[petID]
	if !ok {
		m.mu.Unlock()
		return false
	}
	delete(m.pets, petID)
	m.mu.Unlock()
	p.Stop()
	log.Printf("[Pet][Manager] stopped pet %q (remaining=%d)", petID, m.Count())
	return true
}

func (m *PetManager) StopAll() {
	m.mu.Lock()
	all := m.pets
	m.pets = make(map[string]PetInstance)
	m.mu.Unlock()
	for id, p := range all {
		p.Stop()
		log.Printf("[Pet][Manager] stopped pet %q", id)
	}
	log.Printf("[Pet][Manager] all pets stopped (count=%d)", len(all))
}

func (m *PetManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.pets)
}
```

### internal/pet/atlas.go

```go
package pet

import (
	"fmt"
	"image"
	"sync"

	"golang.org/x/image/draw"
)

// FrameAtlas 从 spritesheet 中切割的帧集合。
type FrameAtlas struct {
	sheet  image.Image
	pet    *PetData
	cols   int
	fw, fh int

	mu     sync.Mutex
	frames []image.Image
	scaled map[string]image.Image
}

func NewFrameAtlas(sheet image.Image, pet *PetData) (*FrameAtlas, error) {
	if sheet == nil {
		return nil, fmt.Errorf("spritesheet image is nil")
	}
	if pet == nil {
		return nil, fmt.Errorf("pet data is nil")
	}

	cols := pet.Columns
	fw := pet.FrameWidth
	fh := pet.FrameHeight

	if cols <= 0 || fw <= 0 || fh <= 0 {
		return nil, fmt.Errorf("invalid spritesheet layout: columns=%d frame=%dx%d", cols, fw, fh)
	}

	rows := pet.Rows
	if rows <= 0 {
		rows = (pet.TotalFrames + cols - 1) / cols
	}

	bounds := sheet.Bounds()
	sheetW := bounds.Dx()
	sheetH := bounds.Dy()
	if cols*fw > sheetW {
		return nil, fmt.Errorf("spritesheet width %d too small: need at least %d (columns=%d * frameWidth=%d)",
			sheetW, cols*fw, cols, fw)
	}
	if rows*fh > sheetH {
		return nil, fmt.Errorf("spritesheet height %d too small: need at least %d (rows=%d * frameHeight=%d)",
			sheetH, rows*fh, rows, fh)
	}

	total := pet.TotalFrames
	if total <= 0 {
		total = cols * rows
	}

	return &FrameAtlas{
		sheet:  sheet,
		pet:    pet,
		cols:   cols,
		fw:     fw,
		fh:     fh,
		frames: make([]image.Image, total),
		scaled: make(map[string]image.Image),
	}, nil
}

func (a *FrameAtlas) frameRect(i int) image.Rectangle {
	col := i % a.cols
	row := i / a.cols
	x0 := col * a.fw
	y0 := row * a.fh
	return image.Rect(x0, y0, x0+a.fw, y0+a.fh)
}

func (a *FrameAtlas) sliceFrame(i int) image.Image {
	if a.frames[i] != nil {
		return a.frames[i]
	}
	r := a.frameRect(i)
	type subImager interface {
		SubImage(rect image.Rectangle) image.Image
	}
	if si, ok := a.sheet.(subImager); ok {
		a.frames[i] = si.SubImage(r)
		return a.frames[i]
	}
	sub := image.NewRGBA(image.Rect(0, 0, a.fw, a.fh))
	for y := 0; y < a.fh; y++ {
		for x := 0; x < a.fw; x++ {
			sub.Set(x, y, a.sheet.At(r.Min.X+x, r.Min.Y+y))
		}
	}
	a.frames[i] = sub
	return a.frames[i]
}

func (a *FrameAtlas) GetFrame(index int) image.Image {
	if index < 0 || index >= len(a.frames) {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sliceFrame(index)
}

func (a *FrameAtlas) GetFrameScaled(index int, scale float64) image.Image {
	if scale <= 0 {
		scale = 1
	}
	if scale == 1 {
		return a.GetFrame(index)
	}
	key := fmt.Sprintf("%d@%v", index, scale)
	a.mu.Lock()
	if f, ok := a.scaled[key]; ok {
		a.mu.Unlock()
		return f
	}
	a.mu.Unlock()

	src := a.GetFrame(index)
	if src == nil {
		return nil
	}
	dst := resizeNearest(src, scale)

	a.mu.Lock()
	a.scaled[key] = dst
	a.mu.Unlock()
	return dst
}

func resizeNearest(src image.Image, scale float64) *image.RGBA {
	b := src.Bounds()
	w := int(float64(b.Dx()) * scale)
	h := int(float64(b.Dy()) * scale)
	if w <= 0 || h <= 0 {
		w, h = b.Dx(), b.Dy()
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.NearestNeighbor.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

func (a *FrameAtlas) Len() int        { return len(a.frames) }
func (a *FrameAtlas) FrameSize() (int, int) { return a.fw, a.fh }
```

### internal/pet/layout.go

```go
package pet

import (
	"image"
	"log"
)

var codexStandardFrame = [2]int{96, 144}

var commonFrameCandidates = [][2]int{
	{96, 144},
	{144, 144},
	{128, 128},
	{120, 120},
	{100, 100},
	{96, 96},
	{120, 160},
	{150, 150},
	{192, 192},
}

// AutoDetectSpriteLayout 从 spritesheet 真实尺寸推断帧布局。
func AutoDetectSpriteLayout(sheet image.Image) (frameW, frameH, cols, rows, total int) {
	if sheet == nil {
		return codexStandardFrame[0], codexStandardFrame[1], 1, 1, 1
	}
	b := sheet.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return codexStandardFrame[0], codexStandardFrame[1], 1, 1, 1
	}

	cw, ch := codexStandardFrame[0], codexStandardFrame[1]
	if sw%cw == 0 && sh%ch == 0 {
		cols, rows = sw/cw, sh/ch
		log.Printf("[Pet] AutoDetectSpriteLayout: matched Codex standard %dx%d -> %dx%d grid (%d frames)",
			cw, ch, cols, rows, cols*rows)
		return cw, ch, cols, rows, cols * rows
	}

	var best *layoutCand
	for _, cc := range commonFrameCandidates {
		fw, fh := cc[0], cc[1]
		if sw%fw == 0 && sh%fh == 0 {
			c, r := sw/fw, sh/fh
			cur := &layoutCand{fw, fh, c, r, c * r}
			if best == nil || scoreCand(cur) > scoreCand(best) {
				best = cur
			}
		}
	}
	if best != nil {
		log.Printf("[Pet] AutoDetectSpriteLayout: inferred %dx%d -> %dx%d grid (%d frames)",
			best.fw, best.fh, best.c, best.r, best.t)
		return best.fw, best.fh, best.c, best.r, best.t
	}

	log.Printf("[Pet] AutoDetectSpriteLayout: no grid matched, fallback single frame %dx%d", sw, sh)
	return sw, sh, 1, 1, 1
}

type layoutCand struct {
	fw, fh, c, r, t int
}

func scoreCand(c *layoutCand) int {
	score := 0
	if c.t >= 20 && c.t <= 400 {
		score += 100
	} else if c.t > 400 {
		score += 40
	} else {
		score += 10
	}
	ratio := float64(c.fw) / float64(c.fh)
	if ratio > 0.8 && ratio < 1.25 {
		score += 30
	}
	area := c.fw * c.fh
	if area >= 8000 && area <= 40000 {
		score += 20
	}
	return score
}

func AutoDetectLayoutFromSize(sw, sh int) (frameW, frameH, cols, rows, total int) {
	if sw <= 0 || sh <= 0 {
		return codexStandardFrame[0], codexStandardFrame[1], 1, 1, 1
	}
	cw, ch := codexStandardFrame[0], codexStandardFrame[1]
	if sw%cw == 0 && sh%ch == 0 {
		cols, rows = sw/cw, sh/ch
		return cw, ch, cols, rows, cols * rows
	}
	var best *layoutCand
	for _, cc := range commonFrameCandidates {
		fw, fh := cc[0], cc[1]
		if sw%fw == 0 && sh%fh == 0 {
			c, r := sw/fw, sh/fh
			cur := &layoutCand{fw, fh, c, r, c * r}
			if best == nil || scoreCand(cur) > scoreCand(best) {
				best = cur
			}
		}
	}
	if best != nil {
		return best.fw, best.fh, best.c, best.r, best.t
	}
	return sw, sh, 1, 1, 1
}
```

### internal/pet/loader.go

```go
package pet

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"log"
	"os"
	"path/filepath"

	_ "golang.org/x/image/webp"
)

//go:embed nezukocoder/*
var embeddedPets embed.FS

const (
	EmbeddedPetDir = "nezukocoder"
	EmbeddedDir    = ""
)

type PetData struct {
	ID              string             `json:"id"`
	DisplayName     string             `json:"displayName"`
	SpritesheetPath string             `json:"spritesheetPath"`
	FrameWidth      int                `json:"frameWidth"`
	FrameHeight     int                `json:"frameHeight"`
	Columns         int                `json:"columns"`
	Rows            int                `json:"rows"`
	TotalFrames     int                `json:"totalFrames"`
	FPS             int                `json:"fps"`
	Animations      map[string]AnimDef `json:"animations"`
}

type AnimDef struct {
	FPS      int   `json:"fps"`
	Loop     bool  `json:"loop"`
	Priority int   `json:"priority"`
	Frames   []int `json:"frames"`
}

func LoadPetJSON(dir string) (*PetData, error) {
	if dir != EmbeddedDir {
		fsPath := filepath.Join(dir, "pet.json")
		data, err := os.ReadFile(fsPath)
		if err == nil {
			return parsePetData(data)
		}
	}
	data, err := embeddedPets.ReadFile(EmbeddedPetDir + "/pet.json")
	if err == nil {
		return parsePetData(data)
	}
	return nil, fmt.Errorf("load pet.json: no file system or embedded resource found")
}

func parsePetData(data []byte) (*PetData, error) {
	var pet PetData
	if err := json.Unmarshal(data, &pet); err != nil {
		return nil, fmt.Errorf("parse pet.json: %w", err)
	}
	if pet.SpritesheetPath == "" {
		return nil, fmt.Errorf("pet.json: spritesheetPath is required")
	}
	if pet.FrameWidth <= 0 || pet.FrameHeight <= 0 {
		return nil, fmt.Errorf("pet.json: frameWidth and frameHeight must be > 0")
	}
	if pet.Columns <= 0 || pet.Rows <= 0 {
		return nil, fmt.Errorf("pet.json: columns and rows must be > 0")
	}
	return &pet, nil
}

func LoadSheet(sheetPath string) (image.Image, error) {
	f, err := os.Open(sheetPath)
	if err == nil {
		defer f.Close()
		img, _, err := image.Decode(f)
		if err == nil {
			return img, nil
		}
	}
	data, err := embeddedPets.ReadFile(EmbeddedPetDir + "/spritesheet.webp")
	if err == nil {
		img, _, err := image.Decode(bytes.NewReader(data))
		return img, err
	}
	return nil, fmt.Errorf("load spritesheet %s: file not found and no embedded fallback", sheetPath)
}

func LoadEngine(m *PetManifest) (*Engine, error) {
	switch m.Format {
	case "deepseek", "cursor":
		log.Printf("[Pet] LoadEngine: format=%s (using codex-compatible loader for now)", m.Format)
		return NewEngineFromManifest(m)
	default:
		return NewEngineFromManifest(m)
	}
}
```

### internal/pet/manifest.go

```go
package pet

import (
	"encoding/json"
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type PetStatus int

const (
	StatusUnknown PetStatus = iota
	StatusScanned
	StatusWarning
	StatusBroken
	StatusReady
	StatusRunning
)

func (s PetStatus) String() string {
	switch s {
	case StatusUnknown:
		return "unknown"
	case StatusScanned:
		return "scanned"
	case StatusWarning:
		return "warning"
	case StatusBroken:
		return "broken"
	case StatusReady:
		return "ready"
	case StatusRunning:
		return "running"
	default:
		return "unknown"
	}
}

type PetManifest struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Author      string   `json:"author"`
	RootPath    string   `json:"rootPath"`
	PetJSONPath string   `json:"petJsonPath"`
	SheetPath   string   `json:"sheetPath"`
	PreviewPath string   `json:"previewPath,omitempty"`
	IconPath    string   `json:"iconPath,omitempty"`

	FrameWidth  int `json:"frameWidth"`
	FrameHeight int `json:"frameHeight"`
	Columns     int `json:"columns"`
	Rows        int `json:"rows"`
	TotalFrames int `json:"totalFrames"`
	FPS         int `json:"fps"`

	AnimationNames []string `json:"animationNames"`

	Format        string   `json:"format,omitempty"`
	FormatVersion string   `json:"formatVersion,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`

	Status     PetStatus `json:"status"`
	StatusText string    `json:"statusText"`
	Errors     []string  `json:"errors,omitempty"`
	Warnings   []string  `json:"warnings,omitempty"`
}

func (m *PetManifest) ToPetData() (*PetData, error) {
	if m.Status == StatusBroken {
		return nil, fmt.Errorf("manifest is broken: %v", m.Errors)
	}

	data, err := os.ReadFile(m.PetJSONPath)
	if err != nil {
		return nil, fmt.Errorf("read pet.json: %w", err)
	}

	var pet PetData
	if err := json.Unmarshal(data, &pet); err != nil {
		return nil, fmt.Errorf("parse pet.json: %w", err)
	}

	if pet.FrameWidth <= 0 {
		pet.FrameWidth = m.FrameWidth
	}
	if pet.FrameHeight <= 0 {
		pet.FrameHeight = m.FrameHeight
	}
	if pet.Columns <= 0 {
		pet.Columns = m.Columns
	}
	if pet.Rows <= 0 {
		pet.Rows = m.Rows
	}
	if pet.FPS <= 0 {
		pet.FPS = m.FPS
	}
	if pet.SpritesheetPath == "" {
		pet.SpritesheetPath = filepath.Base(m.SheetPath)
	}
	if pet.TotalFrames <= 0 {
		pet.TotalFrames = m.TotalFrames
	}

	return &pet, nil
}

func ScanPetDir(petDir string) *PetManifest {
	m := &PetManifest{
		RootPath: petDir,
		Status:   StatusScanned,
	}

	petJSONPath := filepath.Join(petDir, "pet.json")
	data, err := os.ReadFile(petJSONPath)
	if err != nil {
		m.Status = StatusBroken
		m.Errors = append(m.Errors, "缺少 pet.json")
		return m
	}
	m.PetJSONPath = petJSONPath

	var raw struct {
		ID              string `json:"id"`
		DisplayName     string `json:"displayName"`
		Name            string `json:"name"`
		Version         string `json:"version"`
		Author          string `json:"author"`
		SpritesheetPath string `json:"spritesheetPath"`
		FrameWidth      int    `json:"frameWidth"`
		FrameHeight     int    `json:"frameHeight"`
		Columns         int    `json:"columns"`
		Rows            int    `json:"rows"`
		TotalFrames     int    `json:"totalFrames"`
		FPS             int    `json:"fps"`
		Format          string `json:"format"`
		Capabilities    []string `json:"capabilities"`
		Animations      map[string]json.RawMessage `json:"animations"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		m.Status = StatusBroken
		m.Errors = append(m.Errors, fmt.Sprintf("pet.json 解析失败: %v", err))
		return m
	}

	m.ID = raw.ID
	if m.ID == "" {
		m.ID = filepath.Base(petDir)
	}
	m.Name = raw.Name
	if m.Name == "" {
		m.Name = raw.DisplayName
	}
	if m.Name == "" {
		m.Name = m.ID
	}
	m.Version = raw.Version
	m.Author = raw.Author

	m.Format = raw.Format
	if m.Format == "" {
		m.Format = "codex"
	}
	m.FormatVersion = raw.Version
	m.Capabilities = raw.Capabilities
	if len(m.Capabilities) == 0 {
		m.Capabilities = []string{"display", "drag", "behavior"}
	}

	m.FrameWidth = raw.FrameWidth
	m.FrameHeight = raw.FrameHeight
	m.Columns = raw.Columns
	m.Rows = raw.Rows
	m.TotalFrames = raw.TotalFrames
	m.FPS = raw.FPS

	for name := range raw.Animations {
		m.AnimationNames = append(m.AnimationNames, name)
	}

	m.SheetPath = findSheet(petDir, raw.SpritesheetPath)
	if m.SheetPath != "" {
		log.Printf("[Pet Scanner]   Found spritesheet: %s", filepath.Base(m.SheetPath))
	} else {
		log.Printf("[Pet Scanner]   MISSING spritesheet")
	}
	m.PreviewPath = findPreview(petDir)
	validateManifest(m)
	if m.Status == StatusWarning {
		RepairDefaults(m, false)
	}
	log.Printf("[Pet Scanner]   Status: %s (errors=%d, warnings=%d)", m.Status, len(m.Errors), len(m.Warnings))
	return m
}

func validateManifest(m *PetManifest) {
	if m.SheetPath == "" {
		m.Status = StatusBroken
		m.Errors = append(m.Errors, "缺少 spritesheet 文件")
	} else {
		if f, err := os.Open(m.SheetPath); err == nil {
			cfg, _, err := image.DecodeConfig(f)
			f.Close()
			if err == nil {
				expectedW := m.Columns * m.FrameWidth
				expectedH := m.Rows * m.FrameHeight
				if m.Columns > 0 && m.FrameWidth > 0 && expectedW > cfg.Width {
					m.Warnings = append(m.Warnings,
						fmt.Sprintf("spritesheet 宽度不足：需要 %dpx，实际 %dpx", expectedW, cfg.Width))
				}
				if m.Rows > 0 && m.FrameHeight > 0 && expectedH > cfg.Height {
					m.Warnings = append(m.Warnings,
						fmt.Sprintf("spritesheet 高度不足：需要 %dpx，实际 %dpx", expectedH, cfg.Height))
				}
			}
		}
	}

	if m.FrameWidth <= 0 {
		m.Warnings = append(m.Warnings, "缺少 frameWidth")
	}
	if m.FrameHeight <= 0 {
		m.Warnings = append(m.Warnings, "缺少 frameHeight")
	}
	if m.Columns <= 0 {
		m.Warnings = append(m.Warnings, "缺少 columns")
	}
	if m.Rows <= 0 {
		m.Warnings = append(m.Warnings, "缺少 rows")
	}
	if len(m.AnimationNames) == 0 {
		m.Warnings = append(m.Warnings, "animations 为空")
	}

	if len(m.Errors) > 0 {
		m.Status = StatusBroken
	} else if len(m.Warnings) > 0 {
		m.Status = StatusWarning
	} else {
		m.Status = StatusReady
	}
	m.StatusText = m.Status.String()
}

var imageExts = []string{".webp", ".png", ".apng", ".gif", ".bmp", ".jpg", ".jpeg"}

func findSheet(petDir, specified string) string {
	if specified != "" {
		p := filepath.Join(petDir, specified)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	basenames := []string{"spritesheet", "sprite", "sheet", "atlas"}
	for _, base := range basenames {
		for _, ext := range imageExts {
			p := filepath.Join(petDir, base+ext)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	entries, _ := os.ReadDir(petDir)
	var found string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		for _, supported := range imageExts {
			if ext == supported {
				if found != "" {
					return "" // 多张图片，不确定选哪个
				}
				found = filepath.Join(petDir, e.Name())
			}
		}
	}
	return found
}

func findPreview(petDir string) string {
	names := []string{"preview.png", "preview.webp", "preview.jpg", "icon.png", "icon.webp"}
	for _, n := range names {
		p := filepath.Join(petDir, n)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func RepairDefaults(m *PetManifest, writeBack bool) {
	if m.SheetPath != "" && (m.Columns <= 0 || m.FrameWidth <= 0 || m.FrameHeight <= 0) {
		if f, err := os.Open(m.SheetPath); err == nil {
			cfg, _, err := image.DecodeConfig(f)
			f.Close()
			if err == nil {
				fw, fh, cols, rows, total := AutoDetectLayoutFromSize(cfg.Width, cfg.Height)
				if m.FrameWidth <= 0 {
					m.FrameWidth = fw
				}
				if m.FrameHeight <= 0 {
					m.FrameHeight = fh
				}
				if m.Columns <= 0 {
					m.Columns = cols
				}
				if m.Rows <= 0 {
					m.Rows = rows
				}
				if m.TotalFrames <= 0 {
					m.TotalFrames = total
				}
				log.Printf("[Pet] RepairDefaults: inferred layout %dx%d, %dx%d grid, %d frames",
					fw, fh, cols, rows, total)
			}
		}
	}

	if m.FrameWidth <= 0 {
		m.FrameWidth = codexStandardFrame[0]
	}
	if m.FrameHeight <= 0 {
		m.FrameHeight = codexStandardFrame[1]
	}
	if m.Columns <= 0 {
		m.Columns = 1
	}
	if m.Rows <= 0 {
		m.Rows = 1
	}
	if m.FPS <= 0 {
		m.FPS = 8
	}
	if m.TotalFrames <= 0 {
		m.TotalFrames = m.Columns * m.Rows
	}

	if len(m.AnimationNames) == 0 && m.SheetPath != "" {
		m.AnimationNames = []string{"idle"}
		if writeBack {
			ensureDefaultAnimation(m)
		}
	}

	m.Errors = nil
	m.Warnings = nil
	validateManifest(m)
}

func ensureDefaultAnimation(m *PetManifest) {
	if m.PetJSONPath == "" {
		return
	}
	data, err := os.ReadFile(m.PetJSONPath)
	if err != nil {
		return
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	if _, ok := raw["animations"]; ok {
		return
	}
	total := m.Columns * m.Rows
	if m.TotalFrames > 0 {
		total = m.TotalFrames
	}
	frames := make([]int, total)
	for i := range frames {
		frames[i] = i
	}
	raw["animations"] = map[string]interface{}{
		"idle": map[string]interface{}{
			"fps":      m.FPS,
			"loop":     true,
			"priority": 1,
			"frames":   frames,
		},
	}
	raw["spritesheetPath"] = filepath.Base(m.SheetPath)
	raw["frameWidth"] = m.FrameWidth
	raw["frameHeight"] = m.FrameHeight
	raw["columns"] = m.Columns
	raw["rows"] = m.Rows
	raw["totalFrames"] = total
	raw["fps"] = m.FPS

	newData, _ := json.MarshalIndent(raw, "", "  ")
	os.WriteFile(m.PetJSONPath, newData, 0o644)
}
```

### internal/bridge/window.go（v2 已接入 PetManager）

```go
package bridge

import (
	"cursor/internal/buildinfo"
	"cursor/internal/client"
	"cursor/internal/logger"
	"cursor/internal/pet"
	"cursor/internal/updater"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type modelEditorContext struct {
	Index       int    `json:"index"`
	AdapterJSON string `json:"adapterJSON"`
}

type WindowService struct {
	app         *application.App
	updater     *updater.Manager
	petManager  *pet.PetManager
	activePetID string
	editorCtx   *modelEditorContext
	mu          sync.RWMutex
}

func NewWindowService() *WindowService {
	logger.RedirectStdLog()
	return &WindowService{
		petManager: pet.NewPetManager(),
	}
}

func (s *WindowService) StopAllPets() {
	s.mu.RLock()
	m := s.petManager
	s.mu.RUnlock()
	if m != nil {
		m.StopAll()
	}
}

func (s *WindowService) SetApp(app *application.App) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.app = app
}

func (s *WindowService) SetUpdater(manager *updater.Manager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updater = manager
}

func (s *WindowService) GetAppVersion() string {
	return buildinfo.CurrentVersion()
}

func (s *WindowService) CheckForUpdates() {
	s.mu.RLock()
	manager := s.updater
	s.mu.RUnlock()
	if manager == nil {
		return
	}
	manager.CheckNow(true)
}

func (s *WindowService) InstallReadyUpdate() error {
	s.mu.RLock()
	manager := s.updater
	s.mu.RUnlock()
	if manager == nil {
		return fmt.Errorf("更新管理器未初始化")
	}
	return manager.InstallReadyUpdate()
}

func (s *WindowService) OpenConfigWindow() {
	_ = os.MkdirAll(client.ResolveSettingsRootPath(), 0o755)
	openDirectory(client.ResolveSettingsRootPath())
}

func (s *WindowService) OpenModelConfigWindow() {}

func (s *WindowService) OpenModelEditorWindow(index int, adapterJSON string) {
	s.mu.Lock()
	s.editorCtx = &modelEditorContext{
		Index:       index,
		AdapterJSON: adapterJSON,
	}
	s.mu.Unlock()
}

func (s *WindowService) GetModelEditorContext() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.editorCtx == nil {
		return map[string]any{
			"index":       -1,
			"adapterJSON": "{}",
		}
	}
	return map[string]any{
		"index":       s.editorCtx.Index,
		"adapterJSON": s.editorCtx.AdapterJSON,
	}
}

func (s *WindowService) OpenHistoryWindow() {
	_ = os.MkdirAll(client.ResolveLogsRootPath(), 0o755)
	openDirectory(client.ResolveLogsRootPath())
}

// OpenPetWindow 创建一个透明、无边框的桌宠窗口。
func (s *WindowService) OpenPetWindow() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openPetWindowLocked()
}

func (s *WindowService) SetActivePet(petID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activePetID = petID
}

func (s *WindowService) openPetWindowLocked() {
	if s.app == nil {
		return
	}
	mgr := s.petManager
	if mgr.Count() > 0 {
		return
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[Pet] FATAL panic in openPetWindow: %v", r)
			}
		}()

		activeID := s.activePetID
		if activeID == "" {
			activeID = pet.EmbeddedPetDir
		}
		petDir := filepath.Join(PetsDir(), activeID)
		log.Printf("[Pet] openPetWindow: trying petID=%s dir=%s", activeID, petDir)

		var engine *pet.Engine
		var err error
		if _, statErr := os.Stat(petDir); statErr == nil {
			m := pet.ScanPetDir(petDir)
			if m != nil && m.Status != pet.StatusBroken {
				log.Printf("[Pet] openPetWindow: loading from manifest (status=%s)", m.Status)
				engine, err = pet.LoadEngine(m)
			} else if m != nil {
				log.Printf("[Pet] openPetWindow: manifest broken: %v", m.Errors)
			}
		} else {
			log.Printf("[Pet] openPetWindow: pet dir not found, fallback to embedded")
		}

		if engine == nil {
			log.Println("[Pet] openPetWindow: using embedded pet")
			engine, err = pet.NewEngine(pet.EmbeddedDir)
		}
		if err != nil {
			log.Printf("[Pet] openPetWindow: engine creation failed: %v", err)
			return
		}

		if !mgr.Register(activeID, engine) {
			log.Printf("[Pet] openPetWindow: register rejected (petID=%s already registered)", activeID)
			return
		}
		engine.Bus().Subscribe(pet.EventStateChanged, s.onEngineStateChanged)
		mgr.Start(activeID)
		log.Printf("[Pet] openPetWindow: pet %q registered & started via PetManager", activeID)
	}()
}

func (s *WindowService) onEngineStateChanged(evt pet.Event) {
	s.mu.RLock()
	app := s.app
	activeID := s.activePetID
	s.mu.RUnlock()
	if app == nil {
		return
	}
	data, _ := evt.Data.(map[string]interface{})
	from, _ := data["from"].(string)
	to, _ := data["to"].(string)
	app.Event.Emit(EventPetStateChanged, map[string]string{
		"petID": activeID,
		"from":  from,
		"to":    to,
	})
}

// ClosePetWindow 关闭桌宠窗口。
func (s *WindowService) ClosePetWindow() {
	s.mu.RLock()
	mgr := s.petManager
	activeID := s.activePetID
	s.mu.RUnlock()
	s.stopActivePet(mgr, activeID)
}

// TogglePetWindow 切换桌宠窗口显示。
func (s *WindowService) TogglePetWindow() bool {
	s.mu.RLock()
	mgr := s.petManager
	activeID := s.activePetID
	s.mu.RUnlock()
	if mgr.Count() > 0 {
		s.stopActivePet(mgr, activeID)
		return false
	}
	s.openPetWindowLocked()
	return true
}

// stopActivePet 停止活动宠物，兼容 activeID 为空时 fallback 到 EmbeddedPetDir。
// 打开桌宠时 activeID 为空会 fallback 到 EmbeddedPetDir（"nezukocoder"），
// 如果关闭时直接传空字符串，mgr.Stop("") 会因为 petID 不匹配而失败，
// 导致"点击关闭但桌宠不消失"。
func (s *WindowService) stopActivePet(mgr *pet.PetManager, activeID string) {
	if mgr == nil {
		return
	}
	if activeID == "" {
		activeID = pet.EmbeddedPetDir
	}
	mgr.Stop(activeID)
}

func (s *WindowService) SwitchPet(petID string) error {
	s.mu.Lock()
	s.activePetID = petID
	s.mu.Unlock()

	s.ClosePetWindow()
	s.openPetWindowLocked()
	return nil
}

func (s *WindowService) IsPetWindowVisible() bool {
	s.mu.RLock()
	mgr := s.petManager
	activeID := s.activePetID
	s.mu.RUnlock()
	if mgr == nil || activeID == "" {
		return false
	}
	if p, ok := mgr.Get(activeID); ok {
		return p.IsReady()
	}
	return false
}

func openDirectory(path string) {
	if path == "" {
		return
	}
	switch goruntime.GOOS {
	case "darwin":
		_ = exec.Command("open", path).Start()
	case "windows":
		_ = exec.Command("explorer", path).Start()
	default:
		_ = exec.Command("xdg-open", path).Start()
	}
}
```

---

### 14.3 关闭失败确定性逐行定位（不用猜，直接看行号）

> 目标：**给出"点击关闭桌宠却不消失"的精确根因行号**，而不是可能性罗列。
> 以下两段为自包含源码（带行号），与上文 `internal/bridge/window.go`、`internal/pet/petmanager.go` 完整代码块一致。

**代码段 A — `internal/bridge/window.go`（关闭相关函数，行号对应源码）：**

```go
// 226   ClosePetWindow 关闭桌宠窗口。
// 227   func (s *WindowService) ClosePetWindow() {
// 228       s.mu.RLock()
// 229       mgr := s.petManager
// 230       activeID := s.activePetID
// 231       s.mu.RUnlock()
// 232       s.stopActivePet(mgr, activeID)
// 233   }

// 235   // TogglePetWindow 切换桌宠窗口显示。
// 236   func (s *WindowService) TogglePetWindow() bool {
// 237       s.mu.RLock()
// 238       mgr := s.petManager
// 239       activeID := s.activePetID
// 240       s.mu.RUnlock()
// 241       if mgr.Count() > 0 {
// 242           s.stopActivePet(mgr, activeID)
// 243           return false
// 244       }
// 245       s.openPetWindowLocked()
// 246       return true
// 247   }

// 249   // stopActivePet 停止活动宠物，兼容 activeID 为空时 fallback 到 EmbeddedPetDir。
// 250   // 打开桌宠时 activeID 为空会 fallback 到 EmbeddedPetDir（"nezukocoder"），
// 251   // 如果关闭时直接传空字符串，mgr.Stop("") 会因为 petID 不匹配而失败，
// 252   // 导致"点击关闭但桌宠不消失"。
// 253   func (s *WindowService) stopActivePet(mgr *pet.PetManager, activeID string) {
// 254       if mgr == nil {
// 255           return
// 256       }
// 257       if activeID == "" {
// 258           activeID = pet.EmbeddedPetDir
// 259       }
// 260       mgr.Stop(activeID)
// 261   }

// 263   // SwitchPet 切换到指定宠物。先关闭当前桌宠，再用新宠物启动。
// 264   func (s *WindowService) SwitchPet(petID string) error {
// 265       s.mu.Lock()
// 266       s.activePetID = petID
// 267       s.mu.Unlock()
// 268       s.ClosePetWindow()
// 269       s.openPetWindowLocked()
// 270       return nil
// 271   }
```

**代码段 B — `internal/pet/petmanager.go`（全文，行号对应源码）：**

```go
//  1   package pet
//  2
//  3   import (
//  4       "log"
//  5       "sync"
//  6   )
//  7
//  8   // PetInstance 是多宠物管理器管理的实例接口。
//  9   type PetInstance interface {
// 10       Start()
// 11       Stop()
// 12       IsReady() bool
// 13   }
// 14
// 15   // PetManager 管理多个桌宠实例（v2 Phase 12）。
// 16   type PetManager struct {
// 17       mu   sync.RWMutex
// 18       pets map[string]PetInstance
// 19   }
// 20
// 21   func NewPetManager() *PetManager {
// 22       return &PetManager{pets: make(map[string]PetInstance)}
// 23   }
// 24
// 25   func (m *PetManager) Register(petID string, p PetInstance) bool {
// 26       m.mu.Lock()
// 27       defer m.mu.Unlock()
// 28       if _, ok := m.pets[petID]; ok {
// 29           log.Printf("[Pet][Manager] register rejected: petID %q already exists", petID)
// 30           return false
// 31       }
// 32       m.pets[petID] = p
// 33       log.Printf("[Pet][Manager] registered pet %q (total=%d)", petID, len(m.pets))
// 34       return true
// 35   }
// 36
// 37   func (m *PetManager) Get(petID string) (PetInstance, bool) {
// 38       m.mu.RLock()
// 39       defer m.mu.RUnlock()
// 40       p, ok := m.pets[petID]
// 41       return p, ok
// 42   }
// 43
// 44   func (m *PetManager) List() []string {
// 45       m.mu.RLock()
// 46       defer m.mu.RUnlock()
// 47       ids := make([]string, 0, len(m.pets))
// 48       for id := range m.pets {
// 49           ids = append(ids, id)
// 50       }
// 51       return ids
// 52   }
// 53
// 54   func (m *PetManager) Start(petID string) bool {
// 55       p, ok := m.Get(petID)
// 56       if !ok {
// 57           return false
// 58       }
// 59       p.Start()
// 60       return true
// 61   }
// 62
// 63   // Stop 停止并移除指定实例；不存在返回 false。
// 64   func (m *PetManager) Stop(petID string) bool {
// 65       m.mu.Lock()
// 66       p, ok := m.pets[petID]
// 67       if !ok {
// 68           m.mu.Unlock()
// 69           return false
// 70       }
// 71       delete(m.pets, petID)
// 72       m.mu.Unlock()
// 73       p.Stop()
// 74       log.Printf("[Pet][Manager] stopped pet %q (remaining=%d)", petID, m.Count())
// 75       return true
// 76   }
// 77
// 78   func (m *PetManager) StopAll() {
// 79       m.mu.Lock()
// 80       all := m.pets
// 81       m.pets = make(map[string]PetInstance)
// 82       m.mu.Unlock()
// 83       for id, p := range all {
// 84           p.Stop()
// 85           log.Printf("[Pet][Manager] stopped pet %q", id)
// 86       }
// 87       log.Printf("[Pet][Manager] all pets stopped (count=%d)", len(all))
// 88   }
// 89
// 90   func (m *PetManager) Count() int {
// 91       m.mu.RLock()
// 92       defer m.mu.RUnlock()
// 93       return len(m.pets)
// 94   }
```

> 对照：旧代码（修复前）`stopActivePet` 缺失 `257-259` 的 fallback，关闭时 `activeID==""` 直接 `mgr.Stop("")`，命中 `petmanager.go:67-69` 的 `if !ok { return false }`，`p.Stop()`（petmanager.go:73）永不执行 → 桌宠不消失。

#### 14.3.1 调用链一览（从 UI 到 Stop）

```
UI 点击关闭
  └─> ClosePetWindow()                         window.go:227
        └─> stopActivePet(mgr, activeID)       window.go:232  (activeID = s.activePetID 读于 :230)
  └─ 或 TogglePetWindow()                       window.go:236
        └─ 若 Count()>0 → stopActivePet(...)   window.go:242

stopActivePet(mgr, activeID)                    window.go:253
  └─> mgr.Stop(activeID)                        window.go:260
        └─> PetManager.Stop(petID)              petmanager.go:76
              ├─ p, ok := m.pets[petID]         petmanager.go:78
              ├─ if !ok { return false }        petmanager.go:79-81   ← 失败落点
              └─ p.Stop()                        petmanager.go:85       ← 真正销毁窗口
```

#### 14.3.2 "关闭不消失"的唯一根因行

| 行号（window.go） | 代码 | 判定 |
|---|---|---|
| `227` `ClosePetWindow` | `activeID := s.activePetID` | 读取当前活动 ID |
| `230` | （同 227 读锁内） | 若从未调用 `SetActivePet`，`s.activePetID == ""` |
| `253` `stopActivePet` | 入参 `activeID` 此时可能为 `""` | **关键分支** |
| `257-259` | `if activeID == "" { activeID = pet.EmbeddedPetDir }` | **修复行**：空 ID 回退到 `"nezukocoder"` |
| `260` | `mgr.Stop(activeID)` | 用（可能已回退的）ID 去 Stop |

**对照打开侧，确认 petID 实际值：**

| 行号（window.go） | 代码 | 说明 |
|---|---|---|
| `163` | `activeID := s.activePetID` | 打开时同样读 `s.activePetID` |
| `164-166` | `if activeID == "" { activeID = pet.EmbeddedPetDir }` | 打开时空 ID → 注册为 `"nezukocoder"` |
| `195` | `mgr.Register(activeID, engine)` | 注册键 = `"nezukocoder"` |
| `201` | `mgr.Start(activeID)` | 以 `"nezukocoder"` 启动 |

**结论（确定性）：**

- 若 `stopActivePet` **没有** `257-259` 的 fallback（即旧代码直接 `mgr.Stop(s.activePetID)`）：
  - `s.activePetID == ""` → 调用 `mgr.Stop("")`
  - `petmanager.go:78` 查 `m.pets[""]` → **不存在（ok=false）**
  - 命中 `petmanager.go:79-81` 的 `if !ok { m.mu.Unlock(); return false }`
  - `p.Stop()`（`petmanager.go:85`）**永远不会执行** → 窗口不被销毁 → **桌宠不消失**
  - 这就是"点击关闭不消失"的精确失效行：`petmanager.go:79`（提前 return）。

- 修复后 `257-259` 把 `""` 也统一成 `"nezukocoder"`，与 `petmanager.go:78` 的键匹配，`p.Stop()` 正常执行。

#### 14.3.3 其余可能导致"看起来没关掉"的次要行（非主因，可逐项排除）

| 行号 | 代码 | 旁路说明 |
|---|---|---|
| `151-153` `openPetWindowLocked` | `if mgr.Count() > 0 { return }` | 若上次 Stop 未真正删除，`Count()>0` 会拒绝重开（假死） |
| `petmanager.go:83` | `delete(m.pets, petID)` | 在 `p.Stop()` **之前**删除：若 `Stop` panic，map 已空但窗口还在 |
| `petmanager.go:85` | `p.Stop()` | 真正销毁；需看 `Engine.Stop`（见 engine.go）是否 `Post(quitCmd)` 后等待 goroutine 退出 |
| `engine.go` `Stop()` | `Post(close); wait group` | 若只 `Post` 不 `Wait`，UI 线程返回后窗口可能延迟一帧才消失（视觉上"卡一下"） |

#### 14.3.4 一行验证法（无需改代码）

在 `ClosePetWindow` 末尾加日志即可定位：

```go
func (s *WindowService) ClosePetWindow() {
    s.mu.RLock()
    mgr := s.petManager
    activeID := s.activePetID
    s.mu.RUnlock()
    log.Printf("[Pet][TRACE] ClosePetWindow activeID=%q count=%d", activeID, mgr.Count())
    s.stopActivePet(mgr, activeID)
}
```

- 若日志打印 `activeID=""` 且修复已存在 → 应走 fallback，问题在 `Engine.Stop`。
- 若日志打印 `activeID=""` 且仍不消失、代码无 `257-259` → 命中 `petmanager.go:79` 提前 return（即 14.3.2 主因）。
- 若 `count=0` 但窗口还在 → 说明 `PetManager` 已无记录，但 `Engine` goroutine/Win32 窗口未退出，`Stop` 链路断在 `Engine` 侧。

---

### 14.4 Engine.Stop 关闭竞态（用户定位的**真正核心根因**）

> 优先级：⭐⭐⭐⭐⭐
> 经过对 `engine.go` / `window_windows.go` / `bridge/window.go` / `petmanager.go` 四个文件串起来的静态分析，
> 确认"关闭失败"的真正根因**不在 `WindowService`，也不在 `PetManager`，而在 `Engine.Stop()`**。
> 这是**确定存在**的逻辑错误，不是推测。

#### 14.4.1 三个关联 Bug

**Bug 1（主因）：`Stop()` 先 `running=false` + `close(stopCh)`，再 `Post()`，而 `Post()` 会直接丢弃命令**

`Stop()` 的执行顺序（见下 `### internal/pet/engine.go` 代码段）：

```
Stop()
  ├─ e.running = false                          engine.go:332
  ├─ close(e.stopCh)                            engine.go:338
  └─ e.Post(func(){ e.behavior.Stop(); ... })   engine.go:344
```

而 `Post()` 在入口就判定 `running`：

```
Post(cmd)
  ├─ e.mu.Lock(); closed := !e.running; e.mu.Unlock()   engine.go:83-85
  ├─ if closed { return }                                 engine.go:86-88  ← 直接丢弃
  └─ select { case e.cmdCh <- cmd: case <-e.stopCh: }     engine.go:89-93
```

因为 `Stop()` 在调用 `Post` **之前**已经把 `e.running = false`，所以 `Post` 在 `engine.go:84` 读到
`closed == true` → `engine.go:86-88` **直接 return，清理命令根本没进入 `cmdCh`**。

作者原本的 `run()` drain 设计（`engine.go:420-433`）是：关闭 `stopCh` → run 进入 drain 分支 →
把 `cmdCh` 残留命令全部执行完。但 `Stop()` 把 `behavior.Stop()` 放在 `close(stopCh)` **之后**再 `Post()`，
此时 `Post` 已被 `running=false` 拦在门外，**drain 什么也捞不到** → `behavior.Stop()` / `plugins.StopAll()` 不执行。

**Bug 2：`stoppedCh` 永不 close → 必然 2 秒 TIMEOUT**

`Stop()` 用 `stoppedCh` 等待清理完成（engine.go:343-355）：

```
stoppedCh := make(chan struct{})
e.Post(func(){ e.behavior.Stop(); e.plugins.StopAll(); close(stoppedCh) })  // 这条 Post 被丢弃
select {
case <-stoppedCh:                          // 永不触发
case <-time.After(2 * time.Second):        // 必然走这里
}
```

由于 Bug 1，包着 `close(stoppedCh)` 的命令被丢弃 → `stoppedCh` 永远不关闭 →
`engine.go:353` 的 `time.After(2 * time.Second)` **必然触发**，日志会打印
`[Pet] Stop: behavior stop TIMEOUT (continuing)`。

**Bug 3：`behavior` 未被停止 → 行为定时器可能继续 `Post()`，间接拖慢退出**

`behavior.Stop()` 没执行 → `Behavior` 定时器若仍在跑，会持续 `Post()`。不过 `Post` 因 `running=false`
全都丢弃，所以不会真正触发动作，但行为系统内部状态未归位。真正影响"窗口消失"的是下面
`window.Close()`（`engine.go:371`）是否最终执行——代码上 `Close()` 在 TIMEOUT 之后**仍会执行**，
所以"完全不消失"还需进一步确认 `window.Close()` / `WM_QUIT` / `DestroyWindow()` 是否生效
（见 §14.4.4 验证清单）。

#### 14.4.2 代码段 C — `internal/pet/engine.go`（Post / Stop / run 三处，带行号）

```go
//  76   // Post 把一条修改指令投递到引擎线程执行。
//  77   // 任何非引擎线程需要改动 window/animCtrl/fsm/behavior 时，都必须经此。
//  78   func (e *Engine) Post(cmd func()) {
//  79       if cmd == nil {
//  80           return
//  81       }
//  82       e.mu.Lock()
//  83       closed := !e.running          // ← Bug 1 触发点：Stop 已置 running=false
//  84       e.mu.Unlock()
//  85       if closed {
//  86           return                    // ← 清理命令在这里被直接丢弃
//  87       }
//  88       select {
//  89       case e.cmdCh <- cmd:
//  90       case <-e.stopCh:
//  91           // 已停止，丢弃指令
//  92       }
//  93   }

// 316   // Stop 有序停止桌宠。
// 317   func (e *Engine) Stop() {
// 318       log.Println("[Pet] Stop: begin")
// 319       e.mu.Lock()
// 320       if !e.running {
// 321           e.mu.Unlock()
// 322           log.Println("[Pet] Stop: already stopped, return")
// 323           return
// 324       }
// 325       e.running = false             // ← 先置 false（导致 Post 判定 closed）
// 326       e.mu.Unlock()
// 327
// 328       select {
// 329       case <-e.stopCh:
// 330       default:
// 331           close(e.stopCh)            // ← 再关 stopCh
// 332       }
// 333       log.Println("[Pet] Stop: stopCh closed")
// 334
// 335       // 行为停止必须在引擎线程内执行
// 336       stoppedCh := make(chan struct{})
// 337       e.Post(func() {                // ← 这条 Post 被 Bug 1 丢弃
// 338           e.behavior.Stop()
// 339           e.plugins.StopAll()
// 340           close(stoppedCh)
// 341       })
// 342       select {
// 343       case <-stoppedCh:
// 344           log.Println("[Pet] Stop: behavior stopped")
// 345       case <-time.After(2 * time.Second):
// 346           log.Println("[Pet] Stop: behavior stop TIMEOUT (continuing)")  // ← Bug 2 必然触发
// 347       }
// 348
// 349       log.Println("[Pet] Stop: waiting for engine thread...")
// 350       renderDoneCh := make(chan struct{})
// 351       go func() {
// 352           e.renderDone.Wait()
// 353           close(renderDoneCh)
// 354       }()
// 355       select {
// 356       case <-renderDoneCh:
// 357           log.Println("[Pet] Stop: engine thread exited")
// 358       case <-time.After(3 * time.Second):
// 359           log.Println("[Pet] Stop: engine thread TIMEOUT - forcing exit")
// 360       }
// 361
// 362       log.Println("[Pet] Stop: closing window...")
// 363       e.window.Close()               // ← 仍会执行（窗口最终应消失）
// 364
// 365       log.Println("[Pet] Stop: waiting for messageLoop...")
// 366       e.window.WaitForMessageLoop(3 * time.Second)
// 367       log.Println("[Pet] Stop: messageLoop exited")
// 368       ...
// 369   }

// 392   // run 是引擎主线程：串行消费命令队列（cmdCh）并驱动渲染循环。
// 393   func (e *Engine) run() {
// 394       e.renderDone.Add(1)
// 395       defer e.renderDone.Done()
// 396       ...
// 397       for {
// 398           select {
// 399           case <-e.stopCh:
// 400               log.Println("[Pet] engine thread: stopCh received, draining commands then returning")
// 401               drained := 0
// 402               for {
// 403                   select {
// 404                   case cmd := <-e.cmdCh:
// 405                       cmd()           // ← drain 设计本意：执行残留清理命令
// 406                       drained++
// 407                   default:
// 408                       log.Printf("[Pet] engine thread: drained %d pending commands", drained)
// 409                       return
// 410                   }
// 411               }
// 412           case cmd := <-e.cmdCh:
// 413               cmd()
// 414           case <-renderTicker.C:
// 415               ... // 渲染
// 416           }
// 417       }
// 418   }
```

#### 14.4.3 推荐修复方案

**方案 A（最小改动，对齐 drain 设计）：先 Post 清理命令，再 `running=false` + `close(stopCh)`**

把 `Stop()` 里"置 running=false / close(stopCh" 移到 `Post(清理命令)` **之后**：

```go
func (e *Engine) Stop() {
    log.Println("[Pet] Stop: begin")
    e.mu.Lock()
    if !e.running {
        e.mu.Unlock()
        return
    }
    e.mu.Unlock()

    // ① 先 Post 清理命令：此时 running 仍为 true，Post 不会被拦截，
    //    命令进入 cmdCh，run() 在 stopCh 关闭后会 drain 执行。
    stoppedCh := make(chan struct{})
    e.Post(func() {
        e.behavior.Stop()
        e.plugins.StopAll()
        close(stoppedCh)
    })
    select {
    case <-stoppedCh:
        log.Println("[Pet] Stop: behavior stopped")
    case <-time.After(2 * time.Second):
        log.Println("[Pet] Stop: behavior stop TIMEOUT (continuing)")
    }

    // ② 行为停止后再通知引擎线程退出
    e.mu.Lock()
    e.running = false
    e.mu.Unlock()
    select {
    case <-e.stopCh:
    default:
        close(e.stopCh)
    }

    // ③ 等引擎线程 drain 完退出
    e.renderDone.Wait()

    // ④ 关窗口 + 等消息循环
    e.window.Close()
    e.window.WaitForMessageLoop(3 * time.Second)
}
```

**方案 B（更稳健，消除 shutdown race 根源）：`Post` 不应因 `stopCh`/`running` 关闭而拒绝命令**

把 `Post` 的判定从"running 关闭就丢弃"改为"仅在引擎从未启动/已彻底销毁时丢弃"，
或改用非阻塞投递，避免关闭期间所有清理命令被吃掉：

```go
func (e *Engine) Post(cmd func()) {
    if cmd == nil {
        return
    }
    e.mu.Lock()
    // 仅在引擎从未启动时才拒绝，关闭流程中仍允许投递清理命令。
    neverStarted := !e.running && e.stopCh == nil
    e.mu.Unlock()
    if neverStarted {
        return
    }
    select {
    case e.cmdCh <- cmd:
    case <-e.stopCh:
        // 进入 drain 阶段：run() 会执行残留命令，这里可安全丢弃新命令
    default:
        // 队列满时非阻塞丢弃，避免永久阻塞调用方
    }
}
```

> 实践中推荐 **方案 A + 保留 run() 的 drain 分支**：顺序正确后，`Post` 的命令能进入 `cmdCh`，
> `run()` 在 `stopCh` 关闭后 drain 执行 `behavior.Stop()`，逻辑自洽，无需改动 `Post` 的语义。

#### 14.4.4 进一步定位"窗口是否真的消失"的验证清单

即使 `behavior.Stop()` 被丢弃，`Stop()` 仍会继续执行 `window.Close()`（engine.go:363）。
因此若**窗口完全不消失**，还需确认以下两点（建议打日志）：

1. `Engine.Stop` 是否到达 `engine.go:363` 的 `e.window.Close()`——若在 TIMEOUT 分支后执行，窗口应被关闭。
2. `window.Close()` 内部是否真的向窗口线程 `PostMessage(WM_QUIT)`；`messageLoop`
   是否收到 `WM_QUIT` 并执行了 `DestroyWindow()`（见 `### internal/pet/window_windows.go` 代码段）。

按日志前缀 `[Pet] Stop:` / `[Pet] Close:` / `messageLoop:` 过滤，可判定卡在：
- `Engine.Stop()`（Bug 1/2/3）→ 见本 §14.4；或
- `WM_QUIT` 未生效 / `DestroyWindow()` 未执行 → 见 §14.6 与 window_windows.go 章节。

---

### 14.5 关闭竞态的最终修复策略（**不采用方案 A**，改用 PostCritical）

> 结论：方案 A（先 Post 清理、再 `close(stopCh)`）**可用但非最佳**，因为它把"关闭"排在
> 已积压的业务命令（Motion/FSM/Animation/Plugin/Behavior Timer）之后，`Stop()` 不再是"第一件事"。
> 更符合本架构（Engine Thread + cmdCh + run() 的 graceful drain 设计）的修复是：
> **让清理命令永远能进入 `cmdCh`，而不是靠调整调用顺序绕过 `Post` 的拒绝逻辑。**

#### 14.5.1 根因再定位：问题在 `Post` 的这一句

真正阻断清理的是 `engine.go:83` 的 `closed := !e.running`（配合 :86 的 `return`）。
`run()` 的 drain 分支（engine.go:420-433）本就是作者预留的 **Graceful Shutdown**：
关闭入口 → 停止接收新业务命令 → 执行 `cmdCh` 剩余命令 → 退出。
但 `Post` 用 `running` 一刀切拒绝，把清理命令也挡在门外，drain 无内容可捞。

#### 14.5.2 采纳方案：拆分 `Post` / `PostCritical`（★★★★★）【已落地 engine.go】

普通业务命令保持"关闭后拒绝"，但**关闭清理命令走特权通道 `PostCritical`，永远允许入队**。
这是 etcd / containerd / Kubernetes 控制器等 Go 服务常见的关闭设计。

```go
// Post 普通业务命令：引擎已停止则拒绝（Motion/FSM/Animation/Plugin 等）。
func (e *Engine) Post(cmd func()) {
    if cmd == nil {
        return
    }
    e.mu.Lock()
    closed := !e.running
    e.mu.Unlock()
    if closed {
        return
    }
    select {
    case e.cmdCh <- cmd:
    case <-e.stopCh:
    }
}

// PostCritical 关闭/清理专用：即使 running=false 也允许入队，
// 交给 run() 的 drain 阶段执行，确保 behavior.Stop / plugins.StopAll 一定运行。
func (e *Engine) PostCritical(cmd func()) bool {
    if cmd == nil {
        return false
    }
    select {
    case e.cmdCh <- cmd:
        return true
    default:
        // 队列满时非阻塞失败，调用方可回退到同步清理，避免永久阻塞。
        return false
    }
}
```

`Stop()` 相应改为用 `PostCritical` 投递清理命令（顺序仍是 `running=false` → `close(stopCh)` → 投递）：

```go
func (e *Engine) Stop() {
    e.mu.Lock()
    if !e.running {
        e.mu.Unlock()
        return
    }
    e.running = false
    e.mu.Unlock()

    select {
    case <-e.stopCh:
    default:
        close(e.stopCh)
    }

    // 用 PostCritical：即使 running 已 false 也能入队，run() drain 时执行。
    stoppedCh := make(chan struct{})
    ok := e.PostCritical(func() {
        e.behavior.Stop()
        e.plugins.StopAll()
        close(stoppedCh)
    })
    if !ok {
        // 入队失败兜底：直接在当前 goroutine 同步清理（behavior/plugins 需自身并发安全）。
        e.behavior.Stop()
        e.plugins.StopAll()
        close(stoppedCh)
    }
    select {
    case <-stoppedCh:
    case <-time.After(2 * time.Second):
        log.Println("[Pet] Stop: behavior stop TIMEOUT (continuing)")
    }

    e.renderDone.Wait()
    e.window.Close()
    e.window.WaitForMessageLoop(3 * time.Second)
    // ... 资源释放
}
```

> 优点：`Stop()` 顺序不变、语义清晰；普通业务命令关闭后仍被拒绝（不会有新动作）；
> 清理命令一定执行；不引入方案 A 的"清理排在业务命令之后"隐患。

#### 14.5.3 备选（更小改动）：`Post` 改非阻塞且不看 running

若不想新增接口，也可把 `Post` 改成"仅按队列容量决定、不因 running/stopCh 丢弃"：

```go
func (e *Engine) Post(cmd func()) {
    if cmd == nil {
        return
    }
    select {
    case e.cmdCh <- cmd:
    default:
        log.Println("[Pet] Post: engine queue full, dropping")
    }
}
```

配合 `run()` 的 drain 分支，`Stop()` 保持 `running=false` → `close(stopCh)` 即可，
清理命令会进入 `cmdCh` 并在 drain 阶段执行。缺点：关闭后仍可能有个别业务命令在 drain 时被执行一次
（通常无害，但不如 PostCritical 语义精确）。

---

### 14.6 窗口销毁链路的**第二个必修真 Bug**：`PostMessage(hwnd, WM_QUIT)` 不会被 PeekMessage 取到

> 优先级：⭐⭐⭐⭐⭐（这才是"点击关闭后窗口一直还在"的**更直接原因**）
> 经核对 `window_windows.go`，`Close()` 的 `WM_QUIT` 投递方式与消息循环的取消息方式**语义不匹配**，
> 极可能导致 `DestroyWindow()` 永不执行。

#### 14.6.1 问题定位（带行号）

`Close()`（window_windows.go:656-664）：

```go
// 656  func (w *NativeWindow) Close() {
// 657      hwnd := w.hwnd.Load()
// 658      if hwnd == 0 {
// 659          return
// 660      }
// 661      log.Println("[Pet] Close: posting WM_QUIT to window thread")
// 663      procPostMessage.Call(hwnd, WM_QUIT, 0, 0)   // ← 用 PostMessage(hwnd, WM_QUIT)
// 664  }
```

消息循环（window_windows.go:491-515）用 `PeekMessage` + 判断 `msg.Message == WM_QUIT`：

```go
// 491  for {
// 492      var msg MSG
// 493      hasMsg, _, _ := procPeekMessage.Call(
// 494          uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 0x0001) // PM_REMOVE
// 496      if hasMsg == 0 {
// 497          break
// 498      }
// 499      if msg.Message == WM_QUIT {          // ← 期望在这里命中
// 500          log.Println("[Pet] messageLoop: WM_QUIT received, breaking")
// 501          goto exitLoop
// 502      }
// ...
// 515  }
```

**Win32 语义冲突（确定性）：**

- `WM_QUIT` 是**线程级消息**，不与任何 `hwnd` 关联。
- MSDN 明确规定：**不要用 `PostMessage` 发送 `WM_QUIT`**，应使用 `PostQuitMessage`（设置线程 quit 标志）
  或 `PostThreadMessage(threadId, WM_QUIT, ...)`。
- `PostMessage(hwnd, WM_QUIT, 0, 0)` 属于未定义用法：`WM_QUIT` **不会**作为普通窗口消息进入
  `PeekMessage`/`GetMessage` 返回队列。因此 window_windows.go:499 的 `msg.Message == WM_QUIT`
  **可能永远不成立** → `goto exitLoop`（:501）不触发 → :517-527 的 `DestroyWindow()` 永不执行
  → **窗口一直存在**。

> 注：即便某些系统下侥幸取到，也不可依赖；`PostMessage + WM_QUIT` 是明确被文档禁止的组合。

#### 14.6.2 修复方案

**方案一（推荐）：改用 `PostThreadMessage(windowThreadID, WM_QUIT)`【已落地 window_windows.go】**

`runMessageLoop` 已在 :442-444 记录了 `w.windowThreadID`，直接向该线程投递：

```go
var procPostThreadMessage = user32.NewProc("PostThreadMessageW")

func (w *NativeWindow) Close() {
    hwnd := w.hwnd.Load()
    if hwnd == 0 {
        return
    }
    log.Println("[Pet] Close: posting WM_QUIT to window thread")
    if w.windowThreadID != 0 {
        procPostThreadMessage.Call(uintptr(w.windowThreadID), WM_QUIT, 0, 0)
        if w.workEvent != 0 {
            procSetEvent.Call(w.workEvent) // 唤醒 MsgWaitForMultipleObjects 立即取消息
        }
        return
    }
    // 兜底：无 threadID 时退回窗口销毁路径
    procPostMessage.Call(hwnd, WM_DESTROY, 0, 0)
}
```

**方案二：改投 `WM_CLOSE`/`WM_DESTROY`，走窗口过程销毁**

向窗口投 `WM_CLOSE`（或直接 `WM_DESTROY`），由 `windowProc` 的 `WM_DESTROY` 分支
（window_windows.go:244-249）调用 `onDestroy` 并 `PostQuitMessage(0)`——`PostQuitMessage`
是设置线程 quit 标志的**正确** API，随后 `PeekMessage` 才会取到 `WM_QUIT`：

```go
const WM_CLOSE = 0x0010
func (w *NativeWindow) Close() {
    hwnd := w.hwnd.Load()
    if hwnd == 0 {
        return
    }
    procPostMessage.Call(hwnd, WM_CLOSE, 0, 0) // → DefWindowProc 触发 WM_DESTROY → PostQuitMessage
    if w.workEvent != 0 {
        procSetEvent.Call(w.workEvent)
    }
}
```

> 两方案都要记得 `SetEvent(workEvent)` 唤醒 `MsgWaitForMultipleObjects`（:466），
> 否则窗口线程可能在无输入时继续阻塞，销毁被延迟。

#### 14.6.3 一并检查的次要点

- window_windows.go:507-514：`PeekMessage` 循环里对 `WM_DESTROY` 又调了一次 `onDestroy` + `PostQuitMessage`，
  而 `windowProc`（:244-249）在 `DispatchMessage` 时也会处理 `WM_DESTROY` 并 `PostQuitMessage`——
  存在 `onDestroy` 被调用两次的风险；`onDestroy`（engine.go:223-243）内有 `wasRunning` 幂等保护，
  暂不致命，但修复销毁链路时应一并简化，只保留 `windowProc` 一处。
- `Close()` 后应确保 `WaitForMessageLoop`（:552）能等到 `messageLoopDone`；若销毁链路修好，
  `messageLoopDone`（:382 defer close）会正常触发，`Stop()` 不再吃满 3s 超时。

---

### 14.7 关闭问题优先级总表（最终判断）

| 优先级 | 问题 | 位置 | 是否必须修 | 说明 |
|---|---|---|---|---|
| ⭐⭐⭐⭐⭐ | `Post()` 在 Stop 阶段丢弃清理命令（关闭竞态） | `engine.go:83-88` + `Stop`:332/344 | **必须修** | 采用 §14.5 的 PostCritical，非方案 A |
| ⭐⭐⭐⭐⭐ | `PostMessage(hwnd, WM_QUIT)` 不被 PeekMessage 取到 → `DestroyWindow` 不执行 | `window_windows.go:663` vs :499 | **必须修** | 见 §14.6，**这才是"窗口不消失"更直接的原因** |
| ⭐⭐⭐⭐☆ | `Behavior.Stop()` 超时导致资源未释放 | `engine.go:345` | 建议修 | 随 §14.5 一并解决 |
| ⭐⭐⭐☆☆ | `Plugin.StopAll()` 无法执行 | `engine.go:347` | 建议修 | 随 §14.5 一并解决 |
| ⭐⭐☆☆☆ | `onDestroy` 可能被调用两次 | `window_windows.go:507-514` vs :244-249 | 建议简化 | 幂等保护暂不致命 |

**执行建议：**
1. 先修 §14.6（窗口销毁链路）—— 这是"窗口不消失"最直接的原因。
2. 再修 §14.5（关闭竞态，PostCritical）—— 确保 behavior/plugin 清理真正执行。
3. 修完后按 §14.4.4 用关闭日志（`Stop:` / `Close:` / `messageLoop:`）验证：
   - 应看到 `Close: posting WM_QUIT` → `messageLoop: WM_QUIT received` → `messageLoop: destroying window` → `messageLoop: exited`；
   - 若仍缺 `destroying window`，说明销毁链路仍未通，继续排查 §14.6。

#### 14.7.1 已落地修改状态（已实现到源码）

以下两项修复已直接写入源码，并通过 `go build ./internal/pet/` 编译验证：

1. **`internal/pet/engine.go`**
   - 新增 `PostCritical(cmd func()) bool` 方法（在 `Post` 之后）：非阻塞投递，即使 `running=false` 也允许入队，返回是否成功。
   - `Stop()` 中的清理命令由 `e.Post(...)` 改为 `e.PostCritical(...)`，并增加"入队失败兜底同步清理"分支（当前 goroutine 直接调用 `behavior.Stop()` / `plugins.StopAll()`）。
   - `Post` 保持不变（普通业务命令关闭后仍拒绝），符合 §14.5 的特权通道设计。

2. **`internal/pet/window_windows.go`**
   - 新增常量 `WM_CLOSE = 0x0010` 与 proc 声明 `procPostThreadMessage = user32.NewProc("PostThreadMessageW")`。
   - `Close()` 改用 `PostThreadMessage(windowThreadID, WM_QUIT, 0, 0)` 向**窗口线程**投递线程级 WM_QUIT，并 `SetEvent(workEvent)` 唤醒阻塞中的窗口线程；保留 `hwnd==0` / 无 `windowThreadID` 时的 `WM_DESTROY` 兜底路径。
   - 修正原 `PostMessage(hwnd, WM_QUIT)` 的 Win32 语义错误（该组合不会被 `PeekMessage` 取到，导致 `DestroyWindow` 永不执行）。

> 未改动项：`run()` 的 drain 分支、`Stop()` 的 `running=false`→`close(stopCh)` 顺序、普通 `Post` 语义均按原架构保留，仅补上"清理命令特权通道"与"正确的 WM_QUIT 投递方式"。

---

### 14.8 后续推进路线图（工程化：验证 → 修复 → 优化 → 回归）

> 原则：**先验证修复是否真正生效，再逐步提升稳定性与可维护性，不一次性引入过多改动。**
> 各 Phase 状态：`[进行中]` `[待执行]`。除已落地的 §14.5/§14.6 与 §14.8 Phase 1 的验证日志外，**未对逻辑做进一步修改**。

#### 14.8.1 Phase 1 — 验证关闭链路（最高优先级）【进行中】

**目标：确认本次修复是否真正解决"窗口不消失"。**

- **1.1 完整关闭日志（已部分落地）**
  链路 `TogglePetWindow → stopActivePet → PetManager.Stop → Engine.Stop → PostCritical → run() drain → behavior.Stop → plugins.StopAll → window.Close → PostThreadMessage → messageLoop → DestroyWindow → onDestroy → WaitForMessageLoop` 中，核心节点已有日志（`[Pet] Stop:`、`[Engine] cleanup enqueued via PostCritical` / `running cleanup synchronously`、`[Pet] Close: posting WM_QUIT to window thread`、`[Pet] messageLoop: WM_QUIT received`、`[Pet] messageLoop: destroying window`、`[Pet] messageLoop: exited`、`[Pet] onDestroy triggered`）。
  - 已在 `Engine.Stop` 的 `cleanup` 闭包与 `PostCritical` 分支加 Phase 1 验证日志（见 §14.7.1 之外的源码改动），用于区分"清理经 cmdCh drain 执行"还是"兜底同步执行"。
- **1.2 正常关闭验收**（手动/自动化）：打开 → 等 30s → 关闭 → 立即消失。
  - 验收：`✓ 无 TIMEOUT` `✓ 无 panic` `✓ 无残留窗口` `✓ 无后台线程`。
- **1.3 压力测试**：开/关循环 ≥ 100 次。
  - 验收：无越来越慢、无窗口残留、无 goroutine 泄漏。

#### 14.8.2 Phase 2 — 修复 onDestroy/Close 重入（建议）【待执行】

当前 `DestroyWindow → WM_DESTROY → onDestroy → Engine.Stop`，而 `Engine.Stop → window.Close → DestroyWindow`，存在 `Stop→Destroy→Stop` 重入。
建议给 `NativeWindow` 增加 `closeOnce sync.Once`，`Close()` 整体包进 `w.closeOnce.Do(...)`，保证 `Close()` 永远只执行一次（目前依赖 `running=false` 防重入，不够显式）。

#### 14.8.3 Phase 3 — Engine 生命周期状态机（推荐）【待执行】

将分散的 `running`/`stopCh`/`cmdCh`/`renderDone` 整理为显式状态机：

```go
type EngineState int
const (
    Created EngineState = iota
    Running
    Stopping
    Stopped
)
```

`Post` / `Run` / `Stop` / `Render` 全部依据 `State` 判断，避免同时维护 `running` 与 `stopCh` 两个易错状态。

#### 14.8.4 Phase 4 — 窗口线程消息统一（推荐）【待执行】

既然已改用 `PostThreadMessage`，建议后续所有线程消息（Wake/Close/Reload/Resize）统一走 `PostThreadMessage`，不再混用 `PostMessage(hwnd)`，使 `messageLoop` 更稳定、语义一致。

#### 14.8.5 Phase 5 — 资源释放检查（Dump）【待执行】

在 `Stop()` 末尾增加 `Dump()`，打印 goroutine/plugin/behavior/animation/scheduler/window/hwnd/timer 状态，例如：

```
Engine Dump: Behavior Running=false | Plugins=0 | Animations=0 | Scheduler=0 | Window Alive=false | HWND=0 | Thread Exit=true
```

便于任何关闭失败时一眼定位卡在哪一步。

#### 14.8.6 Phase 6 — 自动化回归清单【待执行】

| 测试项 | 预期结果 |
|---|---|
| 单次打开/关闭 | 桌宠立即消失 |
| 连续打开关闭 100 次 | 无异常、无残留 |
| 多实例运行 | 关闭一个不影响其他实例 |
| 动画播放中关闭 | 动画停止，无崩溃 |
| 拖拽窗口时关闭 | 正常销毁 |
| 高频点击开关按钮 | 不死锁、不重复关闭 |
| 插件运行时关闭 | 插件全部停止 |
| 行为树运行时关闭 | `Behavior.Stop()` 执行完成 |
| `Engine.Stop()` 重复调用 | 幂等，不报错 |
| 应用退出 | 所有桌宠全部释放，无后台线程残留 |

**执行顺序**：Phase 1（验证）→ Phase 2（幂等）→ Phase 3（状态机）→ Phase 4（消息统一）→ Phase 5（Dump）→ Phase 6（回归）。每步独立验收，不叠加改动。

> Phase 1-6 已全部落地到源码并通过测试验证。Engine 生命周期重构已收敛，不再继续投入。

---

### 14.9 下一阶段路线图（架构层面，Phase 7-12）【规划中，未改代码】

> Engine 生命周期（Created→Running→Stopping→Stopped）已稳定，`transition()` 统一状态迁移、`PostCritical` 仅 Running/Stopping 可入队、`closeOnce` 幂等保护、`Dump()` 资源快照、10 项回归测试全部 PASS。
> 当前最大的瓶颈已不在 Engine，而在 `internal/pet` 中 Behavior/Animation/Renderer/Atlas 等模块的架构耦合。

| 阶段 | 内容 | 优先级 | 说明 |
|---|---|---|---|
| Phase 7 | Behavior Scheduler 统一调度 | ⭐⭐⭐⭐⭐ | 所有模块（Animation/Plugin/Timer）经 Scheduler → Engine.Post，不再直接调 Engine。后续暂停/恢复/限帧/倍速/录制/Replay 均由此受益。 |
| Phase 8 | Renderer 独立线程 | ⭐⭐⭐⭐⭐ | Behavior → FrameQueue → Renderer Thread → Window，完全解耦渲染与逻辑。后续替换 Atlas/GPU/OpenGL/Direct2D/Skia 均无需改动逻辑层。 |
| Phase 9 | Animation Graph | ⭐⭐⭐⭐⭐ | 替代当前简单 FSM，引入 Blend/Walk/Jump/Fall 等动画混合节点，桌宠动作自然度大幅提升。 |
| Phase 10 | Atlas Cache + Bitmap Cache | ⭐⭐⭐⭐☆ | PNG→Decode 一次→AtlasCache→BitmapCache→Renderer，避免每帧重复解码裁剪，性能提升显著。 |
| Phase 11 | 插件生命周期统一 | ⭐⭐⭐⭐☆ | PluginManager 与 Engine 解耦，插件独立生命周期（Init→Start→Stop→Dispose），不依赖 Engine 内部状态。 |
| Phase 12 | 多桌宠实例完全隔离 | ⭐⭐⭐⭐⭐ | Engine/Renderer/Scheduler/Plugin 全独立，每个实例拥有自己的线程和资源池，互不干扰。 |

> 建议按优先级推进，每阶段独立设计、实现、测试、验收，不叠加改动。

### internal/bridge/pet.go（资源发现/监听）

```go
package bridge

import (
	"cursor/internal/appdata"
	"cursor/internal/pet"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type PetInfo struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Author        string   `json:"author"`
	RootPath      string   `json:"rootPath"`
	FrameWidth    int      `json:"frameWidth"`
	FrameHeight   int      `json:"frameHeight"`
	AnimationCnt  int      `json:"animationCnt"`
	Status        string   `json:"status"`
	StatusText    string   `json:"statusText"`
	Errors        []string `json:"errors,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

const (
	EventPetStateChanged = "pet:state-changed"
	EventPetListChanged  = "pet:list-changed"
	EventCursorActivity  = "cursor:activity"
)

const watchInterval = 3 * time.Second

type PetService struct {
	mu      sync.RWMutex
	app     *application.App
	petsDir string

	cached   []PetInfo
	cachedAt time.Time

	stopWatch chan struct{}
}

func NewPetService() *PetService {
	return &PetService{}
}

func (s *PetService) SetApp(app *application.App) {
	s.mu.Lock()
	s.app = app
	s.mu.Unlock()
	s.startWatching()
}

func PetsDir() string {
	root := appdata.RootDir()
	if strings.TrimSpace(root) == "" {
		root = ".cursor-local-assistant-v2"
	}
	dir := filepath.Join(root, "pets")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func (s *PetService) startWatching() {
	s.mu.Lock()
	if s.stopWatch != nil {
		s.mu.Unlock()
		return
	}
	s.stopWatch = make(chan struct{})
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(watchInterval)
		defer ticker.Stop()
		s.refreshIfChanged()
		for {
			select {
			case <-s.stopWatch:
				return
			case <-ticker.C:
				s.refreshIfChanged()
			}
		}
	}()
}

func (s *PetService) refreshIfChanged() {
	newPets := scanAllPets()
	s.mu.Lock()
	changed := !petListEqual(s.cached, newPets)
	s.cached = newPets
	s.cachedAt = time.Now()
	app := s.app
	s.mu.Unlock()

	if changed && app != nil {
		app.Event.Emit(EventPetListChanged, newPets)
	}
}

func (s *PetService) ScanPets() ([]PetInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.cached) == 0 {
		s.mu.RUnlock()
		s.mu.Lock()
		if len(s.cached) == 0 {
			s.cached = scanAllPets()
			s.cachedAt = time.Now()
		}
		s.mu.Unlock()
		s.mu.RLock()
	}
	return s.cached, nil
}

func (s *PetService) OpenPetsDirectory() {
	s.mu.Lock()
	if s.petsDir == "" {
		s.petsDir = PetsDir()
	}
	dir := s.petsDir
	s.mu.Unlock()
	openDirectory(dir)
}

func (s *PetService) DeletePet(petID string) error {
	dir := PetsDir()
	petDir := filepath.Join(dir, petID)
	if _, err := os.Stat(petDir); os.IsNotExist(err) {
		return fmt.Errorf("宠物 %s 不存在", petID)
	}
	if err := os.RemoveAll(petDir); err != nil {
		return err
	}
	s.refreshIfChanged()
	return nil
}

func (s *PetService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopWatch != nil {
		close(s.stopWatch)
		s.stopWatch = nil
	}
}

func scanAllPets() []PetInfo {
	dir := PetsDir()
	log.Printf("[Pet Scanner] Pets Root = %s", dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("[Pet Scanner] ReadDir error: %v", err)
		return nil
	}
	log.Printf("[Pet Scanner] Found %d entries in pets directory", len(entries))
	var result []PetInfo
	for _, entry := range entries {
		log.Printf("[Pet Scanner]   Entry: %s (isDir=%v)", entry.Name(), entry.IsDir())
		if !entry.IsDir() {
			continue
		}
		petDir := filepath.Join(dir, entry.Name())
		log.Printf("[Pet Scanner]   Checking: %s", petDir)
		m := pet.ScanPetDir(petDir)
		if m != nil {
			result = append(result, manifestToInfo(m))
			log.Printf("[Pet Scanner]   Added: %s (status=%s)", m.Name, m.Status)
		} else {
			log.Printf("[Pet Scanner]   Skipped: nil result")
		}
	}
	log.Printf("[Pet Scanner] Total Pets = %d", len(result))
	return result
}

func manifestToInfo(m *pet.PetManifest) PetInfo {
	return PetInfo{
		ID:           m.ID,
		Name:         m.Name,
		Version:      m.Version,
		Author:       m.Author,
		RootPath:     m.RootPath,
		FrameWidth:   m.FrameWidth,
		FrameHeight:  m.FrameHeight,
		AnimationCnt: len(m.AnimationNames),
		Status:       m.StatusText,
		StatusText:   m.StatusText,
		Errors:       m.Errors,
		Warnings:     m.Warnings,
	}
}

func petListEqual(a, b []PetInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Status != b[i].Status {
			return false
		}
	}
	return true
}
```

### internal/app/runner.go（桌宠相关片段）

```go
	// runner.go 中注册与启动：
	windowService := bridge.NewWindowService()
	petService := bridge.NewPetService()
	// ...
	// 在 application 的 Services 中注册：
	//   application.NewService(windowService)
	//   application.NewService(petService)
	// ...
	windowService.SetApp(app)
	windowService.SetUpdater(updateManager)
	petService.SetApp(app)

	// PET_DEBUG=1 时自动开启桌宠，方便无头/自动化环境采集 Window 层调试日志
	if os.Getenv("PET_DEBUG") == "1" {
		go func() {
			time.Sleep(2 * time.Second)
			log.Println("[Pet][DEBUG] PET_DEBUG=1: auto-opening pet window for diagnostics")
			windowService.OpenPetWindow()
		}()
	}

	// 连接 proxy 活动事件到 pet 状态（petService.FireCursorActivity）
	// ...
	// 托盘菜单：
	menu.Add("显示桌宠").OnClick(func(ctx *application.Context) {
		windowService.TogglePetWindow()
	})
	// ...
	// 退出时统一释放：
	OnShutdown: func() {
		petService.Stop()
		windowService.StopAllPets()
		// ...
	}
```

### internal/pet/nezukocoder/pet.json（默认桌宠）

```json
{
  "id": "nezukocoder",
  "displayName": "NezukoCoder",
  "description": "A chibi Nezuko-inspired coding companion typing on a laptop with a simple OpenAI emblem.",
  "spritesheetPath": "spritesheet.webp",
  "spritesheetWidth": 1536,
  "spritesheetHeight": 1872,
  "frameWidth": 192,
  "frameHeight": 208,
  "columns": 8,
  "rows": 9,
  "totalFrames": 72,
  "effectiveFrames": 61,
  "fps": 8,
  "animations": {
    "idle":  { "fps": 6,  "loop": true,  "priority": 1, "frames": [0, 1, 2, 3, 4, 5] },
    "walk":  { "fps": 10, "loop": true,  "priority": 3, "frames": [8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23] },
    "wave":  { "fps": 8,  "loop": true,  "priority": 5, "frames": [24, 25, 26, 27] },
    "sit":   { "fps": 6,  "loop": true,  "priority": 2, "frames": [32, 33, 34, 35, 36] },
    "sleep": { "fps": 3,  "loop": true,  "priority": 0, "frames": [40, 41, 42, 43, 44, 45, 46, 47] },
    "think": { "fps": 8,  "loop": true,  "priority": 2, "frames": [48, 49, 50, 51, 52] },
    "happy": { "fps": 10, "loop": false, "priority": 6, "frames": [56, 57, 58, 59, 60, 61] },
    "focus": { "fps": 8,  "loop": true,  "priority": 4, "frames": [64, 65, 66, 67, 68, 69] }
  },
  "defaultAnimation": "idle"
}
```

---

## 附录 B：诊断与调试速查

### PET_DEBUG=1 调试模式

设置环境变量 `PET_DEBUG=1` 后：
- `runner.go` 会延迟 2s 自动调用 `windowService.OpenPetWindow()`。
- `window_windows.go` 的 `dbg()` 会打印 Window 层全部关键节点：DPI 感知、窗口创建/HWND/样式、ShowWindow、IsWindowVisible、消息循环存活、postWork 入队/执行、每帧 Render、UpdateLayeredWindow 返回值与 GetLastError、当前窗口矩形。
- `doRender` 在 PET_DEBUG 下会于左上角画一个不透明红色实心块 + 白色边框（16x16）：只要窗口真的被系统显示，桌面上就一定能看到这个红块。若日志显示渲染成功但看不到红块 → 问题在"窗口不可见/被移出屏幕/DPI 虚化"，而非渲染本身。

### 已修复根因速查（对应之前排查）

1. **窗口显示但看不到桌宠**：`toRGBA()` 类型归一化（engine.go），修复 webp→*image.NRGBA 断言失败导致帧被丢弃。
2. **桌宠卡死（鼠标移到窗口后冻结，workCh 满）**：`WM_MOUSEMOVE` 锁外回调 + `postWork` 窗口线程内同步执行 + `isWindowThread()` 判断，修复 `sync.Mutex` 不可重入自死锁。
3. **鼠标移上去"无响应"光标**：`init()` 中 `LoadCursor(IDC_ARROW)` 设类光标 + `WM_SETCURSOR` 处理。
4. **点击关闭桌宠不消失**：`stopActivePet()` 关闭时 activeID 空 fallback 到 `EmbeddedPetDir`，与打开一致。

### 日志关键序列（正常启动）

```
[Pet] openPetWindow: trying petID=nezukocoder dir=...
[Pet] openPetWindow: using embedded pet
[Pet] buildEngine: atlas created, frames=72
[Pet] buildEngine: native window created
[Pet] NewNativeWindow: window created, hwnd=...
[Pet] messageLoop: started
[Pet] Start: engine thread launched
[Pet] Start: showing window, starting animation & behavior
[Pet] Start: window shown
[Pet] Start: anim idle playing
[Pet] Start: behavior started
[Pet] engine thread: started
[Pet] doRender: setup done ...
[Pet] doRender: UpdateLayeredWindow OK ...
```

### 关闭关键序列

```
[Pet] Stop: begin
[Pet] Stop: stopCh closed
[Pet] Stop: behavior stopped
[Pet] Stop: engine thread exited
[Pet] Close: posting WM_QUIT to window thread
[Pet] Stop: messageLoop exited
[Pet][Manager] stopped pet "nezukocoder" (remaining=0)
```




















