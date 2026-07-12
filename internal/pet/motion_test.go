package pet

import (
	"math"
	"testing"
)

// 用伪窗口测试 Motion 的纯逻辑（不依赖真实 Win32）。
type fakeMotionWindow struct {
	x, y  int
	w, h  int
	moves int
	lastX int
	lastY int
}

func (f *fakeMotionWindow) Position() (int, int) { return f.x, f.y }
func (f *fakeMotionWindow) Size() (int, int)     { return f.w, f.h }
func (f *fakeMotionWindow) MoveTo(x, y int) {
	f.moves++
	f.lastX, f.lastY = x, y
	f.x, f.y = x, y
}

// fakeEventPublisher 记录发布过的事件。
type fakeEventPublisher struct {
	events []Event
}

func (p *fakeEventPublisher) Publish(e Event) {
	if p.events == nil {
		p.events = make([]Event, 0)
	}
	p.events = append(p.events, e)
}

func newTestMotion() (*MotionController, *fakeMotionWindow, *fakeEventPublisher) {
	fw := &fakeMotionWindow{x: 100, y: 100, w: 50, h: 50}
	pub := &fakeEventPublisher{}
	// 绕过 NewMotionController 的 metrics 调用，直接构造。
	m := &MotionController{
		win:       fw,
		curX:      100, curY: 100,
		targetX: 100, tgtY: 100,
		arrived:   true,
		enabled:   true,
		smoothing: 10,
		screenW:   1920, screenH: 1080,
		events:    pub,
	}
	return m, fw, pub
}

func TestMotion_MoveToClampsToScreen(t *testing.T) {
	m, _, _ := newTestMotion()
	// 目标超出屏幕，应被 clamp（屏幕 1920x1080，窗口 50x50）。
	ok := m.MoveTo(3000, 3000)
	if !ok {
		t.Fatal("expected move to initiate")
	}
	if m.targetX != 1870 || m.tgtY != 1030 {
		t.Fatalf("target not clamped: got (%v,%v)", m.targetX, m.tgtY)
	}
}

func TestMotion_ConvergesToTarget(t *testing.T) {
	m, fw, _ := newTestMotion()
	m.MoveTo(500, 500)
	if m.IsArrived() {
		t.Fatal("should not be arrived immediately after MoveTo")
	}
	// 模拟 60Hz 推进 1 秒，应到达。
	arrivedAt := -1
	for i := 0; i < 200; i++ {
		m.Update(1000.0 / 60.0)
		if m.IsArrived() {
			arrivedAt = i
			break
		}
	}
	if arrivedAt < 0 {
		t.Fatal("motion never arrived within 200 frames")
	}
	if math.Abs(float64(fw.lastX)-500) > 1 || math.Abs(float64(fw.lastY)-500) > 1 {
		t.Fatalf("final pos not at target: (%d,%d)", fw.lastX, fw.lastY)
	}
	if !m.IsArrived() {
		t.Fatal("IsArrived should be true after convergence")
	}
}

func TestMotion_OnArriveFiresOnce(t *testing.T) {
	m, _, pub := newTestMotion()
	m.MoveTo(600, 600)
	for i := 0; i < 200; i++ {
		m.Update(1000.0 / 60.0)
	}
	// arrived 后继续 Update 不应再触发事件（已 arrived 直接 return）。
	count := 0
	for _, e := range pub.events {
		if e.Type == EventMotionArrived {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("EventMotionArrived should fire exactly once, got %d", count)
	}
}

func TestMotion_DisableStopsMovement(t *testing.T) {
	m, fw, _ := newTestMotion()
	m.MoveTo(800, 800)
	m.Disable()
	startX := fw.lastX
	for i := 0; i < 30; i++ {
		m.Update(1000.0 / 60.0)
	}
	if fw.lastX != startX {
		t.Fatalf("disabled motion should not move, moved to %d", fw.lastX)
	}
}
