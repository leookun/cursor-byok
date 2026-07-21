package plugin

import (
	"context"
	"fmt"
	"sort"
	"strings"

	plugin "cursor/internal/plugin"
	virtualmodel "cursor/internal/backend/virtualmodel"
	optimize "cursor/internal/backend/runtime/optimize"
	vm_moa "cursor/internal/backend/virtualmodel/moa"
)

// builtinCatalog maps plugin names to factories for the MVP local registry.
// These are trusted, in-process plugins demonstrating the SDK + sandbox path.
// Remote registry / .so / WASM loading is future work (ADR-047).
var builtinCatalog = map[string]PluginFactory{
	"echo":      func() plugin.Plugin { return &echoPlugin{} },
	"text-stats": func() plugin.Plugin { return &textStatsPlugin{} },
	"moa-bridge": func() plugin.Plugin { return &moaBridgePlugin{} },
}

// echoPlugin is a minimal callable plugin (no Virtual Model registration).
type echoPlugin struct{}

func (p *echoPlugin) Name() string    { return "echo" }
func (p *echoPlugin) Version() string { return "1.0.0" }
func (p *echoPlugin) Init(r *plugin.Registry) error {
	return nil // nothing to register
}
func (p *echoPlugin) Call(ctx context.Context, input map[string]any) (map[string]any, error) {
	text, _ := input["text"].(string)
	return map[string]any{"echo": text}, nil
}

// textStatsPlugin computes simple stats over an input string.
type textStatsPlugin struct{}

func (p *textStatsPlugin) Name() string    { return "text-stats" }
func (p *textStatsPlugin) Version() string { return "1.0.0" }
func (p *textStatsPlugin) Init(r *plugin.Registry) error {
	return nil
}
func (p *textStatsPlugin) Call(ctx context.Context, input map[string]any) (map[string]any, error) {
	text, _ := input["text"].(string)
	words := strings.Fields(text)
	chars := len([]rune(text))
	return map[string]any{
		"chars": len(text),
		"runes": chars,
		"words": len(words),
	}, nil
}

// moaBridgePlugin demonstrates a plugin registering a Virtual Model factory
// into the shared registry, and additionally exposing a callable summary.
type moaBridgePlugin struct{}

func (p *moaBridgePlugin) Name() string    { return "moa-bridge" }
func (p *moaBridgePlugin) Version() string { return "1.0.0" }
func (p *moaBridgePlugin) Init(r *plugin.Registry) error {
	return r.RegisterVirtualModel("moa-bridge-vm", func(cs vm_moa.ChannelService, opt *optimize.Runtime) virtualmodel.VirtualModel {
		return &bridgeVM{}
	})
}
func (p *moaBridgePlugin) Call(ctx context.Context, input map[string]any) (map[string]any, error) {
	return map[string]any{"registered": "moa-bridge-vm"}, nil
}

// bridgeVM is a trivial VirtualModel so the registry path is exercised.
type bridgeVM struct{}

func (m *bridgeVM) ID() string          { return "moa-bridge-vm" }
func (m *bridgeVM) DisplayName() string { return "MOA Bridge VM" }
func (m *bridgeVM) Enabled() bool       { return true }

// AdapterMetadata 演示用，无物理 adapter 可继承，返回空元数据。
func (m *bridgeVM) AdapterMetadata(_ context.Context) virtualmodel.AdapterMetadata {
	return virtualmodel.AdapterMetadata{}
}

func (m *bridgeVM) Execute(ctx context.Context, req *virtualmodel.ExecuteRequest) (*virtualmodel.ExecuteResult, error) {
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}
	return &virtualmodel.ExecuteResult{Text: "moa-bridge response"}, nil
}

// sortedCatalogNames returns built-in catalog names in sorted order (stable).
func sortedCatalogNames() []string {
	names := make([]string, 0, len(builtinCatalog))
	for n := range builtinCatalog {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}