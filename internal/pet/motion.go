package pet

import (
	"math"
)

// motionWindow 是 Motion 控制器所需的窗口能力抽象（便于测试注入）。
type motionWindow interface {
	Position() (int, int)
	Size() (int, int)
	MoveTo(x, y int)
}

// MotionController 负责桌宠窗口的平滑移动（v2 Phase 7）。
//
// 把"瞬间 MoveTo"升级为带缓动的插值移动：
//   - MoveTo 设定目标坐标（自动 clamp 到屏幕内，避免走出屏幕）。
//   - Update(dt) 每帧由引擎线程调用，按指数缓动逼近目标，
//     产生自然的"滑动"而非瞬移，复刻 Codex 桌宠的走动观感。
//   - 到达目标后发布 EventMotionArrived（仅一次），供 Behavior 判断走动结束。
//   - 支持 Stop/重置，窗口被拖拽时由外部 Disable 以避免与拖拽打架。
//
// 线程：Motion 由引擎线程独占调用（Update/MoveTo），内部仅通过
// window.Position/MoveTo（后者经窗口线程投递 WinAPI）交互，无线程安全问题。
type MotionController struct {
	win motionWindow

	curX, curY    float64
	targetX, tgtY float64
	arrived       bool
	enabled       bool

	// smoothing 缓动系数（每秒收敛比例），越大越快逼近目标。
	smoothing float64

	// events 用于发布 EventMotionArrived，无需持有 *Engine。
	events EventPublisher

	// screenW/H 屏幕尺寸，用于 clamp 目标到可见区域。
	screenW, screenH int
}

// NewMotionController 创建移动控制器。
func NewMotionController(win *NativeWindow, events EventPublisher) *MotionController {
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
		smoothing: 6.0, // 约 0.16s 收敛到 90%
		screenW:   sw,
		screenH:   sh,
		events:    events,
	}
}

// Start 实现 Lifecycle，启用移动控制器。
func (m *MotionController) Start() {
	m.enabled = true
}

// Stop 实现 Lifecycle，禁用移动控制器并停止当前移动。
func (m *MotionController) Stop() {
	m.enabled = false
}

// Dispose 实现 Lifecycle，释放移动控制器资源。
func (m *MotionController) Dispose() {
	m.Stop()
}

// Ensure MotionController implements Lifecycle.
var _ Lifecycle = (*MotionController)(nil)

// SetSmoothing 设置缓动系数（每秒），值越大移动越快。
func (m *MotionController) SetSmoothing(s float64) {
	if s > 0 {
		m.smoothing = s
	}
}

// MoveTo 设定移动目标（自动 clamp 到屏幕内）。返回是否确实发起了新移动。
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

// Disable 暂停移动（如用户拖拽时），保持当前位置。
func (m *MotionController) Disable() {
	m.enabled = false
}

// Enable 恢复移动。
func (m *MotionController) Enable() {
	m.enabled = true
}

// IsArrived 是否已到达目标。
func (m *MotionController) IsArrived() bool {
	return m.arrived
}

// Update 每帧推进插值（dt 毫秒）。必须在引擎线程调用。
func (m *MotionController) Update(dtMs float64) {
	if !m.enabled || m.arrived {
		return
	}
	dt := dtMs / 1000.0
	// 指数缓动：cur += (target-cur) * (1 - e^{-k*dt})
	factor := 1 - math.Exp(-m.smoothing*dt)
	newX := m.curX + (m.targetX-m.curX)*factor
	newY := m.curY + (m.tgtY-m.curY)*factor

	// 收敛判定：足够近则吸附到目标。
	const eps = 0.5
	if math.Abs(m.targetX-newX) < eps && math.Abs(m.tgtY-newY) < eps {
		newX = m.targetX
		newY = m.tgtY
		m.arrived = true
	}
	m.curX = newX
	m.curY = newY
	m.win.MoveTo(int(newX), int(newY))

	if m.arrived && m.events != nil {
		m.events.Publish(Event{Type: EventMotionArrived})
	}
}

// winSizeViaMetrics 读取屏幕尺寸（与 window 创建时一致）。
func winSizeViaMetrics() (int, int) {
	sw, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
	sh, _, _ := procGetSystemMetrics.Call(SM_CYSCREEN)
	return int(sw), int(sh)
}

// clampInt 限制在 [lo,hi]。
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
