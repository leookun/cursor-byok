package backend

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"path/filepath"

	"cursor/internal/appdata"
	"cursor/internal/backend/forwarder"
	cacheruntime "cursor/internal/backend/runtime/cache"
	contextruntime "cursor/internal/backend/runtime/context"
	"cursor/internal/backend/runtime/embedding"
	optimize "cursor/internal/backend/runtime/optimize"
	pluginruntime "cursor/internal/backend/runtime/plugin"
	telemetryruntime "cursor/internal/backend/runtime/telemetry"
	toolruntime "cursor/internal/backend/runtime/tool"
	workflowruntime "cursor/internal/backend/runtime/workflow"
	"cursor/internal/backend/server"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/backend/server/upstream"
	vm "cursor/internal/backend/virtualmodel"
	vm_aos "cursor/internal/backend/virtualmodel/aos"
	vmconfig "cursor/internal/backend/virtualmodel/config"
	vm_moa "cursor/internal/backend/virtualmodel/moa"
	"cursor/internal/logger"
	"cursor/internal/netproxy"
	legacyruntime "cursor/internal/runtime"
)

const healthPath = "/healthz"

const tabServerBaseURL = "https://tab.leokun.cn"

type Host struct {
	store      *serverconfig.Store
	listenAddr string
	configs    *serverconfig.Manager
	healthHTTP *http.Client

	// configSaveMu serializes a config persistence operation with the runtime
	// update it requires. It is intentionally independent from runMu: manager
	// listeners run synchronously during Save and may use runtime state.
	configSaveMu saveConfigGate

	runMu      sync.RWMutex
	httpServer *http.Server

	lastRunErr error

	mux http.Handler

	// Runtime instances are rebuilt with the request mux and swapped together.
	runtimeMu sync.RWMutex
	runtimes  hostRuntimeState

	// vmManager is rebuilt with the request mux; used for optional Evolver benchmarks.
	vmMu      sync.RWMutex
	vmManager *vm.Manager

	// agentModule is the forwarder.Module bound to the current runtime set.
	// Host.Stop closes its underlying Service (history-maintenance goroutine,
	// stream broker, etc.) before tearing down the HTTP server. R14.
	agentModule *forwarder.Module

	// evolutionDone tracks the background self-evolution goroutine launched by
	// Host.Start so Host.Stop can wait for it to drain. R17 lifecycle unification.
	evolutionDone chan struct{}

	// onModelActivity 是模型活动状态回调（由 runner 注入，桥接到 pet）。
	// forwarder 在 stream 关键节点调用它，state 取值：thinking|working|idle|error。
	// Host 不直接依赖 pet 包，仅做转发，保持低耦合。
	onModelActivityMu sync.RWMutex
	onModelActivity   func(state string)
}

// saveConfigGate keeps production on the standard mutex path. Tests may set
// waitHook to observe a caller only after it has joined the private waiter
// queue; that path uses the same standard-library synchronization primitives
// without exposing a public locking API.
type saveConfigGate struct {
	mu       sync.Mutex
	cond     *sync.Cond
	held     bool
	waiters  int
	waitHook func(serverconfig.Config)
}

func (gate *saveConfigGate) Lock(cfg serverconfig.Config) {
	if gate.waitHook == nil {
		gate.mu.Lock()
		return
	}

	gate.mu.Lock()
	if gate.cond == nil {
		gate.cond = sync.NewCond(&gate.mu)
	}
	if !gate.held {
		gate.held = true
		gate.mu.Unlock()
		return
	}
	gate.waiters++

	// The waiter is now committed to the serialized queue before the hook can
	// signal the test. Cond.Wait below is the actual blocking wait.
	gate.waitHook(cfg)
	for gate.held {
		gate.cond.Wait()
	}
	gate.waiters--
	gate.held = true
	gate.mu.Unlock()
}

func (gate *saveConfigGate) Unlock() {
	if gate.waitHook == nil {
		gate.mu.Unlock()
		return
	}

	gate.mu.Lock()
	gate.held = false
	if gate.waiters > 0 {
		gate.cond.Signal()
	}
	gate.mu.Unlock()
}

type hostRuntimeState struct {
	cacheRuntime     *cacheruntime.Runtime
	toolRuntime      *toolruntime.Runtime
	optRuntime       *optimize.Runtime
	telemetryRuntime *telemetryruntime.Runtime
	// R14 lifecycle unification: the following runtimes were previously local
	// to rebuildLocked and never closed. Host.Stop / swapRuntimeState now close
	// them in reverse construction order.
	contextRT   *contextruntime.Runtime
	pluginRT    *pluginruntime.Runtime
}

func NewHost(store *serverconfig.Store) (*Host, error) {
	if store == nil {
		return nil, fmt.Errorf("backend config store is required")
	}
	configs, err := serverconfig.NewManager(context.Background(), store)
	if err != nil {
		return nil, err
	}
	cfg := configs.Current()
	host := &Host{
		store:      store,
		listenAddr: cfg.BackendListenAddr,
		configs:    configs,
		healthHTTP: newLoopbackHTTPClient(),
	}
	if err := host.rebuild(cfg); err != nil {
		return nil, err
	}
	// 配置热更新：Optimization Tier/Budget 原地生效（无需停服）
	configs.Subscribe(func(next serverconfig.Config) {
		host.applyOptimizationConfig(next.Optimization)
	})
	return host, nil
}

// GetCostSummary 返回 Optimization Runtime 的进程内成本摘要。
func (host *Host) GetCostSummary() *optimize.CostTracker {
	if host == nil {
		return &optimize.CostTracker{}
	}
	rt := host.runtimeStateSnapshot().optRuntime
	if rt == nil {
		return &optimize.CostTracker{}
	}
	return rt.GetCostSummary()
}

