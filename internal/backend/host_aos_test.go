package backend

import (
	"context"
	"net/http"
	"reflect"
	"sync"
	"testing"

	serverconfig "cursor/internal/backend/server/config"
	vm "cursor/internal/backend/virtualmodel"
	vm_aos "cursor/internal/backend/virtualmodel/aos"
)

func TestConvertAOSTeamConfigPreservesExecutionMode(t *testing.T) {
	team := convertAOSTeamConfig(&serverconfig.AOSConfig{
		ExecutionMode: "internal",
		Leader:        serverconfig.AOSLeaderConfig{AdapterID: "leader"},
	})
	if team.ExecutionMode != serverconfig.AOSExecutionModeInternal {
		t.Fatalf("team execution mode = %q, want %q", team.ExecutionMode, serverconfig.AOSExecutionModeInternal)
	}
}

func TestBuildVirtualModelManagerRegistersEnabledAOSOnceAndInjectsManager(t *testing.T) {
	cfg := serverconfig.DefaultConfig()
	cfg.VirtualModels.AOS = &serverconfig.AOSConfig{Enabled: true}
	mgr := buildVirtualModelManager(&cfg, nil, nil)
	models := mgr.List()
	count := 0
	for _, model := range models {
		if model.ID() == vm_aos.ModelID {
			count++
			assertAOSManager(t, model, mgr)
		}
	}
	if count != 1 {
		t.Fatalf("AOS registration count = %d, want 1", count)
	}
}

func TestBuildVirtualModelManagerSkipsDisabledAOS(t *testing.T) {
	cfg := serverconfig.DefaultConfig()
	cfg.VirtualModels.AOS = &serverconfig.AOSConfig{Enabled: false}
	mgr := buildVirtualModelManager(&cfg, nil, nil)
	if _, ok := mgr.Get(vm_aos.ModelID); ok {
		t.Fatal("disabled AOS model was registered")
	}
}

