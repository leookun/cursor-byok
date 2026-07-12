package pet

// Lifecycle 是桌宠引擎组件的统一生命周期契约（Phase 8）。
// 所有由 Engine（Composition Root）装配的核心模块都应实现该接口，
// 使启动、停止、释放过程可编排、可审计，避免生命周期代码散落在各处。
type Lifecycle interface {
	// Start 初始化并启动组件。调用时所有依赖已通过构造函数注入完毕。
	Start()
	// Stop 停止组件运行。应取消订阅、停止定时器、结束内部 goroutine，
	// 但通常保留可重启状态；与 Dispose 区分在于 Stop 后可再次 Start。
	Stop()
	// Dispose 释放组件持有的资源。组件被 Dispose 后不应再使用。
	Dispose()
}

// LifecycleManager 按注册顺序启动组件，按相反顺序停止/释放组件。
// 它本身也实现 Lifecycle，可作为整体被 Engine 管理。
type LifecycleManager struct {
	components []Lifecycle
}

// NewLifecycleManager 创建生命周期管理器。
func NewLifecycleManager() *LifecycleManager {
	return &LifecycleManager{}
}

// Register 按启动顺序注册一个生命周期组件。
func (lm *LifecycleManager) Register(c Lifecycle) {
	if c == nil || lm == nil {
		return
	}
	lm.components = append(lm.components, c)
}

// Start 按注册顺序启动所有组件。
func (lm *LifecycleManager) Start() {
	if lm == nil {
		return
	}
	for _, c := range lm.components {
		if c != nil {
			c.Start()
		}
	}
}

// Stop 按注册相反顺序停止所有组件。
func (lm *LifecycleManager) Stop() {
	if lm == nil {
		return
	}
	for i := len(lm.components) - 1; i >= 0; i-- {
		if c := lm.components[i]; c != nil {
			c.Stop()
		}
	}
}

// Dispose 按注册相反顺序释放所有组件。
func (lm *LifecycleManager) Dispose() {
	if lm == nil {
		return
	}
	for i := len(lm.components) - 1; i >= 0; i-- {
		if c := lm.components[i]; c != nil {
			c.Dispose()
		}
	}
}

// Ensure LifecycleManager 自身也实现 Lifecycle。
var _ Lifecycle = (*LifecycleManager)(nil)
