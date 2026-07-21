package plugin

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	plugin "cursor/internal/plugin"
)

// Caller is an optional capability a plugin may implement so it can be invoked
// dynamically via CallPlugin. The base plugin.Plugin interface only defines
// load-time registration (Name/Version/Init); execution is opt-in so that
// plugins which only register Virtual Models/ Tools need not implement Call.
type Caller interface {
	Call(ctx context.Context, input map[string]any) (map[string]any, error)
}

// PluginFactory constructs a fresh plugin.Plugin instance by name.
type PluginFactory func() plugin.Plugin

// Entry is the combined view returned by List: a manifest plus whether it is
// currently installed (present in the store).
type Entry struct {
	Manifest
	Installed bool `json:"installed"`
}

// Runtime is the Plugin Marketplace runtime. It owns the plugin.Registry
// (shared with the virtual-model builder) and the persisted catalog, and runs
// plugin logic inside a constrained sandbox.
type Runtime struct {
	registry  *plugin.Registry
	store     *Store
	catalog   map[string]PluginFactory
	loadTo    time.Duration
	callTo    time.Duration

	mu        sync.RWMutex
	instances map[string]plugin.Plugin // loaded plugin instances (for Call/Unload)

	// closed 标记 Close 是否已调用（R14 lifecycle unification）。
	closed bool
}

// NewRuntime creates the plugin runtime and loads every enabled, already
// installed plugin from the persisted catalog.
func NewRuntime(dir string) (*Runtime, error) {
	store, err := NewStore(dir)
	if err != nil {
		return nil, err
	}
	rt := &Runtime{
		registry:  plugin.NewRegistry(),
		store:     store,
		catalog:   builtinCatalog,
		loadTo:    DefaultLoadTimeout,
		callTo:    DefaultCallTimeout,
		instances: make(map[string]plugin.Plugin),
	}
	rt.loadInstalled()
	return rt, nil
}

// Registry returns the underlying plugin.Registry so the host can hand it to
// the virtual-model manager (VMR + MOA see plugin-registered Virtual Models).
func (rt *Runtime) Registry() *plugin.Registry { return rt.registry }

// loadInstalled loads every enabled manifest recorded in the store. Failures
// are non-fatal: a broken plugin is skipped so the rest of the host still boots.
func (rt *Runtime) loadInstalled() {
	for _, m := range rt.store.List() {
		if !m.Enabled {
			continue
		}
		if _, err := rt.load(m); err != nil {
			// ponytail: skip a broken/stale plugin at startup; do not crash host.
			continue
		}
	}
}

// load constructs and registers a plugin from a manifest, adds it to the
// in-memory instance set, and persists the manifest. It is the common path for
// both startup auto-load and the Marketplace install endpoint.
func (rt *Runtime) load(m Manifest) (plugin.Plugin, error) {
	rt.mu.Lock()
	if _, ok := rt.instances[m.Name]; ok {
		rt.mu.Unlock()
		return nil, fmt.Errorf("plugin %q already loaded", m.Name)
	}
	factory, ok := rt.catalog[m.Name]
	if !ok {
		rt.mu.Unlock()
		return nil, fmt.Errorf("plugin %q not found in built-in catalog", m.Name)
	}
	instance := factory()
	rt.mu.Unlock()

	if err := runSandboxed(context.Background(), rt.loadTo, func(ctx context.Context) error {
		return rt.registry.RegisterPlugin(instance)
	}); err != nil {
		return nil, fmt.Errorf("init plugin %q: %w", m.Name, err)
	}

	rt.mu.Lock()
	rt.instances[m.Name] = instance
	rt.mu.Unlock()

	if err := rt.store.Upsert(m); err != nil {
		return nil, err
	}
	return instance, nil
}

