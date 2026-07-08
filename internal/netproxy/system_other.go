//go:build !darwin && !windows

package netproxy

func loadSystemProxyConfig() systemProxyConfig {
	return systemProxyConfig{Source: "env-only"}
}
