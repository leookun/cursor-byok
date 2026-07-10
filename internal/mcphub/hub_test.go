package mcphub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// newFakeBackend 启动一个只有一个 echo 工具的假 MCP 后端，返回其 URL 与关闭函数。
func newFakeBackend(t *testing.T, name string) (string, func()) {
	t.Helper()
	srv := server.NewMCPServer(name, "1.0.0")
	srv.AddTool(
		mcp.NewTool("echo", mcp.WithDescription("原样回显 text"),
			mcp.WithString("text", mcp.Required(), mcp.Description("要回显的文本"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			text, err := req.RequireString("text")
			if err != nil {
				return mcp.NewToolResultError("missing text"), nil
			}
			return mcp.NewToolResultText("echo:" + text), nil
		},
	)
	handler := server.NewStreamableHTTPServer(srv, server.WithStateLess(true), server.WithDisableLocalhostProtection(true))
	ts := httptest.NewServer(handler)
	return ts.URL, ts.Close
}

func TestHubAggregatesAndRoutes(t *testing.T) {
	backendURL, closeBackend := newFakeBackend(t, "fake-backend")
	defer closeBackend()

	hub := NewHub(func() []Backend {
		return []Backend{{Name: "doge-LC", URL: backendURL, Enabled: true}}
	})
	hubServer := httptest.NewServer(hub.Handler())
	defer hubServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cli, err := client.NewStreamableHttpClient(hubServer.URL)
	if err != nil {
		t.Fatalf("new hub client: %v", err)
	}
	defer cli.Close()
	if err := cli.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	var initReq mcp.InitializeRequest
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "1.0.0"}
	if _, err := cli.Initialize(ctx, initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	// 网关只暴露两个元工具。
	tools, err := cli.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	if !names["list-all-tools"] || !names["call-tool"] {
		t.Fatalf("hub 应只暴露 list-all-tools/call-tool，实际: %v", names)
	}
	if len(tools.Tools) != 2 {
		t.Fatalf("hub 应恰好暴露 2 个元工具，实际 %d 个", len(tools.Tools))
	}

	// list-all-tools 应聚合出后端 doge-LC 的 echo 工具。
	var listReq mcp.CallToolRequest
	listReq.Params.Name = "list-all-tools"
	listRes, err := cli.CallTool(ctx, listReq)
	if err != nil {
		t.Fatalf("call list-all-tools: %v", err)
	}
	listText := firstText(t, listRes)
	if !strings.Contains(listText, "doge-LC") || !strings.Contains(listText, "echo") {
		t.Fatalf("list-all-tools 未聚合后端工具，返回: %s", listText)
	}

	// call-tool 应路由到 doge-LC 的 echo。
	var callReq mcp.CallToolRequest
	callReq.Params.Name = "call-tool"
	callReq.Params.Arguments = map[string]any{
		"serverName": "doge-LC",
		"toolName":   "echo",
		"toolArgs":   map[string]any{"text": "hello"},
	}
	callRes, err := cli.CallTool(ctx, callReq)
	if err != nil {
		t.Fatalf("call call-tool: %v", err)
	}
	if got := firstText(t, callRes); got != "echo:hello" {
		t.Fatalf("call-tool 路由结果错误，期望 echo:hello，实际 %q", got)
	}

	// 未知后端应返回错误结果而非 panic。
	var badReq mcp.CallToolRequest
	badReq.Params.Name = "call-tool"
	badReq.Params.Arguments = map[string]any{"serverName": "nope", "toolName": "echo"}
	badRes, err := cli.CallTool(ctx, badReq)
	if err != nil {
		t.Fatalf("call unknown backend: %v", err)
	}
	if !badRes.IsError {
		t.Fatalf("调用未知后端应返回 IsError=true")
	}
}

func firstText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		return ""
	}
	raw, err := json.Marshal(res.Content[0])
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	var tc struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &tc)
	return tc.Text
}
