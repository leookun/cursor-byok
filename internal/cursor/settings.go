package cursor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"cursor/internal/appdata"
	"cursor/internal/logger"
)

// injectedCursorSettingsKeys 表示当前模块中的 injectedCursorSettingsKeys 状态值。
var injectedCursorSettingsKeys = []string{
	"http.proxy",
	"http.proxyKerberosServicePrincipal",
	"http.proxySupport",
	"cursor.general.disableHttp2",
	"http.experimental.systemCertificatesV2",
}

// EnsureCACertFile 用于处理与 EnsureCACertFile 相关的逻辑。
func EnsureCACertFile(certPEM []byte, currentPath string) (string, error) {
	certPath := appdata.CACertFilePath()
	if samePath(strings.TrimSpace(currentPath), certPath) {
		if _, err := os.Stat(certPath); err == nil {
			logger.Infof("ensureCACertFile: reusing path=%s", certPath)
			return certPath, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return "", fmt.Errorf("创建证书配置目录失败: %w", err)
	}

	if existing, err := os.ReadFile(certPath); err == nil && bytes.Equal(existing, certPEM) {
		logger.Infof("ensureCACertFile: unchanged path=%s", certPath)
		return certPath, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("读取内置 CA 证书失败: %w", err)
	}

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return "", fmt.Errorf("写入内置 CA 证书失败: %w", err)
	}
	sum := sha256.Sum256(certPEM)
	logger.Infof(
		"ensureCACertFile: wrote path=%s sha256=%s size=%d",
		certPath,
		strings.ToUpper(hex.EncodeToString(sum[:])),
		len(certPEM),
	)
	return certPath, nil
}

