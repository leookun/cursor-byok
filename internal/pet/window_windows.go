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
// 在真实 Windows 上排查"服务显示已开启但桌面无窗口"时，把这一项打开即可
// 一次性看到：窗口创建/HWND/样式、ShowWindow 是否成功、IsWindowVisible、
// 消息循环是否存活、postWork 入队/执行、每帧 Render、UpdateLayeredWindow
// 返回值与 GetLastError、当前窗口矩形，从而快速定位是 Window 没显示还是没渲染。
var petDebug = os.Getenv("PET_DEBUG") == "1"

func dbg(format string, args ...interface{}) {
	if petDebug {
		log.Printf("[Pet][DEBUG] "+format, args...)
	}
}

// dbgDPI 在 PET_DEBUG=1 时打印进程/系统 DPI 感知信息。
// 高 DPI（如 150%/200%）下，若进程不是 Per-Monitor Aware，系统会对 Layered
// Window 做位图拉伸或把它放到虚拟坐标外，导致"服务已开但桌面看不到窗口"。
// awareness: 0=UNAWARE, 1=SYSTEM_AWARE, 2=PER_MONITOR_AWARE。
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
	procPostThreadMessage = user32.NewProc("PostThreadMessageW")
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
	// DPI 感知相关：Layered Window 在高 DPI 下常因缩放策略被虚化/放到屏外，
	// 这些 API 用于在调试模式打印进程 DPI 感知级别与系统/窗口 DPI。
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
	HWND_TOPMOST     = ^uintptr(0) // -1
	SWP_NOSIZE       = 0x0001
	SWP_NOMOVE       = 0x0002
	SWP_NOACTIVATE   = 0x0010
	SWP_SHOWWINDOW   = 0x0040
	WM_DESTROY       = 0x0002
	WM_CLOSE         = 0x0010
	WM_QUIT          = 0x0012
	WM_LBUTTONDOWN   = 0x0201
	WM_MOUSEMOVE     = 0x0200
	WM_LBUTTONUP     = 0x0202
	WM_RBUTTONUP     = 0x0205
	// 窗口线程消息统一策略（Phase 4）：
	//   - WM_QUIT：PostThreadMessage(windowThreadID, WM_QUIT) — 线程级关闭。
	//   - WinAPI 操作（渲染/移动/显示）：全部经 postWork→workCh 在窗口线程同步执行。
	//   - WM_DESTROY：仅 Close() 兜底路径（无 threadID 时），走 windowProc→onDestroy。
	// 已移除 WM_PET_RENDER/MOVE/SHOW/HIDE（原设计通过 PostMessage 跨线程操作，
	// 后统一迁移到 postWork 通道模式，这些常量不再使用）。
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
	// messageLoopDone 在 messageLoop 退出后 close，可用于等待窗口消息循环结束
	messageLoopDone chan struct{}
	// workCh 是窗口线程的工作队列：引擎线程/其它线程把 WinAPI 操作
	// （渲染/移动/显示）经此投递，由窗口线程串行执行，避免跨线程调用 WinAPI。
	// 消息循环用 MsgWaitForMultipleObjects 同时等待系统消息与 workCh。
	workCh chan func()
	// workEvent 是 workCh 对应的内核事件句柄：postWork 时 SetEvent 唤醒
	// 窗口线程的 MsgWaitForMultipleObjects，避免忙等/轮询延迟。
	workEvent uintptr
	// renderCount 累计成功渲染帧数，仅用于 PET_DEBUG=1 的 Debug Overlay 帧号显示。
	renderCount uint64
	// windowThreadID 记录运行 runMessageLoop 的 OS 线程 ID。
	// postWork 据此判断调用方是否就是窗口线程本身：若是则直接同步执行，
	// 避免 onDrag→MoveTo→postWork 的自等待死锁。
	windowThreadID uint32
	// closeOnce 确保 Close() 只执行一次，防止 Stop→Destroy→Stop 重入导致
	// 重复向窗口线程投递 WM_QUIT 或重复调用 DestroyWindow。
	closeOnce sync.Once
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
	wc.Style = 0 // CS_HREDRAW | CS_VREDRAW
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
		// 无类光标而保留旧光标/显示"无响应"等待圈。HTCLIENT=1 表示在客户区。
		if hArrowCursor != 0 {
			procSetCursor.Call(hArrowCursor)
		}
		return 1 // TRUE：已处理，系统不再继续
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
			// 记录窗口屏幕位置
			var rect RECT
			procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
			w.winStartX = int(rect.Left)
			w.winStartY = int(rect.Top)
			w.mu.Unlock()
			// 捕获鼠标
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
			// 复用右键作为菜单触发（通过 onDragEnd 回调通知）
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

