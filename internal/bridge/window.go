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

// modelEditorContext 保存当前模型编辑器窗口的初始化上下文。
type modelEditorContext struct {
	Index       int    `json:"index"`
	AdapterJSON string `json:"adapterJSON"`
}

// WindowService 定义了当前模块中的 WindowService 类型。
type WindowService struct {
	app         *application.App
	updater     *updater.Manager
	petManager  *pet.PetManager
	activePetID string
	editorCtx   *modelEditorContext
	mu          sync.RWMutex
}

func NewWindowService() *WindowService {
	// 把 pet 包的标准库 log 诊断日志重定向到 app.log，
	// 否则 GUI 下 stderr 被丢弃，开启桌宠后无法从日志排查无响应问题。
	logger.RedirectStdLog()
	return &WindowService{
		petManager: pet.NewPetManager(),
	}
}

// StopAllPets 在程序退出时统一释放所有桌宠实例，避免句柄/线程泄漏。
func (s *WindowService) StopAllPets() {
	s.mu.RLock()
	m := s.petManager
	s.mu.RUnlock()
	if m != nil {
		m.StopAll()
	}
}

// SetModelActivity 接收模型活动状态并转发给所有已注册桌宠。
// 由 runner 订阅 forwarder 事件后调用，是 pet 包与外部子系统的唯一桥接入口。
// state 取值：thinking | working | idle | error
func (s *WindowService) SetModelActivity(state string) {
	s.mu.RLock()
	m := s.petManager
	s.mu.RUnlock()
	if m == nil {
		return
	}
	m.SetActivity(pet.ActivityState(state))
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

// OpenPetWindowIfClosed 仅在桌宠未运行时才打开，返回是否实际执行了打开。
// 供前端开关确定性调用，避免 toggle 竞态。
func (s *WindowService) OpenPetWindowIfClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.petManager.Count() > 0 {
		return false
	}
	s.openPetWindowLocked()
	return true
}

// SetActivePet 设置用户当前选择的宠物 ID。
// 必须在 TogglePetWindow/OpenPetWindow 之前调用，或下次启动桌宠时生效。
func (s *WindowService) SetActivePet(petID string) {
	clean, err := pet.ValidatePetID(petID)
	if err != nil {
		logger.Warnf("rejecting SetActivePet(%q): %v", petID, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activePetID = clean
}

// openPetWindowLocked 内部方法（调用者必须持有写锁）。
// 使用原生 Win32 Layered Window 实现透明桌宠，异步启动避免阻塞。
// v2：引擎经 PetManager 注册管理，状态变化事件桥接到前端。
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

		// 步骤 1：尝试加载用户选择的宠物
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

		// 步骤 2：失败则使用嵌入资源
		if engine == nil {
			log.Println("[Pet] openPetWindow: using embedded pet")
			engine, err = pet.NewEngine(pet.EmbeddedDir)
		}
		if err != nil {
			log.Printf("[Pet] openPetWindow: engine creation failed: %v", err)
			return
		}

		// 步骤 3：经 PetManager 注册并启动（v2 多宠物管理接入点）。
		if !mgr.Register(activeID, engine) {
			log.Printf("[Pet] openPetWindow: register rejected (petID=%s already registered)", activeID)
			return
		}
		// 订阅引擎状态变化事件，桥接到前端 EventPetStateChanged。
		engine.Bus().Subscribe(pet.EventStateChanged, s.onEngineStateChanged)
		mgr.Start(activeID)
		log.Printf("[Pet] openPetWindow: pet %q registered & started via PetManager", activeID)
	}()
}

// onEngineStateChanged 将 v2 引擎 EventBus 的 state.changed 事件
// 桥接到前端可监听的 EventPetStateChanged。
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
	count := mgr.Count()
	s.mu.RUnlock()
	log.Printf("[Bridge] ClosePetWindow: activeID=%q mgr.count=%d", activeID, count)
	s.stopActivePet(mgr, activeID)
}

// TogglePetWindow 切换桌宠窗口显示。
func (s *WindowService) TogglePetWindow() bool {
	s.mu.RLock()
	mgr := s.petManager
	activeID := s.activePetID
	count := mgr.Count()
	s.mu.RUnlock()
	log.Printf("[Bridge] TogglePetWindow: activeID=%q mgr.count=%d", activeID, count)
	if count > 0 {
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
		log.Println("[Bridge] stopActivePet: mgr is nil, skip")
		return
	}
	if activeID == "" {
		activeID = pet.EmbeddedPetDir
		log.Printf("[Bridge] stopActivePet: activeID was empty, fallback to %q", activeID)
	}
	log.Printf("[Bridge] stopActivePet: calling mgr.Stop(%q)", activeID)
	ok := mgr.Stop(activeID)
	log.Printf("[Bridge] stopActivePet: mgr.Stop(%q) returned %v", activeID, ok)
}

// SwitchPet 切换到指定宠物。先关闭当前桌宠，再用新宠物启动。
// 如果新宠物加载失败，会回退到嵌入资源。
func (s *WindowService) SwitchPet(petID string) error {
	clean, err := pet.ValidatePetID(petID)
	if err != nil {
		logger.Warnf("rejecting SwitchPet(%q): %v", petID, err)
		return err
	}
	s.mu.Lock()
	s.activePetID = clean
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
