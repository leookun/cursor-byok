package app

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	bridge "cursor/internal/bridge"
	"cursor/internal/logger"
)

// serveUserPetAsset 优先从用户 pets 目录提供自定义宠物资源。
// 命中并成功响应时返回 true；否则返回 false，交给内置静态资源处理。
func serveUserPetAsset(w http.ResponseWriter, r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}

	reqPath := path.Clean("/" + strings.TrimSpace(r.URL.Path))
	const petsPrefix = "/pets/"
	if reqPath != "/pets" && !strings.HasPrefix(reqPath, petsPrefix) {
		return false
	}
	if reqPath == "/pets" || reqPath == "/pets/" {
		return false
	}

	rel := strings.TrimPrefix(reqPath, petsPrefix)
	if rel == "" || rel == "." || strings.Contains(rel, "..") {
		return false
	}

	fullPath := filepath.Join(bridge.PetsDir(), filepath.FromSlash(rel))
	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		return false
	}

	http.ServeFile(w, r, fullPath)
	return true
}

// logEmbeddedCAInfo 记录嵌入 CA 证书信息。
func logEmbeddedCAInfo(certPEM []byte) {
	if len(certPEM) == 0 {
		logger.Errorf("embedded CA is empty")
		return
	}
	cert, err := parseEmbeddedCert(certPEM)
	if err != nil {
		logger.Errorf("parse embedded CA failed: %v", err)
		return
	}
	sum := sha256.Sum256(cert.Raw)
	logger.Infof(
		"embedded CA loaded: sha256=%s subject=%s valid=%s~%s",
		strings.ToUpper(hex.EncodeToString(sum[:])),
		cert.Subject.String(),
		cert.NotBefore.Format("2006-01-02"),
		cert.NotAfter.Format("2006-01-02"),
	)
}

// parseEmbeddedCert 解析 PEM 或 DER 编码的证书。
func parseEmbeddedCert(data []byte) (*x509.Certificate, error) {
	if block, _ := pem.Decode(data); block != nil {
		return x509.ParseCertificate(block.Bytes)
	}
	return x509.ParseCertificate(data)
}

// browserReachableLoopbackBaseURL 将监听地址转为浏览器可访问的环回地址。
func browserReachableLoopbackBaseURL(listenAddr string) string {
	host, port, err := splitHostPort(strings.TrimSpace(listenAddr))
	if err != nil || strings.TrimSpace(port) == "" {
		return "http://127.0.0.1:0"
	}
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// splitHostPort 简单的主机端口分离。
func splitHostPort(s string) (host, port string, err error) {
	host, port, err = net.SplitHostPort(s)
	return
}