// IDC_ARROW 是标准箭头光标资源 ID，LoadCursor(0, IDC_ARROW) 获取系统默认箭头。
// 分层窗口若不设置类光标 / 不处理 WM_SETCURSOR，鼠标移上去会显示"无响应"光标。
const IDC_ARROW = 32512

// WM_SETCURSOR：鼠标移入窗口时系统询问"该显示什么光标"。必须返回 TRUE 表示
// 已设置光标，否则 DefWindowProc 在无类光标时会保留旧光标/显示等待圈。
const WM_SETCURSOR = 0x0020

// hArrowCursor 缓存系统箭头光标句柄，供 WM_SETCURSOR 处理使用。
var hArrowCursor uintptr

// NewNativeWindow 创建一个透明 Layered Window。
// 关键约束：Win32 窗口消息循环必须与窗口在同一个 OS 线程上运行。
// 因此窗口创建和 messageLoop 都在一个 LockOSThread 的 goroutine 中完成。
func NewNativeWindow(width, height int) (*NativeWindow, error) {
	className := syscall.StringToUTF16Ptr("CursorPetWindow")
	title := syscall.StringToUTF16Ptr("")

	// 先打印 DPI 感知信息：这是"服务已开但看不到窗口"最常见的隐性根因之一。
	dbgDPI()

	screenW, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
	screenH, _, _ := procGetSystemMetrics.Call(SM_CYSCREEN)

	// 使用 WS_EX_LAYERED + WS_EX_TOOLWINDOW + WS_EX_TOPMOST 让窗口能接收鼠标事件用于拖拽。
	// 不使用 WS_EX_TRANSPARENT，因为它会让整个窗口完全忽略鼠标事件，无法拖拽。
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
	// 创建 workEvent 内核事件，用于唤醒窗口线程消费 workCh。
	ev, _, _ := procCreateEvent.Call(0, 0, 0, 0)
	w.workEvent = ev
	if w.workEvent == 0 {
		log.Println("[Pet] NewNativeWindow: failed to create workEvent")
	}

	// 启动 locked OS 线程，在该线程上完成窗口创建和消息循环。
	// 这样 DestroyWindow/PostMessage/GetMessage 都在同一线程，消息路由正常。
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
		// 立即检查窗口是否真实可见（仅在调试模式下，避免无关开销）。
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
		// 在同一线程上运行消息循环
		w.runMessageLoop()
	}()

	// 等待窗口创建结果（最多 2s 防止异常死锁）
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
// 用 MsgWaitForMultipleObjects 同时等待系统消息与 workCh（渲染/移动/显示指令），
// 确保所有 WinAPI 操作都在窗口线程执行（杜绝跨线程调用 WinAPI）。
// 收到 WM_QUIT（PostThreadMessage 或 PostQuitMessage 投递）后返回。
func (w *NativeWindow) runMessageLoop() {
	log.Println("[Pet] messageLoop: started")
	// 记录窗口线程 ID，供 postWork 判断是否在窗口线程内调用（避免自等待死锁）。
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
		// 用 MsgWaitForMultipleObjects 同时等待：① 窗口消息 ② workEvent（workCh 有指令）。
		// 这样渲染/移动指令能即时唤醒窗口线程，无轮询延迟。
		var handles [1]uintptr
		nCount := uintptr(0)
		if w.workEvent != 0 {
			handles[0] = w.workEvent
			nCount = 1
		}
		_, _, _ = procMsgWaitForMultipleObjects.Call(
			nCount,                         // nCount
			uintptr(unsafe.Pointer(&handles[0])), // pHandles
			0,                              // bWaitAll = FALSE
			uintptr(0xFFFFFFFF),            // INFINITE：无消息且无工作时阻塞
			uintptr(QS_ALLINPUT),           // dwWakeMask
		)
		_ = WAIT_OBJECT_0
		_ = WAIT_TIMEOUT

		// 先排空 workCh 中积压的渲染/移动指令（在窗口线程执行）。
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

		// 处理所有可用的系统消息
		for {
			var msg MSG
			hasMsg, _, _ := procPeekMessage.Call(
				uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 0x0001, // PM_REMOVE
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

			// 收到 WM_DESTROY 后调用 onDestroy（清理资源），并退出循环。
			if msg.Message == WM_DESTROY {
				log.Println("[Pet] messageLoop: WM_DESTROY received")
				if w.onDestroy != nil {
					w.onDestroy()
				}
				// 主动投递 WM_QUIT 让下一轮循环退出
				procPostQuitMessage.Call(0)
			}
		}
	}
exitLoop:
	// 消息循环退出前主动销毁窗口（确保清理 GDI 资源）
	hwnd := w.hwnd.Load()
	if hwnd != 0 {
		log.Println("[Pet] messageLoop: destroying window")
		procDestroyWindow.Call(hwnd)
		nativeWindowsMu.Lock()
		delete(nativeWindows, hwnd)
		nativeWindowsMu.Unlock()
		w.hwnd.Store(0)
	}
	// 关闭 workEvent 内核事件，避免句柄泄漏。
	if w.workEvent != 0 {
		procCloseHandle.Call(w.workEvent)
		w.workEvent = 0
	}
}

