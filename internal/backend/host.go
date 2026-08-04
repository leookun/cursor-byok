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
	store            *serverconfig.Store
	listenAddr       string
	configs          *serverconfig.Manager
	healthHTTP       *http.Client
	controlPlaneAuth upstream.AuthorizationProvider

	runMu      sync.RWMutex
	httpServer *http.Server

	lastRunErr error

	mux http.Handler
}

func NewHost(store *serverconfig.Store, controlPlaneAuth upstream.AuthorizationProvider) (*Host, error) {
	if store == nil {
		return nil, fmt.Errorf("backend config store is required")
	}
	configs, err := serverconfig.NewManager(context.Background(), store)
	if err != nil {
		return nil, err
	}
	cfg := configs.Current()
	host := &Host{
		store:            store,
		listenAddr:       cfg.BackendListenAddr,
		configs:          configs,
		healthHTTP:       newLoopbackHTTPClient(),
		controlPlaneAuth: controlPlaneAuth,
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
		),
		server.POST(legacyRunSSEProcedure,
			server.Name("run_sse"),
			server.ConnectStream(),
			server.Local(server.HTTPHandlerAction(agentModule.LocalRunSSE)),
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
		),
		server.POST("/aiserver.v1.ServerConfigService/GetServerConfig",
			server.Name("server_config_service_get_server_config"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "server_config_service_get_server_config",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetServerConfigResponse",
				MockBuilder:   upstream.ServerConfigMockBuilder,
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
		),
		server.POST("/aiserver.v1.AiService/GetUsableModels",
			server.Name("usable_models"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "usable_models",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetUsableModelsResponse",
				MockBuilder:   upstream.UsableModelsMockBuilder,
			})),
		),
		server.POST("/aiserver.v1.AiService/GetDefaultModelForCli",
			server.Name("default_model_for_cli"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "default_model_for_cli",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetDefaultModelForCliResponse",
				MockBuilder:   upstream.DefaultModelForCliMockBuilder,
			})),
		),
		server.POST("/aiserver.v1.AiService/GetDefaultModel",
			server.Name("default_model"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "default_model",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetDefaultModelResponse",
				MockBuilder:   upstream.DefaultModelMockBuilder,
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
		),
		server.POST("/aiserver.v1.AnalyticsService/SubmitLogs",
			server.Name("analytics_submit_logs"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "analytics_submit_logs",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.SubmitLogsResponse",
				MockBuilder:   upstream.SubmitLogsMockBuilder,
			})),
		),
		server.POST("/aiserver.v1.AnalyticsService/TrackEvents",
			server.Name("analytics_track_events"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "analytics_track_events",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.TrackEventsResponse",
				MockBuilder:   upstream.EmptyMockBuilder,
			})),
		),
		server.POST("/v1/traces",
			server.Name("otlp_traces"),
			server.HTTP(),
			server.Local(upstream.FixedStatusAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "otlp_traces",
				StatusCode: http.StatusOK,
			})),
		),
		server.POST("/oauth/token",
			server.Name("oauth_token"),
			server.HTTP(),
			server.Local(upstream.MockOAuthAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "oauth_token",
				StatusCode: http.StatusOK,
			})),
		),
		server.POST("/aiserver.v1.AuthService/GetEmail",
			server.Name("auth_service_get_email"),
			server.ConnectUnary(),
			server.Local(upstream.MockAuthEmailAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "auth_service_get_email",
				StatusCode: http.StatusOK,
			})),
		),
		tabServerProcedure("/aiserver.v1.AiService/StreamCpp", "ai_stream_cpp", server.ConnectStream(), routeDeps),
		tabServerProcedure("/aiserver.v1.AiService/StreamNextCursorPrediction", "ai_stream_next_cursor_prediction", server.ConnectStream(), routeDeps),
		tabServerProcedure("/aiserver.v1.AiService/GetCppEditClassification", "ai_get_cpp_edit_classification", server.ConnectUnary(), routeDeps),
		tabServerProcedure("/aiserver.v1.AiService/RefreshTabContext", "ai_refresh_tab_context", server.ConnectUnary(), routeDeps),
		tabServerProcedure("/aiserver.v1.AiService/CppConfig", "ai_cpp_config", server.ConnectUnary(), routeDeps),
		tabServerProcedure("/aiserver.v1.AiService/CppEditHistoryStatus", "ai_cpp_edit_history_status", server.ConnectUnary(), routeDeps),
		tabServerProcedure("/aiserver.v1.AiService/CppAppend", "ai_cpp_append", server.ConnectUnary(), routeDeps),
		tabServerProcedure("/aiserver.v1.AiService/CppEditHistoryAppend", "ai_cpp_edit_history_append", server.ConnectUnary(), routeDeps),
		tabServerProcedure("/aiserver.v1.AiService/ReportAiCodeChangeMetrics", "ai_report_ai_code_change_metrics", server.ConnectUnary(), routeDeps),
		tabServerProcedure("/aiserver.v1.AiService/WriteGitCommitMessage", "ai_write_git_commit_message", server.ConnectUnary(), routeDeps),
		tabServerProcedure("/aiserver.v1.AiService/WriteGitBranchName", "ai_write_git_branch_name", server.ConnectUnary(), routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceFastRepoInitHandshakeV2Procedure, "repository_fast_repo_init_handshake_v2", server.ConnectUnary(), agentModule),
		repositoryServiceProcedure(forwarder.RepositoryServiceFastRepoInitHandshakeProcedure, "repository_fast_repo_init_handshake", server.ConnectUnary(), agentModule),
		repositoryServiceProcedure(forwarder.RepositoryServiceFastRepoSyncCompleteProcedure, "repository_fast_repo_sync_complete", server.ConnectUnary(), agentModule),
		repositoryServiceProcedure(forwarder.RepositoryServiceSyncMerkleSubtreeV2Procedure, "repository_sync_merkle_subtree_v2", server.ConnectUnary(), agentModule),
		repositoryServiceProcedure(forwarder.RepositoryServiceSyncMerkleSubtreeProcedure, "repository_sync_merkle_subtree", server.ConnectUnary(), agentModule),
		repositoryServiceProcedure(forwarder.RepositoryServiceFastUpdateFileV2Procedure, "repository_fast_update_file_v2", server.ConnectUnary(), agentModule),
		repositoryServiceProcedure(forwarder.RepositoryServiceFastUpdateFileProcedure, "repository_fast_update_file", server.ConnectUnary(), agentModule),
		repositoryServiceProcedure(forwarder.RepositoryServiceEnsureIndexCreatedProcedure, "repository_ensure_index_created", server.ConnectUnary(), agentModule),
		repositoryServiceProcedure(forwarder.RepositoryServiceGetCopyStatusProcedure, "repository_get_copy_status", server.ConnectUnary(), agentModule),
		repositoryServiceProcedure(forwarder.RepositoryServiceGetUploadLimitsProcedure, "repository_get_upload_limits", server.ConnectUnary(), agentModule),
		repositoryServiceProcedure(forwarder.RepositoryServiceGetNumFilesToSendProcedure, "repository_get_num_files_to_send", server.ConnectUnary(), agentModule),
		repositoryServiceProcedure(forwarder.RepositoryServiceGetAvailableChunkingStrategiesProcedure, "repository_get_available_chunking_strategies", server.ConnectUnary(), agentModule),
		repositoryServiceProcedure(forwarder.RepositoryServiceGetHighLevelFolderDescriptionProcedure, "repository_get_high_level_folder_description", server.ConnectUnary(), agentModule),
		repositoryServiceProcedure(forwarder.RepositoryServiceRepositoryStatusProcedure, "repository_status", server.ConnectUnary(), agentModule),
		repositoryServiceProcedure(forwarder.RepositoryServiceBatchRepositoryStatusProcedure, "repository_batch_status", server.ConnectUnary(), agentModule),
		uploadServiceProcedure(forwarder.UploadServiceUploadDocumentationProcedure, "upload_documentation", server.ConnectUnary(), agentModule),
		uploadServiceProcedure(forwarder.UploadServiceGetDocProcedure, "upload_get_doc", server.ConnectUnary(), agentModule),
		uploadServiceProcedure(forwarder.UploadServiceGetPagesProcedure, "upload_get_pages", server.ConnectUnary(), agentModule),
		uploadServiceProcedure(forwarder.UploadServiceUploadedStatusProcedure, "upload_uploaded_status", server.ConnectUnary(), agentModule),
		server.Any("/aiserver.v1.AiService/*",
			server.Name("ai_service"),
			server.HTTP(),
			server.Local(server.HTTPHandlerAction(agentModule.AiHandler)),
		),
		tabServerProcedure("/aiserver.v1.CppService/AvailableModels", "cpp_available_models", server.ConnectUnary(), routeDeps),
		tabServerProcedure("/aiserver.v1.CppService/RecordCppFate", "cpp_record_cpp_fate", server.ConnectUnary(), routeDeps),
		server.Any("/aiserver.v1.CppService/*",
			server.Name("cpp_service"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
		),
		tabServerProcedure("/aiserver.v1.FileSyncService/FSSyncFile", "file_sync_sync_file", server.ConnectUnary(), routeDeps),
		tabServerProcedure("/aiserver.v1.FileSyncService/FSIsEnabledForUser", "file_sync_is_enabled_for_user", server.ConnectUnary(), routeDeps),
		tabServerProcedure("/aiserver.v1.FileSyncService/FSConfig", "file_sync_config", server.ConnectUnary(), routeDeps),
		tabServerProcedure("/aiserver.v1.FileSyncService/FSUploadFile", "file_sync_upload_file", server.ConnectUnary(), routeDeps),
		server.Any("/aiserver.v1.FileSyncService/*",
			server.Name("file_sync"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
		),
		server.POST("/aiserver.v1.DashboardService/GetTokenUsage",
			server.Name("dashboard_token_usage"),
			server.HTTP(),
			server.Local(server.HTTPHandlerAction(agentModule.AiHandler)),
		),
		server.POST("/aiserver.v1.DashboardService/GetGlassEarlyPreviewEnrollment",
			server.Name("dashboard_glass_early_preview_enrollment"),
			server.ConnectUnary(),
			server.Local(server.HTTPHandlerAction(agentModule.AiHandler)),
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
		),
		server.POST("/aiserver.v1.DashboardService/GetManagedSkills",
			server.Name("dashboard_get_managed_skills"),
			server.ConnectUnary(),
			server.Local(cursorControlPlaneAction(
				host.controlPlaneAuth,
				routeDeps,
				"dashboard_get_managed_skills",
				upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
					Name:          "dashboard_get_managed_skills",
					StatusCode:    http.StatusOK,
					MockProtoType: "aiserver.v1.GetManagedSkillsResponse",
					MockBuilder:   upstream.DashboardManagedSkillsMockBuilder,
				}),
			)),
		),
		server.POST("/aiserver.v1.DashboardService/GetTeamAdminSettingsOrEmptyIfNotInTeam",
			server.Name("dashboard_get_team_admin_settings_or_empty"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_get_team_admin_settings_or_empty",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetTeamAdminSettingsResponse",
				MockBuilder:   upstream.EmptyMockBuilder,
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetTeamReposOrEmptyIfNotInTeam",
			server.Name("dashboard_get_team_repos_or_empty"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_get_team_repos_or_empty",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetTeamReposResponse",
				MockBuilder:   upstream.EmptyMockBuilder,
			})),
		),
		server.POST("/aiserver.v1.DashboardService/ListMarketplaces",
			server.Name("dashboard_list_marketplaces"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_list_marketplaces",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.ListMarketplacesResponse",
				MockBuilder:   upstream.EmptyMockBuilder,
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetGlobalCommands",
			server.Name("dashboard_get_global_commands"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_get_global_commands",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetGlobalCommandsResponse",
				MockBuilder:   upstream.EmptyMockBuilder,
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetEffectiveUserPlugins",
			server.Name("dashboard_get_effective_user_plugins"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_get_effective_user_plugins",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetEffectiveUserPluginsResponse",
				MockBuilder:   upstream.EmptyMockBuilder,
			})),
		),
		server.POST("/aiserver.v1.DashboardService/RegisterMarketplaceAndPlugins",
			server.Name("dashboard_register_marketplace_and_plugins"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_register_marketplace_and_plugins",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.RegisterMarketplaceAndPluginsResponse",
				MockBuilder:   upstream.EmptyMockBuilder,
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetCliDownloadUrl",
			server.Name("dashboard_get_cli_download_url"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_get_cli_download_url",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetCliDownloadUrlResponse",
				MockBuilder:   upstream.EmptyMockBuilder,
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
		),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/AddMarketplace", "dashboard_add_marketplace", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/AddMcpServersFromPlugin", "dashboard_add_mcp_servers_from_plugin", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/BatchGetPluginMcpConfig", "dashboard_batch_get_plugin_mcp_config", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/GetAvailableMcpServers", "dashboard_get_available_mcp_servers", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/GetEffectiveUserPlugins", "dashboard_get_effective_user_plugins", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/GetPlugin", "dashboard_get_plugin", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/GetPluginMcpConfig", "dashboard_get_plugin_mcp_config", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/InstallUserPlugin", "dashboard_install_user_plugin", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/ListMarketplacePlugins", "dashboard_list_marketplace_plugins", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/ListMarketplaces", "dashboard_list_marketplaces", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/ListUserPluginInstalls", "dashboard_list_user_plugin_installs", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/RefreshMarketplace", "dashboard_refresh_marketplace", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/RegisterMarketplaceAndPlugins", "dashboard_register_marketplace_and_plugins", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/RemoveMarketplace", "dashboard_remove_marketplace", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/ResolvePluginsByRef", "dashboard_resolve_plugins_by_ref", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/UninstallUserPlugin", "dashboard_uninstall_user_plugin", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.DashboardService/UpdateUserPluginInstall", "dashboard_update_user_plugin_install", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		cursorControlPlaneProcedure("/aiserver.v1.MCPRegistryService/GetKnownServers", "mcp_registry_get_known_servers", server.ConnectUnary(), host.controlPlaneAuth, routeDeps),
		server.Any("/aiserver.v1.DashboardService/*",
			server.Name("dashboard"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
		),
		server.Any("/aiserver.v1.NetworkService/*",
			server.Name("network_service"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
		),
		server.Any("/aiserver.v1.InAppAdService/*",
			server.Name("in_app_ad"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
		),
		server.GET("/auth/full_stripe_profile",
			server.Name("auth_full_stripe_profile"),
			server.HTTP(),
			server.Local(upstream.MockAuthFullStripeProfileAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "auth_full_stripe_profile",
				StatusCode: http.StatusOK,
			})),
		),
		server.GET("/auth/stripe_profile",
			server.Name("auth_stripe_profile"),
			server.HTTP(),
			server.Local(upstream.MockAuthStripeProfileAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "auth_stripe_profile",
				StatusCode: http.StatusOK,
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
		),
		server.Any("/auth/poll",
			server.Name("auth_poll"),
			server.HTTP(),
			server.Local(upstream.MockAuthPollAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "auth_poll",
				StatusCode: http.StatusOK,
			})),
		),
		server.POST("/auth/logout",
			server.Name("auth_logout"),
			server.HTTP(),
			server.Local(upstream.FixedStatusAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "auth_logout",
				StatusCode: http.StatusNoContent,
			})),
		),
		server.Any("/auth/*",
			server.Name("auth_proxy"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
		),
	)

	return nil
}

