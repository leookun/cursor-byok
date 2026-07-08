package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cursor/internal/cursor"
	"cursor/internal/logger"
	"cursor/internal/mitm"
	"cursor/internal/netproxy"
	localruntime "cursor/internal/runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ProxyState 定义了当前模块中的 ProxyState 类型。
type ProxyState struct {
	// ListenAddr 保留旧字段兼容前端缓存，实际值等于 proxyListenAddr。
	ListenAddr string `json:"listenAddr"`
	// Running 保留旧字段兼容前端缓存，实际值等于 proxyRunning。
	Running bool `json:"running"`
	// BackendListenAddr 表示嵌入式 backend 监听地址。
	BackendListenAddr string `json:"backendListenAddr"`
	// BackendRunning 表示嵌入式 backend 是否已启动。
	BackendRunning bool `json:"backendRunning"`
	// ProxyListenAddr 表示 MITM 代理监听地址。
	ProxyListenAddr string `json:"proxyListenAddr"`
	// ProxyRunning 表示 MITM 代理是否已启动。
	ProxyRunning bool `json:"proxyRunning"`
	// CursorSettingsApplied 表示宿主代理设置是否已注入。
	CursorSettingsApplied bool `json:"cursorSettingsApplied"`
	// NetProxySource 表示当前出站网络代理来源：system/env/direct。
	NetProxySource string `json:"netProxySource"`
	// NetProxyActive 表示当前出站网络代理是否启用。
	NetProxyActive bool `json:"netProxyActive"`
	// NetProxyUsingSystem 表示当前出站网络代理是否来自操作系统代理。
	NetProxyUsingSystem bool `json:"netProxyUsingSystem"`
	// NetProxyUsingEnv 表示当前出站网络代理是否来自环境变量。
	NetProxyUsingEnv bool `json:"netProxyUsingEnv"`
	// NetProxyHTTP 表示当前 HTTP 代理地址，已移除凭据。
	NetProxyHTTP string `json:"netProxyHttp"`
	// NetProxyHTTPS 表示当前 HTTPS 代理地址，已移除凭据。
	NetProxyHTTPS string `json:"netProxyHttps"`
	// NetProxyPACIgnored 表示检测到 PAC/自动代理但本轮按直连处理。
	NetProxyPACIgnored bool `json:"netProxyPacIgnored"`
	// NetProxyDescription 表示当前出站网络代理摘要，已移除凭据。
	NetProxyDescription string `json:"netProxyDescription"`
	// LastError 表示当前声明中的 LastError。
	LastError string `json:"lastError"`
}

// StartProxy 用于处理与 StartProxy 相关的逻辑。
func (s *ProxyService) StartProxy() (ProxyState, error) {
	logger.Infof("start service requested config_path=%s logs_root=%s", s.configPath, s.logsRoot)
	fail := func(step string, err error) (ProxyState, error) {
		logger.Errorf("start service failed step=%s err=%v", step, err)
		s.setLastError(err)
		s.emitState()
		return s.GetState(), err
	}
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return fail("load_user_config", err)
	}
	if err := s.ensureBackendHost(); err != nil {
		return fail("ensure_backend_host", err)
	}
	if !s.backendHost.IsRunning() {
		logger.Infof("starting embedded backend listen_addr=%s", s.backendHost.ListenAddr())
		if err := s.backendHost.Start(); err != nil {
			return fail("start_backend", err)
		}
	} else {
		logger.Infof("embedded backend already running listen_addr=%s", s.backendHost.ListenAddr())
	}
	healthCtx, healthCancel := context.WithTimeout(context.Background(), backendReadyTimeout)
	defer healthCancel()
	if err := s.waitForBackend(healthCtx); err != nil {
		return fail("wait_backend_ready", err)
	}
	logger.Infof("embedded backend ready listen_addr=%s", s.backendHost.ListenAddr())
	if err := s.ensureProxy(cfg); err != nil {
		return fail("ensure_proxy", err)
	}

	// 启动时注入账号信息
	if err := cursor.InjectCursorUserInfo(localruntime.InjectAccountEmail, localruntime.InjectAuthToken); err != nil {
		logger.Errorf("injectCursorUserInfo failed: %v", err)
		// 不阻断启动，仅记录日志
	}

	if s.proxy != nil && !s.proxy.IsRunning() {
		logger.Infof("starting mitm proxy listen_addr=%s", s.proxy.Snapshot().ListenAddr)
		if err := s.proxy.Start(); err != nil {
			return fail("start_mitm_proxy", err)
		}
	}

	if err := s.ApplyCursorSettings(); err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if s.proxy != nil {
			_ = s.proxy.Stop(stopCtx)
		}
		_ = s.backendHost.Stop(stopCtx)
		startErr := fmt.Errorf("服务已启动，但注入 Cursor 配置失败: %w", err)
		logger.Errorf("start service failed step=apply_cursor_settings err=%v", startErr)
		s.setLastError(startErr)
		s.emitState()
		return s.GetState(), startErr
	}

	s.setLastError(nil)
	s.emitState()
	state := s.GetState()
	logger.Infof(
		"start service completed backend_listen_addr=%s proxy_listen_addr=%s cursor_settings_applied=%t",
		state.BackendListenAddr,
		state.ProxyListenAddr,
		state.CursorSettingsApplied,
	)
	return state, nil
}

