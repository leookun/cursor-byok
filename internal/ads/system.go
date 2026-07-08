package ads

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func displayOSName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	default:
		return "Windows"
	}
}

func displayOSVersion() string {
	switch runtime.GOOS {
	case "darwin":
		return commandOutput(2*time.Second, "sw_vers", "-productVersion")
	case "linux":
		if version := linuxPrettyName(); version != "" {
			return version
		}
		return commandOutput(2*time.Second, "uname", "-r")
	default:
		return ""
	}
}

func commandOutput(timeout time.Duration, name string, args ...string) string {
	cmd := exec.Command(name, args...)
	timer := time.AfterFunc(timeout, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer timer.Stop()
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func linuxPrettyName() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "PRETTY_NAME" {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	return ""
}
