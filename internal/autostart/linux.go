//go:build linux

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const desktopFileName = "cursor-assistant.desktop"

var desktopFileContent = strings.ReplaceAll(`[Desktop Entry]
Type=Application
Name=Cursor 助手
Comment=Cursor 助手 - 本地模型代理
Exec=EXEC_COMMAND
Icon=cursor-assistant
Terminal=false
Categories=Development;
X-GNOME-Autostart-enabled=true
`, "EXEC_COMMAND", "%s")

func (m *Manager) enable(silent bool) error {
	dir, err := autostartDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建自启动目录失败: %w", err)
	}

	content := fmt.Sprintf(desktopFileContent, m.execCommand(silent))
	path := filepath.Join(dir, desktopFileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("写入自启动文件失败: %w", err)
	}
	return nil
}

func (m *Manager) disable() error {
	dir, err := autostartDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, desktopFileName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除自启动文件失败: %w", err)
	}
	return nil
}