func TestHostSaveConfigReplacesAOSModelWhileRunning(t *testing.T) {
	store := serverconfig.NewStore(t.TempDir()+"\\config.yaml", t.TempDir())
	configs, err := serverconfig.NewManager(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	initial := serverconfig.DefaultConfig()
	initial.VirtualModels.AOS = &serverconfig.AOSConfig{
		Enabled: true,
		Leader:  serverconfig.AOSLeaderConfig{AdapterID: "leader-before"},
	}
	mgr := buildVirtualModelManager(&initial, nil, configs)
	host := &Host{configs: configs, vmManager: mgr, httpServer: &http.Server{}}

	updated := initial
	updated.VirtualModels.AOS = &serverconfig.AOSConfig{
		Enabled:       true,
		ExecutionMode: "internal",
		Leader:        serverconfig.AOSLeaderConfig{AdapterID: "leader-after"},
	}
	if _, err := host.SaveConfig(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	model, ok := mgr.Get(vm_aos.ModelID)
	if !ok {
		t.Fatal("AOS model missing after config save")
	}
	aosModel := model.(*vm_aos.AOSModel)
	if aosModel.LeaderAdapterID() != "leader-after" {
		t.Fatalf("leader adapter = %q, want leader-after", aosModel.LeaderAdapterID())
	}
	assertAOSManager(t, model, mgr)
}

func TestHostSaveConfigDisablesAndReenablesAOSWhileRunning(t *testing.T) {
	store := serverconfig.NewStore(t.TempDir()+"\\config.yaml", t.TempDir())
	configs, err := serverconfig.NewManager(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	initial := serverconfig.DefaultConfig()
	initial.VirtualModels.AOS = &serverconfig.AOSConfig{
		Enabled: true,
		Leader:  serverconfig.AOSLeaderConfig{AdapterID: "leader-before"},
	}
	mgr := buildVirtualModelManager(&initial, nil, configs)
	host := &Host{configs: configs, vmManager: mgr, httpServer: &http.Server{}}

	disabled := initial
	disabled.VirtualModels.AOS = &serverconfig.AOSConfig{
		Enabled:       false,
		ExecutionMode: "internal",
		Leader:        serverconfig.AOSLeaderConfig{AdapterID: "leader-disabled"},
	}
	if _, err := host.SaveConfig(context.Background(), disabled); err != nil {
		t.Fatal(err)
	}
	current := configs.Current()
	if current.VirtualModels.AOS == nil || current.VirtualModels.AOS.Enabled {
		t.Fatal("disabled AOS config was not persisted")
	}
	if current.VirtualModels.AOS.ExecutionMode != serverconfig.AOSExecutionModeInternal {
		t.Fatalf("execution mode = %q, want %q", current.VirtualModels.AOS.ExecutionMode, serverconfig.AOSExecutionModeInternal)
	}
	if _, ok := mgr.Get(vm_aos.ModelID); ok {
		t.Fatal("AOS model remained registered after disable")
	}

	reenabled := disabled
	reenabled.VirtualModels.AOS = &serverconfig.AOSConfig{
		Enabled:       true,
		ExecutionMode: "internal",
		Leader:        serverconfig.AOSLeaderConfig{AdapterID: "leader-after"},
	}
	if _, err := host.SaveConfig(context.Background(), reenabled); err != nil {
		t.Fatal(err)
	}
	model, ok := mgr.Get(vm_aos.ModelID)
	if !ok {
		t.Fatal("AOS model missing after re-enable")
	}
	aosModel := model.(*vm_aos.AOSModel)
	if aosModel.LeaderAdapterID() != "leader-after" {
		t.Fatalf("leader adapter = %q, want leader-after", aosModel.LeaderAdapterID())
	}
	assertAOSManager(t, model, mgr)
}

func TestHostSaveConfigSerializesConcurrentAOSReplacement(t *testing.T) {
	store := serverconfig.NewStore(t.TempDir()+"\\config.yaml", t.TempDir())
	configs, err := serverconfig.NewManager(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	initial := serverconfig.DefaultConfig()
	initial.VirtualModels.AOS = &serverconfig.AOSConfig{
		Enabled: true,
		Leader:  serverconfig.AOSLeaderConfig{AdapterID: "leader-initial"},
	}
	mgr := buildVirtualModelManager(&initial, nil, configs)
	host := &Host{configs: configs, vmManager: mgr, httpServer: &http.Server{}}

	first := initial
	first.VirtualModels.AOS = &serverconfig.AOSConfig{
		Enabled: true,
		Leader:  serverconfig.AOSLeaderConfig{AdapterID: "leader-first"},
	}
	second := initial
	second.VirtualModels.AOS = &serverconfig.AOSConfig{
		Enabled: true,
		Leader:  serverconfig.AOSLeaderConfig{AdapterID: "leader-second"},
	}

	firstSaveEntered := make(chan struct{})
	releaseFirstSave := make(chan struct{})
	var blockFirst sync.Once
	secondSaveAtBoundary := make(chan struct{})
	var observeSecond sync.Once
	host.configSaveMu.waitHook = func(cfg serverconfig.Config) {
		if cfg.VirtualModels.AOS == nil || cfg.VirtualModels.AOS.Leader.AdapterID != "leader-second" {
			return
		}
		observeSecond.Do(func() { close(secondSaveAtBoundary) })
	}
	configs.Subscribe(func(next serverconfig.Config) {
		if next.VirtualModels.AOS == nil || next.VirtualModels.AOS.Leader.AdapterID != "leader-first" {
			return
		}
		blockFirst.Do(func() {
			close(firstSaveEntered)
			<-releaseFirstSave
		})
	})

	firstDone := make(chan error, 1)
	go func() {
		_, err := host.SaveConfig(context.Background(), first)
		firstDone <- err
	}()
	<-firstSaveEntered

	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		_, err := host.SaveConfig(context.Background(), second)
		secondDone <- err
	}()
	<-secondStarted
	<-secondSaveAtBoundary
	select {
	case err := <-firstDone:
		t.Fatalf("first SaveConfig completed before release: %v", err)
	default:
	}
	select {
	case err := <-secondDone:
		t.Fatalf("second SaveConfig completed before first release: %v", err)
	default:
	}
	close(releaseFirstSave)
	if err := <-firstDone; err != nil {
		t.Fatalf("first SaveConfig: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second SaveConfig: %v", err)
	}
	if current := configs.Current(); current.VirtualModels.AOS == nil || current.VirtualModels.AOS.Leader.AdapterID != "leader-second" {
		t.Fatalf("config manager leader = %q, want leader-second", aosLeaderAdapterID(current.VirtualModels.AOS))
	}

	model, ok := mgr.Get(vm_aos.ModelID)
	if !ok {
		t.Fatal("AOS model missing after concurrent config saves")
	}
	aosModel := model.(*vm_aos.AOSModel)
	if aosModel.LeaderAdapterID() != "leader-second" {
		t.Fatalf("leader adapter = %q, want leader-second", aosModel.LeaderAdapterID())
	}
}

func aosLeaderAdapterID(cfg *serverconfig.AOSConfig) string {
	if cfg == nil {
		return ""
	}
	return cfg.Leader.AdapterID
}

func assertAOSManager(t *testing.T, model vm.VirtualModel, want *vm.Manager) {
	t.Helper()
	aosModel, ok := model.(*vm_aos.AOSModel)
	if !ok {
		t.Fatalf("model type = %T, want *aos.AOSModel", model)
	}
	field := reflect.ValueOf(aosModel).Elem().FieldByName("vmManager")
	if !field.IsValid() || field.IsNil() || field.Pointer() != reflect.ValueOf(want).Pointer() {
		t.Fatal("AOS model does not reference the registered virtual-model manager")
	}
}