// SetOnDestroy 注册销毁回调。
func (w *NativeWindow) SetOnDestroy(fn func()) {
	w.onDestroy = fn
}

// SetOnDrag 注册拖拽回调（dx, dy 相对于拖拽起点）。
func (w *NativeWindow) SetOnDrag(fn func(dx, dy int)) {
	w.onDrag = fn
}

// SetOnDragEnd 注册拖拽结束回调（也用作右键回调）。
func (w *NativeWindow) SetOnDragEnd(fn func()) {
	w.onDragEnd = fn
}

// WaitForMessageLoop 等待窗口消息循环退出（最多 timeout）。用于确保
// 进程退出前所有 native goroutine 干净结束，避免 Wails 主循环等待时显示"未响应"。
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

// Show 显示窗口。经窗口线程执行 ShowWindow，避免跨线程调用 WinAPI。
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
		ret, _, _ := procShowWindow.Call(hwnd, 5) // SW_SHOW
		dbg("ShowWindow(SW_SHOW) ret=%d (0=fail, GetLastError=%d)", ret, getLastError())
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		dbg("IsWindowVisible after Show = %v", vis != 0)
		var rc RECT
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
		dbg("WindowRect after Show = (%d,%d,%d,%d) size=%dx%d",
			rc.Left, rc.Top, rc.Right, rc.Bottom, rc.Right-rc.Left, rc.Bottom-rc.Top)
	})
}

// Hide 隐藏窗口。经窗口线程执行 ShowWindow。
func (w *NativeWindow) Hide() {
	hwnd := w.hwnd.Load()
	if hwnd == 0 {
		return
	}
	w.mu.Lock()
	w.shown = false
	w.mu.Unlock()
	w.postWork(func() {
		procShowWindow.Call(hwnd, 0) // SW_HIDE
	})
}

// postWork 把一条 WinAPI 操作投递到窗口线程执行。
// 任何非窗口线程都不得直接调用 WinAPI（UpdateLayeredWindow/SetWindowPos/
// ShowWindow），必须经此，由窗口线程串行执行（杜绝跨线程 WinAPI 调用）。
// isWindowThread 判断当前 goroutine 是否运行在窗口线程上。
// postWork 据此决定是直接同步执行 work（窗口线程内）还是投递到 workCh。
// 注意：Go 的 goroutine 可能被调度到不同 OS 线程，但 windowProc 和
// runMessageLoop 都通过 runtime.LockOSThread 锁定在创建窗口的线程上，
// 因此比较 GetCurrentThreadId 与 windowThreadID 是可靠的。
func (w *NativeWindow) isWindowThread() bool {
	if w.windowThreadID == 0 {
		return false
	}
	tid, _, _ := procGetCurrentThreadId.Call()
	return uint32(tid) == w.windowThreadID
}

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
	// 旧实现"满就丢弃"会导致窗口线程卡死时引擎侧静默假死、画面冻结且无从感知。
	select {
	case w.workCh <- fn:
	default:
		select {
		case w.workCh <- fn:
		case <-time.After(1 * time.Second):
			log.Println("[Pet][ERROR] postWork: workCh still full after 1s wait (window thread likely blocked in doRender) - dropping work")
		}
	}
	// 唤醒窗口线程的 MsgWaitForMultipleObjects，避免渲染/移动延迟。
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