// OptimizationRuntime 返回当前 Optimization Runtime（只读用途）。
func (host *Host) OptimizationRuntime() *optimize.Runtime {
	if host == nil {
		return nil
	}
	return host.runtimeStateSnapshot().optRuntime
}

// CacheRuntime 返回当前 Cache Runtime（供前端 Dashboard / IPC 读取统计）。
func (host *Host) CacheRuntime() *cacheruntime.Runtime {
	if host == nil {
		return nil
	}
	return host.runtimeStateSnapshot().cacheRuntime
}

// ToolRuntime 返回当前 Tool Runtime（供前端工具管理页 IPC）。
func (host *Host) ToolRuntime() *toolruntime.Runtime {
	if host == nil {
		return nil
	}
	return host.runtimeStateSnapshot().toolRuntime
}

// TelemetryRuntime 返回当前 Telemetry Runtime（供 REST / IPC 读取每日摘要）。
func (host *Host) TelemetryRuntime() *telemetryruntime.Runtime {
	if host == nil {
		return nil
	}
	return host.runtimeStateSnapshot().telemetryRuntime
}

// VirtualModelManager 返回当前 Virtual Model 管理器（供 Replay / 遥测 IPC）。
func (host *Host) VirtualModelManager() *vm.Manager {
	if host == nil {
		return nil
	}
	host.vmMu.RLock()
	defer host.vmMu.RUnlock()
	return host.vmManager
}

func (host *Host) applyOptimizationConfig(cfg serverconfig.OptimizationConfig) {
	if host == nil {
		return
	}
	normalized := serverconfig.NormalizeOptimizationConfig(cfg)
	rt := host.runtimeStateSnapshot().optRuntime
	if rt == nil {
		return
	}
	rt.SetEnabled(normalized.Enabled)
	rt.SetQualityTier(optimize.QualityTier(normalized.QualityTier))
	rt.SetMonthlyBudgetUSD(normalized.MonthlyBudgetUSD)
}

func (host *Host) runtimeStateSnapshot() hostRuntimeState {
	if host == nil {
		return hostRuntimeState{}
	}
	host.runtimeMu.RLock()
	defer host.runtimeMu.RUnlock()
	return host.runtimes
}

func (host *Host) swapRuntimeState(next hostRuntimeState) {
	host.runtimeMu.Lock()
	prev := host.runtimes
	host.runtimes = next
	host.runtimeMu.Unlock()
	// Close the previous runtime set in reverse construction order so file
	// handles / SQLite connections / goroutines are released before the new
	// set re-opens equivalent resources. Best-effort: errors are logged.
	// R14: lifecycle unification.
	closeRuntimeState(context.Background(), prev, "swapRuntimeState")
}

func (host *Host) ConfigManager() *serverconfig.Manager {
	if host == nil {
		return nil
	}
	return host.configs
}

func (host *Host) LoadConfig(ctx context.Context) (serverconfig.Config, error) {
	if host == nil || host.configs == nil {
		return serverconfig.DefaultConfig(), nil
	}
	return host.configs.Load(ctx)
}

func (host *Host) SaveConfig(ctx context.Context, cfg serverconfig.Config) (serverconfig.Config, error) {
	if host == nil || host.configs == nil {
		return serverconfig.Config{}, fmt.Errorf("backend config manager is not initialized")
	}
	host.configSaveMu.Lock(cfg)
	defer host.configSaveMu.Unlock()

	normalized, err := host.configs.Save(ctx, cfg)
	if err != nil {
		return serverconfig.Config{}, err
	}

	host.runMu.Lock()
	running := host.httpServer != nil
	if !running {
		if rebuildErr := host.rebuildLocked(normalized); rebuildErr != nil {
			host.runMu.Unlock()
			return serverconfig.Config{}, rebuildErr
		}
	}
	host.runMu.Unlock()

	if running {
		if err := host.replaceAOSModel(normalized); err != nil {
			return serverconfig.Config{}, err
		}
	}
	return normalized, nil
}

func (host *Host) ListenAddr() string {
	if host == nil {
		return ""
	}
	host.runMu.RLock()
	defer host.runMu.RUnlock()
	return host.listenAddr
}

func (host *Host) BaseURL() string {
	listenAddr := strings.TrimSpace(host.ListenAddr())
	if listenAddr == "" {
		return ""
	}
	return "http://" + listenAddr
}

func (host *Host) IsRunning() bool {
	if host == nil {
		return false
	}
	host.runMu.RLock()
	defer host.runMu.RUnlock()
	return host.httpServer != nil
}

func (host *Host) LastRunError() error {
	if host == nil {
		return nil
	}
	host.runMu.RLock()
	defer host.runMu.RUnlock()
	return host.lastRunErr
}

