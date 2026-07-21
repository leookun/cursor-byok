// mcp_tools.go extracts MCP tool handling from service.go (TD-002).
// Contains: updateStreamMCPToolServers, rewriteDirectMCPToolInvocation,
// normalizeCallMCPToolInvocation, lookupMCPToolServer.
package forwarder

import (
	"encoding/json"
	"strings"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func (service *Service) updateStreamMCPToolServers(stream *ActiveStream, requestContext *agentv1.RequestContext) {
	if stream == nil {
		return
	}
	servers := collectMCPToolServers(requestContext)
	if len(servers) == 0 {
		return
	}
	stream.mu.Lock()
	if stream.MCPToolServers == nil {
		stream.MCPToolServers = make(map[string]string, len(servers))
	}
	for toolName, serverIdentifier := range servers {
		trimmedToolName := strings.TrimSpace(toolName)
		trimmedServerIdentifier := strings.TrimSpace(serverIdentifier)
		if trimmedToolName == "" || trimmedServerIdentifier == "" {
			continue
		}
		stream.MCPToolServers[trimmedToolName] = trimmedServerIdentifier
	}
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	// ADR-026: Sync dynamically discovered MCP tools into Tool Runtime
	// so they have metadata (category, cacheable) and can use result cache.
	if service.toolRuntime != nil && requestContext != nil {
		mcpTools := collectMCPToolInfos(requestContext, servers)
		if len(mcpTools) > 0 {
			service.toolRuntime.SyncMCPTools(mcpTools)
		}
	}
}

func (service *Service) rewriteDirectMCPToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) runtimecore.ToolInvocation {
	toolName := strings.TrimSpace(invocation.ToolName)
	if toolName == "" || isExecTool(toolName) {
		return invocation
	}
	serverIdentifier := lookupMCPToolServer(stream, toolName)
	if serverIdentifier == "" {
		return invocation
	}

	arguments := make(map[string]any)
	if len(invocation.ArgsJSON) > 0 {
		_ = json.Unmarshal(invocation.ArgsJSON, &arguments)
	}
	payload := struct {
		Server    string         `json:"server"`
		ToolName  string         `json:"toolName"`
		Arguments map[string]any `json:"arguments,omitempty"`
	}{
		Server:    serverIdentifier,
		ToolName:  toolName,
		Arguments: arguments,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return invocation
	}
	invocation.ToolName = "CallMcpTool"
	invocation.ArgsJSON = encoded
	return invocation
}

func (service *Service) normalizeCallMCPToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) runtimecore.ToolInvocation {
	if strings.TrimSpace(invocation.ToolName) != "CallMcpTool" {
		return invocation
	}

	payload, err := runtimecore.DecodeMCPToolPayload(invocation.ArgsJSON)
	if err != nil {
		return invocation
	}

	serverIdentifier := firstNonEmpty(payload.Server, payload.ProviderIdentifier)
	toolName := strings.TrimSpace(payload.ToolName)
	name := strings.TrimSpace(payload.Name)
	if toolName == "" {
		toolName = runtimecore.InferMCPToolName(serverIdentifier, name)
	}
	if serverIdentifier == "" {
		serverIdentifier = lookupMCPToolServer(stream, toolName)
		if serverIdentifier == "" && name != "" {
			serverIdentifier = runtimecore.InferMCPServerIdentifier(name)
		}
	}

	if toolName == "" {
		return invocation
	}

	normalized := struct {
		Server    string         `json:"server"`
		ToolName  string         `json:"toolName"`
		Arguments map[string]any `json:"arguments,omitempty"`
	}{
		Server:    serverIdentifier,
		ToolName:  toolName,
		Arguments: payload.Arguments,
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return invocation
	}
	invocation.ArgsJSON = encoded
	return invocation
}

func lookupMCPToolServer(stream *ActiveStream, toolName string) string {
	trimmedToolName := strings.TrimSpace(toolName)
	if trimmedToolName == "" {
		return ""
	}
	if stream != nil {
		stream.mu.Lock()
		serverIdentifier := strings.TrimSpace(stream.MCPToolServers[trimmedToolName])
		stream.mu.Unlock()
		if serverIdentifier != "" {
			return serverIdentifier
		}
	}
	return ""
}