// Close 请求销毁窗口（线程安全）。
// 通过向窗口线程投递线程级 WM_QUIT 触发有序退出，runMessageLoop 内部
// 会在该 OS 线程上调用 DestroyWindow 并清理资源。绝不跨线程调用 DestroyWindow。
//
// 注意：不能用 PostMessage(hwnd, WM_QUIT)，因为 WM_QUIT 是线程级消息，
// MSDN 明确禁止用 PostMessage 发送，且 PeekMessage/GetMessage 不会以
// 关联 hwnd 的普通消息形式返回它，会导致消息循环永远收不到 WM_QUIT、
// DestroyWindow 永不执行、窗口一直存在。正确做法是向窗口线程（而非窗口）
// 投递 WM_QUIT。
//
// 幂等保护：通过 closeOnce 保证 Close() 只执行一次。即使 Engine.Stop 调用
// Close() 后 onDestroy 回调又触发 Engine.Stop→Close() 路径，也不会重复
// 投递 WM_QUIT 或重复 DestroyWindow。
func (w *NativeWindow) Close() {
	w.closeOnce.Do(func() {
		w.closeLocked()
	})
}

// closeLocked 是 Close 的实际实现，由 closeOnce 确保最多执行一次。
func (w *NativeWindow) closeLocked() {
	hwnd := w.hwnd.Load()
	if w.windowThreadID != 0 {
		log.Printf("[Pet] Close: posting WM_QUIT to window thread %d", w.windowThreadID)
		procPostThreadMessage.Call(uintptr(w.windowThreadID), WM_QUIT, 0, 0)
		// 唤醒可能在 MsgWaitForMultipleObjects 上阻塞的窗口线程，立即取消息。
		if w.workEvent != 0 {
			procSetEvent.Call(w.workEvent)
		}
		return
	}
	// 兜底：无 threadID 记录时，退回向窗口投递 WM_DESTROY 触发 windowProc 销毁路径。
	if hwnd != 0 {
		log.Println("[Pet] Close: no threadID, posting WM_DESTROY fallback")
		procPostMessage.Call(hwnd, WM_DESTROY, 0, 0)
		if w.workEvent != 0 {
			procSetEvent.Call(w.workEvent)
		}
	}
}

// Start 实现 Lifecycle，显示窗口。
func (w *NativeWindow) Start() {
	w.Show()
}

// Stop 实现 Lifecycle，关闭窗口。
func (w *NativeWindow) Stop() {
	w.Close()
}

// Dispose 实现 Lifecycle，关闭窗口并等待消息循环退出。
func (w *NativeWindow) Dispose() {
	w.Close()
	w.WaitForMessageLoop(3 * time.Second)
}

// Ensure NativeWindow implements Lifecycle.
var _ Lifecycle = (*NativeWindow)(nil)

// MoveTo 移动窗口到屏幕坐标。经窗口线程执行 SetWindowPos。
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

// Position 返回窗口当前屏幕坐标（由 MoveTo 维护，非实时 Win32 查询）。
// Motion 控制器据此计算插值起点，避免每帧跨线程查询 GetWindowRect。
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
// 调用方（Engine）传入的是 FrameAtlas 已切好的 *image.RGBA，直接复用其 Pix/Stride，零拷贝。
// 实际 UpdateLayeredWindow 在窗口线程上执行（见 doRender + postWork），避免跨线程 WinAPI。
func (w *NativeWindow) Render(img *image.RGBA) {
	if img == nil {
		dbg("Render skipped: img==nil")
		return
	}
	hwnd := w.hwnd.Load()
	if hwnd == 0 {
		dbg("Render skipped: hwnd==0 (window destroyed)")
		return // 窗口已销毁，跳过渲染
	}
	b := img.Bounds()
	dbg("Render: frame bounds=%dx%d stride=%d", b.Dx(), b.Dy(), img.Stride)
	// 捕获当前 img 指针，投递到窗口线程执行实际渲染。
	// atlas 帧像素在渲染期间不会被修改（引擎只切帧索引），跨线程读取安全。
	w.postWork(func() {
		w.doRender(hwnd, img)
	})
}

