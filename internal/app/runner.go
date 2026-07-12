package app

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"os"
	goruntime "runtime"
	"strings"
	"time"

	"cursor/internal/ads"
	"cursor/internal/appdata"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/buildinfo"
	"cursor/internal/cursor"
	"cursor/internal/historymetrics"

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
	// adRefreshInterval 表示后台广告拉取间隔。
	adRefreshInterval = 3 * time.Minute
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
	application.RegisterEvent[bridge.ProxyState]("proxy:state")
	application.RegisterEvent[bridge.UserConfig]("user-config:changed")
	application.RegisterEvent[bridge.ModelAdapterTestResultsPayload]("model-adapter-test:updated")
	application.RegisterEvent[bridge.AdRuntime](ads.EventUpdated)
	application.RegisterEvent[updater.StatePayload](updater.EventState)
	application.RegisterEvent[updater.ProgressPayload](updater.EventProgress)
	application.RegisterEvent[updater.ReadyPayload](updater.EventReady)
	application.RegisterEvent[updater.ErrorPayload](updater.EventError)
	application.RegisterEvent[string](bridge.EventCursorActivity)
	application.RegisterEvent[[]bridge.PetInfo](bridge.EventPetListChanged)
	application.RegisterEvent[map[string]string](bridge.EventPetStateChanged)
}

