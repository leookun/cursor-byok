package autostart

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/logger"
)

// Manager 管理操作系统的开机自启动条目。
type Manager struct {
	store   *serverconfig.Store
	exePath string
	appName string
}

// NewManager 创建一个 autostart Manager。
// store 用于读写用户配置，appName 用于生成自启动条目名称。
func NewManager(store *serverconfig.Store, appName string) *Manager {
	exePath, err := os.Executable()
	if err != nil {
		exePath = ""
	}
	return &Manager{
		store:   store,
		exePath: exePath,
		appName: appName,
	}
}

// State 返回当前自启动状态。
type State struct {
	AutoStart       bool `json:"autoStart"`
	AutoStartSilent bool `json:"autoStartSilent"`
}

// GetState 从配置中读取自启动状态。
func (m *Manager) GetState(ctx context.Context) (State, error) {
	if m.store == nil {
		return State{}, nil
	}
	cfg, err := m.store.Load(ctx)
	if err != nil {
		return State{}, fmt.Errorf("读取配置失败: %w", err)
	}
	return State{
		AutoStart:       cfg.AutoStart,
		AutoStartSilent: cfg.AutoStartSilent,
	}, nil
}

// SetState 更新自启动配置并同步操作系统自启动条目。
func (m *Manager) SetState(ctx context.Context, enabled, silent bool) error {
	if m.store == nil {
		return fmt.Errorf("配置存储未初始化")
	}
	cfg, err := m.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("读取配置失败: %w", err)
	}
	cfg.AutoStart = enabled
	cfg.AutoStartSilent = silent
	if _, err := m.store.Save(ctx, cfg); err != nil {
		return fmt.Errorf("保存配置失败: %w", err)
	}
	if err := m.syncOS(ctx, enabled, silent); err != nil {
		logger.Errorf("同步操作系统自启动条目失败: %v", err)
	}
	return nil
}

// SyncOnStartup 在应用启动时同步操作系统自启动条目到当前配置状态。
func (m *Manager) SyncOnStartup(ctx context.Context) {
	if m.store == nil {
		return
	}
	cfg, err := m.store.Load(ctx)
	if err != nil {
		logger.Errorf("启动同步自启动: 读取配置失败: %v", err)
		return
	}
	if err := m.syncOS(ctx, cfg.AutoStart, cfg.AutoStartSilent); err != nil {
		logger.Errorf("启动同步自启动失败: %v", err)
	}
}

func (m *Manager) syncOS(_ context.Context, enabled, silent bool) error {
	if m.exePath == "" {
		return fmt.Errorf("无法获取可执行文件路径")
	}
	if enabled {
		return m.enable(silent)
	}
	return m.disable()
}

// execCommand 返回启动命令（含 --headless 参数时静默启动）。
func (m *Manager) execCommand(silent bool) string {
	if silent {
		return fmt.Sprintf("%q --headless", m.exePath)
	}
	return fmt.Sprintf("%q", m.exePath)
}

// autostartDir 返回自启动配置文件所在目录。
func autostartDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户主目录失败: %w", err)
	}
	return filepath.Join(home, ".config", "autostart"), nil
}
