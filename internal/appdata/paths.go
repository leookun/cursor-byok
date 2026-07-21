package appdata

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

const (
	appDirName       = ".cursor-byok"
	legacyAppDirName = ".cursor-local-assistant-v2"
)

// RootDir 返回应用配置根目录。
func RootDir() string {
	MigrateIfNeeded()
	return appRootDir(appDirName)
}

func legacyRootDir() string {
	return appRootDir(legacyAppDirName)
}

// MigrateIfNeeded 将旧的配置目录（legacyAppDirName）重命名为新的
// 配置目录（appDirName）。仅当旧目录存在且新目录不存在时执行迁移，
// 保证幂等且安全：若新目录已存在或旧目录不存在，均为 no-op。
func MigrateIfNeeded() {
	legacyDir := legacyRootDir()
	newDir := appRootDir(appDirName)

	if _, err := os.Stat(newDir); err == nil {
		return // 新目录已存在，无需迁移
	}
	if _, err := os.Stat(legacyDir); err != nil {
		return // 旧目录不存在，无需迁移
	}

	if err := os.Rename(legacyDir, newDir); err != nil {
		log.Printf("[appdata] migrate %s -> %s failed: %v", legacyDir, newDir, err)
		return
	}
	log.Printf("[appdata] migrated config dir %s -> %s", legacyDir, newDir)
}

func appRootDir(dirName string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return dirName
	}
	return filepath.Join(homeDir, dirName)
}

// ConfigFilePath 返回统一用户配置文件路径。
func ConfigFilePath() string {
	return filepath.Join(RootDir(), "config.yaml")
}

func DataRootPath() string {
	return filepath.Join(RootDir(), "data")
}

func HistoryRootPath() string {
	return filepath.Join(RootDir(), "history")
}

func UsageFilePath() string {
	return filepath.Join(HistoryRootPath(), "usage.json")
}

func CodebaseIndexRootPath() string {
	return filepath.Join(DataRootPath(), "codebase-index")
}

func DocsIndexRootPath() string {
	return filepath.Join(DataRootPath(), "docs-index")
}

func RulesRootPath() string {
	return filepath.Join(RootDir(), "rules")
}

// LogsRootPath 返回统一日志根目录路径。
func LogsRootPath() string {
	return filepath.Join(RootDir(), "logs")
}

// CACertFilePath 返回注入给宿主的 CA 文件路径。
func CACertFilePath() string {
	return filepath.Join(DataRootPath(), "ca.crt")
}

// CAKeyFilePath 返回 CA 私钥的本地存储路径。
// 私钥仅落盘在用户目录下（0600），不嵌入二进制。
func CAKeyFilePath() string {
	return filepath.Join(DataRootPath(), "ca.key")
}

// UpdatesRootPath 返回更新包下载的受信任目录路径。
// 下载的更新归档必须落入此目录，spawn 前由 updater 做路径围栏校验。
func UpdatesRootPath() string {
	return filepath.Join(DataRootPath(), "updates")
}
