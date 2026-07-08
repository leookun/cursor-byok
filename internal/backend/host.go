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

	"cursor/internal/ads"
	"cursor/internal/appdata"
	"cursor/internal/backend/forwarder"
	"cursor/internal/backend/server"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/backend/server/upstream"
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

	runMu      sync.RWMutex
	httpServer *http.Server

	lastRunErr error

	mux http.Handler
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
	return host, nil
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
	normalized, err := host.configs.Save(ctx, cfg)
	if err != nil {
		return serverconfig.Config{}, err
	}
	if host.httpServer == nil {
		if rebuildErr := host.rebuild(normalized); rebuildErr != nil {
			return serverconfig.Config{}, rebuildErr
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
	return nil
}

func (host *Host) Stop(ctx context.Context) error {
	if host == nil {
		return nil
	}
	host.runMu.Lock()
	serverInstance := host.httpServer
	host.httpServer = nil
	host.runMu.Unlock()
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
	agentModule := forwarder.NewModule(appdata.HistoryRootPath(), host.configs)
	legacyBidiAppendProcedure := "/aiserver.v1.BidiService/BidiAppend"
	legacyRunSSEProcedure := "/agent.v1.AgentService/RunSSE"
	routeDeps := upstream.Dependencies{
		SystemSettingService: &serverSystemSettings{configs: host.configs},
		HTTPClient:           netproxy.NewHTTPClient(30000 * time.Second),
	}

	host.mux = server.New(
		server.Use(
			server.Recover(),
			server.ServerContext(),
			server.PolicyMiddleware(host.configs),
			server.ErrorEncoder(),
		),
		server.Mount(ads.RoutePrefix, ads.NewHTTPHandler(appdata.AdsRootPath())),
		server.GET(healthPath,
			server.Name("healthz"),
			server.HTTP(),
			server.Local(server.Health()),
		),
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
	)

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
	configs *serverconfig.Manager
}

func (settings *serverSystemSettings) ResolveModelAdapters(ctx context.Context) ([]legacyruntime.ModelAdapterConfig, error) {
	snapshot, err := settings.configs.LegacyRuntimeSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snapshot.ModelAdapters, nil
}
