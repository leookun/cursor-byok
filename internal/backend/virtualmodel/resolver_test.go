package virtualmodel

import (
	"context"
	"testing"

	legacyruntime "cursor/internal/runtime"
	vmconfig "cursor/internal/backend/virtualmodel/config"
)

// recursiveAdapterSvc simulates host.serverSystemSettings: ResolveModelAdapters
// merges virtual models by calling BuildVirtualModelAdapterConfigs again.
// Before the fix, BuildVirtualModelAdapterConfigs called ResolveFallbackAdapterID
// which called this → infinite recursion / OS freeze when Cursor pulled models.
type recursiveAdapterSvc struct {
	r     *VMResolver
	depth int
	max   int
}

func (s *recursiveAdapterSvc) ResolveModelAdapters(ctx context.Context) ([]legacyruntime.ModelAdapterConfig, error) {
	s.depth++
	if s.depth > s.max {
		tPanic("ResolveModelAdapters re-entered too many times — AvailableModels recursion")
	}
	physical := []legacyruntime.ModelAdapterConfig{{
		ID: "phys-1", DisplayName: "Physical", Type: "openai", ModelID: "gpt", APIKey: "k", BaseURL: "http://x",
	}}
	// Same merge path as host.serverSystemSettings.ResolveModelAdapters
	return s.r.MergeVirtualModelAdapters(ctx, physical), nil
}

func tPanic(msg string) { panic(msg) }

type stubVM struct {
	id      string
	name    string
	enabled bool
}

func (s *stubVM) ID() string          { return s.id }
func (s *stubVM) DisplayName() string { return s.name }
func (s *stubVM) Enabled() bool       { return s.enabled }
func (s *stubVM) Execute(context.Context, *ExecuteRequest) (*ExecuteResult, error) {
	return nil, nil
}
func (s *stubVM) AdapterMetadata(context.Context) AdapterMetadata {
	return AdapterMetadata{TooltipData: "tooltip-" + s.id}
}

func TestBuildVirtualModelAdapterConfigs_NoRecursionOnAvailableModelsPath(t *testing.T) {
	mgr := NewManager()
	_ = mgr.Register(&stubVM{id: "aos", name: "AOS", enabled: true})
	_ = mgr.Register(&stubVM{id: vmconfig.MOAModelID, name: "MOA", enabled: true})

	r := NewVMResolver(mgr, nil)
	svc := &recursiveAdapterSvc{max: 3}
	// Wire the same cycle host uses: adapterSvc → Merge → Build
	r.adapterSvc = svc
	svc.r = r

	// This is the AvailableModels entrypoint shape.
	out, err := svc.ResolveModelAdapters(context.Background())
	if err != nil {
		t.Fatalf("ResolveModelAdapters: %v", err)
	}
	if len(out) < 3 {
		t.Fatalf("expected virtual+physical adapters, got %d", len(out))
	}
	// Virtual models first
	if out[0].ID != "aos" && out[0].ID != vmconfig.MOAModelID {
		t.Fatalf("expected virtual model first, got %q", out[0].ID)
	}
	// Must have non-zero context window defaults
	for _, a := range out {
		if a.Type == VirtualAdapterType && a.ContextWindowTokens <= 0 {
			t.Fatalf("virtual %q ContextWindowTokens must be > 0 for Cursor", a.ID)
		}
	}
}

func TestBuildVirtualModelAdapterConfigs_SkipsDisabled(t *testing.T) {
	mgr := NewManager()
	_ = mgr.Register(&stubVM{id: "aos", name: "AOS", enabled: false})
	r := NewVMResolver(mgr, nil)
	out := r.BuildVirtualModelAdapterConfigs(context.Background())
	if len(out) != 0 {
		t.Fatalf("disabled VMs must not appear, got %d", len(out))
	}
}