func samePath(left string, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

// SetSystemNodeExtraCACerts 用于处理与 SetSystemNodeExtraCACerts 相关的逻辑。
func SetSystemNodeExtraCACerts(caCertPath string) error {
	caCertPath = strings.TrimSpace(caCertPath)
	if caCertPath == "" {
		return errors.New("CA 证书路径为空")
	}
	if err := os.Setenv("NODE_EXTRA_CA_CERTS", caCertPath); err != nil {
		return fmt.Errorf("设置进程环境变量失败: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("launchctl", "setenv", "NODE_EXTRA_CA_CERTS", caCertPath).CombinedOutput()
		if err != nil {
			return fmt.Errorf("写入 macOS 用户环境变量失败: %v: %s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		// Linux 发行版环境变量持久化方式差异较大，这里先确保当前进程生效。
		logger.Infof("setSystemNodeExtraCACerts: linux detected, applied to current process only")
	default:
		return fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}

	logger.Infof("setSystemNodeExtraCACerts: NODE_EXTRA_CA_CERTS=%s", caCertPath)
	return nil
}

// ClearSystemNodeExtraCACerts 用于处理与 ClearSystemNodeExtraCACerts 相关的逻辑。
func ClearSystemNodeExtraCACerts() error {
	if err := os.Unsetenv("NODE_EXTRA_CA_CERTS"); err != nil {
		return fmt.Errorf("清理进程环境变量失败: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("launchctl", "unsetenv", "NODE_EXTRA_CA_CERTS").CombinedOutput()
		if err != nil {
			return fmt.Errorf("清理 macOS 用户环境变量失败: %v: %s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		logger.Infof("clearSystemNodeExtraCACerts: linux detected, cleared in current process only")
	default:
		return fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}

	logger.Infof("clearSystemNodeExtraCACerts: NODE_EXTRA_CA_CERTS cleared")
	return nil
}

// WriteUserProxySettings 用于处理与 WriteUserProxySettings 相关的逻辑。
func WriteUserProxySettings(proxyURL string) error {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return errors.New("代理地址为空")
	}

	settingsPath, err := resolveCursorSettingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("创建 Cursor 配置目录失败: %w", err)
	}

	settings := make(map[string]any)
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("读取 Cursor 配置失败: %w", err)
		}
	} else if len(bytes.TrimSpace(data)) > 0 {
		parsed, err := decodeCursorSettingsJSONC(data)
		if err != nil {
			if removeErr := os.Remove(settingsPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return fmt.Errorf("解析 Cursor 配置失败，且删除损坏配置失败: %w", removeErr)
			}
			logger.Infof("writeCursorUserProxySettings: removed invalid settings path=%s err=%v", settingsPath, err)
			data = nil
		} else {
			settings = parsed
		}
	}

	settings["http.proxy"] = proxyURL
	settings["http.proxyKerberosServicePrincipal"] = proxyURL
	settings["http.proxySupport"] = "on"
	settings["cursor.general.disableHttp2"] = true
	settings["http.experimental.systemCertificatesV2"] = true

	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 Cursor 配置失败: %w", err)
	}
	encoded = append(encoded, '\n')

	if len(bytes.TrimSpace(data)) > 0 && bytes.Equal(data, encoded) {
		logger.Infof("writeCursorUserProxySettings: unchanged path=%s proxy=%s", settingsPath, proxyURL)
		return nil
	}

	tempPath := settingsPath + ".tmp"
	if err := os.WriteFile(tempPath, encoded, 0o644); err != nil {
		return fmt.Errorf("写入 Cursor 配置临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, settingsPath); err != nil {
		return fmt.Errorf("保存 Cursor 配置失败: %w", err)
	}

	logger.Infof("writeCursorUserProxySettings: path=%s proxy=%s", settingsPath, proxyURL)
	return nil
}

// ClearUserProxySettings 用于处理与 ClearUserProxySettings 相关的逻辑。
func ClearUserProxySettings() error {
	settingsPath, err := resolveCursorSettingsPath()
	if err != nil {
		return err
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("读取 Cursor 配置失败: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}

	settings := make(map[string]any)
	parsed, err := decodeCursorSettingsJSONC(data)
	if err != nil {
		return fmt.Errorf("解析 Cursor 配置失败: %w", err)
	}
	settings = parsed

	changed := false
	for _, key := range injectedCursorSettingsKeys {
		if _, exists := settings[key]; exists {
			delete(settings, key)
			changed = true
		}
	}
	if !changed {
		return nil
	}

	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 Cursor 配置失败: %w", err)
	}
	encoded = append(encoded, '\n')

	tempPath := settingsPath + ".tmp"
	if err := os.WriteFile(tempPath, encoded, 0o644); err != nil {
		return fmt.Errorf("写入 Cursor 配置临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, settingsPath); err != nil {
		return fmt.Errorf("保存 Cursor 配置失败: %w", err)
	}

	logger.Infof("clearCursorUserProxySettings: path=%s", settingsPath)
	return nil
}

// resolveCursorSettingsPath 用于处理与 resolveCursorSettingsPath 相关的逻辑。
func resolveCursorSettingsPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户目录失败: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support", "Cursor", "User", "settings.json"), nil
	case "windows":
		appData := os.Getenv("APPDATA")
		if strings.TrimSpace(appData) == "" {
			appData = filepath.Join(homeDir, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Cursor", "User", "settings.json"), nil
	case "linux":
		configDir := os.Getenv("XDG_CONFIG_HOME")
		if strings.TrimSpace(configDir) == "" {
			configDir = filepath.Join(homeDir, ".config")
		}
		return filepath.Join(configDir, "Cursor", "User", "settings.json"), nil
	default:
		return "", fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}
}

// decodeCursorSettingsJSONC 用于处理与 decodeCursorSettingsJSONC 相关的逻辑。
func decodeCursorSettingsJSONC(data []byte) (map[string]any, error) {
	result := make(map[string]any)
	normalized, err := normalizeJSONC(data)
	if err != nil {
		return nil, err
	}
	normalized = bytes.TrimSpace(normalized)
	if len(normalized) == 0 {
		return result, nil
	}
	if err := json.Unmarshal(normalized, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// normalizeJSONC 用于处理与 normalizeJSONC 相关的逻辑。
func normalizeJSONC(data []byte) ([]byte, error) {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}
	withoutComments, err := stripJSONCComments(data)
	if err != nil {
		return nil, err
	}
	return stripJSONCTrailingCommas(withoutComments), nil
}

// stripJSONCComments 用于处理与 stripJSONCComments 相关的逻辑。
func stripJSONCComments(data []byte) ([]byte, error) {
	out := make([]byte, 0, len(data))
	inString := false
	inLineComment := false
	inBlockComment := false
	escaped := false

	for i := 0; i < len(data); i++ {
		ch := data[i]

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
				out = append(out, ch)
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && i+1 < len(data) && data[i+1] == '/' {
				inBlockComment = false
				i++
				continue
			}
			if ch == '\n' {
				out = append(out, ch)
			}
			continue
		}
		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}
		if ch == '/' && i+1 < len(data) {
			next := data[i+1]
			if next == '/' {
				inLineComment = true
				i++
				continue
			}
			if next == '*' {
				inBlockComment = true
				i++
				continue
			}
		}
		out = append(out, ch)
	}

	if inBlockComment {
		return nil, errors.New("JSONC 块注释未闭合")
	}
	return out, nil
}

// stripJSONCTrailingCommas 用于处理与 stripJSONCTrailingCommas 相关的逻辑。
func stripJSONCTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false

	for i := 0; i < len(data); i++ {
		ch := data[i]
		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}

		if ch == ',' {
			j := i + 1
			for j < len(data) && isJSONWhitespace(data[j]) {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
		}

		out = append(out, ch)
	}

	return out
}

// isJSONWhitespace 用于处理与 isJSONWhitespace 相关的逻辑。
func isJSONWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n'
}

// ProxyURLFromListenAddr 用于处理与 ProxyURLFromListenAddr 相关的逻辑。
func ProxyURLFromListenAddr(listenAddr string) string {
	addr := strings.TrimSpace(listenAddr)
	if addr == "" {
		return "http://127.0.0.1:8080"
	}

	// :8189 -> 127.0.0.1:8189
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr
	}

	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
			host = "127.0.0.1"
		}
		return "http://" + net.JoinHostPort(host, port)
	}

	return "http://" + addr
}
