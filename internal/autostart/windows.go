//go:build windows

package autostart

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

const registryKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
const registryValueName = "CursorAssistant"

func (m *Manager) enable(silent bool) error {
	key, _, err := registry.CreateKey(
		registry.CURRENT_USER,
		registryKeyPath,
		registry.SET_VALUE|registry.QUERY_VALUE,
	)
	if err != nil {
		return fmt.Errorf("打开注册表键失败: %w", err)
	}
	defer key.Close()

	if err := key.SetStringValue(registryValueName, m.execCommand(silent)); err != nil {
		return fmt.Errorf("写入注册表失败: %w", err)
	}
	return nil
}

func (m *Manager) disable() error {
	key, err := registry.OpenKey(
		registry.CURRENT_USER,
		registryKeyPath,
		registry.SET_VALUE,
	)
	if err != nil {
		if err == registry.ErrNotExist {
			return nil
		}
		return fmt.Errorf("打开注册表键失败: %w", err)
	}
	defer key.Close()

	if err := key.DeleteValue(registryValueName); err != nil {
		if err == registry.ErrNotExist {
			return nil
		}
		return fmt.Errorf("删除注册表值失败: %w", err)
	}
	return nil
}