func (host *Host) Start() error {
	if host == nil {
		return fmt.Errorf("backend host is nil")
	}
	cfg := host.configs.Current()

	host.runMu.Lock()
	defer host.runMu.Unlock()
	if host.httpServer != nil {
		return fmt.Errorf("backend is already running")
	}
	if err := host.rebuildLocked(cfg); err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              host.listenAddr,
		Handler:           host.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	listener, err := net.Listen("tcp", host.listenAddr)
	if err != nil {
		host.lastRunErr = fmt.Errorf("监听内置后端 %s 失败: %w", host.listenAddr, err)
		return host.lastRunErr
	}
	host.listenAddr = listener.Addr().String()
	host.httpServer = httpServer
	host.lastRunErr = nil
	logger.Infof("内置后端监听成功 listen_addr=%s", host.listenAddr)

	go func(serverInstance *http.Server, serverListener net.Listener) {
		// R17: lifecycle unification — recover so a Serve panic does not
		// take down the whole process. The goroutine is already managed
		// via httpServer.Shutdown; this only adds the panic guard.
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf("内置后端 Serve goroutine panicked: %v", r)
			}
		}()
		logger.Infof("内置后端开始提供服务 listen_addr=%s", serverListener.Addr().String())
		if err := serverInstance.Serve(serverListener); err != nil && err != http.ErrServerClosed {
			runErr := fmt.Errorf("内置后端在 %s 上异常退出: %w", serverListener.Addr().String(), err)
			host.runMu.Lock()
			if host.httpServer == serverInstance {
				host.httpServer = nil
			}
			host.lastRunErr = runErr
			host.runMu.Unlock()
			logger.Errorf("%v", runErr)
		}
	}(httpServer, listener)
	// Self-evolution: non-blocking read-only diagnosis (ADR-028). Never blocks serve.
	// R17: track the goroutine via evolutionDone so Host.Stop can wait for it
	// before tearing down runtimes (the evolver touches runtime snapshots).
	host.evolutionDone = make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf("host: runBackgroundEvolutionCheck panicked: %v", r)
			}
		}()
		host.runBackgroundEvolutionCheck()
	}(host.evolutionDone)
	return nil
}

// SetModelActivityCallback 注册模型活动状态回调。
// forwarder 在 stream 关键节点（思考/正文/完成/错误）调用它通知外部。
// 由 runner 注入，桥接到 pet.PetManager.SetActivity。
// 线程安全：可在任意时刻调用；若 forwarder Module 已存在，会同步转发给其 Service。
func (host *Host) SetModelActivityCallback(fn func(state string)) {
	if host == nil {
		return
	}
	host.onModelActivityMu.Lock()
	host.onModelActivity = fn
	host.onModelActivityMu.Unlock()
	// 若 forwarder Module 已构建，同步注入；否则在下次 buildRuntimes 时注入。
	host.runMu.RLock()
	module := host.agentModule
	host.runMu.RUnlock()
	if module != nil && module.Service != nil {
		module.Service.SetModelActivityCallback(fn)
	}
}

func (host *Host) Stop(ctx context.Context) error {
	if host == nil {
		return nil
	}
	host.runMu.Lock()
	serverInstance := host.httpServer
	host.httpServer = nil
	evolutionDone := host.evolutionDone
	host.evolutionDone = nil
	host.runMu.Unlock()

	// 1) Stop the forwarder Service first: this cancels the history-maintenance
	//    goroutine and the stream broker (R15/R17). Best-effort, log errors.
	if host.agentModule != nil && host.agentModule.Service != nil {
		if err := host.agentModule.Service.Shutdown(ctx); err != nil {
			logger.Errorf("forwarder service shutdown error: %v", err)
		}
	}

	// 2) Close all runtimes in reverse construction order (R14). Best-effort,
	//    each error is logged and swallowed so a single bad runtime does not
	//    block HTTP server shutdown.
	closeRuntimeState(ctx, host.runtimeStateSnapshot(), "Host.Stop")

	// 3) Wait for the background self-evolution goroutine to drain so we do
	//    not leave it writing to disk after the process appears to have exited.
	//    R17: lifecycle unification.
	if evolutionDone != nil {
		select {
		case <-evolutionDone:
		case <-time.After(5 * time.Second):
			logger.Warnf("host: background evolution goroutine did not exit within 5s; proceeding")
		}
	}

	if serverInstance == nil {
		return nil
	}
	err := serverInstance.Shutdown(ctx)
	return err
}

func (host *Host) HealthCheck(ctx context.Context) error {
	if host == nil {
		return fmt.Errorf("backend host is nil")
	}
	if runErr := host.LastRunError(); runErr != nil {
		return runErr
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, host.BaseURL()+healthPath, nil)
	if err != nil {
		return err
	}
	client := host.healthHTTP
	if client == nil {
		client = newLoopbackHTTPClient()
	}
	response, err := client.Do(request)
	if err != nil {
		inProcessErr := host.InProcessHealthCheck()
		if inProcessErr == nil {
			logger.Errorf("内置后端进程内健康检查成功，但 loopback 访问失败 base_url=%s err=%v", host.BaseURL(), err)
			return fmt.Errorf("内置后端进程内健康检查成功，但本机 loopback 访问失败: %w", err)
		}
		logger.Errorf("内置后端 loopback 与进程内健康检查均失败 loopback_err=%v in_process_err=%v", err, inProcessErr)
		if runErr := host.LastRunError(); runErr != nil {
			return runErr
		}
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("内置后端健康检查返回状态码 %d", response.StatusCode)
	}
	return nil
}

func (host *Host) InProcessHealthCheck() error {
	if host == nil {
		return fmt.Errorf("backend host is nil")
	}
	if host.mux == nil {
		return fmt.Errorf("backend handler is nil")
	}
	request := httptest.NewRequest(http.MethodGet, "http://inprocess"+healthPath, nil)
	recorder := httptest.NewRecorder()
	host.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		return fmt.Errorf("in-process health status %d", recorder.Code)
	}
	body := strings.TrimSpace(recorder.Body.String())
	if body != "ok" {
		return fmt.Errorf("in-process health body %q", body)
	}
	logger.Infof("内置后端进程内健康检查成功")
	return nil
}

func newLoopbackHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout:   1 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:   false,
			MaxIdleConns:        1,
			MaxIdleConnsPerHost: 1,
			IdleConnTimeout:     30 * time.Second,
		},
	}
}

func (host *Host) rebuild(cfg serverconfig.Config) error {
	host.runMu.Lock()
	defer host.runMu.Unlock()
	return host.rebuildLocked(cfg)
}

