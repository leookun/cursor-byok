package backend

import (
	"context"
	"testing"
	"time"

	"cursor/internal/backend/forwarder"
	cacheruntime "cursor/internal/backend/runtime/cache"
	contextruntime "cursor/internal/backend/runtime/context"
	pluginruntime "cursor/internal/backend/runtime/plugin"
	telemetryruntime "cursor/internal/backend/runtime/telemetry"
	optimize "cursor/internal/backend/runtime/optimize"
	toolruntime "cursor/internal/backend/runtime/tool"
	vm "cursor/internal/backend/virtualmodel"
)

// TestHost_Stop_ClosesRuntimes verifies that Host.Stop closes every runtime
// that holds resources (cache/context/optimize/telemetry/plugin/tool/forwarder),
// not just the HTTP server. See R14 (lifecycle unification batch).
//
// TODO: enable -race in CI once CGO is available (R17 spec).
func TestHost_Stop_ClosesRuntimes(t *testing.T) {
	host := newHostWithRuntimesForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := host.Stop(ctx); err != nil {
		t.Fatalf("Host.Stop err=%v", err)
	}

	state := host.runtimeStateSnapshot()
	if state.cacheRuntime == nil || !state.cacheRuntime.IsClosed() {
		t.Fatal("cacheRuntime not closed by Host.Stop")
	}
	if state.contextRT == nil || !state.contextRT.IsClosed() {
		t.Fatal("contextRT not closed by Host.Stop")
	}
	if state.optRuntime == nil || !state.optRuntime.IsClosed() {
		t.Fatal("optRuntime not closed by Host.Stop")
	}
	if state.telemetryRuntime == nil || !state.telemetryRuntime.IsClosed() {
		t.Fatal("telemetryRT not closed by Host.Stop")
	}
	if state.pluginRT == nil || !state.pluginRT.IsClosed() {
		t.Fatal("pluginRT not closed by Host.Stop")
	}
}

// TestHost_Stop_ClosesForwarderService verifies the forwarder Service is shut
// down before HTTP server closes (R15 wiring sanity check from R14 lens).
func TestHost_Stop_ClosesForwarderService(t *testing.T) {
	host := newHostWithRuntimesForTest(t)
	if host.agentModule == nil || host.agentModule.Service == nil {
		t.Fatal("agentModule/Service not wired on Host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := host.Stop(ctx); err != nil {
		t.Fatalf("Host.Stop err=%v", err)
	}
	if !host.agentModule.Service.IsShutdown() {
		t.Fatal("forwarder Service not shut down by Host.Stop")
	}
}

// newHostWithRuntimesForTest constructs a Host with all runtimes populated to
// real (lightweight) instances against temp dirs, but without starting the
// HTTP server. This lets us assert lifecycle behavior in isolation.
func newHostWithRuntimesForTest(t *testing.T) *Host {
	t.Helper()
	cacheRT, err := cacheruntime.NewRuntime(t.TempDir())
	if err != nil {
		t.Fatalf("cache runtime: %v", err)
	}
	contextRT, err := contextruntime.NewRuntime(t.TempDir())
	if err != nil {
		t.Fatalf("context runtime: %v", err)
	}
	optRT := optimize.NewRuntimeWithStore(0, optimize.TierBalanced, "")
	telemetryRT, err := telemetryruntime.NewRuntime(t.TempDir())
	if err != nil {
		t.Fatalf("telemetry runtime: %v", err)
	}
	pluginRT, err := pluginruntime.NewRuntime(t.TempDir())
	if err != nil {
		t.Fatalf("plugin runtime: %v", err)
	}
	toolRT := toolruntime.NewRuntime()
	toolRT.RegisterBuiltinTools()

	// Construct a forwarder Module so Host.Stop can exercise the Service
	// shutdown wiring (R15). We pass a fresh VM manager and nil resolver —
	// the Service is not used for live traffic in this test, only lifecycle.
	vmManager := vm.NewManager()
	historyRoot := t.TempDir()
	module := forwarder.NewModuleWithRuntimes(historyRoot, nil, vmManager, optRT, cacheRT, toolRT, contextRT)

	host := &Host{agentModule: module}
	host.swapRuntimeState(hostRuntimeState{
		cacheRuntime:     cacheRT,
		contextRT:        contextRT,
		optRuntime:       optRT,
		telemetryRuntime: telemetryRT,
		pluginRT:         pluginRT,
		toolRuntime:      toolRT,
	})
	return host
}
