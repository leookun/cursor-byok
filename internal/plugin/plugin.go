// Package plugin provides a Plugin SDK for registering third-party
// Virtual Models, Tools, and Runtime extensions.
//
// Design: ADR-021. Research: docs/research/plugin-sdk.md.
package plugin

import (
	"context"
	"fmt"
	"sync"

	virtualmodel "cursor/internal/backend/virtualmodel"
	optimize "cursor/internal/backend/runtime/optimize"
	vm_moa "cursor/internal/backend/virtualmodel/moa"
)

// Plugin is the interface that third-party plugins must implement.
type Plugin interface {
	// Name returns the plugin's unique name.
	Name() string
	// Version returns the plugin's version string.
	Version() string
	// Init is called when the plugin is loaded, allowing it to register
	// Virtual Models, Tools, etc. via the Registry.
	Init(registry *Registry) error
}

// VirtualModelFactory creates a VirtualModel instance with the given dependencies.
type VirtualModelFactory func(channelSvc vm_moa.ChannelService, optRuntime *optimize.Runtime) virtualmodel.VirtualModel

// Registry is the central registry for plugin-registered components.
type Registry struct {
	mu            sync.Mutex
	plugins       map[string]Plugin
	virtualModels map[string]VirtualModelFactory
}

// NewRegistry creates a new plugin Registry.
func NewRegistry() *Registry {
	return &Registry{
		plugins:       make(map[string]Plugin),
		virtualModels: make(map[string]VirtualModelFactory),
	}
}

// RegisterPlugin loads and initializes a plugin.
func (r *Registry) RegisterPlugin(p Plugin) error {
	if r == nil || p == nil {
		return fmt.Errorf("registry or plugin is nil")
	}
	name := p.Name()
	if name == "" {
		return fmt.Errorf("plugin name is empty")
	}

	// Check for duplicate and store placeholder atomically.
	r.mu.Lock()
	if _, exists := r.plugins[name]; exists {
		r.mu.Unlock()
		return fmt.Errorf("plugin %q already registered", name)
	}
	r.plugins[name] = p
	r.mu.Unlock()

	// Init is called outside the lock because plugin implementations
	// typically call RegisterVirtualModel (or other locked methods) inside
	// Init. Holding r.mu here would deadlock since sync.Mutex is not
	// reentrant.
	if err := p.Init(r); err != nil {
		r.mu.Lock()
		delete(r.plugins, name)
		r.mu.Unlock()
		return fmt.Errorf("plugin %q init failed: %w", name, err)
	}

	return nil
}

// RegisterVirtualModel registers a VirtualModel factory.
func (r *Registry) RegisterVirtualModel(id string, factory VirtualModelFactory) error {
	if r == nil {
		return fmt.Errorf("registry is nil")
	}
	if id == "" {
		return fmt.Errorf("virtual model id is empty")
	}
	if factory == nil {
		return fmt.Errorf("factory is nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.virtualModels[id]; exists {
		return fmt.Errorf("virtual model %q already registered", id)
	}

	r.virtualModels[id] = factory
	return nil
}

// GetVirtualModelFactory returns the factory for a registered VirtualModel.
func (r *Registry) GetVirtualModelFactory(id string) (VirtualModelFactory, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	factory, ok := r.virtualModels[id]
	return factory, ok
}

// ListVirtualModels returns all registered VirtualModel IDs.
func (r *Registry) ListVirtualModels() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.virtualModels))
	for id := range r.virtualModels {
		ids = append(ids, id)
	}
	return ids
}

// Unregister removes a plugin from the registry and clears the plugin-owned
// VirtualModel map. Used by the Marketplace runtime on uninstall/Stop.
// Safe to call for an unknown name (no-op).
//
// ponytail: VirtualModel factories are not owner-tagged, so a targeted unload
// clears the whole plugin VM map (only plugin registered VMs live here — MOA
// and AOS register into vm.Manager, not this registry). UnloadAll runs at
// Host.Stop so a full clear is always safe there. Upgrade path: tag factories
// with an owner plugin name and delete by owner for surgical targeted unload.
func (r *Registry) Unregister(name string) {
	if r == nil || name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.plugins, name)
	r.virtualModels = make(map[string]VirtualModelFactory)
}

// ListPlugins returns all registered plugin names.
func (r *Registry) ListPlugins() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.plugins))
	for name := range r.plugins {
		names = append(names, name)
	}
	return names
}

// CreateVirtualModels creates all registered VirtualModel instances
// using the given dependencies and returns them.
func (r *Registry) CreateVirtualModels(channelSvc vm_moa.ChannelService, optRuntime *optimize.Runtime) []virtualmodel.VirtualModel {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	models := make([]virtualmodel.VirtualModel, 0, len(r.virtualModels))
	for _, factory := range r.virtualModels {
		model := factory(channelSvc, optRuntime)
		if model != nil {
			models = append(models, model)
		}
	}
	return models
}

// PluginCount returns the number of registered plugins.
func (r *Registry) PluginCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.plugins)
}

// Ensure context is imported (used by plugin authors in Init)
var _ = context.Background