// LoadPlugin installs and loads a plugin by name from the built-in catalog.
// manifest carries version/metadata hints; installedAt is set here, source is
// forced to "builtin" for the MVP local registry.
func (rt *Runtime) LoadPlugin(name string, manifest Manifest) (*Entry, error) {
	m := Manifest{
		Name:        name,
		Version:     manifest.Version,
		Enabled:     true,
		Source:      "builtin",
		InstalledAt: time.Now(),
		Metadata:    manifest.Metadata,
	}
	if m.Version == "" {
		if f, ok := rt.catalog[name]; ok {
			m.Version = f().Version()
		}
	}
	if _, err := rt.load(m); err != nil {
		return nil, err
	}
	return &Entry{Manifest: m, Installed: true}, nil
}

// UnloadPlugin removes a loaded plugin from the registry and the store.
func (rt *Runtime) UnloadPlugin(name string) error {
	rt.mu.Lock()
	delete(rt.instances, name)
	rt.mu.Unlock()

	rt.registry.Unregister(name)

	if _, err := rt.store.Remove(name); err != nil {
		return err
	}
	return nil
}

// Toggle flips the enabled flag of an installed plugin. A disabled plugin
// remains loaded but CallPlugin refuses to execute it.
func (rt *Runtime) Toggle(name string) (*Entry, error) {
	m, ok := rt.store.Get(name)
	if !ok {
		return nil, fmt.Errorf("plugin %q is not installed", name)
	}
	m.Enabled = !m.Enabled
	if err := rt.store.Upsert(m); err != nil {
		return nil, err
	}
	return &Entry{Manifest: m, Installed: true}, nil
}

// CallPlugin invokes a loaded, enabled, callable plugin with the given input.
func (rt *Runtime) CallPlugin(name string, input map[string]any) (map[string]any, error) {
	rt.mu.RLock()
	instance, ok := rt.instances[name]
	rt.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("plugin %q is not loaded", name)
	}
	m, _ := rt.store.Get(name)
	if m.Name != "" && !m.Enabled {
		return nil, fmt.Errorf("plugin %q is disabled", name)
	}
	caller, ok := instance.(Caller)
	if !ok {
		return nil, fmt.Errorf("plugin %q does not support Call", name)
	}

	// Capture the timeout under lock, run outside to avoid holding the mutex.
	rt.mu.RLock()
	to := rt.callTo
	rt.mu.RUnlock()

	return callSandboxed(context.Background(), to, func(ctx context.Context) (map[string]any, error) {
		return caller.Call(ctx, input)
	})
}

// UnloadAll removes every loaded plugin. Called on Host.Stop.
func (rt *Runtime) UnloadAll() {
	rt.mu.Lock()
	names := make([]string, 0, len(rt.instances))
	for n := range rt.instances {
		names = append(names, n)
	}
	rt.instances = make(map[string]plugin.Plugin)
	rt.mu.Unlock()

	for _, n := range names {
		rt.registry.Unregister(n)
	}
}

// Close unloads all plugins and marks the runtime closed. Subsequent Close
// calls are no-ops. R14: lifecycle unification.
func (rt *Runtime) Close(ctx context.Context) error {
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return nil
	}
	rt.closed = true
	rt.mu.Unlock()
	rt.UnloadAll()
	return nil
}

// IsClosed reports whether Close has been invoked on this runtime.
func (rt *Runtime) IsClosed() bool {
	if rt == nil {
		return false
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.closed
}

// List returns installed plugins plus any built-in catalog entries not yet
// installed, sorted by name for stable UI ordering.
func (rt *Runtime) List() []Entry {
	installed := make(map[string]Manifest, 0)
	for _, m := range rt.store.List() {
		installed[m.Name] = m
	}

	entries := make([]Entry, 0, len(rt.catalog)+len(installed))
	for name, m := range installed {
		entries = append(entries, Entry{Manifest: m, Installed: true})
		delete(installed, name) // guard duplicates (shouldn't happen)
	}
	for name, factory := range rt.catalog {
		if _, ok := installed[name]; ok {
			continue
		}
		v := factory().Version()
		entries = append(entries, Entry{
			Manifest: Manifest{
				Name:    name,
				Version: v,
				Enabled: false,
				Source:  "builtin",
			},
			Installed: false,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries
}