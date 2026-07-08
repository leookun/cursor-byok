package main

import (
	"cursor/internal/app"
	"cursor/internal/logger"
	"embed"
	"os"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

//go:embed build/tray.png
var trayIcon []byte

func main() {
	logger.Init()
	if err := app.Run(app.EmbeddedResources{
		Assets:   assets,
		AppIcon:  appIcon,
		TrayIcon: trayIcon,
	}); err != nil {
		logger.Errorf("app run failed: %v", err)
		os.Exit(1)
	}
}
