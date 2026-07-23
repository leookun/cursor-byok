package app

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"os"
	goruntime "runtime"
	"time"

	"cursor/internal/appdata"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/buildinfo"

	"github.com/leaanthony/u"

	bridge "cursor/internal/bridge"
	"cursor/internal/certs"
	"cursor/internal/logger"
	"cursor/internal/mitm"
	"cursor/internal/netproxy"
	"cursor/internal/updater"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

const (
	// appName 表示当前模块中的 appName 状态值。
	appName = "Cursor助手"
)

// EmbeddedResources 定义了当前模块中的 EmbeddedResources 类型。
type EmbeddedResources struct {
	// Assets 表示当前声明中的 Assets。
	Assets fs.FS
	// AppIcon 表示当前声明中的 AppIcon。
	AppIcon []byte
	// TrayIcon 表示当前声明中的 TrayIcon。
	TrayIcon []byte
}

// init 用于处理与 init 相关的逻辑。
func init() {
	// R16: use centralized event-name constants instead of raw strings.
	application.RegisterEvent[bridge.ProxyState](EventProxyState)
	application.RegisterEvent[bridge.UserConfig](EventUserConfigChanged)
	application.RegisterEvent[bridge.ModelAdapterTestResultsPayload](EventModelAdapterTestUpdated)
	application.RegisterEvent[updater.StatePayload](EventUpdateState)
	application.RegisterEvent[updater.ProgressPayload](EventUpdateProgress)
	application.RegisterEvent[updater.ReadyPayload](EventUpdateReady)
	application.RegisterEvent[updater.ErrorPayload](EventUpdateError)
	application.RegisterEvent[map[string]string](EventCursorActivity)
	application.RegisterEvent[[]bridge.PetInfo](EventPetListChanged)
	application.RegisterEvent[map[string]string](EventPetStateChanged)
}

