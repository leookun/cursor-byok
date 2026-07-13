package backend

import (
	"context"
	"testing"
	"time"

	serverconfig "cursor/internal/backend/server/config"
	optimize "cursor/internal/backend/runtime/optimize"
	vmconfig "cursor/internal/backend/virtualmodel/config"
	vm_moa "cursor/internal/backend/virtualmodel/moa"
	legacyruntime "cursor/internal/runtime"
)

// testChannelResolver 满足 moa.ChannelResolver（与 serverconfig.Manager 同契约）。
type testChannelResolver struct {
	lastModelID string
}

func (r *testChannelResolver) SelectChannelForModel(_ context.Context, modelID string) (*legacyruntime.ResolvedChannel, error) {
	r.lastModelID = modelID
	return &legacyruntime.ResolvedChannel{
		ID:       modelID,
		Name:     "test-adapter",
		Provider: "openai",
		Model:    "gpt-4o-mini",
		BaseURL:  "https://example.invalid",
		APIKey:   "test-key",
	}, nil
}

func (r *testChannelResolver) ProviderStreamIdleTimeout(context.Context) time.Duration {
	return 30 * time.Second
}

// TestBuildVirtualModelManager_WiresNonNilChannelService 断言生产装配不为 MOA 注入 nil ChannelService。
func TestBuildVirtualModelManager_WiresNonNilChannelService(t *testing.T) {
	cfg := serverconfig.DefaultConfig()
	cfg.VirtualModels = serverconfig.VirtualModelsConfig{
		MOA: &serverconfig.VirtualModelConfig{
			Enabled:    true,
			WorkflowID: "moa-default",
			Planner: &serverconfig.VirtualModelNodeBindingConfig{
				AdapterID: "adapter-coding",
				Enabled:   true,
			},
			Nodes: map[string]*serverconfig.VirtualModelNodeBindingConfig{
				"coding": {AdapterID: "adapter-coding", Enabled: true},
			},
		},
	}
	opt := optimize.NewRuntime(50, optimize.TierBalanced)
	resolver := &testChannelResolver{}

	mgr := buildVirtualModelManager(&cfg, opt, resolver)
	vmModel, ok := mgr.Get(vmconfig.MOAModelID)
	if !ok || vmModel == nil {
		t.Fatal("MOA not registered when VirtualModels.MOA.Enabled")
	}
	moaModel, ok := vmModel.(*vm_moa.MOAModel)
	if !ok {
		t.Fatalf("registered type %T, want *moa.MOAModel", vmModel)
	}
	if !moaModel.HasChannelService() {
		t.Fatal("buildVirtualModelManager must inject non-nil ChannelService (AdapterChannelService over ModelAdapter resolver)")
	}

	// 通过真实 AdapterChannelService 路径解析 adapter（驱动 shipped ResolveChannel）
	info, err := moaModel.ResolveChannelForTest(context.Background(), "adapter-coding")
	if err != nil {
		t.Fatalf("ResolveChannel via injected service: %v", err)
	}
	if info == nil || info.ModelID != "gpt-4o-mini" {
		t.Fatalf("unexpected channel info: %+v", info)
	}
	if resolver.lastModelID != "adapter-coding" {
		t.Fatalf("resolver not consulted, last=%q", resolver.lastModelID)
	}
}

func TestBuildVirtualModelManager_NilResolverLeavesNoService(t *testing.T) {
	cfg := serverconfig.DefaultConfig()
	cfg.VirtualModels = serverconfig.VirtualModelsConfig{
		MOA: &serverconfig.VirtualModelConfig{Enabled: true},
	}
	mgr := buildVirtualModelManager(&cfg, nil, nil)
	vmModel, ok := mgr.Get(vmconfig.MOAModelID)
	if !ok {
		t.Fatal("expected MOA registered")
	}
	moaModel := vmModel.(*vm_moa.MOAModel)
	if moaModel.HasChannelService() {
		t.Fatal("nil channelResolver should not inject service")
	}
}
