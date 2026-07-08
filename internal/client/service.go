package client

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"cursor/internal/appdata"
	backend "cursor/internal/backend"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/certs"
	"cursor/internal/logger"
	"cursor/internal/mitm"
	"cursor/internal/netproxy"
)

const (
	// publicAPITimeout 表示当前模块中的 publicAPITimeout 状态值。
	publicAPITimeout = 15 * time.Second
	// backendReadyTimeout 表示等待嵌入式 backend 就绪的最长时间。
	backendReadyTimeout = 15 * time.Second
	// backendHealthCheckInterval 表示轮询 backend 健康检查的间隔。
	backendHealthCheckInterval = 1 * time.Second
	// backendHealthCheckAttemptTimeout 限制单次健康检查耗时，避免一次阻塞吃掉全部启动预算。
	backendHealthCheckAttemptTimeout = 1 * time.Second
)

// ProxyService 定义了当前模块中的 ProxyService 类型。
type ProxyService struct {
	// proxy 表示当前声明中的 proxy。
	proxy *mitm.ProxyServer
	// certManager 用于在代理监听地址变化时重建 MITM 服务。
	certManager *certs.Manager
	// backendHost 表示当前嵌入式 backend 服务。
	backendHost *backend.Host

	// mu 表示当前声明中的 mu。
	mu sync.RWMutex
	// lastError 表示当前声明中的 lastError。
	lastError string
	// cursorSettingsApplied 表示当前是否已完成宿主代理设置注入。
	cursorSettingsApplied bool

	// configMu 表示当前声明中的 configMu。
	configMu sync.Mutex
	// configPath 表示当前声明中的 configPath。
	configPath string
	// store 表示统一的配置存储。
	store *serverconfig.Store
	// caCertPEM 表示当前声明中的 caCertPEM。
	caCertPEM []byte

	// caFileMu 表示当前声明中的 caFileMu。
	caFileMu sync.Mutex
	// caFilePath 表示当前声明中的 caFilePath。
	caFilePath string

	// publicClient 表示当前声明中的 publicClient。
	publicClient *http.Client
	// logsRoot 表示当前声明中的 logsRoot。
	logsRoot string
	// modelTestMu 保护模型测速缓存。
	modelTestMu sync.RWMutex
	// modelTestResults 保存当前进程内的模型测速结果。
	modelTestResults map[string]ModelAdapterTestResult
}

// NewProxyService 用于处理与 NewProxyService 相关的逻辑。
func NewProxyService(proxy *mitm.ProxyServer, certManager *certs.Manager, caCertPEM []byte) *ProxyService {
	if err := appdata.EnsureAssistantHome(); err != nil {
		logger.Errorf("ensure assistant home failed: %v", err)
	}
	copiedCert := make([]byte, len(caCertPEM))
	copy(copiedCert, caCertPEM)

	service := &ProxyService{
		proxy:            proxy,
		certManager:      certManager,
		configPath:       resolveUserConfigPath(),
		logsRoot:         resolveLogsRootPath(),
		caCertPEM:        copiedCert,
		publicClient:     netproxy.NewHTTPClient(publicAPITimeout),
		modelTestResults: make(map[string]ModelAdapterTestResult),
	}
	service.store = serverconfig.NewStore(service.configPath, service.logsRoot)
	host, err := backend.NewHost(service.store)
	if err != nil {
		logger.Errorf("init backend host failed: %v", err)
	} else {
		service.backendHost = host
	}
	return service
}

func (s *ProxyService) ensureBackendHost() error {
	if s == nil {
		return nil
	}
	if s.backendHost != nil {
		return nil
	}
	host, err := backend.NewHost(s.store)
	if err != nil {
		return err
	}
	s.backendHost = host
	return nil
}

func (s *ProxyService) ensureProxy(cfg serverconfig.Config) error {
	if s == nil {
		return nil
	}
	baseURL := ""
	if s.backendHost != nil {
		baseURL = s.backendHost.BaseURL()
	}
	if baseURL == "" {
		baseURL = "http://" + cfg.BackendListenAddr
	}
	listenAddr := cfg.ProxyListenAddr

	if s.proxy != nil {
		snapshot := s.proxy.Snapshot()
		if snapshot.ListenAddr == listenAddr {
			return s.proxy.UpdateBaseURL(baseURL)
		}
		if snapshot.Running {
			return fmt.Errorf("代理正在运行，不能从 %s 切换到 %s，请先停止服务", snapshot.ListenAddr, listenAddr)
		}
	}

	proxyServer, err := mitm.NewProxyServer(listenAddr, baseURL, "", "", s.certManager)
	if err != nil {
		return err
	}
	s.proxy = proxyServer
	return nil
}

func (s *ProxyService) waitForBackend(ctx context.Context) error {
	if s == nil || s.backendHost == nil {
		return nil
	}
	ticker := time.NewTicker(backendHealthCheckInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		healthCtx, healthCancel := context.WithTimeout(ctx, backendHealthCheckAttemptTimeout)
		err := s.backendHost.HealthCheck(healthCtx)
		healthCancel()
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("等待内置后端就绪失败: %w", lastErr)
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
