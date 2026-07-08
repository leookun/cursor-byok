//go:build windows

package netproxy

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

const internetSettingsRegistryPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

func loadSystemProxyConfig() systemProxyConfig {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsRegistryPath, registry.QUERY_VALUE)
	if err != nil {
		return systemProxyConfig{
			Source:         "windows",
			LoadErrMessage: sanitizeLoadError(err),
		}
	}
	defer key.Close()

	var cfg systemProxyConfig
	cfg.Source = "windows"
	if value, _, err := key.GetStringValue("AutoConfigURL"); err == nil && strings.TrimSpace(value) != "" {
		cfg.PACEnabled = true
		cfg.PACURL = strings.TrimSpace(value)
	}
	if value, _, err := key.GetIntegerValue("AutoDetect"); err == nil && value != 0 {
		cfg.PACEnabled = true
	}
	if value, _, err := key.GetStringValue("ProxyOverride"); err == nil {
		cfg.Bypass, cfg.ExcludeSimple = parseWindowsProxyOverride(value)
	}
	enabled, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return cfg
	}
	proxyServer, _, err := key.GetStringValue("ProxyServer")
	if err != nil {
		cfg.LoadErrMessage = sanitizeLoadError(err)
		return cfg
	}
	cfg.HTTPProxy, cfg.HTTPSProxy, cfg.SOCKSProxy = parseWindowsProxyServer(proxyServer)
	return cfg
}

func parseWindowsProxyServer(raw string) (string, string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", ""
	}
	if !strings.Contains(raw, "=") {
		proxy := normalizeProxyAddress("http", raw)
		return proxy, proxy, ""
	}

	var httpProxy, httpsProxy, socksProxy string
	for _, part := range strings.Split(raw, ";") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "http":
			httpProxy = normalizeProxyAddress("http", value)
		case "https":
			httpsProxy = normalizeProxyAddress("http", value)
		case "socks", "socks5":
			socksProxy = normalizeProxyAddress("socks5", value)
		}
	}
	return httpProxy, httpsProxy, socksProxy
}

func parseWindowsProxyOverride(raw string) ([]string, bool) {
	var (
		rules         []string
		excludeSimple bool
	)
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.EqualFold(part, "<local>") {
			excludeSimple = true
			continue
		}
		rules = append(rules, part)
	}
	return rules, excludeSimple
}

func sanitizeLoadError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return "unknown"
	}
	return strings.ReplaceAll(text, " ", "_")
}
