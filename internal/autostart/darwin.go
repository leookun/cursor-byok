//go:build darwin

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
)

const plistName = "com.cursor-assistant.autostart.plist"

func launchAgentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户主目录失败: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents"), nil
}

func (m *Manager) enable(silent bool) error {
	dir, err := launchAgentsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 LaunchAgents 目录失败: %w", err)
	}

	args := []string{m.exePath}
	if silent {
		args = append(args, "--headless")
	}

	plistContent := buildPlistContent(args)
	path := filepath.Join(dir, plistName)
	if err := os.WriteFile(path, []byte(plistContent), 0o644); err != nil {
		return fmt.Errorf("写入 plist 文件失败: %w", err)
	}
	return nil
}

func buildPlistContent(args []string) string {
	argsXML := ""
	for _, arg := range args {
		argsXML += fmt.Sprintf("\t\t<string>%s</string>\n", arg)
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.cursor-assistant.autostart</string>
	<key>ProgramArguments</key>
	<array>
%s	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
	<key>StandardOutPath</key>
	<string>/dev/null</string>
	<key>StandardErrorPath</key>
	<string>/dev/null</string>
</dict>
</plist>
`, argsXML)
}

func (m *Manager) disable() error {
	dir, err := launchAgentsDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, plistName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除 plist 文件失败: %w", err)
	}
	return nil
}
