package forwarder

import (
	"encoding/json"
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestMCPToolRegistryUsesCanonicalServerAndToolName(t *testing.T) {
	requestContext := &agentv1.RequestContext{
		McpFileSystemOptions: &agentv1.McpFileSystemOptions{
			McpDescriptors: []*agentv1.McpDescriptor{
				{
					ServerIdentifier: "user-fast-context",
					ServerName:       "fast-context",
					Tools: []*agentv1.McpToolDescriptor{
						{ToolName: "fast_context_search"},
					},
				},
				{
					ServerIdentifier: "backup-context",
					Tools: []*agentv1.McpToolDescriptor{
						{ToolName: "fast_context_search"},
					},
				},
			},
		},
	}

	registry := collectMCPToolServers(requestContext)
	if registry["user-fast-context-fast_context_search"] != "user-fast-context" {
		t.Fatalf("identifier canonical lookup = %q", registry["user-fast-context-fast_context_search"])
	}
	if registry["fast-context-fast_context_search"] != "user-fast-context" {
		t.Fatalf("server-name alias lookup = %q", registry["fast-context-fast_context_search"])
	}
	if registry["backup-context-fast_context_search"] != "backup-context" {
		t.Fatalf("backup-context canonical lookup = %q", registry["backup-context-fast_context_search"])
	}
	if _, found := registry["fast_context_search"]; found {
		t.Fatal("ambiguous bare MCP tool name must not select a server")
	}
}

func TestRewriteDirectMCPInvocationUsesCanonicalLookup(t *testing.T) {
	stream := &ActiveStream{
		MCPToolServers: map[string]string{
			"fast-context-fast_context_search": "fast-context",
		},
	}
	invocation := runtimecore.ToolInvocation{
		ToolName: "fast-context-fast_context_search",
		ArgsJSON: []byte(`{"query":"trace mcp lifecycle"}`),
	}

	rewritten := (&Service{}).rewriteDirectMCPToolInvocation(stream, invocation)
	if rewritten.ToolName != "CallMcpTool" {
		t.Fatalf("rewritten tool = %q, want CallMcpTool", rewritten.ToolName)
	}
	var payload struct {
		Server   string `json:"server"`
		ToolName string `json:"toolName"`
	}
	if err := json.Unmarshal(rewritten.ArgsJSON, &payload); err != nil {
		t.Fatalf("decode rewritten args: %v", err)
	}
	if payload.Server != "fast-context" {
		t.Fatalf("server = %q, want fast-context", payload.Server)
	}
	if payload.ToolName != "fast_context_search" {
		t.Fatalf("tool name = %q, want fast_context_search", payload.ToolName)
	}
}

func TestNormalizeCallMCPInvocationResolvesServerNameAlias(t *testing.T) {
	stream := &ActiveStream{
		MCPToolServers: map[string]string{
			"fast-context-fast_context_search": "user-fast-context",
		},
	}
	invocation := runtimecore.ToolInvocation{
		ToolName: "CallMcpTool",
		ArgsJSON: []byte(`{"server":"fast-context","toolName":"fast_context_search","arguments":{"query":"trace mcp lifecycle"}}`),
	}

	normalized := (&Service{}).normalizeCallMCPToolInvocation(stream, invocation)
	var payload struct {
		Server   string `json:"server"`
		ToolName string `json:"toolName"`
	}
	if err := json.Unmarshal(normalized.ArgsJSON, &payload); err != nil {
		t.Fatalf("decode normalized args: %v", err)
	}
	if payload.Server != "user-fast-context" {
		t.Fatalf("server = %q, want user-fast-context", payload.Server)
	}
	if payload.ToolName != "fast_context_search" {
		t.Fatalf("tool name = %q, want fast_context_search", payload.ToolName)
	}
}

func TestHydrateStreamMCPToolServersRestoresPersistedRegistry(t *testing.T) {
	stream := &ActiveStream{}
	persisted := map[string]string{
		"fast-context-fast_context_search": "fast-context",
	}

	hydrateStreamMCPToolServers(stream, persisted)
	if server := lookupMCPToolServer(stream, "fast-context-fast_context_search"); server != "fast-context" {
		t.Fatalf("rehydrated server = %q, want fast-context", server)
	}
}

func TestUpdateStreamMCPToolServersPersistsRegistryForNextRequest(t *testing.T) {
	store := NewConversationFileStore(t.TempDir())
	service := &Service{store: store}
	stream := &ActiveStream{ConversationID: "conversation"}
	requestContext := &agentv1.RequestContext{
		McpFileSystemOptions: &agentv1.McpFileSystemOptions{
			McpDescriptors: []*agentv1.McpDescriptor{{
				ServerIdentifier: "fast-context",
				Tools:            []*agentv1.McpToolDescriptor{{ToolName: "fast_context_search"}},
			}},
		},
	}

	service.updateStreamMCPToolServers(stream, requestContext)
	conversation, err := store.LoadConversation("conversation")
	if err != nil {
		t.Fatalf("load persisted conversation: %v", err)
	}
	nextStream := &ActiveStream{}
	hydrateStreamMCPToolServers(nextStream, conversation.MCPToolServers)
	if server := lookupMCPToolServer(nextStream, "fast-context-fast_context_search"); server != "fast-context" {
		t.Fatalf("persisted server = %q, want fast-context", server)
	}
}
