package backend

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cursor/internal/appdata"
	cacheruntime "cursor/internal/backend/runtime/cache"
	"cursor/internal/backend/runtime/evolver"
	optimize "cursor/internal/backend/runtime/optimize"
	toolruntime "cursor/internal/backend/runtime/tool"
	virtualmodel "cursor/internal/backend/virtualmodel"
	vm "cursor/internal/backend/virtualmodel"
	"cursor/internal/docguard"
)

func TestResolveRepoRootForEvolution_FromWorkingDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	want, err := docguard.RepoRoot(wd)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	got, err := resolveRepoRootForEvolution()
	if err != nil {
		t.Fatalf("resolveRepoRootForEvolution: %v", err)
	}
	if got != want {
		t.Fatalf("root = %q, want %q", got, want)
	}
	// Sanity: handbook must exist for background diagnosis to proceed.
	if st, err := os.Stat(filepath.Join(got, "docs", "handbook")); err != nil || !st.IsDir() {
		t.Fatalf("handbook missing under %s: %v", got, err)
	}
}

func TestRunBackgroundEvolutionCheck_NoPanic(t *testing.T) {
	// Full Evolve+Persist path: should log and return without panicking.
	host := &Host{}
	host.runBackgroundEvolutionCheck()
}

func TestRuntimeMetricSnapshot_WithInitializedRuntimeDependencies(t *testing.T) {
	cacheRT, err := cacheruntime.NewRuntime(t.TempDir())
	if err != nil {
		t.Fatalf("NewRuntime(cache): %v", err)
	}
	messages := []cacheruntime.Message{{Role: "user", Content: "hello"}}
	if err := cacheRT.Store(messages, "", "test-model", "agent", "cached", 7, 3, time.Hour); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, _, hit := cacheRT.Lookup(messages, "", "test-model", "agent"); !hit {
		t.Fatal("expected cache hit")
	}

	toolRT := toolruntime.NewRuntime()
	toolRT.RegisterBuiltinTools()
	toolRT.SetBridges(&hostEvolverExecStub{}, nil)
	args := []byte(`{"path":"/tmp/test.go"}`)
	if _, err := toolRT.Execute(context.Background(), "read_file", args); err != nil {
		t.Fatalf("first tool Execute: %v", err)
	}
	if _, err := toolRT.Execute(context.Background(), "read_file", args); err != nil {
		t.Fatalf("second tool Execute: %v", err)
	}

	optRT := optimize.NewRuntime(42, optimize.TierBalanced)
	optRT.RecordCost("gpt-4o", 1000, 1000)

	host := &Host{cacheRuntime: cacheRT, toolRuntime: toolRT, optRuntime: optRT}
	snap := host.evolutionRuntimeMetricSnapshot()
	if snap == nil {
		t.Fatal("expected snapshot")
	}
	if !snap.HasCache || snap.CacheExactHits != 1 || snap.CacheTokensSaved != 3 || snap.CacheHitRate <= 0 {
		t.Fatalf("unexpected cache metrics: %+v", snap)
	}
	if !snap.HasToolCache || snap.ToolCacheHits != 1 || snap.ToolCacheMisses != 1 || snap.ToolCacheHitRate <= 0 {
		t.Fatalf("unexpected tool cache metrics: %+v", snap)
	}
	if !snap.HasOptimize || snap.OptimizeTurns != 1 || snap.OptimizeSpentUSD <= 0 || snap.OptimizeBudgetUSD != 42 {
		t.Fatalf("unexpected optimize metrics: %+v", snap)
	}
}

func TestRuntimeMetricSnapshot_WithNilRuntimeDependencies(t *testing.T) {
	snap := (*Host)(nil).evolutionRuntimeMetricSnapshot()
	if snap == nil {
		t.Fatal("expected snapshot")
	}
	if snap.HasCache || snap.HasToolCache || snap.HasOptimize {
		t.Fatalf("expected no evidence from nil host, got %+v", snap)
	}

	host := &Host{}
	snap = host.evolutionRuntimeMetricSnapshot()
	if snap == nil {
		t.Fatal("expected snapshot")
	}
	if snap.HasCache || snap.HasToolCache || snap.HasOptimize {
		t.Fatalf("expected no evidence from nil dependencies, got %+v", snap)
	}
}

func TestExportEvolutionRuntimeMetrics_UsesHostSnapshot(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	optRT := optimize.NewRuntime(7, optimize.TierBalanced)
	optRT.RecordCost("gpt-4o-mini", 1000, 1000)
	host := &Host{optRuntime: optRT}

	if err := host.exportEvolutionRuntimeMetrics(); err != nil {
		t.Fatalf("exportEvolutionRuntimeMetrics: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(appdata.DataRootPath(), "runtime-metrics", "current.json"))
	if err != nil {
		t.Fatalf("ReadFile(current.json): %v", err)
	}
	var got evolver.RuntimeMetricSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal(current.json): %v", err)
	}
	if !got.HasOptimize || got.OptimizeBudgetUSD != 7 || got.OptimizeTurns != 1 {
		t.Fatalf("unexpected exported snapshot: %+v", got)
	}
}

func TestExportEvolutionRuntimeMetrics_SkipsWithoutEvidence(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	if err := (&Host{}).exportEvolutionRuntimeMetrics(); err != nil {
		t.Fatalf("exportEvolutionRuntimeMetrics: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appdata.DataRootPath(), "runtime-metrics", "current.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no runtime metric export, stat err=%v", err)
	}
}

func TestRunBackgroundEvolutionCheck_CallsRuntimeMetricExport(t *testing.T) {
	data, err := os.ReadFile("host_evolver.go")
	if err != nil {
		t.Fatalf("ReadFile(host_evolver.go): %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "host.exportEvolutionRuntimeMetrics()") {
		t.Fatal("runBackgroundEvolutionCheck must call host.exportEvolutionRuntimeMetrics before Evolve")
	}
	if !strings.Contains(text, "evolver.WriteRuntimeMetricExports(appdata.DataRootPath(), snap)") {
		t.Fatal("exportEvolutionRuntimeMetrics must use the canonical WriteRuntimeMetricExports call site")
	}
}

func TestPreferredEvolutionModel_PrefersAOS(t *testing.T) {
	manager := vm.NewManager()
	_ = manager.Register(&stubVirtualModel{id: "moa", enabled: true})
	_ = manager.Register(&stubVirtualModel{id: "aos", enabled: true})
	host := &Host{vmManager: manager}
	got := host.preferredEvolutionModel()
	if got == nil || got.ID() != "aos" {
		t.Fatalf("expected aos model, got %#v", got)
	}
}

type stubVirtualModel struct {
	id      string
	enabled bool
}

func (m *stubVirtualModel) ID() string          { return m.id }
func (m *stubVirtualModel) DisplayName() string { return m.id }
func (m *stubVirtualModel) Enabled() bool       { return m.enabled }
func (m *stubVirtualModel) Execute(context.Context, *virtualmodel.ExecuteRequest) (*virtualmodel.ExecuteResult, error) {
	return &virtualmodel.ExecuteResult{Text: "ok"}, nil
}

type hostEvolverExecStub struct{}

func (s *hostEvolverExecStub) OpenExec(toolName string, argsJSON []byte) (string, []byte, error) {
	return "exec-1", []byte(`{"result":"file content"}`), nil
}
