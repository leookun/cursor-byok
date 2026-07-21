// Package client — Tool Runtime 管理与前端 DTO。
package client

import "encoding/json"

// ToolEntryDTO 工具条目 DTO（供前端展示）。
type ToolEntryDTO struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Category     string          `json:"category"`
	Enabled      bool            `json:"enabled"`
	Cacheable    bool            `json:"cacheable"`
	CacheTTL     string          `json:"cacheTTL"` // 人类可读（如 "5m0s"）
	InternalName string          `json:"internalName"`
	Server       string          `json:"server,omitempty"`
	Schema       json.RawMessage `json:"schema,omitempty"`
}

// ClearToolCacheResult 清空工具结果缓存的返回 DTO。
type ClearToolCacheResult struct {
	Cleared bool `json:"cleared"`
}

// ToolCacheStatsDTO 工具缓存统计 DTO。
type ToolCacheStatsDTO struct {
	Hits    int64   `json:"hits"`
	Misses  int64   `json:"misses"`
	Entries int     `json:"entries"`
	HitRate float64 `json:"hitRate"`
}

// MCPServerInfoDTO MCP server 概要 DTO。
type MCPServerInfoDTO struct {
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	ToolCount   int    `json:"toolCount"`
	EnabledTool int    `json:"enabledTool"`
}

// ListTools 列出所有已注册工具。
func (s *ProxyService) ListTools() ([]ToolEntryDTO, error) {
	if s == nil || s.backendHost == nil {
		return nil, nil
	}
	rt := s.backendHost.ToolRuntime()
	if rt == nil {
		return nil, nil
	}
	entries := rt.List()
	result := make([]ToolEntryDTO, 0, len(entries))
	for _, e := range entries {
		result = append(result, ToolEntryDTO{
			Name:         e.Name,
			Description:  e.Description,
			Category:     string(e.Category),
			Enabled:      e.Enabled,
			Cacheable:    e.Cacheable,
			CacheTTL:     e.CacheTTL.String(),
			InternalName: e.InternalName,
			Server:       e.Server,
			Schema:       e.Schema,
		})
	}
	return result, nil
}

// ListMCPServers 列出所有已知 MCP server。
func (s *ProxyService) ListMCPServers() ([]MCPServerInfoDTO, error) {
	if s == nil || s.backendHost == nil {
		return nil, nil
	}
	rt := s.backendHost.ToolRuntime()
	if rt == nil {
		return nil, nil
	}
	servers := rt.ListMCPServers()
	result := make([]MCPServerInfoDTO, 0, len(servers))
	for _, sv := range servers {
		result = append(result, MCPServerInfoDTO{
			Name:        sv.Name,
			Enabled:     sv.Enabled,
			ToolCount:   sv.ToolCount,
			EnabledTool: sv.EnabledTool,
		})
	}
	return result, nil
}

// ToggleMCPServer 启用/禁用指定 MCP server 的所有工具。
func (s *ProxyService) ToggleMCPServer(server string, enabled bool) error {
	if s == nil || s.backendHost == nil {
		return nil
	}
	rt := s.backendHost.ToolRuntime()
	if rt == nil {
		return nil
	}
	return rt.ToggleMCPServer(server, enabled)
}

// ToggleTool 启用/禁用指定工具。
func (s *ProxyService) ToggleTool(name string, enabled bool) error {
	if s == nil || s.backendHost == nil {
		return nil
	}
	rt := s.backendHost.ToolRuntime()
	if rt == nil {
		return nil
	}
	return rt.Enable(name, enabled)
}

// GetToolCacheStats 返回工具缓存统计。
func (s *ProxyService) GetToolCacheStats() (ToolCacheStatsDTO, error) {
	if s == nil || s.backendHost == nil {
		return ToolCacheStatsDTO{}, nil
	}
	rt := s.backendHost.ToolRuntime()
	if rt == nil {
		return ToolCacheStatsDTO{}, nil
	}
	stats := rt.CacheStats()
	return ToolCacheStatsDTO{
		Hits:    stats.Hits,
		Misses:  stats.Misses,
		Entries: stats.Entries,
		HitRate: stats.HitRate,
	}, nil
}

// ClearToolCache 清空工具结果缓存。
func (s *ProxyService) ClearToolCache() (ClearToolCacheResult, error) {
	if s == nil || s.backendHost == nil {
		return ClearToolCacheResult{}, nil
	}
	rt := s.backendHost.ToolRuntime()
	if rt == nil {
		return ClearToolCacheResult{}, nil
	}
	rt.ClearCache()
	return ClearToolCacheResult{Cleared: true}, nil
}