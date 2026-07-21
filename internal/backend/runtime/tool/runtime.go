// Package tool 实现 Tool Runtime：统一工具注册、发现和执行。
// 统一管理 Filesystem / MCP / Browser / Shell / Git / Search 等工具。
//
// 与现有 ExecBridge / InteractionBridge 的关系：
//
//	ToolRuntime 是上层统一入口，按 Category 分派到 ExecBridge 或 InteractionBridge。
//	ExecBridge 处理 CategoryFilesystem / CategoryShell / CategoryGit
//	InteractionBridge 处理 CategoryBrowser / CategorySearch
//
// 架构：
//
//	ToolRuntime.Execute(toolName, args)
//	    ├── CategoryFilesystem → ExecBridge.OpenExec("Read" / "Write" / ...)
//	    ├── CategoryShell      → ExecBridge.OpenExec("Shell" / ...)
//	    ├── CategoryBrowser    → InteractionBridge.OpenQuery("WebFetch" / ...)
//	    ├── CategorySearch     → InteractionBridge.OpenQuery("WebSearch" / ...)
//	    └── CategoryMCP        → ExecBridge.OpenExec("CallMcpTool" / ...)
package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Runtime 是 Tool Runtime 的主入口。
type Runtime struct {
	mu      sync.RWMutex
	entries map[string]*ToolEntry

	// 外部桥接（由 Forwarder 注入）
	execBridge        ExecBridge
	interactionBridge InteractionBridge

	// 工具结果缓存（ADR-016）
	cacheMu sync.RWMutex
	cache   map[string]*cacheEntry
	// cacheHits/cacheMisses track result-cache effectiveness (ADR-043).
	cacheHits   int64
	cacheMisses int64

	// mcpServerMeta tracks per-server sync timestamps for health/status.
	mcpMetaMu sync.RWMutex
	mcpMeta   map[string]*mcpServerMeta

	// closed 标记 Close 是否已调用（R14 lifecycle unification）。
	closed bool
}

// mcpServerMeta tracks observable state for a single MCP server.
type mcpServerMeta struct {
	lastSyncAt time.Time
	lastError  string
}

type cacheEntry struct {
	result  *ToolResult
	expires time.Time
}

// ToolEntry 工具条目。
type ToolEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
	Category    ToolCategory    `json:"category"`
	Enabled     bool            `json:"enabled"`
	Cacheable   bool            `json:"cacheable"`
	CacheTTL    time.Duration   `json:"cacheTTL"`
	// InternalName 对应的 Cursor 内部工具名（如 "Read"、"WebSearch"）
	InternalName string `json:"internalName,omitempty"`
	// Server 仅对 CategoryMCP 有效，标识所属 MCP server（ADR-026）
	Server string `json:"server,omitempty"`
}

// ToolCategory 工具类别。
type ToolCategory string

const (
	CategoryFilesystem ToolCategory = "filesystem"
	CategoryMCP        ToolCategory = "mcp"
	CategoryBrowser    ToolCategory = "browser"
	CategoryShell      ToolCategory = "shell"
	CategoryGit        ToolCategory = "git"
	CategorySearch     ToolCategory = "search"
)

// ToolResult 工具执行结果。
type ToolResult struct {
	Success bool   `json:"success"`
	Output  string `json:"output"`
	Error   string `json:"error,omitempty"`
	Tokens  int    `json:"tokens"`
	Cached  bool   `json:"cached,omitempty"` // ADR-016: from cache
}

// ExecBridge 执行型工具桥接接口（由 forwarder 的 execbridge 实现）。
type ExecBridge interface {
	OpenExec(toolName string, argsJSON []byte) (execID string, serverPayload []byte, err error)
}

// InteractionBridge 交互型工具桥接接口（由 forwarder 的 interactionbridge 实现）。
type InteractionBridge interface {
	OpenQuery(toolName string, argsJSON []byte) (queryID string, serverPayload []byte, err error)
}

// NewRuntime 创建 Tool Runtime。
func NewRuntime() *Runtime {
	return &Runtime{
		entries: make(map[string]*ToolEntry),
	}
}