func repositoryServiceProcedure(pattern string, name string, protocol server.RouteOption, module *forwarder.Module) server.Option {
	localAction := server.HTTPHandlerAction(module.RepositoryServiceHandler)
	return server.POST(pattern,
		server.Name(name),
		protocol,
		server.Local(localAction),
	)
}

func uploadServiceProcedure(pattern string, name string, protocol server.RouteOption, module *forwarder.Module) server.Option {
	localAction := server.HTTPHandlerAction(module.UploadServiceHandler)
	return server.POST(pattern,
		server.Name(name),
		protocol,
		server.Local(localAction),
	)
}

func tabServerProcedure(pattern string, name string, protocol server.RouteOption, deps upstream.Dependencies) server.Option {
	forward := upstream.ForwardAction(deps, upstream.CompatRouteConfig{Name: name})
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
		return forward(ctx)
	}
	return server.POST(pattern,
		server.Name(name),
		protocol,
		server.Local(action),
	)
}

func cursorControlPlaneProcedure(
	pattern string,
	name string,
	protocol server.RouteOption,
	authorizationProvider upstream.AuthorizationProvider,
	deps upstream.Dependencies,
) server.Option {
	notFound := func(ctx *server.Context) error {
		http.NotFound(ctx.Writer, ctx.Request)
		return nil
	}
	return server.POST(pattern,
		server.Name(name),
		protocol,
		server.Local(cursorControlPlaneAction(authorizationProvider, deps, name, notFound)),
	)
}

func cursorControlPlaneAction(
	authorizationProvider upstream.AuthorizationProvider,
	deps upstream.Dependencies,
	name string,
	fallback server.HandlerFunc,
) server.HandlerFunc {
	forward := upstream.AuthenticatedForwardAction(deps, upstream.CompatRouteConfig{Name: name}, authorizationProvider)
	return func(ctx *server.Context) error {
		if authorizationProvider == nil || !authorizationProvider.SignedIn() {
			return fallback(ctx)
		}
		if ctx == nil || ctx.Request == nil || ctx.Request.URL == nil {
			return fmt.Errorf("Cursor 控制面请求上下文无效")
		}
		targetURL := *ctx.Request.URL
		targetURL.Scheme = "https"
		targetURL.Host = "api2.cursor.sh:443"
		ctx.UpstreamURL = &targetURL
		return forward(ctx)
	}
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