// Run 用于处理与 Run 相关的逻辑。
func Run(resources EmbeddedResources) error {
	logger.Init()
	netproxy.InstallDefaultTransport()
	applyOutboundProxyFromDisk()

	certManager, err := certs.EnsureCA(appdata.CACertFilePath(), appdata.CAKeyFilePath())
	if err != nil {
		return err
	}
	caCertPEM := certManager.CACertPEM()
	logEmbeddedCAInfo(caCertPEM)

	defaultBackendBaseURL := "http://" + serverconfig.DefaultBackendListenAddr
	proxyServer, err := mitm.NewProxyServer(serverconfig.DefaultProxyListenAddr, defaultBackendBaseURL, "", "", certManager)
	if err != nil {
		return err
	}
	proxyService := bridge.NewProxyService(proxyServer, certManager, caCertPEM)
	// 将 mitm 的 Cursor 活跃信号接到 PetService（最终发射 cursor:activity 事件）
	proxyServer.SetCursorActivityCallback(proxyService.FireCursorActivity)
	metricsService := bridge.NewMetricsService()
	windowService := bridge.NewWindowService()
	petService := bridge.NewPetService()
	var updateManager *updater.Manager

	var mainWindow *application.WebviewWindow
	appCtx := NewAppContext()

	app := application.New(application.Options{
		Name:        appName,
		Description: appName,
		Services: []application.Service{
			application.NewService(proxyService),
			application.NewService(metricsService),
			application.NewService(windowService),
			application.NewService(petService),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(resources.Assets),
			Middleware: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if serveUserPetAsset(w, r) {
						return
					}
					next.ServeHTTP(w, r)
				})
			},
		},
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyAccessory,
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		OnShutdown: func() {
			// R16: AppContext cancellation propagates to every derived
			// background goroutine first, then we shut down services.
			appCtx.Cancel()
			petService.Stop()
			windowService.StopAllPets()
			if updateManager != nil {
				updateManager.Shutdown()
			}
			proxyService.ShutdownForQuit()
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.cursor-assistant.single-instance",
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				logger.Infof("检测到实例请求，已忽略")
				// 不激活窗口，避免干扰用户工作
			},
		},
	})

	updateManager = updater.NewManager(app)

	windowService.SetApp(app)
	windowService.SetUpdater(updateManager)
	petService.SetApp(app)

	// PET_DEBUG=1 时自动开启桌宠，方便无头/自动化环境采集 Window 层调试日志
	// （正常运行由前端 UI 触发 OpenPetWindow；此钩子仅用于调试，不影响正常流程）。
	if os.Getenv("PET_DEBUG") == "1" {
		// R17: tracked + recover-guarded lifecycle goroutine (was naked go func()).
		LifecycleGo(appCtx, "petDebugAutoOpen", func(ctx context.Context) {
			select {
			case <-time.After(2 * time.Second): // 等 app 事件循环与窗口线程就绪
			case <-ctx.Done():
				return
			}
			log.Println("[Pet][DEBUG] PET_DEBUG=1: auto-opening pet window for diagnostics")
			windowService.OpenPetWindow()
		})
	}

	// 连接 proxy 活动事件到 pet 状态
	proxyService.SetCursorActivityCallback(func(method, path string) {
		app.Event.Emit(bridge.EventCursorActivity, map[string]string{
			"method": method,
			"path":   path,
		})
	})
	// 桥接模型活动状态（thinking/working/idle/error）到桌宠 FSM。
	// forwarder 在 stream 关键节点 emit；Host 仅转发，runner 负责把 Host 的
	// 回调接到 WindowService.SetModelActivity → PetManager.SetActivity。
	if backendHost := proxyService.BackendHost(); backendHost != nil {
		backendHost.SetModelActivityCallback(func(state string) {
			windowService.SetModelActivity(state)
		})
	}
	mainWindow = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:               appName,
		Width:               700,
		Height:              520,
		MinWidth:            640,
		MinHeight:           480,
		DisableResize:       false,
		Frameless:           goruntime.GOOS == "windows",
		URL:                 "/",
		Hidden:              false,
		HideOnEscape:        false,
		MinimiseButtonState: application.ButtonEnabled,
		MaximiseButtonState: application.ButtonHidden,
		CloseButtonState:    application.ButtonEnabled,
		BackgroundColour:    application.RGBA{Red: 25, Green: 25, Blue: 25, Alpha: 255},
		Mac: application.MacWindow{
			Backdrop:      application.MacBackdropLiquidGlass,
			DisableShadow: false,
			TitleBar: application.MacTitleBar{
				AppearsTransparent:   true,
				Hide:                 false,
				HideTitle:            true,
				FullSizeContent:      true,
				UseToolbar:           false,
				HideToolbarSeparator: true,
			},
			WebviewPreferences: application.MacWebviewPreferences{
				FullscreenEnabled:                   u.True,
				TextInteractionEnabled:              u.True,
				AllowsBackForwardNavigationGestures: u.False,
			},
		},
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: false,
		},
	})

	window := mainWindow
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		window.Hide()
		e.Cancel()
	})

	showMainWindow := func() {
		window.Show().Focus()
	}
	toggleMainWindow := func() {
		if window.IsVisible() {
			window.Hide()
			return
		}
		showMainWindow()
	}

	systray := app.SystemTray.New()
	menu := app.Menu.New()
	statusItem := menu.Add("状态：未启动").SetEnabled(false)
	menu.AddSeparator()
	startItem := menu.Add("启动服务")
	stopItem := menu.Add("停止服务")
	menu.Add("检查更新").OnClick(func(ctx *application.Context) {
		updateManager.CheckNow(true)
	})
	menu.AddSeparator()
	menu.Add("显示窗口").OnClick(func(ctx *application.Context) {
		showMainWindow()
	})
	menu.Add("隐藏窗口").OnClick(func(ctx *application.Context) {
		window.Hide()
	})
	menu.Add("显示桌宠").OnClick(func(ctx *application.Context) {
		windowService.TogglePetWindow()
	})
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(ctx *application.Context) {
		proxyService.ShutdownForQuit()
		app.Quit()
	})

	refreshTray := func() {
		state := proxyService.GetState()
		if state.Running {
			statusItem.SetLabel("状态：运行中")
			startItem.SetEnabled(false)
			stopItem.SetEnabled(true)
		} else {
			statusItem.SetLabel("状态：未启动")
			startItem.SetEnabled(true)
			stopItem.SetEnabled(false)
		}
	}
	app.Event.On(EventProxyState, func(event *application.CustomEvent) {
		refreshTray()
	})
	// Re-apply outbound proxy whenever the user config is saved (the payload
	// is the normalized serverconfig.Config). Keeps netproxy in sync with
	// config.yaml edits without a restart.
	app.Event.On(EventUserConfigChanged, func(event *application.CustomEvent) {
		if cfg, ok := event.Data.(bridge.UserConfig); ok {
			applyOutboundProxy(cfg)
		}
	})
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(event *application.ApplicationEvent) {
		logger.Infof("应用版本：v%s", buildinfo.CurrentVersion())
		updateManager.Start()
		// R17: tracked + recover-guarded lifecycle goroutine (was naked go func()).
		LifecycleGo(appCtx, "autoStartProxy", func(ctx context.Context) {
			logger.Infof("application started, begin auto start service in background")
			if _, err := proxyService.StartProxy(); err != nil {
				logger.Errorf("自动启动服务失败: %v", err)
			} else {
				state := proxyService.GetState()
				logger.Infof("代理已自动启动: %s", state.ProxyListenAddr)
			}
		})
	})

	startItem.OnClick(func(ctx *application.Context) {
		if _, err := proxyService.StartProxy(); err != nil {
			logger.Errorf("启动服务失败: %v", err)
		}
		refreshTray()
	})
	stopItem.OnClick(func(ctx *application.Context) {
		if _, err := proxyService.StopProxy(); err != nil {
			logger.Errorf("停止服务失败: %v", err)
		}
		refreshTray()
	})

	if len(resources.AppIcon) > 0 {
		switch goruntime.GOOS {
		case "darwin":
			systray.SetTemplateIcon(resources.TrayIcon)
		case "windows":
			systray.SetIcon(resources.AppIcon)
		default:
			systray.SetIcon(resources.TrayIcon)
		}
	}
	systray.SetTooltip(appName)
	systray.OnClick(toggleMainWindow).SetMenu(menu)
	refreshTray()

	return app.Run()
}

// applyOutboundProxy pushes the configured outbound proxy override into the
// netproxy resolver. Both empty values clear the manual override and fall
// back to environment/system proxy detection.
func applyOutboundProxy(cfg serverconfig.Config) {
	netproxy.SetManualProxy(cfg.OutboundProxy.HTTPProxy, cfg.OutboundProxy.HTTPSProxy)
}

// applyOutboundProxyFromDisk reads the user config from disk (best-effort:
// missing/malformed config is non-fatal at startup — the default empty
// override is applied) and pushes the proxy override into netproxy. This
// runs once at startup before the backend host is built, so the config
// manager is not yet available; we read directly via the Store.
func applyOutboundProxyFromDisk() {
	store := serverconfig.NewStore(appdata.ConfigFilePath(), appdata.LogsRootPath())
	cfg, err := store.Load(context.Background())
	if err != nil {
		logger.Errorf("启动时读取出站代理配置失败，使用默认值: %v", err)
		cfg = serverconfig.DefaultConfig()
	}
	applyOutboundProxy(cfg)
}
