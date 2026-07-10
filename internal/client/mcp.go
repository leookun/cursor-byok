package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cursor/internal/mcphub"
)

// MCPTestResult 定义 MCP 后端连通性测试结果（供前端展示）。
type MCPTestResult = mcphub.TestResult

// TestMCPServer 连接指定 MCP 后端并列举其工具，返回工具数量与名称，供管理界面「测试连接」使用。
func (s *ProxyService) TestMCPServer(rawURL string) MCPTestResult {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return mcphub.TestBackend(ctx, rawURL)
}

// CursorMcpApplyResult 表示一键写入 Cursor mcp.json 的结果。
type CursorMcpApplyResult struct {
	OK         bool   `json:"ok"`
	Endpoint   string `json:"endpoint"`
	Path       string `json:"path"`
	BackupPath string `json:"backupPath,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ApplyCursorMcpHubConfig 一键把 Cursor 的 mcp.json 重写为仅指向本网关端点，并备份原文件。
// 端点优先取当前后端实际监听地址，其次取配置，最后回退默认，保证与界面展示一致。
func (s *ProxyService) ApplyCursorMcpHubConfig() CursorMcpApplyResult {
	endpoint := "http://" + s.resolveHubHostPort() + "/mcp/hub"

	home, err := os.UserHomeDir()
	if err != nil {
		return CursorMcpApplyResult{Endpoint: endpoint, Error: fmt.Sprintf("无法定位用户目录: %v", err)}
	}
	dir := filepath.Join(home, ".cursor")
	path := filepath.Join(dir, "mcp.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return CursorMcpApplyResult{Endpoint: endpoint, Path: path, Error: fmt.Sprintf("创建 .cursor 目录失败: %v", err)}
	}

	result := CursorMcpApplyResult{Endpoint: endpoint, Path: path}

	// 备份现有文件（存在且非空时）。
	if existing, readErr := os.ReadFile(path); readErr == nil && len(strings.TrimSpace(string(existing))) > 0 {
		backup := path + "." + time.Now().Format("20060102-150405") + ".bak"
		if writeErr := os.WriteFile(backup, existing, 0o644); writeErr != nil {
			return CursorMcpApplyResult{Endpoint: endpoint, Path: path, Error: fmt.Sprintf("备份原 mcp.json 失败: %v", writeErr)}
		}
		result.BackupPath = backup
	}

	payload := map[string]any{
		"mcpServers": map[string]any{
			"mcp-hub": map[string]any{
				"type": "http",
				"url":  endpoint,
			},
		},
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return CursorMcpApplyResult{Endpoint: endpoint, Path: path, BackupPath: result.BackupPath, Error: fmt.Sprintf("生成配置失败: %v", err)}
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return CursorMcpApplyResult{Endpoint: endpoint, Path: path, BackupPath: result.BackupPath, Error: fmt.Sprintf("写入 mcp.json 失败: %v", err)}
	}
	result.OK = true
	return result
}

// resolveHubHostPort 推导网关对 Cursor 暴露的 host:port（把 0.0.0.0/:: 归一到 127.0.0.1）。
func (s *ProxyService) resolveHubHostPort() string {
	addr := ""
	if s.backendHost != nil {
		addr = strings.TrimSpace(s.backendHost.ListenAddr())
	}
	if addr == "" {
		if cfg, err := s.LoadUserConfig(); err == nil {
			addr = strings.TrimSpace(cfg.BackendListenAddr)
		}
	}
	if addr == "" {
		addr = "127.0.0.1:18090"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