// doRender 在窗口线程上执行实际 UpdateLayeredWindow。
// 不在引擎线程/其它线程直接调用 WinAPI，杜绝跨线程窗口操作导致的
// 黑屏/闪烁/句柄错误。
func (w *NativeWindow) doRender(hwnd uintptr, img *image.RGBA) {
	t0 := time.Now()
	if img == nil || hwnd == 0 {
		log.Printf("[Pet][ERROR] doRender: early return img=nil?%v hwnd=%d", img == nil, hwnd)
		return
	}
	bounds := img.Bounds()
	bw := bounds.Dx()
	bh := bounds.Dy()

	// 创建内存 DC 并绘制到位图
	hdcMem, _, _ := procCreateCompatibleDC.Call(0)
	if hdcMem == 0 {
		log.Printf("[Pet][ERROR] doRender: CreateCompatibleDC failed hwnd=%x", hwnd)
		return
	}
	defer procDeleteDC.Call(hdcMem)

	var bi BITMAPINFOHEADER
	bi.BiSize = uint32(unsafe.Sizeof(bi))
	bi.BiWidth = int32(bw)
	bi.BiHeight = -int32(bh) // top-down
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

	// 拷贝像素（BGRA → 预乘 Alpha 用于 UpdateLayeredWindow）
	// alpha=0 的像素必须 RGB 也清零，避免预乘时残留黑色背景
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
				dst[0] = byte(uint32(src[2]) * uint32(alpha) / 255) // B
				dst[1] = byte(uint32(src[1]) * uint32(alpha) / 255) // G
				dst[2] = byte(uint32(src[0]) * uint32(alpha) / 255) // R
				dst[3] = alpha                                      // A
			}
		}
	}

	// Debug Overlay：在 PET_DEBUG=1 时于左上角强制画一个不透明红色实心块 +
	// 白色边框（16x16）。目的：即使桌宠内容全透明/空帧/alpha=0，只要窗口真的
	// 被系统显示出来，桌面上就一定能看到这个红块。若日志显示渲染成功但看不到
	// 红块 → 问题在"窗口不可见/被移出屏幕/DPI 虚化"，而非渲染本身。
	if petDebug {
		const ov = 16
		for row := 0; row < ov && row < bh; row++ {
			for col := 0; col < ov && col < bw; col++ {
				dst := pixels[row*bw*4+col*4:]
				edge := row == 0 || col == 0 || row == ov-1 || col == ov-1
				if edge {
					dst[0], dst[1], dst[2], dst[3] = 255, 255, 255, 255 // 白色边框（预乘后仍为白）
				} else {
					// 预乘 alpha：纯红 (R=255) 且 alpha=255 → B=0,G=0,R=255,A=255
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

	// hdcDst 传 0，让系统使用窗口当前位置而非屏幕 DC，
	// 避免渲染出覆盖整个屏幕的黑色背景层
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
		// 经典失败：DPI/多屏下窗口矩形越界、尺寸为 0、或 Blend 结构异常。
		// 必须记录 GetLastError 才能定位，否则用户只看到"服务正常但没窗口"。
		log.Printf("[Pet][ERROR] UpdateLayeredWindow FAILED: GetLastError=%d (hwnd=%x size=%dx%d took=%.2fms)",
			getLastError(), hwnd, bw, bh, float64(ulwDur.Microseconds())/1000)
	} else {
		log.Printf("[Pet] doRender: UpdateLayeredWindow OK hwnd=%x size=%dx%d took=%.2fms total=%.2fms",
			hwnd, bw, bh, float64(ulwDur.Microseconds())/1000, float64(time.Since(t0).Microseconds())/1000)
		w.renderCount++
		// 只在前 5 帧和之后每 60 帧打印一次，避免日志刷屏但仍能确认渲染在持续。
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