func (host *Host) rebuildLocked(cfg serverconfig.Config) error {
	host.listenAddr = cfg.BackendListenAddr

	// 构建 Tool Runtime（统一工具注册与管理）
	toolRT := toolruntime.NewRuntime()
	toolRT.RegisterBuiltinTools()

	// 构建 Cache Runtime（精确缓存 + 语义缓存）
	cacheRuntime, err := cacheruntime.NewRuntime(appdata.DataRootPath())
	if err != nil {
		return fmt.Errorf("create cache runtime: %w", err)
	}

	// 构建 Context Runtime（上下文构建/压缩/排序/窗口管理/记忆注入）
	contextRT, err := contextruntime.NewRuntime(appdata.DataRootPath())
	if err != nil {
		return fmt.Errorf("create context runtime: %w", err)
	}

	// Wire APIEmbedder for real semantic search (ADR-025).
	// Scans user-configured ModelAdapters for the first OpenAI-compatible one
	// and creates a FallbackEmbedder (APIEmbedder + SimpleEmbedder fallback).
	wireEmbedder(cfg, cacheRuntime, contextRT)

	// 构建 Optimization Runtime（Token Budget + Cost Optimizer）— 从用户配置读取
	// 始终创建实例以支持热切换 Enabled；策略关闭时 AllocateBudget 不覆盖 max tokens。
	optCfg := serverconfig.NormalizeOptimizationConfig(cfg.Optimization)
	tier := optimize.QualityTier(optCfg.QualityTier)
	if tier == "" {
		tier = optimize.TierBalanced
	}
	// 跨进程恢复 spent/turns（ADR-010）；路径在 appdata data 根下。
	optRuntime := optimize.NewRuntimeWithStore(optCfg.MonthlyBudgetUSD, tier, optimize.DefaultCostStorePath())
	optRuntime.SetEnabled(optCfg.Enabled)

	// Telemetry Runtime（每日摘要 REST）
	telemetryRT, err := telemetryruntime.NewRuntime(filepath.Join(appdata.DataRootPath(), "telemetry"))
	if err != nil {
		return fmt.Errorf("create telemetry runtime: %w", err)
	}

	// Workflow store + REST handler（前端 WorkflowEditor）
	workflowStore, err := workflowruntime.NewStore(filepath.Join(appdata.DataRootPath(), "workflow"))
	if err != nil {
		return fmt.Errorf("create workflow store: %w", err)
	}
	workflowHandler := workflowruntime.NewHandler(workflowStore)

	// Plugin Marketplace REST handler（前端 Plugins 页）
	pluginRT, err := pluginruntime.NewRuntime(filepath.Join(appdata.DataRootPath(), "plugin"))
	if err != nil {
		return fmt.Errorf("create plugin runtime: %w", err)
	}
	pluginHandler := pluginruntime.NewHandler(pluginRT)

	cacheHandler := cacheruntime.NewHandler(cacheRuntime)
	toolHandler := toolruntime.NewHandler(toolRT)
	telemetryHandler := telemetryruntime.NewHandler(telemetryRT)

	host.swapRuntimeState(hostRuntimeState{
		cacheRuntime:     cacheRuntime,
		toolRuntime:      toolRT,
		optRuntime:       optRuntime,
		telemetryRuntime: telemetryRT,
		contextRT:        contextRT,
		pluginRT:         pluginRT,
	})

	// 构建 Virtual Model Runtime 并注册内置虚拟模型（MOA）
	// host.configs 实现 SelectChannelForModel + ProviderStreamIdleTimeout，供 MOA 专家解析已有 ModelAdapter
	vmManager := buildVirtualModelManager(&cfg, optRuntime, host.configs)
	host.vmMu.Lock()
	host.vmManager = vmManager
	host.vmMu.Unlock()

	agentModule := forwarder.NewModuleWithRuntimes(appdata.HistoryRootPath(), host.configs, vmManager, optRuntime, cacheRuntime, toolRT, contextRT)
	// Track the active forwarder module so Host.Stop can shut down its
	// underlying Service (history maintenance goroutine + stream broker).
	// R14/R15: lifecycle unification.
	host.agentModule = agentModule
	// 注入模型活动状态回调：forwarder 在 stream 关键节点（思考/正文/完成/错误）
	// 调用它通知外部。Host 仅做转发，不依赖 pet 包（保持低耦合）。
	if host.onModelActivity != nil {
		agentModule.Service.SetModelActivityCallback(host.onModelActivity)
	}
	legacyBidiAppendProcedure := "/aiserver.v1.BidiService/BidiAppend"
	legacyRunSSEProcedure := "/agent.v1.AgentService/RunSSE"
	routeDeps := upstream.Dependencies{
		SystemSettingService: &serverSystemSettings{configs: host.configs, vmManager: vmManager},
		HTTPClient:           netproxy.NewHTTPClient(30000 * time.Second),
	}

	// Flatten runtime REST handlers into server.New option list.
	routeOpts := []server.Option{
		server.Use(
			server.Recover(),
			server.ServerContext(),
			server.PolicyMiddleware(host.configs),
			server.ErrorEncoder(),
		),
		server.GET(healthPath,
			server.Name("healthz"),
			server.HTTP(),
			server.Local(server.Health()),
		),
	}
	routeOpts = append(routeOpts, workflowHandler.Routes()...)
	routeOpts = append(routeOpts, pluginHandler.Routes()...)
	routeOpts = append(routeOpts, cacheHandler.Routes()...)
	routeOpts = append(routeOpts, toolHandler.Routes()...)
	routeOpts = append(routeOpts, telemetryHandler.Routes()...)

	host.mux = server.New(append(routeOpts,
		server.POST(legacyBidiAppendProcedure,
			server.Name("bidi_append"),
			server.ConnectUnary(),
			server.Local(server.HTTPHandlerAction(agentModule.LocalBidiHandler)),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "bidi_append",
			})),
		),
		server.POST(legacyRunSSEProcedure,
			server.Name("run_sse"),
			server.ConnectStream(),
			server.Local(server.HTTPHandlerAction(agentModule.LocalRunSSE)),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "run_sse",
			})),
		),
		server.POST("/aiserver.v1.AiService/ServerTime",
			server.Name("server_time"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "server_time",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.ServerTimeResponse",
				MockBuilder:   upstream.ServerTimeMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "server_time",
			})),
		),
		server.POST("/aiserver.v1.AiService/GetServerConfig",
			server.Name("server_config"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "server_config",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetServerConfigResponse",
				MockBuilder:   upstream.ServerConfigMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "server_config",
			})),
		),
		server.POST("/aiserver.v1.AiService/AvailableModels",
			server.Name("available_models"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "available_models",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.AvailableModelsResponse",
				MockBuilder:   upstream.AvailableModelsMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "available_models",
			})),
		),
		server.POST("/aiserver.v1.AiService/GetDefaultModelNudgeData",
			server.Name("default_model_nudge"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "default_model_nudge",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetDefaultModelNudgeDataResponse",
				MockBuilder:   upstream.DefaultModelNudgeMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "default_model_nudge",
			})),
		),
		server.POST("/aiserver.v1.AnalyticsService/BootstrapStatsig",
			server.Name("bootstrap_statsig"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "bootstrap_statsig",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.BootstrapStatsigResponse",
				MockBuilder:   upstream.BootstrapStatsigMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "bootstrap_statsig",
			})),
		),
		server.POST("/aiserver.v1.AnalyticsService/GetFirstWindowStatsigDecision",
			server.Name("first_window_statsig_decision"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "first_window_statsig_decision",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetFirstWindowStatsigDecisionResponse",
				MockBuilder:   upstream.FirstWindowStatsigDecisionMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "first_window_statsig_decision",
			})),
		),
		server.POST("/oauth/token",
			server.Name("oauth_token"),
			server.HTTP(),
			server.Local(upstream.MockOAuthAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "oauth_token",
				StatusCode: http.StatusOK,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "oauth_token",
			})),
		),
		server.POST("/aiserver.v1.AuthService/GetEmail",
			server.Name("auth_service_get_email"),
			server.ConnectUnary(),
			server.Local(upstream.MockAuthEmailAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "auth_service_get_email",
				StatusCode: http.StatusOK,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "auth_service_get_email",
			})),
		),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/StreamCpp", "ai_stream_cpp", server.ConnectStream(), routeDeps),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/StreamNextCursorPrediction", "ai_stream_next_cursor_prediction", server.ConnectStream(), routeDeps),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/GetCppEditClassification", "ai_get_cpp_edit_classification", server.ConnectUnary(), routeDeps),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/RefreshTabContext", "ai_refresh_tab_context", server.ConnectUnary(), routeDeps),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/CppConfig", "ai_cpp_config", server.ConnectUnary(), routeDeps),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/CppEditHistoryStatus", "ai_cpp_edit_history_status", server.ConnectUnary(), routeDeps),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/CppAppend", "ai_cpp_append", server.ConnectUnary(), routeDeps),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/CppEditHistoryAppend", "ai_cpp_edit_history_append", server.ConnectUnary(), routeDeps),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/ReportAiCodeChangeMetrics", "ai_report_ai_code_change_metrics", server.ConnectUnary(), routeDeps),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/WriteGitCommitMessage", "ai_write_git_commit_message", server.ConnectUnary(), routeDeps),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/WriteGitBranchName", "ai_write_git_branch_name", server.ConnectUnary(), routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceFastRepoInitHandshakeV2Procedure, "repository_fast_repo_init_handshake_v2", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceFastRepoInitHandshakeProcedure, "repository_fast_repo_init_handshake", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceFastRepoSyncCompleteProcedure, "repository_fast_repo_sync_complete", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceSyncMerkleSubtreeV2Procedure, "repository_sync_merkle_subtree_v2", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceSyncMerkleSubtreeProcedure, "repository_sync_merkle_subtree", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceFastUpdateFileV2Procedure, "repository_fast_update_file_v2", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceFastUpdateFileProcedure, "repository_fast_update_file", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceEnsureIndexCreatedProcedure, "repository_ensure_index_created", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceGetCopyStatusProcedure, "repository_get_copy_status", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceGetUploadLimitsProcedure, "repository_get_upload_limits", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceGetNumFilesToSendProcedure, "repository_get_num_files_to_send", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceGetAvailableChunkingStrategiesProcedure, "repository_get_available_chunking_strategies", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceGetHighLevelFolderDescriptionProcedure, "repository_get_high_level_folder_description", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceRepositoryStatusProcedure, "repository_status", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceBatchRepositoryStatusProcedure, "repository_batch_status", server.ConnectUnary(), agentModule, routeDeps),
		uploadServiceProcedure(forwarder.UploadServiceUploadDocumentationProcedure, "upload_documentation", server.ConnectUnary(), agentModule, routeDeps),
		uploadServiceProcedure(forwarder.UploadServiceGetDocProcedure, "upload_get_doc", server.ConnectUnary(), agentModule, routeDeps),
		uploadServiceProcedure(forwarder.UploadServiceGetPagesProcedure, "upload_get_pages", server.ConnectUnary(), agentModule, routeDeps),
		uploadServiceProcedure(forwarder.UploadServiceUploadedStatusProcedure, "upload_uploaded_status", server.ConnectUnary(), agentModule, routeDeps),
		server.Any("/aiserver.v1.AiService/*",
			server.Name("ai_service"),
			server.HTTP(),
			server.Local(server.HTTPHandlerAction(agentModule.AiHandler)),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "ai_service",
			})),
		),
		tabServerUpstreamProcedure("/aiserver.v1.CppService/AvailableModels", "cpp_available_models", server.ConnectUnary(), routeDeps),
		tabServerUpstreamProcedure("/aiserver.v1.CppService/RecordCppFate", "cpp_record_cpp_fate", server.ConnectUnary(), routeDeps),
		server.Any("/aiserver.v1.CppService/*",
			server.Name("cpp_service"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "cpp_service",
			})),
		),
		tabServerUpstreamProcedure("/aiserver.v1.FileSyncService/FSSyncFile", "file_sync_sync_file", server.ConnectUnary(), routeDeps),
		tabServerUpstreamProcedure("/aiserver.v1.FileSyncService/FSIsEnabledForUser", "file_sync_is_enabled_for_user", server.ConnectUnary(), routeDeps),
		tabServerUpstreamProcedure("/aiserver.v1.FileSyncService/FSConfig", "file_sync_config", server.ConnectUnary(), routeDeps),
		tabServerUpstreamProcedure("/aiserver.v1.FileSyncService/FSUploadFile", "file_sync_upload_file", server.ConnectUnary(), routeDeps),
		server.Any("/aiserver.v1.FileSyncService/*",
			server.Name("file_sync"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "file_sync",
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetTokenUsage",
			server.Name("dashboard_token_usage"),
			server.HTTP(),
			server.Local(server.HTTPHandlerAction(agentModule.AiHandler)),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_token_usage",
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetGlassEarlyPreviewEnrollment",
			server.Name("dashboard_glass_early_preview_enrollment"),
			server.ConnectUnary(),
			server.Local(server.HTTPHandlerAction(agentModule.AiHandler)),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_glass_early_preview_enrollment",
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetCurrentPeriodUsage",
			server.Name("dashboard_current_period_usage"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_current_period_usage",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetCurrentPeriodUsageResponse",
				MockBuilder:   upstream.DashboardCurrentPeriodUsageMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_current_period_usage",
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetTeams",
			server.Name("dashboard_get_teams"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_get_teams",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetTeamsResponse",
				MockBuilder:   upstream.DashboardTeamsMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_get_teams",
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetManagedSkills",
			server.Name("dashboard_get_managed_skills"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_get_managed_skills",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetManagedSkillsResponse",
				MockBuilder:   upstream.DashboardManagedSkillsMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_get_managed_skills",
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetMe",
			server.Name("dashboard_get_me"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_get_me",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetMeResponse",
				MockBuilder:   upstream.DashboardGetMeMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_get_me",
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetUserPrivacyMode",
			server.Name("dashboard_user_privacy_mode"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_user_privacy_mode",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetUserPrivacyModeResponse",
				MockBuilder:   upstream.DashboardUserPrivacyModeMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_user_privacy_mode",
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetPlanInfo",
			server.Name("dashboard_plan_info"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_plan_info",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetPlanInfoResponse",
				MockBuilder:   upstream.DashboardPlanInfoMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_plan_info",
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetUsageLimitStatusAndActiveGrants",
			server.Name("dashboard_usage_limit_status"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_usage_limit_status",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetUsageLimitStatusAndActiveGrantsResponse",
				MockBuilder:   upstream.DashboardUsageLimitStatusAndActiveGrantsMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_usage_limit_status",
			})),
		),
		server.POST("/aiserver.v1.DashboardService/IsOnNewPricing",
			server.Name("dashboard_is_on_new_pricing"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_is_on_new_pricing",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.IsOnNewPricingResponse",
				MockBuilder:   upstream.DashboardIsOnNewPricingMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_is_on_new_pricing",
			})),
		),
		// tabServerUpstreamProcedure("/aiserver.v1.DashboardService/GetEffectiveUserPlugins", "dashboard_get_effective_user_plugins", server.ConnectUnary(), routeDeps),
		server.Any("/aiserver.v1.DashboardService/*",
			server.Name("dashboard"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard",
			})),
		),
		server.Any("/aiserver.v1.NetworkService/*",
			server.Name("network_service"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "network_service",
			})),
		),
		server.Any("/aiserver.v1.InAppAdService/*",
			server.Name("in_app_ad"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "in_app_ad",
			})),
		),
		server.GET("/auth/full_stripe_profile",
			server.Name("auth_full_stripe_profile"),
			server.HTTP(),
			server.Local(upstream.MockAuthFullStripeProfileAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "auth_full_stripe_profile",
				StatusCode: http.StatusOK,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "auth_full_stripe_profile",
			})),
		),
		server.GET("/auth/stripe_profile",
			server.Name("auth_stripe_profile"),
			server.HTTP(),
			server.Local(upstream.MockAuthStripeProfileAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "auth_stripe_profile",
				StatusCode: http.StatusOK,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "auth_stripe_profile",
			})),
		),
		server.GET("/auth/has_valid_payment_method",
			server.Name("auth_has_valid_payment_method"),
			server.HTTP(),
			server.Local(upstream.MockJSONAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "auth_has_valid_payment_method",
				StatusCode: http.StatusOK,
				JSONBody: map[string]any{
					"hasValidPaymentMethod": true,
				},
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "auth_has_valid_payment_method",
			})),
		),
		server.Any("/auth/poll",
			server.Name("auth_poll"),
			server.HTTP(),
			server.Local(upstream.MockAuthPollAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "auth_poll",
				StatusCode: http.StatusOK,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "auth_poll",
			})),
		),
		server.POST("/auth/logout",
			server.Name("auth_logout"),
			server.HTTP(),
			server.Local(upstream.FixedStatusAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "auth_logout",
				StatusCode: http.StatusNoContent,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "auth_logout",
			})),
		),
		server.Any("/auth/*",
			server.Name("auth_proxy"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "auth_proxy",
			})),
		),
	)...)

	return nil
}

