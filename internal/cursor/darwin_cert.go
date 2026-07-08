//go:build darwin

package cursor

import (
	"crypto/sha1"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os/exec"
	"strings"

	"cursor/internal/logger"
)

const (
	darwinSecurityExe       = "security"
	darwinLoginKeychainName = "login.keychain-db"
)

func getCertSHA1Fingerprint(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", fmt.Errorf("无法解析证书 PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("解析证书失败: %w", err)
	}
	fingerprint := fmt.Sprintf("%X", sha1.Sum(cert.Raw))
	return fingerprint, nil
}

func isCACertInstalled(certPEM []byte) (bool, error) {
	fingerprint, err := getCertSHA1Fingerprint(certPEM)
	if err != nil {
		return false, fmt.Errorf("获取证书指纹失败: %w", err)
	}

	out, err := exec.Command(darwinSecurityExe, "find-certificate", "-a", "-Z", darwinLoginKeychainName).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("检查 macOS 登录钥匙串失败: %w: %s", err, strings.TrimSpace(string(out)))
	}
	installed := strings.Contains(strings.ToUpper(string(out)), fingerprint)
	if installed {
		logger.Infof("isCACertInstalled: cert found in macOS login keychain, fingerprint=%s", fingerprint)
	} else {
		logger.Infof("isCACertInstalled: cert not found in macOS login keychain, fingerprint=%s", fingerprint)
	}
	return installed, nil
}

func installCACertToDarwinKeychain(certPEM []byte, certPath string) error {
	fingerprint, err := getCertSHA1Fingerprint(certPEM)
	if err != nil {
		return fmt.Errorf("获取证书指纹失败: %w", err)
	}

	logger.Infof("installCACertToDarwinKeychain: installing cert into login keychain, path=%s fingerprint=%s", certPath, fingerprint)
	out, err := exec.Command(
		darwinSecurityExe,
		"add-trusted-cert",
		"-d",
		"-r", "trustRoot",
		"-p", "ssl",
		"-k", darwinLoginKeychainName,
		certPath,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("安装 CA 到 macOS 登录钥匙串失败: %w: %s", err, strings.TrimSpace(string(out)))
	}

	installed, err := isCACertInstalled(certPEM)
	if err != nil {
		return fmt.Errorf("验证 macOS 证书安装状态失败: %w", err)
	}
	if !installed {
		return fmt.Errorf("证书导入命令已执行，但 macOS 登录钥匙串中未找到证书")
	}

	logger.Infof("installCACertToDarwinKeychain: cert installed successfully, fingerprint=%s", fingerprint)
	return nil
}

// EnsureCACertInstalled 确保 CA 证书已安装到 macOS 登录钥匙串。
func EnsureCACertInstalled(certPEM []byte, certPath string) error {
	installed, err := isCACertInstalled(certPEM)
	if err != nil {
		return fmt.Errorf("检查 macOS 证书安装状态失败: %w", err)
	}
	if installed {
		logger.Infof("ensureCACertInstalled: cert already installed in macOS login keychain, skipping")
		return nil
	}

	logger.Infof("ensureCACertInstalled: cert not installed in macOS login keychain, installing...")
	return installCACertToDarwinKeychain(certPEM, certPath)
}
