package cursor

import (
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/denisbrodbeck/machineid"
)

// GetDeviceID 用于处理与 GetDeviceID 相关的逻辑。
func GetDeviceID() (string, error) {
	deviceID, err := machineid.ProtectedID("cursor")
	if err != nil || strings.TrimSpace(deviceID) == "" {
		rawID, rawErr := machineid.ID()
		if rawErr != nil {
			if err != nil {
				return "", fmt.Errorf("获取设备码失败: %w", err)
			}
			return "", fmt.Errorf("获取设备码失败: %w", rawErr)
		}
		deviceID = rawID
	}
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return "", errors.New("获取设备码失败: 设备码为空")
	}
	return deviceID, nil
}

// defaultDeviceMeta 用于处理与 defaultDeviceMeta 相关的逻辑。
func defaultDeviceMeta() string {
	return fmt.Sprintf("%s / %s", displayOSName(runtime.GOOS), runtime.GOARCH)
}

// displayOSName 用于处理与 displayOSName 相关的逻辑。
func displayOSName(goos string) string {
	switch strings.ToLower(strings.TrimSpace(goos)) {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	default:
		return goos
	}
}