func directUpstreamProcedure(pattern string, name string, protocol server.RouteOption, deps upstream.Dependencies) server.Option {
	direct := upstream.DirectAction(deps, upstream.CompatRouteConfig{Name: name})
	action := func(ctx *server.Context) error {
		if ctx != nil && ctx.UpstreamURL == nil && ctx.Request != nil && ctx.Request.URL != nil {
			targetURL := *ctx.Request.URL
			targetURL.Scheme = "https"
			targetURL.Host = "api2.cursor.sh:443"
			ctx.UpstreamURL = &targetURL
		}
		return direct(ctx)
	}
	return server.POST(pattern,
		server.Name(name),
		protocol,
		server.Local(action),
		server.Upstream(action),
	)
}

func repositoryServiceProcedure(pattern string, name string, protocol server.RouteOption, module *forwarder.Module, deps upstream.Dependencies) server.Option {
	localAction := server.HTTPHandlerAction(module.RepositoryServiceHandler)
	upstreamAction := upstream.DirectAction(deps, upstream.CompatRouteConfig{Name: name})
	return server.POST(pattern,
		server.Name(name),
		protocol,
		server.Local(localAction),
		server.Upstream(upstreamAction),
	)
}

func uploadServiceProcedure(pattern string, name string, protocol server.RouteOption, module *forwarder.Module, deps upstream.Dependencies) server.Option {
	localAction := server.HTTPHandlerAction(module.UploadServiceHandler)
	upstreamAction := upstream.DirectAction(deps, upstream.CompatRouteConfig{Name: name})
	return server.POST(pattern,
		server.Name(name),
		protocol,
		server.Local(localAction),
		server.Upstream(upstreamAction),
	)
}