// SetBridges 设置外部桥接。
func (rt *Runtime) SetBridges(exec ExecBridge, interaction InteractionBridge) {
	if rt == nil {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.execBridge = exec
	rt.interactionBridge = interaction
}

// Register 注册工具。
func (rt *Runtime) Register(entry *ToolEntry) error {
	if rt == nil {
		return fmt.Errorf("tool runtime is nil")
	}
	if entry == nil || strings.TrimSpace(entry.Name) == "" {
		return fmt.Errorf("tool entry name is required")
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.entries[strings.TrimSpace(entry.Name)] = entry
	return nil
}

// Get 获取工具。
func (rt *Runtime) Get(name string) (*ToolEntry, bool) {
	if rt == nil {
		return nil, false
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	entry, ok := rt.entries[strings.TrimSpace(name)]
	return entry, ok
}

// GetByInternalName 按 Cursor 内部工具名查找。
func (rt *Runtime) GetByInternalName(internalName string) (*ToolEntry, bool) {
	if rt == nil {
		return nil, false
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	for _, entry := range rt.entries {
		if strings.EqualFold(strings.TrimSpace(entry.InternalName), strings.TrimSpace(internalName)) {
			return entry, true
		}
	}
	return nil, false
}

// List 列出所有已注册工具。
func (rt *Runtime) List() []*ToolEntry {
	if rt == nil {
		return nil
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	result := make([]*ToolEntry, 0, len(rt.entries))
	for _, entry := range rt.entries {
		result = append(result, entry)
	}
	return result
}

// Enable 启用/禁用工具。
func (rt *Runtime) Enable(name string, enabled bool) error {
	if rt == nil {
		return fmt.Errorf("tool runtime is nil")
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	entry, ok := rt.entries[strings.TrimSpace(name)]
	if !ok {
		return fmt.Errorf("tool %q not found", name)
	}
	entry.Enabled = enabled
	return nil
}

// IsExecTool 判断工具是否为执行型（走 ExecBridge）。
func (rt *Runtime) IsExecTool(name string) bool {
	if rt == nil {
		return false
	}
	entry, ok := rt.GetByInternalName(name)
	if !ok {
		return false
	}
	switch entry.Category {
	case CategoryFilesystem, CategoryShell, CategoryGit, CategoryMCP:
		return true
	default:
		return false
	}
}

// IsInteractionTool 判断工具是否为交互型（走 InteractionBridge）。
func (rt *Runtime) IsInteractionTool(name string) bool {
	if rt == nil {
		return false
	}
	entry, ok := rt.GetByInternalName(name)
	if !ok {
		return false
	}
	switch entry.Category {
	case CategoryBrowser, CategorySearch:
		return true
	default:
		return false
	}
}

// GetCategory 获取工具的分类。
func (rt *Runtime) GetCategory(internalName string) ToolCategory {
	if rt == nil {
		return ""
	}
	entry, ok := rt.GetByInternalName(internalName)
	if !ok {
		return ""
	}
	return entry.Category
}

// SyncFromCatalog 从现有 tool catalog 同步工具注册信息。
// 参数 tools 是已加载的 JSON Schema 列表。
func (rt *Runtime) SyncFromCatalog(toolSchemas []json.RawMessage, toolNames []string) {
	if rt == nil {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()

	for i, name := range toolNames {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		// 如果已注册，跳过
		if _, exists := rt.entries[strings.ToLower(trimmed)]; exists {
			continue
		}
		entry := &ToolEntry{
			Name:         strings.ToLower(trimmed),
			Description:  trimmed + " tool",
			Category:     classifyCursorTool(trimmed),
			Enabled:      true,
			Cacheable:    isToolCacheable(trimmed),
			CacheTTL:     toolCacheTTL(trimmed),
			InternalName: trimmed,
		}
		if i < len(toolSchemas) {
			entry.Schema = toolSchemas[i]
		}
		rt.entries[strings.ToLower(trimmed)] = entry
	}
}

// MCPToolInfo contains metadata for a dynamically discovered MCP tool (ADR-026).
type MCPToolInfo struct {
	ToolName    string
	Server      string
	Description string
	Schema      json.RawMessage
}

// SyncMCPTools registers dynamically discovered MCP tools into the Tool Runtime (ADR-026).
// Each tool is registered as CategoryMCP with Cacheable=true and a 2-minute TTL.
// Idempotent: already-registered tools are skipped.
func (rt *Runtime) SyncMCPTools(tools []MCPToolInfo) {
	if rt == nil || len(tools) == 0 {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, info := range tools {
		trimmedName := strings.TrimSpace(info.ToolName)
		if trimmedName == "" {
			continue
		}
		key := strings.ToLower(trimmedName)
		if _, exists := rt.entries[key]; exists {
			continue
		}
		desc := strings.TrimSpace(info.Description)
		if desc == "" {
			desc = "MCP tool: " + trimmedName
		}
		entry := &ToolEntry{
			Name:         key,
			Description:  desc,
			Category:     CategoryMCP,
			Enabled:      true,
			Cacheable:    true,
			CacheTTL:     2 * time.Minute,
			InternalName: trimmedName,
			Server:       strings.TrimSpace(info.Server),
			Schema:       info.Schema,
		}
		rt.entries[key] = entry
	}
}

// MCPServerInfo 描述一个 MCP server 的概要信息。
type MCPServerInfo struct {
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Status      string `json:"status"`       // "connected" | "disconnected" | "unknown"
	ToolCount   int    `json:"toolCount"`
	EnabledTool int    `json:"enabledTool"`
	LastSyncAt  string `json:"lastSyncAt"`   // RFC3339 or empty
}

// ListMCPServers 列出所有已知 MCP server 及其统计。
func (rt *Runtime) ListMCPServers() []MCPServerInfo {
	if rt == nil {
		return nil
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	// server → {enabled, total}
	type acc struct{ total, enabled int }
	servers := make(map[string]*acc)
	for _, entry := range rt.entries {
		if entry.Category != CategoryMCP || entry.Server == "" {
			continue
		}
		a, ok := servers[entry.Server]
		if !ok {
			a = &acc{}
			servers[entry.Server] = a
		}
		a.total++
		if entry.Enabled {
			a.enabled++
		}
	}

	result := make([]MCPServerInfo, 0, len(servers))
	for name, a := range servers {
		result = append(result, MCPServerInfo{
			Name:        name,
			Enabled:     a.enabled > 0,
			ToolCount:   a.total,
			EnabledTool: a.enabled,
		})
	}
	return result
}

// ToggleMCPServer 启用/禁用指定 MCP server 的所有工具。
func (rt *Runtime) ToggleMCPServer(server string, enabled bool) error {
	if rt == nil {
		return fmt.Errorf("tool runtime is nil")
	}
	if strings.TrimSpace(server) == "" {
		return fmt.Errorf("server name is required")
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	server = strings.TrimSpace(server)

	found := false
	for _, entry := range rt.entries {
		if entry.Category == CategoryMCP && entry.Server == server {
			entry.Enabled = enabled
			found = true
		}
	}
	if !found {
		return fmt.Errorf("MCP server %q not found", server)
	}
	return nil
}

// classifyCursorTool 将 Cursor 内部工具名映射到 Category。
func classifyCursorTool(name string) ToolCategory {
	switch strings.TrimSpace(name) {
	case "Read", "Write", "Delete", "Glob", "Grep", "Ls", "ReadLints", "PatchEdit":
		return CategoryFilesystem
	case "Shell", "WriteShellStdin", "ForceBackgroundShell":
		return CategoryShell
	case "CallMcpTool", "FetchMcpResource", "ListMcpResources":
		return CategoryMCP
	case "WebSearch":
		return CategorySearch
	case "WebFetch":
		return CategoryBrowser
	case "Task", "AskQuestion", "CreatePlan", "SwitchMode", "TodoWrite":
		return CategoryFilesystem // 这些是 Forwarder 内部处理，不走桥接
	default:
		return CategoryFilesystem
	}
}

func isToolCacheable(name string) bool {
	switch strings.TrimSpace(name) {
	case "Read", "Glob", "Ls", "ReadLints", "WebSearch", "WebFetch", "FetchMcpResource":
		return true
	default:
		return false
	}
}

func toolCacheTTL(name string) time.Duration {
	switch strings.TrimSpace(name) {
	case "Read", "Glob", "Ls":
		return 5 * time.Minute
	case "ReadLints":
		return 1 * time.Minute
	case "WebSearch":
		return 10 * time.Minute
	case "WebFetch":
		return 5 * time.Minute
	case "FetchMcpResource":
		return 2 * time.Minute
	default:
		return 0
	}
}

// Execute 按 Category 分派到 ExecBridge / InteractionBridge（Phase 6 统一入口）。
// 注意：当前 Forwarder 主路径仍直接调用桥接器以保持协议状态机完整；
// Execute 供未来统一分发与单测使用，不替代 handleToolInvocation 的 pending 生命周期。
func (rt *Runtime) Execute(ctx context.Context, toolName string, argsJSON []byte) (*ToolResult, error) {
	_ = ctx
	if rt == nil {
		return nil, fmt.Errorf("tool runtime is nil")
	}
	name := strings.TrimSpace(toolName)
	entry, ok := rt.GetByInternalName(name)
	if !ok {
		entry, ok = rt.Get(name)
	}
	if !ok {
		return &ToolResult{Success: false, Error: fmt.Sprintf("tool %q not registered", name)}, fmt.Errorf("tool %q not registered", name)
	}
	if !entry.Enabled {
		return &ToolResult{Success: false, Error: fmt.Sprintf("tool %q is disabled", entry.Name)}, fmt.Errorf("tool %q is disabled", entry.Name)
	}
	// ADR-016: Check tool result cache
	if entry.Cacheable {
		cacheKey := toolCacheKey(name, argsJSON)
		if cached := rt.lookupCache(cacheKey); cached != nil {
			rt.cacheMu.Lock()
			rt.cacheHits++
			rt.cacheMu.Unlock()
			cached.Cached = true
			return cached, nil
		}
		rt.cacheMu.Lock()
		rt.cacheMisses++
		rt.cacheMu.Unlock()
	}

	internal := strings.TrimSpace(entry.InternalName)
	if internal == "" {
		internal = name
	}

	rt.mu.RLock()
	exec := rt.execBridge
	interaction := rt.interactionBridge
	rt.mu.RUnlock()

	switch entry.Category {
	case CategoryFilesystem, CategoryShell, CategoryGit, CategoryMCP:
		if exec == nil {
			return &ToolResult{Success: false, Error: "exec bridge not configured"}, fmt.Errorf("exec bridge not configured")
		}
		execID, payload, err := exec.OpenExec(internal, argsJSON)
		if err != nil {
			return &ToolResult{Success: false, Error: err.Error()}, err
		}
		result := &ToolResult{Success: true, Output: string(payload) + "\nexecID=" + execID}
		if entry.Cacheable {
			rt.storeCache(toolCacheKey(name, argsJSON), result, entry.CacheTTL)
		}
		return result, nil
	case CategoryBrowser, CategorySearch:
		if interaction == nil {
			return &ToolResult{Success: false, Error: "interaction bridge not configured"}, fmt.Errorf("interaction bridge not configured")
		}
		queryID, payload, err := interaction.OpenQuery(internal, argsJSON)
		if err != nil {
			return &ToolResult{Success: false, Error: err.Error()}, err
		}
		result := &ToolResult{Success: true, Output: string(payload) + "\nqueryID=" + queryID}
		if entry.Cacheable {
			rt.storeCache(toolCacheKey(name, argsJSON), result, entry.CacheTTL)
		}
		return result, nil
	default:
		return &ToolResult{Success: false, Error: fmt.Sprintf("unsupported category %q", entry.Category)}, fmt.Errorf("unsupported category %q", entry.Category)
	}
}

// RegisterBuiltinTools 注册内置工具集。
func (rt *Runtime) RegisterBuiltinTools() {
	builtins := []*ToolEntry{
		{Name: "read_file", InternalName: "Read", Description: "Read contents of a file", Category: CategoryFilesystem, Enabled: true, Cacheable: true, CacheTTL: 5 * time.Minute, Schema: json.RawMessage(`{"name":"read_file","description":"Read contents of a file"}`)},
		{Name: "write_file", InternalName: "Write", Description: "Write contents to a file", Category: CategoryFilesystem, Enabled: true, Cacheable: false, Schema: json.RawMessage(`{"name":"write_file","description":"Write contents to a file"}`)},
		{Name: "search_files", InternalName: "Glob", Description: "Search for files by pattern", Category: CategoryFilesystem, Enabled: true, Cacheable: true, CacheTTL: 5 * time.Minute, Schema: json.RawMessage(`{"name":"search_files","description":"Search for files by pattern"}`)},
		{Name: "search_content", InternalName: "Grep", Description: "Search file contents", Category: CategoryFilesystem, Enabled: true, Cacheable: true, CacheTTL: 1 * time.Minute, Schema: json.RawMessage(`{"name":"search_content","description":"Search file contents"}`)},
		{Name: "list_directory", InternalName: "Ls", Description: "List directory contents", Category: CategoryFilesystem, Enabled: true, Cacheable: true, CacheTTL: 5 * time.Minute, Schema: json.RawMessage(`{"name":"list_directory","description":"List directory contents"}`)},
		{Name: "read_lints", InternalName: "ReadLints", Description: "Read linter errors", Category: CategoryFilesystem, Enabled: true, Cacheable: true, CacheTTL: 1 * time.Minute, Schema: json.RawMessage(`{"name":"read_lints","description":"Read linter errors"}`)},
		{Name: "execute_shell", InternalName: "Shell", Description: "Execute a shell command", Category: CategoryShell, Enabled: true, Cacheable: false, Schema: json.RawMessage(`{"name":"execute_shell","description":"Execute a shell command"}`)},
		{Name: "web_search", InternalName: "WebSearch", Description: "Search the web", Category: CategorySearch, Enabled: true, Cacheable: true, CacheTTL: 10 * time.Minute, Schema: json.RawMessage(`{"name":"web_search","description":"Search the web"}`)},
		{Name: "web_fetch", InternalName: "WebFetch", Description: "Fetch content from a URL", Category: CategoryBrowser, Enabled: true, Cacheable: true, CacheTTL: 5 * time.Minute, Schema: json.RawMessage(`{"name":"web_fetch","description":"Fetch content from a URL"}`)},
	}
	for _, entry := range builtins {
		_ = rt.Register(entry)
	}
}

// toolCacheKey generates a cache key from tool name and args (ADR-016).
func toolCacheKey(toolName string, argsJSON []byte) string {
	h := sha256.Sum256([]byte(toolName + "\n" + string(argsJSON)))
	return hex.EncodeToString(h[:])
}

// lookupCache checks the tool result cache.
func (rt *Runtime) lookupCache(key string) *ToolResult {
	if rt == nil || rt.cache == nil {
		return nil
	}
	rt.cacheMu.RLock()
	defer rt.cacheMu.RUnlock()
	entry, ok := rt.cache[key]
	if !ok || time.Now().After(entry.expires) {
		return nil
	}
	return entry.result
}

// storeCache writes a tool result to the cache with TTL.
func (rt *Runtime) storeCache(key string, result *ToolResult, ttl time.Duration) {
	if rt == nil || ttl <= 0 {
		return
	}
	rt.cacheMu.Lock()
	defer rt.cacheMu.Unlock()
	if rt.cache == nil {
		rt.cache = make(map[string]*cacheEntry)
	}
	rt.cache[key] = &cacheEntry{result: result, expires: time.Now().Add(ttl)}
}

// ToolCacheStats summarizes tool-result cache effectiveness.
type ToolCacheStats struct {
	Hits    int64   `json:"hits"`
	Misses  int64   `json:"misses"`
	Entries int     `json:"entries"`
	HitRate float64 `json:"hitRate"`
}

// CacheStats returns a snapshot of tool-result cache counters.
func (rt *Runtime) CacheStats() ToolCacheStats {
	if rt == nil {
		return ToolCacheStats{}
	}
	rt.cacheMu.RLock()
	defer rt.cacheMu.RUnlock()
	stats := ToolCacheStats{Hits: rt.cacheHits, Misses: rt.cacheMisses, Entries: len(rt.cache)}
	total := stats.Hits + stats.Misses
	if total > 0 {
		stats.HitRate = float64(stats.Hits) / float64(total)
	}
	return stats
}

// ClearCache 清空工具结果缓存（保留命中/未命中计数，仅清空条目）。
func (rt *Runtime) ClearCache() {
	if rt == nil {
		return
	}
	rt.cacheMu.Lock()
	defer rt.cacheMu.Unlock()
	rt.cache = make(map[string]*cacheEntry)
}

// Close marks the runtime closed and drops in-memory tool entries/cache.
// Subsequent Close calls are no-ops. R14: lifecycle unification.
// Tool Runtime has no persistent handles, so Close is mostly a lifecycle
// marker that also releases transient state for test determinism.
func (rt *Runtime) Close(ctx context.Context) error {
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return nil
	}
	rt.closed = true
	rt.entries = make(map[string]*ToolEntry)
	rt.mu.Unlock()
	rt.ClearCache()
	return nil
}

// IsClosed reports whether Close has been invoked on this runtime.
func (rt *Runtime) IsClosed() bool {
	if rt == nil {
		return false
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.closed
}
