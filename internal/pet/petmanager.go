package pet

import (
	"log"
	"sync"
)

// PetInstance 是多宠物管理器管理的实例接口。
// *Engine 天然满足（拥有 Start/Stop/IsReady），便于测试用 fake 注入。
type PetInstance interface {
	Start()
	Stop()
	IsReady() bool
}

// PetManager 管理多个桌宠实例（v2 Phase 12）。
//
// 设计目标：
//   - 支持同时运行多只宠物（不同 petID 对应不同 Engine/资源）。
//   - 并发安全：bridge/前端可在任意线程调用注册/获取/停止。
//   - 生命周期统一：StopAll 在程序退出时一次性释放所有实例，避免泄漏。
//   - 防重复：同一 petID 重复注册会被拒绝（返回 false），避免双开。
type PetManager struct {
	mu   sync.RWMutex
	pets map[string]PetInstance
}

// NewPetManager 创建管理器。
func NewPetManager() *PetManager {
	return &PetManager{pets: make(map[string]PetInstance)}
}

// Register 注册一个宠物实例。petID 重复时返回 false（不覆盖）。
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

// Get 获取指定 petID 的实例。
func (m *PetManager) Get(petID string) (PetInstance, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.pets[petID]
	return p, ok
}

// List 返回当前所有 petID。
func (m *PetManager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.pets))
	for id := range m.pets {
		ids = append(ids, id)
	}
	return ids
}

// Start 启动指定实例；不存在返回 false。
func (m *PetManager) Start(petID string) bool {
	p, ok := m.Get(petID)
	if !ok {
		return false
	}
	p.Start()
	return true
}

// Stop 停止并移除指定实例；不存在返回 false。
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

// StopAll 停止并移除所有实例（程序退出时调用，避免泄漏）。
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

// Count 返回当前实例数量。
func (m *PetManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.pets)
}