// Run 用于处理与 Run 相关的逻辑。
func Run(resources EmbeddedResources) error {
	logger.Init()
	netproxy.InstallDefaultTransport()

	embeddedCACertPEM := certs.EmbeddedCACertPEM()
	logEmbeddedCAInfo(embeddedCACertPEM)

	certManager, err := certs.NewEmbeddedManager()
	if err != nil {
		return err
	}

	defaultBackendBaseURL := "http://" + serverconfig.DefaultBackendListenAddr
	proxyServer, err := mitm.NewProxyServer(serverconfig.DefaultProxyListenAddr, defaultBackendBaseURL, "", "", certManager)
	if err != nil {
		return err
	}
	proxyService := bridge.NewProxyService(proxyServer, certManager, embeddedCACertPEM)
	// 将 mitm 的 Cursor 活跃信号接到 PetService（最终发射 cursor:activity 事件）
	proxyServer.SetCursorActivityCallback(proxyService.FireCursorActivity)
	adAssetBaseURL := defaultBackendBaseURL
	if cfg, err := proxyService.LoadUserConfig(); err == nil {
		adAssetBaseURL = browserReachableLoopbackBaseURL(cfg.BackendListenAddr)
	}
	metricsService := bridge.NewMetricsService()
	windowService := bridge.NewWindowService()
	petService := bridge.NewPetService()
	adCore := ads.NewService(ads.Options{
		StoreRoot:    appdata.AdsRootPath(),
		HTTPClient:   netproxy.NewHTTPClient(30 * time.Second),
		AppVersion:   buildinfo.CurrentVersion(),
		AssetBaseURL: adAssetBaseURL + ads.RoutePrefix,
		DeviceID:     cursor.GetDeviceID,
		Metrics: func(context.Context) (ads.MetricsSnapshot, error) {
			if err := appdata.EnsureAssistantHome(); err != nil {
				return ads.MetricsSnapshot{}, err
			}
			summary, err := historymetrics.LoadUsageSummary(appdata.UsageFilePath())
			if err != nil {
				return ads.MetricsSnapshot{}, err
			}
			return ads.MetricsSnapshot{
				TurnsTotal:         summary.TurnsTotal,
				RequestTokensTotal: summary.RequestTokensTotal,
				PromptTokensTotal:  summary.PromptTokensTotal,
				CacheReadTokens:    summary.CacheReadTokens,
				CacheWriteTokens:   summary.CacheWriteTokens,
			}, nil
		},
		ProviderCount: func(context.Context) (int, error) {
			cfg, err := proxyService.LoadUserConfig()
			if err != nil {
				return 0, err
			}
			return len(cfg.ModelAdapters), nil
		},
	})
	adService := bridge.NewAdService(adCore)
	var updateManager *updater.Manager

	var mainWindow *application.WebviewWindow
	adRefreshCtx, stopAdRefresh := context.WithCancel(context.Background())

	app := application.New(application.Options{
		Name:        appName,
		Description: appName,
		Services: []application.Service{
			application.NewService(proxyService),
			application.NewService(metricsService),
			application.NewService(windowService),
			application.NewService(adService),
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
			stopAdRefresh()
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

	refreshAdAssetBaseURL := func() bool {
		state := proxyService.GetState()
		backendListenAddr := strings.TrimSpace(state.BackendListenAddr)
		if backendListenAddr == "" {
			backendListenAddr = serverconfig.DefaultBackendListenAddr
		}
		return adCore.SetAssetBaseURL(browserReachableLoopbackBaseURL(backendListenAddr) + ads.RoutePrefix)
	}
	refreshAdRuntime := func() {
		runtimeState, err := adCore.GetRuntime(context.Background())
		if err != nil {
			return
		}
		app.Event.Emit(ads.EventUpdated, runtimeState)
	}
	refreshAd := func(ctx context.Context) {
		if ctx == nil {
			ctx = context.Background()
		}
		runtimeState, changed, err := adCore.Refresh(ctx)
		if err != nil || !changed {
			return
		}
		app.Event.Emit(ads.EventUpdated, runtimeState)
	}
	refreshAdAsync := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		go func() {
			defer cancel()
			refreshAd(ctx)
		}()
	}
	startAdRefreshLoop := func(ctx context.Context) {
		go func() {
			refreshAd(ctx)
			ticker := time.NewTicker(adRefreshInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					refreshAd(ctx)
				}
			}
		}()
	}

	updateManager = updater.NewManager(app)

	windowService.SetApp(app)
	windowService.SetUpdater(updateManager)
	petService.SetApp(app)

	// PET_DEBUG=1 时自动开启桌宠，方便无头/自动化环境采集 Window 层调试日志
	// （正常运行由前端 UI 触发 OpenPetWindow；此钩子仅用于调试，不影响正常流程）。
	if os.Getenv("PET_DEBUG") == "1" {
		go func() {
			time.Sleep(2 * time.Second) // 等 app 事件循环与窗口线程就绪
			log.Println("[Pet][DEBUG] PET_DEBUG=1: auto-opening pet window for diagnostics")
			windowService.OpenPetWindow()
		}()
	}

	// 连接 proxy 活动事件到 pet 状态
	proxyService.SetCursorActivityCallback(func(method, path string) {
		app.Event.Emit(bridge.EventCursorActivity, map[string]string{
			"method": method,
			"path":   path,
		})
	})
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
	window.RegisterHook(events.Common.WindowFocus, func(e *application.WindowEvent) {
		refreshAdAsync()
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
		if refreshAdAssetBaseURL() {
			refreshAdRuntime()
		}
	}
	app.Event.On("proxy:state", func(event *application.CustomEvent) {
		refreshTray()
	})
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(event *application.ApplicationEvent) {
		logger.Infof("应用版本：v%s", buildinfo.CurrentVersion())
		updateManager.Start()
		startAdRefreshLoop(adRefreshCtx)
		go func() {
			logger.Infof("application started, begin auto start service in background")
			if _, err := proxyService.StartProxy(); err != nil {
				logger.Errorf("自动启动服务失败: %v", err)
			} else {
				state := proxyService.GetState()
				if refreshAdAssetBaseURL() {
					refreshAdRuntime()
				}
				logger.Infof("代理已自动启动: %s", state.ProxyListenAddr)
			}
		}()
	})

	startItem.OnClick(func(ctx *application.Context) {
		if _, err := proxyService.StartProxy(); err != nil {
			logger.Errorf("启动服务失败: %v", err)
		} else if refreshAdAssetBaseURL() {
			refreshAdRuntime()
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
