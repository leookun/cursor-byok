// Package mcphub 将多个上游 MCP 后端聚合为单一 MCP 端点，对 Cursor 只暴露
// list-all-tools / call-tool 两个元工具，从而绕开 Cursor 对单会话工具数量的上限，
// 同时让多开的同类后端（如 ida×2、doge×2）靠各自的 serverName 精确路由、互不串扰。
//
// 设计移植自 warpdev/mcp-hub-mcp 的接口与行为，用 Go 原生实现（基于 mark3labs/mcp-go），
// 编译进主程序，运行时零外部依赖、无子进程。
package mcphub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"cursor/internal/logger"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	hubServerName    = "cursor-byok-mcp-hub"
	hubServerVersion = "1.0.0"
	hubClientName    = "cursor-byok-hub"
	hubClientVersion = "1.0.0"

	backendDialTimeout = 20 * time.Second
	backendListTimeout = 30 * time.Second
	backendCallTimeout = 10 * time.Minute
	backendHTTPCeiling = 10 * time.Minute
)

// Backend 描述一个被聚合的上游 MCP 后端。
type Backend struct {
	Name    string
	URL     string
	Enabled bool
}

// BackendsProvider 实时返回当前生效的后端列表（通常从配置读取），
// 使配置改动无需重启即可对网关生效。
type BackendsProvider func() []Backend

// Hub 聚合多个上游 MCP 后端，对 Cursor 暴露一个标准 MCP 端点。
type Hub struct {
	provider BackendsProvider

	once    sync.Once
	handler http.Handler
}

// NewHub 用给定的后端提供者创建一个网关。provider 为空时网关无可用后端但不会 panic。
func NewHub(provider BackendsProvider) *Hub {
	return &Hub{provider: provider}
}

// Handler 返回可挂载到 HTTP 路由上的 MCP 端点处理器（幂等，仅构建一次）。
func (h *Hub) Handler() http.Handler {
	h.once.Do(func() {
		srv := server.NewMCPServer(hubServerName, hubServerVersion)
		srv.AddTool(listAllToolsDef(), h.handleListAllTools)
		srv.AddTool(callToolDef(), h.handleCallTool)
		h.handler = server.NewStreamableHTTPServer(
			srv,
			server.WithStateLess(true),
			// 端点仅绑定在 127.0.0.1 上、供本机 Cursor 使用，关闭 DNS 重绑定/本地保护
			// 以避免 Cursor 携带的 Origin/Host 头触发误拒。
			server.WithDisableLocalhostProtection(true),
		)
	})
	return h.handler
}

// ---- 元工具定义 ----

func listAllToolsDef() mcp.Tool {
	return mcp.NewTool("list-all-tools",
		mcp.WithDescription("列出所有已聚合 MCP 后端的全部可用工具，含所属 serverName、工具名与参数 schema。"+
			"在调用 call-tool 之前，先用它了解有哪些后端、哪些工具。可选传入 serverName 只查看某一个后端。"),
		mcp.WithString("serverName",
			mcp.Description("可选：只列出该后端的工具；留空则列出全部后端。")),
	)
}

func callToolDef() mcp.Tool {
	return mcp.NewTool("call-tool",
		mcp.WithDescription("在指定 MCP 后端上调用某个工具。用 serverName 指定目标后端（多开的同类后端靠它区分），"+
			"toolName 指定工具名，toolArgs 为传给该工具的参数对象。"),
		mcp.WithString("serverName", mcp.Required(),
			mcp.Description("目标后端名称，见 list-all-tools 返回的 serverName。")),
		mcp.WithString("toolName", mcp.Required(),
			mcp.Description("要调用的工具名。")),
		mcp.WithObject("toolArgs",
			mcp.Description("传给该工具的参数对象（JSON object），无参数可省略。")),
	)
}

// ---- 元工具处理 ----

type serverToolsView struct {
	ServerName string     `json:"serverName"`
	Tools      []mcp.Tool `json:"tools,omitempty"`
	Error      string     `json:"error,omitempty"`
}

