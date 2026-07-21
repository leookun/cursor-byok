// scan_secrets.go — 构建前隐私扫描：检查 git 跟踪的文件和构建产物里有没有疑似密钥/token。
//
// 用法：
//   go run ./scripts/scan_secrets                    # 扫描 git 跟踪的源码
//   go run ./scripts/scan_secrets --binary bin/windows-64.exe  # 额外扫描构建产物
//
// 退出码：0=通过，1=发现疑似密钥。CI 可据此中断发布。
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// 疑似密钥的正则模式。匹配到任一即报警。
var secretPatterns = []*regexp.Regexp{
	// OpenAI / Anthropic 风格 key
	regexp.MustCompile(`sk-[a-zA-Z0-9_\-]{20,}`),
	// Bearer token
	regexp.MustCompile(`Bearer\s+[a-zA-Z0-9_\-\.]{20,}`),
	// Cursor access token（通常是 JWT 或长 hex）
	regexp.MustCompile(`eyJ[a-zA-Z0-9_\-]{20,}\.[a-zA-Z0-9_\-]{20,}`),
	// 通用 API key 赋值：api_key = "..." / apiKey: "..."（长度>15）
	regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|secret[_-]?key)\s*[:=]\s*["'][a-zA-Z0-9_\-]{15,}["']`),
	// 私钥头（只匹配 PRIVATE KEY，不匹配 RSA/CERTIFICATE 公钥证书）
	regexp.MustCompile(`-----BEGIN\s+(EC|OPENSSH|RSA\s+PRIVATE|PRIVATE)\s`),
}

// 白名单：匹配到的内容如果包含这些占位符，不算密钥。
var allowlist = []*regexp.Regexp{
	regexp.MustCompile(`YOUR_CURSOR_TOKEN_HERE`),
	regexp.MustCompile(`sk-xxxxxx`),
	regexp.MustCompile(`sk-\.\.\.`),
	regexp.MustCompile(`example\.com`),
	regexp.MustCompile(`placeholder`),
	regexp.MustCompile(`(?i)sample`),
	regexp.MustCompile(`(?i)test`),
	// 本地注入用的假 JWT（internal/runtime/defaults.go InjectAuthToken）
	// JWT payload base64 含 "fake-cursor-local-user"，但匹配的是原始字符串，
	// 所以用 JWT 固定前缀白名单。
	regexp.MustCompile(`eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9\.eyJzdWIiOiJmYWtl`),
}

type finding struct {
	file    string
	pattern string
	preview string
}

func main() {
	var binaryPath string
	flag.StringVar(&binaryPath, "binary", "", "额外扫描的构建产物路径（如 bin/windows-64.exe）")
	flag.Parse()

	var allFindings []finding

	// 1. 扫描 git 跟踪的源码文件
	trackedFiles := gitTrackedFiles()
	for _, f := range trackedFiles {
		if isBinaryByExt(f) {
			continue
		}
		findings := scanFile(f)
		allFindings = append(allFindings, findings...)
	}

	// 2. 额外扫描构建产物（只扫 token 类，不扫私钥头——
	//    标准库 crypto/x509 会把 "BEGIN RSA PRIVATE" 作为常量编入二进制，是误报）
	if binaryPath != "" {
		findings := scanBinary(binaryPath)
		allFindings = append(allFindings, findings...)
	}

	if len(allFindings) == 0 {
		fmt.Println("✅ 隐私扫描通过：未发现疑似密钥")
		return
	}

	fmt.Printf("❌ 隐私扫描失败：发现 %d 处疑似密钥\n", len(allFindings))
	for i, f := range allFindings {
		fmt.Printf("  %d. %s [%s] %s\n", i+1, f.file, f.pattern, f.preview)
	}
	fmt.Println("\n如果是误报，可在 scripts/scan_secrets.go 的 allowlist 添加对应占位符。")
	os.Exit(1)
}

func gitTrackedFiles() []string {
	cmd := exec.Command("git", "ls-files")
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "git ls-files 失败: %v\n", err)
		os.Exit(1)
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}

func isBinaryByExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".ico", ".icns", ".webp",
		".ttf", ".woff", ".woff2", ".otf",
		".zip", ".tar", ".gz", ".tar.gz",
		".exe", ".dll", ".so", ".dylib",
		".db", ".sqlite", ".bin":
		return true
	}
	return false
}

func scanFile(path string) []finding {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return scanBytes(path, data)
}

func scanBinary(path string) []finding {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取二进制失败 %s: %v\n", path, err)
		return nil
	}
	// 二进制扫描只检查 token 类模式，跳过私钥头
	// （标准库 crypto/x509 会把 "BEGIN RSA PRIVATE" 作为常量编入，是误报）。
	var findings []finding
	for _, pat := range secretPatterns {
		if strings.Contains(pat.String(), "BEGIN") {
			continue
		}
		matches := pat.FindAll(data, -1)
		for _, m := range matches {
			if isAllowlisted(m) {
				continue
			}
			preview := string(m)
			if len(preview) > 60 {
				preview = preview[:60] + "..."
			}
			findings = append(findings, finding{
				file:    path,
				pattern: pat.String(),
				preview: preview,
			})
		}
	}
	return findings
}

func scanBytes(file string, data []byte) []finding {
	var findings []finding
	for _, pat := range secretPatterns {
		matches := pat.FindAll(data, -1)
		for _, m := range matches {
			if isAllowlisted(m) {
				continue
			}
			preview := string(m)
			if len(preview) > 60 {
				preview = preview[:60] + "..."
			}
			findings = append(findings, finding{
				file:    file,
				pattern: pat.String(),
				preview: preview,
			})
		}
	}
	return findings
}

func isAllowlisted(match []byte) bool {
	for _, ap := range allowlist {
		if ap.Match(match) {
			return true
		}
	}
	// 检查匹配内容是否全是占位符常见词
	lower := bytes.ToLower(match)
	if bytes.Contains(lower, []byte("your_")) || bytes.Contains(lower, []byte("placeholder")) {
		return true
	}
	return false
}
