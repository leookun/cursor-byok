//go:build darwin

package netproxy

import (
	"os/exec"
	"strconv"
	"strings"
)

func loadSystemProxyConfig() systemProxyConfig {
	output, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		return systemProxyConfig{
			Source:         "macos",
			LoadErrMessage: sanitizeLoadError(err),
		}
	}
	cfg := parseDarwinProxyOutput(string(output))
	cfg.Source = "macos"
	return cfg
}

func parseDarwinProxyOutput(output string) systemProxyConfig {
	var cfg systemProxyConfig
	values := map[string]string{}
	inExceptions := false

	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if line == "}" {
			inExceptions = false
			continue
		}
		key, value, ok := strings.Cut(line, " : ")
		if !ok {
			key, value, ok = strings.Cut(line, ":")
		}
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if inExceptions {
			if value != "" && value != "<array> {" {
				cfg.Bypass = append(cfg.Bypass, value)
			}
			continue
		}
		if key == "ExceptionsList" {
			inExceptions = strings.Contains(value, "<array>")
			continue
		}
		values[key] = value
	}

	if boolValue(values["HTTPEnable"]) {
		cfg.HTTPProxy = hostPortProxy("http", values["HTTPProxy"], values["HTTPPort"])
	}
	if boolValue(values["HTTPSEnable"]) {
		cfg.HTTPSProxy = hostPortProxy("http", values["HTTPSProxy"], values["HTTPSPort"])
	}
	if boolValue(values["SOCKSEnable"]) {
		cfg.SOCKSProxy = hostPortProxy("socks5", values["SOCKSProxy"], values["SOCKSPort"])
	}
	cfg.ExcludeSimple = boolValue(values["ExcludeSimpleHostnames"])
	cfg.PACEnabled = boolValue(values["ProxyAutoConfigEnable"]) || boolValue(values["ProxyAutoDiscoveryEnable"])
	cfg.PACURL = strings.TrimSpace(values["ProxyAutoConfigURLString"])
	return cfg
}

func boolValue(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return value == "1" || value == "true" || value == "yes"
}

func hostPortProxy(scheme string, host string, portValue string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	port := strings.TrimSpace(portValue)
	if port != "" {
		if _, err := strconv.Atoi(port); err == nil {
			host = host + ":" + port
		}
	}
	return normalizeProxyAddress(scheme, host)
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