func tabServerUpstreamProcedure(pattern string, name string, protocol server.RouteOption, deps upstream.Dependencies) server.Option {
	direct := upstream.DirectAction(deps, upstream.CompatRouteConfig{Name: name})
	action := func(ctx *server.Context) error {
		if ctx != nil && ctx.Request != nil && ctx.Request.URL != nil {
			baseURL, err := url.Parse(tabServerBaseURL)
			if err != nil {
				return fmt.Errorf("解析 tab server 地址失败: %w", err)
			}
			targetURL := *ctx.Request.URL
			targetURL.Scheme = baseURL.Scheme
			targetURL.Host = baseURL.Host
			ctx.UpstreamURL = &targetURL
		}
		return direct(ctx)
	}
	return server.POST(pattern,
		server.Name(name),
		protocol,
		server.Local(action),
		server.Upstream(action),
	)
}

type serverSystemSettings struct {
	configs   *serverconfig.Manager
	vmManager *vm.Manager
}

func (settings *serverSystemSettings) ResolveModelAdapters(ctx context.Context) ([]legacyruntime.ModelAdapterConfig, error) {
	snapshot, err := settings.configs.LegacyRuntimeSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	// 合并虚拟模型 adapter 条目
	vmResolver := vm.NewVMResolver(settings.vmManager, settings)
	merged := vmResolver.MergeVirtualModelAdapters(ctx, snapshot.ModelAdapters)
	return merged, nil
}