func (h *Hub) handleListAllTools(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	filter := strings.TrimSpace(req.GetString("serverName", ""))
	backends := h.enabledBackends()
	if filter != "" {
		filtered := backends[:0:0]
		for _, b := range backends {
			if strings.EqualFold(strings.TrimSpace(b.Name), filter) {
				filtered = append(filtered, b)
			}
		}
		backends = filtered
	}

	views := make([]serverToolsView, len(backends))
	var wg sync.WaitGroup
	for i := range backends {
		wg.Add(1)
		go func(i int, b Backend) {
			defer wg.Done()
			view := serverToolsView{ServerName: b.Name}
			tools, err := h.listBackendTools(ctx, b)
			if err != nil {
				view.Error = err.Error()
				logger.Infof("mcphub 列举后端工具失败 server=%s url=%s err=%v", b.Name, b.URL, err)
			} else {
				view.Tools = tools
			}
			views[i] = view
		}(i, backends[i])
	}
	wg.Wait()

	payload := map[string]any{"servers": views}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("序列化工具列表失败: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (h *Hub) handleCallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	serverName, err := req.RequireString("serverName")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数 serverName"), nil
	}
	toolName, err := req.RequireString("toolName")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数 toolName"), nil
	}
	var toolArgs any
	if args := req.GetArguments(); args != nil {
		toolArgs = args["toolArgs"]
	}

	backend, ok := h.findBackend(serverName)
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf(
			"未找到已启用的后端 %q，请用 list-all-tools 查看当前可用的 serverName。", serverName)), nil
	}

	conn, err := h.dial(ctx, backend.URL)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("连接后端 %q 失败: %v", serverName, err)), nil
	}
	defer conn.Close()

	callCtx, cancel := context.WithTimeout(ctx, backendCallTimeout)
	defer cancel()

	var callReq mcp.CallToolRequest
	callReq.Params.Name = toolName
	callReq.Params.Arguments = toolArgs
	result, err := conn.CallTool(callCtx, callReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("调用 %s/%s 失败: %v", serverName, toolName, err)), nil
	}
	return result, nil
}

// ---- 后端连接（每次操作新建，避免陈旧会话/状态问题）----

func (h *Hub) dial(ctx context.Context, url string) (*client.Client, error) {
	conn, err := client.NewStreamableHttpClient(url, transport.WithHTTPTimeout(backendHTTPCeiling))
	if err != nil {
		return nil, err
	}
	startCtx, cancel := context.WithTimeout(ctx, backendDialTimeout)
	defer cancel()
	if err := conn.Start(startCtx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	var initReq mcp.InitializeRequest
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: hubClientName, Version: hubClientVersion}
	if _, err := conn.Initialize(startCtx, initReq); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (h *Hub) listBackendTools(ctx context.Context, b Backend) ([]mcp.Tool, error) {
	conn, err := h.dial(ctx, b.URL)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	listCtx, cancel := context.WithTimeout(ctx, backendListTimeout)
	defer cancel()
	result, err := conn.ListTools(listCtx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, err
	}
	return result.Tools, nil
}

// ---- 后端列表 ----

func (h *Hub) enabledBackends() []Backend {
	if h.provider == nil {
		return nil
	}
	var out []Backend
	for _, b := range h.provider() {
		if !b.Enabled {
			continue
		}
		if strings.TrimSpace(b.Name) == "" || strings.TrimSpace(b.URL) == "" {
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (h *Hub) findBackend(name string) (Backend, bool) {
	name = strings.TrimSpace(name)
	for _, b := range h.enabledBackends() {
		if strings.EqualFold(strings.TrimSpace(b.Name), name) {
			return b, true
		}
	}
	return Backend{}, false
}

// ---- 连接测试（供管理界面使用）----

// TestResult 表示一次后端连通性测试结果。
type TestResult struct {
	OK         bool     `json:"ok"`
	ToolCount  int      `json:"toolCount"`
	ToolNames  []string `json:"toolNames,omitempty"`
	Error      string   `json:"error,omitempty"`
	DurationMS int64    `json:"durationMS"`
}

// TestBackend 连接指定 URL 的 MCP 后端并列举其工具，返回工具数量与名称，供界面展示。
func TestBackend(ctx context.Context, url string) TestResult {
	start := time.Now()
	fail := func(err error) TestResult {
		return TestResult{OK: false, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()}
	}
	if strings.TrimSpace(url) == "" {
		return fail(fmt.Errorf("URL 不能为空"))
	}
	h := &Hub{}
	conn, err := h.dial(ctx, url)
	if err != nil {
		return fail(err)
	}
	defer conn.Close()
	listCtx, cancel := context.WithTimeout(ctx, backendListTimeout)
	defer cancel()
	result, err := conn.ListTools(listCtx, mcp.ListToolsRequest{})
	if err != nil {
		return fail(err)
	}
	names := make([]string, 0, len(result.Tools))
	for _, t := range result.Tools {
		names = append(names, t.Name)
	}
	return TestResult{
		OK:         true,
		ToolCount:  len(result.Tools),
		ToolNames:  names,
		DurationMS: time.Since(start).Milliseconds(),
	}
}
