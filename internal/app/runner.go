package app

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io/fs"
	goruntime "runtime"
	"strings"
	"time"

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
	application.RegisterEvent[bridge.ProxyState]("proxy:state")
	application.RegisterEvent[bridge.UserConfig]("user-config:changed")
	application.RegisterEvent[bridge.ModelAdapterTestResultsPayload]("model-adapter-test:updated")
	application.RegisterEvent[updater.StatePayload](updater.EventState)
	application.RegisterEvent[updater.ProgressPayload](updater.EventProgress)
	application.RegisterEvent[updater.ReadyPayload](updater.EventReady)
	application.RegisterEvent[updater.ErrorPayload](updater.EventError)
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
	metricsService := bridge.NewMetricsService()
	windowService := bridge.NewWindowService()
	var updateManager *updater.Manager

	var mainWindow *application.WebviewWindow

	app := application.New(application.Options{
		Name:        appName,
		Description: appName,
		Services: []application.Service{
			application.NewService(proxyService),
			application.NewService(metricsService),
			application.NewService(windowService),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(resources.Assets),
		},
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyAccessory,
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		OnShutdown: func() {
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
	app.Event.On("proxy:state", func(event *application.CustomEvent) {
		refreshTray()
	})
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(event *application.ApplicationEvent) {
		logger.Infof("应用版本：v%s", buildinfo.CurrentVersion())
		updateManager.Start()
		go func() {
			logger.Infof("application started, begin auto start service in background")
			if _, err := proxyService.StartProxy(); err != nil {
				logger.Errorf("自动启动服务失败: %v", err)
			} else {
				state := proxyService.GetState()
				logger.Infof("代理已自动启动: %s", state.ProxyListenAddr)
			}
		}()
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

// logEmbeddedCAInfo 用于处理与 logEmbeddedCAInfo 相关的逻辑。
func logEmbeddedCAInfo(certPEM []byte) {
	if len(certPEM) == 0 {
		logger.Errorf("embedded CA is empty")
		return
	}
	cert, err := parseEmbeddedCert(certPEM)
	if err != nil {
		logger.Errorf("parse embedded CA failed: %v", err)
		return
	}
	sum := sha256.Sum256(cert.Raw)
	logger.Infof(
		"embedded CA loaded: sha256=%s subject=%s valid=%s~%s",
		strings.ToUpper(hex.EncodeToString(sum[:])),
		cert.Subject.String(),
		cert.NotBefore.Format(time.RFC3339),
		cert.NotAfter.Format(time.RFC3339),
	)
}

// parseEmbeddedCert 用于处理与 parseEmbeddedCert 相关的逻辑。
func parseEmbeddedCert(data []byte) (*x509.Certificate, error) {
	if block, _ := pem.Decode(data); block != nil {
		return x509.ParseCertificate(block.Bytes)
	}
	return x509.ParseCertificate(data)
}
