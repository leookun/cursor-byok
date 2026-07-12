package pet

import (
	"log"
	"sync"
)

// Plugin 是桌宠插件接口（v2 Phase 10）。
//
// 插件通过 PluginAPI 获得对引擎的安全访问（事件总线、状态机、调度器），
// 实现"订阅事件 + 注入行为"的扩展模式，而无需修改引擎核心代码。
// 所有插件回调都在引擎线程执行（经 Scheduler 派发），天然无 data race。
type Plugin interface {
	// Name 返回插件唯一名（用于日志/去重）。
	Name() string
	// Init 在引擎启动后调用，插件在此订阅事件、注册意图修饰器等。
	Init(api PluginAPI) error
	// Dispose 在引擎停止前调用，插件应在此退订/清理。
	Dispose()
}

// PluginAPI 是插件访问引擎能力的门面（Phase 7.5 解耦版）。
// 不再暴露 *Engine，也不再提供 Post；插件应通过 Schedule+EventBus 与引擎交互。
type PluginAPI interface {
	// Bus 返回事件总线，插件可订阅/发布事件。
	Bus() *EventBus
	// FSM 返回状态机，插件可查询/切换状态。
	FSM() *StateMachine
	// RequestIntent 请求 Behavior 应用一个意图（如让宠物挥手）。
	RequestIntent(it Intent)
	// Resolver 返回动画解析器（插件可注册新状态->动画映射）。
	Resolver() *AnimationResolver
	// RegisterOwner 在调度器注册一个任务所有者，返回句柄供 Schedule 使用。
	RegisterOwner(name string, priority int) OwnerHandle
	// UnregisterOwner 注销所有者并取消其所有任务。
	UnregisterOwner(h OwnerHandle)
	// Schedule 按 TaskSpec 调度任务；任务最终经 Scheduler 派发到引擎线程执行。
	Schedule(spec TaskSpec) (cancel func())
}

// pluginAPIImpl 是 PluginAPI 的默认实现，仅依赖 Scheduler/EventBus/FSM/Resolver。
type pluginAPIImpl struct {
	sched    *Scheduler
	bus      *EventBus
	fsm      *StateMachine
	resolver *AnimationResolver
}

// newPluginAPI 创建插件 API 实现。
func newPluginAPI(sched *Scheduler, bus *EventBus, fsm *StateMachine, resolver *AnimationResolver) PluginAPI {
	return &pluginAPIImpl{
		sched:    sched,
		bus:      bus,
		fsm:      fsm,
		resolver: resolver,
	}
}

func (p *pluginAPIImpl) Bus() *EventBus               { return p.bus }
func (p *pluginAPIImpl) FSM() *StateMachine           { return p.fsm }
func (p *pluginAPIImpl) Resolver() *AnimationResolver { return p.resolver }

func (p *pluginAPIImpl) RequestIntent(it Intent) {
	if p.bus == nil {
		return
	}
	p.bus.Publish(Event{Type: EventRequestIntent, Data: it})
}

func (p *pluginAPIImpl) RegisterOwner(name string, priority int) OwnerHandle {
	if p.sched == nil {
		return OwnerHandle(0)
	}
	return p.sched.RegisterOwner(Owner{Name: name, Priority: priority})
}

func (p *pluginAPIImpl) UnregisterOwner(h OwnerHandle) {
	if p.sched == nil {
		return
	}
	p.sched.UnregisterOwner(h)
}

func (p *pluginAPIImpl) Schedule(spec TaskSpec) func() {
	if p.sched == nil {
		return func() {}
	}
	return p.sched.Schedule(spec)
}

// PluginManager 管理插件生命周期（v2 Phase 10）。
type PluginManager struct {
	mu      sync.Mutex
	plugins []Plugin
	api     PluginAPI
	started bool
}

// NewPluginManager 创建插件管理器。
func NewPluginManager(api PluginAPI) *PluginManager {
	if api == nil {
		api = &pluginAPIImpl{}
	}
	return &PluginManager{api: api}
}

// Register 注册插件。若引擎已启动则立即 Init。
func (m *PluginManager) Register(p Plugin) error {
	m.mu.Lock()
	for _, existing := range m.plugins {
		if existing.Name() == p.Name() {
			m.mu.Unlock()
			return nil // 同名忽略，避免重复注册
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

// StartAll 初始化并启动所有已注册插件。
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

// StopAll 停止并释放所有插件（兼容旧调用）。
func (m *PluginManager) StopAll() {
	m.Stop()
}

// Start 实现 Lifecycle，初始化并启动所有已注册插件。
func (m *PluginManager) Start() {
	m.StartAll()
}

// Stop 实现 Lifecycle，停止并释放所有插件。
func (m *PluginManager) Stop() {
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

// Dispose 实现 Lifecycle，释放插件管理器资源。
func (m *PluginManager) Dispose() {
	m.Stop()
}

// Ensure PluginManager implements Lifecycle.
var _ Lifecycle = (*PluginManager)(nil)

// Count 返回当前已注册的插件数量。
func (m *PluginManager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.plugins)
}