// buildVirtualModelManager 根据配置构建 Virtual Model Runtime 管理器。
// channelResolver 必须非 nil 才能让 MOA 专家通过已有 ModelAdapter 解析/调用渠道（禁止 nil ChannelService）。
//
// 注册策略：
//   - AOS：仅在配置显式启用时注册（对 Cursor 透明，作为普通 channel ID）
//   - BestOfN/Debate/Reflection/ToT：当前不注册
//     理由：Virtual Model 硬性规则 #1 "对 Cursor 透明"，客户端不感知 Workflow。
//     这些变体作为 AOS 内部专家编排的候选实现，未来通过 AOS team 配置动态启用。
//     避免在 AvailableModels 中暴露过多虚拟模型 ID，保持用户体验简洁。
func buildVirtualModelManager(cfg *serverconfig.Config, optRuntime *optimize.Runtime, channelResolver vm_moa.ChannelResolver) *vm.Manager {
	manager := vm.NewManager()
	var aosCfg *serverconfig.AOSConfig
	if cfg != nil {
		aosCfg = cfg.VirtualModels.AOS
	}
	if aosCfg != nil && aosCfg.Enabled {
		if err := registerAOSModel(manager, aosCfg, optRuntime, channelResolver); err != nil {
			logger.Errorf("注册 AOS 虚拟模型失败: %v", err)
		}
	}
	return manager
}

func registerAOSModel(manager *vm.Manager, cfg *serverconfig.AOSConfig, optRuntime *optimize.Runtime, channelResolver vm_moa.ChannelResolver) error {
	if manager == nil {
		return fmt.Errorf("virtual model manager is required")
	}
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	var channelSvc vm_moa.ChannelService
	if channelResolver != nil {
		channelSvc = vm_moa.NewAdapterChannelService(channelResolver)
	}
	aosModel := vm_aos.NewAOSModel(convertAOSTeamConfig(cfg), channelSvc, optRuntime)
	aosModel.SetPlanningAdvisor(evolverPlanningAdvisor{})
	aosModel.SetVMManager(manager)
	if channelResolver != nil {
		aosModel.SetChannelResolver(channelResolver)
	}
	return manager.Register(aosModel)
}