// StopProxy 用于处理与 StopProxy 相关的逻辑。
func (s *ProxyService) StopProxy() (ProxyState, error) {
	logger.Infof("stop service requested")
	fail := func(step string, err error) (ProxyState, error) {
		logger.Errorf("stop service failed step=%s err=%v", step, err)
		s.setLastError(err)
		s.emitState()
		return s.GetState(), err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s.proxy != nil && s.proxy.IsRunning() {
		logger.Infof("stopping mitm proxy listen_addr=%s", s.proxy.Snapshot().ListenAddr)
		if err := s.proxy.Stop(ctx); err != nil {
			return fail("stop_mitm_proxy", err)
		}
	}

	if err := s.ClearCursorSettings(); err != nil {
		return fail("clear_cursor_settings", err)
	}
	if s.backendHost != nil {
		logger.Infof("stopping embedded backend listen_addr=%s", s.backendHost.ListenAddr())
		if err := s.backendHost.Stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return fail("stop_backend", err)
		}
	}

	s.setLastError(nil)
	s.emitState()
	state := s.GetState()
	logger.Infof(
		"stop service completed backend_running=%t proxy_running=%t cursor_settings_applied=%t",
		state.BackendRunning,
		state.ProxyRunning,
		state.CursorSettingsApplied,
	)
	return state, nil
}

// GetState 用于处理与 GetState 相关的逻辑。
func (s *ProxyService) GetState() ProxyState {
	var proxySnap mitm.Snapshot
	if s.proxy != nil {
		proxySnap = s.proxy.Snapshot()
	}
	s.mu.RLock()
	lastError := s.lastError
	cursorSettingsApplied := s.cursorSettingsApplied
	s.mu.RUnlock()
	backendListenAddr := ""
	backendRunning := false
	if s.backendHost != nil {
		backendListenAddr = s.backendHost.ListenAddr()
		backendRunning = s.backendHost.IsRunning()
	}
	netProxy := netproxy.CurrentStatus()
	return ProxyState{
		ListenAddr:            proxySnap.ListenAddr,
		Running:               proxySnap.Running,
		BackendListenAddr:     backendListenAddr,
		BackendRunning:        backendRunning,
		ProxyListenAddr:       proxySnap.ListenAddr,
		ProxyRunning:          proxySnap.Running,
		CursorSettingsApplied: cursorSettingsApplied,
		NetProxySource:        netProxy.Source,
		NetProxyActive:        netProxy.Active,
		NetProxyUsingSystem:   netProxy.UsingSystemProxy,
		NetProxyUsingEnv:      netProxy.UsingEnvProxy,
		NetProxyHTTP:          netProxy.HTTPProxy,
		NetProxyHTTPS:         netProxy.HTTPSProxy,
		NetProxyPACIgnored:    netProxy.PACIgnored,
		NetProxyDescription:   netProxy.Description,
		LastError:             lastError,
	}
}

// ClearLastError 用于处理与 ClearLastError 相关的逻辑。
func (s *ProxyService) ClearLastError() ProxyState {
	s.setLastError(nil)
	s.emitState()
	return s.GetState()
}

// SetBaseURL 用于处理与 SetBaseURL 相关的逻辑。
func (s *ProxyService) SetBaseURL(baseURL string) (ProxyState, error) {
	_ = strings.TrimSpace(baseURL)
	err := fmt.Errorf("backend/proxy 地址已固定，不再支持直接修改 baseURL")
	s.setLastError(err)
	s.emitState()
	return s.GetState(), err
}

// setLastError 用于处理与 setLastError 相关的逻辑。
func (s *ProxyService) setLastError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.lastError = ""
		return
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		msg = "unknown error"
	}
	s.lastError = msg
}

// emitState 用于处理与 emitState 相关的逻辑。
func (s *ProxyService) emitState() {
	app := application.Get()
	if app == nil {
		return
	}
	state := s.GetState()
	if state.Running {
		state.LastError = ""
	}
	app.Event.Emit("proxy:state", state)
}

// ShutdownForQuit 用于处理与 ShutdownForQuit 相关的逻辑。
func (s *ProxyService) ShutdownForQuit() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var finalErr error

	if s.proxy != nil {
		if err := s.proxy.Stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
			finalErr = err
		}
	}
	if err := s.ClearCursorSettings(); err != nil {
		finalErr = errors.Join(finalErr, err)
	}
	if s.backendHost != nil {
		if err := s.backendHost.Stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
			finalErr = errors.Join(finalErr, err)
		}
	}
	if finalErr != nil {
		s.setLastError(finalErr)
	}
}

func (s *ProxyService) setCursorSettingsApplied(applied bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursorSettingsApplied = applied
}