func (host *Host) replaceAOSModel(cfg serverconfig.Config) error {
	manager := host.VirtualModelManager()
	if manager == nil {
		return fmt.Errorf("virtual model manager is not initialized")
	}
	if cfg.VirtualModels.AOS == nil || !cfg.VirtualModels.AOS.Enabled {
		manager.Unregister(vm_aos.ModelID)
		return nil
	}
	return registerAOSModel(manager, cfg.VirtualModels.AOS, host.OptimizationRuntime(), host.configs)
}

func convertNodeBinding(cfg *serverconfig.VirtualModelNodeBindingConfig) *vmconfig.NodeBindingConfig {
	if cfg == nil {
		return nil
	}
	return &vmconfig.NodeBindingConfig{
		AdapterID: cfg.AdapterID,
		Enabled:   cfg.Enabled,
	}
}

func convertNodeBindings(cfg map[string]*serverconfig.VirtualModelNodeBindingConfig) map[string]*vmconfig.NodeBindingConfig {
	if cfg == nil {
		return nil
	}
	result := make(map[string]*vmconfig.NodeBindingConfig, len(cfg))
	for k, v := range cfg {
		result[k] = convertNodeBinding(v)
	}
	return result
}

// convertAOSTeamConfig converts serverconfig AOSConfig to an AOS TeamProfile.
// If no members are configured, uses DefaultTeam with the leader's adapter.
func convertAOSTeamConfig(cfg *serverconfig.AOSConfig) *vm_aos.TeamProfile {
	if cfg == nil {
		team := vm_aos.DefaultTeam("")
		team.ExecutionMode = serverconfig.AOSExecutionModeCursorTask
		return team
	}
	leaderAdapter := strings.TrimSpace(cfg.Leader.AdapterID)
	if len(cfg.Members) == 0 {
		team := vm_aos.DefaultTeam(leaderAdapter)
		team.ExecutionMode = serverconfig.NormalizeAOSExecutionMode(cfg.ExecutionMode)
		return team
	}
	team := &vm_aos.TeamProfile{
		Leader:        vm_aos.LeaderConfig{AdapterID: leaderAdapter},
		Workflow:      vm_aos.WorkflowConfig{Mode: "auto", MaxParallel: 4, Timeout: "120s", Retry: 1},
		Sprints:       vm_aos.SprintConfig{MaxIterations: 3},
		ExecutionMode: serverconfig.NormalizeAOSExecutionMode(cfg.ExecutionMode),
	}
	for _, m := range cfg.Members {
		team.Members = append(team.Members, vm_aos.MemberConfig{
			ID:           m.ID,
			Name:         m.Name,
			AdapterID:    m.AdapterID,
			SystemPrompt: m.SystemPrompt,
			// Tags are NOT set from user config; AOSModel.RecognizeMembers
			// populates them at runtime once the Leader has "met" the team.
		})
	}
	return team
}

// wireEmbedder scans user-configured ModelAdapters for the first OpenAI-compatible
// adapter and creates a FallbackEmbedder (APIEmbedder + SimpleEmbedder fallback).
// Injects it into both Cache Runtime and Memory Runtime (via Context Runtime).
// If no suitable adapter is found, both runtimes keep their default SimpleEmbedder.
func wireEmbedder(cfg serverconfig.Config, cacheRT *cacheruntime.Runtime, contextRT *contextruntime.Runtime) {
	for _, adapter := range cfg.ModelAdapters {
		if strings.ToLower(strings.TrimSpace(adapter.Type)) != "openai" {
			continue
		}
		baseURL := strings.TrimSpace(adapter.BaseURL)
		apiKey := strings.TrimSpace(adapter.APIKey)
		if baseURL == "" || apiKey == "" {
			continue
		}
		model := embedding.ResolveEmbeddingModel(adapter.ModelID)
		apiEmb := embedding.NewAPIEmbedder(baseURL, apiKey, model)
		fallback := embedding.NewFallbackEmbedder(apiEmb, embedding.NewSimpleEmbedder())
		if cacheRT != nil {
			cacheRT.SetEmbedder(fallback)
		}
		if contextRT != nil {
			contextRT.SetEmbedder(fallback)
		}
		return
	}
}

// closeRuntimeState closes every runtime held by state in reverse
// construction order (Plugin → Telemetry → Optimize → Context → Cache → Tool).
// Each Close call is wrapped in a recover() so a single panicking runtime
// cannot abort the rest of the shutdown sequence. R14: lifecycle unification.
func closeRuntimeState(ctx context.Context, state hostRuntimeState, source string) {
	closers := []struct {
		name  string
		close func(context.Context) error
	}{
		{"plugin", func(c context.Context) error {
			if state.pluginRT == nil {
				return nil
			}
			return state.pluginRT.Close(c)
		}},
		{"telemetry", func(c context.Context) error {
			if state.telemetryRuntime == nil {
				return nil
			}
			return state.telemetryRuntime.Close(c)
		}},
		{"optimize", func(c context.Context) error {
			if state.optRuntime == nil {
				return nil
			}
			return state.optRuntime.Close(c)
		}},
		{"context", func(c context.Context) error {
			if state.contextRT == nil {
				return nil
			}
			return state.contextRT.CloseCtx(c)
		}},
		{"cache", func(c context.Context) error {
			if state.cacheRuntime == nil {
				return nil
			}
			return state.cacheRuntime.Close(c)
		}},
		{"tool", func(c context.Context) error {
			if state.toolRuntime == nil {
				return nil
			}
			return state.toolRuntime.Close(c)
		}},
	}
	for _, entry := range closers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Errorf("host: runtime %s close panicked source=%s err=%v", entry.name, source, r)
				}
			}()
			if err := entry.close(ctx); err != nil {
				logger.Errorf("host: runtime %s close error source=%s err=%v", entry.name, source, err)
			}
		}()
	}
}
